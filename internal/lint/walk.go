package lint

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// excludedDirNames are the directory NAMES pruned at any depth by the walk
// (R14 simplification). Pruning is by name, not by path.
var excludedDirNames = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	".docsync":     true,
}

// WalkRepoFiles recursively lists file paths under repoRoot as POSIX-relative
// strings, pruning excluded directory names. The walk is git-agnostic: every
// file except those under node_modules/, .git/, dist/ and .docsync/ is reported;
// hidden files ARE included; symbolic links are skipped (never followed);
// .gitignore is NOT honored. Directory entries are visited in sorted order
// (os.ReadDir already returns entries sorted by filename — matching the TS
// byName sort for POSIX paths) for deterministic output. The underlying fs
// error propagates on failure (fail closed — a failed walk must never look like
// "no files").
func WalkRepoFiles(repoRoot string) ([]string, error) {
	files := []string{}
	var visit func(dir, prefix string) error
	visit = func(dir, prefix string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue // symbolic links are skipped (never followed)
			}
			relPath := entry.Name()
			if prefix != "" {
				relPath = prefix + "/" + entry.Name()
			}
			if entry.IsDir() {
				if excludedDirNames[entry.Name()] {
					continue
				}
				if err := visit(filepath.Join(dir, entry.Name()), relPath); err != nil {
					return err
				}
			} else if entry.Type().IsRegular() {
				files = append(files, relPath)
			}
		}
		return nil
	}
	if err := visit(repoRoot, ""); err != nil {
		return nil, err
	}
	return files, nil
}

// docFilePattern matches the documentation extensions the adapter treats as docs
// (files ending in .md/.mdx/.txt). Repositories with other doc formats should
// call LintManifest with their own docFiles.
var docFilePattern = regexp.MustCompile(`\.(?:md|mdx|txt)$`)

// LintManifestFromRepo lints a manifest against a repository root on disk. The
// adapter decides what counts as documentation for coverage defects: files
// ending in .md/.mdx/.txt. The walk is the git-agnostic simplification documented
// on WalkRepoFiles; Phase 8 wires git ls-files. A walk error propagates (fail
// closed).
func LintManifestFromRepo(manifest schemas.Manifest, repoRoot string) (LintReport, error) {
	repoFiles, err := WalkRepoFiles(repoRoot)
	if err != nil {
		return LintReport{}, err
	}
	docFiles := []string{}
	for _, file := range repoFiles {
		if docFilePattern.MatchString(file) {
			docFiles = append(docFiles, file)
		}
	}
	return LintManifest(LintManifestParams{
		Manifest:  manifest,
		RepoFiles: repoFiles,
		DocFiles:  &docFiles,
	})
}
