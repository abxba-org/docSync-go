// Ported from packages/core/src/orchestrator/contracts.ts (P5-T02; ARCH §3.3,
// §3.4, §13; D13/D14; V-P5-4).
//
// Inter-stage edge contracts: every §3.3 edge payload the orchestrator
// RECEIVES from an UNTRUSTED port is schema-validated at the edge, by the
// APPLICATION, BEFORE any use (ARCH §13: the application, never the model,
// decides what is permitted). One fail-closed validator per edge payload:
//
//   - ParseDecisionEdge — JEV → orchestrator (`docsync.decision/v1`);
//   - ParsePatchEdge — SONNET AGENT → VERIFICATION (`docsync.doc-patch/v1`);
//   - ParseVerificationEdge — VERIFICATION → POLICY
//     (`docsync.verification-report/v1`);
//   - ParseRelevanceContextEdge — DOCUMENT GRAPH → JEV relevance context (the
//     core-internal §3.3 edge; strict closed mirror of RelevanceContext in
//     types.go).
//
// A violating payload NEVER enters the pipeline hand-patched: run-level
// callers map StageContractError onto the canonical degradation defaults
// (ARCH §3.4 — JEV abstention to REVIEW / writer abstain / verifier_uncertain),
// so a violation always degrades to LESS automation, never a crash and never
// a guessed repair. All validators are PURE: no fs, no git, no network, no
// clock.
//
// Error discipline (D14; ARCH §14 invariants 5/9): every error detail is
// non-empty evidence text naming the edge plus the schema issue summary —
// never a stack-only failure.

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

// StageContractEdge is one §3.3 edge payload the orchestrator validates
// (closed union).
type StageContractEdge string

// The closed edge union (TS StageContractEdge).
const (
	EdgeDecision         StageContractEdge = "decision"
	EdgePatch            StageContractEdge = "patch"
	EdgeVerification     StageContractEdge = "verification"
	EdgeRelevanceContext StageContractEdge = "relevance-context"
)

// StageContractError is the typed inter-stage contract violation (P5-T02;
// ARCH §3.3/§3.4). Thrown/returned by the fail-closed edge validators when a
// payload received from an untrusted port violates its wire schema. Edge
// names the offending §3.3 edge so run-level callers can route the violation
// onto the canonical degradation defaults without string matching. The
// detail is ALWAYS non-empty evidence text naming the edge and the schema
// issue summary (never stack-only).
type StageContractError struct {
	pipeline *PipelineError
	edge     StageContractEdge
}

// Error renders the pipeline-error message (detail only — the contract
// violation is never stage-scoped, mirroring the TS super(detail, undefined)).
func (e *StageContractError) Error() string { return e.pipeline.Error() }

// Code returns EDOCSYNC_STAGE_CONTRACT.
func (e *StageContractError) Code() string { return CodeStageContract }

// Edge returns which §3.3 edge payload violated its contract.
func (e *StageContractError) Edge() StageContractEdge { return e.edge }

// Stage returns the stage the failure is scoped to ("" — never stage-scoped).
func (e *StageContractError) Stage() StageName { return e.pipeline.Stage() }

// Detail returns the deterministic reason text.
func (e *StageContractError) Detail() string { return e.pipeline.Detail() }

// Unwrap exposes the base pipeline error (so errors.As finds both the typed
// refinement and the PipelineError base, mirroring the TS instanceof chain).
func (e *StageContractError) Unwrap() error { return e.pipeline }

// IsStageContractError reports whether err is (or wraps) the typed
// inter-stage contract violation.
func IsStageContractError(err error) bool {
	var contractErr *StageContractError
	return asStageContractError(err, &contractErr)
}

func asStageContractError(err error, target **StageContractError) bool {
	for err != nil {
		if contractErr, ok := err.(*StageContractError); ok {
			*target = contractErr
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// StageContractEdgeOf returns the edge carried by err and whether err is a
// StageContractError at all.
func StageContractEdgeOf(err error) (StageContractEdge, bool) {
	var contractErr *StageContractError
	if asStageContractError(err, &contractErr) {
		return contractErr.Edge(), true
	}
	return "", false
}

/* ------------------------------------------------------------ issue summary */

// issueSummaryOf renders the non-empty schema issue summary
// (`path: message; …`) — evidence detail, D13. A non-ValidationError becomes
// a single root-path entry.
func issueSummaryOf(err error) string {
	var validationErr *schemas.ValidationError
	issues := []schemas.Issue{}
	if e, ok := err.(*schemas.ValidationError); ok {
		validationErr = e
	}
	if validationErr != nil {
		issues = validationErr.Issues
	} else {
		issues = append(issues, schemas.Issue{Message: err.Error()})
	}
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		path := "(root)"
		if len(issue.Path) > 0 {
			path = strings.Join(issue.Path, ".")
		}
		parts = append(parts, fmt.Sprintf("%s: %s", path, issue.Message))
	}
	summary := strings.TrimSpace(strings.Join(parts, "; "))
	if summary == "" {
		return "schema validation failed (no issue details)"
	}
	return summary
}

// newStageContractError builds the typed violation with the edge-naming
// evidence detail.
func newStageContractError(edge StageContractEdge, wire string, cause error) *StageContractError {
	detail := fmt.Sprintf("%s-edge contract violation (%s): %s", edge, wire, issueSummaryOf(cause))
	return &StageContractError{
		pipeline: NewPipelineRunError(detail, "", cause),
		edge:     edge,
	}
}

/* ---------------------------------------------------------- value re-checks */

// revalidateStruct re-validates an already typed payload an untrusted port
// returned — the Go spelling of the TS `parseDecisionEdge(raw.decision)` (the
// port's object is re-encoded onto its wire form and re-parsed, so a
// hand-built struct that violates the schema still fails the edge exactly
// like raw wire bytes).
func revalidateStruct[T any](value *T, parse func([]byte) (*T, error)) (*T, error) {
	if value == nil {
		return nil, fmt.Errorf("nil payload")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

/* --------------------------------------------------------------- edge specs */

// edgeSpec pairs one edge with its wire schema id and parser.
type edgeSpec[T any] struct {
	edge  StageContractEdge
	wire  string
	parse func([]byte) (T, error)
}

// parseEdgePayload is the fail-closed edge parse: schema-validate raw or
// return the typed violation (TS parseEdgePayload).
func parseEdgePayload[T any](spec edgeSpec[T], raw []byte) (T, error) {
	parsed, err := spec.parse(raw)
	if err == nil {
		return parsed, nil
	}
	var zero T
	return zero, newStageContractError(spec.edge, spec.wire, err)
}

var (
	decisionEdge = edgeSpec[*schemas.Decision]{
		edge:  EdgeDecision,
		wire:  "docsync.decision/v1",
		parse: schemas.ParseDecision,
	}
	patchEdge = edgeSpec[*schemas.DocPatch]{
		edge:  EdgePatch,
		wire:  "docsync.doc-patch/v1",
		parse: schemas.ParseDocPatch,
	}
	verificationEdge = edgeSpec[*schemas.VerificationReport]{
		edge:  EdgeVerification,
		wire:  "docsync.verification-report/v1",
		parse: schemas.ParseVerificationReport,
	}
)

/* -------------------------------------------------------- the edge parsers */

// ParseDecisionEdge validates the decision payload received from the
// UNTRUSTED decision port against the registered `docsync.decision/v1` wire
// schema (§5.4) before any routing use.
func ParseDecisionEdge(raw []byte) (*schemas.Decision, error) {
	return parseEdgePayload(decisionEdge, raw)
}

// ParsePatchEdge validates the patch payload received from the UNTRUSTED
// writer port against the registered `docsync.doc-patch/v1` wire schema
// before it can enter pendingPatches.
func ParsePatchEdge(raw []byte) (*schemas.DocPatch, error) {
	return parseEdgePayload(patchEdge, raw)
}

// ParseVerificationEdge validates the report received from the UNTRUSTED
// verifier port against the registered `docsync.verification-report/v1` wire
// schema before any use.
func ParseVerificationEdge(raw []byte) (*schemas.VerificationReport, error) {
	return parseEdgePayload(verificationEdge, raw)
}

// ParseDecisionValue re-validates a typed decision the untrusted decision
// port returned (defense in depth — the wire parser is still the authority).
func ParseDecisionValue(decision *schemas.Decision) (*schemas.Decision, error) {
	return revalidateStruct(decision, schemas.ParseDecision)
}

// ParsePatchValue re-validates a typed patch the untrusted writer port
// returned.
func ParsePatchValue(patch *schemas.DocPatch) (*schemas.DocPatch, error) {
	return revalidateStruct(patch, schemas.ParseDocPatch)
}

// ParseVerificationValue re-validates a typed report the untrusted verifier
// port returned.
func ParseVerificationValue(report *schemas.VerificationReport) (*schemas.VerificationReport, error) {
	return revalidateStruct(report, schemas.ParseVerificationReport)
}

/* ------------------------------------------- relevance-context edge (closed) */

// viaKindValues is the closed `via` provenance kind union of the DOCUMENT
// GRAPH → JEV edge (TS VIA_KIND_VALUES).
var viaKindValues = []string{
	string(ViaKindMappingSource),
	string(ViaKindConcept),
	string(ViaKindContract),
}

// relevanceContextEdgeWire is the strict shadow shape of RelevanceContext:
// caps.maxDocs decodes as json.Number so a non-integer bound yields the
// zod-equivalent issue path (`caps.maxDocs`) instead of a Go unmarshal type
// error, and an ABSENT caps object yields the zod "Required" issue.
type relevanceContextEdgeWire struct {
	Docs []struct {
		DocPath string `json:"docPath"`
		Mode    string `json:"mode"`
		Via     []struct {
			Kind   string `json:"kind"`
			Detail string `json:"detail"`
		} `json:"via"`
		Contracts []string `json:"contracts"`
	} `json:"docs"`
	Concepts []string `json:"concepts"`
	Caps     *struct {
		MaxDocs json.Number `json:"maxDocs"`
	} `json:"caps"`
}

// relevanceContextFromWire maps the wire shadow onto RelevanceContext and
// reports the closed-shape violations (zod issue paths preserved).
func relevanceContextFromWire(wire *relevanceContextEdgeWire) (*RelevanceContext, []schemas.Issue) {
	var issues []schemas.Issue
	context := &RelevanceContext{
		Docs:     make([]RelevanceDoc, 0, len(wire.Docs)),
		Concepts: []string{},
		Caps:     RelevanceCaps{},
	}
	if wire.Concepts != nil {
		context.Concepts = append(context.Concepts, wire.Concepts...)
	}
	for docIndex, doc := range wire.Docs {
		path := []string{"docs", itoa(docIndex)}
		if issue := schemas.RequiredString(doc.DocPath, 1, append(append([]string{}, path...), "docPath")); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := schemas.CheckEnum(doc.Mode, workspaceModeValueStrings(), append(append([]string{}, path...), "mode")); issue != nil {
			issues = append(issues, *issue)
		}
		mapped := RelevanceDoc{
			DocPath:   doc.DocPath,
			Mode:      workspace.WorkspaceMode(doc.Mode),
			Via:       make([]RelevanceVia, 0, len(doc.Via)),
			Contracts: append([]string{}, doc.Contracts...),
		}
		for viaIndex, via := range doc.Via {
			viaPath := append(append([]string{}, path...), "via", itoa(viaIndex))
			if issue := schemas.CheckEnum(via.Kind, viaKindValues, append(append([]string{}, viaPath...), "kind")); issue != nil {
				issues = append(issues, *issue)
			}
			mapped.Via = append(mapped.Via, RelevanceVia{Kind: RelevanceViaKind(via.Kind), Detail: via.Detail})
		}
		context.Docs = append(context.Docs, mapped)
	}
	if wire.Caps == nil {
		issues = append(issues, schemas.Issue{Path: []string{"caps"}, Message: "Required"})
		return context, issues
	}
	context.Caps = RelevanceCaps{}
	if wire.Caps.MaxDocs.String() == "" {
		issues = append(issues, schemas.Issue{Path: []string{"caps", "maxDocs"}, Message: "Required"})
		return context, issues
	}
	maxDocs, err := wire.Caps.MaxDocs.Int64()
	if err != nil {
		issues = append(issues, schemas.Issue{Path: []string{"caps", "maxDocs"}, Message: "Expected integer, received float"})
		return context, issues
	}
	if maxDocs < 0 {
		issues = append(issues, schemas.Issue{Path: []string{"caps", "maxDocs"}, Message: "Number must be greater than or equal to 0"})
		return context, issues
	}
	context.Caps.MaxDocs = int(maxDocs)
	return context, issues
}

// ParseRelevanceContextEdge validates a relevance context against the closed
// RelevanceContext shape — candidate docs with their modes, bounded `via`
// provenance trails (closed kind union), contract names, concept names, and
// the caps.maxDocs bound. Jev never sees unbounded repo content through this
// edge.
func ParseRelevanceContextEdge(raw []byte) (*RelevanceContext, error) {
	var wire relevanceContextEdgeWire
	if err := schemas.StrictDecode(raw, &wire); err != nil {
		return nil, newStageContractError(EdgeRelevanceContext, relevanceContextEdgeWireName, err)
	}
	context, issues := relevanceContextFromWire(&wire)
	if len(issues) > 0 {
		return nil, newStageContractError(
			EdgeRelevanceContext, relevanceContextEdgeWireName, schemas.NewValidationError(issues...))
	}
	return context, nil
}

// relevanceContextEdgeWireName is the core-internal edge wire name (TS: the
// `docsync.relevance-context (core-internal §3.3 DOCUMENT GRAPH → JEV edge)`
// wire string).
const relevanceContextEdgeWireName = "docsync.relevance-context (core-internal §3.3 DOCUMENT GRAPH → JEV edge)"

// workspaceModeValueStrings renders WORKSPACE_MODE_VALUES as []string for
// schemas.CheckEnum.
func workspaceModeValueStrings() []string {
	values := workspace.WorkspaceModeValues
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

// itoa is the small integer renderer shared with the schema issue paths.
func itoa(i int) string { return fmt.Sprintf("%d", i) }
