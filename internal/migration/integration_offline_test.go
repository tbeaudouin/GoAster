package migration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/scheduler"
)

func TestOfflineSchedulerPathIsolation(t *testing.T) {
	home, err := os.MkdirTemp(".", ".migration-home-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	home, err = filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	path, err := SchedulerPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".goaster", "migration-monitor", "jobs.json") || strings.Contains(path, string(filepath.Separator)+"artefacts"+string(filepath.Separator)) {
		t.Fatalf("unexpected scheduler path: %s", path)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("scheduler directory: %v %v", info, err)
	}
}

func TestOfflineRecipientChangeAndRestart(t *testing.T) {
	for _, change := range []string{"restart", "availability"} {
		t.Run(change, func(t *testing.T) {
			recipient := int64(42)
			creates, deliveries := 0, 0
			opts := Options{TempDirectory: t.TempDir(), MigrationRecipient: func() (int64, error) { return recipient, nil }, PrepareRecipientHandoff: func(id int64) (func(context.Context, []byte) error, error) {
				if recipient != id {
					return nil, errors.New("recipient changed")
				}
				return func(context.Context, []byte) error { deliveries++; return nil }, nil
			}, DeliverHandoff: func(context.Context, []byte) error { deliveries++; return nil }, LoadHetznerToken: func() (string, error) { return "synthetic", nil }, WebOrigin: "https://example.invalid", HTTPClient: offlineDoer(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/migration/v1/machines/create" {
					creates++
					return nil, errors.New("unexpected create")
				}
				if change == "availability" {
					recipient = 43
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":["fsn1"]}`))}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":[]}`))}, nil
			})}
			path := filepath.Join(t.TempDir(), "jobs.json")
			jobs, err := scheduler.New(scheduler.Options{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			req := Request{ServerType: "cx22"}
			result, err := StartMonitor(t.Context(), req, opts, jobs)
			if err != nil {
				t.Fatal(err)
			}
			if change == "restart" {
				if result.Status != "monitoring" {
					t.Fatalf("start: %+v", result)
				}
				jobs, err = scheduler.New(scheduler.Options{Path: path})
				if err != nil {
					t.Fatal(err)
				}
				job, ok := jobs.Get(result.MonitorID)
				if !ok {
					t.Fatal("job missing after restart")
				}
				recipient = 43
				if err := ScheduledExecutor(t.Context(), job.Message, jobs, opts); err == nil {
					t.Fatal("changed recipient accepted")
				}
			} else if !result.ManualReview {
				t.Fatalf("changed recipient: %+v", result)
			}
			job, ok := jobs.Get(result.MonitorID)
			if !ok || job.Enabled || creates != 0 || deliveries != 0 {
				t.Fatalf("job=%+v creates=%d deliveries=%d", job, creates, deliveries)
			}
		})
	}
}
