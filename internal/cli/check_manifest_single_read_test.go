package cli_test

// Ported from packages/cli/test/check-manifest-single-read-p12-t16.test.ts
// (G1-2) — ONE manifest read per `docsync check` command: the report-only
// advisories ride on the composed runtime (derived from the SAME read that
// gated the run), so no second read can fail between the gate and the run.
//
// DEVIATION NOTE: the TS suite observes the read COUNT at the fs boundary
// (hoisted `vi.mock` counters over node:fs/promises: manifest === 1,
// orgPolicy === 1, plus an injected-EACCES-on-second-read lane). Go has no
// fs mock seam at that boundary, so the single-read invariant is pinned
// through its OBSERVABLE consequences instead:
//   - the advisory surface renders intact from the same read that gated the
//     run (text and --json alike);
//   - a manifest read failure is the fail-closed CONFIG lane (exit 2) — the
//     "second read turns a passing check into exit 3" shape cannot occur,
//     because the Go composition performs exactly ONE manifest read
//     (createCliPipelineRuntime → ValidateRepositoryConfig →
//     LoadAndValidateManifest) and threads its advisories onto the runtime.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonUnmarshalString decodes one captured JSON line (the TS JSON.parse).
func jsonUnmarshalString(line string, target any) error {
	return json.Unmarshal([]byte(line), target)
}

func TestCheckManifestSingleReadTextModeCarriesAdvisories(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	// The run's honest exit (the empty offline window) with the advisory
	// surface intact — the same read gated the run and rendered the floors.
	assert.Equal(t, cli.ExitOK, exit)
	assert.Len(t, cliAdvisoryLines(captured.Err(), "docsync check:"), 2)
}

func TestCheckManifestSingleReadJSONCarriesAdvisories(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, jsonUnmarshalString(captured.stdoutLines()[0], &payload))
	advisories, ok := payload["advisories"].([]any)
	require.True(t, ok)
	assert.Len(t, advisories, 2)
}

func TestCheckManifestSingleReadWithoutOrgPolicyOmitsAdvisories(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	// Absence is not a review: no floors, no advisories, honest exit.
	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, jsonUnmarshalString(captured.stdoutLines()[0], &payload))
	_, present := payload["advisories"]
	assert.False(t, present)
}

func TestCheckManifestSingleReadFailureIsConfigNotRuntime(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based read failures do not apply to root")
	}
	repo := newGitFixture(t)
	repo.seedBase()
	manifestPath := filepath.Join(repo.root, ".docsync", "manifest.yml")
	require.NoError(t, os.Chmod(manifestPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(manifestPath, 0o644) })
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--base", "HEAD"), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	// The pre-G1-2 bug shape turned an inter-read I/O failure into exit 3;
	// with ONE read the failure is the fail-closed CONFIG lane (exit 2), and
	// no advisory surface renders from a read that never completed.
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Empty(t, cliAdvisoryLines(captured.Err(), "docsync check:"))
}
