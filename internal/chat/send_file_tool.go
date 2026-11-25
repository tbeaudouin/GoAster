package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"goaster/internal/attachment"
	"goaster/internal/diagnostics"
)

const (
	sendFileToolName               = "send_file"
	sendFileMaxBytes         int64 = 50_000_000
	sendFileMaxFilenameBytes       = 180
)

var sendFileCodexTool = codexTool{
	Type:        "function",
	Name:        sendFileToolName,
	Description: "Send a regular file from GoAster's private artefact workspace to the current authenticated Telegram conversation. The destination and exact forum topic come from the current transport-bound conversation, never from tool arguments. Binary files such as ZIP archives are supported; symbolic links, paths outside the workspace, directories, FIFOs, other non-regular files, and files larger than 50,000,000 bytes (50 MB) are rejected. An optional filename must be a safe basename.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path relative to the GoAster artefact workspace (or an absolute path inside it).",
			},
			"filename": map[string]any{
				"type":        "string",
				"maxLength":   sendFileMaxFilenameBytes,
				"description": "Optional safe basename to show in Telegram; defaults to the source file basename.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	},
}

// SendFileOperation is the fixed delivery capability used by the model-facing
// send_file tool. Destination values come from the current request context.
type SendFileOperation func(context.Context, int64, int64, string, []byte) error

// RegisterSendFileTool exposes bounded regular-file delivery to a configured
// Telegram sender. It never accepts a destination or credential as an
// argument; those are supplied by the transport and runtime.
func RegisterSendFileTool(registrar ToolRegistrar, send SendFileOperation) error {
	if registrar == nil {
		return errors.New("send_file tool registrar is nil")
	}
	if send == nil {
		return errors.New("send_file operation is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        sendFileCodexTool.Name,
		Description: sendFileCodexTool.Description,
		Parameters:  sendFileCodexTool.Parameters,
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeSendFile(ctx, arguments, send)
		},
	})
}

func executeSendFile(ctx context.Context, arguments string, send SendFileOperation) (string, error) {
	if ctx == nil {
		return "send_file unavailable: the current Telegram conversation context is missing.", nil
	}
	if send == nil {
		return "send_file unavailable: Telegram delivery is not configured.", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(sendFileToolName, arguments)
	if err != nil {
		return sendFileArgumentFailure(err), nil
	}
	if !sendFileUniqueArguments(arguments) {
		return sendFileArgumentFailure(errors.New("duplicate argument")), nil
	}
	pathName, err := fileToolStringArgument(args, sendFileToolName, "path", true)
	if err != nil || strings.TrimSpace(pathName) == "" {
		return "send_file failed: path must be a non-empty string.", nil
	}
	filename, err := fileToolStringArgument(args, sendFileToolName, "filename", false)
	if err != nil {
		return "send_file failed: filename must be a safe string.", nil
	}
	if _, supplied := args["filename"]; supplied {
		if err := validateSendFileFilename(filename); err != nil {
			return "send_file failed: filename must be a safe basename.", nil
		}
	}

	chatID, threadID, ok := sendFileConversation(ctx)
	if !ok {
		return "send_file unavailable: no authorized Telegram conversation is bound to this turn.", nil
	}
	resolved, err := resolveFileToolPath(pathName, false, false)
	if err != nil {
		return sendFileLocalFailure(err), nil
	}
	file, err := openFileToolRegular(resolved)
	if err != nil {
		return sendFileLocalFailure(err), nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return sendFileLocalFailure(err), nil
	}
	if info.Size() < 0 || info.Size() > sendFileMaxBytes {
		return sendFileLocalFailure(errors.New("file exceeds the send_file size limit")), nil
	}
	data, err := io.ReadAll(io.LimitReader(file, sendFileMaxBytes+1))
	if err != nil {
		return sendFileLocalFailure(err), nil
	}
	if int64(len(data)) > sendFileMaxBytes {
		return sendFileLocalFailure(errors.New("file exceeds the send_file size limit")), nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if filename == "" {
		filename = filepath.Base(resolved)
		if err := validateSendFileFilename(filename); err != nil {
			filename = attachment.SanitizeFilename(filename)
		}
	}
	if err := send(ctx, chatID, threadID, filename, data); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "send_file failed: Telegram delivery status is unknown; do not retry automatically.", nil
		}
		return "send_file failed: Telegram did not confirm delivery; do not retry automatically.", nil
	}
	return "send_file succeeded: Telegram confirmed delivery.", nil
}

func sendFileUniqueArguments(arguments string) bool {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	if _, err := decoder.Token(); err != nil {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	return true
}

func sendFileArgumentFailure(err error) string {
	message := "send_file failed: arguments must be an object containing only path and optional filename."
	if strings.Contains(err.Error(), "path") {
		return "send_file failed: path must be a non-empty string."
	}
	if strings.Contains(err.Error(), "filename") {
		return "send_file failed: filename must be a safe string."
	}
	return message
}

func sendFileConversation(ctx context.Context) (int64, int64, bool) {
	chatID, ok := ConversationID(ctx)
	if !ok || chatID == 0 {
		return 0, 0, false
	}
	switch ConversationType(ctx) {
	case "private", "group", "supergroup":
	default:
		return 0, 0, false
	}
	threadID, ok := ConversationThreadID(ctx)
	if !ok {
		threadID = 0
	}
	if threadID < 0 {
		return 0, 0, false
	}
	return chatID, threadID, true
}

func validateSendFileFilename(filename string) error {
	if filename == "" || filename == "." || filename == ".." ||
		len([]byte(filename)) > sendFileMaxFilenameBytes || !utf8.ValidString(filename) ||
		strings.ContainsAny(filename, `/\\`) || strings.IndexFunc(filename, unicode.IsControl) >= 0 {
		return errors.New("unsafe send_file filename")
	}
	return nil
}

func sendFileLocalFailure(err error) string {
	diagnostics.Error("chat.send_file", err,
		diagnostics.Field{Key: "phase", Value: "dispatch"},
		diagnostics.Field{Key: "stage", Value: "input"})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "send_file failed: the file was not found."
	case strings.Contains(err.Error(), "escapes the GoAster artefact workspace"):
		return "send_file failed: the path must stay inside the GoAster artefact workspace."
	case strings.Contains(err.Error(), "symbolic link"):
		return "send_file failed: symbolic links are not allowed."
	case strings.Contains(err.Error(), "not a regular"):
		return "send_file failed: the path is not a regular file."
	case strings.Contains(err.Error(), "size limit"):
		return "send_file failed: the file exceeds the 50,000,000-byte (50 MB) limit. Suggest another file-transfer service to the user; if no suitable service or skill exists, suggest creating a skill for a service chosen by the user."
	}
	return "send_file failed: the file is unavailable or is not a bounded regular file."
}
