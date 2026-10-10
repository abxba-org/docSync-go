package layout

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// LoadDocsyncLayout snapshots the on-disk `.docsync/` tree of a repository and
// delegates to the pure ValidateLayout. It resolves the canonical tree only —
// it does NOT parse manifest contents.
//
// I/O failures return a *LayoutIoError — fail-closed (D8), never "assume
// empty". A missing `.docsync/` is NOT an I/O failure: it is the
// `missing_docsync_dir` configuration violation (D7/D8), returned in the
// result.
func LoadDocsyncLayout(repoRoot string) (LayoutResult, error) {
	docsyncAbs := filepath.Join(repoRoot, schemas.DocSyncDir)

	rootInfo, err := os.Stat(docsyncAbs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isNotDirErr(err) {
			return LayoutResult{Violations: []LayoutViolation{{
				Kind:     ViolationMissingDocsyncDir,
				Path:     schemas.DocSyncDir,
				Expected: schemas.DocSyncDir + "/ as a directory at the repository root",
				Message:  ".docsync/ directory not found — no manifest, no run (D7/D8)",
			}}}, nil
		}
		return LayoutResult{}, NewLayoutIoError(
			"failed to read "+schemas.DocSyncDir+"/ under \""+repoRoot+"\"", err)
	}

	if !rootInfo.IsDir() {
		return LayoutResult{Violations: []LayoutViolation{{
			Kind:     ViolationDirExpected,
			Path:     schemas.DocSyncDir,
			Expected: schemas.DocSyncDir + "/ as a directory at the repository root",
			Message:  "canonical .docsync/ directory exists as a file",
		}}}, nil
	}

	entries, err := collectEntries(docsyncAbs, "")
	if err != nil {
		var ioErr *LayoutIoError
		if errors.As(err, &ioErr) {
			return LayoutResult{}, ioErr
		}
		return LayoutResult{}, NewLayoutIoError(
			"failed to read "+schemas.DocSyncDir+"/ under \""+repoRoot+"\"", err)
	}

	return ValidateLayout(entries), nil
}

// isNotDirErr mirrors Node's ENOTDIR (e.g. `.docsync` resolving through a
// path component that is a file). syscall.ENOTDIR is defined on the Unix and
// Windows syscall packages alike.
func isNotDirErr(err error) bool {
	return errors.Is(err, syscall.ENOTDIR)
}

// collectEntries walks `.docsync/` recursively into a flat LayoutEntry slice
// (directories included). Symlinks are resolved to their target kind (a
// symlinked directory classifies as a dir and is checked against the
// canonical tree) but are NEVER descended into — no traversal loops, no
// reading outside `.docsync/` through a link. Broken symlinks are a typed I/O
// failure, not silently skipped entries (fail-closed).
func collectEntries(dir, relativeBase string) ([]LayoutEntry, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, NewLayoutIoError("failed to list directory \""+dir+"\"", err)
	}

	var entries []LayoutEntry
	for _, dirent := range dirents {
		relativePath := dirent.Name()
		if relativeBase != "" {
			relativePath = relativeBase + "/" + dirent.Name()
		}
		absolutePath := filepath.Join(dir, dirent.Name())

		switch {
		case dirent.Type()&fs.ModeSymlink != 0:
			kind, err := resolvedSymlinkKind(absolutePath, relativePath)
			if err != nil {
				return nil, err
			}
			entries = append(entries, LayoutEntry{Path: relativePath, Kind: kind})
		case dirent.IsDir():
			entries = append(entries, LayoutEntry{Path: relativePath, Kind: LayoutEntryDir})
			nested, err := collectEntries(absolutePath, relativePath)
			if err != nil {
				return nil, err
			}
			entries = append(entries, nested...)
		case dirent.Type().IsRegular():
			entries = append(entries, LayoutEntry{Path: relativePath, Kind: LayoutEntryFile})
		default:
			// Non-regular fs objects (fifo, socket, device): surface them so
			// the validator flags anything non-canonical instead of silently
			// ignoring it (fail-closed).
			entries = append(entries, LayoutEntry{Path: relativePath, Kind: LayoutEntryFile})
		}
	}
	return entries, nil
}

// resolvedSymlinkKind stats through the link (os.Stat follows symlinks) and
// classifies the target kind; a broken or unreadable link is fail-closed.
func resolvedSymlinkKind(absolutePath, relativePath string) (LayoutEntryKind, error) {
	targetInfo, err := os.Stat(absolutePath)
	if err != nil {
		return "", NewLayoutIoError(
			"broken or unreadable symlink at "+docsyncPath(relativePath), err)
	}
	if targetInfo.IsDir() {
		return LayoutEntryDir, nil
	}
	return LayoutEntryFile, nil
}
