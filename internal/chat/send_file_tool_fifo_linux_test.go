package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSendFileToolRejectsFIFOWithoutBlocking(t *testing.T) {
	root := setupFileToolWorkspace(t)
	pathName := filepath.Join(root, "pipe")
	if err := unix.Mkfifo(pathName, 0600); err != nil {
		t.Fatal(err)
	}
	handler := sendFileToolHandler(t, func(context.Context, int64, int64, string, []byte) error {
		t.Fatal("FIFO was sent")
		return nil
	})
	done := make(chan string, 1)
	go func() {
		result, _ := handler(sendFileTelegramContext("group", -10042, 3), `{"path":"pipe"}`)
		done <- result
	}()
	select {
	case result := <-done:
		if !strings.Contains(result, "not a regular file") {
			t.Fatalf("FIFO result = %q", result)
		}
	case <-time.After(time.Second):
		fd, err := unix.Open(pathName, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			defer unix.Close(fd)
			<-done
		}
		t.Fatal("send_file blocked while inspecting FIFO")
	}
}
