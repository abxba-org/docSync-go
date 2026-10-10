package boundaries

import (
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DocsyncPathClass is the classification of a `.docsync/`-relative path:
// human-owned config, system-written state, or unknown (TS `DocsyncPathClass`).
type DocsyncPathClass string

// Path classes (TS 'config' | 'state' | 'unknown').
const (
	PathClassConfig  DocsyncPathClass = "config"
	PathClassState   DocsyncPathClass = "state"
	PathClassUnknown DocsyncPathClass = "unknown"
)

// WriteDecision is the outcome of a write guard: allowed (TS `{ allowed: true }`)
// or refused with a reason (TS `WriteRefusal`).
type WriteDecision struct {
	// Allowed is true when the write may proceed.
	Allowed bool
	// Reason is the refusal reason; empty for allowed decisions.
	Reason string
}

const docSyncPrefix = schemas.DocSyncDir + "/"
const statePrefix = "state/"

// configFileSet mirrors CONFIG_FILE_SET: the human-owned config files at the
// `.docsync/` root (schemas.ConfigFiles — a superset carrying org-policy.yml).
var configFileSet = buildStringSet(schemas.ConfigFiles)

// configDirPrefixes mirrors CONFIG_DIR_PREFIXES: every TOP_LEVEL_DIR except
// `state`, as a trailing-slash prefix (evaluation/, memory/, policy-history/).
var configDirPrefixes = func() []string {
	prefixes := make([]string, 0, len(schemas.TopLevelDirs))
	for _, dir := range schemas.TopLevelDirs {
		if dir == "state" {
			continue
		}
		prefixes = append(prefixes, dir+"/")
	}
	return prefixes
}()

func buildStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

type normalizedPath struct {
	ok     bool
	path   string
	reason string
}

// normalizeDocsyncPath normalizes a `.docsync/`-relative write target: the
// leading `.docsync/` prefix is optional and stripped; `..`, absolute paths,
// backslashes and other malformed input are REFUSED with a reason — never
// thrown away, never resolved away.
func normalizeDocsyncPath(input string) normalizedPath {
	if strings.Contains(input, "\\") {
		return normalizedPath{reason: `backslashes are not allowed in docsync paths ("` + input + `") — use POSIX '/' separators`}
	}
	if strings.HasPrefix(input, "/") || hasDrivePrefix(input) {
		return normalizedPath{reason: `absolute paths are not allowed ("` + input + `") — use a path relative to .docsync/`}
	}
	if strings.IndexByte(input, 0) >= 0 {
		return normalizedPath{reason: `NUL bytes are not allowed in docsync paths ("` + input + `")`}
	}
	stripped := input
	if strings.HasPrefix(stripped, docSyncPrefix) {
		stripped = stripped[len(docSyncPrefix):]
	}
	if len(stripped) == 0 {
		return normalizedPath{reason: `path is empty ("` + input + `") — expected a file path inside .docsync/`}
	}
	segments := strings.Split(stripped, "/")
	for _, segment := range segments {
		if segment == ".." {
			return normalizedPath{reason: `path traversal ('..') is not allowed ("` + input + `")`}
		}
	}
	for _, segment := range segments {
		if len(segment) == 0 {
			return normalizedPath{reason: `empty path segments are not allowed ("` + input + `")`}
		}
	}
	for _, segment := range segments {
		if segment == "." {
			return normalizedPath{reason: `'.' segments are not allowed ("` + input + `") — use a direct path inside .docsync/`}
		}
	}
	return normalizedPath{ok: true, path: stripped}
}

// hasDrivePrefix mirrors the TS /^[A-Za-z]:($|\/)/ absolute-path test.
func hasDrivePrefix(input string) bool {
	if len(input) < 2 || input[1] != ':' {
		return false
	}
	c := input[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return false
	}
	return len(input) == 2 || input[2] == '/'
}

// classifyNormalizedPath classifies a normalized path; normalization failures
// classify as "unknown".
func classifyNormalizedPath(path string) DocsyncPathClass {
	if _, ok := configFileSet[path]; ok {
		return PathClassConfig
	}
	for _, prefix := range configDirPrefixes {
		if strings.HasPrefix(path, prefix) {
			return PathClassConfig
		}
	}
	if strings.HasPrefix(path, statePrefix) {
		return PathClassState
	}
	return PathClassUnknown
}

// ClassifyDocsyncPath classifies a `.docsync/`-relative path: `manifest.yml` →
// "config"; `policy.yml` → "config"; `evaluation/**`, `memory/**`,
// `policy-history/**` → "config"; `state/**` → "state"; everything else (repo
// files, traversal, absolute/backslash input, the bare `.docsync` dir itself)
// → "unknown".
func ClassifyDocsyncPath(relPath string) DocsyncPathClass {
	normalized := normalizeDocsyncPath(relPath)
	if !normalized.ok {
		return PathClassUnknown
	}
	return classifyNormalizedPath(normalized.path)
}

func unknownPathRefusal(relPath, path string) WriteDecision {
	return WriteDecision{
		Reason: `"` + path + `" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for "` + relPath + `" (fail closed)`,
	}
}

// GuardStateWrite is the guard for state writers (state store, orchestrator):
// ONLY `state/**` is allowed. Human-owned config is refused with a PR-review
// pointer (V-P0-13); unknown paths are refused both ways (invariant 3).
func GuardStateWrite(relPath string) WriteDecision {
	normalized := normalizeDocsyncPath(relPath)
	if !normalized.ok {
		return WriteDecision{Reason: normalized.reason}
	}
	switch classifyNormalizedPath(normalized.path) {
	case PathClassState:
		return WriteDecision{Allowed: true}
	case PathClassConfig:
		return WriteDecision{
			Reason: `state writers must not touch human-owned config; "` + normalized.path + `" (from "` + relPath + `") is config-owned — edit via a reviewable PR`,
		}
	default:
		return unknownPathRefusal(relPath, normalized.path)
	}
}

// GuardConfigWrite is the guard for config writers (CLI init, promotion
// pipeline): ONLY config paths are allowed. `state/` is system-written and
// refused (V-P0-13); `policy-history/**` classifies as config — the promotion
// pipeline is its human-PR-reviewed write path; unknown paths are refused both
// ways (invariant 3).
func GuardConfigWrite(relPath string) WriteDecision {
	normalized := normalizeDocsyncPath(relPath)
	if !normalized.ok {
		return WriteDecision{Reason: normalized.reason}
	}
	switch classifyNormalizedPath(normalized.path) {
	case PathClassConfig:
		return WriteDecision{Allowed: true}
	case PathClassState:
		return WriteDecision{
			Reason: `state/ is system-written; config writers must not touch "` + normalized.path + `" (from "` + relPath + `")`,
		}
	default:
		return unknownPathRefusal(relPath, normalized.path)
	}
}
