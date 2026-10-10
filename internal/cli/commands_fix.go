// Ported from packages/cli/src/commands/fix.ts (+ fix/flow.ts, fix/capture.ts,
// fix/record-store.ts; P7-T08..T13) — the WRITE verb: run the composed pipeline
// through the fix flow, apply only verified minimal patches to manifest-listed
// writable docs, record the fix-run provenance, render, resolve the exit code.
//
// Fail-closed order (D8):
//  1. `--offline` is REFUSED (OD-R16: fix requires the model-backed stages);
//  2. the run id: `--run-id` must be a canonical 26-char ULID else usage error;
//  3. compose the fix runtime (fails closed before any AI stage);
//  4. resolve the change window exactly like check;
//  5. run the fix flow: same-run gate → dirty gate → pipeline run → candidate
//     gates (write-scope incidents, verification, selectors) → apply → CAS
//     fix-run record;
//  6. render report + fix summary lines;
//  7. exit: config-error → 2; runtime-error → 3; noop or completed with
//     applied=0 ∧ refused=0 → 0; applied>0 ∨ refused>0 → 1.
//
// Credentials discipline (§3 rule 8): the API key never reaches this module.
// Library discipline: never os.Exit — the returned DocsyncExitCode is the
// process exit code (P7-T06/T07).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

// fixPrefix is the stderr line prefix for fix diagnostics.
const fixPrefix = "docsync fix:"

// FixOptions are the runFix options.
type FixOptions struct {
	// Cwd is the repository root; defaults to the process cwd.
	Cwd string
	// Env is the environment map (key source only); defaults to os.Environ().
	Env map[string]string
	// Clock is the injected ISO-8601 clock; defaults to wallClock.
	Clock func() string
	// Transport is the model transport injection seam (recorded doubles, OD-2).
	Transport any
	// Budgets are the pipeline budget overrides.
	Budgets *orchestrator.PipelineBudgetOverrides
	// FlowDeps (tests) injects a fully-constructed FixFlowDeps, bypassing
	// composition + the change-window resolve — the flow is exercised directly.
	FlowDeps *FixFlowDeps
}

// fixGlyphs are the glyph pairs for the summary lines (§12.1 + ASCII).
type fixGlyphs struct{ applied, skipped, refused string }

func fixGlyphSet(mode GlyphMode) fixGlyphs {
	if mode == GlyphsFallback {
		return fixGlyphs{applied: "[ok]", skipped: "[skip]", refused: "[!]"}
	}
	return fixGlyphs{applied: "✓", skipped: "⏭", refused: "⚠"}
}

/* ------------------------------------------------------------- flow result */

// FixFlowApplied is one applied patch.
type FixFlowApplied struct {
	DocPath      string `json:"docPath"`
	PatchRef     string `json:"patchRef"`
	ThreeWayUsed bool   `json:"threeWayUsed"`
}

// FixFlowSkipped is one skipped candidate.
type FixFlowSkipped struct {
	DocPath string `json:"docPath"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
}

// FixFlowRefused is one refused candidate (P7-T10 lanes).
type FixFlowRefused struct {
	DocPath    string `json:"docPath"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail"`
	ReDetected bool   `json:"reDetected"`
}

// FixFlowResultKind is the kind of the closed flow result union.
type FixFlowResultKind string

const (
	FixResultNoop        FixFlowResultKind = "noop"
	FixResultCompleted   FixFlowResultKind = "completed"
	FixResultConfigError FixFlowResultKind = "config-error"
	FixResultRuntimeErr  FixFlowResultKind = "runtime-error"
)

// FixFlowResult is the closed flow result union.
type FixFlowResult struct {
	Kind    FixFlowResultKind
	Reason  string                   // noop reason
	Report  *schemas.CanonicalReport // completed
	Applied []FixFlowApplied
	Skipped []FixFlowSkipped
	Refused []FixFlowRefused
	Detail  string // config/runtime error detail
}

// RenderFixSummary renders the fix summary lines (pure): one line per
// applied/skipped/refused entry, in flow order. Empty result → no lines.
func RenderFixSummary(result FixFlowResult, mode GlyphMode) []string {
	glyph := fixGlyphSet(mode)
	lines := []string{}
	for _, entry := range result.Applied {
		lines = append(lines, glyph.applied+" applied "+entry.DocPath+" ("+entry.PatchRef+")")
	}
	for _, entry := range result.Skipped {
		lines = append(lines, glyph.skipped+" skipped "+entry.DocPath+" ("+entry.Reason+" — "+entry.Detail+")")
	}
	for _, entry := range result.Refused {
		lines = append(lines, glyph.refused+" refused "+entry.DocPath+" ("+entry.Reason+" — "+entry.Detail+
			"; re-detected: "+boolYesNo(entry.ReDetected)+")")
	}
	return lines
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

/* --------------------------------------------------------------------- deps */

// PatchSource resolves the verified DocPatch object for a candidate (nil = no
// patch resolvable → 'not-a-candidate').
type PatchSource func(ctx context.Context, docPath string, patchRef *string) (*schemas.DocPatch, error)

// ReVerifyPort is the re-verification port for 3-way race merges (nil → races
// abstain, fail closed).
type ReVerifyPort func(patchedContent string, racePatch *schemas.DocPatch) (pass bool, detail string)

// FixApplyOutcome is the flow-facing apply outcome (mirrors patch.ApplyOutcome).
type FixApplyOutcome struct {
	Kind              string // applied | noop | unanchored | abstain
	ResultContent     string
	ThreeWayUsed      bool
	NoopReason        string
	AbstainReason     string
	Detail            string
	UnanchoredReasons []string
}

// FixApplyFn applies one patch (tests inject fakes; production = patch.ApplyDocPatch).
type FixApplyFn func(ctx context.Context, patchDoc *schemas.DocPatch, worktreeContent *string, allowlist, protected map[string]bool, isRunApplied func(string, string) bool) (FixApplyOutcome, error)

// FixIncidentPort records write-scope safety incidents (invariants 2/3, P6-T12).
type FixIncidentPort interface {
	Record(at, runID, rule, offendingPath, detail, docPath string) error
}

// FixRunRecordStore is the CAS fix-run record store (record-store.ts): the
// same-run gate + prior-run patch_ref provenance lookups. Backed by an
// append-only JSONL file under the state dir.
type FixRunRecordStore struct {
	stateDir string
	mu       sync.Mutex
}

// NewFixRunRecordStore builds a store rooted at the given state dir.
func NewFixRunRecordStore(stateDir string) *FixRunRecordStore {
	return &FixRunRecordStore{stateDir: stateDir}
}

func (s *FixRunRecordStore) path() string {
	return filepath.Join(s.stateDir, "fix-run-records.jsonl")
}

// FixRunRecord is one persisted fix-run record (docsync.fix-run-record/v1).
type FixRunRecord struct {
	Schema    string             `json:"schema"`
	RunID     string             `json:"run_id"`
	Applied   []FixRecordApplied `json:"applied"`
	PatchRefs []string           `json:"patch_refs,omitempty"`
}

// FixRecordApplied is one applied row of the record.
type FixRecordApplied struct {
	DocPath      string `json:"doc_path"`
	PatchRef     string `json:"patch_ref"`
	ThreeWayUsed bool   `json:"three_way_used"`
}

// Has reports whether a record for runID exists (same-run gate).
func (s *FixRunRecordStore) Has(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.readAll() {
		if rec.RunID == runID {
			return true
		}
	}
	return false
}

// HasPatchRef reports whether a prior record carries patchRef (provenance only).
func (s *FixRunRecordStore) HasPatchRef(patchRef string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.readAll() {
		for _, ref := range rec.PatchRefs {
			if ref == patchRef {
				return true
			}
		}
	}
	return false
}

// Write appends the record (CAS append; a duplicate run id is idempotent).
func (s *FixRunRecordStore) Write(record FixRunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *FixRunRecordStore) readAll() []FixRunRecord {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil
	}
	var records []FixRunRecord
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record FixRunRecord
		if json.Unmarshal([]byte(line), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

// FixFlowDeps is everything the fix flow needs, injected.
type FixFlowDeps struct {
	Runtime     CliRuntime
	Git         *gitx.GitClient
	Store       *FixRunRecordStore
	Incidents   FixIncidentPort
	Io          CliIo
	Clock       func() string
	PatchSource PatchSource
	ReVerify    ReVerifyPort
	Apply       FixApplyFn
}

// FixFlowInput is the fix flow input.
type FixFlowInput struct {
	RepoRoot     string
	Repo         string
	Base         string
	Head         string
	RunID        string
	AllowDirty   bool
	DocSelectors []string
	// Budgets are the pipeline budget overrides the run carries (the CLI
	// passes the command-level overrides through to the runtime, TS parity).
	Budgets *orchestrator.PipelineBudgetOverrides
}

// noopIncident is a silent incident sink (production default until the incident
// sink is rebound; incidents are best-effort bookkeeping here).
type noopIncident struct{}

func (noopIncident) Record(_, _, _, _, _, _ string) error { return nil }

/* ------------------------------------------------- the incident log sink */

// incidentLogFilename is the safety-incident ledger name under the state dir
// (TS INCIDENT_LOG_FILENAME: `incidents.jsonl`).
const incidentLogFilename = "incidents.jsonl"

// safetyIncidentRecord is the on-disk record (TS SAFETY_INCIDENT_SCHEMA:
// docsync.safety-incident/v1 — strict field order, the caller-supplied
// clock `at`, the deterministic incident_id derived below).
type safetyIncidentRecord struct {
	Schema        string `json:"schema"`
	At            string `json:"at"`
	RunID         string `json:"run_id"`
	Rule          string `json:"rule"`
	OffendingPath string `json:"offending_path"`
	Detail        string `json:"detail"`
	DocPath       string `json:"doc_path,omitempty"`
	IncidentID    string `json:"incident_id"`
}

// fileIncidentSink appends schema-valid safety-incident JSONL lines (the TS
// fileIncidentSink + createIncidentLog): the deterministic `inc-<sha16>` id
// is the sha256 over the canonical record WITHOUT the id; same inputs always
// produce the same id (evidence duplication, never loss). Exactly one retry,
// then fail loud — an incident that cannot be recorded is itself an incident.
type fileIncidentSink struct {
	path string
	mu   sync.Mutex
}

func newFileIncidentSink(stateDir string) *fileIncidentSink {
	return &fileIncidentSink{path: filepath.Join(stateDir, incidentLogFilename)}
}

// IncidentRecord is the incident-log interface (the FixIncidentPort seam the
// TS IncidentSink/append satisfies).
func (s *fileIncidentSink) Record(at, runID, rule, offendingPath, detail, docPath string) error {
	record := safetyIncidentRecord{
		Schema:        "docsync.safety-incident/v1",
		At:            at,
		RunID:         runID,
		Rule:          rule,
		OffendingPath: offendingPath,
		Detail:        detail,
		DocPath:       docPath,
	}
	withoutID, err := json.Marshal(&record)
	if err != nil {
		return err
	}
	record.IncidentID = "inc-" + docutil.ContentHash(string(withoutID))[:16]
	line, err := json.Marshal(&record)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// Exactly one retry, then fail loud.
	for attempt := 0; ; attempt++ {
		file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, writeErr := file.Write(line)
			closeErr := file.Close()
			if writeErr == nil && closeErr == nil {
				return nil
			}
			err = writeErr
			if err == nil {
				err = closeErr
			}
		}
		if attempt >= 1 {
			return fmt.Errorf(
				"docsync: incident %s (rule=%s, path=%s) could not be persisted after one retry: %v",
				record.IncidentID, rule, offendingPath, err)
		}
	}
}

// RunFix runs the `fix` verb (P7-T08..T13).
func RunFix(args *ParsedInvocation, io CliIo, options FixOptions) DocsyncExitCode {
	ctx := context.Background()
	repoRoot := options.Cwd
	if repoRoot == "" {
		repoRoot = "."
	}
	repo := "local/repo"
	if args.Repo != nil {
		repo = *args.Repo
	}
	env := options.Env
	if env == nil {
		env = osEnv()
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock
	}

	/* 1. Offline refusal (OD-R16): fix is model-backed; offline is check-only. */
	if args.Offline {
		io.Stderr(fixPrefix + " --offline is not supported for fix — fix requires the model-backed " +
			"decision/writer/verifier stages (OD-R16: offline is check-only)")
		return ExitConfig
	}

	/* 2. Run id: explicit --run-id must be a canonical ULID (P7-T08). */
	runID := ""
	if args.RunID == nil {
		runID = newULID()
	} else if !isValidULID(*args.RunID) {
		io.Stderr(fixPrefix + " invalid --run-id '" + *args.RunID + "' — expected a canonical 26-char ULID " +
			"(P7-T08 provenance key; omit the flag to auto-generate)")
		return ExitConfig
	} else {
		runID = *args.RunID
	}

	/* Test injection seam: exercise the flow directly. */
	if options.FlowDeps != nil {
		result := RunFixFlow(ctx, *options.FlowDeps, FixFlowInput{
			RepoRoot:     repoRoot,
			Repo:         repo,
			RunID:        runID,
			AllowDirty:   args.AllowDirty,
			DocSelectors: args.DocPaths,
		})
		return renderFixFlowResult(result, args, io, nil)
	}

	stateDir := filepath.Join(repoRoot, schemas.DocSyncDir, "state")
	store := NewFixRunRecordStore(stateDir)
	// The safety-incident ledger rides the SAME state dir (TS fileIncidentSink).
	incidents := newFileIncidentSink(stateDir)

	/* 3. Compose — ALL config validation happens here, before any AI stage. */
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	if err != nil {
		return renderFixComposeFailure(err, io)
	}
	capture := newPatchCapture()
	runtime, err := createCliPipelineRuntime(CliRuntimeOptions{
		RepoRoot:  repoRoot,
		Repo:      repo,
		Env:       env,
		Clock:     clock,
		Transport: options.Transport,
		OnPatch: func(captured *schemas.DocPatch, _ string) {
			capture.add(captured)
		},
	})
	if err != nil {
		return renderFixComposeFailure(err, io)
	}

	/* 4. The change window. */
	base, ok := resolveBaseRef(ctx, repoRoot, args, io)
	if !ok {
		return ExitConfig
	}

	/* 5. The fix flow. */
	result := RunFixFlow(ctx, FixFlowDeps{
		Runtime:   runtime,
		Git:       git,
		Store:     store,
		Incidents: incidents,
		Io:        io,
		Clock:     clock,
		PatchSource: func(ctx context.Context, docPath string, patchRef *string) (*schemas.DocPatch, error) {
			return capture.resolve(docPath, patchRef)
		},
		Apply: applyDocPatchReal(git),
	}, FixFlowInput{
		RepoRoot:     repoRoot,
		Repo:         repo,
		Base:         base,
		Head:         "HEAD",
		RunID:        runID,
		AllowDirty:   args.AllowDirty,
		DocSelectors: args.DocPaths,
		Budgets:      options.Budgets,
	})
	return renderFixFlowResult(result, args, io, nil)
}

// renderFixFlowResult renders the flow result and maps it onto the exit code.
func renderFixFlowResult(result FixFlowResult, args *ParsedInvocation, io CliIo, _ interface{}) DocsyncExitCode {
	switch result.Kind {
	case FixResultConfigError:
		io.Stderr(fixPrefix + " " + result.Detail)
		return ExitConfig
	case FixResultRuntimeErr:
		io.Stderr(fixPrefix + " " + result.Detail)
		return ExitRuntime
	case FixResultNoop:
		if args.JSON {
			io.Stdout(jsonString(fixNoopPayload{Report: nil, Applied: []FixFlowApplied{}, Skipped: []FixFlowSkipped{}, Refused: []FixFlowRefused{}, RunID: runIDOf(args)}))
		} else {
			io.Stdout("no-op: run-id " + runIDOf(args) + " already recorded")
		}
		return ExitOK
	}

	if args.JSON {
		report, ok := revalidateFixReport(result, io)
		if !ok {
			return ExitRuntime
		}
		io.Stdout(jsonString(fixCompletedPayload{
			Report:  report,
			Applied: result.Applied,
			Skipped: result.Skipped,
			Refused: result.Refused,
			RunID:   runIDOf(args),
		}))
	} else {
		quiet := args.Quiet
		io.Stdout(orchestrator.RenderCheckText(result.Report, &orchestrator.CheckRenderOptions{
			Glyphs: glyphSetOf(args.Glyphs),
			Quiet:  &quiet,
		}))
		for _, line := range RenderFixSummary(result, args.Glyphs) {
			io.Stdout(line)
		}
	}

	if len(result.Applied) > 0 || len(result.Refused) > 0 {
		return ExitDrift
	}
	return ExitOK
}

// runIDOf returns a display run id for the noop/json line (a completed run uses
// the args' run id; absent it is a generated id not echoed in the JSON here).
func runIDOf(args *ParsedInvocation) string {
	if args.RunID != nil {
		return *args.RunID
	}
	return ""
}

// revalidateFixReport re-pins the canonical report before emit (D14): an invalid
// report fails closed to exit 3 and never emits.
func revalidateFixReport(result FixFlowResult, io CliIo) (*schemas.CanonicalReport, bool) {
	if result.Report == nil {
		io.Stderr(fixPrefix + " canonical report failed schema validation — failing closed, nothing emitted (D14): nil report")
		return nil, false
	}
	if issues := result.Report.Validate(); len(issues) > 0 {
		io.Stderr(fixPrefix + " canonical report failed schema validation — failing closed, nothing " +
			"emitted (D14): " + renderIssues(issues))
		return nil, false
	}
	return result.Report, true
}

func renderFixComposeFailure(err error, io CliIo) DocsyncExitCode {
	var cfg *CliConfigError
	if errors.As(err, &cfg) {
		io.Stderr(cfg.Error())
		for _, line := range cfg.Diagnostics {
			io.Stderr(line)
		}
		return ExitConfig
	}
	io.Stderr(fixPrefix + " " + describeErr(err))
	if isConfigClass(err) {
		return ExitConfig
	}
	return ExitRuntime
}

/* ------------------------------------------------------------- the flow core */

// RunFixFlow is the deterministic fix orchestration (fix/flow.ts): gates → apply
// → CAS record. Every LLM surface is behind the injected runtime (D14).
func RunFixFlow(ctx context.Context, deps FixFlowDeps, input FixFlowInput) FixFlowResult {
	io := deps.Io
	if io == nil {
		io = DefaultIo()
	}
	apply := deps.Apply
	if apply == nil {
		apply = func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (FixApplyOutcome, error) {
			return FixApplyOutcome{}, docutil.New(docutil.CodeInternal, "fix apply port is not bound (declared seam)")
		}
	}

	/* (a) SAME-RUN GATE — pipeline NOT invoked (A9; V-P7-13). */
	if deps.Store != nil && deps.Store.Has(input.RunID) {
		return FixFlowResult{Kind: FixResultNoop, Reason: "same-run-skip"}
	}

	/* Manifest allowlist + per-doc mode map (fail closed: invalid → config). */
	manifestResult, err := validate.LoadAndValidateManifest(input.RepoRoot, validate.ManifestValidateOptions{})
	if err != nil {
		return FixFlowResult{Kind: FixResultConfigError, Detail: fixPrefix + " " + describeErr(err)}
	}
	if manifestResult.Manifest == nil {
		return FixFlowResult{Kind: FixResultConfigError, Detail: manifestConfigDetail(manifestResult.Diagnostics)}
	}
	for _, advisory := range manifestResult.Advisories {
		io.Stderr(fixPrefix + " " + advisory.Message)
	}
	manifest := manifestResult.Manifest
	allowlist := map[string]bool{}
	modeByPath := map[string]string{}
	protectedPaths := map[string]bool{}
	for _, entry := range manifest.Documents {
		allowlist[entry.Path] = true
		mode := effectiveMode(entry, manifest.Defaults)
		modeByPath[entry.Path] = mode
		if isProtectedModeStr(mode) {
			protectedPaths[entry.Path] = true
		}
	}

	/* (b) DIRTY GATE — dirty manifest-listed docs skip unless --allow-dirty. */
	dirtyDocs := map[string]bool{}
	if deps.Git != nil {
		porcelain, _ := deps.Git.StatusPorcelain(ctx)
		for _, line := range porcelain {
			entry := ParsePorcelainLine(line)
			if entry == nil || entry.Status == "??" {
				continue
			}
			if allowlist[entry.Path] && !strings.HasPrefix(entry.Path, ".docsync/") {
				dirtyDocs[entry.Path] = true
			}
		}
	}

	/* (c) PIPELINE RUN — typed failure lanes (D8/D14). */
	outcome, err := deps.Runtime.Run(ctx, CliRuntimeRunInput{Base: input.Base, Head: input.Head, Budgets: input.Budgets})
	if err != nil {
		if isConfigClass(err) {
			return FixFlowResult{Kind: FixResultConfigError, Detail: describeErr(err)}
		}
		return FixFlowResult{Kind: FixResultRuntimeErr, Detail: describeErr(err)}
	}

	// The result slices start EMPTY (never nil) so the JSON surfaces emit
	// `[]` rather than `null` (the TS payload contract).
	var applied = []FixFlowApplied{}
	var skipped = []FixFlowSkipped{}
	var refused = []FixFlowRefused{}
	appliedThisRun := map[string]bool{}
	reDetected := false

	var selectAndApply func(doc schemas.RunDocument, fromRedetection bool)
	selectAndApply = func(doc schemas.RunDocument, fromRedetection bool) {
		docPath := doc.DocPath
		isRunApplied := func(runID, path string) bool {
			return runID == input.RunID && appliedThisRun[path]
		}

		/* WRITE-SCOPE GUARD (invariants 2/3) — before ANY other gate. */
		if !allowlist[docPath] || strings.HasPrefix(docPath, ".docsync/") {
			recordIncident(deps.Incidents, deps.Clock(), input.RunID, "out_of_allowlist_path", docPath,
				"fix flow blocked a candidate outside the manifest allowlist (never applied, invariants 2/3): "+docPath, docPath)
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "out-of-scope",
				Detail: "path is not manifest-listed documentation (blocked + incident, V-P7-10)"})
			return
		}

		/* Selector scope. */
		if len(input.DocSelectors) > 0 && !matchesSelector(docPath, input.DocSelectors) {
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "out-of-scope",
				Detail: "excluded by --doc selectors"})
			return
		}

		/* Dirty gate — per-doc. */
		if dirtyDocs[docPath] && !input.AllowDirty {
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "dirty",
				Detail: "working tree has uncommitted changes to this doc (pass --allow-dirty to override)"})
			return
		}

		/* Protected-mode gate — blocked + incident, never applied (D2 floor). */
		mode := modeByPath[docPath]
		if !isWritableModeStr(mode) || isProtectedModeStr(mode) {
			recordIncident(deps.Incidents, deps.Clock(), input.RunID, "protected_path_patch", docPath,
				"fix flow blocked a patch candidate in non-writable mode '"+mode+"' (locked resolves at ANY confidence, D2)", docPath)
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "locked-or-protected",
				Detail: "manifest mode '" + mode + "' is not in WRITABLE_MODES (" + strings.Join(workspace.WritableModes, "|") + ")"})
			return
		}

		/* Outcome gate (RATIFIED): only auto|suggest are fix candidates. */
		if doc.PolicyOutcome != schemas.PipelineOutcomeAuto && doc.PolicyOutcome != schemas.PipelineOutcomeSuggest {
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "not-a-candidate",
				Detail: "policy_outcome '" + string(doc.PolicyOutcome) + "' is not auto|suggest (review/blocked/ignore are never applied)"})
			return
		}

		/* Verification gate (V-P7-15, fail closed). */
		verification := doc.Verification
		if verification == nil || verification.Verdict != schemas.VerdictPass ||
			!verification.AutoEligible || len(verification.AutoBlockReasons) > 0 {
			detail := "no verification block — never applied (fail closed)"
			if verification != nil {
				detail = "verdict=" + string(verification.Verdict) + " auto_eligible=" + boolYesNo(verification.AutoEligible) +
					" blocks=[" + strings.Join(verification.AutoBlockReasons, ", ") + "]"
			}
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "unverified", Detail: detail})
			return
		}

		/* Apply stage (e). */
		var patchDoc *schemas.DocPatch
		if deps.PatchSource != nil {
			patchDoc, err = deps.PatchSource(ctx, docPath, verification.PatchRef)
			if err != nil {
				refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "abstain", Detail: describeErr(err), ReDetected: fromRedetection})
				return
			}
		}
		if patchDoc == nil {
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "not-a-candidate",
				Detail: "no verified patch object resolvable for this run (patch-source port returned null)"})
			return
		}

		worktreeContent := readWorktreeFile(input.RepoRoot, docPath)
		applyOutcome, err := apply(ctx, patchDoc, worktreeContent, allowlist, protectedPaths, isRunApplied)
		if err != nil {
			refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "abstain", Detail: describeErr(err), ReDetected: fromRedetection})
			return
		}

		switch applyOutcome.Kind {
		case patchOutcomeApplied:
			writeDocFile(input.RepoRoot, docPath, applyOutcome.ResultContent)
			appliedThisRun[docPath] = true
			patchRef, refErr := verifier.ComputePatchRef(patchDoc)
			if refErr != nil {
				refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "abstain", Detail: describeErr(refErr), ReDetected: fromRedetection})
				return
			}
			applied = append(applied, FixFlowApplied{DocPath: docPath, PatchRef: patchRef, ThreeWayUsed: applyOutcome.ThreeWayUsed})
			return
		case patchOutcomeNoop:
			skipped = append(skipped, FixFlowSkipped{DocPath: docPath, Reason: "same-run-skip",
				Detail: "patch engine no-op '" + applyOutcome.NoopReason + "' — nothing to write (A9 idempotent re-run)"})
			return
		case patchOutcomeUnanchored:
			detail := strings.Join(applyOutcome.UnanchoredReasons, "; ")
			if detail == "" {
				detail = "anchors did not resolve content-hash-first"
			}
			refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "unanchored", Detail: detail, ReDetected: fromRedetection})
			return
		default: // abstain — the P7-T10 refuse-and-rerun lane for the doc-changed race.
			if applyOutcome.AbstainReason == string(patch.AbstainDocChangedRaceUnverified) && !reDetected {
				reDetected = true
				freshOutcome, rerr := deps.Runtime.Run(ctx, CliRuntimeRunInput{Base: input.Base, Head: input.Head, Budgets: input.Budgets})
				if rerr != nil {
					refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "stale-patch", Detail: describeErr(rerr), ReDetected: true})
					return
				}
				for i := range freshOutcome.Documents {
					if freshOutcome.Documents[i].DocPath == docPath {
						selectAndApply(freshOutcome.Documents[i], true)
						return
					}
				}
				refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: "stale-patch",
					Detail: "re-detection produced no candidate for this doc", ReDetected: true})
				return
			}
			reason := "abstain"
			if applyOutcome.AbstainReason == string(patch.AbstainDocChangedRaceUnverified) {
				reason = "stale-patch"
			}
			refused = append(refused, FixFlowRefused{DocPath: docPath, Reason: reason,
				Detail: applyOutcome.Detail, ReDetected: fromRedetection || reDetected})
			return
		}
	}

	for i := range outcome.Documents {
		selectAndApply(outcome.Documents[i], false)
	}

	/* (f) Fix-run record — written ONCE at completion, via CAS. */
	if deps.Store != nil {
		record := FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: input.RunID}
		for _, entry := range applied {
			record.Applied = append(record.Applied, FixRecordApplied{DocPath: entry.DocPath, PatchRef: entry.PatchRef, ThreeWayUsed: entry.ThreeWayUsed})
			record.PatchRefs = append(record.PatchRefs, entry.PatchRef)
		}
		if err := deps.Store.Write(record); err != nil {
			return FixFlowResult{Kind: FixResultRuntimeErr, Detail: describeErr(err)}
		}
	}

	/* (g) Completed — the canonical report is the parity display artifact. */
	report, err := orchestrator.BuildCanonicalReport(outcome)
	if err != nil {
		return FixFlowResult{Kind: FixResultRuntimeErr, Detail: "canonical report failed validation: " + describeErr(err)}
	}
	return FixFlowResult{Kind: FixResultCompleted, Report: report, Applied: applied, Skipped: skipped, Refused: refused}
}

/* ---------------------------------------------------------------- helpers */

// apply outcome kind literals (patch.ApplyOutcome parity).
const (
	patchOutcomeApplied    = "applied"
	patchOutcomeNoop       = "noop"
	patchOutcomeUnanchored = "unanchored"
)

// newULID mints a canonical 26-char ULID fix-run id (P7-T08). The generator is
// entropy-backed and does not fail in practice; on the rare error an empty id
// is returned and the pipeline gates everything downstream.
func newULID() string {
	id, _ := docutil.NewULIDGenerator().Generate()
	return id
}

func isValidULID(v string) bool { return docutil.IsValidULID(v) }

// applyDocPatchReal wraps the real patch.ApplyDocPatch engine (production apply).
func applyDocPatchReal(git *gitx.GitClient) FixApplyFn {
	return func(ctx context.Context, patchDoc *schemas.DocPatch, worktreeContent *string, allowlist, protected map[string]bool, isRunApplied func(string, string) bool) (FixApplyOutcome, error) {
		applyOutcome, err := patch.ApplyDocPatch(ctx, patch.ApplyDocPatchInput{
			Patch:             patchDoc,
			Git:               git,
			Rev:               "HEAD",
			WorktreeContent:   worktreeContent,
			ManifestAllowlist: allowlist,
			ProtectedPaths:    protected,
			Now:               wallClock,
			IsRunApplied: func(runID, path string) bool {
				if isRunApplied == nil {
					return false
				}
				return isRunApplied(runID, path)
			},
		})
		if err != nil {
			return FixApplyOutcome{}, err
		}
		mapped := FixApplyOutcome{
			Kind:          string(applyOutcome.Kind),
			ResultContent: applyOutcome.ResultContent,
			ThreeWayUsed:  applyOutcome.ThreeWayUsed,
			NoopReason:    string(applyOutcome.NoopReason),
			AbstainReason: string(applyOutcome.AbstainReason),
			Detail:        applyOutcome.Detail,
		}
		for _, hunk := range applyOutcome.UnanchoredHunks {
			mapped.UnanchoredReasons = append(mapped.UnanchoredReasons, string(hunk.Reason))
		}
		return mapped, nil
	}
}

// newPatchCapture builds the in-memory current-run patch capture (null until the
// runtime's onPatch hook is rebound; the flow's patch-source returns null).
func newPatchCapture() *patchCapture { return &patchCapture{} }

type patchCapture struct{ patches []schemas.DocPatch }

func (c *patchCapture) add(p *schemas.DocPatch) {
	if p != nil {
		c.patches = append(c.patches, *p)
	}
}

func (c *patchCapture) resolve(docPath string, patchRef *string) (*schemas.DocPatch, error) {
	var forDoc []*schemas.DocPatch
	for i := range c.patches {
		if c.patches[i].DocPath == docPath {
			forDoc = append(forDoc, &c.patches[i])
		}
	}
	if len(forDoc) == 0 {
		return nil, nil
	}
	if patchRef == nil {
		if len(forDoc) == 1 {
			return forDoc[0], nil
		}
		return nil, nil
	}
	for _, p := range forDoc {
		if ref, err := verifier.ComputePatchRef(p); err == nil && ref == *patchRef {
			return p, nil
		}
	}
	return nil, nil
}

// effectiveMode is the canonical mode resolution: entry mode → defaults mode →
// 'suggest'.
func effectiveMode(entry schemas.ManifestEntry, defaults *schemas.ManifestDefaults) string {
	if entry.Mode != nil {
		return string(*entry.Mode)
	}
	if defaults != nil && defaults.Mode != nil {
		return string(*defaults.Mode)
	}
	return string(schemas.DocModeSuggest)
}

func isWritableModeStr(mode string) bool {
	for _, m := range workspace.WritableModes {
		if m == mode {
			return true
		}
	}
	return false
}

func isProtectedModeStr(mode string) bool {
	for _, m := range workspace.ProtectedModes {
		if m == mode {
			return true
		}
	}
	return false
}

func matchesSelector(docPath string, selectors []string) bool {
	for _, selector := range selectors {
		if docPath == selector || strings.HasPrefix(docPath, selector+"/") {
			return true
		}
	}
	return false
}

func recordIncident(incidents FixIncidentPort, at, runID, rule, offendingPath, detail, docPath string) {
	if incidents == nil {
		return
	}
	_ = incidents.Record(at, runID, rule, offendingPath, detail, docPath)
}

// manifestConfigDetail renders a manifest diagnostic list to one line detail.
func manifestConfigDetail(diagnostics []validate.Diagnostic) string {
	if len(diagnostics) == 0 {
		return fixPrefix + " manifest.yml not found — no manifest, no run (D7/D8)"
	}
	parts := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		parts = append(parts, d.Code+": "+d.Message)
	}
	return fixPrefix + " " + strings.Join(parts, "; ")
}

func readWorktreeFile(repoRoot, docPath string) *string {
	data, err := os.ReadFile(filepath.Join(repoRoot, docPath))
	if err != nil {
		return nil
	}
	content := string(data)
	return &content
}

func writeDocFile(repoRoot, docPath, content string) {
	target := filepath.Join(repoRoot, docPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(target, []byte(content), 0o644)
}

// JSON payload shapes (fix --json surface).
type fixNoopPayload struct {
	Report  any              `json:"report"`
	Applied []FixFlowApplied `json:"applied"`
	Skipped []FixFlowSkipped `json:"skipped"`
	Refused []FixFlowRefused `json:"refused"`
	RunID   string           `json:"run_id"`
}

type fixCompletedPayload struct {
	Report  *schemas.CanonicalReport `json:"report"`
	Applied []FixFlowApplied         `json:"applied"`
	Skipped []FixFlowSkipped         `json:"skipped"`
	Refused []FixFlowRefused         `json:"refused"`
	RunID   string                   `json:"run_id"`
}
