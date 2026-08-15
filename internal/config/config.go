package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type IMAPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Pass     string `yaml:"pass"`
	UseTLS   bool   `yaml:"use_tls"`
	StartTLS bool   `yaml:"starttls"`
}

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Pass     string `yaml:"pass"`
	UseTLS   bool   `yaml:"use_tls"`
	StartTLS bool   `yaml:"starttls"`
}

type DKIMConfig struct {
	Selector string `yaml:"selector"`
	Domain   string `yaml:"domain"`
	KeyFile  string `yaml:"key_file"`
}

type QueueConfig struct {
	QueueFile   string `yaml:"queue_file"`
	RetryMax    int    `yaml:"retry_max"`
	BackoffBase string `yaml:"backoff_base"`
	BackoffCap  string `yaml:"backoff_cap"`
	WorkerTick  string `yaml:"worker_tick"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Account is one named mail identity: its own From address, SMTP server,
// IMAP server and DKIM key. Accounts are declared under the top-level
// "accounts:" map; the legacy flat form (top-level from/smtp/imap/dkim) is
// still accepted and is synthesized into a single account named "default".
type Account struct {
	Name string     `yaml:"-"`
	From string     `yaml:"from"`
	SMTP SMTPConfig `yaml:"smtp"`
	IMAP IMAPConfig `yaml:"imap"`
	DKIM DKIMConfig `yaml:"dkim"`
}

type Config struct {
	// Legacy flat form — kept for backward compatibility. Used only when
	// no "accounts:" map is defined.
	From string     `yaml:"from"`
	SMTP SMTPConfig `yaml:"smtp"`
	IMAP IMAPConfig `yaml:"imap"`
	DKIM DKIMConfig `yaml:"dkim"`

	// Multi-account form.
	DefaultAccount string             `yaml:"default_account"`
	Accounts       map[string]Account `yaml:"accounts"`

	// Global (account-independent) settings.
	Queue QueueConfig `yaml:"queue"`
	Log   LogConfig   `yaml:"log"`
}

func Defaults() *Config {
	return &Config{
		SMTP: SMTPConfig{
			Port:     587,
			UseTLS:   false,
			StartTLS: true,
		},
		IMAP: IMAPConfig{
			Port:   993,
			UseTLS: true,
		},
		Queue: QueueConfig{
			QueueFile:   "hermes_queue.json",
			RetryMax:    10,
			BackoffBase: "1s",
			BackoffCap:  "5m",
			WorkerTick:  "10s",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Defaults()

	if path == "" {
		path = "hermes.yaml"
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && path == "hermes.yaml" {
		if home := os.Getenv("HOME"); home != "" {
			homePath := home + "/hermes.yaml"
			if d, err2 := os.ReadFile(homePath); err2 == nil {
				path = homePath
				data = d
				err = nil
			}
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			applyEnvOverrides(cfg)
			if err := cfg.Validate(); err != nil {
				return nil, fmt.Errorf("no config file and env vars incomplete: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

// AccountNames returns the declared account names, sorted.
func (c *Config) AccountNames() []string {
	names := make([]string, 0, len(c.Accounts))
	for name := range c.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve picks the account to use. Selection order:
//
//	name argument (--account) -> HERMES_ACCOUNT -> config default_account ->
//	the only declared account, if exactly one -> error.
//
// When no "accounts:" map is declared at all, a single account named
// "default" is synthesized from the legacy top-level from/smtp/imap/dkim
// fields, so existing flat configs keep working unchanged.
//
// HERMES_FROM / HERMES_SMTP_* / HERMES_IMAP_* / HERMES_DKIM_* env overrides
// are applied to the resolved account.
func (c *Config) Resolve(name string) (*Account, error) {
	if len(c.Accounts) == 0 {
		acct := Account{
			Name: "default",
			From: c.From,
			SMTP: c.SMTP,
			IMAP: c.IMAP,
			DKIM: c.DKIM,
		}
		applyAccountEnvOverrides(&acct)
		if err := acct.Validate(); err != nil {
			return nil, err
		}
		return &acct, nil
	}

	selected := name
	if selected == "" {
		selected = os.Getenv("HERMES_ACCOUNT")
	}
	if selected == "" {
		selected = c.DefaultAccount
	}
	if selected == "" {
		if len(c.Accounts) == 1 {
			selected = c.AccountNames()[0]
		} else {
			return nil, fmt.Errorf(
				"no account selected: pass --account, set HERMES_ACCOUNT, or set default_account in the config (available accounts: %s)",
				strings.Join(c.AccountNames(), ", "))
		}
	}

	acct, ok := c.Accounts[selected]
	if !ok {
		return nil, fmt.Errorf("account %q not found (available accounts: %s)",
			selected, strings.Join(c.AccountNames(), ", "))
	}
	acct.Name = selected

	applyAccountDefaults(&acct)
	applyAccountEnvOverrides(&acct)

	if err := acct.Validate(); err != nil {
		return nil, err
	}
	return &acct, nil
}

// AccountForFrom finds the account whose "from" address matches addr
// (case-insensitively). Used to route "hermes send --from X" to the account
// that owns X.
func (c *Config) AccountForFrom(addr string) (*Account, bool) {
	if addr == "" {
		return nil, false
	}
	want := strings.ToLower(strings.TrimSpace(addr))

	if len(c.Accounts) == 0 {
		if strings.ToLower(strings.TrimSpace(c.From)) == want {
			acct, err := c.Resolve("")
			if err != nil {
				return nil, false
			}
			return acct, true
		}
		return nil, false
	}

	for _, name := range c.AccountNames() {
		if strings.ToLower(strings.TrimSpace(c.Accounts[name].From)) == want {
			acct, err := c.Resolve(name)
			if err != nil {
				return nil, false
			}
			return acct, true
		}
	}
	return nil, false
}

// applyAccountDefaults fills in the per-account equivalents of Defaults()
// for accounts read out of the "accounts:" map (which the YAML decoder
// populates from zero values, not from Defaults()).
func applyAccountDefaults(a *Account) {
	if a.SMTP.Port == 0 {
		a.SMTP.Port = 587
	}
	if !a.SMTP.UseTLS && !a.SMTP.StartTLS {
		if a.SMTP.Port == 465 {
			a.SMTP.UseTLS = true
		} else {
			a.SMTP.StartTLS = true
		}
	}
	if a.IMAP.Port == 0 {
		a.IMAP.Port = 993
	}
	if !a.IMAP.UseTLS && !a.IMAP.StartTLS {
		a.IMAP.UseTLS = true
	}
}

// applyEnvOverrides applies every HERMES_* override. The account-scoped part
// lands on the legacy top-level fields (which Resolve turns into the
// "default" account when no accounts map is declared); the global part lands
// on Config itself.
func applyEnvOverrides(cfg *Config) {
	acct := Account{From: cfg.From, SMTP: cfg.SMTP, IMAP: cfg.IMAP, DKIM: cfg.DKIM}
	applyAccountEnvOverrides(&acct)
	cfg.From, cfg.SMTP, cfg.IMAP, cfg.DKIM = acct.From, acct.SMTP, acct.IMAP, acct.DKIM

	applyGlobalEnvOverrides(cfg)
}

func applyAccountEnvOverrides(a *Account) {
	if v := os.Getenv("HERMES_FROM"); v != "" {
		a.From = v
	}
	if v := os.Getenv("HERMES_SMTP_HOST"); v != "" {
		a.SMTP.Host = v
	}
	if v := os.Getenv("HERMES_SMTP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &a.SMTP.Port)
	}
	if v := os.Getenv("HERMES_SMTP_USER"); v != "" {
		a.SMTP.User = v
	}
	if v := os.Getenv("HERMES_SMTP_PASS"); v != "" {
		a.SMTP.Pass = v
	}
	if v := os.Getenv("HERMES_SMTP_USE_TLS"); v == "true" || v == "1" {
		a.SMTP.UseTLS = true
	}
	if v := os.Getenv("HERMES_SMTP_STARTTLS"); v == "false" || v == "0" {
		a.SMTP.StartTLS = false
	}
	if v := os.Getenv("HERMES_IMAP_HOST"); v != "" {
		a.IMAP.Host = v
	}
	if v := os.Getenv("HERMES_IMAP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &a.IMAP.Port)
	}
	if v := os.Getenv("HERMES_IMAP_USER"); v != "" {
		a.IMAP.User = v
	}
	if v := os.Getenv("HERMES_IMAP_PASS"); v != "" {
		a.IMAP.Pass = v
	}
	if v := os.Getenv("HERMES_IMAP_USE_TLS"); v == "true" || v == "1" {
		a.IMAP.UseTLS = true
	}
	if v := os.Getenv("HERMES_IMAP_STARTTLS"); v == "false" || v == "0" {
		a.IMAP.StartTLS = false
	}
	if v := os.Getenv("HERMES_DKIM_SELECTOR"); v != "" {
		a.DKIM.Selector = v
	}
	if v := os.Getenv("HERMES_DKIM_DOMAIN"); v != "" {
		a.DKIM.Domain = v
	}
	if v := os.Getenv("HERMES_DKIM_KEY_FILE"); v != "" {
		a.DKIM.KeyFile = v
	}
}

func applyGlobalEnvOverrides(cfg *Config) {
	if v := os.Getenv("HERMES_QUEUE_QUEUE_FILE"); v != "" {
		cfg.Queue.QueueFile = v
	}
	if v := os.Getenv("HERMES_QUEUE_RETRY_MAX"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Queue.RetryMax)
	}
	if v := os.Getenv("HERMES_QUEUE_BACKOFF_BASE"); v != "" {
		cfg.Queue.BackoffBase = v
	}
	if v := os.Getenv("HERMES_QUEUE_BACKOFF_CAP"); v != "" {
		cfg.Queue.BackoffCap = v
	}
}

// Validate checks only global, account-independent settings. Per-account
// SMTP credentials are validated in Account.Validate (via Resolve), so a
// broken second account cannot stop the first one from working.
func (c *Config) Validate() error {
	if c.Queue.BackoffBase != "" {
		if _, err := time.ParseDuration(c.Queue.BackoffBase); err != nil {
			return fmt.Errorf("queue.backoff_base: %w", err)
		}
	}
	if c.Queue.BackoffCap != "" {
		if _, err := time.ParseDuration(c.Queue.BackoffCap); err != nil {
			return fmt.Errorf("queue.backoff_cap: %w", err)
		}
	}
	if c.DefaultAccount != "" && len(c.Accounts) > 0 {
		if _, ok := c.Accounts[c.DefaultAccount]; !ok {
			return fmt.Errorf("default_account %q is not defined under accounts: (available accounts: %s)",
				c.DefaultAccount, strings.Join(c.AccountNames(), ", "))
		}
	}
	return nil
}

// Validate checks that this account has everything needed to send mail.
func (a *Account) Validate() error {
	if a.SMTP.Host == "" {
		return fmt.Errorf("account %q: smtp.host is required", a.Name)
	}
	if a.SMTP.User == "" {
		return fmt.Errorf("account %q: smtp.user is required", a.Name)
	}
	if a.SMTP.Pass == "" {
		return fmt.Errorf("account %q: smtp.pass is required (set in config or HERMES_SMTP_PASS)", a.Name)
	}
	return nil
}

// ValidateIMAP checks that this account has everything needed for read/watch.
func (a *Account) ValidateIMAP() error {
	if a.IMAP.Host == "" {
		return fmt.Errorf("account %q: imap.host is required for read/watch (set in config or HERMES_IMAP_HOST)", a.Name)
	}
	if a.IMAP.User == "" {
		return fmt.Errorf("account %q: imap.user is required for read/watch (set in config or HERMES_IMAP_USER)", a.Name)
	}
	if a.IMAP.Pass == "" {
		return fmt.Errorf("account %q: imap.pass is required for read/watch (set in config or HERMES_IMAP_PASS)", a.Name)
	}
	return nil
}
