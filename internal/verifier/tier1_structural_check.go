package verifier

// Ported from packages/verifier/src/tier1/structural-check.ts (P4-T05; PLAN
// §5.4 item 5): the PATCHED content must not be structurally mangled. Checked
// (on the simulated patched content; skipped when the application did not
// produce one):
//
//   - balanced backtick code fences — every fence closed, in order; fence-info
//     lines are delimiters, not content (shared scanner, markdown.go);
//   - YAML front-matter parses (only when the document HAS front-matter);
//   - `yaml`-tagged fences parse; `json`-tagged fences parse via JSON.parse;
//   - heading structure: every (level, text) heading present pre-patch must
//     still be present post-patch (multiset, per key — silent heading deletion
//     is structural mangling). The spec's conservative, TESTABLE rule is
//     honored in its fireable form: with a pure line splice, headings outside
//     the hunks' resolved ranges are preserved by construction, so the
//     fireable defect is a heading removed INSIDE an edited range (a hunk that
//     deletes/replaces a heading without re-inserting it) — flagged here as
//     "silent heading deletion" and routed to REVIEW by the tier failure.
//     List-item collapse is folded into this heading rule per the spec's
//     conservative directive (a collapsed list under an unchanged heading is
//     caught by the claim-level Tier-2 checks, not by invented line heuristics).

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CheckStructuralIntegrity validates the patched content's Markdown
// structure.
func CheckStructuralIntegrity(input Tier1Input, patchedContent *string) schemas.VerificationCheck {
	if patchedContent == nil {
		return schemas.VerificationCheck{
			ID:     "structural-integrity",
			Status: schemas.CheckSkipped,
			Detail: tier1DetailPtr(
				"no patched content available (application not simulated successfully) — structural " +
					"integrity cannot be assessed",
			),
		}
	}

	var defects []string
	content := *patchedContent

	scan := ScanFences(content)
	if scan.UnclosedAt != nil {
		defects = append(defects,
			"unclosed code fence opened at line "+strconv.Itoa(*scan.UnclosedAt)+" (fences must close in order)")
	}
	for _, block := range scan.Blocks {
		if block.Language == "yaml" {
			if err := ParseYAMLStrict(block.Code, "code fence tagged yaml at line "+strconv.Itoa(block.StartLine)); err != nil {
				defects = append(defects, err.Error())
			}
		} else if block.Language == "json" {
			var probe any
			if err := json.Unmarshal([]byte(block.Code), &probe); err != nil {
				defects = append(defects,
					"code fence tagged json at line "+strconv.Itoa(block.StartLine)+" is not valid JSON: "+err.Error())
			}
		}
	}

	frontMatter := ScanFrontMatter(content)
	if frontMatter != nil {
		if err := ParseYAMLStrict(frontMatter.YAML, "front-matter"); err != nil {
			defects = append(defects, err.Error())
		}
	}

	if headingDefect := checkHeadings(input, content); headingDefect != nil {
		defects = append(defects, *headingDefect)
	}

	if len(defects) > 0 {
		return schemas.VerificationCheck{
			ID:     "structural-integrity",
			Status: schemas.CheckFail,
			Detail: tier1DetailPtr(strings.Join(defects, "; ")),
		}
	}
	return schemas.VerificationCheck{ID: "structural-integrity", Status: schemas.CheckPass}
}

// checkHeadings is the heading-preservation rule: multiset per (level, text) —
// post-patch occurrences must cover pre-patch occurrences. Each side excludes
// its OWN fenced/front-matter lines (line numbers shift between pre and post),
// so a `# comment` inside a code fence is never a heading on either side.
// The returned string is the defect evidence, or nil (TS null).
func checkHeadings(input Tier1Input, patchedContent string) *string {
	docContent := ""
	if input.DocContent != nil {
		docContent = *input.DocContent
	}
	pre := countHeadingKeys(ExtractHeadings(docContent, tier1ExcludedLines(docContent)))
	post := countHeadingKeys(ExtractHeadings(patchedContent, tier1ExcludedLines(patchedContent)))
	var missing []string
	// Iterated in first-occurrence order (document order) so the detail is
	// deterministic — Go map iteration order is not.
	for _, key := range pre.order {
		count := pre.counts[key]
		postCount := post.counts[key]
		if postCount < count {
			missing = append(missing, key+" (pre "+strconv.Itoa(count)+", post "+strconv.Itoa(postCount)+")")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return tier1DetailPtr(
		"headings disappeared from the patched content (silent heading deletion is structural mangling): " +
			strings.Join(missing, "; "))
}

// tier1HeadingKeyCounts is a multiset over (level, text) heading keys that
// remembers first-occurrence order (the TS Map iteration order the detail
// relied on).
type tier1HeadingKeyCounts struct {
	order  []string
	counts map[string]int
}

// countHeadingKeys builds the per-key multiset in first-occurrence order.
func countHeadingKeys(headings []HeadingOccurrence) tier1HeadingKeyCounts {
	result := tier1HeadingKeyCounts{counts: make(map[string]int, len(headings))}
	for _, heading := range headings {
		key := strings.Repeat("#", heading.Level) + " " + heading.Text
		if _, seen := result.counts[key]; !seen {
			result.order = append(result.order, key)
		}
		result.counts[key]++
	}
	return result
}
