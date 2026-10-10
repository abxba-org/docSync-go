// Ported from packages/github-app/src/webhook/ (P8-T05/T06) — the fail-closed
// webhook admission core.
//
// verify (verify.ts): verifyWebhookSignature — HMAC SHA-256 over the raw body
// under the webhook secret, constant-time compared against the
// X-Hub-Signature-256 header. A missing/malformed header never throws — it is
// simply not a valid signature.
//
// receiver (receiver.ts): createWebhookReceiver — the composition unit builds
// CEV (control input) from the raw request (event name, delivery id, signature
// header), rejects invalid signature/event surface/payload before any handler,
// self-trigger dedup suppresses docsync's own output, and dispatches ONLY to
// registered handlers (status 4 contract: fail closed to less automation,
// never more).
//
// Fail-closed statuses (D7/D8/D16): invalid signature → 401-class reject +
// no handler; unexpected event → 200 README ACK + no handler; missing handler
// → 202 SKIP; a payload we cannot safely normalize → 200 reject + no handler.

package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/gitpr"
)

// recvUint8Max bounds the raw webhook body at 1 MiB (TS uint8Max guard).
const recvUint8Max = 1024 * 1024

// signatureHeaderPrefix is the only accepted HMAC-SHA-256 header prefix (TS
// "sha256=").
const signatureHeaderPrefix = "sha256="

// hexStringPattern validates a decoded hex signature (TS /^[0-9a-f]+$/i).
var hexStringPattern = regexp.MustCompile(`^[0-9a-f]+$`)

func isHexString(value string) bool {
	return len(value) > 0 && (len(value)%2 == 0) && hexStringPattern.MatchString(value)
}

// VerifyWebhookSignature checks the GitHub X-Hub-Signature-256 header against
// the HMAC SHA-256 of the raw body (TS verifyWebhookSignature). Never throws —
// a missing/malformed/non-hex/short header is simply not a valid signature.
func VerifyWebhookSignature(rawBody string, signatureHeader string, secret string) bool {
	body := rawBody
	if len(body) > recvUint8Max {
		body = body[:recvUint8Max]
	}
	if signatureHeader == "" {
		return false
	}
	header := strings.TrimSpace(signatureHeader)
	if !strings.HasPrefix(header, signatureHeaderPrefix) {
		return false
	}
	providedHex := strings.TrimSpace(strings.TrimPrefix(header, signatureHeaderPrefix))
	if !isHexString(providedHex) {
		return false
	}
	provided, err := hex.DecodeString(providedHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	expected := mac.Sum(nil)
	if len(provided) != len(expected) {
		return false
	}
	return hmac.Equal(provided, expected)
}

// WebhookLogger is the structured logging seam (stderr / structured sink).
type WebhookLogger struct {
	Info func(message string)
	Warn func(message string)
}

func (l *WebhookLogger) info(message string) {
	if l != nil && l.Info != nil {
		l.Info(message)
	}
}

func (l *WebhookLogger) warn(message string) {
	if l != nil && l.Warn != nil {
		l.Warn(message)
	}
}

// WebhookResultKind is the receiver result-union tag.
type WebhookResultKind string

// The closed union arms (TS WebhookResult kinds).
const (
	WebhookResultHandled        WebhookResultKind = "handled"
	WebhookResultSkipped        WebhookResultKind = "skipped"
	WebhookResultRejected       WebhookResultKind = "rejected"
	WebhookResultSuppressed     WebhookResultKind = "suppressed"
	WebhookResultSignatureError WebhookResultKind = "signature-error"
)

// WebhookResult is the outcome of one delivery (the TS discriminated union,
// flattened with a Kind tag). Event/Action/Reason carry the tag-specific
// payload; HTTPStatus is the delivery-class HTTP status.
type WebhookResult struct {
	Kind       WebhookResultKind
	Event      string
	Action     *string
	Reason     string
	HTTPStatus int
}

// WebhookPR is the normalized pull_request dispatch context.
type WebhookPR struct {
	Number     int
	HeadSha    string
	HeadBranch string
	Action     string
	// BaseBranch is nil when the payload omitted pull_request.base.ref.
	BaseBranch *string
}

// WebhookPush is the normalized push dispatch context.
type WebhookPush struct {
	Branch string
	After  string
}

// WebhookCheckRunPR is the PR association of a check_run delivery.
type WebhookCheckRunPR struct {
	Number  int
	HeadSha string
}

// WebhookCheckRun is the normalized check_run dispatch context.
type WebhookCheckRun struct {
	ID                        int64
	PR                        *WebhookCheckRunPR
	RequestedActionIdentifier *string
}

// WebhookHandlerContext is the normalized delivery context handed to handlers.
type WebhookHandlerContext struct {
	InstallationID any
	Actor          *string
	Repo           *string
	DeliveryID     *string
	PR             *WebhookPR
	Push           *WebhookPush
	CheckRun       *WebhookCheckRun
	// Credentials is sealed on the context; derived from the injected
	// credentialsFor (defaults to {kind:none}).
	Credentials gitpr.PrCredentials
}

// WebhookEventHandler is one routed handler (pull_request / check_run / push).
type WebhookEventHandler func(ctx WebhookHandlerContext) error

// WebhookHandlers is the registered handler set (a nil field = not registered
// → the receiver emits the 202 SKIP result).
type WebhookHandlers struct {
	PullRequest WebhookEventHandler
	CheckRun    WebhookEventHandler
	Push        WebhookEventHandler
}

// WebhookHeaders is the request metadata (the TS WebhookHeaders: the `?` members
// are empty strings here).
type WebhookHeaders struct {
	Event     string
	Delivery  string
	Signature string
}

// CredentialsInput is the input passed to the credentials resolver.
type CredentialsInput struct {
	InstallationID any
	Repo           *string
}

// CredentialsForFunc resolves the sealed credentials for one delivery
// (TODO(docsync-action/cli): the JWT → installation-token bridge binds here; a
// nil return degrades to {kind:none}).
type CredentialsForFunc func(input CredentialsInput) *gitpr.PrCredentials

// WebhookReceiverOptions is everything injected.
type WebhookReceiverOptions struct {
	Secret         string
	AppIdentity    string
	Handlers       WebhookHandlers
	Logger         *WebhookLogger
	CredentialsFor CredentialsForFunc
}

// WebhookReceiver is the created receiver.
type WebhookReceiver struct {
	options WebhookReceiverOptions
}

// NewWebhookReceiver creates the webhook receiver (TS createWebhookReceiver).
func NewWebhookReceiver(options WebhookReceiverOptions) *WebhookReceiver {
	return &WebhookReceiver{options: options}
}

func (r *WebhookReceiver) handleHeaderEvent(headers WebhookHeaders) WebhookResultKind {
	switch headers.Event {
	case "pull_request", "check_run", "push":
		return WebhookResultHandled
	default:
		return WebhookResultRejected
	}
}

// HandleWebhook processes one delivery: TEV → CEV (verify → parse → route →
// dispatch). Returns the delivery result and any handler error (handlers that
// throw propagate; the app.go handlers deliberately never throw).
func (r *WebhookReceiver) HandleWebhook(rawBody string, headers WebhookHeaders) (WebhookResult, error) {
	logger := r.options.Logger

	// Control input (TEV → CEV): the normalized signature + event + body.
	if !VerifyWebhookSignature(rawBody, headers.Signature, r.options.Secret) {
		return WebhookResult{
			Kind:       WebhookResultSignatureError,
			HTTPStatus: 401,
			Reason:     "invalid signature (401-class, D7)",
		}, nil
	}

	parsedBody, err := parseWebhookBody(rawBody)
	if err != nil {
		logger.warn("malformed payload (200 reject, fail closed): " + err.Error())
		return WebhookResult{
			Kind:       WebhookResultRejected,
			Event:      headers.Event,
			HTTPStatus: 200,
			Reason:     "malformed payload: " + err.Error(),
		}, nil
	}

	action := asStringPayload(parsedBody["action"])
	actor := asStringPayloadNested(parsedBody, "sender", "login")
	repo := asStringPayloadNested(parsedBody, "repository", "full_name")
	deliveryID := stringOrNil(headers.Delivery)
	var logEvent string
	if action != nil {
		logEvent = headers.Event + " action=" + *action
	} else {
		logEvent = headers.Event
	}

	// Unexpected event surface (rejected before parse; README ACK 200).
	if r.handleHeaderEvent(headers) != WebhookResultHandled {
		reason := "event=" + headers.Event + " ignored (not in webhook surface)"
		logger.info("rejected " + reason + " (README ACK)")
		return WebhookResult{
			Kind:       WebhookResultRejected,
			Event:      headers.Event,
			Action:     action,
			HTTPStatus: 200,
			Reason:     reason,
		}, nil
	}

	// Status 4 contract: fail closed to LESS automation. Self-trigger
	// suppression is a security posture (never a config toggle): an app's own
	// PR is dropped even if no handler is registered.
	var branch *string
	switch headers.Event {
	case "pull_request":
		branch = asStringPayloadNested(parsedBody, "pull_request", "head", "ref")
	case "push":
		b := branchFromRef(asStringPayload(parsedBody["ref"]))
		if b != nil {
			branch = b
		}
	}
	if isSelfTrigger(actor, branch, r.options.AppIdentity) {
		logger.info("rejected event " + logEvent + " (self-trigger, invariant 11)")
		return WebhookResult{
			Kind:       WebhookResultSuppressed,
			Event:      headers.Event,
			HTTPStatus: 202,
		}, nil
	}

	// Install/verify: without an installation the delivery exists outside our
	// install surface (fail closed).
	if !installationPresent(parsedBody) {
		logger.warn("delivery with no installation (reject, fail closed): event=" + logEvent)
		return WebhookResult{
			Kind:       WebhookResultRejected,
			Event:      headers.Event,
			Action:     action,
			HTTPStatus: 200,
			Reason:     "no installation (fail closed)",
		}, nil
	}

	installationID := installationIDOf(parsedBody)

	// Route.
	switch headers.Event {
	case "pull_request":
		return r.routePullRequest(parsedBody, actor, repo, deliveryID, installationID, logger)
	case "check_run":
		return r.routeCheckRun(parsedBody, actor, repo, deliveryID, installationID, logger)
	case "push":
		return r.routePush(parsedBody, actor, repo, deliveryID, installationID, logger)
	default:
		// Unreachable — the header step rejected unexpected events.
		logger.info("rejected event " + logEvent + " (not in webhook surface, README ACK)")
		return WebhookResult{
			Kind:       WebhookResultRejected,
			Event:      headers.Event,
			Action:     action,
			HTTPStatus: 200,
			Reason:     "event=" + headers.Event + " ignored (not in webhook surface)",
		}, nil
	}
}

func (r *WebhookReceiver) credentials(installationID any, repo *string) gitpr.PrCredentials {
	if r.options.CredentialsFor != nil {
		if resolved := r.options.CredentialsFor(CredentialsInput{InstallationID: installationID, Repo: repo}); resolved != nil {
			return *resolved
		}
	}
	return gitpr.PrCredentials{Kind: gitpr.PrCredentialsNone}
}

func (r *WebhookReceiver) routePullRequest(
	parsedBody map[string]any, actor, repo, deliveryID *string, installationID any, logger *WebhookLogger,
) (WebhookResult, error) {
	event := "pull_request"
	action := asStringPayload(parsedBody["action"])
	actionName := ""
	if action != nil {
		actionName = *action
	}
	logEvent := event + " action=" + actionName

	if actionName != "opened" && actionName != "synchronize" {
		logger.info("rejected event " + logEvent + " ignored (not in webhook surface)")
		return WebhookResult{Kind: WebhookResultRejected, Event: event, Action: action, HTTPStatus: 200,
			Reason: "action=" + actionName + " ignored (not in webhook surface)"}, nil
	}
	if r.options.Handlers.PullRequest == nil {
		logger.info("skipped event " + logEvent + ": no handler registered")
		return WebhookResult{Kind: WebhookResultSkipped, Event: event, Action: action, HTTPStatus: 202,
			Reason: "no handler registered"}, nil
	}
	pr, ok := parsePullRequestContext(parsedBody)
	if !ok {
		logger.warn("rejected malformed pull_request payload (200 reject): missing pr fields")
		return WebhookResult{Kind: WebhookResultRejected, Event: event, Action: action, HTTPStatus: 200,
			Reason: "malformed payload"}, nil
	}
	ctx := WebhookHandlerContext{
		InstallationID: installationID, Actor: actor, Repo: repo, DeliveryID: deliveryID,
		PR:          pr,
		Credentials: r.credentials(installationID, repo),
	}
	if err := r.options.Handlers.PullRequest(ctx); err != nil {
		return WebhookResult{}, err
	}
	return WebhookResult{Kind: WebhookResultHandled, Event: event, Action: action, HTTPStatus: 200,
		Reason: "instance 5 -- action handled"}, nil
}

func (r *WebhookReceiver) routeCheckRun(
	parsedBody map[string]any, actor, repo, deliveryID *string, installationID any, logger *WebhookLogger,
) (WebhookResult, error) {
	event := "check_run"
	action := asStringPayload(parsedBody["action"])
	actionName := ""
	if action != nil {
		actionName = *action
	}
	logEvent := event + " action=" + actionName

	if actionName != "requested_action" {
		logger.info("rejected event " + logEvent + " ignored (not in webhook surface)")
		return WebhookResult{Kind: WebhookResultRejected, Event: event, Action: action, HTTPStatus: 200,
			Reason: "action=" + actionName + " ignored (not in webhook surface)"}, nil
	}
	if r.options.Handlers.CheckRun == nil {
		logger.info("skipped event " + logEvent + ": no handler registered")
		return WebhookResult{Kind: WebhookResultSkipped, Event: event, Action: action, HTTPStatus: 202,
			Reason: "no handler registered"}, nil
	}
	checkRun, ok := parseCheckRunContext(parsedBody)
	if !ok {
		logger.warn("rejected malformed payload (200 reject)")
		return WebhookResult{Kind: WebhookResultRejected, Event: event, Action: action, HTTPStatus: 200,
			Reason: "malformed payload"}, nil
	}
	ctx := WebhookHandlerContext{
		InstallationID: installationID, Actor: actor, Repo: repo, DeliveryID: deliveryID,
		CheckRun:    checkRun,
		Credentials: r.credentials(installationID, repo),
	}
	if err := r.options.Handlers.CheckRun(ctx); err != nil {
		return WebhookResult{}, err
	}
	return WebhookResult{Kind: WebhookResultHandled, Event: event, Action: action, HTTPStatus: 200,
		Reason: "instance 7 -- action handled"}, nil
}

func (r *WebhookReceiver) routePush(
	parsedBody map[string]any, actor, repo, deliveryID *string, installationID any, logger *WebhookLogger,
) (WebhookResult, error) {
	event := "push"
	action := asStringPayload(parsedBody["action"])
	actionName := ""
	if action != nil {
		actionName = *action
	}
	logEvent := event
	if actionName != "" {
		logEvent = event + " action=" + actionName
	}

	if r.options.Handlers.Push == nil {
		logger.info("skipped event " + logEvent + ": no handler registered")
		return WebhookResult{Kind: WebhookResultSkipped, Event: event, Action: action, HTTPStatus: 202,
			Reason: "no handler registered"}, nil
	}
	push := parsePushContext(parsedBody)
	if push == nil {
		logger.warn("rejected malformed push payload (200 reject): missing push fields")
		return WebhookResult{Kind: WebhookResultRejected, Event: event, Action: action, HTTPStatus: 200,
			Reason: "malformed payload"}, nil
	}
	ctx := WebhookHandlerContext{
		InstallationID: installationID, Actor: actor, Repo: repo, DeliveryID: deliveryID,
		Push:        push,
		Credentials: r.credentials(installationID, repo),
	}
	if err := r.options.Handlers.Push(ctx); err != nil {
		return WebhookResult{}, err
	}
	return WebhookResult{Kind: WebhookResultHandled, Event: event, Action: action, HTTPStatus: 200,
		Reason: "instance 9 -- action handled"}, nil
}

/* --------------------------------------------------------------- helpers */

// isSelfTrigger (V-P8-17): the receiver owns the general dup rule; the core
// P6-T19 classifier (branch prefix + author-bot identity) supplies the pure
// signal. Suppressed when EITHER fires (branch prefix OR author identity).
func isSelfTrigger(actor *string, branch *string, appIdentity string) bool {
	core := gitpr.EvaluateSelfTrigger(gitpr.SelfTriggerCheckInput{
		Branch:         branch,
		AuthorIdentity: actor,
		BotIdentity:    appIdentity,
		DefaultBranch:  "",
	})
	if core.Suppressed {
		return true
	}
	if actor != nil && *actor == appIdentity {
		return true
	}
	if branch != nil && strings.HasPrefix(*branch, gitpr.SelfTriggerBranchPrefix) {
		return true
	}
	return false
}

func installationPresent(payload map[string]any) bool {
	value, ok := payload["installation"]
	if !ok || value == nil {
		return false
	}
	_, isObject := value.(map[string]any)
	return isObject
}

func installationIDOf(payload map[string]any) any {
	installation := nestedObjectValue(payload, "installation")
	if installation == nil {
		return nil
	}
	switch v := installation["id"].(type) {
	case float64:
		if v == float64(int64(v)) {
			return int64(v)
		}
		return v
	case string:
		if v == "" {
			return nil
		}
		return v
	default:
		return nil
	}
}

func branchFromRef(ref *string) *string {
	if ref == nil || strings.TrimSpace(*ref) == "" {
		return nil
	}
	const prefix = "refs/heads/"
	trimmed := strings.TrimSpace(*ref)
	if !strings.HasPrefix(trimmed, prefix) {
		return nil
	}
	branch := trimmed[len(prefix):]
	if branch == "" {
		return nil
	}
	return &branch
}

func parsePullRequestContext(payload map[string]any) (*WebhookPR, bool) {
	pr := nestedObjectValue(payload, "pull_request")
	if pr == nil {
		return nil, false
	}
	number, ok := jsonWhole(pr["number"])
	if !ok {
		return nil, false
	}
	headSha := asStringPayloadNested(pr, "head", "sha")
	if headSha == nil {
		return nil, false
	}
	headBranch := asStringPayloadNested(pr, "head", "ref")
	if headBranch == nil {
		return nil, false
	}
	baseBranch := asStringPayloadNested(pr, "base", "ref")
	action := asStringPayload(payload["action"])
	actionName := ""
	if action != nil {
		actionName = *action
	}
	return &WebhookPR{
		Number:     int(number),
		HeadSha:    *headSha,
		HeadBranch: *headBranch,
		Action:     actionName,
		BaseBranch: baseBranch,
	}, true
}

func parsePushContext(payload map[string]any) *WebhookPush {
	after := asStringPayload(payload["after"])
	branch := branchFromRef(asStringPayload(payload["ref"]))
	if after == nil || branch == nil {
		return nil
	}
	return &WebhookPush{Branch: *branch, After: *after}
}

func parseCheckRunContext(payload map[string]any) (*WebhookCheckRun, bool) {
	checkRun := nestedObjectValue(payload, "check_run")
	if checkRun == nil {
		return nil, false
	}
	var id int64
	switch v := checkRun["id"].(type) {
	case float64:
		id = int64(v)
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			id = 0
		} else {
			id = parsed
		}
	}
	var pr *WebhookCheckRunPR
	if pullRequests, ok := checkRun["pull_requests"].([]any); ok && len(pullRequests) > 0 {
		if first, ok := pullRequests[0].(map[string]any); ok {
			number, numberOK := jsonWhole(first["number"])
			headSha := asStringPayloadNested(first, "head", "sha")
			if numberOK && headSha != nil {
				pr = &WebhookCheckRunPR{Number: int(number), HeadSha: *headSha}
			}
		}
	}
	requestedActionID := asStringPayloadNested(payload, "requested_action", "identifier")
	return &WebhookCheckRun{ID: id, PR: pr, RequestedActionIdentifier: requestedActionID}, true
}

// ------------------------------------------------------------ body + navigation

// parseWebhookBody decodes the raw JSON webhook body (TS JSON.parse).
func parseWebhookBody(raw string) (map[string]any, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, newGithubAppError("event payload is not a JSON object (aborting)")
	}
	return parsed, nil
}

// asStringPayload returns the string at key when it is a non-empty string, else
// nil (TS asString / firstString — null for anything else).
func asStringPayload(value any) *string {
	if text, ok := value.(string); ok && len(text) > 0 {
		return &text
	}
	return nil
}

// nestedObjectValue returns object[key] when it is a plain object, else nil.
func nestedObjectValue(obj map[string]any, key string) map[string]any {
	if value, ok := obj[key]; ok {
		if nested, ok := value.(map[string]any); ok {
			return nested
		}
	}
	return nil
}

// asStringPayloadNested walks the key path and returns the final string value
// (nil when any hop is absent / not an object / not a non-empty string).
func asStringPayloadNested(obj map[string]any, path ...string) *string {
	current := obj
	for i, key := range path {
		if i == len(path)-1 {
			return asStringPayload(current[key])
		}
		current = nestedObjectValue(current, key)
		if current == nil {
			return nil
		}
	}
	return nil
}

// jsonWhole returns the integer value of a JSON number that is a whole number
// (TS Number.isInteger); ok=false otherwise.
func jsonWhole(value any) (int64, bool) {
	switch n := value.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

// stringOrNil maps "" → nil (the TS header `?` absence), else the value.
func stringOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
