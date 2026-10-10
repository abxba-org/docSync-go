package globs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Pinned picomatch options (TS engine.ts PICO_MATCH_OPTIONS):
//
//		{ dot: true, windows: false }
//
//	  - dot: true      → hidden files and dotted directories DO match wildcards
//	    (`**/*.yml` matches `.hidden/config.yml`). This is NOT the picomatch
//	    default — docSync opts IN — so a pattern's own leading dot is not
//	    privileged: any wildcard already reaches dotfiles.
//	  - windows: false → POSIX separators only; `\` is a guard-rejected error,
//	    never a separator. Paths are POSIX relative paths by contract.
//
// The translation below is RE2-only: Go's regexp has no lookaround, so every
// picomatch lookahead is rewritten as an equivalent construct:
//
//   - picomatch's globstar body `(?:(?!(?:^|/)\.{1,2}(?:/|$)).)*?` — "any
//     characters, no `.`/`..` component anywhere" — becomes segNoDot/
//     globstarBody (`.` and `..` segments never match, even with { dot: true }).
//   - picomatch's segment-initial star `(?!\.{1,2}(?:/|$))(?=.)[^/]*?` — "a
//     non-empty component that is not `.`/`..`" — becomes starTail/starSlash.
//   - a leading `!` (picomatch wraps the regex in a negative lookahead) is
//     applied by CompiledGlob.Match instead, which is exactly equivalent.
//   - the two picomatch fastpath layers (the makeRe-level switch for patterns
//     starting with `.`/`*`, and the tokenizer-level fastpath for patterns
//     without `/()[]{}"`) are mirrored: both are observable, mainly through
//     the optional trailing slash.
const (
	// segHead matches a component starting with a non-dot character.
	segHead = `[^./][^/]*`
	// segDot matches a component starting with one dot followed by a non-dot,
	// non-slash character (".foo").
	segDot = `\.[^/.][^/]*`
	// segDotDot matches a component starting with two dots followed by at
	// least one more character ("..foo").
	segDotDot = `\.\.[^/]+`
	// segNoDot matches one path-segment component without "/" that is neither
	// "." nor ".." (picomatch's NO_DOTS lookahead, RE2 form).
	segNoDot = `(?:` + segHead + `|` + segDot + `|` + segDotDot + `)`
	// globstarBody matches the run of segments a `**` may span: zero or more
	// (possibly empty) components separated by "/", none being "." or "..".
	// It already tolerates one trailing "/".
	globstarBody = `(?:` + segNoDot + `?/)*` + segNoDot + `?`
	// starTail is a pattern-final star: a non-empty component (not "."/".."),
	// optionally followed by a trailing slash, or a bare slash (picomatch's
	// `(?!DOTS)(?=.)[^/]*?/?`).
	starTail = `(?:/|` + segNoDot + `(?:/)?)`
	// starSlashStar is a segment-initial star that is the last token of its
	// segment but not of the pattern: the component is optional (picomatch's
	// `(?=.)[^/]*?` — the required char may be the following "/" itself).
	starSlashStar = `(?:` + segNoDot + `)?`
	// dotStarTail is a star directly after a literal "." at the end of the
	// pattern (picomatch's `(?=.)[^/]*?/?`): at least one more character,
	// never a bare "." segment.
	dotStarTail = `(?:[^/]*/|[^/]+)`
	// starAny is a plain star: any run of non-separator characters.
	starAny = `[^/]*`
	// qmarkAny is `?`: exactly one non-separator character.
	qmarkAny = `[^/]`
	// negExtglob is the RE2 approximation of picomatch's negated extglob
	// `!(...)` (a negative lookahead in picomatch). It matches any
	// slash-free run; the excluded alternatives themselves also match — see
	// DEVIATIONS.
	negExtglob = `[^/]*`
)

// CompiledGlob is a compiled manifest glob (TS CompiledGlob).
type CompiledGlob struct {
	// Pattern is the pattern exactly as written.
	Pattern string
	// Specificity is the pattern's deterministic specificity score.
	Specificity GlobSpecificity

	re      *regexp.Regexp
	negated bool
}

// Match reports whether path matches the compiled glob with picomatch's
// { dot: true, windows: false } semantics. Two picomatch behaviors from
// picomatch.test are preserved exactly:
//
//   - an empty path never matches (not even a negated glob);
//   - a path exactly equal to the pattern matches literally, before (and
//     regardless of) the compiled pattern — this is why a brace pattern also
//     matches its own literal text.
func (g *CompiledGlob) Match(path string) bool {
	if path == "" {
		return false
	}
	if path == g.Pattern {
		return true
	}
	matched := g.re.MatchString(path)
	if g.negated {
		return !matched
	}
	return matched
}

// CompileGlob compiles a glob for matching. It is the ENFORCEMENT POINT for
// the ARCH §11.3 escape guards (fail closed, D8): a guarded pattern never
// compiles, so nothing downstream (MatchPaths, routing, lint) can silently use
// an unsafe glob. Returns a GlobGuardError (code EDOCSYNC_GLOB) when the
// pattern fails its guard — the Go form of the TS thrown error.
func CompileGlob(pattern string) (*CompiledGlob, error) {
	guard := GuardGlob(pattern)
	if !guard.OK {
		return nil, NewGlobGuardError(pattern, guard)
	}
	body, negated := translatePattern(pattern)
	re, err := regexp.Compile(`\A(?:` + body + `)\z`)
	if err != nil {
		// The translator is total by construction; fail closed rather than
		// silently matching nothing.
		return nil, docutil.New(CodeGlob, fmt.Sprintf("glob pattern %q rejected (uncompilable): %v", pattern, err))
	}
	return &CompiledGlob{
		Pattern:     pattern,
		Specificity: ScoreGlob(pattern),
		re:          re,
		negated:     negated,
	}, nil
}

// GlobMatch is one (path, pattern) hit (TS GlobMatch).
type GlobMatch struct {
	Pattern    string `json:"pattern"`
	EntryIndex int    `json:"entryIndex"`
	Path       string `json:"path"`
}

// PatternEntry is a (manifest entry index, pattern) pair to match against —
// the TS inline `{ index: number; pattern: string }` type of matchPaths'
// patterns parameter.
type PatternEntry struct {
	Index   int
	Pattern string
}

// MatchPaths matches changed POSIX paths against (entryIndex, pattern) pairs.
// Every pattern is compiled first (a failing guard aborts the whole call with
// a GlobGuardError before any matching — fail closed, D8), then every
// (path, pattern) hit is reported, grouped per changed path in input order.
// Empty inputs yield empty results.
func MatchPaths(changedPaths []string, patterns []PatternEntry) ([]GlobMatch, error) {
	type entry struct {
		PatternEntry
		matcher *CompiledGlob
	}
	compiled := make([]entry, 0, len(patterns))
	for _, p := range patterns {
		matcher, err := CompileGlob(p.Pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, entry{PatternEntry: p, matcher: matcher})
	}
	matches := []GlobMatch{}
	for _, path := range changedPaths {
		for _, e := range compiled {
			if e.matcher.Match(path) {
				matches = append(matches, GlobMatch{
					Pattern:    e.Pattern,
					EntryIndex: e.Index,
					Path:       path,
				})
			}
		}
	}
	return matches, nil
}

// extWordSuffix matches picomatch's fastpath default case
// `/^(.*?)\.(\w+)$/`: a prefix followed by a final `.word` extension.
var extWordSuffix = regexp.MustCompile(`^(.*?)\.([A-Za-z0-9_]+)$`)

// translatePattern converts a glob into an anchored RE2 body (without the
// anchors) plus its negation flag. Steps mirror picomatch's parse(): strip a
// leading `!` run (odd count negates), strip one leading "./", then translate
// — through the makeRe-level fastpath when the pattern starts with "." or
// "*", otherwise through the tokenizer.
func translatePattern(pattern string) (body string, negated bool) {
	// picomatch's makeRe-level fastpath gate looks at the ORIGINAL pattern:
	// a negated pattern ("!…") never takes it, so a negated body keeps the
	// tokenizer form (no optional trailing slash).
	layer1 := len(pattern) > 0 && (pattern[0] == '.' || pattern[0] == '*')
	p := pattern
	if len(p) > 0 && p[0] == '!' && !bangOpensExtglob(p) {
		// picomatch negate(): consume consecutive "!" while the run is not an
		// extglob opener; an odd total count negates, but every "!" is dropped.
		i := 0
		for i+1 < len(p) && p[i+1] == '!' &&
			(i+2 >= len(p) || p[i+2] != '(' || (i+3 < len(p) && p[i+3] == '?')) {
			i++
		}
		negated = (i+1)%2 == 1
		p = p[i+1:]
	}
	// The makeRe-level fastpath applies when the original pattern starts with
	// "." or "*", and it strips its own "./" prefix.
	if layer1 {
		if strings.HasPrefix(p, "./") {
			p = p[2:]
		}
		p = collapseGlobstars(p)
		if body, ok := fastpathCreate(p, true); ok {
			return body, negated
		}
	}
	// picomatch advances past a leading "./" (it is not part of the match).
	if strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = collapseGlobstars(p)
	return (&translator{pattern: p, fastpath: fastpathEligible(pattern)}).translateAll(), negated
}

// fastpathEligible reports whether picomatch would compile the pattern through
// its tokenizer-level fastpath (parse.js): it does NOT start with "*" or "!"
// and contains none of `/()[]{}"`. Fastpath patterns lose the optional
// trailing slash, which is observable (`a*` does not match `a/`, but `*.md`
// matches `a.md/`).
func fastpathEligible(pattern string) bool {
	if pattern == "" {
		return false
	}
	if pattern[0] == '*' || pattern[0] == '!' {
		return false
	}
	return !strings.ContainsAny(pattern, "/()[]{}\"")
}

// fastpathCreate mirrors picomatch's makeRe-level fastpath switch
// (parse.fastpaths) for patterns starting with "." or "*", with `final`
// selecting the top-level forms (which append one optional trailing slash)
// over the recursive base forms. Returns ok=false when the pattern is not one
// of the fastpath shapes.
func fastpathCreate(pattern string, final bool) (string, bool) {
	switch pattern {
	case "*":
		if final {
			return starTail, true
		}
		return starAny, true
	case ".*":
		if final {
			return `\.` + dotStarTail, true
		}
		return `\.` + starAny, true
	case "*.*":
		if final {
			return starAny + `\.` + dotStarTail, true
		}
		return starAny + `\.` + starAny, true
	case "*/*":
		if final {
			return starSlashStar + `/` + starTail, true
		}
		return starSlashStar + `/` + starAny, true
	case "**":
		if final {
			return globstarBody + `(?:/)?`, true
		}
		return globstarBody, true
	case "**/*":
		if final {
			return `(?:` + globstarBody + `/)?` + starTail, true
		}
		return `(?:` + globstarBody + `/)?` + starAny, true
	case "**/*.*":
		if final {
			return `(?:` + globstarBody + `/)?` + starAny + `\.` + dotStarTail, true
		}
		return `(?:` + globstarBody + `/)?` + starAny + `\.` + starAny, true
	case "**/.*":
		if final {
			return `(?:` + globstarBody + `/)?` + `\.` + dotStarTail, true
		}
		return `(?:` + globstarBody + `/)?` + `\.` + starAny, true
	}
	// default: a fastpath prefix followed by a final ".word" extension.
	if m := extWordSuffix.FindStringSubmatch(pattern); m != nil {
		if base, ok := fastpathCreate(m[1], false); ok {
			if final {
				return base + `\.` + m[2] + `(?:/)?`, true
			}
			return base + `\.` + m[2], true
		}
	}
	return "", false
}

// bangOpensExtglob reports whether p starts with the `!(...)` extglob opener
// (which must NOT be consumed as negation). picomatch: `peek() === '(' &&
// (peek(2) !== '?' || !/[!=<:]/.test(peek(3)))`.
func bangOpensExtglob(p string) bool {
	if len(p) < 2 || p[0] != '!' || p[1] != '(' {
		return false
	}
	if len(p) >= 4 && p[2] == '?' && strings.IndexByte("!=<:", p[3]) >= 0 {
		return false
	}
	return true
}

// isGlobstarSegment reports whether s is a full-segment globstar (a run of
// two or more stars).
func isGlobstarSegment(s string) bool {
	return len(s) >= 2 && strings.Trim(s, "*") == ""
}

// collapseGlobstars merges consecutive globstar segments separated by a single
// "/" into one globstar (picomatch strips consecutive `/**/`), so `**/**`,
// `a/**/**` and `**/**/x` behave exactly like `**`, `a/**` and `**/x`.
// An empty segment between them (a double slash) blocks the collapse, exactly
// as in picomatch.
func collapseGlobstars(p string) string {
	if !strings.Contains(p, "**") {
		return p
	}
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		if isGlobstarSegment(s) && len(out) > 0 && isGlobstarSegment(out[len(out)-1]) {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, "/")
}

// translator walks a glob body once and emits its RE2 translation. Brace and
// extglob alternatives are translated as fragments: they never receive the
// pattern-final star form or the optional trailing slash.
type translator struct {
	pattern  string
	pos      int
	out      strings.Builder
	fragment bool
	fastpath bool
	prevChar byte
	// prevWasStar reports whether the token before a separator was a plain
	// star (picomatch's `afterStar`): a globstar after it does NOT get the
	// `|$` (parent-directory) alternative, because `*/**` cannot match `a`.
	prevWasStar bool
	// slashAfterStar reports whether the separator just emitted directly
	// followed a star token (only meaningful to a following globstar).
	slashAfterStar bool
}

func (t *translator) translateAll() string {
	for t.pos < len(t.pattern) {
		t.scanToken()
	}
	return t.out.String()
}

func (t *translator) scanToken() {
	c := t.pattern[t.pos]
	switch c {
	case '*':
		t.scanStar()
	case '?':
		t.scanQmark()
	case '[':
		t.scanBracket()
	case '{':
		t.scanBrace()
	case '/':
		t.scanSlash()
	case '@', '+':
		if t.extglobOpens() {
			t.scanExtgroup(c)
			return
		}
		t.literal(c)
	case '!':
		if t.extglobOpens() {
			t.scanExtgroup(c)
			return
		}
		t.literal(c)
	case '\\':
		// Escaped literal (guards reject backslashes, but stay total).
		t.pos++
		if t.pos < len(t.pattern) {
			t.out.WriteString(regexp.QuoteMeta(string(t.pattern[t.pos])))
			t.pos++
		} else {
			t.out.WriteString(`\\`)
		}
		t.prevChar = '\\'
		t.prevWasStar = false
		t.slashAfterStar = false
	default:
		t.literal(c)
	}
}

func (t *translator) literal(c byte) {
	t.out.WriteString(regexp.QuoteMeta(string(c)))
	t.pos++
	t.prevChar = c
	t.prevWasStar = false
	t.slashAfterStar = false
}

// scanSlash emits a separator, absorbing it into a trailing `/**` so that
// `x/**` compiles to `x(?:/GS)?` (the parent directory itself matches). The
// slash is NOT absorbed after a star (`*/**` requires the separator).
func (t *translator) scanSlash() {
	rest := t.pattern[t.pos+1:]
	if len(rest) >= 2 && strings.Trim(rest, "*") == "" && !t.prevWasStar {
		t.pos++
		t.prevChar = '/'
		t.prevWasStar = false
		t.slashAfterStar = false
		return
	}
	t.slashAfterStar = t.prevWasStar
	t.out.WriteByte('/')
	t.pos++
	t.prevChar = '/'
	t.prevWasStar = false
}

func (t *translator) scanStar() {
	// A single `*` immediately followed by `(` (next char not `?`) is the
	// extglob star; a `**` run before `(` is a globstar plus a literal paren.
	if t.pos+1 < len(t.pattern) && t.pattern[t.pos+1] == '(' &&
		!(t.pos+2 < len(t.pattern) && t.pattern[t.pos+2] == '?') {
		t.scanExtgroup('*')
		return
	}
	start := t.pos
	for t.pos < len(t.pattern) && t.pattern[t.pos] == '*' {
		t.pos++
	}
	runLen := t.pos - start
	segStart := start == 0 || t.pattern[start-1] == '/'
	atEnd := t.pos == len(t.pattern)
	segEnd := atEnd || t.pattern[t.pos] == '/'
	if runLen >= 2 && segStart && segEnd {
		t.scanGlobstar(start, t.pos)
		t.prevChar = '*'
		t.prevWasStar = false
		t.slashAfterStar = false
		return
	}
	switch {
	case segStart && atEnd && !t.fragment:
		// Pattern-final star (the optional trailing slash lives inside this
		// form).
		t.out.WriteString(starTail)
	case segStart && atEnd:
		// Fragment-final star: no trailing-slash tolerance.
		t.out.WriteString(starAny)
	case segStart && t.pattern[t.pos] == '/':
		// Segment-final star before a "/": the component is optional.
		t.out.WriteString(starSlashStar)
	case atEnd && !t.fragment && !t.fastpath:
		// Non-segment-initial star at pattern end: picomatch appends `/?`.
		t.out.WriteString(starAny + `(?:/)?`)
	default:
		t.out.WriteString(starAny)
	}
	t.prevChar = '*'
	t.prevWasStar = true
	t.slashAfterStar = false
}

// scanGlobstar emits a full-segment `**` (a run of ≥2 stars bounded by `/` or
// the pattern ends). The three positions map to:
//
//	`**`        → GS                 (already tolerates a trailing "/")
//	`**/x`      → (?:GS/)?x          (zero or more leading segments)
//	`x/**`      → x(?:/GS)?          (the parent dir itself matches — picomatch's `|$`)
//	`x/**/y`    → x/(?:GS/)?y        (zero or more middle segments)
//
// After a star segment (`*/**`) the trailing form loses the `|$` alternative:
// the separator is required, so `*/**` cannot match a bare `a`.
func (t *translator) scanGlobstar(start, end int) {
	switch {
	case start == 0 && end == len(t.pattern):
		t.out.WriteString(globstarBody)
	case start == 0:
		t.out.WriteString(`(?:` + globstarBody + `/)?`)
		t.pos++ // skip the "/" after the globstar
	case end == len(t.pattern):
		if t.slashAfterStar {
			// The separator was emitted by scanSlash; the globstar body
			// follows it directly (no parent-directory alternative).
			t.out.WriteString(globstarBody)
			return
		}
		t.out.WriteString(`(?:/` + globstarBody + `)?`)
	default:
		t.out.WriteString(`(?:` + globstarBody + `/)?`)
		t.pos++ // skip the "/" after the globstar
	}
}

func (t *translator) scanQmark() {
	// `?(` is the extglob qmark unless it directly follows a literal "(".
	if t.pos+1 < len(t.pattern) && t.pattern[t.pos+1] == '(' &&
		!(t.pos+2 < len(t.pattern) && t.pattern[t.pos+2] == '?') && t.prevChar != '(' {
		t.scanExtgroup('?')
		return
	}
	if t.prevChar == '(' {
		// picomatch escapes a qmark directly after a paren token.
		t.out.WriteString(`\?`)
	} else {
		t.out.WriteString(qmarkAny)
	}
	t.pos++
	t.prevChar = '?'
	t.prevWasStar = false
	t.slashAfterStar = false
}

// extglobOpens reports whether the current `@`/`+`/`!` opens an extglob group.
func (t *translator) extglobOpens() bool {
	c := t.pattern[t.pos]
	if t.pos+1 >= len(t.pattern) || t.pattern[t.pos+1] != '(' {
		return false
	}
	if c != '!' {
		return !(t.pos+2 < len(t.pattern) && t.pattern[t.pos+2] == '?')
	}
	return bangOpensExtglob(t.pattern[t.pos:])
}

// scanExtgroup translates `kind(...)`: `@`→group, `*`→zero+, `+`→one+,
// `?`→zero-or-one, `!`→negated. The negation extglob has no RE2 equivalent
// (picomatch compiles it to a negative lookahead); negExtglob is the closest
// conservative approximation — see DEVIATIONS.
func (t *translator) scanExtgroup(kind byte) {
	open := t.pos + 1 // the "("
	closeIdx, ok := findParenClose(t.pattern, open)
	if !ok {
		t.literal(kind)
		return
	}
	inner := t.pattern[open+1 : closeIdx]
	parts := splitTopLevel(inner, '|')
	alts := make([]string, 0, len(parts))
	for _, part := range parts {
		frag := &translator{pattern: part, fragment: true, fastpath: t.fastpath}
		alts = append(alts, frag.translateAll())
	}
	group := `(?:` + strings.Join(alts, `|`) + `)`
	switch kind {
	case '@':
		t.out.WriteString(group)
	case '*':
		t.out.WriteString(group + `*`)
	case '+':
		t.out.WriteString(group + `+`)
	case '?':
		t.out.WriteString(group + `?`)
	case '!':
		t.out.WriteString(negExtglob)
	}
	t.pos = closeIdx + 1
	t.prevChar = ')'
	t.slashAfterStar = false
	t.prevWasStar = false
}

// scanBrace translates `{...}`: comma-separated alternatives become an
// alternation, `{a..c}` becomes a character range, and a brace with neither is
// literal (picomatch escapes the braces but still parses the content).
func (t *translator) scanBrace() {
	closeIdx, ok := findBraceClose(t.pattern, t.pos)
	if !ok {
		t.literal('{')
		return
	}
	inner := t.pattern[t.pos+1 : closeIdx]
	if parts := splitTopLevel(inner, ','); len(parts) > 1 {
		alts := make([]string, 0, len(parts))
		for _, part := range parts {
			frag := &translator{pattern: part, fragment: true, fastpath: t.fastpath}
			alts = append(alts, frag.translateAll())
		}
		t.out.WriteString(`(?:` + strings.Join(alts, `|`) + `)`)
	} else if lo, hi, ok := dotRange(inner); ok {
		t.out.WriteString(`[` + lo + `-` + hi + `]`)
	} else {
		frag := &translator{pattern: inner, fragment: true, fastpath: t.fastpath}
		t.out.WriteString(`\{` + frag.translateAll() + `\}`)
	}
	t.pos = closeIdx + 1
	t.prevChar = '}'
	t.slashAfterStar = false
	t.prevWasStar = false
}

// dotRange reports the bounds of a `{a..c}`-style brace range. Only
// single-rune bounds in order are expanded (picomatch's own output for
// multi-rune bounds is a degenerate character class; we keep the brace
// literal instead — see DEVIATIONS).
func dotRange(inner string) (lo, hi string, ok bool) {
	idx := strings.Index(inner, "..")
	if idx <= 0 || idx+2 >= len(inner) {
		return "", "", false
	}
	lo, hi = inner[:idx], inner[idx+2:]
	if strings.Contains(lo, ".") || strings.Contains(hi, ".") {
		return "", "", false
	}
	loRunes := []rune(lo)
	hiRunes := []rune(hi)
	if len(loRunes) != 1 || len(hiRunes) != 1 || loRunes[0] > hiRunes[0] {
		return "", "", false
	}
	return string(loRunes[0]), string(hiRunes[0]), true
}

// scanBracket translates `[...]` with picomatch's bracket semantics:
// a leading "]" is a literal member, "[^...]" negates (and can never match a
// separator), "[!...]" is kept as an invalid-class double alternative (both
// the literal bracket text and the raw class), and POSIX classes such as
// [:alpha:] pass through to RE2.
func (t *translator) scanBracket() {
	start := t.pos
	i := t.pos + 1
	negated := false
	if i < len(t.pattern) && t.pattern[i] == '^' {
		negated = true
		i++
	}
	first := true
	var body strings.Builder
	closed := false
	for i < len(t.pattern) {
		c := t.pattern[i]
		if c == ']' && !first {
			closed = true
			break
		}
		first = false
		if c == '\\' && i+1 < len(t.pattern) {
			body.WriteByte('\\')
			body.WriteByte(t.pattern[i+1])
			i += 2
			continue
		}
		if c == '[' && i+1 < len(t.pattern) && t.pattern[i+1] == ':' {
			if j := strings.Index(t.pattern[i:], ":]"); j >= 0 {
				body.WriteString(t.pattern[i : i+j+2])
				i += j + 2
				continue
			}
		}
		body.WriteByte(c)
		i++
	}
	if !closed {
		t.literal('[')
		return
	}
	raw := t.pattern[start : i+1]
	content := body.String()
	// picomatch decides between "regex character class" and "match both the
	// literal text and the class": a class whose content carries no regex
	// special character (and no POSIX class) keeps BOTH readings, which is why
	// `[abc]` also matches the literal string "[abc]". A negated class always
	// takes the single-class route (its leading "^" is itself special).
	posix := strings.Contains(content, "[:")
	if !negated && !posix && !hasRegexSpecial(content) {
		t.out.WriteString(`(?:` + regexp.QuoteMeta(raw) + `|[` + content + `])`)
	} else {
		// A leading "]" must be escaped for RE2.
		if strings.HasPrefix(content, "]") {
			content = `\` + content
		}
		cls := `[` + content + `]`
		// picomatch adds "/" to a negated class (a class never matches a
		// separator) — but not to a POSIX class, whose compiled form keeps
		// "/" matchable, and not when "/" is already a member.
		if negated {
			cls = `[^` + content + `]`
			if !posix && !strings.Contains(content, "/") {
				cls = `[^` + content + `/]`
			}
		}
		if _, err := regexp.Compile(cls); err != nil {
			t.out.WriteString(regexp.QuoteMeta(raw))
		} else {
			t.out.WriteString(cls)
		}
	}
	t.pos = i + 1
	if !t.fragment && !t.fastpath && t.pos == len(t.pattern) {
		// picomatch's maybe_slash: a pattern ending in a bracket tolerates a
		// trailing slash.
		t.out.WriteString(`(?:/)?`)
	}
	t.prevChar = ']'
	t.prevWasStar = false
	t.slashAfterStar = false
}

// hasRegexSpecial reports whether s contains a regex special character per
// picomatch's REGEX_SPECIAL_CHARS `/[-*+?.^${}(|)[\]]/`. It decides whether a
// bracket is compiled as a plain regex class or matched both literally and as
// a class.
func hasRegexSpecial(s string) bool {
	return strings.ContainsAny(s, `-*+?.^${}()|[]`)
}

// findBraceClose returns the index of the "}" matching the "{" at open.
func findBraceClose(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '[':
			i = skipClass(s, i)
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// findParenClose returns the index of the ")" matching the "(" at open.
func findParenClose(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '[':
			i = skipClass(s, i)
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// skipClass returns the index of the "]" closing the class starting at i
// (a "]" as the first content char is a literal member). If the class is
// unterminated it returns the last index.
func skipClass(s string, i int) int {
	j := i + 1
	if j < len(s) && (s[j] == '^' || s[j] == '!') {
		j++
	}
	if j < len(s) && s[j] == ']' {
		j++
	}
	for j < len(s) && s[j] != ']' {
		if s[j] == '\\' {
			j++
		}
		j++
	}
	if j < len(s) {
		return j
	}
	return len(s) - 1
}

// splitTopLevel splits s on sep, ignoring separators nested in classes,
// braces or parens.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	parens, braces := 0, 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '[':
			i = skipClass(s, i)
		case '(':
			parens++
		case ')':
			if parens > 0 {
				parens--
			}
		case '{':
			braces++
		case '}':
			if braces > 0 {
				braces--
			}
		}
		if parens == 0 && braces == 0 && i < len(s) && s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
