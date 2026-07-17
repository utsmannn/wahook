package webhook

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"wahook/internal/config"
	"wahook/internal/payload"
)

func intPtr(i int) *int { return &i }

func TestMatchFilters(t *testing.T) {
	dm := &payload.Payload{Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "halo"}
	group := &payload.Payload{Chat: "1@g.us", Sender: "62@s.whatsapp.net", IsGroup: true, Type: "text", Text: "halo"}
	fromMe := &payload.Payload{Chat: "62@s.whatsapp.net", Sender: "me@s.whatsapp.net", IsFromMe: true, Type: "text", Text: "halo"}
	broadcast := &payload.Payload{Chat: "status@broadcast", Sender: "me@s.whatsapp.net", Type: "text", Text: "halo"}
	cmd := &payload.Payload{Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "!cmd do"}
	img := &payload.Payload{Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "image", Text: "!cmd caption"}

	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name string
		f    config.FilterConfig
		p    *payload.Payload
		want bool
	}{
		{"empty accepts dm", config.FilterConfig{}, dm, true},
		{"groups_only rejects dm", config.FilterConfig{GroupsOnly: true}, dm, false},
		{"groups_only accepts group", config.FilterConfig{GroupsOnly: true}, group, true},
		{"dm_only rejects group", config.FilterConfig{DMOnly: true}, group, false},
		{"ignore_from_me", config.FilterConfig{IgnoreFromMe: true}, fromMe, false},
		{"broadcast default ignored", config.FilterConfig{}, broadcast, false},
		{"broadcast allowed", config.FilterConfig{IgnoreBroadcast: boolPtr(false)}, broadcast, true},
		{"senders whitelist hit", config.FilterConfig{Senders: []string{"62@s.whatsapp.net"}}, dm, true},
		{"senders whitelist miss", config.FilterConfig{Senders: []string{"63@s.whatsapp.net"}}, dm, false},
		{"keyword hit", config.FilterConfig{KeywordPrefix: "!cmd"}, cmd, true},
		{"keyword miss", config.FilterConfig{KeywordPrefix: "!cmd"}, dm, false},
		{"keyword rejects non-text", config.FilterConfig{KeywordPrefix: "!cmd"}, img, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchFilters(c.f, c.p); got != c.want {
				t.Errorf("matchFilters = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDispatchDeliversToMatchingOnly(t *testing.T) {
	var hitsA, hitsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	cfgs := []config.WebhookConfig{
		{Name: "a", URL: srvA.URL, Timeout: config.Duration(2 * time.Second), Retry: intPtr(0)},
		{Name: "b", URL: srvB.URL, Timeout: config.Duration(2 * time.Second), Retry: intPtr(0),
			Filters: config.FilterConfig{GroupsOnly: true}},
	}
	d := NewDispatcher(cfgs, slog.Default())
	d.Dispatch(&payload.Payload{ID: "1", Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "halo"})
	d.Shutdown(3 * time.Second)

	if hitsA.Load() != 1 {
		t.Errorf("webhook a hits = %d, want 1", hitsA.Load())
	}
	if hitsB.Load() != 0 {
		t.Errorf("webhook b (groups_only) hits = %d, want 0", hitsB.Load())
	}
}

func TestDeliveryPayloadBodyAndHeaders(t *testing.T) {
	var gotBody []byte
	var gotAuth, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfgs := []config.WebhookConfig{{
		Name: "a", URL: srv.URL, Timeout: config.Duration(2 * time.Second), Retry: intPtr(0),
		Headers: map[string]string{"Authorization": "Bearer tok"},
	}}
	d := NewDispatcher(cfgs, slog.Default())
	d.Dispatch(&payload.Payload{ID: "abc", Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "halo dunia"})
	d.Shutdown(3 * time.Second)

	var p payload.Payload
	if err := json.Unmarshal(gotBody, &p); err != nil {
		t.Fatalf("body is not a valid payload: %v", err)
	}
	if p.ID != "abc" || p.Text != "halo dunia" || p.Type != "text" {
		t.Errorf("unexpected payload: %+v", p)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfgs := []config.WebhookConfig{{Name: "a", URL: srv.URL, Timeout: config.Duration(2 * time.Second), Retry: intPtr(1)}}
	d := NewDispatcher(cfgs, slog.Default())
	d.Dispatch(&payload.Payload{ID: "r1", Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "x"})
	d.Shutdown(5 * time.Second)

	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (1 fail + 1 retry)", calls.Load())
	}
}

func TestRetryExhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfgs := []config.WebhookConfig{{Name: "a", URL: srv.URL, Timeout: config.Duration(2 * time.Second), Retry: intPtr(2)}}
	d := NewDispatcher(cfgs, slog.Default())
	d.Dispatch(&payload.Payload{ID: "r2", Chat: "62@s.whatsapp.net", Sender: "62@s.whatsapp.net", Type: "text", Text: "x"})
	d.Shutdown(10 * time.Second)

	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (1 + 2 retries)", calls.Load())
	}
}
