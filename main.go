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
		Long:  "Hermes — a CLI tool for sending and reading email via SMTP and IMAP.",
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
		Long:  "Compose and send an email via SMTP. If delivery fails, the message is enqueued for retry.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(to) == 0 {
				return fmt.Errorf("--to is required")
			}
			if from == "" {
				from = "hermes@localhost"
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

			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			if from == "hermes@localhost" {
				from = cfg.SMTP.User
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
			if err := mail.Deliver(from, append(append(to, cc...), bcc...), raw, smtpCfg); err != nil {
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
				}))
			} else {
				log.Printf("sent successfully")
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&to, "to", nil, "recipient (repeatable)")
	cmd.Flags().StringVar(&from, "from", "", "envelope-from (default: smtp.user)")
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
		Args:  cobra.ExactArgs(1),
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
