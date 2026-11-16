package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goaster/internal/diagnostics"
	"goaster/internal/migration"
)

const migrationHandoffFilename = "goaster-migration-handoff.txt"
const migrationPrivateKeyFilename = "id_ed25519"
const migrationHandoffMaxBytes int64 = 32 * 1024

func migrationTelegramClient() httpDoer {
	return &http.Client{
		Timeout:       40 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: noRedirect,
	}
}

// PrepareMigrationHandoff fixes the paired recipient before a create request.
func PrepareMigrationHandoff() (func(context.Context, []byte) error, error) {
	return prepareMigrationHandoffWith(DefaultPaths, migrationTelegramClient(), "https://api.telegram.org")
}

// PrepareMigrationHandoffParts fixes the paired recipient before an active
// one-shot create. The setup URL and private key are delivered separately.
func PrepareMigrationHandoffParts() (func(context.Context, string, []byte) error, error) {
	return prepareMigrationHandoffPartsWith(DefaultPaths, migrationTelegramClient(), "https://api.telegram.org")
}

func MigrationRecipient() (int64, error) { return migrationRecipient(DefaultPaths) }

func migrationRecipient(paths Paths) (int64, error) {
	data, err := readPrivate(paths.UserID)
	if err != nil {
		return 0, errors.Join(errors.New("migration paired user is unavailable"), err)
	}
	defer clear(data)
	id, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.Join(errors.New("migration paired user is unavailable"), err)
	}
	return id, nil
}

func PrepareRecipientMigrationHandoff(expected int64) (func(context.Context, []byte) error, error) {
	return prepareRecipientMigrationHandoffWith(DefaultPaths, migrationTelegramClient(), "https://api.telegram.org", expected)
}

func PrepareRecipientMigrationHandoffParts(expected int64) (func(context.Context, string, []byte) error, error) {
	return PrepareRecipientMigrationHandoffPartsWith(DefaultPaths, migrationTelegramClient(), "https://api.telegram.org", expected)
}

func prepareRecipientMigrationHandoffWith(paths Paths, client httpDoer, apiBase string, expected int64) (func(context.Context, []byte) error, error) {
	current, err := migrationRecipient(paths)
	if err != nil || expected <= 0 || current != expected {
		return nil, errors.Join(errors.New("migration paired user changed or is unavailable"), err)
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	return func(ctx context.Context, data []byte) error {
		current, err := migrationRecipient(paths)
		if err != nil || current != expected {
			return errors.Join(errors.New("migration paired user changed or is unavailable"), err)
		}
		return sendMigrationHandoffTo(ctx, paths, client, apiBase, expected, data)
	}, nil
}

// PrepareRecipientMigrationHandoffPartsWith configures the split handoff transport.
func PrepareRecipientMigrationHandoffPartsWith(paths Paths, client httpDoer, apiBase string, expected int64) (func(context.Context, string, []byte) error, error) {
	current, err := migrationRecipient(paths)
	if err != nil || expected <= 0 || current != expected {
		return nil, errors.Join(errors.New("migration paired user changed or is unavailable"), err)
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	return func(ctx context.Context, setupURL string, privateKey []byte) error {
		return sendMigrationHandoffPartsTo(ctx, paths, client, apiBase, expected, setupURL, privateKey)
	}, nil
}

func prepareMigrationHandoffWith(paths Paths, client httpDoer, apiBase string) (func(context.Context, []byte) error, error) {
	userData, err := readPrivate(paths.UserID)
	if err != nil {
		return nil, errors.New("migration paired user is unavailable")
	}
	defer clear(userData)
	userID, err := strconv.ParseInt(string(userData), 10, 64)
	if err != nil || userID <= 0 {
		return nil, errors.New("migration paired user is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	return func(ctx context.Context, data []byte) error {
		return sendMigrationHandoffTo(ctx, paths, client, apiBase, userID, data)
	}, nil
}

func prepareMigrationHandoffPartsWith(paths Paths, client httpDoer, apiBase string) (func(context.Context, string, []byte) error, error) {
	userData, err := readPrivate(paths.UserID)
	if err != nil {
		return nil, errors.New("migration paired user is unavailable")
	}
	defer clear(userData)
	userID, err := strconv.ParseInt(string(userData), 10, 64)
	if err != nil || userID <= 0 {
		return nil, errors.New("migration paired user is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return nil, errors.Join(errors.New("migration Telegram token is unavailable"), err)
	}
	return func(ctx context.Context, setupURL string, privateKey []byte) error {
		return sendMigrationHandoffPartsTo(ctx, paths, client, apiBase, userID, setupURL, privateKey)
	}, nil
}

// deliveryError keeps the underlying cause while indicating that Telegram owns the diagnostic.
type deliveryError struct{ cause error }

func (err deliveryError) Error() string     { return "migration handoff delivery failed" }
func (err deliveryError) Unwrap() error     { return err.cause }
func (err deliveryError) DiagnosticLogged() {}

func sendMigrationHandoffTo(ctx context.Context, paths Paths, client httpDoer, apiBase string, userID int64, data []byte) error {
	if len(data) == 0 || int64(len(data)) > migrationHandoffMaxBytes {
		return errors.New("migration handoff is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return deliveryError{cause: err}
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return deliveryError{cause: err}
	}
	if err := sendDocument(ctx, client, apiBase, string(token), userID, migrationHandoffFilename, data, diagnostics.Field{Key: "stage", Value: "handoff"}, diagnostics.Field{Key: "creation_state", Value: "created"}, diagnostics.Field{Key: "code", Value: "handoff_failure"}); err != nil {
		return deliveryError{cause: err}
	}
	return nil
}

func sendMigrationHandoffPartsTo(ctx context.Context, paths Paths, client httpDoer, apiBase string, userID int64, setupURL string, privateKey []byte) error {
	if strings.TrimSpace(setupURL) == "" || len(privateKey) == 0 || int64(len(privateKey)) > migrationHandoffMaxBytes {
		return errors.New("migration split handoff is unavailable")
	}
	current, err := migrationRecipient(paths)
	if err != nil || current != userID {
		return errors.Join(errors.New("migration paired user changed or is unavailable"), err)
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendMessage"})
		return deliveryError{cause: err}
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendMessage"})
		return deliveryError{cause: err}
	}
	sender := adapter{client: client, apiBase: apiBase, token: string(token)}
	if _, err := sender.sendMessage(ctx, userID, setupURL); err != nil {
		return deliveryError{cause: err}
	}
	current, err = migrationRecipient(paths)
	if err != nil || current != userID {
		return errors.Join(errors.New("migration paired user changed or is unavailable"), err)
	}
	if err := sendDocument(ctx, client, apiBase, string(token), userID, migrationPrivateKeyFilename, privateKey, diagnostics.Field{Key: "stage", Value: "handoff"}, diagnostics.Field{Key: "creation_state", Value: "created"}, diagnostics.Field{Key: "code", Value: "handoff_failure"}); err != nil {
		return deliveryError{cause: err}
	}
	return nil
}

// ReportMigrationOutcome sends a fixed safe status message to the paired
// private owner. It never includes the setup URL, private key, token, or
// request body.
func ReportMigrationOutcome(ctx context.Context, owner int64, result migration.OneShotResult) error {
	return reportMigrationOutcomeWith(ctx, DefaultPaths, migrationTelegramClient(), "https://api.telegram.org", owner, result)
}

func reportMigrationOutcomeWith(ctx context.Context, paths Paths, client httpDoer, apiBase string, owner int64, result migration.OneShotResult) error {
	current, err := migrationRecipient(paths)
	if err != nil || owner <= 0 || current != owner {
		return reportOutcomeFailure(errors.Join(errors.New("migration paired user changed or is unavailable"), err))
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return reportOutcomeFailure(err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return reportOutcomeFailure(err)
	}
	sender := adapter{client: client, apiBase: apiBase, token: string(token)}
	if _, err := sender.sendMessage(ctx, owner, migrationOutcomeText(result)); err != nil {
		return deliveryError{cause: err}
	}
	return nil
}

func reportOutcomeFailure(err error) error {
	diagnostics.Error("telegram.migration_outcome", err, diagnostics.Field{Key: "method", Value: "sendMessage"})
	return deliveryError{cause: err}
}

func migrationOutcomeText(result migration.OneShotResult) string {
	if result.CreationState == "not_created" && result.Code == "target_exists" {
		return "Existing GoAster server found for the exact requested name " + result.ServerName + ". No new server was created; this does not mean setup or SSH private-key material was recovered."
	}
	if result.CreationState == "not_created" && result.Code == "no_capacity" {
		return "Migration capacity is not available yet for " + result.ServerName + ". No server was created."
	}
	if result.CreationState == "created" && result.Handoff == "delivered" {
		return "Migration " + result.ServerName + " was created. The setup link and SSH private-key attachment were sent separately."
	}
	if result.CreationState == "created" {
		return "Migration created, but private handoff delivery was incomplete. It will not be retried automatically."
	}
	if result.CreationState == "unknown" {
		return "Migration result is uncertain. It will not be retried automatically; reconcile the provider state before any new attempt."
	}
	return "Migration was not created."
}
