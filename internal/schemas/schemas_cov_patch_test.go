package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covValidHunk is the minimal valid hunk (one mandatory evidence ref).
func covValidHunk() schemas.PatchHunk {
	return schemas.PatchHunk{
		Anchor:       covAnchor(42, 42),
		DeleteText:   "Refunds require an idempotency key.",
		InsertText:   "Refunds require an idempotency key for amounts above 10 USD.",
		EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"}},
		ClaimIDs:     []string{"claim_182"},
	}
}

// TestCOV_SCHEMA_039_PatchAnchorGuardMatrix drives every guard of
// PatchAnchor.Validate directly.
func TestCOV_SCHEMA_039_PatchAnchorGuardMatrix(t *testing.T) {
	hunk := covValidHunk()
	assert.Empty(t, hunk.Anchor.Validate())

	bad := &schemas.PatchAnchor{
		StartLine:   0,
		EndLine:     0,
		ContentHash: "not-a-digest",
	}
	issues := bad.Validate()
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "start_line").Message)
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "end_line").Message)
	assert.Equal(t, "content_hash must be bare 64-hex sha256 (no sha256: prefix)",
		covIssueAt(issues, "content_hash").Message)

	emptyHash := &schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: ""}
	emptyIssues := emptyHash.Validate()
	require.Len(t, emptyIssues, 2)
	assert.Contains(t, covIssueAt(emptyIssues, "content_hash").Message, "String must contain at least 1 character(s)")
	assert.True(t, covHasMessage(emptyIssues, "content_hash must be bare 64-hex sha256 (no sha256: prefix)"))

	inverted := &schemas.PatchAnchor{StartLine: 42, EndLine: 41, ContentHash: covAnchor(1, 1).ContentHash}
	issues = inverted.Validate()
	assert.Equal(t, "end_line must be greater than or equal to start_line", covIssueAt(issues, "end_line").Message)
	assert.Nil(t, covIssueAt(issues, "start_line"))

	// A sha256-prefixed digest is the canonical OD-R42 shape violation.
	prefixed := &schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: "sha256:cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64"}
	require.NotNil(t, covIssueAt(prefixed.Validate(), "content_hash"))
}

// TestCOV_SCHEMA_040_PatchHunkGuardMatrix covers the per-hunk guards: the
// nested anchor wiring, the mandatory minimum of one evidence ref and the
// evidence-loop validation.
func TestCOV_SCHEMA_040_PatchHunkGuardMatrix(t *testing.T) {
	valid := covValidHunk()
	assert.Empty(t, valid.Validate())

	badAnchor := valid
	badAnchor.Anchor = schemas.PatchAnchor{EndLine: -3, ContentHash: ""}
	issues := badAnchor.Validate()
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "anchor", "start_line").Message)
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "anchor", "end_line").Message)

	noEvidence := valid
	noEvidence.EvidenceRefs = nil
	issues = noEvidence.Validate()
	assert.Equal(t, "Array must contain at least 1 element(s)", covIssueAt(issues, "evidence_refs").Message)

	badEvidence := valid
	badEvidence.EvidenceRefs = []schemas.Evidence{{Kind: "", Ref: "", Note: ""}, {Kind: "symbol_diff", Ref: "src/a.ts:1"}}
	issues = badEvidence.Validate()
	assert.Contains(t, covIssueAt(issues, "evidence_refs", "0", "kind").Message,
		"Invalid enum value. Expected 'symbol_diff' | 'api_diff' | 'config_diff' | 'dependency_diff' | 'doc_anchor' | 'other', received ''")
	assert.Nil(t, covIssueAt(issues, "evidence_refs", "1", "kind"))

	// Evidence kinds are a closed vocabulary.
	wrongKind := valid
	wrongKind.EvidenceRefs = []schemas.Evidence{{Kind: "telepathy", Ref: "src/a.ts:1"}}
	issues = wrongKind.Validate()
	require.NotNil(t, covIssueAt(issues, "evidence_refs", "0", "kind"))
	assert.Contains(t, covIssueAt(issues, "evidence_refs", "0", "kind").Message, "Invalid enum value. Expected")
}

// TestCOV_SCHEMA_041_DocPatchMinimalityAndProvenance covers the sidecar
// blocks and their optional-refinement guards.
func TestCOV_SCHEMA_041_DocPatchMinimalityAndProvenance(t *testing.T) {
	validMinimality := &schemas.DocPatchMinimality{Measured: true, AddedLines: 3, RemovedLines: 1}
	assert.Empty(t, validMinimality.Validate())

	badMinimality := &schemas.DocPatchMinimality{Measured: false, AddedLines: -1, RemovedLines: -1}
	issues := badMinimality.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "added_lines").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "removed_lines").Message)

	validProvenance := &schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "anthropic/claude-sonnet-4.5", GeneratedAt: "2026-10-04T22:31:00Z"}
	assert.Empty(t, validProvenance.Validate())

	badProvenance := &schemas.DocPatchProvenance{PolicyVersion: 0, WriterModel: "", GeneratedAt: "nope"}
	issues = badProvenance.Validate()
	assert.Equal(t, "Number must be greater than 0", covIssueAt(issues, "policy_version").Message)
	assert.Equal(t, "String must contain at least 1 character(s)", covIssueAt(issues, "writer_model").Message)
	assert.Contains(t, covIssueAt(issues, "generated_at").Message, "Invalid datetime")
}

// TestCOV_SCHEMA_042_DocPatchRootGuards covers the root guards: the schema
// literal, the empty-hunk protocol violation, both evidence loops, the
// verified_claims element check and both superRefine refinements.
func TestCOV_SCHEMA_042_DocPatchRootGuards(t *testing.T) {
	assert.Empty(t, schemas.DocPatchExample.Validate())

	emptySchema := schemas.DocPatchExample
	emptySchema.Schema = ""
	issues := emptySchema.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)

	badSchema := schemas.DocPatchExample
	badSchema.Schema = "docsync.doc-patch/v2"
	require.NotNil(t, covIssueAt(badSchema.Validate(), "schema"))
	assert.Contains(t, covIssueAt(badSchema.Validate(), "schema").Message,
		`Invalid literal value, expected "docsync.doc-patch/v1"`)

	noHunks := schemas.DocPatchExample
	noHunks.Hunks = nil
	issues = noHunks.Validate()
	assert.Equal(t, "Array must contain at least 1 element(s)", covIssueAt(issues, "hunks").Message)

	emptyHunk := schemas.DocPatchExample
	emptyHunk.Hunks = []schemas.PatchHunk{{}}
	issues = emptyHunk.Validate()
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "hunks", "0", "anchor", "start_line").Message)
	assert.Equal(t, "Array must contain at least 1 element(s)", covIssueAt(issues, "hunks", "0", "evidence_refs").Message)

	badEvidenceRefs := schemas.DocPatchExample
	badEvidenceRefs.EvidenceRefs = []schemas.Evidence{{Kind: "", Ref: ""}}
	require.NotNil(t, covIssueAt(badEvidenceRefs.Validate(), "evidence_refs", "0", "kind"))

	badMinimality := schemas.DocPatchExample
	badMinimality.Minimality = schemas.DocPatchMinimality{AddedLines: -2}
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(badMinimality.Validate(), "minimality", "added_lines").Message)

	badProvenance := schemas.DocPatchExample
	badProvenance.Provenance.PolicyVersion = 0
	assert.Equal(t, "Number must be greater than 0", covIssueAt(badProvenance.Validate(), "provenance", "policy_version").Message)

	badClaims := schemas.DocPatchExample
	badClaims.VerifiedClaims = []string{"claim_182", ""}
	assert.Equal(t, "String must contain at least 1 character(s)",
		covIssueAt(badClaims.Validate(), "verified_claims", "1").Message)

	// Refinement 1 — overlapping hunk line ranges, sorted by (start, end).
	overlapping := schemas.DocPatchExample
	overlapping.Hunks = []schemas.PatchHunk{
		{Anchor: covAnchor(20, 30), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:20"}}},
		{Anchor: covAnchor(21, 25), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:21"}}},
	}
	issues = overlapping.Validate()
	require.NotNil(t, covIssueAt(issues, "hunks"))
	assert.Contains(t, covIssueAt(issues, "hunks").Message, "hunks 0 and 1 overlap in line ranges [20, 30] and [21, 25]")
	assert.Contains(t, covIssueAt(issues, "hunks").Message, "hunk line ranges must be strictly non-overlapping")

	// Refinement 1 — the same ranges supplied out of order still detect.
	unsorted := schemas.DocPatchExample
	unsorted.Hunks = []schemas.PatchHunk{
		{Anchor: covAnchor(21, 25), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:21"}}},
		{Anchor: covAnchor(20, 30), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:20"}}},
	}
	require.NotNil(t, covIssueAt(unsorted.Validate(), "hunks"))

	// Refinement 1 — non-overlapping ranges are accepted (and preserve order).
	disjoint := schemas.DocPatchExample
	disjoint.Hunks = []schemas.PatchHunk{
		{Anchor: covAnchor(51, 55), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:51"}}},
		{Anchor: covAnchor(42, 44), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:42"}}},
	}
	assert.Empty(t, disjoint.Validate())

	// Refinement 2 — the claim-level subset rule (P11-T15, OD-R26).
	subset := schemas.DocPatchExample
	subset.Hunks = []schemas.PatchHunk{
		{Anchor: covAnchor(42, 42), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:42"}}, VerifiedClaimRefs: []string{"claim_182", "claim_999"}},
	}
	subset.VerifiedClaims = []string{"claim_182"}
	issues = subset.Validate()
	require.NotNil(t, covIssueAt(issues, "hunks", "0", "verified_claim_refs"))
	assert.Contains(t, covIssueAt(issues, "hunks", "0", "verified_claim_refs").Message,
		"hunks[0].verified_claim_refs contains 'claim_999' which is not in the patch-level verified_claims set")
	assert.Contains(t, covIssueAt(issues, "hunks", "0", "verified_claim_refs").Message,
		"every hunk verified_claim_ref must be a subset of the patch-level verified_claims (P11-T15, OD-R26)")

	// Refinement 2 — an absent patch-level set rejects any ref.
	noPatchLevel := subset
	noPatchLevel.VerifiedClaims = nil
	require.NotNil(t, covIssueAt(noPatchLevel.Validate(), "hunks", "0", "verified_claim_refs"))

	// Refinement 2 — a subset of the declared set is accepted.
	covered := subset
	covered.VerifiedClaims = []string{"claim_182", "claim_999"}
	assert.Empty(t, covered.Validate())

	// No refs at all is always accepted (strictly-additive legacy patches).
	legacy := schemas.DocPatchExample
	legacy.Hunks = []schemas.PatchHunk{
		{Anchor: covAnchor(42, 42), EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:42"}}},
	}
	legacy.VerifiedClaims = nil
	assert.Empty(t, legacy.Validate())
}

// TestCOV_SCHEMA_043_DocPatchParseStrictness covers both rejection branches of
// ParseDocPatch.
func TestCOV_SCHEMA_043_DocPatchParseStrictness(t *testing.T) {
	parsed, err := schemas.ParseDocPatch([]byte(`{"schema":"docsync.doc-patch/v1","run_id":"r","doc_path":"docs/a.md","base_content_hash":"cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64","hunks":[{"anchor":{"start_line":1,"end_line":1,"content_hash":"cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64"},"delete_text":"a","insert_text":"b","evidence_refs":[{"kind":"symbol_diff","ref":"src/a.ts:1"}]}],"evidence_refs":[],"minimality":{"measured":false,"added_lines":1,"removed_lines":1},"provenance":{"policy_version":17,"writer_model":"m","generated_at":"2026-10-04T22:31:00Z"}}`))
	require.NoError(t, err)
	assert.Equal(t, "docs/a.md", parsed.DocPath)

	_, err = schemas.ParseDocPatch([]byte(`{"nope":1}`))
	require.Error(t, err)

	_, err = schemas.ParseDocPatch([]byte(`{"schema":"docsync.doc-patch/v1","run_id":"","doc_path":"d","base_content_hash":"x","hunks":[],"evidence_refs":[],"minimality":{"measured":true,"added_lines":0,"removed_lines":0},"provenance":{"policy_version":1,"writer_model":"m","generated_at":"2026-10-04T22:31:00Z"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hunks: Array must contain at least 1 element(s)")
}

// covEvidence builds an addressable Evidence value (Validate is a pointer
// method, so the composite literal needs a variable).
func covEvidence(kind, ref string, notes ...string) schemas.Evidence {
	ev := schemas.Evidence{Kind: schemas.EvidenceKind(kind), Ref: ref}
	if len(notes) > 0 {
		ev.Note = notes[0]
	}
	return ev
}

// TestCOV_SCHEMA_044_EvidenceRefIssueMachineResolvableKinds covers the
// per-kind machine-resolvability rules of the shared evidence-ref validator,
// including the unprefixed dependency shape and the config-key fallback.
func TestCOV_SCHEMA_044_EvidenceRefIssueMachineResolvableKinds(t *testing.T) {
	accepted := [][2]string{
		{"symbol_diff", "src/a.ts:42"},
		{"symbol_diff", "src/a.ts:refundRequest"},
		{"api_diff", "src/a.ts"},
		{"config_diff", "config/app.yml:server.port"},
		{"dependency_diff", "lodash"},
		{"dependency_diff", "@scope/pkg"},
		{"doc_anchor", "docs/a.md:42"},
		{"other", "free-form provenance note"},
	}
	for _, pair := range accepted {
		assert.Empty(t, schemas.EvidenceRefIssue(pair[0], pair[1]),
			"evidence kind %q ref %q should be machine-resolvable", pair[0], pair[1])
	}

	// symbol_diff: a bad split, a bad path component, a bad anchor component.
	assert.Equal(t, "symbol_diff ref must be 'path:line' or 'path:symbolName' (got 'with spaces')",
		schemas.EvidenceRefIssue("symbol_diff", "with spaces"))
	assert.Equal(t, "symbol_diff path component '/abs.ts' is not a repo-relative path",
		schemas.EvidenceRefIssue("symbol_diff", "/abs.ts:42"))
	assert.Equal(t, "symbol_diff anchor 'nope nope' is neither a 1-based line number nor a symbol name",
		schemas.EvidenceRefIssue("symbol_diff", "src/a.ts:nope nope"))

	// api_diff: a non-repo-relative ref.
	assert.Equal(t, "api_diff ref must be a repo-relative path without an anchor (got '/abs.ts')",
		schemas.EvidenceRefIssue("api_diff", "/abs.ts"))

	// config_diff: a bad split, a bad file component, a malformed key.
	assert.Equal(t, "config_diff ref must be 'file:key' (got 'has spaces')",
		schemas.EvidenceRefIssue("config_diff", "has spaces"))
	assert.Equal(t, "config_diff file component '../abs.yml' is not a repo-relative path",
		schemas.EvidenceRefIssue("config_diff", "../abs.yml:port"))
	assert.Equal(t, "config_diff key component ' ' is empty or malformed",
		schemas.EvidenceRefIssue("config_diff", "config/app.yml: "))

	// dependency_diff: only npm package names are accepted.
	assert.Equal(t, "dependency_diff ref must be an npm package name (got '.')",
		schemas.EvidenceRefIssue("dependency_diff", "."))
	assert.Equal(t, "dependency_diff ref must be an npm package name (got '..')",
		schemas.EvidenceRefIssue("dependency_diff", ".."))
	assert.Equal(t, "dependency_diff ref must be an npm package name (got 'src/a.ts')",
		schemas.EvidenceRefIssue("dependency_diff", "src/a.ts"))

	// doc_anchor: the same split/path/anchor chain as symbol_diff.
	assert.Equal(t, "doc_anchor ref must be 'path:line' (got 'docs/a.md')",
		schemas.EvidenceRefIssue("doc_anchor", "docs/a.md"))
	assert.Equal(t, "doc_anchor path component '/abs.md' is not a repo-relative path",
		schemas.EvidenceRefIssue("doc_anchor", "/abs.md:42"))
	assert.Equal(t, "doc_anchor anchor 'nope nope' is not a 1-based line number",
		schemas.EvidenceRefIssue("doc_anchor", "docs/a.md:nope nope"))

	// `other` evidence is free-form but must carry a non-empty ref.
	assert.Equal(t, "other evidence ref must be non-empty", schemas.EvidenceRefIssue("other", ""))

	// An unknown kind renders the closed-vocabulary message.
	assert.Equal(t,
		"unknown evidence kind 'nope' — not a machine-resolvable anchor taxonomy member",
		schemas.EvidenceRefIssue("nope", "src/a.ts"))

	// The enum + required-ref half of EVIDENCE_SCHEMA is still enforced by
	// the structural Validate.
	badKind := covEvidence("nope", "src/a.ts")
	require.NotNil(t, covIssueAt(badKind.Validate(), "kind"))
	emptyRef := covEvidence("symbol_diff", "")
	require.NotNil(t, covIssueAt(emptyRef.Validate(), "ref"))
	validEvidence := covEvidence("symbol_diff", "src/a.ts:42")
	assert.Empty(t, validEvidence.Validate())
}
