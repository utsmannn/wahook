// Package config loads and validates wahook's YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings like "10s".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a standard time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the root configuration structure.
type Config struct {
	Device   DeviceConfig    `yaml:"device"`
	Webhooks []WebhookConfig `yaml:"webhooks"`
}

// DeviceConfig holds session storage settings.
type DeviceConfig struct {
	Store string `yaml:"store"`
}

// WebhookConfig describes one webhook endpoint.
type WebhookConfig struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Timeout Duration          `yaml:"timeout"`
	Retry   *int              `yaml:"retry"`
	Filters FilterConfig      `yaml:"filters"`
}

// RetryCount returns the configured retry count, defaulting to 2.
func (w WebhookConfig) RetryCount() int {
	if w.Retry == nil {
		return 2
	}
	return *w.Retry
}

// FilterConfig describes per-webhook message filters (ANDed together).
type FilterConfig struct {
	GroupsOnly      bool     `yaml:"groups_only"`
	DMOnly          bool     `yaml:"dm_only"`
	IgnoreFromMe    bool     `yaml:"ignore_from_me"`
	IgnoreBroadcast *bool    `yaml:"ignore_broadcast"`
	Senders         []string `yaml:"senders"`
	KeywordPrefix   string   `yaml:"keyword_prefix"`
}

// ShouldIgnoreBroadcast reports whether broadcast/status messages are dropped (default true).
func (f FilterConfig) ShouldIgnoreBroadcast() bool {
	if f.IgnoreBroadcast == nil {
		return true
	}
	return *f.IgnoreBroadcast
}

// Load reads, parses, defaults and validates the config file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Device.Store == "" {
		c.Device.Store = "./wa.db"
	}
	for i := range c.Webhooks {
		if c.Webhooks[i].Timeout == 0 {
			c.Webhooks[i].Timeout = Duration(10 * time.Second)
		}
	}
}

func (c *Config) validate() error {
	if len(c.Webhooks) == 0 {
		return fmt.Errorf("config: webhooks must have at least 1 entry")
	}
	seen := make(map[string]bool)
	for i, w := range c.Webhooks {
		if w.Name == "" {
			return fmt.Errorf("config: webhooks[%d]: name is required", i)
		}
		if seen[w.Name] {
			return fmt.Errorf("config: duplicate webhook name %q", w.Name)
		}
		seen[w.Name] = true
		u, err := url.Parse(w.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("config: webhook %q: url must be a valid http(s) URL", w.Name)
		}
		if w.Retry != nil && *w.Retry < 0 {
			return fmt.Errorf("config: webhook %q: retry must be >= 0", w.Name)
		}
		if w.Filters.GroupsOnly && w.Filters.DMOnly {
			return fmt.Errorf("config: webhook %q: groups_only and dm_only are mutually exclusive", w.Name)
		}
	}
	return nil
}
