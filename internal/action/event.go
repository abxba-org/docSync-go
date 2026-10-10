// Ported from packages/action/src/event.ts (P8-T16): turns the raw GitHub
// Actions event payload plus the GITHUB_* environment into a typed
// ActionContext. PURE — no I/O, no AI, no clocks.
//
// Fail-closed (ARCH §0.3 D8/D14 posture): a malformed payload or an event we
// cannot interpret returns an *ActionEventError; the runner maps it to the
// configuration-error lane (exit 2) — it is NEVER reported as "no drift".
//
// Fork detection (A7, docs/spikes/A7-github-token-forks.md): a pull_request
// event is a fork PR when pull_request.head.repo.full_name differs from
// repository.full_name. When either full name is missing or empty the
// relationship is INDETERMINATE and we resolve isFork=true — indeterminate
// degrades to LESS automation (report-only), never to more.

package action

import (
	"encoding/json"
	"fmt"
)

// ActionPullRequestContext is the context of a pull_request event run.
type ActionPullRequestContext struct {
	Number     int
	HeadSha    string
	HeadBranch string
	BaseBranch string
	// IsFork is true when the PR head lives in a fork repo (A7 — forces
	// report-only).
	IsFork bool
}

// ActionPushContext is the context of a push event run.
type ActionPushContext struct {
	Branch string
	After  string
}

// ActionContext is the typed event context consumed by the runner (P8-T16).
type ActionContext struct {
	// EventName is one of "pull_request" | "push" | "other".
	EventName string
	// Repo is the base repository full name (owner/name) when determinable
	// from the payload; nil when the payload omits a usable full_name.
	Repo *string
	PR   *ActionPullRequestContext
	Push *ActionPushContext
}

// ActionEventError is the typed parse failure — the fail-closed lane (exit 2 at
// the runner). The TS class carried a stable `code`; the Go port carries only
// the message text, which tests and users observe verbatim.
type ActionEventError struct {
	Message string
}

// Error implements error (the observable TS message text).
func (e *ActionEventError) Error() string { return e.Message }

// isPlainObject mirrors the TS isPlainObject guard (object, non-nil, non-array).
func isPlainObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// asStringOrEmpty mirrors firstString: a string value, or "" for anything else.
func asStringOrEmpty(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// asStringField returns the string at key when it is a string (the receiver's
// asString seam), else "".
func asStringField(object map[string]any, key string) string {
	return asStringOrEmpty(object[key])
}

// nestedObject returns object[key] when it is a plain object, else nil (the
// receiver's isPlainObject ? ... : undefined navigation).
func nestedObject(object map[string]any, key string) map[string]any {
	if field, ok := object[key]; ok {
		if nested, ok := field.(map[string]any); ok {
			return nested
		}
	}
	return nil
}

// jsonInteger mirrors `typeof v === 'number' && Number.isInteger(v)`: true for
// a JSON number (float64) or a native Go integer that is a whole number. Both
// shapes are accepted because a pre-parsed object payload carries Go ints
// while a JSON-string payload decodes numbers to float64 — JavaScript treats
// them identically.
func jsonInteger(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// resolveIsFork is the A7 fork predicate: different full names ⇒ fork;
// indeterminate ⇒ fork (fail closed to report-only).
func resolveIsFork(payload map[string]any) bool {
	repository := nestedObject(payload, "repository")
	pr := nestedObject(payload, "pull_request")
	head := nestedObject(pr, "head")
	headRepo := nestedObject(head, "repo")
	headFullName := asStringField(headRepo, "full_name")
	baseFullName := asStringField(repository, "full_name")
	if len(headFullName) == 0 || len(baseFullName) == 0 {
		return true
	}
	return headFullName != baseFullName
}

// stripRefsHeads returns the base name from a refs/heads/<name> ref (the input
// as-is otherwise).
func stripRefsHeads(ref string) string {
	const prefix = "refs/heads/"
	if len(ref) >= len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):]
	}
	return ref
}

// parseActionPayload normalizes the raw payload into an object map. A JSON
// string is parsed (non-object JSON is a typed failure); an already-parsed
// object passes through. Mirrors the TS `string | object` union.
func parseActionPayload(rawPayload any) (map[string]any, error) {
	switch payload := rawPayload.(type) {
	case string:
		var parsed any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			return nil, &ActionEventError{Message: fmt.Sprintf(
				"event payload is not valid JSON (fail-closed, D8): %s", err.Error())}
		}
		object, ok := parsed.(map[string]any)
		if !ok {
			return nil, &ActionEventError{Message: "event payload is not a JSON object (fail-closed, D8): aborting before any check stage"}
		}
		return object, nil
	case map[string]any:
		return payload, nil
	case nil:
		return nil, &ActionEventError{Message: "event payload must be a JSON string or object (fail-closed, D8)"}
	default:
		return nil, &ActionEventError{Message: "event payload must be a JSON string or object (fail-closed, D8)"}
	}
}

// ParseActionEvent parses the raw event payload (a JSON string or a pre-parsed
// object) plus the GITHUB_* environment into a typed ActionContext. Returns an
// *ActionEventError on malformed payloads / environment (fail-closed, D8).
func ParseActionEvent(rawPayload any, env *RunnerEnv) (*ActionContext, error) {
	payload, err := parseActionPayload(rawPayload)
	if err != nil {
		return nil, err
	}

	eventName := env.GithubEventName
	if len(eventName) == 0 {
		return nil, &ActionEventError{Message: "GITHUB_EVENT_NAME is missing (fail-closed, D8): the action cannot interpret the trigger"}
	}

	repoFullName := asStringField(nestedObject(payload, "repository"), "full_name")
	var repo *string
	if len(repoFullName) > 0 {
		repo = &repoFullName
	}

	if eventName == "pull_request" {
		pr := nestedObject(payload, "pull_request")
		if pr == nil {
			return nil, &ActionEventError{Message: "GITHUB_EVENT_NAME is 'pull_request' but the payload carries no pull_request object (fail-closed, D8)"}
		}
		number, ok := jsonInteger(pr["number"])
		if !ok || number <= 0 {
			return nil, &ActionEventError{Message: "pull_request payload is malformed: missing/invalid number (fail-closed, D8)"}
		}
		head := nestedObject(pr, "head")
		headSha := asStringField(head, "sha")
		if len(headSha) == 0 {
			return nil, &ActionEventError{Message: "pull_request payload is malformed: missing head sha (fail-closed, D8)"}
		}
		var headBranch string
		if len(env.GithubHeadRef) > 0 {
			headBranch = env.GithubHeadRef
		} else {
			headBranch = asStringField(head, "ref")
		}
		base := nestedObject(pr, "base")
		var baseBranch string
		if len(env.GithubBaseRef) > 0 {
			baseBranch = env.GithubBaseRef
		} else {
			baseBranch = asStringField(base, "ref")
		}
		if len(headBranch) == 0 || len(baseBranch) == 0 {
			return nil, &ActionEventError{Message: "pull_request payload is malformed: cannot determine head/base branch (fail-closed, D8)"}
		}
		return &ActionContext{
			EventName: "pull_request",
			Repo:      repo,
			PR: &ActionPullRequestContext{
				Number:     number,
				HeadSha:    headSha,
				HeadBranch: headBranch,
				BaseBranch: baseBranch,
				IsFork:     resolveIsFork(payload),
			},
		}, nil
	}

	if eventName == "push" {
		payloadRef := asStringOrEmpty(payload["ref"])
		var branch string
		if len(payloadRef) > 0 {
			branch = stripRefsHeads(payloadRef)
		} else {
			branch = stripRefsHeads(env.GithubRef)
		}
		var after string
		if payloadAfter := asStringOrEmpty(payload["after"]); len(payloadAfter) > 0 {
			after = payloadAfter
		} else {
			after = env.GithubSha
		}
		if len(branch) == 0 || len(after) == 0 {
			return nil, &ActionEventError{Message: "push payload is malformed: cannot determine branch/after commit (fail-closed, D8)"}
		}
		return &ActionContext{
			EventName: "push",
			Repo:      repo,
			Push:      &ActionPushContext{Branch: branch, After: after},
		}, nil
	}

	return &ActionContext{EventName: "other", Repo: repo}, nil
}
