package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/attachment"
)

func TestLoadRetainedImageScopeAndIntegrity(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "content")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	f := attachment.RetainedFile{ID: "abc", SHA256: hex.EncodeToString(sum[:]), Expires: time.Now().Add(time.Hour), Metadata: attachment.Metadata{Type: "image", Filename: "x.png", Path: path, Size: int64(len(data))}}
	sink := make(chan string, 1)
	ctx := context.WithValue(WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f}), retainedImageSinkKey{}, sink)
	for _, tc := range []struct{ arg, want string }{{`{"id":"other"}`, "image_unavailable"}, {`{"id":"abc","path":"/etc/passwd"}`, "invalid_arguments"}, {`{"id":null}`, "invalid_arguments"}, {`{"id":"abc"} {}`, "invalid_arguments"}} {
		result, err := executeLoadRetainedImage(ctx, tc.arg)
		if err != nil || !strings.Contains(result, tc.want) {
			t.Fatalf("%s: %s %v", tc.arg, result, err)
		}
	}
	result, err := executeLoadRetainedImage(ctx, `{"id":"abc"}`)
	if err != nil || !strings.Contains(result, `"ok":true`) {
		t.Fatalf("%s %v", result, err)
	}
	if got := <-sink; !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatal("missing image data")
	}
	if err := os.WriteFile(path, make([]byte, len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	result, _ = executeLoadRetainedImage(ctx, `{"id":"abc"}`)
	if !strings.Contains(result, "image_unavailable") {
		t.Fatal(result)
	}
	f.Expires = time.Now().Add(-time.Second)
	ctx = context.WithValue(WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f}), retainedImageSinkKey{}, sink)
	result, _ = executeLoadRetainedImage(ctx, `{"id":"abc"}`)
	if !strings.Contains(result, "image_unavailable") {
		t.Fatal(result)
	}
}

func TestRetainedImageRegisteredForNormalAndScheduled(t *testing.T) {
	p := NewCodexProvider(CredentialStore{})
	for _, ctx := range []context.Context{context.Background(), context.WithValue(context.Background(), scheduledTurnKey{}, true)} {
		found := false
		for _, tool := range p.tools.definitionsFor(ctx) {
			if tool.Name == loadRetainedImageCodexTool.Name {
				found = true
			}
		}
		if !found {
			t.Fatal("image tool missing")
		}
	}
}

func TestLoadRetainedImageContinuesOnlyOnce(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "content")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	f := attachment.RetainedFile{ID: "abc", SHA256: hex.EncodeToString(sum[:]), Expires: time.Now().Add(time.Hour), Metadata: attachment.Metadata{Type: "image", Filename: "x.png", Path: path, Size: int64(len(data))}}
	requests := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		images := 0
		for _, item := range req.Input {
			for _, content := range item.Content {
				if content.Type == "input_image" {
					images++
					if !strings.HasPrefix(content.ImageURL, "data:image/png;base64,") {
						t.Error("wrong image")
					}
				}
			}
		}
		switch requests {
		case 1:
			if images != 0 {
				t.Error("unexpected initial image")
			}
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"one","name":"load_retained_image","arguments":"{\"id\":\"abc\"}"}]}`)
		case 2:
			if images != 1 {
				t.Errorf("continuation images = %d", images)
			}
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"two","name":"load_retained_image","arguments":"{\"id\":\"missing\"}"}]}`)
		case 3:
			if images != 0 {
				t.Errorf("later request images = %d", images)
			}
			fmt.Fprint(w, `{"output_text":"done"}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer backend.Close()
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "test-token", AccountID: "test-account", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p := NewCodexProvider(store)
	p.backendURL = backend.URL
	p.httpClient = backend.Client()
	reply, err := p.Reply(WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f}), "gpt-test", "inspect")
	if err != nil || reply != "done" || requests != 3 {
		t.Fatalf("reply=%q requests=%d err=%v", reply, requests, err)
	}
}
