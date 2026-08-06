package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port = %d, want 587", cfg.SMTP.Port)
	}
	if cfg.SMTP.UseTLS != false {
		t.Errorf("SMTP.UseTLS = %v, want false", cfg.SMTP.UseTLS)
	}
	if cfg.SMTP.StartTLS != true {
		t.Errorf("SMTP.StartTLS = %v, want true", cfg.SMTP.StartTLS)
	}
	if cfg.Queue.QueueFile != "hermes_queue.json" {
		t.Errorf("Queue.QueueFile = %q, want hermes_queue.json", cfg.Queue.QueueFile)
	}
	if cfg.Queue.RetryMax != 10 {
		t.Errorf("Queue.RetryMax = %d, want 10", cfg.Queue.RetryMax)
	}
	if cfg.Queue.BackoffBase != "1s" {
		t.Errorf("Queue.BackoffBase = %q, want 1s", cfg.Queue.BackoffBase)
	}
	if cfg.Queue.BackoffCap != "5m" {
		t.Errorf("Queue.BackoffCap = %q, want 5m", cfg.Queue.BackoffCap)
	}
	if cfg.Queue.WorkerTick != "10s" {
		t.Errorf("Queue.WorkerTick = %q, want 10s", cfg.Queue.WorkerTick)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format = %q, want text", cfg.Log.Format)
	}
}

func clearHermesEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"HERMES_FROM",
		"HERMES_SMTP_HOST",
		"HERMES_SMTP_PORT",
		"HERMES_SMTP_USER",
		"HERMES_SMTP_PASS",
		"HERMES_SMTP_USE_TLS",
		"HERMES_SMTP_STARTTLS",
		"HERMES_DKIM_SELECTOR",
		"HERMES_DKIM_DOMAIN",
		"HERMES_DKIM_KEY_FILE",
		"HERMES_QUEUE_QUEUE_FILE",
		"HERMES_QUEUE_RETRY_MAX",
		"HERMES_QUEUE_BACKOFF_BASE",
		"HERMES_QUEUE_BACKOFF_CAP",
		"HERMES_IMAP_HOST",
		"HERMES_IMAP_PORT",
		"HERMES_IMAP_USER",
		"HERMES_IMAP_PASS",
		"HERMES_IMAP_USE_TLS",
		"HERMES_IMAP_STARTTLS",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}

func TestLoad_ValidYAMLFile(t *testing.T) {
	clearHermesEnv(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlContent := `
smtp:
  host: smtp.example.com
  port: 2525
  user: alice
  pass: secret
  use_tls: true
  starttls: false
dkim:
  selector: mail
  domain: example.com
  key_file: /keys/dkim.pem
queue:
  queue_file: /var/lib/hermes/queue.json
  retry_max: 5
  backoff_base: 2s
  backoff_cap: 1m
  worker_tick: 30s
log:
  level: debug
  format: json
`
	if err := os.WriteFile(path, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SMTP.Host != "smtp.example.com" {
		t.Errorf("SMTP.Host = %q, want smtp.example.com", cfg.SMTP.Host)
	}
	if cfg.SMTP.Port != 2525 {
		t.Errorf("SMTP.Port = %d, want 2525", cfg.SMTP.Port)
	}
	if cfg.SMTP.User != "alice" {
		t.Errorf("SMTP.User = %q, want alice", cfg.SMTP.User)
	}
	if cfg.SMTP.Pass != "secret" {
		t.Errorf("SMTP.Pass = %q, want secret", cfg.SMTP.Pass)
	}
	if !cfg.SMTP.UseTLS {
		t.Errorf("SMTP.UseTLS = false, want true")
	}
	if cfg.SMTP.StartTLS {
		t.Errorf("SMTP.StartTLS = true, want false")
	}
	if cfg.DKIM.Selector != "mail" || cfg.DKIM.Domain != "example.com" || cfg.DKIM.KeyFile != "/keys/dkim.pem" {
		t.Errorf("DKIM = %+v, unexpected", cfg.DKIM)
	}
	if cfg.Queue.RetryMax != 5 {
		t.Errorf("Queue.RetryMax = %d, want 5", cfg.Queue.RetryMax)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("Log = %+v, unexpected", cfg.Log)
	}
}

func TestLoad_MissingFileCompleteEnvVars(t *testing.T) {
	clearHermesEnv(t)
	t.Setenv("HERMES_SMTP_HOST", "smtp.env.example.com")
	t.Setenv("HERMES_SMTP_USER", "envuser")
	t.Setenv("HERMES_SMTP_PASS", "envpass")

	dir := t.TempDir()
	missingPath := filepath.Join(dir, "does-not-exist.yaml")

	cfg, err := Load(missingPath)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.SMTP.Host != "smtp.env.example.com" {
		t.Errorf("SMTP.Host = %q, want smtp.env.example.com", cfg.SMTP.Host)
	}
	if cfg.SMTP.User != "envuser" {
		t.Errorf("SMTP.User = %q, want envuser", cfg.SMTP.User)
	}
	if cfg.SMTP.Pass != "envpass" {
		t.Errorf("SMTP.Pass = %q, want envpass", cfg.SMTP.Pass)
	}
	// defaults should still apply for fields env didn't touch
	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port = %d, want default 587", cfg.SMTP.Port)
	}
}

func TestLoad_MissingFileIncompleteEnvVars(t *testing.T) {
	clearHermesEnv(t)
	t.Setenv("HERMES_SMTP_HOST", "smtp.env.example.com")
	// user/pass intentionally not set -> Validate() should fail

	dir := t.TempDir()
	missingPath := filepath.Join(dir, "does-not-exist.yaml")

	_, err := Load(missingPath)
	if err == nil {
		t.Fatal("Load() error = nil, want error for incomplete env vars")
	}
}

func TestLoad_EnvOverridesLayeredOnFile(t *testing.T) {
	clearHermesEnv(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlContent := `
smtp:
  host: smtp.file.example.com
  user: fileuser
  pass: filepass
`
	if err := os.WriteFile(path, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	t.Setenv("HERMES_SMTP_HOST", "smtp.env.example.com")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SMTP.Host != "smtp.env.example.com" {
		t.Errorf("SMTP.Host = %q, want env override smtp.env.example.com", cfg.SMTP.Host)
	}
	// values not overridden by env should come from the file
	if cfg.SMTP.User != "fileuser" {
		t.Errorf("SMTP.User = %q, want fileuser (from file, not overridden)", cfg.SMTP.User)
	}
}

func TestLoad_HomeFallback(t *testing.T) {
	clearHermesEnv(t)

	fakeHome := t.TempDir()
	homeConfigPath := filepath.Join(fakeHome, "hermes.yaml")
	yamlContent := `
smtp:
  host: smtp.home.example.com
  user: homeuser
  pass: homepass
`
	if err := os.WriteFile(homeConfigPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write home config file: %v", err)
	}

	// Make sure no hermes.yaml exists in the current working directory,
	// which would shadow the ~/hermes.yaml fallback.
	if _, err := os.Stat("hermes.yaml"); err == nil {
		t.Skip("a hermes.yaml exists in the test working directory; skipping to avoid a false pass/fail")
	}

	t.Setenv("HOME", fakeHome)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v", err)
	}
	if cfg.SMTP.Host != "smtp.home.example.com" {
		t.Errorf("SMTP.Host = %q, want smtp.home.example.com (loaded via ~/hermes.yaml fallback)", cfg.SMTP.Host)
	}
}

func TestApplyEnvOverrides_PerVar(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		envVal  string
		check   func(*Config) bool
		descrip string
	}{
		{"smtp host", "HERMES_SMTP_HOST", "host.example.com", func(c *Config) bool { return c.SMTP.Host == "host.example.com" }, "sets SMTP.Host"},
		{"smtp port", "HERMES_SMTP_PORT", "2222", func(c *Config) bool { return c.SMTP.Port == 2222 }, "sets SMTP.Port"},
		{"smtp user", "HERMES_SMTP_USER", "bob", func(c *Config) bool { return c.SMTP.User == "bob" }, "sets SMTP.User"},
		{"smtp pass", "HERMES_SMTP_PASS", "hunter2", func(c *Config) bool { return c.SMTP.Pass == "hunter2" }, "sets SMTP.Pass"},
		{"smtp use_tls true", "HERMES_SMTP_USE_TLS", "true", func(c *Config) bool { return c.SMTP.UseTLS == true }, "sets SMTP.UseTLS"},
		{"smtp use_tls 1", "HERMES_SMTP_USE_TLS", "1", func(c *Config) bool { return c.SMTP.UseTLS == true }, "sets SMTP.UseTLS via 1"},
		{"smtp starttls false", "HERMES_SMTP_STARTTLS", "false", func(c *Config) bool { return c.SMTP.StartTLS == false }, "unsets SMTP.StartTLS"},
		{"smtp starttls 0", "HERMES_SMTP_STARTTLS", "0", func(c *Config) bool { return c.SMTP.StartTLS == false }, "unsets SMTP.StartTLS via 0"},
		{"dkim selector", "HERMES_DKIM_SELECTOR", "sel1", func(c *Config) bool { return c.DKIM.Selector == "sel1" }, "sets DKIM.Selector"},
		{"dkim domain", "HERMES_DKIM_DOMAIN", "dkim.example.com", func(c *Config) bool { return c.DKIM.Domain == "dkim.example.com" }, "sets DKIM.Domain"},
		{"dkim key_file", "HERMES_DKIM_KEY_FILE", "/tmp/key.pem", func(c *Config) bool { return c.DKIM.KeyFile == "/tmp/key.pem" }, "sets DKIM.KeyFile"},
		{"queue file", "HERMES_QUEUE_QUEUE_FILE", "/tmp/q.json", func(c *Config) bool { return c.Queue.QueueFile == "/tmp/q.json" }, "sets Queue.QueueFile"},
		{"queue retry_max", "HERMES_QUEUE_RETRY_MAX", "42", func(c *Config) bool { return c.Queue.RetryMax == 42 }, "sets Queue.RetryMax"},
		{"queue backoff_base", "HERMES_QUEUE_BACKOFF_BASE", "3s", func(c *Config) bool { return c.Queue.BackoffBase == "3s" }, "sets Queue.BackoffBase"},
		{"queue backoff_cap", "HERMES_QUEUE_BACKOFF_CAP", "10m", func(c *Config) bool { return c.Queue.BackoffCap == "10m" }, "sets Queue.BackoffCap"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearHermesEnv(t)
			t.Setenv(tt.envVar, tt.envVal)
			cfg := Defaults()
			applyEnvOverrides(cfg)
			if !tt.check(cfg) {
				t.Errorf("%s: applyEnvOverrides did not apply %s=%s as expected (%s)", tt.name, tt.envVar, tt.envVal, tt.descrip)
			}
		})
	}

	t.Run("starttls not false/0 leaves default", func(t *testing.T) {
		clearHermesEnv(t)
		t.Setenv("HERMES_SMTP_STARTTLS", "yes")
		cfg := Defaults()
		applyEnvOverrides(cfg)
		if !cfg.SMTP.StartTLS {
			t.Errorf("SMTP.StartTLS = false, want true (unrecognized value should not disable it)")
		}
	})

	t.Run("use_tls not true/1 leaves default", func(t *testing.T) {
		clearHermesEnv(t)
		t.Setenv("HERMES_SMTP_USE_TLS", "nope")
		cfg := Defaults()
		applyEnvOverrides(cfg)
		if cfg.SMTP.UseTLS {
			t.Errorf("SMTP.UseTLS = true, want false (unrecognized value should not enable it)")
		}
	})
}

func TestValidate(t *testing.T) {
	validCfg := func() *Config {
		return &Config{
			SMTP: SMTPConfig{
				Host: "smtp.example.com",
				User: "user",
				Pass: "pass",
			},
			Queue: QueueConfig{
				BackoffBase: "1s",
				BackoffCap:  "5m",
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:    "fully valid config",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "missing smtp.host",
			mutate:  func(c *Config) { c.SMTP.Host = "" },
			wantErr: true,
		},
		{
			name:    "missing smtp.user",
			mutate:  func(c *Config) { c.SMTP.User = "" },
			wantErr: true,
		},
		{
			name:    "missing smtp.pass",
			mutate:  func(c *Config) { c.SMTP.Pass = "" },
			wantErr: true,
		},
		{
			name:    "invalid backoff_base",
			mutate:  func(c *Config) { c.Queue.BackoffBase = "not-a-duration" },
			wantErr: true,
		},
		{
			name:    "invalid backoff_cap",
			mutate:  func(c *Config) { c.Queue.BackoffCap = "not-a-duration" },
			wantErr: true,
		},
		{
			name:    "empty backoff strings are allowed (optional)",
			mutate:  func(c *Config) { c.Queue.BackoffBase = ""; c.Queue.BackoffCap = "" },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCfg()
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// sanity check that time.ParseDuration behaves as config.Validate assumes,
// guarding against stdlib behavior changes affecting our assumptions above.
func TestBackoffDurationParsing(t *testing.T) {
	if _, err := time.ParseDuration("1s"); err != nil {
		t.Fatalf("expected 1s to parse: %v", err)
	}
	if _, err := time.ParseDuration("not-a-duration"); err == nil {
		t.Fatalf("expected not-a-duration to fail parsing")
	}
}

func TestDefaults_IMAP(t *testing.T) {
	cfg := Defaults()
	if cfg.IMAP.Port != 993 {
		t.Errorf("IMAP.Port = %d, want 993", cfg.IMAP.Port)
	}
	if !cfg.IMAP.UseTLS {
		t.Errorf("IMAP.UseTLS = false, want true")
	}
}

func TestLoad_ValidYAMLWithIMAP(t *testing.T) {
	clearHermesEnv(t)

	dir := t.TempDir()
	path := dir + "/config.yaml"
	yamlContent := `
smtp:
  host: smtp.example.com
  user: alice
  pass: secret
imap:
  host: imap.example.com
  port: 993
  user: bob
  pass: imap-secret
  use_tls: true
  starttls: false
`
	os.WriteFile(path, []byte(yamlContent), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.IMAP.Host != "imap.example.com" {
		t.Errorf("IMAP.Host = %q", cfg.IMAP.Host)
	}
	if cfg.IMAP.User != "bob" {
		t.Errorf("IMAP.User = %q", cfg.IMAP.User)
	}
	if cfg.IMAP.Pass != "imap-secret" {
		t.Errorf("IMAP.Pass = %q", cfg.IMAP.Pass)
	}
	if !cfg.IMAP.UseTLS {
		t.Errorf("IMAP.UseTLS = false, want true")
	}
	if cfg.IMAP.StartTLS {
		t.Errorf("IMAP.StartTLS = true, want false")
	}
}

func TestApplyEnvOverrides_IMAP(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		envVal string
		check  func(*Config) bool
	}{
		{"imap host", "HERMES_IMAP_HOST", "imap.example.com", func(c *Config) bool { return c.IMAP.Host == "imap.example.com" }},
		{"imap port", "HERMES_IMAP_PORT", "2222", func(c *Config) bool { return c.IMAP.Port == 2222 }},
		{"imap user", "HERMES_IMAP_USER", "bob", func(c *Config) bool { return c.IMAP.User == "bob" }},
		{"imap pass", "HERMES_IMAP_PASS", "hunter2", func(c *Config) bool { return c.IMAP.Pass == "hunter2" }},
		{"imap use_tls true", "HERMES_IMAP_USE_TLS", "true", func(c *Config) bool { return c.IMAP.UseTLS == true }},
		{"imap starttls false", "HERMES_IMAP_STARTTLS", "false", func(c *Config) bool { return c.IMAP.StartTLS == false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearHermesEnv(t)
			t.Setenv(tt.envVar, tt.envVal)
			cfg := Defaults()
			applyEnvOverrides(cfg)
			if !tt.check(cfg) {
				t.Errorf("%s: applyEnvOverrides did not apply %s=%s as expected", tt.name, tt.envVar, tt.envVal)
			}
		})
	}
}

func TestValidate_NoIMAPRequired(t *testing.T) {
	clearHermesEnv(t)

	dir := t.TempDir()
	path := dir + "/config.yaml"
	yamlContent := `
smtp:
  host: smtp.example.com
  user: alice
  pass: secret
`
	os.WriteFile(path, []byte(yamlContent), 0600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("AT-11 regression: Load(send-only config) error = %v, want nil (IMAP must not be required)", err)
	}
	if cfg.IMAP.Host != "" {
		t.Errorf("IMAP.Host = %q, want empty (IMAP fields should remain at defaults when not configured)", cfg.IMAP.Host)
	}
	if cfg.IMAP.Port != 993 {
		t.Errorf("IMAP.Port = %d, want 993 (default should still apply)", cfg.IMAP.Port)
	}
}
