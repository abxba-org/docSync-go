package globs_test

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/stretchr/testify/assert"
)

func TestScoreGlobMetrics(t *testing.T) {
	// TS: scoreGlob metrics — exact metrics for representative patterns.
	for _, c := range []struct {
		pattern string
		want    globs.GlobSpecificity
	}{
		{"src/payments/**", globs.GlobSpecificity{Segments: 3, LiteralChars: 13, DoubleStarCount: 1, WildcardStars: 0}},
		{"src/payments/refunds/**", globs.GlobSpecificity{Segments: 4, LiteralChars: 21, DoubleStarCount: 1, WildcardStars: 0}},
		{"**/x", globs.GlobSpecificity{Segments: 2, LiteralChars: 2, DoubleStarCount: 1, WildcardStars: 0}},
		{"src/*.ts", globs.GlobSpecificity{Segments: 2, LiteralChars: 7, DoubleStarCount: 0, WildcardStars: 1}},
		{"src/**/*.ts", globs.GlobSpecificity{Segments: 3, LiteralChars: 8, DoubleStarCount: 1, WildcardStars: 1}},
		{"", globs.GlobSpecificity{Segments: 0, LiteralChars: 0, DoubleStarCount: 0, WildcardStars: 0}},
		// metacharacters never count as literal chars; a run of ≥2 stars counts
		// once, "***" as one double-star run.
		{"*?[]{}()!", globs.GlobSpecificity{Segments: 1, LiteralChars: 0, DoubleStarCount: 0, WildcardStars: 1}},
		{"a/**/b/**/c", globs.GlobSpecificity{Segments: 5, LiteralChars: 7, DoubleStarCount: 2, WildcardStars: 0}},
		{"{a,b}/*.ts", globs.GlobSpecificity{Segments: 2, LiteralChars: 7, DoubleStarCount: 0, WildcardStars: 1}},
	} {
		assert.Equalf(t, c.want, globs.ScoreGlob(c.pattern), "ScoreGlob(%q)", c.pattern)
	}
}

func TestCompareSpecificityTotalOrder(t *testing.T) {
	// TS: rules (1)–(4) of compareSpecificity.
	assert.Greater(t, globs.CompareSpecificity(globs.ScoreGlob("src/payments/refunds/**"), globs.ScoreGlob("src/payments/**")), 0,
		"orders by segments first")
	assert.Greater(t, globs.CompareSpecificity(globs.ScoreGlob("src/x"), globs.ScoreGlob("src/**")), 0,
		"breaks segment ties by more literal chars")
	assert.Greater(t, globs.CompareSpecificity(globs.ScoreGlob("*a/*?/x"), globs.ScoreGlob("*a/**/x")), 0,
		"breaks literal ties by fewer **")
	assert.Greater(t, globs.CompareSpecificity(globs.ScoreGlob("?a/?b/x"), globs.ScoreGlob("*a/*b/x")), 0,
		"breaks ** ties by fewer single *")
	// metric ties return 0 — the §11.3 equal-specificity tie, deliberately
	// NOT broken here.
	assert.Equal(t, 0, globs.CompareSpecificity(globs.ScoreGlob("src/a/**"), globs.ScoreGlob("src/b/**")))
	assert.Equal(t, 0, globs.CompareSpecificity(globs.ScoreGlob("aaa/**"), globs.ScoreGlob("bbb/**")))

	// transitivity across depth levels.
	broad := globs.ScoreGlob("src/**")
	mid := globs.ScoreGlob("src/a/**")
	deep := globs.ScoreGlob("src/a/b/**")
	assert.Greater(t, globs.CompareSpecificity(deep, mid), 0)
	assert.Greater(t, globs.CompareSpecificity(mid, broad), 0)
	assert.Greater(t, globs.CompareSpecificity(deep, broad), 0)
}

// patternArb mirrors the TS fast-check generator: patterns built from the
// tokens 'src', 'docs', '*', '**', '/' with a deterministic seed.
func patternArb(rng *rand.Rand) string {
	tokens := []string{"src", "docs", "*", "**", "/"}
	n := rng.Intn(9) // minLength 0, maxLength 8
	out := ""
	for i := 0; i < n; i++ {
		out += tokens[rng.Intn(len(tokens))]
	}
	return out
}

func TestCompareSpecificityStrictWeakOrderProperties(t *testing.T) {
	// TS property tests (fast-check, 300 runs): antisymmetry and transitivity
	// over generated patterns.
	rng := rand.New(rand.NewSource(20261010))
	const runs = 300
	for i := 0; i < runs; i++ {
		a, b := patternArb(rng), patternArb(rng)
		ab := globs.CompareSpecificity(globs.ScoreGlob(a), globs.ScoreGlob(b))
		ba := globs.CompareSpecificity(globs.ScoreGlob(b), globs.ScoreGlob(a))
		if ab == 0 {
			assert.Equalf(t, 0, ba, "antisymmetry: cmp(%q,%q)=0 must imply cmp(%q,%q)=0", a, b, b, a)
		} else {
			assert.Equalf(t, -ab, ba, "antisymmetry: cmp(%q,%q)=%d vs cmp(%q,%q)=%d", a, b, ab, b, a, ba)
		}
	}
	for i := 0; i < runs; i++ {
		a, b, c := patternArb(rng), patternArb(rng), patternArb(rng)
		ab := globs.CompareSpecificity(globs.ScoreGlob(a), globs.ScoreGlob(b))
		bc := globs.CompareSpecificity(globs.ScoreGlob(b), globs.ScoreGlob(c))
		ac := globs.CompareSpecificity(globs.ScoreGlob(a), globs.ScoreGlob(c))
		violation := (ab > 0 && bc > 0 && ac <= 0) || (ab < 0 && bc < 0 && ac >= 0)
		assert.Falsef(t, violation, "transitivity violated for %q, %q, %q", a, b, c)
	}
}

func TestSpecificityKey(t *testing.T) {
	// TS: deterministic and identical for identical patterns.
	assert.Equal(t, globs.SpecificityKey("src/**/*.ts"), globs.SpecificityKey("src/**/*.ts"))
	// rule 1: deeper patterns sort first (descending key order).
	assert.Greater(t, globs.SpecificityKey("src/payments/refunds/**"), globs.SpecificityKey("src/payments/**"))
	// rule 3: fewer ** sorts first.
	assert.Greater(t, globs.SpecificityKey("*a/*?/x"), globs.SpecificityKey("*a/**/x"))
	// rule 5: among metric ties the lexicographically greater pattern sorts
	// first.
	assert.Greater(t, globs.SpecificityKey("src/b/**"), globs.SpecificityKey("src/a/**"))

	// key order agrees with the full specificity order including rule 5
	// (TS fast-check property, 300 runs).
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 300; i++ {
		a, b := patternArb(rng), patternArb(rng)
		cmp := globs.CompareSpecificity(globs.ScoreGlob(a), globs.ScoreGlob(b))
		keyA, keyB := globs.SpecificityKey(a), globs.SpecificityKey(b)
		keyCmp := 0
		switch {
		case keyA > keyB:
			keyCmp = 1
		case keyA < keyB:
			keyCmp = -1
		}
		expected := cmp
		if expected == 0 {
			switch {
			case a > b:
				expected = 1
			case a < b:
				expected = -1
			}
		}
		assert.Equalf(t, expected, keyCmp, "key order disagreement for %q vs %q", a, b)
	}

	// descending codepoint comparator over keys yields the total order.
	keys := []string{
		globs.SpecificityKey("src/a/**"),
		globs.SpecificityKey("src/**"),
		globs.SpecificityKey("src/payments/refunds/**"),
		globs.SpecificityKey("src/b/**"),
	}
	sorted := append([]string(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })
	assert.Equal(t, globs.SpecificityKey("src/payments/refunds/**"), sorted[0], "deepest pattern first")
	assert.Equal(t, globs.SpecificityKey("src/b/**"), sorted[1], fmt.Sprintf("descending order: %v", sorted))
	assert.Equal(t, globs.SpecificityKey("src/a/**"), sorted[2])
	assert.Equal(t, globs.SpecificityKey("src/**"), sorted[3], "broadest pattern last")
}
