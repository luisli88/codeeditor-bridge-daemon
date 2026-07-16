// Package filetree implements the `fs` channel (contracts/websocket-protocol.md
// → Canal `fs`): directory listing, file content, and saving a file back —
// no rename/delete/filesystem watching, deferred to a later increment.
package filetree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Request is the `fs` channel's request payload. Action mirrors
// devpod.RunPayload.Action's naming convention — a flat discriminator, not
// gitmanager.GitOperationRequest's domain-specific "Phase" (`fs` has no
// multi-step workflow to name phases of). Path is always workspace-relative;
// "" means the workspace root.
type Request struct {
	Action  string `json:"action"` // "list" | "read" | "write"
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"` // "write" only — the file's full new content
}

// Entry is one child of a listed directory — metadata only
// (contracts/websocket-protocol.md: "no contenido completo, salvo al abrir
// un archivo específico").
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // workspace-relative; feed straight back as a follow-up Request.Path
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size,omitempty"`
}

// Response is the `fs` channel's response payload. Action always echoes the
// request's Action.
type Response struct {
	Action  string  `json:"action"`
	Path    string  `json:"path"`
	Entries []Entry `json:"entries,omitempty"` // "list"
	// Content is sent as-is via string(data) for "read" — a genuinely binary
	// file will come through as mangled UTF-8 (encoding/json replaces
	// invalid bytes with U+FFFD). EditorViewModel/Runestone is a text editor
	// with nothing to do with a binary file anyway, so this is a known,
	// deliberately unhandled edge for this increment, not an oversight.
	Content string `json:"content,omitempty"`
}

var (
	ErrInvalidPath   = errors.New("filetree: path escapes workspace root")
	ErrNotFound      = errors.New("filetree: not found")
	ErrNotAFile      = errors.New("filetree: is a directory, not a file")
	ErrNotADirectory = errors.New("filetree: is a file, not a directory")
)

// Browser implements the `fs` channel's Actions against a Workspace's real
// filesystem clone.
type Browser struct {
	workspacePath func(workspaceID string) string
}

// NewBrowser builds a Browser rooted at workspacePath — the same EFS layout
// gitmanager.Cloner.WorkspacePath/Operations already use.
func NewBrowser(workspacePath func(workspaceID string) string) *Browser {
	return &Browser{workspacePath: workspacePath}
}

// resolvePath rejects a client-supplied path that would escape workspaceID's
// root — same rejection rule as gitmanager.LocalFileStore.pathFor (reject any
// ".." segment, then filepath.Join+Clean). Unlike that ref (never raw user
// input), Path here IS untrusted client input crossing a real filesystem-read
// boundary, so this guard is load-bearing, not defense in depth. Not handled:
// a symlink committed inside the repo pointing outside the workspace root
// would still be followed — planting one already requires write access to
// the repo, which the `run` channel already grants equivalent-or-greater
// access via arbitrary command execution, so this is deliberately out of
// scope for this increment.
func (b *Browser) resolvePath(workspaceID, path string) (string, error) {
	if strings.Contains(path, "..") {
		return "", ErrInvalidPath
	}
	return filepath.Join(b.workspacePath(workspaceID), filepath.Clean(path)), nil
}

// List returns path's directory children, relative to workspaceID's root.
func (b *Browser) List(workspaceID, path string) ([]Entry, error) {
	full, err := b.resolvePath(workspaceID, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	switch {
	case os.IsNotExist(err):
		return nil, fmt.Errorf("%w: %q", ErrNotFound, path)
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, fmt.Errorf("%w: %q", ErrNotADirectory, path)
	}
	dirEntries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.Name() == ".git" {
			continue // never surface the clone's own .git internals
		}
		var size int64
		if childInfo, err := de.Info(); err == nil {
			size = childInfo.Size()
		}
		entries = append(entries, Entry{
			Name: de.Name(), Path: filepath.ToSlash(filepath.Join(path, de.Name())), IsDir: de.IsDir(), Size: size,
		})
	}
	return entries, nil
}

// Read returns path's file content, relative to workspaceID's root.
func (b *Browser) Read(workspaceID, path string) (string, error) {
	full, err := b.resolvePath(workspaceID, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("%w: %q", ErrNotFound, path)
	case err != nil:
		return "", err
	case info.IsDir():
		return "", fmt.Errorf("%w: %q", ErrNotAFile, path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Write overwrites path's file content, relative to workspaceID's root.
// Creates the file if it doesn't already exist, but not a missing parent
// directory — the editor only ever opens a file `List` already reported,
// so there's always a real parent directory for a save to land in; a
// brand-new file (not opened from an existing listing) isn't a case this
// increment needs to handle.
func (b *Browser) Write(workspaceID, path, content string) error {
	full, err := b.resolvePath(workspaceID, path)
	if err != nil {
		return err
	}
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		return fmt.Errorf("%w: %q", ErrNotAFile, path)
	}
	return os.WriteFile(full, []byte(content), 0o644)
}
