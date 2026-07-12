package gitmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DevcontainerConfig is deliberately untyped beyond "a JSON object" —
// FR-026 only requires detecting/generating it and showing it editable
// before provisioning, not validating every possible devcontainer.json
// field DevPod understands.
type DevcontainerConfig map[string]any

// devcontainerPaths are checked in order — the spec-compliant location
// first, then the legacy root-level file some older repos still use.
var devcontainerPaths = []string{
	filepath.Join(".devcontainer", "devcontainer.json"),
	"devcontainer.json",
}

// languageMarkers maps a marker file (relative to the repo root) to the
// language it indicates — order matters where a repo could match more
// than one (e.g. a Go backend with a package.json for its docs site):
// the first match in this slice order becomes the primary language for
// DefaultConfigFor's image pick, but DetectLanguages returns every match
// for the LSP bootstrap step, which installs one per detected language.
var languageMarkers = []struct {
	file     string
	language string
}{
	{"go.mod", "go"},
	{"Cargo.toml", "rust"},
	{"Package.swift", "swift"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	{"build.gradle.kts", "java"},
	{"requirements.txt", "python"},
	{"pyproject.toml", "python"},
	{"tsconfig.json", "typescript"},
	{"package.json", "javascript"},
}

// devcontainerImages mirrors the standard images at
// mcr.microsoft.com/devcontainers/<language> — the same base images
// GitHub Codespaces and VS Code Dev Containers use, kept identical here
// on purpose so a generated devcontainer.json behaves the same everywhere
// (research.md §3: DevPod's whole value is devcontainer.json portability).
var devcontainerImages = map[string]string{
	"go":         "mcr.microsoft.com/devcontainers/go:latest",
	"rust":       "mcr.microsoft.com/devcontainers/rust:latest",
	"swift":      "mcr.microsoft.com/devcontainers/swift:latest",
	"java":       "mcr.microsoft.com/devcontainers/java:latest",
	"python":     "mcr.microsoft.com/devcontainers/python:latest",
	"typescript": "mcr.microsoft.com/devcontainers/typescript-node:latest",
	"javascript": "mcr.microsoft.com/devcontainers/javascript-node:latest",
}

const defaultImage = "mcr.microsoft.com/devcontainers/base:ubuntu"

// DetectLanguages returns every language repoPath matches a marker file
// for, in languageMarkers order. Empty if none match (a generic/unknown
// project still gets a usable default via DefaultConfigFor).
func DetectLanguages(repoPath string) []string {
	var languages []string
	seen := make(map[string]bool)
	for _, marker := range languageMarkers {
		if _, err := os.Stat(filepath.Join(repoPath, marker.file)); err != nil {
			continue
		}
		if seen[marker.language] {
			continue
		}
		seen[marker.language] = true
		languages = append(languages, marker.language)
	}
	return languages
}

// DetectOrGenerate reads repoPath's own devcontainer.json (FR-026) if
// present, otherwise generates a default one from the detected
// languages — always editable by the caller before provisioning.
func DetectOrGenerate(repoPath string) (DevcontainerConfig, error) {
	for _, relPath := range devcontainerPaths {
		data, err := os.ReadFile(filepath.Join(repoPath, relPath))
		if err != nil {
			continue
		}
		var config DevcontainerConfig
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, err
		}
		return config, nil
	}
	return DefaultConfigFor(DetectLanguages(repoPath)), nil
}

// DefaultConfigFor generates a minimal devcontainer.json for the given
// detected languages, using the first as the primary (base image)
// language.
func DefaultConfigFor(languages []string) DevcontainerConfig {
	image := defaultImage
	if len(languages) > 0 {
		if mapped, ok := devcontainerImages[languages[0]]; ok {
			image = mapped
		}
	}
	return DevcontainerConfig{
		"image": image,
	}
}
