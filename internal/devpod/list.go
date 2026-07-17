package devpod

import (
	"context"
	"fmt"
	"strings"
)

// ProjectInfo is what Lister reconstructs about an already-provisioned
// Project purely from what's observable on disk/via `devpod` — see
// Lister's own doc comment for exactly how much this can and can't
// recover.
type ProjectInfo struct {
	WorkspaceID   string `json:"workspaceId"`
	RepositoryURL string `json:"repositoryUrl"`
	Status        string `json:"status"`
}

type (
	// ListClonedFunc returns every workspaceID that has a cloned
	// repository on disk under --workspace-dir, regardless of how far
	// provisioning got beyond that (gitmanager.Cloner.ListWorkspaceIDs).
	ListClonedFunc func() ([]string, error)
	// RepositoryURLFunc reads a cloned Workspace's own git remote —
	// Cloner doesn't persist the URL anywhere separately, the clone
	// itself is the only record of it (gitmanager.Cloner.RepositoryURL).
	RepositoryURLFunc func(workspacePath string) (string, error)
)

// Lister reconstructs the Projects that already exist on this Host purely
// from external, observable signals — repositoryUrl from each clone's own
// git remote, "ready" vs "cloning" from whether `devpod` actually
// finished `up`-ing it. There is no persisted Proyecto record anywhere
// server-side (Provisioner is deliberately stateless — see its own doc
// comment), so this is a best-effort reconstruction, not stored history:
// a Project stuck mid-provisioning at, say, lsp-bootstrap looks identical
// here to one that's fully `ready` (both have a real devpod workspace
// up), and devcontainerConfig/languageServers/provisioningSteps are never
// recoverable this way at all — a client reconciling against this result
// should treat "ready" as "at least clonado y con contenedor arriba", not
// as a guarantee every bootstrap step actually finished. Good enough to
// make an already-cloned Project visible again after the app's own local
// record of it is gone (a reinstall, a second device); not a substitute
// for real per-project state persistence.
type Lister struct {
	workspacePath WorkspacePathFunc
	listCloned    ListClonedFunc
	repositoryURL RepositoryURLFunc
	runner        Runner
}

// NewLister builds a Lister.
func NewLister(
	workspacePath WorkspacePathFunc,
	listCloned ListClonedFunc,
	repositoryURL RepositoryURLFunc,
	runner Runner,
) *Lister {
	return &Lister{workspacePath: workspacePath, listCloned: listCloned, repositoryURL: repositoryURL, runner: runner}
}

// List returns one ProjectInfo per cloned Workspace. A clone whose git
// remote can't be read (corrupted, `origin` never set) is skipped rather
// than failing the whole list — one bad entry shouldn't hide every other
// real Project.
func (l *Lister) List(ctx context.Context) ([]ProjectInfo, error) {
	workspaceIDs, err := l.listCloned()
	if err != nil {
		return nil, fmt.Errorf("devpod: list cloned workspaces: %w", err)
	}
	upIDs, err := l.runner.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("devpod: list devpod workspaces: %w", err)
	}
	up := make(map[string]bool, len(upIDs))
	for _, id := range upIDs {
		up[strings.ToLower(id)] = true
	}

	infos := make([]ProjectInfo, 0, len(workspaceIDs))
	for _, id := range workspaceIDs {
		repoURL, err := l.repositoryURL(l.workspacePath(id))
		if err != nil {
			continue
		}
		status := StatusCloning
		if up[strings.ToLower(id)] {
			status = StatusReady
		}
		infos = append(infos, ProjectInfo{WorkspaceID: id, RepositoryURL: repoURL, Status: status})
	}
	return infos, nil
}
