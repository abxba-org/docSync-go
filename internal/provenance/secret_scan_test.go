package provenance_test

// Black-box port of packages/core/test/provenance.test.ts, secret-scan half
// (V-P9-7: every forbidden pattern class is flagged; the record's own 40-hex
// source_commit is exempt from the long-run rule; assertNoSecrets throws typed
// and the store refuses pre-write).
//
// Pure: no filesystem, no network, no clocks.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordWithSecret mirrors the TS recordWith helper: a valid record carrying an
// extra source file (the row a dirty value would ride in).
func recordWithSecret(t *testing.T, extraSourceFile string) *schemas.Provenance {
	t.Helper()
	return buildRecord(t, func(in *provenance.BuildProvenanceInput) {
		in.SourceFiles = []string{"src/payments/refund.ts", extraSourceFile}
	})
}

// secretClasses mirrors the TS secretClasses list: one entry per forbidden
// pattern class (V-P9-7).
var secretClasses = []string{
	"ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR",
	"sk-" + strings.Repeat("a", 20),
	"Bearer token12345",
	"OPENROUTER_API_KEY=valor12345678",
	strings.Repeat("A", 48),
	strings.Repeat("f", 48),
	"-----BEGIN RSA PRIVATE KEY-----",
	"see the system prompt below",
	"full transcript of the session",
	"reviewer@example.com",
}

func TestV_P9_7_EachForbiddenPatternClassIsFlagged(t *testing.T) {
	for _, secret := range secretClasses {
		t.Run(secret, func(t *testing.T) {
			scan, err := provenance.ScanProvenanceRecord(*recordWithSecret(t, secret))
			require.NoError(t, err)
			require.False(t, scan.Ok, "secret %q must be flagged", secret)
			assert.NotEmpty(t, scan.Findings)
			for _, finding := range scan.Findings {
				// Findings never carry the secret value itself.
				assert.NotContains(t, finding, secret)
			}
		})
	}
}

func TestV_P9_7_OwnSourceCommitIsExemptFromTheLongRunRule(t *testing.T) {
	// The record's own 40-hex source_commit is legitimately 40 hex.
	scan, err := provenance.ScanProvenanceRecord(*buildRecord(t, nil))
	require.NoError(t, err)
	assert.True(t, scan.Ok)
	assert.Empty(t, scan.Findings)
}

func TestV_P9_7_OwnSourceCommitIsNotExemptAsAnotherRecordsValue(t *testing.T) {
	// A DIFFERENT record's sha riding in source_files is not exempt: the
	// exemption is scoped to the record's own source_commit.
	otherSha := "0123456789abcdef0123456789abcdef01234567"
	scan, err := provenance.ScanProvenanceRecord(*recordWithSecret(t, otherSha))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	require.Len(t, scan.Findings, 1)
	assert.Contains(t, scan.Findings[0], "long opaque base64/hex run (40 chars)")
}

func TestV_P9_7_FindingsNameThePatternClass(t *testing.T) {
	cases := []struct {
		secret      string
		wantFinding string
	}{
		{"ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR", "token-like string (GitHub gh[posur]_ token): ghp_A1…[REDACTED]"},
		{"sk-" + strings.Repeat("a", 20), "token-like string (sk- API key): sk-aaa…[REDACTED]"},
		{"Bearer token12345", "token-like string (Bearer credential): Bearer…[REDACTED]"},
		{"OPENROUTER_API_KEY=valor12345678", "token-like string (OPENROUTER_API_KEY value): OPENRO…[REDACTED]"},
		{"-----BEGIN RSA PRIVATE KEY-----", "key material (PEM block): -----B…[REDACTED]"},
		{"see the system prompt below", "prompt-dump marker (system prompt) — records never ship prompts"},
		{"full transcript of the session", "prompt-dump marker (full transcript) — records never ship transcripts"},
		{"reviewer@example.com", "reviewer identity (email address): review…[REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.secret, func(t *testing.T) {
			scan, err := provenance.ScanProvenanceRecord(*recordWithSecret(t, tc.secret))
			require.NoError(t, err)
			require.False(t, scan.Ok)
			assert.Contains(t, scan.Findings, tc.wantFinding)
		})
	}
}

func TestV_P9_7_AssertNoSecretsThrowsTyped(t *testing.T) {
	dirty := recordWithSecret(t, "ghp_"+"A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR")

	err := provenance.AssertNoSecrets(*dirty)
	require.Error(t, err)
	var secretErr *provenance.ProvenanceSecretError
	require.ErrorAs(t, err, &secretErr)
	require.NotEmpty(t, secretErr.Findings)
	assert.Contains(t, err.Error(), "a provenance record is not a secret container (ARCH §12.5)")

	// A clean record passes the gate with no error.
	require.NoError(t, provenance.AssertNoSecrets(*buildRecord(t, nil)))
}

func TestV_P9_7_SecretsInEveryStringFieldAreCaught(t *testing.T) {
	// The scan covers the WHOLE serialized record, not just source files.
	cases := []struct {
		name      string
		overrides func(*provenance.BuildProvenanceInput)
	}{
		{"run id", func(in *provenance.BuildProvenanceInput) { in.RunID = "run_" + strings.Repeat("A", 40) }},
		{"detector model", func(in *provenance.BuildProvenanceInput) { in.RoutedModels.DetectorModel = "reviewer@example.com" }},
		{"writer model", func(in *provenance.BuildProvenanceInput) { in.RoutedModels.WriterModel = "Bearer abcdefgh" }},
		{"manifest version", func(in *provenance.BuildProvenanceInput) { in.ManifestVersion = "system prompt" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, err := provenance.BuildProvenanceRecord(validInput(tc.overrides))
			if err != nil {
				// The builder itself refuses the dirty value — that is an even
				// earlier gate than the scan.
				var buildErr *provenance.ProvenanceBuildError
				require.ErrorAs(t, err, &buildErr)
				return
			}
			require.Error(t, provenance.AssertNoSecrets(*record))
		})
	}
}

func TestV_P9_7_FortyHexRunInADifferentFieldIsStillFlagged(t *testing.T) {
	// The exemption is by VALUE, so the same hex riding a second time in
	// source_files (i.e. a second 40-hex run beyond the record's own) is flagged.
	// Here the record's own sha is repeated — a legitimate-looking duplicate is
	// still exempt, so this case uses a distinct 40-hex run instead.
	scan, err := provenance.ScanProvenanceRecord(*recordWithSecret(t, "f"+sha[1:]))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Contains(t, scan.Findings[0], "long opaque base64/hex run (40 chars)")
}
