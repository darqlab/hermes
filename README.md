# hermes — CLI mail sender and reader

Send and read email from the terminal. Designed for **shell scripts** and **LLM agents** (Claude, OpenCode, cron jobs). Single static Go binary, zero runtime dependencies.

```
hermes send --to ops@x.com --subject "Disk 90%" --body "sda1 at 92%"
hermes read --unseen-only --limit 5
hermes watch --json
```

**Current status:** v0.0.8 — send + read + watch all live.

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
from: "forge@example.com"     # default envelope/header From; falls back to smtp.user if unset

smtp:
  host: smtp.example.com
  port: 587
  user: alerts@example.com
  pass: ""                    # or set HERMES_SMTP_PASS
  use_tls: false              # direct TLS (port 465)
  starttls: true              # STARTTLS upgrade (port 587)

imap:
  host: imap.example.com
  port: 993
  user: alerts@example.com
  pass: ""                    # or set HERMES_IMAP_PASS
  use_tls: true               # direct TLS (port 993) — IMAP convention
  starttls: false

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
HERMES_ACCOUNT=work                # selects a named account (see "Multiple accounts")
HERMES_FROM=forge@example.com
HERMES_SMTP_HOST=smtp.example.com  HERMES_SMTP_PORT=587
HERMES_SMTP_USER=alerts@x.com      HERMES_SMTP_PASS=secret
HERMES_SMTP_USE_TLS=true           HERMES_SMTP_STARTTLS=false
HERMES_DKIM_SELECTOR=hermes        HERMES_DKIM_DOMAIN=example.com
HERMES_DKIM_KEY_FILE=/secrets/dkim.key
HERMES_QUEUE_QUEUE_FILE=hermes_queue.json  HERMES_QUEUE_RETRY_MAX=10
HERMES_QUEUE_BACKOFF_BASE=1s       HERMES_QUEUE_BACKOFF_CAP=5m
HERMES_IMAP_HOST=imap.example.com  HERMES_IMAP_PORT=993
HERMES_IMAP_USER=alerts@x.com      HERMES_IMAP_PASS=secret
HERMES_IMAP_USE_TLS=true           HERMES_IMAP_STARTTLS=false
```

Env vars override config file values. imap.* fields are only required for `read`/`watch` — `send` works without any imap config present.

## Multiple accounts

Hermes can hold several mail identities in one config. Declare them under a
top-level `accounts:` map, each with its own `from`/`smtp`/`imap`/`dkim` block:

```yaml
# hermes.yaml
default_account: darqlab

accounts:
  darqlab:                        # Zoho — direct TLS on 465
    from: dennis@darqlab.net
    smtp: {host: smtppro.zoho.com, port: 465, user: dennis@darqlab.net, pass: "", use_tls: true}
    imap: {host: imappro.zoho.com, port: 993, user: dennis@darqlab.net, pass: "", use_tls: true}
    dkim: {selector: hermes, domain: darqlab.net, key_file: dkim.key}

  work:                           # Office 365 — STARTTLS on 587
    from: you@yourcompany.com
    smtp: {host: smtp.office365.com, port: 587, user: you@yourcompany.com, pass: "", starttls: true}
    imap: {host: outlook.office365.com, port: 993, user: you@yourcompany.com, pass: "", use_tls: true}

queue: {queue_file: hermes_queue.json, retry_max: 10}
```

`queue:` and `log:` stay global — only `from`/`smtp`/`imap`/`dkim` are per-account.

### Selecting an account

Every command takes `--account` / `-a`:

```bash
hermes send -a work --to boss@yourcompany.com --subject "Status" --body "..."
hermes read -a work --unseen-only
hermes watch -a darqlab --json
```

Selection order (first match wins):

1. `--account NAME` / `-a NAME`
2. `HERMES_ACCOUNT=NAME` env var
3. `default_account:` in the config file
4. the only declared account, when exactly one exists

If none applies and more than one account is declared, the command fails and
lists the available account names.

For `hermes send`, an explicit `--from` that matches an account's `from`
address (case-insensitive) selects that account automatically — so
`hermes send --from you@yourcompany.com ...` goes out through `work` without
naming it:

```bash
hermes send --from you@yourcompany.com --to boss@yourcompany.com --subject "Hi" --body "..."
```

`--account` always wins over `--from` matching.

### What the account controls

The selected account supplies the SMTP server used for delivery, the DKIM key
used for signing, the IMAP server used by `read`/`watch`, and the default
`From` address. A message that fails delivery is queued **with its account
recorded**, so the retry goes out through the same server. Queue entries
written by older Hermes versions have no account field; they deliver through
the default account.

### Env vars and multiple accounts

`HERMES_FROM`, `HERMES_SMTP_*`, `HERMES_IMAP_*` and `HERMES_DKIM_*` are applied
to *whichever account was resolved* — handy for keeping passwords out of the
file for the one account a given script uses:

```bash
HERMES_ACCOUNT=work HERMES_SMTP_PASS="$WORK_APP_PASSWORD" \
  hermes send --to boss@yourcompany.com --subject "Nightly" --body "ok"
```

`HERMES_QUEUE_*` and `HERMES_ACCOUNT` itself are global.

### Backward compatibility

The flat config (top-level `from:`/`smtp:`/`imap:`/`dkim:` with no `accounts:`
map) is still fully supported and behaves exactly as before — it is treated as
a single account named `default`, and `--account` is not needed.

Validation is per-account and lazy: a misconfigured second account never stops
the first one from working.

### SMTP AUTH mechanisms

Hermes picks its auth mechanism from the server's EHLO response: `AUTH PLAIN`
when offered, otherwise `AUTH LOGIN`. This matters for Office 365
(`smtp.office365.com:587`), which advertises `AUTH LOGIN XOAUTH2` and does not
offer PLAIN at all. Credentials are never sent over an unencrypted connection.

## Commands

### `hermes send`

Send email via SMTP. Composes MIME (plain/HTML/multipart/attachments), DKIM-signs, delivers. On failure, enqueues to the persistent retry queue.

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
# → {"status":"ok","server":"2.0.0 OK: queued as ..."}

# Send through a named account (see "Multiple accounts")
hermes send --account work --to boss@yourcompany.com --subject "Status" --body "..."

# Quiet success output (LLM-friendly, minimal tokens)
hermes send --json --quiet --to user@x.com --subject "Test" --body "ok"
# → {"status":"ok"}

# Dry-run (print composed MIME, don't send)
hermes send --dry-run --to test@x.com --subject "Preview" --body "hi"

# Fail without queueing
hermes send --no-queue --to user@x.com --subject "Urgent" --body "..."

# Skip DKIM signing
hermes send --no-sign --to user@x.com --subject "Test" --body "..."
```

### `hermes read`

Fetch messages from an IMAP mailbox. Server-side search with multiple filters (combined with AND).

```bash
# Most recent 10 messages from INBOX (tab-delimited: date\tfrom\tsubject)
hermes read

# Unread only
hermes read --unseen-only --limit 5

# Filter by sender
hermes read --from "alert@example.com" --limit 10

# Filter by subject
hermes read --subject "disk full"

# Filter by message body
hermes read --body "urgent"

# Date range
hermes read --since 2026-08-01 --before 2026-08-05

# Combined filters (AND)
hermes read --from "joven" --since 2026-08-03 --unseen-only

# Fetch a single message by IMAP UID
hermes read --uid 42 --json

# JSON output (full body fields)
hermes read --json --limit 20

# JSON headers-only (no body_text/body_html — low-token for triage)
hermes read --json --headers-only --limit 20

# Specific mailbox
hermes read --mailbox Sent --limit 5
```

**Output formats:**
- Default (text): `2026-08-05 07:11\tSender Name <addr>\tSubject` per line
- JSON (`--json`): array of objects with `uid`, `from`, `to`, `cc`, `subject`, `date`, `flags`, `body_text`, `body_html`, `attachments`
- Zero matches: `[]` (JSON) or `no messages` (text), exit 0

### `hermes watch`

Watch a mailbox for new messages using IMAP IDLE (falls back to polling when IDLE unavailable). Runs until killed (SIGINT/SIGTERM).

```bash
# Watch INBOX (tab-delimited output per new message)
hermes watch

# JSON Lines output (one JSON object per line, streamable)
hermes watch --json

# Custom poll interval when IDLE unavailable
hermes watch --poll-interval 10s

# Watch a specific mailbox
hermes watch --mailbox Orders

# Background watch, log to file
hermes watch --json >> watch.log &
```

Reconnects with exponential backoff on connection drop (1s → 2s → 4s → ... → 30s cap).

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
| 0 | Success (send delivered; read/watch completed; zero matches is success) |
| 1 | Any failure (config, auth, delivery, connect, search, parse) |

Structured failure info is in JSON status fields or stderr — not in exit code space.

## LLM integration patterns

### Pattern 1 — fire-and-forget alert

```bash
hermes send --to ops@x.com --subject "Task done" --body "$OUTPUT"
```

### Pattern 2 — send with quiet JSON output (lowest token cost)

```bash
hermes send --json --quiet --to user@x.com --subject "..." --body "..."
# → {"status":"ok"}
```

### Pattern 3 — attach output from a pipeline

```bash
some-command 2>&1 | hermes send --to dev@x.com --subject "Pipeline output"
```

### Pattern 4 — read recent unseen messages (triage)

```bash
hermes read --unseen-only --json --headers-only
# returns metadata only, no body blob
```

### Pattern 5 — search by sender and date (contextual lookup)

```bash
hermes read --from "joven" --since 2026-08-01 --json --headers-only
```

### Pattern 6 — fetch a specific message thread

```bash
hermes read --uid 42 --json     # fetch one message
hermes read --subject "Deploy"  # all messages with "Deploy" in subject
```

### Pattern 7 — stream-watch for incoming mail

```bash
hermes watch --json              # one JSON object per line as mail arrives
```

## License

MIT
