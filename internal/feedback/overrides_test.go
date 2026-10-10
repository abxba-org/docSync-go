// Ported from packages/core/test/feedback-overrides.test.ts (P10-T06; ARCH
// §8.7, §11.4; CTX §23; F10-4, INV-7): repeated identical corrections aggregate
// into counted evidence and a QUEUED proposal artifact; the click is
// acknowledged instantly, the policy change arrives later as a reviewable
// proposal — never bypassing evaluation.
//
// Aggregation is a pure function, so no temp state is needed (the TS suite's
// afterEach is a no-op for the same reason).

package feedback_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const overrideAt = "2026-10-08T12:00:00Z"

func feedbackCorrectionRow(mutate func(*schemas.FeedbackEvent)) schemas.FeedbackEvent {
	row := schemas.FeedbackEvent{
		Schema:        "docsync.feedback-event/v1",
		EventID:       "01JAR3TB000000000000000000",
		At:            overrideAt,
		DecisionID:    "d_override",
		PolicyVersion: 17,
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

func feedbackCorrectionRowWithID(id string, mutate func(*schemas.FeedbackEvent)) schemas.FeedbackEvent {
	return feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
		row.EventID = id
		if mutate != nil {
			mutate(row)
		}
	})
}

func TestOverrideAggregation(t *testing.T) {
	t.Run("groups repeated identical corrections (same doc_path + statement) into counted evidence and a QUEUED proposal, acknowledging instantly (INV-7)", func(t *testing.T) {
		rows := []schemas.FeedbackEvent{
			feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("src/generated/** is not documentation-relevant")
				row.DocPath = new("docs/sdk.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000001", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("SRC/Generated/**  is NOT documentation-relevant")
				row.DocPath = new("docs/sdk.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000002", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalAccept
			}),
		}
		acknowledgments, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{Rows: rows, At: overrideAt})
		require.NoError(t, err)
		require.Len(t, acknowledgments, 1)
		ack := acknowledgments[0]
		assert.Equal(t, 2, ack.Count)
		require.NotNil(t, ack.DocPath)
		assert.Equal(t, "docs/sdk.md", *ack.DocPath)
		assert.Equal(t, "src/generated/** is not documentation-relevant", ack.Statement)
		assert.Equal(t, feedback.OVERRIDE_ACKNOWLEDGMENT, ack.Acknowledgment)

		proposal := ack.Proposal
		assert.Equal(t, "docsync.policy-proposal/v1", proposal.Schema)
		assert.Equal(t, schemas.PolicyProposalStatusQueued, proposal.Status)
		assert.Equal(t, schemas.PolicyProposalTypeRoutingRule, proposal.Type)
		assert.Equal(t, 17, proposal.PolicyVersionBasis)
		assert.Equal(t, overrideAt, proposal.CreatedAt)
		assert.True(t, docutil.IsValidULID(proposal.ProposalID))
		require.Len(t, proposal.CountedEvidence, 1)
		assert.Equal(t, schemas.CountedEvidence{
			Statement:    `2/2 identical reviewer overrides on docs/sdk.md: "src/generated/** is not documentation-relevant"`,
			SupportCount: 2,
			TotalCount:   2,
		}, proposal.CountedEvidence[0])
		assert.Equal(t, map[string]any{
			"kind":      "reviewer_override_aggregation",
			"doc_path":  "docs/sdk.md",
			"statement": "src/generated/** is not documentation-relevant",
		}, proposal.Artifact)
	})

	t.Run("emits nothing below the repetition threshold; non-correction signals never aggregate", func(t *testing.T) {
		assert.Equal(t, 2, feedback.OVERRIDE_PROPOSAL_THRESHOLD)

		single, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{
			Rows: []schemas.FeedbackEvent{feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
				row.DocPath = new("docs/api.md")
			})},
			At: overrideAt,
		})
		require.NoError(t, err)
		assert.Empty(t, single)

		nonCorrections, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{
			Rows: []schemas.FeedbackEvent{
				feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
					row.Signal = schemas.FeedbackEventSignalAccept
				}),
				feedbackCorrectionRowWithID("01JAR3TB000000000000000003", func(row *schemas.FeedbackEvent) {
					row.Signal = schemas.FeedbackEventSignalMarkAsIntentional
				}),
			},
			At: overrideAt,
		})
		require.NoError(t, err)
		assert.Empty(t, nonCorrections)
	})

	t.Run("keeps distinct statements and missing doc_path observations in separate buckets", func(t *testing.T) {
		rows := []schemas.FeedbackEvent{
			feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
				row.DocPath = new("docs/a.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000004", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
				row.DocPath = new("docs/b.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000005", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000006", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("Too  VERBOSE")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000007", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalAlwaysIgnore
				row.DocPath = new("docs/a.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000008", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalAlwaysIgnore
				row.DocPath = new("docs/a.md")
			}),
		}
		acknowledgments, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{Rows: rows, At: overrideAt})
		require.NoError(t, err)
		require.Len(t, acknowledgments, 2)

		countByStatement := map[string]int{}
		var missingDocPath *feedback.OverrideAcknowledgment
		for i := range acknowledgments {
			countByStatement[acknowledgments[i].Statement] = acknowledgments[i].Count
			if acknowledgments[i].DocPath == nil {
				missingDocPath = &acknowledgments[i]
			}
		}
		assert.Equal(t, 2, countByStatement["always_ignore"])
		require.NotNil(t, missingDocPath, "the null doc-path bucket must surface its own acknowledgment")
		assert.Equal(t, 2, missingDocPath.Count)
		require.Len(t, missingDocPath.Proposal.CountedEvidence, 1)
		assert.Equal(t, 6, missingDocPath.Proposal.CountedEvidence[0].TotalCount)
		_, hasDocPath := missingDocPath.Proposal.Artifact["doc_path"]
		assert.False(t, hasDocPath, "a statement observed without a doc path never carries one")

		// always_ignore rows carry the literal override marker as statement.
		alwaysIgnore := acknowledgments[1]
		assert.Equal(t, feedback.ALWAYS_IGNORE_STATEMENT, alwaysIgnore.Statement)
		require.NotNil(t, alwaysIgnore.DocPath)
		assert.Equal(t, "docs/a.md", *alwaysIgnore.DocPath)
	})

	t.Run("refuses counted evidence spanning policy versions (fail closed)", func(t *testing.T) {
		rows := []schemas.FeedbackEvent{
			feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
				row.PolicyVersion = 17
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000008", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
				row.PolicyVersion = 18
			}),
		}
		_, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{Rows: rows, At: overrideAt})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Contains(t, invalid.Message, "spans policy versions")
	})

	t.Run("pre-assigned proposal ids are consumed one per queued group, in order", func(t *testing.T) {
		rows := []schemas.FeedbackEvent{
			feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalAlwaysIgnore
				row.DocPath = new("docs/a.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000009", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalAlwaysIgnore
				row.DocPath = new("docs/a.md")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000010", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("too verbose")
			}),
			feedbackCorrectionRowWithID("01JAR3TB000000000000000011", func(row *schemas.FeedbackEvent) {
				row.Signal = schemas.FeedbackEventSignalReject
				row.RejectReason = new("TOO verbose")
			}),
		}
		proposalIDs := []string{"01JAR3TC00000000000000000A", "01JAR3TC00000000000000000B"}
		acknowledgments, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{
			Rows:        rows,
			At:          overrideAt,
			ProposalIDs: proposalIDs,
		})
		require.NoError(t, err)
		require.Len(t, acknowledgments, 2)
		assert.Equal(t, proposalIDs[0], acknowledgments[0].Proposal.ProposalID)
		assert.Equal(t, proposalIDs[1], acknowledgments[1].Proposal.ProposalID)
		// Sub-threshold groups never consume an id (the ids index the QUEUED
		// groups, exactly like the TS acknowledgments.length cursor).
		assert.Equal(t, feedback.ALWAYS_IGNORE_STATEMENT, acknowledgments[0].Statement)
	})
}

func TestNormalizeFeedbackSurfaces(t *testing.T) {
	t.Run("normalizeCorrectionStatement collapses whitespace and case-folds", func(t *testing.T) {
		assert.Equal(t, "same wording", feedback.NormalizeCorrectionStatement("Same   wording "))
		assert.Equal(t, "", feedback.NormalizeCorrectionStatement("  \t\n "))
	})

	t.Run("correctionStatementOf maps the three correction signals and ignores the rest", func(t *testing.T) {
		reject := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalReject
			row.RejectReason = new("Too  VERBOSE")
		})
		assert.Equal(t, "too verbose", feedback.CorrectionStatementOf(reject))

		edit := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalEdit
			row.EditDiff = new("-a\n+b")
		})
		// The statement is the NORMALIZED edit diff (whitespace collapsed,
		// case-folded) — identical edits group together.
		assert.Equal(t, "-a +b", feedback.CorrectionStatementOf(edit))

		alwaysIgnore := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalAlwaysIgnore
		})
		assert.Equal(t, feedback.ALWAYS_IGNORE_STATEMENT, feedback.CorrectionStatementOf(alwaysIgnore))

		accept := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalAccept
		})
		assert.Equal(t, "", feedback.CorrectionStatementOf(accept))

		marked := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalMarkAsIntentional
		})
		assert.Equal(t, "", feedback.CorrectionStatementOf(marked))

		rejectWithoutReason := feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalReject
		})
		assert.Equal(t, "", feedback.CorrectionStatementOf(rejectWithoutReason))
	})

	t.Run("isDocAlreadyCorrect matches only the canonical FP reason", func(t *testing.T) {
		assert.True(t, feedback.IsDocAlreadyCorrect(feedback.DOC_ALREADY_CORRECT_REASON))
		assert.True(t, feedback.IsDocAlreadyCorrect("Doc  already correct"))
		assert.True(t, feedback.IsDocAlreadyCorrect("  DOC ALREADY CORRECT  "))
		assert.False(t, feedback.IsDocAlreadyCorrect("docs are already correct"))
		assert.False(t, feedback.IsDocAlreadyCorrect("already correct"))
		assert.False(t, feedback.IsDocAlreadyCorrect(""))
		assert.Equal(t, "doc already correct", feedback.DOC_ALREADY_CORRECT_REASON)
	})
}
