package verifier

// Ported from packages/verifier/src/tier1/markdown.ts (P4-T02..T07; ARCH
// §4.8 Tier 1): shared deterministic Markdown scanning for the Tier-1
// validators. Pure over `{content}` inputs — no fs/network/clock — and
// consumed by structural-integrity, link-resolution and examples-parse so the
// three checks share ONE fence/front-matter/heading interpretation (drift
// between checks is a bug).
//
// Deliberately conservative subset, documented for testability:
//   - Code fences are backtick fences only (```, length >= 3, up to 3 leading
//     spaces); opening fences carry an optional info string whose first word is
//     the language (lowercased — see ScanFences); closing fences carry NO info
//     string (CommonMark: an info string makes the line literal content, not a
//     closer). Fence-info lines are fence delimiters, never counted as content.
//   - YAML front-matter is a LEADING `---` block closed by a bare `---` or
//     `...` line; only a document that HAS front-matter is YAML-checked.
//   - Headings are ATX only (`#{1..6}` + text, closing hashes stripped); lines
//     inside fenced blocks or front-matter are never headings.
//   - Slug anchors follow the GitHub-slugger rules docSync relies on: lowercase,
//     keep letters/digits/underscores/hyphens/spaces, drop every other
//     character, trim, collapse runs of spaces to one hyphen, then de-duplicate
//     with `-1`, `-2`, … suffixes in document order.
//
// Regex parity note: the TS ATX pattern uses a lazy group (`(?:\s+(.*?))?`);
// Go RE2 has no lazy quantifiers, so the greedy twin `(?:\s+(.*))?` is used
// and the captured text is TrimSpace'd — identical capture for every input
// (the trailing `\s*$` admits only whitespace after the group, and trailing
// whitespace is trimmed either way).

import (
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// FencedBlock is one fenced code block, 1-based line-addressed (fence
// delimiter lines excluded from Code).
type FencedBlock struct {
	// Language is the fence info string's first word, taken verbatim ("" when
	// absent — see ScanFences).
	Language string
	// Code is the lines between the fences, joined with '\n' (no trailing
	// newline).
	Code string
	// StartLine is the 1-based line of the OPENING fence delimiter.
	StartLine int
	// EndLine is the 1-based line of the CLOSING fence delimiter (or last
	// content line when unclosed).
	EndLine int
	// Unclosed is true when the closing fence was never found (scan continues
	// to EOF).
	Unclosed bool
}

// FenceScan is the fence scan result of one document.
type FenceScan struct {
	// Blocks are the fenced blocks in document order (unclosed ones included,
	// flagged).
	Blocks []FencedBlock
	// UnclosedAt is the 1-based line of the first unclosed fence's opening
	// delimiter; nil when all closed (TS null).
	UnclosedAt *int
	// FencedLines holds every line number owned by fences (delimiters +
	// content) or front-matter.
	FencedLines map[int]bool
}

var (
	// fenceOpenRE is TS FENCE_OPEN — an opening fence (backticks, length >= 3,
	// up to 3 leading spaces) with an optional info string.
	fenceOpenRE = regexp.MustCompile("^ {0,3}(`{3,})(.*)$")
	// fenceCloseRE is TS FENCE_CLOSE — a closing fence carries NO info string.
	fenceCloseRE = regexp.MustCompile("^ {0,3}`{3,}\\s*$")
	// atxHeadingRE is TS ATX_HEADING (greedy twin; see the package note).
	atxHeadingRE = regexp.MustCompile("^ {0,3}(#{1,6})(?:\\s+(.*))?\\s*$")
)

// openFence tracks the fence currently being scanned (internal).
type openFence struct {
	language  string
	startLine int
	codeLines []string
}

// ScanFences scans backtick fences over the document's lines. A `yaml`/`json`
// fence's body is NOT re-scanned for nested fences (backtick lines inside a
// fenced block are literal content — CommonMark), so one pass is exact.
func ScanFences(content string) FenceScan {
	lines := strings.Split(content, "\n")
	blocks := make([]FencedBlock, 0)
	fencedLines := make(map[int]bool)
	var open *openFence

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		lineNumber := index + 1
		if open == nil {
			match := fenceOpenRE.FindStringSubmatch(line)
			if match != nil {
				info := strings.TrimSpace(match[2])
				language := ""
				if fields := strings.Fields(info); len(fields) > 0 {
					// The TS implementation takes the info string's first word
					// verbatim (its doc comment says "lowercased", the code does
					// not) — the implementation is ported exactly, so ```YAML
					// is NOT a yaml-tagged fence on either side.
					language = fields[0]
				}
				open = &openFence{language: language, startLine: lineNumber, codeLines: make([]string, 0)}
				fencedLines[lineNumber] = true
			}
			continue
		}
		fencedLines[lineNumber] = true
		if fenceCloseRE.MatchString(line) {
			blocks = append(blocks, FencedBlock{
				Language:  open.language,
				Code:      strings.Join(open.codeLines, "\n"),
				StartLine: open.startLine,
				EndLine:   lineNumber,
				Unclosed:  false,
			})
			open = nil
			continue
		}
		open.codeLines = append(open.codeLines, line)
	}

	var unclosedAt *int
	if open != nil {
		at := open.startLine
		unclosedAt = &at
		blocks = append(blocks, FencedBlock{
			Language:  open.language,
			Code:      strings.Join(open.codeLines, "\n"),
			StartLine: open.startLine,
			EndLine:   len(lines),
			Unclosed:  true,
		})
	}
	return FenceScan{Blocks: blocks, UnclosedAt: unclosedAt, FencedLines: fencedLines}
}

// FrontMatter is the leading YAML front-matter block.
type FrontMatter struct {
	// YAML is the lines between the `---` delimiters, joined with '\n'.
	YAML string
	// EndLine is the 1-based line of the closing delimiter.
	EndLine int
	// Lines holds the 1-based lines owned by the front-matter (delimiters
	// included).
	Lines map[int]bool
}

// ScanFrontMatter returns the LEADING front-matter block, or nil when the
// document has none.
func ScanFrontMatter(content string) *FrontMatter {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil
	}
	owned := map[int]bool{1: true}
	for index := 1; index < len(lines); index++ {
		owned[index+1] = true
		line := lines[index]
		if line == "---" || line == "..." {
			return &FrontMatter{
				YAML:    strings.Join(lines[1:index], "\n"),
				EndLine: index + 1,
				Lines:   owned,
			}
		}
	}
	// Unterminated front-matter is NOT front-matter (it is plain prose).
	return nil
}

// tier1ParseYAML parses YAML document text, returning the parser's error
// verbatim (nil when valid). The single YAML surface the tier-1 checks share
// (front-matter, `yaml` fences, examples).
func tier1ParseYAML(yamlText string) error {
	var probe any
	return yaml.Unmarshal([]byte(yamlText), &probe)
}

// ParseYAMLStrict parses the YAML document content (front-matter body or
// fenced `yaml` code), failing with a *StructuralDefect naming `where`.
func ParseYAMLStrict(yamlText, where string) error {
	if err := tier1ParseYAML(yamlText); err != nil {
		return &StructuralDefect{Message: where + " is not valid YAML: " + err.Error()}
	}
	return nil
}

// StructuralDefect is the typed structural defect — the check layer maps it
// into a check record (TS: class StructuralDefect extends Error). Error()
// returns the bare TS message text so check details stay byte-identical; Code
// maps the class to the docutil code the migration assigns it (the TS class
// had no docutil entry — closest code, per CONVENTIONS.md "Errors").
type StructuralDefect struct {
	Message string
}

// Error returns the defect message (TS `error.message`).
func (e *StructuralDefect) Error() string { return e.Message }

// Code is the docutil code mapped for this TS error class.
func (e *StructuralDefect) Code() string { return docutil.CodeVerify }

/* ------------------------------------------------------------------ headings */

// HeadingOccurrence is one ATX heading occurrence (1-based line; level 1..6;
// text without hashes).
type HeadingOccurrence struct {
	Level int
	Text  string
	Line  int
}

// ExtractHeadings returns the ATX headings of one document, skipping the
// given line numbers (nil = skip nothing).
func ExtractHeadings(content string, skipLines map[int]bool) []HeadingOccurrence {
	lines := strings.Split(content, "\n")
	headings := make([]HeadingOccurrence, 0)
	for index, line := range lines {
		if skipLines[index+1] {
			continue
		}
		match := atxHeadingRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		text := strings.TrimSpace(match[2])
		if len(text) == 0 {
			continue
		}
		headings = append(headings, HeadingOccurrence{
			Level: len(match[1]),
			Text:  text,
			Line:  index + 1,
		})
	}
	return headings
}

var (
	// slugStripRE drops every character GitHub slugs drop (TS
	// /[^\p{L}\p{N}\s_-]+/gu).
	slugStripRE = regexp.MustCompile(`[^\p{L}\p{N}\s_-]+`)
	// slugSpaceRE collapses whitespace runs to one hyphen (TS /\s+/g).
	slugSpaceRE = regexp.MustCompile(`\s+`)
	// slugTrimRE strips leading/trailing hyphens (TS /^-+|-+$/g).
	slugTrimRE = regexp.MustCompile(`^-+|-+$`)
)

// SlugifyHeading returns the GitHub-style slug of one heading text (see the
// package doc for the exact rules). Deterministic and pure.
func SlugifyHeading(headingText string) string {
	lower := strings.ToLower(strings.TrimSpace(headingText))
	stripped := slugStripRE.ReplaceAllString(lower, "")
	collapsed := slugSpaceRE.ReplaceAllString(stripped, "-")
	return slugTrimRE.ReplaceAllString(collapsed, "")
}

// HeadingAnchors returns the anchor set of one document's headings, GitHub
// order: first occurrence is bare, later duplicates get `-1`, `-2`, … —
// deterministic.
func HeadingAnchors(content string, skipLines map[int]bool) map[string]bool {
	anchors := make(map[string]bool)
	seen := make(map[string]int)
	for _, heading := range ExtractHeadings(content, skipLines) {
		base := SlugifyHeading(heading.Text)
		count := seen[base]
		seen[base] = count + 1
		if count == 0 {
			anchors[base] = true
		} else {
			anchors[base+"-"+strconv.Itoa(count)] = true
		}
	}
	return anchors
}

/* --------------------------------------------------------------------- links */

// LinkOccurrence is one inline Markdown link/image target (1-based line;
// images included).
type LinkOccurrence struct {
	// Target is the target as written (no title, no percent-decoding).
	Target string
	Line   int
}

// inlineLinkRE is TS INLINE_LINK — reference-style and autolinks are out of
// scope.
var inlineLinkRE = regexp.MustCompile(`!?\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)`)

// ExtractLinks returns the inline links in document order.
func ExtractLinks(content string) []LinkOccurrence {
	links := make([]LinkOccurrence, 0)
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		for _, match := range inlineLinkRE.FindAllStringSubmatch(line, -1) {
			links = append(links, LinkOccurrence{Target: match[2], Line: index + 1})
		}
	}
	return links
}

// tier1ExcludedLines returns the non-content line numbers of one content
// snapshot (fenced + front-matter lines) — links and headings there are
// literal content. The TS duplicated this private helper in
// structural-check.ts and links-check.ts; the Go port keeps ONE definition
// (the package must not declare it twice).
func tier1ExcludedLines(content string) map[int]bool {
	excluded := make(map[int]bool)
	for line := range ScanFences(content).FencedLines {
		excluded[line] = true
	}
	if frontMatter := ScanFrontMatter(content); frontMatter != nil {
		for line := range frontMatter.Lines {
			excluded[line] = true
		}
	}
	return excluded
}
