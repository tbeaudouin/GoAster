package command

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BackgroundPtyKeyMode describes the arrow-key encoding expected by a PTY
// application. Full-screen terminal programs can switch between CSI and SS3
// modes by writing the standard smkx/rmkx sequences.
type BackgroundPtyKeyMode uint8

const (
	BackgroundPtyKeyModeCSI BackgroundPtyKeyMode = iota
	BackgroundPtyKeyModeSS3
)

const backgroundPtyKeyModeNotFound BackgroundPtyKeyMode = 255

const (
	backgroundPtyKeyModeSetSequence   = "\x1b[?1h"
	backgroundPtyKeyModeResetSequence = "\x1b[?1l"
	backgroundPtyKeyModeCarryBytes    = len(backgroundPtyKeyModeSetSequence) - 1
)

type backgroundPtyKeyModeDetector struct {
	carry string
}

var backgroundKeyMap = map[string]string{
	"enter":     "\r",
	"return":    "\r",
	"tab":       "\t",
	"escape":    "\x1b",
	"esc":       "\x1b",
	"space":     " ",
	"comma":     ",",
	"backspace": "\x7f",
	"bspace":    "\x7f",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"home":      "\x1b[1~",
	"end":       "\x1b[4~",
	"pageup":    "\x1b[5~",
	"pagedown":  "\x1b[6~",
	"pgup":      "\x1b[5~",
	"pgdn":      "\x1b[6~",
	"insert":    "\x1b[2~",
	"ic":        "\x1b[2~",
	"delete":    "\x1b[3~",
	"del":       "\x1b[3~",
	"dc":        "\x1b[3~",
	"btab":      "\x1b[Z",
	"f1":        "\x1bOP",
	"f2":        "\x1bOQ",
	"f3":        "\x1bOR",
	"f4":        "\x1bOS",
	"f5":        "\x1b[15~",
	"f6":        "\x1b[17~",
	"f7":        "\x1b[18~",
	"f8":        "\x1b[19~",
	"f9":        "\x1b[20~",
	"f10":       "\x1b[21~",
	"f11":       "\x1b[23~",
	"f12":       "\x1b[24~",
}

var backgroundSS3KeyMap = map[string]string{
	"up":    "\x1bOA",
	"down":  "\x1bOB",
	"right": "\x1bOC",
	"left":  "\x1bOD",
	"home":  "\x1bOH",
	"end":   "\x1bOF",
}

// EncodeBackgroundKeySequence converts comma-separated named non-secret
// terminal keys into bytes suitable for an allowlisted interactive session. It
// accepts named keys, small modifier forms, and one printable character per
// comma-delimited token. Pasted text and raw control bytes are not accepted;
// neither this path nor Input is a secret-safe channel.
func EncodeBackgroundKeySequence(keys string, mode BackgroundPtyKeyMode) (string, error) {
	if strings.TrimSpace(keys) == "" {
		return "", fmt.Errorf("keys cannot be empty")
	}
	if len(keys) > MaxSessionInputBytes {
		return "", fmt.Errorf("keys exceed the %d-byte limit", MaxSessionInputBytes)
	}

	var encoded strings.Builder
	for _, token := range strings.Split(keys, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		sequence, err := encodeBackgroundKeyToken(token, mode)
		if err != nil {
			return "", err
		}
		if encoded.Len()+len(sequence) > MaxSessionInputBytes {
			return "", fmt.Errorf("encoded keys exceed the %d-byte limit", MaxSessionInputBytes)
		}
		encoded.WriteString(sequence)
	}
	if encoded.Len() == 0 {
		return "", fmt.Errorf("keys cannot be empty")
	}
	return encoded.String(), nil
}

func encodeBackgroundKeyToken(token string, mode BackgroundPtyKeyMode) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	normalized := strings.ToLower(token)

	for _, prefix := range []string{"c-", "ctrl-"} {
		if strings.HasPrefix(normalized, prefix) {
			character := token[len(prefix):]
			if utf8.RuneCountInString(character) != 1 {
				return "", fmt.Errorf("invalid ctrl key")
			}
			runeValue, _ := utf8.DecodeRuneInString(character)
			if runeValue >= 'A' && runeValue <= 'Z' {
				runeValue += 'a' - 'A'
			}
			if runeValue < 'a' || runeValue > 'z' {
				return "", fmt.Errorf("invalid ctrl key")
			}
			return string(runeValue & 0x1f), nil
		}
	}

	for _, prefix := range []string{"m-", "alt-"} {
		if strings.HasPrefix(normalized, prefix) {
			character := token[len(prefix):]
			if !validBackgroundPrintableKey(character) {
				return "", fmt.Errorf("invalid alt key")
			}
			return "\x1b" + character, nil
		}
	}

	for _, prefix := range []string{"s-", "shift-"} {
		if strings.HasPrefix(normalized, prefix) {
			key := normalized[len(prefix):]
			sequence, ok := backgroundKeyMap[key]
			if !ok {
				return "", fmt.Errorf("unknown key with shift")
			}
			if len(sequence) != 1 {
				return "", fmt.Errorf("unsupported shift key")
			}
			return strings.ToUpper(sequence), nil
		}
	}

	if mode == BackgroundPtyKeyModeSS3 {
		if sequence, ok := backgroundSS3KeyMap[normalized]; ok {
			return sequence, nil
		}
	}
	if sequence, ok := backgroundKeyMap[normalized]; ok {
		return sequence, nil
	}
	if validBackgroundPrintableKey(token) {
		return token, nil
	}
	return "", fmt.Errorf("unknown terminal key (use a named key or one printable character)")
}

func validBackgroundPrintableKey(token string) bool {
	if !utf8.ValidString(token) || utf8.RuneCountInString(token) != 1 {
		return false
	}
	runeValue, _ := utf8.DecodeRuneInString(token)
	return runeValue != ',' && unicode.IsPrint(runeValue)
}

func detectBackgroundPtyKeyMode(raw string) BackgroundPtyKeyMode {
	lastSMKX := strings.LastIndex(raw, backgroundPtyKeyModeSetSequence)
	lastRMKX := strings.LastIndex(raw, backgroundPtyKeyModeResetSequence)
	if lastSMKX == -1 && lastRMKX == -1 {
		return backgroundPtyKeyModeNotFound
	}
	if lastSMKX > lastRMKX {
		return BackgroundPtyKeyModeSS3
	}
	return BackgroundPtyKeyModeCSI
}

func observeBackgroundPtyKeyMode(detector backgroundPtyKeyModeDetector, content []byte) (backgroundPtyKeyModeDetector, BackgroundPtyKeyMode) {
	// Scan the whole read before retaining only the suffix needed to join a
	// sequence split across reads. Trimming before scanning can discard a valid
	// mode switch when a PTY emits it near the start of a large chunk.
	raw := detector.carry + string(content)
	detected := detectBackgroundPtyKeyMode(raw)
	if len(raw) > backgroundPtyKeyModeCarryBytes {
		detector.carry = raw[len(raw)-backgroundPtyKeyModeCarryBytes:]
	} else {
		detector.carry = raw
	}
	return detector, detected
}
