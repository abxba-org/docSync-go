// Ported from packages/github-app/src/app-webhooks.ts + index.ts wiring
// (P8-T04/T09/T11/T14 composition): the App webhook handler — a
// handleWebhook-compatible receiver (signature verify → suppress → route) with
// the composed check runtime on pull_request and the escalation handler on
// check_run. The default-off push handler rides as a passthrough seam.
//
// PORT (seam): the composed check runtime and the escalation runtime are
// internal/cli composition (TODO(githubapp/cli)); this unit accepts them as
// injected seams (AppCheckRuntime / escalation.runPatch) so the wiring, routing,
// and escalation idempotency are testable network-free with doubles (OD-2).
package githubapp

import "github.com/DUDE616/docSync-go/internal/gitpr"

// AppPackageVersion mirrors the TS GITHUB_APP_PACKAGE_VERSION barrel constant.
const AppPackageVersion = "0.0.0"

// AppPRInfo is the PR identity forwarded to the check runtime.
type AppPRInfo struct {
	Number     int
	HeadSha    string
	HeadBranch string
	BaseBranch string
}

// AppCheckInput is the input of the check runtime seam.
type AppCheckInput struct {
	PR AppPRInfo
}

// AppCheckResultKind is the tag of the check-runtime result.
type AppCheckResultKind string

// AppCheckResultPosted is the only result kind the wiring consumes.
const AppCheckResultPosted AppCheckResultKind = "posted"

// AppCheckResult is the check-runtime result (TS {kind:'posted', checkRunId?,
// detail?}).
type AppCheckResult struct {
	Kind       AppCheckResultKind
	CheckRunID *int
	Detail     string
}

// AppCheckRuntime is the composed check-runtime seam (wired to internal/cli;
// TODO(githubapp/cli)): runs the aggregated check run for one PR head.
type AppCheckRuntime interface {
	HandlePullRequest(input AppCheckInput) (AppCheckResult, error)
}

// AppEscalation holds the escalation wiring (composed escalation seam +
// auto-proposal flag — DEFAULT OFF).
type AppEscalation struct {
	RunPatch     RunPatchPort
	AutoProposal *AutoProposal
}

// AppWebhookHandlerDeps is everything injected.
type AppWebhookHandlerDeps struct {
	AppCheckRuntime AppCheckRuntime
	AppIdentity     string
	Secret          string
	Logger          *WebhookLogger
	Escalation      *AppEscalation
	// PushHandler is a custom push handler passthrough (takes precedence over any
	// push config; the push-config composition path is a documented seam).
	PushHandler WebhookEventHandler
	// Clock forwards to the escalation handler logs; nil ⇒ the deterministic
	// epoch clock (fail-safe when no clock is injected).
	Clock func() string
}

// AppWebhookHandler is the created App webhook handler.
type AppWebhookHandler struct {
	receiver *WebhookReceiver
}

// HandleWebhook dispatches one delivery through the wired receiver.
func (h *AppWebhookHandler) HandleWebhook(rawBody string, headers WebhookHeaders) (WebhookResult, error) {
	return h.receiver.HandleWebhook(rawBody, headers)
}

// CreateAppWebhookHandler creates the App webhook handler (TS
// createAppWebhookHandler).
func CreateAppWebhookHandler(deps AppWebhookHandlerDeps) *AppWebhookHandler {
	logger := deps.Logger
	if logger == nil {
		logger = &WebhookLogger{Info: func(string) {}, Warn: func(string) {}}
	}
	clock := deps.Clock
	if clock == nil {
		clock = epochClock
	}

	// The escalation handler is built iff a runPatch seam is provided (else the
	// check_run surface stays an UNWIRED seam: logged skip, no patch path).
	var generatePatchHandler GeneratePatchHandler
	if deps.Escalation != nil && deps.Escalation.RunPatch != nil {
		generatePatchHandler = CreateGeneratePatchHandler(GeneratePatchHandlerDeps{
			RunPatch:     deps.Escalation.RunPatch,
			Clock:        clock,
			Logger:       func(line string) { logger.Info(line) },
			AutoProposal: deps.Escalation.AutoProposal,
		})
	}

	handlers := WebhookHandlers{
		PullRequest: func(ctx WebhookHandlerContext) error {
			// The receiver guarantees a PR for dispatched pull_request
			// deliveries; a missing repo identity is a defensive no-op (fail
			// closed).
			if ctx.PR == nil || ctx.Repo == nil {
				logger.Info("pull-request: delivery carries no PR/repo identity — no-op (fail closed)")
				return nil
			}
			baseBranch := ""
			if ctx.PR.BaseBranch != nil {
				baseBranch = *ctx.PR.BaseBranch
			}
			_, err := deps.AppCheckRuntime.HandlePullRequest(AppCheckInput{
				PR: AppPRInfo{
					Number:     ctx.PR.Number,
					HeadSha:    ctx.PR.HeadSha,
					HeadBranch: ctx.PR.HeadBranch,
					BaseBranch: baseBranch,
				},
			})
			return err
		},
		CheckRun: func(ctx WebhookHandlerContext) error {
			// P8-T12/T14: the composed escalation seam. Unknown identifier →
			// the handler's typed no-op; a missing PR association → logged no-op.
			if generatePatchHandler == nil {
				logger.Info("escalation: composed handler not configured")
				return nil
			}
			var pr *WebhookCheckRunPR
			if ctx.CheckRun != nil {
				pr = ctx.CheckRun.PR
			}
			if ctx.Repo == nil || pr == nil {
				logger.Warn("escalation: check_run delivery carries no repository/PR association — no-op")
				return nil
			}
			redact := func(text string) string { return gitpr.RedactGitHubToken(text, ctx.Credentials) }
			// The delivery id is the idempotency key: GitHub redeliveries of the
			// SAME delivery keep the id, so a redelivery hits the run-once
			// registry (V-P8-9) instead of producing a second doc PR.
			deliveryKey := "no-delivery-id"
			if ctx.DeliveryID != nil {
				deliveryKey = *ctx.DeliveryID
			}
			idempotencyKey := "escalation-" + deliveryKey
			var actionIdentifier *string
			if ctx.CheckRun != nil {
				actionIdentifier = ctx.CheckRun.RequestedActionIdentifier
			}
			result, err := generatePatchHandler.HandleRequestedAction(GeneratePatchRequest{
				Repo:             *ctx.Repo,
				PR:               RunPatchPortPRNumber{Number: pr.Number, HeadSha: pr.HeadSha},
				ActionIdentifier: actionIdentifier,
				IdempotencyKey:   idempotencyKey,
			})
			if err != nil {
				var duplicate *DuplicateEscalationError
				if errorsAsDuplicate(err, &duplicate) {
					logger.Warn("escalation duplicate rejected (key=" + duplicate.IdempotencyKey + ")")
					return nil
				}
				logger.Warn("escalation failed (fail closed): " + redact(err.Error()))
				return nil
			}
			if result.Kind == EscalationIgnored {
				logger.Info("escalation ignored: " + result.Reason)
				return nil
			}
			logger.Info("escalation " + string(result.Kind) + ": " + redact(result.Detail))
			return nil
		},
		Push: deps.PushHandler,
	}

	receiver := NewWebhookReceiver(WebhookReceiverOptions{
		Secret:      deps.Secret,
		AppIdentity: deps.AppIdentity,
		Logger:      logger,
		Handlers:    handlers,
	})
	return &AppWebhookHandler{receiver: receiver}
}

func epochClock() string { return "1970-01-01T00:00:00.000Z" }

func errorsAsDuplicate(err error, target **DuplicateEscalationError) bool {
	for err != nil {
		if dup, ok := err.(*DuplicateEscalationError); ok {
			*target = dup
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
