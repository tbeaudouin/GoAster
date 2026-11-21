package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"goaster/internal/diagnostics"
)

func isRestartCommand(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && (fields[0] == "/restart" || strings.HasPrefix(fields[0], "/restart@"))
}

func systemdRestart(ctx context.Context) error {
	if err := systemdCheck(); err != nil {
		return predispatchError{err}
	}
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "goaster-agent.service")
	if err := cmd.Start(); err != nil {
		return predispatchError{err}
	}
	return cmd.Wait()
}

type predispatchError struct{ err error }

func (e predispatchError) Error() string { return "restart process could not start" }
func (e predispatchError) Unwrap() error { return e.err }

var restartReadyTimeout = 15 * time.Second
var restartReadyInterval = time.Second
var systemdCheck = func() error { _, err := os.Stat("/run/systemd/system"); return err }
var systemdAvailable = func() bool { return systemdCheck() == nil }

func (a *adapter) restart(ctx context.Context, chatID int64) {
	a.turnMu.Lock()
	if a.restarting || a.queuedWork > 0 || len(a.activeTurns) > 0 {
		a.turnMu.Unlock()
		a.restartSend(ctx, chatID, "Agent is busy. Use /stop for active work and try again when idle.")
		return
	}
	a.restarting = true
	a.turnMu.Unlock()
	blocked := false
	defer func() {
		if !blocked {
			a.turnMu.Lock()
			a.restarting = false
			a.turnMu.Unlock()
		}
	}()
	run := a.restartService
	if run == nil {
		if !systemdAvailable() {
			a.restartSend(ctx, chatID, "Restart is unavailable: systemd is not running.")
			return
		}
		run = systemdRestart
	}
	guardID := ""
	guardStatus, guardRequestID, guardReason := "absent", "", "unavailable"
	guardCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(guardCtx, http.MethodPost, "http://127.0.0.1:8765/internal/restart-guard", nil)
	if err == nil && a.agent != nil && a.controlSecret != "" {
		req.Header.Set(telegramControlAuthorizationHeader, a.controlSecret)
		resp, requestErr := a.agent.Do(req)
		guardReason = "transport_failure"
		err = requestErr
		if err == nil && resp != nil {
			guardStatus = strconv.Itoa(resp.StatusCode)
			guardRequestID = resp.Header.Get("X-Request-ID")
			guardReason = "http_status"
			if resp.Body != nil {
				resp.Body.Close()
			}
			if resp.StatusCode == http.StatusConflict {
				a.restartSend(ctx, chatID, "Agent is busy. Use /stop for active work and try again when idle.")
				return
			}
			guardID = resp.Header.Get("X-Restart-Guard")
			if resp.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("restart guard returned HTTP %d", resp.StatusCode)
			} else if guardID == "" {
				guardReason = "missing_token"
				err = errors.New("restart guard returned no token")
			}
		} else if err == nil {
			guardReason = "missing_response"
			err = errors.New("restart guard returned no response")
		}
	} else if err == nil {
		err = errors.New("restart guard is unavailable")
	}
	if err != nil {
		diagnostics.Error("telegram.restart.guard", err, diagnostics.Field{Key: "restart_reason", Value: guardReason}, diagnostics.Field{Key: "status", Value: guardStatus}, diagnostics.Field{Key: "request_id", Value: diagnostics.RequestID(guardRequestID)}, diagnostics.Field{Key: "request_id_detail", Value: restartRequestIDDetail(guardRequestID)})
		a.restartSend(ctx, chatID, "Restart unavailable: could not check agent activity.")
		return
	}
	releaseGuard := true
	if guardID != "" {
		defer func() {
			if !releaseGuard {
				return
			}
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer releaseCancel()
			releaseStatus, releaseRequestID, releaseReason := "absent", "", "transport_failure"
			request, requestErr := http.NewRequestWithContext(releaseCtx, http.MethodDelete, "http://127.0.0.1:8765/internal/restart-guard", nil)
			if requestErr == nil {
				request.Header.Set(telegramControlAuthorizationHeader, a.controlSecret)
				request.Header.Set("X-Restart-Guard", guardID)
				response, releaseErr := a.agent.Do(request)
				requestErr = releaseErr
				if response != nil {
					releaseStatus = strconv.Itoa(response.StatusCode)
					releaseRequestID = response.Header.Get("X-Request-ID")
					if response.Body != nil {
						response.Body.Close()
					}
					if response.StatusCode != http.StatusNoContent && requestErr == nil {
						releaseReason = "http_status"
						requestErr = fmt.Errorf("guard release returned HTTP %d", response.StatusCode)
					}
				} else if requestErr == nil {
					releaseReason = "missing_response"
					requestErr = errors.New("guard release missing response")
				}
			}
			if requestErr != nil {
				diagnostics.Error("telegram.restart.release", requestErr, diagnostics.Field{Key: "restart_reason", Value: releaseReason}, diagnostics.Field{Key: "status", Value: releaseStatus}, diagnostics.Field{Key: "request_id", Value: diagnostics.RequestID(releaseRequestID)}, diagnostics.Field{Key: "request_id_detail", Value: restartRequestIDDetail(releaseRequestID)})
			}
		}()
	}

	ackCtx, ackCancel := context.WithTimeout(ctx, 5*time.Second)
	err = a.send(ackCtx, chatID, "Restarting the agent")
	ackCancel()
	if err != nil {
		return
	}
	commitCtx, commitCancel := context.WithTimeout(ctx, 5*time.Second)
	commitStatus, commitRequestID, commitReason := "absent", "", "transport_failure"
	commit, commitErr := http.NewRequestWithContext(commitCtx, http.MethodPut, "http://127.0.0.1:8765/internal/restart-guard", nil)
	if commitErr == nil {
		commit.Header.Set(telegramControlAuthorizationHeader, a.controlSecret)
		commit.Header.Set("X-Restart-Guard", guardID)
		resp, requestErr := a.agent.Do(commit)
		commitErr = requestErr
		if resp != nil {
			commitStatus = strconv.Itoa(resp.StatusCode)
			commitRequestID = resp.Header.Get("X-Request-ID")
			if resp.Body != nil {
				resp.Body.Close()
			}
			if resp.StatusCode != http.StatusNoContent && commitErr == nil {
				commitReason = "http_status"
				commitErr = errors.New("guard commit rejected")
			}
		} else if commitErr == nil {
			commitReason = "missing_response"
			commitErr = errors.New("guard commit missing response")
		}
	}
	commitCancel()
	if commitErr != nil {
		// A lost commit response may have made the guard permanent. Keep both sides closed.
		blocked = true
		releaseGuard = false
		diagnostics.Error("telegram.restart.commit", commitErr, diagnostics.Field{Key: "restart_reason", Value: commitReason}, diagnostics.Field{Key: "status", Value: commitStatus}, diagnostics.Field{Key: "request_id", Value: diagnostics.RequestID(commitRequestID)}, diagnostics.Field{Key: "request_id_detail", Value: restartRequestIDDetail(commitRequestID)})
		a.restartSend(ctx, chatID, "Restart outcome is unknown. Manually verify the agent service status.")
		return
	}
	restartCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	err = run(restartCtx)
	stop()
	if err != nil {
		var before predispatchError
		if errors.As(err, &before) {
			diagnostics.Error("telegram.restart.systemctl", err, diagnostics.Field{Key: "restart_reason", Value: "predispatch_failure"})
			a.restartSend(ctx, chatID, "Agent restart failed. Check the service status.")
			return
		}
		blocked = true
		releaseGuard = false
		exitCode := "absent"
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			exitCode = strconv.Itoa(exitErr.ExitCode())
		}
		diagnostics.Error("telegram.restart.systemctl", err, diagnostics.Field{Key: "restart_reason", Value: "completion_uncertain"}, diagnostics.Field{Key: "exit_code", Value: exitCode})
		a.restartSend(ctx, chatID, "Restart outcome is unknown. Manually verify the agent service status.")
		return
	}
	readyCtx, done := context.WithTimeout(ctx, restartReadyTimeout)
	defer done()
	for {
		if a.agentEndpointAvailable(readyCtx, "/healthz") {
			a.restartSend(ctx, chatID, "Agent ready")
			return
		}
		timer := time.NewTimer(restartReadyInterval)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			diagnostics.Error("telegram.restart.readiness", readyCtx.Err())
			a.restartSend(ctx, chatID, "Agent restarted but is not ready. Check the service status.")
			return
		case <-timer.C:
		}
	}
}

func (a *adapter) restartSend(ctx context.Context, chatID int64, text string) {
	_ = a.send(ctx, chatID, text)
}

func restartRequestIDDetail(id string) string {
	if id == "" {
		return "absent"
	}
	if diagnostics.RequestID(id) == "" {
		return "unrecognized"
	}
	return "present"
}
