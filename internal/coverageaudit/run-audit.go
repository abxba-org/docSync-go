// Ported from core/src/coverage-audit/run-audit.ts (P11-T22; ARCH §16.1 A5;
// D14): the coverage-corpus directory loader.
//
// The corpus convention: `<corpusDir>/<caseId>/` with a REQUIRED `truth.json`
// declaring the case's deterministic ground truth, and EITHER a before/after
// source pair (`before.<ext>` + `after.<ext>`, same extension — measured live
// by a language analyzer, e.g. the analyzer-typescript study runner) OR a
// `proxies.json` with precomputed fired proxies (non-AST languages, e.g.
// config cases measured through core's config-diff classifier).
//
// FAIL CLOSED (D14): a missing/unparsable/invalid `truth.json`, an unknown
// truth label, or unknown proxy kinds returns a *CoverageCorpusError — never a
// silently skipped case (a skipped behavioral case would fake recall). Pure fs
// reads only; case order is code-unit sorted (deterministic).
package coverageaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// EDOCSYNC_COVERAGE_CORPUS is the distinguishable error code for
// corpus-loading failures (fail closed, D14) — the TS CoverageCorpusError code.
const EDOCSYNC_COVERAGE_CORPUS = "EDOCSYNC_COVERAGE_CORPUS"

// CoverageCorpusError is the typed error every corpus-loading failure carries:
// its message text is exactly the TS `coverage corpus: <detail>` message and
// its code is EDOCSYNC_COVERAGE_CORPUS (the TS error's observable identity).
type CoverageCorpusError struct {
	detail string
}

// Error renders the preserved TS message (`coverage corpus: <detail>`).
func (e *CoverageCorpusError) Error() string { return "coverage corpus: " + e.detail }

// Code returns EDOCSYNC_COVERAGE_CORPUS.
func (e *CoverageCorpusError) Code() string { return EDOCSYNC_COVERAGE_CORPUS }

// corpusErrorf builds a *CoverageCorpusError.
func corpusErrorf(format string, args ...any) *CoverageCorpusError {
	return &CoverageCorpusError{detail: fmt.Sprintf(format, args...)}
}

// CoverageCorpusCase is one loaded corpus case (measurement still to be
// performed or already carried).
type CoverageCorpusCase struct {
	// CaseID is the case id — the directory name (code-unit sorted overall).
	CaseID string
	// Language is the corpus-declared language key.
	Language string
	// Truth is the declared deterministic ground truth.
	Truth schemas.BehavioralProxyTruth
	// Note is an optional declared note (carried onto declared-gap records).
	Note *string
	// BeforeSource is the before-side source text when the corpus carries a
	// `before.<ext>` file.
	BeforeSource *string
	// AfterSource is the after-side source text when the corpus carries an
	// `after.<ext>` file.
	AfterSource *string
	// FiredProxies are the precomputed fired proxies when the corpus carries
	// a `proxies.json`.
	FiredProxies []string
}

// readTruthFile reads and parses one case's truth.json into its raw object,
// failing closed on an unreadable file, invalid JSON, or a non-object document.
func readTruthFile(dir, caseID string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "truth.json"))
	if err != nil {
		return nil, corpusErrorf("case '%s' has no readable truth.json (fail closed, D14)", caseID)
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, corpusErrorf("case '%s' truth.json is not valid JSON (fail closed)", caseID)
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return nil, corpusErrorf("case '%s' truth.json is not an object (fail closed)", caseID)
	}
	return record, nil
}

// stringField extracts a truth.json string field with TS `typeof` fidelity:
// present reports key presence, isString reports the value's type.
func stringField(record map[string]any, key string) (value string, present, isString bool) {
	v, ok := record[key]
	if !ok {
		return "", false, false
	}
	s, isStr := v.(string)
	return s, true, isStr
}

// LoadCoverageCorpus loads every case under corpusDir. Deterministic
// (code-unit sorted case ids); pure fs reads; returns a *CoverageCorpusError
// on any malformed case — fail closed, never a silently skipped case (D14).
func LoadCoverageCorpus(corpusDir string) ([]CoverageCorpusCase, error) {
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, corpusErrorf("corpus directory '%s' is not readable (fail closed)", corpusDir)
	}
	caseIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		caseIDs = append(caseIDs, entry.Name())
	}
	sort.SliceStable(caseIDs, func(i, j int) bool { return compareStrings(caseIDs[i], caseIDs[j]) < 0 })

	cases := []CoverageCorpusCase{}
	for _, caseID := range caseIDs {
		dir := filepath.Join(corpusDir, caseID)
		truth, err := readTruthFile(dir, caseID)
		if err != nil {
			return nil, err
		}
		declaredID, _, declaredIsString := stringField(truth, "caseId")
		if !declaredIsString || declaredID == "" {
			return nil, corpusErrorf("case '%s' truth.json lacks a nonempty caseId", caseID)
		}
		if declaredID != caseID {
			return nil, corpusErrorf("case '%s' truth.json declares caseId '%s' — directory/truth mismatch", caseID, declaredID)
		}
		language, _, languageIsString := stringField(truth, "language")
		if !languageIsString || language == "" {
			return nil, corpusErrorf("case '%s' truth.json lacks a nonempty language", caseID)
		}
		truthLabel, _, truthIsString := stringField(truth, "truth")
		if !truthIsString ||
			(truthLabel != string(schemas.ProxyTruthBehavioral) &&
				truthLabel != string(schemas.ProxyTruthNonBehavioral) &&
				truthLabel != string(schemas.ProxyTruthUncertain)) {
			return nil, corpusErrorf("case '%s' truth.json truth '%s' is not a known truth label", caseID, truthLabel)
		}
		var note *string
		if raw, present := truth["note"]; present {
			noteText, isString := raw.(string)
			if !isString {
				return nil, corpusErrorf("case '%s' truth.json note is not a string", caseID)
			}
			note = &noteText
		}

		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			return nil, corpusErrorf("case '%s' directory is not readable (fail closed, D14)", caseID)
		}
		files := make([]string, 0, len(dirEntries))
		for _, entry := range dirEntries {
			files = append(files, entry.Name())
		}
		sort.SliceStable(files, func(i, j int) bool { return compareStrings(files[i], files[j]) < 0 })
		before := ""
		after := ""
		for _, name := range files {
			if before == "" && strings.HasPrefix(name, "before.") {
				before = name
			}
			if after == "" && strings.HasPrefix(name, "after.") {
				after = name
			}
		}
		var beforeSource, afterSource *string
		if before != "" {
			if after == "" {
				return nil, corpusErrorf("case '%s' carries '%s' without an after.<ext> pair", caseID, before)
			}
			beforeText, err := os.ReadFile(filepath.Join(dir, before))
			if err != nil {
				return nil, corpusErrorf("case '%s' carries an unreadable '%s' (fail closed)", caseID, before)
			}
			afterText, err := os.ReadFile(filepath.Join(dir, after))
			if err != nil {
				return nil, corpusErrorf("case '%s' carries an unreadable '%s' (fail closed)", caseID, after)
			}
			beforeStr := string(beforeText)
			afterStr := string(afterText)
			beforeSource = &beforeStr
			afterSource = &afterStr
		} else if after != "" {
			return nil, corpusErrorf("case '%s' carries '%s' without a before.<ext> pair", caseID, after)
		}

		var firedProxies []string
		if containsString(files, "proxies.json") {
			raw, err := os.ReadFile(filepath.Join(dir, "proxies.json"))
			if err != nil {
				return nil, corpusErrorf("case '%s' proxies.json is not valid JSON (fail closed)", caseID)
			}
			var parsed any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, corpusErrorf("case '%s' proxies.json is not valid JSON (fail closed)", caseID)
			}
			record, isObject := parsed.(map[string]any)
			var list []any
			isArray := false
			if isObject {
				list, isArray = record["firedProxies"].([]any)
			}
			if !isArray {
				return nil, corpusErrorf("case '%s' proxies.json lacks a firedProxies string array (fail closed)", caseID)
			}
			for _, item := range list {
				if _, isString := item.(string); !isString {
					return nil, corpusErrorf("case '%s' proxies.json lacks a firedProxies string array (fail closed)", caseID)
				}
			}
			kinds := make([]string, 0, len(list))
			for _, item := range list {
				kind := item.(string)
				if !schemas.IsValidProxyKind(kind) {
					return nil, corpusErrorf("case '%s' proxies.json proxy kind '%s' is not a known proxy kind", caseID, kind)
				}
				kinds = append(kinds, kind)
			}
			firedProxies = kinds
		}

		cases = append(cases, CoverageCorpusCase{
			CaseID:       caseID,
			Language:     language,
			Truth:        schemas.BehavioralProxyTruth(truthLabel),
			Note:         note,
			BeforeSource: beforeSource,
			AfterSource:  afterSource,
			FiredProxies: firedProxies,
		})
	}
	return cases, nil
}

// containsString mirrors Array#includes.
func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
