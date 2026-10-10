package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// capturedIo records every stdout/stderr line (the TS helpers.ts captureIo).
type capturedIo struct {
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func captureIo() *capturedIo {
	return &capturedIo{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
}

func (c *capturedIo) Stdout(line string) { c.stdout.WriteString(line + "\n") }
func (c *capturedIo) Stderr(line string) { c.stderr.WriteString(line + "\n") }
func (c *capturedIo) Out() string        { return c.stdout.String() }
func (c *capturedIo) Err() string        { return c.stderr.String() }

// gitFixture is a real, local git repo (no network) with pinned-identity
// commits — the Go mirror of helpers.ts createCliFixtureRepo.
type gitFixture struct {
	t    *testing.T
	root string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	root := t.TempDir()
	r := &gitFixture{t: t, root: root}
	r.git("init", "-b", "main", "-q")
	r.git("config", "core.autocrlf", "false")
	r.git("config", "core.eol", "lf")
	r.git("config", "user.name", "docsync-test")
	r.git("config", "user.email", "docsync-test@invalid")
	return r
}

func (r *gitFixture) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *gitFixture) write(relPath, content string) {
	r.t.Helper()
	target := filepath.Join(r.root, relPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitFixture) commitAll(message string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *gitFixture) commitPaths(paths []string, message string) string {
	r.t.Helper()
	r.git(append([]string{"add", "--"}, paths...)...)
	r.git("commit", "-q", "-m", message)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

const fixtureManifest = `version: 1
documents:
  - path: docs/api.md
    mode: suggest
    sources:
      - "src/payments/**"
    triggers:
      - public_behavior
  - path: docs/locked.md
    mode: locked
    sources:
      - "src/payments/**"
    triggers:
      - public_behavior
`

const fixtureModels = `schema: docsync.models/v1
version: 1
roles:
  decision-engine:
    model: z-ai/glm-4.7
  judge:
    model: openai/gpt-4.1
  writer:
    model: anthropic/claude-sonnet-4.5
`

func (r *gitFixture) seedBase() {
	r.write(".docsync/manifest.yml", fixtureManifest)
	r.write(".docsync/models.yml", fixtureModels)
	r.write(".docsync/instructions.md", "# instructions\n- keep diffs minimal\n")
	r.write("docs/api.md", "# API reference\n\nThe refund call captures the payment.\n")
	r.write("docs/locked.md", "# Operations runbook (locked)\n\nThe refund ledger is append-only.\n")
	r.write("src/payments/refund.ts", "export function refund(): void {}\n")
	r.commitAll("chore: fixture base tree")
}

/* ------------------------------------------------------- run outcomes */

func degradedOfflineOutcome(docs, unknownCoverage []string) *schemas.RunOutcome {
	documents := make([]schemas.RunDocument, 0, len(docs))
	abstains := make([]schemas.RunAbstain, 0, len(docs))
	for _, docPath := range docs {
		dp := docPath
		documents = append(documents, schemas.RunDocument{
			DocPath:       docPath,
			PolicyOutcome: schemas.PipelineOutcomeReview,
			Abstain:       &schemas.RunDocumentAbstain{Reason: "model_unavailable", Detail: "decision unavailable (offline)"},
		})
		id := "ab-" + dp
		abstains = append(abstains, schemas.RunAbstain{AbstainID: id, DocPath: &dp, Reason: "model_unavailable", Detail: "decision unavailable (offline)"})
	}
	return &schemas.RunOutcome{
		Schema:          schemas.RunOutcomeSchemaID,
		RunID:           "offline#a..b",
		Key:             schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status:          schemas.RunStatusDegraded,
		Stages:          []schemas.PipelineStage{{Stage: "analyze", Status: schemas.RunStageOk}},
		Documents:       documents,
		Abstains:        abstains,
		Provenance:      schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
		Degradations:    []schemas.Degradation{{Stage: "jev", Kind: schemas.DegradationJevUnavailable, Detail: "decision unavailable (offline)"}},
		UnknownCoverage: unknownCoverage,
		EarlySignal:     schemas.EarlySignal{},
		StateChurn:      schemas.StateChurn{},
	}
}

func driftOutcome(rows []reportRow) *schemas.RunOutcome {
	documents := make([]schemas.RunDocument, 0, len(rows))
	for _, row := range rows {
		confidence := row.confidence
		documents = append(documents, schemas.RunDocument{
			DocPath:       row.docPath,
			PolicyOutcome: row.outcome,
			Confidence:    &confidence,
		})
	}
	return &schemas.RunOutcome{
		Schema:      schemas.RunOutcomeSchemaID,
		RunID:       "run#a..b",
		Key:         schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status:      schemas.RunStatusCompleted,
		Stages:      []schemas.PipelineStage{{Stage: "analyze", Status: schemas.RunStageOk}},
		Documents:   documents,
		StartedAt:   strPtrValue("2026-01-01T00:00:00Z"),
		FinishedAt:  strPtrValue("2026-01-01T00:00:01Z"),
		Provenance:  schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
		EarlySignal: schemas.EarlySignal{},
		StateChurn:  schemas.StateChurn{},
	}
}

type reportRow struct {
	docPath    string
	outcome    schemas.PipelineOutcome
	confidence float64
}

func strPtrValue(s string) *string { return &s }

/* ------------------------------------------------------- fake runtime */

type fakeRuntime struct {
	outcome    *schemas.RunOutcome
	err        error
	advisories []string
}

func (f *fakeRuntime) Run(_ context.Context, _ cli.CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	return f.outcome, f.err
}

func (f *fakeRuntime) Advisories() []string { return f.advisories }
