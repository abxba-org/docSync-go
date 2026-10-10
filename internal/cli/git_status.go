// Ported from packages/cli/src/git-status.ts — a parsed `git status
// --porcelain` line (XY status plus path).
package cli

import "strings"

// PorcelainEntry is one parsed `git status --porcelain` line: the XY status
// plus the (dirty) path, and for renames the original path.
type PorcelainEntry struct {
	// Status is the two-character XY porcelain status.
	Status string
	// Path is the (NEW, for renames) repo-relative path.
	Path string
	// OrigPath is the rename source path (renames only).
	OrigPath string
}

// ParsePorcelainLine parses one `status --porcelain` line to a (status, path)
// pair (XY + path). Renames (` X -> Y`) report status X, the NEW path, and the
// original path. A line shorter than 4 characters is not a valid porcelain line
// and returns nil.
func ParsePorcelainLine(line string) *PorcelainEntry {
	if len(line) < 4 {
		return nil
	}
	status := line[:2]
	path := line[3:]
	arrow := strings.Index(path, " -> ")
	if arrow >= 0 {
		origPath := strings.TrimSpace(path[:arrow])
		newPath := strings.TrimSpace(path[arrow+4:]) // renames: the NEW path is dirty
		return &PorcelainEntry{Status: status, Path: newPath, OrigPath: origPath}
	}
	return &PorcelainEntry{Status: status, Path: strings.TrimSpace(path)}
}
