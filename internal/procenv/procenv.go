// Package procenv resolves and runs provider CLIs through the plain binary
// rather than through a terminal wrapper shim.
//
// Terminal multiplexers put shim directories on PATH so that every `claude` or
// `codex` launch is intercepted and rewritten. cmux, for example, ships
// `<tmpdir>/cmux-cli-shims/claude`, and its wrapper injects `--session-id` plus
// a full hook set into the child's arguments. A quota probe must observe the
// CLI, not a wrapped editor session: interception registers a session with the
// terminal, spawns that wrapper's hooks once per refresh, and leaves per-session
// state on disk. Resolving and launching with shim directories removed from PATH
// keeps a probe to the binary the user installed.
//
// This package reads the environment only. It never writes, exports, or logs it.
package procenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// shimPathMarker identifies a wrapper shim directory by path component. Shim
// directories are generated per terminal launch under a temporary root, so the
// marker matches the stable component rather than a full path.
const shimPathMarker = "cmux-cli-shims"

// Path returns the process PATH with wrapper shim directories removed.
func Path() string {
	return sanitizePath(os.Getenv("PATH"))
}

// Env returns the process environment with PATH sanitized, so a child that
// resolves a command itself (a shell pipeline, for instance) also skips shims.
func Env() []string {
	environment := os.Environ()
	sanitized := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") {
			continue
		}
		sanitized = append(sanitized, entry)
	}
	return append(sanitized, "PATH="+Path())
}

// LookPath resolves name the way exec.LookPath does, against the sanitized
// PATH. exec.LookPath reads the live environment and cannot be pointed at
// another value, and a child's cmd.Env does not affect resolution performed by
// the parent, so resolution is done here explicitly.
func LookPath(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(Path()) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func sanitizePath(path string) string {
	if path == "" {
		return path
	}
	entries := filepath.SplitList(path)
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.Contains(entry, shimPathMarker) {
			continue
		}
		kept = append(kept, entry)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
