package patch_test

// Black-box port of test/patch-helpers.ts (P3-T08..T16): a DocPatch factory over
// the finalized @docsync/schemas shape, a hunk factory using the core hashSpan
// convention, sample documents, and a catFile double. Test-only doubles — the
// real *gitx.GitClient is exercised by gitx's own tests. Shared by all
// *_test.go files in this package (Go test files in one package share top-level
// declarations, so the fixtures live here instead of in any single suite).

import (
	"context"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// docLine is one document fixture line; DOC is the canonical small doc used
// across the suite.
var (
	docLines = []string{
		"# Payments",
		"",
		"Refunds require an idempotency key.",
		"Amounts above 10 USD are held for review.",
		"",
		"See docs/architecture.md for the flow.",
	}

	doc = strings.Join(docLines, "\n") + "\n"

	// richDocLines is a richer document (9 lines, 7 non-empty) for multi-hunk
	// tests — small documents trip the whole-document-rewrite structural rule
	// when several lines change, which is its own covered behavior.
	richDocLines = []string{
		"# Payments",
		"",
		"Refunds require an idempotency key.",
		"Amounts above 10 USD are held for review.",
		"",
		"See docs/architecture.md for the flow.",
		"Escalation contacts live in docs/oncall.md.",
		"The review window is 48 hours.",
		"Refund disputes go to docs/disputes.md.",
	}

	richDoc = strings.Join(richDocLines, "\n") + "\n"
)

// defaultDocPatchEvidence is the shared evidence ref used by the factories.
func defaultDocPatchEvidence() []schemas.Evidence {
	return []schemas.Evidence{{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  "src/payments/refund.ts:42",
		Note: "idempotencyKey became optional",
	}}
}

// hunkOpts mirrors the TS hunk() options struct.
type hunkOpts struct {
	start               int
	deleteText          string
	insert              string
	claimIDs            []string
	verifiedClaimRefs   []string
	contentHashOverride string
}

// hunk builds a writer delete/insert hunk with a correct content-hash-first
// anchor (TS: hunk()).
func hunk(o hunkOpts) schemas.PatchHunk {
	height := 1
	if o.deleteText != "" {
		height = len(strings.Split(o.deleteText, "\n"))
	}
	contentHash := o.contentHashOverride
	if contentHash == "" {
		contentHash = docutil.ContentHash(o.deleteText)
	}
	h := schemas.PatchHunk{
		Anchor: schemas.PatchAnchor{
			StartLine:   o.start,
			EndLine:     o.start + height - 1,
			ContentHash: contentHash,
		},
		DeleteText:   o.deleteText,
		InsertText:   o.insert,
		EvidenceRefs: defaultDocPatchEvidence(),
	}
	if o.claimIDs != nil {
		h.ClaimIDs = o.claimIDs
	}
	if o.verifiedClaimRefs != nil {
		h.VerifiedClaimRefs = o.verifiedClaimRefs
	}
	return h
}

// docPatchOpts mirrors the TS makeDocPatch() options struct.
type docPatchOpts struct {
	docPath        string
	baseContent    string
	hunks          []schemas.PatchHunk
	runID          string
	verifiedClaims []string
}

// makeDocPatch builds a complete DocPatch-shaped record (schema-valid unless a
// field is deliberately weakened) (TS: makeDocPatch()).
func makeDocPatch(o docPatchOpts) *schemas.DocPatch {
	docPath := o.docPath
	if docPath == "" {
		docPath = "docs/payments.md"
	}
	runID := o.runID
	if runID == "" {
		runID = "acme/payments#8a73f2c..81af92e"
	}
	p := &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           runID,
		DocPath:         docPath,
		BaseContentHash: docutil.ContentHash(o.baseContent),
		Hunks:           o.hunks,
		EvidenceRefs:    defaultDocPatchEvidence(),
		Minimality:      schemas.DocPatchMinimality{Measured: false, AddedLines: 0, RemovedLines: 0},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 17,
			WriterModel:   "anthropic/claude-sonnet-4.5",
			GeneratedAt:   "2026-10-06T09:40:00.000Z",
		},
	}
	if o.verifiedClaims != nil {
		p.VerifiedClaims = o.verifiedClaims
	}
	return p
}

// NOW is the injected clock used across the suites (TS: NOW).
func NOW() string { return "2026-10-06T10:00:00.000Z" }

// gitCall records one CatFile invocation.
type gitCall struct {
	rev  string
	path string
}

// catFileDouble is a structural GitClient double (CatFile only — the engine's
// whole surface), mirroring the TS catFileDouble. Set err for the failure
// doubles; set calls to capture invocations.
type catFileDouble struct {
	content string
	err     error
	calls   *[]gitCall
}

func newCatFileDouble(content string, calls *[]gitCall) *catFileDouble {
	return &catFileDouble{content: content, calls: calls}
}

func newCatFileErr(err error) *catFileDouble {
	return &catFileDouble{err: err}
}

func (c *catFileDouble) CatFile(_ context.Context, rev, path string) (gitx.CatFileResult, error) {
	if c.calls != nil {
		*c.calls = append(*c.calls, gitCall{rev: rev, path: path})
	}
	if c.err != nil {
		return gitx.CatFileResult{}, c.err
	}
	return gitx.CatFileResult{BlobSha: strings.Repeat("a", 40), Content: c.content}, nil
}

// strp returns a pointer to s (for optional string fields).
func strp(s string) *string { return &s }
