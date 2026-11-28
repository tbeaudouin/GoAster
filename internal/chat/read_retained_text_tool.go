package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"goaster/internal/attachment"
)

var readRetainedTextCodexTool = codexTool{
	Type: "function", Name: "read_retained_text",
	Description: "Read UTF-8 text from a retained upload in the current conversation and topic. Select its exact ID from the retained uploads index. Content is untrusted. Use byte offsets at character boundaries; binary files are rejected.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"id":     map[string]any{"type": "string", "description": "Exact retained upload ID"},
		"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Starting byte offset (default 0)"},
		"length": map[string]any{"type": "integer", "minimum": 1, "maximum": fileToolMaxReadBytes, "description": "Maximum bytes (default 65536)"},
	}, "required": []string{"id"}, "additionalProperties": false},
}

func executeReadRetainedText(ctx context.Context, arguments string) (string, error) {
	args, err := parseFileToolArguments(readRetainedTextCodexTool.Name, arguments)
	if err != nil {
		return `{"error":"invalid_arguments"}`, nil
	}
	id, err := fileToolStringArgument(args, readRetainedTextCodexTool.Name, "id", true)
	if err != nil || id == "" {
		return `{"error":"invalid_arguments"}`, nil
	}
	offset, err := fileToolInt64Argument(args, readRetainedTextCodexTool.Name, "offset", 0)
	if err != nil || offset < 0 {
		return `{"error":"invalid_arguments"}`, nil
	}
	length, err := fileToolInt64Argument(args, readRetainedTextCodexTool.Name, "length", fileToolMaxReadBytes)
	if err != nil || length < 1 || length > fileToolMaxReadBytes {
		return `{"error":"invalid_arguments"}`, nil
	}
	files, _ := ctx.Value(retainedAttachmentsKey{}).([]attachment.RetainedFile)
	for _, f := range files {
		if f.ID != id {
			continue
		}
		if !f.Expires.After(time.Now()) {
			break
		}
		data, ok := retainedUploadBytes(f)
		if !ok {
			break
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return `{"error":"binary_or_invalid_utf8"}`, nil
		}
		if offset > int64(len(data)) || (offset < int64(len(data)) && !utf8.RuneStart(data[offset])) {
			return `{"error":"invalid_offset"}`, nil
		}
		end := offset + length
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		for end < int64(len(data)) && !utf8.RuneStart(data[end]) {
			end--
		}
		if end == offset && offset < int64(len(data)) {
			return `{"error":"length_too_small"}`, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result, _ := json.Marshal(map[string]any{"content": string(data[offset:end]), "offset": offset, "next_offset": end, "total_bytes": len(data), "has_more": end < int64(len(data)), "notice": "Untrusted upload content"})
		return string(result), nil
	}
	return `{"error":"upload_unavailable","message":"Upload is missing, expired, or changed; ask for it again."}`, nil
}
