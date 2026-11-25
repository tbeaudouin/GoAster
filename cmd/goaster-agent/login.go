package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
)

type agentLoginEvent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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

func handleAgentLogin(w http.ResponseWriter, r *http.Request, controlSecret string, login func(context.Context, func(string, string) error) error) {
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
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	if err := login(ctx, func(url, code string) error {
		if url != "https://auth.openai.com/codex/device" || len(code) < 4 || len(code) > 32 {
			return errors.New("invalid login instructions")
		}
		for _, c := range code {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("invalid login instructions")
			}
		}
		return writeAgentLoginEvent(w, controltransport.LoginOutputEvent, fmt.Sprintf("Open %s\nEnter this one-time code: %s", url, code), flush)
	}); err != nil {
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
