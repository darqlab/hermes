# hermes — CLI mail sender and reader

Send and read email from the terminal. Designed for **shell scripts** and **LLM agents** (Claude, OpenCode, cron jobs). Single static Go binary, zero runtime dependencies.

```
hermes send --to ops@x.com --subject "Disk 90%" --body "sda1 at 92%"
cat report.csv | hermes send --to team@x.com --subject "Weekly" --attach report.csv
hermes queue list
hermes read --json --limit 5 --mailbox INBOX       # coming in v0.2
hermes watch --json --mailbox Orders                # coming in v0.2
```

**Current status:** v0.1 — send engine complete. IMAP reader coming in v0.2.

## Install

```bash
# Linux / macOS
curl -sL https://raw.githubusercontent.com/darqlab/hermes/main/install.sh | bash

# Manual — download binary from GitHub Releases
# https://github.com/darqlab/hermes/releases/latest
```

### Per-platform binaries

| Platform | Binary |
|----------|--------|
| Linux x86_64 | `hermes-linux-amd64` |
| Linux ARM64 | `hermes-linux-arm64` |
| macOS Intel | `hermes-darwin-amd64` |
| macOS Apple Silicon | `hermes-darwin-arm64` |
| Windows x86_64 | `hermes-windows-amd64.exe` |

### Build from source

```bash
git clone https://github.com/darqlab/hermes.git && cd hermes
make build && cp hermes ~/.local/bin/
```

### Check the version

```bash
hermes --version   # or: hermes -v
```

## Quick start (fresh install)

```bash
# 1. Install
curl -sL https://raw.githubusercontent.com/darqlab/hermes/main/install.sh | sh
# installs to ~/.local/bin/hermes — make sure that's on PATH

# 2. Create a config
#    Two valid locations — pick based on how you'll invoke hermes:
#      a) ./hermes.yaml       — per-project config, used when you run hermes from that directory
#      b) ~/hermes.yaml       — machine-wide default, used automatically from ANY directory
#                                when no ./hermes.yaml exists and no --config flag is given
cp hermes.yaml.example ~/hermes.yaml
# Edit: fill in smtp.host, smtp.user, smtp.pass (see "Config" below for every field)

# 3. Verify without sending anything (no SMTP connection made)
hermes send --to test@example.com --subject "Preview" --body "hi" --no-sign --dry-run

# 4. Send a real test
hermes send --to yourself@gmail.com --subject "Test" --body "It works"

# 5. With DKIM (optional)
#    Generate: openssl genrsa -out dkim.key 2048
#    DNS: publish <selector>._domainkey.<domain> TXT record
#    Fill dkim.key_file/selector/domain in hermes.yaml, then:
hermes send --to user@x.com --subject "Signed" --body "DKIM test"
```

**Config resolution order** (first match wins): `--config <path>` flag → `./hermes.yaml` in the current directory → `~/hermes.yaml` as a fallback if neither of the above exists → `HERMES_*` env vars layer on top of whichever file was loaded (or stand alone with no file at all, as long as every required field is covered).

## Config

Hermes reads `hermes.yaml` from the current directory (or `--config <path>`). All values can be overridden by env vars.

```yaml
# hermes.yaml
smtp:
  host: smtp.example.com
  port: 587
  user: alerts@example.com
  pass: ""                    # or set HERMES_SMTP_PASS
  use_tls: false              # direct TLS (port 465)
  starttls: true              # STARTTLS upgrade (port 587)

dkim:
  selector: hermes
  domain: example.com
  key_file: dkim.key          # RSA private key PEM

queue:
  queue_file: hermes_queue.json
  retry_max: 10               # max retries before dead-letter
  backoff_base: 1s            # first retry delay
  backoff_cap: 5m             # max retry delay
  worker_tick: 10s            # retry worker poll interval

log:
  level: info                 # debug, info, warn, error
  format: text                # text, json
```

### Env var overrides

```
HERMES_SMTP_HOST=smtp.example.com
HERMES_SMTP_PORT=587
HERMES_SMTP_USER=alerts@example.com
HERMES_SMTP_PASS=secret
HERMES_SMTP_USE_TLS=true
HERMES_SMTP_STARTTLS=false
HERMES_DKIM_SELECTOR=hermes
HERMES_DKIM_DOMAIN=example.com
HERMES_DKIM_KEY_FILE=/secrets/dkim.key
HERMES_QUEUE_QUEUE_FILE=hermes_queue.json
HERMES_QUEUE_RETRY_MAX=10
HERMES_QUEUE_BACKOFF_BASE=1s
HERMES_QUEUE_BACKOFF_CAP=5m
```

Config file values take precedence (env vars only apply if no config file exists or the value is empty).

## Commands

### `hermes send`

```bash
# Plain text
hermes send --to ops@x.com --subject "Alert" --body "Disk full"

# HTML
hermes send --to team@x.com --subject "Weekly" \
  --body-html "$(cat report.html)"

# Both text + HTML (multipart/alternative)
hermes send --to user@x.com --subject "Hello" \
  --body "Plain fallback" --body-html "<h1>Hello</h1>"

# From stdin (LLM / script pipeline)
echo "Build failed" | hermes send --to dev@x.com --subject "CI"

# Multiple recipients + CC/BCC
hermes send --to a@x.com --to b@x.com --cc archive@x.com \
  --bcc admin@x.com --subject "FYI" --body "..."

# Attachments
hermes send --to team@x.com --subject "Report" \
  --body "Attached" --attach report.csv --attach chart.png

# JSON output (machine-readable)
hermes send --json --to user@x.com --subject "Test" --body "ok"
# → {"status":"ok"}
# → {"status":"queued","job_id":"abc123...","error":"connection refused"}

# Dry-run (print composed MIME, don't send)
hermes send --dry-run --to test@x.com --subject "Preview" --body "hi"

# Fail without queueing
hermes send --no-queue --to user@x.com --subject "Urgent" --body "..."

# Skip DKIM signing
hermes send --no-sign --to user@x.com --subject "Test" --body "..."
```

### `hermes queue`

```bash
# List all jobs
hermes queue list

# Filter by status
hermes queue list --status pending
hermes queue list --status dead

# JSON output
hermes queue list --json | jq '.[] | select(.retry_count > 3)'

# Force retry a specific job
hermes queue retry abc12345-...

# Retry all failed jobs
hermes queue retry-all

# Remove completed and dead jobs
hermes queue purge
```

### Resilience

- **Send fails?** The message is enqueued with full MIME (DKIM-signed, attachments included). Exit code 1.
- **Queue survives restarts** — stored as JSON on disk.
- **Exponential backoff** — 1s → 2s → 4s → 8s → ... up to 5min cap.
- **Dead letter** — after `retry_max` failures (default 10), the job is marked `dead` and logged. Use `hermes queue retry <id>` to retry manually.
- **Worker** — background goroutine polls the queue at `worker_tick` intervals (default 10s) and retries pending jobs automatically when run as a daemon.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Sent successfully |
| 1 | Transient failure — message queued for retry |
| 2 | Permanent failure (config error, auth rejected) |

## LLM integration patterns

### Pattern 1 — fire-and-forget alert

```bash
hermes send --to ops@x.com --subject "Task done" --body "$OUTPUT"
```

### Pattern 2 — send with fallback to queue

```bash
hermes send --json --to user@x.com --subject "..." --body "..."
case $? in
  0) echo "Delivered" ;;
  1) echo "Queued for retry" ;;
  2) echo "Permanent failure" ;;
esac
```

### Pattern 3 — attach output from a pipeline

```bash
some-command 2>&1 | hermes send --to dev@x.com --subject "Pipeline output"
```

### Pattern 4 — dry-run to preview what will be sent

```bash
hermes send --dry-run --to test@x.com --subject "Preview" \
  --body "Content" --body-html "<h1>Content</h1>"
```

## Roadmap

| Version | What |
|---------|------|
| v0.1 | SMTP send, DKIM signing, JSON-file queue with retry, CLI |
| v0.2 | IMAP reader: `hermes read`, `hermes watch` (IDLE), MIME parsing |
| v0.3 | Docker deployment, daemon mode with HTTP control plane |

## License

MIT
