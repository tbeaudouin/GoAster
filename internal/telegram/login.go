package telegram

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
)

const loginResponseLimit = 1 << 20

type loginAgentOutcome uint8

const (
	loginAgentUnknown loginAgentOutcome = iota
	loginAgentCompleted
	loginAgentFailed
	loginAgentCanceled
	loginAgentBusy
)

func isLoginCommand(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && (fields[0] == telegramLoginCommand || strings.HasPrefix(fields[0], telegramLoginCommand+"@"))
}

func (a *adapter) runLogin(ctx context.Context, m message) {
	key := telegramTurnKeyForMessage(m)
	a.turnMu.Lock()
	if a.loginBlocked {
		a.turnMu.Unlock()
		a.send(ctx, m.Chat.ID, "OpenAI login outcome is unknown. Check the agent status before trying again.")
		return
	}
	if a.restarting {
		a.turnMu.Unlock()
		a.send(ctx, m.Chat.ID, "Agent restart in progress. Please try again shortly.")
		return
	}
	if a.loginActive {
		a.turnMu.Unlock()
		a.send(ctx, m.Chat.ID, "OpenAI login is already running. Use /stop to cancel it.")
		return
	}
	if a.queuedWork > 0 || len(a.activeTurns) > 0 {
		a.turnMu.Unlock()
		a.send(ctx, m.Chat.ID, "The agent is busy. Finish active work before starting OpenAI login.")
		return
	}
	turnCtx, cancelTurn := context.WithCancel(ctx)
	if a.activeTurns == nil {
		a.activeTurns = make(map[telegramTurnKey][]telegramActiveTurn)
	}
	a.nextTurnID++
	turnID := a.nextTurnID
	a.activeTurns[key] = append(a.activeTurns[key], telegramActiveTurn{id: turnID, userID: m.From.ID, cancel: cancelTurn})
	a.loginActive = true
	a.turnMu.Unlock()

	outcome, _ := a.loginAgent(turnCtx, m.Chat.ID)
	a.turnMu.Lock()
	turns := a.activeTurns[key]
	for index, turn := range turns {
		if turn.id == turnID {
			turns = append(turns[:index], turns[index+1:]...)
			if len(turns) == 0 {
				delete(a.activeTurns, key)
			} else {
				a.activeTurns[key] = turns
			}
			break
		}
	}
	a.loginActive = false
	if outcome == loginAgentUnknown && turnCtx.Err() == nil {
		a.loginBlocked = true
	}
	a.turnMu.Unlock()
	canceled := turnCtx.Err() != nil
	cancelTurn()
	if canceled {
		return
	}
	switch outcome {
	case loginAgentCompleted:
		return
	case loginAgentBusy:
		a.send(ctx, m.Chat.ID, "The agent is busy. Finish active work before starting OpenAI login.")
	case loginAgentFailed:
		a.send(ctx, m.Chat.ID, "OpenAI login did not complete. Check the agent status before trying again.")
	default:
		a.send(ctx, m.Chat.ID, "OpenAI login outcome is unknown. Check the agent status before trying again.")
	}
}

func (a *adapter) loginAgent(ctx context.Context, chatID int64) (loginAgentOutcome, error) {
	if a == nil || a.agent == nil {
		return telegramLoginProtocolFailure(errors.New("remote agent client is unavailable"), "", "")
	}
	requestContext, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://127.0.0.1:8765"+controltransport.LoginPath, nil)
	if err != nil {
		return telegramLoginProtocolFailure(fmt.Errorf("build login request: %w", err), "", "")
	}
	request.Header.Set(telegramControlAuthorizationHeader, a.controlSecret)
	response, err := a.agent.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return loginAgentCanceled, err
		}
		return telegramLoginProtocolFailure(fmt.Errorf("login transport: %w", err), "", "")
	}
	if response == nil || response.Body == nil {
		return telegramLoginProtocolFailure(errors.New("login response body is missing"), "", "")
	}
	defer response.Body.Close()
	requestReference := diagnostics.RequestReference(response.Header.Get(telegramAgentRequestReferenceHeader))
	if response.StatusCode == http.StatusConflict {
		return loginAgentBusy, nil
	}
	if response.StatusCode != http.StatusOK {
		_, err := telegramLoginProtocolFailure(fmt.Errorf("login agent returned status %s", response.Status), response.Status, requestReference)
		return loginAgentFailed, err
	}

	scanner := bufio.NewScanner(io.LimitReader(response.Body, loginResponseLimit))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return telegramLoginProtocolFailure(fmt.Errorf("decode login event: %w", err), response.Status, requestReference)
		}
		switch event.Type {
		case controltransport.LoginOutputEvent:
			if strings.TrimSpace(event.Text) == "" {
				continue
			}
			for _, chunk := range splitTelegramMarkdown(event.Text, 2000) {
				if err := a.send(requestContext, chatID, chunk); err != nil {
					if ctx.Err() != nil {
						return loginAgentCanceled, err
					}
					return loginAgentUnknown, err
				}
			}
		case controltransport.LoginCompleteEvent:
			text := strings.TrimSpace(event.Text)
			if text == "" {
				text = "OpenAI subscription authentication complete."
			}
			for _, chunk := range splitTelegramMarkdown(text, 2000) {
				if err := a.send(requestContext, chatID, chunk); err != nil {
					if ctx.Err() != nil {
						return loginAgentCanceled, err
					}
					return loginAgentUnknown, err
				}
			}
			return loginAgentCompleted, nil
		case controltransport.LoginFailedEvent:
			return loginAgentFailed, nil
		default:
			return telegramLoginProtocolFailure(errors.New("login response contained an unrecognized event"), response.Status, requestReference)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return loginAgentCanceled, err
		}
		return telegramLoginProtocolFailure(fmt.Errorf("read login response: %w", err), response.Status, requestReference)
	}
	return telegramLoginProtocolFailure(errors.New("login response ended without an outcome"), response.Status, requestReference)
}

func telegramLoginProtocolFailure(err error, status, requestReference string) (loginAgentOutcome, error) {
	if err == nil {
		return loginAgentUnknown, nil
	}
	fields := []diagnostics.Field{{Key: "phase", Value: "login"}}
	if strings.TrimSpace(status) != "" {
		fields = append(fields, diagnostics.Field{Key: "status", Value: status})
	}
	if safeReference := diagnostics.RequestReference(requestReference); safeReference != "" {
		fields = append(fields, diagnostics.Field{Key: "request_reference", Value: safeReference})
	}
	diagnostics.Error("telegram.agent.login", err, fields...)
	return loginAgentUnknown, err
}
