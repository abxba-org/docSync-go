package drift

// Ported from core/src/drift/differential.ts — historical drift analysis:
// baseline differentials (P11-T16; ARCH §6.1; CTX §11; PLAN §12.3 F11-9).
// Given a stored baseline (the record of what a document was verified
// against: sources/symbols/claims + the commit) and the current HEAD, this
// module computes the differential ARCH §6.1 binds as the evidence set that
// must be re-evaluated:
//
//     diff baseline_commit..head  ∩  baseline scope
//
// INTERSECT, never blanket (PRIN-2): only baseline-recorded source files,
// only baseline-recorded symbol names, only baseline-recorded claim ids are
// evaluated. Files/symbols/claims outside the recorded scope are invisible
// to this module's output by construction — the report cannot leak them.
//
// The differential is deterministic and ZERO-LLM: every fact is derived from
// git object identity (blob shas, tree diffs, patch stats) and exact text
// membership — never timestamps, never model output. Per-claim verdicts:
//
//   - `unchanged`      — the claim's recorded backing blob sha IS the blob at
//     head (git content identity: the recorded backing bytes survive verbatim).
//   - `source-changed` — the source file changed in the range but the recorded
//     blob sha is still the blob at head (the claim's own bytes are intact;
//     drift elsewhere in the file, support not refuted).
//   - `support-moved`  — the head blob differs from the recorded
//     `source.blob_sha`: the recorded backing bytes no longer exist at head →
//     proposed status `suspect` (ARCH §4.3 direct-invalidation posture; the
//     same target the W2 rule table maps symbol refutation to — the claim-store
//     vocabulary, dispatched via claims.LoseSupport).
//   - `source-missing` — the source file does not exist at head → `suspect`.
//
// The D12 ladder resolution (baselines.ResolveBaseline) is REUSED verbatim for
// the baseline-vs-head identity context (rung + evidence) — never duplicated.
// All output lists are sorted with code-unit comparison (deterministic,
// locale-independent); two runs on identical inputs yield byte-identical
// canonical JSON.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnalyzeDriftInput is the drift-analysis input: the baseline record, the head
// to diff against, and the claim-store rows named by baseline.claim_ids.
type AnalyzeDriftInput struct {
	// Baseline is the stored baseline (ARCH §6.1 layout) whose scope bounds
	// the analysis.
	Baseline *schemas.Baseline
	// Head is the head rev (ref or sha) to diff `baseline_commit..head`
	// against.
	Head string
	// Git is the typed git client (the only git surface used here).
	Git *gitx.GitClient
	// Claims are the claim-store rows to consider — ONLY ids recorded in
	// baseline.claim_ids are evaluated (scope intersection); extra claims are
	// ignored entirely.
	Claims []schemas.Claim
	// RunID is the owning run id (provenance in the report).
	RunID string
	// At is the ISO-8601 report stamp (caller-owned; no hidden clock).
	At string
}

// refutedSupportProposal is the proposed status for refuted support (ARCH §4.3
// → W2 rule-table identity: `suspect` → claims.LoseSupport).
const refutedSupportProposal = schemas.ClaimStatusSuspect

// compareStrings is the TS `a < b ? -1 : a > b ? 1 : 0` code-unit comparison.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// diffIndex is indexed diff access: records keyed by new path AND original
// path (renames).
type diffIndex struct {
	byPath map[string]*gitx.DiffFileRecord
	stats  map[string]*gitx.DiffStatRecord
}

// indexDiff keys the diff records and stats by path AND old_path (renames).
func indexDiff(records []gitx.DiffFileRecord, stats []gitx.DiffStatRecord) diffIndex {
	index := diffIndex{
		byPath: make(map[string]*gitx.DiffFileRecord, len(records)),
		stats:  make(map[string]*gitx.DiffStatRecord, len(stats)),
	}
	for i := range records {
		record := &records[i]
		index.byPath[record.Path] = record
		if record.OldPath != nil {
			index.byPath[*record.OldPath] = record
		}
	}
	for i := range stats {
		stat := &stats[i]
		index.stats[stat.Path] = stat
		if stat.OldPath != nil {
			index.stats[*stat.OldPath] = stat
		}
	}
	return index
}

// blobContent is a blob read: its sha plus its content.
type blobContent struct {
	sha     string
	content string
}

// blobContentOrNull returns the blob content of `<rev>:<path>`, or nil when
// the object does not exist (missing blob at rev / unknown path — a *declared
// absence*, not an error). Any other git failure propagates (typed
// *gitx.GitExecError, D14: never swallowed).
func blobContentOrNull(ctx context.Context, git *gitx.GitClient, rev, path string) (*blobContent, error) {
	cat, err := git.CatFile(ctx, rev, path)
	if err != nil {
		if gitErr, ok := gitx.AsGitExecError(err); ok && gitErr.Kind == gitx.KindMissingRev {
			return nil, nil
		}
		return nil, err
	}
	return &blobContent{sha: cat.BlobSha, content: cat.Content}, nil
}

// AnalyzeDrift computes the P11-T16 baseline differential: per-source,
// per-symbol and per-claim drift facts, strictly intersected with the
// baseline's recorded scope, with commit-range and blob-identity evidence.
// Deterministic and zero-LLM (see module doc); the returned report is
// validated against `docsync.drift-report/v1` before it is returned (fail
// closed on any producer bug).
func AnalyzeDrift(ctx context.Context, input AnalyzeDriftInput) (*schemas.DriftReport, error) {
	if input.Baseline == nil {
		return nil, baselineNilError()
	}
	if input.Git == nil {
		return nil, gitNilError()
	}
	baseline := input.Baseline
	git := input.Git

	headSha, err := git.ResolveRev(ctx, input.Head)
	if err != nil {
		return nil, err
	}
	baselineSha, err := git.ResolveRev(ctx, baseline.BaselineCommit)
	if err != nil {
		return nil, err
	}

	// D12 ladder resolution is reused verbatim (never duplicated): the rung and
	// its evidence string are report context for the differential.
	ladder, err := baselines.ResolveBaseline(ctx, baselines.ResolveBaselineInput{
		Baseline: baseline,
		Head:     headSha,
		Git:      git,
	})
	if err != nil {
		return nil, err
	}

	diff, err := git.DiffFiles(ctx, baselineSha, headSha)
	if err != nil {
		return nil, err
	}
	stat, err := git.DiffStat(ctx, baselineSha, headSha)
	if err != nil {
		return nil, err
	}
	index := indexDiff(diff, stat)

	/* ------------------------------------------------------------- sources */
	// ONLY recorded source files — the intersection is the loop bound itself.
	sources := make([]schemas.DriftSourceFact, 0, len(baseline.SourceFiles))
	for _, path := range sortedUnique(baseline.SourceFiles) {
		record := index.byPath[path]
		if record == nil {
			sources = append(sources, schemas.DriftSourceFact{Path: path, Status: schemas.DriftSourceStatusUnchanged})
			continue
		}
		fact := schemas.DriftSourceFact{Path: record.Path, Status: schemas.DriftSourceStatus(record.Status)}
		if record.OldPath != nil {
			oldPath := *record.OldPath
			fact.OldPath = &oldPath
		}
		statRecord := index.stats[record.Path]
		if statRecord != nil {
			// Binary files carry no counts (gitx leaves the pointers nil);
			// the field is then absent, exactly as the TS spread produced
			// neither additions nor deletions for them.
			fact.Additions = statRecord.Additions
			fact.Deletions = statRecord.Deletions
		}
		sources = append(sources, fact)
	}

	/* -------------------------------------------------------------- symbols */
	// ONLY recorded symbol names, checked ONLY against changed recorded files:
	// a symbol is associated with a file iff its name appears in the file's
	// BASELINE blob (content identity — the recorded scope is where the symbol
	// lived at baseline); the head blob then answers present/absent.
	recordedSymbols := sortedUnique(baseline.SourceSymbols)
	symbols := make([]schemas.DriftSymbolFact, 0, len(recordedSymbols))
	for _, sourceFact := range sources {
		if sourceFact.Status == schemas.DriftSourceStatusUnchanged {
			continue
		}
		record := index.byPath[sourceFact.Path]
		if record == nil {
			continue
		}
		baselinePath := record.Path
		if record.OldPath != nil {
			baselinePath = *record.OldPath
		}
		if record.Status == gitx.DiffStatusAdded {
			continue // a file added after the baseline recorded no symbols
		}
		baselineBlob, err := blobContentOrNull(ctx, git, baselineSha, baselinePath)
		if err != nil {
			return nil, err
		}
		if baselineBlob == nil {
			continue // unreadable baseline side yields no symbol facts (documented)
		}
		headBlob, err := blobContentOrNull(ctx, git, headSha, record.Path)
		if err != nil {
			return nil, err
		}
		for _, name := range recordedSymbols {
			if !containsText(baselineBlob.content, name) {
				continue // symbol did not live in this file
			}
			kind := schemas.DriftSymbolAbsent
			if headBlob != nil && containsText(headBlob.content, name) {
				kind = schemas.DriftSymbolPresent
			}
			symbols = append(symbols, schemas.DriftSymbolFact{Name: name, Path: record.Path, Kind: kind})
		}
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		byName := compareStrings(symbols[i].Name, symbols[j].Name)
		if byName != 0 {
			return byName < 0
		}
		return compareStrings(symbols[i].Path, symbols[j].Path) < 0
	})

	/* --------------------------------------------------------------- claims */
	// ONLY claims recorded in baseline.claim_ids (scope); evaluated by git
	// object identity of the recorded backing blob (ARCH §4.3 source anchor).
	claimIDsInScope := make(map[string]bool, len(baseline.ClaimIDs))
	for _, id := range baseline.ClaimIDs {
		claimIDsInScope[id] = true
	}
	claimsInScope := make([]schemas.Claim, 0, len(input.Claims))
	for _, claim := range input.Claims {
		if claimIDsInScope[claim.ID] {
			claimsInScope = append(claimsInScope, claim)
		}
	}
	sort.SliceStable(claimsInScope, func(i, j int) bool {
		return compareStrings(claimsInScope[i].ID, claimsInScope[j].ID) < 0
	})
	claimFacts := make([]schemas.DriftClaimFact, 0, len(claimsInScope))
	for _, claim := range claimsInScope {
		sourcePath := claim.Source.File
		record := index.byPath[sourcePath]
		headBlob, err := blobContentOrNull(ctx, git, headSha, sourcePath)
		if err != nil {
			return nil, err
		}
		var verdict schemas.DriftClaimVerdict
		var proposed *string
		switch {
		case headBlob == nil:
			verdict = schemas.DriftClaimSourceMissing
			proposed = strPtrLocal(refutedSupportProposal)
		case headBlob.sha == claim.Source.BlobSHA:
			// The recorded blob is still the blob at head: the file may have
			// changed elsewhere (support not refuted).
			if record == nil {
				verdict = schemas.DriftClaimUnchanged
			} else {
				verdict = schemas.DriftClaimSourceChanged
			}
		default:
			verdict = schemas.DriftClaimSupportMoved
			proposed = strPtrLocal(refutedSupportProposal)
		}
		fact := schemas.DriftClaimFact{
			ClaimID:        claim.ID,
			Verdict:        verdict,
			PreviousStatus: claim.Status,
			SourceBlobSHA:  claim.Source.BlobSHA,
			EvidencePaths:  []string{sourcePath},
		}
		if proposed != nil {
			fact.ProposedStatus = proposed
		}
		if headBlob != nil {
			sha := headBlob.sha
			fact.SourceBlobSHAAtHead = &sha
		}
		claimFacts = append(claimFacts, fact)
	}

	changedSources := 0
	for _, fact := range sources {
		if fact.Status != schemas.DriftSourceStatusUnchanged {
			changedSources++
		}
	}
	driftedSymbols := 0
	for _, fact := range symbols {
		if fact.Kind == schemas.DriftSymbolAbsent {
			driftedSymbols++
		}
	}
	driftedClaims := 0
	for _, fact := range claimFacts {
		if fact.Verdict != schemas.DriftClaimUnchanged {
			driftedClaims++
		}
	}

	report := schemas.DriftReport{
		Schema:         schemas.DriftReportSchemaID,
		RunID:          input.RunID,
		At:             input.At,
		Head:           headSha,
		BaselineCommit: baselineSha,
		LadderRun:      schemas.LadderRun(ladder.Run),
		LadderEvidence: ladder.Evidence,
		DocPath:        baseline.DocPath,
		Sources:        sources,
		Symbols:        symbols,
		Claims:         claimFacts,
		Summary: schemas.DriftReportSummary{
			SourcesChanged: changedSources,
			SymbolsDrifted: driftedSymbols,
			ClaimsDrifted:  driftedClaims,
		},
	}
	// Fail closed on any producer bug: the report must satisfy its own schema.
	return validateDriftReport(report)
}

// validateDriftReport is the Go rendering of the TS `DRIFT_REPORT_SCHEMA.parse`
// on the produced report: the value is serialized and strictly parsed back
// against `docsync.drift-report/v1` (unknown keys rejected, zod validation
// messages reproduced), and the PARSED report is what the caller receives.
func validateDriftReport(report schemas.DriftReport) (*schemas.DriftReport, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return schemas.ParseDriftReport(raw)
}

// sortedUnique dedupes and sorts a recorded scope list with code-unit
// comparison (the TS `[...new Set(values)].sort(compareStrings)`).
func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.SliceStable(out, func(i, j int) bool { return compareStrings(out[i], out[j]) < 0 })
	return out
}

// containsText is the TS `String.prototype.includes` (exact substring
// membership, no normalization).
func containsText(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// strPtrLocal is the local pointer helper (the TS optional-field projection).
func strPtrLocal(s string) *string { return &s }

// baselineNilError is the fail-closed refusal for an absent baseline record
// (the TS type made the value non-nullable; the Go port refuses rather than
// dereferencing).
func baselineNilError() error {
	return docutil.New(docutil.CodeInternal,
		"analyzeDrift: baseline is nil — refusing to analyze an absent record (fail closed)")
}

// gitNilError is the fail-closed refusal for an absent git client.
func gitNilError() error {
	return docutil.New(docutil.CodeInternal,
		"analyzeDrift: git client is nil — refusing to analyze without git (fail closed)")
}
