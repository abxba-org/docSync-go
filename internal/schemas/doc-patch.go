package schemas

import (
	"sort"
	"strconv"
	"strings"
)

// Ported from packages/schemas/src/v1/doc-patch.ts — DocPatch wire schema
// `docsync.doc-patch/v1` (ARCH §4.9; finalized P3-T06). Structured, anchored
// replacements — never whole-document rewrites (invariant 12). Anchors are
// content-hash-first, line-number-second; application races abstain to REVIEW,
// never force-apply.
//
// Per-hunk evidence (OD-R08): every hunk carries at least one independent
// machine-resolvable evidence ref (ARCH §4.7 duty 5 / §4.8 Tier-2 surface) —
// the mandatory minimum, so patches stay valid on sparse-claim repositories
// (V1 fallback); `claim_ids` adds the stronger provenance when the claim store
// already covers the anchor. The patch-level `evidence_refs` array stays as the
// aggregate view; the per-hunk refs are the verifier's binding surface.
//
// Whole-document-rewrite rejection (P3-T07, invariant 12): the schema cannot
// see the document, so it enforces only shape invariants — strictly
// non-overlapping hunk line ranges (a structural-integrity defect the applier
// would reject anyway, caught here cheaply). The structural rejection itself
// lives in the pure exported validators IsWholeDocumentRewrite /
// AssertNotWholeDocumentRewrite; its enforcement points are the applier
// precheck (Phase 3) and the Phase-5 orchestrator gate pre-application
// (OD-R43 ratification) — see RewriteRejectionSchemaNote.
//
// Hash format coherence (OD-R42 context): anchor `content_hash` and patch
// `base_content_hash` are BARE 64-hex lowercase sha256 digests — no `sha256:`
// prefix — matching core's contentHash conventions (state/hash.ts); the
// refinement fails at parse time (hex.go's IsBareSHA256Hex).

// docPatchSchemaID is the wire schema literal (TS: `z.literal('docsync.doc-patch/v1')`).
const docPatchSchemaID = "docsync.doc-patch/v1"

// PatchAnchor is one anchored replacement position: content-hash-first,
// line-number-second (TS: PATCH_ANCHOR_SCHEMA, z.infer PatchAnchor).
type PatchAnchor struct {
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	ContentHash string `json:"content_hash"`
}

// Validate checks the anchor against PATCH_ANCHOR_SCHEMA, including the
// superRefine `end_line >= start_line` (TS: PATCH_ANCHOR_SCHEMA.superRefine).
func (a *PatchAnchor) Validate() []Issue {
	var issues []Issue
	if a.StartLine < 1 {
		Collect(&issues, &Issue{Path: []string{"start_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.EndLine < 1 {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "Number must be greater than or equal to 1"})
	}
	Collect(&issues, RequiredString(a.ContentHash, 1, []string{"content_hash"}))
	if !IsBareSHA256Hex(a.ContentHash) {
		Collect(&issues, &Issue{Path: []string{"content_hash"}, Message: "content_hash must be bare 64-hex sha256 (no sha256: prefix)"})
	}
	if a.EndLine < a.StartLine {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "end_line must be greater than or equal to start_line"})
	}
	return issues
}

// PatchHunk is one anchored replacement (TS: PATCH_HUNK_SCHEMA, z.infer
// PatchHunk). `delete_text === ""` is a legitimate addition-only hunk;
// `insert_text === ""` is a deletion-only hunk. Evidence per OD-R08:
// machine-resolvable `evidence_refs` (min 1) are mandatory; `claim_ids` is the
// optional stronger provenance when claim coverage exists.
//
// Claim-level edit semantics (P11-T15, OD-R26): `verified_claim_refs` is an
// OPTIONAL, strictly-additive per-hunk list of the claim-store ids whose
// verification this hunk carries forward — hunk-based application is retained
// unchanged, and the field is absent from every legacy patch (DX-P11-2). The
// verifier resolves each ref against the claim source available to it and FAILS
// CLOSED on an unresolvable ref; the doc-patch schema only shapes the records
// and enforces the subset rule at the patch level below.
type PatchHunk struct {
	Anchor            PatchAnchor `json:"anchor"`
	DeleteText        string      `json:"delete_text"`
	InsertText        string      `json:"insert_text"`
	EvidenceRefs      []Evidence  `json:"evidence_refs"`
	ClaimIDs          []string    `json:"claim_ids,omitempty"`
	VerifiedClaimRefs []string    `json:"verified_claim_refs,omitempty"`
}

// Validate checks the hunk against PATCH_HUNK_SCHEMA.
func (h *PatchHunk) Validate() []Issue {
	var issues []Issue
	for _, iss := range h.Anchor.Validate() {
		iss.Path = append([]string{"anchor"}, iss.Path...)
		issues = append(issues, iss)
	}
	if len(h.EvidenceRefs) < 1 {
		Collect(&issues, &Issue{Path: []string{"evidence_refs"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range h.EvidenceRefs {
		for _, iss := range h.EvidenceRefs[i].Validate() {
			iss.Path = append([]string{"evidence_refs", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i, claimID := range h.ClaimIDs {
		Collect(&issues, RequiredString(claimID, 1, []string{"claim_ids", itoa(i)}))
	}
	for i, ref := range h.VerifiedClaimRefs {
		Collect(&issues, RequiredString(ref, 1, []string{"verified_claim_refs", itoa(i)}))
	}
	return issues
}

// DocPatchMinimality is the writer's measured minimality (TS:
// DOC_PATCH_MINIMALITY_SCHEMA, z.infer DocPatchMinimality).
type DocPatchMinimality struct {
	Measured     bool `json:"measured"`
	AddedLines   int  `json:"added_lines"`
	RemovedLines int  `json:"removed_lines"`
}

// Validate checks the minimality block against DOC_PATCH_MINIMALITY_SCHEMA.
func (m *DocPatchMinimality) Validate() []Issue {
	var issues []Issue
	if m.AddedLines < 0 {
		Collect(&issues, &Issue{Path: []string{"added_lines"}, Message: "Number must be greater than or equal to 0"})
	}
	if m.RemovedLines < 0 {
		Collect(&issues, &Issue{Path: []string{"removed_lines"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// DocPatchProvenance is writer provenance (P3-T15 spec, PLAN §4.4: actual
// routed model id + policy_version + generated_at; ARCH §10.6/§12.5).
// `writer_model` MUST be the actually-routed model, never the role's
// configured default — enforcement at the writer adapter (V-P3-9); this
// schema only shapes the record. `generated_at` is ISO-8601; the clock is
// injected by callers.
type DocPatchProvenance struct {
	PolicyVersion int    `json:"policy_version"`
	WriterModel   string `json:"writer_model"`
	GeneratedAt   string `json:"generated_at"`
}

// Validate checks the provenance block against DOC_PATCH_PROVENANCE_SCHEMA.
func (p *DocPatchProvenance) Validate() []Issue {
	var issues []Issue
	if p.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(p.WriterModel, 1, []string{"writer_model"}))
	// z.string().datetime() — 'Z' suffix only (no offset support), pinned by
	// the shared run-record.go helper.
	Collect(&issues, datetimeIssue(p.GeneratedAt, []string{"generated_at"}))
	return issues
}

// DocPatch is the `docsync.doc-patch/v1` wire object (TS: DOC_PATCH_SCHEMA,
// z.infer DocPatch).
type DocPatch struct {
	Schema          string             `json:"schema"`
	RunID           string             `json:"run_id"`
	DocPath         string             `json:"doc_path"`
	BaseContentHash string             `json:"base_content_hash"`
	Hunks           []PatchHunk        `json:"hunks"`
	EvidenceRefs    []Evidence         `json:"evidence_refs"`
	Minimality      DocPatchMinimality `json:"minimality"`
	Provenance      DocPatchProvenance `json:"provenance"`
	// VerifiedClaims is the patch-level set of verified claim ids (P11-T15,
	// OD-R26): every hunk's verified_claim_refs must be a subset of this set
	// (Validate below). Strictly additive and OPTIONAL — every legacy patch
	// parses unchanged (DX-P11-2); the verifier resolves each ref against its
	// claim source and fails closed on an unresolvable one (never strips, never
	// silently passes).
	VerifiedClaims []string `json:"verified_claims,omitempty"`
}

// Validate checks the patch against DOC_PATCH_SCHEMA, including both
// superRefine refinements: strictly non-overlapping hunk line ranges and the
// claim-level subset rule (P11-T15, OD-R26 — every hunk's
// verified_claim_refs must be a subset of the patch-level verified_claims).
func (p *DocPatch) Validate() []Issue {
	var issues []Issue
	if p.Schema != docPatchSchemaID {
		msg := `Invalid literal value, expected "` + docPatchSchemaID + `"`
		if p.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(p.RunID, 1, []string{"run_id"}))
	Collect(&issues, RequiredString(p.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, RequiredString(p.BaseContentHash, 1, []string{"base_content_hash"}))
	if !IsBareSHA256Hex(p.BaseContentHash) {
		Collect(&issues, &Issue{Path: []string{"base_content_hash"}, Message: "base_content_hash must be bare 64-hex sha256 (no sha256: prefix)"})
	}
	// hunks min(1): an EMPTY patch is a protocol violation — a writer that
	// confirms no drift ABSTAINS upstream, it never emits a zero-hunk patch.
	if len(p.Hunks) < 1 {
		Collect(&issues, &Issue{Path: []string{"hunks"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range p.Hunks {
		for _, iss := range p.Hunks[i].Validate() {
			iss.Path = append([]string{"hunks", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range p.EvidenceRefs {
		for _, iss := range p.EvidenceRefs[i].Validate() {
			iss.Path = append([]string{"evidence_refs", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range p.Minimality.Validate() {
		iss.Path = append([]string{"minimality"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range p.Provenance.Validate() {
		iss.Path = append([]string{"provenance"}, iss.Path...)
		issues = append(issues, iss)
	}
	for i, claim := range p.VerifiedClaims {
		Collect(&issues, RequiredString(claim, 1, []string{"verified_claims", itoa(i)}))
	}

	// superRefine — hunks sorted by (start_line, end_line), stable, so tie
	// order matches the TS `ordered` comparator over the original array.
	ordered := make([]orderedHunk, len(p.Hunks))
	for i := range p.Hunks {
		ordered[i] = orderedHunk{hunk: &p.Hunks[i], index: i}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].hunk.Anchor.StartLine != ordered[j].hunk.Anchor.StartLine {
			return ordered[i].hunk.Anchor.StartLine < ordered[j].hunk.Anchor.StartLine
		}
		return ordered[i].hunk.Anchor.EndLine < ordered[j].hunk.Anchor.EndLine
	})
	// Strictly non-overlapping hunk line ranges.
	for i := 1; i < len(ordered); i++ {
		previous := ordered[i-1]
		current := ordered[i]
		if current.hunk.Anchor.StartLine <= previous.hunk.Anchor.EndLine {
			Collect(&issues, &Issue{
				Path: []string{"hunks"},
				Message: "hunks " + itoa(previous.index) + " and " + itoa(current.index) + " overlap in line ranges " +
					"[" + itoa(previous.hunk.Anchor.StartLine) + ", " + itoa(previous.hunk.Anchor.EndLine) + "] and " +
					"[" + itoa(current.hunk.Anchor.StartLine) + ", " + itoa(current.hunk.Anchor.EndLine) + "] — " +
					"hunk line ranges must be strictly non-overlapping",
			})
		}
	}
	// Claim-level subset rule (P11-T15, OD-R26): a hunk may only carry forward
	// verification of claims the patch declares verified at its level — a ref
	// outside `verified_claims` (including the case where the patch-level set
	// is absent entirely) is a parse-time defect naming the field and the
	// offending ref.
	patchLevel := make(map[string]bool, len(p.VerifiedClaims))
	for _, claim := range p.VerifiedClaims {
		patchLevel[claim] = true
	}
	for _, entry := range ordered {
		for _, ref := range entry.hunk.VerifiedClaimRefs {
			if !patchLevel[ref] {
				Collect(&issues, &Issue{
					Path: []string{"hunks", itoa(entry.index), "verified_claim_refs"},
					Message: "hunks[" + itoa(entry.index) + "].verified_claim_refs contains '" + ref + "' which is not in the " +
						"patch-level verified_claims set — every hunk verified_claim_ref must be a subset " +
						"of the patch-level verified_claims (P11-T15, OD-R26)",
				})
			}
		}
	}
	return issues
}

// orderedHunk pairs a hunk with its original index for the superRefine sorts
// (TS: `patch.hunks.map((hunk, index) => ({ hunk, index }))`).
type orderedHunk struct {
	hunk  *PatchHunk
	index int
}

// ParseDocPatch strictly parses and validates `docsync.doc-patch/v1` JSON
// bytes (TS: DOC_PATCH_SCHEMA.parse; unknown keys are rejected — strictObject,
// OD-R01).
func ParseDocPatch(data []byte) (*DocPatch, error) {
	var p DocPatch
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

/* ------------------------------------------- whole-document-rewrite rejection */

// RewriteRejectionSchemaNote — why the schema cannot carry the
// whole-document-rewrite rule (P3-T07, invariant 12): the schema sees only the
// patch, never the document, so "delete_text equals the document body" is
// undecidable at parse time. The structural rejection therefore lives in the
// pure validators IsWholeDocumentRewrite / AssertNotWholeDocumentRewrite
// below; its enforcement points are the applier precheck (Phase 3) and the
// Phase-5 orchestrator gate pre-application (OD-R43 ratification); the schema
// contributes the shape invariants only (non-overlapping hunk ranges).
const RewriteRejectionSchemaNote = "whole-document-rewrite rejection needs the document body, which the schema " +
	"cannot see: shape-level parsing enforces non-overlapping hunk ranges only, " +
	"while the structural rule (delete_text spans >= WHOLE_DOC_REWRITE_RATIO of " +
	"the document's non-empty lines, or the applied result regenerates the " +
	"document beyond the same ratio) is enforced by the exported pure validator " +
	"assertNotWholeDocumentRewrite at the applier precheck (Phase 3) and the " +
	"Phase-5 orchestrator gate pre-application (OD-R43 ratification; invariant 12)"

// WholeDocumentRewriteRatio is the architectural constant: the SHAPE of the
// whole-document-rewrite rule is architecture (invariant 12); the exact ratio
// is calibrated data. 0.9 means a patch whose deletion span (or applied-result
// structural diff) covers >= 90% of the document's non-empty lines is a
// rewrite, not a minimal patch. Never hard-code this value at call sites —
// consume the exported constant (TS: WHOLE_DOC_REWRITE_RATIO).
const WholeDocumentRewriteRatio = 0.9

// WholeDocumentRewriteReason — which structural rule classified a patch as a
// rewrite (TS: WholeDocumentRewriteReason).
type WholeDocumentRewriteReason string

// Whole-document-rewrite reason values (TS: 'deletion_span' | 'structural_diff').
const (
	WholeDocumentRewriteDeletionSpan   WholeDocumentRewriteReason = "deletion_span"
	WholeDocumentRewriteStructuralDiff WholeDocumentRewriteReason = "structural_diff"
)

// WholeDocumentRewriteErrorCode is the machine-readable failure code (stable
// string consumers can switch on; TS: WHOLE_DOCUMENT_REWRITE_ERROR_CODE).
const WholeDocumentRewriteErrorCode = "WHOLE_DOCUMENT_REWRITE"

// WholeDocumentRewriteError is thrown by AssertNotWholeDocumentRewrite —
// invariant 12 enforcement point (TS: WholeDocumentRewriteError). It carries
// the machine-readable code, which structural rule fired, and the measured
// ratio that crossed WholeDocumentRewriteRatio.
type WholeDocumentRewriteError struct {
	Reason        WholeDocumentRewriteReason
	MeasuredRatio float64
}

// Code returns the machine-readable failure code (TS: static/instance `code`).
func (e *WholeDocumentRewriteError) Code() string { return WholeDocumentRewriteErrorCode }

// Error reproduces the TS message exactly.
func (e *WholeDocumentRewriteError) Error() string {
	return "whole-document rewrite rejected structurally (" + string(e.Reason) + "): measured ratio " +
		strconv.FormatFloat(e.MeasuredRatio, 'g', -1, 64) + " >= WHOLE_DOC_REWRITE_RATIO " +
		strconv.FormatFloat(WholeDocumentRewriteRatio, 'g', -1, 64)
}

func isNonEmptyLine(line string) bool {
	return strings.TrimSpace(line) != ""
}

func countNonEmptyLines(lines []string) int {
	count := 0
	for _, line := range lines {
		if isNonEmptyLine(line) {
			count++
		}
	}
	return count
}

// lineMultiset is the multiset of trimmed non-empty lines — the
// structural-diff basis for rule B.
func lineMultiset(lines []string) map[string]int {
	counts := make(map[string]int)
	for _, line := range lines {
		if !isNonEmptyLine(line) {
			continue
		}
		counts[strings.TrimSpace(line)]++
	}
	return counts
}

// multisetDelta is the symmetric line-multiset delta: added + removed
// non-empty lines.
func multisetDelta(a, b map[string]int) int {
	delta := 0
	for key, count := range a {
		delta += absInt(count - b[key])
	}
	for key, count := range b {
		if _, ok := a[key]; !ok {
			delta += count
		}
	}
	return delta
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// deletionSpanNonEmptyCount counts the non-empty document lines covered by the
// union of deletion hunks' anchor ranges.
func deletionSpanNonEmptyCount(hunks []PatchHunk, lines []string) int {
	covered := make(map[int]bool)
	for _, hunk := range hunks {
		if hunk.DeleteText == "" {
			continue
		}
		for line := hunk.Anchor.StartLine; line <= hunk.Anchor.EndLine; line++ {
			covered[line-1] = true
		}
	}
	count := 0
	for index := range covered {
		if index >= 0 && index < len(lines) && isNonEmptyLine(lines[index]) {
			count++
		}
	}
	return count
}

// applyHunksStructurally simulates hunk application (anchored line replacement,
// bottom-up so earlier indices do not shift) — mirrors the applier's semantics
// only for measurement; the reference applyHunks engine lives in core. Anchor
// ranges are clamped to the document; out-of-bounds anchors are the applier's
// UNANCHORED/abstain concern, not this predicate's. Zero-width anchors
// (start_line === end_line, pure insertion) remove NOTHING — they insert before
// the anchor line, mirroring the engine (patch/apply.ts: a pure insertion
// splices with removeCount 0; the inclusive-range removal applies only to
// replacements/deletions).
func applyHunksStructurally(hunks []PatchHunk, lines []string) []string {
	applied := make([]string, len(lines))
	copy(applied, lines)
	bottomUp := make([]PatchHunk, len(hunks))
	copy(bottomUp, hunks)
	sort.SliceStable(bottomUp, func(i, j int) bool {
		return bottomUp[i].Anchor.StartLine > bottomUp[j].Anchor.StartLine
	})
	for _, hunk := range bottomUp {
		start := hunk.Anchor.StartLine
		if start < 1 {
			start = 1
		}
		start-- // 0-based
		end := hunk.Anchor.EndLine
		if end > len(applied) {
			end = len(applied)
		}
		var replacement []string
		if hunk.InsertText != "" {
			replacement = strings.Split(hunk.InsertText, "\n")
		}
		removeCount := 0
		if hunk.DeleteText != "" {
			removeCount = end - start
			if removeCount < 0 {
				removeCount = 0
			}
		}
		// splice(start, removeCount, ...replacement) with TS clamping semantics.
		if start > len(applied) {
			start = len(applied)
		}
		tailFrom := start + removeCount
		if tailFrom > len(applied) {
			tailFrom = len(applied)
		}
		next := make([]string, 0, start+len(replacement)+len(applied)-tailFrom)
		next = append(next, applied[:start]...)
		next = append(next, replacement...)
		next = append(next, applied[tailFrom:]...)
		applied = next
	}
	return applied
}

// rewriteDetection mirrors the TS RewriteDetection interface.
type rewriteDetection struct {
	reason        WholeDocumentRewriteReason
	measuredRatio float64
}

func detectWholeDocumentRewrite(patch *DocPatch, docContent string) *rewriteDetection {
	var hunks []PatchHunk
	if patch != nil {
		hunks = patch.Hunks
	}
	lines := strings.Split(docContent, "\n")
	totalNonEmpty := countNonEmptyLines(lines)
	if totalNonEmpty == 0 {
		// An empty document has nothing to rewrite: an addition-only patch is
		// creation, not a replacement of existing content.
		return nil
	}
	// Rule A — deletion span: the union of deletion hunks' anchor ranges may
	// not cover >= WholeDocumentRewriteRatio of the document's non-empty lines.
	// This is span-based by design: a writer that read the whole body into
	// delete_text is encoding a whole-document replacement regardless of what
	// it inserts back.
	deletionRatio := float64(deletionSpanNonEmptyCount(hunks, lines)) / float64(totalNonEmpty)
	if deletionRatio >= WholeDocumentRewriteRatio {
		return &rewriteDetection{reason: WholeDocumentRewriteDeletionSpan, measuredRatio: deletionRatio}
	}
	// Rule B — structural diff: with all hunks applied, the result must not
	// regenerate a document whose line-multiset diff vs the original exceeds
	// the same ratio — the backstop for insert-dominated rewrites.
	applied := applyHunksStructurally(hunks, lines)
	diffRatio := float64(multisetDelta(lineMultiset(lines), lineMultiset(applied))) / float64(totalNonEmpty)
	if diffRatio >= WholeDocumentRewriteRatio {
		return &rewriteDetection{reason: WholeDocumentRewriteStructuralDiff, measuredRatio: diffRatio}
	}
	return nil
}

// IsWholeDocumentRewrite is the pure predicate: does this patch structurally
// encode a whole-document rewrite of docContent? Rule A — the union of
// deletion hunks' anchor ranges covers >= WholeDocumentRewriteRatio of the
// document's non-empty lines; Rule B — the applied result's line-multiset diff
// vs the original exceeds the same ratio (symmetric delta: replacing k lines
// contributes 2k, so patches that rewrite ~half the document's lines are
// caught). No clocks, no I/O; consumes the exported ratio constant.
func IsWholeDocumentRewrite(patch *DocPatch, docContent string) bool {
	return detectWholeDocumentRewrite(patch, docContent) != nil
}

// MeasureStructuralDiffRatio exposes the rule-B measurement
// (testability/parity surface, V-P3-10 fix pass): the structural line-multiset
// diff ratio between the ORIGINAL document and the patch's structurally
// simulated application — the same quantity rule B compares against
// WholeDocumentRewriteRatio, divided by the original's non-empty line count (0
// for an empty document). Pure: mirrors the applier's splice semantics
// including zero-width insertion anchors (removeCount 0); the core engine
// (applyDocPatch + measureMinimality) is the reference — parity is pinned by
// the shared fixtures/parity tests.
func MeasureStructuralDiffRatio(patch *DocPatch, docContent string) float64 {
	var hunks []PatchHunk
	if patch != nil {
		hunks = patch.Hunks
	}
	lines := strings.Split(docContent, "\n")
	totalNonEmpty := countNonEmptyLines(lines)
	if totalNonEmpty == 0 {
		return 0
	}
	applied := applyHunksStructurally(hunks, lines)
	return float64(multisetDelta(lineMultiset(lines), lineMultiset(applied))) / float64(totalNonEmpty)
}

// AssertNotWholeDocumentRewrite is the structural rejection point (P3-T07,
// invariant 12): returns a *WholeDocumentRewriteError when the patch is a
// whole-document rewrite of docContent. Enforced at the applier precheck
// (Phase 3, V-P3-2) and at the Phase-5 orchestrator gate pre-application
// (OD-R43 ratification) — a writer double returning a full-document
// replacement is rejected STRUCTURALLY before application.
func AssertNotWholeDocumentRewrite(patch *DocPatch, docContent string) error {
	detection := detectWholeDocumentRewrite(patch, docContent)
	if detection != nil {
		return &WholeDocumentRewriteError{Reason: detection.reason, MeasuredRatio: detection.measuredRatio}
	}
	return nil
}

// DocPatchExample is the wire example (OD-2). Hash format (OD-R42 coherence):
// BARE 64-hex lowercase sha256 — no 'sha256:' prefix — matching core
// contentHash conventions. The values are real digests: the hunk's
// `content_hash` is sha256 over the exact quoted span (`delete_text`,
// newline-joined — the hashSpan convention), and `base_content_hash` is
// sha256 over the sentinel string "docs/payments.md @ 81af92e" standing in for
// the base document body (TS: DOC_PATCH_EXAMPLE).
var DocPatchExample = DocPatch{
	Schema:          docPatchSchemaID,
	RunID:           "acme/payments#8a73f2c..81af92e",
	DocPath:         "docs/payments.md",
	BaseContentHash: "cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",
	Hunks: []PatchHunk{
		{
			Anchor: PatchAnchor{
				StartLine:   42,
				EndLine:     42,
				ContentHash: "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506",
			},
			DeleteText: "Refunds require an idempotency key.",
			InsertText: "Refunds require an idempotency key for amounts above 10 USD.",
			EvidenceRefs: []Evidence{
				{
					Kind: "symbol_diff",
					Ref:  "src/payments/refund.ts:42",
					Note: "idempotencyKey became optional",
				},
			},
			ClaimIDs: []string{"claim_182"},
		},
	},
	EvidenceRefs: []Evidence{
		{
			Kind: "symbol_diff",
			Ref:  "src/payments/refund.ts:42",
			Note: "idempotencyKey became optional",
		},
	},
	Minimality: DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1},
	Provenance: DocPatchProvenance{
		PolicyVersion: 17,
		WriterModel:   "anthropic/claude-sonnet-4.5",
		GeneratedAt:   "2026-10-04T22:31:00Z",
	},
}
