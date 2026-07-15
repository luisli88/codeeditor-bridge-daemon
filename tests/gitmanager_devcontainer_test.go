package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/gitmanager"
)

func TestDetectLanguages_MatchesMarkerFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n")
	writeFile(t, dir, "package.json", "{}\n")

	languages := gitmanager.DetectLanguages(dir)

	require.ElementsMatch(t, []string{"go", "javascript"}, languages)
}

func TestDetectLanguages_NoMarkers_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	languages := gitmanager.DetectLanguages(dir)

	require.Empty(t, languages)
}

// FR-026: an existing devcontainer.json is detected and returned as-is.
func TestDetectOrGenerate_ExistingDevcontainerJSON_IsDetected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755))
	writeFile(t, dir, filepath.Join(".devcontainer", "devcontainer.json"), `{"image": "custom:latest"}`)

	config, err := gitmanager.DetectOrGenerate(dir)

	require.NoError(t, err)
	require.Equal(t, "custom:latest", config["image"])
}

// FR-026: no devcontainer.json anywhere falls back to a generated default.
func TestDetectOrGenerate_NoExistingConfig_GeneratesDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n")

	config, err := gitmanager.DetectOrGenerate(dir)

	require.NoError(t, err)
	require.Equal(t, "mcr.microsoft.com/devcontainers/go:latest", config["image"])
}

// `devpod up` only ever reads .devcontainer/devcontainer.json off disk —
// it has no API for an in-memory config — so a generated config that's
// never written is invisible to it and it silently falls back to its own
// detection instead (confirmed by hand: it picked javascript-node over a
// generated typescript-node for a repo with both tsconfig.json and
// package.json). The generated config must actually land on disk.
func TestDetectOrGenerate_NoExistingConfig_WritesGeneratedFileToDisk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n")

	_, err := gitmanager.DetectOrGenerate(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), "mcr.microsoft.com/devcontainers/go:latest")
}

// An existing devcontainer.json must be left byte-untouched — it's
// already what `devpod up` will read, re-writing it risks reformatting
// or clobbering fields DevcontainerConfig doesn't round-trip perfectly.
func TestDetectOrGenerate_ExistingDevcontainerJSON_IsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755))
	original := `{"image": "custom:latest"}`
	writeFile(t, dir, filepath.Join(".devcontainer", "devcontainer.json"), original)

	_, err := gitmanager.DetectOrGenerate(dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"))
	require.NoError(t, err)
	require.Equal(t, original, string(data))
}

func TestDetectOrGenerate_LegacyRootDevcontainerJSON_IsDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devcontainer.json", `{"image": "legacy:latest"}`)

	config, err := gitmanager.DetectOrGenerate(dir)

	require.NoError(t, err)
	require.Equal(t, "legacy:latest", config["image"])
}

func TestDefaultConfigFor_UnknownLanguage_UsesBaseImage(t *testing.T) {
	config := gitmanager.DefaultConfigFor([]string{"cobol"})

	require.Equal(t, "mcr.microsoft.com/devcontainers/base:ubuntu", config["image"])
}

func TestDefaultConfigFor_NoLanguages_UsesBaseImage(t *testing.T) {
	config := gitmanager.DefaultConfigFor(nil)

	require.Equal(t, "mcr.microsoft.com/devcontainers/base:ubuntu", config["image"])
}
