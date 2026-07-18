// Package webhook dispatches payloads to configured endpoints via
// per-endpoint async workers with timeout and retry.
package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"wahook/internal/config"
	"wahook/internal/payload"
)

const queueSize = 100

// Dispatcher fans out payloads to one worker per configured webhook.
type Dispatcher struct {
	workers []*worker
	log     *slog.Logger
}

// NewDispatcher creates and starts a worker for each webhook config.
func NewDispatcher(cfgs []config.WebhookConfig, log *slog.Logger) *Dispatcher {
	d := &Dispatcher{log: log}
	for _, cfg := range cfgs {
		w := &worker{
			cfg:    cfg,
			log:    log.With("webhook", cfg.Name),
			queue:  make(chan *payload.Payload, queueSize),
			done:   make(chan struct{}),
			client: &http.Client{Timeout: cfg.Timeout.Std()},
		}
		d.workers = append(d.workers, w)
		go w.run()
	}
	return d
}

// Dispatch enqueues p to every webhook whose filters accept it.
// Enqueue is non-blocking; a full queue drops the message for that webhook.
func (d *Dispatcher) Dispatch(p *payload.Payload) {
	for _, w := range d.workers {
		if !matchFilters(w.cfg.Filters, p) {
			continue
		}
		select {
		case w.queue <- p:
		default:
			w.log.Warn("queue full, dropping message", "msg_id", p.ID)
		}
	}
}

// Shutdown stops enqueueing and drains queues, bounded by timeout.
func (d *Dispatcher) Shutdown(timeout time.Duration) {
	for _, w := range d.workers {
		close(w.queue)
	}
	allDone := make(chan struct{})
	go func() {
		for _, w := range d.workers {
			<-w.done
		}
		close(allDone)
	}()
	select {
	case <-allDone:
	case <-time.After(timeout):
		d.log.Warn("shutdown timeout, some messages may be undelivered")
	}
}

type worker struct {
	cfg    config.WebhookConfig
	log    *slog.Logger
	queue  chan *payload.Payload
	done   chan struct{}
	client *http.Client
}

func (w *worker) run() {
	defer close(w.done)
	for p := range w.queue {
		w.deliver(p)
	}
}

func (w *worker) deliver(p *payload.Payload) {
	body, err := json.Marshal(p)
	if err != nil {
		w.log.Error("marshal payload failed", "msg_id", p.ID, "err", err)
		return
	}
	attempts := 1 + w.cfg.RetryCount()
	backoff := time.Second
	start := time.Now()
	for attempt := 1; attempt <= attempts; attempt++ {
		err := w.post(body)
		if err == nil {
			w.log.Info("delivered", "msg_id", p.ID, "attempt", attempt, "latency", time.Since(start).Round(time.Millisecond))
			return
		}
		if attempt < attempts {
			w.log.Warn("delivery failed, retrying", "msg_id", p.ID, "attempt", attempt, "err", err, "backoff", backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	w.log.Error("delivery failed permanently", "msg_id", p.ID, "attempts", attempts)
}

func (w *worker) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func matchFilters(f config.FilterConfig, p *payload.Payload) bool {
	if f.GroupsOnly && !p.IsGroup {
		return false
	}
	if f.DMOnly && p.IsGroup {
		return false
	}
	if f.IgnoreFromMe && p.IsFromMe {
		return false
	}
	if f.ShouldIgnoreBroadcast() && strings.HasSuffix(p.Chat, "@broadcast") {
		return false
	}
	if f.ShouldIgnoreNewsletter() && strings.HasSuffix(p.Chat, "@newsletter") {
		return false
	}
	if len(f.Senders) > 0 && !slices.Contains(f.Senders, p.Sender) {
		return false
	}
	if f.KeywordPrefix != "" {
		if p.Type != "text" || !strings.HasPrefix(p.Text, f.KeywordPrefix) {
			return false
		}
	}
	return true
}
