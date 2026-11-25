package attachment

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const RetainedMaxBytes int64 = 256 << 20
const RetainedMaxFiles = 128
const RetainedTTL = 7 * 24 * time.Hour

var errRetainedMissingContent = errors.New("retained content missing")
var ErrRetainedQuota = errors.New("retained attachment quota exceeded")
var retainedLocks [256]sync.Mutex

// RetainedScope identifies a conversation. ThreadID zero denotes its default thread.
type RetainedScope struct {
	ChatID   int64
	ThreadID int64
}

// RetainedFile is a verified reference, valid only while WithRetainedScope holds its lease.
// Metadata.Validate is for temporary files and does not validate these references.
type RetainedFile struct {
	ID       string
	Metadata Metadata
	SHA256   string
	Created  time.Time
	Expires  time.Time
}
type retainedRecord struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Filename string    `json:"filename"`
	MIMEType string    `json:"mime_type"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
}

// RetainedLease must only be used inside its WithRetainedScope callback.
type RetainedLease struct {
	root  string
	scope string
}

// DefaultRetainedRoot is a sibling of the private artefacts directory.
func DefaultRetainedRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate retained uploads home: %w", err)
	}
	return filepath.Join(home, ".goaster", "uploads"), nil
}

// WithRetainedScope serializes retained operations in this process. Hold the
// callback for the entire request using returned paths; never retain the lease
// or paths after it returns. Multiple processes must be serialized by the owner.
// root is the exact storage root, and must be an absolute path.
func WithRetainedScope(root string, scope RetainedScope, fn func(RetainedLease) error) error {
	if fn == nil {
		return errors.New("invalid retained upload scope")
	}
	lease, release, err := AcquireRetainedScope(root, scope)
	if err != nil {
		return err
	}
	defer release()
	return fn(lease)
}

// AcquireRetainedScope holds a bounded, process-local scope lock until release.
func AcquireRetainedScope(root string, scope RetainedScope) (RetainedLease, func(), error) {
	if scope.ChatID == 0 || scope.ThreadID < 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return RetainedLease{}, nil, errors.New("invalid retained upload scope or root")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", scope.ChatID, scope.ThreadID)))
	name := hex.EncodeToString(sum[:])
	hash := sha256.Sum256([]byte(root + ":" + name))
	retainedLocks[hash[0]].Lock()
	var lease RetainedLease
	err := func() error {
		if err := os.MkdirAll(root, 0700); err != nil {
			return fmt.Errorf("create retained upload root: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("inspect retained upload root: %w", err)
		}
		if resolved != root {
			return errors.New("retained upload root contains a symlink")
		}
		info, err := os.Lstat(root)
		if err != nil {
			return fmt.Errorf("inspect retained upload root: %w", err)
		}
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("retained upload root is not private")
		}
		r, err := os.OpenRoot(root)
		if err != nil {
			return fmt.Errorf("open retained upload root: %w", err)
		}
		defer r.Close()
		if err := r.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create retained scope: %w", err)
		}
		info, err = r.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect retained scope: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("retained scope is not private")
		}
		lease = RetainedLease{root: root, scope: name}
		return nil
	}()
	if err != nil {
		retainedLocks[hash[0]].Unlock()
		return RetainedLease{}, nil, err
	}
	return lease, retainedLocks[hash[0]].Unlock, nil
}

func retainedID(s string) bool {
	if len(s) != 32 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == s
}
func privateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Mode().Perm()&0400 != 0
}

// List verifies every record and its content, then removes expired uploads in
// this scope. Any corrupt record fails the operation without trusting its paths.
func (l RetainedLease) List() ([]RetainedFile, error) { return l.list(time.Now().UTC()) }
func (l RetainedLease) list(now time.Time) ([]RetainedFile, error) {
	r, err := os.OpenRoot(filepath.Join(l.root, l.scope))
	if err != nil {
		return nil, fmt.Errorf("open retained scope: %w", err)
	}
	defer r.Close()
	dir, err := r.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open retained listing: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, fmt.Errorf("read retained listing: %w", err)
	}
	var files []RetainedFile
	var expired []string
	var pending []string
	for _, entry := range entries {
		id := entry.Name()
		isPending := strings.HasPrefix(id, ".pending-") && retainedID(strings.TrimPrefix(id, ".pending-"))
		if !retainedID(id) && !isPending {
			return nil, errors.New("invalid retained upload entry")
		}
		info, err := r.Lstat(id)
		if err != nil {
			return nil, fmt.Errorf("inspect retained upload directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("retained upload directory is not private")
		}
		if isPending {
			pending = append(pending, id)
			continue
		}
		child, err := r.OpenRoot(id)
		if err != nil {
			return nil, fmt.Errorf("open retained upload: %w", err)
		}
		file, e := readRetained(child.FS(), child.Lstat, id, filepath.Join(l.root, l.scope, id))
		child.Close()
		if errors.Is(e, errRetainedMissingContent) {
			expired = append(expired, id)
			continue
		}
		if e != nil {
			return nil, e
		}
		if !file.Expires.After(now) {
			expired = append(expired, id)
		} else {
			files = append(files, file)
		}
	}
	for _, id := range pending {
		if err := r.RemoveAll(id); err != nil {
			return nil, fmt.Errorf("remove pending retained upload: %w", err)
		}
	}
	for _, id := range expired {
		if err := r.RemoveAll(id); err != nil {
			return nil, fmt.Errorf("remove expired retained upload: %w", err)
		}
	}
	return files, nil
}

func readRetained(fsys fs.FS, lstat func(string) (os.FileInfo, error), id, dir string) (RetainedFile, error) {
	info, err := lstat("metadata.json")
	if err != nil {
		return RetainedFile{}, fmt.Errorf("inspect retained metadata: %w", err)
	}
	if !privateFile(info) || info.Size() > 4096 {
		return RetainedFile{}, errors.New("invalid retained metadata file")
	}
	f, err := fsys.Open("metadata.json")
	if err != nil {
		return RetainedFile{}, fmt.Errorf("open retained metadata: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if err != nil {
		return RetainedFile{}, fmt.Errorf("read retained metadata: %w", err)
	}
	if len(data) > 4096 {
		return RetainedFile{}, errors.New("retained metadata too large")
	}
	var rec retainedRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return RetainedFile{}, fmt.Errorf("decode retained metadata: %w", err)
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return RetainedFile{}, errors.New("invalid retained metadata trailing data")
	}
	digest, err := hex.DecodeString(rec.SHA256)
	if rec.ID != id || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != rec.SHA256 || rec.Created.IsZero() || !rec.Expires.Equal(rec.Created.Add(RetainedTTL)) {
		return RetainedFile{}, errors.New("invalid retained metadata")
	}
	if err := validateFields(rec.Type, rec.Filename, rec.MIMEType, rec.Size); err != nil {
		return RetainedFile{}, fmt.Errorf("invalid retained metadata: %w", err)
	}
	info, err = lstat("content")
	if errors.Is(err, os.ErrNotExist) {
		return RetainedFile{}, errRetainedMissingContent
	}
	if err != nil {
		return RetainedFile{}, fmt.Errorf("inspect retained content: %w", err)
	}
	if !privateFile(info) || info.Size() != rec.Size {
		return RetainedFile{}, errors.New("invalid retained content file")
	}
	f, err = fsys.Open("content")
	if err != nil {
		return RetainedFile{}, fmt.Errorf("open retained content: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxBytes+1))
	f.Close()
	if err != nil {
		return RetainedFile{}, fmt.Errorf("read retained content: %w", err)
	}
	if n != rec.Size || hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
		return RetainedFile{}, errors.New("retained content integrity mismatch")
	}
	return RetainedFile{ID: id, Metadata: Metadata{Type: rec.Type, Filename: rec.Filename, MIMEType: rec.MIMEType, Size: rec.Size, Path: filepath.Join(dir, "content")}, SHA256: rec.SHA256, Created: rec.Created, Expires: rec.Expires}, nil
}

// Retain validates and persists one upload, rejecting it when either quota is full.
func (l RetainedLease) Retain(in Incoming) (RetainedFile, error) {
	m, err := in.Validate()
	if err != nil {
		return RetainedFile{}, fmt.Errorf("validate retained upload: %w", err)
	}
	files, err := l.List()
	if err != nil {
		return RetainedFile{}, err
	}
	if !retainedQuotaAllows(files, m.Size) {
		return RetainedFile{}, ErrRetainedQuota
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return RetainedFile{}, fmt.Errorf("generate retained upload ID: %w", err)
	}
	id := hex.EncodeToString(random[:])
	now := time.Now().UTC()
	sum := sha256.Sum256(in.Data)
	rec := retainedRecord{ID: id, Type: m.Type, Filename: m.Filename, MIMEType: m.MIMEType, Size: m.Size, SHA256: hex.EncodeToString(sum[:]), Created: now, Expires: now.Add(RetainedTTL)}
	data, err := json.Marshal(rec)
	if err != nil {
		return RetainedFile{}, fmt.Errorf("encode retained metadata: %w", err)
	}
	r, err := os.OpenRoot(filepath.Join(l.root, l.scope))
	if err != nil {
		return RetainedFile{}, fmt.Errorf("open retained scope: %w", err)
	}
	defer r.Close()
	stage := ".pending-" + id
	if err := r.Mkdir(stage, 0700); err != nil {
		return RetainedFile{}, fmt.Errorf("create retained upload: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			r.RemoveAll(stage)
		}
	}()
	if err := r.WriteFile(filepath.Join(stage, "content"), in.Data, 0600); err != nil {
		return RetainedFile{}, fmt.Errorf("write retained content: %w", err)
	}
	if err := r.WriteFile(filepath.Join(stage, "metadata.json"), data, 0600); err != nil {
		return RetainedFile{}, fmt.Errorf("write retained metadata: %w", err)
	}
	if _, err := r.Lstat(id); err == nil {
		return RetainedFile{}, errors.New("retained upload ID already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RetainedFile{}, fmt.Errorf("inspect retained upload destination: %w", err)
	}
	if err := r.Rename(stage, id); err != nil {
		return RetainedFile{}, fmt.Errorf("publish retained upload: %w", err)
	}
	ok = true
	m.Path = filepath.Join(l.root, l.scope, id, "content")
	return RetainedFile{ID: id, Metadata: m, SHA256: rec.SHA256, Created: now, Expires: rec.Expires}, nil
}

func retainedQuotaAllows(files []RetainedFile, added int64) bool {
	if added < 0 || added > RetainedMaxBytes || len(files) >= RetainedMaxFiles {
		return false
	}
	remaining := RetainedMaxBytes - added
	for _, file := range files {
		if file.Metadata.Size < 0 || file.Metadata.Size > remaining {
			return false
		}
		remaining -= file.Metadata.Size
	}
	return true
}

// SweepRetainedExpired opportunistically visits inactive managed scopes.
func SweepRetainedExpired(root string, active RetainedScope) error {
	activeHash := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", active.ChatID, active.ThreadID)))
	activeName := hex.EncodeToString(activeHash[:])
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("invalid retained upload root")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect retained upload root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("inspect retained upload root: %w", err)
	}
	if resolved != root || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("retained upload root is not private")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open retained upload root: %w", err)
	}
	defer r.Close()
	dir, err := r.Open(".")
	if err != nil {
		return fmt.Errorf("open retained root listing: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return fmt.Errorf("read retained root listing: %w", err)
	}
	var failures error
	for _, entry := range entries {
		name := entry.Name()
		if name == activeName {
			continue
		}
		decoded, decodeErr := hex.DecodeString(name)
		if decodeErr != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != name {
			continue
		}
		hash := sha256.Sum256([]byte(root + ":" + name))
		if !retainedLocks[hash[0]].TryLock() {
			continue
		}
		info, statErr := r.Lstat(name)
		if statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0077 == 0 {
			_, statErr = (RetainedLease{root: root, scope: name}).List()
		}
		retainedLocks[hash[0]].Unlock()
		if statErr != nil {
			failures = errors.Join(failures, fmt.Errorf("sweep retained scope: %w", statErr))
		}
	}
	return failures
}

// Clear removes this whole conversation's retained uploads. The caller owns
// chat clear/count semantics and should invoke this while holding the lease.
func (l RetainedLease) Clear() error {
	r, err := os.OpenRoot(filepath.Join(l.root, l.scope))
	if err != nil {
		return fmt.Errorf("open retained scope: %w", err)
	}
	defer r.Close()
	d, err := r.Open(".")
	if err != nil {
		return fmt.Errorf("open retained listing: %w", err)
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return fmt.Errorf("read retained listing: %w", err)
	}
	for _, e := range entries {
		if strings.ContainsAny(e.Name(), `/\\`) {
			return errors.New("invalid retained entry")
		}
		if err := r.RemoveAll(e.Name()); err != nil {
			return fmt.Errorf("clear retained upload: %w", err)
		}
	}
	return nil
}
