package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/filetree"
)

func newTestBrowser(dir string) *filetree.Browser {
	return filetree.NewBrowser(func(workspaceID string) string { return dir })
}

func TestBrowser_List_Root_ReturnsRealEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "hello\n")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	browser := newTestBrowser(dir)

	entries, err := browser.List("ws-1", "")

	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	require.ElementsMatch(t, []string{"README.md", "src"}, names)
}

func TestBrowser_List_Subdirectory_ReturnsItsChildren(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	writeFile(t, dir, "src/main.go", "package main\n")
	browser := newTestBrowser(dir)

	entries, err := browser.List("ws-1", "src")

	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "main.go", entries[0].Name)
	require.Equal(t, "src/main.go", entries[0].Path)
	require.False(t, entries[0].IsDir)
}

func TestBrowser_List_ExcludesGitDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	writeFile(t, dir, "README.md", "hello\n")
	browser := newTestBrowser(dir)

	entries, err := browser.List("ws-1", "")

	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "README.md", entries[0].Name)
}

func TestBrowser_List_MissingPath_ReturnsNotFound(t *testing.T) {
	browser := newTestBrowser(t.TempDir())

	_, err := browser.List("ws-1", "does-not-exist")

	require.ErrorIs(t, err, filetree.ErrNotFound)
}

func TestBrowser_List_OnAFile_ReturnsNotADirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file.txt", "content\n")
	browser := newTestBrowser(dir)

	_, err := browser.List("ws-1", "file.txt")

	require.ErrorIs(t, err, filetree.ErrNotADirectory)
}

func TestBrowser_List_PathEscapesWorkspace_ReturnsInvalidPath(t *testing.T) {
	browser := newTestBrowser(t.TempDir())

	_, err := browser.List("ws-1", "../../etc")

	require.ErrorIs(t, err, filetree.ErrInvalidPath)
}

func TestBrowser_Read_ReturnsRealContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "hello\n")
	browser := newTestBrowser(dir)

	content, err := browser.Read("ws-1", "README.md")

	require.NoError(t, err)
	require.Equal(t, "hello\n", content)
}

func TestBrowser_Read_OnADirectory_ReturnsNotAFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	browser := newTestBrowser(dir)

	_, err := browser.Read("ws-1", "src")

	require.ErrorIs(t, err, filetree.ErrNotAFile)
}

func TestBrowser_Read_MissingPath_ReturnsNotFound(t *testing.T) {
	browser := newTestBrowser(t.TempDir())

	_, err := browser.Read("ws-1", "does-not-exist.txt")

	require.ErrorIs(t, err, filetree.ErrNotFound)
}

func TestBrowser_Read_PathEscapesWorkspace_ReturnsInvalidPath(t *testing.T) {
	browser := newTestBrowser(t.TempDir())

	_, err := browser.Read("ws-1", "../../etc/passwd")

	require.ErrorIs(t, err, filetree.ErrInvalidPath)
}

func TestBrowser_Write_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "old content\n")
	browser := newTestBrowser(dir)

	err := browser.Write("ws-1", "README.md", "new content\n")

	require.NoError(t, err)
	content, err := browser.Read("ws-1", "README.md")
	require.NoError(t, err)
	require.Equal(t, "new content\n", content)
}

func TestBrowser_Write_MissingFile_CreatesIt(t *testing.T) {
	dir := t.TempDir()
	browser := newTestBrowser(dir)

	err := browser.Write("ws-1", "new.txt", "content\n")

	require.NoError(t, err)
	content, err := browser.Read("ws-1", "new.txt")
	require.NoError(t, err)
	require.Equal(t, "content\n", content)
}

func TestBrowser_Write_OnADirectory_ReturnsNotAFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	browser := newTestBrowser(dir)

	err := browser.Write("ws-1", "src", "content\n")

	require.ErrorIs(t, err, filetree.ErrNotAFile)
}

func TestBrowser_Write_PathEscapesWorkspace_ReturnsInvalidPath(t *testing.T) {
	browser := newTestBrowser(t.TempDir())

	err := browser.Write("ws-1", "../../etc/passwd", "pwned\n")

	require.ErrorIs(t, err, filetree.ErrInvalidPath)
}
