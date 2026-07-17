package gitmanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LocalFileStore is a SecretStore for self-hosted Hosts that don't have
// (and, per Principio II, never will have) an AWS account of their own —
// AWS Secrets Manager (store_secretsmanager.go) is the only production
// implementation otherwise, which made self-hosted git credentials a
// hard AWS dependency even for a Host with zero managed-tier involvement.
// Secrets are written one file per ref, 0600/dirs 0700, matching the
// permissions convention already used for the clone credential temp
// files elsewhere in this package.
type LocalFileStore struct {
	baseDir string
}

// NewLocalFileStore builds a LocalFileStore rooted at baseDir. baseDir is
// created (0700) on first Put if it doesn't exist yet.
func NewLocalFileStore(baseDir string) *LocalFileStore {
	return &LocalFileStore{baseDir: baseDir}
}

func (s *LocalFileStore) Put(_ context.Context, ref string, value string) error {
	path, err := s.pathFor(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("gitmanager: create secret dir for %q: %w", ref, err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		return fmt.Errorf("gitmanager: write secret %q: %w", ref, err)
	}
	return nil
}

// List returns every ref under baseDir whose path starts with prefix,
// walking subdirectories (a ref like "codeeditor/git-credentials/<owner>/
// <id>" is itself nested a few levels deep). A prefix directory that
// doesn't exist yet — no credentials registered for this owner at all —
// returns an empty slice, not an error.
func (s *LocalFileStore) List(_ context.Context, prefix string) ([]string, error) {
	if strings.Contains(prefix, "..") {
		return nil, errors.New("gitmanager: invalid secret prefix")
	}
	root := filepath.Join(s.baseDir, filepath.Clean(prefix))
	var refs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.baseDir, path)
		if relErr != nil {
			return relErr
		}
		refs = append(refs, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gitmanager: list secrets under %q: %w", prefix, err)
	}
	return refs, nil
}

func (s *LocalFileStore) Get(_ context.Context, ref string) (string, error) {
	path, err := s.pathFor(ref)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("gitmanager: read secret %q: %w", ref, err)
	}
	return string(data), nil
}

// pathFor rejects a ref that would escape baseDir. Every ref in practice
// comes from CredentialManager.secretRef (never raw user input), but this
// costs nothing and turns a hypothetical future caller's mistake into an
// error instead of a path-traversal write/read. Rejecting any ".." makes
// escaping baseDir via filepath.Join impossible, so there's nothing
// further to check once that's done.
func (s *LocalFileStore) pathFor(ref string) (string, error) {
	if ref == "" || strings.Contains(ref, "..") {
		return "", errors.New("gitmanager: invalid secret ref")
	}
	return filepath.Join(s.baseDir, filepath.Clean(ref)), nil
}
