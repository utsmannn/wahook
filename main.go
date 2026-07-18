// wahook — WhatsApp personal → webhook forwarder.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	_ "time/tzdata" // embedded tz database — final image is scratch (no system tzdata)

	"wahook/internal/config"
	"wahook/internal/payload"
	"wahook/internal/webhook"
	"wahook/internal/whatsapp"
)

// seenIDs is a small bounded LRU of recently-dispatched message IDs.
// whatsmeow can surface the same message twice (e.g. LID vs PN addressing),
// so we dedupe by Info.ID to avoid forwarding duplicates.
type seenIDs struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
	max   int
}

func newSeenIDs(max int) *seenIDs {
	return &seenIDs{ids: make(map[string]struct{}), max: max}
}

// add reports whether id was already present.
func (s *seenIDs) add(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return true
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.ids, oldest)
	}
	return false
}

// attachMedia downloads the media attached to evt via c and base64-encodes
// it into p.Media.Data. Files larger than maxBytes are skipped with an error
// note on the payload.
func attachMedia(log *slog.Logger, c *whatsapp.Client, p *payload.Payload, evt *events.Message, maxBytes int64) {
	if maxBytes > 0 && int64(p.Media.FileLength) > maxBytes {
		p.Media.Error = fmt.Sprintf("file size %d exceeds max_bytes %d, skipped", p.Media.FileLength, maxBytes)
		log.Warn("media too large, skipping", "id", p.ID, "size", p.Media.FileLength, "max", maxBytes)
		return
	}
	data, mime, err := c.DownloadMessageMedia(evt)
	if err != nil {
		if errors.Is(err, whatsapp.ErrNotDownloadable) {
			return
		}
		log.Warn("media download failed", "id", p.ID, "err", err)
		p.Media.Error = err.Error()
		return
	}
	p.Media.Data = base64.StdEncoding.EncodeToString(data)
	if mime != "" {
		p.Media.MimeType = mime
	}
	log.Info("media downloaded", "id", p.ID, "bytes", len(data))
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}
	log.Info("config loaded", "webhooks", len(cfg.Webhooks), "store", cfg.Device.Store)

	dispatcher := webhook.NewDispatcher(cfg.Webhooks, log)
	seen := newSeenIDs(1024)

	var waClient *whatsapp.Client
	waClient, err = whatsapp.New(context.Background(), cfg.Device.Store, log, func(evt *events.Message) {
		p := payload.FromMessage(evt)
		if p.IsEmpty() {
			return
		}
		if seen.add(p.ID) {
			log.Debug("duplicate message dropped", "id", p.ID)
			return
		}
		if cfg.Media.Download && p.Media != nil {
			attachMedia(log, waClient, p, evt, cfg.Media.MaxBytes)
		}
		log.Info("message received", "id", p.ID, "chat", p.Chat, "type", p.Type, "text_len", len(p.Text))
		dispatcher.Dispatch(p)
	})
	if err != nil {
		log.Error("init whatsapp client", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := waClient.Connect(ctx); err != nil {
		log.Error("connect", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
	log.Info("shutting down")
	dispatcher.Shutdown(5 * time.Second)
	waClient.Disconnect()
}
