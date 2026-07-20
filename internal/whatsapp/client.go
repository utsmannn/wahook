// Package whatsapp wraps the whatsmeow client: session store, QR pairing,
// connection lifecycle and event dispatch.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

// ErrNotDownloadable is returned by DownloadMessageMedia when the message
// carries no downloadable media (text, contact, location, reaction, etc.).
var ErrNotDownloadable = errors.New("message has no downloadable media")

// Client wraps a whatsmeow client and its session store.
type Client struct {
	wa        *whatsmeow.Client
	container *sqlstore.Container
	log       *slog.Logger
	onMessage func(*events.Message)

	groupMu    sync.RWMutex
	groupNames map[string]string
	groupMiss  map[string]time.Time
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

	c := &Client{
		wa:         cli,
		container:  container,
		log:        log,
		onMessage:  onMessage,
		groupNames: make(map[string]string),
		groupMiss:  make(map[string]time.Time),
	}
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

// DownloadMessageMedia fetches and decrypts the media attached to evt and
// returns the raw bytes and mime type.
func (c *Client) DownloadMessageMedia(evt *events.Message) ([]byte, string, error) {
	msg := evt.Message
	var downloadable whatsmeow.DownloadableMessage
	var mime string
	switch {
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		downloadable = m
		mime = m.GetMimetype()
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		downloadable = m
		mime = m.GetMimetype()
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		downloadable = m
		mime = m.GetMimetype()
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		downloadable = m
		mime = m.GetMimetype()
	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		downloadable = m
		mime = m.GetMimetype()
	default:
		return nil, "", ErrNotDownloadable
	}
	data, err := c.wa.Download(context.Background(), downloadable)
	if err != nil {
		return nil, mime, err
	}
	return data, mime, nil
}

// ChatName returns the display name for a group JID ("Subject" in WA terms).
// Results are cached in memory; failed lookups are cached as misses for 10
// minutes to avoid hammering the server on a group we can't see.
// Returns "" for non-group JIDs or when the lookup fails.
func (c *Client) ChatName(ctx context.Context, jid types.JID) string {
	if jid.Server != types.GroupServer {
		return ""
	}
	key := jid.String()

	c.groupMu.RLock()
	if name, ok := c.groupNames[key]; ok {
		c.groupMu.RUnlock()
		return name
	}
	if at, miss := c.groupMiss[key]; miss && time.Since(at) < 10*time.Minute {
		c.groupMu.RUnlock()
		return ""
	}
	c.groupMu.RUnlock()

	c.groupMu.Lock()
	defer c.groupMu.Unlock()
	if name, ok := c.groupNames[key]; ok {
		return name
	}
	if at, miss := c.groupMiss[key]; miss && time.Since(at) < 10*time.Minute {
		return ""
	}

	info, err := c.wa.GetGroupInfo(ctx, jid)
	if err != nil {
		c.log.Debug("group info lookup failed", "chat", key, "err", err)
		c.groupMiss[key] = time.Now()
		return ""
	}
	name := info.GroupName.Name
	c.groupNames[key] = name
	delete(c.groupMiss, key)
	return name
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
