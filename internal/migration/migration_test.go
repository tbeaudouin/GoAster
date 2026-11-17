package migration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHetznerTokenFailsClosedForMissingMalformedAndUnreadableSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Unsetenv("HCLOUD_TOKEN"); err != nil {
		t.Fatal(err)
	}

	if _, err := loadHetznerToken(); !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("missing token error = %v, want ErrHetznerTokenMissing", err)
	}

	settingsPath := filepath.Join(home, ".goaster", "env")
	if err := os.WriteFile(settingsPath, []byte("HCLOUD_TOKEN=synthetic-secret-value\nnot-a-setting\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHetznerToken(); err == nil || errors.Is(err, ErrHetznerTokenMissing) || strings.Contains(err.Error(), "synthetic-secret-value") {
		t.Fatalf("malformed settings error = %v", err)
	}

	t.Setenv("HCLOUD_TOKEN", "synthetic-process-token")
	if _, err := loadHetznerToken(); !errors.Is(err, ErrCredentialStorageUnreadable) {
		t.Fatalf("malformed settings bypassed by process token: %v", err)
	}
	t.Setenv("HCLOUD_TOKEN", "")

	if err := os.WriteFile(settingsPath, []byte("HCLOUD_TOKEN=synthetic token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHetznerToken(); err == nil || errors.Is(err, ErrHetznerTokenMissing) || strings.Contains(err.Error(), "synthetic token") {
		t.Fatalf("invalid saved token error = %v", err)
	}

	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settingsPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHetznerToken(); err == nil || errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("unreadable settings path error = %v", err)
	}
}

func TestValidMigrationServerNameRequiresExactLowercaseHexSuffix(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{name: "goaster-migration-0123456789abcdef", valid: true},
		{name: "goaster-migration-0123456789ABCDEf"},
		{name: "goaster-migration-0123456789abcde"},
		{name: "other-0123456789abcdef"},
	} {
		if got := ValidMigrationServerName(test.name); got != test.valid {
			t.Errorf("ValidMigrationServerName(%q) = %v, want %v", test.name, got, test.valid)
		}
	}
}
