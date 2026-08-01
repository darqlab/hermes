package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/darqlab/hermes/internal/config"
	"github.com/darqlab/hermes/internal/mail"
	"github.com/darqlab/hermes/internal/queue"
)

var version = "dev"

var (
	cfgFile  string
	logLevel string

	rootCmd = &cobra.Command{
		Use:   "hermes",
		Short: "Mail sender and reader",
		Long: `Hermes — a CLI tool for sending and reading email via SMTP and IMAP.

Hermes is a single static binary with no runtime dependencies and no network
listener. It is designed to be invoked headlessly from shell scripts, cron
jobs, and LLM agents — not driven interactively.

Status: sending is implemented now ("hermes send", "hermes queue ..."; Phase
1). Reading/watching mail over IMAP ("hermes read", "hermes watch") is not
yet implemented (planned Phase 2) — those commands do not currently exist.

Configuration resolution order (later steps override earlier ones):
  1. Built-in defaults (e.g. smtp.port=587, queue.retry_max=10).
  2. Config file: the path given by --config, else "./hermes.yaml", else
     "~/hermes.yaml" if neither of the above exists. If no config file is
     found at all, Hermes falls back to defaults + env vars only.
  3. Environment variable overrides, applied on top of whatever the file (or
     defaults) set:
       HERMES_FROM, HERMES_SMTP_HOST, HERMES_SMTP_PORT, HERMES_SMTP_USER,
       HERMES_SMTP_PASS, HERMES_SMTP_USE_TLS (true/1), HERMES_SMTP_STARTTLS
       (false/0 to disable), HERMES_DKIM_SELECTOR, HERMES_DKIM_DOMAIN,
       HERMES_DKIM_KEY_FILE, HERMES_QUEUE_QUEUE_FILE, HERMES_QUEUE_RETRY_MAX,
       HERMES_QUEUE_BACKOFF_BASE, HERMES_QUEUE_BACKOFF_CAP.

Top-level "from" (config file) / HERMES_FROM (env) sets the default envelope-
and-header From address used when "hermes send --from" is omitted. If unset,
it falls back to smtp.user.

smtp.host, smtp.user, and smtp.pass are always required (from file or env)
except for "hermes send --dry-run", which composes a message and prints it
without touching config or the network at all.

Exit codes: 0 on success. Non-zero (1) on any failure — config/validation
errors, compose errors, delivery failures (including when the message was
successfully queued for retry: queuing does not count as success), and queue
command errors. There is no separate code space beyond 0/1 — check stderr
output or, for "send --json", the JSON "status" field to distinguish failure
reasons.`,
		Example: `  # Send a plain-text email using ./hermes.yaml or env-var config
  hermes send --to you@example.com --subject "Hi" --body "Hello there"

  # Get machine-readable output for scripting (see "hermes send --help")
  hermes send --to you@example.com --subject "Hi" --body "Hello" --json

  # Inspect what would be sent, without any config or network access
  echo "hello" | hermes send --to you@example.com --subject "Hi" --dry-run`,
	}
)

func main() {
	rootCmd.Version = version
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./hermes.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	rootCmd.AddCommand(sendCmd())
	rootCmd.AddCommand(queueCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func sendCmd() *cobra.Command {
	var (
		to        []string
		from      string
		cc        []string
		bcc       []string
		replyTo   string
		subject   string
		body      string
		bodyHTML  string
		attach    []string
		useJSON   bool
		noQueue   bool
		dryRun    bool
		noSign    bool
	)

	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send an email",
		Long: `Compose and send an email via SMTP.

Body: comes from --body (plain text), --body-html, or piped stdin (used only
when neither --body nor --body-html is set). At least one of the three is
required; --body and --body-html may both be set to send a multipart
message.

Recipients: --to is required (repeatable for multiple recipients). --cc and
--bcc are optional and repeatable. --from is optional; if omitted it
defaults to the resolved config's top-level "from" (or HERMES_FROM env var),
falling back to "smtp.user" if neither is set (see "hermes --help" for
config resolution order). --dry-run does not load config, so with --from
omitted it prints a "hermes@localhost" placeholder From address instead.

On delivery failure: the composed message is automatically enqueued to the
local retry queue (see "hermes queue --help") unless --no-queue is set, in
which case the command fails immediately instead of queuing. Either way, a
failed/queued send exits non-zero — queuing for retry is not treated as
success.

--dry-run composes the MIME message and prints it to stdout without loading
config, signing, or sending — no SMTP config or network access is needed for
a dry run.

--no-sign skips DKIM signing even if dkim.key_file is configured.

--json switches stdout/stderr to a single JSON object per invocation instead
of log lines, for scripting. Exact shapes (fields as actually emitted by
main.go):
  Success (stdout, exit 0):
    {"status":"ok","server":"<SMTP server response>"}
  Delivery failed, queued for retry (stdout, exit 1):
    {"status":"queued","job_id":"<uuid>","error":"<delivery error>"}
  Delivery failed with --no-queue (stderr, exit 1):
    {"status":"failed","error":"<delivery error>"}`,
		Example: `  # Plain send
  hermes send --to you@example.com --subject "Hi" --body "Hello there"

  # JSON output for scripting/LLM agents
  hermes send --to you@example.com --subject "Hi" --body "Hello" --json

  # Dry run: print composed MIME, no config or network needed
  hermes send --to you@example.com --subject "Hi" --body "Hello" --dry-run

  # Body piped via stdin
  echo "Hello from a script" | hermes send --to you@example.com --subject "Hi"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(to) == 0 {
				return fmt.Errorf("--to is required")
			}
			if body == "" && bodyHTML == "" {
				stat, _ := os.Stdin.Stat()
				if (stat.Mode() & os.ModeCharDevice) == 0 {
					stdin, err := io.ReadAll(os.Stdin)
					if err != nil {
						return fmt.Errorf("read stdin: %w", err)
					}
					body = string(stdin)
				}
			}
			if body == "" && bodyHTML == "" {
				return fmt.Errorf("body is required (--body, --body-html, or stdin)")
			}

			var cfg *config.Config
			if dryRun {
				if from == "" {
					from = "hermes@localhost"
				}
			} else {
				var err error
				cfg, err = loadConfig()
				if err != nil {
					return fmt.Errorf("config: %w", err)
				}
				if from == "" {
					if cfg.From != "" {
						from = cfg.From
					} else {
						from = cfg.SMTP.User
					}
				}
			}

			log.Printf("composing message to %v", to)
			raw, err := mail.Compose(mail.MessageOpts{
				From:        from,
				To:          to,
				Cc:          cc,
				Bcc:         bcc,
				ReplyTo:     replyTo,
				Subject:     subject,
				Body:        body,
				BodyHTML:    bodyHTML,
				Attachments: attach,
			})
			if err != nil {
				return fmt.Errorf("compose: %w", err)
			}

			if dryRun {
				fmt.Print(string(raw))
				return nil
			}

			if cfg.DKIM.KeyFile != "" && !noSign {
				log.Printf("dkim signing (domain=%s, selector=%s)", cfg.DKIM.Domain, cfg.DKIM.Selector)
				raw, err = mail.Sign(raw, cfg.DKIM.Domain, cfg.DKIM.Selector, cfg.DKIM.KeyFile)
				if err != nil {
					return fmt.Errorf("dkim: %w", err)
				}
			}

			smtpCfg := mail.SMTPDeliverConfig{
				Host:     cfg.SMTP.Host,
				Port:     cfg.SMTP.Port,
				User:     cfg.SMTP.User,
				Pass:     cfg.SMTP.Pass,
				UseTLS:   cfg.SMTP.UseTLS,
				StartTLS: cfg.SMTP.StartTLS,
			}

			log.Printf("delivering via %s:%d", cfg.SMTP.Host, cfg.SMTP.Port)
			resp, err := mail.Deliver(from, append(append(to, cc...), bcc...), raw, smtpCfg)
			if err != nil {
				log.Printf("delivery failed: %v", err)

				if noQueue {
					if useJSON {
						fmt.Fprintln(os.Stderr, jsonString(map[string]any{
							"status": "failed",
							"error":  err.Error(),
						}))
					}
					return fmt.Errorf("delivery failed (--no-queue): %w", err)
				}

				store, qErr := openStore(cfg)
				if qErr != nil {
					return fmt.Errorf("delivery failed and queue unavailable: %w (queue error: %v)", err, qErr)
				}
				defer store.Close()

				job := &queue.Job{
					ID:           uuid.New().String(),
					EnvelopeFrom: from,
					EnvelopeTo:   append(append(to, cc...), bcc...),
					RawMIME:      raw,
					Status:       queue.StatusPending,
					CreatedAt:    time.Now(),
				}
				if err := store.Enqueue(job); err != nil {
					return fmt.Errorf("delivery failed and enqueue failed: %w", err)
				}

				if useJSON {
					fmt.Println(jsonString(map[string]any{
						"status": "queued",
						"job_id": job.ID,
						"error":  err.Error(),
					}))
				} else {
					log.Printf("queued job %s for retry", job.ID[:8])
				}
				os.Exit(1)
			}

			if useJSON {
				fmt.Println(jsonString(map[string]any{
					"status": "ok",
					"server": resp,
				}))
			} else {
				log.Printf("sent successfully — %s", resp)
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&to, "to", nil, "recipient (repeatable)")
	cmd.Flags().StringVar(&from, "from", "", "envelope-from (default: config's \"from\"/HERMES_FROM, else smtp.user)")
	cmd.Flags().StringSliceVar(&cc, "cc", nil, "CC recipient (repeatable)")
	cmd.Flags().StringSliceVar(&bcc, "bcc", nil, "BCC recipient (repeatable)")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Reply-To address")
	cmd.Flags().StringVar(&subject, "subject", "", "subject line")
	cmd.Flags().StringVar(&body, "body", "", "plain-text body (or pipe via stdin)")
	cmd.Flags().StringVar(&bodyHTML, "body-html", "", "HTML body")
	cmd.Flags().StringSliceVar(&attach, "attach", nil, "file to attach (repeatable)")
	cmd.Flags().BoolVar(&useJSON, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&noQueue, "no-queue", false, "fail immediately on delivery error (don't enqueue)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compose and print MIME to stdout, don't send")
	cmd.Flags().BoolVar(&noSign, "no-sign", false, "skip DKIM signing")

	return cmd
}

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Manage the send queue",
		Long: `Manage the local persistent send queue.

Messages that fail delivery via "hermes send" (without --no-queue) are
stored here as JSON on disk (path: queue.queue_file, default
"hermes_queue.json") and retried automatically with exponential backoff
(queue.backoff_base up to queue.backoff_cap, up to queue.retry_max attempts
before a job is marked "dead"). Job states: pending, done, failed, dead.

These subcommands only inspect/manipulate the queue file directly — none of
them starts a background worker.`,
		Example: `  hermes queue list
  hermes queue list --json=true
  hermes queue retry <job-id>
  hermes queue retry-all
  hermes queue purge`,
	}

	cmd.AddCommand(queueListCmd())
	cmd.AddCommand(queueRetryCmd())
	cmd.AddCommand(queueRetryAllCmd())
	cmd.AddCommand(queuePurgeCmd())

	return cmd
}

func queueListCmd() *cobra.Command {
	var (
		useJSON string
		status  string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List queued jobs",
		Long: `List jobs currently in the send queue.

Default output is a human-readable table (ID truncated to 8 chars, STATUS,
RETRIES, NEXT RETRY, ERROR truncated to 50 chars). Pass --json=true (or
--json=1) for a full JSON array of Job objects instead, one field per Job
struct member:
  [
    {
      "id": "<full uuid>",
      "envelope_from": "sender@example.com",
      "envelope_to": ["recipient@example.com"],
      "raw_mime": "<base64-encoded raw MIME bytes>",
      "retry_count": 0,
      "next_retry_at": "<RFC3339 timestamp, zero value if not scheduled>",
      "status": "pending|done|failed|dead",
      "created_at": "<RFC3339 timestamp>",
      "last_error": "<omitted if empty>"
    }
  ]
--json is a string flag, not a boolean switch — it must be given a value
(--json=true / --json=1), not used bare as "--json".
Use --status to filter by exactly one status: pending, done, failed, dead.
The JSON output (with full, untruncated job IDs) is the reliable input for
"hermes queue retry <id>", since the table view only shows a truncated ID.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close()

			jobs, err := store.List(queue.JobStatus(status))
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}

			if useJSON == "true" || useJSON == "1" {
				if len(jobs) == 0 {
					jobs = []*queue.Job{}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(jobs)
			}

			if len(jobs) == 0 {
				fmt.Println("No jobs in queue.")
				return nil
			}
			fmt.Printf("%-10s %-10s %-7s %-20s %s\n", "ID", "STATUS", "RETRIES", "NEXT RETRY", "ERROR")
			for _, j := range jobs {
				nextRetry := "-"
				if !j.NextRetryAt.IsZero() {
					nextRetry = j.NextRetryAt.Format("2006-01-02 15:04")
				}
				errStr := j.LastError
				if len(errStr) > 50 {
					errStr = errStr[:50] + "..."
				}
				if errStr == "" {
					errStr = "-"
				}
				fmt.Printf("%-10s %-10s %-7d %-20s %s\n", j.ID[:8], j.Status, j.RetryCount, nextRetry, errStr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&useJSON, "json", "", "output as JSON")
	cmd.Flags().StringVar(&status, "status", "", "filter by status: pending, done, failed, dead")
	return cmd
}

func queueRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry <id>",
		Short: "Force retry of a failed job",
		Long: `Reset a job to "pending" (retry_count=0, next_retry_at cleared,
last_error cleared) so it will be picked up for immediate retry.

<id> must be the FULL job ID (the complete UUID) — matching is exact, not a
prefix match. The default "queue list" table only shows the first 8
characters of each ID, which is not enough; run "hermes queue list
--json=true" and copy the full "id" field.`,
		Example: `  hermes queue retry 3fa1c2d4-5678-4abc-9def-0123456789ab`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.RetryNow(args[0]); err != nil {
				return fmt.Errorf("retry: %w", err)
			}
			log.Printf("job %s set for immediate retry", args[0][:8])
			return nil
		},
	}
}

func queueRetryAllCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry-all",
		Short: "Retry all failed and dead jobs",
		Long: `Reset every job currently in "failed" or "dead" status back to
"pending" (retry_count=0, next_retry_at cleared, last_error cleared). Jobs
already "pending" or "done" are left untouched.`,
		Example: `  hermes queue retry-all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.RetryAll(); err != nil {
				return fmt.Errorf("retry-all: %w", err)
			}
			log.Printf("all failed jobs set for immediate retry")
			return nil
		},
	}
}

func queuePurgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "purge",
		Short: "Remove completed and dead jobs",
		Long: `Permanently delete all jobs in "done" or "dead" status from the
queue file. Jobs in "pending" or "failed" status are kept.`,
		Example: `  hermes queue purge`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close()

			if err := store.Purge(); err != nil {
				return fmt.Errorf("purge: %w", err)
			}
			log.Printf("purged completed and dead jobs")
			return nil
		},
	}
}

func openStore(cfg *config.Config) (*queue.Store, error) {
	backoffBase, err := time.ParseDuration(cfg.Queue.BackoffBase)
	if err != nil {
		return nil, fmt.Errorf("parse backoff_base: %w", err)
	}
	backoffCap, err := time.ParseDuration(cfg.Queue.BackoffCap)
	if err != nil {
		return nil, fmt.Errorf("parse backoff_cap: %w", err)
	}
	return queue.NewStore(cfg.Queue.QueueFile, cfg.Queue.RetryMax, backoffBase, backoffCap)
}

func jsonString(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return buf.String()
}
