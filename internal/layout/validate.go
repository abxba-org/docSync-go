package layout

import (
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// LayoutEntryKind is the observed kind of one `.docsync/` entry.
type LayoutEntryKind string

// Entry kinds (TS: 'file' | 'dir').
const (
	LayoutEntryFile LayoutEntryKind = "file"
	LayoutEntryDir  LayoutEntryKind = "dir"
)

// LayoutEntry is one observed entry under `.docsync/`; Path is
// `.docsync/`-relative POSIX.
type LayoutEntry struct {
	Path string          `json:"path"`
	Kind LayoutEntryKind `json:"kind"`
}

// LayoutViolationKind enumerates the D8 hard configuration errors the
// canonical-tree check can raise.
type LayoutViolationKind string

// Violation kinds (TS union, values preserved verbatim).
const (
	ViolationMisplacedFile       LayoutViolationKind = "misplaced_file"
	ViolationUnknownEntry        LayoutViolationKind = "unknown_entry"
	ViolationMemoryExtraFile     LayoutViolationKind = "memory_extra_file"
	ViolationStateUnknownEntry   LayoutViolationKind = "state_unknown_entry"
	ViolationMissingRequiredFile LayoutViolationKind = "missing_required_file"
	ViolationMissingDocsyncDir   LayoutViolationKind = "missing_docsync_dir"
	ViolationDirExpected         LayoutViolationKind = "dir_expected"
	ViolationFileExpected        LayoutViolationKind = "file_expected"
)

// LayoutViolation is one canonical-tree violation with the line-precise
// rendering `<path>: <message> (expected <expected>)`.
type LayoutViolation struct {
	Kind     LayoutViolationKind `json:"kind"`
	Path     string              `json:"path"`
	Expected string              `json:"expected"`
	Message  string              `json:"message"`
}

// CanonicalLayout is the tree a successful validation resolves to.
// ManifestPath is always present (D7/D8: no manifest, no run); everything
// else is presence-optional (TS optional fields serialize as omitempty).
type CanonicalLayout struct {
	ManifestPath     string   `json:"manifestPath"`
	PolicyPath       string   `json:"policyPath,omitempty"`
	PolicyHistoryDir string   `json:"policyHistoryDir,omitempty"`
	ConceptsPath     string   `json:"conceptsPath,omitempty"`
	InstructionsPath string   `json:"instructionsPath,omitempty"`
	ModelsPath       string   `json:"modelsPath,omitempty"`
	EvaluationDir    string   `json:"evaluationDir,omitempty"`
	MemoryDir        string   `json:"memoryDir,omitempty"`
	MemoryFiles      []string `json:"memoryFiles,omitempty"`
	StateDir         string   `json:"stateDir,omitempty"`
	StateSubdirs     []string `json:"stateSubdirs,omitempty"`
	StateFiles       []string `json:"stateFiles,omitempty"`
}

// LayoutResult is the TS `LayoutValidationOk | LayoutValidationErr` union:
// either OK with the resolved canonical layout, or the full violation list.
type LayoutResult struct {
	// OK reports whether the layout is canonical.
	OK bool
	// Resolved is the canonical layout (valid when OK).
	Resolved CanonicalLayout
	// Violations lists every violation, sorted deterministically (valid when !OK).
	Violations []LayoutViolation
}

// Canonical name sets (TS: CONFIG_FILE_SET etc.), built once from the
// single-source-of-truth constants in internal/schemas.
var (
	configFileSet  = stringSet(schemas.ConfigFiles)
	memoryFileSet  = stringSet(schemas.MemoryFiles)
	stateFileSet   = stringSet(schemas.StateFiles)
	stateSubdirSet = stringSet(schemas.StateSubdirs)
	topLevelDirSet = stringSet(schemas.TopLevelDirs)
	docSyncDir     = schemas.DocSyncDir
	docSyncPrefix  = docSyncDir + "/"
	topLevelExpect = buildTopLevelExpected()
	memoryExpected = buildMemoryExpected()
	stateExpected  = buildStateExpected()
)

const (
	msgUnknownEntry    = "entry is not part of the canonical .docsync/ tree"
	msgMisplacedFile   = "canonical file name found outside its canonical location"
	msgMisplacedTopDir = "canonical top-level directory found in a nested location"
	msgMemoryExtra     = "memory/ must contain exactly the four canonical memory files"
	msgStateUnknown    = "state/ must contain only the canonical state entries"
	msgMissingManifest = "manifest.yml is required — no manifest, no run (D7/D8)"
	msgDirExpected     = "canonical directory exists as a file"
	msgFileExpected    = "canonical file exists as a directory"
)

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func buildTopLevelExpected() string {
	files := make([]string, len(schemas.ConfigFiles))
	for i, file := range schemas.ConfigFiles {
		files[i] = docSyncPrefix + file
	}
	dirs := make([]string, len(schemas.TopLevelDirs))
	for i, dir := range schemas.TopLevelDirs {
		dirs[i] = docSyncPrefix + dir + "/"
	}
	return strings.Join(files, ", ") + " or " + strings.Join(dirs, ", ")
}

func buildMemoryExpected() string {
	return docSyncPrefix + "memory/ containing exactly " + strings.Join(schemas.MemoryFiles, ", ")
}

func buildStateExpected() string {
	dirs := make([]string, len(schemas.StateSubdirs))
	for i, dir := range schemas.StateSubdirs {
		dirs[i] = dir + "/"
	}
	return docSyncPrefix + "state/ containing only " + strings.Join(dirs, ", ") +
		" (directories) and " + strings.Join(schemas.StateFiles, ", ") + " (files)"
}

func docsyncPath(relative string) string { return docSyncPrefix + relative }

func newViolation(kind LayoutViolationKind, path, expected, message string) LayoutViolation {
	return LayoutViolation{Kind: kind, Path: path, Expected: expected, Message: message}
}

// isWellFormedRelativePath: relative POSIX path with no empty/`.`/`..`
// segments, no backslashes, no NUL.
func isWellFormedRelativePath(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/") {
		return false
	}
	if strings.Contains(path, "\\") {
		return false
	}
	if strings.Contains(path, "\x00") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// classifyEntry classifies one observed entry against the canonical D1 tree.
// Purely path- and kind-based: no cross-entry lookups, so partial snapshots
// classify deterministically. Returns nil when the entry is canonical; the
// TS EntryClassification carried role/name fields for the ok verdicts, but
// no consumer read them (buildResolved works off the kind map), so the port
// keeps only the violation branch.
func classifyEntry(entry LayoutEntry) *LayoutViolation {
	path, kind := entry.Path, entry.Kind
	if !isWellFormedRelativePath(path) {
		return &LayoutViolation{
			Kind:     ViolationUnknownEntry,
			Path:     path,
			Expected: "a relative POSIX path without empty/'.'/'..' segments, backslashes or NUL",
			Message:  msgUnknownEntry,
		}
	}

	segments := strings.Split(path, "/")
	head := segments[0]
	base := segments[len(segments)-1]
	// second is "" when the path has a single segment (TS: undefined); the
	// branches that read it only run for paths nested 3+ deep, where it is
	// always present.
	var second string
	if len(segments) > 1 {
		second = segments[1]
	}

	if len(segments) == 1 {
		if configFileSet[head] {
			if kind == LayoutEntryFile {
				return nil
			}
			return &LayoutViolation{
				Kind:     ViolationFileExpected,
				Path:     path,
				Expected: docsyncPath(head) + " as a file",
				Message:  msgFileExpected,
			}
		}
		if topLevelDirSet[head] {
			if kind == LayoutEntryDir {
				return nil
			}
			return &LayoutViolation{
				Kind:     ViolationDirExpected,
				Path:     path,
				Expected: docsyncPath(head) + "/ as a directory",
				Message:  msgDirExpected,
			}
		}
		return &LayoutViolation{Kind: ViolationUnknownEntry, Path: path, Expected: topLevelExpect, Message: msgUnknownEntry}
	}

	// A canonical CONFIG file name found anywhere other than its top-level location.
	if configFileSet[base] {
		if kind == LayoutEntryDir {
			return &LayoutViolation{
				Kind:     ViolationFileExpected,
				Path:     path,
				Expected: docsyncPath(base) + " as a file (canonical config file name used as a directory)",
				Message:  msgFileExpected,
			}
		}
		return &LayoutViolation{
			Kind:     ViolationMisplacedFile,
			Path:     path,
			Expected: docsyncPath(base),
			Message:  msgMisplacedFile,
		}
	}

	// A canonical CONFIG file name used as a directory (entries nested below it).
	if configFileSet[head] {
		return &LayoutViolation{
			Kind:     ViolationFileExpected,
			Path:     path,
			Expected: docsyncPath(head) + " as a file (nested entries are not allowed)",
			Message:  msgFileExpected,
		}
	}

	if !topLevelDirSet[head] {
		return &LayoutViolation{Kind: ViolationUnknownEntry, Path: path, Expected: topLevelExpect, Message: msgUnknownEntry}
	}

	switch head {
	case "evaluation", "policy-history":
		// Free-form contents (evaluation holds anything; policy-history holds
		// versioned policies) — canonical CONFIG file names were already
		// rejected above.
		return nil
	case "memory":
		if len(segments) == 2 {
			if memoryFileSet[base] {
				if kind == LayoutEntryFile {
					return nil
				}
				return &LayoutViolation{
					Kind:     ViolationFileExpected,
					Path:     path,
					Expected: docsyncPath(path) + " as a file",
					Message:  msgFileExpected,
				}
			}
			if topLevelDirSet[base] {
				return &LayoutViolation{
					Kind:     ViolationMisplacedFile,
					Path:     path,
					Expected: docsyncPath(base) + "/ at the .docsync/ top level",
					Message:  msgMisplacedTopDir,
				}
			}
			return &LayoutViolation{Kind: ViolationMemoryExtraFile, Path: path, Expected: memoryExpected, Message: msgMemoryExtra}
		}
		// Nested deeper than `memory/<file>`: either a misplaced top-level dir
		// subtree (e.g. memory/policy-history/v1.json — the V-P0-3 named case)
		// or stray nesting.
		if topLevelDirSet[second] {
			return &LayoutViolation{
				Kind:     ViolationMisplacedFile,
				Path:     path,
				Expected: docsyncPath(second) + "/ at the .docsync/ top level",
				Message:  msgMisplacedTopDir,
			}
		}
		return &LayoutViolation{
			Kind:     ViolationMemoryExtraFile,
			Path:     path,
			Expected: memoryExpected + " (no nesting)",
			Message:  msgMemoryExtra,
		}
	case "state":
		if len(segments) == 2 {
			if stateFileSet[base] {
				if kind == LayoutEntryFile {
					return nil
				}
				return &LayoutViolation{
					Kind:     ViolationFileExpected,
					Path:     path,
					Expected: docsyncPath(path) + " as a file",
					Message:  msgFileExpected,
				}
			}
			if stateSubdirSet[base] {
				if kind == LayoutEntryDir {
					return nil
				}
				return &LayoutViolation{
					Kind:     ViolationDirExpected,
					Path:     path,
					Expected: docsyncPath(path) + "/ as a directory",
					Message:  msgDirExpected,
				}
			}
			return &LayoutViolation{Kind: ViolationStateUnknownEntry, Path: path, Expected: stateExpected, Message: msgStateUnknown}
		}
		// Deeper than `state/<entry>`: free-form under canonical state subdirs
		// (system-written artifacts); stray under anything else.
		if stateSubdirSet[second] {
			return nil
		}
		if stateFileSet[second] {
			return &LayoutViolation{
				Kind:     ViolationFileExpected,
				Path:     path,
				Expected: docsyncPath("state/"+second) + " as a file (nested entries are not allowed)",
				Message:  msgFileExpected,
			}
		}
		return &LayoutViolation{Kind: ViolationStateUnknownEntry, Path: path, Expected: stateExpected, Message: msgStateUnknown}
	default:
		return &LayoutViolation{Kind: ViolationUnknownEntry, Path: path, Expected: topLevelExpect, Message: msgUnknownEntry}
	}
}

// ValidateLayout validates a flat snapshot of observed paths under
// `.docsync/` against the canonical D1 tree. Encoded rules (identical to the
// TS validator):
//   - Top level allows exactly the CONFIG files and the TOP_LEVEL_DIRS
//     directories (subset allowed) — anything else is `unknown_entry`.
//   - `manifest.yml` must exist as a file, else `missing_required_file` (D7/D8).
//   - `memory/` holds exactly the canonical memory files (presence optional).
//   - `policy-history/` must sit at the top level, never under `memory/` (V-P0-3).
//   - `state/` holds only the canonical state subdirs/files; contents under
//     canonical state subdirs are free-form, as are `evaluation/` and
//     `policy-history/`.
//   - Canonical CONFIG file names anywhere else → `misplaced_file`.
//   - Canonical dir as file → `dir_expected`; canonical file as dir → `file_expected`.
//
// Violations are deterministic and sorted by path for stable diagnostics.
func ValidateLayout(entries []LayoutEntry) LayoutResult {
	byPath := toKindMap(entries)

	ordered := make([]LayoutEntry, 0, len(byPath))
	for _, path := range sortedKeys(byPath) {
		ordered = append(ordered, LayoutEntry{Path: path, Kind: byPath[path]})
	}

	var violations []LayoutViolation
	for _, entry := range ordered {
		if v := classifyEntry(entry); v != nil {
			violations = append(violations, *v)
		}
	}
	if byPath["manifest.yml"] != LayoutEntryFile {
		violations = append(violations, newViolation(
			ViolationMissingRequiredFile,
			"manifest.yml",
			docsyncPath("manifest.yml"),
			msgMissingManifest,
		))
	}

	if len(violations) > 0 {
		sortViolations(violations)
		return LayoutResult{Violations: violations}
	}
	return LayoutResult{OK: true, Resolved: buildResolved(byPath)}
}

// toKindMap dedupes observed entries into a path → kind map. When a path is
// observed as both kinds (possible only in hand-built snapshots, never from
// the fs adapter), "dir" wins — it is the strictly more informative
// observation.
func toKindMap(entries []LayoutEntry) map[string]LayoutEntryKind {
	byPath := make(map[string]LayoutEntryKind, len(entries))
	for _, entry := range entries {
		current, seen := byPath[entry.Path]
		if !seen {
			byPath[entry.Path] = entry.Kind
		} else if current == LayoutEntryFile && entry.Kind == LayoutEntryDir {
			byPath[entry.Path] = LayoutEntryDir
		}
	}
	return byPath
}

func sortedKeys(byPath map[string]LayoutEntryKind) []string {
	keys := make([]string, 0, len(byPath))
	for key := range byPath {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func buildResolved(byPath map[string]LayoutEntryKind) CanonicalLayout {
	resolved := CanonicalLayout{ManifestPath: docsyncPath("manifest.yml")}
	if byPath["policy.yml"] == LayoutEntryFile {
		resolved.PolicyPath = docsyncPath("policy.yml")
	}
	if byPath["concepts.yml"] == LayoutEntryFile {
		resolved.ConceptsPath = docsyncPath("concepts.yml")
	}
	if byPath["instructions.md"] == LayoutEntryFile {
		resolved.InstructionsPath = docsyncPath("instructions.md")
	}
	if byPath["models.yml"] == LayoutEntryFile {
		resolved.ModelsPath = docsyncPath("models.yml")
	}
	if byPath["evaluation"] == LayoutEntryDir {
		resolved.EvaluationDir = docsyncPath("evaluation")
	}
	if byPath["policy-history"] == LayoutEntryDir {
		resolved.PolicyHistoryDir = docsyncPath("policy-history")
	}
	if byPath["memory"] == LayoutEntryDir {
		resolved.MemoryDir = docsyncPath("memory")
		var present []string
		for _, file := range schemas.MemoryFiles {
			if byPath["memory/"+file] == LayoutEntryFile {
				present = append(present, docsyncPath("memory/"+file))
			}
		}
		resolved.MemoryFiles = present
	}
	if byPath["state"] == LayoutEntryDir {
		resolved.StateDir = docsyncPath("state")
		var subdirs []string
		for _, dir := range schemas.StateSubdirs {
			if byPath["state/"+dir] == LayoutEntryDir {
				subdirs = append(subdirs, docsyncPath("state/"+dir))
			}
		}
		resolved.StateSubdirs = subdirs
		var files []string
		for _, file := range schemas.StateFiles {
			if byPath["state/"+file] == LayoutEntryFile {
				files = append(files, docsyncPath("state/"+file))
			}
		}
		resolved.StateFiles = files
	}
	return resolved
}

// compareViolations is the deterministic total order for violations
// (path, kind, message, expected) — the TS compareViolations.
func compareViolations(a, b LayoutViolation) int {
	if a.Path != b.Path {
		return strings.Compare(a.Path, b.Path)
	}
	if a.Kind != b.Kind {
		return strings.Compare(string(a.Kind), string(b.Kind))
	}
	if a.Message != b.Message {
		return strings.Compare(a.Message, b.Message)
	}
	return strings.Compare(a.Expected, b.Expected)
}

func sortViolations(violations []LayoutViolation) {
	sort.SliceStable(violations, func(i, j int) bool {
		return compareViolations(violations[i], violations[j]) < 0
	})
}
