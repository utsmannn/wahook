package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
webhooks:
  - name: a
    url: http://localhost:9000/wa
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Device.Store != "./wa.db" {
		t.Errorf("store default = %q, want ./wa.db", cfg.Device.Store)
	}
	w := cfg.Webhooks[0]
	if w.Timeout.Std() != 10*time.Second {
		t.Errorf("timeout default = %v, want 10s", w.Timeout.Std())
	}
	if w.RetryCount() != 2 {
		t.Errorf("retry default = %d, want 2", w.RetryCount())
	}
	if !w.Filters.ShouldIgnoreBroadcast() {
		t.Error("ignore_broadcast default should be true")
	}
}

func TestLoadExplicitValues(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
device:
  store: /tmp/x.db
webhooks:
  - name: a
    url: https://example.com/hook
    timeout: 3s
    retry: 0
    headers:
      Authorization: Bearer tok
    filters:
      ignore_broadcast: false
      groups_only: true
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	w := cfg.Webhooks[0]
	if cfg.Device.Store != "/tmp/x.db" {
		t.Errorf("store = %q", cfg.Device.Store)
	}
	if w.Timeout.Std() != 3*time.Second {
		t.Errorf("timeout = %v, want 3s", w.Timeout.Std())
	}
	if w.RetryCount() != 0 {
		t.Errorf("retry = %d, want 0", w.RetryCount())
	}
	if w.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("headers = %v", w.Headers)
	}
	if w.Filters.ShouldIgnoreBroadcast() {
		t.Error("ignore_broadcast should be false")
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"no webhooks": `device: {}`,
		"duplicate name": `
webhooks:
  - {name: a, url: http://x.com}
  - {name: a, url: http://y.com}`,
		"bad url": `
webhooks:
  - {name: a, url: ftp://x.com}`,
		"groups and dm": `
webhooks:
  - name: a
    url: http://x.com
    filters: {groups_only: true, dm_only: true}`,
		"negative retry": `
webhooks:
  - {name: a, url: http://x.com, retry: -1}`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, yaml)); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}
