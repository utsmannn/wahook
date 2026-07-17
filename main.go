// wahook — WhatsApp personal → webhook forwarder.
package main

import (
	"context"
	"flag"
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
	mu   sync.Mutex
	ids  map[string]struct{}
	order []string
	max  int
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

	waClient, err := whatsapp.New(context.Background(), cfg.Device.Store, log, func(evt *events.Message) {
		p := payload.FromMessage(evt)
		// Skip receipts / protocol / typing indicators — no user content.
		if p.IsEmpty() {
			return
		}
		// Dedupe: whatsmeow may emit the same message twice (LID vs PN).
		if seen.add(p.ID) {
			log.Debug("duplicate message dropped", "id", p.ID)
			return
		}
		// No message text at info level (privacy) — debug only.
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
