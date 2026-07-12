package gitmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConflictResolution mirrors the three choices FR-035 requires: "usar el
// mío / usar el otro / editar manualmente".
type ConflictResolution string

const (
	ConflictResolutionMine   ConflictResolution = "mine"
	ConflictResolutionTheirs ConflictResolution = "theirs"
	ConflictResolutionManual ConflictResolution = "manual"
)

// ConflictBlock is one `<<<<<<< / ======= / >>>>>>>` region in a
// conflicted file — "mío" is HEAD's side, "el otro" is the incoming
// branch's side (FR-035).
type ConflictBlock struct {
	Mine   string `json:"mine"`
	Theirs string `json:"theirs"`
}

// ConflictedFile is one file `git` left with conflict markers after a
// merge/pull.
type ConflictedFile struct {
	Path   string          `json:"path"`
	Blocks []ConflictBlock `json:"blocks"`
}

const (
	markerMineStart = "<<<<<<<"
	markerSeparator = "======="
	markerTheirsEnd = ">>>>>>>"
)

// ConflictedFiles lists every file `git` left with unresolved conflict
// markers, per `git diff --name-only --diff-filter=U`, each parsed into
// its block-by-block mine/theirs regions (FR-035).
func (o *Operations) ConflictedFiles(ctx context.Context, workspaceID string) ([]ConflictedFile, error) {
	out, err := o.run(ctx, workspaceID, nil, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}

	var files []ConflictedFile
	for _, path := range strings.Split(strings.TrimSpace(out), "\n") {
		if path == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(o.workspacePath(workspaceID), path))
		if err != nil {
			return nil, fmt.Errorf("gitmanager: read conflicted file %s: %w", path, err)
		}
		files = append(files, ConflictedFile{Path: path, Blocks: parseConflictBlocks(string(content))})
	}
	return files, nil
}

// parseConflictBlocks scans content for `<<<<<<<`/`=======`/`>>>>>>>`
// regions, in order — block index N is the Nth region found top to
// bottom, which is the same order ResolveBlock's index parameter expects.
func parseConflictBlocks(content string) []ConflictBlock {
	var blocks []ConflictBlock
	lines := strings.Split(content, "\n")

	var mine, theirs []string
	state := 0 // 0 = outside a conflict, 1 = in "mine" half, 2 = in "theirs" half
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, markerMineStart):
			state = 1
			mine, theirs = nil, nil
		case strings.HasPrefix(line, markerSeparator) && state == 1:
			state = 2
		case strings.HasPrefix(line, markerTheirsEnd) && state == 2:
			blocks = append(blocks, ConflictBlock{Mine: strings.Join(mine, "\n"), Theirs: strings.Join(theirs, "\n")})
			state = 0
		case state == 1:
			mine = append(mine, line)
		case state == 2:
			theirs = append(theirs, line)
		}
	}
	return blocks
}

// ResolveBlock replaces the block-th conflict region in path with the
// chosen resolution: `mine`/`theirs` use that block's own recorded text,
// `manual` uses manualText verbatim (FR-035). Re-parses the file fresh
// each call, since resolving one block changes every later block's
// position in the file.
func (o *Operations) ResolveBlock(
	ctx context.Context, workspaceID, path string, block int, resolution ConflictResolution, manualText string,
) error {
	fullPath := filepath.Join(o.workspacePath(workspaceID), path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("gitmanager: read conflicted file %s: %w", path, err)
	}

	resolved, err := replaceConflictBlock(string(content), block, resolution, manualText)
	if err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(resolved), 0o644) //nolint:gosec
}

func replaceConflictBlock(content string, target int, resolution ConflictResolution, manualText string) (string, error) {
	lines := strings.Split(content, "\n")
	var out []string
	state := 0
	var mine, theirs []string
	seen := 0

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, markerMineStart):
			state = 1
			mine, theirs = nil, nil
		case strings.HasPrefix(line, markerSeparator) && state == 1:
			state = 2
		case strings.HasPrefix(line, markerTheirsEnd) && state == 2:
			if seen == target {
				out = append(out, resolvedLines(resolution, mine, theirs, manualText)...)
			} else {
				out = append(out, markerMineStart, strings.Join(mine, "\n"), markerSeparator, strings.Join(theirs, "\n"), markerTheirsEnd)
			}
			seen++
			state = 0
		case state == 1:
			mine = append(mine, line)
		case state == 2:
			theirs = append(theirs, line)
		default:
			out = append(out, line)
		}
	}

	if seen <= target {
		return "", fmt.Errorf("gitmanager: no conflict block at index %d (found %d)", target, seen)
	}
	return strings.Join(out, "\n"), nil
}

func resolvedLines(resolution ConflictResolution, mine, theirs []string, manualText string) []string {
	switch resolution {
	case ConflictResolutionMine:
		return mine
	case ConflictResolutionTheirs:
		return theirs
	default:
		return strings.Split(manualText, "\n")
	}
}

// StageResolvedFile runs `git add path` — call once every block in path
// has been resolved, so the merge commit sees no remaining markers.
func (o *Operations) StageResolvedFile(ctx context.Context, workspaceID, path string) error {
	return o.Stage(ctx, workspaceID, []string{path})
}
