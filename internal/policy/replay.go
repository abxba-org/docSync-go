// Ported from packages/policy/src/shadow/replay.ts (P12-T01; ARCH §8.6; D6; CTX §21):
// the offline historical-replay lane — the PRIMARY shadow lane per D6 — the
// CANDIDATE policy and the PRODUCTION policy are run over a recorded dataset
// split (the Phase-10 frozen/rolling split manifest, consumed as DATA because
// policy must not import core, OD-1) through INJECTED replay ports (recorded
// doubles; zero network, zero LLM, no clocks — deterministic and offline by
// construction). Dual-Jev on every PR is explicitly rejected (D6).
//
// Identical-input guarantee (V-P12-2; ARCH §8.6 "Shadow runs see identical
// inputs as production so differences are attributable to the policy"): the
// engine canonicalizes each case's inputs (ChangeAnalysis + candidate set +
// diff-subset hash) into ONE sha256 input hash, hands the SAME inputs to both
// replay ports, and asserts each port's RECORDED observed-input hash equals
// the engine hash — a tampered or diverging input path raises the typed
// ShadowInputMismatchError. The per-case input hashes are recorded into the
// agreement matrix (V-P12-2).
//
// Memoization separation (ARCH §7.1 item 4, D6): replay decisions are keyed by
// policy version (production vs candidate ports are per-version recorded
// doubles); shadow results never enter the production memo namespace.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------ typed errors */

// ShadowInputMismatchError is the fail-closed V-P12-2 violation: a replay
// port observed inputs differing from the canonical case hash
// (TS ShadowInputMismatchError).
type ShadowInputMismatchError struct {
	CaseID   string
	Policy   string // "production" | "candidate"
	Expected string
	Observed string

	inner *docutil.Error
}

func newShadowInputMismatchError(caseID, policy, expected, observed string) *ShadowInputMismatchError {
	msg := fmt.Sprintf(
		"shadow input mismatch (V-P12-2): case %s, %s policy observed input hash %s but the canonical case input hash is %s — "+
			"candidate and production must receive identical ChangeAnalysis + candidate sets (ARCH §8.6; D6)",
		caseID, policy, observed, expected)
	return &ShadowInputMismatchError{CaseID: caseID, Policy: policy, Expected: expected, Observed: observed, inner: docutil.New(docutil.CodeValidation, msg)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowInputMismatchError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowInputMismatchError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowInputMismatchError) Unwrap() error { return e.inner }

// ShadowReplayCoverageError is the fail-closed coverage violation: the replay
// input set cannot cover the requested split (TS ShadowReplayCoverageError).
type ShadowReplayCoverageError struct {
	Message string

	inner *docutil.Error
}

func newShadowReplayCoverageError(message string) *ShadowReplayCoverageError {
	return &ShadowReplayCoverageError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowReplayCoverageError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowReplayCoverageError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowReplayCoverageError) Unwrap() error { return e.inner }

// ShadowReplayInputError is the fail-closed violation for a malformed
// recorded replay input (TS ShadowReplayInputError).
type ShadowReplayInputError struct {
	Message string

	inner *docutil.Error
}

func newShadowReplayInputError(message string) *ShadowReplayInputError {
	return &ShadowReplayInputError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowReplayInputError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowReplayInputError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowReplayInputError) Unwrap() error { return e.inner }

/* -------------------------------------------------------------------- types */

// ShadowReplayInputs are the recorded replay inputs of one case — identical
// bytes for both policies (TS ShadowReplayInputs).
type ShadowReplayInputs struct {
	ChangeAnalysis schemas.ChangeAnalysis
	// CandidateSet is the candidate-document set the decision saw (sorted for
	// the hash).
	CandidateSet []string
	// DiffSubsetHash is the diff-subset slice hash captured at recording time
	// (ARCH §7.1).
	DiffSubsetHash string
}

// ShadowReplayCase is one replay case: its id and its recorded inputs
// (TS ShadowReplayCase).
type ShadowReplayCase struct {
	CaseID string
	Inputs ShadowReplayInputs
}

// ShadowReplayPortResult is what a replay port observed: the decision action
// plus the hash of what it received (TS ShadowReplayPortResult).
type ShadowReplayPortResult struct {
	Action schemas.Action
	// ObservedInputHash is the sha256 of the inputs the port ACTUALLY
	// received (tamper-evidence).
	ObservedInputHash string
}

// ShadowReplayPort is the injected replay port — a recorded double in
// production, a deterministic double in tests (TS ShadowReplayPort).
type ShadowReplayPort func(caseInput ShadowReplayCase) ShadowReplayPortResult

// ShadowReplaySplit is the Phase-10 split manifest shape, consumed
// structurally as data (OD-1; TS ShadowReplaySplit).
type ShadowReplaySplit struct {
	SplitID string
	CaseIDs []string
}

// ShadowReplayInput is the runShadowReplay input (TS ShadowReplayInput).
type ShadowReplayInput struct {
	ProductionPolicyVersion int
	CandidatePolicyVersion  int
	Split                   ShadowReplaySplit
	// Cases is the recorded case set (a superset of the split is allowed).
	Cases            []ShadowReplayCase
	ProductionReplay ShadowReplayPort
	CandidateReplay  ShadowReplayPort
	// Now is the injected instant (ISO-8601) stamped as the matrix computed_at.
	Now string
}

// ShadowReplayPair is one replayed decision pair — a ShadowDecisionPair with
// tamper-evidence hashes (TS ShadowReplayPair).
type ShadowReplayPair struct {
	CaseID                      string
	ProductionAction            schemas.Action
	CandidateAction             schemas.Action
	InputHash                   string
	ObservedProductionInputHash string
	ObservedCandidateInputHash  string
}

// decisionPair projects the pair onto the matrix vocabulary
// (TS: ShadowReplayPair extends ShadowDecisionPair).
func (p ShadowReplayPair) decisionPair() ShadowDecisionPair {
	return ShadowDecisionPair{
		CaseID:           p.CaseID,
		ProductionAction: p.ProductionAction,
		CandidateAction:  p.CandidateAction,
		InputHash:        p.InputHash,
	}
}

// ShadowReplayResult is the runShadowReplay outcome (TS ShadowReplayResult).
type ShadowReplayResult struct {
	SplitID                 string
	ProductionPolicyVersion int
	CandidatePolicyVersion  int
	// Pairs are the replayed decision pairs in split order (sorted case ids) —
	// deterministic.
	Pairs      []ShadowReplayPair
	Matrix     schemas.ShadowAgreementMatrix
	ComputedAt string
}

/* ---------------------------------------------------------------- functions */

// shadowISOInstantRe mirrors z.string().datetime({ offset: false }) (the TS
// ISO_INSTANT): a canonical ISO-8601 instant WITHOUT an offset ('Z' only),
// with optional fractional seconds.
var shadowISOInstantRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// assertShadowISOInstant enforces the canonical ISO-8601 instant shape
// (TS assertIsoInstant). The regex is the zod parity check; time.Parse adds
// real-calendar validation (fail closed on impossible dates).
func assertShadowISOInstant(value, label string) error {
	invalid := newShadowReplayInputError(fmt.Sprintf(
		"%s must be a canonical ISO-8601 instant (got %s)", label, value))
	if !shadowISOInstantRe.MatchString(value) {
		return invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return invalid
	}
	return nil
}

// shadowCanonicalJSON renders value as canonical JSON (the local TS
// canonicalJson of the policy compiler: keys sorted, arrays in order, no
// undefined values) via docutil.CanonicalJSON — never ad-hoc marshaling.
func shadowCanonicalJSON(value any) (string, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(generic)
}

// ShadowReplayInputHash is the canonical input hash of one case's replay
// inputs (V-P12-2): sha256 over the canonical JSON of the ChangeAnalysis, the
// SORTED candidate set and the diff-subset hash — byte-stable against key
// order and candidate order (TS shadowReplayInputHash).
func ShadowReplayInputHash(inputs ShadowReplayInputs) (string, error) {
	wire, err := json.Marshal(inputs.ChangeAnalysis)
	if err == nil {
		_, err = schemas.ParseChangeAnalysis(wire)
	}
	if err != nil {
		return "", newShadowReplayInputError(fmt.Sprintf(
			"shadowReplayInputHash: changeAnalysis is not a valid docsync.change-analysis/v1 record: %s", err))
	}
	for _, candidate := range inputs.CandidateSet {
		// TS also refused non-string entries; a Go []string cannot hold any.
		if len(candidate) == 0 {
			return "", newShadowReplayInputError(fmt.Sprintf(
				"shadowReplayInputHash: candidateSet entries must be non-empty strings (got %s)", candidate))
		}
	}
	if !schemas.IsShadowInputHash(inputs.DiffSubsetHash) {
		return "", newShadowReplayInputError(fmt.Sprintf(
			"shadowReplayInputHash: diffSubsetHash must be a sha256 hex digest (got %s)", inputs.DiffSubsetHash))
	}
	sorted := append([]string(nil), inputs.CandidateSet...)
	sort.Strings(sorted)
	canonical, err := shadowCanonicalJSON(map[string]any{
		"changeAnalysis": inputs.ChangeAnalysis,
		"candidateSet":   sorted,
		"diffSubsetHash": inputs.DiffSubsetHash,
	})
	if err != nil {
		return "", newShadowReplayInputError(fmt.Sprintf(
			"shadowReplayInputHash: canonical serialization failed: %s", err))
	}
	return docutil.ContentHash(canonical), nil
}

// RunShadowReplay runs the offline shadow replay (P12-T01). Deterministic and
// offline: the only inputs are recorded data and injected ports; the same
// fixture produces byte-identical results across runs. Every split case must
// have recorded inputs (fail closed, ShadowReplayCoverageError); both ports
// must observe the canonical input hash for every case (fail closed,
// ShadowInputMismatchError — V-P12-2).
func RunShadowReplay(input ShadowReplayInput) (ShadowReplayResult, error) {
	if err := assertShadowISOInstant(input.Now, "now"); err != nil {
		return ShadowReplayResult{}, err
	}
	if input.ProductionPolicyVersion == input.CandidatePolicyVersion {
		return ShadowReplayResult{}, newShadowReplayInputError(
			"runShadowReplay: production and candidate policy versions must differ — a shadow run compares two policies")
	}
	caseByID := make(map[string]ShadowReplayCase, len(input.Cases))
	for _, replayCase := range input.Cases {
		if _, dup := caseByID[replayCase.CaseID]; dup {
			return ShadowReplayResult{}, newShadowReplayCoverageError(
				"runShadowReplay: duplicate caseId in the recorded case set")
		}
		caseByID[replayCase.CaseID] = replayCase
	}
	splitCaseIDs := append([]string(nil), input.Split.CaseIDs...)
	sort.Strings(splitCaseIDs)
	var missing []string
	for _, caseID := range splitCaseIDs {
		if _, ok := caseByID[caseID]; !ok {
			missing = append(missing, caseID)
		}
	}
	if len(missing) > 0 {
		return ShadowReplayResult{}, newShadowReplayCoverageError(fmt.Sprintf(
			"runShadowReplay: split %s references cases without recorded inputs: %s",
			input.Split.SplitID, strings.Join(missing, ", ")))
	}

	pairs := make([]ShadowReplayPair, 0, len(splitCaseIDs))
	for _, caseID := range splitCaseIDs {
		replayCase, ok := caseByID[caseID]
		if !ok {
			return ShadowReplayResult{}, newShadowReplayCoverageError(fmt.Sprintf(
				"runShadowReplay: split case %s has no recorded inputs", caseID))
		}
		inputHash, err := ShadowReplayInputHash(replayCase.Inputs)
		if err != nil {
			return ShadowReplayResult{}, err
		}
		production := input.ProductionReplay(replayCase)
		candidate := input.CandidateReplay(replayCase)
		if production.ObservedInputHash != inputHash {
			return ShadowReplayResult{}, newShadowInputMismatchError(
				caseID, "production", inputHash, production.ObservedInputHash)
		}
		if candidate.ObservedInputHash != inputHash {
			return ShadowReplayResult{}, newShadowInputMismatchError(
				caseID, "candidate", inputHash, candidate.ObservedInputHash)
		}
		if _, err := parseShadowAction(caseID, "productionAction", string(production.Action)); err != nil {
			return ShadowReplayResult{}, err
		}
		if _, err := parseShadowAction(caseID, "candidateAction", string(candidate.Action)); err != nil {
			return ShadowReplayResult{}, err
		}
		pairs = append(pairs, ShadowReplayPair{
			CaseID:                      caseID,
			ProductionAction:            production.Action,
			CandidateAction:             candidate.Action,
			InputHash:                   inputHash,
			ObservedProductionInputHash: production.ObservedInputHash,
			ObservedCandidateInputHash:  candidate.ObservedInputHash,
		})
	}

	decisionPairs := make([]ShadowDecisionPair, len(pairs))
	for i, pair := range pairs {
		decisionPairs[i] = pair.decisionPair()
	}
	matrix, err := ComputeAgreementMatrix(ComputeAgreementMatrixInput{
		Pairs:                   decisionPairs,
		ProductionPolicyVersion: input.ProductionPolicyVersion,
		CandidatePolicyVersion:  input.CandidatePolicyVersion,
		SplitID:                 input.Split.SplitID,
		Now:                     input.Now,
	})
	if err != nil {
		return ShadowReplayResult{}, err
	}
	return ShadowReplayResult{
		SplitID:                 input.Split.SplitID,
		ProductionPolicyVersion: input.ProductionPolicyVersion,
		CandidatePolicyVersion:  input.CandidatePolicyVersion,
		Pairs:                   pairs,
		Matrix:                  matrix,
		ComputedAt:              input.Now,
	}, nil
}
