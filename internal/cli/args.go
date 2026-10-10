// Ported from packages/cli/src/args.ts (P7-T01) — CLI argument parsing,
// fully dependency-free (OD-1: no commander/yargs).
//
// Tolerant positioning: the first positional is the verb and every flag
// (`--help`/`-h` included) may appear anywhere. Unknown verbs/flags/values are
// reported as an error string on the returned ParsedInvocation — parsing NEVER
// throws; the dispatcher (cmd.go) turns errors into usage messages with exit 2
// (config-class, D8).
package cli

import (
	"math"
	"os"
	"strconv"
	"strings"
)

// CLI_VERBS are the verbs accepted by the CLI surface (P7-T01; audit P12-T16).
var CLI_VERBS = []string{"check", "fix", "pr", "init", "audit"}

// CliVerb is one accepted verb.
type CliVerb string

// The accepted verbs.
const (
	VerbCheck CliVerb = "check"
	VerbFix   CliVerb = "fix"
	VerbPr    CliVerb = "pr"
	VerbInit  CliVerb = "init"
	VerbAudit CliVerb = "audit"
)

// GlyphMode is the renderer glyph mode: unicode arrows by default, ASCII
// fallback on demand.
type GlyphMode string

// The glyph modes.
const (
	GlyphsUnicode  GlyphMode = "unicode"
	GlyphsFallback GlyphMode = "fallback"
)

// AUDIT_CONCURRENCY_CEILING is the upper bound on the audit engine worker-pool
// cap (`--concurrency`): a valid positive SAFE integer is clamped to this
// ceiling so an absurdly large value never fans out unbounded work; a value
// beyond math.MaxInt64 fails validation as a usage error (F1-4).
const auditConcurrencyCeiling = 64

// AUDIT_COMMIT_WALK_CEILING is the upper bound on the sweep commit-walk
// (`--commit-walk`); the audit verb clamps a valid positive SAFE integer to
// this ceiling. Exported: the audit verb (commands_audit.go) owns the clamp.
const AuditCommitWalkCeiling = 1000

// ParsedInvocation is one parsed invocation. Quiet defaults ON (renderer quiet
// default); `--no-quiet` turns it off. Error is set iff parsing found a problem
// (unknown verb, unknown `--glyphs` value, malformed `--source-pr`, unexpected
// positional). Pointers are nil when the corresponding flag was absent.
type ParsedInvocation struct {
	Verb    CliVerb
	Help    bool
	JSON    bool
	Strict  bool
	Verbose bool
	Offline bool
	Quiet   bool
	Glyphs  GlyphMode

	// fix only (P7-T03/T04): allow application on a dirty worktree.
	AllowDirty bool
	// fix only (OD-R17): `--doc` selectors, repeatable.
	DocPaths []string
	// fix only (P7-T08): explicit fix-run id (canonical ULID).
	RunID *string
	// check only (P7-T02): base ref of the change window.
	Base *string
	// check/fix/pr run-key identity (`org/name`); default `local/repo`.
	Repo *string
	// pr only: re-derive from an existing pull request.
	SourcePr *int
	// pr only (P7-T16): explicit default-branch name.
	DefaultBranch *string
	// init only (OD-R15): overwrite existing config files.
	Force bool

	// audit only (P12-T16).
	AuditTrigger     *string
	AuditBranch      *string
	AuditConcurrency *string
	AuditCommitWalk  *string
	AuditPostMerge   bool
	AuditOrgReport   bool

	// Error is the parse error message; empty when parsing succeeded.
	Error string
}

// auditTriggerValue is the typed `--trigger` value.
type auditTriggerValue string

const (
	triggerNightly   auditTriggerValue = "nightly"
	triggerPostMerge auditTriggerValue = "post-merge"
)

func baseParsedInvocation() ParsedInvocation {
	return ParsedInvocation{
		Quiet:    true,
		Glyphs:   GlyphsUnicode,
		DocPaths: []string{},
	}
}

// environFunc reads one environment variable; nil value means "unset".
type environFunc func(string) (string, bool)

func defaultGlyphs(env environFunc) GlyphMode {
	if v, ok := env("DOC_SYNC_GLYPHS"); ok && v == "fallback" {
		return GlyphsFallback
	}
	return GlyphsUnicode
}

// argOption describes one accepted option for the tolerant parser.
type argOption struct {
	aliases  []string // '--name' and/or '-x'
	typ      string   // "boolean" | "string"
	multiple bool
}

var argOptions = []argOption{
	{aliases: []string{"--help", "-h"}, typ: "boolean"},
	{aliases: []string{"--json"}, typ: "boolean"},
	{aliases: []string{"--strict"}, typ: "boolean"},
	{aliases: []string{"--verbose"}, typ: "boolean"},
	{aliases: []string{"--offline"}, typ: "boolean"},
	{aliases: []string{"--no-quiet"}, typ: "boolean"},
	{aliases: []string{"--glyphs"}, typ: "string"},
	{aliases: []string{"--doc"}, typ: "string", multiple: true},
	{aliases: []string{"--base"}, typ: "string"},
	{aliases: []string{"--run-id"}, typ: "string"},
	{aliases: []string{"--repo"}, typ: "string"},
	{aliases: []string{"--allow-dirty"}, typ: "boolean"},
	{aliases: []string{"--source-pr"}, typ: "string"},
	{aliases: []string{"--default-branch"}, typ: "string"},
	{aliases: []string{"--force"}, typ: "boolean"},
	{aliases: []string{"--trigger"}, typ: "string"},
	{aliases: []string{"--branch"}, typ: "string"},
	{aliases: []string{"--concurrency"}, typ: "string"},
	{aliases: []string{"--commit-walk"}, typ: "string"},
	{aliases: []string{"--post-merge"}, typ: "boolean"},
	{aliases: []string{"--org-report"}, typ: "boolean"},
}

func canonicalOptionName(flag string) (string, *argOption) {
	for i := range argOptions {
		for _, a := range argOptions[i].aliases {
			if a == flag {
				return argOptions[i].aliases[0], &argOptions[i]
			}
		}
	}
	return "", nil
}

// looseArgs is the tolerant parse result: option values by canonical name plus
// the positional tokens. string options are string; multiple options are
// []string; boolean options are bool.
type looseArgs struct {
	values      map[string]any
	positionals []string
}

// parseLoose parses argv the way Node's parseArgs({allowPositionals:true,
// strict:false}) does for the option set above: known booleans take no value
// (`--name` → true), known strings consume `--name=value` or the next token
// (append for `multiple`), and anything else (unknown flags, their values,
// bare positionals) lands in positionals. It never errors.
func parseLoose(argv []string) looseArgs {
	result := looseArgs{values: map[string]any{}, positionals: []string{}}
	i := 0
	for i < len(argv) {
		token := argv[i]
		switch {
		case strings.HasPrefix(token, "--"):
			name := token
			inlineValue := ""
			hasInline := false
			if eq := strings.IndexByte(token, '='); eq >= 0 {
				name = token[:eq]
				inlineValue = token[eq+1:]
				hasInline = true
			}
			canonical, opt := canonicalOptionName(name)
			if opt == nil {
				// Non-strict: the unknown flag is a positional; its value (if
				// any) becomes its own positional too (Node parity).
				result.positionals = append(result.positionals, token)
				if !hasInline && i+1 < len(argv) && looksLikeFlagValue(argv[i+1]) {
					result.positionals = append(result.positionals, argv[i+1])
					i++
				}
				i++
				continue
			}
			switch opt.typ {
			case "boolean":
				result.values[canonical] = true
			case "string":
				var value string
				if hasInline {
					value = inlineValue
				} else if i+1 < len(argv) {
					value = argv[i+1]
					i++
				}
				if opt.multiple {
					list, _ := result.values[canonical].([]string)
					result.values[canonical] = append(list, value)
				} else {
					result.values[canonical] = value
				}
			}
			i++
		case token == "-h":
			result.values["--help"] = true
			i++
		case len(token) > 1 && strings.HasPrefix(token, "-"):
			// Unknown short option cluster → positional(s) (Node non-strict).
			result.positionals = append(result.positionals, token)
			if i+1 < len(argv) && looksLikeFlagValue(argv[i+1]) {
				result.positionals = append(result.positionals, argv[i+1])
				i++
			}
			i++
		default:
			result.positionals = append(result.positionals, token)
			i++
		}
	}
	return result
}

// looksLikeFlagValue reports whether a token is a flag value (not another flag,
// not a lone '-').
func looksLikeFlagValue(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	return true
}

func asBool(values map[string]any, name string) bool {
	v, _ := values[name].(bool)
	return v
}

func asStringValue(values map[string]any, name string) (string, bool) {
	v, ok := values[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func asStringArray(values map[string]any, name string) []string {
	list, _ := values[name].([]string)
	return list
}

// parseIntLenient mirrors Number.parseInt(value, 10): a leading optional sign
// and digits; ok is false when no digit could be parsed (NaN).
func parseIntLenient(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	end := 0
	if s[0] == '+' || s[0] == '-' {
		end = 1
	}
	digits := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// isDigits reports whether s is non-empty and all ASCII digits (TS /^\d+$/).
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ParseCliArgs parses `docsync <verb> [flags]` against the real process env;
// never throws (see module doc).
func ParseCliArgs(argv []string) ParsedInvocation {
	return parseCliArgs(argv, os.LookupEnv)
}

// ParseCliArgsWithEnv parses `docsync <verb> [flags]` against an injected
// environment (glyph-mode default); never throws.
func ParseCliArgsWithEnv(argv []string, env map[string]string) ParsedInvocation {
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
	return parseCliArgs(argv, lookup)
}

func parseCliArgs(argv []string, env environFunc) ParsedInvocation {
	parsed := parseLoose(argv)
	values := parsed.values

	result := baseParsedInvocation()
	result.Glyphs = defaultGlyphs(env)
	result.Help = asBool(values, "--help")
	result.JSON = asBool(values, "--json")
	result.Strict = asBool(values, "--strict")
	result.Verbose = asBool(values, "--verbose")
	result.Offline = asBool(values, "--offline")
	// Quiet default ON; --no-quiet switches the renderer back to per-row output.
	result.Quiet = !asBool(values, "--no-quiet")
	result.AllowDirty = asBool(values, "--allow-dirty")
	result.DocPaths = asStringArray(values, "--doc")
	result.Force = asBool(values, "--force")

	if rawRunID, ok := asStringValue(values, "--run-id"); ok && len(rawRunID) > 0 {
		runID := rawRunID
		result.RunID = &runID
	}
	if rawBase, ok := asStringValue(values, "--base"); ok && len(rawBase) > 0 {
		base := rawBase
		result.Base = &base
	}
	if rawRepo, ok := asStringValue(values, "--repo"); ok && len(rawRepo) > 0 {
		repo := rawRepo
		result.Repo = &repo
	}

	if rawGlyphs, ok := asStringValue(values, "--glyphs"); ok {
		if rawGlyphs == "unicode" || rawGlyphs == "fallback" {
			result.Glyphs = GlyphMode(rawGlyphs)
		} else {
			result.Error = "unknown --glyphs value: " + rawGlyphs + " (expected unicode|fallback)"
			return result
		}
	}

	if rawDefaultBranch, ok := asStringValue(values, "--default-branch"); ok && len(rawDefaultBranch) > 0 {
		defaultBranch := rawDefaultBranch
		result.DefaultBranch = &defaultBranch
	}

	if rawSourcePr, ok := asStringValue(values, "--source-pr"); ok {
		asNumber, parsed := parseIntLenient(rawSourcePr)
		if !parsed || asNumber <= 0 || asNumber > math.MaxInt32 {
			result.Error = "invalid --source-pr value: " + rawSourcePr + " (expected a positive integer)"
			return result
		}
		sourcePr := asNumber
		result.SourcePr = &sourcePr
	}

	// --- audit verb flags (P12-T16) ---
	result.AuditPostMerge = asBool(values, "--post-merge")
	result.AuditOrgReport = asBool(values, "--org-report")

	if rawTrigger, ok := asStringValue(values, "--trigger"); ok {
		// Loop B is never on the PR critical path (P11-T18): only the two
		// non-PR contexts are accepted; anything else is a usage defect.
		if rawTrigger == string(triggerNightly) || rawTrigger == string(triggerPostMerge) {
			trigger := rawTrigger
			result.AuditTrigger = &trigger
		} else {
			result.Error = "unknown --trigger value: " + rawTrigger +
				" (expected nightly|post-merge — PR contexts are structurally refused, ARCH §7.2)"
			return result
		}
	}

	if rawBranch, ok := asStringValue(values, "--branch"); ok {
		// F1-4: an empty/blank --branch is a usage error — NEVER a silent fall
		// back to `main`. The exit-2 lane is consistent with the other audit
		// validators.
		if strings.TrimSpace(rawBranch) != "" {
			branch := rawBranch
			result.AuditBranch = &branch
		} else {
			result.Error = "invalid --branch value: " + jsonStringLiteral(rawBranch) +
				" (expected a non-empty branch name)"
			return result
		}
	}

	if rawConcurrency, ok := asStringValue(values, "--concurrency"); ok {
		parsedInt, parsedOk := parseIntLenient(rawConcurrency)
		if !isDigits(rawConcurrency) || !parsedOk || parsedInt < 1 || parsedInt > math.MaxInt32 {
			result.Error = "invalid --concurrency value: " + rawConcurrency +
				" (expected a positive safe integer)"
			return result
		}
		// Clamp to the engine worker-pool ceiling (F1-4): a valid but absurdly
		// large integer never fans out unbounded work.
		clamped := parsedInt
		if clamped > auditConcurrencyCeiling {
			clamped = auditConcurrencyCeiling
		}
		result.AuditConcurrency = strPtr(strconv.Itoa(clamped))
	}

	if rawCommitWalk, ok := asStringValue(values, "--commit-walk"); ok {
		parsedInt, parsedOk := parseIntLenient(rawCommitWalk)
		if !isDigits(rawCommitWalk) || !parsedOk || parsedInt < 1 || parsedInt > math.MaxInt32 {
			result.Error = "invalid --commit-walk value: " + rawCommitWalk +
				" (expected a positive safe integer)"
			return result
		}
		// Deliberately NOT clamped here (unlike `--concurrency`): the ceiling is
		// applied by the audit verb, which owns the walk depth (L2).
		result.AuditCommitWalk = strPtr(rawCommitWalk)
	}

	// Verb position: first positional. Any later positional is unexpected.
	if len(parsed.positionals) > 0 {
		verbRaw := parsed.positionals[0]
		extra := parsed.positionals[1:]
		if !isKnownVerb(verbRaw) {
			result.Error = "unknown verb: " + jsonStringLiteral(verbRaw) +
				" (expected one of " + strings.Join(CLI_VERBS, ", ") + ")"
			return result
		}
		result.Verb = CliVerb(verbRaw)
		if len(extra) > 0 {
			quoted := make([]string, len(extra))
			for i, a := range extra {
				quoted[i] = jsonStringLiteral(a)
			}
			result.Error = "unexpected argument(s): " + strings.Join(quoted, ", ")
			return result
		}
	}

	return result
}

func isKnownVerb(v string) bool {
	for _, verb := range CLI_VERBS {
		if verb == v {
			return true
		}
	}
	return false
}

// jsonStringLiteral renders s as a JSON string literal (TS JSON.stringify),
// used for the unknown-verb/unexpected-argument/--branch error messages.
func jsonStringLiteral(s string) string {
	return strconv.Quote(s)
}

// UsageText is the short usage text (P7-T01): verbs, flags, and the four exit
// codes with their meaning — one line each (P7-T06/T07). Returned WITHOUT a
// trailing newline (the caller's stdout writer appends one per line).
func UsageText() string {
	return strings.Join(usageLines, "\n")
}

// usageLines is the byte-stable usage body (TS usageText()).
var usageLines = []string{
	"docsync — Git-native documentation consistency engine (P7-T01)",
	"",
	"Usage: docsync <verb> [flags]",
	"",
	"Verbs:",
	"  check   detect documentation drift",
	"  fix     apply verified minimal patches",
	"  pr      open a documentation PR",
	"  init    scaffold a minimal valid .docsync/ configuration (OD-R15)",
	"  audit   run the Loop B background drift audit (advisory only, P12-T16)",
	"",
	"Shared flags:",
	"  --json                    machine-readable output",
	"  --strict                  promote advisory rows to drift (D10)",
	"  --verbose                 per-stage timing output",
	"  --offline                 deterministic-only run; never calls an AI stage (OD-R16)",
	"  --no-quiet                show per-row output in non-error runs (quiet is the default)",
	"  --glyphs <unicode|fallback>  glyph set (default auto; env DOC_SYNC_GLYPHS=fallback)",
	"",
	"Verb flags:",
	"  check: --base <ref>         base of the change window (default origin/HEAD when resolvable)",
	"  check: --repo <org/name>    run-key repository identity (default local/repo)",
	"  fix:   --allow-dirty          permit application on a dirty worktree",
	"  fix:   --run-id <ulid>        explicit fix-run id (canonical ULID; default auto-generated, P7-T08)",
	"  fix:   --doc <path>           restrict to specific documents (repeatable, OD-R17)",
	"  pr:    --source-pr <number>   re-derive from an existing pull request",
	"  pr:    --default-branch <name>  default branch of the repository (default origin/HEAD deref)",
	"  init:  --force                overwrite existing .docsync/ config files",
	"  audit: --trigger <nightly|post-merge>  trigger context (default nightly; PR contexts are structurally refused, ARCH §7.2)",
	"  audit: --branch <name>        branch the trigger runs on (default main)",
	"  audit: --post-merge           also configure the post-merge trigger (OD-R27 opt-in)",
	"  audit: --concurrency <n>      engine worker-pool cap (default 4, P12-T16)",
	"  audit: --commit-walk <n>      sweep commit-walk bound (default 50, P12-T16)",
	"  audit: --org-report           render the org-shaped drift report (P12-T15 surface, advisory)",
	"",
	"Exit codes:",
	"  0  no drift detected",
	"  1  drift detected (candidates found)",
	"  2  configuration/manifest error (fail-closed before any AI stage, D8)",
	"  3  runtime failure (fail-closed, D14)",
	"",
}

func strPtr(s string) *string { return &s }
