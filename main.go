package main

import (
	"bytes"
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
			To          []struct {
				PhoneNumber string `json:"phone_number"`
				Status      string `json:"status,omitempty"`
			} `json:"to"`
			From        struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"from"`
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

	// Get first recipient if array
	var toNumber string
	if len(payload.Data.Payload.To) > 0 {
		toNumber = payload.Data.Payload.To[0].PhoneNumber
	}
	
	log.Printf("Received SMS from %s to %s: %s",
		payload.Data.Payload.From.PhoneNumber,
		toNumber,
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
	// Build the request body
	body := map[string]interface{}{
		"text": fmt.Sprintf("Telnyx SMS received: %+v", payload),
		"mode": "now",
	}
	
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}
	
	// Create the request
	url := r.openclawWebhookURL + "/wake"
	log.Printf("Forwarding to: %s", url)
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("request create error: %v", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.webhookSecret)
	
	// Send the request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http post error: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenClaw returned status %d", resp.StatusCode)
	}
	
	log.Printf("Forwarded %s event to OpenClaw successfully", eventType)
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
