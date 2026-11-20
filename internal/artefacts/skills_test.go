package artefacts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoasterMigrationSkillMatchesApprovedWording(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "skills", "goaster-migration", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := os.ReadFile(filepath.Join("testdata", "goaster-migration-approved.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, approved) {
		t.Fatal("skills/goaster-migration/SKILL.md changed from approved wording")
	}
}

func TestDefaultFilesystemSkillContracts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	assetDir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	if err := InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}

	metadata, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read-write-temp-files", "git-repositories"} {
		if !strings.Contains(metadata, "name: "+name+"\n") {
			t.Errorf("default skill %q is not discoverable: %q", name, metadata)
		}
	}

	loaded := make(map[string]string)
	for _, name := range []string{"read-write-temp-files", "git-repositories", "goaster-fork", "goaster-find-contributions"} {
		content, err := Load(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		loaded[name] = strings.Join(strings.Fields(content), " ")
	}

	temp := loaded["read-write-temp-files"]
	for _, phrase := range []string{
		"/tmp/goaster", "trusted system directory with the sticky bit", "current identity with mode `0700`",
		"Separate identities cannot share this root", "TMPDIR", "TMP", "TEMP",
		"different unprivileged identity cannot traverse a `0700` task directory",
		"separate fresh directory directly under trusted system `/tmp`", "approved test identity",
		"atomic replacement", "~/.goaster/agent-auth.json", "~/.goaster/auth.json",
		"per-turn attachment directories", "exact generated task path",
	} {
		if !strings.Contains(temp, phrase) {
			t.Errorf("temporary-file workflow missing %q", phrase)
		}
	}
	for _, oldDefault := range []string{"~/.goaster/tmp", "<real user home>/.goaster/tmp", "$HOME/.goaster/tmp"} {
		if strings.Contains(strings.Join([]string{metadata, temp}, " "), oldDefault) {
			t.Errorf("temporary-file metadata or workflow retains old home-based default %q", oldDefault)
		}
	}

	repositories := loaded["git-repositories"]
	for _, phrase := range []string{
		"/srv/git/<owner>/<repo>", "explicit path", "Keep existing approved repositories where they are",
		"single path components", "trusted real directory", "symlinks", "scoped provisioning",
		"Do not fall back elsewhere", "git status --short --ignored", "exact Git root",
		"origin", "nested clone", "`pull`, `reset`, or `checkout` automatically",
		"`github-auth`", "tokens in URLs", "`goaster-fork` workflow",
	} {
		if !strings.Contains(repositories, phrase) {
			t.Errorf("git-repositories workflow missing %q", phrase)
		}
	}
	if !strings.Contains(loaded["goaster-fork"], "Before choosing a new local checkout path, load the [git-repositories skill]") {
		t.Error("goaster-fork does not load git-repositories before choosing a checkout path")
	}
	for _, phrase := range []string{"Before choosing a new path, load", "without pulling, resetting, or checking out a branch"} {
		if !strings.Contains(loaded["goaster-find-contributions"], phrase) {
			t.Errorf("goaster-find-contributions missing repository path guidance %q", phrase)
		}
	}
}

func TestInstallBundledStartersOnce(t *testing.T) {
	assetDir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	if os.Getenv("GOASTER_STARTER_LOCK_TEST") == "holder" {
		root := os.Getenv("GOASTER_STARTER_LOCK_ROOT")
		first := true
		err := installStartersAt(context.Background(), root, func(dir string) error {
			if dir == root && first {
				first = false
				if err := os.WriteFile(filepath.Join(root, "test-ready"), []byte("ready"), 0600); err != nil {
					return err
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(filepath.Join(root, "test-release")); err == nil {
						return syncStarterDirectory(dir)
					}
					time.Sleep(10 * time.Millisecond)
				}
				return errors.New("timed out waiting to release installer")
			}
			return syncStarterDirectory(dir)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".goaster", "artefacts")
	files, err := starterFiles()
	if err != nil || len(files) == 0 {
		t.Fatalf("bundled starters: %v", err)
	}
	for name, contents := range files {
		for filename, want := range contents {
			got, err := os.ReadFile(filepath.Join(root, "skills", name, filename))
			if err != nil || string(got) != string(want) {
				t.Fatalf("installed %s/%s differs from bundled build: %v", name, filename, err)
			}
		}
	}
	metadata, err := discoverAt(root)
	if err != nil || !strings.Contains(metadata, "name: goaster-fork") || !strings.Contains(metadata, "name: hetzner-auto-backup") {
		t.Fatalf("installed starter workflows were not discoverable: %q, %v", metadata, err)
	}
	if _, err := loadAt(root, "goaster-fork"); err != nil {
		t.Fatalf("installed GoAster fork workflow was not loadable: %v", err)
	}
	if _, err := loadAt(root, "hetzner-auto-backup"); err != nil {
		t.Fatalf("installed Hetzner automatic-backup workflow was not loadable: %v", err)
	}
	memoryPath := filepath.Join(root, "memory", "MEMORY.md")
	memory, err := os.ReadFile(memoryPath)
	if err != nil || !strings.Contains(string(memory), "project-memory") {
		t.Fatalf("default memory = %q, error = %v", memory, err)
	}
	info, err := os.Stat(memoryPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("default memory permissions: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "telegram-commands.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("starter installation created a persistent Telegram command reference: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("user memory"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "skills", "project-memory", "SKILL.md")
	if err := os.WriteFile(path, []byte("custom workflow"), 0600); err != nil {
		t.Fatal(err)
	}
	// A release containing revised starter guidance must not overwrite any part
	// of an already installed, user-edited workflow.
	for _, filename := range []string{"META.md", "SKILL.md", "AUTH.md"} {
		file := filepath.Join(root, "skills", "ripgrep-search", filename)
		if err := os.WriteFile(file, []byte("user edit in "+filename), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "custom workflow" {
		t.Fatalf("existing skill was updated: %q", got)
	}
	if got, err := os.ReadFile(memoryPath); err != nil || string(got) != "user memory" {
		t.Fatalf("existing memory changed: %q, %v", got, err)
	}
	for _, filename := range []string{"META.md", "SKILL.md", "AUTH.md"} {
		file := filepath.Join(root, "skills", "ripgrep-search", filename)
		if got, err := os.ReadFile(file); err != nil || string(got) != "user edit in "+filename {
			t.Fatalf("existing ripgrep-search %s changed: %q, %v", filename, got, err)
		}
	}
	checkConcurrentStarterInstallation(t)
	checkStarterSourceValidation(t)
	checkDefaultMemoryWithoutHardLinks(t)
}

func TestBundledMigrationAvailabilityGuidanceIsCurrent(t *testing.T) {
	hetznerAuthPhrases := []string{"GET /v1/server_types?name=<url-encoded-exact-type>", "server_types[]", "locations[].available", "removed 2026-10-01", "capacity guarantee"}
	for path, phrases := range map[string][]string{
		filepath.Join("..", "..", "skills", "hetzner-auth", "SKILL.md"): hetznerAuthPhrases,
		filepath.Join("..", "..", "skills", "hetzner-auth", "AUTH.md"):  hetznerAuthPhrases,
		filepath.Join("..", "..", "skills", "goaster-migration", "SKILL.md"): {
			"GET /v1/server_types?name=<requested-server-type>",
			"server_types[].locations[].available",
			"Do not use the retired datacenters endpoint.",
		},
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := strings.Join(strings.Fields(string(data)), " ")
		for _, phrase := range phrases {
			if !strings.Contains(content, phrase) {
				t.Errorf("%s missing current availability guidance %q", path, phrase)
			}
		}
		for _, forbidden := range []string{"Use GET /v1/datacenters", "call GET /v1/datacenters", "Use the migration availability tool", "Use the migration availability endpoint"} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s retained active retired availability guidance %q", path, forbidden)
			}
		}
	}
}

func checkDefaultMemoryWithoutHardLinks(t interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	assetDir, err := starterDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(assetDir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	unsupported := errors.New("hard links unsupported")
	link := func(source, destination string) error {
		data, err := os.ReadFile(source)
		if err != nil || string(data) != string(want) {
			t.Fatalf("fallback source was incomplete: %q, %v", data, err)
		}
		return unsupported
	}
	root := t.TempDir()
	err = installDefaultMemoryUsing(root, syncStarterDirectory, link, renameMemoryNoReplace)
	path := filepath.Join(root, "memory", "MEMORY.md")
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("exclusive rename fallback failed: %v", err)
		}
		if data, readErr := os.ReadFile(path); readErr != nil || string(data) != string(want) {
			t.Fatalf("fallback memory = %q, %v", data, readErr)
		}
	} else if err == nil {
		t.Fatal("unsupported publication methods should fail safely")
	} else if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed fallback left a destination: %v", statErr)
	}

	// Another writer wins between the existence check and publication.
	racingRoot := t.TempDir()
	racingLink := func(source, destination string) error {
		if err := os.WriteFile(destination, []byte("user memory"), 0600); err != nil {
			t.Fatal(err)
		}
		return unsupported
	}
	err = installDefaultMemoryUsing(racingRoot, syncStarterDirectory, racingLink, renameMemoryNoReplace)
	if (runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows") && err != nil {
		t.Fatalf("concurrent destination was not preserved: %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(racingRoot, "memory", "MEMORY.md")); readErr != nil || string(data) != "user memory" {
		t.Fatalf("concurrent user memory was changed: %q, %v", data, readErr)
	}

	failedRoot := t.TempDir()
	failedRename := func(source, destination string) error { return errors.New("rename unsupported") }
	if err := installDefaultMemoryUsing(failedRoot, syncStarterDirectory, link, failedRename); err == nil || !strings.Contains(err.Error(), "create MEMORY.md yourself") {
		t.Fatalf("unsupported methods did not provide recovery guidance: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(failedRoot, "memory"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed publication left partial files: %v, %v", entries, err)
	}
}

func checkStarterSourceValidation(t interface {
	TempDir() string
	Setenv(string, string)
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	assets := filepath.Join(fixture, "skills")
	dir := filepath.Join(assets, "example")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"MEMORY.md": "memory", "example/META.md": "name: example\ndescription: example\n",
		"example/SKILL.md": "workflow", "example/AUTH.md": "authentication",
	} {
		if err := os.WriteFile(filepath.Join(assets, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chdir(fixture); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(working)
	t.Setenv("GOASTER_STARTER_DIR", "")
	if found, err := starterDirectory(); err == nil && found == assets {
		t.Fatal("arbitrary current-directory skills were trusted")
	}
	t.Setenv("GOASTER_STARTER_DIR", assets)
	if found, err := starterDirectory(); err != nil || found != assets {
		t.Fatalf("explicit development override = %q, %v", found, err)
	}
	if _, err := starterFiles(); err != nil {
		t.Fatalf("valid explicit starter tree rejected: %v", err)
	}
	for _, extra := range []string{filepath.Join(assets, ".private"), filepath.Join(dir, ".private")} {
		if err := os.WriteFile(extra, []byte("unexpected"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := starterFiles(); err == nil {
			t.Fatalf("unexpected starter asset accepted: %s", extra)
		}
		if err := os.Remove(extra); err != nil {
			t.Fatal(err)
		}
	}
}

func checkConcurrentStarterInstallation(t interface {
	TempDir() string
	Cleanup(func())
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	child := exec.Command(os.Args[0], "-test.run=^TestInstallBundledStartersOnce$")
	child.Env = append(os.Environ(), "GOASTER_STARTER_LOCK_TEST=holder", "GOASTER_STARTER_LOCK_ROOT="+root)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "test-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first installer did not acquire the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := installStartersAt(ctx, root, syncStarterDirectory)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting installer did not honor cancellation: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- installStartersAt(ctx, root, syncStarterDirectory) }()
	select {
	case err := <-done:
		t.Fatalf("concurrent installer did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "test-release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent installer failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent installer did not finish")
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "project-memory", "SKILL.md")); err != nil {
		t.Fatalf("starter installation missing after concurrent startups: %v", err)
	}
}

func TestStarterInstallationPreservesIncompleteState(t *testing.T) {
	assetDir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	failure := errors.New("directory sync failed")
	err = installStartersAt(context.Background(), root, func(dir string) error {
		if filepath.Base(dir) == "internet-fetch" {
			return failure
		}
		return syncStarterDirectory(dir)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("install error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".skills-installing")); err != nil {
		t.Fatalf("partial installation lock: %v", err)
	}
	if err := installStartersAt(context.Background(), root, syncStarterDirectory); err == nil || !strings.Contains(err.Error(), "may be incomplete") {
		t.Fatalf("partial installation was accepted: %v", err)
	}
}

func TestStarterInstallationLeavesExistingEmptyDirectoryUntouched(t *testing.T) {
	assetDir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	directory := filepath.Join(root, "skills")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := installStartersAt(context.Background(), root, syncStarterDirectory); err != nil {
		t.Fatalf("empty existing directory = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("existing directory was modified: %v, %v", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "telegram-commands.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing skills directory received a command-reference migration: %v", err)
	}
	checkInvalidExistingSkillText(t)

	// Existing files remain untouched even when package assets are unavailable.
	memoryPath := filepath.Join(root, "memory", "MEMORY.md")
	if err := os.WriteFile(memoryPath, []byte("existing memory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", filepath.Join(root, "missing-assets"))
	if err := installStartersAt(context.Background(), root, syncStarterDirectory); err != nil {
		t.Fatalf("existing files require assets: %v", err)
	}
	if got, err := os.ReadFile(memoryPath); err != nil || string(got) != "existing memory" {
		t.Fatalf("existing memory changed: %q %v", got, err)
	}

	missingRoot := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	if err := installStartersAt(context.Background(), missingRoot, syncStarterDirectory); err == nil {
		t.Fatal("missing starter assets were accepted")
	}
	if _, err := os.Lstat(filepath.Join(missingRoot, "skills")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing assets created skill files: %v", err)
	}
	// A preexisting memory symlink is never followed or overwritten.
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	otherRoot := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	if err := os.MkdirAll(filepath.Join(otherRoot, "memory"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "memory-target")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(otherRoot, "memory", "MEMORY.md")); err == nil {
		if err := installStartersAt(context.Background(), otherRoot, syncStarterDirectory); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "original" {
			t.Fatalf("symlink target changed: %q %v", got, err)
		}
	}
}

func checkInvalidExistingSkillText(t interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	for _, filename := range []string{"SKILL.md", "AUTH.md"} {
		for _, content := range []string{" \n\t", "invalid \xff"} {
			root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
			dir := filepath.Join(root, "skills", "custom")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, text := range map[string]string{
				"META.md":  "name: custom\ndescription: existing skill\n",
				"SKILL.md": "workflow", "AUTH.md": "authentication",
				filename: content,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := installStartersAt(context.Background(), root, syncStarterDirectory); err != nil {
				t.Fatalf("existing skill installation: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, filename))
			if err != nil || string(data) != content {
				t.Fatalf("existing file changed: %q, %v", data, err)
			}
		}
	}
}

func TestStarterInstallationReportsFinalDirectorySyncFailure(t *testing.T) {
	assetDir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", assetDir)
	root := filepath.Join(t.TempDir(), ".goaster", "artefacts")
	failure := errors.New("final sync failed")
	rootSyncs := 0
	err = installStartersAt(context.Background(), root, func(directory string) error {
		if directory == root {
			rootSyncs++
			if rootSyncs == 3 {
				return failure
			}
		}
		return syncStarterDirectory(directory)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("final sync error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "project-memory", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".skills-installing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed installation lock: %v", err)
	}
}
