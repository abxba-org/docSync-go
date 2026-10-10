package verifier

// Ported from packages/verifier/src/tier1/examples-check.ts (P4-T07; PLAN
// §5.4 item 7): fenced code blocks in the PATCHED content that declare a
// tooling-backed language must parse:
//
//   - `json` blocks via JSON.parse; `yaml` blocks via the `yaml` parser;
//   - blocks with NO language tag are prose — ignored;
//   - blocks tagged with OTHER languages do not fail the check: the check
//     status is 'pass' but the detail records the skipped sub-results ("n
//     blocks parsed, m skipped (no tooling declared)") — a skipped sub-result
//     is provenance, never a silent omission;
//   - the optional `exampleChecker` port (future tooling) is invoked per fenced
//     block with its language + code; a non-ok result fails the check.
//
// Any parse failure or checker failure ⇒ fail with line-precise detail.
// Skipped when no patched content exists (the application did not simulate).

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CheckExamplesParse validates the fenced example blocks of the patched
// content.
func CheckExamplesParse(input Tier1Input, patchedContent *string) schemas.VerificationCheck {
	if patchedContent == nil {
		return schemas.VerificationCheck{
			ID:     "examples-parse",
			Status: schemas.CheckSkipped,
			Detail: tier1DetailPtr(
				"no patched content available (application not simulated successfully) — examples cannot be parsed",
			),
		}
	}

	var failures []string
	parsed := 0
	skipped := 0

	for _, block := range ScanFences(*patchedContent).Blocks {
		if block.Language == "" {
			continue // no language tag — prose, ignored
		}
		toolingBacked := block.Language == "json" || block.Language == "yaml"
		ok := true
		if block.Language == "json" {
			var probe any
			if err := json.Unmarshal([]byte(block.Code), &probe); err != nil {
				ok = false
				failures = append(failures,
					"json block at line "+strconv.Itoa(block.StartLine)+" does not parse: "+err.Error())
			}
		} else if block.Language == "yaml" {
			if err := tier1ParseYAML(block.Code); err != nil {
				ok = false
				failures = append(failures,
					"yaml block at line "+strconv.Itoa(block.StartLine)+" does not parse: "+err.Error())
			}
		}
		if checker := input.ExampleChecker; checker != nil {
			result, err := checker(ExampleBlock{
				Language: block.Language,
				Code:     block.Code,
				Line:     block.StartLine,
			})
			if err != nil {
				ok = false
				failures = append(failures,
					"example checker threw on block tagged '"+block.Language+"' at line "+
						strconv.Itoa(block.StartLine)+": "+tier1ErrorMessage(err))
			} else if result.Ok {
				if !toolingBacked {
					parsed++
				}
			} else {
				ok = false
				checkerDetail := ""
				if result.Detail != nil {
					checkerDetail = ": " + *result.Detail
				}
				failures = append(failures,
					"block tagged '"+block.Language+"' at line "+strconv.Itoa(block.StartLine)+
						" rejected by the example checker"+checkerDetail)
			}
		} else if !toolingBacked {
			skipped++
			continue
		}
		if ok && toolingBacked {
			parsed++
		}
	}

	if len(failures) > 0 {
		return schemas.VerificationCheck{
			ID:     "examples-parse",
			Status: schemas.CheckFail,
			Detail: tier1DetailPtr(
				strconv.Itoa(len(failures)) + " unparsable example(s): " + strings.Join(failures, "; ")),
		}
	}
	return schemas.VerificationCheck{
		ID:     "examples-parse",
		Status: schemas.CheckPass,
		Detail: tier1DetailPtr(
			strconv.Itoa(parsed) + " block(s) parsed, " + strconv.Itoa(skipped) +
				" skipped (no tooling declared)"),
	}
}
