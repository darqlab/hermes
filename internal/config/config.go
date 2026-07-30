package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

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

type Config struct {
	SMTP  SMTPConfig  `yaml:"smtp"`
	DKIM  DKIMConfig  `yaml:"dkim"`
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

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("HERMES_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("HERMES_SMTP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.SMTP.Port)
	}
	if v := os.Getenv("HERMES_SMTP_USER"); v != "" {
		cfg.SMTP.User = v
	}
	if v := os.Getenv("HERMES_SMTP_PASS"); v != "" {
		cfg.SMTP.Pass = v
	}
	if v := os.Getenv("HERMES_SMTP_USE_TLS"); v == "true" || v == "1" {
		cfg.SMTP.UseTLS = true
	}
	if v := os.Getenv("HERMES_SMTP_STARTTLS"); v == "false" || v == "0" {
		cfg.SMTP.StartTLS = false
	}
	if v := os.Getenv("HERMES_DKIM_SELECTOR"); v != "" {
		cfg.DKIM.Selector = v
	}
	if v := os.Getenv("HERMES_DKIM_DOMAIN"); v != "" {
		cfg.DKIM.Domain = v
	}
	if v := os.Getenv("HERMES_DKIM_KEY_FILE"); v != "" {
		cfg.DKIM.KeyFile = v
	}
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

func (c *Config) Validate() error {
	if c.SMTP.Host == "" {
		return fmt.Errorf("smtp.host is required")
	}
	if c.SMTP.User == "" {
		return fmt.Errorf("smtp.user is required")
	}
	if c.SMTP.Pass == "" {
		return fmt.Errorf("smtp.pass is required (set in config or HERMES_SMTP_PASS)")
	}
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
	return nil
}
