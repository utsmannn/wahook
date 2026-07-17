// wahook — WhatsApp personal → webhook forwarder.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	_ "time/tzdata" // embedded tz database — final image is scratch (no system tzdata)

	"wahook/internal/config"
	"wahook/internal/payload"
	"wahook/internal/webhook"
	"wahook/internal/whatsapp"
)

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

	waClient, err := whatsapp.New(context.Background(), cfg.Device.Store, log, func(evt *events.Message) {
		p := payload.FromMessage(evt)
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
