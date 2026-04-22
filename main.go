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

// TelnyxVoicePayload represents a voice webhook payload from Telnyx
type TelnyxVoicePayload struct {
	Data struct {
		RecordType string `json:"record_type"`
		EventType  string `json:"event_type"`
		ID         string `json:"id"`
		OccurredAt string `json:"occurred_at"`
		Payload    struct {
			CallControlID  string `json:"call_control_id"`
			ConnectionID   string `json:"connection_id"`
			CallLegID      string `json:"call_leg_id"`
			CallSessionID  string `json:"call_session_id"`
			ClientState    string `json:"client_state"`
			From           string `json:"from"`
			To             string `json:"to"`
			Direction      string `json:"direction"`
			State          string `json:"state"`
			AudioURL       string `json:"audio_url,omitempty"`
			Text           string `json:"text,omitempty"`
			Status         string `json:"status,omitempty"`
			DTMF           string `json:"dtmf,omitempty"`
			RecordingURL   string `json:"recording_url,omitempty"`
			RecordingID    string `json:"recording_id,omitempty"`
		} `json:"payload"`
	} `json:"data"`
}

// TelnyxCallCommand represents a call control command to send to Telnyx
type TelnyxCallCommand struct {
	Command      string `json:"command"`
	CallControlID string `json:"call_control_id,omitempty"`
	WebhookURL   string `json:"webhook_url,omitempty"`
	AudioURL     string `json:"audio_url,omitempty"`
	Text         string `json:"text,omitempty"`
	Language     string `json:"language,omitempty"`
	Voice        string `json:"voice,omitempty"`
	Record       string `json:"record,omitempty"`
	RecordFormat string `json:"record_format,omitempty"`
	To           string `json:"to,omitempty"`
	From         string `json:"from,omitempty"`
	ClientState  string `json:"client_state,omitempty"`
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
		"text": fmt.Sprintf("Telnyx %s received: %+v", eventType, payload),
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
	
	// Log token (first 10 chars for debugging)
	if len(r.webhookSecret) > 10 {
		log.Printf("Using token: %s... (len=%d)", r.webhookSecret[:10], len(r.webhookSecret))
	} else {
		log.Printf("Token too short or empty: len=%d", len(r.webhookSecret))
	}
	
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

// sendTelnyxCommand sends a call control command to Telnyx API
func (r *WebhookRouter) sendTelnyxCommand(apiKey string, command TelnyxCallCommand) error {
	jsonBody, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}

	url := fmt.Sprintf("https://api.telnyx.com/v2/calls/%s/actions", command.CallControlID)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("request create error: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http post error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telnyx returned status %d", resp.StatusCode)
	}

	log.Printf("Sent command %s to Telnyx for call %s", command.Command, command.CallControlID)
	return nil
}

// handleTelnyxVoice handles incoming Telnyx voice webhooks
func (r *WebhookRouter) handleTelnyxVoice(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload TelnyxVoicePayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		log.Printf("Error decoding Telnyx voice payload: %v", err)
		return
	}

	eventType := payload.Data.EventType
	log.Printf("Received voice event %s: from %s to %s (call_control_id: %s)",
		eventType,
		payload.Data.Payload.From,
		payload.Data.Payload.To,
		payload.Data.Payload.CallControlID)

	// Get API key from environment
	apiKey := os.Getenv("TELNYX_API_KEY")
	if apiKey == "" {
		log.Printf("Warning: TELNYX_API_KEY not set")
	}

	// Handle different voice events
	switch eventType {
	case "call.initiated":
		// Answer incoming call
		if apiKey != "" && payload.Data.Payload.Direction == "incoming" {
			command := TelnyxCallCommand{
				Command:       "answer",
				CallControlID: payload.Data.Payload.CallControlID,
				WebhookURL:    r.getWebhookURL("/webhook/telnyx/voice"),
			}
			if err := r.sendTelnyxCommand(apiKey, command); err != nil {
				log.Printf("Error answering call: %v", err)
			}
		}

	case "call.answered":
		// Play greeting and start recording
		if apiKey != "" {
			// Speak a greeting
			command := TelnyxCallCommand{
				Command:       "speak",
				CallControlID: payload.Data.Payload.CallControlID,
				WebhookURL:    r.getWebhookURL("/webhook/telnyx/voice"),
				Text:          "Hello, you've reached SparkForge. Please leave a message after the tone.",
				Language:      "en-US",
				Voice:         "female",
			}
			if err := r.sendTelnyxCommand(apiKey, command); err != nil {
				log.Printf("Error speaking greeting: %v", err)
			}
		}

	case "call.speak.ended":
		// Start recording after greeting
		if apiKey != "" {
			command := TelnyxCallCommand{
				Command:       "record_start",
				CallControlID: payload.Data.Payload.CallControlID,
				WebhookURL:    r.getWebhookURL("/webhook/telnyx/voice"),
				RecordFormat:  "wav",
			}
			if err := r.sendTelnyxCommand(apiKey, command); err != nil {
				log.Printf("Error starting recording: %v", err)
			}
		}

	case "call.hangup":
		// Call ended - forward notification to OpenClaw
		log.Printf("Call ended: %s -> %s", payload.Data.Payload.From, payload.Data.Payload.To)
		if payload.Data.Payload.RecordingURL != "" {
			log.Printf("Recording available at: %s", payload.Data.Payload.RecordingURL)
		}

	case "call.recording.saved":
		// Recording saved - notify OpenClaw
		log.Printf("Recording saved for call %s: %s", payload.Data.Payload.CallControlID, payload.Data.Payload.RecordingURL)
	}

	// Forward event to OpenClaw
	if err := r.forwardToOpenClaw("telnyx_voice_"+eventType, payload); err != nil {
		log.Printf("Error forwarding to OpenClaw: %v", err)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// getWebhookURL returns the full URL for a webhook path
func (r *WebhookRouter) getWebhookURL(path string) string {
	// This should be the public-facing webhook URL
	// For now, we'll use an environment variable or construct it
	baseURL := os.Getenv("PUBLIC_WEBHOOK_URL")
	if baseURL == "" {
		// Default to the same host as the router is running on
		// In production, this should be set to the external URL (e.g., https://hooks.sparkforge.io)
		return "https://hooks.sparkforge.io" + path
	}
	return baseURL + path
}

func main() {
	openclawURL := os.Getenv("OPENCLAW_WEBHOOK_URL")
	if openclawURL == "" {
		openclawURL = "http://localhost:18789/webhook"
	}

	secret := os.Getenv("WEBHOOK_SECRET")

	router := NewRouter(openclawURL, secret)

	http.HandleFunc("/webhook/telnyx/sms", router.handleTelnyxSMS)
	http.HandleFunc("/webhook/telnyx/voice", router.handleTelnyxVoice)
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
