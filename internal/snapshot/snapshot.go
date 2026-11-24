// Package snapshot gives read-only, path-jailed access to the project snapshot (S-D2, S-D11).
//
// A snapshot is a plain local directory, typically copied onto the VPS with scp or
// rsync and mounted read-only. This package only reads that directory; how it got
// there (ingestion) is someone else's job, so a pull script can be added later
// without touching the runtime (R7).
//
// Every path the model supplies goes through resolve, and every file is opened
// through an os.Root, which refuses ".." and symlinks that lead outside the
// snapshot. Secret-looking files (.env, keys, .git) are hidden entirely.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// ProjectFile is the optional metadata file at the snapshot root.
const ProjectFile = "project.json"

// Errors returned for rejected paths. Their text is shown to the model.
var (
	ErrOutside  = errors.New("path is outside the snapshot")
	ErrDenied   = errors.New("path is not available")
	ErrNotFound = errors.New("no such file or directory")
)

// Project is the optional project.json at the snapshot root.
type Project struct {
	// Name is shown in the overview and, from M3, in the UI.
	Name string `json:"name"`
	// StarterQuestions are offered as clickable prompts in the UI (R11).
	StarterQuestions []string `json:"starter_questions"`
	// OverviewFiles are included in the cached overview, in order. When empty,
	// well-known names are used (see defaultOverviewFiles).
	OverviewFiles []string `json:"overview_files"`
	// SessionsDir holds cleaned conversation exports, indexed in the overview.
	// When empty, "sessions" or "docs/sessions" is used if present.
	SessionsDir string `json:"sessions_dir"`
}

// Limits bounds what the tools and the overview return.
type Limits struct {
	// MaxToolChars caps the text one tool call returns.
	MaxToolChars int
	// MaxOverviewChars caps the generated overview.
	MaxOverviewChars int
}

// Snapshot is an opened snapshot directory.
type Snapshot struct {
	root    *os.Root
	fsys    fs.FS
	limits  Limits
	Project Project
}

// Open opens dir as a snapshot and reads project.json if present.
// A malformed project.json is an error, so a bad deploy fails at startup.
func Open(dir string, limits Limits) (*Snapshot, error) {
	if limits.MaxToolChars <= 0 || limits.MaxOverviewChars <= 0 {
		return nil, errors.New("snapshot: limits must be positive")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	s := &Snapshot{root: root, fsys: root.FS(), limits: limits}
	b, err := fs.ReadFile(s.fsys, ProjectFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		root.Close()
		return nil, fmt.Errorf("snapshot: read %s: %w", ProjectFile, err)
	default:
		if err := json.Unmarshal(b, &s.Project); err != nil {
			root.Close()
			return nil, fmt.Errorf("snapshot: parse %s: %w", ProjectFile, err)
		}
	}
	return s, nil
}

// Close releases the snapshot directory.
func (s *Snapshot) Close() error { return s.root.Close() }

// resolve turns a model-supplied path into a clean path relative to the snapshot
// root, or rejects it. "" and "/" mean the root. It is lexical; os.Root adds the
// symlink check when the path is opened.
func resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsAny(p, "\x00\\") {
		return "", ErrOutside
	}
	if p == "" || p == "/" || p == "." {
		return ".", nil
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return "", ErrOutside
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") || !fs.ValidPath(p) {
		return "", ErrOutside
	}
	if denied(p) {
		return "", ErrDenied
	}
	return p, nil
}

// denied reports whether any part of p looks like VCS internals or a secret.
// Such paths are invisible to the model: not listed, searched, or read.
func denied(p string) bool {
	if p == "." {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if deniedName(part) {
			return true
		}
	}
	return false
}

func deniedName(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case ".git", ".hg", ".svn", ".ssh", ".gnupg", ".aws", ".netrc", ".npmrc", ".pypirc", ".docker":
		return true
	}
	if n == ".env" || strings.HasPrefix(n, ".env.") {
		return true
	}
	for _, pre := range []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"} {
		if strings.HasPrefix(n, pre) {
			return true
		}
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// openErr maps filesystem errors to messages that are safe to show the model.
func openErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case strings.Contains(err.Error(), "escapes"):
		// os.Root refused a symlink or ".." that leads outside the snapshot.
		return ErrOutside
	default:
		return ErrDenied
	}
}
