package verifier

// Ported from packages/verifier/src/tier1/allowlist-check.ts (P4-T03; PLAN
// §5.4 item 3 — the allowlist rule is item 3's first sentence; ARCH §11.4; D2
// defense in depth). This check is pure over the injected input data and runs
// even when the application simulation failed — it is the verifier's
// independent re-check of permissions (defense in depth, lock layer 4):
//
//   - allowlist (invariant 3): `patch.doc_path` must be in the manifest-derived
//     writable allowlist the APPLICATION injected — never derived from model
//     output here.
//
// The ownership-floor half of the TS file is ported in
// tier1_ownership_floor.go (D2): same file, its own Go file per the unit's
// file map.

import (
	"strconv"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// writableModes is TS WRITABLE_MODES (core/src/workspace/types.ts:96) — the
// modes AI may ever write: auto | suggest | human (human is review-gated).
// locked/generated/external are NOT writable at any confidence (D2).
var writableModes = map[schemas.DocMode]bool{
	schemas.DocModeAuto:    true,
	schemas.DocModeSuggest: true,
	schemas.DocModeHuman:   true,
}

// CheckManifestAllowlistOnly re-checks that the patch targets a
// manifest-listed, writable document (invariant 3).
func CheckManifestAllowlistOnly(input Tier1Input) schemas.VerificationCheck {
	if input.ManifestAllowlist[input.Patch.DocPath] {
		return schemas.VerificationCheck{
			ID:     "manifest-allowlist-only",
			Status: schemas.CheckPass,
		}
	}
	return schemas.VerificationCheck{
		ID:     "manifest-allowlist-only",
		Status: schemas.CheckFail,
		Detail: tier1DetailPtr(
			"doc_path '" + input.Patch.DocPath + "' is not in the manifest-derived writable allowlist " +
				"(" + strconv.Itoa(len(input.ManifestAllowlist)) + " path(s)) — docsync patches only manifest-listed documents " +
				"(invariant 3; the allowlist is computed from the manifest, never from model output)",
		),
	}
}
