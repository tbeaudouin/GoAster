package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/command"
)

const (
	backgroundToolLegacyPTYKeys  = "up,down,left,right,y,N,comma"
	backgroundToolLegacyPTYBytes = "\x1b[A\x1b[B\x1b[D\x1b[CyN,"
)

func TestBackgroundCommandToolHelper(t *testing.T) {
	marker := false
	ptyMarker := false
	ptyByteCount := 0
	for index, argument := range os.Args {
		if argument == "--goaster-background-tool-helper" {
			marker = true
		}
		if argument == "--goaster-background-tool-pty-helper" {
			ptyMarker = true
			if index+1 < len(os.Args) {
				ptyByteCount, _ = strconv.Atoi(os.Args[index+1])
			}
		}
	}
	if !marker {
		return
	}
	if ptyMarker {
		if ptyByteCount <= 0 {
			os.Exit(2)
		}
		stty := exec.Command("/bin/stty", "-icanon", "min", "1", "time", "0", "-echo")
		stty.Stdin = os.Stdin
		stty.Stdout = os.Stdout
		stty.Stderr = os.Stderr
		if err := stty.Run(); err != nil {
			os.Exit(2)
		}
		fmt.Print("tool-pty-ready")
		received := make([]byte, ptyByteCount)
		if _, err := io.ReadFull(os.Stdin, received); err != nil {
			os.Exit(2)
		}
		fmt.Printf("tool-pty-received:%x", received)
		os.Exit(0)
	}
	fmt.Print("tool-ready")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return
	}
	fmt.Printf("tool-received:%s", strings.TrimSuffix(line, "\n"))
}

type backgroundCommandToolRegistrar struct {
	register func(Tool) error
}

func (registrar backgroundCommandToolRegistrar) RegisterTool(tool Tool) error {
	return registrar.register(tool)
}

func exerciseBackgroundCommandTool(t testing.TB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{{
		Name: "preview", Description: "Local preview", Executable: executable, Args: []string{"-test.run=^$"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	registered := Tool{}
	registrar := backgroundCommandToolRegistrar{register: func(tool Tool) error {
		registered = tool
		return nil
	}}
	if err := RegisterBackgroundCommandTool(registrar, manager); err != nil {
		t.Fatalf("RegisterBackgroundCommandTool() error = %v", err)
	}
	if registered.Name != backgroundCommandToolName || registered.Handler == nil || registered.Parameters["additionalProperties"] != false {
		t.Fatalf("registered tool = %+v", registered)
	}
	if !strings.Contains(registered.Description, "not secret-safe") ||
		!strings.Contains(registered.Description, "empty strings in fields unused by an action are accepted as absent") ||
		!strings.Contains(registered.Description, "Interactive pipe or PTY input is opt-in") ||
		!strings.Contains(registered.Description, "input/write and send-keys are only for non-secret data or terminal keys") ||
		!strings.Contains(registered.Description, "never send passwords, passphrases, tokens, API keys, one-time codes, private keys, or any other secret") ||
		!strings.Contains(registered.Description, "never pasted text") ||
		!strings.Contains(registered.Description, "PTY echo") ||
		!strings.Contains(registered.Description, "github-login") ||
		!strings.Contains(registered.Description, "printable character per comma-delimited token") ||
		!strings.Contains(registered.Description, "use named comma for a literal comma") ||
		!strings.Contains(registered.Description, "backward-compatible keys_only setting only disables input/write; it never narrows send-keys") ||
		!strings.Contains(registered.Description, "Every eligible PTY accepts the same supported named terminal keys") ||
		!strings.Contains(registered.Description, "keeps input/write unavailable") ||
		!strings.Contains(registered.Description, "for the bundled github-login profile only, never use input/write or enter user secrets") ||
		!strings.Contains(registered.Description, "official verification URL and one-time code") ||
		!strings.Contains(registered.Description, "Never relay passwords, tokens, credentials, or other secrets") ||
		strings.Contains(registered.Description, "This generic tool is not secret-safe: never use input") {
		t.Fatalf("background tool description omits the non-secret boundary: %q", registered.Description)
	}
	properties, ok := registered.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool properties = %#v", registered.Parameters["properties"])
	}
	for _, field := range []string{"command", "executable", "args", "cwd", "shell"} {
		if _, exists := properties[field]; exists {
			t.Fatalf("background tool accepts %s: %+v", field, properties)
		}
	}
	listed, err := registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"name":"preview"`) || !strings.Contains(listed, `"description":"Local preview"`) || strings.Contains(listed, executable) || strings.Contains(listed, "-test.run") {
		t.Fatalf("list result = %q, error = %v", listed, err)
	}
	for _, arguments := range []string{
		`{"action":"start","name":"preview","command":"echo injected"}`,
		`{"action":"start","name":"preview","executable":"/bin/echo"}`,
		`{"action":"start","name":"preview","args":["injected"]}`,
		`{"action":"start","name":"preview","cwd":"/tmp"}`,
		`{"action":"start","name":"preview","shell":"echo injected"}`,
		`{"action":"start","name":"preview","id":"bg-000001"}`,
		`{"action":"start","name":"missing"}`,
		`{"action":"unknown"}`,
	} {
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"`) || strings.Contains(result, "started") {
			t.Fatalf("arguments %s returned %q, error = %v; want a rejected request", arguments, result, err)
		}
	}
	started, err := registered.Handler(context.Background(), `{"action":"start","name":"preview"}`)
	if err != nil || !strings.Contains(started, `"status":"started"`) || !strings.Contains(started, `"id":"bg-000001"`) {
		t.Fatalf("allowlisted background start failed: %q, %v", started, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err = registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"status":"configuration_unavailable"`) || !strings.Contains(listed, `"id":"bg-000001"`) {
		t.Fatalf("list with invalid config = %q, error = %v; want configuration error with the existing session", listed, err)
	}
	for _, arguments := range []string{
		`{"action":"list","action":"start"}`,
		`{"action":"list","command":"echo injected"}`,
		`{"action":"start","name":"preview","pty":true}`,
		`{"action":"list"} {}`,
		`[]`,
	} {
		if _, err := parseBackgroundCommandToolArguments(arguments); err == nil {
			t.Fatalf("parseBackgroundCommandToolArguments(%s) error = nil", arguments)
		}
	}
}

func TestBackgroundCommandToolSupportsAllowlistedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{
		{
			Name: "interactive", Executable: executable,
			Args: []string{"-test.run=^TestBackgroundCommandToolHelper$", "--", "--goaster-background-tool-helper"}, Interactive: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	registered := Tool{}
	registrar := backgroundCommandToolRegistrar{register: func(tool Tool) error {
		registered = tool
		return nil
	}}
	if err := RegisterBackgroundCommandTool(registrar, manager); err != nil {
		t.Fatal(err)
	}

	listed, err := registered.Handler(context.Background(), `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"interactive":true`) {
		t.Fatalf("interactive list = %q, error = %v", listed, err)
	}
	started, err := registered.Handler(context.Background(), `{"action":"start","name":"interactive"}`)
	if err != nil || !strings.Contains(started, `"status":"started"`) || !strings.Contains(started, `"id":"bg-000001"`) {
		t.Fatalf("interactive command did not start: %q, %v", started, err)
	}
	for _, action := range []string{"input", "output", "stop"} {
		arguments := fmt.Sprintf(`{"action":%q,"id":"bg-000001"}`, action)
		if action == "input" {
			arguments = `{"action":"input","id":"bg-000001","data":"hello"}`
		}
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || strings.Contains(result, `"status":"not_found"`) {
			t.Fatalf("started session action %s failed: %q, %v", action, result, err)
		}
	}

	for _, arguments := range []string{
		`{"action":"input","id":"bg-999999","data":"x"}`,
		`{"action":"start","name":"interactive","pty":true}`,
		`{"action":"input","id":"bg-000001","data":"x","keys":"enter"}`,
	} {
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"`) || strings.Contains(result, "input_queued") {
			t.Fatalf("authorization/input boundary for %s returned %q, error = %v", arguments, result, err)
		}
	}
}

func TestBackgroundCommandToolKeysOnlyDoesNotRestrictSendKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{
		{
			Name: "legacy-keys", Executable: executable,
			Args: []string{"-test.run=^TestBackgroundCommandToolHelper$", "--", "--goaster-background-tool-helper", "--goaster-background-tool-pty-helper", strconv.Itoa(len(backgroundToolLegacyPTYBytes))},
			PTY:  true, KeysOnly: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	registered := Tool{}
	registrar := backgroundCommandToolRegistrar{register: func(tool Tool) error {
		registered = tool
		return nil
	}}
	if err := RegisterBackgroundCommandTool(registrar, manager); err != nil {
		t.Fatal(err)
	}
	started, err := registered.Handler(context.Background(), `{"action":"start","name":"legacy-keys"}`)
	if err != nil || !strings.Contains(started, `"status":"started"`) || !strings.Contains(started, `"id":"bg-000001"`) {
		t.Fatalf("legacy keys-only start = %q, error = %v", started, err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		result, err := registered.Handler(context.Background(), `{"action":"output","id":"bg-000001"}`)
		if err != nil {
			t.Fatalf("legacy keys-only PTY readiness output = %q, error = %v", result, err)
		}
		if strings.Contains(result, `"stdout":"tool-pty-ready"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy keys-only PTY did not become ready: %q", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, action := range []string{"input", "write"} {
		arguments := fmt.Sprintf(`{"action":%q,"id":"bg-000001","data":"blocked"}`, action)
		result, err := registered.Handler(context.Background(), arguments)
		if err != nil || !strings.Contains(result, `"status":"input_unavailable"`) || strings.Contains(result, `"status":"input_queued"`) {
			t.Fatalf("legacy keys-only %s result = %q, error = %v", action, result, err)
		}
	}
	result, err := registered.Handler(context.Background(), fmt.Sprintf(`{"action":"send-keys","id":"bg-000001","keys":%q}`, backgroundToolLegacyPTYKeys))
	if err != nil || !strings.Contains(result, `"status":"keys_queued"`) {
		t.Fatalf("legacy keys-only send-keys result = %q, error = %v", result, err)
	}
	wantOutput := `"stdout":"tool-pty-readytool-pty-received:` + fmt.Sprintf("%x", []byte(backgroundToolLegacyPTYBytes)) + `"`
	deadline = time.Now().Add(6 * time.Second)
	for {
		result, err = registered.Handler(context.Background(), `{"action":"output","id":"bg-000001"}`)
		if err != nil {
			t.Fatalf("legacy keys-only PTY output = %q, error = %v", result, err)
		}
		if strings.Contains(result, wantOutput) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy keys-only PTY received output = %q, want exact %q", result, wantOutput)
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(6 * time.Second)
	for {
		result, err = registered.Handler(context.Background(), `{"action":"status","id":"bg-000001"}`)
		if err != nil {
			t.Fatalf("legacy keys-only PTY status = %q, error = %v", result, err)
		}
		if strings.Contains(result, `"state":"exited"`) {
			if !strings.Contains(result, `"exit_code":0`) || strings.Contains(result, `"input_failed":true`) {
				t.Fatalf("legacy keys-only PTY unsuccessful completion = %q", result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy keys-only PTY did not complete: %q", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestParseBackgroundCommandToolArgumentsReasons(t *testing.T) {
	large := strings.Repeat("x", command.MaxSessionInputBytes+1)
	tests := []struct {
		name       string
		arguments  string
		wantReason string
		unwanted   string
	}{
		{name: "malformed JSON", arguments: `{not-json`, wantReason: backgroundCommandReasonMalformedJSON},
		{name: "non-object array", arguments: `[]`, wantReason: backgroundCommandReasonObjectRequired},
		{name: "non-object null", arguments: `null`, wantReason: backgroundCommandReasonObjectRequired},
		{name: "duplicate field", arguments: `{"action":"list","action":"start"}`, wantReason: backgroundCommandReasonDuplicateField},
		{name: "unsupported field", arguments: `{"action":"list","private_field":"PRIVATE_VALUE"}`, wantReason: backgroundCommandReasonUnsupportedField, unwanted: "private_field"},
		{name: "known field null", arguments: `{"action":null}`, wantReason: backgroundCommandReasonFieldType},
		{name: "known field wrong type", arguments: `{"action":true}`, wantReason: backgroundCommandReasonFieldType},
		{name: "trailing data", arguments: `{"action":"list"}{}`, wantReason: backgroundCommandReasonTrailingData},
		{name: "missing action", arguments: `{}`, wantReason: backgroundCommandReasonActionRequired},
		{name: "oversized input", arguments: `{"action":"input","id":"session-id","data":"` + large + `"}`, wantReason: backgroundCommandReasonInputTooLarge, unwanted: large[:128]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseBackgroundCommandToolArguments(test.arguments)
			if err == nil {
				t.Fatal("parseBackgroundCommandToolArguments() error = nil")
			}
			argumentErr, ok := err.(backgroundCommandToolArgumentError)
			if !ok || argumentErr.reason != test.wantReason {
				t.Fatalf("parse error = %#v, want reason %q", err, test.wantReason)
			}
			if test.unwanted != "" && strings.Contains(argumentErr.Error(), test.unwanted) {
				t.Fatalf("parse error exposed %q: %q", test.unwanted, argumentErr.Error())
			}
		})
	}
}

func TestBackgroundCommandToolActionValidationResults(t *testing.T) {
	tests := []struct {
		name       string
		arguments  string
		wantReason string
		wantText   string
	}{
		{
			name:       "list rejects nonempty data",
			arguments:  `{"action":"list","data":"blocked"}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "start rejects nonempty keys",
			arguments:  `{"action":"start","name":"preview","keys":"enter"}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   `{"action":"start","name":"configured-name"}`,
		},
		{
			name:       "start requires name",
			arguments:  `{"action":"start","name":"","id":"","data":"","keys":""}`,
			wantReason: backgroundCommandReasonMissingName,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "start requires non-whitespace name",
			arguments:  `{"action":"start","name":" \t\n"}`,
			wantReason: backgroundCommandReasonMissingName,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "session action requires id",
			arguments:  `{"action":"status","name":"","id":"","data":"","keys":""}`,
			wantReason: backgroundCommandReasonMissingID,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "session action requires non-whitespace id",
			arguments:  `{"action":"status","id":" \t\n"}`,
			wantReason: backgroundCommandReasonMissingID,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "input requires data",
			arguments:  `{"action":"input","id":"session-id"}`,
			wantReason: backgroundCommandReasonMissingData,
			wantText:   `"data"`,
		},
		{
			name:       "send keys requires keys",
			arguments:  `{"action":"send-keys","id":"session-id","keys":" "}`,
			wantReason: backgroundCommandReasonMissingKeys,
			wantText:   `"keys":"enter"`,
		},
		{
			name:       "status rejects nonempty data",
			arguments:  `{"action":"status","id":"session-id","data":"blocked"}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   "name, data, and keys must be empty",
		},
		{
			name:       "unsupported action is not echoed",
			arguments:  `{"action":"private-unknown-action"}`,
			wantReason: backgroundCommandReasonUnsupportedAction,
			wantText:   backgroundCommandActionsMessage,
		},
		{
			name:       "missing action despite other placeholders",
			arguments:  `{"action":"","name":"","id":"","data":"","keys":""}`,
			wantReason: backgroundCommandReasonActionRequired,
			wantText:   `{"action":"list"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executeBackgroundCommandTool(context.Background(), test.arguments, command.BackgroundCommandManager{})
			if err != nil {
				t.Fatalf("executeBackgroundCommandTool() error = %v", err)
			}
			escapedWantText := strings.ReplaceAll(test.wantText, `"`, `\"`)
			if !strings.Contains(result, `"status":"invalid_request"`) ||
				!strings.Contains(result, `"reason":"`+test.wantReason+`"`) ||
				!strings.Contains(result, escapedWantText) {
				t.Fatalf("result = %q, want status/reason/text %q/%q/%q", result, "invalid_request", test.wantReason, test.wantText)
			}
			if strings.Contains(result, "private-unknown-action") {
				t.Fatalf("result echoed unsupported action: %q", result)
			}
		})
	}
}

func TestBackgroundCommandToolInvalidCallCanBeCorrected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	data, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()

	invalid, err := executeBackgroundCommandTool(context.Background(), `{"action":"list","keys":"blocked"}`, manager)
	if err != nil || !strings.Contains(invalid, `"reason":"forbidden_fields"`) {
		t.Fatalf("invalid call = %q, error = %v", invalid, err)
	}
	corrected, err := executeBackgroundCommandTool(context.Background(), `{"action":"list"}`, manager)
	if err != nil || !strings.Contains(corrected, `"status":"ok"`) || !strings.Contains(corrected, `"commands":[]`) || !strings.Contains(corrected, `"sessions":[]`) {
		t.Fatalf("corrected call = %q, error = %v", corrected, err)
	}
}

func TestBackgroundCommandToolFailureResultsAreSafeAndDiagnosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-background-commands.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()

	var result string
	log := captureChatDiagnostics(t, func() {
		var err error
		result, err = executeBackgroundCommandTool(context.Background(), `{"action":"list"}`, manager)
		if err != nil {
			t.Fatalf("executeBackgroundCommandTool() error = %v", err)
		}
	})
	for _, want := range []string{
		`"status":"configuration_unavailable"`,
		`"reason":"configuration_unavailable"`,
		`"next_action":"{\"action\":\"list\"}"`,
		"owner-managed background command configuration is unavailable",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("result = %q, missing %q", result, want)
		}
	}
	if strings.Contains(result, path) || strings.Contains(result, "background command configuration is malformed") {
		t.Fatalf("result exposed path or raw manager detail: %q", result)
	}
	if strings.Count(log, "ERROR operation=chat.background_command") != 1 || !strings.Contains(log, `phase="model_command"`) || !strings.Contains(log, `code="unavailable"`) {
		t.Fatalf("diagnostics = %q, want one bounded background-manager record", log)
	}
	if strings.Contains(log, path) {
		t.Fatalf("diagnostics exposed configuration path: %q", log)
	}
}

func TestCodexProviderPassesBackgroundValidationResultToNextRequest(t *testing.T) {
	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			writeCodexFunctionCall(t, w, "background-invalid", backgroundCommandToolName, `{"action":"list","data":"blocked"}`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			} else {
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "background-invalid" ||
					!strings.Contains(output.Output, `"status":"invalid_request"`) ||
					!strings.Contains(output.Output, `"reason":"forbidden_fields"`) ||
					!strings.Contains(output.Output, "other declared fields must be empty") {
					t.Errorf("background validation result did not reach next request: %+v", output)
				}
			}
			fmt.Fprint(w, `{"output_text":"corrected after background tool guidance"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	if err := RegisterBackgroundCommandTool(provider, command.BackgroundCommandManager{}); err != nil {
		t.Fatal(err)
	}
	reply, err := provider.Reply(context.Background(), "gpt-test", "correct a background tool call")
	if err != nil || reply != "corrected after background tool guidance" {
		t.Fatalf("provider reply = %q, error = %v", reply, err)
	}
	if backendRequests.Load() != 2 {
		t.Fatalf("backend requests = %d, want 2", backendRequests.Load())
	}
}

func TestCodexProviderAcceptsAllDeclaredBackgroundFieldsWithEmptyPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	config, err := json.Marshal(command.BackgroundCommandConfig{Version: 1, Commands: []command.BackgroundCommandDefinition{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	manager := command.NewBackgroundCommandManagerAt(path)
	defer manager.Close()

	var backendRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendRequests.Add(1) {
		case 1:
			registered := codexTool{}
			registeredFound := false
			for i := range request.Tools {
				if request.Tools[i].Name == backgroundCommandToolName {
					registered = request.Tools[i]
					registeredFound = true
					break
				}
			}
			if !registeredFound {
				t.Errorf("Codex request omitted %s tool", backgroundCommandToolName)
			} else {
				if registered.Type != "function" {
					t.Errorf("background tool type = %q, want function", registered.Type)
				}
				properties, ok := registered.Parameters["properties"].(map[string]any)
				if !ok || len(properties) != 5 {
					t.Errorf("background tool properties = %#v, want five declared string fields", registered.Parameters["properties"])
				} else {
					for _, field := range []string{"action", "name", "id", "data", "keys"} {
						property, ok := properties[field].(map[string]any)
						if !ok || property["type"] != "string" {
							t.Errorf("background tool property %q = %#v, want string", field, properties[field])
						}
					}
				}
				required, ok := registered.Parameters["required"].([]any)
				if !ok || len(required) != 1 || required[0] != "action" {
					t.Errorf("background tool required = %#v, want [action]", registered.Parameters["required"])
				}
				if registered.Parameters["additionalProperties"] != false {
					t.Errorf("background tool additionalProperties = %#v, want false", registered.Parameters["additionalProperties"])
				}
			}
			writeCodexFunctionCall(t, w, "background-empty-fields", backgroundCommandToolName,
				`{"action":"list","name":"","id":"","data":"","keys":""}`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			} else {
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "background-empty-fields" ||
					!strings.Contains(output.Output, `"status":"ok"`) ||
					!strings.Contains(output.Output, `"commands":[]`) ||
					!strings.Contains(output.Output, `"sessions":[]`) {
					t.Errorf("empty-placeholder tool result did not reach next request as success: %+v", output)
				}
			}
			fmt.Fprint(w, `{"output_text":"empty placeholders accepted"}`)
		default:
			t.Errorf("unexpected backend request %d", backendRequests.Load())
			fmt.Fprint(w, `{"output_text":"unexpected"}`)
		}
	}))
	defer backend.Close()

	provider := newShellTestProvider(t, backend)
	if err := RegisterBackgroundCommandTool(provider, manager); err != nil {
		t.Fatal(err)
	}
	reply, err := provider.Reply(context.Background(), "gpt-test", "list background commands")
	if err != nil || reply != "empty placeholders accepted" {
		t.Fatalf("provider reply = %q, error = %v", reply, err)
	}
	if backendRequests.Load() != 2 {
		t.Fatalf("backend requests = %d, want 2", backendRequests.Load())
	}
}

func TestBackgroundCommandToolDoesNotRetainPrivateArgumentText(t *testing.T) {
	privateField := "private-field-name"
	privateValue := "PRIVATE_ARGUMENT_VALUE"
	result, err := executeBackgroundCommandTool(context.Background(), fmt.Sprintf(`{"action":"list","%s":%q}`, privateField, privateValue), command.BackgroundCommandManager{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, privateField) || strings.Contains(result, privateValue) || strings.Contains(result, "private-field") {
		t.Fatalf("unsafe argument data reached tool result: %q", result)
	}
	if !strings.Contains(result, `"reason":"unsupported_field"`) || !strings.Contains(result, "Use only action, name, id, data, or keys") {
		t.Fatalf("unsupported-field result = %q", result)
	}
}

func TestBackgroundCommandValidationAcceptsEmptyIrrelevantFields(t *testing.T) {
	tests := []struct {
		action          string
		arguments       string
		forbiddenFields []string
	}{
		{"list", `{"action":"list","name":"","id":"","data":"","keys":""}`, []string{"name", "id", "data", "keys"}},
		{"start", `{"action":"start","name":"preview","id":"","data":"","keys":""}`, []string{"id", "data", "keys"}},
		{"status", `{"action":"status","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"output", `{"action":"output","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"stop", `{"action":"stop","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"poll", `{"action":"poll","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"read", `{"action":"read","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"kill", `{"action":"kill","name":"","id":"session-id","data":"","keys":""}`, []string{"name", "data", "keys"}},
		{"input", `{"action":"input","name":"","id":"session-id","data":"text","keys":""}`, []string{"name", "keys"}},
		{"write", `{"action":"write","name":"","id":"session-id","data":"text","keys":""}`, []string{"name", "keys"}},
		{"send-keys", `{"action":"send-keys","name":"","id":"session-id","data":"","keys":"enter"}`, []string{"name", "data"}},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			parsed, err := parseBackgroundCommandToolArguments(test.arguments)
			if err == nil {
				err = validateBackgroundCommandToolArguments(parsed)
			}
			if err != nil {
				t.Fatalf("empty irrelevant fields rejected: %v", err)
			}

			for _, field := range test.forbiddenFields {
				for _, value := range []string{"blocked", " \t\n"} {
					arguments := map[string]string{}
					if err := json.Unmarshal([]byte(test.arguments), &arguments); err != nil {
						t.Fatalf("decode base arguments: %v", err)
					}
					arguments[field] = value
					encoded, err := json.Marshal(arguments)
					if err != nil {
						t.Fatalf("encode forbidden %s: %v", field, err)
					}
					parsed, err := parseBackgroundCommandToolArguments(string(encoded))
					if err == nil {
						err = validateBackgroundCommandToolArguments(parsed)
					}
					argumentErr, ok := err.(backgroundCommandToolArgumentError)
					if !ok || argumentErr.reason != backgroundCommandReasonForbiddenFields {
						t.Errorf("nonempty forbidden %s accepted for %s: %v", field, test.action, err)
					}
					if err != nil && strings.Contains(err.Error(), value) {
						t.Errorf("forbidden %s value was echoed for %s: %v", field, test.action, err)
					}
				}
			}
		})
	}

	parsed, err := parseBackgroundCommandToolArguments(`{"action":"input","name":"","id":"session-id","data":"","keys":""}`)
	if err == nil {
		err = validateBackgroundCommandToolArguments(parsed)
	}
	if err != nil {
		t.Fatalf("explicit empty input data rejected despite being present: %v", err)
	}
}

func TestBackgroundCommandUnexpectedFailuresAreSafe(t *testing.T) {
	private := "PRIVATE unknown key: token not allowlisted"
	log := captureChatDiagnostics(t, func() {
		for _, action := range []string{"input", "send-keys", "stop"} {
			var reply map[string]any
			if action == "stop" {
				reply = backgroundCommandSessionFailureReply(fmt.Errorf("%s", private), action)
			} else {
				reply = backgroundCommandInputErrorReply(fmt.Errorf("%s", private), action)
			}
			encoded, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), private) || reply["reason"] == backgroundCommandReasonInvalidKeys {
				t.Errorf("unsafe result: %s", encoded)
			}
		}
	})
	if strings.Contains(log, private) || strings.Count(log, "ERROR operation=chat.background_command.") != 3 {
		t.Fatalf("unsafe or missing diagnostics: %q", log)
	}
	for _, action := range []string{"input", "send-keys", "stop"} {
		if !strings.Contains(log, "operation=chat.background_command."+action) {
			t.Errorf("missing action %s: %q", action, log)
		}
	}
}

func TestBackgroundCommandInvalidJSONResultsContainOnlyTrustedContent(t *testing.T) {
	for _, arguments := range []string{
		`{"action":"list","PRIVATE_FIELD":"PRIVATE_VALUE"}`,
		`{"action":"list","PRIVATE_FIELD":null}`,
		`{"action":"PRIVATE_VALUE","action":"list"}`,
		`{"action":"list"} PRIVATE_VALUE`,
		`{"action":"PRIVATE_VALUE`,
		`{"action":null,"data":"PRIVATE_VALUE"}`,
		`{"action":["PRIVATE_VALUE"]}`,
	} {
		result, err := executeBackgroundCommandTool(context.Background(), arguments, command.BackgroundCommandManager{})
		if err != nil || strings.Contains(result, "PRIVATE_") || !strings.Contains(result, `"status":"invalid_request"`) || !strings.Contains(result, `"reason":`) || !strings.Contains(result, `"message":`) {
			t.Errorf("unsafe or incomplete result for %q: %q, %v", arguments, result, err)
		}
	}
}
