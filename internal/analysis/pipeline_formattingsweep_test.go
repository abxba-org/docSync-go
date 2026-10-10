package analysis_test

// Ported from packages/core/test/pipeline-formattingsweep.test.ts (P1-T20;
// V-P1-3; ARCH §4.1/§3.7, R10; OD-R38 ruling). A whitespace-only change
// across SEVERAL mapped payment sources (refund.ts, types.ts, client.ts):
//
//   - the ChangeAnalysis carries the `formatting_only` short-circuit hint —
//     the DECISION stage is skipped (zero decision-model budget; no Jev);
//   - the suspect driver SKIPS the VERIFIED→SUSPECT transition for the same
//     reason (OD-R38): formatting-only sweeps must not churn docs into
//     SUSPECT/decision loops. The entry stays transitioned:false with the
//     computed invalidation evidence PLUS a `short-circuit` reason naming the
//     hint and the R10 decision-stage skip, and the doc STAYS VERIFIED;
//   - NO claim-level loseSupport happens anywhere: the claims stay verified
//     with intake-only history — claim transitions are decision-stage work,
//     never structural.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

const pipelineSweepRun = "run_p1t20_sweep"

// formattingVariants bumps the indentation of ONE unique line per mapped file
// (tab → double tab: trim-equal removed/added pairs — pure formatting, nothing
// else changes). Mirrors the TS FORMATTING_VARIANTS over this fixture.
var pipelineFormattingVariants = []struct{ path, from, to string }{
	{"src/payments/refund.ts", "\tidempotencyKey: string;", "\t\tidempotencyKey: string;"},
	{"src/payments/types.ts", "\tid: string;", "\t\tid: string;"},
	{"src/api/payments/client.ts", "\tbaseUrl: string;", "\t\tbaseUrl: string;"},
}

// applyFormattingSweep applies every whitespace-only variant and returns the
// changed paths (TS the per-variant write loop).
func applyFormattingSweep(t *testing.T, pipeline *testkit.PaymentsPipeline) []string {
	t.Helper()
	changed := make([]string, 0, len(pipelineFormattingVariants))
	for _, variant := range pipelineFormattingVariants {
		content, err := pipeline.Read(variant.path)
		require.NoError(t, err)
		require.True(t, strings.Contains(content, variant.from),
			"payments fixture drifted: %s lacks %q", variant.path, variant.from)
		require.NoError(t, pipeline.Write(variant.path,
			strings.Replace(content, variant.from, variant.to, 1)))
		changed = append(changed, variant.path)
	}
	return changed
}

// TestV_P1_3_WhitespaceOnlySweepShortCircuitsWithoutSuspectChurn ports the
// headline formatting-sweep case.
func TestV_P1_3_WhitespaceOnlySweepShortCircuitsWithoutSuspectChurn(t *testing.T) {
	pipeline := newPaymentsPipelineRepo(t)

	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	_, err = pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc(base))

	changed := applyFormattingSweep(t, pipeline)
	head, err := pipeline.CommitPaths(changed, "style: whitespace sweep across mapped sources")
	require.NoError(t, err)

	record := buildPipelineAnalysis(t, pipeline, base, head, nil)

	// Three mapped files changed, each detected formatting-only (per-row flag).
	paths := make([]string, 0, len(record.Files))
	for _, row := range record.Files {
		paths = append(paths, row.Path)
		assert.Equal(t, schemas.FileStatusModified, row.Status)
		require.NotNil(t, row.FormattingOnly)
		assert.True(t, *row.FormattingOnly)
	}
	assert.Equal(t, []string{
		"src/api/payments/client.ts",
		"src/payments/refund.ts",
		"src/payments/types.ts",
	}, paths)

	// V-P1-3 contract: the short-circuit hint skips the DECISION stage —
	// EXACTLY one hint, never duplicated.
	assert.Equal(t, []schemas.ClassificationHint{{
		Kind: schemas.ClassificationHintFormattingOnly,
		Note: "all changed paths are whitespace-equivalent — skip Jev entirely (R10)",
	}}, record.ClassificationHints)
	// Declared V1 gap unchanged (deep analysis still has no symbol coverage).
	requireDeepSkippedV1(t, record)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         pipeline.Store,
		Graph:         pipeline.Graph,
		Analysis:      *record,
		Git:           pipeline.Git,
		Head:          head,
		RunID:         pipelineSweepRun,
		PolicyVersion: testkit.PipelinePolicyVersion,
		At:            pipelineSuspectAt,
		Claims:        pipelineClaims(t, pipeline.Claims),
	})
	require.NoError(t, err)
	assert.Zero(t, report.ModelInvocations)

	// V-P1-3 + OD-R38: the driver SKIPS the VERIFIED→SUSPECT transition for a
	// whole-change formatting_only hint — the doc STAYS VERIFIED (no churn),
	// and the entry reports the evidence plus the short-circuit reason last.
	assert.Equal(t, 0, report.TransitionedCount)
	entry, found := pipelineReportEntry(report, testkit.PaymentsDoc)
	require.True(t, found)
	require.NotNil(t, entry.From)
	require.Nil(t, entry.To)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.False(t, entry.Transitioned)

	// Evidence first (graph-mapping glob wording + baseline literal wording +
	// the content-agnostic claim intersection), the short-circuit note last.
	// The claim intersection is content-agnostic: it still names BOTH claims
	// whose mapped sources changed, despite the change being whitespace-only.
	requirePipelineReasons(t, entry, []baselines.InvalidationReason{
		{
			Kind:   baselines.ReasonClaimSourceChanged,
			Detail: `claims [claim_refund_idempotency] reference changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonClaimSourceChanged,
			Detail: `claims [claim_refund_retry_window] reference changed file "src/api/payments/client.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/api/payments/**" matches changed file "src/api/payments/client.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/api/payments/client.ts" matches changed file "src/api/payments/client.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/**" matches changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/**" matches changed file "src/payments/types.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/refund.ts" matches changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/types.ts" matches changed file "src/payments/types.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonShortCircuit,
			Detail: `whole-change short-circuit hint "formatting_only" — R10 decision-stage skip (no SUSPECT churn for formatting/test-only sweeps)`,
		},
	})

	// No churn: the doc state record keeps its initial VERIFIED entry only.
	docState, err := statemachine.NewDocStateMachine(pipeline.Store).GetState(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, docState)
	assert.Equal(t, schemas.DocStateVerified, docState.State)
	require.Len(t, docState.Transitions, 1)
	assert.Equal(t, schemas.DocStateVerified, docState.Transitions[0].State)
	assert.Equal(t, testkit.PipelineInitRun, docState.Transitions[0].RunID)

	// ZERO claim-level loseSupport: no pipeline stage performs claim
	// transitions — both claims keep their intake-only history and stay
	// verified despite their sources being (formatting-only) changed.
	for _, claimID := range []string{testkit.ClaimIdempotency, testkit.ClaimRetryWindow} {
		claim, err := pipeline.Claims.Get(claimID)
		require.NoError(t, err)
		require.NotNil(t, claim, claimID)
		assert.Equal(t, schemas.ClaimStatusVerified, claim.Status, claimID)
		assert.Len(t, claim.History, 1, "intake event only for %s", claimID)
	}
}
