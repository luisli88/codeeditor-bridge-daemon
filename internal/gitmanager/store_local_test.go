package gitmanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFileStore_PutThenGet_RoundTrips(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	if err := store.Put(ctx, "codeeditor/git-credentials/user-1/cred-1", "super-secret"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "codeeditor/git-credentials/user-1/cred-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "super-secret" {
		t.Errorf("got %q, want %q", got, "super-secret")
	}
}

func TestLocalFileStore_Put_WritesRestrictivePermissions(t *testing.T) {
	baseDir := t.TempDir()
	store := NewLocalFileStore(baseDir)
	ctx := context.Background()

	if err := store.Put(ctx, "codeeditor/git-credentials/user-1/cred-1", "secret"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := os.Stat(filepath.Join(baseDir, "codeeditor/git-credentials/user-1/cred-1"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestLocalFileStore_Get_MissingRef_ReturnsError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())

	if _, err := store.Get(context.Background(), "codeeditor/git-credentials/user-1/does-not-exist"); err == nil {
		t.Error("expected an error for a missing ref, got nil")
	}
}

func TestLocalFileStore_PathTraversalRef_ReturnsError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	if err := store.Put(ctx, "../escape", "secret"); err == nil {
		t.Error("expected Put to reject a path-traversal ref, got nil")
	}
	if _, err := store.Get(ctx, "../escape"); err == nil {
		t.Error("expected Get to reject a path-traversal ref, got nil")
	}
}

func TestLocalFileStore_Put_DirCreationFails_ReturnsError(t *testing.T) {
	baseDir := t.TempDir()
	// A regular file where Put needs to mkdir a directory — MkdirAll
	// must fail with "not a directory".
	blocker := filepath.Join(baseDir, "codeeditor")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	store := NewLocalFileStore(baseDir)

	if err := store.Put(context.Background(), "codeeditor/git-credentials/user-1/cred-1", "secret"); err == nil {
		t.Error("expected Put to fail when its parent directory can't be created, got nil")
	}
}

func TestLocalFileStore_EmptyRef_ReturnsError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	if err := store.Put(ctx, "", "secret"); err == nil {
		t.Error("expected Put to reject an empty ref, got nil")
	}
}

func TestLocalFileStore_List_ReturnsOnlyRefsUnderPrefix(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())
	ctx := context.Background()

	for _, ref := range []string{
		"codeeditor/git-credentials/owner-1/cred-1",
		"codeeditor/git-credentials/owner-1/cred-1.meta.json",
		"codeeditor/git-credentials/owner-1/cred-2",
		"codeeditor/git-credentials/owner-2/cred-1",
	} {
		if err := store.Put(ctx, ref, "value"); err != nil {
			t.Fatalf("Put(%q): %v", ref, err)
		}
	}

	refs, err := store.List(ctx, "codeeditor/git-credentials/owner-1/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs under owner-1, got %d: %v", len(refs), refs)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "codeeditor/git-credentials/owner-1/") {
			t.Errorf("List leaked a ref outside its prefix: %q", ref)
		}
	}
}

// A prefix nobody has ever written under (no credentials registered for
// that owner yet, e.g. a Host reconnecting for the first time) is not an
// error — CredentialManager.List needs an empty result here, not a
// filesystem error, to report "no credentials" instead of failing outright.
func TestLocalFileStore_List_PrefixNeverWritten_ReturnsEmptyNotError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())

	refs, err := store.List(context.Background(), "codeeditor/git-credentials/never-seen/")

	if err != nil {
		t.Fatalf("expected no error for an unwritten prefix, got %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected no refs, got %v", refs)
	}
}

func TestLocalFileStore_List_PathTraversalPrefix_ReturnsError(t *testing.T) {
	store := NewLocalFileStore(t.TempDir())

	if _, err := store.List(context.Background(), "../escape"); err == nil {
		t.Error("expected List to reject a path-traversal prefix, got nil")
	}
}
