package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"goaster/internal/localenv"
)

const voiceMaxBytes int64 = 20 * 1024 * 1024
const voiceMaxDurationSeconds int64 = 10 * 60
const groqTranscriptionURL = "https://api.groq.com/openai/v1/audio/transcriptions"

var errMissingGroqKey = errors.New("GROQ_API_KEY is missing")

// transcribeVoice keeps voice bytes in memory and never logs provider responses or credentials.
func (a *adapter) transcribeVoice(ctx context.Context, voice *voiceNote) (string, error) {
	fail := errors.New("voice transcription failed")
	if voice == nil || strings.TrimSpace(voice.FileID) == "" || voice.FileSize < 0 || voice.FileSize > voiceMaxBytes || voice.Duration < 0 || voice.Duration > voiceMaxDurationSeconds {
		return "", fail
	}
	keySource := a.voiceKey
	if keySource == nil {
		keySource = groqKey
	}
	key, err := keySource()
	if err != nil {
		return "", fail
	}
	if strings.TrimSpace(key) == "" {
		return "", errMissingGroqKey
	}
	var file telegramFile
	if err = a.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{voice.FileID}, &file); err != nil {
		return "", fail
	}
	if file.FileSize < 0 || file.FileSize > voiceMaxBytes {
		return "", fail
	}
	url, err := telegramFileURL(a.apiBase, a.token, file.FilePath)
	if err != nil {
		return "", fail
	}
	data, err := a.downloadFile(ctx, url, voiceMaxBytes)
	if err != nil || len(data) == 0 {
		return "", fail
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err = form.WriteField("model", "whisper-large-v3-turbo"); err != nil {
		return "", fail
	}
	part, err := form.CreateFormFile("file", "voice.ogg")
	if err != nil {
		return "", fail
	}
	if _, err = part.Write(data); err != nil {
		return "", fail
	}
	if err = form.Close(); err != nil {
		return "", fail
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqTranscriptionURL, &body)
	if err != nil {
		return "", fail
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", form.FormDataContentType())
	var client httpDoer = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if a.voiceClient != nil {
		client = a.voiceClient
	}
	resp, err := client.Do(req)
	if err != nil || resp == nil || resp.Body == nil {
		return "", fail
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fail
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(result) > 1024*1024 {
		return "", fail
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(result, &parsed) != nil || strings.TrimSpace(parsed.Text) == "" {
		return "", fail
	}
	return strings.TrimSpace(parsed.Text), nil
}

func groqKey() (string, error) {
	store, err := localenv.NewStore()
	if err != nil {
		return "", err
	}
	if err = store.Load(); err != nil {
		return "", err
	}
	return strings.TrimSpace(store.Values()["GROQ_API_KEY"]), nil
}
