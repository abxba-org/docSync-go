package gitpr

// Ported from packages/core/src/gitpr/pr-platform.ts (P6-T11..T13; PLAN §7.3
// "Rate, burst, idempotency, lifecycle"; ARCH §12.2).
//
// The Platform Seam (D1-D5) + the PR write path's marker-keyed idempotent upsert:
//
//   - PrRef — the platform's PR object, structurally typed. id and labels are
//     deliberately ABSENT: everything this flow writes is addressed by NUMBER
//     (the stable machine-readable identity every platform in the seam carries),
//     so an adapter is a projection, never a mapping.
//   - PrPlatformPort — the SEVEN-METHOD surface every planned platform (GitHub
//     REST, and later GitLab/Bitbucket/Azure DevOps) implements. The flow never
//     learns the platform's identity; github-rest-platform.go and the in-memory
//     test double are the two implementations in this package.
//   - PrCredentials — the INV-8 sealed credential union (app-installation |
//     developer | none). Validation is explicit (assertCredentialsShape).
//   - createDocPrClient — the ONE write ceremony: one doc PR per source PR
//     (rule 4) realized as the marker-keyed idempotent upsert — the SAME
//     idempotency key updates the existing PR, a missing PR is created behind
//     one bounded API-410 retry (withGoneRetry, client.go). No dedupe on the
//     platform's side, no flood: the marker IS the dedupe.
//
// This file also ports the TS test double (InMemoryPrPlatform) so the Go tests
// share ONE implementation of the port with the production platform — keyed by
// repo + PR number, fully mutable, with an injected clock (no wall clock, P19).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

/* ------------------------------------------------------------------ the ref */

// PrState is the closed PR state union this seam carries.
type PrState string

// The two PR states.
const (
	PrStateOpen   PrState = "open"
	PrStateClosed PrState = "closed"
)

// PrRef is a platform PR, structurally typed (TS PrRef): id/branch/base/title/
// body/state/updated_at. There is no `id` and no `labels` — everything this
// flow writes is addressed by number.
type PrRef struct {
	Number    int     `json:"number"`
	Branch    string  `json:"branch"`
	Base      string  `json:"base"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	State     PrState `json:"state"`
	UpdatedAt string  `json:"updated_at"`
}

/* ----------------------------------------------------------------- the port */

// PrLookupState is the closed lookup-state union of PrMarkerLookupOptions.
type PrLookupState string

// The two lookup states.
const (
	PrLookupAny  PrLookupState = "any"
	PrLookupOpen PrLookupState = "open"
)

// PrMarkerLookupOptions is the marker-lookup options (TS PrMarkerLookupOptions).
type PrMarkerLookupOptions struct {
	// State restricts the scan: `open` only, or `any` (the nil options value).
	State PrLookupState
}

// CreatePrInput is the create-PR payload.
type CreatePrInput struct {
	Repo   string
	Branch string
	Base   string
	Title  string
	Body   string
}

// UpdatePrInput is the update payload (TS `{ title?, body? }`): a nil field
// means "leave alone".
type UpdatePrInput struct {
	Title *string
	Body  *string
}

// PrPlatformPort is the Platform Seam (D1-D5; P6-T11): the seven-method surface
// every planned platform implements — the flow never learns the platform's
// identity, and adapters are projections onto PrRef (addressed by number).
type PrPlatformPort interface {
	// ListOpenDocsPrs lists the open docsync PRs (branch prefixed `docsync/`),
	// used for the open-PR cap governor.
	ListOpenDocsPrs(ctx context.Context, repo string) ([]PrRef, error)
	// FindByPrBodyMarker finds the marker-keyed PR (the idempotency seam).
	FindByPrBodyMarker(ctx context.Context, repo, marker string, opts *PrMarkerLookupOptions) (PrRef, bool, error)
	// CreatePr opens a PR.
	CreatePr(ctx context.Context, input CreatePrInput) (PrRef, error)
	// UpdatePr updates an existing PR.
	UpdatePr(ctx context.Context, pr PrRef, input UpdatePrInput) (PrRef, error)
	// MarkSuperseded is the lifecycle transition (closing the stale PR; the
	// marker comment is the audit trail).
	MarkSuperseded(ctx context.Context, pr, newerPr PrRef) error
	// ClosePr closes a PR with a comment.
	ClosePr(ctx context.Context, pr PrRef, comment string) error
	// AppendPrComment appends a marker/incident comment.
	AppendPrComment(ctx context.Context, pr PrRef, body string) error
}

/* ------------------------------------------------------------- credentials */

// PrCredentialsKind is the sealed credential-union kind (TS PrCredentials['kind']).
type PrCredentialsKind string

// The closed union: app-installation | developer | none.
const (
	PrCredentialsAppInstallation PrCredentialsKind = "app-installation"
	PrCredentialsDeveloper       PrCredentialsKind = "developer"
	PrCredentialsNone            PrCredentialsKind = "none"
)

// CodePrCredentials preserves the TS EDOCSYNC_PR_CREDENTIALS code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it is carried
// on the Go error type instead of inventing a new docutil code).
const CodePrCredentials = "EDOCSYNC_PR_CREDENTIALS"

// PrCredentialsError is the fail-closed credential-shape refusal (INV-8: fail
// fast, before any git write or platform call). TS: PrCredentialsError.
type PrCredentialsError struct {
	// Message preserves the TS message text verbatim.
	Message string

	coded *docutil.Error
}

// Error renders the coded error in docutil house style.
func (e *PrCredentialsError) Error() string { return e.coded.Error() }

// Code reports the machine-readable code (EDOCSYNC_PR_CREDENTIALS).
func (e *PrCredentialsError) Code() string { return CodePrCredentials }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *PrCredentialsError) Unwrap() error { return e.coded }

// newPrCredentialsError builds the credential refusal.
func newPrCredentialsError(message string) *PrCredentialsError {
	return &PrCredentialsError{Message: message, coded: docutil.New(CodePrCredentials, message)}
}

// PrCredentials is the sealed credential union (TS PrCredentials). Kind is the
// discriminant; InstallationID (app-installation) and Token (app-installation |
// developer) are the kind-specific payloads. FAIL CLOSED: an unknown kind is
// refused, and the fields are validated per kind (a half credential never
// reaches the client).
type PrCredentials struct {
	Kind           PrCredentialsKind
	InstallationID string
	Token          string
}

// assertCredentialsShape is the explicit per-kind validation (TS
// assertCredentialsShape) — fail fast, before any git write or platform call.
func assertCredentialsShape(credentials PrCredentials) error {
	ok := false
	switch credentials.Kind {
	case PrCredentialsAppInstallation:
		ok = len(credentials.InstallationID) > 0 && len(credentials.Token) > 0
	case PrCredentialsDeveloper:
		ok = len(credentials.Token) > 0
	case PrCredentialsNone:
		ok = true
	}
	if !ok {
		return newPrCredentialsError(
			"invalid credentials shape for kind '" + string(credentials.Kind) +
				"': app-installation requires installationId+token, developer requires token, " +
				"none requires neither — the flow NEVER proceeds with half credentials (INV-8)")
	}
	return nil
}

/* ------------------------------------------------------------------ markers */

// PrIdempotencyMarkerPrefix / PrIdempotencyMarkerSuffix are the exact marker
// delimiters (TS PR_IDEMPOTENCY_MARKER_PREFIX/SUFFIX).
const (
	PrIdempotencyMarkerPrefix = "<!-- docsync:idempotency:"
	PrIdempotencyMarkerSuffix = " -->"
)

// IdempotencyMarker renders the exact marker comment for a key (TS
// idempotencyMarker).
func IdempotencyMarker(key string) string {
	return PrIdempotencyMarkerPrefix + key + PrIdempotencyMarkerSuffix
}

// stampPrBodyMarkers is the single stamp point (TS stampMarkers): the EXTRA
// markers (idempotency, self-trigger, superseded-by) always ride as trailing
// comments — they NEVER split a word in prose, they never touch the summary
// (the canonical provenance stays byte-stable; D5), and the stamp is IDEMPOTENT
// (re-invoking never duplicates it).
func stampPrBodyMarkers(body, marker string, markers []string) string {
	trimmed := strings.TrimRightFunc(body, unicode.IsSpace)
	if strings.Contains(trimmed, marker) {
		// Already stamped — a re-invocation never duplicates the stamp.
		return trimmed + "\n"
	}
	extras := make([]string, 0, len(markers))
	for _, extra := range markers {
		if len(extra) == 0 {
			continue
		}
		extras = append(extras, "<!-- docsync:"+extra+" -->")
	}
	parts := make([]string, 0, len(extras)+2)
	parts = append(parts, trimmed, marker)
	parts = append(parts, extras...)
	return strings.Join(parts, "\n\n") + "\n"
}

/* ----------------------------------------------- the in-memory test double */

// inMemoryTimestamp renders the deterministic `updated_at` of a new PR —
// `new Date((n-1)*1000).toISOString()` (the TS double), so records sort and
// compare byte-identically.
func inMemoryTimestamp(prNumber int) string {
	return time.UnixMilli(int64(prNumber-1) * 1000).UTC().Format("2006-01-02T15:04:05.000Z")
}

// InMemoryPrPlatform is the port of the TS test double: an in-memory
// PrPlatformPort (mutex-protected; no network, no wall clock) whose PR records
// live in a flat per-repo list, exactly like doubleValues in the TS module.
// Tests use it directly for idempotency/cap/marker assertions, which is also
// why it is part of the PACKAGE and not the test package (it mirrors the test
// double's TS placement in pr-platform.ts).
type InMemoryPrPlatform struct {
	mu sync.Mutex
	// values is the flat PR record list (TS doubleValues), order = creation.
	values []PrRef
	// repoByPr maps a PR number back to its repo (TS repoByPr).
	repoByPr map[int]string
	// comments holds the comment thread per PR number.
	comments map[int][]string
	// clock is the INJECTED clock (default: the deterministic epoch clock).
	clock func() string
}

// InMemoryPrPlatformOptions are the double's injectable options.
type InMemoryPrPlatformOptions struct {
	// Clock stamps updated_at; nil uses the deterministic epoch clock.
	Clock func() string
}

// NewInMemoryPrPlatform builds the double over the optional injected clock.
func NewInMemoryPrPlatform(opts *InMemoryPrPlatformOptions) *InMemoryPrPlatform {
	clock := func() string { return inMemoryTimestamp(0) }
	if opts != nil && opts.Clock != nil {
		clock = opts.Clock
	}
	return &InMemoryPrPlatform{
		values:   []PrRef{},
		repoByPr: make(map[int]string),
		comments: make(map[int][]string),
		clock:    clock,
	}
}

// nextNumberLocked assigns the next PR number (mutated under mu).
func (p *InMemoryPrPlatform) nextNumberLocked() int {
	next := 0
	for _, value := range p.values {
		if value.Number > next {
			next = value.Number
		}
	}
	return next + 1
}

// insertCommentLocked appends a comment body to a PR's thread.
func (p *InMemoryPrPlatform) insertCommentLocked(prNumber int, body string) {
	p.comments[prNumber] = append(p.comments[prNumber], body)
}

// ListOpenDocsPrs lists the open docsync PRs of repo (TS listOpenDocsPrs).
func (p *InMemoryPrPlatform) ListOpenDocsPrs(_ context.Context, repo string) ([]PrRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var matches []PrRef
	for _, value := range p.values {
		if p.repoByPr[value.Number] != repo {
			continue
		}
		if !strings.HasPrefix(value.Branch, SelfTriggerBranchPrefix) {
			continue
		}
		if value.State != PrStateOpen {
			continue
		}
		matches = append(matches, value)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Number < matches[j].Number })
	return matches, nil
}

// FindByPrBodyMarker finds the marker-keyed PR (TS findByPrBodyMarker).
func (p *InMemoryPrPlatform) FindByPrBodyMarker(
	_ context.Context,
	repo string,
	marker string,
	opts *PrMarkerLookupOptions,
) (PrRef, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, value := range p.values {
		if !strings.Contains(value.Body, marker) {
			continue
		}
		if opts != nil && opts.State != "" && value.State != PrState(opts.State) {
			continue
		}
		return value, true, nil
	}
	return PrRef{}, false, nil
}

// CreatePr opens a PR (TS createPr). The returned ref is the full record; the
// TS double returned `{ repo, number }` (a subset the TS type system accepted),
// Go's return type is the full PrRef.
func (p *InMemoryPrPlatform) CreatePr(_ context.Context, input CreatePrInput) (PrRef, error) {
	if len(input.Repo) == 0 || len(input.Branch) == 0 || len(input.Base) == 0 ||
		len(input.Title) == 0 || len(input.Body) == 0 {
		return PrRef{}, fmt.Errorf(
			"InMemoryPrPlatform.createPr: repo/branch/base/title/body must be nonempty")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	number := p.nextNumberLocked()
	created := PrRef{
		Number:    number,
		Branch:    input.Branch,
		Base:      input.Base,
		Title:     input.Title,
		Body:      input.Body,
		State:     PrStateOpen,
		UpdatedAt: inMemoryTimestamp(number),
	}
	p.values = append(p.values, created)
	p.repoByPr[number] = input.Repo
	p.insertCommentLocked(number, "1000-docsync-pr-created")
	return created, nil
}

// UpdatePr updates an existing PR (TS updatePr) — a posted-but-empty update is
// a failed fetch, and a missing PR is a loud error (never a silent success).
func (p *InMemoryPrPlatform) UpdatePr(_ context.Context, pr PrRef, input UpdatePrInput) (PrRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for index := range p.values {
		if p.values[index].Number != pr.Number {
			continue
		}
		if input.Title != nil {
			p.values[index].Title = *input.Title
		}
		if input.Body != nil {
			p.values[index].Body = *input.Body
		}
		p.values[index].UpdatedAt = p.clock()
		return p.values[index], nil
	}
	return PrRef{}, fmt.Errorf("InMemoryPrPlatform: no PR #%d", pr.Number)
}

// MarkSuperseded runs the lifecycle transition (TS markSuperseded).
func (p *InMemoryPrPlatform) MarkSuperseded(_ context.Context, pr, newerPr PrRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for index := range p.values {
		if p.values[index].Number != pr.Number {
			continue
		}
		note := ""
		if newerPr.Number != 0 {
			note = fmt.Sprintf("superseded by #%d — newer analysis (PLAN §7.3)", newerPr.Number)
		} else {
			note = newerPr.Body
		}
		if len(note) > 0 {
			p.insertCommentLocked(pr.Number, note)
		}
		p.values[index].State = PrStateClosed
		return nil
	}
	return fmt.Errorf("InMemoryPrPlatform: no PR #%d", pr.Number)
}

// ClosePr closes a PR (TS closePr).
func (p *InMemoryPrPlatform) ClosePr(_ context.Context, pr PrRef, comment string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for index := range p.values {
		if p.values[index].Number == pr.Number {
			p.values[index].State = PrStateClosed
			return nil
		}
	}
	return fmt.Errorf("InMemoryPrPlatform: no PR #%d", pr.Number)
}

// AppendPrComment appends a comment (TS appendPrComment).
func (p *InMemoryPrPlatform) AppendPrComment(_ context.Context, pr PrRef, body string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, value := range p.values {
		if value.Number == pr.Number {
			p.insertCommentLocked(pr.Number, body)
			return nil
		}
	}
	return fmt.Errorf("InMemoryPrPlatform: no PR #%d", pr.Number)
}

// CommentsFor returns the comment thread of a PR (TS commentsFor) — a copy, so
// callers can never mutate the double's state.
func (p *InMemoryPrPlatform) CommentsFor(prNumber int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.comments[prNumber]...)
}

// Prs returns every PR record of a repo (test assertion seam — the TS double
// exposes its values only to its own module, the Go tests need the projection).
func (p *InMemoryPrPlatform) Prs(repo string) []PrRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	var matches []PrRef
	for _, value := range p.values {
		if p.repoByPr[value.Number] == repo {
			matches = append(matches, value)
		}
	}
	return matches
}

// ------------------------------------------------------- the upsert client

// DocPrClientDeps is everything the client takes (TS CreateDocPrClientDeps).
type DocPrClientDeps struct {
	Platform      PrPlatformPort
	Credentials   PrCredentials
	Repo          string
	DefaultBranch string
	// Clock is the injected ISO-8601 clock (never a wall clock, P19).
	Clock func() string
}

// PrUpsertAction is the upsert action union (TS 'created' | 'updated').
type PrUpsertAction string

// The two upsert actions.
const (
	PrUpsertCreated PrUpsertAction = "created"
	PrUpsertUpdated PrUpsertAction = "updated"
)

// UpsertDocPrInput is the upsert payload (TS UpsertDocPrInput).
type UpsertDocPrInput struct {
	Branch         string
	Title          string
	Body           string
	IdempotencyKey string
	// Markers are the extra trailing marker comments (self-trigger,
	// superseded-by, incident ids) stamped after the idempotency marker.
	Markers []string
	// LookupState restricts the marker lookup (nil ⇒ `open`).
	LookupState *PrLookupState
}

// UpsertDocPrResult is the upsert outcome (TS UpsertDocPrResult).
type UpsertDocPrResult struct {
	PR     PrRef
	Action PrUpsertAction
}

// DocPrClient is the marker-keyed idempotent PR upsert (TS DocPrClient).
type DocPrClient interface {
	UpsertDocPr(ctx context.Context, input UpsertDocPrInput) (UpsertDocPrResult, error)
}

// docPrClient is the constructed upsert client. The credential union is
// validated by the constructor and then deliberately NOT retained (INV-8:
// "validated by the composed client, then never retained") — the platform
// owns the token at call time, so no docPrClient field carries it.
type docPrClient struct {
	platform      PrPlatformPort
	repo          string
	defaultBranch string
	clock         func() string
}

// NewDocPrClient builds the client (TS createDocPrClient). It FAILS CLOSED —
// exactly like the TS constructor — on a malformed credential shape (INV-8):
// a half credential never reaches the platform.
func NewDocPrClient(deps DocPrClientDeps) (DocPrClient, error) {
	if deps.Platform == nil {
		return nil, docutil.New(docutil.CodeConfig, "createDocPrClient: platform is required")
	}
	if deps.Repo == "" {
		return nil, docutil.New(docutil.CodeConfig, "createDocPrClient: repo must be a nonempty string")
	}
	if deps.DefaultBranch == "" {
		return nil, docutil.New(docutil.CodeConfig,
			"createDocPrClient: defaultBranch must be a nonempty string (INV-1)")
	}
	if deps.Clock == nil {
		return nil, docutil.New(docutil.CodeConfig, "createDocPrClient: clock must be a () => string function")
	}
	if err := assertCredentialsShape(deps.Credentials); err != nil {
		return nil, err
	}
	return &docPrClient{
		platform:      deps.Platform,
		repo:          deps.Repo,
		defaultBranch: deps.DefaultBranch,
		clock:         deps.Clock,
	}, nil
}

// UpsertDocPr is the single write ceremony (TS upsertDocPr): the SAME
// idempotency key UPDATES the existing PR; the FIRST keyed PR for a missing PR
// CREATES behind one bounded 410 retry. Dedupe is marker-keyed — the client
// never lists-and-guesses, and re-invocation never duplicates.
func (c *docPrClient) UpsertDocPr(ctx context.Context, input UpsertDocPrInput) (UpsertDocPrResult, error) {
	marker := IdempotencyMarker(input.IdempotencyKey)
	lookupState := PrLookupOpen
	if input.LookupState != nil {
		lookupState = *input.LookupState
	}
	stamped := stampPrBodyMarkers(input.Body, marker, input.Markers)
	input.Body = stamped

	found, ok, err := c.platform.FindByPrBodyMarker(ctx, c.repo, marker, &PrMarkerLookupOptions{State: lookupState})
	if err != nil {
		return UpsertDocPrResult{}, err
	}
	if ok {
		updated, err := c.platform.UpdatePr(ctx, found, UpdatePrInput{Title: &input.Title, Body: &input.Body})
		if err != nil {
			return UpsertDocPrResult{}, err
		}
		return UpsertDocPrResult{PR: updated, Action: PrUpsertUpdated}, nil
	}

	// Creation only — the one idempotent-unsafe-on-410 call. A 410 under
	// eventual consistency is one bounded retry of the SAME creation (the marker
	// makes it idempotent — same key ⇒ same PR, never a duplicate, INV-11).
	created, err := WithGoneRetry(func() (PrRef, error) {
		return c.platform.CreatePr(ctx, CreatePrInput{
			Repo:   c.repo,
			Branch: input.Branch,
			Base:   c.defaultBranch,
			Title:  input.Title,
			Body:   input.Body,
		})
	}, &GoneRetryOptions{})
	if err != nil {
		return UpsertDocPrResult{}, err
	}
	return UpsertDocPrResult{PR: created, Action: PrUpsertCreated}, nil
}
