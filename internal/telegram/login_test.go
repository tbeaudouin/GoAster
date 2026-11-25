package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
)

func TestPrivateOwnerLoginStreamsInstructionsWithoutReadinessCheck(t *testing.T) {
	var sent []string
	var sentMu sync.Mutex
	var loginCalls int
	a := &adapter{
		pairing:       &pairing{userID: 42},
		controlSecret: "control-secret",
		apiBase:       "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				return nil, fmt.Errorf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			sentMu.Lock()
			sent = append(sent, payload.Text)
			sentMu.Unlock()
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != controltransport.LoginPath {
				return nil, fmt.Errorf("unexpected agent request %s", r.URL.Path)
			}
			if r.Header.Get(telegramControlAuthorizationHeader) != "control-secret" {
				return nil, errors.New("missing control authentication")
			}
			loginCalls++
			return response(http.StatusOK, `{"type":"output","text":"Open https://auth.example/device\nEnter this one-time code: PRIVATE-CODE\nWaiting...\n"}`+"\n"+`{"type":"complete","text":"OpenAI subscription authentication complete."}`+"\n"), nil
		}),
	}
	a.handle(context.Background(), privateMessage(42, telegramLoginCommand))
	sentMu.Lock()
	got := append([]string(nil), sent...)
	sentMu.Unlock()
	if loginCalls != 1 || len(got) != 2 || !strings.Contains(got[0], "PRIVATE-CODE") || got[1] != "OpenAI subscription authentication complete." {
		t.Fatalf("login calls=%d messages=%q", loginCalls, got)
	}
}

func TestLoginRequiresPrivateOwnerAndExactInvocation(t *testing.T) {
	var sent []string
	var agentCalls int
	a := &adapter{
		pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			agentCalls++
			return response(http.StatusOK, `{"type":"complete"}`+"\n"), nil
		}),
	}
	a.handle(context.Background(), privateMessage(99, telegramLoginCommand))
	a.handle(context.Background(), textMessage(42, -100, "group", telegramLoginCommand))
	a.handle(context.Background(), privateMessage(42, telegramLoginCommand+" extra"))
	if agentCalls != 0 || len(sent) != 2 || sent[0] != "OpenAI login is available only in a private chat." || sent[1] != "Use /login in a private chat without arguments or attachments." {
		t.Fatalf("agent calls=%d messages=%q", agentCalls, sent)
	}
}

func TestStopCancelsPrivateOwnerLogin(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var sent []string
	a := &adapter{
		pairing: &pairing{userID: 42}, controlSecret: "control-secret", apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != controltransport.LoginPath {
				return nil, errors.New("unexpected agent path")
			}
			close(entered)
			<-r.Context().Done()
			close(canceled)
			return nil, r.Context().Err()
		}),
	}
	done := make(chan struct{})
	go func() {
		a.handle(context.Background(), privateMessage(42, telegramLoginCommand))
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("login did not start")
	}
	a.handle(context.Background(), privateMessage(42, telegramStopCommand))
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("login did not receive cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled login did not finish")
	}
	if len(sent) != 1 || sent[0] != telegramStopMessage {
		t.Fatalf("cancellation messages = %q", sent)
	}
}

func TestLoginDoesNotReplayUncertainTransportOutcome(t *testing.T) {
	var sent []string
	loginCalls := 0
	a := &adapter{
		pairing: &pairing{userID: 42}, controlSecret: "control-secret", apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			sent = append(sent, payload.Text)
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			loginCalls++
			return nil, errors.New("connection lost after dispatch")
		}),
	}
	a.handle(context.Background(), privateMessage(42, telegramLoginCommand))
	a.handle(context.Background(), privateMessage(42, telegramLoginCommand))
	if loginCalls != 1 || len(sent) != 2 || !strings.Contains(sent[0], "outcome is unknown") || !strings.Contains(sent[1], "outcome is unknown") {
		t.Fatalf("login calls=%d messages=%q", loginCalls, sent)
	}
}

func TestLoginTransportDiagnosticsRedactPrivateCause(t *testing.T) {
	var logs bytes.Buffer
	restore := diagnostics.SetOutput(&logs)
	defer restore()
	a := &adapter{agent: doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`transport token=LOGIN_TOKEN private device detail`)
	})}
	outcome, err := a.loginAgent(context.Background(), 42)
	if outcome != loginAgentUnknown || err == nil {
		t.Fatalf("login transport outcome=%v error=%v", outcome, err)
	}
	if !strings.Contains(logs.String(), "ERROR operation=telegram.agent.login") || strings.Contains(logs.String(), "LOGIN_TOKEN") || strings.Contains(logs.String(), "private device detail") {
		t.Fatalf("login transport diagnostic = %q", logs.String())
	}
}
