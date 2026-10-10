# docSync — Architecture

> **Status:** Decision-ready v1.0 · **Date:** 2026-10-05
> **Source of truth:** `docSync_CONTEXT.md` (all 47 sections are traced in Appendix B)
> **One change from context:** AI inference is accessed **through OpenRouter** (user decision, 2026-10-05) — see §10.

---

## Contents

- §0 Executive Summary & Resolved Decisions
- §1 Product Thesis, Positioning & Moat
- §2 Constitutional Design Principles
- §3 System Overview (Production & Ultimate Architecture)
- §4 Pipeline Components (Analyzer → Graph → Jev → Sonnet → Verification → Policy)
- §5 Policy Engine & Decisioning
- §6 Documentation Lifecycle State (Baselines, State Machine, Storage)
- §7 The Two Loops (Synchronous PR Loop, Background Drift Audit)
- §8 Policy Lifecycle & Learning (Architect, Compiler, Versioning, Canary, Memory, Feedback)
- §9 Evaluation Framework
- §10 Model Access Layer — OpenRouter
- §11 Repository Configuration & Governance (`.docsync/`)
- §12 Developer Experience & CI Integration
- §13 Orchestrator vs LLM Responsibility
- §14 Safety Invariants
- §15 Roadmap (V1 / V2 / V3)
- §16 Risks, Assumptions & Open Items
- Appendix A — Design Methodology
- Appendix B — Traceability Matrix (context §1–§47 → this document)
- Appendix C — Glossary

---

## §0 Executive Summary & Resolved Decisions

### 0.1 One-Sentence Definition (context §47)

> **A Git-native documentation consistency engine that builds a repository-specific decision policy, detects documentation drift from code changes, uses Jev for structured routing and Claude Sonnet for evidence-backed minimal patches, and continuously improves its policy through evaluated feedback and safe versioned promotion.**

docSync is a **project-scoped Documentation Consistency Engine** — not a generic AI documentation generator. It detects when code changes make documentation stale, identifies *only* the relevant documentation, proposes *minimal* documentation diffs, verifies them, and integrates with the normal Git/PR workflow. The system becomes **repository-specific over time**: each repository develops its own documentation mappings, decision policy, exceptions, thresholds, and learned patterns.

### 0.2 How to read this document

Sections §1–§3 give the product thesis and system shape. §4–§7 specify the production pipeline and its state. §8–§9 specify the learning/policy machinery. §10–§12 specify the model-access, governance, and developer-facing surfaces. §13–§14 are the non-negotiables. §15–§16 are delivery and risk. Appendices provide methodology and a completeness audit against `docSync_CONTEXT.md`.

### 0.3 Resolved design decisions (conflicts from the expert-review challenge round)

These were contested between the specialist teams; each is decided here and is binding:

| # | Decision | Rationale |
|---|---|---|
| D1 | `.docsync/` layout is reconciled: `state/` is a new top-level, system-written directory; `policy-history/` lives at top level (not inside `memory/`). `memory/` holds exactly `decisions.md`, `known-patterns.md`, `exceptions.md`, `failures.md`. | §9, §20, §25 gave three slightly different layouts; one canonical layout must win. |
| D2 | **Ownership is the floor.** Manifest modes (`locked`, `generated`, `external`, `human`) override any classifier confidence. A locked document resolves to `blocked` at any confidence; Jev cannot override ownership. | Prevents a high-confidence decision from ever violating a governance lock. |
| D3 | **Displayed confidence is pinned to the producing `(policy_version, model binding)`**; every UI surface shows the policy version next to percentages. | The same number (e.g., 97%) means different things across policy versions and model bindings; display without version pinning erodes provenance. |
| D4 | **One documentation PR per source PR**, aggregating all candidate docs, with **per-document partial-pass semantics** (a single failing doc's verification does not block the others' merge-worthiness; it is dropped from the PR and reported). | Rollback simplicity vs verification granularity — both satisfied. |
| D5 | **Provenance precedence:** PR body summary = canonical; `.docsync/state/` = archival (content-addressed, append-only); check-run metadata = ephemeral view. Markdown document bodies are never modified for provenance. | Churn/merge-conflict avoidance + auditability. |
| D6 | **Shadow/canary evaluation runs primarily offline (historical replay) plus sampled live shadow**, not dual-Jev on every PR. | Protects Loop A latency and CI budget; live shadow only for a sampled fraction. |
| D7 | **The manifest is an allowlist.** Unmapped documents are never scanned or patched; `docsync check` reports them as "unknown coverage" rather than silently ignoring them. | Enforces "do not scan every Markdown file" (principle 2) while remaining honest about coverage. |
| D8 | **Fail-closed configuration.** An invalid manifest halts before any AI stage (exit code 2 / check failure with line-precise errors). No last-known-good fallback. | A broken manifest is a broken safety contract. |
| D9 | **All AI inference goes through OpenRouter.** Jev and Sonnet are *logical roles* bound to OpenRouter model IDs via a versioned `models.yml`. Model swaps are treated like policy changes: shadow/canary before production. | Single access layer, provider fallbacks, unified cost/telemetry, pluggable routing. |
| D10 | **Check conclusion is advisory (`neutral`) by default**; `failure` only when a repository opts into enforcement (e.g., high-risk/locked docs affected, `--strict`). | Documentation checks must not break builds by default in V1. |
| D11 | Routing thresholds (e.g., `0.94` / `0.72`) are **per-policy-version, per-repository calibrated data**, never hard-coded. The confidence × risk matrix *shape* is fixed; its calibration evolves. | Thresholds reasoned, not measured, must be calibrated before trusting them. |
| D12 | **Baseline resolution order:** exact SHA → same tree hash → patch-id/merge-base match → treat as invalidated (SUSPECT). Baselines never silently survive history rewrites. | Squash merges, force-pushes, and merge queues orphan SHA-keyed state. |
| D13 | **Verification never self-approves.** The Stage-3 judge is never the writer instance; rejects fail closed to `review` with evidence attached, never silent drops. | Removes structural self-approval bias. |
| D14 | Analyzer failure ≠ "no docs affected". Inconclusive analysis routes to `review`; every failure mode degrades toward *less* automation, never more. | Fail-safe default. |

### 0.4 Non-negotiables at a glance

```text
Git owns the documentation. AI proposes changes to it.
The application controls whether AI is allowed to make those changes.
```

Full invariant list in §14.

---

## §1 Product Thesis, Positioning & Moat

### 1.1 Original product idea (context §1)

- Code lives in a Git repository; Markdown docs are maintained alongside it.
- When code changes, the system inspects the Git diff.
- It determines which documentation may now be stale.
- It does **not** scan or rewrite every `.md` file in the repository.
- The project has a mutable, repository-local manifest describing which docs are relevant, which are mutable, and which are protected.
- The system runs automatically before/around a push/PR or manually.
- It creates normal Git diffs/PRs so developers review documentation changes exactly like code changes.

Desired workflow:

```text
Code change
    ↓
Git diff
    ↓
Identify affected documentation
    ↓
Generate minimal documentation patch
    ↓
Show Git diff / create PR
    ↓
Human review
```

The preferred design is **PR-first**, with local and CI modes available (§12).

### 1.2 Ecosystem context (context §2)

The broad workflow is already becoming a product category; docSync's opportunity is the *controlled, evidence-backed, repository-specific* system, not "AI updates Markdown after a code change":

| System | Relevance |
|---|---|
| Mintlify Automations | code-change-aware documentation automation and PR workflows |
| uptodate.sh | code changes → affected docs → documentation PR workflow |
| ReadMe AI Writer | GitHub PR/code-diff-aware documentation updates |
| GitBook Git Sync | Git↔docs sync, not code-behavior-to-doc-drift detection |
| Open-source Code-to-Docs GitHub Actions | PR-diff analysis → documentation updates |
| OpenClaw/Hermes-style self-learning | persistent learnings/skills, autonomous refinement concepts |

Referenced during research:

- TypeSafe / Jev: https://typesafe.ai/blog/introducing-system-one-models-and-jev
- OpenClaw self-learning: https://docs.openclaw.ai/tools/self-learning
- GitBook Git Sync: https://www.gitbook.com/features/git-sync
- uptodate: https://www.uptodate.sh/
- Mintlify Automations: https://www.mintlify.com/blog/docs-on-autopilot
- ReadMe GitHub AI Writer: https://docs.readme.com/main/docs/github-ai-writer
- Red Hat Code-to-Docs: https://developers.redhat.com/articles/2026/04/21/ai-powered-documentation-updates-code-diff-docs-pr-one-comment
- Bernstein docs-drift playbook: https://docs.bernstein.run/en/latest/playbooks/docs-drift/

### 1.3 Core product thesis (context §3)

> Maintain a machine-verifiable relationship between software behavior and the documentation that claims to describe it.

The product answers a fixed chain of questions for any code change:

```text
What changed?
    ↓
What externally observable behavior changed?
    ↓
Which documented concepts/claims depend on it?
    ↓
Which documentation may now be stale?
    ↓
What is the safest correction?
    ↓
Can the correction be verified?
    ↓
Should it be auto-applied, proposed, or blocked?
```

Shape of the chain: questions 1–3 are **detection and attribution** (deterministic analysis + Jev); 4–5 are **investigation** (Sonnet); 6 is **verification**; 7 is **policy**. The intelligence layers exist to answer these questions — none of them exists to "write docs."

Positioning: **Documentation drift / documentation consistency engine** — not **AI documentation generator**. This is load-bearing: it dictates evaluation (correctness of updates, not volume of edits, §9), the safety model (Git owns the docs, §2), and what the LLM layers may do (minimal evidence-backed patches, never rewrites).

### 1.4 Strategic insight and moat (context §43, §44)

The most defensible part is not the LLM that writes Markdown. The differentiating infrastructure is:

- repository-specific decision policy
- code↔concept↔documentation graph
- claim-level evidence tracking
- historical drift dataset
- human feedback loop
- confidence × risk policy
- versioned Jev policies
- shadow/canary promotion
- provenance
- baseline tracking
- safe Git/PR workflow

This turns a generic LLM wrapper into a system that **gets better for each repository**.

Strongest formulation (context §44):

> **The system is a repository-specific, self-calibrating documentation policy engine.**

Automatic Markdown updates are only one output. Other outputs: documentation drift detection, stale claim detection, PR checks, policy suggestions, documentation health reports, historical audits, reviewer assistance, generated remediation PRs.

---

## §2 Constitutional Design Principles

### 2.1 Central design principle (context §4)

> **Git owns the documentation. AI proposes changes to it. The application controls whether AI is allowed to make those changes.**

Operational corollary:

> **Never ask a generative model to rediscover a fact that the system can represent explicitly.**

The corollary is a cost, latency, *and* reliability rule. Generative rediscovery is slow, non-deterministic, and unauditable; explicit representation is cheap, deterministic, and versionable. Division of knowledge:

- **Git** already knows what changed.
- **Deterministic analysis** knows paths/symbols/schemas/config changes.
- **The manifest** knows which documentation is allowed to be touched.
- **The document graph** knows relationships between code and documentation.
- **Jev** decides what matters / how to route the case.
- **Claude Sonnet** investigates difficult cases and creates the minimal patch.
- **Verification** checks the patch.
- **Git** remains the final source of truth.

In practice: every input to Jev and Sonnet must be traceable to an explicit representation (a diff hunk, a manifest entry, a graph edge, a policy clause). If a stage needs a fact no explicit representation holds, the fix is to *add the representation* (mapping, claim, contract) — not to prompt harder. Reviewers should treat "the model figured it out itself" as a pipeline defect, not a feature.

### 2.2 Tripartite responsibility split (context §5)

> **Jev decides whether/where/how risky. Sonnet determines what the correct documentation patch should be. The application decides whether the patch is permitted.**

No layer may assume another's role: Jev never writes prose; Sonnet never grants itself permission; the application never invents content. This is what makes each layer independently replaceable (§10) and independently evaluable (§9).

### 2.3 Final design principles (context §46, verbatim)

1. **Project-scoped by default.**
2. **Do not scan every Markdown file on every change.**
3. **Use deterministic evidence before AI.**
4. **Use Jev for structured decisioning, not prose generation.**
5. **Use Sonnet for difficult investigation and patch generation.**
6. **Treat documentation as claims backed by sources.**
7. **Maintain explicit code↔concept↔documentation relationships.**
8. **Allow per-document mutability/ownership modes.**
9. **Never give the model unrestricted write authority.**
10. **Generate minimal diffs rather than whole-document rewrites.**
11. **Use confidence × risk instead of confidence alone.**
12. **Provide an explicit abstain/review path.**
13. **Version the decision policy.**
14. **Let Sonnet propose policy improvements, not directly mutate production policy.**
15. **Evaluate candidate policy changes offline.**
16. **Use shadow/canary policies before promotion.**
17. **Learn from reviewer corrections.**
18. **Persist repository-specific memory, but make it evidence-backed.**
19. **Keep the production orchestrator mostly deterministic.**
20. **Optimize for documentation correctness, not the number of generated edits.**

---

## §3 System Overview

### 3.1 Two planes of operation (context §45)

- **Control plane (slow):** repository profiling → policy synthesis (Policy Architect) → policy compilation → offline evaluation → versioned promotion/canary of Jev policies.
- **Data plane (fast, per-PR):** deterministic change analysis → Jev decision → retrieval → Sonnet patch → verification → policy engine → Git diff/PR.

The PRODUCTION line between the planes is the safety boundary: **nothing crosses it without passing EVALUATION** (context §19/§21). This is what makes autonomous improvement possible **without autonomous degradation**.

### 3.2 High-level production architecture (context §7)

```text
                         ┌──────────────────────┐
                         │       Git PR         │
                         │  commit / branch     │
                         └──────────┬───────────┘
                                    │
                                    ▼
                         ┌──────────────────────┐
                         │   CHANGE ANALYZER    │
                         │                      │
                         │ git diff             │
                         │ AST / symbols        │
                         │ API changes          │
                         │ dependency changes   │
                         │ tests / config       │
                         └──────────┬───────────┘
                                    │
                                    ▼
                    ┌────────────────────────────────┐
                    │       DOCUMENT GRAPH            │
                    │                                 │
                    │ code ── concepts ── documents  │
                    │       └── APIs                  │
                    │       └── configs               │
                    │       └── workflows             │
                    └───────────────┬────────────────┘
                                    │
                                    ▼
                         ┌──────────────────────┐
                         │         JEV          │
                         │                      │
                         │ classify             │
                         │ score                │
                         │ route                │
                         │ confidence           │
                         └──────────┬───────────┘
                                    │
                  ┌─────────────────┼─────────────────┐
                  │                 │                 │
                  ▼                 ▼                 ▼
               IGNORE            REVIEW           CONTINUE
                                                       │
                                                       ▼
                                            ┌──────────────────┐
                                            │ RETRIEVAL ENGINE │
                                            │                  │
                                            │ exact mappings   │
                                            │ symbols          │
                                            │ semantic search  │
                                            │ history          │
                                            └────────┬─────────┘
                                                     │
                                                     ▼
                                            ┌──────────────────┐
                                            │  SONNET AGENT    │
                                            │                  │
                                            │ investigate      │
                                            │ reason           │
                                            │ produce patch    │
                                            └────────┬─────────┘
                                                     │
                                                     ▼
                                            ┌──────────────────┐
                                            │  VERIFICATION    │
                                            │                  │
                                            │ factual claims   │
                                            │ source evidence  │
                                            │ links            │
                                            │ examples         │
                                            │ schema           │
                                            └────────┬─────────┘
                                                     │
                                                     ▼
                                            ┌──────────────────┐
                                            │ POLICY ENGINE    │
                                            │                  │
                                            │ auto             │
                                            │ suggest          │
                                            │ human review     │
                                            │ blocked          │
                                            └────────┬─────────┘
                                                     │
                                                     ▼
                                                Git diff / PR
```

### 3.3 Stage responsibilities and inter-stage contracts

Each stage is a pure-ish function over typed inputs; the orchestrator owns sequencing, retries, and validation (§13).

| Edge | Contract | Notes |
|---|---|---|
| CHANGE ANALYZER → DOCUMENT GRAPH | `ChangeAnalysis` record, keyed by `(repo, base_sha, head_sha)`; fully recomputable from Git | no side-channel state required to reproduce (§4.1) |
| DOCUMENT GRAPH → JEV | `RelevanceContext`: candidate docs/concepts/contracts touched by the change, each annotated with *which graph edge or mapping produced it* | Jev never sees unbounded repo content |
| JEV → orchestrator | `JevDecision`: typed decision per the repo's compiled policy schema + scores/confidence + routing (`IGNORE \| REVIEW \| CONTINUE`) | decision object on the wire: §5.4 |
| orchestrator → RETRIEVAL ENGINE (only on CONTINUE) | `ChangeAnalysis` + `JevDecision` in; bounded, ranked candidate set out | provenance per candidate (retrieval level, score) |
| RETRIEVAL ENGINE → SONNET AGENT | constrained workspace: diff + structured change analysis + Jev decision + manifest/policy + relevant source files + relevant documents + project instructions | **not** the whole repository by default (§4.7) |
| SONNET AGENT → VERIFICATION | `DocPatch`: minimal structured patch (target doc, anchor, hunk) + per-hunk evidence references | never a whole-document rewrite (§4.9) |
| VERIFICATION → POLICY ENGINE | `VerificationReport`: per-claim status + evidence check results | three tiers, §4.8 |
| POLICY ENGINE → Git | final outcome `auto \| suggest \| human review \| blocked` | explicit abstention; uncertain → review (§5) |

### 3.4 Failure and degradation defaults

- Analyzer failure → check fails **inconclusive**, never "no docs affected" by default (D14).
- Jev unavailability / schema-violating output → bounded retries, then route to **REVIEW** (abstain), never auto.
- Sonnet timeout → emit a "potentially stale" finding without patch.
- Verification failure → patch rejected; proposal surfaced for human review with evidence attached.
- Model/provider outage (OpenRouter) → fail toward advisory/neutral check or REVIEW; never fail-closed blocking merges on infra failure unless policy explicitly opts in.
- **Every failure degrades toward less automation, not more** (D14).

### 3.5 Idempotency

A full pipeline run is keyed by `(repo, base_sha, head_sha, manifest_version, policy_version)`. Re-running the same key produces the same decision trail: deterministic stages are pure; LLM stage outputs are cached by input hash + policy version; Git side effects are deduplicated (§12.4).

### 3.6 Preferred ultimate architecture (context §45)

```text
                         REPOSITORY
                              │
                              ▼
                    ┌──────────────────┐
                    │ Repository       │
                    │ Profiler         │
                    └────────┬─────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │ Policy Architect │
                    │     SONNET      │
                    └────────┬─────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │ Policy Compiler  │
                    └────────┬─────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │ Jev Policy vN    │
                    └────────┬─────────┘
                             │
                     ┌───────▼───────┐
                     │  EVALUATION   │
                     └───────┬───────┘
                             │
                       promote/canary
                             │
                             ▼
────────────────────────────────────────────────────
                      PRODUCTION
────────────────────────────────────────────────────
                             │
                          Git diff
                             │
                             ▼
                       Deterministic
                       change analysis
                             │
                             ▼
                          Jev vN
                             │
                             ▼
                     document candidates
                             │
                             ▼
                         Sonnet
                   documentation agent
                             │
                             ▼
                         verifier
                             │
                             ▼
                       policy engine
                             │
                             ▼
                        Git diff / PR
                             │
                             ▼
                       human feedback
                             │
                             ▼
                    evaluation dataset
                             │
                             ▼
                    policy refinement
```

Reading of the diagram:

- **Control plane (top):** Repository → Repository Profiler → Policy Architect (Sonnet) → Policy Compiler → typed `Jev Policy vN` → **EVALUATION** → only then `promote/canary` across the PRODUCTION boundary. Sonnet never mutates live policy: it proposes; the compiler produces a versioned artifact; evaluation (offline replay + shadow/canary) decides; promotion is an application-owned, versioned, rollback-able act — **software deployment semantics for policy** (§8).
- **Data plane (below the line):** production runs a specific, pinned policy version — never a mutable prompt.
- **Closing loop:** human feedback → evaluation dataset → policy refinement feeds the next control-plane iteration (§8.7, §9).

### 3.7 Scalability boundaries

- **Monorepo-scale diffs** may touch 10⁵ files. Deep (AST/API/schema) analysis applies only to files matched by manifest mappings or the symbol index; beyond configured caps (file count / diff bytes) the run downgrades to path-level analysis, marks `deep_analysis: skipped`, and Jev routes to **REVIEW** rather than guessing.
- **Symbol/AST work** is per-language pluggable (tree-sitter / compiler APIs), symbol index cached per blob SHA, built incrementally per changed file. Languages without tooling degrade to path/dependency-level analysis — *declared explicitly in the run record*, never silently.
- **Document graph** is plain files under `.docsync/` (Git-native, reviewable, no graph-DB dependency); reverse indexes built per run from changed-file sets, not full-graph traversals.
- **Semantic index** lazily rebuilt per shard; staleness tolerated because embeddings are fallback recall only (§4.5).
- **CI budget:** Loop A is bounded per run (max docs investigated, max patches); overflow routes to REVIEW with a "partial coverage" marker, never silently dropped. Loop B (§7.2) is the pressure valve for what Loop A defers.

---

## §4 Pipeline Components

### 4.1 Deterministic Change Analyzer (context §8)

Architectural rule: **do not make AI solve problems that deterministic software can solve.** Before Jev, the analyzer computes, from Git alone:

```text
- git diff
- file paths
- path mappings
- AST parsing where useful
- symbol changes
- dependency changes
- OpenAPI/schema diffs
- configuration diffs
- generated-file detection
- test-only detection
- formatting-only detection
- explicit source→doc mappings
```

`ChangeAnalysis` contract (deterministic, cacheable by blob SHAs):

```yaml
run_id: <repo>#<base_sha>..<head_sha>
files:
  - path: src/payments/refund.ts
    status: modified
    generated: false
    test_only: false
    formatting_only: false
    symbols_changed:
      - refundRequest(idempotencyKey: string -> string?)
    api_diff: <openapi/schema delta, if any>
    config_diff: <semantic config keys changed, if any>
dependencies_changed: [...]
classification_hints:
  - test_only            # short-circuits: skip Jev entirely
  - formatting_only      # short-circuits: skip Jev entirely
  - generated            # route per policy (e.g., inspect generated output)
```

Notes:

- `test_only` and `formatting_only` detection runs *before* Jev; a repo-wide formatting sweep must not consume decision-model budget or generate doc churn.
- Generated-file detection is heuristic and learned over time (memory pattern `generated-sdk`, §8.8: "changes under `src/api/generated` may alter public documentation indirectly" → action `inspect_generated_output`). Mispredictions feed closed-loop learning (§8.7).
- **Claim-level invalidation requires type-aware diffs.** The target case `idempotencyKey: string → string?` is a type-level change; without AST support the invalidation is impossible and must be declared as a coverage gap in the run record — never silently skipped.
- Analyzer failure is inconclusive, not negative (D14).

### 4.2 Document graph — bidirectional code ↔ concepts ↔ docs (context §10)

The system maintains the tripartite relation, not a one-way similarity lookup:

```text
code ↔ concepts ↔ docs
```

Canonical mapping record (repository-versioned in `.docsync/`):

```yaml
doc: docs/payments.md

sources:
  - src/payments/refund.ts
  - src/payments/types.ts

concepts:
  - refunds
  - idempotency

contracts:
  - refund_request
  - refund_response
```

Responsibilities and invariants:

- Nodes: code symbols, concepts, documents, APIs, configs, workflows. Edges derive from these mappings plus claim references.
- Must support **reverse lookup** (code change → affected docs) *and* **forward lookup** (doc → supporting sources); this makes baselines, claims, and Loop B possible.
- Mappings are declarative and reviewable in PRs; automation may *propose* mapping edits (via policy refinement), never silently rewrite them.
- A doc with no mapping is invisible to the pipeline (a feature — no blanket scanning), but `docsync check` reports unmapped-but-changed docs as **"unknown coverage"** (D7).
- Deleted/renamed sources leave dangling edges: the reconciler marks them `orphaned` and re-routes affected docs to SUSPECT rather than silently dropping coverage.

### 4.3 Documentation as claims (context §11)

The unit of verification is the **claim** — an atomic statement in documentation backed (or not) by the source of truth.

Example Markdown:

```markdown
Refunds require an idempotency key.
```

Internal representation:

```text
Claim #182

text:
Refunds require an idempotency key.

source:
src/payments/refund.ts:42

concept:
refunds

status:
verified
```

A code change can then *directly invalidate* a claim:

```diff
- idempotencyKey: string
+ idempotencyKey?: string
```

Claim #182's support at `src/payments/refund.ts:42` is gone → status flips `verified → suspect/stale`, and the containing document enters the state machine (§6.2). This is much stronger than asking an LLM whether a Markdown file "looks related" to a diff. The system's headline query:

> **Which documented claims lost support in the source of truth?**

Contracts and rules:

- Claims are stored in `.docsync/state/claims/` with `text`, `anchor` (file + line range + **content hash of the quoted span**), `source` (code file:line + blob SHA), `concept`, `status`, and history.
- **Anchoring is content-hash-first, line-number-second.** Humans edit docs between runs; ambiguous re-anchoring marks the claim `UNANCHORED` and routes to REVIEW — never silently re-anchors.
- V1 fallback: manifest-declared / hand-anchored claims. Automated extraction (V2) output is a *proposal*; it becomes truth only after verification against source evidence.
- Prose-heavy docs may resist anchoring: fall back to section-level checking where claim extraction is weak (§16).

### 4.4 Documentation contracts (context §12)

Important documents declare what they promise:

```yaml
doc: docs/api.md

contracts:
  - public_endpoints
  - request_schema
  - response_schema
  - authentication
  - error_codes
```

A change is classified against each contract:

```text
public_endpoints:   false
request_schema:     true  (0.98)
response_schema:    false
authentication:     false
error_codes:        false
```

Semantics: a contract is a named, checkable expectation over the doc's content (endpoints listed, schema fields, auth behavior, error codes). The per-contract classification vector is consumed by Jev routing and by the verifier (a proposed patch must not silently violate an unmarked contract). Contract hits at confidence ≥ policy thresholds drive retrieval and patch scoping — in the example, only `request_schema` work is in scope. This gives Sonnet more precise context and makes the system more explainable.

Rules: contract definitions are themselves versioned policy data in `.docsync/`; classification below the review threshold abstains to REVIEW; a doc with declared contracts but zero checkable claims is flagged as a coverage defect in `docsync check`.

### 4.5 Retrieval engine and hierarchy (context §8)

Strictly ordered retrieval hierarchy — each level tried before the next, results carrying their level as provenance:

```text
1. Explicit manifest
2. Path mapping
3. Symbol mapping
4. API/schema mapping
5. Concept/document graph
6. Semantic retrieval
```

**Embeddings/vector search are a fallback recall mechanism only — never the source of truth.** The vector index may be stale, lossy, or absent; correctness must never depend on it. Every semantic-retrieval candidate must be traceable to a deterministic confirmation (manifest entry, mapping, claim, or contract) before it can drive an automatic action; **semantic-only candidates route to REVIEW at best.** Candidate counts per level are capped; semantic candidates without deterministic corroboration are a known false-positive source (§16).

### 4.6 Jev — typed decision layer (context §5)

Jev is **not** the writing model. It is the typed decision/classification/routing layer. The system hands Jev structured state (diff-derived change representation, manifest context, document-graph candidates) and asks structured questions:

- Did externally observable behavior change?
- Did a public API contract change?
- Did configuration semantics change?
- Is documentation likely affected?
- Which candidate docs are relevant?
- What is the risk level?
- Should this be ignored, reviewed, or sent to Sonnet?
- What is the confidence/probability of the decision?

Jev returns **structured answers/probabilities**; the application — not the model — uses them for branching. Every question is typed (boolean, choice, score) and every answer carries a probability, so downstream policy reasons about uncertainty instead of pretending decisions are certain.

Routing outcome: `IGNORE | REVIEW | CONTINUE` (three-way, per the production diagram).

### 4.7 Sonnet — investigation and patch layer (context §5)

Sonnet receives a **constrained workspace**, not the whole repository by default:

```text
Git diff
+
structured change analysis
+
Jev decision
+
repository manifest / policy
+
relevant source files
+
relevant documents
+
project instructions
```

Within that workspace Sonnet has exactly five duties:

1. Verify whether the docs are actually stale.
2. Inspect the exact affected section.
3. Determine the minimum correct change.
4. Produce a patch rather than rewrite the entire document.
5. Explain the evidence behind the patch.

Duty 5 is mandatory output, not decoration: the evidence explanation is what the verifier (§4.8) and the human reviewer consume, and what makes a patch auditable after the fact.

### 4.8 Verification (context §6 Stage 3, §30; context §5 Sonnet duties)

Verification is a pipeline of three tiers; a patch must clear all three. **Every check emits a record into the patch's verification/provenance object.**

**Tier 1 — deterministic validators (always run):**

- patch applies cleanly to current worktree/head;
- only manifest-permitted files/sections touched; locked/generated/external documents untouched (D2);
- minimality bound respected (patch size proportional to the triggering diff; **whole-document rewrites rejected outright**, §4.9);
- structural integrity: Markdown fences/YAML/code blocks still parse; list/heading structure not silently mangled;
- links resolve (internal anchors exist; external URLs reachable or explicitly allowlisted);
- documented examples still parse (and, where tooling exists, type-check or lint).

**Tier 2 — evidence-backed claim checking (the core):** the patched region is decomposed into claims ("Refunds require an idempotency key", "the endpoint returns `429` on rate limit", "the flag defaults to `false`"). Each claim must be bound to a source anchor available in the constrained workspace: `file:line` span, symbol signature, OpenAPI path/operation, config key with resolved value, or test name. The verifier checks each claim against the source of truth at the PR head:

- signatures/field names/types match the symbol or schema;
- values, defaults, enums, error codes match;
- behavioral predicates (ordering, idempotency, retry semantics) match documented contract clauses;
- claims the patch *removed* are checked for whether their supporting source still exists (**over-deletion guard**);
- claims with **no resolvable anchor** are flagged `unanchored` — an unanchored claim may pass to `suggest` but **never** to `auto`.

**Tier 3 — second decision pass (independent judge):** pre-doc, post-doc, and triggering diff re-scored by Jev (or the routed judge model, §10) on a typed question set: *stale_before? / resolved_after? / minimal? / introduced_new_inaccuracy?* The judge is deliberately **not** the writer instance (D13). Output: `pass | review | reject`. Rejects fail closed to review with evidence attached — never silently discarded.

### 4.9 Patch strategy (context §29)

Sonnet produces a **minimal patch**, not an entire rewritten Markdown document:

```text
Sonnet
 ↓
structured patch
 ↓
apply patch
 ↓
git diff
```

Explicitly avoided:

```text
Sonnet
 ↓
regenerate entire docs/payments.md
```

Why: (1) minimal patches **limit accidental change** — no silent reformatting, tone drift, or collateral edits a rewrite inevitably introduces; (2) they make **code-review-style inspection natural** — the reviewer sees a diff of exactly the claims that changed, sized against the triggering code change. Structured patches (anchored replacements, not whole-file text) are what the verifier's minimality bound and claim-checking operate on; **whole-document rewrites are rejected at verification regardless of content quality** (§4.8 Tier-1 minimality bound; invariant 12). Patch application races: if the doc changed between Sonnet's read and patch application, a failed 3-way merge **abstains to REVIEW**, never force-applies.

---

## §5 Policy Engine & Decisioning

### 5.1 Three-layer intelligence pipeline (context §6)

The system uses at least three intelligence stages and **must NOT rely on one generative model to make every decision**:

```text
Stage 1 — Decision:        Change representation → Jev → structured decision
Stage 2 — Investigation:   Relevant evidence    → Sonnet → minimal documentation patch
Stage 3 — Verification:    Proposed patch       → validation / second decision pass → pass/review/reject
```

Rationale: a single model that decides, writes, and checks its own work has structural self-approval bias, no calibrated uncertainty, and no cheap failure mode. Splitting the layers means Stage-1 failures are cheap (misroute caught later), Stage-2 failures are contained (bad patch rejected at Stage 3), and Stage-3 failures are the safety net (fail closed to review, never to auto-apply).

### 5.2 Policy states and outcomes (context §13)

Four broad outcomes:

```text
IGNORE
SUGGEST
AUTO
LOCKED/BLOCKED
```

`review` is additionally used as a **workflow state** (something a human is expected to act on), distinct from the four outcome labels. In practice the policy engine maps to:

- ignore
- human review
- auto update
- blocked

Two hard rules:

1. **Do not force uncertain cases into update/ignore.**
2. The system needs an explicit **abstain / don't know** path.

**Abstention is a first-class, named outcome, never a silent fall-through:**

```json
{
  "action": "review",
  "abstain": true,
  "abstain_reason": "calibration_floor",
  "abstain_detail": "documentation_impact probability 0.83 falls between auto floor (0.94) and review threshold (0.72) under policy v17"
}
```

Enumerated `abstain_reason` values: `insufficient_confidence`, `conflicting_evidence` (question answers disagree with deterministic analysis), `policy_gap` (no question/rule covers this change kind), `calibration_floor` (probability in the policy's no-man's zone), `verifier_uncertain` (Stage-3 judge returned `review`). Every abstained case (a) lands in the human-review queue with evidence attached, and (b) becomes a labeled data point once resolved (§8.7). Abstain **rate** is a monitored metric per policy version: too high signals a policy gap; too low combined with high human-override rate signals overconfidence.

### 5.3 Confidence × risk (context §14)

A single global confidence threshold is insufficient. Decisions are made on **confidence × risk**:

```text
                 CONFIDENCE
                 low     medium     high
              ┌────────┬──────────┬──────────┐
low risk      │ review │ suggest  │ auto     │
              ├────────┼──────────┼──────────┤
medium risk   │ block  │ review   │ suggest  │
              ├────────┼──────────┼──────────┤
high risk     │ block  │ block    │ review   │
              └────────┴──────────┴──────────┘
```

Read structurally: automation (`auto`) exists in exactly one cell; the diagonal is `review`; anything left of the diagonal is human-gated or `block`; **high-risk rows never produce `auto` at any confidence level**. High-risk docs should be much harder to auto-modify.

High-risk areas:

- security
- compliance
- legal
- architecture decisions
- externally approved documents
- public API contracts in critical systems

Calibration rules: risk is derived from the manifest (document mode, owner, declared risk class) and Jev's `risk` choice — **whichever is higher wins** (conservative max). A `locked` manifest document always resolves to `blocked` regardless of confidence; the classifier cannot override ownership (D2). Confidence bucket boundaries live in the versioned policy (D11); the matrix's *shape* is fixed, its *calibration* is evolvable per repository.

### 5.4 Decision object on the wire

Stage 1's output is a versioned, schema-validated object. Illustrative shape:

```json
{
  "schema": "docsync.decision/v1",
  "decision_id": "d_01J9ZK7Q2M",
  "policy_version": 17,
  "change": {
    "commit": "81af92e",
    "diff_ref": "git:81af92e",
    "change_analysis_ref": "analysis/01J9ZK6X8A"
  },
  "change_kind": "behavioral",
  "documentation_impact": "possible",
  "risk": "medium",
  "confidence": 0.91,
  "answers": {
    "public_behavior_changed": { "value": true,  "probability": 0.87 },
    "api_contract_changed":    { "value": false, "probability": 0.12 },
    "affected_doc_count":      { "value": 2,     "probability": 0.91 }
  },
  "candidates": ["docs/payments.md", "docs/api.md"],
  "action": "review",
  "abstain": false,
  "abstain_reason": null,
  "evidence": [
    { "kind": "symbol_diff", "ref": "src/payments/refund.ts:42", "note": "idempotencyKey became optional" }
  ],
  "trace": {
    "model": "jev",
    "policy_hash": "sha256:…",
    "latency_ms": 740
  }
}
```

Mandatory properties: `schema` version; `policy_version` (§8.4); every answer carries both a **value and a probability** (branching uses the value, policy uses the probability); `candidates` reference manifest-known paths only; `evidence` items are machine-resolvable anchors, not free text; `trace` pins the policy hash and model so any decision can be replayed later. Note: abstention keys on a specific **answer probability** (e.g., `documentation_impact` at 0.83 vs the policy's thresholds, §5.2) — `confidence` above is the overall decision score and can legitimately read 0.91 with `abstain: false` in one run and trigger abstention in another. Ownership: Team A owns the change-analysis schema, the decision-layer owner owns the decision-object schema; both versioned independently.

---

## §6 Documentation Lifecycle State

### 6.1 Baselines (context §27)

Every accepted documentation state has a **baseline**: the record of what the docs were verified against.

```text
Documentation version
    ├── source files
    ├── source symbols
    ├── claims
    └── code revision
```

Example:

```text
docs/payments.md
was verified against
commit 8a73f2c
```

Invalidation flow:

```text
source changed
   ↓
baseline invalidated
   ↓
evaluate affected claims
```

This makes the system **stateful and auditable**: any doc's status is answerable as "verified as of commit X against sources Y, symbols Z, claims C", and the differential between current HEAD and the baseline commit is precisely the evidence set that must be re-evaluated.

Storage: `.docsync/state/baselines/<doc-path>.json` — `{doc_path, doc_content_hash, baseline_commit, source_files[], source_symbols[], claim_ids[], verified_at, verified_by_run, policy_version}`.

Rules:

- Comparison is by **Git object identity**, not timestamps: baseline invalidation = diff `baseline_commit..head` intersected with the baseline's sources/symbols/claims.
- **Rebase/squash resolution (D12):** exact SHA → same tree hash → patch-id/merge-base match → treat as invalidated (SUSPECT). Baselines never silently survive a history rewrite on the default branch.
- **Concurrency:** baseline updates are compare-and-swap writes (persist only if stored `doc_content_hash` + baseline commit match the values the run started from); on mismatch, re-run claim evaluation.
- Baselines are the bridge between Loop A (PR-time differential evaluation) and Loop B (historical audit): both consume the same records.

### 6.2 Documentation state machine (context §28)

```text
                    ┌──────────┐
                    │ VERIFIED │
                    └────┬─────┘
                         │
                    code changes
                         │
                         ▼
                    ┌──────────┐
                    │ SUSPECT  │
                    └────┬─────┘
                         │
                       Jev
                         │
               ┌─────────┼─────────┐
               ▼         ▼         ▼
             CLEAR     REVIEW    STALE
               │                   │
               │                 Sonnet
               │                   │
               │                   ▼
               │               PROPOSED
               │                   │
               │                verify
               │                   │
               │              ┌────┴────┐
               │              ▼         ▼
               │          VERIFIED   REJECTED
               │
               ▼
             CLOSED
```

Transition ownership:

| Transition | Trigger | Actor |
|---|---|---|
| VERIFIED → SUSPECT | deterministic analysis intersects doc's mapped sources/claims/baseline | deterministic |
| SUSPECT → CLEAR | Jev: documentation_impact = none, with recorded evidence | Jev + policy |
| SUSPECT → REVIEW | Jev uncertain / abstain path / low confidence | Jev + policy |
| SUSPECT → STALE | Jev: impact definite/possible above threshold | Jev + policy |
| STALE → PROPOSED | Sonnet produces a DocPatch | Sonnet |
| PROPOSED → VERIFIED | verification passed AND policy allows (auto mode), OR human merged the doc PR | verifier + policy/human |
| PROPOSED → REJECTED | verification failed, or reviewer rejected/edited | verifier/human |
| VERIFIED → CLOSED | doc deleted, archived, or superseded | deterministic/human |

`CLEAR` resolves a SUSPECT without action; `CLOSED` is the terminal state for resolved threads; `REVIEW` is the explicit **abstain** outcome — the system must never force uncertain cases into update/ignore.

Storage: `.docsync/state/docs/<path>.json` — `{state, since, last_run, policy_version, evidence_refs}` plus an **append-only transition log** (state, timestamp, run id, reason). Transitions are compare-and-swap on the previous state hash; a conflicting concurrent transition (e.g., CI run vs local `docsync fix`) loses gracefully and re-runs evaluation. The state machine is *internal truth*; GitHub check/PR surfaces are projections of it.

### 6.3 State storage and idempotency (`.docsync/state/`)

All mutable system state lives under the repository (reconciled canonical layout, D1):

```text
.docsync/
├── manifest.yml
├── policy.yml
├── concepts.yml
├── instructions.md
├── models.yml               # role → OpenRouter model bindings (§10)
├── evaluation/
├── memory/
│   ├── decisions.md
│   ├── known-patterns.md
│   ├── exceptions.md
│   └── failures.md
├── policy-history/          # versioned, retained policy versions (§8.4)
└── state/                   # derived + durable run state (system-written)
    ├── baselines/<doc>.json
    ├── claims/<id>.json
    ├── docs/<path>.json     # state machine + transition log
    ├── runs/<repo>#<base>..<head>.json
    └── audit-cursor
```

Rules:

- **Config (manifest/policy/concepts/models) is human-owned and PR-reviewed; `state/` is system-written.**
- **Idempotency:** run records keyed by `(base_sha, head_sha, manifest_version, policy_version)`; re-runs upsert rather than duplicate. Deterministic stages are pure functions of Git content (blob-SHA cacheable); LLM stage results cached by input hash.
- **Concurrency:** state files are compare-and-swap (expected-previous hash); CI uses a repo-scoped concurrency queue for state writes. Local CLI and CI may both run; the loser re-runs evaluation rather than overwriting.
- **Auditability:** nothing is deleted — transitions, decisions, and policy history are append-only, giving full replay of "why was this doc changed on this commit".
- Committing `state/` to Git creates diff noise; if a team prefers, `state/` may be CI-maintained with Git-anchored references — but auditability then depends on CI retention. **V1 default: committed** (durable, reviewable), with the noise risk noted in §16.

---

## §7 The Two Loops (context §26)

### 7.1 Loop A — Synchronous PR loop

```text
PR
 ↓
diff
 ↓
affected docs
 ↓
Jev
 ↓
Sonnet
 ↓
verification
 ↓
PR / review
```

Loop A is **fast and developer-facing**: it runs inside the PR's lifecycle (check or comment) and its output is a reviewable Git artifact — a patch, a review request, or silence (for correctly-ignored changes).

Latency discipline (target: p50 ≤ ~10 s for ignore-routed PRs, ≤ ~60 s end-to-end for routed ones):

1. **Stage budgets:** deterministic change analysis ≤ 2–5 s; Jev ≲ 1 s (typed model, small structured payload, no repository text); retrieval ≤ 1 s (indexed graph, no embedding search on the hot path unless declared fallback); Sonnet only for cases routed past Stage 1; verification = deterministic validators + one small second-decision pass.
2. **Short-circuiting:** the majority of PRs are `ignore`-routed and never invoke Sonnet — their Loop-A cost is the deterministic pre-filter plus one Jev call. Baseline checks (§6.1) short-circuit documents whose verified baseline still holds.
3. **Parallelism:** candidate documents investigated concurrently (independent Sonnet sessions when unrelated); deterministic validators run in parallel with the judge.
4. **Incrementality/caching:** change analysis cached per commit; Jev decisions memoized for identical (diff-subset, policy-version) keys — shadow runs must key on `policy_version` so shadow decisions never poison memoized production results (D6).
5. **Early signal:** post the GitHub check after Stage 1 (decision + candidates + confidence), update when Stage 2/3 complete — developers see "2 documents potentially stale, patch generating" within seconds.
6. **Bounded workspace + batching:** Sonnet's workspace capped; related candidate docs sharing context batched into one session.
7. **Graceful degradation:** if the Sonnet stage exceeds budget, emit a review request with change analysis and evidence instead of blocking on a patch — a late *suggestion* is acceptable; a hung check is not.

### 7.2 Loop B — Background drift audit

```text
Repository history
 ↓
document graph
 ↓
claims
 ↓
drift analysis
 ↓
background findings
```

**Purpose:** catch documentation that became stale *earlier* — drift no single current PR makes obvious (cumulative drift, merged changes that skipped doc updates, docs stale before docSync adoption).

Architecture:

- Trigger: scheduled workflow (e.g., nightly) or post-merge on the default branch — **never** on the developer's PR critical path.
- Semantics: **read-mostly.** Loop B computes drift over history (baseline differentials, claim-support sweeps, document-graph reconciliation) and emits `background findings` with evidence (claim IDs, commits, confidence). Findings surface as advisory artifacts: a default-branch check run, drift dashboard, or issue — **not auto-writes**.
- Cost control: incremental since the last audit cursor (`state/audit-cursor`); chunked per document; findings deduplicated by claim ID so nightly runs don't spam.
- Findings warranting patches become SUGGEST items (or doc PRs only in `suggest`/`auto` modes per policy), **reusing the same verification + policy engine as Loop A** — one code path, two triggers.

---

## §8 Policy Lifecycle & Learning

### 8.1 Sonnet as Policy Architect (context §16)

> Sonnet should help write/refine the Jev decision policy for each repository.

But this does **not** mean "Sonnet writes a live prompt forever." The better abstraction:

> **Sonnet is a repository-specific Policy Architect / Policy Compiler input generator.**

Sonnet receives:

- repository structure/profile
- current Jev policy
- documentation manifest
- concepts
- historical PRs
- prior Jev decisions
- false positives
- false negatives
- human reviewer corrections
- known exceptions

It can then propose:

1. new decision types
2. new Jev questions
3. better mappings
4. routing rules
5. thresholds
6. evidence requirements
7. new exceptions
8. new repository-specific concepts

Example learned issue:

```text
Current policy:
  internal_refactor → ignore

Historical evidence:
  14/23 internal refactors changed generated API docs.

Recommendation:
  internal_refactor should route to generated-output inspection.
```

That is **policy learning, not prompt optimization**: the unit of change is a versioned decision-procedure artifact (questions, routing rules, thresholds, exceptions), grounded in counted evidence (14/23), and it must still pass through evaluation and promotion (§8.4–§8.6) before it affects production.

### 8.2 Policy compilation (context §17)

```text
Repository
    ↓
Repository profiling
    ↓
Sonnet Policy Architect
    ↓
Decision policy
    ↓
Policy compiler
    ↓
Typed Jev workflow
```

The system asks:

> **What decision procedure should this repository have?**

rather than only:

> What should Jev's prompt say?

The compiler is **deterministic**: it validates the policy (schema conformance, question types, threshold sanity, choice-set closure, trigger-taxonomy mapping to the decision schema) and compiles it into the typed Jev workflow for that repository. A policy that fails compilation is returned to the architect with compiler diagnostics — **no partial policies ever reach production**.

### 8.3 Example Jev policy structure (context §18)

```yaml
version: 17

decision_schema:

  change_kind:
    choices:
      - docs_only
      - internal
      - behavioral
      - public_api
      - configuration
      - architecture

  documentation_impact:
    choices:
      - none
      - possible
      - definite

  risk:
    choices:
      - low
      - medium
      - high

  action:
    choices:
      - ignore
      - review
      - update
      - blocked

questions:

  public_behavior_changed:
    type: boolean

  api_contract_changed:
    type: boolean

  affected_doc_count:
    type: score

routing:

  high_confidence:
    threshold: 0.94

  review:
    threshold: 0.72
```

The exact schema is compiled/generated according to the Jev API and the repository's needs — this example is illustrative of shape, not a fixed contract. What the structure encodes: **closed choice sets** (no free-text `change_kind`), **typed questions**, and **routing thresholds** (`high_confidence: 0.94`, `review: 0.72`) that parameterize the confidence × risk matrix (§5.3). Anything not expressible in this schema must be expressed as a new *schema element via the Policy Architect* — never as prose injected into a prompt.

#### 8.3.1 Repository-specific Jev policies (context §15)

A key design decision: **every repository should have its own Jev decision policy.** Different repositories define "important change" differently.

Generic universal questions:

```text
behavior_changed?
public_contract_changed?
documentation_impacted?
risk_level?
```

Payments repository:

```text
payment_flow_changed?
idempotency_changed?
webhook_contract_changed?
compliance_behavior_changed?
```

Kubernetes/operator repository:

```text
CRD_changed?
kubectl_usage_changed?
generated_manifest_changed?
operator_behavior_changed?
```

Frontend repository:

```text
user_visible_behavior_changed?
public_component_api_changed?
configuration_changed?
accessibility_behavior_changed?
```

The system starts with a **small universal schema** and lets the repository-specific policy **grow over time**. Domain questions are *additive*: the universal set always runs; repository questions refine routing and risk. Question definitions are declarative (compiled to Jev's typed workflow, §8.2), never prompt prose.

The manifest's user-facing `triggers:`/`ignore:` vocabulary (`public_behavior`, `api_change`, `configuration_change`, `architecture_change`, `internal_refactor`, `test_only`) must map onto this schema via a versioned mapping layer; validation errors name the schema version expected (§11.3).

### 8.4 Version every Jev policy (context §20)

```yaml
policy_version: 17

created_by:
  model: claude-sonnet

reason:
  "Reduce false negatives for generated API docs."

evaluation:
  precision: 0.96
  recall: 0.93
  unsafe_auto_updates: 0

previous:
  version: 16

status: canary
```

Rules:

- **Policy history is retained** (`.docsync/policy-history/`): every version, its evaluation, and its reason persist — a full audit trail of how the repository's decision procedure evolved.
- **Rollback is easy:** versions are self-contained with explicit `previous` pointers; rollback = promoting the prior version (a metadata operation, not a rebuild).
- `unsafe_auto_updates` is a **gate metric**: any candidate showing unsafe auto-updates on the evaluation set is not promotable regardless of precision/recall.
- Every decision object carries `policy_version` (§5.4), so historical decisions are always attributable to the exact policy that produced them.

### 8.5 Do not let Sonnet mutate production policy directly (context §19)

This is a critical safety decision.

Bad model:

```text
Sonnet
  ↓
edit live Jev policy
  ↓
use changed policy
  ↓
self-evaluate
  ↓
change policy again
```

Preferred model:

```text
Sonnet
  ↓
propose policy update
  ↓
offline evaluation
  ↓
compare with current policy
  ↓
canary/shadow
  ↓
promotion decision
  ↓
production
```

**Policy refinement should behave like software deployment.** The bad model has no rollback, no baseline, and a self-grading loop that can silently degrade the system while reporting success. The preferred model gives every policy change a diff, an evaluation, a comparison, a bounded rollout, and an explicit promotion decision. Architecturally (D13/D14): even in `auto` learning mode (§8.9), promotion remains application-owned and versioned; any path where Sonnet output becomes live policy without crossing EVALUATION is vetoed.

### 8.6 Shadow evaluation / canary policies (context §21)

```text
Current policy:  v17
Candidate policy: v18
```

For a period, both run:

```text
PR
 ├── Jev v17 → production decision
 └── Jev v18 → shadow decision
```

The system compares them **without letting v18 affect developers**. Example:

```text
v17: correct 93.2%
v18: correct 96.1%
```

Only then promote v18. This gives **autonomous improvement without autonomous degradation**: the candidate accumulates real-traffic evidence at zero risk to developers.

Guardrails (making the numbers trustworthy):

- Shadow runs see **identical inputs** as production (same change analysis, same candidates) so differences are attributable to the policy, not the pipeline.
- Comparison is decision-level: an agreement/disagreement matrix (both-ignore, both-review, v17-review/v18-ignore, …), with every disagreement resolvable into a label via reviewer outcome.
- **Promotion gates:** aggregate correctness (93.2% → 96.1% style), **no regression** in false-update rate or abstention quality, `unsafe_auto_updates = 0` on the eval set, and a minimum sample size / significance floor so a lucky streak cannot promote a policy.
- Time-box canaries; auto-expire stale shadows.
- **Compute (D6):** shadow evaluation runs primarily as **offline historical replay**, plus a **sampled** live shadow to protect Loop A latency and CI budget.

### 8.7 Closed-loop learning from GitHub / review feedback (context §22)

Every accepted/rejected/edited decision becomes evaluation data.

**False negative:**

```text
Jev says:  docs_affected = false
But developer later changes docs/api.md in the same PR
→ Record: FALSE NEGATIVE
```

**False positive:**

```text
Jev says:  docs/api.md affected
Sonnet proposes a patch
Reviewer rejects it because documentation was already correct
→ Record: FALSE POSITIVE
```

Human feedback becomes high-value training/evaluation data. Signals captured: **accept**, **edit** (the edited diff is gold — it shows both what was wrong and what "minimal" meant to the human), **reject with reason**, **mark as intentional**, **always ignore this pattern**. Repeated identical corrections surface as Policy-Architect proposals (e.g., 17 reviewer overrides of "src/generated/** is not documentation-relevant" → propose exclusion) — **feedback accelerates proposals; it never bypasses evaluation** (§11.4).

Label hygiene: implicit signals are ambiguous (a reviewer ignoring a suggestion may mean "wrong" or "busy"). The recorder normalizes labels — timeout ⇒ `unlabeled` (not `accepted`); edit ⇒ `corrected` (with the edit diff stored); reject ⇒ `false_positive` only when the stated reason is "doc already correct", otherwise `style_rejected`. Feedback rows carry `policy_version`, decision id, and signal type, so evaluation distinguishes "the policy was wrong" from "the presentation was wrong."

### 8.8 Persistent repository memory (context §25)

Memory must be **structured and evidence-backed rather than arbitrary text**. Layout (canonical, D1):

```text
.docsync/memory/
├── decisions.md
├── known-patterns.md
├── exceptions.md
└── failures.md
```

Structured record example:

```yaml
pattern:
  name: generated-sdk

evidence:
  - PR: 381
  - PR: 402
  - PR: 449

observation:
  "Changes under src/api/generated may alter public documentation indirectly."

action:
  inspect_generated_output

confidence:
  0.93
```

Memory **feeds policy refinement and retrieval**: patterns give Jev context and give the Policy Architect counted evidence; `exceptions.md` seeds policy exceptions; `failures.md` seeds negative examples for evaluation. Rules:

- Every record carries machine-checkable evidence (PR numbers, decision ids, anchors) and a confidence. Unverifiable entries do not get high-confidence treatment — an anecdote is a hypothesis with confidence near the floor, not a fact.
- Memory is **prunable**: when later feedback contradicts a pattern, the correction lands in `failures.md` and the pattern's confidence decays — memory cannot ossify wrong beliefs.
- Reviewer feedback events feed memory in the shared structured record format (§11.4); the DX layer never invents its own event schema.

### 8.9 Self-improvement modes (context §24)

Borrowing the `auto / propose / off` pattern from agent self-learning systems, adapted to software safety:

```yaml
learning:
  mode: propose
```

- **off** — no policy learning.
- **propose** — Sonnet generates policy updates for human approval.
- **canary** — candidate policy runs alongside production policy.
- **auto** — candidate can be promoted automatically if all evaluation gates pass.

Recommended default:

```text
propose
```

`canary` and `auto` are *escalations of trust*, not separate algorithms: `canary` automates the shadow phase (§8.6) once a human has approved the candidate; `auto` additionally automates promotion when every gate (offline eval comparison, no unsafe auto-updates, significance floor, no regression on abstention/false-update metrics) passes. Even in `auto`, promotion events are logged with full evaluation evidence and rollback remains one operation (§8.4). `learning.mode` is a governance knob with a single config home and a permission story (repo owner vs org policy, §11.2) — its gates cannot be bypassed by config.

---

## §9 Evaluation Framework (context §35, §36)

Evaluation is a **first-class component, not an afterthought**. Historical PRs become a benchmark dataset for the repository.

Core metrics:

- **Detection recall** — did the system find documentation that truly needed updates?
- **Precision** — did it avoid touching docs unnecessarily?
- **Patch correctness** — was the generated documentation factually correct?
- **Minimality** — did the patch modify only what was needed?
- **False-update rate** — how often did it change documentation that was already correct?
- **Abstention quality** — did it correctly avoid automatic decisions when uncertain?

The goal is **not**:

> maximize number of docs changed.

The goal **is**:

> **maximize correct documentation updates while minimizing unjustified edits.**

Rigor:

- Metrics computed **per policy version** on a fixed, version-pinned evaluation split, plus a rolling "recent PRs" split to catch drift; labels come from the feedback recorder with the §8.7 hygiene rules applied before counting.
- Patch correctness judged against source anchors (claim checking, §4.8); minimality **measured** (edit distance / hunk count relative to the triggering diff), not eyeballed.
- Selection bias: only reviewed cases produce labels; auto-applied patches are unverified by humans unless sampled — maintain a periodic **spot-check rate** over auto-applied patches and treat audit findings as eval data.
- Frozen eval splits prevent contamination between feedback used for candidate-policy training and the benchmark.

Suggested policy improvement objective (context §36):

```text
score =
  0.40 × precision
+ 0.30 × recall
+ 0.20 × patch_correctness
+ 0.10 × efficiency
```

- For **high-risk repositories, safety should dominate**: the weighting shifts so false-update rate and unsafe-auto-update gates dominate the objective (a high-risk repo's score collapses if `unsafe_auto_updates > 0`, regardless of other terms).
- Weights are **repository-configurable**: a docs-heavy low-risk repo can afford recall-weighted tuning; a payments or auth repo cannot.
- The objective is used to *compare candidate policies* during offline evaluation and canary comparison; it is never optimized by re-running against live developers.
- `efficiency` includes inference cost and latency, fed by OpenRouter usage accounting (§10.6).

---

## §10 Model Access Layer — OpenRouter

> **User decision (2026-10-05):** all AI inference is accessed **through OpenRouter**. This section is the binding addition to the context document; it implements §39 (model routing) concretely.

### 10.1 Why an access layer

The context (§39) requires that the architecture not be hard-coded around Jev + Sonnet forever:

```text
                 Decision Engine
                       │
               ┌────────┴────────┐
               │                 │
              Jev             future model
               │                 │
               └────────┬────────┘
                        ▼
                   Policy Engine
                        ▼
                   Agent Router
                        │
        ┌───────────────┼────────────────┐
        ▼               ▼                ▼
     small model     Sonnet          higher-end model
```

OpenRouter is chosen as the single inference gateway for every LLM stage. Jev and Sonnet are therefore **logical roles**, not hard-coded models. Possible future patterns — all supported without redesign:

```text
Jev → Sonnet

Jev → Sonnet
     ↓
high-risk cases → stronger model

local classifier → Sonnet
```

### 10.2 Logical roles

| Role | Stage | Typical model class | Output contract |
|---|---|---|---|
| `decision-engine` ("Jev") | Stage 1 | fast, cheap, typed/JSON-capable | `docsync.decision/v1` object (§5.4) |
| `patch-writer` ("Sonnet") | Stage 2 | strong reasoning + long context | `DocPatch` + evidence (§4.7, §4.9) |
| `judge` | Stage 3 tier 3 | independent instance, never the writer instance | `pass \| review \| reject` (D13) |
| `policy-architect` | Control plane | strongest reasoning | policy proposal artifacts (§8.1) |
| `claim-extractor` (V2) | claim extraction | mid-tier | claim records (§4.3) |
| `drift-auditor` (V2/V3, Loop B) | background sweeps | cost-efficient mid-tier | background findings (§7.2) |

### 10.3 Role → model binding (`models.yml`)

Bindings are declarative, versioned, and reviewable — same discipline as policy:

```yaml
version: 3
gateway: openrouter

roles:
  decision-engine:
    primary: anthropic/claude-sonnet-4.5        # example; actual Jev-binding per repo
    fallbacks:
      - openai/gpt-4.1-mini
    params:
      temperature: 0
      max_tokens: 2000
    routing:
      provider:
        order: [anthropic, google]              # provider preference order
        allow_fallbacks: true

  patch-writer:
    primary: anthropic/claude-sonnet-4.5
    fallbacks:
      - anthropic/claude-3.7-sonnet
    params:
      temperature: 0

  judge:
    primary: openai/gpt-4.1
    constraint: must_not_equal_patch-writer_instance   # D13

  policy-architect:
    primary: anthropic/claude-sonnet-4.5
```

Rules:

- `models.yml` changes are **treated like policy changes**: any model swap is shadow/canary evaluated (same machinery as §8.6) before production — model churn is a known cause of silent quality drift (§16).
- Routing policy (which model for which risk/confidence bucket — e.g., "high-risk → stronger model") is itself declarative and versioned: **a policy change, not a code change**.
- Any model that honors the stage's typed interface is admissible; this is also what makes shadow-evaluating a *replacement model* (not just a new policy version) possible with the same canary machinery.

### 10.4 Structured outputs and validation

- Stage 1's decision object and Stage 3's judge verdict are schema-validated at the gateway boundary.
- Models with native structured-output support get JSON-schema-constrained responses; others get constrained prompting + strict validation.
- A schema-violating output is **retried (bounded), then abstained to REVIEW** — never hand-patched into compliance by the orchestrator (§3.4).

### 10.5 Resilience

- **Model fallbacks** (`fallbacks:` chain) and **provider fallbacks** (`provider.order`, `allow_fallbacks`) are per-role.
- Total inference failure → fail toward advisory/neutral check or REVIEW; never fail-closed blocking merges on infra failure unless policy opts in (§3.4, D10).
- Rate limits: per-role concurrency caps; Loop A never issues unbounded parallel model calls.

### 10.6 Telemetry, cost, and provenance

- OpenRouter usage accounting feeds: the `efficiency` term of the improvement objective (§9), per-PR cost budgets, and per-policy-version cost attribution.
- **Provenance names the actual routed model** (role → model id + provider actually used), never a hardcoded brand (§12.5). If routing selects a different writer for a case, the record names the actual model — this keeps provenance truthful under model routing (V3).
- Latency (`latency_ms` in the decision trace) is recorded per stage for Loop A budget enforcement.

### 10.7 Secrets and safety

- `OPENROUTER_API_KEY` lives **only** in the orchestrator's environment (CI secret store / local env). Models never hold credentials (invariant 8, §14); keys are never sent to models in prompts and never stored in `.docsync/` files.
- The gateway boundary is also the **prompt-injection containment boundary**: repository content (code, docs, configs) passed to models is untrusted input; model outputs are untrusted data validated against typed schemas; no model output influences permissions, file selection, or Git credentials (§13).
- Prompt caching (where the routed provider supports it) is applied to the deterministic prefix of workspaces (manifest/policy/instructions), keyed by content hash.

### 10.8 Calibration across models

Thresholds (D11) are calibrated **per model binding**: swapping the decision-engine model invalidates the previous calibration; the new binding must pass the calibration check on the evaluation split before promotion. Displayed confidence is pinned to `(policy_version, model binding)` (D3).

---

## §11 Repository Configuration & Governance

### 11.1 Repository-scoped manifest (context §9)

Every repository carries its own documentation policy as versioned, reviewable files. There is no hidden server-side configuration for repository behavior; **what the repository contains is what runs**. Canonical layout in §6.3.

Example `manifest.yml`:

```yaml
version: 1

documents:

  - path: docs/payments.md
    mode: auto

    sources:
      - src/payments/**
      - src/api/payments/**

    triggers:
      - public_behavior
      - api_change
      - configuration_change

    ignore:
      - internal_refactor
      - test_only

  - path: docs/architecture.md
    mode: suggest

    sources:
      - src/**

    triggers:
      - architecture_change

  - path: docs/security.md
    mode: locked

    sources:
      - src/auth/**
      - src/security/**
```

Entry fields:

- **`path`** — the document file (required; must exist in-repo or be flagged).
- **`mode`** — the mutability class (§11.2).
- **`sources`** — glob patterns over code/config paths feeding this document (e.g., `src/payments/**`).
- **`triggers`** — change classifications that make the document a patch candidate.
- **`ignore`** — change classifications that never rouse this document.

The manifest is the **allowlist**, not a hint (D7): a document absent from the manifest is never scanned, never patched; unmapped-but-changed documents are surfaced only as **"unknown coverage"** in `docsync check` (D7) — never silently ignored.

### 11.2 Document modes and ownership (context §9, §31)

Five mutability modes:

| Mode | Definition |
|---|---|
| `auto` | Automatic patching is permitted **within policy** — only after verification passes and the confidence × risk gate clears. `auto` never bypasses locks, verification, or the abstain path. |
| `suggest` | Generate a proposal but require human review; surfaces as a check-run report or doc PR awaiting review — never a direct commit. |
| `locked` | May be **detected** as affected but must **not** be automatically modified. The system reports; humans act. |
| `generated` | Owned by another generator; AI does not fight it. Drift is observed and reported to the generator's maintainers; the regenerating tool owns corrections. |
| `human` | Human-owned docs; AI can suggest only. Review-gated like `suggest`, but signals authorship provenance. |

Independent of mutability, the system tracks **authorship/ownership** (context §31), because protection is keyed to authorship as well as mode. Five categories: **human-authored**, **AI-maintained**, **generated**, **external**, **locked**.

```yaml
docs:
  docs/api.md:
    owner: ai
    mode: auto

  docs/architecture.md:
    owner: human
    mode: suggest

  docs/generated-api.md:
    owner: generator
    mode: generated

  docs/security.md:
    owner: security-team
    mode: locked
```

Core principle: **AI should not fight another source-of-truth generator.** Editing a generated doc's output is guaranteed churn (the next regeneration reverts it). `generated` documents are excluded from the patch candidate set; `external` documents likewise (drift reported with a pointer to the external source); `locked` is the strongest case (§11.4).

Governance floors (org layer, forward-declared ahead of V3): an org-owned configuration defines **floors**, e.g., "documents matching `docs/security/**` may not exceed `suggest`", "all repos run report-only until their manifest is reviewed." A repository manifest violating a floor is a **validation error**, not a silent clamp. `locked` is terminal — no flag or policy layer converts it; the only path to a mode change is a human PR editing `manifest.yml`, deliberately the most visible diff in the repository. Machine-proposed policy PRs may **never** carry a mode transition to `auto` (§11.4).

### 11.3 Configuration precedence and validation (Team C rigor)

Precedence (most restrictive wins; **governance floors dominate**):

1. **Invocation flags / workflow inputs** — may tighten behavior (report-only, dry-run) but **never loosen** mutability modes or bypass locks.
2. **Per-document entries in `manifest.yml`**.
3. **Repository manifest `defaults:` block** for documents without explicit settings.
4. **Org-level floors** (GitHub App settings or org policy repository).
5. **Built-in defaults** — `mode: suggest`, learning `mode: propose`, check-only behavior, abstain-on-uncertainty.

Additional rules:

- **Glob overlap:** when two entries' `sources` match a changed file, the more specific glob wins; on a tie the more restrictive mode wins (fail-safe); between equally-restrictive entries the document routes to *both* candidates.
- **Environment variables carry credentials and endpoints only — never policy.** Policy must be reviewable in Git.

**Manifest validation — fail closed (D8).** An invalid manifest means the system **does not run**: no AI calls, no patches, no PRs, no partial behavior; no last-known-good fallback.

Hard errors (check fails before any intelligence stage): YAML/schema violations (unknown `mode`, missing `path`, duplicate paths, unsupported `version`); trigger/ignore values absent from the engine's decision schema; effective mode conflicting with an org floor; globs escaping the repository or matching `.docsync/` internals / `.github/workflows/`.

Warnings (run proceeds, annotated): `sources` globs matching zero files today; `path` not yet existing; coverage lint findings.

Surfaces: local CLI exit code `2` (config error; `0` clean, `1` drift found, `3` runtime failure, §12.1); CI check concludes `failure` titled `docsync: configuration error` listing offending lines. Remediation is always a normal PR fixing `manifest.yml` — recovery stays inside the Git workflow.

### 11.4 Locks: detected, never touched — four enforcement layers

A lock violation must defeat four independent layers:

1. **Candidate selection:** locked/generated docs may be *detected* as affected (Jev still classifies; the 🔒 row still appears), but they are removed from the patch candidate set **before any writer model is invoked**.
2. **Writer constraints:** the writer's workspace marks protected paths read-only; instructions permit analysis but forbid patch emission for protected paths.
3. **Application guard:** the orchestrator computes the writable-path allowlist **from the manifest itself, not from model output**, and validates every patch hunk against it before applying anything; a patch touching a protected path is rejected **whole** — no partial application.
4. **PR filter:** before creating a doc PR, the orchestrator re-diffs the branch against the allowlist; any out-of-allowlist change aborts PR creation and is logged as a **safety incident** (alert-worthy bug class, not silently swallowed).

Visible contract for a locked document:

```text
🔒 docs/security.md
  Potentially affected
  locked
```

Reviewer "always ignore this pattern" and accumulated overrides (e.g., 17 overrides of "src/generated/** is not documentation-relevant") become Policy-Architect *proposals* via the promotion pipeline — **models propose; humans promote**; the click is acknowledged instantly, the policy change arrives later as a reviewable PR. Mode transitions to `auto` never appear in machine-proposed PRs.

---

## §12 Developer Experience & CI Integration

The governing DX principle (context §33): **the UI should make the system feel like Git, not a chatbot.** Native GitHub primitives only — check runs, diffs, PRs. No conversation threads, no free-form AI comments. Every AI output is materialized as a diff a human reviews with normal review tooling.

### 12.1 CLI (context §32)

Three verbs, one mental model: **check** reads, **fix** writes to the working tree, **pr** writes to a remote branch.

#### `docsync check`

```bash
docsync check
```

```text
Documentation consistency

✓ docs/auth.md
✓ docs/users.md

⚠ docs/payments.md
  Behaviour changed
  confidence: 97%

⚠ docs/api.md
  Request schema changed
  confidence: 94%

🔒 docs/security.md
  Potentially affected
  locked
```

Visual language: `✓` consistent · `⚠` potentially stale (reason + confidence) · `🔒` affected but locked — surfaced, never touched. Output mirrors exactly what CI will show, so local and CI never disagree about the facts. Displayed confidence is pinned to the producing policy version and model binding (D3). Fallback glyphs (`[ok]`, `[!]`, `[locked]`) for constrained terminals.

Exit codes (for scripts and CI reuse):

| Code | Meaning |
|---|---|
| `0` | No drift detected |
| `1` | Drift detected (candidates found) |
| `2` | Configuration/manifest error (fail-closed, D8) |
| `3` | Runtime failure (API/model/verifier error — also fail-closed) |

Flags: `--strict` promotes `⚠` to exit `1` even when the policy engine would only suggest; `--json` emits the machine-readable report for custom integrations.

#### `docsync fix`

```bash
docsync fix
```

Runs the full pipeline — detection, Jev routing, writer investigation, verification — but applies the **verified** patch to the **local working tree** instead of creating a PR. The developer reviews with the tool they already trust:

```bash
git diff
```

Idempotency guarantee:

- Patches are minimal, content-anchored diffs against current file state; applying an already-applied patch is a no-op (second run's detection sees docs consistent with sources → "no candidates").
- Every run writes a provenance record keyed by run ID; re-invoking with the same run ID is detected and skipped, never re-applied.
- If a doc changed between runs and the stored patch no longer applies, `fix` **refuses and re-runs detection** against current content rather than force-applying; a 3-way merge attempt is allowed only if the merged result **re-passes verification** before being written.
- Working-tree guard: docs with uncommitted local changes are skipped unless `--allow-dirty`; `fix` writes **only** to manifest-listed documents in writable modes — never code files, never `.docsync/` internals, never locked/generated docs.

`fix` is the fastest review loop in the product: the human sees the diff in their own editor; committing remains entirely the human's act.

#### `docsync pr`

```bash
docsync pr
```

Creates a documentation PR from the local analysis using the standard conventions (§12.4), e.g.:

```text
docs: synchronize documentation with PR #481
```

Same safety rails as the CI path: provenance required, allowlist-enforced, locked docs excluded, never pushed to the default branch. Uses the developer's existing push credentials; never requests elevated scopes.

### 12.2 CI / GitHub integration (context §33)

```text
Developer PR
   ↓
GitHub Action / GitHub App
   ↓
docsync check
   ↓
Documentation consistency check
```

Possible GitHub check UI:

```text
┌───────────────────────────────────────────┐
│ Documentation Consistency                 │
│                                           │
│ ⚠ 2 documents potentially stale           │
│                                           │
│ src/payments/refund.ts                    │
│        ↓                                  │
│ docs/payments.md                 97%       │
│ docs/api.md                      94%       │
│                                           │
│ docs/architecture.md             11%       │
│ └─ no action                              │
│                                           │
│ [ Generate documentation patch ]          │
└───────────────────────────────────────────┘
```

Design intent: `src/payments/refund.ts → docs/payments.md (97%)` and `docs/api.md (94%)` are candidates; **showing the rejected candidate** (`docs/architecture.md`, 11%, no action) is deliberate — it proves the system evaluated and abstained, which is what makes the check trustworthy rather than noisy (the abstention-visibility requirement of §5.2; shown sparingly to avoid noise). The **[ Generate documentation patch ]** button is an explicit, human-initiated escalation (`check_run.requested_action` → writer → verification → doc PR); no patch generation runs without someone asking (default V1 posture; repos with reviewed manifests may opt into auto-proposal).

Check-run lifecycle: one check named `docsync/check` per PR, **upserted** (never duplicated) per run; conclusion maps from the policy engine — `success` (clear/no action), **`neutral` (advisory findings; default posture, D10)**, `failure` only when configured to enforce (e.g., high-risk locked docs affected, `--strict`). Confidence percentages (97/94/11) are display projections of Jev/verification scores, pinned to the policy version (D3). The check is updated after Stage 1 and again after Stage 2/3 (early-signal pattern, §7.1).

#### GitHub App permission model (minimum viable authority)

| Permission | Scope | Why |
|---|---|---|
| **Contents** | Read & write | Read repo files/diffs; write documentation files on `docsync/*` branches |
| **Pull requests** | Read & write | Create documentation PRs, attach provenance body, labels |
| **Checks** | Read & write | Create/update the consistency check; receive the button's `requested_action` |

Explicitly **not** granted: Administration, Workflows (must never modify CI definitions), Members, Secrets, Deployments, Webhooks, Issues. The App cannot do anything a reviewer couldn't see in a diff. Operational rules:

- **Repo-scoped installations only** (never "all repositories"); org admins opt repos in.
- **Short-lived installation tokens** per run; no long-lived PATs; tokens never reach the model layer — the deterministic orchestrator performs all Git/API writes.
- **Webhook surface:** `pull_request` (opened/synchronize), `check_run.requested_action`, `push` (optional, post-merge checks). Nothing else.
- **Action variant:** `GITHUB_TOKEN` with `contents: write`, `pull-requests: write`, `checks: write`. Documented limitation: PRs created with `GITHUB_TOKEN` do not trigger downstream workflows and forks cannot be written; the App avoids both. The Action is the zero-install fallback and runs **report-only on fork PRs**.
- **Fork PRs:** report-only mode (check shows mapping/candidates; no patch generation, no PR creation — the App holds no write access to the fork). Documented limitation, not a bug.
- **Self-trigger suppression:** docsync ignores its own doc PRs (by branch prefix `docsync/` **and** App identity, not just path); the system never creates a doc PR from its own doc PR.

#### PR creation rate and burst limits

- **One documentation PR per source PR per run** — candidate docs aggregate into a single PR (D4), with per-document partial-pass semantics: a single failing doc is dropped from the PR and reported, not blocking the rest.
- **Max open docsync PRs per repository:** 3 (configurable 1–10). At the cap: downgrade to check-run report + "patch ready, apply manually".
- **Max doc PRs per repository per hour:** 10 (configurable); bursts queue, not fail.
- **Idempotency key** = (source PR head SHA, doc-set content hash); re-runs update the existing check/doc PR instead of duplicating.
- **Concurrency group** `docsync-<repo>-<pr>`: a new push supersedes the in-flight run.
- **429/403 secondary-rate responses:** exponential backoff with jitter, then fail-safe to report-only. The system never retries its way into a flood.
- **Stale doc PRs:** if the source PR evolves after the doc PR opens, the doc PR is marked "superseded by newer analysis" and the idempotent re-run replaces it; deterministic branch names (`docsync/pr-481-<head-sha7>`) keep dedup simple. Legitimate re-creation after a revert gets a deterministic disambiguator suffix.

### 12.3 Merge-order conflicts between doc PRs and code PRs

- Both may touch `docs/api.md`. Mitigations: one doc PR per source PR (D4); rebase the doc branch on the current default branch before pushing; if the target doc changed since Sonnet read it, the patch is re-applied/3-way merged **only if it re-passes verification**; if a prior doc PR already fixed the drift, claims are re-evaluated against new HEAD and the no-op patch is dropped.
- Doc PR checks re-run against the merge target (merge queues, squash merges — baseline re-resolution per D12).
- Post-merge `push` handling: re-run evaluation after the code PR merges so a doc PR that no longer applies is superseded rather than conflicting.

### 12.4 The separate documentation PR (context §34)

Documentation changes ship as a **separate PR** — never direct pushes to the developer's code PR, never direct commits to the default branch. Five reasons, in order of weight:

1. **Independently reviewable** — docs get their own reviewer and approval.
2. **AI-generated changes remain isolated** — reviewers always know which diffs came from the machine.
3. **Easy rollback** — revert one PR; documentation returns to its prior verified state.
4. **The code PR stays focused** — no inflated diffs, CI time, or review burden.
5. **Clear provenance** — the doc PR is born with its evidence record.

Conventions:

- **Branch:** `docsync/<source-pr-number>-docs` (or `docsync/<run-id>` for local flows).
- **Title:** `docs: synchronize documentation with PR #481` (source-PR-linked; deterministic suffix distinguishes rare multiples).
- **Body:** provenance summary (§12.5), candidate mapping table, verification results, and the "generated by docsync — review like any code change" banner.
- The doc PR **links to** the source PR but does not mechanically depend on it. An **integrated mode** (documentation commits appended to the code PR with explicit author opt-in) is possible later and explicitly deferred — it trades isolation for convenience and must never become the default before the trust loop is proven.

### 12.5 Provenance and evidence records (context §30)

Every automatic documentation change carries structured provenance — the answer to "why does this diff exist?" must be inspectable months later, in the PR, without re-running anything:

```yaml
docsync:
  source_commit: 81af92e

  source_files:
    - src/payments/refund.ts
    - src/payments/types.ts

  detector:
    model: jev
    confidence: 0.97

  writer:
    model: claude-sonnet

  verification:
    status: passed

  generated_at: 2026-10-04T22:31:00Z
```

(With model routing, `detector.model` / `writer.model` record the **actual routed OpenRouter model IDs** — §10.6; the values above are the illustrative forms from the context.)

Storage precedence (D5):

- **PR body summary — canonical:** permanent, visible, where review and provenance live together.
- **`.docsync/state/` — archival:** durable, in-repo, content-addressed, append-only; survives check retention windows.
- **Check-run metadata — ephemeral view:** what reviewers see first; retention-limited.

Governance rules:

1. **Provenance is required.** The orchestrator refuses to create a doc PR whose commits lack a provenance record. A provenance-less PR is a bug, not a format option.
2. Records are **append-only and content-addressed** by run ID; re-running the same run is a no-op, never a history rewrite.
3. Provenance **reflects what actually ran** (actual models, actual policy version, actual verification status).
4. Provenance is **not a secret container**: no tokens, no prompt dumps, no reviewer identities beyond what the repo already exposes.
5. Provenance never modifies the Markdown document bodies (churn/merge-conflict avoidance).

---

## §13 Orchestrator vs LLM Responsibility (context §38, §37)

Production orchestration must be deterministic where possible:

```text
ORCHESTRATOR / APPLICATION
├── permissions
├── file selection
├── policies
├── thresholds
├── workflow
├── retries
├── validation
├── Git operations
└── promotion/rollback

LLMs
├── classification (Jev)
├── reasoning/investigation (Sonnet)
├── policy proposals (Sonnet)
└── evidence interpretation
```

**Do not make an LLM the root authority for permissions or mergeability.** Concretely:

- The orchestrator is a deterministic state machine (code + `.docsync/` configuration). LLM invocations are **leaf nodes** with typed, schema-validated inputs and outputs; schema-violating output is retried, then abstained to REVIEW (§3.4).
- Permissions, file selection (the constrained Sonnet workspace), thresholds, retry policy, Git operations, and policy promotion/rollback are application-owned. **Models never approve their own outputs into production** (D13).
- **Development/research orchestrator vs production orchestrator (context §37 distinction):** the development/research loop (policy architecture, evaluation experiments, this design process itself) may use sub-agents freely; the **production product must not depend on an unconstrained LLM orchestrator** for core workflow decisions. Production orchestration is code/configuration driven, with LLMs operating inside bounded stages.
- Model routing is pluggable (§10): Jev/Sonnet are the current instances of typed roles, not identities; the decision engine and agent router admit replacement models without architectural redesign.
- Prompt-injection containment: repository content passed to models is untrusted input; outputs are untrusted data validated against typed schemas; no model output influences permissions, file selection, or Git credentials.

---

## §14 Safety Invariants

The non-negotiables. **Every one is enforced in deterministic orchestrator code, never delegated to model judgment:**

1. **Never push to the default branch.** All writes land on `docsync/*` branches and surface as PRs. Merging is always a human act.
2. **Never modify code files.** Only manifest-listed documentation paths, in writable modes, are written.
3. **Never touch files outside the manifest allowlist** — including `.docsync/` internals (except through the policy-promotion pipeline) and `.github/workflows/**`.
4. **Locked, generated, and external documents are detected and reported, never modified** (four-layer enforcement, §11.4; ownership is the floor, D2).
5. **No patch is applied or shipped without passing verification**, and the confidence × risk policy gate applies even to `auto`-mode documents.
6. **Abstain, don't guess:** uncertain cases route to review; the system never forces an uncertain case into update-or-ignore (context §13).
7. **Models propose; humans promote.** No reviewer action or model output writes policy directly; all policy changes traverse the promotion pipeline (§8.5).
8. **Models never hold credentials.** The orchestrator performs every Git/API operation with short-lived, least-privilege tokens; the OpenRouter key lives only in the orchestrator environment (§10.7).
9. **Fail closed** on configuration errors, unknown API errors, and verification failures — degrade to report-only/REVIEW, never to best-effort writes (D8, D14).
10. **Provenance or it didn't happen.** Every automated change ships with its evidence record (§12.5).
11. **No flood:** PR creation is rate-limited, idempotent, and self-trigger-suppressed (§12.2).
12. **Whole-document rewrites are rejected at verification** — minimal diffs only, regardless of content quality (§4.9).
13. **Anchors don't silently move:** ambiguous claim re-anchoring routes to REVIEW (§4.3).

---

## §15 Roadmap

### 15.1 V1 — narrow, safe, useful (context §40)

```text
V1
├── GitHub App or GitHub Action
├── .docsync/manifest.yml
├── Git diff analyzer
├── deterministic file mapping
├── Jev classifier
├── Sonnet patch generator
├── patch verifier
├── confidence × risk policy
├── GitHub PR creation
└── provenance
```

Simple flow:

```text
PR
 ↓
changed files
 ↓
manifest
 ↓
Jev
 ↓
candidate docs
 ↓
Sonnet
 ↓
minimal diff
 ↓
verification
 ↓
documentation PR
```

Governance framing: V1 ships with every invariant in §14 active, report-only-by-default posture (D10), and the five reviewer actions from day one — **the feedback loop must exist as early as the patches do, or evaluation data never accumulates.** V1 fallbacks per context: manifest-declared/hand-anchored claims before automated extraction (§4.3).

### 15.2 V2 — deeper understanding, same safety surface (context §41)

```text
V2
├── symbol mapping
├── AST analysis
├── API/schema diff
├── document graph
├── claim extraction
├── historical drift analysis
└── evaluation framework
```

Framing: V2 upgrades *precision* (fewer wrong candidates, sharper mappings in the check UI) and enables claim-level reporting ("which documented claims lost support in the source of truth?") without changing developer-facing primitives — still checks, diffs, PRs. Claim-level edit objects become a natural refinement of the patch representation once claim extraction matures (same verifier applies). Loop B drift analysis goes from scheduled sweeps to historical differential analysis.

### 15.3 V3 — autonomy under governance (context §42)

```text
V3
├── autonomous background audits
├── repository-specific policy refinement
├── shadow/canary policies
├── adaptive thresholds
├── model routing
├── organizational policy
└── cross-repository documentation consistency
```

Framing: V3's autonomy items are exactly the ones that require the promotion pipeline, shadow/canary evaluation, org policy floors (§11.2), and audit-grade provenance to already be mature. Background audits and cross-repo consistency expand where findings *surface* (issues, dashboards, org reports), but **the write path never changes: branch → PR → human review.** Org policy becomes first-class; adaptive thresholds ride the same versioned-promotion rails as every other policy change; model routing (§10) graduates from per-role bindings to org-wide routing policies.

---

## §16 Risks, Assumptions & Open Items

Consolidated from the three expert-review challenge rounds. "Owner" indicates which subsystem must resolve it; "when" indicates the milestone gate.

### 16.1 Assumptions to verify (before trusting the design's numbers)

| # | Assumption | Risk if wrong | Owner / gate |
|---|---|---|---|
| A1 | Jev (the decision-engine role) returns **calibrated** per-answer probabilities at ≲1 s via OpenRouter | 0.94/0.72 thresholds meaningless; `auto` fires on miscalibrated confidence | Measure ECE/reliability per model binding; gate: V1 |
| A2 | Sonnet reliably returns **minimal anchored patches with evidence**, not rewrites | Rewrite tendency breaks verification and minimality metrics; structured-patch tooling must be built, not prompted | Benchmark on historical PRs; V1 |
| A3 | Historical PRs yield a **usable labeled benchmark** (incl. true negatives) | Evaluation framework starves; canary machinery has no ground truth | Dataset-yield study; V1–V2 |
| A4 | Claim **extraction + anchoring** works on representative docs | Tier-2 verification degrades to section-level checking | Extractor quality trial; V2 |
| A5 | "Externally observable behavior changed" is deterministically approximable (signature/type/API-surface proxies) | Detection recall drops for pure behavioral changes | Per-language AST coverage audit; V2 |
| A6 | Check-run `requested_action` button works cleanly with Checks+Contents+PR permissions | UX mockup degraded (comment-command fallback) | Platform spike; V1 |
| A7 | `GITHUB_TOKEN`/fork semantics (no downstream workflows, forks unwritable, fork visibility) | Action-variant UX differs from App | Platform spike; V1 |
| A8 | Teams accept **committed** `.docsync/state/` | Diff noise → state moved to CI-only, weakening auditability | Real-world trial; V1 |
| A9 | Minimal-diff application survives real-world Markdown (front-matter, fences, lists, renames) | `fix` idempotency guarantee weakened | 3-way-merge trials; V1 |
| A10 | Latency envelope (p50 ≤ 60 s routed) holds against real PR sizes + OpenRouter rate limits | Large diffs need sharding | Load test; V1 |

### 16.2 Top risks and mitigations

| # | Risk | Mitigation (already in architecture) |
|---|---|---|
| R1 | Miscalibrated "high confidence" feeding `auto` | Calibration monitoring per model binding; `unsafe_auto_updates = 0` promotion gate; conservative max of manifest/Jev risk (D2/D11) |
| R2 | Model/prompt drift on OpenRouter upgrades | Pin model IDs; treat model swap like a policy change with shadow/canary (D9, §10.3) |
| R3 | Canary metric gaming (inflated confidence, route-everything-to-review, eval-set quirks) | Gate on abstention quality + false-update rate, not just accuracy; per-bucket metrics; significance floor; time-boxed canaries (§8.6) |
| R4 | Threshold overfitting to history | Time-split evaluation; rolling re-evaluation; thresholds per policy version (D11) |
| R5 | Verifier false-rejects poisoning feedback data | Track verifier-vs-human disagreement as its own metric; proportional minimality bounds; rejects land in review, never silent (D13) |
| R6 | Doc PR ↔ code PR merge conflicts; stale doc PRs | One doc PR per source PR; rebase + re-verify; supersede markers; post-merge re-runs (§12.3) |
| R7 | Check-run UX noise → teams mute the check | Single aggregated check; quiet mode; sparse no-action rows; `neutral` default (D10) |
| R8 | Prompt injection via repository content | Typed-schema validation at gateway; untrusted-input containment; models never touch credentials/permissions (§10.7, §13) |
| R9 | Baseline orphaning on squash/force-push | D12 resolution ladder; fail-to-SUSPECT semantics |
| R10 | Generated-file churn / regeneration loops | Pre-Jev `formatting_only`/`generated` short-circuits; content-hash dedup of drift reports (§4.1, §11.2) |
| R11 | Abstention pathologies (over/under-abstention) | Abstain-rate + override-rate monitored as a 2-D health metric per policy version (§5.2) |
| R12 | Memory poisoning / loop collapse | Evidence-backed records with decaying confidence; contradiction recording; offline-evaluation wall between memory and production (§8.8, §8.5) |
| R13 | Feedback selection bias (auto-applied patches unverified by humans) | Periodic spot-check rate over auto-applied patches (§9) |
| R14 | Manifest drift (rotting globs, dead entries) | Scheduled manifest lint; manifest changes always trigger re-validation (§11.3) |
| R15 | Multi-writer state races (CI vs local `fix`) | CAS writes + repo-scoped concurrency queue + graceful re-run (§6.2, §6.3) |
| R16 | Cross-patch inconsistency (two docs patched contradictorily) | Batched Sonnet sessions; verifier detects same-claim-different-value across patches |
| R17 | Trigger-taxonomy drift between manifest and Jev schema | Versioned mapping layer; validation errors name expected schema version (§8.3, §11.3) |
| R18 | Confidence-display trust erosion | Display pinned to `(policy_version, model binding)` (D3) |

### 16.3 Explicitly deferred / open decisions

- **Integrated mode** (doc commits in the code PR): deferred; opt-in only after the trust loop is proven (§12.4).
- **`state/` committed vs CI-only:** V1 default committed; revisit with A8 evidence (D1 note).
- **Full org policy config:** V3; only floors are forward-declared in V1 (§11.2).
- **Claim-level edit objects** as the patch representation: V2+ refinement (§15.2).
- **External graph DB / index cache** for very large monorepos: optimization only, never truth (§3.7).
- **Model identity for "Jev" on OpenRouter:** the exact decision-model binding is a per-repository calibration choice (D9, §10.3); the Jev *role* (typed decision layer, context §5) is fixed.

---

## Appendix A — Design Methodology (context §37)

This architecture was produced by an orchestrated expert review mirroring the context's three-team structure; each team produced drafts and challenge rounds (assumptions, risks, alternatives, disagreements), and an orchestrator resolved conflicts into the decisions in §0.3.

**Team A — Systems & Architecture** (distributed-systems architect, AI/ML systems architect, developer-platform/Git architect): pipeline architecture, deterministic preprocessing, graph/baseline/state design, CI/Git integration, scalability and boundaries → §3, §4.1–§4.5, §6, §7.2, §12.2–§12.3.

**Team B — Intelligence & Decisioning** (classifier/ML engineer, agentic-AI architect, evaluation/reliability specialist): Jev decision design, Sonnet role, policy compilation, uncertainty, evaluation, feedback loops, model routing → §4.6–§4.9, §5, §8, §9, §10.

**Team C — Developer Experience & Governance** (Git/CI DX architect, documentation-information architect, security/product/governance engineer): `.docsync` configuration, user workflow, locks and permissions, reviewer feedback, provenance, safe autonomous behavior → §11, §12, §14.

**Orchestrator:** synthesizes, resolves conflicts (§0.3), owns the final document.

Important distinction carried into the product itself (context §37/§38):

- The **development/research orchestrator** may use sub-agents (as this design process did).
- The **production product** does not depend on an unconstrained LLM orchestrator for core workflow decisions: production orchestration is code/configuration driven, with LLMs operating inside bounded stages (§13).

## Appendix B — Traceability Matrix

Every section of `docSync_CONTEXT.md` maps to this document (completeness audit basis):

| Context § | Topic | Where covered |
|---|---|---|
| §1 | Original product idea, workflow, PR-first | §1.1, §12 |
| §2 | Ecosystem / research context + references | §1.2 |
| §3 | Core product thesis + question chain + positioning | §1.3 |
| §4 | Central design principle + division of knowledge | §2.1 |
| §5 | Jev + Claude Sonnet model split, workspace, duties, tripartite principle | §2.2, §4.6, §4.7 |
| §6 | Three-layer intelligence pipeline | §5.1 |
| §7 | High-level production architecture | §3.2, §3.3 |
| §8 | Deterministic pre-filtering + retrieval hierarchy + embeddings fallback | §4.1, §4.5 |
| §9 | Repository-scoped manifest + `.docsync/` + example + modes | §11.1, §11.2, §6.3 |
| §10 | Bidirectional code ↔ concepts ↔ docs mapping | §4.2 |
| §11 | Documentation as claims (+ invalidation example, headline query) | §4.3 |
| §12 | Documentation contracts (+ classification example) | §4.4 |
| §13 | Policy states + abstain path | §5.2 |
| §14 | Confidence × risk matrix + high-risk list | §5.3 |
| §15 | Repository-specific Jev policies (universal/payments/k8s/frontend) | §8.3, §11.3 (taxonomy mapping) |
| §16 | Sonnet as Policy Architect (+ 14/23 example) | §8.1 |
| §17 | Policy compilation | §8.2 |
| §18 | Example Jev policy structure (YAML, 0.94/0.72) | §8.3 |
| §19 | No direct policy mutation (bad vs preferred model) | §8.5 |
| §20 | Versioned policies (v17 example, 0.96/0.93/0, canary) | §8.4 |
| §21 | Shadow/canary (93.2% vs 96.1%) | §8.6 |
| §22 | Closed-loop learning (FN/FP examples) | §8.7 |
| §23 | Reviewer controls (5 actions, 17-overrides example) | §11.4, §8.7 |
| §24 | Self-improvement modes (off/propose/canary/auto, default propose) | §8.9 |
| §25 | Persistent memory (layout + generated-sdk pattern record) | §8.8 (D1 reconciles §25's `memory/policy-history/` to top-level `policy-history/`) |
| §26 | Two loops (Loop A / Loop B) | §7.1, §7.2 |
| §27 | Baselines (8a73f2c example, invalidation flow) | §6.1 |
| §28 | Documentation state machine (ASCII + transitions) | §6.2 |
| §29 | Patch strategy (minimal patch, no rewrites) | §4.9 |
| §30 | Evidence/provenance record (81af92e example) | §12.5 |
| §31 | Generated documentation protection (5 categories, owner/mode YAML) | §11.2 |
| §32 | CLI check/fix/pr (+ transcripts, PR #481) | §12.1 |
| §33 | CI/GitHub integration (+ check UI mockup) | §12.2, §3.3 (architectural view) |
| §34 | Separate documentation PR (5 reasons) | §12.4 |
| §35 | Evaluation framework (6 metrics, goal) | §9 |
| §36 | Policy improvement objective (0.40/0.30/0.20/0.10) | §9 |
| §37 | Orchestrator philosophy (three teams) | Appendix A |
| §38 | Orchestrator vs LLM responsibility | §13 |
| §39 | Model routing pluggable (+ future patterns) | §10 |
| §40 | Recommended V1 (components + flow) | §15.1 |
| §41 | Recommended V2 | §15.2 |
| §42 | Recommended V3 | §15.3 |
| §43 | Product moat / differentiation | §1.4 |
| §44 | Key strategic insight + other outputs | §1.4 |
| §45 | Preferred ultimate architecture (full diagram) | §3.6 |
| §46 | Final design principles (20) | §2.3 |
| §47 | One-sentence product definition | §0.1 |

**User addition (2026-10-05):** AI inference through OpenRouter → §10 (referenced from D9, §13, §15.3, §16).

## Appendix C — Glossary

| Term | Meaning |
|---|---|
| **Baseline** | The record of the code revision (commit), sources, symbols, and claims a document was last verified against (§6.1). |
| **Claim** | An atomic, anchorable statement in documentation, backed or not by source evidence (§4.3). |
| **Contract** | A named, checkable expectation a document declares over its own content (§4.4). |
| **Constrained workspace** | The bounded context (diff, analysis, decision, manifest/policy, relevant files/docs, instructions) given to the writer model — never the whole repository (§4.7). |
| **Document graph** | The explicit code ↔ concepts ↔ docs relation incl. APIs, configs, workflows (§4.2). |
| **DocPatch** | A minimal structured patch (target document, anchor, hunk) plus evidence references (§4.9). |
| **Jev** | The typed decision/classification/routing role (Stage 1), bound to a concrete model via OpenRouter (§4.6, §10). |
| **Loop A / Loop B** | The synchronous PR loop and the background drift audit (§7). |
| **Manifest** | The repository's allowlist of documents, their sources, triggers, ignores, and modes (§11.1). |
| **Policy Architect** | The Sonnet role that proposes versioned decision-policy changes (§8.1). |
| **Policy Compiler** | The deterministic validator/compiler turning policy proposals into typed Jev workflows (§8.2). |
| **Provenance** | The structured evidence record attached to every automated change (§12.5). |
| **Sonnet** | The investigation + patch role (Stage 2), bound to a concrete model via OpenRouter (§4.7, §10). |
| **Shadow/canary** | Running a candidate policy alongside production without affecting developers, then promoting on demonstrated win (§8.6). |
| **State machine** | VERIFIED → SUSPECT → CLEAR/REVIEW/STALE → PROPOSED → VERIFIED/REJECTED/CLOSED (§6.2). |

---

*End of ARCHITECTURE.md. Built from `docSync_CONTEXT.md` (47/47 sections traced in Appendix B) plus the OpenRouter inference decision (§10).*
