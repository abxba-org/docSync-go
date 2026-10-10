// Ported from packages/policy/src/versioning.ts — policy-history versioning,
// hashing, rollback and the §8.4 gate (OD-R04/R4; ARCH §8.3.1/§8.4/§8.5).
//
// policy-history/v1 is an append-only, hold-band-ordered record chain rendered
// under `.docsync/policy-history/` (R4). The chain is validated structurally:
// record shape, unique strictly increasing versions, and the previous-pointer
// binding (`policy-history/v<N-1>.yml`). The schema files themselves are owned
// by the state/CAS package — this layer only validates the record chain.
//
// UNSAFE AUTO UPDATES (ARCH §8.4): policy rollback is metadata-only, and
// promotion requires recording that no unsafe auto update was performed by the
// candidate policies. Recording any unsafe auto update blocks promotion;
// recording zero allows promotion subject to the other gates.
//
// Hashing seam: canonical JSON bytes are produced by docutil.CanonicalJSON and
// digested by docutil.ContentHash, so a Go-built policy history hashes
// byte-identically to the TypeScript implementation (compiler.go's policyHash
// is the same seam).
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Policy-history layout constants (POLICY_HISTORY_*).
const (
	// PolicyHistoryDefaultDir is the default state directory a policy history
	// lives under (`.docsync`).
	PolicyHistoryDefaultDir = ".docsync"
	// PolicyHistorySubdir is the directory under the state dir holding the
	// version records.
	PolicyHistorySubdir = "policy-history"
	// PolicyHistoryDefaultExt is the record file extension.
	PolicyHistoryDefaultExt = "yml"
	// PolicyPointerSchemaID is the live-pointer artifact id (OD-R04) —
	// deliberately distinct from the Jev policy document id
	// (docsync.jev-policy/v1, OD-R40).
	PolicyPointerSchemaID = "docsync.policy/v1"
)

// PolicyVersionRecord is one appended policy-history version record
// (PolicyVersionRecord). Optional fields are pointers: nil is absent (the TS
// optional keys). Previous is nil for the first record (TS: null).
type PolicyVersionRecord struct {
	PolicyVersion int               `json:"policy_version"`
	CreatedBy     string            `json:"created_by"`
	Reason        string            `json:"reason"`
	CreatedAt     string            `json:"created_at"`
	Previous      *string           `json:"previous"`
	PolicyHash    string            `json:"policyHash"`
	Evaluation    *PolicyEvaluation `json:"evaluation,omitempty"`
	// UnsafeAutoUpdates reuses the schema gate struct
	// (TS: { enabled: boolean }).
	UnsafeAutoUpdates *UnsafeAutoUpdates `json:"unsafe_auto_updates,omitempty"`
}

// PolicyHistory is a structurally validated record chain (PolicyHistory).
type PolicyHistory struct {
	StorageDir    string                `json:"storageDir"`
	Records       []PolicyVersionRecord `json:"records"`
	ActiveVersion int                   `json:"activeVersion"`
}

// BuildPolicyHistoryInput is the buildPolicyHistory argument
// (TS: { records, storageDir? }); StorageDir defaults to `.docsync`.
type BuildPolicyHistoryInput struct {
	Records    []PolicyVersionRecord
	StorageDir string
}

// BuildPolicyHistoryResult is the discriminated union of
// `{ok:true, history}` and `{ok:false, diagnostics}` (TS union; Go has no
// unions — History is non-nil exactly when OK is true).
type BuildPolicyHistoryResult struct {
	OK          bool
	History     *PolicyHistory
	Diagnostics []PolicyDiagnostic
}

// RollbackToResult is the discriminated union of `{ok:true, activeVersion,
// pointerRecord}` and `{ok:false, diagnostics}` (TS union). Rollback-as-
// metadata: it copies an older version FORWARD under a fresh record — it never
// deletes or rewrites history (ARCH §8.5).
type RollbackToResult struct {
	OK            bool
	ActiveVersion int
	PointerRecord *schemas.PolicyPointer
	Diagnostics   []PolicyDiagnostic
}

// UnsafeAutoUpdatesGateResult ports the TS UnsafeAutoUpdatesGate type: the
// count and the derived verdict (promotion allowed only when zero were
// recorded). JSON keys match the TS wire shape.
type UnsafeAutoUpdatesGateResult struct {
	UnsafeAutoUpdates int  `json:"unsafe_auto_updates"`
	PromotionAllowed  bool `json:"promotionAllowed"`
}

// PolicyHistoryPath renders the on-disk path of one version record
// (policyHistoryPath; R4): `policy-history/v<version>.<ext>` with ext
// defaulting to `yml`.
//
// The TS original threw a RangeError on a non-positive version; Go returns a
// coded error (docutil.CodeValidation — the closest existing class: a value
// out of range is a caller input error) carrying the same message text.
func PolicyHistoryPath(version int, ext ...string) (string, error) {
	extension := PolicyHistoryDefaultExt
	if len(ext) > 0 {
		extension = ext[0]
	}
	path, errMsg := policyHistoryPath(version, extension)
	if errMsg != "" {
		return "", docutil.New(docutil.CodeValidation, errMsg)
	}
	return path, nil
}

// policyHistoryPath is the message-returning internal form: the empty message
// means success.
func policyHistoryPath(version int, ext string) (string, string) {
	if version <= 0 {
		return "", fmt.Sprintf("policyHistoryPath requires a positive integer version (got %d)", version)
	}
	return PolicyHistorySubdir + "/v" + strconv.Itoa(version) + "." + ext, ""
}

// BuildPolicyHistory validates the record chain and returns the typed history
// (buildPolicyHistory; R4/§8.4). Callers decode records from JSON/YAML — this
// is the structural gate (OD-R04). All-or-nothing: on any diagnostic the
// history is not produced.
func BuildPolicyHistory(input BuildPolicyHistoryInput) BuildPolicyHistoryResult {
	diagnostics := validatePolicyHistoryRecords(input.Records)
	if len(diagnostics) > 0 {
		return BuildPolicyHistoryResult{OK: false, Diagnostics: diagnostics}
	}
	if len(input.Records) == 0 {
		// Unreachable through the validator (it rejects an empty chain);
		// fail closed rather than producing a versionless history.
		diagnostics = append(diagnostics, PolicyDiagnostic{
			Kind:    PolicyDiagnosticKindRecordInvalid,
			Path:    "records",
			Message: "policy history must contain at least one version record",
		})
		return BuildPolicyHistoryResult{OK: false, Diagnostics: diagnostics}
	}
	storageDir := input.StorageDir
	if storageDir == "" {
		storageDir = PolicyHistoryDefaultDir
	}
	history := PolicyHistory{
		StorageDir:    storageDir,
		Records:       input.Records,
		ActiveVersion: input.Records[len(input.Records)-1].PolicyVersion,
	}
	return BuildPolicyHistoryResult{OK: true, History: &history}
}

// validatePolicyHistoryRecords ports validatePolicyHistoryRecords: record
// shape, unique strictly increasing versions, and the previous-pointer
// binding (`policy-history/v<N-1>.yml`).
func validatePolicyHistoryRecords(records []PolicyVersionRecord) []PolicyDiagnostic {
	diagnostics := make([]PolicyDiagnostic, 0)
	if len(records) == 0 {
		return append(diagnostics, PolicyDiagnostic{
			Kind:    PolicyDiagnosticKindRecordInvalid,
			Path:    "records",
			Message: "policy history must contain at least one version record (bootstrap v1 first — ARCH §8.3.1/§15.1)",
		})
	}

	seenVersions := make(map[int]struct{}, len(records))
	for index := range records {
		record := records[index]
		diagnosticBase := fmt.Sprintf("records.%d", index)
		if record.PolicyVersion <= 0 {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind:    PolicyDiagnosticKindRecordInvalid,
				Path:    diagnosticBase + ".policy_version",
				Message: fmt.Sprintf("policy_version must be a positive integer (got %d)", record.PolicyVersion),
			})
		} else {
			// Chain checks (the TS wrapped these in try/catch around
			// policyHistoryPath's RangeError).
			if index == 0 && record.Previous != nil {
				diagnostics = append(diagnostics, PolicyDiagnostic{
					Kind:    PolicyDiagnosticKindPreviousPointerViolation,
					Path:    diagnosticBase + ".previous",
					Message: fmt.Sprintf("the first policy-history record must have previous: null (got %s)", strconv.Quote(*record.Previous)),
				})
			}
			if _, duplicate := seenVersions[record.PolicyVersion]; duplicate {
				diagnostics = append(diagnostics, PolicyDiagnostic{
					Kind:    PolicyDiagnosticKindDuplicateVersion,
					Path:    diagnosticBase + ".policy_version",
					Message: fmt.Sprintf("duplicate policy_version %d — versions must be unique", record.PolicyVersion),
				})
				continue
			}
			seenVersions[record.PolicyVersion] = struct{}{}
			if index > 0 {
				prior := records[index-1]
				if record.PolicyVersion < prior.PolicyVersion {
					diagnostics = append(diagnostics, PolicyDiagnostic{
						Kind:    PolicyDiagnosticKindVersionOrderViolation,
						Path:    diagnosticBase + ".policy_version",
						Message: fmt.Sprintf("policy_version %d is lower than the prior record's %d — history must be strictly increasing", record.PolicyVersion, prior.PolicyVersion),
					})
					continue
				}
				expected, errMsg := policyHistoryPath(prior.PolicyVersion, PolicyHistoryDefaultExt)
				if errMsg != "" {
					// The TS RangeError surfaced here as a record_invalid
					// diagnostic with the throw's message.
					diagnostics = append(diagnostics, PolicyDiagnostic{
						Kind:    PolicyDiagnosticKindRecordInvalid,
						Path:    diagnosticBase,
						Message: errMsg,
					})
				} else if record.Previous == nil || *record.Previous != expected {
					got := "null"
					if record.Previous != nil {
						got = strconv.Quote(*record.Previous)
					}
					diagnostics = append(diagnostics, PolicyDiagnostic{
						Kind:    PolicyDiagnosticKindPreviousPointerViolation,
						Path:    diagnosticBase + ".previous",
						Message: fmt.Sprintf("previous must point at the prior version's history record '%s' (got %s)", expected, got),
					})
				}
			}
		}

		for _, field := range []struct {
			name  string
			value string
		}{
			{"created_by", record.CreatedBy},
			{"reason", record.Reason},
			{"created_at", record.CreatedAt},
			{"policyHash", record.PolicyHash},
		} {
			if len(field.value) == 0 {
				diagnostics = append(diagnostics, PolicyDiagnostic{
					Kind:    PolicyDiagnosticKindRecordInvalid,
					Path:    diagnosticBase + "." + field.name,
					Message: fmt.Sprintf("%s must be a non-empty string", field.name),
				})
			}
		}
		// TS: previous must be null or a non-empty string. A typed *string is
		// always a string, so the only representable failure is empty.
		if record.Previous != nil && len(*record.Previous) == 0 {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind:    PolicyDiagnosticKindRecordInvalid,
				Path:    diagnosticBase + ".previous",
				Message: "previous must be null or a non-empty pointer string",
			})
		}
		if record.Evaluation != nil {
			if len(record.Evaluation.Split) == 0 {
				diagnostics = append(diagnostics, PolicyDiagnostic{
					Kind:    PolicyDiagnosticKindRecordInvalid,
					Path:    diagnosticBase + ".evaluation.split",
					Message: "evaluation.split must be a non-empty string",
				})
			}
			for metric, value := range record.Evaluation.Metrics {
				if isInfiniteMetric(value) {
					diagnostics = append(diagnostics, PolicyDiagnostic{
						Kind:    PolicyDiagnosticKindRecordInvalid,
						Path:    diagnosticBase + ".evaluation.metrics." + metric,
						Message: "evaluation metrics must be finite numbers",
					})
				}
			}
		}
		// TS: `typeof record.unsafe_auto_updates?.enabled !== 'boolean'` — the
		// Go record field is a typed *UnsafeAutoUpdates, so an enabled flag is
		// always a boolean; the runtime check is unnecessary here.
	}
	return diagnostics
}

// isInfiniteMetric applies the TS Number.isFinite check over the decoded
// metric (JSON cannot carry ±Inf, but a YAML `.inf` can).
func isInfiniteMetric(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

// RollbackTo performs rollback-as-metadata (rollbackTo; ARCH §8.5): validate
// the chain, then copy an older version FORWARD under a fresh record. It never
// deletes or rewrites history. The returned pointer record targets the
// restored version's own history record.
func RollbackTo(records []PolicyVersionRecord, version int) RollbackToResult {
	history := BuildPolicyHistory(BuildPolicyHistoryInput{Records: records})
	if !history.OK {
		return RollbackToResult{OK: false, Diagnostics: history.Diagnostics}
	}

	targetIndex := -1
	for index := range records {
		if records[index].PolicyVersion == version {
			targetIndex = index
			break
		}
	}
	if targetIndex == -1 {
		return RollbackToResult{
			OK: false,
			Diagnostics: []PolicyDiagnostic{{
				Kind:    PolicyDiagnosticKindVersionNotFound,
				Path:    "version",
				Message: fmt.Sprintf("cannot roll back to policy version %d: absent from policy history (active version %d)", version, history.History.ActiveVersion),
			}},
		}
	}
	target := records[targetIndex]

	pointerPath, err := PolicyHistoryPath(version)
	if err != nil {
		var coded *docutil.Error
		message := err.Error()
		if errors.As(err, &coded) {
			message = coded.Msg
		}
		return RollbackToResult{
			OK: false,
			Diagnostics: []PolicyDiagnostic{{
				Kind:    PolicyDiagnosticKindSchemaViolation,
				Path:    "version",
				Message: message,
			}},
		}
	}

	pointerRecord := schemas.PolicyPointer{
		Schema:  PolicyPointerSchemaID,
		Version: version,
		Pointer: pointerPath,
		Hash:    target.PolicyHash,
	}
	// Defensive round-trip: the pointer record MUST satisfy the registered
	// `docsync.policy/v1` schema before it is handed back (R4).
	wire, err := json.Marshal(pointerRecord)
	if err != nil {
		return RollbackToResult{
			OK: false,
			Diagnostics: []PolicyDiagnostic{{
				Kind:    PolicyDiagnosticKindSchemaViolation,
				Path:    "version",
				Message: "rollback pointer record is not JSON-serializable: " + err.Error(),
			}},
		}
	}
	parsed, err := schemas.ParsePolicyPointer(wire)
	if err != nil {
		return RollbackToResult{
			OK: false,
			Diagnostics: []PolicyDiagnostic{{
				Kind:    PolicyDiagnosticKindSchemaViolation,
				Path:    "version",
				Message: "rollback pointer payload does not satisfy the registered docsync.policy/v1 schema: " + formatIssueList(err),
			}},
		}
	}
	return RollbackToResult{OK: true, ActiveVersion: version, PointerRecord: parsed}
}

// UnsafeAutoUpdatesGate ports unsafeAutoUpdatesGate: the §8.4 promotion gate —
// count the versions that recorded an unsafe auto update (an explicit
// `enabled: true`, or a non-zero `evaluation.metrics.unsafe_auto_updates`).
func UnsafeAutoUpdatesGate(records []PolicyVersionRecord) UnsafeAutoUpdatesGateResult {
	count := 0
	for index := range records {
		record := records[index]
		if (record.UnsafeAutoUpdates != nil && record.UnsafeAutoUpdates.Enabled) ||
			(record.Evaluation != nil && record.Evaluation.Metrics["unsafe_auto_updates"] > 0) {
			count++
		}
	}
	return UnsafeAutoUpdatesGateResult{
		UnsafeAutoUpdates: count,
		PromotionAllowed:  count == 0,
	}
}
