// Package whatsapp wraps the whatsmeow client: session store, QR pairing,
// connection lifecycle and event dispatch.
package whatsapp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

// Client wraps a whatsmeow client and its session store.
type Client struct {
	wa        *whatsmeow.Client
	container *sqlstore.Container
	log       *slog.Logger
	onMessage func(*events.Message)
}

// New opens (or creates) the sqlite session store at storePath and builds a client.
// onMessage is invoked synchronously by whatsmeow for every incoming message —
// it must never block.
func New(ctx context.Context, storePath string, log *slog.Logger, onMessage func(*events.Message)) (*Client, error) {
	dbLog := waLog.Stdout("Database", "WARN", true)
	container, err := sqlstore.New(ctx, "sqlite", "file:"+storePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", dbLog)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device store: %w", err)
	}

	waLogger := waLog.Stdout("WhatsApp", "INFO", true)
	cli := whatsmeow.NewClient(device, waLogger)
	cli.EnableAutoReconnect = true
	cli.AutoTrustIdentity = true

	c := &Client{wa: cli, container: container, log: log, onMessage: onMessage}
	cli.AddEventHandler(c.handleEvent)
	return c, nil
}

// Connect connects to WhatsApp, running the QR pairing flow on first login.
func (c *Client) Connect(ctx context.Context) error {
	if c.wa.Store.ID == nil {
		// No stored credentials — QR pairing. GetQRChannel must be
		// called BEFORE Connect.
		qrChan, err := c.wa.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("get qr channel: %w", err)
		}
		if err := c.wa.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		c.log.Info("pairing required — scan the QR code from WhatsApp > Linked Devices")
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			case "success":
				c.log.Info("pairing successful")
			case "timeout":
				return fmt.Errorf("qr pairing timed out")
			default:
				c.log.Info("qr event", "event", evt.Event)
			}
		}
		return nil
	}

	if err := c.wa.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c.log.Info("connecting with existing session", "jid", c.wa.Store.ID.String())
	return nil
}

// Disconnect closes the WhatsApp connection and the session store.
func (c *Client) Disconnect() {
	c.wa.Disconnect()
	if err := c.container.Close(); err != nil {
		c.log.Warn("closing session store", "err", err)
	}
}

func (c *Client) handleEvent(evt any) {
	switch v := evt.(type) {
	case *events.Message:
		c.onMessage(v)
	case *events.Connected:
		c.log.Info("connected to whatsapp")
	case *events.Disconnected:
		c.log.Warn("disconnected, auto-reconnect will retry")
	case *events.ConnectFailure:
		c.log.Warn("connect failure", "reason", v.Reason.String())
	case *events.LoggedOut:
		c.log.Error("logged out from whatsapp — re-pairing required (delete session store and restart)")
		os.Exit(2)
	}
}
