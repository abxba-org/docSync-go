package schemas

// Ported from packages/schemas/src/v1/org-policy.ts (P12-T13; ARCH §11.2,
// §11.3; D8; OD-R30). Organizational floors that apply across repositories:
// mode ceilings and report-only-until-reviewed floors. Fail-closed
// validation (D8): violations are errors, never silent clamps. `locked`
// floors are terminal (INV-4).
//
// Report-only floors stay ADVISORY until the org-policy file declares their
// manifest `reviewed: true` — the flag lives in this HUMAN-OWNED file, so
// flipping it is a review act; an undeclared review is not a review.
//
// Precedence (ARCH §11.2/§11.3): flags > per-doc > manifest defaults > org
// floors > built-ins. Org policy is a REPOSITORY FILE (reviewable in PRs);
// the GitHub App fetches and hash-pins it into run records (OD-R30).

// OrgPolicySchemaID is the registry ID for this schema (ORG_POLICY_SCHEMA_ID).
const OrgPolicySchemaID = "docsync.org-policy/v1"

// LearningMode is one learning-mode value re-exported from policy governance
// for the org default (LEARNING_MODE_VALUES).
type LearningMode string

// Learning mode values.
const (
	LearningModeOff     LearningMode = "off"
	LearningModePropose LearningMode = "propose"
	LearningModeCanary  LearningMode = "canary"
	LearningModeAuto    LearningMode = "auto"
)

// LearningModeValues lists the learning modes in canonical order.
var LearningModeValues = []LearningMode{
	LearningModeOff, LearningModePropose, LearningModeCanary, LearningModeAuto,
}

// IsValidLearningMode reports membership.
func IsValidLearningMode(s string) bool {
	for _, v := range LearningModeValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// Floor kinds — the discriminated-union discriminator on `kind`.
const (
	FloorKindModeCeiling             = "mode_ceiling"
	FloorKindReportOnlyUntilReviewed = "report_only_until_reviewed"
)

// FloorKindValues lists the floor kinds in canonical order.
var FloorKindValues = []string{FloorKindModeCeiling, FloorKindReportOnlyUntilReviewed}

// OrgFloor is one entry of the FLOOR_SCHEMA discriminated union on `kind`
// (zod's discriminated union over the two strictObject variants is ported as
// a single struct with pointer fields for each variant, so marshaling omits
// the keys a variant does not carry and the TS wire bytes are preserved):
//
//   - kind: 'mode_ceiling'             → pattern + maxMode (DOC_MODE)
//   - kind: 'report_only_until_reviewed' → pattern + requiredReviewers?
//   - reviewed?
type OrgFloor struct {
	Kind string `json:"kind"`
	// Pattern is shared by both variants: minimal pattern — exact path,
	// bare directory, or trailing `dir/**`.
	Pattern string `json:"pattern"`
	// MaxMode is the mode_ceiling variant field: the maximum mode documents
	// matching this pattern may resolve to.
	MaxMode *DocMode `json:"maxMode,omitempty"`
	// RequiredReviewers is the report_only_until_reviewed variant field:
	// optional required reviewers; if absent, any reviewer suffices.
	RequiredReviewers []string `json:"requiredReviewers,omitempty"`
	// Reviewed is the report_only_until_reviewed variant field: whether the
	// org has declared the matching manifest reviewed. Absence behaves as
	// `false` — an undeclared review is not a review (fail closed; P12-T13).
	Reviewed *bool `json:"reviewed,omitempty"`
}

// Validate checks one floor against FLOOR_SCHEMA (strict), reproducing the
// z.discriminatedUnion contract: an unknown discriminator rejects, each
// variant requires its own fields, and keys belonging to the OTHER variant
// reject as unrecognized (zod's strictObject message).
func (f *OrgFloor) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(f.Kind, FloorKindValues, []string{"kind"}))
	Collect(&issues, RequiredString(f.Pattern, 1, []string{"pattern"}))
	switch f.Kind {
	case FloorKindModeCeiling:
		if f.MaxMode == nil {
			Collect(&issues, &Issue{Path: []string{"maxMode"}, Message: "Required"})
		} else {
			Collect(&issues, CheckEnum(string(*f.MaxMode), stringsOf(DocModeValues), []string{"maxMode"}))
		}
		if f.RequiredReviewers != nil {
			Collect(&issues, orgFloorUnrecognizedKey("requiredReviewers"))
		}
		if f.Reviewed != nil {
			Collect(&issues, orgFloorUnrecognizedKey("reviewed"))
		}
	case FloorKindReportOnlyUntilReviewed:
		if f.MaxMode != nil {
			Collect(&issues, orgFloorUnrecognizedKey("maxMode"))
		}
		if f.RequiredReviewers != nil {
			if len(f.RequiredReviewers) < 1 {
				Collect(&issues, &Issue{Path: []string{"requiredReviewers"}, Message: "Array must contain at least 1 element(s)"})
			}
			for i, r := range f.RequiredReviewers {
				Collect(&issues, RequiredString(r, 1, []string{"requiredReviewers", itoa(i)}))
			}
		}
	}
	return issues
}

// orgFloorUnrecognizedKey renders zod's strictObject unknown-key message for
// a floor variant carrying another variant's key.
func orgFloorUnrecognizedKey(key string) *Issue {
	return &Issue{Path: []string{key}, Message: "Unrecognized key(s) in object: '" + key + "'"}
}

// OrgLearning is the optional learning configuration in org policy
// (ORG_LEARNING_SCHEMA).
type OrgLearning struct {
	// DefaultMode is the org default for learning.mode resolution
	// (precedence: repo config > org default > shipped default).
	DefaultMode *LearningMode `json:"defaultMode,omitempty"`
}

// Validate checks the learning block (strict).
func (l *OrgLearning) Validate() []Issue {
	var issues []Issue
	if l.DefaultMode != nil {
		Collect(&issues, CheckEnum(string(*l.DefaultMode), stringsOf(LearningModeValues), []string{"defaultMode"}))
	}
	return issues
}

// OrgPolicy is the full org policy (ORG_POLICY_SCHEMA).
type OrgPolicy struct {
	// Version is the schema version (integer, bump on breaking changes).
	Version int `json:"version"`
	// Floors must declare at least one floor.
	Floors []OrgFloor `json:"floors"`
	// Learning is the optional org default for learning.mode.
	Learning *OrgLearning `json:"learning,omitempty"`
	// CreatedAt is the creation timestamp (ISO 8601).
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is the last update timestamp (ISO 8601).
	UpdatedAt string `json:"updatedAt"`
}

// Validate checks the policy against ORG_POLICY_SCHEMA (strict).
func (p *OrgPolicy) Validate() []Issue {
	var issues []Issue
	if p.Version != 1 {
		msg := "Invalid literal value, expected 1"
		if p.Version == 0 {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"version"}, Message: msg})
	}
	if len(p.Floors) < 1 {
		Collect(&issues, &Issue{Path: []string{"floors"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range p.Floors {
		for _, iss := range p.Floors[i].Validate() {
			iss.Path = append([]string{"floors", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if p.Learning != nil {
		for _, iss := range p.Learning.Validate() {
			iss.Path = append([]string{"learning"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, datetimeIssue(p.CreatedAt, []string{"createdAt"}))
	Collect(&issues, datetimeIssue(p.UpdatedAt, []string{"updatedAt"}))
	return issues
}

// ParseOrgPolicy strictly decodes and validates `docsync.org-policy/v1`
// bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseOrgPolicy(data []byte) (*OrgPolicy, error) {
	var p OrgPolicy
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// Typed pointer values backing OrgPolicyExample (a var initializer cannot
// take the address of a constant; orgPolicy-prefixed names keep the package
// namespace clean against sibling files).
var (
	orgPolicySecurityMaxMode = DocModeSuggest
	orgPolicyArchMaxMode     = DocModeHuman
	orgPolicyDefaultMode     = LearningModePropose
	orgPolicyReviewed        = true
)

// OrgPolicyExample mirrors ORG_POLICY_EXAMPLE: a realistic example with both
// floor kinds and a learning default (golden data for byte-for-byte
// serializer checks).
var OrgPolicyExample = OrgPolicy{
	Version: 1,
	Floors: []OrgFloor{
		{Kind: FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: &orgPolicySecurityMaxMode},
		{Kind: FloorKindModeCeiling, Pattern: "docs/architecture.md", MaxMode: &orgPolicyArchMaxMode},
		{
			Kind:              FloorKindReportOnlyUntilReviewed,
			Pattern:           "docs/compliance/**",
			RequiredReviewers: []string{"@security-team", "@compliance-team"},
		},
		{Kind: FloorKindReportOnlyUntilReviewed, Pattern: "docs/legal/**"},
		{
			Kind:              FloorKindReportOnlyUntilReviewed,
			Pattern:           "docs/hr/**",
			RequiredReviewers: []string{"@hr-team"},
			Reviewed:          &orgPolicyReviewed,
		},
	},
	Learning:  &OrgLearning{DefaultMode: &orgPolicyDefaultMode},
	CreatedAt: "2026-10-01T12:00:00Z",
	UpdatedAt: "2026-10-01T12:00:00Z",
}
