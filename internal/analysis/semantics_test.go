package analysis_test

// Ported from packages/core/test/analysis-semantics.test.ts (traceability,
// plan.md §7): semantic differs tests (P1-T05, OD-R06) — path families,
// change-level dependency flag, openapi flag, and key-level typed config
// diffs over the small V1 family set (.env / .json / .yaml/.yml), including
// the D14 declared gaps (unparseable input, toml/ini) which must yield
// `config_diff: undefined` + note, never a silent false.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

func requireChanged(t *testing.T, result *analysis.ConfigDiffResult, note string) {
	t.Helper()
	require.NotNil(t, result, "expected a config diff result")
	require.NotNil(t, result.ConfigDiff, "expected config_diff to be assessed")
	assert.True(t, *result.ConfigDiff, "expected config_diff true")
	require.NotNil(t, result.Note, "expected a note")
	assert.Equal(t, note, *result.Note)
}

func requireUnchanged(t *testing.T, result *analysis.ConfigDiffResult) {
	t.Helper()
	require.NotNil(t, result, "expected a config diff result")
	require.NotNil(t, result.ConfigDiff, "expected config_diff to be assessed")
	assert.False(t, *result.ConfigDiff, "expected config_diff false")
	assert.Nil(t, result.Note, "expected no note for an unchanged diff")
}

func requireGap(t *testing.T, result *analysis.ConfigDiffResult, note string) {
	t.Helper()
	require.NotNil(t, result, "expected a declared-gap result")
	assert.Nil(t, result.ConfigDiff, "config_diff must be undefined (declared gap)")
	require.NotNil(t, result.Note, "expected a gap note")
	assert.Equal(t, note, *result.Note)
}

func TestClassifyPathFamily_P1T05_OD_R06(t *testing.T) {
	t.Run("dependency manifests match by basename at any depth", func(t *testing.T) {
		for _, path := range []string{
			"package.json",
			"packages/core/package.json",
			"apps/web/package.json",
			"pnpm-lock.yaml",
			"a/b/package-lock.json",
			"yarn.lock",
			"requirements.txt",
			"pyproject.toml",
			"go.mod",
			"Cargo.toml",
			"Cargo.lock",
		} {
			assert.Equal(t, analysis.PathFamilyDependencyManifest, analysis.ClassifyPathFamily(path), path)
		}
	})

	t.Run("openapi/swagger and *.schema.json are the openapi family", func(t *testing.T) {
		for _, path := range []string{
			"openapi.json",
			"openapi.yaml",
			"api/openapi.yml",
			"swagger.json",
			"docs/swagger.yaml",
			"schemas/user.schema.json",
		} {
			assert.Equal(t, analysis.PathFamilyOpenAPI, analysis.ClassifyPathFamily(path), path)
		}
		// not *.schema.json
		assert.Equal(t, analysis.PathFamilyConfig, analysis.ClassifyPathFamily("src/schema.json"))
	})

	t.Run("config family: .env* plus json/yaml/yml/toml/ini extensions", func(t *testing.T) {
		for _, path := range []string{
			".env",
			".env.local",
			"config/.env.production",
			"config/settings.json",
			"conf.yml",
			"conf2.yaml",
			"app.toml",
			"settings.ini",
		} {
			assert.Equal(t, analysis.PathFamilyConfig, analysis.ClassifyPathFamily(path), path)
		}
	})

	t.Run("test beats source; source beats other; precise families beat extensions", func(t *testing.T) {
		assert.Equal(t, analysis.PathFamilySource, analysis.ClassifyPathFamily("src/main.ts"))
		assert.Equal(t, analysis.PathFamilyTest, analysis.ClassifyPathFamily("src/main.test.ts"))
		assert.Equal(t, analysis.PathFamilyTest, analysis.ClassifyPathFamily("test/a.ts"))
		assert.Equal(t, analysis.PathFamilySource, analysis.ClassifyPathFamily("src/x.py"))
		assert.Equal(t, analysis.PathFamilyOther, analysis.ClassifyPathFamily("README.md"))
		assert.Equal(t, analysis.PathFamilyOther, analysis.ClassifyPathFamily("assets/logo.png"))
		assert.Equal(t, analysis.PathFamilyOther, analysis.ClassifyPathFamily("Makefile"))
		// package.json inside a test dir is still a dependency manifest (precise first).
		assert.Equal(t, analysis.PathFamilyDependencyManifest, analysis.ClassifyPathFamily("test/fixtures/package.json"))
	})
}

func TestDependencyChanged_P1T05(t *testing.T) {
	t.Run("any manifest path flips the change-level flag; empty and source-only diffs do not", func(t *testing.T) {
		assert.False(t, analysis.DependencyChanged(nil))
		assert.False(t, analysis.DependencyChanged([]analysis.PathOnlyFile{{Path: "src/a.ts"}, {Path: "src/b.ts"}}))
		assert.True(t, analysis.DependencyChanged([]analysis.PathOnlyFile{{Path: "src/a.ts"}, {Path: "packages/x/package.json"}}))
		assert.True(t, analysis.DependencyChanged([]analysis.PathOnlyFile{{Path: "pnpm-lock.yaml"}}))
	})
}

func TestApiDiffFor_P1T05_OD_R28Scope(t *testing.T) {
	t.Run("openapi family gives boolean api_diff true with a fixed note; others give nil", func(t *testing.T) {
		result := analysis.ApiDiffFor(analysis.PathOnlyFile{Path: "api/openapi.yaml"})
		require.NotNil(t, result)
		assert.True(t, result.ApiDiff)
		assert.Contains(t, result.Note, "OD-R28")
		assert.Contains(t, result.Note, "Phase 11")
		assert.Nil(t, analysis.ApiDiffFor(analysis.PathOnlyFile{Path: "src/openapi-client.ts"}))
		assert.Nil(t, analysis.ApiDiffFor(analysis.PathOnlyFile{Path: "README.md"}))
	})
}

func TestConfigDiffFor_EnvFamily_P1T05(t *testing.T) {
	t.Run("typed key-level diff with a deterministic sorted note", func(t *testing.T) {
		result := analysis.ConfigDiffFor(
			".env",
			"API_URL=https://api.example\nDEBUG=false\nPORT=8080\n",
			"API_URL=https://api2.example\nDEBUG=true\nPORT=8080\nNEW_KEY=\"hello world\"\n",
		)
		requireChanged(t, result,
			"config keys changed (3): API_URL: \"https://api.example\" → \"https://api2.example\"; "+
				"DEBUG: false → true; "+
				"NEW_KEY: absent → \"hello world\"")
	})

	t.Run("typed values: numbers, booleans, null are compared and rendered bare", func(t *testing.T) {
		result := analysis.ConfigDiffFor(
			".env",
			"A=1\nB=true\nC=null\nD=\"x\"\n",
			"A=2\nB=false\nC=5\nD=\"y\"\n",
		)
		requireChanged(t, result,
			"config keys changed (4): A: 1 → 2; B: true → false; C: null → 5; D: \"x\" → \"y\"")
		// Symmetric quotes are stripped on both sides, so these are NOT a change.
		quoted := analysis.ConfigDiffFor(".env", "D=\"x\"\n", "D=x\n")
		requireUnchanged(t, quoted)
	})

	t.Run("env-only false when both sides parse and no key changed", func(t *testing.T) {
		result := analysis.ConfigDiffFor(".env.local", "A=1\n# comment\n", "A=1\n\n")
		requireUnchanged(t, result)
	})

	t.Run("a line without = is a parse failure: declared gap, never config_diff false", func(t *testing.T) {
		result := analysis.ConfigDiffFor(".env", "A=1\n", "A=1\nBROKEN LINE\n")
		requireGap(t, result, "unparseable — semantic config diff unavailable")
		// Same on the old side.
		oldBroken := analysis.ConfigDiffFor(".env", "NO_EQUALS\n", "A=1\n")
		requireGap(t, oldBroken, "unparseable — semantic config diff unavailable")
	})
}

func TestConfigDiffFor_JSONFamily_P1T05(t *testing.T) {
	t.Run("nested key paths are walked recursively with typed values", func(t *testing.T) {
		result := analysis.ConfigDiffFor(
			"config/settings.json",
			`{"a": {"b": 1}, "c": "s", "list": [1, 2]}`,
			`{"a": {"b": 2}, "c": "s", "list": [1, 3]}`,
		)
		requireChanged(t, result, "config keys changed (2): a.b: 1 → 2; list: [1,2] → [1,3]")
	})

	t.Run("added and removed keys render as absent on the missing side", func(t *testing.T) {
		result := analysis.ConfigDiffFor("cfg.json", `{"keep": 1, "gone": true}`, `{"keep": 1, "new": 7}`)
		requireChanged(t, result, "config keys changed (2): gone: true → absent; new: absent → 7")
	})

	t.Run("unchanged json gives config_diff false with no note", func(t *testing.T) {
		result := analysis.ConfigDiffFor("cfg.json", `{"a": 1}`, "{\n  \"a\": 1\n}\n")
		requireUnchanged(t, result)
	})

	t.Run("malformed json on either side gives the declared gap note (D14)", func(t *testing.T) {
		cases := [][2]string{
			{`{"a": 1}`, `{"a": 1`},
			{"not json", `{"a": 1}`},
			{`[1, 2]`, `{"a": 1}`}, // non-object root: no key extraction
		}
		for _, texts := range cases {
			result := analysis.ConfigDiffFor("cfg.json", texts[0], texts[1])
			requireGap(t, result, "unparseable — semantic config diff unavailable")
		}
	})

	t.Run("note is capped at 20 sorted keys with an ellipsis", func(t *testing.T) {
		var oldBuilder, newBuilder strings.Builder
		oldBuilder.WriteString("{")
		newBuilder.WriteString("{")
		for i := 0; i < 25; i += 1 {
			if i > 0 {
				oldBuilder.WriteString(",")
				newBuilder.WriteString(",")
			}
			key := fmt.Sprintf("k%02d", i)
			fmt.Fprintf(&oldBuilder, "%q:%d", key, i)
			fmt.Fprintf(&newBuilder, "%q:%d", key, i+1)
		}
		oldBuilder.WriteString("}")
		newBuilder.WriteString("}")

		result := analysis.ConfigDiffFor("cfg.json", oldBuilder.String(), newBuilder.String())
		require.NotNil(t, result)
		require.NotNil(t, result.ConfigDiff)
		assert.True(t, *result.ConfigDiff)
		require.NotNil(t, result.Note)
		note := *result.Note
		assert.True(t, strings.HasPrefix(note, "config keys changed (25): "))
		assert.Contains(t, note, "k00: 0 → 1")
		assert.NotContains(t, note, "k24: 24 → 25")
		assert.True(t, strings.HasSuffix(note, "; …"))
		assert.Equal(t, 20, strings.Count(note, ";"))
	})
}

func TestConfigDiffFor_YAMLFamily_P1T05_ShallowOD_R06Scope(t *testing.T) {
	t.Run("top-level and one nested level diff at key level", func(t *testing.T) {
		result := analysis.ConfigDiffFor(
			"config/app.yml",
			"host: localhost\nport: 5432\npool:\n  min: 1\n  max: 10\n",
			"host: example.internal\nport: 5432\npool:\n  min: 2\n  max: 10\n",
		)
		requireChanged(t, result,
			"config keys changed (2): host: \"localhost\" → \"example.internal\"; pool.min: 1 → 2")
	})

	t.Run("deeper objects compare as canonical-JSON leaf blobs (declared shallow scope)", func(t *testing.T) {
		result := analysis.ConfigDiffFor(
			"conf.yaml",
			"a:\n  b:\n    d: 5\n",
			"a:\n  b:\n    d: 6\n",
		)
		requireChanged(t, result, "config keys changed (1): a.b: {\"d\":5} → {\"d\":6}")
	})

	t.Run("malformed yaml on either side gives the declared gap note (D14)", func(t *testing.T) {
		result := analysis.ConfigDiffFor("conf.yaml", "a: 1\n", "a: [unclosed\n")
		requireGap(t, result, "unparseable — semantic config diff unavailable")
		oldBroken := analysis.ConfigDiffFor("conf.yaml", "{unterminated\n", "a: 1\n")
		requireGap(t, oldBroken, "unparseable — semantic config diff unavailable")
	})
}

func TestConfigDiffFor_DeclaredGaps_D14(t *testing.T) {
	t.Run("toml/ini have no V1 parser: config_diff undefined + fixed gap note", func(t *testing.T) {
		for _, path := range []string{"app.toml", "settings.ini", "config/app.toml"} {
			result := analysis.ConfigDiffFor(path, "[a]\nb=1\n", "[a]\nb=2\n")
			requireGap(t, result, "toml/ini semantic config diff unavailable in V1 — declared gap (D14)")
		}
	})

	t.Run("non-config paths give nil (caller must not ask); other config extensions stay gaps", func(t *testing.T) {
		assert.Nil(t, analysis.ConfigDiffFor("src/main.ts", "a", "b"))
		assert.Nil(t, analysis.ConfigDiffFor("README.md", "a", "b"))
		assert.Nil(t, analysis.ConfigDiffFor("package.json", `{"a":1}`, `{"a":2}`)) // manifest family, not config
	})
}
