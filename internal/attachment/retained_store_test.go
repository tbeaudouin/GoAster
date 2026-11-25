package attachment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetainedStoreLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uploads")
	scope := RetainedScope{ChatID: 123, ThreadID: 4}
	in := Incoming{Type: "document", Filename: "report.txt", Size: 5, Data: []byte("hello")}
	var saved RetainedFile
	err := WithRetainedScope(root, scope, func(l RetainedLease) error { var err error; saved, err = l.Retain(in); return err })
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Metadata.Filename != in.Filename || !saved.Expires.Equal(saved.Created.Add(RetainedTTL)) {
		t.Fatal(saved)
	}
	err = WithRetainedScope(root, scope, func(l RetainedLease) error {
		files, err := l.List()
		if err != nil {
			return err
		}
		if len(files) != 1 || files[0] != saved {
			t.Fatalf("loaded: %+v", files)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = WithRetainedScope(root, RetainedScope{ChatID: 123, ThreadID: 5}, func(l RetainedLease) error {
		files, err := l.List()
		if len(files) != 0 {
			t.Fatal(files)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = WithRetainedScope(root, scope, func(l RetainedLease) error {
		files, err := l.list(saved.Expires)
		if len(files) != 0 {
			t.Fatal(files)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved.Metadata.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestRetainedCorruptionAndClear(t *testing.T) {
	for _, mutate := range []string{"content", "metadata.json", "symlink", "permissions"} {
		t.Run(mutate, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "uploads")
			scope := RetainedScope{ChatID: 1}
			err := WithRetainedScope(root, scope, func(l RetainedLease) error {
				f, err := l.Retain(Incoming{Type: "document", Filename: "x.txt", Size: 1, Data: []byte("x")})
				if err != nil {
					return err
				}
				dir := filepath.Dir(f.Metadata.Path)
				switch mutate {
				case "content":
					err = os.WriteFile(f.Metadata.Path, []byte("y"), 0600)
				case "metadata.json":
					err = os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"path":"/tmp"}`), 0600)
				case "symlink":
					err = os.Remove(f.Metadata.Path)
					if err == nil {
						err = os.Symlink("metadata.json", f.Metadata.Path)
					}
				case "permissions":
					err = os.Chmod(f.Metadata.Path, 0644)
				}
				if err != nil {
					return err
				}
				if _, err = l.List(); err == nil {
					t.Fatal("accepted corrupt upload")
				}
				return l.Clear()
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	root := filepath.Join(t.TempDir(), "uploads")
	err := WithRetainedScope(root, RetainedScope{ChatID: 2}, func(l RetainedLease) error {
		target := t.TempDir()
		marker := filepath.Join(target, "marker")
		if err := os.WriteFile(marker, []byte("safe"), 0600); err != nil {
			return err
		}
		path := filepath.Join(root, l.scope, ".pending-0123456789abcdef0123456789abcdef")
		if err := os.Symlink(target, path); err != nil {
			return err
		}
		if _, err := l.List(); err == nil {
			t.Fatal("accepted pending symlink")
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "safe" {
			t.Fatalf("symlink target changed: %q, %v", data, err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		for _, name := range []string{".pending-invalid", ".pending-0123456789ABCDEF0123456789ABCDEF"} {
			path := filepath.Join(root, l.scope, name)
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			if _, err := l.List(); err == nil {
				t.Fatalf("accepted unknown entry %q", name)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedQuotaAndScopeValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uploads")
	if err := WithRetainedScope(root, RetainedScope{}, func(RetainedLease) error { return nil }); err == nil {
		t.Fatal("accepted invalid scope")
	}
	err := WithRetainedScope(root, RetainedScope{ChatID: -1}, func(l RetainedLease) error {
		for i := 0; i < RetainedMaxFiles; i++ {
			if _, err := l.Retain(Incoming{Type: "document", Filename: "x", Data: []byte{}}); err != nil {
				return err
			}
		}
		_, err := l.Retain(Incoming{Type: "document", Filename: "x", Data: []byte{}})
		if !errors.Is(err, ErrRetainedQuota) {
			t.Fatalf("quota: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedLeaseSerializes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uploads")
	scope := RetainedScope{ChatID: 1}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 2)
	waiterStarted := make(chan struct{})
	go func() {
		done <- WithRetainedScope(root, scope, func(RetainedLease) error { close(entered); <-release; return nil })
	}()
	<-entered
	go func() {
		close(waiterStarted)
		done <- WithRetainedScope(root, scope, func(RetainedLease) error {
			select {
			case <-release:
				return nil
			default:
				return errors.New("lease overlapped")
			}
		})
	}()
	<-waiterStarted
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRetainedByteQuotaBoundary(t *testing.T) {
	files := []RetainedFile{{Metadata: Metadata{Size: RetainedMaxBytes - 1}}}
	if !retainedQuotaAllows(files, 1) || retainedQuotaAllows(files, 2) {
		t.Fatal("incorrect byte quota boundary")
	}
}

func TestRetainedMissingContentAndSweepLease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uploads")
	scope := RetainedScope{ChatID: 51}
	lease, release, err := AcquireRetainedScope(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	file, err := lease.Retain(Incoming{Type: "document", Filename: "x", Size: 1, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(filepath.Dir(file.Metadata.Path), "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var rec retainedRecord
	if err = json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	rec.Created = time.Now().UTC().Add(-RetainedTTL - time.Hour)
	rec.Expires = rec.Created.Add(RetainedTTL)
	data, err = json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = SweepRetainedExpired(root, RetainedScope{ChatID: 52}); err != nil {
		t.Fatal(err)
	}
	if content, readErr := os.ReadFile(file.Metadata.Path); readErr != nil || string(content) != "x" {
		t.Fatalf("active lease lost readable content: %q, %v", content, readErr)
	}
	release()
	release = func() {}
	if err = SweepRetainedExpired(root, RetainedScope{ChatID: 52}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file.Metadata.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired content remains: %v", err)
	}
	err = WithRetainedScope(root, scope, func(l RetainedLease) error {
		f, e := l.Retain(Incoming{Type: "document", Filename: "x", Size: 1, Data: []byte("x")})
		if e != nil {
			return e
		}
		if e = os.Remove(f.Metadata.Path); e != nil {
			return e
		}
		files, e := l.List()
		if e != nil {
			return e
		}
		if len(files) != 0 {
			t.Fatal("missing file indexed")
		}
		if _, e = os.Stat(filepath.Dir(f.Metadata.Path)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("stale record remains: %v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate death at each write stage, then exercise both ordinary listing and sweep.
	for _, operation := range []string{"list", "sweep"} {
		err = WithRetainedScope(root, scope, func(l RetainedLease) error {
			valid, err := l.Retain(Incoming{Type: "document", Filename: "valid", Size: 1, Data: []byte("v")})
			if err != nil {
				return err
			}
			for i, id := range []string{"00000000000000000000000000000001", "00000000000000000000000000000002", "00000000000000000000000000000003"} {
				dir := filepath.Join(root, l.scope, ".pending-"+id)
				if err := os.Mkdir(dir, 0700); err != nil {
					return err
				}
				if i > 0 {
					if err := os.WriteFile(filepath.Join(dir, "content"), []byte("x"), 0600); err != nil {
						return err
					}
				}
				if i > 1 {
					if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{"), 0600); err != nil {
						return err
					}
				}
			}
			if operation == "list" {
				files, err := l.List()
				if err != nil || len(files) != 1 || files[0].ID != valid.ID {
					t.Fatalf("list: %+v, %v", files, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if operation == "sweep" {
			if err := SweepRetainedExpired(root, RetainedScope{ChatID: 52}); err != nil {
				t.Fatal(err)
			}
		}
		err = WithRetainedScope(root, scope, func(l RetainedLease) error {
			entries, err := os.ReadDir(filepath.Join(root, l.scope))
			if err != nil {
				return err
			}
			if len(entries) != 1 || !retainedID(entries[0].Name()) {
				t.Fatalf("%s left entries: %v", operation, entries)
			}
			files, err := l.List()
			if err != nil || len(files) != 1 {
				t.Fatalf("%s lost valid upload: %+v, %v", operation, files, err)
			}
			return l.Clear()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
