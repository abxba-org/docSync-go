package gitpr

// Ported from packages/core/src/gitpr/pr-filter.ts (P6-T11; PLAN §7.3 lines
// 1166-1171; ARCH §11.4 §14; INV-2, INV-3, INV-4; OD-R12).
//
// PR-side safety filter — lock enforcement LAYER 4. Before a doc PR is created,
// the orchestrator re-diffs the branch against the writable-path allowlist
// COMPUTED FROM THE MANIFEST ITSELF, never from model/pipeline output (INV-2/3 —
// PLAN §7.3: "computed from the manifest itself, not from model output"). The
// derivation reads ONLY the manifest structure: a changed path is allowed iff it
// is
//
//	(a) a manifest-listed document whose EFFECTIVE mode (`entry.mode ??
//	    manifest.defaults.mode ?? 'suggest'` — the canonical resolution used by
//	    workspace/assemble.ts and validate/manifest.ts) is in WRITABLE_MODES
//	    (auto | suggest | human — workspace/types.go), or
//	(b) under `.docsync/state/` — the OD-R12 committed-state carriage, which
//	    rides EVERY doc PR (PLAN §7.3 line 1144) and is therefore ALWAYS allowed.
//
// ANYTHING else is a violation: source files, README, `.github/workflows/**`,
// `manifest.yml`/policy files (INV-7: machine PRs never modify them), and — the
// INV-4 class — manifest-listed docs whose effective mode is PROTECTED (locked |
// generated; PROTECTED_MODES). Rule routing: a manifest-listed path that is not
// writable → `protected_path_patch` (INV-4); any other path →
// `out_of_allowlist_path` (INV-2/3).
//
// WHOLE-PATCH REJECTION (INV-4): one protected-path violation rejects the WHOLE
// result (`ok: false`) — no partial filter-through ships the clean hunks while
// dropping the dirty ones. The caller aborts PR creation and feeds
// {@link PrFilterResult} to the safety-incident sink (P6-T12 sibling): every
// violation is logged, never silently swallowed (PLAN §7.3 line 1170).
//
// The diff is enumerated through the injected git client
// (`DiffFiles(ctx, base, branch)`, rename-aware; a rename is classified by its
// NEW path). Determinism: violations follow git's canonical diff record order —
// a function of the two trees, nothing else (ARCH §13).

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

// CodePrFilterConfig preserves the TS EDOCSYNC_PR_FILTER_CONFIG code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const CodePrFilterConfig = "EDOCSYNC_PR_FILTER_CONFIG"

// prFilterConfigError builds the PrFilterConfigError equivalent: the fail-closed
// configuration refusal (D8/D14) — malformed deps/input/manifest or an injected
// git transport bound to a DIFFERENT repo root than the one the call declares.
// The filter never diffs the wrong repo silently.
func prFilterConfigError(message string) error {
	return docutil.New(CodePrFilterConfig, message)
}

// StateCarriagePrefix is the committed-state carriage namespace (OD-R12; PLAN
// §7.3 line 1144): paths under this prefix ride every doc PR and are ALWAYS
// allowed by the layer-4 filter — the audit trail travels with the documentation
// hunks. Mirrors the state-channel prefix enforced by commit-assembly.go.
const StateCarriagePrefix = ".docsync/state/"

// PrFilterRule is the violation class of one out-of-allowlist / protected-path
// diff entry.
type PrFilterRule string

// The two violation classes.
const (
	PrFilterRuleOutOfAllowlist PrFilterRule = "out_of_allowlist_path"
	PrFilterRuleProtectedPath  PrFilterRule = "protected_path_patch"
)

// PrFilterViolation is one violation: what changed where, under which rule, with
// evidence.
type PrFilterViolation struct {
	// Path is the repo-relative changed path (the NEW path for renames).
	Path string
	Rule PrFilterRule
	// Detail is the evidence for the safety-incident sink (self-contained,
	// path-stable).
	Detail string
}

// PrFilterResult is the filter result: `ok: false` iff any violation
// (whole-patch rejection — INV-4).
type PrFilterResult struct {
	OK         bool
	Violations []PrFilterViolation
}

// DerivedPathAllowlist is the manifest-derived path allowlist split (D7 +
// §11.4 layer 4).
type DerivedPathAllowlist struct {
	// WritablePaths are the manifest doc paths whose effective mode is in
	// WRITABLE_MODES.
	WritablePaths map[string]bool
	// ProtectedPaths are the manifest doc paths that are NOT writable —
	// locked/generated per PROTECTED_MODES (fail closed: any mode outside
	// WRITABLE_MODES lands here, so a future mode value can never become
	// silently writable).
	ProtectedPaths map[string]bool
}

// effectiveMode is the effective mode of one manifest entry (`entry.mode ??
// defaults.mode ?? 'suggest'`).
func effectiveMode(entry schemas.ManifestEntry, manifest *schemas.Manifest) schemas.DocMode {
	if entry.Mode != nil {
		return *entry.Mode
	}
	if manifest.Defaults != nil && manifest.Defaults.Mode != nil {
		return *manifest.Defaults.Mode
	}
	return schemas.DocModeSuggest
}

// DerivePrPathAllowlist derives the layer-4 path allowlist FROM THE MANIFEST
// STRUCTURE ONLY (INV-2/3): writable = manifest documents ∩ WRITABLE_MODES;
// protected = every other manifest-listed path (locked | generated per
// PROTECTED_MODES; the `external` pseudo-mode cannot appear IN a manifest — it
// IS absence from it). Pure; same manifest → identical result.
func DerivePrPathAllowlist(manifest *schemas.Manifest) DerivedPathAllowlist {
	writablePaths := map[string]bool{}
	protectedPaths := map[string]bool{}
	for _, entry := range manifest.Documents {
		mode := effectiveMode(entry, manifest)
		switch {
		case workspace.IsWritableMode(string(mode)):
			writablePaths[entry.Path] = true
		case prFilterIsProtectedMode(string(mode)):
			protectedPaths[entry.Path] = true
		default:
			// Unreachable for the closed DocMode union; kept fail closed so
			// mode drift can never widen the allowlist (D8 — never guess, never
			// widen).
			protectedPaths[entry.Path] = true
		}
	}
	return DerivedPathAllowlist{WritablePaths: writablePaths, ProtectedPaths: protectedPaths}
}

// prFilterIsProtectedMode reports membership in PROTECTED_MODES (TS
// `PROTECTED_MODES.includes(mode)`; workspace exports the list, not a predicate).
func prFilterIsProtectedMode(mode string) bool {
	for _, protected := range workspace.ProtectedModes {
		if protected == mode {
			return true
		}
	}
	return false
}

// PrFilterDeps is the filter's dependency bundle: the injected git transport
// (W2 seam).
type PrFilterDeps struct {
	Git *gitx.GitClient
}

// FilterBranchDiffInput is one call's input.
type FilterBranchDiffInput struct {
	// RepoRoot is the absolute repo root — must match the injected git client's
	// binding.
	RepoRoot string
	// Base is the base ref of the re-diff (e.g. the PR's merge target).
	Base string
	// Branch is the head ref of the doc branch about to become the PR.
	Branch string
	// Manifest is the parsed manifest (D7 allowlist authority) — never model
	// output.
	Manifest *schemas.Manifest
}

// PrFilter is the layer-4 filter port (P6-T11).
type PrFilter interface {
	// FilterBranchDiff re-diffs `base..branch` and classifies every changed
	// path against the MANIFEST-derived allowlist (INV-2/3). Any violation ⇒
	// `ok: false` — the caller aborts PR creation and logs the incidents
	// (P6-T12 sink).
	FilterBranchDiff(ctx context.Context, input FilterBranchDiffInput) (PrFilterResult, error)
}

// prFilter is the constructed filter: it performs no I/O beyond
// `git.DiffFiles` and holds no credentials (INV-8).
type prFilter struct {
	git *gitx.GitClient
}

// CreatePrFilter builds the layer-4 filter (P6-T11). The git transport is
// INJECTED (W2 seam).
func CreatePrFilter(deps PrFilterDeps) (PrFilter, error) {
	if deps.Git == nil {
		return nil, prFilterConfigError(
			"createPrFilter deps.git must be a GitClient (a diffFiles function is required)")
	}
	return &prFilter{git: deps.Git}, nil
}

// assertFilterInput is the fail-closed structural check on one call's input
// (D8). (The TS also checked the manifest's runtime shape and a non-object
// input; Go's struct types make both unrepresentable, so the checkable fields
// are asserted here.)
func assertFilterInput(input FilterBranchDiffInput) error {
	if input.RepoRoot == "" || !filepath.IsAbs(input.RepoRoot) {
		return prFilterConfigError("filterBranchDiff input.repoRoot must be a nonempty absolute path")
	}
	if input.Base == "" {
		return prFilterConfigError("filterBranchDiff input.base must be a nonempty string")
	}
	if input.Branch == "" {
		return prFilterConfigError("filterBranchDiff input.branch must be a nonempty string")
	}
	if input.Manifest == nil {
		return prFilterConfigError("filterBranchDiff input.manifest must be a parsed Manifest (a documents array is required)")
	}
	return nil
}

// FilterBranchDiff — see PrFilter.
func (f *prFilter) FilterBranchDiff(ctx context.Context, input FilterBranchDiffInput) (PrFilterResult, error) {
	if err := assertFilterInput(input); err != nil {
		return PrFilterResult{}, err
	}
	boundRoot := filepath.Clean(f.git.Root())
	declaredRoot := filepath.Clean(input.RepoRoot)
	if boundRoot != declaredRoot {
		return PrFilterResult{}, prFilterConfigError(
			"filterBranchDiff refused: deps.git is bound to '" + boundRoot + "' but the call declares " +
				"repoRoot '" + declaredRoot + "' — the filter never diffs the wrong repo (fail closed)")
	}

	allowlist := DerivePrPathAllowlist(input.Manifest)
	manifestPaths := map[string]bool{}
	for path := range allowlist.WritablePaths {
		manifestPaths[path] = true
	}
	for path := range allowlist.ProtectedPaths {
		manifestPaths[path] = true
	}

	violations := []PrFilterViolation{}
	// Rename-aware diff (D12 machinery): records arrive in git's canonical
	// order; a rename record's `path` is the NEW path — classification follows
	// where the content LANDED, never where it came from.
	changed, err := f.git.DiffFiles(ctx, input.Base, input.Branch)
	if err != nil {
		return PrFilterResult{}, err
	}
	for _, record := range changed {
		path := record.Path
		if strings.HasPrefix(path, StateCarriagePrefix) {
			continue // OD-R12 carriage — ALWAYS allowed
		}
		if allowlist.WritablePaths[path] {
			continue // manifest-listed writable doc
		}

		if manifestPaths[path] {
			// Manifest-listed but NOT writable ⇒ protected mode (locked |
			// generated) — INV-4: ONE such violation rejects the WHOLE patch.
			violations = append(violations, PrFilterViolation{
				Path: path,
				Rule: PrFilterRuleProtectedPath,
				Detail: "path '" + path + "' is a manifest-listed document in a protected mode (" +
					strings.Join(workspace.ProtectedModes, "|") +
					") — INV-4 whole-patch rejection: a patch touching a protected path is " +
					"rejected whole, no partial application survives to the PR",
			})
			continue
		}
		violations = append(violations, PrFilterViolation{
			Path: path,
			Rule: PrFilterRuleOutOfAllowlist,
			Detail: "path '" + path + "' is neither a manifest-listed writable document (modes " +
				strings.Join(workspace.WritableModes, "|") + ") nor under '" + StateCarriagePrefix +
				"' (INV-2/3: the allowlist is computed from the manifest itself, never from model output)",
		})
	}

	return PrFilterResult{OK: len(violations) == 0, Violations: violations}, nil
}
