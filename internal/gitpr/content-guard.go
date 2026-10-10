package gitpr

import (
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from core/src/gitpr/content-guard.ts — the machine-PR content guard
// (P6-T27; PLAN §7.3 lines 1166-1171: "Machine-proposed PRs never carry a mode
// transition to `auto` and never modify `manifest.yml`/policy files — the only
// path to a mode change is a human PR"; P12-T13 extends the protected set with
// `.docsync/org-policy.yml`, the org's floors; ARCH §11.4; INV-7).
//
// A pure, fail-closed gate over the machine PR's INTENDED change set,
// evaluated BEFORE any git write or platform call. Two rules, both absolute
// for a machine-proposed PR:
//
//  1. manifest_or_policy_edit — any changed path that is a manifest/policy
//     file. The default protected set is the exact names `manifest.yml` and
//     `.docsync/manifest.yml`, every `.docsync/policy*` path, and the org
//     policy file `.docsync/org-policy.yml` (P12-T13: the org's floors are
//     human-owned configuration — a model-proposed patch to it would rewrite
//     the org's floors); callers may inject a replacement list
//     (GuardMachinePrContentInput.ProtectedPaths) for repos that keep the
//     manifest at a custom location. The trailing-`*` entry form is a PATH
//     PREFIX rule (`.docsync/policy*` matches `.docsync/policy.yml`,
//     `.docsync/policies/rates.yml`, ...); every other entry is an exact name.
//  2. machine_mode_transition — any document whose asserted POST-PATCH mode is
//     `auto`. The ONLY path to a mode transition is a HUMAN PR — even from
//     `suggest` — so a machine PR claiming a post-patch mode of `auto` for any
//     document (manifest-listed or not) is refused. Contract: ModeAfter is
//     supplied ONLY when the caller asserts that the patch CHANGES the
//     document's mode (mode information lives in the manifest, which a
//     DocPatch cannot express — a content-only patch has no ModeAfter and never
//     trips this rule; an `auto`-mode document that STAYS `auto` is not a
//     transition). This guard is REDUNDANT-IN-DEPTH with the pipeline's D2
//     mode gate — the pipeline already refuses machine-side mode transitions;
//     this layer re-checks the claim at the PR boundary because INV-7 is a
//     binding invariant, not a pipeline courtesy.
//
// WHOLE-PATCH POSTURE: like the layer-4 PR filter, any violation refuses the
// WHOLE machine PR (OK false) — no partial application survives. Violations
// are deterministic (changed-path order, then doc-patch order; deduped by
// path+rule at first occurrence) and carry self-contained evidence for the
// safety-incident sink. No I/O, no clocks, no models (P19).

/* -------------------------------------------------------------------- rules */

// MachinePrContentRule is the closed violation-rule union of this guard.
type MachinePrContentRule string

const (
	// MachinePrRuleManifestPolicy: the machine PR touches a manifest or policy
	// file (INV-7).
	MachinePrRuleManifestPolicy MachinePrContentRule = "manifest_or_policy_edit"
	// MachinePrRuleModeTransition: the machine PR would transition a
	// document's mode to `auto` (INV-7).
	MachinePrRuleModeTransition MachinePrContentRule = "machine_mode_transition"
)

// DefaultProtectedPolicyPaths is the default protected manifest/policy path
// set (exact names + the `policy*` prefix + the org policy file). Treated as
// immutable by every caller (the TS Object.freeze); the Go slice is never
// mutated in this package.
var DefaultProtectedPolicyPaths = []string{
	"manifest.yml",
	".docsync/manifest.yml",
	".docsync/policy*",
	".docsync/org-policy.yml",
}

// MachinePrContentViolation is one refused aspect of the machine PR: which
// path, which rule, why.
type MachinePrContentViolation struct {
	Rule   MachinePrContentRule
	Path   string
	Detail string
}

// GuardMachinePrContentResult is the guard verdict: OK is false iff any
// violation, and Violations is then non-empty (empty when OK — the TS
// `violations: readonly []` shape).
type GuardMachinePrContentResult struct {
	OK         bool
	Violations []MachinePrContentViolation
}

// MachinePrDocPatch is one document patch with its ASSERTED post-patch mode
// when (and only when) the caller claims the patch changes the mode —
// `ModeAfter: "auto"` from a machine PR is always a violation (INV-7; the only
// path to `auto` is a human PR, even from `suggest`).
type MachinePrDocPatch struct {
	DocPath   string
	ModeAfter *string
}

// GuardMachinePrContentInput is the guard input.
type GuardMachinePrContentInput struct {
	// ChangedPaths is every repo-relative path the machine PR would change
	// (docs, state, anything).
	ChangedPaths []string
	// ManifestPaths is the manifest-tracked document paths (context for the
	// evidence detail; the allowlist ENFORCEMENT itself is the layer-4
	// filter's job, not this guard's).
	ManifestPaths []string
	// DocPatches are the document patches with their asserted post-patch mode.
	DocPatches []MachinePrDocPatch
	// ProtectedPaths is a replacement for DefaultProtectedPolicyPaths (repos
	// with a custom manifest location). Same matching semantics: a trailing
	// `*` is a path-prefix rule, anything else is an exact name.
	ProtectedPaths []string
}

// CodeMachinePrContentGuard preserves the TS
// EDOCSYNC_MACHINE_PR_CONTENT_GUARD code verbatim (carried through docutil.New
// per the globs/statemachine precedent — no new docutil code invented).
const CodeMachinePrContentGuard = "EDOCSYNC_MACHINE_PR_CONTENT_GUARD"

// MachinePrContentGuardError is a fail-closed refusal of a malformed guard
// input (a bug, never a guess).
type MachinePrContentGuardError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_MACHINE_PR_CONTENT_GUARD).
func (e *MachinePrContentGuardError) Code() string { return CodeMachinePrContentGuard }

// Error renders the coded error in docutil house style.
func (e *MachinePrContentGuardError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *MachinePrContentGuardError) Unwrap() error { return e.coded }

// newMachinePrContentGuardError builds the typed guard-input refusal.
func newMachinePrContentGuardError(message string) *MachinePrContentGuardError {
	return &MachinePrContentGuardError{coded: docutil.New(CodeMachinePrContentGuard, message)}
}

// matchesProtectedEntry reports whether entry (an exact name or a trailing-`*`
// prefix rule) matches path.
func matchesProtectedEntry(path, entry string) bool {
	if strings.HasSuffix(entry, "*") {
		return strings.HasPrefix(path, entry[:len(entry)-1])
	}
	return path == entry
}

// assertGuardInput ports the TS assertGuardInput for every check that Go's
// typed API can still express (empty path/patch strings). The TS guards for
// non-array/non-object inputs are unreachable on typed fields — Go's type
// system rejects them at compile time.
func assertGuardInput(input GuardMachinePrContentInput) error {
	assertEntries := func(field string, values []string) error {
		for _, value := range values {
			if len(value) == 0 {
				return newMachinePrContentGuardError("input." + field + " entries must be nonempty strings")
			}
		}
		return nil
	}
	if err := assertEntries("changedPaths", input.ChangedPaths); err != nil {
		return err
	}
	if err := assertEntries("manifestPaths", input.ManifestPaths); err != nil {
		return err
	}
	for index, patch := range input.DocPatches {
		if len(patch.DocPath) == 0 {
			return newMachinePrContentGuardError(
				"input.docPatches[" + strconv.Itoa(index) + "].docPath must be a nonempty string")
		}
		if patch.ModeAfter != nil && len(*patch.ModeAfter) == 0 {
			return newMachinePrContentGuardError(
				"input.docPatches[" + strconv.Itoa(index) + "].modeAfter must be a nonempty string when provided")
		}
	}
	if input.ProtectedPaths != nil {
		if err := assertEntries("protectedPaths", input.ProtectedPaths); err != nil {
			return newMachinePrContentGuardError(
				"input.protectedPaths must be an array of nonempty strings when provided")
		}
	}
	return nil
}

// GuardMachinePrContent evaluates the INV-7 machine-PR content guard
// (P6-T27). Pure and deterministic: changed-path order first (rule 1), then
// doc-patch order (rule 2), deduped by path+rule at first occurrence. A false
// OK refuses the WHOLE machine PR — the caller aborts before any git/platform
// side effect and feeds every violation to the safety-incident sink.
func GuardMachinePrContent(input GuardMachinePrContentInput) (GuardMachinePrContentResult, error) {
	if err := assertGuardInput(input); err != nil {
		return GuardMachinePrContentResult{}, err
	}
	protectedPaths := input.ProtectedPaths
	if protectedPaths == nil {
		protectedPaths = DefaultProtectedPolicyPaths
	}
	manifestPaths := make(map[string]bool, len(input.ManifestPaths))
	for _, path := range input.ManifestPaths {
		manifestPaths[path] = true
	}

	var violations []MachinePrContentViolation
	seen := make(map[string]bool)

	for _, path := range input.ChangedPaths {
		entry := ""
		for _, candidate := range protectedPaths {
			if matchesProtectedEntry(path, candidate) {
				entry = candidate
				break
			}
		}
		if entry == "" {
			continue
		}
		key := string(MachinePrRuleManifestPolicy) + "\x00" + path
		if seen[key] {
			continue
		}
		seen[key] = true
		alsoManifest := ""
		if manifestPaths[path] {
			alsoManifest = " (the path is also a manifest-tracked document path)"
		}
		violations = append(violations, MachinePrContentViolation{
			Rule: MachinePrRuleManifestPolicy,
			Path: path,
			Detail: "path '" + path + "' is a manifest/policy file (matched protected entry '" + entry +
				"') — INV-7: machine-proposed PRs never modify manifest.yml/policy files (the org " +
				"policy file included); the manifest, policy, and org floors are human-owned " +
				"configuration" + alsoManifest,
		})
	}

	for _, patch := range input.DocPatches {
		if patch.ModeAfter == nil || *patch.ModeAfter != "auto" {
			continue
		}
		key := string(MachinePrRuleModeTransition) + "\x00" + patch.DocPath
		if seen[key] {
			continue
		}
		seen[key] = true
		listed := "not manifest-listed"
		if manifestPaths[patch.DocPath] {
			listed = "manifest-listed"
		}
		violations = append(violations, MachinePrContentViolation{
			Rule: MachinePrRuleModeTransition,
			Path: patch.DocPath,
			Detail: "document '" + patch.DocPath + "' (" + listed + ") asserts a post-patch mode of " +
				"'auto' from a machine PR — INV-7: the ONLY path to a mode transition is a human PR " +
				"(even from 'suggest'); redundant-in-depth with the pipeline's D2 mode gate",
		})
	}

	if len(violations) == 0 {
		return GuardMachinePrContentResult{OK: true, Violations: []MachinePrContentViolation{}}, nil
	}
	return GuardMachinePrContentResult{OK: false, Violations: violations}, nil
}
