package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
)

const maxLoginOutputChunkBytes = 64 << 10

type agentLoginEvent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type agentLoginOutputWriter struct {
	writer http.ResponseWriter
	flush  func()
}

func (writer agentLoginOutputWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if len(data) > maxLoginOutputChunkBytes {
		return 0, fmt.Errorf("login output chunk exceeds %d bytes", maxLoginOutputChunkBytes)
	}
	// The terminal login output includes the credential path. The paired
	// Telegram flow only needs the device instructions; completion is emitted
	// as a separate safe event below.
	text := string(data)
	if completion := strings.Index(text, "OpenAI subscription authentication complete."); completion >= 0 {
		text = text[:completion]
	}
	if strings.TrimSpace(text) == "" {
		return len(data), nil
	}
	if err := writeAgentLoginEvent(writer.writer, controltransport.LoginOutputEvent, text, writer.flush); err != nil {
		return 0, err
	}
	return len(data), nil
}

func writeAgentLoginEvent(writer http.ResponseWriter, eventType, text string, flush func()) error {
	data, err := json.Marshal(agentLoginEvent{Type: eventType, Text: text})
	if err != nil {
		return fmt.Errorf("encode login event: %w", err)
	}
	data = append(data, '\n')
	written, err := writer.Write(data)
	if err != nil {
		return fmt.Errorf("write login event: %w", err)
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if flush != nil {
		flush()
	}
	return nil
}

func handleAgentLogin(w http.ResponseWriter, r *http.Request, controlSecret string, login func(context.Context, io.Writer) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validControlRequest(r, controlSecret) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if login == nil {
		http.Error(w, "login unavailable", http.StatusServiceUnavailable)
		return
	}
	if !restartWorkMu.TryLock() {
		http.Error(w, "busy", http.StatusConflict)
		return
	}
	defer restartWorkMu.Unlock()

	requestReference := newAgentRequestReference()
	w.Header().Set(agentRequestReferenceHeader, requestReference)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flush := func() {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	flush()
	output := agentLoginOutputWriter{writer: w, flush: flush}
	if err := login(r.Context(), output); err != nil {
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			return
		}
		if eventErr := writeAgentLoginEvent(w, controltransport.LoginFailedEvent, "", flush); eventErr != nil {
			err = errors.Join(err, eventErr)
		}
		diagnostics.Error("agent.login", err,
			diagnostics.Field{Key: "phase", Value: "oauth"},
			diagnostics.Field{Key: "request_reference", Value: requestReference},
		)
		return
	}
	if err := writeAgentLoginEvent(w, controltransport.LoginCompleteEvent, "OpenAI subscription authentication complete.", flush); err != nil {
		if r.Context().Err() == nil {
			diagnostics.Error("agent.login", err,
				diagnostics.Field{Key: "phase", Value: "result_delivery"},
				diagnostics.Field{Key: "request_reference", Value: requestReference},
			)
		}
	}
}
