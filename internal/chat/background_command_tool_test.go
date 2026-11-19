package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"goaster/internal/command"
)

func TestBackgroundCommandToolHelper(t *testing.T) {
	marker := false
	for _, argument := range os.Args {
		if argument == "--goaster-background-tool-helper" {
			marker = true
			break
		}
	}
	if !marker {
		return
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
		!strings.Contains(registered.Description, "never use input") ||
		!strings.Contains(registered.Description, "PTY echo") ||
		!strings.Contains(registered.Description, "github-login") ||
		!strings.Contains(registered.Description, "official verification URL and one-time code") ||
		!strings.Contains(registered.Description, "Never relay passwords, tokens, credentials, or other secrets") {
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
			name:       "list rejects empty data",
			arguments:  `{"action":"list","data":""}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "start rejects empty keys",
			arguments:  `{"action":"start","name":"preview","keys":""}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   `{"action":"start","name":"configured-name"}`,
		},
		{
			name:       "start requires name",
			arguments:  `{"action":"start"}`,
			wantReason: backgroundCommandReasonMissingName,
			wantText:   `{"action":"list"}`,
		},
		{
			name:       "session action requires id",
			arguments:  `{"action":"status"}`,
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
			name:       "status rejects data",
			arguments:  `{"action":"status","id":"session-id","data":""}`,
			wantReason: backgroundCommandReasonForbiddenFields,
			wantText:   "only action and id",
		},
		{
			name:       "unsupported action is not echoed",
			arguments:  `{"action":"private-unknown-action"}`,
			wantReason: backgroundCommandReasonUnsupportedAction,
			wantText:   backgroundCommandActionsMessage,
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

	invalid, err := executeBackgroundCommandTool(context.Background(), `{"action":"list","keys":""}`, manager)
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
			writeCodexFunctionCall(t, w, "background-invalid", backgroundCommandToolName, `{"action":"list","data":""}`)
		case 2:
			if len(request.Input) != 3 {
				t.Errorf("follow-up input length = %d, want 3: %+v", len(request.Input), request.Input)
			} else {
				output := request.Input[2]
				if output.Type != "function_call_output" || output.CallID != "background-invalid" ||
					!strings.Contains(output.Output, `"status":"invalid_request"`) ||
					!strings.Contains(output.Output, `"reason":"forbidden_fields"`) ||
					!strings.Contains(output.Output, "list accepts only action") {
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

func TestBackgroundCommandValidationPreservesEmptyIrrelevantFields(t *testing.T) {
	for _, arguments := range []string{
		`{"action":"list","name":" ","id":""}`,
		`{"action":"start","name":"example","id":" "}`,
		`{"action":"status","id":"x","name":" "}`,
		`{"action":"output","id":"x","name":""}`,
		`{"action":"stop","id":"x","name":""}`,
		`{"action":"poll","id":"x","name":""}`,
		`{"action":"read","id":"x","name":""}`,
		`{"action":"kill","id":"x","name":""}`,
		`{"action":"input","id":"x","name":"","data":""}`,
		`{"action":"write","id":"x","name":"","data":""}`,
		`{"action":"send-keys","id":"x","name":"","keys":"enter"}`,
	} {
		parsed, err := parseBackgroundCommandToolArguments(arguments)
		if err == nil {
			err = validateBackgroundCommandToolArguments(parsed)
		}
		if err != nil {
			t.Errorf("%s rejected: %v", arguments, err)
		}
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
