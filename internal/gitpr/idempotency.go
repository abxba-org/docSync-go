package gitpr

// Ported from packages/core/src/gitpr/idempotency.ts (P6-T13; PLAN §7.3 "Rate,
// burst, idempotency, lifecycle"; ARCH §12.2; invariant 11).
//
// Idempotency is the no-flood law's second leg: the idempotency key is the ONLY
// handle the upsert has on "the same logical change". Two inputs, deterministically
// mapped:
//
//   - Source-PR runs: `pr-<full-head-sha>-<doc-set-content-hash>` — the same
//     source PR head + the same documentation change ⇒ the same key ⇒ the
//     marker-keyed upsert UPDATES the open PR, never duplicates it.
//   - Run-scoped runs: `local-<run-id>-<doc-set-content-hash>` when a run id is
//     supplied, else the bare `local-<doc-set-content-hash>` head.
//
// The doc-set content hash is what makes the key REVISION-AWARE: it hashes the
// SORTED (doc_path, writer_model, span_hashes) triples through canonical JSON —
// the same content hash internal/state hashes bytes with — so two runs with the
// same documentation effect produce the same key even when their run metadata
// differs, and a REAL doc change produces a new key.
//
// Pure, total over valid input, fail-closed on malformed input (never guesses).
// No clocks, no I/O, no models (P19).

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// LocalKeyHead is the run-scoped key head (TS LOCAL_KEY_HEAD).
const LocalKeyHead = "local"

// CodeIdempotencyKey preserves the TS EDOCSYNC_IDEMPOTENCY_KEY code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const CodeIdempotencyKey = "EDOCSYNC_IDEMPOTENCY_KEY"

// idempotencyKeyError builds the IdempotencyKeyError equivalent (the TS message
// text is preserved verbatim).
func idempotencyKeyError(message string) error {
	return docutil.New(CodeIdempotencyKey, message)
}

var (
	// idemFullShaHex matches a full 40-hex object name (TS FULL_SHA_HEX).
	idemFullShaHex = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	// idemRunIDSafe is the safe run-id subset (TS RUN_ID_SAFE).
	idemRunIDSafe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// idemJSONExcerpt renders the bounded JSON string echo the TS error messages
// embed (JSON.stringify(value.slice(0, 64)) — raw text, \b\f\n\r\t escapes,
// \u00xx for the remaining control characters).
func idemJSONExcerpt(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				const hexDigits = "0123456789abcdef"
				b.WriteByte(hexDigits[(r>>4)&0xf])
				b.WriteByte(hexDigits[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// idemFirstRunes64 slices s to at most 64 runes (the TS `slice(0, 64)` bound on
// echoed input).
func idemFirstRunes64(s string) string {
	runes := []rune(s)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return s
}

// idemCanonicalJSON renders the canonical JSON the hash is taken over (the TS
// canonicalJson): keys sorted recursively, arrays in order — the same bytes
// internal/state hashes.
func idemCanonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(json.RawMessage(raw))
}

// DocSetContentHash hashes the documentation set (TS docSetContentHash):
// sha256 hex over the canonical JSON of `[{ doc_path, writer_model,
// span_hashes: [...] }]` — patches sorted by doc path, span hashes in the
// patch's (schema-guaranteed non-overlapping, line-ascending) hunk order.
// Deterministic and ORDER-INDEPENDENT over patch permutations; line numbers are
// deliberately excluded so the same textual change re-anchored at a shifted
// line keeps the same key. The span hashes are the per-hunk anchor
// `content_hash` values (the span identity).
func DocSetContentHash(patches []schemas.DocPatch) (string, error) {
	entries := make([]map[string]any, 0, len(patches))
	for index := range patches {
		spanHashes := make([]string, 0, len(patches[index].Hunks))
		for _, hunk := range patches[index].Hunks {
			spanHashes = append(spanHashes, hunk.Anchor.ContentHash)
		}
		entries = append(entries, map[string]any{
			"doc_path":     patches[index].DocPath,
			"writer_model": patches[index].Provenance.WriterModel,
			"span_hashes":  spanHashes,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, _ := entries[i]["doc_path"].(string)
		right, _ := entries[j]["doc_path"].(string)
		return left < right
	})
	canonical, err := idemCanonicalJSON(entries)
	if err != nil {
		return "", err
	}
	return docutil.ContentHash(canonical), nil
}

// ComputeIdempotencyKeyInput is the key computation input (TS
// ComputeIdempotencyKeyInput): the resolved source-PR head sha (nil for local
// runs), the doc-set content hash, and the optional local-run disambiguator.
type ComputeIdempotencyKeyInput struct {
	SourcePrHeadSha   *string
	DocSetContentHash string
	// RunID is the optional local-run disambiguator (passthrough): when there is
	// no source PR, `local:<runId>:<hash>` distinguishes distinct local runs;
	// omit for the bare `local:<hash>` form.
	RunID string
}

// ComputeIdempotencyKey derives the stable key the PR client injects as the
// hidden idempotency marker (TS computeIdempotencyKey): `<sha>:<hash>` for
// source-PR runs, `local:<hash>` for run-scoped runs, or
// `local:<runId>:<hash>` when a local run carries a disambiguator. Fail closed
// on malformed input — a non-40-hex sha or an empty hash is a typed
// IdempotencyKeyError, never a silently-degraded key.
func ComputeIdempotencyKey(input ComputeIdempotencyKeyInput) (string, error) {
	hash := input.DocSetContentHash
	if hash == "" {
		return "", idempotencyKeyError("docSetContentHash must be a nonempty string")
	}
	if input.SourcePrHeadSha == nil {
		if input.RunID == "" {
			return LocalKeyHead + ":" + hash, nil
		}
		if !idemRunIDSafe.MatchString(input.RunID) {
			return "", idempotencyKeyError(
				"runId must be a safe branch fragment (nonempty, starting alphanumeric, only " +
					"[A-Za-z0-9._-]; got " + idemJSONExcerpt(idemFirstRunes64(input.RunID)) + ")")
		}
		return LocalKeyHead + ":" + input.RunID + ":" + hash, nil
	}
	sourcePrHeadSha := *input.SourcePrHeadSha
	if !idemFullShaHex.MatchString(sourcePrHeadSha) {
		return "", idempotencyKeyError(
			"sourcePrHeadSha must be a full 40-hex commit sha (got " +
				idemJSONExcerpt(idemFirstRunes64(sourcePrHeadSha)) + ")")
	}
	return strings.ToLower(sourcePrHeadSha) + ":" + hash, nil
}
