// wahook — WhatsApp personal → webhook forwarder.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"net/http"

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

// attachMedia downloads the media attached to evt, saves it under storageDir,
// and sets p.Media.FileURL when publicURL is non-empty. Files larger than
// maxBytes are skipped with an error note on the payload.
func attachMedia(log *slog.Logger, c *whatsapp.Client, p *payload.Payload, evt *events.Message, storageDir, publicURL string, maxBytes int64) {
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
	if mime != "" {
		p.Media.MimeType = mime
	}
	name := p.ID + extFor(mime)
	full := filepath.Join(storageDir, name)
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		log.Warn("media storage mkdir failed", "id", p.ID, "err", err)
		p.Media.Error = err.Error()
		return
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		log.Warn("media storage write failed", "id", p.ID, "err", err)
		p.Media.Error = err.Error()
		return
	}
	if publicURL != "" {
		p.Media.FileURL = publicURL + "/files/" + name
	}
	log.Info("media stored", "id", p.ID, "bytes", len(data), "path", full)
}

// extFor returns a file extension for a mime type, defaulting to .bin.
func extFor(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg", "audio/ogg; codecs=opus":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "application/pdf":
		return ".pdf"
	}
	return ".bin"
}

// startFileServer serves /files/ from dir on port 8080.
// Mounted at /files/ so it composes with media.public_url + "/files/" + name.
func startFileServer(dir string, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(dir))))
	go func() {
		log.Info("file server listening", "addr", ":8080")
		if err := http.ListenAndServe(":8080", mux); err != nil {
			log.Error("file server stopped", "err", err)
		}
	}()
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
	if cfg.Media.PublicURL != "" {
		log.Info("media serving enabled", "public_url", cfg.TrimPublicURL(), "storage", cfg.Media.Storage)
		startFileServer(cfg.Media.Storage, log)
	}

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
		if cfg.Media.PublicURL != "" && p.Media != nil {
			attachMedia(log, waClient, p, evt, cfg.Media.Storage, cfg.TrimPublicURL(), cfg.Media.MaxBytes)
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
