package scheduler

import (
	"context"
	"errors"
	"goaster/internal/migration"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationTaskPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := New(Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := migration.Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcdef", Locations: []string{"fsn1"}}
	job, err := s.CreateOnce(request, 7)
	if err != nil {
		t.Fatal(err)
	}
	request.Locations[0] = "nbg1"
	loaded, err := New(Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(job.ID)
	if !ok || got.TaskType != TaskMigrationCreateOnce || got.MigrationRequest.Locations[0] != "fsn1" || got.EverySeconds != 1800 || got.OriginChatID != 7 || got.NextRun == nil || !got.NextRun.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("job: %+v", got)
	}
	if _, err := s.CreateOnce(migration.Request{}, 7); err == nil {
		t.Fatal("invalid request accepted")
	}
}

func TestMigrationTaskPersistentClaimWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := New(Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateOnce(migration.Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcdef"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the destination during the write transaction so the real atomic
	// rename fails, then restore the original persistent state.
	backup := path + ".backup"
	s.saveJobs = func(jobs []Job) error {
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		err := s.store.Save(jobs)
		if removeErr := os.Remove(path); removeErr != nil {
			t.Fatal(removeErr)
		}
		if restoreErr := os.Rename(backup, path); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		return err
	}
	_, _, err = s.ClaimExecutionMatching(context.Background(), job)
	if err == nil {
		t.Fatal("claim succeeded despite filesystem write failure")
	}
	s.saveJobs = nil
	loaded, err := New(Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(job.ID)
	if !ok || !got.Enabled || got.Status != StatusScheduled || got.ExecutionClaim || got.RunCount != 0 {
		t.Fatalf("false claim/completion: %+v", got)
	}
}

func TestDisabledMigrationTaskRejectsStaleClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateOnce(migration.Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcdef"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(job.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimExecutionMatching(context.Background(), job); !errors.Is(err, ErrExecutionClaimUnavailable) {
		t.Fatalf("stale claim: %v", err)
	}
	loaded, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get(job.ID)
	if !ok || got.Enabled || got.ExecutionClaim || got.Status != StatusDisabled {
		t.Fatalf("disabled job changed: %+v", got)
	}
}
