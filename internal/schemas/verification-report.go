package schemas

// Verification tier values.
const (
	Tier1 = 1
	Tier2 = 2
	Tier3 = 3
)

// VerificationVerdict is the overall verification outcome.
type VerificationVerdict string

// Verification verdict values (ARCH §4.8).
const (
	VerdictPass   VerificationVerdict = "pass"
	VerdictReview VerificationVerdict = "review"
	VerdictReject VerificationVerdict = "reject"
)

// VerificationVerdictValues lists all verdicts.
var VerificationVerdictValues = []VerificationVerdict{VerdictPass, VerdictReview, VerdictReject}

// IsValidVerificationVerdict reports membership.
func IsValidVerificationVerdict(v string) bool {
	for _, x := range VerificationVerdictValues {
		if string(x) == v {
			return true
		}
	}
	return false
}

// CheckStatus values.
const (
	CheckPass    = "pass"
	CheckFail    = "fail"
	CheckSkipped = "skipped"
)

// CheckStatusValues lists all check statuses.
var CheckStatusValues = []string{CheckPass, CheckFail, CheckSkipped}

// VerificationCheckIds is the closed check-id vocabulary (kebab-case),
// grouped by tier in declaration order. Free-string ids are rejected.
var VerificationCheckIds = []string{
	// Tier 1 — deterministic validators.
	"patch-applies-cleanly",
	"manifest-allowlist-only",
	"ownership-floor",
	"minimality-bound",
	"whole-document-rewrite",
	"structural-integrity",
	"link-resolution",
	"examples-parse",
	// Tier 2 — evidence-backed claim checking.
	"claim-decomposition",
	"claim-anchor-binding",
	"head-verification",
	"over-deletion-guard",
	"contract-aware-check",
	"coverage-defect",
	"behavioral-predicate-unverified",
	"cross-patch-consistency",
	// Tier 3 — independent judge (D13).
	"judge-second-pass",
	"judge-independence",
}

// CheckTier is the frozen check-id → tier mapping.
var CheckTier = map[string]int{
	"patch-applies-cleanly":              Tier1,
	"manifest-allowlist-only":            Tier1,
	"ownership-floor":                    Tier1,
	"minimality-bound":                   Tier1,
	"whole-document-rewrite":             Tier1,
	"structural-integrity":               Tier1,
	"link-resolution":                    Tier1,
	"examples-parse":                     Tier1,
	"claim-decomposition":                Tier2,
	"claim-anchor-binding":               Tier2,
	"head-verification":                  Tier2,
	"over-deletion-guard":                Tier2,
	"contract-aware-check":               Tier2,
	"coverage-defect":                    Tier2,
	"behavioral-predicate-unverified":    Tier2,
	"cross-patch-consistency":            Tier2,
	"judge-second-pass":                  Tier3,
	"judge-independence":                 Tier3,
}

// VerificationClaimStatus values.
const (
	ClaimVerdictSupported             = "supported"
	ClaimVerdictMismatched            = "mismatched"
	ClaimVerdictUnanchored            = "unanchored"
	ClaimVerdictOverDeleted           = "over-deleted"
	ClaimVerdictBehavioralUnverified  = "behavioral-unverified"
	ClaimVerdictUnchecked             = "unchecked"
)

// VerificationClaimStatusValues lists all per-claim statuses.
var VerificationClaimStatusValues = []string{
	ClaimVerdictSupported, ClaimVerdictMismatched, ClaimVerdictUnanchored,
	ClaimVerdictOverDeleted, ClaimVerdictBehavioralUnverified, ClaimVerdictUnchecked,
}

// AutoBlockReason values (OD-R09).
const (
	AutoBlockUnanchoredClaims        = "unanchored-claims"
	AutoBlockBehavioralPredicate     = "behavioral-predicate-unverified"
)

// AutoBlockReasonValues lists reasons in canonical order.
var AutoBlockReasonValues = []string{AutoBlockUnanchoredClaims, AutoBlockBehavioralPredicate}

// VerificationJudgeQuestionIds lists the universal judge set.
var VerificationJudgeQuestionIds = []string{
	"stale_before", "resolved_after", "minimal", "introduced_new_inaccuracy",
}

// VerificationCheck is one check record (ARCH §4.8: every check emits a record).
type VerificationCheck struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Detail  *string  `json:"detail,omitempty"`
	Inputs  []string `json:"inputs,omitempty"`
}

// VerificationTierReport is one tier's status + checks.
type VerificationTierReport struct {
	Tier   int                 `json:"tier"`
	Status string              `json:"status"`
	Checks []VerificationCheck  `json:"checks"`
}

// VerificationClaim is a per-claim verification outcome (PLAN §5.3).
type VerificationClaim struct {
	ClaimID string  `json:"claim_id"`
	Status  string  `json:"status"`
	Detail  *string `json:"detail,omitempty"`
}

// VerificationJudgeAnswer is one judge answer: value + probability.
type VerificationJudgeAnswer struct {
	Value       string  `json:"value"`
	Probability float64 `json:"probability"`
}

// VerificationJudge is the independent judge outcome with the full model
// binding pin (D3): model is the actually-routed model.
type VerificationJudge struct {
	Model          string                            `json:"model"`
	RequestedModel *string                           `json:"requested_model,omitempty"`
	Provider       *string                           `json:"provider,omitempty"`
	Verdict        VerificationVerdict               `json:"verdict"`
	Questions      map[string]VerificationJudgeAnswer `json:"questions,omitempty"`
	Reasons        []string                          `json:"reasons,omitempty"`
}

// VerificationJudgeAbstain is a first-class judge abstention (invariant 9).
type VerificationJudgeAbstain struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// VerificationReport is the Phase-4 verification result for one document.
type VerificationReport struct {
	Schema            string                    `json:"schema"`
	RunID             string                    `json:"run_id"`
	DocPath           string                    `json:"doc_path"`
	PatchRef          string                    `json:"patch_ref"`
	Tiers             []VerificationTierReport  `json:"tiers"`
	Claims            []VerificationClaim       `json:"claims"`
	Verdict           VerificationVerdict       `json:"verdict"`
	AutoEligible      bool                      `json:"auto_eligible"`
	AutoBlockReasons  []string                  `json:"auto_block_reasons"`
	PolicyVersion     int                       `json:"policy_version"`
	Judge             *VerificationJudge        `json:"judge,omitempty"`
	JudgeAbstain      *VerificationJudgeAbstain `json:"judge_abstain,omitempty"`
	CrossPatchConflict *bool                    `json:"cross_patch_conflict,omitempty"`
}

// ResolveVerificationVerdict is the CANONICAL verdict mapping (OD-2):
//   - any tier fail OR judge verdict reject ⇒ reject;
//   - cross-patch conflict detected ⇒ review (never auto, never reject on its own);
//   - judge verdict review OR judge abstention present ⇒ review;
//   - otherwise pass.
//
// Unanchored/behavioral claim outcomes do NOT lower the verdict — they only
// clear auto_eligible (OD-R09); autoBlockReasons is accepted for signature
// completeness and deliberately never consulted (the policy engine consumes it).
func ResolveVerificationVerdict(tiers []VerificationTierReport, judge *VerificationJudge, judgeAbstain *VerificationJudgeAbstain, crossPatchConflict bool) VerificationVerdict {
	for _, tier := range tiers {
		if tier.Status == CheckFail {
			return VerdictReject
		}
	}
	if judge != nil && judge.Verdict == VerdictReject {
		return VerdictReject
	}
	if crossPatchConflict {
		return VerdictReview
	}
	if judge != nil && judge.Verdict == VerdictReview {
		return VerdictReview
	}
	if judgeAbstain != nil {
		return VerdictReview
	}
	return VerdictPass
}

func deriveAutoBlockReasons(claims []VerificationClaim) []string {
	var reasons []string
	for _, c := range claims {
		if c.Status == ClaimVerdictUnanchored {
			reasons = append(reasons, AutoBlockUnanchoredClaims)
			break
		}
	}
	for _, c := range claims {
		if c.Status == ClaimVerdictBehavioralUnverified {
			reasons = append(reasons, AutoBlockBehavioralPredicate)
			break
		}
	}
	return reasons
}

// AutoEligibility is the derivation result shape.
type AutoEligibility struct {
	AutoEligible     bool     `json:"auto_eligible"`
	AutoBlockReasons []string `json:"auto_block_reasons"`
}

// ResolveAutoEligibility is the canonical auto-eligibility derivation
// (OD-R09, OD-2): any unanchored claim ⇒ unanchored-claims; any
// behavioral-unverified claim ⇒ behavioral-predicate-unverified; eligible iff
// no reason applies. Extra block reasons widen the set — claim-derived
// reasons are a floor, never shrinkable. Reasons are emitted in canonical
// order, deduplicated, so reports are diff-stable.
func ResolveAutoEligibility(claims []VerificationClaim, extraBlockReasons ...string) AutoEligibility {
	set := map[string]bool{}
	ordered := []string{}
	add := func(r string) {
		if !set[r] {
			set[r] = true
			ordered = append(ordered, r)
		}
	}
	for _, r := range deriveAutoBlockReasons(claims) {
		add(r)
	}
	for _, r := range extraBlockReasons {
		add(r)
	}
	// Canonical order + dedup.
	var canon []string
	for _, want := range AutoBlockReasonValues {
		if set[want] {
			canon = append(canon, want)
		}
	}
	return AutoEligibility{AutoEligible: len(canon) == 0, AutoBlockReasons: canon}
}

// ValidateReport checks a report against the schema refinements: exactly
// three tiers (1, 2, 3 each once), check-id → tier coherence, evidence
// attachment on failing checks, canonical verdict mapping, judge abstention
// consistency, and the auto-eligibility gate.
func (r *VerificationReport) ValidateReport() []Issue {
	var issues []Issue
	// Tier set.
	if len(r.Tiers) != 3 {
		Collect(&issues, &Issue{Path: []string{"tiers"}, Message: "tiers must contain exactly three entries — tier 1, 2 and 3, each exactly once (all three tiers always run; ARCH §4.8)"})
	} else {
		seen := map[int]bool{}
		for _, t := range r.Tiers {
			seen[t.Tier] = true
		}
		if len(seen) != len(r.Tiers) {
			Collect(&issues, &Issue{Path: []string{"tiers"}, Message: "duplicate tier entries — tiers 1, 2 and 3 must each appear exactly once"})
		}
	}
	// Check-id → tier coherence + evidence attachment.
	for ti, tierReport := range r.Tiers {
		for ci, check := range tierReport.Checks {
			want, known := CheckTier[check.ID]
			if known && want != tierReport.Tier {
				Collect(&issues, &Issue{
					Path:    []string{"tiers", itoa(ti), "checks", itoa(ci), "id"},
					Message: "check '" + check.ID + "' belongs to tier " + itoa(want) + " but appears in the tier-" + itoa(tierReport.Tier) + " report (closed vocabulary mapping, CHECK_TIER)",
				})
			}
			if check.Status == CheckFail && (check.Detail == nil || *check.Detail == "") {
				Collect(&issues, &Issue{
					Path:    []string{"tiers", itoa(ti), "checks", itoa(ci), "detail"},
					Message: "failing check '" + check.ID + "' must carry a non-empty detail (invariant 5: verification evidence)",
				})
			}
		}
	}
	// Canonical verdict mapping.
	resolved := ResolveVerificationVerdict(r.Tiers, r.Judge, r.JudgeAbstain, r.CrossPatchConflict != nil && *r.CrossPatchConflict)
	if r.Verdict != resolved {
		Collect(&issues, &Issue{Path: []string{"verdict"}, Message: "verdict '" + string(r.Verdict) + "' contradicts the canonical mapping '" + string(resolved) + "' (reject iff a tier fails or the judge rejects; review on cross-patch conflict, a judge review or abstention; otherwise pass — ARCH §4.8, OD-2, R16)"})
	}
	// Pinned redundancy: judge reject ⇒ report reject.
	if r.Judge != nil && r.Judge.Verdict == VerdictReject && r.Verdict != VerdictReject {
		Collect(&issues, &Issue{Path: []string{"judge", "verdict"}, Message: "judge verdict reject ⇒ report verdict reject (fail closed; ARCH §4.8)"})
	}
	if r.CrossPatchConflict != nil && *r.CrossPatchConflict && r.Verdict == VerdictPass {
		Collect(&issues, &Issue{Path: []string{"cross_patch_conflict"}, Message: "cross_patch_conflict true with verdict 'pass' is forbidden — detected cross-patch inconsistency blocks to review, never auto (R16, V-P4-12)"})
	}
	// Judge abstention consistency.
	if r.Judge != nil && r.JudgeAbstain != nil {
		Collect(&issues, &Issue{Path: []string{"judge"}, Message: "judge and judge_abstain are mutually exclusive — an abstaining judge emits no judge block"})
	}
	var tier3 *VerificationTierReport
	for i := range r.Tiers {
		if r.Tiers[i].Tier == Tier3 {
			tier3 = &r.Tiers[i]
		}
	}
	if r.JudgeAbstain != nil {
		if tier3 == nil || tier3.Status != CheckSkipped {
			Collect(&issues, &Issue{Path: []string{"judge_abstain"}, Message: "judge_abstain present ⇒ tier 3 status must be skipped (the judge did not run)"})
		}
	} else if tier3 != nil && tier3.Status == CheckSkipped {
		Collect(&issues, &Issue{Path: []string{"judge_abstain"}, Message: "tier 3 'skipped' requires a judge_abstain record — a skipped judge tier fails closed to review (invariant 9)"})
	}
	if tier3 != nil && tier3.Status == CheckSkipped && r.Verdict == VerdictPass {
		Collect(&issues, &Issue{Path: []string{"verdict"}, Message: "tier 3 'skipped' ⇒ verdict must not be 'pass' — judge abstention reviews"})
	}
	// Auto-eligibility gate.
	derived := deriveAutoBlockReasons(r.Claims)
	if (len(r.AutoBlockReasons) == 0) != r.AutoEligible {
		if len(r.AutoBlockReasons) == 0 {
			Collect(&issues, &Issue{Path: []string{"auto_block_reasons"}, Message: "auto_block_reasons empty ⇔ auto_eligible true (OD-R09 gate)"})
		} else {
			Collect(&issues, &Issue{Path: []string{"auto_eligible"}, Message: "auto_block_reasons empty ⇔ auto_eligible true (OD-R09 gate)"})
		}
	}
	for _, reason := range derived {
		found := false
		for _, have := range r.AutoBlockReasons {
			if have == reason {
				found = true
				break
			}
		}
		if !found {
			Collect(&issues, &Issue{Path: []string{"auto_block_reasons"}, Message: "claim statuses imply auto_block_reason '" + reason + "' (OD-R09) but it is absent — claim-derived block reasons are a floor"})
		}
	}
	return issues
}
