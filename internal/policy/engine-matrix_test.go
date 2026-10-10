// Ported from packages/policy/test/engine-matrix.test.ts — the fixed §5.3
// matrix shape (architecture code, not policy data), the D11 confidence
// bucketing from calibrated policy data, conservative-max risk resolution and
// the D2 ownership floor.
//
// FILE NOTE: named engine-matrix_test.go because the shadow unit's
// shadow-matrix test owns matrix_test.go (one writer per file).
//
// The shared drift-guard fixture
// (packages/schemas/test/fixtures/modes.fixture.json) lives outside the Go
// module and is test-only data in TS too, so its content is inlined below and
// the equality assertion is preserved verbatim.
package policy_test

import (
	"github.com/DUDE616/docSync-go/internal/docutil"
	"math"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// matrixRouting is the §5.2 bootstrap thresholds (POLICY_EXAMPLE routing) used
// as calibrated DATA throughout — never hard-coded in the engine.
func matrixRouting() policy.Routing {
	calibrationFloor := 0.94
	docImpactFloor := 0.94
	return policy.Routing{
		HighConfidence:               0.94,
		Review:                       0.72,
		CalibrationFloor:             &calibrationFloor,
		DocumentationImpactAutoFloor: &docImpactFloor,
	}
}

// matrixPrefixMatcher — minimal prefix matcher for `dir/**` globs — glob
// matching is owned elsewhere (injected seam).
func matrixPrefixMatcher(path, glob string) bool {
	base := glob
	for {
		if len(base) >= 2 && base[len(base)-2:] == "**" {
			base = base[:len(base)-2]
			continue
		}
		if len(base) >= 1 && base[len(base)-1] == '/' {
			base = base[:len(base)-1]
			continue
		}
		break
	}
	return len(base) > 0 && (path == base || (len(path) > len(base)+1 && path[:len(base)] == base && path[len(base)] == '/'))
}

// modesFixtureProtected / modesFixtureWritable inline the shared drift-guard
// fixture (packages/schemas/test/fixtures/modes.fixture.json) — the policy AND
// @docsync/core test suites assert set equality with it in TS.
var (
	modesFixtureProtected = []string{"locked", "generated", "external"}
	modesFixtureWritable  = []string{"auto", "suggest", "human"}
)

func TestMatrix_FixedShapeMatchesSection53Verbatim(t *testing.T) {
	for _, testCase := range []struct {
		risk       schemas.RiskLevel
		confidence policy.ConfidenceBucket
		cell       policy.MatrixCell
	}{
		{schemas.RiskLevelLow, policy.ConfidenceBucketLow, policy.MatrixCellReview},
		{schemas.RiskLevelLow, policy.ConfidenceBucketMedium, policy.MatrixCellSuggest},
		{schemas.RiskLevelLow, policy.ConfidenceBucketHigh, policy.MatrixCellAuto},
		{schemas.RiskLevelMedium, policy.ConfidenceBucketLow, policy.MatrixCellBlock},
		{schemas.RiskLevelMedium, policy.ConfidenceBucketMedium, policy.MatrixCellReview},
		{schemas.RiskLevelMedium, policy.ConfidenceBucketHigh, policy.MatrixCellSuggest},
		{schemas.RiskLevelHigh, policy.ConfidenceBucketLow, policy.MatrixCellBlock},
		{schemas.RiskLevelHigh, policy.ConfidenceBucketMedium, policy.MatrixCellBlock},
		{schemas.RiskLevelHigh, policy.ConfidenceBucketHigh, policy.MatrixCellReview},
	} {
		t.Run(string(testCase.risk)+" × "+string(testCase.confidence), func(t *testing.T) {
			cell, err := policy.LookupMatrixCell(testCase.risk, testCase.confidence)
			require.NoError(t, err)
			require.Equal(t, testCase.cell, cell)
		})
	}
}

func TestMatrix_ExportsTheTableExactlyAsSection53Specifies(t *testing.T) {
	// auto in exactly one cell; the diagonal is review; block left of the
	// diagonal; high row never auto.
	require.Equal(t, map[schemas.RiskLevel]map[policy.ConfidenceBucket]policy.MatrixCell{
		schemas.RiskLevelLow: {
			policy.ConfidenceBucketLow:    policy.MatrixCellReview,
			policy.ConfidenceBucketMedium: policy.MatrixCellSuggest,
			policy.ConfidenceBucketHigh:   policy.MatrixCellAuto,
		},
		schemas.RiskLevelMedium: {
			policy.ConfidenceBucketLow:    policy.MatrixCellBlock,
			policy.ConfidenceBucketMedium: policy.MatrixCellReview,
			policy.ConfidenceBucketHigh:   policy.MatrixCellSuggest,
		},
		schemas.RiskLevelHigh: {
			policy.ConfidenceBucketLow:    policy.MatrixCellBlock,
			policy.ConfidenceBucketMedium: policy.MatrixCellBlock,
			policy.ConfidenceBucketHigh:   policy.MatrixCellReview,
		},
	}, policy.MATRIX)
}

func TestMatrix_OwnershipFloorModesEqualsTheSharedDriftGuardFixture(t *testing.T) {
	floor := append([]string{}, policy.OwnershipFloorModes...)
	sort.Strings(floor)
	protected := append([]string{}, modesFixtureProtected...)
	sort.Strings(protected)
	// The policy floor IS the protected set (locked|generated|external) —
	// asserted as EQUALITY, not merely a subset, so neither side can drift.
	require.Equal(t, protected, floor)
	require.Len(t, policy.OwnershipFloorModes, len(modesFixtureProtected))
	// The writable complement is pinned by the same fixture.
	require.Equal(t, modesFixtureWritable, []string{"auto", "suggest", "human"})
}

func TestMatrix_VP28_HighRiskRowNeverAuto(t *testing.T) {
	for _, bucket := range []policy.ConfidenceBucket{
		policy.ConfidenceBucketLow, policy.ConfidenceBucketMedium, policy.ConfidenceBucketHigh,
	} {
		cell, err := policy.LookupMatrixCell(schemas.RiskLevelHigh, bucket)
		require.NoError(t, err)
		require.NotEqual(t, policy.MatrixCellAuto, cell)
	}
	cell, err := policy.LookupMatrixCell(schemas.RiskLevelHigh, policy.ConfidenceBucketHigh)
	require.NoError(t, err)
	require.Equal(t, policy.MatrixCellReview, cell)
}

func TestMatrix_D2OwnershipFloorBlocksAtAnyConfidence(t *testing.T) {
	for _, mode := range []policy.OwnershipFloorMode{
		policy.OwnershipFloorLocked, policy.OwnershipFloorGenerated, policy.OwnershipFloorExternal,
	} {
		require.Equal(t, policy.ResolvedCellBlocked, policy.ApplyOwnershipFloor(policy.MatrixCellAuto, mode))
	}
}

func TestMatrix_AutoCellFiresOnlyInModeAuto(t *testing.T) {
	require.Equal(t, policy.ResolvedCellAuto, policy.ApplyOwnershipFloor(policy.MatrixCellAuto, policy.OwnershipFloorAuto))
	require.Equal(t, policy.ResolvedCellAuto, policy.ApplyOwnershipFloor(policy.MatrixCellAuto, policy.OwnershipFloorSuggest))
	require.Equal(t, policy.ResolvedCellAuto, policy.ApplyOwnershipFloor(policy.MatrixCellAuto, policy.OwnershipFloorHuman))
}

func TestMatrix_LowHighInReviewGatedWritableModeStaysAutoAtTheFloor(t *testing.T) {
	// low×high is the only auto cell; the cell itself is auto, and the FLOOR
	// keeps it auto for non-ownership modes — outcome mapping owns the
	// mode-permission degradation (engine-outcome tests).
	cell, err := policy.LookupMatrixCell(schemas.RiskLevelLow, policy.ConfidenceBucketHigh)
	require.NoError(t, err)
	require.Equal(t, policy.MatrixCellAuto, cell)
	require.Equal(t, policy.ResolvedCellAuto, policy.ApplyOwnershipFloor(cell, policy.OwnershipFloorSuggest))
}

func TestConfidenceBucket_D11_BoundariesAreData(t *testing.T) {
	routing := matrixRouting()
	bucket, err := policy.ResolveConfidenceBucket(0.94, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketHigh, bucket)
	bucket, err = policy.ResolveConfidenceBucket(0.9399, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketMedium, bucket)
	bucket, err = policy.ResolveConfidenceBucket(0.72, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketMedium, bucket)
	bucket, err = policy.ResolveConfidenceBucket(0.7199, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketLow, bucket)
	bucket, err = policy.ResolveConfidenceBucket(1, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketHigh, bucket)
	bucket, err = policy.ResolveConfidenceBucket(0, routing)
	require.NoError(t, err)
	require.Equal(t, policy.ConfidenceBucketLow, bucket)
}

func TestConfidenceBucket_RejectsOutOfRangeOrNonFinite(t *testing.T) {
	routing := matrixRouting()
	for _, confidence := range []float64{1.01, -0.01, math.NaN()} {
		_, err := policy.ResolveConfidenceBucket(confidence, routing)
		var rangeErr *policy.EngineRangeError
		require.ErrorAs(t, err, &rangeErr, "confidence %v must fail closed", confidence)
	}
}

func TestRiskBucket_ConservativeMaxManifestDeclaredVsJevRisk(t *testing.T) {
	high := schemas.RiskLevelHigh
	low := schemas.RiskLevelLow
	medium := schemas.RiskLevelMedium
	for _, testCase := range []struct {
		name     string
		input    policy.RiskBucketInput
		expected schemas.RiskLevel
	}{
		{"manifest high + jev low → high", policy.RiskBucketInput{JevRisk: low, ManifestRisk: &high}, schemas.RiskLevelHigh},
		{"jev high + manifest low → high", policy.RiskBucketInput{JevRisk: high, ManifestRisk: &low}, schemas.RiskLevelHigh},
		{"no manifest declaration → Jev risk alone", policy.RiskBucketInput{JevRisk: medium}, schemas.RiskLevelMedium},
		{
			"override globs resolved through the injected matcher",
			policy.RiskBucketInput{
				JevRisk:            low,
				RiskClassOverrides: map[string]schemas.RiskLevel{"docs/security/**": schemas.RiskLevelHigh},
				DocPath:            matrixStrPtr("docs/security/auth.md"),
				MatchGlobs:         matrixPrefixMatcher,
			},
			schemas.RiskLevelHigh,
		},
		{
			"non-matching override leaves Jev risk untouched",
			policy.RiskBucketInput{
				JevRisk:            medium,
				RiskClassOverrides: map[string]schemas.RiskLevel{"docs/security/**": schemas.RiskLevelHigh},
				DocPath:            matrixStrPtr("docs/other.md"),
				MatchGlobs:         matrixPrefixMatcher,
			},
			schemas.RiskLevelMedium,
		},
		{
			"overlapping matching overrides compose to the highest class",
			policy.RiskBucketInput{
				JevRisk: low,
				RiskClassOverrides: map[string]schemas.RiskLevel{
					"docs/security/**":     schemas.RiskLevelHigh,
					"docs/security/api.md": schemas.RiskLevelMedium,
				},
				DocPath:    matrixStrPtr("docs/security/api.md"),
				MatchGlobs: matrixPrefixMatcher,
			},
			schemas.RiskLevelHigh,
		},
		{
			"pre-resolved manifestRisk composes with raw override data",
			policy.RiskBucketInput{
				JevRisk:            medium,
				ManifestRisk:       &medium,
				RiskClassOverrides: map[string]schemas.RiskLevel{"docs/legal/**": schemas.RiskLevelHigh},
				DocPath:            matrixStrPtr("docs/legal/terms.md"),
				MatchGlobs:         matrixPrefixMatcher,
			},
			schemas.RiskLevelHigh,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			risk, err := policy.ResolveRiskBucket(testCase.input)
			require.NoError(t, err)
			require.Equal(t, testCase.expected, risk)
		})
	}
}

func TestRiskBucket_OverridesWithoutMatcherOrPathIsAProgrammerError(t *testing.T) {
	_, err := policy.ResolveRiskBucket(policy.RiskBucketInput{
		JevRisk:            schemas.RiskLevelLow,
		RiskClassOverrides: map[string]schemas.RiskLevel{"a/**": schemas.RiskLevelHigh},
	})
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
}

func TestOwnershipFloor_NormalizesBlockToBlockedAndPassesOthersThrough(t *testing.T) {
	require.Equal(t, policy.ResolvedCellBlocked, policy.ApplyOwnershipFloor(policy.MatrixCellBlock, policy.OwnershipFloorAuto))
	require.Equal(t, policy.ResolvedCellAuto, policy.ApplyOwnershipFloor(policy.MatrixCellAuto, policy.OwnershipFloorAuto))
	require.Equal(t, policy.ResolvedCellSuggest, policy.ApplyOwnershipFloor(policy.MatrixCellSuggest, policy.OwnershipFloorHuman))
	require.Equal(t, policy.ResolvedCellReview, policy.ApplyOwnershipFloor(policy.MatrixCellReview, policy.OwnershipFloorSuggest))
}

func TestHigherRisk_IsTheConservativeMax(t *testing.T) {
	require.Equal(t, schemas.RiskLevelMedium, policy.HigherRisk(schemas.RiskLevelLow, schemas.RiskLevelMedium))
	require.Equal(t, schemas.RiskLevelHigh, policy.HigherRisk(schemas.RiskLevelHigh, schemas.RiskLevelMedium))
	require.Equal(t, schemas.RiskLevelLow, policy.HigherRisk(schemas.RiskLevelLow, schemas.RiskLevelLow))
}

// matrixStrPtr is the *string helper for the override-table pointers.
func matrixStrPtr(v string) *string { return &v }

func TestLookupMatrixCell_GuardsFailClosedOnUnknownBuckets(t *testing.T) {
	_, err := policy.LookupMatrixCell(schemas.RiskLevel("bogus"), policy.ConfidenceBucketLow)
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
	require.Contains(t, rangeErr.Error(), "no matrix row for risk bucket 'bogus'")

	_, err = policy.LookupMatrixCell(schemas.RiskLevelLow, policy.ConfidenceBucket("bogus"))
	rangeErr = nil
	require.ErrorAs(t, err, &rangeErr)
	require.Contains(t, rangeErr.Error(), "no matrix cell for risk bucket 'low' × confidence bucket 'bogus'")
}

func TestHigherRisk_UnknownLevelOrdersLikeTSNaN(t *testing.T) {
	// TS `RISK_ORDER[a] >= RISK_ORDER[b] ? a : b` treats an unknown level as
	// undefined: every comparison is false, so `b` wins whenever either side
	// is unknown. The conservative-max guarantee holds for closed-set inputs
	// (the only typed-reachable case); the unknown-level path is pinned here
	// for exact parity with the TS ordering.
	require.Equal(t, schemas.RiskLevelLow, policy.HigherRisk(schemas.RiskLevel("bogus"), schemas.RiskLevelLow))
	require.Equal(t, schemas.RiskLevel("bogus"), policy.HigherRisk(schemas.RiskLevelHigh, schemas.RiskLevel("bogus")))
}

func TestEngineRangeError_IsACodedError(t *testing.T) {
	_, err := policy.ResolveConfidenceBucket(math.NaN(), matrixRouting())
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
	require.Equal(t, "RANGE", rangeErr.Code())
	require.Contains(t, rangeErr.Error(), "must be a finite number in [0, 1]")
	require.Equal(t, docutil.CodeValidation, docutil.CodeOf(rangeErr))
}

// matrixShapeTagV1 / matrixShapeHashV1 mirror the calibration unit's D11 shape
// pin (internal/policy/thresholds.go: MatrixShapeV1 "fixed-3x3-v1" and
// MatrixShapeHashV1 — the sha256 over canonical JSON of the fixed table). They
// are inlined as literals (not referenced) so this cross-unit consistency
// check needs no compile-time dependency on thresholds.go.
const (
	matrixShapeTagV1  = "fixed-3x3-v1"
	matrixShapeHashV1 = "sha256:82b30f0886a7a8558d2bf448fcdd056c9d3e04e9a09c4aae64f443db5484a418"
)

// TestMatrix_ShapeHashMatchesTheCalibrationD11Pin pins the engine-side table
// to the calibration unit's shape hash: the fixed table is the SAME table
// ComputeMatrixShapeHash pins, so any shape drift fails here too (a shape
// change is an ARCHITECTURE change, never calibration data — D11).
func TestMatrix_ShapeHashMatchesTheCalibrationD11Pin(t *testing.T) {
	table := map[string]any{}
	for risk, row := range policy.MATRIX {
		cells := map[string]any{}
		for confidence, cell := range row {
			cells[string(confidence)] = string(cell)
		}
		table[string(risk)] = cells
	}
	canonical, err := docutil.CanonicalJSON(table)
	require.NoError(t, err)
	require.Equal(t, matrixShapeHashV1, "sha256:"+docutil.ContentHash(canonical))
	require.Equal(t, matrixShapeTagV1, "fixed-3x3-v1")
}
