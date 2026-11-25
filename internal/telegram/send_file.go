package telegram

import (
	"context"
	"errors"
	"strings"

	"goaster/internal/chat"
	"goaster/internal/diagnostics"
)

const telegramSendFileMaxBytes int64 = 50_000_000

// telegramSendFileError keeps the underlying cause available to the runtime
// while exposing only a fixed, secret-free message to callers.
type telegramSendFileError struct{ cause error }

func (err telegramSendFileError) Error() string {
	return "Telegram file delivery failed; delivery was not confirmed"
}

func (err telegramSendFileError) Unwrap() error { return err.cause }

// SendFile delivers one bounded file through the configured Telegram bot.
// The caller supplies only transport-bound destination values; model
// arguments never select the chat, forum topic, or bot token.
func SendFile(ctx context.Context, chatID, threadID int64, filename string, data []byte) error {
	return sendFileWith(ctx, DefaultPaths, migrationTelegramClient(), "https://api.telegram.org", chatID, threadID, filename, data)
}

func sendFileWith(ctx context.Context, paths Paths, client httpDoer, apiBase string, chatID, threadID int64, filename string, data []byte) error {
	if !telegramSendFileSizeAllowed(int64(len(data))) {
		return telegramSendFileConfigurationFailure(errors.New("Telegram send_file exceeds the size limit"))
	}
	if ctx == nil || chatID == 0 || threadID < 0 || strings.TrimSpace(filename) == "" {
		return telegramSendFileConfigurationFailure(errors.New("invalid Telegram file delivery request"))
	}
	token, err := readPrivate(paths.Token)
	if err != nil {
		return telegramSendFileConfigurationFailure(err)
	}
	defer clear(token)
	if err := ValidateToken(string(token)); err != nil {
		return telegramSendFileConfigurationFailure(err)
	}
	if err := sendDocumentWithThread(ctx, client, apiBase, string(token), chatID, threadID, filename, data, diagnostics.Field{Key: "phase", Value: "dispatch"}); err != nil {
		return telegramSendFileError{cause: err}
	}
	return nil
}

func telegramSendFileSizeAllowed(size int64) bool {
	return size >= 0 && size <= telegramSendFileMaxBytes
}

func telegramSendFileConfigurationFailure(err error) error {
	if err == nil {
		err = errors.New("Telegram file delivery configuration is unavailable")
	}
	logged := telegramFailure("sendDocument", err, "", 0, "", diagnostics.Field{Key: "phase", Value: "dispatch"})
	return telegramSendFileError{cause: logged}
}

var _ chat.SendFileOperation = SendFile
