# Webhook Router

Lightweight Go service for routing webhooks from external services to OpenClaw.

## Routes

| Route | Method | Description |
|-------|--------|-------------|
| `/webhook/telnyx/sms` | POST | Receive SMS from Telnyx (Signal verification) |
| `/health` | GET | Health check |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `OPENCLAW_WEBHOOK_URL` | `http://localhost:18789/webhook` | Where to forward events |
| `WEBHOOK_SECRET` | - | Optional validation secret |

## Running Locally

```bash
cd webhook-router
go run main.go
```

## Building

```bash
go build -o webhook-router main.go
```

## Deployment

See `k8s/` directory for Kubernetes manifests.
