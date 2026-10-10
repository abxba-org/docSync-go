// Ported from packages/schemas/src/v1/concepts.ts (docsync.concepts/v1;
// P1-T09..T12; OD-R36): the canonical concepts.yml (D1 layout, optional
// file) — concept → doc membership, per-document contract declarations, and
// hand-anchored claim declarations.
//
// This file is declarative and reviewable: automation may PROPOSE edits to
// concepts.yml, never silently rewrite it (ARCH §4.2). Semantic cross-reference
// validation lives in core's graph builder — shape is validated here.
//
// Claim anchors inside declarations carry an OPTIONAL content_hash: intake
// computes it from the span at the declared lines when absent (ARCH §4.3
// content-hash-first anchoring). Stored claims (CLAIM_SCHEMA) always carry it.
//
// Conventions: snake_case JSON/YAML keys (both tags are required — yaml.v3
// reads `yaml` tags, never `json` tags); unknown keys are rejected at parse
// (strict decode); ClaimSource and IsValidManifestPath are reused from
// state.go/manifest.go, never redefined.
package schemas

// ContractKind is one of the V1 contract kinds consumed by routing (Phase 2)
// and the verifier (Phase 4).
type ContractKind string

// ContractKindValues lists the contract kinds in canonical order.
const (
	ContractKindAPISchema ContractKind = "api_schema"
	ContractKindConfig    ContractKind = "config"
	ContractKindBehavior  ContractKind = "behavior"
	ContractKindContent   ContractKind = "content"
)

// ContractKindValues lists all contract kinds in canonical order
// (CONTRACT_KIND_VALUES).
var ContractKindValues = []ContractKind{
	ContractKindAPISchema, ContractKindConfig, ContractKindBehavior, ContractKindContent,
}

// IsValidContractKind reports membership (CONTRACT_KIND).
func IsValidContractKind(k string) bool {
	for _, v := range ContractKindValues {
		if string(v) == k {
			return true
		}
	}
	return false
}

// ConceptsClaimAnchor mirrors ClaimAnchor (state.ts CLAIM_ANCHOR_SCHEMA) with
// content_hash OPTIONAL: declarations may omit the hash and let intake
// compute it from the span at the declared lines. The line-ordering
// refinement is identical to the stored anchor.
type ConceptsClaimAnchor struct {
	File        string  `json:"file" yaml:"file"`
	StartLine   int     `json:"start_line" yaml:"start_line"`
	EndLine     int     `json:"end_line" yaml:"end_line"`
	ContentHash *string `json:"content_hash,omitempty" yaml:"content_hash,omitempty"`
}

// Validate checks the anchor, including the end_line >= start_line refinement
// (CONCEPTS_CLAIM_ANCHOR_SCHEMA.superRefine).
func (a *ConceptsClaimAnchor) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(a.File, 1, []string{"file"}))
	if a.ContentHash != nil {
		Collect(&issues, RequiredString(*a.ContentHash, 1, []string{"content_hash"}))
	}
	if a.StartLine < 1 {
		Collect(&issues, &Issue{Path: []string{"start_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.EndLine < 1 {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.EndLine < a.StartLine {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "end_line must be greater than or equal to start_line"})
	}
	return issues
}

// ConceptsClaimDeclaration is one hand-anchored claim declaration inside a
// concepts doc entry (CONCEPTS_CLAIM_DECLARATION_SCHEMA).
type ConceptsClaimDeclaration struct {
	ID      string              `json:"id" yaml:"id"`
	Text    string              `json:"text" yaml:"text"`
	Anchor  ConceptsClaimAnchor `json:"anchor" yaml:"anchor"`
	Source  ClaimSource         `json:"source" yaml:"source"`
	Concept *string             `json:"concept,omitempty" yaml:"concept,omitempty"`
}

// Validate checks the claim declaration.
func (d *ConceptsClaimDeclaration) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.ID, 1, []string{"id"}))
	Collect(&issues, RequiredString(d.Text, 1, []string{"text"}))
	for _, iss := range d.Anchor.Validate() {
		iss.Path = append([]string{"anchor"}, iss.Path...)
		issues = append(issues, iss)
	}
	if len(d.Source.File) < 1 {
		Collect(&issues, &Issue{Path: []string{"source", "file"}, Message: "String must contain at least 1 character(s)"})
	}
	if len(d.Source.BlobSHA) < 1 {
		Collect(&issues, &Issue{Path: []string{"source", "blob_sha"}, Message: "String must contain at least 1 character(s)"})
	}
	if d.Source.Line != nil && *d.Source.Line < 1 {
		Collect(&issues, &Issue{Path: []string{"source", "line"}, Message: "Number must be greater than or equal to 1"})
	}
	if d.Concept != nil {
		Collect(&issues, RequiredString(*d.Concept, 1, []string{"concept"}))
	}
	return issues
}

// ConceptsDocEntry is the per-document extension: contracts (names) plus
// hand-anchored claim declarations (CONCEPTS_DOC_ENTRY_SCHEMA).
type ConceptsDocEntry struct {
	Path      string                     `json:"path" yaml:"path"`
	Contracts []string                   `json:"contracts,omitempty" yaml:"contracts,omitempty"`
	Claims    []ConceptsClaimDeclaration `json:"claims,omitempty" yaml:"claims,omitempty"`
}

// Validate checks the doc entry against the schema (strict).
func (e *ConceptsDocEntry) Validate() []Issue {
	var issues []Issue
	if !IsValidManifestPath(e.Path) {
		Collect(&issues, &Issue{Path: []string{"path"}, Message: manifestPathMessage})
	}
	for i := range e.Contracts {
		Collect(&issues, RequiredString(e.Contracts[i], 1, []string{"contracts", itoa(i)}))
	}
	for i := range e.Claims {
		for _, iss := range e.Claims[i].Validate() {
			iss.Path = append([]string{"claims", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ConceptEntry is concept membership: a named concept spanning one or more
// manifest documents (CONCEPT_ENTRY_SCHEMA).
type ConceptEntry struct {
	Name string   `json:"name" yaml:"name"`
	Docs []string `json:"docs" yaml:"docs"`
}

// Validate checks the concept entry.
func (e *ConceptEntry) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.Name, 1, []string{"name"}))
	if len(e.Docs) < 1 {
		Collect(&issues, &Issue{Path: []string{"docs"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range e.Docs {
		Collect(&issues, RequiredString(e.Docs[i], 1, []string{"docs", itoa(i)}))
	}
	return issues
}

// ConceptsConfig is the root concepts configuration. docs maps (a subset of)
// manifest documents to contracts and claims; concepts declares concept →
// doc membership; contractKinds optionally pins a kind per contract name
// (unknown names classify as 'content' downstream, not here).
type ConceptsConfig struct {
	Schema        string                  `json:"schema" yaml:"schema"`
	Docs          []ConceptsDocEntry      `json:"docs" yaml:"docs"`
	Concepts      []ConceptEntry          `json:"concepts,omitempty" yaml:"concepts,omitempty"`
	ContractKinds map[string]ContractKind `json:"contractKinds,omitempty" yaml:"contractKinds,omitempty"`
}

// conceptsAllowedRootKeys is the strict root key set (CONCEPTS_SCHEMA).
var conceptsAllowedRootKeys = []string{"schema", "docs", "concepts", "contractKinds"}

// Validate checks the root concepts config against the schema (strict).
func (c *ConceptsConfig) Validate() []Issue {
	var issues []Issue
	if c.Schema != "docsync.concepts/v1" {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"docsync.concepts/v1\""})
	}
	for i := range c.Docs {
		for _, iss := range c.Docs[i].Validate() {
			iss.Path = append([]string{"docs", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range c.Concepts {
		for _, iss := range c.Concepts[i].Validate() {
			iss.Path = append([]string{"concepts", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for name, kind := range c.ContractKinds {
		if !IsValidContractKind(string(kind)) {
			Collect(&issues, &Issue{Path: []string{"contractKinds", name}, Message: "Invalid enum value. Expected 'api_schema' | 'config' | 'behavior' | 'content', received '" + string(kind) + "'"})
		}
	}
	return issues
}

// ParseConcepts strictly parses and validates YAML/JSON concepts bytes.
// Unknown keys are rejected at the root (zod strictObject semantics).
func ParseConcepts(data []byte) (*ConceptsConfig, error) {
	var c ConceptsConfig
	if isJSONish(data) {
		if err := decodeJSONStrict(data, &c); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrictYAMLOrJSON(data, &c, conceptsAllowedRootKeys); err != nil {
			return nil, err
		}
	}
	if issues := c.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}

// ConceptsExample returns the canonical example config (CONCEPTS_EXAMPLE).
func ConceptsExample() ConceptsConfig {
	return ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []ConceptsDocEntry{
			{
				Path:      "docs/payments.md",
				Contracts: []string{"request_schema", "authentication"},
				Claims: []ConceptsClaimDeclaration{
					{
						ID:   "claim_182",
						Text: "Refunds require an idempotency key.",
						Anchor: ConceptsClaimAnchor{
							File:        "docs/payments.md",
							StartLine:   42,
							EndLine:     42,
							ContentHash: new("1f2e3d4c5b6a7988071627345a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7"),
						},
						Source:  ClaimSource{File: "src/payments/refund.ts", Line: new(42), BlobSHA: "e0d1fa5"},
						Concept: new("refunds"),
					},
					{
						ID:   "claim_183",
						Text: "Payment intents are created server-side only.",
						Anchor: ConceptsClaimAnchor{
							File:      "docs/payments.md",
							StartLine: 50,
							EndLine:   51,
						},
						Source: ClaimSource{File: "src/payments/intent.ts", BlobSHA: "b41c9e2"},
					},
				},
			},
			{
				Path:      "docs/architecture.md",
				Contracts: []string{"module_boundaries"},
			},
		},
		Concepts: []ConceptEntry{
			{Name: "refunds", Docs: []string{"docs/payments.md"}},
			{Name: "payments-overview", Docs: []string{"docs/payments.md", "docs/architecture.md"}},
		},
		ContractKinds: map[string]ContractKind{
			"request_schema":    ContractKindAPISchema,
			"authentication":    ContractKindBehavior,
			"module_boundaries": ContractKindContent,
		},
	}
}
