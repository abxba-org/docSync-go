package workspace

// Ported from core/src/workspace/duties.ts (P3-T05; ARCH §4.7) — the writer's
// five-duty output contract + the SINGLE canonical writer prompt source + the
// lightweight pre-schema output check.
//
// Binding:
//   - The five duties are the VERBATIM ARCH §4.7 texts; duty 5 is MANDATORY
//     OUTPUT, not decoration — the evidence explanation is what the verifier
//     (§4.8) and the human reviewer consume and what makes a patch auditable
//     after the fact.
//   - Invariant 12 phrasing (whole-document rewrites are rejected at
//     verification — minimal diffs only) is part of the prompt.
//   - Lock layer 2 (§11.4): the prompt states that the workspace marks
//     protected paths read-only and that patch emission for protected targets
//     is forbidden; it also forbids patches for source files (invariant 2).
//   - BuildWriterSystemPrompt is the ONLY writer prompt text in the codebase:
//     the writer adapter (next wave) calls it and must NOT duplicate or
//     re-state duty/lock text in @docsync/openrouter role prompts.
//   - ValidateWriterOutputContract is a DELIBERATELY LIGHT structural
//     pre-check (object with doc_path + hunks array) that the adapter runs
//     BEFORE schema validation. FULL validation is schemas.ParseDocPatch
//     (finalized, P3-T06 — hunks min(1) means an empty patch is a protocol
//     violation downstream; writers abstain upstream); this module
//     intentionally does NOT duplicate the schema: one schema source of truth
//     (OD-2), no drift surface here.
//   - DRIFT GUARD: the light contract's verdicts are pinned to the shared
//     committed fixture packages/schemas/test/fixtures/writer-output-contract.fixture.json
//     — the core AND @docsync/openrouter (mirrored copy) test suites assert
//     identical verdicts for the same representative outputs.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// FiveDuties holds the five duties, verbatim ARCH §4.7 texts, in order (TS
// FIVE_DUTIES).
var FiveDuties = []string{
	"Verify whether the docs are actually stale.",
	"Inspect the exact affected section.",
	"Determine the minimum correct change.",
	"Produce a patch rather than rewrite the entire document.",
	"Explain the evidence behind the patch.",
}

// PatchNotRewriteRule is the invariant-12 verbatim phrasing — part of the
// canonical prompt.
const PatchNotRewriteRule = "Whole-document rewrites are rejected at verification — minimal diffs only."

// ProtectedPathsRule is the lock-layer-2 (P3-T04) verbatim phrasing — part of
// the canonical prompt.
const ProtectedPathsRule = "The workspace marks protected paths read-only; patch emission for protected targets is forbidden."

// SourceFilesAnalysisOnlyRule is the invariant-2 phrasing: source files are
// analysis-only.
const SourceFilesAnalysisOnlyRule = "Source files in the workspace are present for analysis only: never emit a patch for a source file or for any path outside the writable allowlist."

// DutyFiveMandatoryOutput is the duty-5 mandatory-output statement (ARCH
// §4.7, adapted for the prompt).
const DutyFiveMandatoryOutput = "Duty 5 is mandatory output, not decoration: the evidence explanation is what the verifier and the human reviewer consume, and what makes a patch auditable after the fact. A patch without its evidence explanation is invalid output."

// DocPatchOutputRule is the structured-output requirement (DocPatch shape;
// schema authority: @docsync/schemas, finalized P3-T06).
const DocPatchOutputRule = "Emit exactly one JSON object conforming to the docsync.doc-patch/v1 DocPatch schema: doc_path (the single target document) and hunks (an array of anchored, minimal delete/insert hunks), together with the evidence that supports the patch. Schema validation is applied downstream; output that fails it is rejected."

// writerIntro is the fixed opening paragraph of the canonical prompt (the TS
// literal, kept verbatim).
const writerIntro = "You are the docSync documentation patch writer (ARCH §4.7). You work ONLY inside the constrained workspace you are given: it is your entire universe — anything not in it cannot be patched, and anything outside it does not exist for you."

// BuildWriterSystemPromptInput is the optional prompt input (TS
// BuildWriterSystemPromptInput). Both fields are optional; nil means absent.
type BuildWriterSystemPromptInput struct {
	// DutiesText optionally replaces the rendered duties block. It MUST still
	// contain every duty string verbatim (fail closed) — the five-duty
	// contract cannot be weakened by an override.
	DutiesText *string
	// ExtraInstructions holds additional adapter-level instructions,
	// appended verbatim after the fixed text.
	ExtraInstructions *string
}

// renderDutiesBlock renders the numbered duties block (TS renderDutiesBlock).
func renderDutiesBlock() string {
	lines := make([]string, 0, len(FiveDuties)+1)
	lines = append(lines, "Your five duties, in order:")
	for index, duty := range FiveDuties {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, duty))
	}
	return strings.Join(lines, "\n")
}

// BuildWriterSystemPrompt builds the canonical writer system prompt (P3-T05).
// Deterministic. Contains: the five duties verbatim, the duty-5
// mandatory-output statement, the patch-not-rewrite prohibition (invariant
// 12), the lock-layer-2 protected paths rule, the source-files analysis-only
// rule (invariant 2), and the structured DocPatch output requirement. The
// workspace — not this prompt — is injected separately as untrustedContent by
// the adapter.
//
// Fail-closed override check: a DutiesText override that does not contain
// every duty verbatim returns a docutil coded error carrying the exact TS
// TypeError message (the five-duty contract cannot be weakened).
func BuildWriterSystemPrompt(input BuildWriterSystemPromptInput) (string, error) {
	dutiesBlock := renderDutiesBlock()
	if input.DutiesText != nil {
		for _, duty := range FiveDuties {
			if !strings.Contains(*input.DutiesText, duty) {
				return "", docutil.ConfigError(
					"dutiesText override must contain every duty verbatim — missing: %q", duty)
			}
		}
		dutiesBlock = *input.DutiesText
	}

	parts := []string{
		writerIntro,
		dutiesBlock,
		DutyFiveMandatoryOutput,
		PatchNotRewriteRule + " Produce a patch rather than rewrite the entire document; a patch that rewrites a document wholesale is invalid output.",
		ProtectedPathsRule,
		SourceFilesAnalysisOnlyRule,
		DocPatchOutputRule,
	}
	if input.ExtraInstructions != nil {
		parts = append(parts, *input.ExtraInstructions)
	}
	return strings.Join(parts, "\n\n"), nil
}

// WriterOutputContractResult is the result of the lightweight structural
// output check (TS WriterOutputContractResult). Ok is true only when no
// violation was collected; Violations lists every violation found (empty for
// ok).
type WriterOutputContractResult struct {
	Ok         bool
	Violations []string
}

// ValidateWriterOutputContract is the lightweight structural pre-check of the
// writer's raw output (P3-T05): the output MUST be a plain object carrying a
// nonempty-string doc_path and a hunks ARRAY (empty is structurally permitted
// HERE — the light pre-check decides shape only; the schema's hunks min(1)
// rejects empty patches downstream, and a stale-not-confirmed writer abstains
// upstream, it does not emit an empty patch). FULL validation is
// schemas.ParseDocPatch — the adapter must run BOTH: this check first (cheap,
// fails fast on non-JSON shapes), the schema second (single source of truth,
// OD-2). DRIFT GUARD: verdicts pinned to the shared committed fixture
// packages/schemas/test/fixtures/writer-output-contract.fixture.json — the
// core and openrouter suites assert identical verdicts for the same outputs.
//
// The generic tree (map[string]any / []any / string / bool / float64 /
// json.Number / nil) is what a JSON decode of the raw writer output produces,
// so the verdicts match the TS check over the same document.
func ValidateWriterOutputContract(output any) WriterOutputContractResult {
	violations := []string{}
	record, ok := output.(map[string]any)
	if !ok {
		return WriterOutputContractResult{
			Ok:         false,
			Violations: []string{"output must be a JSON object (not a primitive, array, or null)"},
		}
	}
	docPath, hasDocPath := record["doc_path"]
	if !hasDocPath {
		violations = append(violations, "output is missing 'doc_path'")
	} else if text, isString := docPath.(string); !isString || text == "" {
		violations = append(violations, "'doc_path' must be a nonempty string")
	}
	hunks, hasHunks := record["hunks"]
	if !hasHunks {
		violations = append(violations, "output is missing 'hunks'")
	} else if _, isArray := hunks.([]any); !isArray {
		violations = append(violations, "'hunks' must be an array")
	}
	if len(violations) == 0 {
		return WriterOutputContractResult{Ok: true}
	}
	return WriterOutputContractResult{Ok: false, Violations: violations}
}
