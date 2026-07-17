# AGENTS.md — wahook

## What this is

`wahook` is a small Go CLI daemon that logs in to a **personal WhatsApp** account via the multidevice API (whatsmeow), listens for incoming messages, and forwards each one as JSON to one or more **HTTP webhooks** configured via `config.yaml`.

Goal: self-hosted, lightweight, standalone binary, sqlite-persisted session. Not an interactive bot — purely a message → webhook forwarder (MVP).

## Stack

| Component | Dependency | Notes |
| --- | --- | --- |
| WA client | `go.mau.fi/whatsmeow` | Multidevice API |
| Session store | `go.mau.fi/whatsmeow/store/sqlstore` + `modernc.org/sqlite` | **Pure Go, no CGO** |
| Config | `gopkg.in/yaml.v3` | |
| QR terminal | `mdp/qrterminal/v3` | |
| Webhook sender | `net/http` stdlib | No framework |
| Logging | `log/slog` stdlib | |

**Full technical detail (flow, config schema, payload schema, whatsmeow gotchas) → read `TECHDOC.md`. It is the technical source of truth for this project.**

## Commands

```bash
go build -o wahook .
./wahook -config config.yaml   # first run: QR is shown in the terminal, scan it from WhatsApp
go test ./...
```

Deployment (the official path, see TECHDOC.md §10):

```bash
docker compose build
docker compose run --rm wahook   # pair once (interactive)
docker compose up -d             # daemon, restart: unless-stopped
```

## Structure

```
wahook/
├── main.go                # entrypoint + wiring
├── config.example.yaml    # example config
└── internal/
    ├── config/            # yaml load + validation
    ├── whatsapp/          # whatsmeow wrapper: connect, QR, event handler
    ├── webhook/           # dispatcher: filter → queue → POST (retry/timeout)
    └── payload/           # events.Message → JSON payload mapping
```

## Rules (project conventions)

1. **No CGO** — `CGO_ENABLED=0` must always build. Do not add a CGO dependency.
2. **Stdlib first** — a new dependency needs a strong reason and must be recorded in TECHDOC.md.
3. **The whatsmeow event handler is synchronous** — DO NOT do blocking I/O (HTTP POST, downloads, etc.) inside the event handler. Always hand off to an async queue. See TECHDOC.md §Delivery.
4. **Do not panic on the runtime path** — all errors go through `log/slog`. Panic is only allowed at startup / on invalid config.
5. **New config/feature → update `TECHDOC.md` first** (or alongside), then implement.
6. Webhook error handling: timeout + retry + drop-with-log when the queue is full. One failing webhook must never block other webhooks or the ACK to WhatsApp.
