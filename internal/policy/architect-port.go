package policy

// Ported from packages/policy/src/architect/port.ts — Policy Architect port
// (P10-T14; ARCH §8.1, CTX §16): the ten-input assembly the architect reasons
// over, and the structural port the LLM adapter implements
// (@docsync/openrouter binds the `policy-architect` role — the models schema
// roles record is open by design, so the binding needs no schema change).
//
// POLICY LEARNING, NOT PROMPT OPTIMIZATION: every proposal must carry counted
// evidence ("17/23 reviewer overrides targeted generated-API routing" style) —
// assertions without counts are rejected downstream (ARCH §8.1). The runner
// NEVER lets the port write policy.

// PolicyArchitectInputs is the ten-input assembly
// (TS: PolicyArchitectInputs). Every field is model-visible context; none of
// it is authority — the runner decides what may reach a proposal.
type PolicyArchitectInputs struct {
	RepositoryProfile   map[string]any
	CurrentPolicy       map[string]any
	Manifest            map[string]any
	Concepts            map[string]any
	HistoricalPrs       []map[string]any
	PriorDecisions      []map[string]any
	FalsePositives      []map[string]any
	FalseNegatives      []map[string]any
	ReviewerCorrections []map[string]any
	KnownExceptions     []map[string]any
}

// ArchitectCountedEvidence is one counted-evidence row
// (TS: PolicyArchitectDraft['counted_evidence'][number]) — the mandatory
// "17/23 style" ratio: assertions without counts are rejected.
type ArchitectCountedEvidence struct {
	Statement    string
	SupportCount int
	TotalCount   int
}

// PolicyArchitectDraft is what the port may propose
// (TS: PolicyArchitectDraft): a proposal type, its counted evidence and the
// policy artifact it carries. It is a DRAFT — never policy.
type PolicyArchitectDraft struct {
	Type            string
	CountedEvidence []ArchitectCountedEvidence
	Artifact        map[string]any
}

// PolicyArchitectPort is the structural port the LLM adapter implements
// (TS: PolicyArchitectPort). Receives ArchitectLoopInputs: the prior failed
// attempt + diagnostics ride along when the runner retries (ARCH §8.2 —
// proposals return to the architect with diagnostics). The Go port returns an
// error where the TS port rejected (model/transport failure): the runner fails
// closed on it rather than guessing.
type PolicyArchitectPort interface {
	Propose(input ArchitectLoopInputs) (PolicyArchitectDraft, error)
}

// PriorFailedAttempt is the prior failed attempt fed back to the architect
// with diagnostics (§8.2) (TS: PriorFailedAttempt).
type PriorFailedAttempt struct {
	Type        string
	Artifact    map[string]any
	Diagnostics []string
}

// ArchitectLoopInputs is the loop input: the ten inputs plus the optional
// prior failed attempt (TS: ArchitectLoopInputs = PolicyArchitectInputs &
// { priorFailedAttempt?: PriorFailedAttempt }). The embedded struct is the
// ten-input assembly; PriorFailedAttempt is present exactly when the runner
// retries.
type ArchitectLoopInputs struct {
	PolicyArchitectInputs
	PriorFailedAttempt *PriorFailedAttempt
}
