package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/attachment"
)

func TestReadRetainedText(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "content")
	data := []byte("aébc")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	f := attachment.RetainedFile{ID: "abc", SHA256: hex.EncodeToString(sum[:]), Expires: time.Now().Add(time.Hour), Metadata: attachment.Metadata{Type: "document", Filename: "test.txt", Path: path, Size: int64(len(data))}}
	ctx := WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f})
	for _, tc := range []struct{ args, want string }{
		{`{"id":"abc","length":2}`, `"content":"a"`},
		{`{"id":"abc","offset":1,"length":2}`, `"content":"é"`},
		{`{"id":"abc","offset":2}`, `invalid_offset`},
		{`{"id":"abc","offset":1,"length":1}`, `length_too_small`},
		{`{"id":"abc","path":"/etc/passwd"}`, `invalid_arguments`},
		{`{"id":"other"}`, `upload_unavailable`},
		{`{"id":"abc","offset":5}`, `"has_more":false`},
	} {
		got, err := executeReadRetainedText(ctx, tc.args)
		if err != nil || !strings.Contains(got, tc.want) {
			t.Fatalf("%s: %s %v", tc.args, got, err)
		}
	}
	if err := os.WriteFile(path, []byte("bad!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, _ := executeReadRetainedText(ctx, `{"id":"abc"}`); !strings.Contains(got, "upload_unavailable") {
		t.Fatal(got)
	}
	binary := []byte{'a', 0, 'b'}
	sum = sha256.Sum256(binary)
	f.SHA256 = hex.EncodeToString(sum[:])
	f.Metadata.Size = int64(len(binary))
	if err := os.WriteFile(path, binary, 0600); err != nil {
		t.Fatal(err)
	}
	ctx = WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f})
	if got, _ := executeReadRetainedText(ctx, `{"id":"abc"}`); !strings.Contains(got, "binary_or_invalid_utf8") {
		t.Fatal(got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "other"), path); err != nil {
		t.Fatal(err)
	}
	if got, _ := executeReadRetainedText(ctx, `{"id":"abc"}`); !strings.Contains(got, "upload_unavailable") {
		t.Fatal(got)
	}
	f.Expires = time.Now().Add(-time.Second)
	ctx = WithRetainedAttachments(context.Background(), []attachment.RetainedFile{f})
	if got, _ := executeReadRetainedText(ctx, `{"id":"abc"}`); !strings.Contains(got, "upload_unavailable") {
		t.Fatal(got)
	}
}
