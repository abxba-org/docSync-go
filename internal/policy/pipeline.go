// Ported from packages/policy/src/promotion/pipeline.ts (P10-T18; P12-T05/
// T06/T07; ARCH §8.4/§8.5/§8.6/§8.9, CTX §24, INV-7): propose → offline
// evaluation (gate library) → human approval → promoted. THE GATES CANNOT BE
// BYPASSED: the pipeline re-runs the promotion gates itself and refuses with
// the failing gate NAMED before any approval is accepted (V-P10-13, F10-16
// bypass-proofing foundation). INV-9 fail-closed end to end.
//
// `learning.mode=auto` (P12-T07, {@link PromotionPipeline.AutoPromote}) is an
// ESCALATION OF TRUST within this ONE pipeline — never a separate fast path
// (PLAN §13.6): the auto path runs the same gate-checked propose, records the
// promotion with the application-owned actor attribution, and promotes —
// still gated, still evidence-logged, still one-op rollback (ARCH §8.9).
// BYPASS-VETO (ARCH §8.5): any path where model output becomes live policy
// without crossing EVALUATION is vetoed — the gate checklist exists ONLY as
// the product of an in-pipeline gate run, config cannot force the auto path,
// and the store cross-checks a presented checklist against its own rows
// (V-P12-9).
//
// Error convention (CONVENTIONS): the two TS error classes become typed Go
// errors carrying their verbatim messages, wrapped in docutil coded errors
// (EDOCSYNC_GUARD — the gate/bypass refusals are guard failures; callers match
// the types with errors.As).
package policy

import (
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AutoPromotionActor is the application-owned actor recorded as the approval
// attribution when the `learning.mode=auto` path promotes (P12-T07): never a
// model identity, never caller-supplied — INV-7 prohibits model self-approval,
// and ARCH §8.9 makes the promotion act application-owned even under auto
// (AUTO_PROMOTION_ACTOR).
const AutoPromotionActor = "app:docsync/promotion-pipeline@v1 (learning.mode=auto)"

// The bypass veto discriminants (TS PromotionBypassVetoError['veto']).
const (
	// PromotionVetoLearningModeNotAuto — a learning.mode other than `auto`
	// demanded automated promotion.
	PromotionVetoLearningModeNotAuto = "learning_mode_not_auto"
	// PromotionVetoFailingGate — no gate-passed proposal exists to promote.
	PromotionVetoFailingGate = "failing_gate"
)

// PromotionGateFailureError ports the TS class: the gate result is carried so
// callers can read the failing gates and the full checklist.
type PromotionGateFailureError struct {
	// FailedGates names every failing gate (the failing gate is NAMED,
	// V-P10-13).
	FailedGates []string
	// Result is the full gate checklist verdict.
	Result PromotionGateResult
	// coded carries the verbatim message and the docutil code.
	coded *docutil.Error
}

// Error renders `promotion rejected — failing gates: …` (the TS message).
func (e *PromotionGateFailureError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error, so errors.As / docutil.CodeOf keep working
// through the wrapper.
func (e *PromotionGateFailureError) Unwrap() error { return e.coded }

// newPromotionGateFailureError builds the error from a gate result.
func newPromotionGateFailureError(result PromotionGateResult) *PromotionGateFailureError {
	return &PromotionGateFailureError{
		FailedGates: result.FailedGates,
		Result:      result,
		coded: docutil.Newf(docutil.CodeGuard, "promotion rejected — failing gates: %s",
			strings.Join(result.FailedGates, ", ")),
	}
}

// PromotionBypassVetoError is raised when a caller attempts to route model
// output into live policy without crossing EVALUATION (BYPASS-VETO, P12-T07;
// ARCH §8.5; V-P12-9) — e.g. a learning.mode other than `auto` demanding
// automated promotion, or no gate-passed proposal to promote.
type PromotionBypassVetoError struct {
	// Veto is the typed discriminant: 'learning_mode_not_auto' |
	// 'failing_gate'.
	Veto string
	// coded carries the verbatim message and the docutil code.
	coded *docutil.Error
}

// Error renders the coded veto message.
func (e *PromotionBypassVetoError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error, so errors.As / docutil.CodeOf keep working
// through the wrapper.
func (e *PromotionBypassVetoError) Unwrap() error { return e.coded }

// newPromotionBypassVetoError builds the veto with a verbatim message.
func newPromotionBypassVetoError(veto, message string) *PromotionBypassVetoError {
	return &PromotionBypassVetoError{Veto: veto, coded: docutil.New(docutil.CodeGuard, message)}
}

// PromotionPipelineOptions configures a PromotionPipeline
// (PromotionPipelineOptions extends PromotionStoreOptions).
type PromotionPipelineOptions struct {
	// HistoryDir is the `.docsync/policy-history` directory
	// (POLICY_HISTORY_SUBDIR).
	HistoryDir string
	// Clock supplies the `at` instant of every appended row.
	Clock func() string
	// NewID supplies promotion ids (injected — never generated behind the
	// caller's back).
	NewID func() string
	// MinComparisonSize raises the gate library's min-comparison floor
	// (upward-only, OD-R24); nil = library default.
	MinComparisonSize *int
	// Alpha is the two-proportion significance level; nil = OD-R24 default
	// (0.05).
	Alpha *float64
}

// PromotionProposalInput is the propose() input (the pipeline half): the
// promotion identity and reason, the embedded evaluation evidence, the two
// evaluation reports, the labeled-disagreement win counts and the optional
// per-bucket comparisons (P12-T06) and binding-calibration evidence (P12-T09).
// Nil pointers/slices mean the TS optional/absent input.
type PromotionProposalInput struct {
	PolicyVersion   int
	PreviousVersion *int
	Reason          string
	Evaluation      PromotionEvaluationEvidence
	BaselineReport  schemas.EvaluationReport
	CandidateReport schemas.EvaluationReport
	CandidateWins   int
	CandidateTotal  int
	BaselineWins    int
	BaselineTotal   int
	// RepoMinComparisonSize is the per-repo comparison floor — it clamps
	// UPWARD ONLY and can never lower or disable the library floor (OD-R24).
	RepoMinComparisonSize *int
	// Buckets are the per-decision-bucket comparisons (P12-T06).
	Buckets []PromotionBucketComparison
	// BindingCalibration is the binding-calibration evidence (P12-T09).
	// Additive: absent (nil) ⇒ the `binding_calibration` gate is not
	// evaluated (byte-compat with existing checklists); present (even empty)
	// ⇒ one `binding_calibration:<key>` row per binding result, empty fails
	// closed.
	BindingCalibration []CalibrationResult
}

// AutoPromotionInput is the autoPromote() input (PromotionPipeline.autoPromote
// — the propose input plus the learning mode that authorized the auto path).
type AutoPromotionInput struct {
	LearningMode LearningMode
	PromotionProposalInput
}

// PromotionPipeline is the ONE promotion pipeline (TS PromotionPipeline).
type PromotionPipeline struct {
	store *PromotionStore
	gates *PromotionGateLibrary
	// lastGateResult is the gate checklist captured at the last successful
	// propose — promote re-presents it to the store, which re-enforces it
	// structurally. If propose never ran, promote fails closed (passed=false).
	lastGateResult *PromotionGateResult
	// lastProposedID is the promotion id created by the last successful
	// propose — the only id the auto path may promote (there is no way to
	// promote any other row).
	lastProposedID string
}

// NewPromotionPipeline builds the pipeline over a promotion store and the
// promotion gate library (TS PromotionPipeline constructor).
func NewPromotionPipeline(options PromotionPipelineOptions) (*PromotionPipeline, error) {
	store, err := NewPromotionStore(PromotionStoreOptions{
		HistoryDir: options.HistoryDir,
		Clock:      options.Clock,
		NewID:      options.NewID,
	})
	if err != nil {
		return nil, err
	}
	gates, err := NewPromotionGateLibrary(PromotionGateLibraryOptions{
		MinComparisonSize: options.MinComparisonSize,
		Alpha:             options.Alpha,
	})
	if err != nil {
		return nil, err
	}
	return &PromotionPipeline{store: store, gates: gates}, nil
}

// gateChecklistFrom converts a gate result into the checklist the store
// re-presents at promote time (the TS code passed the PromotionGateResult
// itself; Go is nominal, so the row shape is mapped explicitly).
func gateChecklistFrom(result PromotionGateResult) PromotionGateChecklist {
	rows := make([]PromotionGateChecklistRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		rows = append(rows, PromotionGateChecklistRow{Gate: row.Gate, Passed: row.Passed})
	}
	return PromotionGateChecklist{
		Passed:      result.Passed,
		FailedGates: result.FailedGates,
		Rows:        rows,
	}
}

// Propose runs the gates FIRST (TS PromotionPipeline.propose): a failing gate
// rejects with the named gates BEFORE any approval or record beyond the
// proposal is accepted (fail closed). On success the checklist is captured and
// the returned gate result carries the full evidence.
func (p *PromotionPipeline) Propose(input PromotionProposalInput) (PromotionGateResult, error) {
	gateResult, err := p.gates.CheckGates(PromotionGateInputs{
		Baseline:                   input.BaselineReport,
		Candidate:                  input.CandidateReport,
		CandidateUnsafeAutoUpdates: input.Evaluation.UnsafeAutoUpdates,
		CandidateWins:              input.CandidateWins,
		CandidateTotal:             input.CandidateTotal,
		BaselineWins:               input.BaselineWins,
		BaselineTotal:              input.BaselineTotal,
		RepoMinComparisonSize:      input.RepoMinComparisonSize,
		Buckets:                    input.Buckets,
		BindingCalibration:         input.BindingCalibration,
	})
	if err != nil {
		return PromotionGateResult{}, err
	}
	if !gateResult.Passed {
		return PromotionGateResult{}, newPromotionGateFailureError(gateResult)
	}
	row, err := p.store.Propose(PromotionProposal{
		PolicyVersion:   input.PolicyVersion,
		PreviousVersion: input.PreviousVersion,
		Reason:          input.Reason,
		Evaluation:      input.Evaluation,
	})
	if err != nil {
		return PromotionGateResult{}, err
	}
	result := gateResult
	p.lastGateResult = &result
	p.lastProposedID = row.PromotionID
	return gateResult, nil
}

// Approve records the human approval (TS PromotionPipeline.approve) — INV-7:
// models propose, humans promote.
func (p *PromotionPipeline) Approve(promotionID, approvedBy string) (*schemas.PromotionRecord, error) {
	return p.store.Approve(promotionID, approvedBy)
}

// Promote promotes an approved row (TS PromotionPipeline.promote): the
// checklist captured at the last gate-passed propose is re-presented to the
// store, which re-enforces it structurally. Without a captured result the
// promotion fails closed (passed=false).
func (p *PromotionPipeline) Promote(promotionID string) (*schemas.PromotionRecord, error) {
	checklist := PromotionGateChecklist{Passed: false}
	if p.lastGateResult != nil {
		checklist = gateChecklistFrom(*p.lastGateResult)
	}
	return p.store.Promote(promotionID, checklist)
}

// AutoPromote is the `learning.mode=auto` promotion path (P12-T07; ARCH §8.9,
// CTX §24): when EVERY gate passes, promotion occurs AUTOMATICALLY —
// application-owned, versioned, logged with full evaluation evidence, one-op
// rollback. It reuses the ONE promotion pipeline (no separate fast path): the
// same gate-checked propose, the same store transitions, the same event log.
//
// Veto structure (V-P12-9; ARCH §8.5):
//   - `LearningMode` must literally be `auto` — config cannot force the auto
//     path for a `propose`/`canary` repo (PromotionBypassVetoError).
//   - The gates re-run in {@link Propose} unconditionally; any failing gate
//     throws PromotionGateFailureError BEFORE a proposal row exists — no
//     evaluation, no promotion, nothing logged.
//   - The approval attribution is the module-constant application actor
//     ({@link AutoPromotionActor}) — never caller-supplied, never a model.
//   - `Promote` re-presents the gate checklist produced by the real gate run;
//     the store vetoes a checklist inconsistent with its own rows.
func (p *PromotionPipeline) AutoPromote(input AutoPromotionInput) (*schemas.PromotionRecord, error) {
	if input.LearningMode != LearningModeAuto {
		return nil, newPromotionBypassVetoError(PromotionVetoLearningModeNotAuto,
			"bypass vetoed: only learning.mode=auto automates promotion (got "+
				jsonStringMode(input.LearningMode)+") — config cannot force the auto path "+
				"(ARCH §8.9; V-P12-9)")
	}
	gateResult, err := p.Propose(input.PromotionProposalInput)
	if err != nil {
		return nil, err
	}
	if p.lastProposedID == "" {
		// Unreachable via the composed pipeline — propose either succeeded and
		// recorded the id, or threw. Fail closed regardless (INV-9).
		return nil, newPromotionBypassVetoError(PromotionVetoFailingGate,
			"bypass vetoed: no gate-passed proposal exists to promote (V-P12-9, INV-9)")
	}
	if _, err := p.store.Approve(p.lastProposedID, AutoPromotionActor); err != nil {
		return nil, err
	}
	return p.store.Promote(p.lastProposedID, gateChecklistFrom(gateResult))
}

// jsonStringMode renders a learning mode the way JSON.stringify does in the
// TS veto message (`got "propose"`).
func jsonStringMode(mode LearningMode) string {
	return `"` + string(mode) + `"`
}

// Rollback is the one-op rollback (TS PromotionPipeline.rollback): a new
// promoted row restoring the PRIOR version.
func (p *PromotionPipeline) Rollback(promotionID string) (*schemas.PromotionRecord, error) {
	return p.store.Rollback(promotionID)
}

// Log replays the promotion event log (TS PromotionPipeline.log).
func (p *PromotionPipeline) Log() ([]schemas.PromotionRecord, error) {
	return p.store.ReadAll()
}
