package verifier

// Tier-2 workspace-restricted anchor binding (port of tier2/bind.ts; P4-T09;
// PLAN §5.4 item 9; ARCH §4.8 "anchor available in the constrained workspace"):
// for each decomposed claim, resolve WHERE its evidence lives — the claim-store
// snapshot first (over-deletion-guard provenance), then a DETERMINISTIC
// token-level search of the injected workspace sources for the claimed tokens
// (re-extracted via ExtractClaimedTokens, never re-inferred).
//
// The `via` field IS the anchor-kind vocabulary ('claim-store' |
// 'workspace-token' | 'none'). The V1 binding surface is TOKEN-LEVEL: config
// keys, test names, and any other backticked token bind the same token way —
// semantic surfaces (OpenAPI path/operation, config key with RESOLVED value,
// test-name structure) are Phase-11 analyzer territory (A4 posture: declared,
// never silent — the claim-anchor-binding record carries the gap note when ≥1
// workspace-token binding exists, tier2.go).
//
// Workspace restriction is the safety posture: the search scans ONLY the
// `workspaceSources` map injected at the orchestrator edge (manifest-scoped
// workspace) — no fs, no git, no network. A token that exists in the repository
// but not in the injected map yields an UNBOUND claim, which routes
// `unanchored` downstream and never auto-eligibility (unanchored ⇒ suggest,
// never auto — PLAN §5.6/ARCH §4.8; a guessed anchor is never fabricated).
// Behavioral and `other` claims still attempt binding — a behavioral claim
// binds for the contract-corroboration path and evidence display (though its
// empty token surface binds only via the claim store), and unverified `other`
// records are harmed by nothing.
//
// Determinism: sources iterate in sorted-path order, lines top-down within a
// file, and a claim binds at the FIRST file+line where ANY claimed token
// matches; the same input always yields deep-equal bindings. Signature claims
// tolerate reformatted sources — after the verbatim span, a whitespace-
// normalized span comparison, then the identifier prefix before `(`
// (declaration sites get reformatted; the call name survives). Pure: no fs, no
// git, no network, no clock of its own, no Date/random.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// unboundDetail is the canonical unbound detail (the claim routes `unanchored`
// downstream).
const unboundDetail = "no workspace source matches the claimed token(s)"

// tier2WithoutWhitespaceRE collapses every whitespace run — the
// reformatted-signature comparison form.
var tier2WithoutWhitespaceRE = regexp.MustCompile(`\s+`)

/* ------------------------------------------------------------------ helpers */

// tier2WithoutWhitespace removes whitespace entirely (TS: replace(/\s+/g, ”)).
func tier2WithoutWhitespace(text string) string {
	return tier2WithoutWhitespaceRE.ReplaceAllString(text, "")
}

// tier2SignatureIdentifierPrefix is the identifier prefix of a signature span:
// everything before the first `(`.
func tier2SignatureIdentifierPrefix(token string) string {
	paren := strings.Index(token, "(")
	prefix := token
	if paren >= 0 {
		prefix = token[:paren]
	}
	return strings.TrimSpace(prefix)
}

// tier2TokenMatchesLine reports whether ONE claimed token matches one source
// line. Non-signature tokens: exact substring. Signature tokens (declarations
// get reformatted): the verbatim span first, then the whitespace-normalized
// span, then the identifier prefix before `(`.
func tier2TokenMatchesLine(token string, kind DecomposedClaimKind, line string) bool {
	if strings.Contains(line, token) {
		return true
	}
	if kind == DecomposedClaimKindSignature {
		if strings.Contains(tier2WithoutWhitespace(line), tier2WithoutWhitespace(token)) {
			return true
		}
		prefix := tier2SignatureIdentifierPrefix(token)
		return prefix != "" && strings.Contains(line, prefix)
	}
	return false
}

// tier2SearchWorkspaceSources is the deterministic workspace search for one
// claim's tokens: source paths in sorted order, lines top-down, binding at the
// FIRST file+line where ANY token matches. Never scans beyond the injected map.
// Unbound (same detail for every reason) when there are no tokens, no sources,
// or no match.
func tier2SearchWorkspaceSources(
	tokens []string,
	kind DecomposedClaimKind,
	paths []string,
	workspaceSources map[string]string,
) ClaimBinding {
	if len(tokens) == 0 || len(paths) == 0 {
		return ClaimBinding{Bound: false, Via: BindingViaNone, Detail: unboundDetail}
	}
	for _, path := range paths {
		content, ok := workspaceSources[path]
		if !ok {
			continue
		}
		lines := claims.SplitDocLines(content)
		for offset, line := range lines {
			for _, token := range tokens {
				if tier2TokenMatchesLine(token, kind, line) {
					line1Based := offset + 1
					return ClaimBinding{
						Bound:  true,
						Via:    BindingViaWorkspaceToken,
						File:   path,
						Line:   &line1Based,
						Detail: fmt.Sprintf("workspace:%s:%d", path, line1Based),
					}
				}
			}
		}
	}
	return ClaimBinding{Bound: false, Via: BindingViaNone, Detail: unboundDetail}
}

/* ---------------------------------------------------------------- exports */

// BindClaims binds each decomposed claim — IN INDEX ORDER (the decomposition
// order; a copy is sorted by `index` so unordered input stays deterministic) —
// to its evidence anchor:
//
//  1. `existing_claim_id` present AND found in `input.claims` ⇒ `claim-store`
//     binding: `file`/`line` from the stored claim's `source` (`line` may be
//     absent when the store never recorded one), detail `claim-store:<id>`.
//  2. Otherwise the claimed tokens (via ExtractClaimedTokens) are searched over
//     `input.workspaceSources` ⇒ `workspace-token` binding at the first
//     file+line a token matches, detail `workspace:<file>:<line>`. The V1
//     surface is token-level (config keys and test names bind the same way);
//     semantic surfaces are a declared Phase-11 gap (A4 posture).
//  3. No match / no workspace sources / no token surface ⇒ unbound
//     (`via: 'none'`) — the claim routes `unanchored`, never auto
//     (PLAN §5.6/ARCH §4.8).
//
// Pure and deterministic (module doc): the same inputs yield deep-equal
// bindings; nothing outside `decomposed` and `input` is read.
func BindClaims(decomposed []DecomposedClaim, input Tier2Input) []ClaimBinding {
	claimsByID := make(map[string]schemas.Claim, len(input.Claims))
	for _, claim := range input.Claims {
		claimsByID[claim.ID] = claim
	}
	workspaceSources := input.WorkspaceSources
	paths := make([]string, 0, len(workspaceSources))
	for path := range workspaceSources {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	sorted := tier2SortDecomposedByIndex(decomposed)
	bindings := make([]ClaimBinding, 0, len(sorted))
	for _, claim := range sorted {
		if claim.ExistingClaimID != "" {
			if stored, found := claimsByID[claim.ExistingClaimID]; found {
				binding := ClaimBinding{
					Index:  claim.Index,
					Bound:  true,
					Via:    BindingViaClaimStore,
					File:   stored.Source.File,
					Detail: fmt.Sprintf("claim-store:%s", claim.ExistingClaimID),
				}
				if stored.Source.Line != nil {
					line := *stored.Source.Line
					binding.Line = &line
				}
				bindings = append(bindings, binding)
				continue
			}
		}
		tokens := ExtractClaimedTokens(claim.Text, claim.Kind)
		searched := tier2SearchWorkspaceSources(tokens, claim.Kind, paths, workspaceSources)
		searched.Index = claim.Index
		bindings = append(bindings, searched)
	}
	return bindings
}
