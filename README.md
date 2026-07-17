# wahook

Personal WhatsApp → webhook forwarder. A small Go daemon built on whatsmeow; incoming messages are forwarded as JSON to HTTP webhooks configured via YAML. See `TECHDOC.md` for technical detail.

## Quickstart (Docker)

```bash
cp config.example.yaml config.yaml   # edit the webhook URL; device.store MUST be /data/wa.db
docker compose build

# 1. Pair once (interactive — scan the QR from WhatsApp > Linked Devices)
docker compose run --rm wahook

# 2. Run forever
docker compose up -d
docker compose logs -f
```

## Quickstart (binary)

```bash
go build -o wahook .
cp config.example.yaml config.yaml    # device.store: ./wa.db
./wahook -config config.yaml          # scan the QR in the terminal
```

## Test

```bash
go test ./...
```
