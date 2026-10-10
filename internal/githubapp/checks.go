// Ported from packages/github-app/src/checks/ (P8-T09..T13) — the aggregated
// check-run surface.
//
//   - upsert-service.ts → UpsertService: idempotent find-or-create + update-by-id
//     over the injected check-run REST client (V-P8-3).
//   - pipeline-check-handler.ts → PullRequestCheckHandler: the surface mapping
//     (config-error → failure, runtime-inconclusive → neutral explicit, success →
//     resolved conclusion + escalation action) and the early-signal lifecycle
//     (V-P8-21: only the FINAL upsert completes with a conclusion).
//   - escalation-handler.ts → GeneratePatchHandler: run-once per idempotency key
//     (V-P8-9/V-P8-10; auto-proposal default OFF — models propose; humans
//     promote, D10).
//   - fallback-guidance.ts → mechanical check text (A6 no-go, OD-R18).
//
// The REST client, the check pipeline runtime, and the escalation runtime are
// injected seams (network-free tests); the composed runtimes are internal/cli
// (TODO(githubapp/cli)) — this package declares the seams and does not import
// internal/cli.

package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ============================================================ upsert service */

// CheckRunUpsertStatus is the lifecycle status of an upsert input (TS
// CheckRunUpsertStatus).
type CheckRunUpsertStatus string

// The lifecycle statuses.
const (
	UpsertQueued     CheckRunUpsertStatus = "queued"
	UpsertInProgress CheckRunUpsertStatus = "in_progress"
	UpsertCompleted  CheckRunUpsertStatus = "completed"
)

// CheckRunUpsertInput is the input of UpsertService.UpsertCheckRun.
type CheckRunUpsertInput struct {
	Repo              string
	HeadSha           string
	Payload           orchestrator.CheckSummaryPayload
	Conclusion        *orchestrator.CheckRunConclusion // nil for queued/in-progress
	Status            CheckRunUpsertStatus
	EscalationEnabled bool
	Partial           bool
}

// CheckRunUpsertResult is one upsert's result.
type CheckRunUpsertResult struct {
	CheckRunID int
	Created    bool
}

// CheckRunUpsertErrorCode is the closed error code set.
type CheckRunUpsertErrorCode string

// The closed code set (TS CheckRunUpsertErrorCode).
const (
	UpsertErrorDuplicate                   CheckRunUpsertErrorCode = "duplicate"
	UpsertErrorCompletedRequiresConclusion CheckRunUpsertErrorCode = "completed-requires-conclusion"
)

// CheckRunUpsertError is the typed upsert-contract error (V-P8-3 duplicate
// race; V-P8-21 REST contract).
type CheckRunUpsertError struct {
	*githubAppError
	Code   CheckRunUpsertErrorCode
	Detail string
}

// CheckRunServiceDeps is everything injected.
type CheckRunServiceDeps struct {
	Client gitpr.CheckRunClient
	Clock  func() string
	Logger func(line string) // optional
}

// UpsertService is the idempotent check-run upsert service (P8-T09).
type UpsertService interface {
	UpsertCheckRun(input CheckRunUpsertInput) (CheckRunUpsertResult, error)
}

func toGitPrConclusion(conclusion *orchestrator.CheckRunConclusion) *gitpr.CheckRunConclusion {
	if conclusion == nil {
		return nil
	}
	value := gitpr.CheckRunConclusion(*conclusion)
	return &value
}

func toGitPrOutput(payload orchestrator.CheckSummaryPayload) *gitpr.CheckRunOutputPayload {
	return &gitpr.CheckRunOutputPayload{Title: payload.Title, Summary: payload.Summary, Text: payload.Text}
}

func checkRunStatusPtr(s CheckRunUpsertStatus) *gitpr.CheckRunStatus {
	value := gitpr.CheckRunStatus(s)
	return &value
}

// isDuplicateCreateError maps the gitpr typed 422 exponent onto the TS
// `status === 422` duplicate-create check.
func isDuplicateCreateError(err error) bool {
	if err == nil {
		return false
	}
	var validationErr *gitpr.GitHubValidationError
	return errors.As(err, &validationErr)
}

type checkRunService struct {
	client gitpr.CheckRunClient
	clock  func() string
	logger func(line string)
}

func (s *checkRunService) logLine(line string) {
	if s.logger != nil {
		s.logger("[docsync:check-run] " + s.clock() + " " + line)
	}
}

func (s *checkRunService) logf(format string, args ...any) {
	s.logLine(fmt.Sprintf(format, args...))
}

// findExistingRun finds the existing docsync/check run for a head (preferring a
// live in_progress/queued run, else the latest).
func (s *checkRunService) findExistingRun(repo, headSha string) (*gitpr.GitHubCheckRun, bool) {
	runs, err := s.client.FindCheckRunsForRef(context.Background(), repo, headSha)
	if err != nil {
		// The core layer stays minimal: a failed find propagates (the retry
		// policy belongs to the caller/governor).
		return nil, false
	}
	ours := make([]gitpr.GitHubCheckRun, 0, len(runs))
	for _, run := range runs {
		if run.Name == nil || *run.Name == gitpr.DOCSYNCCheckName {
			ours = append(ours, run)
		}
	}
	if len(ours) == 0 {
		return nil, false
	}
	for i := range ours {
		if ours[i].Status != nil && (*ours[i].Status == "in_progress" || *ours[i].Status == "queued") {
			return &ours[i], true
		}
	}
	return &ours[len(ours)-1], true
}

func (s *checkRunService) updateExisting(repo string, run gitpr.GitHubCheckRun, input CheckRunUpsertInput) (CheckRunUpsertResult, error) {
	s.logf("found %s id=%d for head=%s — updating by id (status=%s conclusion=%s partial=%t)",
		gitpr.DOCSYNCCheckName, run.ID, input.HeadSha, input.Status, conclusionText(input.Conclusion), input.Partial)
	updated, err := s.client.UpdateCheckRun(context.Background(), repo, run.ID, gitpr.UpdateCheckRunInput{
		Status:            checkRunStatusPtr(input.Status),
		Conclusion:        toGitPrConclusion(input.Conclusion),
		Output:            toGitPrOutput(input.Payload),
		EscalationEnabled: input.EscalationEnabled,
	})
	if err != nil {
		return CheckRunUpsertResult{}, err
	}
	s.logf("updated id=%d (status=%s conclusion=%s)", updated.ID, input.Status, conclusionText(input.Conclusion))
	return CheckRunUpsertResult{CheckRunID: updated.ID, Created: false}, nil
}

// UpsertCheckRun performs one idempotent find-or-create + update-by-id upsert.
func (s *checkRunService) UpsertCheckRun(input CheckRunUpsertInput) (CheckRunUpsertResult, error) {
	// Fail-closed contract (V-P8-21): an upsert with completed status and no
	// conclusion is rejected BEFORE any REST call.
	if input.Status == UpsertCompleted && input.Conclusion == nil {
		return CheckRunUpsertResult{}, &CheckRunUpsertError{
			githubAppError: newGithubAppError(
				"check-run upsert rejected for head=" + input.HeadSha + ": status 'completed' requires a conclusion"),
			Code: UpsertErrorCompletedRequiresConclusion,
			Detail: "GitHub's check-run API requires `conclusion` whenever `status` is 'completed' (422 otherwise). " +
				"Only the final completion upserts (config-error / inconclusive / resolved-conclusion) may set " +
				"status 'completed' — early signals stay in_progress.",
		}
	}
	existing, found := s.findExistingRun(input.Repo, input.HeadSha)
	if found {
		return s.updateExisting(input.Repo, *existing, input)
	}
	s.logf("no %s for head=%s — creating (status=%s partial=%t)",
		gitpr.DOCSYNCCheckName, input.HeadSha, input.Status, input.Partial)
	created, err := s.client.CreateCheckRun(context.Background(), input.Repo, gitpr.CreateCheckRunInput{
		HeadSha:           input.HeadSha,
		Status:            gitpr.CheckRunStatus(input.Status),
		Conclusion:        toGitPrConclusion(input.Conclusion),
		Output:            toGitPrOutput(input.Payload),
		EscalationEnabled: input.EscalationEnabled,
	})
	if err != nil {
		if !isDuplicateCreateError(err) {
			return CheckRunUpsertResult{}, err
		}
		// Duplicate-create race: re-find ONCE; update by id if now visible.
		s.logLine("duplicate-create 422 — re-finding the run created by a concurrent delivery")
		raced, racedFound := s.findExistingRun(input.Repo, input.HeadSha)
		if racedFound {
			return s.updateExisting(input.Repo, *raced, input)
		}
		return CheckRunUpsertResult{}, &CheckRunUpsertError{
			githubAppError: newGithubAppError(
				"check-run upsert: duplicate-create race for head=" + input.HeadSha + " and no run visible after re-find"),
			Code: UpsertErrorDuplicate,
			Detail: "GitHub rejected the create with 422 (duplicate name for the same head) and the re-find found no " +
				"existing docsync/check run; the caller may retry the upsert.",
		}
	}
	s.logf("created id=%d for head=%s", created.ID, input.HeadSha)
	return CheckRunUpsertResult{CheckRunID: created.ID, Created: true}, nil
}

// CreateCheckRunService creates the upsert service. No retries of its own
// beyond the single duplicate-race re-find.
func CreateCheckRunService(deps CheckRunServiceDeps) UpsertService {
	return &checkRunService{client: deps.Client, clock: deps.Clock, logger: deps.Logger}
}

func conclusionText(c *orchestrator.CheckRunConclusion) string {
	if c == nil {
		return "none"
	}
	return string(*c)
}

/* ======================================================= pipeline check handler */

// EarlySignalPort hands the pipeline the in-progress early-signal surface
// (post / update — both stay in_progress; V-P8-21).
type EarlySignalPort interface {
	Post(payload orchestrator.CheckSummaryPayload) error
	Update(payload orchestrator.CheckSummaryPayload) error
}

// RunCheckPortResultKind is the tag of the pipeline result union.
type RunCheckPortResultKind string

// The closed union arms.
const (
	RunCheckPortResultSuccess             RunCheckPortResultKind = "success"
	RunCheckPortResultConfigError         RunCheckPortResultKind = "config-error"
	RunCheckPortResultRuntimeInconclusive RunCheckPortResultKind = "runtime-inconclusive"
)

// RunCheckPortResult is the composed-pipeline result (TS RunCheckPortResult).
type RunCheckPortResult struct {
	Kind        RunCheckPortResultKind
	Report      *schemas.CanonicalReport
	Diagnostics []orchestrator.ConfigErrorDiagnostic
	Reason      string
}

// RunCheckPortInput is the input of the pipeline seam.
type RunCheckPortInput struct {
	Repo        string
	Base        string
	Head        string
	PR          RunCheckPortPR
	EarlySignal EarlySignalPort
}

// RunCheckPortPR is the PR identity for the pipeline seam.
type RunCheckPortPR struct {
	Number int
}

// RunCheckPort is the composed-pipeline seam (wired by the composition unit).
type RunCheckPort func(input RunCheckPortInput) (RunCheckPortResult, error)

// PullRequestCheckHandlerDeps is everything injected.
type PullRequestCheckHandlerDeps struct {
	Service          UpsertService
	RunCheck         RunCheckPort
	Strict           bool
	EnforcementOptIn bool
	Logger           func(line string)
}

// PullRequestCheckInput is the handler invocation input.
type PullRequestCheckInput struct {
	Repo string
	Base string
	Head string
	PR   RunCheckPortPR
}

// PullRequestCheckResult is the handler outcome.
type PullRequestCheckResult struct {
	Kind       string // "completed" | "config-error" | "runtime-inconclusive"
	Conclusion *orchestrator.CheckRunConclusion
	CheckRunID *int
}

// signalKey is the signal-dedupe key: status + conclusion + payload (R7).
func signalKey(status CheckRunUpsertStatus, payload orchestrator.CheckSummaryPayload) string {
	key := struct {
		Status     CheckRunUpsertStatus             `json:"status"`
		Conclusion *orchestrator.CheckRunConclusion `json:"conclusion"`
		Payload    orchestrator.CheckSummaryPayload `json:"payload"`
	}{Status: status, Conclusion: nil, Payload: payload}
	raw, _ := json.Marshal(key)
	return docutil.ContentHash(string(raw))
}

type earlySignalImpl struct {
	upsert func(status CheckRunUpsertStatus, payload orchestrator.CheckSummaryPayload) error
}

func (e *earlySignalImpl) Post(payload orchestrator.CheckSummaryPayload) error {
	return e.upsert(UpsertInProgress, payload)
}
func (e *earlySignalImpl) Update(payload orchestrator.CheckSummaryPayload) error {
	return e.upsert(UpsertInProgress, payload)
}

type pullRequestCheckHandler struct {
	deps PullRequestCheckHandlerDeps
	log  func(line string)
}

// PullRequestCheckHandler is the exported handler seam (TS createPullRequestCheckHandler).
type PullRequestCheckHandler interface {
	HandlePullRequest(input PullRequestCheckInput) (PullRequestCheckResult, error)
}

func (h *pullRequestCheckHandler) logf(format string, args ...any) {
	if h.log != nil {
		h.log(fmt.Sprintf(format, args...))
	}
}

func (h *pullRequestCheckHandler) logLine(line string) {
	if h.log != nil {
		h.log(line)
	}
}

// HandlePullRequest constructs the early-signal port, invokes the port exactly
// once per PR head, and upserts the final check per the D8/D10/D14 surface map.
func (h *pullRequestCheckHandler) HandlePullRequest(input PullRequestCheckInput) (PullRequestCheckResult, error) {
	var lastSignal string
	upsertSignal := func(status CheckRunUpsertStatus, payload orchestrator.CheckSummaryPayload) error {
		key := signalKey(status, payload)
		if key == lastSignal {
			return nil
		}
		lastSignal = key
		_, err := h.deps.Service.UpsertCheckRun(CheckRunUpsertInput{
			Repo: input.Repo, HeadSha: input.Head, Payload: payload,
			Conclusion: nil, Status: status, Partial: true,
		})
		if err != nil {
			return err
		}
		h.logf("[docsync:check-handler] early signal upserted (status=%s)", status)
		return nil
	}
	earlySignal := &earlySignalImpl{upsert: upsertSignal}

	result, err := h.deps.RunCheck(RunCheckPortInput{
		Repo: input.Repo, Base: input.Base, Head: input.Head,
		PR: input.PR, EarlySignal: earlySignal,
	})
	if err != nil {
		return PullRequestCheckResult{}, err
	}

	switch result.Kind {
	case RunCheckPortResultConfigError:
		payload := orchestrator.BuildConfigErrorSummary(result.Diagnostics)
		up, upsertErr := h.deps.Service.UpsertCheckRun(CheckRunUpsertInput{
			Repo: input.Repo, HeadSha: input.Head, Payload: payload,
			Conclusion: conclusionPtr(orchestrator.ConclusionFailure), Status: UpsertCompleted,
		})
		if upsertErr != nil {
			return PullRequestCheckResult{}, upsertErr
		}
		h.logLine("[docsync:check-handler] config-error surface upserted (failure, D8)")
		return PullRequestCheckResult{
			Kind: "config-error", Conclusion: conclusionPtr(orchestrator.ConclusionFailure), CheckRunID: intPtr(up.CheckRunID),
		}, nil

	case RunCheckPortResultRuntimeInconclusive:
		inconclusive := orchestrator.BuildInconclusiveSummary(result.Reason)
		payload := orchestrator.CheckSummaryPayload{Title: inconclusive.Title, Summary: inconclusive.Summary, Text: inconclusive.Summary}
		up, upsertErr := h.deps.Service.UpsertCheckRun(CheckRunUpsertInput{
			Repo: input.Repo, HeadSha: input.Head, Payload: payload,
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: UpsertCompleted,
		})
		if upsertErr != nil {
			return PullRequestCheckResult{}, upsertErr
		}
		h.logLine("[docsync:check-handler] runtime-inconclusive surface upserted (neutral, D14)")
		return PullRequestCheckResult{
			Kind: "runtime-inconclusive", Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), CheckRunID: intPtr(up.CheckRunID),
		}, nil
	}

	resolved := orchestrator.ResolveCheckConclusion(result.Report, &orchestrator.ResolveCheckConclusionOptions{
		Strict: h.deps.Strict, EnforcementOptIn: h.deps.EnforcementOptIn,
	})
	payload, buildErr := orchestrator.BuildCheckPayload(result.Report, nil)
	if buildErr != nil {
		return PullRequestCheckResult{}, buildErr
	}
	up, upsertErr := h.deps.Service.UpsertCheckRun(CheckRunUpsertInput{
		Repo: input.Repo, HeadSha: input.Head, Payload: payload,
		Conclusion: &resolved.Conclusion, Status: UpsertCompleted,
		EscalationEnabled: resolved.Conclusion == orchestrator.ConclusionFailure,
	})
	if upsertErr != nil {
		return PullRequestCheckResult{}, upsertErr
	}
	h.logf("[docsync:check-handler] final check upserted (conclusion=%s; %s)", resolved.Conclusion, resolved.Reason)
	return PullRequestCheckResult{
		Kind: "completed", Conclusion: &resolved.Conclusion, CheckRunID: intPtr(up.CheckRunID),
	}, nil
}

// CreatePullRequestCheckHandler creates the PR check handler.
func CreatePullRequestCheckHandler(deps PullRequestCheckHandlerDeps) PullRequestCheckHandler {
	return &pullRequestCheckHandler{deps: deps, log: deps.Logger}
}

/* ============================================================ escalation handler */

const duplicateEscalationCode = "duplicate-escalation"

// DuplicateEscalationError is raised when an idempotency key is reused (V-P8-9).
type DuplicateEscalationError struct {
	*githubAppError
	IdempotencyKey string
}

// RunPatchPortInput is the input of the escalation seam.
type RunPatchPortInput struct {
	Repo           string
	PR             RunPatchPortPRNumber
	IdempotencyKey string
}

// RunPatchPortPRNumber is the PR identity for the escalation seam.
type RunPatchPortPRNumber struct {
	Number  int
	HeadSha string
}

// RunPatchPortResultKind is the tag of the patch-generation outcome.
type RunPatchPortResultKind string

// The closed patch-generation outcomes.
const (
	PatchShipped    RunPatchPortResultKind = "shipped"
	PatchDowngraded RunPatchPortResultKind = "downgraded"
	PatchAborted    RunPatchPortResultKind = "aborted"
)

// RunPatchPortResult is one patch-generation result.
type RunPatchPortResult struct {
	Kind   RunPatchPortResultKind
	Detail string
}

// RunPatchPort is the composed escalation seam (writer → verify → doc PR).
type RunPatchPort func(input RunPatchPortInput) (RunPatchPortResult, error)

// GeneratePatchRequest is one escalation decision request.
type GeneratePatchRequest struct {
	Repo             string
	PR               RunPatchPortPRNumber
	ActionIdentifier *string // nil when absent (TS string | null)
	IdempotencyKey   string
}

// GeneratePatchResultKind is the tag of the escalation outcome.
type GeneratePatchResultKind string

// The closed escalation outcomes.
const (
	EscalationShipped    GeneratePatchResultKind = "shipped"
	EscalationDowngraded GeneratePatchResultKind = "downgraded"
	EscalationAborted    GeneratePatchResultKind = "aborted"
	EscalationIgnored    GeneratePatchResultKind = "ignored"
)

// GeneratePatchResult is one escalation outcome (TS GeneratePatchResult).
type GeneratePatchResult struct {
	Kind   GeneratePatchResultKind
	Detail string
	Reason string
}

// GeneratePatchHandlerDeps is everything injected.
type GeneratePatchHandlerDeps struct {
	RunPatch RunPatchPort
	// Service is a reserved seam for reflecting escalation state onto the
	// check run; unused here.
	Service      UpsertService
	Clock        func() string
	Logger       func(line string)
	AutoProposal *AutoProposal
}

// AutoProposal is the P8-T13 flag — DEFAULT OFF (V-P8-10).
type AutoProposal struct {
	Enabled bool
}

// GeneratePatchHandler is the escalation handler (P8-T12).
type GeneratePatchHandler interface {
	HandleRequestedAction(request GeneratePatchRequest) (GeneratePatchResult, error)
	RunAutoProposal(request GeneratePatchRequest) (GeneratePatchResult, error)
}

type generatePatchHandler struct {
	deps        GeneratePatchHandlerDeps
	mu          sync.Mutex
	executed    map[string]bool
	autoEnabled bool
}

func (h *generatePatchHandler) logf(format string, args ...any) {
	if h.deps.Logger != nil {
		h.deps.Logger("[docsync:escalation] " + h.deps.Clock() + " " + fmt.Sprintf(format, args...))
	}
}

func (h *generatePatchHandler) escalate(request GeneratePatchRequest) (GeneratePatchResult, error) {
	h.mu.Lock()
	if h.executed[request.IdempotencyKey] {
		h.mu.Unlock()
		h.logf("duplicate escalation rejected (key=%s)", request.IdempotencyKey)
		return GeneratePatchResult{}, &DuplicateEscalationError{
			githubAppError: newGithubAppError(
				"escalation already executed for idempotency key " + request.IdempotencyKey),
			IdempotencyKey: request.IdempotencyKey,
		}
	}
	// Record BEFORE awaiting the port: concurrent repeats hit the registry too.
	h.executed[request.IdempotencyKey] = true
	h.mu.Unlock()
	h.logf("escalation accepted (key=%s pr=#%d)", request.IdempotencyKey, request.PR.Number)
	patch, err := h.deps.RunPatch(RunPatchPortInput{
		Repo: request.Repo, PR: request.PR, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return GeneratePatchResult{}, err
	}
	return GeneratePatchResult{Kind: GeneratePatchResultKind(patch.Kind), Detail: patch.Detail}, nil
}

func (h *generatePatchHandler) HandleRequestedAction(request GeneratePatchRequest) (GeneratePatchResult, error) {
	identifier := gitpr.DOCSYNCGeneratePatchAction.Identifier
	if request.ActionIdentifier == nil || *request.ActionIdentifier != identifier {
		shown := "absent"
		if request.ActionIdentifier != nil {
			shown = *request.ActionIdentifier
		}
		h.logf("unknown requested_action identifier %s — no-op", shown)
		return GeneratePatchResult{
			Kind:   EscalationIgnored,
			Reason: "unknown requested_action identifier: " + shown,
		}, nil
	}
	return h.escalate(request)
}

func (h *generatePatchHandler) RunAutoProposal(request GeneratePatchRequest) (GeneratePatchResult, error) {
	if !h.autoEnabled {
		h.logf("auto-proposal skipped (default off, P8-T13/V-P8-10) — explicit click required")
		return GeneratePatchResult{
			Kind: EscalationIgnored,
			Reason: "auto-proposal disabled (P8-T13 default off): patch generation requires the explicit " +
				"requested_action click",
		}, nil
	}
	return h.escalate(request)
}

// CreateGeneratePatchHandler creates the escalation handler. The run-once
// registry lives for the handler's lifetime.
func CreateGeneratePatchHandler(deps GeneratePatchHandlerDeps) GeneratePatchHandler {
	autoEnabled := deps.AutoProposal != nil && deps.AutoProposal.Enabled
	return &generatePatchHandler{deps: deps, executed: map[string]bool{}, autoEnabled: autoEnabled}
}

/* ============================================================ fallback guidance */

// FallbackGuidanceLine is the deterministic fallback guidance line (A6 no-go,
// OD-R18).
const FallbackGuidanceLine = "run 'docsync fix' locally to apply verified patches"

// RenderFallbackGuidance renders the mechanical fallback guidance block for the
// check-run text (no conversational phrasing, no model output).
func RenderFallbackGuidance(patchReady bool) string {
	lines := []string{
		"docsync escalation fallback (A6 no-go, OD-R18):",
		"- " + FallbackGuidanceLine,
	}
	if patchReady {
		lines = append(lines,
			"- A verified docsync patch is ready for this PR; the command above applies it locally for review in a doc PR.")
	}
	lines = append(lines,
		"- Patch generation requires an explicit human action; docsync never patches documentation without opt-in (D10).")
	return strings.Join(lines, "\n")
}
