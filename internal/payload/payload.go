// Package payload maps whatsmeow message events to the webhook JSON schema.
package payload

import (
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// Payload is the JSON body POSTed to webhook endpoints.
type Payload struct {
	ID        string     `json:"id"`
	Chat      string     `json:"chat"`
	Sender    string     `json:"sender"`
	SenderAlt string     `json:"sender_alt,omitempty"`
	PushName  string     `json:"push_name"`
	IsGroup   bool       `json:"is_group"`
	IsFromMe  bool       `json:"is_from_me"`
	Timestamp time.Time  `json:"timestamp"`
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Media     *MediaInfo `json:"media"`
}

// MediaInfo carries media metadata only — files are not downloaded in MVP.
type MediaInfo struct {
	MimeType   string `json:"mime_type,omitempty"`
	FileName   string `json:"file_name,omitempty"`
	FileLength uint64 `json:"file_length,omitempty"`
	Width      uint32 `json:"width,omitempty"`
	Height     uint32 `json:"height,omitempty"`
	Caption    string `json:"caption,omitempty"`
	Seconds    uint32 `json:"seconds,omitempty"`
	PTT        bool   `json:"ptt,omitempty"`
	Data    string `json:"-"`              // removed: base64 bloated payloads; files now served via file_url
	FileURL string `json:"file_url,omitempty"` // populated when media.public_url is configured
	Error   string `json:"error,omitempty"`    // populated if download/storage failed
}

// IsEmpty reports whether the payload carries no user-visible content.
// Receipts, protocol messages and typing indicators produce an "unknown"
// payload with empty text and no media — these should not be dispatched.
func (p *Payload) IsEmpty() bool {
	return p.Type == "unknown" && p.Text == "" && p.Media == nil
}

// FromMessage converts an events.Message into a Payload.
func FromMessage(evt *events.Message) *Payload {
	info := evt.Info
	p := &Payload{
		ID:        info.ID,
		Chat:      info.Chat.String(),
		Sender:    info.Sender.String(),
		PushName:  info.PushName,
		IsGroup:   info.IsGroup,
		IsFromMe:  info.IsFromMe,
		Timestamp: info.Timestamp,
		Type:      "unknown",
	}
	if !info.SenderAlt.IsEmpty() {
		p.SenderAlt = info.SenderAlt.String()
	}

	msg := evt.Message
	if msg == nil {
		return p
	}

	switch {
	case msg.GetConversation() != "":
		p.Type = "text"
		p.Text = msg.GetConversation()
	case msg.GetExtendedTextMessage() != nil:
		p.Type = "text"
		p.Text = msg.GetExtendedTextMessage().GetText()
	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		p.Type = "image"
		p.Text = m.GetCaption()
		p.Media = &MediaInfo{
			MimeType:   m.GetMimetype(),
			FileLength: m.GetFileLength(),
			Width:      m.GetWidth(),
			Height:     m.GetHeight(),
			Caption:    m.GetCaption(),
		}
	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		p.Type = "video"
		p.Text = m.GetCaption()
		p.Media = &MediaInfo{
			MimeType:   m.GetMimetype(),
			FileLength: m.GetFileLength(),
			Width:      m.GetWidth(),
			Height:     m.GetHeight(),
			Caption:    m.GetCaption(),
			Seconds:    m.GetSeconds(),
		}
	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		p.Type = "audio"
		p.Media = &MediaInfo{
			MimeType:   m.GetMimetype(),
			FileLength: m.GetFileLength(),
			Seconds:    m.GetSeconds(),
			PTT:        m.GetPTT(),
		}
	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		p.Type = "document"
		p.Text = m.GetCaption()
		p.Media = &MediaInfo{
			MimeType:   m.GetMimetype(),
			FileName:   m.GetFileName(),
			FileLength: m.GetFileLength(),
			Caption:    m.GetCaption(),
		}
	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		p.Type = "sticker"
		p.Media = &MediaInfo{MimeType: m.GetMimetype()}
	case msg.GetLocationMessage() != nil:
		m := msg.GetLocationMessage()
		p.Type = "location"
		p.Text = strings.TrimSpace(m.GetName() + " " + m.GetAddress())
	case msg.GetContactMessage() != nil:
		m := msg.GetContactMessage()
		p.Type = "contact"
		p.Text = m.GetDisplayName()
	case msg.GetReactionMessage() != nil:
		m := msg.GetReactionMessage()
		p.Type = "reaction"
		p.Text = m.GetText()
	}
	return p
}
