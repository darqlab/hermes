package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/darqlab/hermes/internal/config"
	"github.com/darqlab/hermes/internal/mail"
	"github.com/darqlab/hermes/internal/queue"
	"github.com/darqlab/hermes/internal/read"

	"github.com/emersion/go-imap/v2"
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

Commands:
  send    Compose and send email via SMTP.
  read    Connect IMAP, fetch and parse messages.
  watch   Watch a mailbox for new messages (IMAP IDLE).
  queue   Manage the persistent send queue.

Configuration resolution order (later steps override earlier ones):
  1. Built-in defaults (e.g. smtp.port=587, imap.port=993, queue.retry_max=10).
  2. Config file: the path given by --config, else "./hermes.yaml", else
     "~/hermes.yaml" if neither of the above exists. If no config file is
     found at all, Hermes falls back to defaults + env vars only.
  3. Environment variable overrides, applied on top of whatever the file (or
     defaults) set:
       HERMES_FROM, HERMES_SMTP_HOST, HERMES_SMTP_PORT, HERMES_SMTP_USER,
       HERMES_SMTP_PASS, HERMES_SMTP_USE_TLS (true/1), HERMES_SMTP_STARTTLS
       (false/0 to disable), HERMES_DKIM_SELECTOR, HERMES_DKIM_DOMAIN,
       HERMES_DKIM_KEY_FILE, HERMES_QUEUE_QUEUE_FILE, HERMES_QUEUE_RETRY_MAX,
       HERMES_QUEUE_BACKOFF_BASE, HERMES_QUEUE_BACKOFF_CAP,
       HERMES_IMAP_HOST, HERMES_IMAP_PORT, HERMES_IMAP_USER, HERMES_IMAP_PASS,
       HERMES_IMAP_USE_TLS (true/1), HERMES_IMAP_STARTTLS (false/0 to
       disable).

Top-level "from" (config file) / HERMES_FROM (env) sets the default envelope-
and-header From address used when "hermes send --from" is omitted. If unset,
it falls back to smtp.user.

smtp.host, smtp.user, and smtp.pass are always required (from file or env)
except for "hermes send --dry-run", which composes a message and prints it
without touching config or the network at all. imap.host, imap.user, and
imap.pass are required only for "hermes read" and "hermes watch" — send
works without any imap.* config present.

Exit codes: 0 on success. Non-zero (1) on any failure — config/validation
errors, compose errors, delivery failures (including when the message was
successfully queued for retry: queuing does not count as success), and
IMAP connect/auth/mailbox/parse errors. There is no separate code space
beyond 0/1 — check stderr output or, for "send --json" and "read --json",
the JSON "status" field or JSON array to distinguish failure reasons.`,
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
	rootCmd.AddCommand(readCmd())
	rootCmd.AddCommand(watchCmd())

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
		quiet     bool
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

--quiet suppresses success output to reduce token cost for LLM agents. With
--json, the "server" field is omitted (only {"status":"ok"}). Without --json,
nothing is printed on success. Failure and queued outputs are unaffected.

--json switches stdout/stderr to a single JSON object per invocation instead
of log lines, for scripting. Exact shapes (fields as actually emitted by
main.go):
  Success (stdout, exit 0):
    {"status":"ok","server":"<SMTP server response>"}
  Success with --quiet (stdout, exit 0):
    {"status":"ok"}
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
				if quiet {
					fmt.Println(jsonString(map[string]any{
						"status": "ok",
					}))
				} else {
					fmt.Println(jsonString(map[string]any{
						"status": "ok",
						"server": resp,
					}))
				}
			} else if !quiet {
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
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress success output (with --json: omit server response; without --json: print nothing on success)")

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

func validateIMAPConfig(cfg *config.Config) error {
	if cfg.IMAP.Host == "" {
		return fmt.Errorf("imap.host is required for read/watch (set in config or HERMES_IMAP_HOST)")
	}
	if cfg.IMAP.User == "" {
		return fmt.Errorf("imap.user is required for read/watch (set in config or HERMES_IMAP_USER)")
	}
	if cfg.IMAP.Pass == "" {
		return fmt.Errorf("imap.pass is required for read/watch (set in config or HERMES_IMAP_PASS)")
	}
	return nil
}

func readCmd() *cobra.Command {
	var (
		mailbox     string
		limit       uint
		from        string
		subject     string
		body        string
		unseenOnly  bool
		useJSON     bool
		headersOnly bool
		msgUID      uint
		since       string
		before      string
	)

	cmd := &cobra.Command{
		Use:   "read",
		Short: "Read messages from IMAP mailbox",
		Long: `Connect to the configured IMAP server, select a mailbox, search for
messages, and print them. By default fetches the most recent 10 messages
from INBOX as tab-delimited lines (date\tfrom\tsubject).

All search is server-side (IMAP SEARCH): no client-side filtering.

Search filters (all optional, combined with AND when multiple are set):
  --from STRING       From header substring
  --subject STRING    Subject header substring
  --body STRING       Message body substring
  --unseen-only       Only messages without \Seen flag
  --since YYYY-MM-DD  Messages on or after this date
  --before YYYY-MM-DD Messages before this date

Output mode (mutually exclusive):
  --uid N             Fetch exactly one message by IMAP UID (overrides all
                      search filters and --limit)
  --limit N           Max messages to return (default 10; takes the most
                      recent N when combined with search)

Output format:
  --json              JSON array of message objects (full body fields)
  --headers-only      Strip body_text/body_html from JSON (text mode is
                      already body-free; this flag has no effect without
                      --json)
  default (no --json) Tab-delimited: "YYYY-MM-DD HH:MM\tfrom\tsubject"
                      per line. No body content. Empty result prints
                      "no messages".

Exit codes: 0 on success (zero matches is success), 1 on any error
(connect, auth, mailbox not found, search failure, fetch failure).
Per-message parse failures are skipped+logged, not fatal.

JSON output shape (exact, as emitted):
  Success (stdout, exit 0):
    [
      {
        "uid": 42,
        "seq_num": 7,
        "from": "Name <addr>",
        "to": ["Name <addr>", ...],
        "cc": ["Name <addr>", ...],     // omitted if empty
        "subject": "decoded subject",
        "date": "2026-01-15T10:30:00Z",  // RFC3339 UTC
        "flags": ["\\Seen", "\\Recent", ...],
        "body_text": "plain text",       // omitted when --headers-only
        "body_html": "<p>html</p>",      // omitted when --headers-only
        "attachments": [                 // omitted if none
          {"filename": "report.pdf", "content_type": "application/pdf", "size_bytes": 1234}
        ]
      }
    ]
    Zero matches: [] (empty array, exit 0)
  Failure (stderr, exit 1): unstructured Go error message

Config: uses imap.host, imap.port, imap.user, imap.pass from config
file or HERMES_IMAP_* env vars. Defaults: port=993, use_tls=true.
Send-only users do not need any imap.* config.`,
		Example: `  hermes read
  hermes read --mailbox INBOX --limit 5 --unseen-only
  hermes read --from "alert@example.com" --subject "disk full"
  hermes read --body "urgent" --limit 20
  hermes read --since 2026-08-01 --before 2026-08-05
  hermes read --json --limit 20 --headers-only
  hermes read --uid 42 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := validateIMAPConfig(cfg); err != nil {
				return err
			}

			client, err := read.DialTLS(cfg.IMAP.Host, cfg.IMAP.Port)
			if err != nil {
				return fmt.Errorf("imap connect: %w", err)
			}
			defer client.Close()

			if err := client.Login(cfg.IMAP.User, cfg.IMAP.Pass); err != nil {
				return fmt.Errorf("imap auth: %w", err)
			}

			if _, err := client.Select(mailbox); err != nil {
				return fmt.Errorf("imap select: %w", err)
			}

			var uids []imap.UID

			if msgUID > 0 {
				uids = []imap.UID{imap.UID(msgUID)}
			} else {
				criteria := &imap.SearchCriteria{}
				if unseenOnly {
					criteria.NotFlag = []imap.Flag{imap.FlagSeen}
				}
				if from != "" {
					criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "FROM", Value: from})
				}
				if subject != "" {
					criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "SUBJECT", Value: subject})
				}
				if body != "" {
					criteria.Body = append(criteria.Body, body)
				}
				if since != "" {
					t, err := time.Parse("2006-01-02", since)
					if err != nil {
						return fmt.Errorf("--since: %w", err)
					}
					criteria.Since = t
				}
				if before != "" {
					t, err := time.Parse("2006-01-02", before)
					if err != nil {
						return fmt.Errorf("--before: %w", err)
					}
					criteria.Before = t
				}

				searchData, err := client.Search(criteria)
				if err != nil {
					return fmt.Errorf("imap search: %w", err)
				}

				uidSet, ok := searchData.All.(imap.UIDSet)
				if !ok {
					if useJSON {
						fmt.Println("[]")
					} else {
						fmt.Println("no messages")
					}
					return nil
				}
				found, ok := uidSet.Nums()
				if !ok || len(found) == 0 {
					if useJSON {
						fmt.Println("[]")
					} else {
						fmt.Println("no messages")
					}
					return nil
				}
				uids = found
				if limit > 0 && uint(len(uids)) > limit {
					uids = uids[len(uids)-int(limit):]
				}
			}

			msgs, err := client.FetchMessages(uids)
			if err != nil {
				return fmt.Errorf("imap fetch: %w", err)
			}

			var parsed []read.Message
			for _, raw := range msgs {
				msg, err := read.ParseMessage(raw)
				if err != nil {
					log.Printf("skipping message uid %v: %v", raw.UID, err)
					continue
				}
				parsed = append(parsed, *msg)
			}

			if useJSON {
				if parsed == nil {
					parsed = []read.Message{}
				}
				if headersOnly {
					for i := range parsed {
						parsed[i].BodyText = ""
						parsed[i].BodyHTML = ""
					}
				}
				fmt.Println(jsonString(parsed))
			} else {
				if len(parsed) == 0 {
					fmt.Println("no messages")
				} else {
					for _, msg := range parsed {
						fmt.Printf("%s\t%s\t%s\n",
							msg.Date.Format("2006-01-02 15:04"),
							msg.From,
							msg.Subject,
						)
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&mailbox, "mailbox", "INBOX", "mailbox to read")
	cmd.Flags().UintVar(&limit, "limit", 10, "max messages to return")
	cmd.Flags().StringVar(&from, "from", "", "filter by From address (substring)")
	cmd.Flags().StringVar(&subject, "subject", "", "filter by Subject (substring)")
	cmd.Flags().StringVar(&body, "body", "", "filter by message body (substring)")
	cmd.Flags().BoolVar(&unseenOnly, "unseen-only", false, "only unseen messages")
	cmd.Flags().BoolVar(&useJSON, "json", false, "output as JSON array")
	cmd.Flags().BoolVar(&headersOnly, "headers-only", false, "omit body_text/body_html from JSON output")
	cmd.Flags().UintVar(&msgUID, "uid", 0, "fetch a single message by IMAP UID")
	cmd.Flags().StringVar(&since, "since", "", "messages on or after date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&before, "before", "", "messages before date (YYYY-MM-DD)")

	return cmd
}

func watchCmd() *cobra.Command {
	var (
		mailbox      string
		pollInterval time.Duration
		useJSON      bool
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch mailbox for new messages (IDLE)",
		Long: `Connect to the configured IMAP server, select a mailbox, and watch
for new messages using IMAP IDLE (RFC 2177). Falls back to polling at
--poll-interval (default 30s) when the server lacks IDLE support.

Runs until killed (SIGINT/SIGTERM — sends clean LOGOUT on shutdown).
Reconnects with exponential backoff on connection drop (base 1s, cap 30s).

Output format:
  --json              One JSON object per line (JSON Lines): each new
                      message is printed as a single line of JSON, so
                      an agent can stream-read line-by-line without
                      buffering. Same shape as "hermes read --json"
                      (all fields: uid, from, to, subject, date, flags,
                      body_text, body_html, attachments).
  default (no --json) Tab-delimited: "YYYY-MM-DD HH:MM\tfrom\tsubject"
                      per line, one line per new message.

Exit codes: 0 on clean shutdown (SIGINT/SIGTERM), 1 on connect/auth/
mailbox error at startup. Per-message parse failures are skipped+logged.

Config: same as "hermes read" (imap.host/port/user/pass).`,
		Example: `  hermes watch
  hermes watch --mailbox INBOX --json
  hermes watch --poll-interval 10s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := validateIMAPConfig(cfg); err != nil {
				return err
			}

			return read.Watch(context.Background(), read.WatchConfig{
				Host:         cfg.IMAP.Host,
				Port:         cfg.IMAP.Port,
				User:         cfg.IMAP.User,
				Pass:         cfg.IMAP.Pass,
				Mailbox:      mailbox,
				PollInterval: pollInterval,
			}, func(msg read.Message) error {
				if useJSON {
					fmt.Println(strings.TrimSpace(jsonString(msg)))
				} else {
					fmt.Printf("%s\t%s\t%s\n",
						msg.Date.Format("2006-01-02 15:04"),
						msg.From,
						msg.Subject,
					)
				}
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&mailbox, "mailbox", "INBOX", "mailbox to watch")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 30*time.Second, "poll interval when IDLE unavailable")
	cmd.Flags().BoolVar(&useJSON, "json", false, "output as JSON Lines")

	return cmd
}
