package provenance

// Ported from core/src/provenance/secret-scan.ts (P9-T01/T07; ARCH §12.5
// governance rule 4 — a provenance record is NOT a secret container: no
// tokens, no prompt dumps, no reviewer identities beyond repo exposure).
// Every store write passes this gate BEFORE any byte hits disk — a dirty
// record is rejected pre-write.
//
// Pattern classes (each tested, V-P9-7):
//   - token-like strings: GitHub `gh[posur]_…` personal/access tokens,
//     OpenAI/OpenRouter-style `sk-…` keys, `Bearer …` header values,
//     `OPENROUTER_API_KEY=<value>` assignments;
//   - long base64/hex runs (≥40 chars) that are NOT the record's own
//     `source_commit` (the sha is legitimately 40-hex and allowed);
//   - PEM key material (`-----BEGIN …`);
//   - prompt-dump markers (`system prompt`, `full transcript` — a provenance
//     record pins evidence, it never ships prompts);
//   - reviewer identity: email addresses.
//
// Precedent: core `redactGitHubToken` (gitpr/github-rest-platform.ts), the
// CLI's `assertNoSecretsUnderDocsync` sweep, and openrouter `REDACTED`. This
// gate is deliberately conservative — a false positive lands in REVIEW (less
// automation, D14); a false negative would leak a secret into the committed
// state tree.

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CodeProvenanceSecret is the closest registered docutil code for a record
// refused by the write-path secret gate (CONVENTIONS.md §Errors; the TS class
// code EDOCSYNC_PROVENANCE_SECRET is not in the code list — class identity is
// preserved by the ProvenanceSecretError TYPE).
const CodeProvenanceSecret = docutil.CodeGuard

// ProvenanceSecretScan is the scan outcome — the value shape of the TS
// discriminated union: Ok is the union tag, and Findings (empty when Ok) is
// the TS ProvenanceSecretScanFindings payload.
type ProvenanceSecretScan struct {
	Ok       bool
	Findings []string
}

// ProvenanceSecretError is the Go port of the TS ProvenanceSecretError class: a
// record refused by the write-path secret scan (INV-8).
type ProvenanceSecretError struct {
	// Findings is the pattern-class list, exactly as the TS class carried it.
	Findings []string
	coded    *docutil.Error
}

func (e *ProvenanceSecretError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *ProvenanceSecretError) Unwrap() error { return e.coded }

// Code returns the coded-error code for this refusal (EDOCSYNC_GUARD).
func (e *ProvenanceSecretError) Code() string { return CodeProvenanceSecret }

// Pattern classes. Go's regexp needs no `g` flag (FindAllString is global);
// every pattern is anchored and quantified exactly as in the TS module, and
// the `i` flag becomes an inline `(?i)`.
var (
	ghTokenRegex              = regexp.MustCompile(`gh[posur]_[A-Za-z0-9]{10,}`)
	skTokenRegex              = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`)
	bearerRegex               = regexp.MustCompile(`\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	openRouterKeyValueRegex   = regexp.MustCompile(`OPENROUTER_API_KEY\\?"?\s*[:=]\s*\\?"?[A-Za-z0-9._-]{8,}`)
	pemRegex                  = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----`)
	systemPromptRegex         = regexp.MustCompile(`(?i)system\s+prompt`)
	transcriptRegex           = regexp.MustCompile(`(?i)full\s+transcript`)
	emailRegex                = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	nonRunTokenSeparatorRegex = regexp.MustCompile(`[^A-Za-z0-9+/=]+`)
)

// longRunRegex matches a long base64/hex-ish run (≥40 chars) — the matching
// value is still compared against the record's own source sha by the scanner.
var longRunRegex = regexp.MustCompile(`^[A-Za-z0-9+/]{40,}={0,2}$|^[a-f0-9]{40,}$`)

// excerpt is the redacted excerpt for findings — prefix only, never the full
// value. JS sliced UTF-16 units; runes are the safe equivalent here.
func excerpt(value string) string {
	runes := []rune(value)
	if len(runes) > 6 {
		runes = runes[:6]
	}
	return string(runes) + "…[REDACTED]"
}

// scanText collects every forbidden-pattern hit in text (the record's own
// source_commit is exempt from the long-run rule), appending to findings.
func scanText(text, sourceCommit string, findings *[]string) {
	for _, match := range ghTokenRegex.FindAllString(text, -1) {
		*findings = append(*findings, "token-like string (GitHub gh[posur]_ token): "+excerpt(match))
	}
	for _, match := range skTokenRegex.FindAllString(text, -1) {
		*findings = append(*findings, "token-like string (sk- API key): "+excerpt(match))
	}
	for _, match := range bearerRegex.FindAllString(text, -1) {
		*findings = append(*findings, "token-like string (Bearer credential): "+excerpt(match))
	}
	for _, match := range openRouterKeyValueRegex.FindAllString(text, -1) {
		*findings = append(*findings, "token-like string (OPENROUTER_API_KEY value): "+excerpt(match))
	}
	for _, match := range pemRegex.FindAllString(text, -1) {
		*findings = append(*findings, "key material (PEM block): "+excerpt(match))
	}
	for _, match := range systemPromptRegex.FindAllString(text, -1) {
		*findings = append(*findings, "prompt-dump marker ("+match+") — records never ship prompts")
	}
	for _, match := range transcriptRegex.FindAllString(text, -1) {
		*findings = append(*findings, "prompt-dump marker ("+match+") — records never ship transcripts")
	}
	for _, match := range emailRegex.FindAllString(text, -1) {
		*findings = append(*findings, "reviewer identity (email address): "+excerpt(match))
	}
	// Long base64/hex runs — the record's own 40-hex source_commit is exempt.
	for _, token := range nonRunTokenSeparatorRegex.Split(text, -1) {
		if len(token) >= 40 && longRunRegex.MatchString(token) && token != sourceCommit {
			*findings = append(*findings, "long opaque base64/hex run ("+strconv.Itoa(len(token))+" chars): "+excerpt(token))
		}
	}
}

// ScanProvenanceRecord scans the serialized record for every forbidden pattern
// class. Pure: the same record always yields the same findings. The
// serialization error (impossible for a record of strings and numbers) is
// propagated rather than swallowed — a record that cannot be scanned is never
// treated as clean (fail closed).
func ScanProvenanceRecord(record schemas.Provenance) (ProvenanceSecretScan, error) {
	text, err := serializeForScan(record)
	if err != nil {
		return ProvenanceSecretScan{}, err
	}
	var findings []string
	scanText(text, record.SourceCommit, &findings)
	if len(findings) == 0 {
		return ProvenanceSecretScan{Ok: true}, nil
	}
	return ProvenanceSecretScan{Ok: false, Findings: findings}, nil
}

// AssertNoSecrets is the exception-style gate the store uses BEFORE any write
// (V-P9-7): a record that is not a clean evidence container never touches disk
// (ARCH §12.5).
func AssertNoSecrets(record schemas.Provenance) error {
	scan, err := ScanProvenanceRecord(record)
	if err != nil {
		return err
	}
	if scan.Ok {
		return nil
	}
	return &ProvenanceSecretError{Findings: scan.Findings, coded: docutil.New(CodeProvenanceSecret,
		"provenance record refused by the write-path secret scan ("+
			strconv.Itoa(len(scan.Findings))+" finding(s)) — a provenance record is not a secret container (ARCH §12.5)")}
}

// serializeForScan renders the record the way the TS scan did: JSON.stringify
// of the record (no spaces, declaration key order — which the Go struct field
// order reproduces exactly).
func serializeForScan(record schemas.Provenance) (string, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return "", docutil.New(docutil.CodeState,
			"cannot serialize a provenance record for the secret scan — refusing (fail closed): "+err.Error())
	}
	return string(raw), nil
}
