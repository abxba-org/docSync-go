// Ported from packages/policy/src/shadow/matrix.ts (P12-T04; ARCH §8.6; CTX §21; D6):
// the decision-level comparison of candidate-vs-production decisions over a
// shadow split — "Comparison is decision-level: an agreement/disagreement
// matrix (both-ignore, both-review, v17-review/v18-ignore, …), with every
// disagreement resolvable into a label via reviewer outcome" (ARCH §8.6).
//
// Vocabulary (closed, additive-safe): the decision values are the
// docsync.decision/v1 ACTION union (schemas.ActionValues, OD-2 — import, never
// duplicate); an unknown decision value fails closed with a typed
// ShadowMatrixVocabularyError. The matrix artifact is a data row set (not a
// fixed-width table), so a future decision vocabulary extension is
// additive-safe for v1 readers.
//
// Identical-input lineage (V-P12-2): every pair carries the recorded input
// hash, persisted into the matrix record's input_hashes with Equal: true —
// the matrix record can only exist when both policies received identical
// inputs.
//
// Pure, deterministic, offline: sorted deterministic cell order, no clocks
// (computed_at is injected), no I/O. The computed matrix is self-checked
// against the registered artifact schema (schemas.ParseShadowAgreementMatrix)
// before return — fail closed on internal drift.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------ typed errors */

// jsonStringify renders value exactly like the TS JSON.stringify used in the
// ported error messages (strings quoted, no HTML escaping).
func jsonStringify(value string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// shadowActionNames lists the closed docsync.decision/v1 action vocabulary in
// canonical order (ACTION_VALUES.join(' | ') parity).
func shadowActionNames() []string {
	names := make([]string, len(schemas.ActionValues))
	for i, action := range schemas.ActionValues {
		names[i] = string(action)
	}
	return names
}

// ShadowMatrixVocabularyError is the fail-closed vocabulary violation: an
// unknown decision value in a pair (TS ShadowMatrixVocabularyError).
type ShadowMatrixVocabularyError struct {
	CaseID string
	Field  string // "candidateAction" | "productionAction"
	Value  string

	inner *docutil.Error
}

func newShadowMatrixVocabularyError(caseID, field, value string) *ShadowMatrixVocabularyError {
	msg := fmt.Sprintf(
		"shadow matrix vocabulary violation on case %s: %s %s is not a docsync.decision/v1 action (%s) — fail closed",
		caseID, field, jsonStringify(value), strings.Join(shadowActionNames(), " | "))
	return &ShadowMatrixVocabularyError{CaseID: caseID, Field: field, Value: value, inner: docutil.New(docutil.CodeValidation, msg)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowMatrixVocabularyError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowMatrixVocabularyError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowMatrixVocabularyError) Unwrap() error { return e.inner }

// ShadowMatrixShapeError is the fail-closed shape violation: a duplicate case
// or a case in several cells (TS ShadowMatrixShapeError).
type ShadowMatrixShapeError struct {
	Message string

	inner *docutil.Error
}

func newShadowMatrixShapeError(message string) *ShadowMatrixShapeError {
	return &ShadowMatrixShapeError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowMatrixShapeError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowMatrixShapeError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowMatrixShapeError) Unwrap() error { return e.inner }

// ShadowReviewerOutcomeError is the fail-closed reviewer-outcome violation:
// an unknown resolution or a stray case (TS ShadowReviewerOutcomeError).
type ShadowReviewerOutcomeError struct {
	Message string

	inner *docutil.Error
}

func newShadowReviewerOutcomeError(message string) *ShadowReviewerOutcomeError {
	return &ShadowReviewerOutcomeError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowReviewerOutcomeError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowReviewerOutcomeError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowReviewerOutcomeError) Unwrap() error { return e.inner }

/* -------------------------------------------------------------------- types */

// ShadowDecisionPair is one candidate-vs-production decision pair over an
// identical-input case (TS ShadowDecisionPair).
type ShadowDecisionPair struct {
	CaseID           string
	ProductionAction schemas.Action
	CandidateAction  schemas.Action
	// InputHash is the recorded input hash — both policies observed identical
	// inputs (V-P12-2).
	InputHash string
}

// ShadowReviewerOutcome is a reviewer outcome resolving one disagreement
// (TS ShadowReviewerOutcome; ARCH §8.7 outcome lineage). Resolution is the
// closed ShadowDisagreementResolution vocabulary minus 'pending'.
type ShadowReviewerOutcome struct {
	CaseID     string
	Resolution schemas.ShadowDisagreementResolution
}

// ComputeAgreementMatrixInput is the computeAgreementMatrix input
// (TS ComputeAgreementMatrixInput).
type ComputeAgreementMatrixInput struct {
	Pairs                   []ShadowDecisionPair
	ProductionPolicyVersion int
	CandidatePolicyVersion  int
	SplitID                 string
	// Now is the injected instant (ISO-8601) stamped as computed_at — never
	// generated.
	Now string
}

/* ---------------------------------------------------------------- functions */

// parseShadowAction parses one decision value against the closed vocabulary,
// failing closed with ShadowMatrixVocabularyError (TS parseAction).
func parseShadowAction(caseID, field, value string) (schemas.Action, error) {
	if !schemas.IsValidAction(value) {
		return "", newShadowMatrixVocabularyError(caseID, field, value)
	}
	return schemas.Action(value), nil
}

// parseRegisteredShadowMatrix is the Go form of SHADOW_AGREEMENT_MATRIX_SCHEMA.parse:
// strict wire decode + validation (unknown JSON keys rejected, mirroring
// z.strictObject), so the self-check fails closed on internal drift.
func parseRegisteredShadowMatrix(matrix schemas.ShadowAgreementMatrix) (*schemas.ShadowAgreementMatrix, error) {
	wire, err := json.Marshal(matrix)
	if err != nil {
		return nil, docutil.New(docutil.CodeInternal,
			fmt.Sprintf("shadow matrix: canonical wire marshal failed: %s", err))
	}
	return schemas.ParseShadowAgreementMatrix(wire)
}

// ComputeAgreementMatrix computes the agreement/disagreement matrix (P12-T04).
// Cells group pairs by (candidate, production) decision; disagreeing cases
// additionally carry one disagreement row with resolution 'pending'. The
// result is validated against docsync.shadow-agreement-matrix/v1 before
// return (fail closed on internal drift) and every pair's input hash is
// persisted into input_hashes with Equal: true (V-P12-2).
func ComputeAgreementMatrix(input ComputeAgreementMatrixInput) (schemas.ShadowAgreementMatrix, error) {
	type cellEntry struct {
		candidate  schemas.Action
		production schemas.Action
		caseIDs    []string
	}
	var cellOrder []string
	cellCases := make(map[string]*cellEntry)
	inputHashes := make(map[string]schemas.ShadowCaseInputHashes, len(input.Pairs))
	seenCases := make(map[string]struct{}, len(input.Pairs))
	disagreements := make([]schemas.ShadowMatrixDisagreement, 0, len(input.Pairs))

	for _, pair := range input.Pairs {
		if _, dup := seenCases[pair.CaseID]; dup {
			return schemas.ShadowAgreementMatrix{}, newShadowMatrixShapeError(fmt.Sprintf(
				"computeAgreementMatrix: duplicate pair for case %s — every case contributes exactly one decision pair",
				pair.CaseID))
		}
		seenCases[pair.CaseID] = struct{}{}
		candidate, err := parseShadowAction(pair.CaseID, "candidateAction", string(pair.CandidateAction))
		if err != nil {
			return schemas.ShadowAgreementMatrix{}, err
		}
		production, err := parseShadowAction(pair.CaseID, "productionAction", string(pair.ProductionAction))
		if err != nil {
			return schemas.ShadowAgreementMatrix{}, err
		}
		key := string(candidate) + "\x00" + string(production)
		entry, ok := cellCases[key]
		if !ok {
			cellCases[key] = &cellEntry{
				candidate:  candidate,
				production: production,
				caseIDs:    []string{pair.CaseID},
			}
			cellOrder = append(cellOrder, key)
		} else {
			entry.caseIDs = append(entry.caseIDs, pair.CaseID)
		}
		inputHashes[pair.CaseID] = schemas.ShadowCaseInputHashes{
			Production: pair.InputHash,
			Candidate:  pair.InputHash,
			Equal:      true,
		}
		if candidate != production {
			disagreements = append(disagreements, schemas.ShadowMatrixDisagreement{
				CaseID:             pair.CaseID,
				CandidateDecision:  candidate,
				ProductionDecision: production,
				Resolution:         schemas.ShadowResolutionPending,
			})
		}
	}

	cells := make([]schemas.ShadowMatrixCell, 0, len(cellOrder))
	for _, key := range cellOrder {
		entry := cellCases[key]
		caseIDs := append([]string(nil), entry.caseIDs...)
		sort.Strings(caseIDs)
		cells = append(cells, schemas.ShadowMatrixCell{
			CandidateDecision:  entry.candidate,
			ProductionDecision: entry.production,
			Count:              len(caseIDs),
			CaseIDs:            caseIDs,
		})
	}
	// Deterministic cell order: (candidate, production) decision pair, then
	// byte-wise stable compare (TS stableCompare).
	sort.SliceStable(cells, func(i, j int) bool {
		if cells[i].CandidateDecision != cells[j].CandidateDecision {
			return cells[i].CandidateDecision < cells[j].CandidateDecision
		}
		return cells[i].ProductionDecision < cells[j].ProductionDecision
	})
	sort.SliceStable(disagreements, func(i, j int) bool {
		return disagreements[i].CaseID < disagreements[j].CaseID
	})

	agreementCount := 0
	for _, cell := range cells {
		if cell.CandidateDecision == cell.ProductionDecision {
			agreementCount += cell.Count
		}
	}

	matrix := schemas.ShadowAgreementMatrix{
		Schema:                  schemas.ShadowAgreementMatrixSchemaID,
		ProductionPolicyVersion: input.ProductionPolicyVersion,
		CandidatePolicyVersion:  input.CandidatePolicyVersion,
		SplitID:                 input.SplitID,
		Cells:                   cells,
		AgreementCount:          agreementCount,
		DisagreementCount:       len(disagreements),
		InputHashes:             inputHashes,
		Disagreements:           disagreements,
		ComputedAt:              input.Now,
	}
	// Self-check against the registered artifact schema (fail closed on drift).
	parsed, err := parseRegisteredShadowMatrix(matrix)
	if err != nil {
		return schemas.ShadowAgreementMatrix{}, err
	}
	return *parsed, nil
}

// ResolveDisagreementLabels resolves disagreement labels via reviewer outcomes
// (ARCH §8.6). Every provided outcome must name a disagreement case of the
// matrix and carry a resolution from the closed vocabulary; unresolved
// disagreements KEEP 'pending'. Pure: returns a new matrix record, the input
// is untouched.
func ResolveDisagreementLabels(matrix schemas.ShadowAgreementMatrix, outcomes []ShadowReviewerOutcome) (schemas.ShadowAgreementMatrix, error) {
	disagreementCases := make(map[string]struct{}, len(matrix.Disagreements))
	for _, row := range matrix.Disagreements {
		disagreementCases[row.CaseID] = struct{}{}
	}
	resolved := make(map[string]schemas.ShadowDisagreementResolution, len(outcomes))
	seenOutcomes := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		if _, dup := seenOutcomes[outcome.CaseID]; dup {
			return schemas.ShadowAgreementMatrix{}, newShadowReviewerOutcomeError(fmt.Sprintf(
				"duplicate reviewer outcome for case %s — one outcome per disagreement case", outcome.CaseID))
		}
		seenOutcomes[outcome.CaseID] = struct{}{}
		if _, ok := disagreementCases[outcome.CaseID]; !ok {
			return schemas.ShadowAgreementMatrix{}, newShadowReviewerOutcomeError(fmt.Sprintf(
				"reviewer outcome names case %s, which is not a disagreement of this matrix — fail closed",
				outcome.CaseID))
		}
		switch outcome.Resolution {
		case schemas.ShadowResolutionCandidateCorrect,
			schemas.ShadowResolutionProductionCorrect,
			schemas.ShadowResolutionBothWrong:
		default:
			return schemas.ShadowAgreementMatrix{}, newShadowReviewerOutcomeError(fmt.Sprintf(
				"reviewer outcome resolution %s for case %s is outside the closed resolution vocabulary — fail closed",
				jsonStringify(string(outcome.Resolution)), outcome.CaseID))
		}
		resolved[outcome.CaseID] = outcome.Resolution
	}

	next := matrix
	rows := make([]schemas.ShadowMatrixDisagreement, len(matrix.Disagreements))
	for i, row := range matrix.Disagreements {
		if resolution, ok := resolved[row.CaseID]; ok {
			row.Resolution = resolution
		}
		rows[i] = row
	}
	next.Disagreements = rows
	// Self-check against the registered artifact schema (fail closed on drift).
	parsed, err := parseRegisteredShadowMatrix(next)
	if err != nil {
		return schemas.ShadowAgreementMatrix{}, err
	}
	return *parsed, nil
}
