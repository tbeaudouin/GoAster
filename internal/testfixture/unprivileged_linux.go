//go:build linux

// Package testfixture provides isolated fixtures for privilege-switching tests.
package testfixture

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Executable copies the current test binary into a private directory owned by
// the child identity. No credentials or other process files are copied.
func Executable(t *testing.T, uid, gid int) string {
	t.Helper()
	info, err := os.Lstat("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSticky == 0 || st.Uid != 0 {
		t.Fatal("/tmp is not a trusted system directory")
	}
	dir, err := os.MkdirTemp("/tmp", "goaster-test-child-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove child fixture: %v", err)
		}
	})
	src, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, "test")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	return path
}
