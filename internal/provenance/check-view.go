package provenance

// Ported from core/src/provenance/check-view.ts (P9-T05; ARCH §12.5; D5 — the
// EPHEMERAL surface). Pure projection of ONE `docsync.provenance/v1` record
// into a `CheckRunOutputPayload`-compatible `{title, summary, text}` for Phase
// 8's check upsert to consume later.
//
// Ephemeral by contract: the payload is marked `ephemeral view` in its text and
// NEVER carries more than the record — deleting the check run loses nothing,
// because the canonical surface is the PR body (P9-T03) and the archival
// surface is `.docsync/state/` (P9-T04). Markdown document bodies are never
// touched (D5 rule 5). No clocks, no I/O, no model calls.

import (
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CheckViewEphemeralMarker is the mandatory marker: every projection text
// carries the D5 ephemeral note.
const CheckViewEphemeralMarker = "ephemeral view — canonical: PR body · archival: .docsync/state (D5)"

// CheckRunOutputPayload is a check-run `output` payload (title/summary/text).
// It is a local mirror of gitpr/check-runs.ts's CheckRunOutputPayload — that
// interface is gitpr-owned and internal/gitpr is not ported yet; when it lands,
// delete this type in favour of the gitpr one (the JSON keys are already
// identical, so the projection stays wire-compatible either way).
type CheckRunOutputPayload struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// ProjectCheckRunMetadata projects the record into check-run metadata (P9-T05).
// Deterministic: same record ⇒ byte-identical payload.
func ProjectCheckRunMetadata(record schemas.Provenance) CheckRunOutputPayload {
	title := "docsync provenance: run " + record.RunID + " (ephemeral view)"
	summary := "docsync provenance for run `" + record.RunID + "` — " + CheckViewEphemeralMarker
	lines := []string{
		"docsync provenance (ephemeral view)",
		"",
		"- Run: `" + record.RunID + "`",
		"- Source commit: `" + record.SourceCommit + "`",
		"- Source files: " + strings.Join(record.SourceFiles, ", "),
		"- Detector: `" + record.Detector.Model + "` (confidence " + fixed2(record.Detector.Confidence) + ")",
		"- Writer: `" + record.Writer.Model + "`",
		"- Verification: `" + string(record.Verification.Status) + "`",
		"- Policy: v" + strconv.Itoa(record.PolicyVersion) + " · Manifest: v" + record.ManifestVersion,
		"- Generated: " + record.GeneratedAt,
		"",
		CheckViewEphemeralMarker,
	}
	return CheckRunOutputPayload{Title: title, Summary: summary, Text: strings.Join(lines, "\n") + "\n"}
}
