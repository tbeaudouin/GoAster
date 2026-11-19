package command

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	ansiparser "github.com/charmbracelet/x/ansi/parser"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/rivo/uniseg"

	"goaster/internal/diagnostics"
)

const (
	// These are the stable dimensions exposed to every background PTY. The
	// kernel and the emulator must use the same size for cursor reports to be
	// meaningful; there is no attached user terminal to copy from here.
	backgroundPTYColumns         = 80
	backgroundPTYRows            = 24
	backgroundPTYScrollbackLines = 1
	backgroundPTYMaxCSIParameter = 65535
	backgroundPTYMaxCSIRepeat    = 4096
	// A stream tail is held only long enough to distinguish a grapheme from
	// the next stream token. Unicode permits arbitrarily long clusters; this
	// bound keeps a hostile cluster from growing memory or making each read
	// rescan an unbounded buffer. A cluster beyond this limit is committed at
	// a valid UTF-8 boundary and may therefore occupy more than one cell.
	backgroundPTYMaxGraphemeTailBytes = 256

	// This bounds the response-pump read, not terminal output capture. The
	// emulator's data buffer is bounded by the pinned library, while its parser
	// parameter count is bounded separately below.
	backgroundPTYResponseBufferBytes = 4096
)

var (
	errBackgroundPTYParser   = errors.New("background PTY terminal parser failed")
	errBackgroundPTYResponse = errors.New("background PTY terminal response failed")
)

// backgroundPTYStreamFailure keeps the complete cause available to Go callers
// while exposing a fixed message to session/status consumers. The asynchronous
// PTY owner emits the diagnostic before publishing this failure.
type backgroundPTYStreamFailure struct{ cause error }

func (failure backgroundPTYStreamFailure) Error() string {
	return "background PTY stream failed"
}

func (failure backgroundPTYStreamFailure) Unwrap() error { return failure.cause }

func (failure backgroundPTYStreamFailure) DiagnosticLogged() {}

type backgroundPTYConfigurer func(backgroundPTYRuntime) backgroundPTYRuntime

type backgroundPTYRuntime struct {
	terminal      io.ReadWriteCloser
	emulator      *vt.SafeEmulator
	emulatorInput backgroundPTYInputAdapter
	responsePipe  io.Closer
	writeGate     chan struct{}
	responseWrite func([]byte) (int, error)
}

// backgroundPTYInputAdapter prevents x/vt's Write-boundary flush from
// turning a grapheme split across PTY reads into multiple cells. x/ansi's
// parser transition table supplies ANSI context; uniseg supplies UAX-29
// segmentation while control sequences pass through without buffering.
type backgroundPTYInputAdapter struct {
	write func([]byte) (int, error)
	flush func() error
}

func (adapter backgroundPTYInputAdapter) Write(data []byte) (int, error) {
	if adapter.write == nil {
		return 0, errors.New("PTY grapheme adapter is unavailable")
	}
	return adapter.write(data)
}

func (adapter backgroundPTYInputAdapter) Flush() error {
	if adapter.flush == nil {
		return nil
	}
	return adapter.flush()
}

func newBackgroundPTYInputAdapter(writer io.Writer) backgroundPTYInputAdapter {
	state := ansiparser.GroundState
	var utf8Remaining int
	var tail []byte

	writeAll := func(data []byte) error {
		written, err := writer.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}

	flush := func() error {
		if len(tail) == 0 {
			return nil
		}
		if err := writeAll(tail); err != nil {
			return err
		}
		tail = nil
		return nil
	}

	hold := func(data []byte) error {
		for len(data) > backgroundPTYMaxGraphemeTailBytes {
			count := backgroundPTYUTF8Prefix(data, backgroundPTYMaxGraphemeTailBytes)
			if count <= 0 {
				return errors.New("PTY grapheme adapter could not make bounded progress")
			}
			if err := writeAll(data[:count]); err != nil {
				return err
			}
			data = data[count:]
		}
		tail = append(tail[:0], data...)
		return nil
	}

	write := func(data []byte) (int, error) {
		if len(data) == 0 {
			return 0, nil
		}

		working := make([]byte, 0, len(tail)+len(data))
		working = append(working, tail...)
		working = append(working, data...)
		tail = nil
		var pending []byte
		var incomplete []byte

		for {
			if len(working) == 0 {
				if len(incomplete) == 0 {
					break
				}
				if state == ansiparser.GroundState {
					pending = append(pending, incomplete...)
					incomplete = nil
					break
				}
				working = incomplete
				incomplete = nil
			}

			// Only hold an incomplete UTF-8 suffix while the ANSI parser is in
			// ground state. If the bytes follow ESC/CSI/OSC state, they are
			// control-string data and must be forwarded immediately.
			if state == ansiparser.GroundState && len(incomplete) == 0 {
				if split := backgroundPTYIncompleteUTF8Suffix(working); split >= 0 {
					incomplete = append(incomplete[:0], working[split:]...)
					working = working[:split]
					continue
				}
			}
			// Segment the entire printable run, including ASCII-led clusters.
			if state == ansiparser.GroundState && backgroundPTYPrintableSequence(working) {
				end := 0
				for end < len(working) && backgroundPTYPrintableSequence(working[end:]) {
					_, size := utf8.DecodeRune(working[end:])
					end += size
				}
				run := working[:end]
				working = working[end:]
				for len(run) > 0 {
					cluster, rest, _, _ := uniseg.FirstGraphemeCluster(run, -1)
					if len(rest) == 0 && len(working) == 0 {
						pending = append(pending, cluster...)
					} else {
						if err := hold(cluster); err != nil {
							return 0, err
						}
						if err := flush(); err != nil {
							return 0, err
						}
					}
					run = rest
				}
				continue
			}

			// Keep the same finite parser context as x/vt, including the type
			// of string and DCS entry states. DecodeSequence loses that context
			// across calls, so a split OSC BEL or DCS prefix is ambiguous.
			if err := flush(); err != nil {
				return 0, err
			}
			count := 0
			for count < len(working) {
				character := working[count]
				count++
				if state == ansiparser.Utf8State {
					utf8Remaining--
					if utf8Remaining == 0 {
						state = ansiparser.GroundState
					}
				} else {
					state, _ = ansiparser.Table.Transition(state, character)
					if state == ansiparser.Utf8State {
						switch {
						case character < 0xE0:
							utf8Remaining = 1
						case character < 0xF0:
							utf8Remaining = 2
						default:
							utf8Remaining = 3
						}
					}
				}
				if state == ansiparser.GroundState {
					break
				}
			}
			if err := writeAll(working[:count]); err != nil {
				return 0, err
			}
			working = working[count:]
		}

		if len(pending) > 0 {
			pending = append(pending, incomplete...)
			incomplete = nil
		}
		if len(incomplete) > 0 {
			pending = append(pending, incomplete...)
		}
		if len(pending) > 0 {
			if err := hold(pending); err != nil {
				return 0, err
			}
		}
		return len(data), nil
	}

	return backgroundPTYInputAdapter{write: write, flush: flush}
}

func backgroundPTYPrintableSequence(sequence []byte) bool {
	if len(sequence) == 0 {
		return false
	}
	first := sequence[0]
	return first >= ansi.SP && first < ansi.DEL || first >= 0xC2 && first <= 0xF4
}

func backgroundPTYIncompleteUTF8Suffix(data []byte) int {
	minimum := len(data) - utf8.UTFMax + 1
	if minimum < 0 {
		minimum = 0
	}
	for start := len(data) - 1; start >= minimum; start-- {
		if utf8.RuneStart(data[start]) {
			if !utf8.FullRune(data[start:]) {
				return start
			}
			break
		}
	}
	return -1
}

func backgroundPTYUTF8Prefix(data []byte, limit int) int {
	if len(data) <= limit {
		return len(data)
	}
	count := limit
	for count > 0 && count < len(data) && !utf8.RuneStart(data[count]) {
		count--
	}
	return count
}

// The parser does not enforce its documented 16-bit parameter limit. Guard
// numeric handlers against overflow-sensitive arithmetic, including the two
// intermediate-byte handlers, and use a tighter limit on work-driving loops.
// Handlers run newest first; returning false delegates to the library.
func configureBackgroundPTYParameterBounds(emulator *vt.SafeEmulator) {
	for _, prefix := range []byte{0, '<', '=', '>', '?'} {
		for _, intermediate := range []byte{0, ' ', '$'} {
			for command := byte('@'); command <= '~'; command++ {
				emulator.RegisterCsiHandler(ansi.Command(prefix, intermediate, command), backgroundPTYCSIParameterGuard)
			}
		}
	}
	for _, command := range []byte{'b', 'I', 'Z'} {
		emulator.RegisterCsiHandler(ansi.Command(0, 0, command), func(params ansi.Params) bool {
			n, _, _ := params.Param(0, 1)
			return n > backgroundPTYMaxCSIRepeat || n < -1
		})
	}
}

func backgroundPTYCSIParameterGuard(params ansi.Params) bool {
	bounded := true
	params.ForEach(-1, func(_ int, param int, _ bool) {
		if param < -1 || param > backgroundPTYMaxCSIParameter {
			bounded = false
		}
	})
	return !bounded
}

func startBackgroundPTY(cmd *exec.Cmd) (backgroundPTYRuntime, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: backgroundPTYRows,
		Cols: backgroundPTYColumns,
	})
	if err != nil {
		if errors.Is(err, pty.ErrUnsupported) {
			return backgroundPTYRuntime{}, ErrPTYNotSupported
		}
		return backgroundPTYRuntime{}, fmt.Errorf("PTY could not be started: %w", err)
	}
	emulator := vt.NewSafeEmulator(backgroundPTYColumns, backgroundPTYRows)
	emulator.SetScrollbackSize(backgroundPTYScrollbackLines)
	configureBackgroundPTYParameterBounds(emulator)
	responsePipe, ok := emulator.InputPipe().(io.Closer)
	if !ok {
		_ = terminal.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		return backgroundPTYRuntime{}, errors.New("PTY terminal response pipe is unavailable")
	}
	return backgroundPTYRuntime{
		terminal:      terminal,
		emulator:      emulator,
		emulatorInput: newBackgroundPTYInputAdapter(emulator),
		responsePipe:  responsePipe,
		writeGate:     backgroundPTYWriteGate(),
	}, nil
}

func backgroundPTYWriteGate() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}

// Write serializes complete input or emulator-response writes. A channel is
// used instead of a pointer-backed lock so both the input worker and response
// pump can share the gate through value copies.
func (runtime backgroundPTYRuntime) Write(data []byte) (int, error) {
	if runtime.writeGate == nil {
		return runtime.terminal.Write(data)
	}
	<-runtime.writeGate
	defer func() { runtime.writeGate <- struct{}{} }()
	return runtime.terminal.Write(data)
}

func (runtime backgroundPTYRuntime) writeResponse(data []byte) (int, error) {
	if runtime.responseWrite != nil {
		return runtime.responseWrite(data)
	}
	return runtime.Write(data)
}

func (runtime backgroundPTYRuntime) writeEmulator(data []byte) (int, error) {
	if runtime.emulatorInput.write != nil {
		return runtime.emulatorInput.Write(data)
	}
	return runtime.emulator.Write(data)
}

func (runtime backgroundPTYRuntime) flushEmulatorInput() error {
	return runtime.emulatorInput.Flush()
}

func (runtime backgroundPTYRuntime) startResponsePump(stop func()) <-chan error {
	done := make(chan error, 1)
	go func() {
		var result error
		defer func() {
			done <- result
			close(done)
		}()

		buffer := make([]byte, backgroundPTYResponseBufferBytes)
		for {
			count, readErr := runtime.emulator.Read(buffer)
			if count > 0 {
				written, writeErr := runtime.writeResponse(buffer[:count])
				if writeErr == nil && written != count {
					writeErr = io.ErrShortWrite
				}
				if writeErr != nil {
					result = backgroundPTYStreamFailure{cause: fmt.Errorf("%w: %w", errBackgroundPTYResponse, writeErr)}
					if stop != nil {
						stop()
					}
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrClosedPipe) {
					return
				}
				result = backgroundPTYStreamFailure{cause: fmt.Errorf("%w: %w", errBackgroundPTYResponse, readErr)}
				if stop != nil {
					stop()
				}
				return
			}
		}
	}()
	return done
}

func (runtime backgroundPTYRuntime) closeResponsePipe() error {
	// SafeEmulator.Read intentionally does not hold its mutex while waiting.
	// Close the library-owned pipe to release that reader without concurrently
	// touching the emulator's closed flag.
	return runtime.responsePipe.Close()
}

func waitForBackgroundPTYResponses(done <-chan error) error {
	if done == nil {
		return nil
	}
	timer := time.NewTimer(backgroundPTYOutputDrainTimeout)
	defer timer.Stop()
	select {
	case err, open := <-done:
		if !open {
			return nil
		}
		return err
	case <-timer.C:
		return errors.New("background PTY response writer did not finish after close")
	}
}

func reportBackgroundPTYFailure(err error, phase string) {
	diagnostics.Error("background.pty", err,
		diagnostics.Field{Key: "stage", Value: "tool"},
		diagnostics.Field{Key: "phase", Value: phase})
}
