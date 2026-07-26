package procenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathDropsShimDirectories(t *testing.T) {
	shim := filepath.Join(os.TempDir(), "cmux-cli-shims", "ABC123")
	t.Setenv("PATH", strings.Join([]string{shim, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	got := Path()
	want := strings.Join([]string{"/usr/bin", "/bin"}, string(os.PathListSeparator))
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestEnvReplacesPathExactlyOnce(t *testing.T) {
	shim := filepath.Join(os.TempDir(), "cmux-cli-shims", "ABC123")
	t.Setenv("PATH", shim+string(os.PathListSeparator)+"/usr/bin")

	var pathEntries []string
	for _, entry := range Env() {
		if strings.HasPrefix(entry, "PATH=") {
			pathEntries = append(pathEntries, entry)
		}
	}
	if len(pathEntries) != 1 {
		t.Fatalf("Env() PATH entries = %v, want exactly one", pathEntries)
	}
	if pathEntries[0] != "PATH=/usr/bin" {
		t.Fatalf("Env() PATH = %q, want %q", pathEntries[0], "PATH=/usr/bin")
	}
}

func TestLookPathSkipsShimAndFindsRealBinary(t *testing.T) {
	shimDir := filepath.Join(t.TempDir(), "cmux-cli-shims")
	realDir := t.TempDir()
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatalf("create shim dir: %v", err)
	}
	shimBinary := filepath.Join(shimDir, "faketool")
	realBinary := filepath.Join(realDir, "faketool")
	for _, path := range []string{shimBinary, realBinary} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// Shim first, exactly how a terminal wrapper prepends it.
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)

	got, err := LookPath("faketool")
	if err != nil {
		t.Fatalf("LookPath(faketool) error = %v", err)
	}
	if got != realBinary {
		t.Fatalf("LookPath(faketool) = %q, want %q", got, realBinary)
	}
}

func TestLookPathReportsNotFoundWhenOnlyShimHasIt(t *testing.T) {
	shimDir := filepath.Join(t.TempDir(), "cmux-cli-shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatalf("create shim dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "faketool"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write shim binary: %v", err)
	}
	t.Setenv("PATH", shimDir)

	if _, err := LookPath("faketool"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("LookPath(faketool) error = %v, want exec.ErrNotFound", err)
	}
}

func TestLookPathIgnoresDirectoriesAndNonExecutables(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "faketool"), 0o755); err != nil {
		t.Fatalf("create decoy dir: %v", err)
	}
	realDir := t.TempDir()
	realBinary := filepath.Join(realDir, "faketool")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write real binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+realDir)

	got, err := LookPath("faketool")
	if err != nil {
		t.Fatalf("LookPath(faketool) error = %v", err)
	}
	if got != realBinary {
		t.Fatalf("LookPath(faketool) = %q, want %q", got, realBinary)
	}
}
