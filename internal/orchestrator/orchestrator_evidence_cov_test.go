package orchestrator_test

// Level-4 retrieval-evidence coverage (ARCH §4.5, P11-T04): a change that
// modifies an OpenAPI document, a key/value config file and an UNPARSEABLE
// OpenAPI stub drives deriveRetrievalEvidence through its parse
// (parseStructuredContent JSON + YAML), delta (ClassifyOpenApiDelta /
// DiffConfigKeys) and merge (mergeApiDeltas + compareText) arms, and the
// declared Unparseable>0 version gap — all asserted through observable
// run-record outcome fields on the caller-owned payments fixture.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

const (
	covOpenAPIJSON = "src/api/openapi.json"
	covConfigYAML  = "config/settings.yaml"
	covBrokenSpec  = "src/api/wip/openapi.yaml"
	// A SECOND valid OpenAPI document: two valid classifications make
	// mergeApiDeltas merge + sort their deltas (compareText), unlike one file.
	covV3Spec = "src/api/v3/openapi.yaml"
	// A config file whose HEAD content is invalid as both JSON and YAML, and a
	// config file whose content parses as a scalar (DiffConfigKeys then fails).
	covBadYAML     = "config/broken.yaml"
	covScalarYAML  = "config/scalar.yaml"
	covBadBaseYAML = "config/badbase.yaml"
	covAddedYAML   = "config/added.yaml"
)

func covOpenAPIBase() string {
	return `{"openapi":"3.0.0","info":{"title":"Payments API","version":"1.0.0"},` +
		`"paths":{"/widgets":{"get":{"responses":{"200":{"description":"list widgets"}}}},` +
		`"/gadgets":{"get":{"responses":{"200":{"description":"list gadgets"}}}},` +
		`"/legacy":{"get":{"responses":{"200":{"description":"legacy"}}}}}}`
}

// covOpenAPIHead removes /gadgets (breaking) and adds /doohickeys (non-breaking)
// — a ≥2-delta OpenAPI change that survives the merge/sort lane.
func covOpenAPIHead() string {
	return `{"openapi":"3.0.0","info":{"title":"Payments API","version":"2.0.0"},` +
		`"paths":{"/widgets":{"get":{"responses":{"200":{"description":"list widgets"}}}},` +
		`"/doohickeys":{"get":{"responses":{"200":{"description":"list doohickeys"}}}}}}`
}

func covConfigBase() string { return "feature_beta: false\nretry_count: 3\ntimeout_ms: 1000\n" }
func covConfigHead() string { return "feature_beta: true\nretry_count: 5\ntimeout_ms: 1000\n" }

// covEvidenceRepo writes the base tree (payments fixture + openapi JSON + config
// YAML + an unparseable openapi stub), commits, then modifies every file at
// head — the level-4 evidence material.
func covEvidenceRepo(t *testing.T) (*testkit.PaymentsPipeline, string, string) {
	t.Helper()
	pipeline := orMatrixRepo(t)

	require.NoError(t, pipeline.Write(covOpenAPIJSON, covOpenAPIBase()))
	require.NoError(t, pipeline.Write(covConfigYAML, covConfigBase()))
	require.NoError(t, pipeline.Write(covBrokenSpec, "note: draft\n")) // parses as YAML, lacks openapi
	require.NoError(t, pipeline.Write(covV3Spec, "openapi: 3.0.0\ninfo:\n  title: V3\n  version: 1.0.0\npaths:\n  /alpha:\n    get:\n      responses:\n        '200': {description: a}\n"))
	require.NoError(t, pipeline.Write(covBadYAML, "valid: true\n"))
	require.NoError(t, pipeline.Write(covScalarYAML, "alpha\n"))
	require.NoError(t, pipeline.Write(covBadBaseYAML, "{{{ this is not json or yaml\n")) // invalid at base

	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)

	require.NoError(t, pipeline.Write(covOpenAPIJSON, covOpenAPIHead()))
	require.NoError(t, pipeline.Write(covConfigYAML, covConfigHead()))
	require.NoError(t, pipeline.Write(covBrokenSpec, "note: draft v2\n"))     // still fails asApiDoc
	require.NoError(t, pipeline.Write(covBadYAML, "foo: [unterminated\n"))    // invalid JSON + YAML
	require.NoError(t, pipeline.Write(covScalarYAML, "beta\n"))               // parses as a scalar; DiffConfigKeys fails
	require.NoError(t, pipeline.Write(covBadBaseYAML, "recovered: true\n"))   // base was unparseable → before-parse Unparseable
	require.NoError(t, pipeline.Write(covAddedYAML, "added_at_head: true\n")) // status=added → evidence skips it
	// A behavioural source change keeps the payments doc on the CONTINUE route.
	refund, err := pipeline.Read(orMatrixRefundSource)
	require.NoError(t, err)
	refundV2 := strings.Replace(refund, "\tidempotencyKey: string;", "\tidempotencyKey?: string;", 1)
	require.NotEqual(t, refund, refundV2)
	require.NoError(t, pipeline.Write(orMatrixRefundSource, refundV2))

	head, err := pipeline.CommitPaths([]string{covOpenAPIJSON, covConfigYAML, covBrokenSpec, covV3Spec, covBadYAML, covScalarYAML, covBadBaseYAML, covAddedYAML, orMatrixRefundSource}, "feat: openapi + config changes")
	require.NoError(t, err)
	return pipeline, base, head
}

// TestCOV_ORCH_401_RetrievalDerivesStructuredEvidence drives the level-4
// evidence derivation: the changed OpenAPI JSON and config YAML are read,
// parsed (JSON + YAML paths of parseStructuredContent) and delta-classified,
// while the unparseable OpenAPI stub is counted and surfaced as a declared
// content-unavailable version gap (D14).
func TestCOV_ORCH_401_RetrievalDerivesStructuredEvidence(t *testing.T) {
	orRequireGit(t)
	pipeline, base, head := covEvidenceRepo(t)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{Kind: orchestrator.WriterResultNoop, Detail: "test"})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	retrieval := orMatrixStage(outcome, orchestrator.StageRetrieval)
	require.NotNil(t, retrieval)
	assert.Equal(t, schemas.RunStageOk, retrieval.Status, "the retrieval stage completes with evidence derived")

	// The unparseable OpenAPI stub is a declared, counted gap.
	deg := covDegradation(outcome, string(orchestrator.StageRetrieval), schemas.DegradationContentUnavailable)
	require.NotNil(t, deg, "unparseable API/config files are reported as a content-unavailable gap")
	assert.Contains(t, deg.Detail, "could not be read/parsed")
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)

	// The doc whose mapping covers the changed OpenAPI document
	// (docs/architecture.md ← src/**) is surfaced as a retrieval candidate.
	require.NotNil(t, covCandidateFor(outcome, orMatrixArchDoc), "the doc mapped to src/** is a retrieval candidate")
}

func covCandidateFor(outcome *schemas.RunOutcome, docPath string) *schemas.RunCandidate {
	for i := range outcome.Candidates {
		if outcome.Candidates[i].DocPath == docPath {
			return &outcome.Candidates[i]
		}
	}
	return nil
}
