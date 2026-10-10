package verifier

// Ported from packages/verifier/src/tier1/links-check.ts (P4-T06; PLAN §5.4
// item 6): links in the PATCHED content must resolve — against data the
// application injected; the verifier NEVER opens a network connection itself
// (a reachability probe is a caller PORT):
//
//   - internal anchors `[text](#anchor)` must match a GitHub-style heading slug
//     of the patched content (rules documented in markdown.go);
//   - relative file links (`docs/other.md`, `./other.md`, `../other.md`,
//     optional `#fragment`) must resolve to a known doc in
//     `linkInputs.knownDocs` (missing target ⇒ fail; a fragment must match the
//     target's heading anchors — the patched doc's own content is used when the
//     link resolves to the patched document itself);
//   - external URLs (`http://`/`https://`) pass ONLY when allowlisted OR the
//     injected `isReachable` port accepts them. WITHOUT a reachability port a
//     non-allowlisted external URL FAILS CLOSED with a fixed detail — no
//     network is ever attempted.
//
// Skipped when no patched content exists (the application did not simulate).

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// externalURLRE is TS EXTERNAL_URL.
var externalURLRE = regexp.MustCompile(`(?i)^https?://`)

// externalDetail is the fixed fail-closed detail for a non-allowlisted
// external URL without a reachability port (TS EXTERNAL_DETAIL).
const externalDetail = "external URL not allowlisted and no reachability port configured " +
	"(fail-closed default; the verifier never performs network I/O)"

// CheckLinkResolution validates the links of the patched content.
func CheckLinkResolution(input Tier1Input, patchedContent *string) schemas.VerificationCheck {
	if patchedContent == nil {
		return schemas.VerificationCheck{
			ID:     "link-resolution",
			Status: schemas.CheckSkipped,
			Detail: tier1DetailPtr(
				"no patched content available (application not simulated successfully) — link resolution cannot run",
			),
		}
	}
	return checkLinks(input, *patchedContent)
}

// checkLinks resolves every inline link of the patched content.
func checkLinks(input Tier1Input, patchedContent string) schemas.VerificationCheck {
	var linkInputs LinkCheckInputs
	if input.LinkInputs != nil {
		linkInputs = *input.LinkInputs
	}
	skipLines := tier1ExcludedLines(patchedContent)
	ownAnchors := HeadingAnchors(patchedContent, skipLines)
	docDir := posixDirname(input.Patch.DocPath)

	ctx := linkResolutionContext{
		ownAnchors: ownAnchors,
		ownPath:    input.Patch.DocPath,
		ownContent: patchedContent,
		docDir:     docDir,
		linkInputs: linkInputs,
	}

	var failures []string
	for _, link := range ExtractLinks(patchedContent) {
		if skipLines[link.Line] {
			continue // links inside code fences are literal content
		}
		if verdict := resolveLink(link.Target, ctx); verdict != nil {
			failures = append(failures, "line "+strconv.Itoa(link.Line)+": "+*verdict)
		}
	}

	if len(failures) > 0 {
		return schemas.VerificationCheck{
			ID:     "link-resolution",
			Status: schemas.CheckFail,
			Detail: tier1DetailPtr(
				strconv.Itoa(len(failures)) + " unresolved link(s): " + strings.Join(failures, "; ")),
		}
	}
	return schemas.VerificationCheck{ID: "link-resolution", Status: schemas.CheckPass}
}

// linkResolutionContext is the resolution context of one run (TS
// LinkResolutionContext).
type linkResolutionContext struct {
	ownAnchors map[string]bool
	ownPath    string
	ownContent string
	docDir     string
	linkInputs LinkCheckInputs
}

// resolveLink resolves one link target. Nil = resolved; a string = the
// failure evidence. Deterministic.
func resolveLink(target string, ctx linkResolutionContext) *string {
	if externalURLRE.MatchString(target) {
		if ctx.linkInputs.ExternalAllowlist != nil && ctx.linkInputs.ExternalAllowlist[target] {
			return nil
		}
		reachability := ctx.linkInputs.IsReachable
		if reachability == nil {
			return tier1DetailPtr(externalDetail)
		}
		reachable, err := reachability(target)
		if err != nil {
			return tier1DetailPtr("reachability port rejected '" + target + "': " + tier1ErrorMessage(err))
		}
		if !reachable {
			return tier1DetailPtr("external URL '" + target + "' rejected by the reachability port")
		}
		return nil
	}

	fragmentIndex := strings.Index(target, "#")
	var fragment *string
	if fragmentIndex >= 0 {
		fragment = tier1DetailPtr(target[fragmentIndex+1:])
	}
	pathPart := target
	if fragmentIndex >= 0 {
		pathPart = target[:fragmentIndex]
	}

	if len(pathPart) == 0 {
		// Pure internal anchor: must match a heading of the patched document.
		if fragment != nil && ctx.ownAnchors[*fragment] {
			return nil
		}
		anchor := ""
		if fragment != nil {
			anchor = *fragment
		}
		return tier1DetailPtr(
			"internal anchor '#" + anchor + "' does not match any heading in the patched document")
	}

	// Relative (or repo-root-absolute) file link — resolve against known docs.
	resolved := resolveDocPath(pathPart, ctx.docDir)
	if resolved == nil {
		return tier1DetailPtr("link target '" + target + "' does not stay inside the repository")
	}
	known := ctx.linkInputs.KnownDocs
	if known == nil {
		return tier1DetailPtr(
			"relative link '" + target + "' resolves to '" + *resolved + "' which is not a known doc " +
				"(missing target; known-docs index has 0 path(s))")
	}
	targetContent, knownDoc := known[*resolved]
	if !knownDoc {
		return tier1DetailPtr(
			"relative link '" + target + "' resolves to '" + *resolved + "' which is not a known doc " +
				"(missing target; known-docs index has " + strconv.Itoa(len(known)) + " path(s))")
	}
	if fragment == nil {
		return nil
	}
	// Fragment: anchor of the TARGET doc — the patched doc's own (patched)
	// content when the link points at it, the known snapshot otherwise.
	content := targetContent
	if *resolved == ctx.ownPath {
		content = ctx.ownContent
	}
	anchors := HeadingAnchors(content, tier1ExcludedLines(content))
	if anchors[*fragment] {
		return nil
	}
	return tier1DetailPtr(
		"anchor '#" + *fragment + "' in '" + *resolved + "' does not match any heading of the target document")
}

// posixDirname is the POSIX dirname of a repo-relative doc path ("" at the
// root).
func posixDirname(docPath string) string {
	slash := strings.LastIndex(docPath, "/")
	if slash < 0 {
		return ""
	}
	return docPath[:slash]
}

// resolveDocPath resolves a link's path part relative to the doc's directory
// (posix, `..`-aware). Nil when the link escapes the repository.
func resolveDocPath(pathPart, docDir string) *string {
	raw := docDir + "/" + pathPart
	if strings.HasPrefix(pathPart, "/") {
		raw = pathPart[1:]
	}
	var segments []string
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			if len(segments) == 0 {
				return nil // escapes the repository
			}
			segments = segments[:len(segments)-1]
			continue
		}
		segments = append(segments, segment)
	}
	return tier1DetailPtr(strings.Join(segments, "/"))
}
