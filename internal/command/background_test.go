package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	appconfig "goaster/config"
	"goaster/internal/diagnostics"
)

const (
	backgroundLegacyPTYKeys  = "up,down,left,right,y,N,comma"
	backgroundLegacyPTYBytes = "\x1b[A\x1b[B\x1b[D\x1b[CyN,"
	backgroundHostilePTYData = "A\x1b[1000000000b\x1b[1000000000I\x1b[1000000000Z\x1b[9223372036854775807b\x1b[18446744073709551615I\x1b[9999999999999999999999999999999999999999;2H"
)

// This helper runs in a separate copy of the test binary, without a shell.
func backgroundTestHelper() {
	index := -1
	for i, arg := range os.Args {
		if arg == "--background-test-helper" {
			index = i
			break
		}
	}
	if index < 0 || len(os.Args) <= index+1 {
		return
	}
	args := os.Args[index+2:]
	switch os.Args[index+1] {
	case "fork-exit":
		_ = os.WriteFile(args[0], []byte("parent launched"), 0600)
		child := exec.Command(os.Args[0], backgroundHelperArgs("waitfile", args[0]+"-child")...)
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	case "report":
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Args []string `json:"args"`
			Dir  string   `json:"dir"`
		}{args, dir})
	case "hold":
		fmt.Print("session-ready")
		time.Sleep(30 * time.Second)
	case "hold-child":
		child := exec.Command(os.Args[0], backgroundHelperArgs("waitfile", args[0])...)
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		time.Sleep(30 * time.Second)
	case "fail":
		fmt.Fprint(os.Stderr, "expected failure")
		os.Exit(7)
	case "secret-output":
		fmt.Fprint(os.Stdout, "output-secret")
		fmt.Fprint(os.Stderr, "error-secret")
	case "overflow":
		fmt.Print(strings.Repeat("x", MaxOutputBytes+1))
	case "waitfile":
		fmt.Print("session-ready")
		time.Sleep(2 * time.Second)
		_ = os.WriteFile(args[0], []byte("process survived"), 0600)
	case "mark-ready":
		_ = os.WriteFile(args[0], []byte("ready"), 0600)
		time.Sleep(30 * time.Second)
	case "interactive-input":
		fmt.Print("input-ready")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("input-received:%s", strings.TrimSuffix(line, "\n"))
	case "interactive-closed-input":
		_ = os.Stdin.Close()
		fmt.Print("input-closed")
		time.Sleep(1500 * time.Millisecond)
	case "interactive-no-read":
		fmt.Print("input-ready")
		time.Sleep(1500 * time.Millisecond)
	case "pty-key":
		fmt.Print("pty-key-ready")
		key, err := bufio.NewReader(os.Stdin).ReadByte()
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-key-received:%d", key)
	case "pty-key-bytes":
		if len(args) != 1 {
			os.Exit(2)
		}
		byteCount, err := strconv.Atoi(args[0])
		if err != nil || byteCount <= 0 {
			os.Exit(2)
		}
		stty := exec.Command("/bin/stty", "-icanon", "min", "1", "time", "0", "-echo")
		stty.Stdin = os.Stdin
		stty.Stdout = os.Stdout
		stty.Stderr = os.Stderr
		if err := stty.Run(); err != nil {
			os.Exit(2)
		}
		fmt.Print("pty-key-bytes-ready")
		received := make([]byte, byteCount)
		if _, err := io.ReadFull(os.Stdin, received); err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-key-bytes-received:%x", received)
	case "pty-cpr-handshake":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		size, err := backgroundPTYSize()
		if err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("pty-size:%s\n", size)
		fmt.Print("pty-output-secret\x1b[2J\x1b[H界")
		for _, chunk := range []string{"\x1b", "[6", "n"} {
			fmt.Print(chunk)
			time.Sleep(10 * time.Millisecond)
		}
		response, err := backgroundReadPTYResponse(reader)
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-cpr-response:%x:pty-progress", response)
	case "pty-unicode-cpr":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		readCPR := func() []byte {
			response, err := backgroundReadPTYResponse(reader)
			if err != nil {
				os.Exit(2)
			}
			return response
		}
		fmt.Print("\x1b[H🇫🇷\x1b[6n")
		fmt.Printf("pty-unicode-unsplit-flag:%x;", readCPR())
		fmt.Print("\x1b[H🇫")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("🇷\x1b[6n")
		fmt.Printf("pty-unicode-split-flag:%x;", readCPR())
		fmt.Print("\x1b[H👩‍💻\x1b[6n")
		fmt.Printf("pty-unicode-unsplit-zwj:%x;", readCPR())
		fmt.Print("\x1b[H👩")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("\u200d")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("💻\x1b[6n")
		fmt.Printf("pty-unicode-split-zwj:%x:pty-unicode-progress", readCPR())
	case "pty-osc-bel-cpr":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("\x1b[H\x1b]2;title")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("\a🇫")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("🇷\x1b[6n")
		response, err := backgroundReadPTYResponse(reader)
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-osc-bel-cpr:%x", response)
	case "pty-cpr-hold":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("pty-concurrent-ready")
		for i := 0; i < 100; i++ {
			fmt.Print("\x1b[6n")
			if _, err := backgroundReadPTYResponse(reader); err != nil {
				os.Exit(0)
			}
			fmt.Print("pty-cpr-progress")
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(30 * time.Second)
	case "pty-hostile":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("pty-hostile-ready\x1b[H")
		fmt.Print(backgroundHostilePTYData + "\x1b[6n")
		if _, err := backgroundReadPTYResponse(reader); err != nil {
			os.Exit(2)
		}
		fmt.Print("pty-hostile-progress")
		if len(args) > 0 && args[0] == "hold" {
			time.Sleep(30 * time.Second)
		}
	case "pty-cpr-overlap":
		if err := backgroundPreparePTYForCPR(); err != nil {
			os.Exit(2)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("pty-overlap-ready\x1b[H\x1b[6n")
		received := make([]byte, len("\x1b[A")+len("\x1b[1;1R"))
		if _, err := io.ReadFull(reader, received); err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-overlap-received:%x:pty-overlap-progress", received)
	case "pty-exit-cpr":
		fmt.Print("\x1b[6n")
	case "pty-exit-output":
		fmt.Print(strings.Repeat("x", 8192))
		fmt.Print("pty-exit-output")
	case "signal-host":
		manager := NewBackgroundCommandManagerAt(args[0])
		unregister := manager.CloseOnSignal()
		defer unregister()
		defer manager.Close()
		session, err := manager.Start(context.Background(), "worker")
		if err != nil {
			os.Exit(3)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			output, err := manager.Output(context.Background(), session.ID)
			if err == nil && strings.Contains(output.Stdout, "session-ready") {
				fmt.Println("host-ready")
				time.Sleep(30 * time.Second)
				os.Exit(4)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(5)
	default:
		backgroundPlatformHelper(os.Args[index+1], args)
		os.Exit(6)
	}
	os.Exit(0)
}

func backgroundHelperArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestExecuteSuccess$", "--", "--background-test-helper", mode}, args...)
}

func backgroundPreparePTYForCPR() error {
	stty := exec.Command("/bin/stty", "-icanon", "min", "1", "time", "0", "-echo")
	stty.Stdin = os.Stdin
	stty.Stdout = io.Discard
	stty.Stderr = os.Stderr
	return stty.Run()
}

func backgroundPTYSize() (string, error) {
	stty := exec.Command("/bin/stty", "size")
	stty.Stdin = os.Stdin
	var output strings.Builder
	stty.Stdout = &output
	stty.Stderr = os.Stderr
	if err := stty.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func backgroundReadPTYResponse(reader *bufio.Reader) ([]byte, error) {
	response := make([]byte, 0, 32)
	for len(response) < 64 {
		character, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		response = append(response, character)
		if character == 'R' {
			return response, nil
		}
	}
	return nil, errors.New("PTY response exceeded the test bound")
}

func backgroundTestExecutable(t testing.TB) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func writeBackgroundConfig(t testing.TB, path string, definitions ...BackgroundCommandDefinition) {
	t.Helper()
	if definitions == nil {
		definitions = []BackgroundCommandDefinition{}
	}
	data, err := json.Marshal(BackgroundCommandConfig{Version: 1, Commands: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func waitBackgroundState(t testing.TB, manager BackgroundCommandManager, id string, states ...string) BackgroundSession {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var session BackgroundSession
	for time.Now().Before(deadline) {
		var err error
		session, err = manager.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if session.State == state {
				return session
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session = %+v, want state in %v", session, states)
	return BackgroundSession{}
}

func waitBackgroundOutput(t testing.TB, manager BackgroundCommandManager, id, want string) BackgroundSessionOutput {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var output BackgroundSessionOutput
	for time.Now().Before(deadline) {
		var err error
		output, err = manager.Output(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.Stdout, want) {
			return output
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output = %+v, want %q", output, want)
	return BackgroundSessionOutput{}
}

func exerciseBackgroundCommandSupport(t testing.TB) {
	t.Helper()
	exerciseBackgroundConfig(t)
	exerciseBackgroundRedactedOutput(t)
	exerciseBackgroundCloseDeadline(t)
	exerciseBackgroundKeyEncoding(t)
	exerciseBackgroundPlatformLifecycle(t)
	exerciseBackgroundExactArgvAndReload(t)
	exerciseBackgroundLifecycle(t)
	exerciseBackgroundInteractiveInput(t)
	exerciseBackgroundInputDoesNotBlockManager(t)
	exerciseBackgroundInputWriteFailure(t)
	exerciseBackgroundClosedInputReported(t)
	exerciseBackgroundPTYInput(t)
	exerciseBackgroundPTYHandshake(t)
	exerciseBackgroundPTYUnicodeCPR(t)
	exerciseBackgroundPTYHostileOutput(t)
	exerciseBackgroundPTYInputResponseOrdering(t)
	exerciseBackgroundCompletionTime(t)
	exerciseBackgroundCanceledStart(t)
	exerciseBackgroundSignalShutdown(t)
}

func exerciseBackgroundRedactedOutput(t testing.TB) {
	t.Helper()
	process, err := StartBackground(BackgroundRequest{
		Executable:   backgroundTestExecutable(t),
		Args:         backgroundHelperArgs("secret-output"),
		RedactOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case execution := <-process.Done:
		if execution.Err != nil || execution.Result.Stdout != "" || execution.Result.Stderr != "" || execution.Result.StdoutTruncated || execution.Result.StderrTruncated {
			t.Fatalf("redacted process result = %+v, want no captured output", execution)
		}
	case <-time.After(6 * time.Second):
		process.Stop()
		t.Fatal("redacted process did not finish")
	}
}

func exerciseBackgroundCompletionTime(t testing.TB) {
	t.Helper()
	startedAt := time.Now().UTC()
	process, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("report")})
	if err != nil {
		t.Fatal(err)
	}
	var execution BackgroundExecution
	select {
	case execution = <-process.Done:
	case <-time.After(6 * time.Second):
		process.Stop()
		t.Fatal("process did not finish")
	}
	if execution.FinishedAt.Before(startedAt) || execution.FinishedAt.After(time.Now().UTC()) {
		t.Fatalf("invalid process finish time: %v", execution.FinishedAt)
	}
	time.Sleep(50 * time.Millisecond)
	completed := make(chan BackgroundExecution, 1)
	completed <- execution
	sessions := map[string]managedBackgroundSession{
		"completed": {process: BackgroundProcess{Done: completed}},
	}
	refreshManagedBackgroundSessions(sessions)
	if got := sessions["completed"].session.FinishedAt; !got.Equal(execution.FinishedAt) {
		t.Fatalf("session finish time = %v, process finished at %v", got, execution.FinishedAt)
	}
}

func exerciseBackgroundCloseDeadline(t testing.TB) {
	t.Helper()
	stopped := make(chan string, 2)
	sessions := map[string]managedBackgroundSession{
		"first":  {process: BackgroundProcess{Stop: func() { stopped <- "first" }, Done: make(chan BackgroundExecution)}},
		"second": {process: BackgroundProcess{Stop: func() { stopped <- "second" }, Done: make(chan BackgroundExecution)}},
	}
	done := make(chan error, 1)
	go func() {
		done <- closeManagedBackgroundSessions(sessions, 50*time.Millisecond)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("shutdown concealed deadline failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager shutdown exceeded its deadline")
	}
	if len(stopped) != len(sessions) {
		t.Fatalf("shutdown stopped %d processes, want %d", len(stopped), len(sessions))
	}
	manager := BackgroundCommandManager{requests: make(chan backgroundManagerRequest), done: make(chan struct{})}
	cleanupFailure := fmt.Errorf("%w: cleanup failed during close", errBackgroundCleanup)
	go func() {
		request := <-manager.requests
		request.response <- backgroundManagerResponse{err: cleanupFailure}
		close(manager.done)
	}()
	if err := manager.Close(); !errors.Is(err, cleanupFailure) {
		t.Fatalf("Close hid cleanup failure: %v", err)
	}
	<-manager.done

	for _, failure := range []error{cleanupFailure, errors.Join(context.Canceled, cleanupFailure), errors.Join(ErrOutputLimit, cleanupFailure)} {
		for _, refreshed := range []bool{false, true} {
			completed := make(chan BackgroundExecution, 1)
			completed <- BackgroundExecution{Err: failure, FinishedAt: time.Now().UTC()}
			close(completed)
			sessions := map[string]managedBackgroundSession{"failed": {stopRequested: true, process: BackgroundProcess{Stop: func() {}, Done: completed}}}
			if refreshed {
				refreshManagedBackgroundSessions(sessions)
			}
			if err := closeManagedBackgroundSessions(sessions, time.Second); !errors.Is(err, cleanupFailure) {
				t.Fatalf("shutdown concealed failure after refresh=%v: %v", refreshed, err)
			}
			if got := sessions["failed"].session; got.State != "failed" || got.Error == "" {
				t.Fatalf("cleanup failure lost: %+v", got)
			}
			if err := closeManagedBackgroundSessions(sessions, time.Second); !errors.Is(err, cleanupFailure) {
				t.Fatalf("repeated shutdown concealed failure: %v", err)
			}
		}
	}
	// Repeated/concurrent Close callers must all observe the same result.
	manager = BackgroundCommandManager{requests: make(chan backgroundManagerRequest), done: make(chan struct{}), closeResult: make(chan error, 1)}
	manager.closeResult <- cleanupFailure
	close(manager.done)
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() { results <- manager.Close() }()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; !errors.Is(err, cleanupFailure) {
			t.Fatalf("Close lost result: %v", err)
		}
	}
	writeFailure := errors.New("diagnostic output failed")
	if err := reportBackgroundShutdown(cleanupFailure, outputWriterFunc(func(data []byte) (int, error) { return 0, writeFailure })); !errors.Is(err, cleanupFailure) || !errors.Is(err, writeFailure) {
		t.Fatalf("shutdown diagnostic lost errors: %v", err)
	}
	if err := reportBackgroundShutdown(cleanupFailure, outputWriterFunc(func(data []byte) (int, error) { return 0, nil })); !errors.Is(err, cleanupFailure) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("shutdown diagnostic lost short write: %v", err)
	}
	closed := make(chan BackgroundExecution)
	close(closed)
	missing := map[string]managedBackgroundSession{"missing": {process: BackgroundProcess{Done: closed}}}
	refreshManagedBackgroundSessions(missing)
	if err := closeManagedBackgroundSessions(missing, time.Second); err == nil {
		t.Fatal("missing completion result was treated as clean")
	}
	retained := map[string]managedBackgroundSession{"failed": {finished: true, err: cleanupFailure}}
	order := []string{"failed"}
	for i := 1; i < maxBackgroundSessions; i++ {
		id := fmt.Sprint(i)
		retained[id] = managedBackgroundSession{finished: true}
		order = append(order, id)
	}
	if _, err := makeBackgroundSessionRoom(retained, order); err != nil {
		t.Fatal(err)
	}
	if _, ok := retained["failed"]; !ok {
		t.Fatal("session eviction discarded a cleanup failure")
	}
	if err := closeManagedBackgroundSessions(retained, time.Second); !errors.Is(err, cleanupFailure) {
		t.Fatalf("eviction hid cleanup error: %v", err)
	}
	for _, timeout := range []bool{false, true} {
		readFailure := errors.New("PTY output read failed")
		closeFailure := errors.New("PTY output close failed")
		readDone := make(chan error, 1)
		if !timeout {
			readDone <- readFailure
			close(readDone)
		}
		terminal := backgroundOutputTestTerminal{close: func() error {
			if timeout {
				readDone <- readFailure
				close(readDone)
			}
			return closeFailure
		}}
		if err := waitForBackgroundPTYOutput(terminal, readDone); !errors.Is(err, readFailure) || !errors.Is(err, closeFailure) || (timeout && !strings.Contains(err.Error(), "deadline")) {
			t.Fatalf("PTY output lost errors (timeout=%v): %v", timeout, err)
		}
	}

	started := time.Now()
	if err := waitForBackgroundPTYOutput(backgroundOutputTestTerminal{close: func() error { return nil }}, make(chan error)); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("unfinished output reader reported success: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("output error handling exceeded its bound")
	}

}

func exerciseBackgroundConfig(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "new", "background-commands.json")
	config, err := LoadBackgroundCommandConfig(path)
	if err != nil || config.Version != 1 || len(config.Commands) != 1 {
		t.Fatalf("default config = %+v, error = %v", config, err)
	}
	defaultCommand := config.Commands[0]
	wantArgs := []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web"}
	if defaultCommand.Name != appconfig.GitHubLoginCommandName || defaultCommand.Executable != "/usr/bin/gh" || !equalStrings(defaultCommand.Args, wantArgs) || !defaultCommand.Interactive || !defaultCommand.PTY || !defaultCommand.KeysOnly || defaultCommand.RedactOutput {
		t.Fatalf("default GitHub login command = %+v", defaultCommand)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(appconfig.BackgroundCommands) {
		t.Fatalf("installed template = %q, error = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("config file permissions = %v, %v; want 0600", info, err)
	}

	executable, err := json.Marshal(backgroundTestExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"name":"server","executable":` + string(executable) + `,"args":[]}`
	interactive := `{"name":"interactive","executable":` + string(executable) + `,"args":[],"interactive":true,"pty":true}`
	config, err = LoadBackgroundCommandConfig(path)
	if err != nil || config.Version != 1 || len(config.Commands) != 1 {
		t.Fatalf("initial background config = %+v, error = %v", config, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"commands":[`+interactive+`]}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err = LoadBackgroundCommandConfig(path)
	if err != nil || len(config.Commands) != 1 || !config.Commands[0].Interactive || !config.Commands[0].PTY {
		t.Fatalf("interactive background config = %+v, error = %v", config, err)
	}
	tests := []struct{ name, data string }{
		{"unknown config field", `{"version":1,"commands":[],"enabled":true}`},
		{"duplicate field", `{"version":1,"version":1,"commands":[]}`},
		{"unsupported version", `{"version":2,"commands":[]}`},
		{"duplicate name", `{"version":1,"commands":[` + valid + `,` + valid + `]}`},
		{"legacy shell command", `{"version":1,"commands":[{"name":"server","command":"echo ready"}]}`},
		{"shell fragment instead of executable", `{"version":1,"commands":[{"name":"server","executable":"echo ready; true","args":[]}]}`},
		{"missing arguments", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `}]}`},
		{"arguments are shell text", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":"one two"}]}`},
		{"nonstring argument", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[1]}]}`},
		{"NUL argument", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":["\u0000"]}]}`},
		{"invalid name", `{"version":1,"commands":[{"name":"../server","executable":` + string(executable) + `,"args":[]}]}`},
		{"unknown entry field", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"command":"echo ready"}]}`},
		{"interactive flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"interactive":"yes"}]}`},
		{"pty flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"pty":1}]}`},
		{"keys-only command without PTY", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"keys_only":true}]}`},
		{"keys-only flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"keys_only":"yes"}]}`},
		{"redact-output flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"redact_output":1}]}`},
	}
	for _, test := range tests {
		if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatalf("accepted invalid configuration: %s", test.name)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatal("accepted a non-private config")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(directory, "missing-target"), path); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatal("accepted a symbolic-link config")
		}
	}

	existingPath := filepath.Join(t.TempDir(), "background-commands.json")
	existingData := []byte(`{"version":1,"commands":[{"name":"existing","executable":` + string(executable) + `,"args":[]}]}`)
	if err := os.WriteFile(existingPath, existingData, 0600); err != nil {
		t.Fatal(err)
	}
	existingConfig, err := LoadBackgroundCommandConfig(existingPath)
	if err != nil || len(existingConfig.Commands) != 1 || existingConfig.Commands[0].Name != "existing" {
		t.Fatalf("existing background config = %+v, error = %v", existingConfig, err)
	}
	if data, err := os.ReadFile(existingPath); err != nil || string(data) != string(existingData) {
		t.Fatalf("existing background config changed: %q, %v", data, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestBundledGitHubAuthDocumentsExact(t *testing.T) {
	wantSkill := strings.Join([]string{
		"# GitHub authentication",
		"",
		"Before checking authentication or logging in, check that \x60/usr/bin/gh\x60 exists",
		"and is executable. If it does not, install GitHub CLI at exactly \x60/usr/bin/gh\x60",
		"before proceeding with login.",
		"",
		"Follow [AUTH.md](AUTH.md) to check or establish GitHub authentication.",
	}, "\n") + "\n"
	wantAuth := strings.Join([]string{
		"# GitHub authentication",
		"",
		"Check authentication on the user's host:",
		"",
		"\x60\x60\x60sh",
		"gh auth status --hostname github.com",
		"gh api --hostname github.com user --jq '.login'",
		"\x60\x60\x60",
		"",
		"Confirm the login matches the account the user named. Do not infer the account",
		"from a Git remote, environment variable, or repository name.",
		"",
		"## Log in",
		"",
		"If authentication is needed, start the private, allowlisted \x60github-login\x60",
		"profile using \x60background_command\x60:",
		"",
		"\x60\x60\x60json",
		"{\"action\":\"start\",\"name\":\"github-login\"}",
		"\x60\x60\x60",
		"",
		"The profile runs this fixed command in an interactive terminal (PTY), without",
		"model-supplied arguments:",
		"",
		"\x60\x60\x60sh",
		"gh auth login --hostname github.com --git-protocol https --web",
		"\x60\x60\x60",
		"",
		"Use the session ID returned by \x60start\x60 for subsequent \x60background_command\x60",
		"calls. The profile's backward-compatible \x60keys_only\x60 setting disables \x60input\x60",
		"and \x60write\x60 but does not restrict \x60send-keys\x60. Every eligible PTY accepts every",
		"named terminal key already supported by \x60send-keys\x60, including arrows, tab,",
		"space, backspace, enter/return, escape, function keys, and the existing",
		"control/modifier forms. It also accepts one ordinary printable character per",
		"comma-delimited token, so answer a Git credential prompt with a single \x60y\x60 or",
		"\x60n\x60 when needed:",
		"",
		"\x60\x60\x60json",
		"{\"action\":\"send-keys\",\"id\":\"<session-id>\",\"keys\":\"y\"}",
		"\x60\x60\x60",
		"",
		"Use named \x60enter\x60 to submit a prompt or open the browser:",
		"",
		"\x60\x60\x60json",
		"{\"action\":\"send-keys\",\"id\":\"<session-id>\",\"keys\":\"enter\"}",
		"\x60\x60\x60",
		"",
		"To cancel, send \x60ctrl-c\x60 to the same session:",
		"",
		"\x60\x60\x60json",
		"{\"action\":\"send-keys\",\"id\":\"<session-id>\",\"keys\":\"ctrl-c\"}",
		"\x60\x60\x60",
		"",
		"Do not use \x60input\x60 or \x60write\x60, paste text, or enter passwords, tokens, codes,",
		"credentials, or any other secrets. A comma separates key tokens; use named",
		"\x60comma\x60 for a literal comma. Read the terminal output and share GitHub's",
		"official verification URL and one-time code with the user, who completes",
		"verification in their browser. Leave the session running while they do so.",
		"",
		"Afterward, check the session with \x60status\x60 and read \x60output\x60 only as needed.",
		"Verify the host and account using the commands above. Stop any still-running",
		"or canceled session and verify its terminal state.",
	}, "\n") + "\n"
	for filename, want := range map[string]string{"SKILL.md": wantSkill, "AUTH.md": wantAuth} {
		got, err := os.ReadFile(filepath.Join("..", "..", "skills", "github-auth", filename))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("github-auth/%s contents differ from the required text", filename)
		}
	}
}

func TestBundledGitHubLoginCommandExact(t *testing.T) {
	configFile, err := os.ReadFile(filepath.Join("..", "..", "config", "background-commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(configFile) != string(appconfig.BackgroundCommands) {
		t.Fatal("embedded background command configuration differs from its source file")
	}
	config, err := decodeBackgroundCommandConfig(configFile)
	if err != nil || config.Version != 1 || len(config.Commands) != 1 {
		t.Fatalf("bundled background commands = %+v, error = %v", config, err)
	}
	profile := config.Commands[0]
	wantArgs := []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web"}
	if profile.Name != appconfig.GitHubLoginCommandName || profile.Executable != "/usr/bin/gh" || !equalStrings(profile.Args, wantArgs) || profile.CWD != "" || !profile.Interactive || !profile.PTY || !profile.KeysOnly || profile.RedactOutput {
		t.Fatalf("bundled GitHub login command = %+v", profile)
	}
}

func exerciseBackgroundInteractiveInput(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path,
		BackgroundCommandDefinition{
			Name: "input", Executable: executable, Args: backgroundHelperArgs("interactive-input"), Interactive: true,
		},
		BackgroundCommandDefinition{Name: "plain", Executable: executable, Args: backgroundHelperArgs("hold")},
	)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()

	configured, err := manager.Configured(context.Background())
	if err != nil || len(configured) != 2 || !configured[0].Interactive && !configured[1].Interactive {
		t.Fatalf("configured interactive commands = %+v, error = %v", configured, err)
	}

	interactive, err := manager.Start(context.Background(), "input")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, interactive.ID, "input-ready")
	if _, err := manager.Input(context.Background(), interactive.ID, "hello\n"); err != nil {
		t.Fatalf("interactive input error = %v", err)
	}
	finished := waitBackgroundState(t, manager, interactive.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 {
		captured, _ := manager.Output(context.Background(), interactive.ID)
		t.Fatalf("interactive session = %+v, output = %+v", finished, captured)
	}
	output, err := manager.Output(context.Background(), interactive.ID)
	if err != nil || !strings.Contains(output.Stdout, "input-received:hello") {
		t.Fatalf("interactive output = %+v, error = %v", output, err)
	}
	if _, err := manager.Input(context.Background(), interactive.ID, "late\n"); !errors.Is(err, ErrBackgroundSessionDone) {
		t.Fatalf("late input error = %v, want ErrBackgroundSessionDone", err)
	}

	plain, err := manager.Start(context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, plain.ID, "session-ready")
	if _, err := manager.Input(context.Background(), plain.ID, "not-accepted\n"); !errors.Is(err, ErrBackgroundSessionNotInteractive) {
		t.Fatalf("noninteractive input error = %v, want ErrBackgroundSessionNotInteractive", err)
	}
	if _, err := manager.SendKeys(context.Background(), interactive.ID, "enter"); !errors.Is(err, ErrBackgroundSessionDone) {
		t.Fatalf("non-PTY key error = %v, want completed session error", err)
	}
	if _, err := manager.Stop(context.Background(), plain.ID); err != nil {
		t.Fatal(err)
	}
	waitBackgroundState(t, manager, plain.ID, "stopped", "failed")
}

func exerciseBackgroundInputDoesNotBlockManager(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "no-read", Executable: executable, Args: backgroundHelperArgs("interactive-no-read"), Interactive: true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "no-read")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "input-ready")

	data := strings.Repeat("x", MaxSessionInputBytes)
	timedOut := false
	busySeen := false
	for index := 0; index < backgroundInputQueueSize+32; index++ {
		result := make(chan error, 1)
		go func() {
			_, inputErr := manager.Input(context.Background(), session.ID, data)
			result <- inputErr
		}()
		select {
		case inputErr := <-result:
			if errors.Is(inputErr, ErrBackgroundSessionInputBusy) {
				busySeen = true
			}
		case <-time.After(300 * time.Millisecond):
			timedOut = true
			break
		}
		if timedOut {
			break
		}
	}
	if !timedOut {
		statusDone := make(chan error, 1)
		go func() {
			_, statusErr := manager.Get(context.Background(), session.ID)
			statusDone <- statusErr
		}()
		select {
		case err := <-statusDone:
			if err != nil {
				t.Fatalf("status after queued input = %v", err)
			}
		case <-time.After(300 * time.Millisecond):
			timedOut = true
		}
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" {
		t.Fatalf("non-reading input session = %+v", finished)
	}
	if !busySeen {
		t.Fatal("unbounded input queue accepted every write")
	}
	if timedOut {
		t.Fatal("manager blocked on input to a child that was not reading")
	}
}

func exerciseBackgroundPTYInput(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		marker := filepath.Join(t.TempDir(), "pty-started")
		_, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("mark-ready", marker), PTY: true})
		if !errors.Is(err, ErrPTYNotSupported) {
			t.Fatalf("unsupported PTY: %v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported PTY launched a child: %v", err)
		}
		return
	}
	executable := backgroundTestExecutable(t)
	probe, err := StartBackground(BackgroundRequest{
		Executable:  executable,
		Args:        backgroundHelperArgs("pty-key"),
		Interactive: true,
		PTY:         true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("PTY probe error = %v", err)
	}
	probe.Stop()
	select {
	case <-probe.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("PTY probe did not clean up")
	}
	outputProcess, err := StartBackground(BackgroundRequest{
		Executable: executable,
		Args:       backgroundHelperArgs("pty-exit-output"),
		PTY:        true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("PTY output process error = %v", err)
	}
	select {
	case execution := <-outputProcess.Done:
		if !strings.Contains(execution.Result.Stdout, "pty-exit-output") {
			t.Fatalf("PTY output after exit = %q", execution.Result.Stdout)
		}
	case <-time.After(3 * time.Second):
		outputProcess.Stop()
		t.Fatal("PTY output process did not finish")
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "legacy-keys", Executable: executable, Args: backgroundHelperArgs("pty-key-bytes", strconv.Itoa(len(backgroundLegacyPTYBytes))), PTY: true, KeysOnly: true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "legacy-keys")
	if err != nil {
		t.Fatal(err)
	}
	if !session.Interactive || !session.PTY {
		t.Fatalf("PTY session metadata = %+v", session)
	}
	waitBackgroundOutput(t, manager, session.ID, "pty-key-bytes-ready")
	if _, err := manager.Input(context.Background(), session.ID, "text"); !errors.Is(err, ErrBackgroundSessionInputUnavailable) {
		t.Fatalf("keys-only PTY accepted text input: %v", err)
	}
	if _, err := manager.SendKeys(context.Background(), session.ID, backgroundLegacyPTYKeys); err != nil {
		t.Fatalf("legacy keys-only PTY rejected supported keys: %v", err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 || finished.InputFailed || finished.FinishedAt.IsZero() {
		t.Fatalf("legacy keys-only PTY session = %+v", finished)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if output.Stdout != "pty-key-bytes-readypty-key-bytes-received:"+fmt.Sprintf("%x", []byte(backgroundLegacyPTYBytes)) {
		t.Fatalf("legacy keys-only PTY received bytes = %q", output.Stdout)
	}
}

func exerciseBackgroundPTYHandshake(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		return
	}
	executable := backgroundTestExecutable(t)
	process, err := StartBackground(BackgroundRequest{
		Executable:  executable,
		Args:        backgroundHelperArgs("pty-cpr-handshake"),
		Interactive: true,
		PTY:         true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("PTY CPR handshake start error = %v", err)
	}
	select {
	case execution := <-process.Done:
		if execution.Err != nil {
			t.Fatalf("PTY CPR handshake error = %v", execution.Err)
		}
		for _, want := range []string{
			"pty-size:24 80",
			"pty-cpr-response:1b5b313b3352",
			"pty-progress",
		} {
			if !strings.Contains(execution.Result.Stdout, want) {
				t.Fatalf("PTY CPR handshake output = %q, missing %q", execution.Result.Stdout, want)
			}
		}
	case <-time.After(6 * time.Second):
		process.Stop()
		t.Fatal("PTY CPR handshake did not finish")
	}

	redacted, err := StartBackground(BackgroundRequest{
		Executable:   executable,
		Args:         backgroundHelperArgs("pty-cpr-handshake"),
		Interactive:  true,
		PTY:          true,
		RedactOutput: true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("redacted PTY CPR handshake start error = %v", err)
	}
	select {
	case execution := <-redacted.Done:
		if execution.Err != nil || execution.Result.Stdout != "" || execution.Result.Stderr != "" {
			t.Fatalf("redacted PTY CPR handshake result = %+v", execution)
		}
	case <-time.After(6 * time.Second):
		redacted.Stop()
		t.Fatal("redacted PTY CPR handshake did not finish")
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name:        "cpr-hold",
		Executable:  executable,
		Args:        backgroundHelperArgs("pty-cpr-hold"),
		Interactive: true,
		PTY:         true,
		KeysOnly:    true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "cpr-hold")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "pty-cpr-progress")
	results := make(chan error, 3)
	for i := 0; i < 2; i++ {
		go func() {
			_, inputErr := manager.SendKeys(context.Background(), session.ID, "y")
			results <- inputErr
		}()
	}
	go func() {
		_, stopErr := manager.Stop(context.Background(), session.ID)
		results <- stopErr
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for i := 0; i < 3; i++ {
		select {
		case operationErr := <-results:
			if operationErr != nil && !errors.Is(operationErr, ErrBackgroundSessionDone) && !errors.Is(operationErr, ErrBackgroundSessionInputUnavailable) {
				t.Fatalf("concurrent PTY input/stop error = %v", operationErr)
			}
		case <-deadline.C:
			t.Fatal("concurrent PTY input/stop did not finish")
		}
	}
	finished := waitBackgroundState(t, manager, session.ID, "stopped", "failed")
	if finished.State != "stopped" {
		t.Fatalf("concurrent PTY cleanup state = %+v", finished)
	}
}

func exerciseBackgroundPTYUnicodeCPR(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		return
	}
	process, err := StartBackground(BackgroundRequest{
		Executable:  backgroundTestExecutable(t),
		Args:        backgroundHelperArgs("pty-unicode-cpr"),
		Interactive: true,
		PTY:         true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("Unicode PTY CPR start error = %v", err)
	}
	select {
	case execution := <-process.Done:
		if execution.Err != nil {
			t.Fatalf("Unicode PTY CPR error = %v", execution.Err)
		}
		for _, want := range []string{
			"pty-unicode-unsplit-flag:1b5b313b3352",
			"pty-unicode-split-flag:1b5b313b3352",
			"pty-unicode-unsplit-zwj:1b5b313b3352",
			"pty-unicode-split-zwj:1b5b313b3352",
			"pty-unicode-progress",
		} {
			if !strings.Contains(execution.Result.Stdout, want) {
				t.Fatalf("Unicode PTY CPR output = %q, missing %q", execution.Result.Stdout, want)
			}
		}
	case <-time.After(6 * time.Second):
		process.Stop()
		t.Fatal("Unicode PTY CPR did not finish")
	}
}

func TestBackgroundPTYUnicodeCPR(t *testing.T) {
	exerciseBackgroundPTYUnicodeCPR(t)
}

func exerciseBackgroundPTYHostileOutput(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		return
	}
	for _, redacted := range []bool{false, true} {
		process, err := StartBackground(BackgroundRequest{
			Executable:   backgroundTestExecutable(t),
			Args:         backgroundHelperArgs("pty-hostile"),
			Interactive:  true,
			PTY:          true,
			RedactOutput: redacted,
		})
		if errors.Is(err, ErrPTYNotSupported) {
			return
		}
		if err != nil {
			t.Fatalf("hostile PTY start error (redacted=%v) = %v", redacted, err)
		}
		select {
		case execution := <-process.Done:
			if execution.Err != nil {
				t.Fatalf("hostile PTY error (redacted=%v) = %v", redacted, execution.Err)
			}
			if redacted {
				if execution.Result.Stdout != "" || execution.Result.Stderr != "" {
					t.Fatalf("hostile PTY retained redacted output = %+v", execution.Result)
				}
			} else if !strings.Contains(execution.Result.Stdout, "pty-hostile-progress") {
				t.Fatalf("hostile PTY output = %q, missing progress", execution.Result.Stdout)
			}
		case <-time.After(3 * time.Second):
			process.Stop()
			t.Fatalf("hostile PTY did not make bounded progress (redacted=%v)", redacted)
		}
	}

	process, err := StartBackground(BackgroundRequest{
		Executable:  backgroundTestExecutable(t),
		Args:        backgroundHelperArgs("pty-hostile", "hold"),
		Interactive: true,
		PTY:         true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("hostile PTY cleanup start error = %v", err)
	}
	progressDeadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(process.Snapshot().Stdout, "pty-hostile-progress") {
		if time.Now().After(progressDeadline) {
			process.Stop()
			t.Fatal("hostile PTY cleanup process did not make progress")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	process.Stop()
	select {
	case execution := <-process.Done:
		if !errors.Is(execution.Err, context.Canceled) || time.Since(started) > 2*time.Second {
			t.Fatalf("hostile PTY cleanup = %+v, elapsed=%v", execution, time.Since(started))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hostile PTY cleanup did not finish")
	}
}

func exerciseBackgroundPTYInputResponseOrdering(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		return
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name:        "cpr-overlap",
		Executable:  backgroundTestExecutable(t),
		Args:        backgroundHelperArgs("pty-cpr-overlap"),
		Interactive: true,
		PTY:         true,
		KeysOnly:    true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "cpr-overlap")
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "pty-overlap-ready")
	if _, err := manager.SendKeys(context.Background(), session.ID, "up"); err != nil {
		t.Fatalf("overlapping PTY key = %v", err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" {
		t.Fatalf("overlapping PTY state = %+v", finished)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	keyFirst := fmt.Sprintf("%x", []byte("\x1b[A\x1b[1;1R"))
	responseFirst := fmt.Sprintf("%x", []byte("\x1b[1;1R\x1b[A"))
	if !strings.Contains(output.Stdout, "pty-overlap-received:"+keyFirst) && !strings.Contains(output.Stdout, "pty-overlap-received:"+responseFirst) {
		t.Fatalf("overlapping PTY bytes = %q, want two complete non-interleaved sequences", output.Stdout)
	}
}

func exerciseBackgroundKeyEncoding(t testing.TB) {
	t.Helper()
	cases := []struct {
		name string
		keys string
		mode BackgroundPtyKeyMode
		want string
	}{
		{name: "named key", keys: "enter", mode: BackgroundPtyKeyModeCSI, want: "\r"},
		{name: "control key", keys: "ctrl-c", mode: BackgroundPtyKeyModeCSI, want: "\x03"},
		{name: "CSI arrow", keys: "up", mode: BackgroundPtyKeyModeCSI, want: "\x1b[A"},
		{name: "SS3 arrow", keys: "up", mode: BackgroundPtyKeyModeSS3, want: "\x1bOA"},
		{name: "printable lower case", keys: "y", mode: BackgroundPtyKeyModeCSI, want: "y"},
		{name: "printable upper case", keys: "N", mode: BackgroundPtyKeyModeCSI, want: "N"},
		{name: "printable sequence", keys: "y,N", mode: BackgroundPtyKeyModeCSI, want: "yN"},
		{name: "ordinary punctuation", keys: "!,?,;,:", mode: BackgroundPtyKeyModeCSI, want: "!?;:"},
		{name: "named comma", keys: "comma", mode: BackgroundPtyKeyModeCSI, want: ","},
		{name: "named comma with punctuation", keys: "!,comma,?", mode: BackgroundPtyKeyModeCSI, want: "!,?"},
		{name: "alt printable preserves case", keys: "alt-Y", mode: BackgroundPtyKeyModeCSI, want: "\x1bY"},
	}
	for _, fixture := range cases {
		got, err := EncodeBackgroundKeySequence(fixture.keys, fixture.mode)
		if err != nil || got != fixture.want {
			t.Fatalf("%s encoded key = %q, error = %v, want %q", fixture.name, got, err, fixture.want)
		}
	}
	for _, keys := range []string{"", "unknown", "yes", "password", "ctrl-", "alt-xx", "\x03", "\x1b[A", "!?", ",,,", ","} {
		if _, err := EncodeBackgroundKeySequence(keys, BackgroundPtyKeyModeCSI); err == nil {
			t.Errorf("accepted invalid key sequence %q", keys)
		}
	}
	if _, err := EncodeBackgroundKeySequence(strings.Repeat("f10,", MaxSessionInputBytes/4), BackgroundPtyKeyModeCSI); err == nil {
		t.Error("accepted a key sequence whose encoded bytes exceed the bound")
	}
	for key, want := range backgroundKeyMap {
		got, err := EncodeBackgroundKeySequence(key, BackgroundPtyKeyModeCSI)
		if err != nil || got != want {
			t.Errorf("existing named key %q encoded as %q, error = %v, want %q", key, got, err, want)
		}
	}
	detector := backgroundPtyKeyModeDetector{}
	var detected BackgroundPtyKeyMode
	detector, detected = observeBackgroundPtyKeyMode(detector, []byte("\x1b[?1h"+strings.Repeat("x", 64)))
	if detected != BackgroundPtyKeyModeSS3 {
		t.Fatalf("long PTY mode sequence detected as %d, want SS3", detected)
	}
	detector = backgroundPtyKeyModeDetector{}
	detector, detected = observeBackgroundPtyKeyMode(detector, []byte("\x1b[?1"))
	if detected != backgroundPtyKeyModeNotFound {
		t.Fatalf("partial PTY mode sequence detected as %d", detected)
	}
	_, detected = observeBackgroundPtyKeyMode(detector, []byte("h"))
	if detected != BackgroundPtyKeyModeSS3 {
		t.Fatalf("split PTY mode sequence detected as %d, want SS3", detected)
	}
}

func TestEncodeBackgroundKeySequenceRejectsUnsupportedShiftedSequences(t *testing.T) {
	for _, keys := range []string{"shift-up", "s-f1"} {
		if got, err := EncodeBackgroundKeySequence(keys, BackgroundPtyKeyModeCSI); err == nil || got != "" {
			t.Errorf("accepted unsupported shifted sequence %q as %q", keys, got)
		}
	}
}

func TestBackgroundPTYResponseWaitIsBounded(t *testing.T) {
	started := time.Now()
	err := waitForBackgroundPTYResponses(make(chan error))
	if err == nil || !strings.Contains(err.Error(), "did not finish after close") {
		t.Fatalf("response wait error = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("response wait exceeded its bound: %v", time.Since(started))
	}
}

func TestBackgroundPTYCreatePreservesWrappedCause(t *testing.T) {
	if !backgroundPTYSupported() {
		return
	}
	_, err := StartBackground(BackgroundRequest{
		Executable: filepath.Join(t.TempDir(), "PRIVATE_PTY_EXECUTABLE_TOKEN"),
		PTY:        true,
	})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PTY creation error = %v, want wrapped os.ErrNotExist", err)
	}
}

func TestBackgroundPTYInjectedStreamFailureHasOneOwnerAndSafeSession(t *testing.T) {
	if !backgroundPTYSupported() {
		return
	}
	privateCause := errors.New("PRIVATE_STREAM_RESPONSE_TOKEN")
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name:         "stream-failure",
		Executable:   backgroundTestExecutable(t),
		Args:         backgroundHelperArgs("pty-cpr-hold"),
		Interactive:  true,
		PTY:          true,
		KeysOnly:     true,
		RedactOutput: true,
	})

	executionReady := make(chan BackgroundExecution, 1)
	start := func(req BackgroundRequest) (BackgroundProcess, error) {
		process, err := startBackground(req, func(runtime backgroundPTYRuntime) backgroundPTYRuntime {
			runtime.responseWrite = func([]byte) (int, error) {
				return 0, privateCause
			}
			return runtime
		})
		if err != nil {
			return BackgroundProcess{}, err
		}
		originalDone := process.Done
		wrappedDone := make(chan BackgroundExecution, 1)
		go func() {
			execution, open := <-originalDone
			if open {
				executionReady <- execution
				wrappedDone <- execution
			}
			close(wrappedDone)
		}()
		process.Done = wrappedDone
		return process, nil
	}
	manager := newBackgroundCommandManagerAt(path, start)
	t.Cleanup(func() { _ = manager.Close() })

	var log bytes.Buffer
	restore := diagnostics.SetOutput(&log)
	defer restore()
	session, err := manager.Start(context.Background(), "stream-failure")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "failed")
	var execution BackgroundExecution
	select {
	case execution = <-executionReady:
	case <-time.After(3 * time.Second):
		t.Fatal("injected PTY stream failure did not publish a completion")
	}
	if !errors.Is(execution.Err, privateCause) {
		t.Fatalf("stream failure lost its wrapped cause: %v", execution.Err)
	}
	shutdownErr, shutdownOwned := backgroundShutdownOwnership(execution.Err)
	if !shutdownOwned || shutdownErr != nil {
		t.Fatalf("stream failure shutdown ownership = owned:%v err:%v, want owned with no shutdown error", shutdownOwned, shutdownErr)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var shutdown bytes.Buffer
	closeErr := manager.Close()
	if err := reportBackgroundShutdown(closeErr, &shutdown); err != nil {
		t.Fatalf("shutdown report = %v", err)
	}

	if finished.State != "failed" || output.Session.State != "failed" {
		t.Fatalf("stream failure session = %+v / %+v", finished, output.Session)
	}
	if output.Stdout != "" || output.Stderr != "" {
		t.Fatalf("redacted stream failure output = %+v", output)
	}
	if strings.Contains(finished.Error, privateCause.Error()) || strings.Contains(output.Session.Error, privateCause.Error()) {
		t.Fatalf("stream failure exposed its private cause: finished=%q output=%q", finished.Error, output.Session.Error)
	}
	if closeErr != nil || shutdown.Len() != 0 {
		t.Fatalf("already-owned stream failure reached shutdown: err=%v report=%q", closeErr, shutdown.String())
	}
	if strings.Count(log.String(), "ERROR operation=background.pty") != 1 || strings.Count(log.String(), "ERROR operation=") != 1 {
		t.Fatalf("stream failure diagnostics = %q, want one PTY owner record", log.String())
	}
	if strings.Contains(log.String(), privateCause.Error()) || strings.Contains(log.String(), "agent.main") || strings.Contains(log.String(), "background command shutdown failed") {
		t.Fatalf("stream failure diagnostic leaked or duplicated ownership: %q", log.String())
	}
}

type backgroundCloseAfterResponseTerminal struct {
	io.ReadWriteCloser
	closeStarted chan struct{}
	writeFailed  chan struct{}
}

func (terminal backgroundCloseAfterResponseTerminal) Close() error {
	close(terminal.closeStarted)
	<-terminal.writeFailed
	return terminal.ReadWriteCloser.Close()
}

func TestBackgroundPTYDrainResponseFailureHasOneOwnerAndSafeCause(t *testing.T) {
	if !backgroundPTYSupported() {
		return
	}
	privateCause := fmt.Errorf("PRIVATE_DRAIN_RESPONSE_TOKEN: %w", os.ErrClosed)
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name:         "drain-failure",
		Executable:   backgroundTestExecutable(t),
		Args:         backgroundHelperArgs("pty-exit-cpr"),
		Interactive:  true,
		PTY:          true,
		KeysOnly:     true,
		RedactOutput: true,
	})

	executionReady := make(chan BackgroundExecution, 1)
	start := func(req BackgroundRequest) (BackgroundProcess, error) {
		process, err := startBackground(req, func(runtime backgroundPTYRuntime) backgroundPTYRuntime {
			closeStarted := make(chan struct{})
			writeFailed := make(chan struct{})
			runtime.terminal = backgroundCloseAfterResponseTerminal{ReadWriteCloser: runtime.terminal, closeStarted: closeStarted, writeFailed: writeFailed}
			runtime.responseWrite = func([]byte) (int, error) {
				<-closeStarted
				close(writeFailed)
				return 0, privateCause
			}
			return runtime
		})
		if err != nil {
			return BackgroundProcess{}, err
		}
		originalDone := process.Done
		wrappedDone := make(chan BackgroundExecution, 1)
		go func() {
			execution, open := <-originalDone
			if open {
				executionReady <- execution
				wrappedDone <- execution
			}
			close(wrappedDone)
		}()
		process.Done = wrappedDone
		return process, nil
	}
	manager := newBackgroundCommandManagerAt(path, start)
	t.Cleanup(func() { _ = manager.Close() })

	var log bytes.Buffer
	restore := diagnostics.SetOutput(&log)
	defer restore()
	session, err := manager.Start(context.Background(), "drain-failure")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "failed")
	var execution BackgroundExecution
	select {
	case execution = <-executionReady:
	case <-time.After(3 * time.Second):
		t.Fatal("drain-time PTY stream failure did not publish a completion")
	}
	if !errors.Is(execution.Err, privateCause) || !errors.Is(execution.Err, os.ErrClosed) {
		t.Fatalf("drain-time stream failure lost its wrapped cause: %v", execution.Err)
	}
	shutdownErr, shutdownOwned := backgroundShutdownOwnership(execution.Err)
	if !shutdownOwned || shutdownErr != nil {
		t.Fatalf("drain-time stream failure shutdown ownership = owned:%v err:%v, want owned with no shutdown error", shutdownOwned, shutdownErr)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := manager.Close()
	if finished.State != "failed" || output.Session.State != "failed" {
		t.Fatalf("drain-time stream failure session = %+v / %+v", finished, output.Session)
	}
	if output.Stdout != "" || output.Stderr != "" || strings.Contains(finished.Error, privateCause.Error()) || strings.Contains(output.Session.Error, privateCause.Error()) {
		t.Fatalf("drain-time stream failure exposed private data: finished=%q output=%+v", finished.Error, output)
	}
	if closeErr != nil {
		t.Fatalf("already-owned drain-time stream failure reached shutdown: %v", closeErr)
	}
	if strings.Count(log.String(), "ERROR operation=background.pty") != 1 || strings.Count(log.String(), "ERROR operation=") != 1 {
		t.Fatalf("drain-time stream failure diagnostics = %q, want one PTY owner record", log.String())
	}
	if strings.Contains(log.String(), privateCause.Error()) || strings.Contains(log.String(), "background command shutdown failed") {
		t.Fatalf("drain-time stream failure diagnostic leaked or duplicated ownership: %q", log.String())
	}
}

func TestBackgroundPTYStreamOwnershipRetainsCleanupFailure(t *testing.T) {
	privateCause := errors.New("PRIVATE_STREAM_RESPONSE_TOKEN")
	cleanupFailure := errors.New("independent PTY close failure")
	streamFailure := backgroundPTYStreamFailure{cause: fmt.Errorf("%w: %w", errBackgroundPTYResponse, privateCause)}
	fullFailure := fmt.Errorf("%w: %w", errBackgroundCleanup, errors.Join(streamFailure, cleanupFailure))
	shutdownFailure := fmt.Errorf("%w: %w", errBackgroundCleanup, cleanupFailure)
	completed := make(chan BackgroundExecution, 1)
	completed <- BackgroundExecution{
		Err:        backgroundExecutionError{cause: fullFailure, shutdownErr: shutdownFailure, shutdownOwned: true},
		FinishedAt: time.Now().UTC(),
	}
	close(completed)
	sessions := map[string]managedBackgroundSession{
		"mixed-failure": {process: BackgroundProcess{Stop: func() {}, Done: completed}},
	}
	closeErr := closeManagedBackgroundSessions(sessions, time.Second)
	if !errors.Is(closeErr, cleanupFailure) || errors.Is(closeErr, privateCause) {
		t.Fatalf("shutdown failure ownership = %v, want cleanup cause only", closeErr)
	}
	if got := sessions["mixed-failure"].session.Error; strings.Contains(got, privateCause.Error()) {
		t.Fatalf("session exposed stream cause: %q", got)
	}
}

type backgroundCloseFailingWriteTerminal struct {
	started chan struct{}
	closed  chan struct{}
}

func (terminal backgroundCloseFailingWriteTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (terminal backgroundCloseFailingWriteTerminal) Write([]byte) (int, error) {
	close(terminal.started)
	<-terminal.closed
	return 0, os.ErrClosed
}
func (terminal backgroundCloseFailingWriteTerminal) Close() error {
	close(terminal.closed)
	return nil
}

func TestBackgroundPTYCloseInducedResponseWriteFailureIsRetained(t *testing.T) {
	emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
	pipe := emulator.InputPipe().(io.Closer)
	terminal := backgroundCloseFailingWriteTerminal{started: make(chan struct{}), closed: make(chan struct{})}
	ptyRuntime := backgroundPTYRuntime{terminal: terminal, emulator: emulator, responsePipe: pipe}
	done := ptyRuntime.startResponsePump(nil)
	writeDone := make(chan error, 1)
	go func() { _, err := emulator.Write([]byte("\x1b[6n")); writeDone <- err }()
	select {
	case <-terminal.started:
	case <-time.After(time.Second):
		t.Fatal("response write did not start")
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ptyRuntime.closeResponsePipe(); err != nil {
		t.Fatal(err)
	}
	if err := waitForBackgroundPTYResponses(done); !errors.Is(err, os.ErrClosed) || !errors.Is(err, errBackgroundPTYResponse) {
		t.Fatalf("close-induced response failure = %v", err)
	}
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("emulator write did not finish")
	}
}

type backgroundBlockingWriteTerminal struct {
	started chan struct{}
	release chan struct{}
}

func (terminal backgroundBlockingWriteTerminal) Read(data []byte) (int, error) {
	return 0, io.EOF
}

func (terminal backgroundBlockingWriteTerminal) Write(data []byte) (int, error) {
	select {
	case <-terminal.started:
	default:
		close(terminal.started)
	}
	<-terminal.release
	return len(data), nil
}

func (terminal backgroundBlockingWriteTerminal) Close() error { return nil }

func TestBackgroundPTYBlockedResponseCleanupIsBounded(t *testing.T) {
	emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
	responsePipe, ok := emulator.InputPipe().(io.Closer)
	if !ok {
		t.Fatal("emulator response pipe does not close")
	}
	terminal := backgroundBlockingWriteTerminal{started: make(chan struct{}), release: make(chan struct{})}
	ptyRuntime := backgroundPTYRuntime{
		terminal:     terminal,
		emulator:     emulator,
		responsePipe: responsePipe,
		writeGate:    backgroundPTYWriteGate(),
	}
	responseDone := ptyRuntime.startResponsePump(nil)
	writeDone := make(chan error, 1)
	go func() {
		_, err := emulator.Write([]byte("\x1b[6n"))
		writeDone <- err
	}()
	select {
	case <-terminal.started:
	case <-time.After(time.Second):
		t.Fatal("response writer did not enter the blocked terminal")
	}
	if err := ptyRuntime.closeResponsePipe(); err != nil {
		t.Fatalf("close response pipe = %v", err)
	}
	started := time.Now()
	err := waitForBackgroundPTYResponses(responseDone)
	if err == nil || !strings.Contains(err.Error(), "did not finish after close") || time.Since(started) > 2*time.Second {
		t.Fatalf("blocked response cleanup = %v, elapsed=%v", err, time.Since(started))
	}
	close(terminal.release)
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("emulator response write = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emulator response write remained blocked")
	}
	select {
	case err, open := <-responseDone:
		if open && err != nil {
			t.Fatalf("response pump after release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response pump remained blocked after release")
	}
}

func exerciseBackgroundExactArgvAndReload(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	sentinel := filepath.Join(directory, "shell-executed")
	args := []string{"two words", "", `"quotes"`, "$HOME", "; touch " + sentinel, "line\nbreak"}
	executable := backgroundTestExecutable(t)
	definition := BackgroundCommandDefinition{
		Name: "report", Description: "Report arguments", Executable: executable,
		Args: backgroundHelperArgs("report", args...), CWD: directory,
	}
	writeBackgroundConfig(t, path, definition)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	if _, err := manager.Start(context.Background(), "missing"); err == nil {
		t.Fatal("started an unlisted name")
	}
	configured, err := manager.Configured(context.Background())
	if err != nil || len(configured) != 1 || configured[0] != (BackgroundCommandInfo{Name: "report", Description: "Report arguments"}) {
		t.Fatalf("configured = %+v, error = %v", configured, err)
	}
	session, err := manager.Start(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 || finished.FinishedAt.IsZero() {
		t.Fatalf("finished = %+v", finished)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir, err := filepath.EvalSymlinks(directory)
	expected, encodeErr := json.Marshal(struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}{args, resolvedDir})
	if err != nil || encodeErr != nil || strings.TrimSpace(output.Stdout) != string(expected) {
		t.Fatalf("child output = %q; want %s (errors %v, %v)", output.Stdout, expected, err, encodeErr)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("shell fragment was executed or could not be checked: %v", err)
	}

	definition.Args = backgroundHelperArgs("report", "changed argument")
	writeBackgroundConfig(t, path, definition)
	second, err := manager.Start(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundState(t, manager, second.ID, "exited")
	output, err = manager.Output(context.Background(), second.ID)
	if err != nil || !strings.Contains(output.Stdout, "changed argument") {
		t.Fatalf("reloaded output = %+v, error = %v", output, err)
	}
	writeBackgroundConfig(t, path)
	configured, err = manager.Configured(context.Background())
	if err != nil || len(configured) != 0 {
		t.Fatalf("reloaded allowlist = %+v, error = %v", configured, err)
	}
	if _, err := manager.Start(context.Background(), "report"); err == nil {
		t.Fatal("started a command removed from the allowlist")
	}
	if _, err := manager.Get(context.Background(), session.ID); err != nil {
		t.Fatalf("removing an allowlist entry lost its existing session: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), "report"); err == nil {
		t.Fatal("started while configuration was invalid")
	}
}

func exerciseBackgroundLifecycle(t testing.TB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path,
		BackgroundCommandDefinition{Name: "hold", Executable: executable, Args: backgroundHelperArgs("hold")},
		BackgroundCommandDefinition{Name: "fail", Executable: executable, Args: backgroundHelperArgs("fail")},
		BackgroundCommandDefinition{Name: "overflow", Executable: executable, Args: backgroundHelperArgs("overflow")},
	)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	ctx := context.Background()
	failed, err := manager.Start(ctx, "fail")
	if err != nil {
		t.Fatal(err)
	}
	failed = waitBackgroundState(t, manager, failed.ID, "exited", "failed")
	if failed.State != "exited" || failed.ExitCode != 7 {
		t.Fatalf("nonzero exit = %+v", failed)
	}
	output, err := manager.Output(ctx, failed.ID)
	if err != nil || output.Stderr != "expected failure" {
		t.Fatalf("nonzero exit output = %+v, error = %v", output, err)
	}
	limited, err := manager.Start(ctx, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	limited = waitBackgroundState(t, manager, limited.ID, "output_limited", "exited", "failed")
	output, err = manager.Output(ctx, limited.ID)
	if err != nil || limited.State != "output_limited" || !output.Session.StdoutTruncated || len(output.Stdout) != MaxOutputBytes {
		t.Fatalf("output-limited session = %+v, captured = %d, error = %v", limited, len(output.Stdout), err)
	}
	running, err := manager.Start(ctx, "hold")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, running.ID, "session-ready")
	if _, err := manager.Stop(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitBackgroundState(t, manager, running.ID, "stopped", "failed")
	if stopped.State != "stopped" || stopped.FinishedAt.IsZero() {
		t.Fatalf("stopped session = %+v", stopped)
	}
	if _, err := manager.Output(ctx, "unknown"); err == nil {
		t.Fatal("accepted an unknown session")
	}
	for i := 0; i < 2; i++ {
		session, err := manager.Start(ctx, "hold")
		if err != nil {
			t.Fatal(err)
		}
		waitBackgroundOutput(t, manager, session.ID, "session-ready")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, "hold"); err == nil {
		t.Fatal("closed manager accepted a start")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func exerciseBackgroundSignalShutdown(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	marker := filepath.Join(directory, "survived")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{Name: "worker", Executable: executable, Args: backgroundHelperArgs("waitfile", marker)})
	host := exec.Command(executable, backgroundHelperArgs("signal-host", path)...)
	stdout, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer host.Process.Kill()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if strings.TrimSpace(line) != "host-ready" {
			t.Fatalf("host reported %q", line)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("host did not start its child")
	}
	if err := host.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Wait() }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("host did not terminate")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived host termination or marker could not be checked: %v", err)
	}
}

func exerciseBackgroundCanceledStart(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	marker := filepath.Join(directory, "ready")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "worker", Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("mark-ready", marker),
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Withhold the reply to simulate a caller disappearing after the child starts.
	manager.requests <- backgroundManagerRequest{action: "start", name: "worker", ctx: ctx, response: make(chan backgroundManagerResponse)}
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	sessions, err := manager.List(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions after cancellation = %+v, error = %v", sessions, err)
	}
	stopped := waitBackgroundState(t, manager, sessions[0].ID, "stopped", "failed")
	if stopped.State != "stopped" {
		t.Fatalf("canceled start left session = %+v", stopped)
	}
}

func exerciseBackgroundInputWriteFailure(t testing.TB) {
	for _, test := range []struct {
		name  string
		write outputWriterFunc
	}{
		{"error", func(p []byte) (int, error) { return 0, errors.New("pipe closed") }},
		{"short", func(p []byte) (int, error) { return len(p) - 1, nil }},
	} {
		func() {
			write, failed, finish := backgroundInputWriter(test.write)
			defer finish()
			if err := write("abc"); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(time.Second)
			for !failed() {
				select {
				case <-deadline:
					t.Fatal("failure not recorded")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if err := write("later"); !errors.Is(err, ErrBackgroundSessionInputUnavailable) {
				t.Fatalf("subsequent write: %v", err)
			}
		}()
	}
}

func exerciseBackgroundClosedInputReported(t testing.TB) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{Name: "closed", Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("interactive-closed-input"), Interactive: true})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "input-closed")
	if _, err := manager.Input(context.Background(), session.ID, "control"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		state, err := manager.Get(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.InputFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("input failure not visible in session")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := manager.Input(context.Background(), session.ID, "later"); !errors.Is(err, ErrBackgroundSessionInputUnavailable) {
		t.Fatalf("subsequent input: %v", err)
	}
}

// Value methods keep output-error fixtures free of pointer declarations.
type backgroundOutputTestTerminal struct{ close func() error }

func (terminal backgroundOutputTestTerminal) Read(data []byte) (int, error)  { return 0, io.EOF }
func (terminal backgroundOutputTestTerminal) Write(data []byte) (int, error) { return len(data), nil }
func (terminal backgroundOutputTestTerminal) Close() error                   { return terminal.close() }

func exerciseBackgroundProcessLifecycle(t testing.TB) {
	t.Helper()
	process, err := StartBackground(BackgroundRequest{
		Executable: backgroundTestExecutable(t),
		Args:       backgroundHelperArgs("hold"),
	})
	if err != nil {
		t.Fatal(err)
	}
	process.Stop()
	select {
	case execution := <-process.Done:
		if !errors.Is(execution.Err, context.Canceled) {
			t.Fatalf("stopped process error = %v, want context.Canceled", execution.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stopped process did not finish")
	}
}

func TestBackgroundPTYOrdinaryCSIParameters(t *testing.T) {
	emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
	configureBackgroundPTYParameterBounds(emulator)
	if _, err := emulator.Write([]byte("\x1b[1;80H")); err != nil {
		t.Fatal(err)
	}
	if pos := emulator.CursorPosition(); pos.X != 79 || pos.Y != 0 {
		t.Fatalf("cursor position = %v, want column 80", pos)
	}
	if _, err := emulator.Write([]byte("\x1b[2;1H\x1b[38;5;196;48;5;255mX\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}
	reference := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
	if _, err := reference.Write([]byte("\x1b[2;1H\x1b[38;5;196;48;5;255mX")); err != nil {
		t.Fatal(err)
	}
	if got, want := emulator.CellAt(0, 1), reference.CellAt(0, 1); got == nil || want == nil || !got.Equal(want) {
		t.Fatalf("colored cell = %v, want %v", got, want)
	}
	responses := make(chan string, 1)
	go func() {
		buf := make([]byte, 128)
		n, _ := emulator.Read(buf)
		responses <- string(buf[:n])
	}()
	if _, err := emulator.Write([]byte("\x1b[?2004$p")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-responses:
		if got != "\x1b[?2004;1$y" {
			t.Fatalf("bracketed paste mode response = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("mode query did not respond")
	}
}

type backgroundPTYAdapterRecordingWriter struct{ writes chan []byte }

func (writer backgroundPTYAdapterRecordingWriter) Write(data []byte) (int, error) {
	copyData := append([]byte(nil), data...)
	writer.writes <- copyData
	return len(data), nil
}

func TestBackgroundPTYInputAdapterPreservesUTF8AndANSIBoundaries(t *testing.T) {
	writes := make(chan []byte, 32)
	adapter := newBackgroundPTYInputAdapter(backgroundPTYAdapterRecordingWriter{writes: writes})
	if _, err := adapter.Write([]byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte{0x95}); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 {
		t.Fatalf("partial UTF-8 was forwarded before completion: %d writes", len(writes))
	}
	if _, err := adapter.Write([]byte{0x8C, 'X'}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte{'\x1b'}); err != nil {
		t.Fatal(err)
	}
	if len(writes) == 0 {
		t.Fatal("escape control was withheld with the grapheme tail")
	}
	if _, err := adapter.Write([]byte("[6")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte("\x1b]2;title")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte{'\x1b'}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte{'\\'}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Write([]byte("B")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Flush(); err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	for {
		select {
		case data := <-writes:
			_, _ = got.Write(data)
		default:
			if got.String() != "界X\x1b[6n\x1b]2;title\x1b\\B" {
				t.Fatalf("adapter output = %q", got.String())
			}
			return
		}
	}
}

func TestBackgroundPTYInputAdapterBoundsLongGraphemeTail(t *testing.T) {
	writes := make(chan []byte, 128)
	adapter := newBackgroundPTYInputAdapter(backgroundPTYAdapterRecordingWriter{writes: writes})
	input := []byte("e" + strings.Repeat("\u0301", backgroundPTYMaxGraphemeTailBytes))
	if _, err := adapter.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Flush(); err != nil {
		t.Fatal(err)
	}
	var total int
	for {
		select {
		case data := <-writes:
			if len(data) > backgroundPTYMaxGraphemeTailBytes {
				t.Fatalf("adapter forwarded a grapheme chunk of %d bytes, limit %d", len(data), backgroundPTYMaxGraphemeTailBytes)
			}
			total += len(data)
		default:
			if total != len(input) {
				t.Fatalf("adapter forwarded %d of %d bytes", total, len(input))
			}
			return
		}
	}
}

// Every split is passed directly to the adapter, independent of PTY read boundaries.
func TestBackgroundPTYInputAdapterUnicodeCPR(t *testing.T) {
	for _, tc := range []struct{ name, text, response string }{
		{"regional indicators", "🇫🇷", "\x1b[1;3R"},
		{"ZWJ", "👩‍💻", "\x1b[1;3R"},
		{"ASCII combining", "e\u0301", "\x1b[1;2R"},
		{"keycap", "1\ufe0f\u20e3", "\x1b[1;3R"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.text)
			for split := 0; split <= len(data)+1; split++ {
				t.Run(strconv.Itoa(split), func(t *testing.T) {
					emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
					defer emulator.Close()
					adapter := newBackgroundPTYInputAdapter(emulator)
					if split == len(data)+1 {
						for _, b := range data {
							if _, err := adapter.Write([]byte{b}); err != nil {
								t.Fatal(err)
							}
						}
					} else {
						for _, part := range [][]byte{data[:split], data[split:]} {
							if _, err := adapter.Write(part); err != nil {
								t.Fatal(err)
							}
						}
					}
					responses := make(chan string, 1)
					go func() { buf := make([]byte, 64); n, _ := emulator.Read(buf); responses <- string(buf[:n]) }()
					if _, err := adapter.Write([]byte("\x1b[6n")); err != nil {
						t.Fatal(err)
					}
					select {
					case got := <-responses:
						if got != tc.response {
							t.Fatalf("CPR = %q, want %q", got, tc.response)
						}
					case <-time.After(time.Second):
						t.Fatal("CPR timed out")
					}
				})
			}
		})
	}
}

// Exercise every boundary in the entire control sequence and grapheme, as
// well as byte-at-a-time writes, independently of kernel PTY chunking.
func TestBackgroundPTYInputAdapterStringContextCPR(t *testing.T) {
	for _, context := range []struct{ name, prefix string }{
		{"OSC BEL", "\x1b]2;title\a"},
		{"OSC ST", "\x1b]2;title\x1b\\"},
		{"OSC C1 ST", "\x1b]2;title\x9c"},
		{"OSC CAN", "\x1b]2;title\x18"},
		{"OSC SUB", "\x1b]2;title\x1a"},
		{"DCS BEL ST", "\x1bPqdata\adata\x1b\\"},
		{"DCS CAN", "\x1bPqdata\x18"},
		{"DCS SUB", "\x1bPqdata\x1a"},
	} {
		for _, tc := range []struct{ name, text, response string }{
			{"flag", "🇫🇷", "\x1b[1;3R"}, {"ZWJ", "👩‍💻", "\x1b[1;3R"},
			{"combining", "e\u0301", "\x1b[1;2R"}, {"keycap", "1\ufe0f\u20e3", "\x1b[1;3R"},
		} {
			t.Run(context.name+"/"+tc.name, func(t *testing.T) {
				data := []byte(context.prefix + tc.text)
				for split := 0; split <= len(data)+1; split++ {
					t.Run(strconv.Itoa(split), func(t *testing.T) {
						emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
						defer emulator.Close()
						adapter := newBackgroundPTYInputAdapter(emulator)
						parts := [][]byte{data[:min(split, len(data))], data[min(split, len(data)):]}
						if split == len(data)+1 {
							parts = nil
							for i := range data {
								parts = append(parts, data[i:i+1])
							}
						}
						for _, part := range parts {
							if _, err := adapter.Write(part); err != nil {
								t.Fatal(err)
							}
						}
						responses := make(chan string, 1)
						go func() { buf := make([]byte, 64); n, _ := emulator.Read(buf); responses <- string(buf[:n]) }()
						if _, err := adapter.Write([]byte("\x1b[6n")); err != nil {
							t.Fatal(err)
						}
						select {
						case got := <-responses:
							if got != tc.response {
								t.Fatalf("CPR = %q, want %q", got, tc.response)
							}
						case <-time.After(time.Second):
							t.Fatal("CPR timed out")
						}
					})
				}
			})
		}
	}
}

func TestBackgroundPTYOSCBELUnicodeCPR(t *testing.T) {
	if !backgroundPTYSupported() {
		t.Skip("PTY unsupported")
	}
	process, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("pty-osc-bel-cpr"), PTY: true})
	if errors.Is(err, ErrPTYNotSupported) {
		t.Skip("PTY unsupported")
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case execution := <-process.Done:
		if execution.Err != nil || !strings.Contains(execution.Result.Stdout, "pty-osc-bel-cpr:1b5b313b3352") {
			t.Fatalf("OSC BEL PTY result = %+v", execution)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OSC BEL PTY did not finish")
	}
}
