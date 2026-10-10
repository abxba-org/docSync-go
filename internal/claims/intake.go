package claims

// Ported from core/src/claims/intake.ts: hand-anchored claim intake
// (P1-T11; ARCH §4.3; OD-R36). V1 claims are HAND-ANCHORED /
// MANIFEST-DECLARED: "manifest-declared" here means declared in the
// manifest-ADJACENT canonical `concepts.yml` (D1 layout, OD-R36) — the
// schema'd `docsync.concepts/v1` file sitting next to `manifest.yml` in
// `.docsync/`, not inside `manifest.yml` itself. Automated claim extraction
// is V2 and proposal-only.
//
// Intake is a CONFIG-error domain (concepts.yml is human-owned, D8 fail
// closed): every problem — missing doc content, out-of-range lines, empty
// span, anchor file mismatch, duplicate ids — is a typed IntakeError, never
// a silent skip and never a guess. Anchors: when a declaration omits
// `content_hash`, intake computes it from the span at the declared lines
// (content-hash-first, ARCH §4.3); a declared hash is trusted as-is (the
// human pinned it). Claims are created through the ClaimStore CAS-create
// (explicit null expectation): a claim that already exists is skipped
// (idempotent re-runs), a concurrent creator conflicts (cas_conflict —
// caller re-runs).
//
// Intake-created claims start `verified` (the anchor is fresh by
// construction: the hash was computed from the live span) with a provenance
// history event naming the run. The only impurity is the optional Now clock
// injection (tests inject a fixed stamp).

import (
	"fmt"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// IntakeErrorKind is the typed config-error taxonomy (fail closed).
type IntakeErrorKind string

const (
	IntakeErrDocContentUnavailable IntakeErrorKind = "doc_content_unavailable"
	IntakeErrLinesOutOfRange       IntakeErrorKind = "lines_out_of_range"
	IntakeErrEmptySpan             IntakeErrorKind = "empty_span"
	IntakeErrAnchorFileMismatch    IntakeErrorKind = "anchor_file_mismatch"
	IntakeErrDuplicateClaimID      IntakeErrorKind = "duplicate_claim_id"
	IntakeErrCASConflict           IntakeErrorKind = "cas_conflict"
)

// IntakeError is one typed configuration error. DocPath names the
// concepts.yml doc entry the error belongs to; ClaimID is empty when the
// error has no single claim (doc_content_unavailable).
type IntakeError struct {
	Kind    IntakeErrorKind
	DocPath string
	ClaimID string
	Message string
}

// IntakeReport is the outcome of one intake run.
type IntakeReport struct {
	// Created: claims created by this intake run.
	Created int
	// SkippedExisting: declarations skipped because their id already exists
	// in the store.
	SkippedExisting int
	// Errors: typed configuration errors (fail closed — nothing
	// half-applied per claim).
	Errors []IntakeError
}

// IntakeClaimInput is the intake input bag.
type IntakeClaimInput struct {
	Concepts schemas.ConceptsConfig
	// DocContents returns the document's current content, or nil when
	// unavailable.
	DocContents func(docPath string) *string
	RunID       string
	Store       *ClaimStore
	// Now is the clock injection (ISO-8601 stamp for history events); nil
	// means the real clock.
	Now func() string
}

// wallClockNow is the default Now: the real UTC clock rendered exactly like
// the TS `new Date().toISOString()` (millisecond precision, Z suffix).
func wallClockNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// IntakeClaims intakes every hand-anchored claim declared in the concepts
// config. Per claim, fail-closed validation order: doc content exists →
// anchor file matches the declaring doc → lines in range → span non-empty →
// duplicate id check (against the store AND within the batch) → CAS-create.
// A claim with ANY error is not written; valid claims proceed independently.
func IntakeClaims(input IntakeClaimInput) (IntakeReport, error) {
	now := input.Now
	if now == nil {
		now = wallClockNow
	}
	at := now()
	if strings.TrimSpace(at) == "" {
		return IntakeReport{}, state.StateKeyError("intake requires a nonempty ISO-8601 timestamp (now())")
	}
	errors := make([]IntakeError, 0)
	created := 0
	skippedExisting := 0

	seenIDs := map[string]bool{}

	for docIndex, entry := range input.Concepts.Docs {
		docContent := input.DocContents(entry.Path)
		if docContent == nil {
			errors = append(errors, IntakeError{
				Kind:    IntakeErrDocContentUnavailable,
				DocPath: entry.Path,
				Message: fmt.Sprintf(`doc "%s" is not readable at intake time — refusing (fail closed; concepts.yml is human-owned config)`, entry.Path),
			})
			continue
		}
		if len(entry.Claims) == 0 {
			continue
		}
		lines := SplitDocLines(*docContent)

		for claimIndex, declaration := range entry.Claims {
			pointer := fmt.Sprintf("docs[%d].claims[%d]", docIndex, claimIndex)
			if seenIDs[declaration.ID] {
				errors = append(errors, IntakeError{
					Kind:    IntakeErrDuplicateClaimID,
					DocPath: entry.Path,
					ClaimID: declaration.ID,
					Message: fmt.Sprintf(`claim id "%s" (%s) is declared more than once — claim ids are unique across concepts.yml`, declaration.ID, pointer),
				})
				continue
			}
			seenIDs[declaration.ID] = true

			if declaration.Anchor.File != entry.Path {
				errors = append(errors, IntakeError{
					Kind:    IntakeErrAnchorFileMismatch,
					DocPath: entry.Path,
					ClaimID: declaration.ID,
					Message: fmt.Sprintf(`%s: anchor file "%s" does not match the declaring doc "%s"`, pointer, declaration.Anchor.File, entry.Path),
				})
				continue
			}

			startLine := declaration.Anchor.StartLine
			endLine := declaration.Anchor.EndLine
			inRange := startLine >= 1 && endLine >= startLine && endLine <= len(lines)
			if !inRange {
				errors = append(errors, IntakeError{
					Kind:    IntakeErrLinesOutOfRange,
					DocPath: entry.Path,
					ClaimID: declaration.ID,
					Message: fmt.Sprintf(`%s: declared lines %d-%d are outside "%s" bounds (1-%d)`, pointer, startLine, endLine, entry.Path, len(lines)),
				})
				continue
			}

			spanText := strings.Join(lines[startLine-1:endLine], "\n")
			if strings.TrimSpace(spanText) == "" {
				errors = append(errors, IntakeError{
					Kind:    IntakeErrEmptySpan,
					DocPath: entry.Path,
					ClaimID: declaration.ID,
					Message: fmt.Sprintf(`%s: the span at lines %d-%d is empty (whitespace only) — a claim must anchor non-empty content`, pointer, startLine, endLine),
				})
				continue
			}

			contentHash := declaration.Anchor.ContentHash
			if contentHash == nil {
				computed := HashSpan(spanText)
				contentHash = &computed
			}
			historyEvent := schemas.ClaimHistoryEvent{
				At:    at,
				RunID: input.RunID,
				Event: fmt.Sprintf("intake: hand-anchored/manifest-declared claim created from concepts.yml (%s)", pointer),
			}
			claim := schemas.Claim{
				ID:   declaration.ID,
				Text: declaration.Text,
				Anchor: schemas.ClaimAnchor{
					File:        entry.Path,
					StartLine:   startLine,
					EndLine:     endLine,
					ContentHash: *contentHash,
				},
				Source:  declaration.Source,
				Concept: declaration.Concept,
				Status:  schemas.ClaimStatusVerified,
				History: []schemas.ClaimHistoryEvent{historyEvent},
			}

			existing, err := input.Store.Get(declaration.ID)
			if err != nil {
				return IntakeReport{}, err
			}
			if existing != nil {
				skippedExisting++
				continue
			}
			// CAS-create: the explicit null expectation refuses to overwrite
			// a claim a concurrent writer created between the read and here.
			write, err := input.Store.Upsert(claim, &ClaimUpsertOptions{})
			if err != nil {
				return IntakeReport{}, err
			}
			if !write.OK {
				errors = append(errors, IntakeError{
					Kind:    IntakeErrCASConflict,
					DocPath: entry.Path,
					ClaimID: declaration.ID,
					Message: fmt.Sprintf(`%s: CAS conflict while creating claim "%s" — a concurrent writer created it; re-run intake`, pointer, declaration.ID),
				})
				continue
			}
			created++
		}
	}

	return IntakeReport{Created: created, SkippedExisting: skippedExisting, Errors: errors}, nil
}
