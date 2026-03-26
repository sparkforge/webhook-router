package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// TelnyxSMSPayload represents the webhook payload from Telnyx
type TelnyxSMSPayload struct {
	Data struct {
		Payload struct {
			To          string `json:"to"`
			From        string `json:"from"`
			Body        string `json:"text"`
			MessageID   string `json:"id"`
		} `json:"payload"`
	} `json:"data"`
}

// WebhookRouter handles incoming webhooks and routes them
type WebhookRouter struct {
	openclawWebhookURL string
	webhookSecret      string
}

func NewRouter(openclawURL, secret string) *WebhookRouter {
	return &WebhookRouter{
		openclawWebhookURL: openclawURL,
		webhookSecret:      secret,
	}
}

func (r *WebhookRouter) handleTelnyxSMS(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload TelnyxSMSPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		log.Printf("Error decoding Telnyx payload: %v", err)
		return
	}

	log.Printf("Received SMS from %s to %s: %s",
		payload.Data.Payload.From,
		payload.Data.Payload.To,
		payload.Data.Payload.Body)

	// Forward to OpenClaw
	if err := r.forwardToOpenClaw("telnyx_sms", payload); err != nil {
		log.Printf("Error forwarding to OpenClaw: %v", err)
		http.Error(w, "Failed to forward", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (r *WebhookRouter) handleHealth(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

func (r *WebhookRouter) forwardToOpenClaw(eventType string, payload interface{}) error {
	// For now, just log it. Later we'll POST to OpenClaw
	log.Printf("Would forward %s event to %s", eventType, r.openclawWebhookURL)
	return nil
}

func main() {
	openclawURL := os.Getenv("OPENCLAW_WEBHOOK_URL")
	if openclawURL == "" {
		openclawURL = "http://localhost:18789/webhook"
	}

	secret := os.Getenv("WEBHOOK_SECRET")

	router := NewRouter(openclawURL, secret)

	http.HandleFunc("/webhook/telnyx/sms", router.handleTelnyxSMS)
	http.HandleFunc("/health", router.handleHealth)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Webhook router starting on port %s", port)
	log.Printf("Forwarding to OpenClaw at: %s", openclawURL)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
