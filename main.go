package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// TelnyxSMSPayload represents the webhook payload from Telnyx
type TelnyxSMSPayload struct {
	Data struct {
		Payload struct {
			To []struct {
				PhoneNumber string `json:"phone_number"`
				Status      string `json:"status,omitempty"`
			} `json:"to"`
			From struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"from"`
			Body      string `json:"text"`
			MessageID string `json:"id"`
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
			CallControlID   string `json:"call_control_id"`
			ConnectionID    string `json:"connection_id"`
			CallLegID       string `json:"call_leg_id"`
			CallSessionID   string `json:"call_session_id"`
			ClientState     string `json:"client_state"`
			From            string `json:"from"`
			To              string `json:"to"`
			Direction       string `json:"direction"`
			State           string `json:"state"`
			AudioURL        string `json:"audio_url,omitempty"`
			Text            string `json:"text,omitempty"`
			Status          string `json:"status,omitempty"`
			DTMF            string `json:"dtmf,omitempty"`
			RecordingURL    string `json:"recording_url,omitempty"`
			RecordingID     string `json:"recording_id,omitempty"`
			TranscriptionID string `json:"transcription_id,omitempty"`
		} `json:"payload"`
	} `json:"data"`
}

// TelnyxCallCommand represents a call control command to send to Telnyx
// Note: call_control_id is NOT included in JSON body - it goes in the URL path
// Command is in the URL path (/actions/{command}), not the body, so json:"-"
type TelnyxCallCommand struct {
	Command               string `json:"-"`
	WebhookURL            string `json:"webhook_url,omitempty"`
	AudioURL              string `json:"audio_url,omitempty"`
	Text                  string `json:"text,omitempty"`
	Language              string `json:"language,omitempty"`
	Voice                 string `json:"voice,omitempty"`
	Format                string `json:"format,omitempty"`
	Channels              string `json:"channels,omitempty"`
	PlayBeep              bool   `json:"play_beep,omitempty"`
	MaxLength             int    `json:"max_length,omitempty"`
	RecordingTrack        string `json:"recording_track,omitempty"`
	To                    string `json:"to,omitempty"`
	From                  string `json:"from,omitempty"`
	ClientState           string `json:"client_state,omitempty"`
	Transcription         bool   `json:"transcription,omitempty"`
	TranscriptionEngine   string `json:"transcription_engine,omitempty"`
	TranscriptionLanguage string `json:"transcription_language,omitempty"`
}

// VoiceCallStore tracks active calls and their recordings
type VoiceCallStore struct {
	calls map[string]*VoiceCall // keyed by call_control_id
}

type VoiceCall struct {
	From          string
	To            string
	RecordingURL  string
	Transcription string
	StartedAt     time.Time
}

var callStore = &VoiceCallStore{calls: make(map[string]*VoiceCall)}

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
func (r *WebhookRouter) sendTelnyxCommand(apiKey string, callControlID string, command TelnyxCallCommand) error {
	jsonBody, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}

	// Telnyx API uses /actions/{command} endpoint format
	url := fmt.Sprintf("https://api.telnyx.com/v2/calls/%s/actions/%s", url.PathEscape(callControlID), command.Command)
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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("Telnyx returned status %d", resp.StatusCode)
	}

	log.Printf("Sent command %s to Telnyx for call %s", command.Command, callControlID)
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
	callID := payload.Data.Payload.CallControlID
	from := payload.Data.Payload.From
	to := payload.Data.Payload.To

	log.Printf("Received voice event %s: from %s to %s (call_control_id: %s)",
		eventType, from, to, callID)

	// Get API key from environment
	apiKey := os.Getenv("TELNYX_API_KEY")
	if apiKey == "" {
		log.Printf("Warning: TELNYX_API_KEY not set")
	}

	// Handle different voice events
	switch eventType {
	case "call.initiated":
		// Store call info
		callStore.calls[callID] = &VoiceCall{
			From:      from,
			To:        to,
			StartedAt: time.Now(),
		}

		// Answer incoming call
		if apiKey != "" && payload.Data.Payload.Direction == "incoming" {
			command := TelnyxCallCommand{
				Command:       "answer",
				WebhookURL:    r.getWebhookURL("/webhook/telnyx/voice"),
			}
			if err := r.sendTelnyxCommand(apiKey, callID, command); err != nil {
				log.Printf("Error answering call: %v", err)
			}
		}

	case "call.answered":
		// Play greeting from Charsi
		if apiKey != "" {
			command := TelnyxCallCommand{
				Command:       "speak",
				WebhookURL:    r.getWebhookURL("/webhook/telnyx/voice"),
				Text:          "Hi there, you've reached SparkForge. I'm Charsi, the AI assistant. Please leave your message after the tone and I'll get back to you.",
				Language:      "en-US",
				Voice:         "female",
			}
			if err := r.sendTelnyxCommand(apiKey, callID, command); err != nil {
				log.Printf("Error speaking greeting: %v", err)
			}
		}

	case "call.speak.ended":
		// Start recording after greeting, with transcription enabled
		if apiKey != "" {
			command := TelnyxCallCommand{
				Command:               "record_start",
				WebhookURL:            r.getWebhookURL("/webhook/telnyx/voice"),
				Format:                "wav",
				Channels:              "single",
				PlayBeep:              true,
				Transcription:         true,
				TranscriptionEngine:   "B", // Telnyx engine
				TranscriptionLanguage: "en-US",
			}
			if err := r.sendTelnyxCommand(apiKey, callID, command); err != nil {
				log.Printf("Error starting recording: %v", err)
			}
		}

	case "call.hangup":
		// Call ended - log it
		log.Printf("Call ended: %s -> %s", from, to)

	case "call.recording.saved":
		// Recording saved - store URL
		if call, ok := callStore.calls[callID]; ok {
			call.RecordingURL = payload.Data.Payload.RecordingURL
			log.Printf("Recording saved for call %s: %s", callID, call.RecordingURL)
		}

		// Note: Transcription comes separately via call.recording.transcription.saved

	case "call.recording.transcription.saved":
		// Transcription received - send to OpenClaw
		transcript := payload.Data.Payload.Text
		recordingURL := payload.Data.Payload.RecordingURL

		// Get the call info
		var caller string
		if call, ok := callStore.calls[callID]; ok {
			caller = call.From
			call.Transcription = transcript
		}

		// Build message for Charsi with the transcription
		message := fmt.Sprintf("📞 Voicemail from %s\n\n📝 Transcription:\n%s\n\n🔊 Recording: %s",
			caller, transcript, recordingURL)

		// Forward to OpenClaw with a clear summary
		notification := map[string]interface{}{
			"from":            caller,
			"to":              to,
			"transcription":   transcript,
			"recording_url":   recordingURL,
			"call_control_id": callID,
			"message":         message,
		}

		if err := r.forwardToOpenClaw("telnyx_voicemail_transcribed", notification); err != nil {
			log.Printf("Error forwarding transcription to OpenClaw: %v", err)
		}

		// Clean up the call from store after we have the transcription
		delete(callStore.calls, callID)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// getWebhookURL returns the full URL for a webhook path
func (r *WebhookRouter) getWebhookURL(path string) string {
	// This should be the public-facing webhook URL
	baseURL := os.Getenv("PUBLIC_WEBHOOK_URL")
	if baseURL == "" {
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
