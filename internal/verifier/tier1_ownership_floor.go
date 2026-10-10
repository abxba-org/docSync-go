package verifier

// Ported from packages/verifier/src/tier1/allowlist-check.ts — the
// ownership-half of P4-T03 (PLAN §5.4 item 3's second sentence; ARCH §11.4;
// D2). Pure over the injected input data and runs even when the application
// simulation failed — the verifier's independent re-check of permissions
// (defense in depth, lock layer 4):
//
//   - ownership (D2 re-check): the effective target mode must be a WRITABLE
//     mode (`auto` | `suggest` | `human`). `locked`/`generated`/`external`
//     documents are rejected HERE even when the application guard also
//     refused — no single layer is trusted to be the only one (D2).
//
// The TS file splits the allowlist and ownership rules across two exported
// checks; the migration keeps the split (one check per Go file) so the
// ownership-floor record keeps its own provenance surface.

import "github.com/DUDE616/docSync-go/internal/schemas"

// CheckOwnershipFloor re-checks the D2 ownership floor at verification:
// locked/generated/external documents are read-only at ANY confidence.
func CheckOwnershipFloor(input Tier1Input) schemas.VerificationCheck {
	if writableModes[input.TargetMode] {
		return schemas.VerificationCheck{
			ID:     "ownership-floor",
			Status: schemas.CheckPass,
		}
	}
	return schemas.VerificationCheck{
		ID:     "ownership-floor",
		Status: schemas.CheckFail,
		Detail: tier1DetailPtr(
			"target mode '" + string(input.TargetMode) + "' is not writable (D2 ownership floor re-check at verification: " +
				"locked/generated/external documents are read-only at any confidence, invariants 2/3) — " +
				"rejected here even if the application guard also refused (defense in depth)",
		),
	}
}
