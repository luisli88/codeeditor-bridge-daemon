package devpod

import (
	"context"
	"errors"
	"testing"
)

// listOnlyRunner implements Runner with only List meaningful — Lister
// never calls Up/Delete, so those two just panic if that assumption ever
// stops holding.
type listOnlyRunner struct {
	ids []string
	err error
}

func (r listOnlyRunner) Up(context.Context, string, func(string)) error { panic("not used by Lister") }
func (r listOnlyRunner) Delete(context.Context, string) error           { panic("not used by Lister") }
func (r listOnlyRunner) List(context.Context) ([]string, error)         { return r.ids, r.err }

func TestLister_List_ClonedAndUp_ReportsReady(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"ws-1"}, nil },
		func(workspacePath string) (string, error) { return "git@github.com:acme/repo.git", nil },
		listOnlyRunner{ids: []string{"ws-1"}},
	)

	infos, err := l.List(context.Background())

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 project, got %d: %v", len(infos), infos)
	}
	if infos[0] != (ProjectInfo{WorkspaceID: "ws-1", RepositoryURL: "git@github.com:acme/repo.git", Status: StatusReady}) {
		t.Fatalf("unexpected project info: %+v", infos[0])
	}
}

func TestLister_List_ClonedButNotUp_ReportsCloning(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"ws-1"}, nil },
		func(workspacePath string) (string, error) { return "git@github.com:acme/repo.git", nil },
		listOnlyRunner{ids: nil},
	)

	infos, err := l.List(context.Background())

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(infos) != 1 || infos[0].Status != StatusCloning {
		t.Fatalf("expected 1 project with status %q, got %+v", StatusCloning, infos)
	}
}

func TestLister_List_StatusMatchIsCaseInsensitive(t *testing.T) {
	// devpod's own `id` field is lowercased internally (confirmed against
	// a real `devpod list --output json`), while workspaceIDs elsewhere in
	// this codebase are the client-generated mixed-case UUID — List must
	// still recognize the same workspace as "up".
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"16861E4B-F021-4B46-8BC0-DAF7EED45B83"}, nil },
		func(workspacePath string) (string, error) { return "git@github.com:acme/repo.git", nil },
		listOnlyRunner{ids: []string{"16861e4b-f021-4b46-8bc0-daf7eed45b83"}},
	)

	infos, err := l.List(context.Background())

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(infos) != 1 || infos[0].Status != StatusReady {
		t.Fatalf("expected status %q, got %+v", StatusReady, infos)
	}
}

func TestLister_List_RepositoryURLUnreadable_SkipsThatWorkspace(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"ws-1", "ws-2"}, nil },
		func(workspacePath string) (string, error) {
			if workspacePath == "/workspaces/ws-1" {
				return "", errors.New("no origin remote")
			}
			return "git@github.com:acme/repo.git", nil
		},
		listOnlyRunner{ids: []string{"ws-2"}},
	)

	infos, err := l.List(context.Background())

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(infos) != 1 || infos[0].WorkspaceID != "ws-2" {
		t.Fatalf("expected only ws-2, got %+v", infos)
	}
}

func TestLister_List_NoClonedWorkspaces_ReturnsEmpty(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return nil, nil },
		func(workspacePath string) (string, error) { return "", nil },
		listOnlyRunner{ids: nil},
	)

	infos, err := l.List(context.Background())

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected no projects, got %+v", infos)
	}
}

func TestLister_List_ListClonedFails_ReturnsError(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return nil, errors.New("disk error") },
		func(workspacePath string) (string, error) { return "", nil },
		listOnlyRunner{},
	)

	_, err := l.List(context.Background())

	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLister_List_RunnerListFails_ReturnsError(t *testing.T) {
	l := NewLister(
		func(workspaceID string) string { return "/workspaces/" + workspaceID },
		func() ([]string, error) { return []string{"ws-1"}, nil },
		func(workspacePath string) (string, error) { return "git@github.com:acme/repo.git", nil },
		listOnlyRunner{err: errors.New("devpod list failed")},
	)

	_, err := l.List(context.Background())

	if err == nil {
		t.Fatal("expected an error")
	}
}
