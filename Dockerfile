# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.mod
COPY main.go ./
RUN go mod tidy
RUN go build -o webhook-router main.go

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/webhook-router .

# Create data directory for SQLite
RUN mkdir -p /app/data

EXPOSE 8080

CMD ["./webhook-router"]
