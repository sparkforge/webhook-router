# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY main.go ./
RUN go mod init webhook-router
RUN go build -o webhook-router main.go

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/webhook-router .

EXPOSE 8080

CMD ["./webhook-router"]
