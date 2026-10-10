package cli_test

// Ported from packages/cli/test/init-template-roles.test.ts (P11-T21, ARCH
// §10.2) — the generated models.yml template binds the advisory roles:
// `drift-auditor` (Loop B) and `claim-extractor` (V2 claims). Deterministic
// assertions on the template constant + the written file + the structural
// gateway gate (RunInit fails closed before writing when the bindings do not
// satisfy the gateway, so an exit-0 init proves the gate passed).
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitTemplateEmitsAdvisoryRoleBindings(t *testing.T) {
	// The structural template object carries BOTH advisory roles.
	assert.Contains(t, cli.InitModelsTemplate, "drift-auditor:")
	assert.Contains(t, cli.InitModelsTemplate, "claim-extractor:")
	// The Phase-0 roles the gateway requires stay bound.
	for _, role := range []string{"decision-engine:", "judge:", "writer:"} {
		assert.Contains(t, cli.InitModelsTemplate, role)
	}

	root := t.TempDir()
	result := cli.RunInit(cli.InitOptions{RepoRoot: root})
	// The gateway structural gate ran over the template BEFORE the write and
	// passed (parseModelsConfig does not throw, TS parity).
	require.Equal(t, cli.ExitOK, result.ExitCode)

	// The generated YAML carries the same bindings (with the P11-T21 comment).
	written, err := os.ReadFile(filepath.Join(root, ".docsync", "models.yml"))
	require.NoError(t, err)
	text := string(written)
	assert.Contains(t, text, "drift-auditor:")
	assert.Contains(t, text, "claim-extractor:")
	assert.Contains(t, text, "P11-T21")
	// Credentials discipline (§3 rule 8): the template never carries a key.
	assert.NotContains(t, strings.ToLower(text), "sk-or-v1")
}
