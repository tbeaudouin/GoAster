package chat

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"goaster/internal/migration"
)

const (
	maxMigrationDiagnostics         = 8
	maxMigrationDiagnosticBytes     = 8 << 10
	migrationDiagnosticNoticePrefix = "App-generated migration diagnostic metadata (safe fields only; not a generic tool result or an assistant claim). Preserve the recorded creation outcome and uncertainty; do not repeat a create solely to reconstruct an omitted result.\n"
)

// MigrationDiagnostic is the small safe metadata record retained for a
// migration tool call. It deliberately excludes generic tool output, tool
// arguments, setup material, credentials, and resource details.
type MigrationDiagnostic struct {
	CreationState   string                    `json:"creation_state"`
	Code            string                    `json:"code,omitempty"`
	Stage           string                    `json:"stage,omitempty"`
	HTTPStatus      int                       `json:"http_status,omitempty"`
	ProviderCode    string                    `json:"provider_code,omitempty"`
	ProviderMessage string                    `json:"provider_message,omitempty"`
	ProviderFields  []migration.ProviderField `json:"provider_fields,omitempty"`
	Handoff         string                    `json:"handoff,omitempty"`
}

type migrationDiagnosticObserverKey struct{}

// WithMigrationDiagnosticObserver lets a session checkpoint only the safe
// result of a migration tool call before the model continues. The callback is
// internal runtime plumbing; it never exposes generic tool results.
func WithMigrationDiagnosticObserver(ctx context.Context, observer func(string) error) context.Context {
	return context.WithValue(ctx, migrationDiagnosticObserverKey{}, observer)
}

func migrationDiagnosticObserver(ctx context.Context) func(string) error {
	observer, _ := ctx.Value(migrationDiagnosticObserverKey{}).(func(string) error)
	return observer
}

func migrationDiagnosticFromToolOutput(output string) (MigrationDiagnostic, bool) {
	if len(output) == 0 || len(output) > maxMigrationDiagnosticBytes {
		return MigrationDiagnostic{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	var result migration.OneShotResult
	if err := decoder.Decode(&result); err != nil {
		return MigrationDiagnostic{}, false
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return MigrationDiagnostic{}, false
	}
	return migrationDiagnosticFromResult(result)
}

func migrationDiagnosticFromResult(result migration.OneShotResult) (MigrationDiagnostic, bool) {
	safe := migration.SanitizeOneShotResult(result)
	if safe.CreationState == "" {
		return MigrationDiagnostic{}, false
	}
	return MigrationDiagnostic{
		CreationState:   safe.CreationState,
		Code:            safe.Code,
		Stage:           safe.Stage,
		HTTPStatus:      safe.HTTPStatus,
		ProviderCode:    safe.ProviderCode,
		ProviderMessage: safe.ProviderMessage,
		ProviderFields:  cloneProviderFields(safe.ProviderFields),
		Handoff:         safe.Handoff,
	}, true
}

func normalizeMigrationDiagnostic(value MigrationDiagnostic) (MigrationDiagnostic, bool) {
	return migrationDiagnosticFromResult(migration.OneShotResult{
		CreationState:   value.CreationState,
		Code:            value.Code,
		Stage:           value.Stage,
		HTTPStatus:      value.HTTPStatus,
		ProviderCode:    value.ProviderCode,
		ProviderMessage: value.ProviderMessage,
		ProviderFields:  value.ProviderFields,
		Handoff:         value.Handoff,
	})
}

func normalizeMigrationDiagnostics(values []MigrationDiagnostic) ([]MigrationDiagnostic, bool) {
	trimmed := false
	if len(values) > maxMigrationDiagnostics {
		values = values[len(values)-maxMigrationDiagnostics:]
		trimmed = true
	}
	result := make([]MigrationDiagnostic, 0, len(values))
	for _, value := range values {
		safe, ok := normalizeMigrationDiagnostic(value)
		if !ok {
			trimmed = true
			continue
		}
		if !migrationDiagnosticEqual(value, safe) {
			trimmed = true
		}
		result = append(result, safe)
	}
	return result, trimmed
}

func appendMigrationDiagnostic(values []MigrationDiagnostic, value MigrationDiagnostic) []MigrationDiagnostic {
	safe, ok := normalizeMigrationDiagnostic(value)
	if !ok {
		return cloneMigrationDiagnostics(values)
	}
	result := cloneMigrationDiagnostics(values)
	result = append(result, safe)
	if len(result) > maxMigrationDiagnostics {
		result = result[len(result)-maxMigrationDiagnostics:]
	}
	return result
}

func migrationDiagnosticEqual(first, second MigrationDiagnostic) bool {
	if first.CreationState != second.CreationState || first.Code != second.Code || first.Stage != second.Stage || first.HTTPStatus != second.HTTPStatus || first.ProviderCode != second.ProviderCode || first.ProviderMessage != second.ProviderMessage || first.Handoff != second.Handoff || len(first.ProviderFields) != len(second.ProviderFields) {
		return false
	}
	for index := range first.ProviderFields {
		left, right := first.ProviderFields[index], second.ProviderFields[index]
		if left.Field != right.Field || len(left.Messages) != len(right.Messages) {
			return false
		}
		for messageIndex := range left.Messages {
			if left.Messages[messageIndex] != right.Messages[messageIndex] {
				return false
			}
		}
	}
	return true
}

func migrationDiagnosticsEqual(first, second []MigrationDiagnostic) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if !migrationDiagnosticEqual(first[index], second[index]) {
			return false
		}
	}
	return true
}

func cloneProviderFields(values []migration.ProviderField) []migration.ProviderField {
	result := make([]migration.ProviderField, 0, len(values))
	for _, value := range values {
		result = append(result, migration.ProviderField{Field: value.Field, Messages: append([]string(nil), value.Messages...)})
	}
	return result
}

func cloneMigrationDiagnostics(values []MigrationDiagnostic) []MigrationDiagnostic {
	result := make([]MigrationDiagnostic, 0, len(values))
	for _, value := range values {
		value.ProviderFields = cloneProviderFields(value.ProviderFields)
		result = append(result, value)
	}
	return result
}

func trimMigrationDiagnosticDetails(value MigrationDiagnostic) (MigrationDiagnostic, bool) {
	if len(value.ProviderFields) > 0 {
		value.ProviderFields = nil
		return value, true
	}
	if value.ProviderMessage != "" {
		value.ProviderMessage = ""
		return value, true
	}
	if value.ProviderCode != "" {
		value.ProviderCode = ""
		return value, true
	}
	return value, false
}

func migrationDiagnosticsNoticeData(values []MigrationDiagnostic) ([]byte, bool) {
	data, err := json.Marshal(values)
	if err != nil || len(migrationDiagnosticNoticePrefix)+len(data) > maxMigrationDiagnosticBytes {
		return nil, false
	}
	return data, true
}

// migrationDiagnosticsForNotice preserves the newest outcome while making the
// complete app-generated notice fit its byte budget. Older detail fields are
// removed before older records are discarded; a final record is reduced to
// its creation state only if the bounded fields still cannot fit.
func migrationDiagnosticsForNotice(values []MigrationDiagnostic) []MigrationDiagnostic {
	safe, _ := normalizeMigrationDiagnostics(values)
	for len(safe) > 0 {
		if _, ok := migrationDiagnosticsNoticeData(safe); ok {
			return safe
		}
		trimmed := false
		for index := range safe {
			next, changed := trimMigrationDiagnosticDetails(safe[index])
			if changed {
				safe[index] = next
				trimmed = true
				break
			}
		}
		if trimmed {
			continue
		}
		if len(safe) > 1 {
			safe = safe[1:]
			continue
		}
		latest := safe[len(safe)-1]
		safe = []MigrationDiagnostic{{CreationState: latest.CreationState}}
	}
	return nil
}

func migrationDiagnosticsMessage(values []MigrationDiagnostic) string {
	safe := migrationDiagnosticsForNotice(values)
	if len(safe) == 0 {
		return ""
	}
	data, ok := migrationDiagnosticsNoticeData(safe)
	if !ok {
		return ""
	}
	return migrationDiagnosticNoticePrefix + string(data)
}
