package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
			Transcription   string `json:"transcription,omitempty"`
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
	Payload               string `json:"payload,omitempty"`
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

// Message represents a stored webhook message in SQLite
type Message struct {
	ID            int       `json:"id"`
	Source        string    `json:"source"`
	CallControlID string    `json:"call_control_id,omitempty"`
	Caller        string    `json:"caller"`
	Callee        string    `json:"callee"`
	Transcription string    `json:"transcription"`
	RecordingURL  string    `json:"recording_url,omitempty"`
	MessageText   string    `json:"message_text"`
	RawPayload    string    `json:"raw_payload"`
	CreatedAt     time.Time `json:"created_at"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	Sent          bool      `json:"sent"`
}

// WebhookRouter handles incoming webhooks and routes them
type WebhookRouter struct {
	db                 *sql.DB
	openclawWebhookURL string
	webhookSecret      string
}

func NewRouter(openclawURL, secret string) *WebhookRouter {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/app/data/messages.db"
	}

	// Ensure directory exists
	dbDir := dbPath[:len(dbPath)-len("/messages.db")]
	if dbDir != "" {
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			log.Printf("Warning: could not create db directory %s: %v", dbDir, err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}

	// Create messages table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			call_control_id TEXT,
			caller TEXT,
			callee TEXT,
			transcription TEXT,
			recording_url TEXT,
			message_text TEXT,
			raw_payload TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			sent_at DATETIME,
			sent BOOLEAN DEFAULT FALSE
		);
		CREATE INDEX IF NOT EXISTS idx_messages_sent ON messages(sent);
		CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at);
	`)
	if err != nil {
		log.Fatalf("Failed to create messages table: %v", err)
	}

	log.Printf("SQLite database initialized at %s", dbPath)

	return &WebhookRouter{
		db:                 db,
		openclawWebhookURL: openclawURL,
		webhookSecret:      secret,
	}
}

func (r *WebhookRouter) storeMessage(source, callControlID, caller, callee, transcription, recordingURL, messageText, rawPayload string) (int64, error) {
	result, err := r.db.Exec(
		`INSERT INTO messages (source, call_control_id, caller, callee, transcription, recording_url, message_text, raw_payload, sent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, FALSE)`,
		source, callControlID, caller, callee, transcription, recordingURL, messageText, rawPayload,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *WebhookRouter) getPendingMessages() ([]Message, error) {
	rows, err := r.db.Query(
		`SELECT id, source, call_control_id, caller, callee, transcription, recording_url, message_text, raw_payload, created_at, sent_at, sent
		 FROM messages WHERE sent = FALSE ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var sentAt sql.NullTime
		err := rows.Scan(
			&m.ID, &m.Source, &m.CallControlID, &m.Caller, &m.Callee,
			&m.Transcription, &m.RecordingURL, &m.MessageText, &m.RawPayload,
			&m.CreatedAt, &sentAt, &m.Sent,
		)
		if err != nil {
			return nil, err
		}
		if sentAt.Valid {
			m.SentAt = &sentAt.Time
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (r *WebhookRouter) markMessagesSent(ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	// Build placeholders and args
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, time.Now().UTC())
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf("UPDATE messages SET sent = TRUE, sent_at = ? WHERE id IN (%s)",
		strings.Join(placeholders, ","),
	)

	_, err := r.db.Exec(query, args...)
	return err
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

	// Store in SQLite instead of forwarding directly
	payloadJSON, _ := json.Marshal(payload)
	_, err := r.storeMessage(
		"telnyx_sms",
		"",
		payload.Data.Payload.From.PhoneNumber,
		toNumber,
		payload.Data.Payload.Body,
		"",
		fmt.Sprintf("SMS from %s: %s", payload.Data.Payload.From.PhoneNumber, payload.Data.Payload.Body),
		string(payloadJSON),
	)
	if err != nil {
		log.Printf("Error storing SMS in SQLite: %v", err)
		http.Error(w, "Failed to store message", http.StatusInternalServerError)
		return
	}

	log.Printf("Stored SMS in SQLite database")
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

func (r *WebhookRouter) handlePendingMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messages, err := r.getPendingMessages()
	if err != nil {
		log.Printf("Error fetching pending messages: %v", err)
		http.Error(w, "Failed to fetch messages", http.StatusInternalServerError)
		return
	}

	// Mark all returned messages as sent
	var ids []int
	for _, m := range messages {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		if err := r.markMessagesSent(ids); err != nil {
			log.Printf("Error marking messages sent: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
		"count":    len(messages),
	})
}

func (r *WebhookRouter) forwardToOpenClaw(eventType string, payload interface{}) error {
	// Build the request body
	var text string
	if m, ok := payload.(map[string]interface{}); ok {
		if msg, ok := m["message"].(string); ok {
			text = msg
		}
	}
	if text == "" {
		text = fmt.Sprintf("Telnyx %s received: %+v", eventType, payload)
	}

	// If target session key is configured, use /hooks/agent endpoint
	targetSession := os.Getenv("TARGET_SESSION_KEY")
	var urlStr string
	var body map[string]interface{}

	if targetSession != "" {
		urlStr = r.openclawWebhookURL + "/agent"
		body = map[string]interface{}{
			"message":    text,
			"sessionKey": targetSession,
			"wakeMode":   "now",
			"deliver":    true,
			"channel":    "last",
		}
		log.Printf("Routing to session via /agent: %s", targetSession)
	} else {
		urlStr = r.openclawWebhookURL + "/wake"
		body = map[string]interface{}{
			"text": text,
			"mode": "now",
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal error: %v", err)
	}

	log.Printf("Forwarding to: %s", urlStr)

	req, err := http.NewRequest("POST", urlStr, bytes.NewBuffer(jsonBody))
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

// fetchTranscription fetches the transcription text from Telnyx API
func (r *WebhookRouter) fetchTranscription(apiKey, recordingID string) (string, error) {
	url := fmt.Sprintf("https://api.telnyx.com/v2/recording_transcriptions?filter[recording_id]=%s", recordingID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Telnyx returned status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			TranscriptionText string `json:"transcription_text"`
			Status            string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Data) == 0 {
		return "", fmt.Errorf("no transcriptions found")
	}

	if result.Data[0].Status != "completed" {
		return "", fmt.Errorf("transcription status: %s", result.Data[0].Status)
	}

	return result.Data[0].TranscriptionText, nil
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
				Command:    "answer",
				WebhookURL: r.getWebhookURL("/webhook/telnyx/voice"),
			}
			if err := r.sendTelnyxCommand(apiKey, callID, command); err != nil {
				log.Printf("Error answering call: %v", err)
			}
		}

	case "call.answered":
		// Play greeting from Charsi
		if apiKey != "" {
			command := TelnyxCallCommand{
				Command:    "speak",
				WebhookURL: r.getWebhookURL("/webhook/telnyx/voice"),
				Payload:    "Hi there, you've reached SparkForge. I'm Charsi, the AI assistant. Please leave your message after the tone and I'll get back to you.",
				Language:   "en-US",
				Voice:      "female",
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
		// Transcription received - store in SQLite instead of sending directly
		transcript := payload.Data.Payload.Text
		if transcript == "" {
			transcript = payload.Data.Payload.Transcription
		}
		recordingURL := payload.Data.Payload.RecordingURL

		// Debug: log full payload to understand structure
		payloadJSON, _ := json.Marshal(payload.Data.Payload)
		log.Printf("DEBUG Transcription payload: %s", string(payloadJSON))
		recordingID := payload.Data.Payload.RecordingID
		// If no transcript in webhook, try to fetch via API
		if transcript == "" && recordingID != "" {
			apiKey := os.Getenv("TELNYX_API_KEY")
			fetchedTranscript, err := r.fetchTranscription(apiKey, recordingID)
			if err != nil {
				log.Printf("Could not fetch transcription via API: %v", err)
			} else if fetchedTranscript != "" {
				transcript = fetchedTranscript
				log.Printf("Fetched transcription via API: %s", transcript)
			}
		}

		// Get the call info
		var caller string
		if call, ok := callStore.calls[callID]; ok {
			caller = call.From
			call.Transcription = transcript
		}

		// Build message for Charsi with the transcription
		message := fmt.Sprintf("📞 Voicemail from %s\n\n📝 Transcription:\n%s\n\n🔊 Recording: %s",
			caller, transcript, recordingURL)

		// Store in SQLite instead of forwarding directly
		rawPayloadJSON, _ := json.Marshal(payload)
		_, err := r.storeMessage(
			"telnyx_voicemail",
			callID,
			caller,
			to,
			transcript,
			recordingURL,
			message,
			string(rawPayloadJSON),
		)
		if err != nil {
			log.Printf("Error storing voicemail in SQLite: %v", err)
		} else {
			log.Printf("Stored voicemail transcription in SQLite database")
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
	http.HandleFunc("/messages/pending", router.handlePendingMessages)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Webhook router starting on port %s", port)
	log.Printf("Forwarding to OpenClaw at: %s", openclawURL)
	log.Printf("Pending messages endpoint: /messages/pending")

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
