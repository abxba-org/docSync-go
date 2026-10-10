package patch

// Ported from core/src/patch/types.ts (P3-T08..T12, P3-T14, P3-T16; ARCH §4.3,
// §4.9, §11.4 layer 3, §12.1/§12.3, A9; invariants 2/3/12/13).
//
// The application engine is a DETERMINISTIC, PURE decision core: it NEVER
// returns an error for a semantic outcome (typed ApplyOutcome results instead —
// a guard refusal IS an abstain-kind outcome at the engine layer, not an
// exception) and it NEVER writes to disk or git itself — it returns
// ResultContent + the outcome record; persistence is the Phase 5/6 write path's
// job. Only ENGINE CONFIGURATION errors (a missing/typed-wrong guard input such
// as the manifest allowlist or the injected clock) surface as a
// *PatchEngineError — a misconfigured guard cannot even decide, and fail-closed
// (D8) means refusing to run, not guessing.

import (
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AppliedHunk (TS: AppliedHunk) is where one hunk actually landed (post
// content-hash-first resolution). Coordinates are BASE-relative: StartLine/
// EndLine are the coordinates within the content the engine spliced — the
// writer's base snapshot when the 3-way merge was used (the merge maps regions
// independently, so they are NOT coordinates in the merged/current document),
// the current document otherwise.
type AppliedHunk struct {
	// Index of the hunk within patch.hunks (original order).
	Index int `json:"index"`
	// StartLine/EndLine are the base-relative inclusive range the hunk applied to.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// ReAnchored is true when the unique content-hash match lay at DIFFERENT lines
	// than recorded — a legitimate re-anchor (hash match, ARCH §4.3), never silent
	// on ambiguity (invariant 13: ambiguous → unanchored abstain instead).
	ReAnchored bool `json:"reAnchored"`
}

// ApplyAbstainReason (TS: ApplyAbstainReason) is why a whole-patch abstention
// fired. overlap/out-of-bounds are post-resolution backstops (the schema's
// non-overlap refinement plus the bounds sanity in anchor resolution make them
// near-unreachable for schema-valid patches; the engine defends in depth
// against malformed DocPatch objects that skipped schema validation).
type ApplyAbstainReason string

const (
	AbstainDocChangedRaceUnverified ApplyAbstainReason = "doc-changed-race-unverified"
	AbstainRewriteRejected          ApplyAbstainReason = "rewrite-rejected"
	AbstainGuardRefused             ApplyAbstainReason = "guard-refused"
	AbstainOverlap                  ApplyAbstainReason = "overlap"
	AbstainOutOfBounds              ApplyAbstainReason = "out-of-bounds"
)

// ApplyOutcomeKind is the discriminated-union tag of ApplyOutcome (the TS
// `kind`).
type ApplyOutcomeKind string

const (
	OutcomeApplied    ApplyOutcomeKind = "applied"
	OutcomeNoop       ApplyOutcomeKind = "noop"
	OutcomeUnanchored ApplyOutcomeKind = "unanchored"
	OutcomeAbstain    ApplyOutcomeKind = "abstain"
)

// NoopReason (TS: 'already-applied' | 'run-id-skip') is the noop-variant reason.
type NoopReason string

const (
	NoopAlreadyApplied NoopReason = "already-applied"
	NoopRunIDSkip      NoopReason = "run-id-skip"
)

// DocStateSignal (TS: 'UNANCHORED') is the doc-state signal the unanchored
// outcome carries for the state machine / run records.
type DocStateSignal string

const DocStateSignalUnanchored DocStateSignal = "UNANCHORED"

// UnanchoredHunk is one per-hunk failure record (index into patch.hunks + a
// deterministic reason).
type UnanchoredHunk struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// ApplyOutcome (TS: ApplyOutcome) is a closed discriminated union rendered as
// one tagged struct (mirroring claims.AnchorResolution): Kind selects the
// variant and only that variant's fields are populated. The engine NEVER
// returns an error for these; every non-applied kind is a decision the caller
// routes (REVIEW / re-run / report), never a partial or silent application
// (invariant 13; ARCH §4.9: races abstain, they never force-apply).
type ApplyOutcome struct {
	Kind ApplyOutcomeKind

	// applied-only: the patched document content is the engine's ONLY output
	// surface; callers persist.
	Patch         *schemas.DocPatch
	ResultContent string
	AppliedHunks  []AppliedHunk
	ThreeWayUsed  bool
	// AppliedAt is the injected-clock timestamp of the outcome (provenance; never
	// a local clock).
	AppliedAt string
	// Minimality is the measured minimality of the actual content change
	// (P3-T12; exposed to the verifier).
	Minimality MinimalityMeasurement

	// noop-only.
	NoopReason NoopReason

	// unanchored-only.
	UnanchoredHunks []UnanchoredHunk
	DocStateSignal  DocStateSignal

	// abstain-only. Detail is deterministic, rule-naming detail for run records /
	// REVIEW routing.
	AbstainReason ApplyAbstainReason
	Detail        string
}

// StaleFinding (TS: StaleFinding) is the writer-timeout finding RECORD (P3-T14):
// when the writer times out, the pipeline still produces a
// 'potentially stale' finding WITHOUT a patch. The application engine itself
// never emits findings (it refuses to fabricate outcomes — ApplyDocPatch only
// ever returns ApplyOutcome); this type is the shared record shape the adapter
// (W6B) fills and the orchestrator routes to REVIEW/state.
type StaleFinding struct {
	Kind    string `json:"kind"` // always 'finding-without-patch'
	DocPath string `json:"doc_path"`
	Reason  string `json:"reason"` // always 'writer-timeout'
	Detail  string `json:"detail"`
}

// StaleFinding field values (TS literal strings).
const (
	StaleFindingKind          = "finding-without-patch"
	StaleFindingReasonTimeout = "writer-timeout"
)

// PatchEngineError (TS: PatchEngineError) is the typed engine-configuration
// error (fail closed, D8): it surfaces ONLY when the engine cannot even decide
// (missing/typed-wrong guard inputs, caller-port contract violations such as a
// base snapshot that does not hash to the patch's base_content_hash). Semantic
// outcomes are ApplyOutcome values, never this error.
//
// Error-class mapping (CONVENTIONS.md "Errors"): the TS static code
// 'PATCH_ENGINE_CONFIG' has no entry in internal/docutil, so it maps to the
// closest code, docutil.CodeConfig (EDOCSYNC_CONFIG) — no new code is invented.
// Error() preserves the exact TS message text; Code()/Unwrap() expose the
// mapped docutil code so consumers/extractors can still switch on it.
type PatchEngineError struct {
	// Msg is the exact TypeScript error message text.
	Msg string

	coded *docutil.Error
}

// newPatchEngineError builds a PatchEngineError carrying the exact message and
// the mapped docutil config code.
func newPatchEngineError(msg string) *PatchEngineError {
	return &PatchEngineError{Msg: msg, coded: docutil.New(docutil.CodeConfig, msg)}
}

// Code returns the machine-readable failure class the TS exposed as
// PatchEngineError.code — mapped to docutil.CodeConfig (see the type doc).
func (e *PatchEngineError) Code() string { return e.coded.Code }

// Error reproduces the TS message text exactly.
func (e *PatchEngineError) Error() string { return e.Msg }

// Unwrap exposes the underlying coded error so docutil.CodeOf resolves it.
func (e *PatchEngineError) Unwrap() error { return e.coded }
