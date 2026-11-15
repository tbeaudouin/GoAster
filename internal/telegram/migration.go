package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"goaster/internal/diagnostics"
)

const migrationHandoffFilename = "goaster-migration-handoff.txt"
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

func sendMigrationHandoffTo(ctx context.Context, paths Paths, client httpDoer, apiBase string, userID int64, data []byte) error {
	if len(data) == 0 || int64(len(data)) > migrationHandoffMaxBytes {
		return errors.New("migration handoff is unavailable")
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		diagnostics.Error("telegram.migration_delivery", err, diagnostics.Field{Key: "method", Value: "sendDocument"})
		return errors.New("migration handoff is unavailable")
	}
	if err := sendDocument(ctx, client, apiBase, string(token), userID, migrationHandoffFilename, data); err != nil {
		return errors.New("migration handoff delivery failed")
	}
	return nil
}
