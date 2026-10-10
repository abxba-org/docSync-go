// Package feedback is the Go port of the Phase 10 feedback module
// (P10-T02..T06; ARCH §8.7; CTX §22, §23): the RAW reviewer-signal recorder,
// the label-hygiene normalizer, the FN/FP capture flows and the override
// aggregation that turns repeated identical corrections into counted evidence
// and a QUEUED policy proposal (INV-7: feedback accelerates proposals, it never
// bypasses evaluation).
//
// Ported files (one writer per file, per CONVENTIONS.md):
//
//	sink.go       core/src/feedback/sink.ts       — append-only JSONL sinks,
//	                                                  the feedback error classes
//	                                                  (EDOCSYNC_FEEDBACK_SINK /
//	                                                  EDOCSYNC_FEEDBACK_INVALID)
//	                                                  and the OD-R22 row paths
//	recorder.go   core/src/feedback/recorder.ts   — createFeedbackRecorder,
//	                                                  record + fail-closed list
//	hygiene.go    core/src/feedback/hygiene.ts    — the ARCH §8.7 hygiene table
//	                                                  (normalized ONCE, before any
//	                                                  counting), the label store
//	                                                  and the canonical FP
//	                                                  reject reason
//	fnfp.go       core/src/feedback/fnfp.ts       — false-negative detection and
//	                                                  false-positive capture
//	overrides.go  core/src/feedback/overrides.ts  — override aggregation into
//	                                                  counted evidence + queued
//	                                                  policy proposals
//
// The TS barrel (core/src/feedback/index.ts) and its wiring into
// packages/core/src/index.ts are NOT ported here: barrels are
// orchestrator-owned. Consumers import this package directly — exactly like the
// TS tests imported the module by relative path.
//
// Everything is data-in/data-out with injected identity and time: `event_id`,
// `label_id` and `proposal_id` default to canonical ULIDs
// (docutil.ULIDGenerator — monotonic, never a wall-clock read inside the
// package), while `at` / `normalized_at` / `created_at` are ALWAYS supplied by
// the caller's clock value (there is no `new Date()` anywhere in the TS module
// and no `time.Now()` here).
//
// Fail-closed discipline (preserved everywhere): every row is validated against
// its registered schema BEFORE persistence and re-validated on read; a
// malformed row is refused, never stored and never skipped; a row that cannot
// be appended is retried exactly once and then surfaced loudly
// (EDOCSYNC_FEEDBACK_SINK) — unrecordable feedback is lost evaluation data.
//
// SWAP POINT (orchestrator rewiring, NOT this unit's edit):
// internal/analysis/generated.go currently carries an INLINE port of the
// recorder's record/list surface as a documented stopgap:
//
//	analysis.FeedbackEventInput      → feedback.FeedbackEventInput
//	analysis.RecordFeedbackEvent     → (*feedback.FeedbackRecorder).Record
//	analysis.ListFeedbackEvents      → (*feedback.FeedbackRecorder).List
//	analysis.FeedbackRecorderOptions → feedback.FeedbackRecorderOptions
//	analysis.FeedbackEventsFilename  → feedback.FEEDBACK_EVENTS_FILENAME
//	analysis.FeedbackDirName         → feedback.FEEDBACK_DIR_NAME
//
// and its private recorder helpers (newJSONLSink / jsonlSink.append /
// jsonlSink.readAll / parseFeedbackEventRow / feedbackEventSchemaID /
// appendLine / marshalJSONLine / truncateForMessage) collapse onto
// feedback.CreateJsonlSink / feedback.CreateFeedbackRecorder once
// analysis.RecordAnalyzerMisprediction delegates here. This package is the real
// home of that surface; generated.go stays untouched until the orchestrator
// rewires it.
package feedback
