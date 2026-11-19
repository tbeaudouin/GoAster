# Background command validation (2026-11-19)

## Scope and result

This change is limited to `background_command` compatibility when a model interface sends every declared string property. The registered object schema is unchanged: `action` is required, and `name`, `id`, `data`, and `keys` remain optional strings. The tool description now explains that empty strings in fields unused by the selected action are accepted as absent.

The handler applies the same rule across all actions: only an exactly empty irrelevant string is ignored, while any raw nonempty forbidden value, including whitespace-only `name` or `id`, returns `invalid_request` with `forbidden_fields`. Required command-name and session-ID checks retain their existing trimming behavior, so whitespace-only values are still treated as missing when those fields are required. `input`/`write` still require that `data` be supplied (an explicit empty value retains its previous behavior); `send-keys` requires nonempty supported key tokens, including named keys, modifier forms, or one printable character per token. Parsing still rejects malformed/non-object JSON, duplicate or unknown fields, non-string values including null, trailing data, oversized values, and missing action. Allowlisting, input and secret warnings, execution controls, and no-replay behavior were not changed.

## Regression coverage

- An action matrix covers all 11 actions with all five declared string fields present, checking that irrelevant empty fields pass and both ordinary nonempty and whitespace-only forbidden values fail without echoing their value.
- Validation cases retain missing action/name/ID/data and empty send-keys failures, including whitespace-only required name and ID values.
- A local fake Codex provider checks the registered model-facing schema, sends a `list` call with every declared field populated and empty irrelevant placeholders, then verifies the successful tool output reaches the follow-up request.
- The command-manager fake-helper and tool end-to-end tests use a generic non-GitHub `legacy-keys` PTY with `keys_only: true`; both reject raw `input`/`write`, then send `up,down,left,right,y,N,comma` and assert that the helper received exactly `\x1b[A\x1b[B\x1b[D\x1b[CyN,`. They also require completed `exited` state, `exit_code: 0`, and no `input_failed`; the helper is local test code and never runs GitHub/login or receives a secret.
- The tool-description expectation preserves generic opt-in non-secret `input`/`write` while scoping the prohibition on those actions to the bundled `github-login` profile.
- Existing parser, allowlist, input, privacy, and unexpected-failure regressions continue to run in the full suite.

## Verification and gaps

Passing offline verification used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` with Go 1.25.9 at `/usr/local/go/bin/go`.

- `go test ./... -count=1 -timeout 300s` (including the background-command and fake-provider regressions): PASS.
- `go vet ./...`: PASS.

The provider-flow regression uses only a local fake HTTP server and a read-only `list` action; it does not contact a live model or exercise a side-effecting tool call. Existing input policy is guidance and command/profile validation, not automatic secret detection; callers must continue to follow the non-secret input restrictions.

## Independent final verification

Parent independently reran the full offline suite, `go vet ./...`, focused `go test -race ./internal/chat ./internal/command -count=1 -timeout 300s`, and `git diff --check`: PASS. Terra review found forbidden whitespace name/ID handling; fixed with raw nonempty flags and regression cases. Subsequent Terra review and final Sol review: SATISFIED. This is source-only validation: running-agent rollout and a real provider/GitHub login remain unverified.

## Current universal PTY key-policy scope (2026-11-19)

This delivery changes only the key policy for all allowed, eligible interactive
PTYs. Every existing named terminal key remains available, and each
comma-delimited token may also be one printable character with case preserved.
Use the named `comma` key for a literal comma. `keys_only` only disables raw
`input`/`write` for backward compatibility; it never restricts `send-keys`.
There is no GitHub-specific key exception and no new configuration knob.

The existing allowlist, command-name-only selection, interactive/PTY checks,
direct execution, bounds, and no-secrets protections remain in force. Empty
keys, unknown or pasted multi-character tokens, raw control/ANSI bytes,
invalid modifier forms, and encoded sequences over `MaxSessionInputBytes` are
rejected. The bundled `github-login` profile remains fixed and keeps
`input`/`write` unavailable because of the same `keys_only` policy.

The exact-text authentication and tool-description regressions were updated,
and focused coverage verifies generic legacy `keys_only` profiles reject raw
input/write while the PTY helper receives the exact arrow, lowercase,
uppercase, and named-comma bytes above and exits successfully. The separate
terminal-stall fix below does not change this key policy or the environment
override behavior.

## Separate background PTY terminal-stall fix (2026-11-19)

The PTY path now calls `github.com/creack/pty.StartWithSize` with a fixed
80-column by 24-row `Winsize` before the child starts. The same dimensions are
given to the pinned `github.com/charmbracelet/x/vt` v0.1.0 `SafeEmulator`, so
the kernel's initial size and the emulator's cursor state agree. Its screen
state keeps only one scrollback line, while the maintained emulator/parser
handles CSI 6n cursor-position reports (and DECXCPR), including
display-width-aware graphemes; no homegrown cursor tracker was added.

The PTY reader now places a bounded stream adapter in front of
`SafeEmulator.Write`. It uses the pinned `x/ansi` decoder for ANSI state and
`uniseg` for UAX-29 segmentation, including ASCII-led graphemes, to hold only the final printable tail, so a regional
indicator or ZWJ sequence split across reads is submitted to `x/vt` as one
grapheme. The adapter carries at most 256 bytes of tail, including an
incomplete UTF-8 suffix, and carries ANSI parser state rather than buffering
CSI/OSC payloads. Complete escape/control bytes, including terminal queries,
are forwarded immediately; a pending printable tail is flushed before them
and once more when the PTY reader reaches EOF. A single grapheme longer than
256 bytes is committed in valid-UTF-8 chunks at that bound; such pathological
clusters are the documented finite display-width gap. The fixed tail bound
also makes repeated short PTY reads bounded linear work instead of an
unbounded or quadratic re-segmentation.

Raw PTY bytes still go through the existing bounded output capture, including
the existing `RedactOutput` behavior. Emulator replies are drained by a
separate 4096-byte response pump. Its library response pipe is closed only
after the PTY reader has drained or hit the existing bounded cleanup deadline;
normal PTY read EOF/Linux EIO remains expected. Every response write failure,
including a close-shaped failure during cleanup, retains its wrapped cause and
uses the redacted `background.pty` diagnostic boundary. A close-induced write
failure is also reported because its cause cannot safely be distinguished from
an independent failure. Parser failures use the same diagnostic boundary.
The asynchronous PTY owner publishes the complete failure tree, while the
manager receives a separate shutdown-owned cleanup subset. A stream failure
already diagnosed by `background.pty` therefore does not become a second
shutdown report; independent cleanup failures remain in the manager close
result. Session/status consumers receive only the fixed safe stream-failure
message, while direct Go callers retain `errors.Is` access to the underlying
cause. PTY creation failures retain their wrapped causes and are diagnosed once
by the chat start owner; the lower PTY layer does not duplicate that record. The
existing allowlist, universal key encoding, input queue, privacy, and session
lifecycle behavior are unchanged. Complete input and emulator-response writes
share a small channel gate, so overlapping writes cannot interleave bytes.

The pinned `x/ansi` parser's parameter-count limit does not limit numeric
parameter values, and the pinned `x/vt` handlers for REP (`CSI ... b`) and tab
movement (`CSI ... I`/`Z`) loop over those values. Supported `RegisterCsiHandler`
hooks are installed after the library defaults for every CSI prefix/final; a
numeric parameter over 65535 is consumed as a bounded no-op before any library
numeric handler runs, while REP/tab counts over 4096 are bounded by their
tighter work limit. This keeps hostile terminal output from driving
billion-iteration work without adding a cursor tracker or terminal parser.

Offline realistic PTY coverage adds a local test-binary child that:

- reads `stty size` and proves the child sees `24 80`;
- emits `界`, then sends `ESC`, `[6`, and `n` in separate writes, verifies the
  emulator response `ESC[1;3R`, and reaches a progress marker;
- directly feeds the adapter and emulator unsplit, every two-part byte split,
  and byte-at-a-time regional indicators, ZWJ, ASCII combining, and keycap
  sequences and checks their exact CPR replies;
- runs an actual Linux PTY child that compares unsplit and timed, read-split
  `🇫🇷` and `👩‍💻` sequences; all four CPR replies are `ESC[1;3R`;
- directly checks partial UTF-8, split CSI/OSC control strings, immediate
  escape forwarding, and that every long-grapheme adapter write is at most
  the 256-byte tail limit;
- repeats CPR/progress while `send-keys` and `stop` run concurrently;
- repeats the handshake with `RedactOutput` and requires empty captured output;
- emits hostile billion-valued REP/CHT/CBT parameters, waits for a CPR response,
  and requires bounded progress both normally and with `RedactOutput`;
- queues a multi-byte `up` sequence alongside CPR and accepts only two complete
  non-interleaved byte sequences in either order;
- bounds an unclosed response-pump wait and a blocked response writer, while
  retaining the existing PTY/input failure and redaction regressions;
- injects private response-writer failures through the manager, including one
  independent close-shaped failure before the underlying terminal closes,
  verifies each complete wrapped cause and safe redacted session/output boundary, and proves that the
  already-owned `background.pty` diagnostic has no duplicate shutdown report
  while an independent cleanup error remains retainable;
- verifies that an actual close-induced response write failure is retained;
- verifies a PTY create error still wraps `os.ErrNotExist`, and the chat start
  path emits one safe diagnostic without the executable path or argument text.

The only pointer exception is at the terminal-library boundary: `StartWithSize`
requires `*pty.Winsize`, and `NewSafeEmulator` returns `*vt.SafeEmulator`; the
response-pipe closer is obtained through the library's `io.Writer` boundary.
No pointer-based cursor state or root-rule relaxation was introduced.

Offline verification used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` with Go
1.25.9 at `/usr/local/go/bin/go`:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat ./internal/command -count=1 -timeout 300s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/command -count=1 -timeout 240s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/command -run '^TestBackgroundPTYUnicodeCPR$' -count=1 -timeout 60s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/command -count=1 -timeout 300s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go mod verify`: PASS.
- `git diff --check`: PASS.

Gaps are deliberate: the tests are offline and use only the local test binary
and Linux PTY support; non-Linux retains the existing `ErrPTYNotSupported`
behavior. A UAX-29 grapheme longer than 256 bytes can be committed in more
than one emulator write, so that pathological case is bounded rather than
fully preserved. This change configures only the initial fixed size and does
not add a resize API or test live full-screen applications beyond the
library-backed CPR handshake. No provider login, GitHub credential use, cloud
action, deployment, runtime upgrade, production rollout, or running-agent
acceptance was performed.
