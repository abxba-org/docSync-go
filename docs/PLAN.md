# docSync — Implementation Plan (PLAN.md)

**Status:** Decision-ready v1.0 — 2026-10-05.
**Source of truth:** `ARCHITECTURE.md` v1.0 + `docSync_CONTEXT.md` (47/47 context sections traced via ARCH Appendix B).
**Coverage claim:** this plan covers 100% of ARCH §0–§16 and CTX §1–§47 across Phases 0–12 (§1–§13); every phase is gated by its verification list (N.5) and contributes traceability rows (N.8). §14 holds the complete coverage matrices; §15 holds the global verification & release checklist.

## Contents

- §0 Executive Overview & Build Strategy
- §1 Phase 0 — Foundation & Configuration Core
- §2 Phase 1 — Deterministic Analysis Core
- §3 Phase 2 — Model Access & Decision Layer
- §4 Phase 3 — Patch Layer (Writer)
- §5 Phase 4 — Verification Engine
- §6 Phase 5 — Orchestrator & Loop A End-to-End
- §7 Phase 6 — Git & PR Integration
- §8 Phase 7 — CLI (check / fix / pr)
- §9 Phase 8 — CI & GitHub Integration (Action + App)
- §10 Phase 9 — Provenance & Safety Hardening
- §11 Phase 10 — Evaluation, Feedback & Policy Lifecycle Foundation
- §12 Phase 11 — V2: Deeper Understanding
- §13 Phase 12 — V3: Autonomy Under Governance
- §14 Traceability & Coverage Matrices
- §15 Global Verification & Release Checklist

---

## §0 Executive Overview & Build Strategy

### 0.1 Objective & definition of done

Build docSync exactly as specified in `ARCHITECTURE.md`: a Git-native documentation consistency engine that builds a repository-specific decision policy, detects documentation drift from code changes, uses Jev for structured routing and the patch-writer role (Sonnet-class) for evidence-backed minimal patches, and continuously improves its policy through evaluated feedback and safe versioned promotion.

**Definition of done (V1):** Phases 0–10 complete with every phase gate (N.5) green; all 13 safety invariants [ARCH §14] enforced by executable tests (Phase 9 suite); all assumptions A1–A10 verified or explicitly dispositioned; report-only-by-default posture (D10) confirmed; `docsync check/fix/pr` CLI complete; one-doc-PR-per-source-PR (D4) and rate limits active. §15.2 is the formal V1 release gate. Phases 11–12 deliver V2 [ARCH §15.2] and V3 [ARCH §15.3] under their own gates (§15.3/§15.4).

**Plan coverage:** 100% of ARCH §0–§16 + CTX §1–§47 is mapped in §14.1–§14.2; every phase is gated by its verification list (N.5); no phase proceeds with a red gate.

### 0.2 Build-order overview

| Phase | Title | Owns | Gate highlight |
|---|---|---|---|
| 0 | Foundation & Configuration Core | Monorepo, schemas, `.docsync/` governance core, fail-closed validation, state store (CAS) | Clean-checkout green; fail-closed with zero AI-stage invocations; CAS/allowlist/ownership-floor verified |
| 1 | Deterministic Analysis Core | ChangeAnalysis, document graph, claims, contracts, retrieval 1–6 interface (levels 1–2, 5, 6 active; 3–4 inactive), baselines (D12), state machine; symbol/AST stubs only — type-aware invalidation deferred to Phase 11 | Purity + zero model invocations; deterministic mapping-based invalidation; D12 squash/force-push sims |
| 2 | Model Access & Decision Layer | `@docsync/openrouter`, Jev runtime, decision object, policy compiler/versioning, policy engine, calibration harness | Abstain-safe, provenance-pinned Stage 1; matrix + calibration gates; no network tests |
| 3 | Patch Layer (Writer) | `patch-writer` role, constrained workspace, DocPatch schema, application engine, guard integration | Minimal content-anchored patches only; rewrites structurally impossible; guard/race/idempotency proven |
| 4 | Verification Engine | Three-tier verification, VerificationReport, cross-patch consistency, judge independence | Every adversarial patch class handled with correct tier records; rejects fail closed |
| 5 | Orchestrator & Loop A E2E | Deterministic stage graph, degradation matrix, idempotency/caching, RunOutcome API | Loop A E2E matrix green; A10 load probe; RunOutcome frozen for Phases 7–8 |
| 6 | Git & PR Integration | Doc PR write path: branch factory, rate/burst/idempotency, merge-order, PR filter (layer 4), feedback capture | Deduplicated, allowlist-filtered doc PR with provenance + `state/` carriage; zero invariant violations |
| 7 | CLI (check / fix / pr) | Three verbs, shared renderer, exit codes 0/1/2/3, local-loop safety rails | Fixture matrix green; byte-comparable CLI/CI facts |
| 8 | CI & GitHub Integration (Action + App) | A6/A7 spikes, GitHub App + Action, check-run lifecycle, escalation, doc-PR via CI | Spikes resolved; App + Action E2E green; secrets boundary tests pass |
| 9 | Provenance & Safety Hardening | `docsync.provenance/v1`, D5 precedence, four-layer lock suite, 13-invariant tests, injection corpus, audit replay | All 13 invariants green as executable tests; degradation matrix as tests |
| 10 | Evaluation, Feedback & Policy Lifecycle Foundation | Feedback recorder/hygiene, benchmark dataset, six metrics, Policy Architect, promotion pipeline v1, memory, `learning.mode` | Replay→label→metrics→proposal→promotion loop E2E; gates enforced; A8 decision documented |
| 11 | V2: Deeper Understanding | Symbol/AST analysis, claim extraction (proposal-only), Loop B, A4/A5 trials | Symbol→claim invalidation with zero LLM; Loop B advisory findings reuse Loop-A path |
| 12 | V3: Autonomy Under Governance | Shadow/canary productionization, `learning.mode=auto`, adaptive thresholds, org floors, cross-repo consistency | Gates trustworthy; write path branch→PR→human review in every test |

### 0.3 Phase dependency graph

```text
Phase 0 (foundation/config core)
   │
   ▼
Phase 1 (deterministic analysis)
   │
   ▼
Phase 2 (model access & decision layer)
   │
   ├──────────────┐
   ▼              ▼
Phase 3        Phase 4        ← 3 & 4 can start after 2 (independent tracks);
(writer)       (verification)    both must integrate by Phase 5
   │              │
   └──────┬───────┘
          ▼
      Phase 5 (orchestrator — Loop A E2E)
          │
   ┌──────┼──────────┐
   ▼      ▼          ▼
Phase 6  Phase 7   Phase 8    ← 6, 7, 8 can run in parallel after 5
(Git/PR) (CLI)     (CI: App+Action)   (all consume the frozen RunOutcome API)
   └──────┼──────────┘
          ▼
      Phase 9 (provenance & safety hardening — all 13 invariants as tests)
          │
          ▼
      Phase 10 (evaluation, feedback & policy lifecycle foundation)
          │
          ▼
      Phase 11 (V2 — deeper understanding)
          │
          ▼
      Phase 12 (V3 — autonomy under governance)
```

Parallelism notes: Phases 3 & 4 both depend on Phase 2 and are mutually independent (Phase 4 consumes Phase 3's DocPatch schema via fixture patches — the schema lands early in Phase 3; integration is enforced at the Phase 5 gate). Phases 6, 7, 8 all consume the Phase 5 single-run pipeline API (RunOutcome, frozen at V-P5-16) and can proceed concurrently; Phase 8 consumes Phase 6 PR primitives, so PR-creation wiring inside Phase 8 starts after Phase 6's client seam is available (or shares the same track). Phase 9 hardens the shipped Phases 0–8; Phase 10 consumes Phase 9's provenance/invariant suite; Phases 11 and 12 are sequential (12 needs 10's promotion pipeline + 11's Loop B/understanding).

### 0.4 Mapping to V1 / V2 / V3

- **V1 [ARCH §15.1]** — delivered by Phases 0–10: GitHub App + Action, `.docsync/manifest.yml`, Git diff analyzer, deterministic file mapping, Jev classifier, patch generator, patch verifier, confidence × risk policy, GitHub PR creation, provenance. V1 ships with every invariant in [ARCH §14] active, report-only-by-default posture (D10), and the five reviewer actions from day one (feedback loop exists as early as the patches do).
- **V2 [ARCH §15.2]** — Phase 11: symbol mapping, AST analysis, API/schema diff, document graph upgrade, claim extraction, historical drift analysis, evaluation framework deepening — precision upgrades without changing developer-facing primitives.
- **V3 [ARCH §15.3]** — Phase 12: autonomous background audits, repository-specific policy refinement, shadow/canary policies, adaptive thresholds, model routing, organizational policy, cross-repository consistency — write path never changes: branch → PR → human review.
- **Invariants active from day one:** all 13 [ARCH §14] apply from the first commit of Phase 0 (enforced as executable tests by Phase 9; structural enforcement begins in Phases 0–3, write-path enforcement in Phases 6–8). Exit codes 0/1/2/3 are defined by the [ARCH §12.1] contract — fail-closed semantics (exit 2, D8) are established in Phase 0, implemented in Phase 7, and consumed by Phase 8 — and are never relaxed.

### 0.5 Orchestrator decisions & conventions

Orchestrator-level conventions adopted before any phase work:

- **OD-1 — Stack:** TypeScript on Node ≥ 20; pnpm-workspaces monorepo with ten packages: `@docsync/core`, `@docsync/cli`, `@docsync/github-app`, `@docsync/action`, `@docsync/schemas`, `@docsync/openrouter`, `@docsync/verifier`, `@docsync/policy`, `@docsync/analyzer-typescript`, `@docsync/analyzer-generic`.
- **OD-2 — Tooling:** vitest, eslint, prettier; zod as the schema source of truth with automated JSON Schema export in `@docsync/schemas` (single source for all cross-package contracts); V1 is files-only — repository files only, no external database.
- **OD-3 — Committed state in V1:** `.docsync/state/` is committed to Git in V1; per-run state-churn counters (Phase 0, P0-T15) make the A8 revisit data-driven; the decision point (keep committed vs CI-only) is documented in Phase 10 (F10-17).
- **OD-4 — Analyzer plugin order:** `@docsync/analyzer-typescript` (tree-sitter) first; `@docsync/analyzer-generic` (path-level) always present; languages without tooling degrade with a declared coverage gap in the run record — never silent [D14].

#### Global open-decisions registry

The chunk writers produced open decisions with colliding numbering (teamA: OD-11–18; teamB1: OD-21–23; teamB2: OD-31–41; teamC1: OD-41–46 — OD-41 collided between teamB2 and teamC1; teamC2: OD-51–55). All are renumbered here into one sequential registry. Every entry is **RESOLVED — the writer's stated recommendation is adopted** (each writer included one). In-text references in §1–§13 use the OD-R numbers below; the original writer numbering is noted for provenance. Two overlapping pairs were reconciled: teamC1 OD-41 + teamC2 OD-54 merged into OD-R12; teamA OD-14 cross-references teamB1's policy-versioning implementation ([ARCH §8.4], Phases 2–3).

| ID | Question | Resolution | Phase | Notes |
|---|---|---|---|---|
| OD-R01 | Unknown-key policy for `manifest.yml` — reject vs forward-compatible ignore? (was teamA OD-11) | RESOLVED — reject (fail-closed per D8); forward evolution via explicit `version` bumps with migration notes | 0 | Loader rejects unknown keys with line-precise errors |
| OD-R02 | Glob engine — picomatch + specificity wrapper vs hand-rolled matcher? (was teamA OD-12) | RESOLVED — picomatch with a thin wrapper encoding the §11.3 specificity/tie rules; property tests pin the semantics | 0 | |
| OD-R03 | Schema-registry enforcement for `state/` artifacts — mandatory registry vs free-form JSON? (was teamA OD-13) | RESOLVED — mandatory registry in `@docsync/schemas`; writers fail closed on schema drift | 0 | Audit-friendliness outweighs flexibility |
| OD-R04 | `policy.yml` vs `policy-history/` layout mechanics — live pointer + immutable artifacts vs single evolving file? (was teamA OD-14) | RESOLVED — `policy.yml` as live pointer; `policy-history/` stores immutable versioned artifacts; final shape settled with the Phase 2–3 policy-engine implementation review | 0 | Cross-reference: Phase 2 (§3) implements policy versioning + `policy-history/` retention per [ARCH §8.4]; Phase 0 ships the layout/loader contract |
| OD-R05 | Symbol-index persistence — per-blob-SHA cache under `state/` vs ephemeral per-run compute? (was teamA OD-15) | RESOLVED — ephemeral by default; opt-in cache flag for monorepos | 1 | Keeps `state/` lean for A8; Phase 11 builds the per-blob-SHA cache properly |
| OD-R06 | Depth of semantic config diffs in V1 — key-level vs typed value parsing? (was teamA OD-16) | RESOLVED — key-level + typed value parsing for a small family set (env, JSON, YAML); deeper rules deferred as policy data | 1 | Phase 11 deepens (F11-5) |
| OD-R07 | Decision-engine role default — native structured-output model vs cheapest JSON-capable + constrained prompting? (was teamB1 OD-21) | RESOLVED — native structured output for `decision-engine` and `judge`; `models.yml` may downgrade with a documented calibration re-check; choice recorded per policy version | 2 | |
| OD-R08 | DocPatch hunks — claim-id references vs independent evidence refs? (was teamB1 OD-22) | RESOLVED — carry both when a claim id exists; independent machine-resolvable evidence refs are the mandatory minimum | 3 | Patches stay valid on sparse-claim repositories (V1 fallback) |
| OD-R09 | Behavioral predicates in V1 — Tier 2 abstain to `suggest` vs defer to Tier 3 judge? (was teamB1 OD-23) | RESOLVED — abstain to `suggest` with an explicit `behavioral_predicate_unverified` check record; Tier 3 alone is not sufficient for `auto` on behavioral claims; full predicate checking with Phase 11 | 4 | |
| OD-R10 | Concurrency for parallel per-doc investigation — in-process pool vs worker-thread isolation? (was teamA OD-17) | RESOLVED — in-process p-limiter pool with per-role concurrency caps; worker isolation only if the A10 load probe shows memory pressure | 5 | |
| OD-R11 | LLM result cache store — content-addressed files under `state/` vs in-memory-only? (was teamA OD-18) | RESOLVED — content-addressed files (cross-run reuse + auditability) with LRU eviction of cache entries only — never state records; final call at implementation review with Phase 2 | 5 | |
| OD-R12 | Do doc PRs carry `.docsync/state/` updates, and how do check-only CI runs persist archival records? (reconciles teamC1 OD-41 + teamC2 OD-54) | RESOLVED — doc PRs carry their `state/` updates for PR-triggered runs (single reviewable audit trail, no second automation PR stream); check-only CI runs persist archival records out-of-band (CI artifact + optional repo-dispatch commit job); the PR body stays canonical per D5 either way; A8 diff-noise evidence from the trial decides whether to split later | 6, 9 | Invariant 1 forbids default-branch pushes, so some reviewable carrier is required either way; revisit with A8 evidence after the real-world trial |
| OD-R13 | Reviewer override input mechanism in V1 — labels vs comment commands vs check-run buttons? (was teamC1 OD-42) | RESOLVED — labels (`docsync:intentional`, `docsync:ignore-pattern`) + native review actions in V1; button escalation with the Phase 8 check-run surface; no free-text comment grammar in V1 | 6 | |
| OD-R14 | Feedback capture fidelity for `edit` when squash/amend destroys the intermediate edit diff? (was teamC1 OD-43) | RESOLVED — derive `corrected` diff from PR-head comparison (base vs final); store both review-event diff (when present) and final-diff basis; Phase 10 evaluates which basis is more informative | 6 | |
| OD-R15 | Ship a minimal `docsync init` in the CLI? (was teamC1 OD-44) | RESOLVED — yes, a thin scaffolding command; schema authority and validation remain with Phase 0; init output must validate cleanly against the Phase 0 loader before exit 0 | 7 | |
| OD-R16 | Offline/reduced mode — `check --offline` without `OPENROUTER_API_KEY`? (was teamC1 OD-45) | RESOLVED — support `check --offline` with deterministic analysis + explicit "decision unavailable" rows (never silently skipping Jev, D14); exit 1 with an offline marker | 7 | |
| OD-R17 | `fix` when multiple unrelated candidate docs exist — patch all vs per-doc selection? (was teamC1 OD-46) | RESOLVED — default patches all verified docs; `--doc <path>` selectors; no interactive wizard in V1 (scriptability over chat-like UX) | 7 | |
| OD-R18 | If A6 fails, what form may the comment-command escalation fallback take without violating the no-chatbot DX principle? (was teamC2 OD-51) | RESOLVED — a single deterministic slash command (e.g., `/docsync patch`) parsed by the App, whose only response is a mechanical acknowledgment + check link; no AI prose; if that still reads conversational, check-only + "run `docsync fix` locally" guidance | 8 | A6 spike decides go/no-go at phase entry |
| OD-R19 | Default state of the post-merge `push` handler in V1? (was teamC2 OD-52) | RESOLVED — implemented but default-off per repo (opt-in via App settings); revisit default-on once Phase 10 evaluation data exists | 8 | |
| OD-R20 | Operational criteria for the auto-proposal opt-in? (was teamC2 OD-53) | RESOLVED — reviewed-manifest marker (reviewer + review commit, schema-validated) AND zero coverage warnings AND an org-floor pass; exact gating finalized in Phase 10; ships default-off | 8 | |
| OD-R21 | Safety-incident notification channel in V1, given the App holds no Issues permission? (was teamC2 OD-55) | RESOLVED — structured incident record + check-run annotation + `failure` conclusion on the offending run + optional repo-configured outbound webhook; Issues permission (or org-level alerting) is a V2 decision with its own permission-rationale review | 9 | |
| OD-R22 | Storage location for raw feedback rows — `state/feedback/` vs `evaluation/`? (was teamB2 OD-31) | RESOLVED — raw append-only rows under `state/feedback/` (system-written archival, D5-style precedence); curated datasets + eval reports under `evaluation/`; exact subpath settled at implementation review | 10 | |
| OD-R23 | GitHub mechanism for the five reviewer signals — derive from PR events vs explicit check-run controls? (was teamB2 OD-32) | RESOLVED — hybrid: accept/edit/reject derived from doc-PR events + reason template; mark-as-intentional + always-ignore via check-run `requested_action` with a deterministic comment-command fallback; parsing owned by the DX layer; records in the shared structured format | 10 | |
| OD-R24 | Significance-floor defaults for promotion gates — minimum sample size and test? (was teamB2 OD-33) | RESOLVED — ≥100 labeled disagreements per comparison and a two-proportion test at α=0.05; repo-configurable upward, never downward (the floor cannot be disabled); revisit with Phase 12 data | 10 | |
| OD-R25 | Default spot-check rate over auto-applied patches? (was teamB2 OD-34) | RESOLVED — 10% (minimum 1 per 50), budget-capped per repo; versioned policy data; findings feed eval labels | 10 | |
| OD-R26 | Claim-level edit-object scope — hunk-attached claim references vs full claim-diff representation? (was teamB2 OD-35) | RESOLVED — V2 ships hunk-attached verified claim references; a full claim-diff representation only if Phase 12 evaluation shows verifier/reviewer benefit; [ARCH §16.3] formally deferred with this recommendation | 11 | |
| OD-R27 | Loop B default trigger — nightly only vs nightly + post-merge? (was teamB2 OD-36) | RESOLVED — nightly default; post-merge optional per-repo config (cost control); both never on the PR critical path | 11 | |
| OD-R28 | OpenAPI delta taxonomy granularity — binary vs three-tier? (was teamB2 OD-37) | RESOLVED — three-tier (breaking / behavior-annotating / non-breaking); the middle tier maps to "possible documentation impact" triggers | 11 | |
| OD-R29 | Live-shadow sample fraction + eligibility? (was teamB2 OD-38) | RESOLVED — default 10% of PRs in repos opted into canary/auto (opt-out per repo), capped per org; the fraction is versioned policy data | 12 | |
| OD-R30 | Org-floor storage + distribution — App settings vs org policy repository file? (was teamB2 OD-39) | RESOLVED — an org policy repository file (reviewable in PRs — "policy must be reviewable in Git"); the App fetches + hash-pins it into run records so enforcement is auditable | 12 | |
| OD-R31 | Integrated-mode promotion criteria — what evidence suffices to opt in? (was teamB2 OD-40) | RESOLVED — ≥90 days in `auto` with ≥200 auto-applied patches, false-update rate < 2%, zero safety incidents, and org-floor review; remains opt-in per repo, never the default | 12 | [ARCH §12.4, §16.3] deferral gate |
| OD-R32 | Cross-repo report surface — org-level reports vs per-repo issues vs external dashboard? (was teamB2 OD-41) | RESOLVED — a scheduled report (issue/PR) on the org policy repository; per-repo issues only for actionable findings; a standalone dashboard deferred until org adoption justifies it | 12 | |

### 0.6 Cross-cutting invariants

- The **13 safety invariants** [ARCH §14] apply to every phase, from Phase 0 onward; Phase 9 maps each to ≥1 executable test/gate and re-asserts them at the hardened boundary.
- **Exit codes 0/1/2/3** [ARCH §12.1]: `0` no drift; `1` drift detected; `2` configuration/manifest error (fail-closed before any AI stage, D8); `3` runtime failure (fail-closed, D14). Defined once, consumed by Phase 8's conclusion mapping.
- **Decision object `docsync.decision/v1`** [ARCH §5.4] is the wire contract for Stage 1; `VerificationReport` [ARCH §4.8] and `docsync.provenance/v1` [ARCH §12.5] are the Stage 3 and provenance contracts.
- **Canonical `.docsync/` layout per D1** [ARCH §0.3 D1]: `manifest.yml`, `policy.yml`, `concepts.yml`, `instructions.md`, `models.yml`, `evaluation/`, `memory/{decisions,known-patterns,exceptions,failures}.md`, `policy-history/` (top level), system-written `state/{baselines,claims,docs,runs,audit-cursor}`.
- **Terminology per ARCH Appendix C** throughout: Jev (typed decision role), Sonnet/patch-writer (investigation + patch role), DocPatch, claim, contract, baseline, constrained workspace, document graph, Loop A/Loop B, manifest, Policy Architect, Policy Compiler, provenance, shadow/canary, state machine.

### 0.7 How to execute this plan

For each phase (§1–§13):

1. Confirm the entry gate (N.2) — prerequisites from prior phases are green.
2. Implement the TODO list (N.4), in order.
3. Satisfy the verification list (N.5) and the Gate line — the phase is complete only when the Gate condition holds; a red gate stops progression.
4. Record the phase's traceability contributions (N.8) into the §14 matrices as evidence lands.
5. Changes to any decision (D1–D14, OD-R registry) require a versioned policy/config change traversing the promotion pipeline — never silent edits. New open decisions discovered during a phase are appended to the OD-R registry with a recommendation before the phase gate closes.

## §1 Phase 0 — Foundation & Configuration Core

### 1.1 Objective & scope

Phase 0 creates the executable foundation of docSync: the pnpm-workspaces monorepo (OD-1), the shared toolchain (OD-2), the versioned schema package, and the repository governance core — the canonical `.docsync/` layout (D1), the manifest as allowlist (D7), document modes and ownership floors, config precedence with fail-closed validation (D8), and the system-owned `state/` storage primitives (CAS writes, run keying) that every later phase consumes [ARCH §6.3, §11.1–§11.3]. Phase 0 delivers no AI behavior; it guarantees every later stage starts from a validated, reviewable, fail-closed configuration surface. Out of scope: analyzers (Phase 1), model access and policy (Phase 2), writer/verifier (Phases 3–4), orchestration (Phase 5), Git/PR writes (Phase 6), CI plumbing (Phase 8).

### 1.2 Prerequisites & entry gate

- Repo currently contains only `ARCHITECTURE.md` and `docSync_CONTEXT.md`; no code exists — Phase 0 starts from an empty workspace.
- Node.js ≥ 20 and pnpm available [OD-1]; no external database in V1 — repository files only (D1 files-only posture, OD-2) [ARCH §6.3].
- Entry gate: `pnpm install && pnpm -r lint && pnpm -r typecheck && pnpm -r test && pnpm -r build` green on the scaffold; `@docsync/schemas` exports JSON Schema for every zod schema; fixture repositories and the golden-file harness are in place and CI-ready (workflow wiring itself is Phase 8).

### 1.3 Features & deliverables

Monorepo & toolchain:
- pnpm workspaces monorepo with the ten packages `@docsync/core`, `@docsync/cli`, `@docsync/github-app`, `@docsync/action`, `@docsync/schemas`, `@docsync/openrouter`, `@docsync/verifier`, `@docsync/policy`, `@docsync/analyzer-typescript`, `@docsync/analyzer-generic` [OD-1].
- TypeScript strict mode; vitest; eslint + prettier enforced; shared tsconfig baseline; per-package build/test scripts; changesets-based versioning policy [OD-2].
- `@docsync/schemas`: every cross-package contract as a zod schema with automated JSON Schema export — `ChangeAnalysis` draft, decision object (`docsync.decision/v1`), `DocPatch`, `VerificationReport`, manifest/config schemas, provenance record, run records [OD-2; ARCH §3.3, §4.1, §4.8, §4.9, §5.4, §12.5].

Governance core (`.docsync/`):
- Canonical layout exactly per D1: `manifest.yml`, `policy.yml`, `concepts.yml`, `instructions.md`, `models.yml`, `evaluation/`, `memory/{decisions,known-patterns,exceptions,failures}.md`, `policy-history/` (top level, not under `memory/`), and system-written `state/{baselines,claims,docs,runs,audit-cursor}` [D1; ARCH §6.3; CTX §9, §25].
- Loader resolving the canonical tree; layout violations are configuration errors [D1; ARCH §6.3].
- Manifest schema (`version`, `documents[]` with `path`/`mode`/`sources`/`triggers`/`ignore`) with the allowlist contract: a document absent from the manifest is never scanned and never patched; unmapped-but-changed docs surface as "unknown coverage", never silently ignored [D7; ARCH §11.1].
- Five document modes (`auto`, `suggest`, `locked`, `generated`, `human`) plus ownership tracking (human-authored / AI-maintained / generated / external / locked) as a pure effective-mode resolution function; **ownership is the floor** (D2): `locked` resolves `blocked` at any confidence; `generated`/`external` are excluded from patch candidates [D2; ARCH §11.2; CTX §31].
- Governance floors: org-floor inputs produce validation errors (never silent clamps); `locked` is terminal — no flag or policy layer converts it; the only path to a mode change is a human PR editing `manifest.yml` [ARCH §11.2, §11.4].
- Config precedence engine (most restrictive wins): invocation flags (may tighten only — never loosen modes or bypass locks) > per-document entries > manifest `defaults:` block > org floors > built-in defaults (`suggest`, learning `propose`, check-only, abstain-on-uncertainty) [ARCH §11.3].
- Fail-closed manifest/policy validation (D8): hard errors halt before any AI stage with line-precise diagnostics — YAML/schema violations, unknown `mode`, missing/duplicate `path`, unsupported `version`, trigger/ignore values absent from the decision schema, effective mode conflicting with an org floor, globs escaping the repository or matching `.docsync/` internals / `.github/workflows/**`; warnings (zero-match globs, nonexistent `path`, coverage lint findings) annotate but proceed; no last-known-good fallback [D8; ARCH §11.3].
- Versioned trigger-taxonomy mapping layer: user-facing `triggers:`/`ignore:` vocabulary → engine decision schema; validation errors name the expected schema version [ARCH §8.3.1, §11.3; R17].
- Glob engine: specificity ordering; on tie the more restrictive mode wins; between equally-restrictive entries the document routes to both candidates; escape guards (repo boundary, `.docsync/` internals, `.github/workflows/`) [ARCH §11.3].
- Environment-variable policy: credentials and endpoints only — never policy; config lint rejects policy arriving via env [ARCH §11.3].
- Config discovery + line-precise error rendering (file:line, expected schema version) exposed as a reusable core API for the CLI (Phase 7) [ARCH §11.3; CTX §32].

State storage core:
- `state/` writers for `baselines/<doc>.json`, `claims/<id>.json`, `docs/<path>.json`, `runs/<key>.json`, `audit-cursor` [ARCH §6.3].
- All state writes compare-and-swap on expected-previous content hash; repo-scoped concurrency-queue interface; the loser of a race re-runs evaluation rather than overwriting [ARCH §6.3; R15].
- Run-record keying `(repo, base_sha, head_sha, manifest_version, policy_version)` with upsert semantics; append-only transition logs; nothing deleted — full replay of "why was this doc changed on this commit" [ARCH §3.5, §6.2, §6.3].
- Config/state write-boundary guards: state writers refuse config paths and vice versa (config is human-owned and PR-reviewed; `state/` is system-written) [ARCH §6.3].
- Committed-`state/` posture (OD-3): V1 default commits `state/` to Git; per-run state-churn counters are emitted into run records so the A8 revisit is data-driven [OD-3; ARCH §6.3, §16.1 A8].

Test kit & fixtures:
- Fixture repositories (payments-style with the `idempotencyKey` case, docs-heavy, monorepo-scale path sets) and a golden-file harness [ARCH §11.1, §4.1].
- Property-based test utilities (glob resolution, precedence, CAS races); in-memory state-store fake and `GitPlatformFake` stub for later phases [ARCH §6.3, §12.2].
- Reference documentation for `.docsync/` (manifest fields, modes, precedence, error taxonomy) [CTX §9; ARCH §11].

### 1.4 TODO list

- [ ] P0-T01 — Scaffold pnpm workspace with the ten packages (OD-1), TS strict, shared tsconfig/eslint/prettier/vitest config [OD-1; OD-2]
- [ ] P0-T02 — `@docsync/schemas`: zod schema set + automated JSON Schema export; versioned schema registry [OD-2; ARCH §5.4]
- [ ] P0-T03 — `.docsync/` canonical layout constants + loader per D1; layout-violation errors [D1; ARCH §6.3; CTX §25]
- [ ] P0-T04 — Manifest schema + zod validation (`path`/`mode`/`sources`/`triggers`/`ignore`) [ARCH §11.1; CTX §9]
- [ ] P0-T05 — Effective-mode resolution: five modes + ownership; locked-terminal enforcement (D2); generated/external excluded from candidates [D2; ARCH §11.2; CTX §31]
- [ ] P0-T06 — Org-floor inputs → validation errors (no clamps); floor-conflict detection [ARCH §11.2, §11.4]
- [ ] P0-T07 — Precedence engine incl. flags-tighten-only rule; property tests [ARCH §11.3]
- [ ] P0-T08 — Fail-closed validator (D8): hard-error/warning taxonomy with file:line diagnostics; halt before AI stages [D8; ARCH §11.3]
- [ ] P0-T09 — Trigger-taxonomy versioned mapping layer; errors name expected schema version [ARCH §8.3.1; R17]
- [ ] P0-T10 — Glob engine: specificity, tie→restrictive, equal→both; escape guards [ARCH §11.3]
- [ ] P0-T11 — Manifest lint (zero-match globs, dead entries, coverage defects) as warnings + lint entry point for scheduled runs (Phase 8) [ARCH §11.3; R14]
- [ ] P0-T12 — State store: writers for baselines/claims/docs/runs/audit-cursor with CAS(expected-hash) + concurrency-queue interface [ARCH §6.3; R15]
- [ ] P0-T13 — Run-record keying + upsert; append-only transition-log primitives [ARCH §3.5, §6.2, §6.3]
- [ ] P0-T14 — Config/state write-boundary guards (bidirectional refusal) [ARCH §6.3]
- [ ] P0-T15 — A8 instrumentation: state-churn counters per run/PR in run records [ARCH §16.1 A8; OD-3]
- [ ] P0-T16 — Env-var policy enforcement (credentials/endpoints only) in config lint [ARCH §11.3]
- [ ] P0-T17 — Fixture repositories + golden-file harness + property-based utils + state/GitPlatform fakes [ARCH §11.1, §12.2, §6.3]
- [ ] P0-T18 — Reference docs for the `.docsync/` config surface [CTX §9; ARCH §11]

### 1.5 Verification list

- [ ] V-P0-1 — Clean-checkout bootstrap: `pnpm install && pnpm -r lint && pnpm -r typecheck && pnpm -r test && pnpm -r build` green [OD-1; OD-2]
- [ ] V-P0-2 — Every zod schema round-trips to JSON Schema and validates a representative instance [OD-2; ARCH §5.4]
- [ ] V-P0-3 — Loader resolves exactly the D1 tree; misplaced file (e.g., `policy-history/` under `memory/`) is a config error [D1; ARCH §6.3]
- [ ] V-P0-4 — Allowlist: manifest-absent doc excluded from candidate enumeration and flagged "unknown coverage" (D7 contract test) [D7]
- [ ] V-P0-5 — Ownership floor: `locked` doc + classifier confidence 0.99 still resolves `blocked`; `generated`/`external` never in patch candidates [D2]
- [ ] V-P0-6 — Floor violation (e.g., `docs/security/**` mode `auto` above floor `suggest`) is a validation error, not a clamp [ARCH §11.2]
- [ ] V-P0-7 — Precedence property test: no invocation-flag combination loosens a mode or bypasses a lock [ARCH §11.3]
- [ ] V-P0-8 — Every hard-error class yields a line-precise config error and zero AI-stage invocations (asserted via no-op model stub) [D8]
- [ ] V-P0-9 — Unknown trigger value error names the expected decision-schema version [R17]
- [ ] V-P0-10 — Glob tests: specificity wins; tie → more restrictive mode; equal → both candidates [ARCH §11.3]
- [ ] V-P0-11 — CAS race: two concurrent writers; loser detects hash mismatch and re-runs, never overwrites [R15; ARCH §6.3]
- [ ] V-P0-12 — Run-record upsert: same key twice → single record; transition logs append-only [ARCH §3.5, §6.2]
- [ ] V-P0-13 — Write-boundary: state writer refuses `manifest.yml`; config writer refuses `state/` [ARCH §6.3]
- [ ] V-P0-14 — A8 counters present in run records after a simulated run [A8; OD-3]
- [ ] V-P0-15 — Zero-match glob and nonexistent `path` produce warnings; run proceeds, annotated [ARCH §11.3]

Gate: phase complete when the workspace is green from a clean checkout, every hard-error class fails closed with line-precise diagnostics and zero AI-stage invocations, CAS/upsert/append-only semantics pass concurrency tests, allowlist + ownership-floor behavior is verified, and the fixture harness can host Phase 1–5 test suites.

### 1.6 Risks, assumptions & decisions

Assigned gates:
- **A8 (teams accept committed `state/`)** — V1 default is committed `state/` (OD-3); Phase 0 ships churn counters (P0-T15) that later phases surface in doc PRs, making the post-trial revisit data-driven; the CI-only alternative is documented but deliberately not built [ARCH §16.1 A8, §6.3; OD-3].
- **R14 (manifest drift)** — manifest lint (zero-match globs, dead entries, coverage defects) ships here as a library + CLI-ready entry point; scheduling it in CI is Phase 8 [ARCH §16.2 R14, §11.3].
- **R15 (multi-writer state races)** — CAS-on-expected-hash + repo-scoped concurrency queue + graceful re-run, implemented once in the state core and consumed by Phases 2/5/6/7 [ARCH §16.2 R15, §6.3].
- **R17 (trigger-taxonomy drift)** — versioned mapping layer with schema-version-naming errors [ARCH §16.2 R17, §8.3.1, §11.3].

Decisions taken:
- D1 adopted verbatim (reconciled layout; `policy-history/` top-level; `memory/` holds exactly the four files) [ARCH §0.3 D1].
- D7 implemented at the loader level: the manifest is the allowlist, not a hint [ARCH §0.3 D7].
- D8 implemented as a pre-flight gate running before any AI stage; no last-known-good fallback [ARCH §0.3 D8].
- D2 embedded in the effective-mode resolution function so no downstream stage can bypass ownership [ARCH §0.3 D2].
- Schema single-source: all cross-package contracts live in `@docsync/schemas` (zod → JSON Schema) to prevent inter-package drift [OD-2].
- YAML parsing via the `yaml` package (preserves line/column for line-precise diagnostics) [ARCH §11.3].

Alternatives considered:
- Last-known-good config fallback on validation failure (rejected: D8 fail-closed; a broken manifest is a broken safety contract) [ARCH §0.3 D8].
- Separate config-store service/DB (rejected: V1 is files-only; repo-local files are reviewable in PRs) [OD-2; ARCH §6.3].
- Silent clamping of floor violations (rejected: §11.2 requires validation errors so misconfiguration stays visible) [ARCH §11.2].

> OPEN DECISION (OD-R01): unknown-key policy for `manifest.yml` — reject unknown top-level/entry keys vs forward-compatible ignore. — Recommendation: reject (fail-closed per D8); forward evolution happens via explicit `version` bumps with migration notes.

> OPEN DECISION (OD-R02): glob engine implementation — picomatch + specificity-scoring wrapper vs hand-rolled matcher. — Recommendation: picomatch with a thin wrapper encoding the §11.3 specificity/tie rules; property tests pin the semantics.

> OPEN DECISION (OD-R03): schema-registry enforcement for `state/` artifacts — every state file validated against a registered schema vs free-form JSON. — Recommendation: mandatory registry in `@docsync/schemas`; writers fail closed on schema drift; audit-friendliness outweighs flexibility.

> OPEN DECISION (OD-R04): `policy.yml` vs `policy-history/` layout mechanics — live pointer + immutable versioned artifacts vs single evolving file. — Recommendation: `policy.yml` as live pointer; `policy-history/` stores immutable versioned artifacts (§8.4); final shape settled with the Phase 2–3 policy-engine implementation review.

### 1.7 Architecture traceability

Covered: [ARCH §0.3 D1, D2, D7, D8], [ARCH §3.5] (run keying), [ARCH §4.1/§4.8/§4.9] (downstream schema drafting), [ARCH §5.4] (decision-object schema), [ARCH §6.2] (transition-log primitives), [ARCH §6.3], [ARCH §8.3.1] (taxonomy mapping layer), [ARCH §11.1], [ARCH §11.2], [ARCH §11.3], [ARCH §11.4] (locked-terminal + floor interface), [ARCH §12.2] (GitPlatformFake stub), [ARCH §12.5] (provenance record schema), [ARCH §16.1 A8], [ARCH §16.2 R14, R15, R17], [CTX §9], [CTX §25], [CTX §31], [CTX §32], [CTX §38].

DEFERRED:
- [ARCH §16.3] Full org policy configuration — V3; only floors are forward-declared in V1 (floor inputs + validation-error behavior shipped here).
- [ARCH §16.3] `state/` CI-only alternative — not built; A8 evidence collection is the revisit path [OD-3].

### 1.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §9 | 0 | §1.3 manifest/allowlist/modes; §1.4 P0-T04/T05; §1.5 V-P0-4 |
| CTX | §25 | 0 | §1.3 memory layout reconciled by D1; §1.4 P0-T03 |
| CTX | §31 | 0 | §1.3 ownership categories; §1.4 P0-T05; §1.5 V-P0-5 |
| CTX | §32 | 0 | §1.3 config discovery/diagnostics API (Phase 7 consumer) |
| CTX | §38 | 0 | §1.3 application owns permissions/config; write-boundary guards |
| ARCH | §0.3 D1 | 0 | §1.3 canonical layout; §1.5 V-P0-3 |
| ARCH | §0.3 D2 | 0 | §1.3 ownership floor; §1.5 V-P0-5 |
| ARCH | §0.3 D7 | 0 | §1.3 allowlist; §1.5 V-P0-4 |
| ARCH | §0.3 D8 | 0 | §1.3 fail-closed validation; §1.5 V-P0-8 |
| ARCH | §3.5 | 0 | §1.3 run keying/upsert; §1.5 V-P0-12 |
| ARCH | §4.1 | 0 | §1.3 ChangeAnalysis schema draft (Phase 1 consumes) |
| ARCH | §4.8 | 0 | §1.3 VerificationReport schema draft (Phase 4 consumes) |
| ARCH | §4.9 | 0 | §1.3 DocPatch schema draft (Phase 3 consumes) |
| ARCH | §5.4 | 0 | §1.3 decision-object schema; §1.5 V-P0-2 |
| ARCH | §6.2 | 0 | §1.3 transition-log primitives |
| ARCH | §6.3 | 0 | §1.3 state core; §1.5 V-P0-11/13 |
| ARCH | §8.3.1 | 0 | §1.3 taxonomy mapping; §1.5 V-P0-9 |
| ARCH | §11.1 | 0 | §1.3 manifest schema; §1.5 V-P0-4 |
| ARCH | §11.2 | 0 | §1.3 modes/floors; §1.5 V-P0-5/6 |
| ARCH | §11.3 | 0 | §1.3 precedence + fail-closed; §1.5 V-P0-7/8/10/15 |
| ARCH | §11.4 | 0 | §1.3 locked-terminal interface |
| ARCH | §12.2 | 0 | §1.3 GitPlatformFake stub (Phase 6/8 consumers) |
| ARCH | §12.5 | 0 | §1.3 provenance schema (Phase 9 consumes) |
| ARCH | §16.1 A8 | 0 | §1.4 P0-T15; §1.5 V-P0-14 |
| ARCH | §16.2 R14 | 0 | §1.4 P0-T11 manifest lint |
| ARCH | §16.2 R15 | 0 | §1.4 P0-T12 CAS; §1.5 V-P0-11 |
| ARCH | §16.2 R17 | 0 | §1.4 P0-T09 mapping layer; §1.5 V-P0-9 |
| ARCH | §16.3 | 0 | DEFERRED full org policy; DEFERRED state CI-only |
| ARCH | §2.3 P8 | 0 | per-document modes honored in resolution |
| ARCH | §2.3 P19 | 0 | deterministic config/validation core |

## §2 Phase 1 — Deterministic Analysis Core

### 2.1 Objective & scope

Phase 1 builds the deterministic, Git-only substrate that answers "what changed and what does it touch?" before any model is invoked [ARCH §4.1; ARCH §2.1 corollary]: the `ChangeAnalysis` pipeline, the document graph, claims, contracts, the retrieval hierarchy, baselines with the D12 resolution ladder, and the documentation state machine's deterministic transitions. Everything here is recomputable from Git (purity), cacheable by blob SHAs, and never calls a model [ARCH §3.5, §4.1]. Out of scope: Jev/decision layer (Phase 2), writer (Phase 3), verification (Phase 4), orchestration budgets (Phase 5), Git writes (Phase 6).

### 2.2 Prerequisites & entry gate

- Phase 0 complete: schemas (`ChangeAnalysis`, claims, baselines, state records), `state/` store with CAS, fixture repos, golden-file harness [ARCH §6.3, §3.5].
- Git available at runtime; decision: analyze through the system `git` binary via a typed exec wrapper (fidelity for rename detection, `patch-id`, tree hashing — required by the D12 ladder) [ARCH §6.1; D12].
- Entry gate: purity test green — identical `(base_sha, head_sha)` blob SHAs produce byte-identical `ChangeAnalysis`; baseline invalidation reproduces the §6.1 example deterministically.

### 2.3 Features & deliverables

Change analyzer [ARCH §4.1; CTX §8]:
- `git diff` computation over `(base_sha, head_sha)`: file statuses (added/modified/deleted/renamed), hunks, per-file records [ARCH §4.1].
- Path-mapping intersection with manifest `sources` globs (retrieval levels 1–2 groundwork) [ARCH §4.1, §4.5].
- Pre-Jev classification hints with short-circuits: `test_only`, `formatting_only` (skip Jev entirely), `generated` (route per policy, e.g. the `inspect_generated_output` action of the memory pattern `generated-sdk`) [ARCH §4.1, §8.8; R10].
- Symbol changes via the analyzer-plugin interface: Phase 1 ships `@docsync/analyzer-typescript` as an **analyzer-plugin STUB (no AST)** — it emits no symbol/type-level diffs in V1; real tree-sitter symbol extraction and signature/type-level diffs (incl. the `string → string?` case) land in Phase 11 (F11-1). `@docsync/analyzer-generic` (path-level, always present) is the working V1 analyzer; when a language lacks tooling, degradation to path/dependency-level analysis is **declared as a coverage gap in the run record — never silent** [OD-4; ARCH §3.7, §4.1; type-aware analysis → Phase 11, ARCH §15.2].
- Dependency changes, OpenAPI/schema diffs, semantic configuration diffs [ARCH §4.1].
- Deep-analysis caps: beyond configured file-count/diff-byte caps the run downgrades to path-level analysis, marks `deep_analysis: skipped`, and flags Jev → REVIEW routing [ARCH §3.7].
- `ChangeAnalysis` record per the §4.1 contract (`run_id`, `files[]` with status/generated/test_only/formatting_only/symbols_changed/api_diff/config_diff, `dependencies_changed`, `classification_hints`), keyed `(repo, base_sha, head_sha)`, fully recomputable from Git, cacheable by blob SHAs [ARCH §3.3, §4.1].
- Analyzer failure is **inconclusive, never negative** (D14): failures emit an inconclusive run record; downstream routes REVIEW [D14; ARCH §4.1, §3.4].

Document graph [ARCH §4.2; CTX §10]:
- Canonical mapping records (`doc`/`sources`/`concepts`/`contracts`) versioned in `.docsync/`; reverse lookup (code change → affected docs) and forward lookup (doc → supporting sources) [ARCH §4.2].
- Reverse indexes built per run from changed-file sets, not full-graph traversals [ARCH §3.7].
- Deleted/renamed sources → edges marked `orphaned`; affected docs re-routed to SUSPECT rather than silently dropping coverage [ARCH §4.2].
- Mappings are declarative and reviewable in PRs; automation may propose mapping edits, never silently rewrite them [ARCH §4.2].

Claims [ARCH §4.3; CTX §11]:
- Claim records in `state/claims/` with `text`, anchor (file + line range + **content hash of the quoted span**), `source` (code file:line + blob SHA), concept, status, history [ARCH §4.3, §6.3].
- Anchoring is content-hash-first, line-number-second; ambiguous re-anchoring marks the claim `UNANCHORED` → REVIEW — never silently re-anchors (invariant 13) [ARCH §4.3].
- V1 fallback: manifest-declared / hand-anchored claims; automated extraction is V2 and proposal-only until verified against source evidence [ARCH §4.3, §15.1].
- Headline query support: "which documented claims lost support in the source of truth?" via baseline differential ∩ claim set [ARCH §4.3, §6.1].

Contracts [ARCH §4.4; CTX §12]:
- Per-document contract declarations; per-contract classification vector consumed by routing (Phase 2) and the verifier (Phase 4); a proposed patch must not silently violate an unmarked contract [ARCH §4.4].
- Sub-threshold classification abstains to REVIEW; declared contracts with zero checkable claims flagged as a coverage defect [ARCH §4.4].

Retrieval engine [ARCH §4.5; CTX §8]:
- Strictly ordered hierarchy levels 1–6 (explicit manifest → path mapping → symbol mapping → API/schema mapping → concept/document graph → semantic retrieval), each candidate carrying its level as provenance [ARCH §4.5]. V1 activation: levels 1–2, 5, 6 active (manifest, path mapping, graph, semantic fallback); levels 3–4 (symbol, API/schema) are **interface-ready but DECLARED INACTIVE**, surfaced as a coverage gap in run records per D14/OD-4 — populated and activated in Phase 11 (F11-2).
- Caps per level; embeddings/vector search are fallback recall only — never the source of truth; semantic-only candidates without deterministic corroboration route to REVIEW at best and can never drive automatic actions [ARCH §4.5].
- Semantic index lazily rebuilt per shard; staleness tolerated because correctness never depends on it [ARCH §3.7, §4.5].

Baselines [ARCH §6.1; CTX §27]:
- Baseline records (`doc_path`, `doc_content_hash`, `baseline_commit`, `source_files[]`, `source_symbols[]`, `claim_ids[]`, `verified_at`, `verified_by_run`, `policy_version`) in `state/baselines/` [ARCH §6.1, §6.3].
- Invalidation = diff(`baseline_commit..head`) ∩ (sources, symbols, claims); comparison by Git object identity, never timestamps [ARCH §6.1].
- D12 resolution ladder: exact SHA → same tree hash → patch-id/merge-base match → treat as invalidated (SUSPECT); baselines never silently survive history rewrites (squash merges, force-pushes, merge queues) [D12; ARCH §6.1; R9].
- Baseline updates are CAS writes (stored `doc_content_hash` + baseline commit must match run-start values; on mismatch, re-run claim evaluation) [ARCH §6.1; R15].
- Baselines bridge Loop A (PR-time differential) and Loop B (historical audit) — same records, two consumers (Loop B itself is Phase 11; records and `audit-cursor` land here) [ARCH §6.1, §7.2].

State machine [ARCH §6.2; CTX §28]:
- States VERIFIED / SUSPECT / CLEAR / REVIEW / STALE / PROPOSED / REJECTED / CLOSED with append-only transition log (state, timestamp, run id, reason) in `state/docs/<path>.json` [ARCH §6.2].
- Deterministic transitions owned here: VERIFIED→SUSPECT (analysis intersects mapped sources/claims/baseline), VERIFIED→CLOSED (doc deleted/archived/superseded); SUSPECT→CLEAR/REVIEW/STALE consume Phase 2 decisions via the Phase 5 orchestrator [ARCH §6.2].
- CAS on previous state hash; a conflicting concurrent transition loses gracefully and re-runs evaluation [ARCH §6.2; R15].
- The state machine is internal truth; GitHub check/PR surfaces are projections of it [ARCH §6.2].

Scalability boundaries [ARCH §3.7]:
- Caps (file count / diff bytes) → path-level downgrade with declared `deep_analysis: skipped` [ARCH §3.7].
- Symbol index (INTERFACE STUB in V1 — no AST; the per-blob-SHA symbol-index cache built incrementally per changed file lands Phase 11 P11-T02) [ARCH §3.7].
- Purity + blob-SHA caching of all analysis artifacts [ARCH §3.5, §3.7].

### 2.4 TODO list

- [ ] P1-T01 — Typed git exec wrapper: diff w/ rename detection, tree hashes, `patch-id`, `cat-file` [ARCH §4.1; D12]
- [ ] P1-T02 — `ChangeAnalysis` builder conforming to the §3.3/§4.1 contract; purity test (identical blob SHAs → identical record) [ARCH §3.3, §4.1]
- [ ] P1-T03 — `test_only` / `formatting_only` detectors with pre-Jev short-circuit hints [ARCH §4.1; R10]
- [ ] P1-T04 — Generated-file heuristic detector + `inspect_generated_output` routing hook [ARCH §4.1, §8.8; R10]
- [ ] P1-T05 — Dependency / OpenAPI-schema / config semantic differs [ARCH §4.1]
- [ ] P1-T06 — Analyzer plugin interface + `@docsync/analyzer-typescript` STUB (no AST; real tree-sitter symbol/type-level analysis lands Phase 11 F11-1) [OD-4; ARCH §4.1, §15.2]
- [ ] P1-T07 — `@docsync/analyzer-generic`: path-level analyzer, always present; coverage-gap declaration in run records [OD-4; ARCH §3.7]
- [ ] P1-T08 — Deep-analysis caps + `deep_analysis: skipped` downgrade + REVIEW-routing flag [ARCH §3.7]
- [ ] P1-T09 — Document graph: mapping records, reverse+forward lookup, per-run reverse indexes, orphan reconciliation [ARCH §4.2, §3.7]
- [ ] P1-T10 — Claim store: content-hash-first anchors, statuses, history; `UNANCHORED`→REVIEW flow [ARCH §4.3; invariant 13]
- [ ] P1-T11 — Hand-anchored / manifest-declared claim intake (V1 fallback) [ARCH §4.3, §15.1]
- [ ] P1-T12 — Contract declarations + per-contract classification vectors + zero-claims coverage-defect flag [ARCH §4.4]
- [ ] P1-T13 — Retrieval engine: levels 1–2, 5, 6 active + levels 3–4 interface-ready but INACTIVE (declared coverage gap in run records; activation Phase 11), with level provenance, caps, corroboration rule (semantic-only → REVIEW) [ARCH §4.5]
- [ ] P1-T14 — Optional semantic index (fallback recall only): lazy shard rebuild, staleness tolerated [ARCH §4.5, §3.7]
- [ ] P1-T15 — Baseline store + invalidation engine (diff ∩ sources/symbols/claims) with CAS updates [ARCH §6.1; R15]
- [ ] P1-T16 — D12 ladder: exact SHA → tree hash → patch-id/merge-base → SUSPECT, with squash/force-push simulators [D12; ARCH §6.1; R9]
- [ ] P1-T17 — State machine core: transitions, append-only log, CAS conflict → graceful re-run [ARCH §6.2; R15]
- [ ] P1-T18 — Inconclusive-analyzer semantics: failure → inconclusive record, never "no docs affected" [D14; ARCH §4.1]
- [ ] P1-T19 — Blob-SHA caches: analysis cache + symbol-index INTERFACE STUB (real per-blob-SHA symbol-index cache built Phase 11 P11-T02) [ARCH §3.5, §3.7]
- [ ] P1-T20 — Fixture golden tests: deterministic path-level/mapping-based invalidation (mapped source change → doc SUSPECT, zero model calls); monorepo-scale cap tests [ARCH §4.1, §4.3, §3.7]

### 2.5 Verification list

- [ ] V-P1-1 — Purity: same `(repo, base_sha, head_sha)` inputs → byte-identical `ChangeAnalysis` [ARCH §4.1, §3.5]
- [ ] V-P1-2 — Deterministic invalidation: a change to a mapped source file flips the baseline and the doc enters SUSPECT with zero model calls; type-level (`string → string?`) invalidation is a DECLARED V1 coverage gap, gated at Phase 11 (V-P11-1) [ARCH §4.1, §4.3; CTX §11]
- [ ] V-P1-3 — Repo-wide formatting sweep consumes zero decision-model budget (no Jev invocation asserted) [ARCH §4.1]
- [ ] V-P1-4 — Cap breach on the monorepo fixture → path-level downgrade + `deep_analysis: skipped` + REVIEW routing flag [ARCH §3.7]
- [ ] V-P1-5 — Reverse lookup (changed file → docs) and forward lookup (doc → sources) both correct [ARCH §4.2]
- [ ] V-P1-6 — Renamed/deleted source → `orphaned` edge; affected doc re-routed to SUSPECT, never silently dropped [ARCH §4.2]
- [ ] V-P1-7 — Anchoring: hash-match re-anchors; ambiguous edit → `UNANCHORED` → REVIEW [ARCH §4.3; invariant 13]
- [ ] V-P1-8 — Semantic-only candidate (no deterministic corroboration) cannot drive any automatic action [ARCH §4.5]
- [ ] V-P1-9 — Squash-merge simulation: baseline resolves via tree hash / patch-id or is marked SUSPECT — never silent survival [D12; R9]
- [ ] V-P1-10 — Injected git failure → inconclusive run record; downstream REVIEW (asserted) — never negative [D14]
- [ ] V-P1-11 — CAS race on baseline/state transition → loser re-runs evaluation, store never corrupted [R15; ARCH §6.1, §6.2]
- [ ] V-P1-12 — Contract vector scopes the case (e.g., only `request_schema` in scope) and sub-threshold classes abstain [ARCH §4.4]
- [ ] V-P1-13 — Language without tooling → coverage gap declared in the run record (never silent) [OD-4; ARCH §3.7]

Gate: phase complete when analyzer purity, the deterministic invalidation case (mapped source change → doc SUSPECT with zero model calls; type-aware invalidation explicitly deferred to Phase 11), D12 squash/force-push simulations, orphan/UNANCHORED semantics, inconclusive-analyzer behavior, and CAS race handling are all green on fixtures with zero model invocations in the entire phase test suite.

### 2.6 Risks, assumptions & decisions

Assigned gates:
- **R9 (baseline orphaning on squash/force-push)** — D12 ladder implemented with squash/force-push simulators; fail-to-SUSPECT semantics [ARCH §16.2 R9, §6.1; D12].
- **R10 (generated churn / regeneration loops)** — pre-Jev `formatting_only`/`generated` short-circuits; content-hash dedup hook for drift reports [ARCH §16.2 R10, §4.1].
- **R15 (multi-writer races)** — CAS on baselines and state transitions; loser re-runs [ARCH §16.2 R15, §6.1, §6.2].
- **D14** — analyzer failure is inconclusive; degradation tests assert downstream REVIEW [ARCH §0.3 D14, §3.4].
- **OD-4** — TypeScript analyzer first; generic path-level analyzer always present; coverage gaps declared in run records, never silent [OD-4; ARCH §3.7].
- A5-adjacent (behavioral-change approximability) is a V2 gate (Phase 11); the V1 mitigation is exactly the declared-coverage-gap mechanism of OD-4 [ARCH §16.1 A5].

Decisions taken:
- Git accessed through the system binary via a typed exec wrapper — `patch-id` and rename-detection fidelity are required by the D12 ladder [D12].
- Analyzer plugin order per OD-4: TypeScript-first stub in V1 (no AST — real tree-sitter symbol extraction lands Phase 11 F11-1); no other language ASTs in V1 [OD-4].
- Claims in V1 are manifest-declared / hand-anchored; automated extraction explicitly V2 [ARCH §4.3, §15.1].
- Reverse indexes rebuilt per run from changed-file sets; no persistent full-graph index in V1 [ARCH §3.7, §4.2].

Alternatives considered:
- Pure-JS git library (isomorphic-git) for analysis (rejected: weaker rename/patch-id fidelity needed by D12).
- Persistent full-graph index (rejected for V1: per-run reverse indexes scale and stay Git-native) [ARCH §3.7].
- Silent skip of un-analyzable languages (rejected: violates OD-4/D14 — declared gaps only).

> OPEN DECISION (OD-R05): symbol-index persistence — per-blob-SHA cache under `state/` vs ephemeral per-run compute. — Recommendation: ephemeral by default (recompute is cheap and keeps `state/` lean for A8), with an opt-in cache flag for monorepos.

> OPEN DECISION (OD-R06): depth of semantic config diffs in V1 — key-level only vs typed value parsing for known config families. — Recommendation: key-level + typed value parsing for a small family set (env, JSON, YAML); deeper semantic rules deferred as policy data.

### 2.7 Architecture traceability

Covered: [ARCH §3.3] (ChangeAnalysis edge contract), [ARCH §3.4] (analyzer failure default), [ARCH §3.5], [ARCH §3.7], [ARCH §4.1], [ARCH §4.2], [ARCH §4.3], [ARCH §4.4], [ARCH §4.5], [ARCH §6.1], [ARCH §6.2], [ARCH §6.3] (state integration), [ARCH §7.2] (Loop B records/audit-cursor groundwork), [ARCH §8.8] (generated-sdk hook), [ARCH §15.1] (V1 hand-anchored claims), [ARCH §0.3 D12, D14], [ARCH §14 invariant 13], [ARCH §16.2 R9, R10, R15], [CTX §8], [CTX §10], [CTX §11], [CTX §12], [CTX §27], [CTX §28], [OD-4].

DEFERRED:
- [ARCH §16.3] Claim-level edit objects as patch representation — V2+ refinement.
- [ARCH §4.3] Automated claim extraction — V2; output is a proposal until verified against source evidence.
- [ARCH §3.7] External graph DB / index cache for very large monorepos — optimization only, never truth.
- [ARCH §7.2] Loop B differential sweeps — Phase 11; storage records and `audit-cursor` land here.
- [ARCH §15.2] Type-aware diffs + symbol/AST analysis + retrieval levels 3–4 activation — Phase 11 (F11-1/F11-2/F11-4); V1 ships the plugin interface + stubs + declared coverage gaps.

### 2.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §8 | 1 | §2.3 analyzer + retrieval hierarchy; §2.4 P1-T02/T13 |
| CTX | §10 | 1 | §2.3 document graph; §2.4 P1-T09; §2.5 V-P1-5/6 |
| CTX | §11 | 1 | §2.3 claims; §2.4 P1-T10; §2.5 V-P1-2 (deterministic mapping-based invalidation) / V-P1-7 — type-aware invalidation Phase 11 (§12.3 F11-4) |
| CTX | §12 | 1 | §2.3 contracts; §2.4 P1-T12; §2.5 V-P1-12 |
| CTX | §27 | 1 | §2.3 baselines; §2.4 P1-T15 |
| CTX | §28 | 1 | §2.3 state machine; §2.4 P1-T17 |
| ARCH | §3.3 | 1 | §2.3 ChangeAnalysis contract; §2.5 V-P1-1 |
| ARCH | §3.4 | 1 | §2.3 inconclusive defaults; §2.5 V-P1-10 |
| ARCH | §3.5 | 1 | §2.3 purity/keying; §2.5 V-P1-1 |
| ARCH | §3.7 | 1 | §2.3 caps/indexes; §2.4 P1-T08/T19; §2.5 V-P1-4 |
| ARCH | §4.1 | 1 | §2.3 analyzer (path-level + stubs); §2.4 P1-T01..T08; §2.5 V-P1-1/2/3/4 — type-aware diffs DEFERRED Phase 11 |
| ARCH | §4.2 | 1 | §2.3 graph; §2.5 V-P1-5/6 |
| ARCH | §4.3 | 1 | §2.3 claims; §2.5 V-P1-7 |
| ARCH | §4.4 | 1 | §2.3 contracts; §2.5 V-P1-12 |
| ARCH | §4.5 | 1 | §2.3 retrieval (levels 1–2, 5, 6 active; 3–4 inactive); §2.5 V-P1-8 |
| ARCH | §6.1 | 1 | §2.3 baselines; §2.5 V-P1-9/11 |
| ARCH | §6.2 | 1 | §2.3 state machine; §2.5 V-P1-11 |
| ARCH | §6.3 | 1 | §2.3 state integration |
| ARCH | §7.2 | 1 | §2.3 Loop B records/audit-cursor groundwork (sweeps DEFERRED Phase 11) |
| ARCH | §8.8 | 1 | §2.3 generated-sdk routing hook; §2.4 P1-T04 |
| ARCH | §15.1 | 1 | §2.3 hand-anchored claims (V1 fallback); §2.4 P1-T11 |
| ARCH | §0.3 D12 | 1 | §2.3 resolution ladder; §2.5 V-P1-9 |
| ARCH | §0.3 D14 | 1 | §2.3 inconclusive semantics; §2.5 V-P1-10 |
| ARCH | §14 inv 13 | 1 | §2.5 V-P1-7 anchors don't silently move |
| ARCH | §16.2 R9 | 1 | §2.4 P1-T16; §2.5 V-P1-9 |
| ARCH | §16.2 R10 | 1 | §2.4 P1-T03/T04; §2.5 V-P1-3 |
| ARCH | §16.2 R15 | 1 | §2.4 P1-T15/T17; §2.5 V-P1-11 |
| ARCH | §16.3 | 1 | DEFERRED claim-level edit objects; DEFERRED automated extraction; graph DB never truth |
| ARCH | §15.2 | 1 | DEFERRED type-aware diffs/symbol analysis/levels 3–4 → Phase 11 |
| ARCH | §2.3 P3 | 1 | deterministic evidence before AI (zero model calls) |
| ARCH | §2.3 P7 | 1 | explicit code↔concept↔doc relationships |
| ARCH | §2.3 P18 | 1 | evidence-backed records (claims/baselines with anchors) |
| OD | OD-4 | 1 | plugin interface + TypeScript stub + generic analyzer + declared coverage gaps; type-aware analysis Phase 11; §2.5 V-P1-13 |


---

## §3 Phase 2 — Model Access & Decision Layer

### 3.1 Objective & scope

Build the OpenRouter model-access layer (`@docsync/openrouter`) and the Stage-1 decision layer (Jev runtime, decision object schema, policy schema + deterministic policy compiler, confidence × risk policy engine, calibration harness, bootstrap default policy). This phase makes all AI inference flow through one versioned, schema-validated, provenance-pinned gateway, and implements Stage 1 (Decision) of the three-layer pipeline [ARCH §5.1, §10]. Scope: typed roles bound to OpenRouter model IDs, structured-output validation with bounded retry → abstain, policy compilation and versioning, the decision object on the wire, and the policy engine's matrix + abstention semantics. Out of scope: patch generation (§4), verification (§5), Git/PR surfaces (Phase 6), and prompt templates for other stages beyond their role contracts.

### 3.2 Prerequisites & entry gate

- Phase 0 complete: package scaffold exists (`@docsync/openrouter`, `@docsync/policy`, `@docsync/schemas`), zod schemas with JSON Schema export available, vitest/eslint/prettier configured [ARCH §6.3; OD-1, OD-2].
- Phase 1 complete: `ChangeAnalysis` record, document graph, retrieval primitives, and state storage exist — Jev consumes structured state derived from them [ARCH §3.3, §4.1–4.5].
- `models.yml` validation exists (Phase 0) and is loadable as an input contract [ARCH §6.3, §10.3].
- `OPENROUTER_API_KEY` can be supplied via environment for integration tests; unit tests run fully against recorded/double responses (no network).
- Entry gate: `pnpm test` green through Phase 1; manifest fixture repositories from Phase 0 reusable as decision-layer fixtures.

### 3.3 Features & deliverables

- `@docsync/openrouter` client implementing the access layer: single inference gateway for every LLM stage; Jev and Sonnet are logical roles, not hard-coded models [ARCH §10.1, §10.2; CTX §39].
- Role → model binding resolution from `models.yml`: roles `decision-engine`, `patch-writer`, `judge`, `policy-architect` implemented; `claim-extractor` (V2) and `drift-auditor` (V2/V3) forward-declared as typed interfaces [ARCH §10.2, §10.3].
- Fallback semantics per role: model `fallbacks:` chain and provider fallbacks (`provider.order`, `allow_fallbacks`) honored at the client level [ARCH §10.5].
- Structured outputs at the gateway boundary: native JSON-schema-constrained responses where the routed model supports it; constrained prompting + strict zod validation otherwise [ARCH §10.4].
- Schema-violating output policy: bounded retries, then abstain to REVIEW — never hand-patched into compliance by the orchestrator [ARCH §10.4, §3.4].
- Per-role concurrency caps; Loop A never issues unbounded parallel model calls [ARCH §10.5].
- Stage latency budgets enforced and recorded: decision-engine ≲1 s target, measured `latency_ms` per stage call [ARCH §7.1 item 1, §10.6].
- Usage/cost telemetry: OpenRouter usage accounting captured per call, attributable per (role, policy_version, model binding); feeds the `efficiency` term of the improvement objective later [ARCH §10.6].
- Provenance truthfulness: records name the actual routed model id + provider used, never a hardcoded brand [ARCH §10.6].
- Prompt caching of the deterministic workspace prefix (manifest/policy/instructions), keyed by content hash, where the routed provider supports it [ARCH §10.7].
- Secret discipline: `OPENROUTER_API_KEY` lives only in the orchestrator environment; keys never sent to models in prompts, never stored in `.docsync/` files [ARCH §10.7].
- Prompt-injection containment at the gateway: repository content passed to models is untrusted input; model outputs are untrusted data validated against typed schemas; no model output influences permissions, file selection, or credentials [ARCH §10.7, §13].
- Jev runtime: typed question set (boolean / choice / score), every answer carrying both a value and a probability; structured questions only — no prose generation [ARCH §4.6; CTX §5].
- Universal question set always runs; repository-specific questions are additive and refine routing/risk [ARCH §8.3.1; CTX §15].
- Routing outcome three-way `IGNORE | REVIEW | CONTINUE` [ARCH §4.6].
- Decision object `docsync.decision/v1` exactly per [ARCH §5.4]: `schema`, `decision_id`, `policy_version`, `change` (commit, diff_ref, change_analysis_ref), `change_kind`, `documentation_impact`, `risk`, `confidence`, `answers` (value+probability per key), `candidates` (manifest-known paths only), `action`, `abstain`, `abstain_reason`, `evidence` (machine-resolvable anchors), `trace` (model, policy_hash, latency_ms).
- Abstention semantics: first-class, named outcome with enumerated `abstain_reason` values — `insufficient_confidence`, `conflicting_evidence`, `policy_gap`, `calibration_floor`, `verifier_uncertain` — each landing in the review queue with evidence and becoming a labeled data point once resolved [ARCH §5.2; CTX §13].
- Policy schema (`@docsync/policy`): closed choice sets for `change_kind`/`documentation_impact`/`risk`/`action`, typed `questions`, `routing` thresholds (`high_confidence`, `review`) [ARCH §8.3].
- Threshold calibration discipline: `0.94`/`0.72`-style thresholds are per-policy-version, per-repository calibrated data, never hard-coded; matrix shape fixed, calibration evolvable [D11; ARCH §5.3, §8.3].
- Deterministic policy compiler: validates schema conformance, question types, threshold sanity, choice-set closure, and trigger-taxonomy mapping to the decision schema; compiles to a typed Jev workflow; failing policies return to the architect with compiler diagnostics — no partial policies ever reach production [ARCH §8.2].
- Trigger-taxonomy → decision-schema versioned mapping layer consumed by the compiler; validation errors name the expected schema version [R17; ARCH §8.3.1, §11.3].
- Policy versioning: every policy version self-contained with `policy_version`, `created_by`, `reason`, `evaluation`, `previous` pointers; history retained under `.docsync/policy-history/`; rollback is a metadata operation (promote prior version); `unsafe_auto_updates = 0` as a promotion gate metric [ARCH §8.4].
- Confidence × risk policy engine: matrix shape fixed (automation `auto` exists in exactly one cell; diagonal is `review`; high-risk rows never produce `auto`); risk = conservative max of manifest risk class and Jev `risk` choice; displayed confidence pinned to `(policy_version, model binding)` [ARCH §5.3; D2, D3, D11].
- Ownership floor enforcement in the engine: `locked` resolves to `blocked` at any confidence; the engine cannot override ownership [D2; ARCH §11.2, §5.3].
- Outcome set: `ignore | suggest | auto | blocked` with `review` as workflow state; abstain path never forces uncertain cases into update/ignore [ARCH §5.2].
- Abstain-rate and human-override-rate health metrics recorded per policy version (2-D health metric) [ARCH §5.2, §16.2 R11].
- Calibration harness: computes ECE/reliability per `(policy_version, model binding)` on the evaluation split; a binding must pass calibration before promotion [A1; ARCH §10.8, §16.1].
- Bootstrap default Jev policy v1: small universal schema, conservative routing thresholds, `abstain`-safe defaults, shipped as a valid compiled artifact so Phase 5 has a real Stage 1 [ARCH §8.3.1, §15.1].
- Model-swap discipline: `models.yml` changes are treated like policy changes — shadow/canary evaluated before production [ARCH §10.3].

### 3.4 TODO list

- [ ] P2-T01 — Scaffold `@docsync/openrouter` and `@docsync/policy` packages with exports, tsconfig strict, vitest suites [OD-1, OD-2]
- [ ] P2-T02 — Implement `OpenRouterClient` with role bindings resolved from `models.yml` (primary/fallbacks/params/provider order) [ARCH §10.2, §10.3]
- [ ] P2-T03 — Implement model+provider fallback walk with per-role concurrency caps and structured timeout handling [ARCH §10.5]
- [ ] P2-T04 — Implement structured-output enforcement: native JSON-schema-constrained response path; constrained-prompting + strict zod validation path [ARCH §10.4]
- [ ] P2-T05 — Implement bounded-retry-on-schema-violation → abstain-to-REVIEW semantics with machine-readable failure records [ARCH §10.4, §3.4]
- [ ] P2-T06 — Implement telemetry: per-call usage/cost/latency capture, keyed (role, policy_version, model binding); actual-routed-model provenance field [ARCH §10.6]
- [ ] P2-T07 — Implement prompt-cache support for the deterministic prefix keyed by content hash [ARCH §10.7]
- [ ] P2-T08 — Add gateway boundary guards: env-only credential resolution, no-key-in-prompt assertions, untrusted-input tagging of repo content [ARCH §10.7, §13]
- [ ] P2-T09 — Define typed role interfaces for `decision-engine`, `patch-writer`, `judge`, `policy-architect`; forward-declare `claim-extractor`, `drift-auditor` [ARCH §10.2]
- [ ] P2-T10 — Implement Jev runtime: typed questions (boolean/choice/score) with value+probability on every answer; three-way routing [ARCH §4.6]
- [ ] P2-T11 — Implement universal + additive repository question composition [ARCH §8.3.1]
- [ ] P2-T12 — Define `docsync.decision/v1` zod schema + JSON Schema export with all §5.4 fields [ARCH §5.4]
- [ ] P2-T13 — Implement decision-object emission with manifest-only candidate validation and machine-resolvable evidence anchors [ARCH §5.4]
- [ ] P2-T14 — Implement enumerated abstain reasons and abstain recording (queue + labeled-data-point hook) [ARCH §5.2]
- [ ] P2-T15 — Implement policy schema (closed choice sets, typed questions, routing thresholds) in `@docsync/policy` [ARCH §8.3]
- [ ] P2-T16 — Implement deterministic policy compiler with diagnostics: schema conformance, question types, threshold sanity, choice-set closure, taxonomy mapping w/ expected schema version in errors [ARCH §8.2, §8.3.1; R17]
- [ ] P2-T17 — Implement policy versioning: `policy-history/` retention, `previous` pointers, rollback-as-metadata-operation, `unsafe_auto_updates` gate metric struct [ARCH §8.4]
- [ ] P2-T18 — Implement confidence × risk matrix evaluation with fixed shape, conservative-max risk, and ownership floor (locked → blocked) [ARCH §5.3; D2, D11]
- [ ] P2-T19 — Implement outcome mapping (`ignore|suggest|auto|blocked` + `review` workflow state) and abstain-first semantics [ARCH §5.2]
- [ ] P2-T20 — Implement abstain-rate + override-rate health metric recording per policy version [ARCH §5.2; R11]
- [ ] P2-T21 — Implement calibration harness: ECE/reliability per (policy_version, model binding) on eval split with pass/fail gate output [A1; ARCH §10.8]
- [ ] P2-T22 — Author bootstrap default Jev policy v1 (universal questions, conservative thresholds) and compile it in CI as a validity test [ARCH §8.3.1, §15.1]
- [ ] P2-T23 — Implement memoization of Jev decisions keyed by (diff-subset, policy_version) with shadow-key isolation [ARCH §7.1 item 4; D6]
- [ ] P2-T24 — Write unit/integration suites with recorded-response doubles; no network in CI unit tests [OD-2]

### 3.5 Verification list

- [ ] V-P2-1 — A policy fixture with an open choice set, bad threshold (≥1), or unknown question type is rejected by the compiler with named diagnostics and never compiles to a workflow [ARCH §8.2]
- [ ] V-P2-2 — A manifest using a trigger absent from the decision schema produces a validation error naming the expected schema version [R17; ARCH §11.3]
- [ ] V-P2-3 — A recorded response violating `docsync.decision/v1` triggers exactly the bounded retry count, then an abstain-to-REVIEW decision with `abstain_reason` set — no hand-patching [ARCH §3.4, §10.4]
- [ ] V-P2-4 — Fallback walk test: primary model error → first fallback used; exhausted fallbacks → provider-order walk; total failure → REVIEW/advisory, never auto [ARCH §10.5, §3.4]
- [ ] V-P2-5 — Concurrency-cap test: 50 parallel calls against a capped role never exceed the cap [ARCH §10.5]
- [ ] V-P2-6 — Decision object round-trips through zod validation and carries `policy_version`, `policy_hash`, `latency_ms`, and value+probability for every answer [ARCH §5.4]
- [ ] V-P2-7 — `candidates` containing a non-manifest path is rejected at emission [ARCH §5.4]
- [ ] V-P2-8 — Matrix tests: high-risk row at confidence 0.99 never yields `auto`; low-risk × high-confidence yields `auto` only when manifest mode permits; `locked` always `blocked` regardless of confidence [ARCH §5.3; D2, D11]
- [ ] V-P2-9 — Every abstained decision carries an enumerated `abstain_reason` and lands in the review queue with evidence attached [ARCH §5.2]
- [ ] V-P2-10 — Calibration harness on a synthetic miscalibrated fixture reports ECE above threshold and gates promotion [A1]
- [ ] V-P2-11 — Telemetry records actual routed model id + provider when a fallback fires [ARCH §10.6]
- [ ] V-P2-12 — `pnpm test` green; lint clean; compiled bootstrap policy v1 valid in CI [OD-2]

Gate: phase complete when Stage 1 (Decision) produces schema-valid, provenance-pinned, abstain-safe decisions from fixtures via the OpenRouter client, with compiler/rollback/matrix/calibration semantics all demonstrated by tests, and no network-dependent test remains.

### 3.6 Risks, assumptions & decisions

- A1 (Jev calibrated probabilities ≲1 s) — mitigation: calibration harness gates every (policy_version, model binding) on ECE/reliability + latency budget before promotion; if wrong, thresholds are meaningless and `auto` must not fire; promotion is blocked, system stays report-only [ARCH §16.1].
- R1 (miscalibrated high confidence feeding auto) — mitigated by A1 harness gate, `unsafe_auto_updates = 0` promotion gate struct, conservative-max risk [ARCH §16.2].
- R2 (model/prompt drift on OpenRouter upgrades) — mitigated by pinned model IDs in `models.yml` and model-swap-as-policy-change shadow/canary discipline [ARCH §10.3, §16.2].
- R4 (threshold overfitting) — thresholds live per policy version with evaluation metadata; time-split evaluation interface reserved for Phase 10 [ARCH §16.2, §8.4].
- R11 (abstention pathologies) — abstain-rate + override-rate recorded per policy version as a 2-D health metric [ARCH §5.2, §16.2].
- R17 (trigger-taxonomy drift) — versioned mapping layer inside the compiler; errors name expected schema version [ARCH §16.2].
- R18 (confidence-display trust erosion) — all emitted confidences carry `(policy_version, model binding)` pinning data [D3; ARCH §16.2].
- Decision taken: Jev/Sonnet/judge/policy-architect are typed role interfaces over one OpenRouter client; replacement models are admissible without redesign [ARCH §10.1–10.3; CTX §39].
- Decision taken: branching uses answer values; policy uses probabilities — enforced by typing, not convention [ARCH §4.6, §5.4].
- Decision taken: no partial policy artifacts; compiler diagnostics are the only output for invalid policies [ARCH §8.2].
- Decision taken: bootstrap policy v1 is conservative (review-heavy) — automation is expanded only via evaluated policy versions [ARCH §8.3.1, §15.1].
- Alternative considered: calling provider SDKs directly per provider — rejected: D9 requires single access layer, unified fallback/cost/provenance [D9; ARCH §10].
- Alternative considered: hard-coding 0.94/0.72 — rejected by D11; thresholds ship in bootstrap policy v1 as calibration data [D11].

> OPEN DECISION (OD-R07): Should the decision-engine role default to a native structured-output-capable model even at higher cost, or prefer the cheapest JSON-capable model and rely on constrained prompting + validation? — Recommendation: default to native structured-output support for `decision-engine` and `judge` (schema fidelity is safety-critical), allow `models.yml` to downgrade with a documented calibration re-check; record the choice per policy version.

### 3.7 Architecture traceability

- Covered: [ARCH §4.6] Jev typed decision layer (runtime, routing); [ARCH §5.1] Stage 1 of the three-layer pipeline; [ARCH §5.2] abstention semantics; [ARCH §5.3] confidence × risk; [ARCH §5.4] decision object; [ARCH §8.2] policy compilation; [ARCH §8.3 + §8.3.1] policy schema + repository-specific questions; [ARCH §8.4] policy versioning; [ARCH §10.1–10.8] model access layer; [ARCH §13] orchestrator-vs-LLM split at the gateway boundary; [ARCH §7.1 items 1, 4] stage budget + memoization groundwork.
- Covered: [D2, D3, D9, D11] enforcement points; [A1] calibration gate; [R1, R2, R4, R11, R17, R18] mitigations.
- Covered: [CTX §5] Jev role split; [CTX §6] Stage 1; [CTX §13] abstain path; [CTX §14] matrix; [CTX §15] repository-specific questions; [CTX §17] compilation; [CTX §18] policy structure; [CTX §20] versioning; [CTX §39] pluggable routing.
- DEFERRED: [ARCH §16.3] model identity for "Jev" on OpenRouter is per-repository calibration choice — bootstrap v1 ships a placeholder binding; final binding is a repository calibration decision, not a code decision.
- DEFERRED: [ARCH §10.2] `claim-extractor` role implementation (V2); [ARCH §10.2] `drift-auditor` role implementation (V2/V3).
- DEFERRED: shadow/canary machinery execution (§8.6) — interfaces and keying prepared here; live shadow evaluation arrives with Phase 10 (evaluation framework).

### 3.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| Context | CTX §5 | 2 | §3.3 (Jev runtime), §3.7 (role split) |
| Context | CTX §6 | 2 | §3.3 (Stage 1 pipeline) |
| Context | CTX §13 | 2 | §3.3, §3.4 (abstain path) |
| Context | CTX §14 | 2 | §3.3 (confidence × risk matrix) |
| Context | CTX §15 | 2 | §3.3, §3.4 (repository-specific questions) |
| Context | CTX §17 | 2 | §3.3, §3.4 (policy compiler) |
| Context | CTX §18 | 2 | §3.3 (policy schema shape) |
| Context | CTX §20 | 2 | §3.3, §3.4 (policy versioning) |
| Context | CTX §39 | 2 | §3.3, §3.6 (pluggable routing) |
| ARCH | §4.6 | 2 | §3.3, §3.4 (Jev runtime) |
| ARCH | §5.1 | 2 | §3.1, §3.3 (Stage 1) |
| ARCH | §5.2 | 2 | §3.3, §3.4 (abstention) |
| ARCH | §5.3 | 2 | §3.3, §3.4 (matrix, D2/D11) |
| ARCH | §5.4 | 2 | §3.3, §3.4 (decision object) |
| ARCH | §7.1 (items 1, 4) | 2 | §3.3, §3.4 (budgets, memoization) |
| ARCH | §8.2 | 2 | §3.3, §3.4 (compiler) |
| ARCH | §8.3 | 2 | §3.3 (policy schema) |
| ARCH | §8.3.1 | 2 | §3.3, §3.4 (additive questions, taxonomy) |
| ARCH | §8.4 | 2 | §3.3, §3.4 (versioning, gate metric) |
| ARCH | §10.1–10.8 | 2 | §3.3, §3.4 (access layer) |
| ARCH | §13 | 2 | §3.3, §3.6 (gateway containment) |
| ARCH | §16.2 | 2 | §3.6 (R1, R2, R4, R11, R17, R18) |
| ARCH | §16.3 | 2 | §3.7 (DEFERRED Jev model identity) |
| Decision | D2 | 2 | §3.3, §3.4 (locked→blocked) |
| Decision | D3 | 2 | §3.3, §3.6 (display pinning) |
| Decision | D9 | 2 | §3.3, §3.6 (single gateway) |
| Decision | D11 | 2 | §3.3, §3.4 (thresholds as data) |
| Decision | D6 | 2 | §3.4 (shadow memoization keying) |
| Assumption | A1 | 2 | §3.5, §3.6 (calibration gate) |
| Risk | R1 | 2 | §3.6 |
| Risk | R2 | 2 | §3.6 |
| Risk | R4 | 2 | §3.6 |
| Risk | R11 | 2 | §3.3, §3.6 |
| Risk | R17 | 2 | §3.3, §3.4 |
| Risk | R18 | 2 | §3.3, §3.6 |
| Invariant | #6 (abstain, don't guess) | 2 | §3.3, §3.5 |
| Invariant | #8 (models never hold credentials) | 2 | §3.3, §3.4 (env-only key) |
| Principle | #4 (Jev structured decisioning) | 2 | §3.3 |
| Principle | #11 (confidence × risk) | 2 | §3.3, §3.5 |
| Principle | #12 (explicit abstain path) | 2 | §3.3, §3.5 |
| Principle | #13 (version the decision policy) | 2 | §3.3, §3.4 |

## §4 Phase 3 — Patch Layer (Writer)

### 4.1 Objective & scope

Build the Stage-2 investigation + patch layer: the `patch-writer` role implementation over the OpenRouter client, the constrained-workspace assembly engine, the `DocPatch` schema, and the application engine that turns verified-concept patches into Git working-tree changes. The writer has exactly five duties and never rewrites whole documents; the application — never the model — owns file selection, allowlists, and application. This phase delivers the write-path machinery used by Phase 5's orchestrator and verified in Phase 4.

### 4.2 Prerequisites & entry gate

- Phase 2 complete: OpenRouter client, `patch-writer` role interface, structured-output enforcement, telemetry [ARCH §10].
- Phase 1 complete: `ChangeAnalysis`, document graph, retrieval engine (bounded ranked candidates), claims storage [ARCH §4.1–4.5].
- Phase 0 complete: manifest validation, writable-path allowlist computation from the manifest, precedence chain [ARCH §11.3, §11.4; D7].
- Entry gate: decision layer emits valid `docsync.decision/v1` on fixtures; retrieval returns bounded candidate sets with provenance.

### 4.3 Features & deliverables

- `patch-writer` role implementation over the OpenRouter client: strong reasoning + long-context model class, temperature-0 default, structured output contract [ARCH §10.2, §10.3].
- Constrained-workspace assembly engine composing exactly the seven components — git diff, structured change analysis, Jev decision, manifest/policy, relevant source files, relevant documents, project instructions — and nothing else by default [ARCH §4.7, §3.3; CTX §5].
- Workspace bounding: size caps per component; related candidate documents sharing context batched into a single writer session [ARCH §3.7 (CI budget), §7.1 item 6].
- Protected-path read-only marking inside the workspace: locked/generated/external paths flagged analysis-only, patch emission forbidden (lock enforcement layer 2) [ARCH §11.4; D2].
- Writer's five duties encoded as the output contract: (1) verify docs actually stale, (2) inspect exact affected section, (3) determine minimum correct change, (4) produce a patch not a rewrite, (5) explain the evidence — duty 5 is mandatory output consumed by verification and reviewers [ARCH §4.7; CTX §5].
- `DocPatch` schema (zod + JSON Schema): target doc path, anchor (content-hash-first, line-range-second), structured hunks, per-hunk evidence references, writer provenance block [ARCH §4.9, §4.3, §12.5].
- Whole-document-rewrite structural rejection: schema marks hunked-only patches valid; full-file replacement payloads are structurally invalid before any model evaluation [ARCH §4.9, §4.8 Tier-1; invariant 12].
- Application engine: applies content-anchored hunks to working tree/head; content-hash-first anchor resolution; ambiguous anchor → abstain to REVIEW [ARCH §4.9, §4.3; invariant 13].
- Staleness-race handling: if the doc changed between writer read and patch application, 3-way merge is attempted only if the merged result re-passes verification; otherwise abstain to REVIEW — never force-applied [ARCH §4.9, §12.3; A9].
- Idempotent application: applying an already-applied patch is a no-op (content-anchored); stored run provenance keyed by run id, re-invocation detected and skipped [ARCH §12.1 `fix` idempotency, §12.5].
- Minimality bound hook: patch size compared against triggering diff size (hunk count / edit distance); bound violations surfaced to the verifier for rejection [ARCH §4.8 Tier-1, §4.9].
- Cross-patch consistency hook: per-patch claim values exposed so the verifier can detect same-claim-different-value across sibling patches (R16 groundwork; batched sessions) [ARCH §16.2 R16].
- Per-hunk evidence references machine-resolvable: file:line spans, symbol signatures, OpenAPI paths, config keys, test names [ARCH §4.8 Tier-2].
- Writer failure/timeout semantics: on timeout, emit a "potentially stale" finding without patch — the orchestrator degrades to review request [ARCH §3.4].
- Provenance capture: writer provenance names the actual routed model id (never a brand), policy_version, and generated_at timestamp, stored with the patch record [ARCH §10.6, §12.5].
- Working-tree guard integration: writer/application write only manifest-listed docs in writable modes; never code files, never `.docsync/` internals, never locked/generated docs [ARCH §12.1, §11.4; invariant 2].
- A2 benchmark plan: historical-PR replay harness measuring rewrite tendency and evidence quality of the writer (described + interface stubs; full execution lands with Phase 10 evaluation framework) [A2; ARCH §16.1].

### 4.4 TODO list

- [ ] Implement `patch-writer` role adapter over OpenRouterClient with temperature-0 params and structured-output enforcement [ARCH §10.2–10.4]
- [ ] Implement constrained-workspace assembler with the exact seven components and per-component size caps [ARCH §4.7, §3.3]
- [ ] Implement related-doc batching into single writer sessions within budget [ARCH §7.1 item 6, §3.7]
- [ ] Mark protected paths read-only in the workspace payload; forbid patch emission for locked/generated/external targets (lock layer 2) [ARCH §11.4; D2]
- [ ] Encode the five-duty output contract in the writer prompt interface + output schema validation [ARCH §4.7]
- [ ] Define and export `DocPatch` zod schema + JSON Schema (target, anchor w/ content hash, hunks, per-hunk evidence refs, provenance) [ARCH §4.9, §12.5]
- [ ] Implement structural whole-document-rewrite rejection (schema-level, before model evaluation) [ARCH §4.9; invariant 12]
- [ ] Implement content-anchored hunk application engine with content-hash-first anchor resolution [ARCH §4.3, §4.9]
- [ ] Implement ambiguous-anchor → UNANCHORED → REVIEW path (no silent re-anchoring) [ARCH §4.3; invariant 13]
- [ ] Implement doc-changed race path: 3-way merge only if merged result re-passes verification; else abstain REVIEW [ARCH §4.9, §12.3; A9]
- [ ] Implement idempotent apply + run-id provenance skip [ARCH §12.1, §12.5]
- [ ] Implement minimality measurement (hunk count/edit distance vs triggering diff) exposed to the verifier [ARCH §4.8 Tier-1, §9 (minimality measured)]
- [ ] Implement per-patch claim-value export for cross-patch consistency checking [R16; ARCH §16.2]
- [ ] Implement writer timeout → "potentially stale" finding without patch [ARCH §3.4]
- [ ] Implement writer provenance record w/ actual routed model id + policy_version + generated_at [ARCH §10.6, §12.5]
- [ ] Wire working-tree guard: application refuses anything outside the manifest-derived writable allowlist [ARCH §11.4, §12.1; invariants 2, 3]
- [ ] Stub the A2 historical-PR replay benchmark interface (rewrite tendency + evidence-quality scoring hooks) [A2; ARCH §16.1]
- [ ] Unit tests: workspace composition exactness; schema rejections; apply/race/idempotency; guard refusals [OD-2]

### 4.5 Verification list

- [ ] Workspace composition test: assembled payload contains exactly the seven components, capped, with protected paths marked read-only [ARCH §4.7, §11.4]
- [ ] A writer double returning a full-document replacement is rejected structurally before application [ARCH §4.9; invariant 12]
- [ ] A DocPatch with an unresolvable/ambiguous anchor abstains to REVIEW and never re-anchors silently [ARCH §4.3; invariant 13]
- [ ] Race test: doc modified between read and apply → no re-verified 3-way result → abstain REVIEW; with re-verification pass → applied [ARCH §4.9; A9]
- [ ] Idempotency test: second apply of an identical patch is a no-op; second run with same run id is skipped [ARCH §12.1]
- [ ] Minimality hook test: oversized patch relative to triggering diff is flagged for verifier rejection [ARCH §4.8]
- [ ] Guard tests: attempt to write a code file, `.docsync/` internal, or locked doc is refused at the application layer [ARCH §11.4; invariants 2, 3]
- [ ] Timeout test: writer budget exceeded → finding-without-patch record emitted, no partial application [ARCH §3.4]
- [ ] Every applied patch carries per-hunk evidence refs and writer provenance naming the actual routed model [ARCH §4.8, §10.6, §12.5]
- [ ] Per-hunk evidence refs resolve against fixture anchors (file:line, symbol, OpenAPI, config, test) [ARCH §4.8 Tier-2]
- [ ] Cross-patch claim-value export test: two patches asserting different values for the same claim id are detectable by the exported structure [R16]
- [ ] `pnpm test` green; lint clean; no network-dependent tests [OD-2]

Gate: phase complete when, on fixture repos with recorded writer doubles, every applied patch is a minimal content-anchored DocPatch carrying evidence + provenance, all guard/race/idempotency semantics are test-demonstrated, and whole-document rewrites are structurally impossible.

### 4.6 Risks, assumptions & decisions

- A2 (Sonnet reliably returns minimal anchored patches with evidence, not rewrites) — mitigation: structural rewrite rejection, minimality measurement, evidence-mandatory output contract, A2 benchmark on historical PRs; if A2 fails, structured-patch tooling is built harder, not prompted harder [ARCH §16.1].
- A9 (minimal-diff application survives real-world Markdown) — mitigation: content-hash-first anchoring, 3-way merge only on re-verification pass, abstain REVIEW on failure; 3-way-merge trials with adversarial fixtures (front-matter, fences, lists) [ARCH §16.1, §4.9].
- R16 (cross-patch inconsistency) — mitigation: batched sessions for related docs + per-patch claim-value export enabling same-claim-different-value detection (full detection lands with the verifier, §5) [ARCH §16.2].
- Decision taken: the writer never receives the whole repository; the workspace is assembled deterministically from manifest/retrieval state, never from model output [ARCH §4.7, §13].
- Decision taken: whole-document rewrites are structurally invalid in the `DocPatch` schema — rejection is not left to a validation "opinion" downstream [ARCH §4.8, §4.9; invariant 12].
- Decision taken: application authority lives in the application engine; the model's output is always untrusted data [ARCH §13].
- Alternative considered: letting the writer emit Markdown file bodies — rejected: violates minimality, claim-checking, and reviewability (§4.9 explicit avoidance).
- Alternative considered: force-applying with git-style auto-merge on races — rejected: only re-verified 3-way results may apply; everything else abstains [ARCH §4.9; A9].

> OPEN DECISION (OD-R08): Should `DocPatch` hunks reference claims by claim id (when the claim store already covers the anchor) or always carry independent evidence refs? — Recommendation: carry both when a claim id exists (stronger provenance) but require independent machine-resolvable evidence refs as the mandatory minimum, so patches remain valid on repositories with sparse claim coverage (V1 fallback per [ARCH §4.3]).

### 4.7 Architecture traceability

- Covered: [ARCH §4.7] Sonnet investigation + patch layer (workspace, five duties); [ARCH §4.9] patch strategy (minimal patch, no rewrites); [ARCH §3.3] CHANGE → workspace edge (constrained workspace contract); [ARCH §3.4] Sonnet timeout degradation; [ARCH §3.7] workspace bounding + batching; [ARCH §7.1 item 6] bounded workspace + batching; [ARCH §11.4 layer 2] writer constraints; [ARCH §12.1] fix idempotency groundwork; [ARCH §12.5] provenance capture; [ARCH §10.2–10.4] writer role + structured outputs.
- Covered: [D2] protected-path floor; [A2, A9] assumption gates; [R16] groundwork.
- Covered: [CTX §5] Sonnet duties; [CTX §29] patch strategy; [CTX §30] provenance.
- DEFERRED: [ARCH §15.2 / §16.3] claim-level edit objects as the patch representation — V2+ refinement; current DocPatch carries hunk-level anchors with claim-id references where available.
- DEFERRED: [ARCH §16.3] integrated mode (doc commits in the code PR) — deferred beyond V1; not touched by this phase.

### 4.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| Context | CTX §5 | 3 | §4.3, §4.4 (workspace, five duties) |
| Context | CTX §29 | 3 | §4.3, §4.5 (minimal patch, no rewrites) |
| Context | CTX §30 | 3 | §4.3, §4.4 (provenance capture) |
| ARCH | §3.3 | 3 | §4.3 (workspace edge contract) |
| ARCH | §3.4 | 3 | §4.3, §4.5 (timeout degradation) |
| ARCH | §3.7 | 3 | §4.3 (bounding, batching) |
| ARCH | §4.7 | 3 | §4.1–§4.4 (writer layer) |
| ARCH | §4.9 | 3 | §4.3–§4.5 (patch strategy) |
| ARCH | §4.8 (Tier-1 hooks) | 3 | §4.3, §4.5 (minimality, evidence export) |
| ARCH | §7.1 (item 6) | 3 | §4.3 (workspace + batching) |
| ARCH | §10.2–10.4 | 3 | §4.3, §4.4 (writer role, structured outputs) |
| ARCH | §11.4 (layer 2) | 3 | §4.3, §4.5 (writer constraints) |
| ARCH | §12.1 | 3 | §4.3, §4.5 (fix idempotency groundwork) |
| ARCH | §12.5 | 3 | §4.3, §4.4 (provenance) |
| ARCH | §15.2 | 3 | §4.7 (DEFERRED claim-level edits) |
| ARCH | §16.2 | 3 | §4.6 (R16) |
| Decision | D2 | 3 | §4.3, §4.5 (protected read-only) |
| Assumption | A2 | 3 | §4.5, §4.6 (benchmark gate) |
| Assumption | A9 | 3 | §4.5, §4.6 (3-way merge trials) |
| Risk | R16 | 3 | §4.3, §4.6 |
| Invariant | #2 (never modify code files) | 3 | §4.3, §4.5 |
| Invariant | #3 (never touch files outside allowlist) | 3 | §4.3, §4.5 |
| Invariant | #12 (no whole-document rewrites) | 3 | §4.3, §4.5 |
| Invariant | #13 (anchors don't silently move) | 3 | §4.3, §4.5 |
| Invariant | #8 (models never hold credentials) | 3 | §4.3 (app-owned application) |
| Principle | #5 (Sonnet for investigation/patch) | 3 | §4.1–§4.3 |
| Principle | #9 (no unrestricted write authority) | 3 | §4.3, §4.5 |
| Principle | #10 (minimal diffs) | 3 | §4.3–§4.5 |

## §5 Phase 4 — Verification Engine

### 5.1 Objective & scope

Build the Stage-3 verification engine: three-tier verification where every check emits a record into the patch's verification/provenance object — Tier 1 deterministic validators, Tier 2 evidence-backed claim checking, Tier 3 an independent judge pass. The engine is the safety net: patches must clear all three tiers; rejects fail closed to review with evidence attached, never silent drops. Delivers the `VerificationReport` schema consumed by the policy engine (Phase 5) and the PR layer (Phase 6).

### 5.2 Prerequisites & entry gate

- Phase 3 complete: `DocPatch` schema, minimality measurements, per-hunk evidence refs, claim-value export, application engine with working-tree guard [ARCH §4.9].
- Phase 2 complete: `judge` role interface, structured-output enforcement, bounded-retry → abstain semantics, calibration pinning [ARCH §10.2, §10.4].
- Phase 1 complete: claim records with content-hash anchors, contracts with classification vectors, source-of-truth access at PR head [ARCH §4.3, §4.4].
- Entry gate: fixture patches (valid, oversized, rewrite-attempting, evidence-free, unanchored-claim) available for the verification suites.

### 5.3 Features & deliverables

- Three-tier verification pipeline; **a patch must clear all three**; every check emits a record into the patch's verification/provenance object [ARCH §4.8].
- Tier 1 deterministic validators (always run) [ARCH §4.8 Tier-1]:
  - patch applies cleanly to current worktree/head;
  - only manifest-permitted files/sections touched; locked/generated/external documents untouched (D2 floor re-checked at verification);
  - minimality bound respected (patch size proportional to triggering diff; whole-document rewrites rejected outright);
  - structural integrity: Markdown fences/YAML/code blocks still parse; list/heading structure not silently mangled;
  - links resolve (internal anchors exist; external URLs reachable or explicitly allowlisted);
  - documented examples still parse and, where tooling exists, type-check or lint.
- Tier 2 evidence-backed claim checking (the core) [ARCH §4.8 Tier-2]:
  - patched region decomposed into claims (signatures, values/defaults, error codes, behavioral predicates);
  - every claim bound to a source anchor available in the constrained workspace: `file:line` span, symbol signature, OpenAPI path/operation, config key with resolved value, or test name;
  - verification against the source of truth at the PR head: signatures/field names/types match; values, defaults, enums, error codes match; behavioral predicates match documented contract clauses;
  - over-deletion guard: claims the patch removed are checked for whether their supporting source still exists (Phase 1 claim-store data feeds this);
  - claims with no resolvable anchor are flagged `unanchored` — may pass to `suggest`, **never** to `auto`.
- Tier 3 second decision pass (independent judge) [ARCH §4.8 Tier-3; D13]:
  - pre-doc, post-doc, and triggering diff re-scored on the typed question set: `stale_before?` / `resolved_after?` / `minimal?` / `introduced_new_inaccuracy?`;
  - judge is never the writer instance (instance-level constraint enforced in code via distinct session identity);
  - output `pass | review | reject`; rejects fail closed to review with evidence attached — never silently discarded.
- Contract-aware checking: per-document contract declarations + classification vectors consumed so a proposed patch must not silently violate an unmarked contract; contract hits at confidence ≥ policy thresholds scope the checking [ARCH §4.4].
- Zero-checkable-claims coverage defect: a doc with declared contracts but zero checkable claims is flagged as a coverage defect (surfaced through Phase 0 lint surface) [ARCH §4.4].
- `VerificationReport` schema (zod + JSON Schema): per-claim status, per-check tier records, evidence-check results, judge verdict, overall `pass | review | reject`, provenance (policy_version, model binding pinning per D3) [ARCH §4.8, §5.4; D3].
- Cross-patch consistency check: detects same-claim-different-value across sibling patches in one run (uses Phase 3 claim-value export) [R16; ARCH §16.2].
- Verifier-vs-human disagreement tracking: rejects/reroute outcomes recorded as a first-class metric input for Phase 10 (R5) [ARCH §16.2 R5].
- Unanchored-claim gating wiring: `unanchored` status forces the policy outcome below `auto` (consumed by Phase 2 policy engine via report) [ARCH §4.8 Tier-2].
- Provenance integration: every check record names its tier, inputs (blob SHAs at head), and outcome; reports are content-addressed and append-only in `.docsync/state/` [ARCH §6.3, §12.5].
- Judge model binding via `models.yml` with `must_not_equal_patch-writer_instance` constraint enforced at binding resolution [ARCH §10.3].

### 5.4 TODO list

- [ ] Define `VerificationReport` zod schema + JSON Schema export (per-claim status, tier records, judge verdict, overall outcome, provenance pinning) [ARCH §4.8; D3]
- [ ] Implement Tier-1 validator: clean-apply check against worktree/head [ARCH §4.8 Tier-1]
- [ ] Implement Tier-1 validator: manifest-permitted files/sections only; locked/generated/external untouched [ARCH §4.8 Tier-1; D2]
- [ ] Implement Tier-1 validator: minimality bound + whole-document-rewrite outright rejection [ARCH §4.8 Tier-1; invariant 12]
- [ ] Implement Tier-1 validator: structural integrity (fences/YAML/code blocks parse; lists/headings intact) [ARCH §4.8 Tier-1]
- [ ] Implement Tier-1 validator: link resolution (internal anchors; external URLs reachable or allowlisted) [ARCH §4.8 Tier-1]
- [ ] Implement Tier-1 validator: documented examples parse and type-check/lint where tooling exists [ARCH §4.8 Tier-1]
- [ ] Implement Tier-2 claim decomposition of the patched region [ARCH §4.8 Tier-2]
- [ ] Implement Tier-2 anchor binding (file:line, symbol, OpenAPI, config key, test name) restricted to the constrained workspace [ARCH §4.8 Tier-2]
- [ ] Implement Tier-2 head-verification: signatures/types, values/defaults/enums/error codes, behavioral predicates vs contract clauses [ARCH §4.8 Tier-2]
- [ ] Implement Tier-2 over-deletion guard using Phase 1 claim store [ARCH §4.8 Tier-2]
- [ ] Implement `unanchored` claim status with suggest-never-auto gating [ARCH §4.8 Tier-2]
- [ ] Implement contract-aware checking against per-doc contract vectors [ARCH §4.4]
- [ ] Implement zero-checkable-claims coverage-defect flag surfaced to the Phase 0 lint surface [ARCH §4.4]
- [ ] Implement Tier-3 judge invocation on the typed question set with pre-doc/post-doc/diff inputs [ARCH §4.8 Tier-3]
- [ ] Enforce judge-≠-writer instance constraint in binding resolution + runtime session identity [ARCH §10.3; D13]
- [ ] Implement `pass | review | reject` outcome with reject → fail-closed-to-review + evidence attachment [ARCH §4.8 Tier-3; D13]
- [ ] Implement cross-patch same-claim-different-value detection across sibling patches [R16; ARCH §16.2]
- [ ] Implement verifier-vs-human disagreement recording as metric input [R5; ARCH §16.2]
- [ ] Implement append-only, content-addressed storage of verification records under `.docsync/state/` [ARCH §6.3, §12.5]
- [ ] Implement schema-violating judge output → bounded retries → `review` (never fabricated pass) [ARCH §3.4, §10.4]
- [ ] Tests: adversarial fixture patches (oversized, rewrite, evidence-free, unanchored, contract-violating, link-rot) each rejected with the right tier record [OD-2]

### 5.5 Verification list

- [ ] Valid fixture patch passes all three tiers and produces a `VerificationReport` with per-claim statuses and a judge verdict [ARCH §4.8]
- [ ] Oversized patch (minimality bound violated) rejected at Tier 1 with a recorded bound measurement [ARCH §4.8 Tier-1]
- [ ] Full-document-rewrite payload rejected outright at Tier 1 regardless of content quality [ARCH §4.8, §4.9; invariant 12]
- [ ] Patch touching a locked/generated/external doc rejected at Tier 1 (D2 re-check) [ARCH §4.8; D2]
- [ ] Patch breaking a Markdown fence or YAML block rejected at Tier 1 with structural-integrity record [ARCH §4.8 Tier-1]
- [ ] Claim asserting a value/enum/error code that does not match the PR head is rejected at Tier 2 with the mismatch recorded [ARCH §4.8 Tier-2]
- [ ] Over-deletion test: patch removing a claim whose source support still exists is rejected (guard fires) [ARCH §4.8 Tier-2]
- [ ] Unanchored claim forces overall outcome below `auto` (suggest at best) in the report contract [ARCH §4.8 Tier-2]
- [ ] Contract-violating patch (silently drops a declared contract element) rejected at Tier 2 [ARCH §4.4]
- [ ] Judge returning `reject` fails closed to review with evidence attached; nothing is silently dropped [ARCH §4.8 Tier-3; D13]
- [ ] Judge schema-violating output → bounded retries → `review` outcome, never a fabricated pass [ARCH §3.4, §10.4]
- [ ] Same-claim-different-value across sibling patches is detected and blocks both patches to review [R16]
- [ ] Every check emits a record; report is content-addressed, append-only, and pins (policy_version, model binding) [ARCH §6.3, §12.5; D3]
- [ ] Verifier-vs-human disagreement events recorded for Phase 10 consumption [R5]
- [ ] `pnpm test` green; lint clean [OD-2]

Gate: phase complete when every fixture patch class (valid/oversized/rewrite/evidence-free/unanchored/contract-violating/link-rot/cross-inconsistent) is verified with the correct tier record and outcome, Tier 3 independence is enforced in code, and all verification records are append-only and provenance-pinned.

### 5.6 Risks, assumptions & decisions

- D13 (verification never self-approves) — structural: judge instance ≠ writer instance, enforced in binding resolution and runtime session identity; rejects fail closed to review with evidence [ARCH §10.3, §4.8 Tier-3].
- R5 (verifier false-rejects poisoning feedback data) — mitigation: verifier-vs-human disagreement tracked as its own metric; proportional minimality bounds; rejects land in review, never silent [ARCH §16.2].
- R16 (cross-patch inconsistency) — completed here: same-claim-different-value detection across sibling patches blocks both to review [ARCH §16.2].
- R11 tie-in (abstention quality) — `unanchored` → suggest-never-auto and judge-`review` outcomes are abstention data points for the Phase 2 health metric [ARCH §5.2, §4.8].
- Assumption noted: A4 (claim extraction + anchoring quality) is a Phase 1/V2 concern; this engine's fallback is section-level checking where claim extraction is weak — declared as a coverage gap in run records, never silent [ARCH §4.3, §16.1].
- Decision taken: all three tiers always run for any candidate patch — no fast-path skipping of Tier 2/3 even for trivial patches (safety net must be uniform) [ARCH §4.8].
- Decision taken: `unanchored` may reach `suggest` but never `auto` — encoded in the report contract consumed by the policy engine [ARCH §4.8 Tier-2].
- Decision taken: verification records are append-only and never modified by later runs (audit replay requirement) [ARCH §6.3].
- Alternative considered: single-tier verification (validators only) — rejected: deterministic validators cannot check factual claim support; Tier 2/3 exist for correctness, not ceremony [ARCH §4.8].
- Alternative considered: writer-instance self-verification to save latency — rejected outright by D13 [D13].

> OPEN DECISION (OD-R09): For behavioral predicates (ordering, idempotency, retry semantics) where deterministic checking is not yet possible in V1, should Tier 2 abstain to `suggest` (safe but noisy) or defer the predicate check entirely to Tier 3's judge? — Recommendation: abstain to `suggest` with an explicit `behavioral_predicate_unverified` check record in V1; Tier 3 alone is not sufficient evidence for `auto` on behavioral claims. Full predicate checking arrives with Phase 11's deeper understanding work.

### 5.7 Architecture traceability

- Covered: [ARCH §4.8] three-tier verification (all validators, claim checking, judge); [ARCH §4.3] claim anchors + over-deletion data; [ARCH §4.4] contracts + classification vectors + zero-claims defect; [ARCH §4.9] rewrite rejection + race abstention support; [ARCH §3.3] VERIFICATION → POLICY edge (`VerificationReport`); [ARCH §3.4] verification-failure degradation; [ARCH §5.2] abstention data points; [ARCH §10.3] judge binding + D13 constraint; [ARCH §6.3] storage; [ARCH §12.5] provenance records.
- Covered: [D2, D3, D13] enforcement points; [R5, R11, R16] mitigations; A4 fallback posture.
- Covered: [CTX §6] Stage 3; [CTX §11] claims as verification unit; [CTX §12] contracts; [CTX §30] evidence verification.
- DEFERRED: [ARCH §16.3] claim-level edit objects as patch representation (V2+) — verification consumes claim data now, claim-level edit objects arrive with Phase 11.
- DEFERRED: full behavioral-predicate deterministic checking (Phase 11 deeper understanding; OD-R09 posture in V1).

### 5.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| Context | CTX §6 | 4 | §5.1, §5.3 (Stage 3) |
| Context | CTX §11 | 4 | §5.3, §5.4 (claims as unit) |
| Context | CTX §12 | 4 | §5.3, §5.4 (contract checking) |
| Context | CTX §30 | 4 | §5.3, §5.5 (evidence records) |
| ARCH | §3.3 | 4 | §5.3 (VerificationReport edge) |
| ARCH | §3.4 | 4 | §5.4, §5.5 (failure degradation) |
| ARCH | §4.3 | 4 | §5.3, §5.4 (anchors, over-deletion) |
| ARCH | §4.4 | 4 | §5.3, §5.4 (contracts) |
| ARCH | §4.8 | 4 | §5.1–§5.5 (all tiers) |
| ARCH | §4.9 | 4 | §5.3, §5.5 (rewrite rejection) |
| ARCH | §5.2 | 4 | §5.6 (abstention data points) |
| ARCH | §6.3 | 4 | §5.3, §5.4 (storage) |
| ARCH | §10.3 | 4 | §5.3, §5.4 (judge binding) |
| ARCH | §12.5 | 4 | §5.3, §5.5 (provenance) |
| ARCH | §15.2 | 4 | §5.7 (DEFERRED claim-level edits) |
| ARCH | §16.2 | 4 | §5.6 (R5, R11, R16) |
| Decision | D2 | 4 | §5.3, §5.5 (locked untouched) |
| Decision | D3 | 4 | §5.3, §5.5 (report pinning) |
| Decision | D13 | 4 | §5.3–§5.6 (independence) |
| Assumption | A4 | 4 | §5.6 (fallback posture) |
| Risk | R5 | 4 | §5.5, §5.6 |
| Risk | R11 | 4 | §5.6 |
| Risk | R16 | 4 | §5.3, §5.5 |
| Invariant | #5 (no patch ships without verification) | 4 | §5.1, §5.5 |
| Invariant | #12 (no whole-document rewrites) | 4 | §5.3, §5.5 |
| Invariant | #13 (anchors don't silently move) | 4 | §5.3 (unanchored gating) |
| Invariant | #9 (fail closed) | 4 | §5.4, §5.5 |
| Principle | #6 (docs as claims backed by sources) | 4 | §5.3, §5.4 |
| Principle | #20 (correctness over edit volume) | 4 | §5.1, §5.5 |

---

## §6 Phase 5 — Orchestrator & Loop A End-to-End

### 6.1 Objective & scope

Phase 5 delivers the production orchestrator: a **deterministic state machine** (code + `.docsync/` configuration) that sequences the pipeline stages — analyzer → document graph → Jev → retrieval → writer → verification → policy engine — enforces every inter-stage contract, owns retries/budgets/degradation, and exposes the single-run pipeline API consumed by the CLI (Phase 7) and CI (Phase 8) [ARCH §13, §3.2–§3.5, §7.1]. It completes Loop A end-to-end on fixtures: PR → diff → affected docs → Jev → Sonnet → verification → policy outcome → reviewable artifact [CTX §26; ARCH §7.1]. No LLM is ever the root authority for permissions, file selection, thresholds, or mergeability [ARCH §13; CTX §38]. Out of scope: Git/PR side effects (Phase 6 consumes outcomes), check-run lifecycle (Phase 8), policy lifecycle/learning (Phase 10).

### 6.2 Prerequisites & entry gate

- Phases 0–4 complete: validated config core; deterministic analysis (Phase 1); decision layer + policy engine + OpenRouter access with role bindings (Phase 2); DocPatch writer (Phase 3); three-tier verification (Phase 4) [ARCH §3.2, §10].
- Entry gate: inter-stage contract tests (§3.3 table) green with model fakes; a fixture run traverses all stages without a real network call.

### 6.3 Features & deliverables

Orchestrator core [ARCH §13; CTX §38]:
- Deterministic stage graph; LLM invocations are leaf nodes with typed, schema-validated inputs/outputs; schema-violating output → bounded retry → abstain to REVIEW — never hand-patched into compliance [ARCH §13, §3.4, §10.4].
- Inter-stage contract enforcement (zod, from `@docsync/schemas`) at every edge of the §3.3 table; `RelevanceContext` is bounded — Jev never sees unbounded repository content [ARCH §3.3, §4.6].
- Application-owned concerns: permissions, file selection (the constrained Sonnet workspace per §4.7), policies, thresholds, workflow, retries, validation, Git-operation handoff (Phase 6), promotion/rollback handoff (Phase 10) [ARCH §13; CTX §38].

Failure & degradation defaults [ARCH §3.4]:
- Analyzer failure → inconclusive, never "no docs affected" (D14); Jev unavailable/schema-violating → bounded retries then REVIEW (abstain); Sonnet timeout → "potentially stale" finding without patch; verification failure → patch rejected, surfaced for review with evidence; model/provider outage → advisory/neutral posture (D10) — never fail-closed blocking merges unless policy opts in; **every failure degrades toward less automation, not more** [ARCH §3.4; D10, D14].

Policy integration [ARCH §5.2]:
- Four outcomes (ignore / suggest / auto / blocked) + `review` as a workflow state; abstention is first-class with enumerated `abstain_reason` (`insufficient_confidence`, `conflicting_evidence`, `policy_gap`, `calibration_floor`, `verifier_uncertain`); every abstained case (a) lands in the human-review queue with evidence attached and (b) becomes a labeled data point once resolved [ARCH §5.2; CTX §13].
- Verification integration honors D13: the Stage-3 judge is never the writer instance; rejects fail closed to review with evidence — never silent drops [ARCH §4.8, §0.3 D13].

Idempotency & caching [ARCH §3.5, §7.1]:
- Run key `(repo, base_sha, head_sha, manifest_version, policy_version)`; deterministic stages pure; LLM stage outputs cached by input hash + policy version; memoization keyed on `policy_version` so shadow runs (Phase 10) never poison production results (D6 interface) [ARCH §3.5, §7.1; D6].
- Re-running the same key produces the same decision trail [ARCH §3.5].

Loop A latency discipline [ARCH §7.1]:
- Stage budgets: deterministic analysis ≤ 2–5 s; Jev ≲ 1 s (typed model, small structured payload, no repository text); retrieval ≤ 1 s (indexed graph, no embedding search on the hot path unless declared fallback); Sonnet only for cases routed past Stage 1; verification = deterministic validators + one small second-decision pass [ARCH §7.1].
- Short-circuiting: the ignore-routed majority never invokes the writer; baseline checks short-circuit documents whose verified baseline still holds [ARCH §7.1, §6.1].
- Parallelism: independent candidate documents investigated concurrently (independent writer sessions when unrelated); deterministic validators run in parallel with the judge [ARCH §7.1].
- Early signal: post the check after Stage 1 (decision + candidates + confidence), update after Stages 2/3 — interface consumed by Phase 8 [ARCH §7.1, §12.2].
- Bounded workspace + batching: Sonnet's workspace capped; related candidate docs sharing context batched into one session [ARCH §7.1, §4.7].
- Graceful degradation: if the writer stage exceeds budget, emit a review request with change analysis and evidence instead of blocking on a patch — a late *suggestion* is acceptable; a hung check is not [ARCH §7.1].

Bounded loop [ARCH §3.7, §7.1]:
- Max docs investigated / max patches per run; overflow routes to REVIEW with a "partial coverage" marker — never silently dropped [ARCH §3.7, §7.1].

Single-run pipeline API [ARCH §12.1]:
- `runPipeline(input) → RunOutcome`: per-document policy outcomes, verification results, candidate mapping, abstain records, provenance references, and one **canonical report object** that renders identically as CLI text (Phase 7) and check-run markdown (Phase 8) — local and CI never disagree about the facts [ARCH §12.1, §7.1].
- Concurrency-group interface `docsync-<repo>-<pr>` exposed for Phases 6/8 (new push supersedes in-flight run) [ARCH §12.2, §7.1].
- State-machine integration: PR-time differential evaluation via baselines; transitions written through CAS [ARCH §6.1, §6.2].

Injection containment [ARCH §10.7, §13; R8]:
- Repository content passed to models is untrusted input; model outputs are untrusted data validated against typed schemas; no model output influences permissions, file selection, or Git credentials; workspace assembly is computed by the application from manifest/policy/analysis only [ARCH §10.7, §13].
- Models never hold credentials; keys live only in the orchestrator environment (CI secret store / local env) [ARCH §10.7; invariant 8].

Telemetry [ARCH §10.6, §7.1]:
- Per-stage `latency_ms` in traces for budget enforcement; OpenRouter usage-accounting interface (cost attribution per run / policy version, feeding §9 efficiency later) [ARCH §10.6].

### 6.4 TODO list

- [ ] P5-T01 — Orchestrator skeleton: typed stage graph, run context, run-record creation keyed per §3.5 [ARCH §3.5, §13]
- [ ] P5-T02 — Inter-stage contract enforcement: zod validation at every §3.3 edge; contract test suite [ARCH §3.3]
- [ ] P5-T03 — Decision-stage integration (Jev role): typed answers with value+probability; bounded retry → abstain REVIEW [ARCH §4.6, §10.4]
- [ ] P5-T04 — Routing: IGNORE / REVIEW / CONTINUE branching; ignore short-circuit (writer never invoked) [ARCH §4.6, §7.1]
- [ ] P5-T05 — Retrieval integration: bounded ranked candidates with level provenance [ARCH §4.5]
- [ ] P5-T06 — Writer-stage integration: constrained workspace assembly (exact §4.7 set, application-owned) [ARCH §4.7, §13]
- [ ] P5-T07 — Verification integration: three tiers; D13 judge/writer instance separation asserted; rejects fail closed to review [ARCH §4.8; D13]
- [ ] P5-T08 — Policy-engine integration: four outcomes + first-class abstain with enumerated reasons; review-queue writes [ARCH §5.2; CTX §13]
- [ ] P5-T09 — Degradation matrix per §3.4 (analyzer / Jev / writer / verifier / outage paths) with D14 assertions [ARCH §3.4; D14]
- [ ] P5-T10 — Idempotency + caching: deterministic purity; LLM result cache keyed input-hash + policy_version; shadow-key isolation [ARCH §3.5, §7.1; D6]
- [ ] P5-T11 — Latency budgets + early-signal check-update interface (post-Stage-1, post-Stage-2/3) [ARCH §7.1]
- [ ] P5-T12 — Parallelism: concurrent per-doc investigation; validators ∥ judge [ARCH §7.1]
- [ ] P5-T13 — Bounded loop: max-docs/max-patches caps; overflow → REVIEW with partial-coverage marker [ARCH §3.7, §7.1]
- [ ] P5-T14 — Baseline-aware PR-time differential evaluation; Loop B shared-records interface [ARCH §6.1, §7.2]
- [ ] P5-T15 — Single-run pipeline API + canonical report object (CLI/check-run render parity contract) [ARCH §12.1]
- [ ] P5-T16 — Injection containment: untrusted-input marking, output schema gates, credential-isolation assertions [ARCH §10.7, §13; R8]
- [ ] P5-T17 — Telemetry: per-stage latency_ms + OpenRouter usage/cost attribution interface [ARCH §10.6, §7.1]
- [ ] P5-T18 — E2E fixture suite: full Loop A matrix + A10 large-diff load probe [ARCH §7.1; A10]

### 6.5 Verification list

- [ ] V-P5-1 — Ignore-routed fixture PR completes without writer invocation; p50 within budget (analysis ≤ 2–5 s, Jev ≲ 1 s on fake models) [ARCH §7.1]
- [ ] V-P5-2 — Continue-routed fixture: DocPatch → three-tier verification → policy outcome → RunOutcome + run record [ARCH §3.2, §4.8]
- [ ] V-P5-3 — Abstain cases surface with named `abstain_reason` and land in the review queue with evidence [ARCH §5.2]
- [ ] V-P5-4 — Schema-violating Jev output → bounded retry → REVIEW; never hand-patched [ARCH §10.4, §3.4]
- [ ] V-P5-5 — Writer timeout → "potentially stale" finding without patch; early-signal updated [ARCH §3.4, §7.1]
- [ ] V-P5-6 — Verification reject → review with evidence attached; never a silent drop [D13; ARCH §4.8]
- [ ] V-P5-7 — Simulated OpenRouter outage → advisory/neutral posture; no merge blocking (D10) [ARCH §3.4; D10]
- [ ] V-P5-8 — Injected analyzer crash → inconclusive, never "no docs affected" [D14]
- [ ] V-P5-9 — Same run key re-executed → identical decision trail; shadow-keyed memoization never leaks into production results [ARCH §3.5; D6]
- [ ] V-P5-10 — Early signal observable after Stage 1 and updated after Stages 2/3 (fake-platform assertions) [ARCH §7.1]
- [ ] V-P5-11 — Cap overflow doc → REVIEW with partial-coverage marker, never silent [ARCH §3.7]
- [ ] V-P5-12 — CAS conflict with a concurrent run → graceful re-run, no store corruption [R15; ARCH §6.2]
- [ ] V-P5-13 — Injection probe: hostile repository content cannot alter permissions, file selection, or output handling [R8; ARCH §10.7, §13]
- [ ] V-P5-14 — Workspace assembly provably independent of model output; no credential reaches any model payload [ARCH §13; invariant 8]
- [ ] V-P5-15 — A10 load probe: large-diff fixture completes routed path p50 ≤ ~60 s or exercises the §3.7 downgrade path [A10; ARCH §7.1, §3.7]
- [ ] V-P5-16 — RunOutcome schema frozen + contract-tested as the Phase 7/8 consumption interface [ARCH §12.1]

Gate: phase complete when the Loop A E2E matrix (ignore / review / continue / abstain / every §3.4 degradation path) is green on fixtures, latency budgets hold under the A10 load probe, injection-containment probes pass, and the single-run pipeline API (RunOutcome + canonical report object) is frozen for Phases 7–8.

### 6.6 Risks, assumptions & decisions

Assigned gates:
- **A10 (latency envelope)** — load probe (P5-T18) validates p50 ≤ ~60 s routed against real PR sizes and modeled OpenRouter latencies; breach exercises the §3.7 downgrade + REVIEW path rather than silently missing budgets [ARCH §16.1 A10, §7.1, §3.7].
- **R8 (prompt injection via repository content)** — containment at the orchestrator boundary: untrusted-input marking, typed-schema output gates, credential isolation (P5-T16, V-P5-13) [ARCH §16.2 R8, §10.7, §13].
- **R15 (multi-writer races)** — CAS + graceful re-run at state transitions [ARCH §16.2 R15, §6.2].
- **R6 interface** (doc PR ↔ code PR conflicts) — the orchestrator supplies the outcome record + idempotency key that Phase 6's supersession/conflict machinery consumes; conflict handling itself lands in Phase 6 [ARCH §12.2–§12.3; R6].
- **D6 interface** — memoization keying includes `policy_version` so Phase 10's shadow runs cannot poison production caches [ARCH §0.3 D6, §7.1].
- **D13** — judge/writer instance separation asserted at orchestration level [ARCH §0.3 D13].
- **D14** — every failure degrades toward less automation; the degradation matrix is a first-class test surface [ARCH §0.3 D14, §3.4].

Decisions taken:
- The orchestrator is a deterministic state machine; production never depends on an unconstrained LLM orchestrator; LLMs operate only as bounded leaf stages [ARCH §13; CTX §37/§38].
- The stage graph is declared in code + `.docsync/` configuration, never in prompts [ARCH §13].
- One canonical report object renders all surfaces (CLI text, check-run markdown) — the mechanical guarantee behind "local and CI never disagree" [ARCH §12.1, §7.1].
- Development/research orchestration may use sub-agents; the production orchestrator does not [ARCH §13, Appendix A; CTX §37].

Alternatives considered:
- Event-driven/queue-based orchestrator for V1 (rejected: single-run API keeps Phase 5 testable; queues arrive with the Phase 8 App runtime).
- LLM-assisted planning/routing inside the orchestrator (rejected outright: §13 forbids LLM root authority over workflow).
- Retrying schema-violating model output by prompt patching (rejected: bounded retry then abstain, §10.4).

> OPEN DECISION (OD-R10): concurrency implementation for parallel per-doc investigation — in-process p-limiter pool vs worker-thread isolation. — Recommendation: in-process pool with per-role concurrency caps (§10.5); worker isolation only if memory pressure is observed in the A10 load probe.

> OPEN DECISION (OD-R11): LLM result cache store — content-addressed files under `state/` vs in-memory-only. — Recommendation: content-addressed files (cross-run reuse + auditability) with LRU eviction of cache entries only — never state records; final call at implementation review with Phase 2.

### 6.7 Architecture traceability

Covered: [ARCH §3.2], [ARCH §3.3], [ARCH §3.4], [ARCH §3.5], [ARCH §4.5] (retrieval consumption), [ARCH §4.6] (routing), [ARCH §4.7] (constrained workspace), [ARCH §4.8] (verification integration), [ARCH §5.2] (abstain path), [ARCH §6.1], [ARCH §6.2], [ARCH §7.1], [ARCH §7.2] (shared-records interface), [ARCH §10.4], [ARCH §10.5], [ARCH §10.6], [ARCH §10.7], [ARCH §12.1] (pipeline API/parity), [ARCH §12.2] (concurrency-group interface), [ARCH §13], [ARCH §14 invariants 5, 6, 8, 9], [ARCH §0.3 D6, D10, D13, D14], [ARCH §16.1 A10], [ARCH §16.2 R6 (interface), R8, R15], [CTX §5], [CTX §6], [CTX §7], [CTX §13], [CTX §26], [CTX §37], [CTX §38].

DEFERRED:
- [ARCH §16.3 / §7.2] Loop B differential sweeps — Phase 11; records + `audit-cursor` interface only here.
- [ARCH §8.6 / D6] Live shadow sampling — Phase 10; memoization keying interface only here.
- [ARCH §10 / §16.3] Org-wide model-routing policies — V3; per-role bindings from Phase 2 consumed as-is.

### 6.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §5 | 5 | §6.3 stage integration (Jev/Sonnet roles); §6.4 P5-T03/T06 |
| CTX | §6 | 5 | §6.3 three-stage pipeline sequencing; §6.5 V-P5-2 |
| CTX | §7 | 5 | §6.3 full architecture realized E2E; §6.5 V-P5-2 |
| CTX | §13 | 5 | §6.3 abstain as first-class outcome; §6.5 V-P5-3 |
| CTX | §26 | 5 | §6.1/§6.3 Loop A E2E (Loop B interface only) |
| CTX | §37 | 5 | §6.6 production vs research orchestrator distinction |
| CTX | §38 | 5 | §6.3 orchestrator owns permissions/files/thresholds |
| ARCH | §3.2 | 5 | §6.3 stage sequencing; §6.5 V-P5-2 |
| ARCH | §3.3 | 5 | §6.3 contract enforcement; §6.4 P5-T02 |
| ARCH | §3.4 | 5 | §6.3 degradation matrix; §6.5 V-P5-4..8 |
| ARCH | §3.5 | 5 | §6.3 idempotency; §6.5 V-P5-9 |
| ARCH | §4.5 | 5 | §6.3 retrieval consumption |
| ARCH | §4.6 | 5 | §6.3 routing; §6.4 P5-T04 |
| ARCH | §4.7 | 5 | §6.3 constrained workspace; §6.4 P5-T06 |
| ARCH | §4.8 | 5 | §6.3 verification integration; §6.5 V-P5-6 |
| ARCH | §5.2 | 5 | §6.3 abstain path; §6.5 V-P5-3 |
| ARCH | §6.1 | 5 | §6.3 baseline short-circuit + differential evaluation |
| ARCH | §6.2 | 5 | §6.3 CAS transitions; §6.5 V-P5-12 |
| ARCH | §7.1 | 5 | §6.3 latency discipline; §6.5 V-P5-1/10/15 |
| ARCH | §7.2 | 5 | shared-records interface (sweeps DEFERRED Phase 11) |
| ARCH | §10.4 | 5 | §6.3 schema-validated outputs; §6.5 V-P5-4 |
| ARCH | §10.5 | 5 | §6.6 per-role concurrency caps |
| ARCH | §10.6 | 5 | §6.4 P5-T17 telemetry/cost attribution |
| ARCH | §10.7 | 5 | §6.3 containment + credentials; §6.5 V-P5-13/14 |
| ARCH | §12.1 | 5 | §6.3 pipeline API + canonical report; §6.5 V-P5-16 |
| ARCH | §12.2 | 5 | §6.3 concurrency-group interface (Phase 6/8 consume) |
| ARCH | §13 | 5 | §6.3 deterministic orchestrator; §6.5 V-P5-13/14 |
| ARCH | §0.3 D6 | 5 | §6.3 memoization keying; §6.5 V-P5-9 |
| ARCH | §0.3 D10 | 5 | §6.5 V-P5-7 outage → neutral |
| ARCH | §0.3 D13 | 5 | §6.3 judge separation; §6.5 V-P5-6 |
| ARCH | §0.3 D14 | 5 | §6.3 degradation; §6.5 V-P5-8 |
| ARCH | §14 inv 5 | 5 | verified-only patches flow; §6.4 P5-T07 |
| ARCH | §14 inv 6 | 5 | abstain, don't guess; §6.4 P5-T08 |
| ARCH | §14 inv 8 | 5 | credentials isolation; §6.5 V-P5-14 |
| ARCH | §14 inv 9 | 5 | fail-closed degradation; §6.5 V-P5-7 |
| ARCH | §16.1 A10 | 5 | §6.4 P5-T18; §6.5 V-P5-15 |
| ARCH | §16.2 R6 | 5 | outcome/idempotency-key interface to Phase 6 |
| ARCH | §16.2 R8 | 5 | §6.4 P5-T16; §6.5 V-P5-13 |
| ARCH | §16.2 R15 | 5 | §6.5 V-P5-12 |
| ARCH | §16.3 | 5 | DEFERRED Loop B sweeps; shadow sampling; org routing |
| ARCH | §2.3 P12 | 5 | explicit abstain/review path surfaced |
| ARCH | §2.3 P19 | 5 | deterministic production orchestrator |

## §7 Phase 6 — Git & PR Integration

### 7.1 Objective & scope

Objective: implement the Git/PR write path that turns verified pipeline output into reviewable Git artifacts — the separate documentation PR end to end: branch factory, commit assembly, PR creation/update with rate/burst governance and idempotency, merge-order-conflict handling, PR-side safety filtering (lock-enforcement layer 4), reviewer feedback-event capture, and the reviewer-override → policy-proposal seam. PR-first is the product's preferred workflow: developers review documentation changes exactly like code changes [ARCH §1.1; CTX §1].

In scope: PR client seam + platform adapters (@docsync/core), branch factory, commit/PR assembly, PR body provenance rendering, rate/burst governor, idempotency keys, concurrency-group supersedence, merge-order ledger, supersede/revert disambiguation, PR filter (§11.4 layer 4), safety-incident log sink, feedback event capture/normalizer, `state/` carriage on doc PRs.
Out of scope (owned elsewhere): check-run lifecycle, GitHub App permission model, webhook surface, `requested_action` buttons (Phase 8 — @docsync/github-app, @docsync/action); orchestrator run sequencing and the repo-scoped run queue (Phase 5); provenance record authoring (Phase 5 composition, Phase 9 hardening — this phase enforces presence and renders); feedback recorder/evaluation integration (Phase 10 — this phase captures raw events in the shared schema); baseline re-resolution math (Phase 1/D12 — this phase triggers re-checks).

### 7.2 Prerequisites & entry gate

- Phase 5 orchestrator produces, per run: verified DocPatches (or per-document failures), decision objects, provenance records, `state/` updates, candidate mapping [ARCH §3.3].
- Phase 3 application engine + writable-path allowlist; Phase 4 verification verdicts per document.
- Platform credentials available via injection (App installation token in CI mode, developer push credentials in local mode); token mechanics arrive with Phase 8 but the injection seam exists now [ARCH §12.2].
- Entry gate: on a fixture repo with a fixture remote — a pipeline run creates exactly one doc PR aggregating all candidates; a second identical run updates the same PR (idempotent); a simulated 429 degrades to report-only; a patch containing an out-of-allowlist hunk aborts PR creation and logs a safety incident; a doc PR by docsync itself never re-triggers the pipeline.

### 7.3 Features & deliverables

**Separate documentation PR [ARCH §12.4; CTX §34]**

- Doc changes ship as a **separate PR** — never direct pushes to the developer's code PR, never direct commits to the default branch; the five reasons honored: independently reviewable, AI changes isolated, easy rollback, code PR stays focused, clear provenance [ARCH §12.4; CTX §34].
- Branch factory: `docsync/<source-pr-number>-docs` for source-PR flows, `docsync/<run-id>` for local flows; deterministic names `docsync/pr-481-<head-sha7>` keep dedup simple; hard guard: no ref creation targeting the default branch, ever [ARCH §12.2, §12.4; INV 1].
- PR title convention: `docs: synchronize documentation with PR #481` (source-PR-linked; deterministic suffix distinguishes rare multiples) [ARCH §12.4; CTX §34].
- PR body: provenance summary (canonical view per D5), candidate mapping table, verification results, and the "generated by docsync — review like any code change" banner [ARCH §12.4, §12.5].
- The doc PR **links to** the source PR but does not mechanically depend on it; integrated mode (doc commits appended to the code PR) is explicitly deferred, opt-in only after the trust loop is proven [ARCH §12.4; DEFERRED §16.3].
- **One documentation PR per source PR per run** (D4): all candidate docs aggregate into a single PR [ARCH §12.2; D4].
- **Per-document partial-pass semantics** (D4): a single failing doc's verification does not block the others' merge-worthiness — the failing doc is dropped from the PR and reported, never silently [ARCH §12.2; D4].
- Doc PR carries its system-written `.docsync/state/` updates alongside the documentation hunks so the audit trail rides a reviewable PR (invariant 1 forbids default-branch pushes; see OD-R12, registry §0.5) [ARCH §6.3; A8].

**Rate, burst, idempotency, lifecycle [ARCH §12.2]**

- Idempotency key = `(source PR head SHA, doc-set content hash)`; re-runs update the existing check/doc PR instead of duplicating [ARCH §12.2].
- Max open docsync PRs per repository: 3 (configurable 1–10); at the cap: downgrade to check-run report + "patch ready, apply manually" [ARCH §12.2].
- Max doc PRs per repository per hour: 10 (configurable); bursts queue, not fail [ARCH §12.2; INV 11].
- Concurrency group `docsync-<repo>-<pr>`: a new push supersedes the in-flight run [ARCH §12.2].
- 429/403 secondary-rate responses: exponential backoff with jitter, then fail-safe to report-only; the system never retries its way into a flood [ARCH §12.2; INV 11].
- Stale doc PRs: if the source PR evolves after the doc PR opens, the doc PR is marked "superseded by newer analysis" and the idempotent re-run replaces it; legitimate re-creation after a revert gets a deterministic disambiguator suffix [ARCH §12.2; R6].
- Self-trigger suppression: docsync ignores its own doc PRs — keyed by branch prefix `docsync/` **and** author identity, not just path; the system never creates a doc PR from its own doc PR [ARCH §12.2].
- Platform-agnostic PR client with injected credentials: App installation tokens (CI) or developer credentials (local) are supplied by the caller; the PR layer stores no credentials itself and performs every Git/API write deterministically [ARCH §12.2, §13; INV 8].

**Merge-order conflicts & PR lifecycle [ARCH §12.3]**

- Rebase the doc branch on the current default branch before pushing [ARCH §12.3].
- If the target doc changed since the writer read it: re-apply/3-way merge **only if the merged result re-passes verification**; a failed 3-way merge abstains to REVIEW, never force-applies (A9) [ARCH §12.3, §12.1; A9].
- If a prior doc PR already fixed the drift: claims are re-evaluated against new HEAD and the no-op patch is dropped [ARCH §12.3].
- Doc PR checks re-run against the merge target (merge queues, squash merges) — triggering baseline re-resolution per D12 [ARCH §12.3; D12].
- Post-merge `push` handling (wired in Phase 8; trigger primitive built here): evaluation re-runs after the code PR merges so a doc PR that no longer applies is superseded rather than conflicting [ARCH §12.3].
- Merge-order ledger: per source-PR record of doc-PR state (`open | superseded | merged | conflicted | dropped`) driving rebase/supersede/no-op transitions [ARCH §12.3; R6].

**PR-side safety (lock enforcement layer 4) [ARCH §11.4; §14]**

- PR filter (layer 4): before creating a doc PR, the orchestrator re-diffs the branch against the writable-path allowlist **computed from the manifest itself, not from model output**; any out-of-allowlist change aborts PR creation [ARCH §11.4; INV 2, 3].
- Whole-patch rejection: a patch touching a protected path is rejected whole — no partial application survives to the PR [ARCH §11.4; INV 4].
- Safety-incident log: out-of-allowlist or lock-violating changes abort PR creation and are logged as **safety incidents** (append-only, alert-worthy bug class — never silently swallowed); alerting surface lands in Phase 9 [ARCH §11.4].
- Machine-proposed PRs never carry a mode transition to `auto` and never modify `manifest.yml`/policy files — the only path to a mode change is a human PR [ARCH §11.4; INV 7].

**Reviewer feedback & override proposals [ARCH §8.7, §11.4; CTX §22, §23]**

- Feedback event capture from the doc PR lifecycle: the five reviewer actions — **accept, edit, reject, mark as intentional, always ignore this pattern** — detected from platform events (merge, review-with-edits, closure/rejection, labels, review comments) [ARCH §8.7; CTX §23; §15.1].
- Label hygiene normalization: timeout ⇒ `unlabeled` (never `accepted`); edit ⇒ `corrected` with the edited diff stored (the edit shows both what was wrong and what "minimal" meant to the human); reject ⇒ `false_positive` only when the stated reason is "doc already correct", otherwise `style_rejected` [ARCH §8.7].
- Feedback rows carry `policy_version`, decision id, and signal type, emitted in the shared structured record format — the DX layer never invents its own event schema; the Phase 10 recorder consumes [ARCH §8.7, §8.8].
- False-negative detection: when Jev routed `ignore` but the developer edits documentation inside the source PR, a FALSE NEGATIVE signal is recorded [ARCH §8.7; CTX §22].
- Reviewer override acknowledgment: the "always ignore this pattern" click is acknowledged instantly on the surface; accumulated overrides (e.g., 17 overrides of "src/generated/** is not documentation-relevant") become Policy-Architect *proposals* via the promotion pipeline — **models propose; humans promote**; the policy change arrives later as a reviewable PR [ARCH §11.4; P14].
- Feedback loop exists from day one: V1 ships the five reviewer actions with the first patches — the feedback loop must exist as early as the patches do, or evaluation data never accumulates [ARCH §15.1].

**State & provenance integration [ARCH §12.5, §6.3]**

- Provenance required: the orchestrator refuses to create a doc PR whose commits lack a provenance record — a provenance-less PR is a bug, not a format option [ARCH §12.5; INV 10].
- Provenance rendering per D5 precedence (PR body = canonical; `state/` = archival; check-run = ephemeral): the PR layer renders records, never mutates Markdown document bodies for provenance [ARCH §12.5; D5].
- Upsert semantics: re-invoking with the same idempotency key updates the existing PR and appends the provenance record (content-addressed, append-only; never a history rewrite) [ARCH §12.5, §12.2].
- State CAS participation: doc PR creation cooperates with the repo-scoped concurrency queue and compare-and-swap state writes; the losing writer (CI run vs local run) re-runs evaluation rather than overwriting (R15; queue implementation owned by Phase 5) [ARCH §6.3; R15].

**Determinism [ARCH §13]**

- PR creation is deterministic orchestrator code: LLM outputs never influence permissions, file selection, or mergeability; no model sits in the write path [ARCH §13; P19].

### 7.4 TODO list

- [ ] Implement branch factory with deterministic naming (`docsync/<source-pr>-docs`, `docsync/pr-<n>-<sha7>`, `docsync/<run-id>`) and a hard default-branch guard that fails on any default-branch ref [ARCH §12.2, §12.4; INV 1]
- [ ] Implement commit assembly: documentation hunks + `.docsync/state/` updates + conventional commit message; assert provenance-record presence before any commit [ARCH §6.3, §12.5; INV 10]
- [ ] Implement PR body renderer: provenance summary, candidate mapping table, verification results, generated-by banner [ARCH §12.4, §12.5; D5]
- [ ] Implement platform-agnostic PR client seam with credential injection (no stored credentials; adapter interface for App token / developer creds) [ARCH §12.2, §13; INV 8]
- [ ] Implement idempotency key computation `(source PR head SHA, doc-set content hash)` + existing-PR lookup/update path [ARCH §12.2]
- [ ] Implement open-PR cap governor (default 3, configurable 1–10) with downgrade-to-report + "patch ready, apply manually" behavior [ARCH §12.2]
- [ ] Implement hourly burst limiter (default 10/hour, configurable) with queueing, never failing on burst [ARCH §12.2; INV 11]
- [ ] Implement concurrency group `docsync-<repo>-<pr>` with in-flight supersedence [ARCH §12.2]
- [ ] Implement 429/403 exponential backoff with jitter + fail-safe to report-only [ARCH §12.2; INV 11]
- [ ] Implement per-document partial-pass PR assembly: drop failing docs from the PR with a report entry; never block other docs [ARCH §12.2; D4]
- [ ] Implement PR filter (layer 4): re-diff branch against manifest-derived allowlist; abort PR creation on any violation; reject protected-path patches whole [ARCH §11.4; INV 2, 3, 4]
- [ ] Implement append-only safety-incident log sink (incident record shape: run id, offending path, rule violated) [ARCH §11.4]
- [ ] Implement stale-PR supersede flow: supersede markers, idempotent replacement, deterministic disambiguator suffix after revert [ARCH §12.2; R6]
- [ ] Implement rebase-before-push + 3-way-merge gate wired to the verification re-run hook (merge allowed only on re-verified result; failure abstains to REVIEW) [ARCH §12.3; A9]
- [ ] Implement drift-already-fixed detection: claims re-evaluated against new HEAD; drop no-op patches [ARCH §12.3]
- [ ] Implement merge-target re-check trigger for merge queues/squash merges (D12 baseline re-resolution kickoff) [ARCH §12.3; D12]
- [ ] Implement the post-merge push re-run trigger primitive (webhook wiring + default-off posture land in Phase 8, OD-R19) [ARCH §12.3]
- [ ] Implement merge-order ledger record per source PR [ARCH §12.3; R6]
- [ ] Implement self-trigger suppression (branch prefix + author identity check) [ARCH §12.2]
- [ ] Implement feedback event listeners for the five reviewer actions over PR events (merge, review-with-edits, closure, labels, comments) [ARCH §8.7; CTX §23]
- [ ] Implement label-hygiene normalizer (unlabeled / corrected+diff / false_positive / style_rejected rules) [ARCH §8.7]
- [ ] Implement shared-schema feedback record emission carrying `policy_version`, decision id, signal type [ARCH §8.7, §8.8]
- [ ] Implement in-source-PR doc-edit detector emitting FALSE NEGATIVE records after ignore routing [ARCH §8.7; CTX §22]
- [ ] Implement instant acknowledgment for override actions + Policy-Architect proposal queue entry (Phase 10 seam) [ARCH §11.4]
- [ ] Implement provenance-required gate refusing PR creation without a record [ARCH §12.5; INV 10]
- [ ] Implement state CAS participation with the repo-scoped queue (loser re-runs) [ARCH §6.3; R15]
- [ ] Implement machine-PR content guard: reject any machine-proposed change to `manifest.yml`/policy files or any mode→auto transition [ARCH §11.4; INV 7]
- [ ] Tests: create/update/supersede/cap/burst/backoff/filter/incident/suppression matrix on a fixture remote [ARCH §12.2, §11.4]
- [ ] Document PR conventions reference matching CTX §34 examples [CTX §34]

### 7.5 Verification list

- [ ] Fixture run creates exactly one doc PR aggregating N candidate docs; a failing doc is dropped and reported while the rest stay merge-worthy [ARCH §12.2; D4]
- [ ] Re-run with the same idempotency key updates the existing PR — zero duplicate PRs across 3 consecutive runs [ARCH §12.2]
- [ ] Open-PR cap test: the 4th concurrent doc PR attempt downgrades to report + "patch ready, apply manually" [ARCH §12.2]
- [ ] Burst test: the 11th PR request within an hour queues and completes later without failing [ARCH §12.2]
- [ ] Backoff test: simulated 429 → exponential backoff with jitter → report-only fallback; no retry flood observed [ARCH §12.2; INV 11]
- [ ] Default-branch guard: any attempt to push or open a PR against the default branch hard-fails in tests [ARCH §12.4; INV 1]
- [ ] PR filter tests: a patch containing a code-file hunk aborts PR creation + incident logged; a locked/generated doc hunk aborts whole [ARCH §11.4; INV 2, 3, 4]
- [ ] Provenance gate test: PR creation without a provenance record is refused with a diagnostic [ARCH §12.5; INV 10]
- [ ] Supersede test: new push on the source PR marks the old doc PR "superseded by newer analysis" and the re-run replaces it; revert + re-create gets the disambiguator suffix [ARCH §12.2; R6]
- [ ] Re-verify gate test: doc changed upstream → 3-way merge attempted only after re-verification passes; failed merge abstains to REVIEW with the working tree untouched [ARCH §12.3; A9]
- [ ] No-op drop test: drift already fixed upstream → claims re-evaluated → no-op patch dropped, no PR noise [ARCH §12.3]
- [ ] Self-trigger test: a run triggered by docsync's own doc PR is suppressed (branch + identity match) [ARCH §12.2]
- [ ] Feedback tests: each of the five actions produces the correctly-labeled record — timeout ⇒ `unlabeled`, edit ⇒ `corrected` with stored diff, ambiguous reject ⇒ `style_rejected`, "doc already correct" reject ⇒ `false_positive` [ARCH §8.7]
- [ ] Feedback schema test: every record carries `policy_version`, decision id, signal type and validates against the shared record schema [ARCH §8.7, §8.8]
- [ ] False-negative test: doc edit inside the source PR after `ignore` routing produces a FALSE NEGATIVE record [ARCH §8.7; CTX §22]
- [ ] Mode-transition guard test: a machine-proposed PR touching `manifest.yml` or carrying mode→auto is rejected and logged [ARCH §11.4; INV 7]
- [ ] Race test: concurrent CI run and local run on the same source PR — the loser re-runs evaluation; no state overwrite (CAS holds) [ARCH §6.3; R15]
- [ ] PR diff scope test: fixture doc PR diff contains only documentation paths + `.docsync/state/` files — no code paths, no Markdown-body provenance edits [ARCH §6.3, §12.5; A8]
- [ ] Determinism test: PR creation path contains no LLM invocation (static assert + runtime trace check) [ARCH §13; P19]

Gate: phase complete when the fixture flow produces a correct, deduplicated, rate-limited, allowlist-filtered doc PR with provenance and `state/` carriage; survives merge-order churn (rebase, supersede, re-verify, no-op drop) without violating a single safety invariant; and all five reviewer actions emit correctly-labeled shared-schema feedback records.

### 7.6 Risks, assumptions & decisions

- **D4 adopted:** one doc PR per source PR + per-document partial-pass semantics; alternative (per-doc PRs) rejected — rollback simplicity and review burden win; granularity preserved via per-doc reporting [ARCH §0.3, §12.2].
- **R6** — doc PR ↔ code PR merge conflicts and stale doc PRs: mitigated by one-PR-per-source-PR, rebase + re-verify before push, supersede markers, idempotent replacement, post-merge re-runs [ARCH §12.3].
- **R15** — multi-writer state races (CI vs local `fix`): CAS writes + repo-scoped concurrency queue + graceful re-run; Phase 5 owns the queue, Phase 6 participates on the PR side [ARCH §6.3].
- **A9 gate (shared with Phase 3):** minimal-diff application must survive real-world Markdown (front-matter, fences, lists, renames) — the merge-order re-verify path and `fix` idempotency are the PR-side trial surfaces [ARCH §16.1 A9].
- **A8:** committed `.docsync/state/` is the V1 default (OD-3); diff-noise mitigation here = doc PRs carry state updates so the noise is at least reviewable in one place; revisit after the real-world trial [ARCH §16.1 A8; OD-3].
- **Invariants enforced in this phase:** 1 (never default branch), 2/3 (allowlist via PR filter), 4 (locked/generated/external rejected whole), 7 (machine PRs never mutate policy), 10 (provenance required), 11 (no flood) [ARCH §14].
- Decisions taken: doc PR carries `state/` updates (satisfies invariant 1 while keeping one audit trail — see OD-R12, registry §0.5); the PR layer is credential-free with injected tokens; a merge-order ledger is an explicit first-class record; feedback capture in V1 uses platform-native events only (merge/review/labels) with no free-text comment grammar; PR creation is a deterministic, LLM-free code path [ARCH §13].

> OPEN DECISION (merged into OD-R12, registry §0.5): do doc PRs carry `.docsync/state/` updates, or is state committed by a separate automation flow? (Invariant 1 forbids default-branch pushes, so some reviewable PR must carry them either way.) — Resolution (reconciled with Phase 9's check-only archival question into the single registry entry OD-R12): doc PRs carry their `state/` updates for PR-triggered runs — single reviewable audit trail, no second automation PR stream; check-only CI runs persist archival records out-of-band (CI artifact + optional repo-dispatch commit job); the PR body stays canonical per D5; A8 diff-noise evidence from the trial decides whether to split later.

> OPEN DECISION (OD-R13): reviewer override input mechanism in V1 — labels vs comment commands vs check-run `requested_action` buttons. — Recommendation: labels (`docsync:intentional`, `docsync:ignore-pattern`) plus native review actions in V1; button escalation arrives with the Phase 8 check-run surface where the platform supports it; no free-text comment grammar in V1 (parseable-command surfaces are an injection and ambiguity liability).

> OPEN DECISION (OD-R14): feedback capture fidelity for `edit` when a reviewer edits commits directly (squash/amend destroys the intermediate edit diff). — Recommendation: derive `corrected` diff from PR-head comparison (base vs final) rather than requiring atomic review edits; store both review-event diff (when present) and final-diff basis; Phase 10 evaluates which basis is more informative.

### 7.7 Architecture traceability

Covered: ARCH §12.4 (separate doc PR, five reasons, conventions, integrated-mode deferral); §12.2 (rate/burst limits, idempotency key, concurrency group, stale-PR supersede, self-trigger suppression); §12.3 (merge-order conflicts, rebase + re-verify, no-op drop, merge-queue re-checks, post-merge re-runs); §12.5 (provenance required, D5 rendering precedence, append-only upsert); §11.4 (layer 4 PR filter, safety-incident log, reviewer overrides → proposals, machine-PR mode-transition ban); §8.7 (five signals, label hygiene, false-negative detection); §8.8 (shared structured record format); §6.3 (state CAS + concurrency participation); §6.2 (PROPOSED → VERIFIED via human merge of the doc PR); §6.1/D12 (merge-queue baseline re-resolution trigger); §3.7 (bounded flows; report-only downgrade); §13 (deterministic write path); §14 (invariants 1, 2, 3, 4, 7, 10, 11); §15.1 (feedback loop from day one); §1.1 (PR-first); §16.1 (A8, A9); §16.2 (R6, R15).
CTX: §1 (PR-first workflow), §22 (FN/FP feedback examples), §23 (five reviewer actions), §30 (provenance in PR), §34 (separate doc PR, five reasons).
DEFERRED (ARCH §16.3): integrated mode (doc commits in the code PR) — opt-in only after the trust loop is proven [§12.4]; full org policy config/floors UX — V3 [§11.2]; safety-incident alerting pipeline — Phase 9 hardening; feedback recorder + evaluation integration — Phase 10; check-run `requested_action` button surfaces and App permission model — Phase 8.

### 7.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| ARCH | §12.4 | 6 | §7.3 separate-PR group; §7.4 TODO; §7.5 verify |
| ARCH | §12.2 | 6 | §7.3 rate/burst/idempotency group; §7.5 verify |
| ARCH | §12.3 | 6 | §7.3 merge-order group; §7.5 verify |
| ARCH | §12.5 | 6 | §7.3 state/provenance group; §7.5 verify |
| ARCH | §11.4 | 6 | §7.3 PR-side safety + reviewer proposals; §7.5 verify |
| ARCH | §8.7 | 6 | §7.3 feedback group; §7.4 TODO; §7.5 verify |
| ARCH | §8.8 | 6 | §7.3 shared record format; §7.5 schema test |
| ARCH | §6.3 | 6 | §7.3 state CAS; §7.5 race test |
| ARCH | §6.2 | 6 | §7.3 (PROPOSED→VERIFIED via human merge) |
| ARCH | §6.1 | 6 | §7.3 merge-queue D12 trigger |
| ARCH | §3.7 | 6 | §7.3 report-only downgrade at caps |
| ARCH | §13 | 6 | §7.3 determinism; §7.5 determinism test |
| ARCH | §14 | 6 | §7.6 invariants 1,2,3,4,7,10,11 |
| ARCH | §15.1 | 6 | §7.3 feedback-from-day-one |
| ARCH | §1.1 | 6 | §7.1 objective (PR-first) |
| ARCH | §16.1 | 6 | §7.6 A8, A9 |
| ARCH | §16.2 | 6 | §7.6 R6, R15 |
| CTX | §1 | 6 | §7.1 objective |
| CTX | §22 | 6 | §7.3 FN/FP signals; §7.5 verify |
| CTX | §23 | 6 | §7.3 five reviewer actions; §7.5 verify |
| CTX | §30 | 6 | §7.3 provenance rendering |
| CTX | §34 | 6 | §7.3 separate doc PR; §7.5 verify |
| D | D4 | 6 | §7.3 one-PR + partial-pass; §7.5 verify |
| D | D5 | 6 | §7.3 provenance precedence rendering |
| D | D12 | 6 | §7.3 merge-queue re-resolution trigger |
| D | D2 | 6 | §7.3 layer-4 protected-path rejection |
| A | A8 | 6 | §7.3 state carriage; §7.6 |
| A | A9 | 6 | §7.3 re-verify gate; §7.5 verify |
| R | R6 | 6 | §7.3 supersede/merge-order; §7.5 verify |
| R | R15 | 6 | §7.3 CAS participation; §7.5 race test |
| INV | 1 (§14) | 6 | §7.3 branch factory; §7.5 guard test |
| INV | 2 (§14) | 6 | §7.3 PR filter; §7.5 verify |
| INV | 3 (§14) | 6 | §7.3 PR filter; §7.5 verify |
| INV | 4 (§14) | 6 | §7.3 whole-patch rejection; §7.5 verify |
| INV | 7 (§14) | 6 | §7.3 machine-PR guard; §7.5 test |
| INV | 10 (§14) | 6 | §7.3 provenance gate; §7.5 test |
| INV | 11 (§14) | 6 | §7.3 rate/burst; §7.5 flood test |
| P | P9 | 6 | §7.3 write authority via allowlist |
| P | P14 | 6 | §7.3 overrides → proposals; §7.5 test |
| P | P17 | 6 | §7.3 reviewer-correction learning capture |
| P | P19 | 6 | §7.3 deterministic PR path; §7.5 test |


---

## §8 Phase 7 — CLI (check / fix / pr)

### 8.1 Objective & scope

Objective: ship the developer-facing CLI with three verbs and one mental model — **check reads, fix writes to the working tree, pr writes to a remote branch** [ARCH §12.1] — including exit-code semantics for scripts/CI, display rules (confidence pinning, unknown coverage, abstention visibility), and the local-loop safety rails (idempotent fix, working-tree guard, allowlist enforcement). The DX principle governs: the UI must make the system feel like Git, not a chatbot — no conversation threads, no free-form AI comments; every AI output is materialized as a diff a human reviews with normal tooling [ARCH §12 intro; CTX §32].

In scope (@docsync/cli): command surface, pipeline invocation through Phase 5 orchestrator seams, the shared output renderer (also consumed by the CI surface in Phase 8), exit codes, flags, the local `docsync pr` flow, run-id provenance records, state CAS participation, credential handling.
Out of scope: CI check-run lifecycle, GitHub App/Action surfaces (Phase 8); pipeline internals (Phases 1–5); provenance record schema authoring (Phase 5/9 — CLI writes and renders records); feedback recorder/evaluation (Phase 10).

### 8.2 Prerequisites & entry gate

- Phases 1–5 pipeline runs locally on a fixture repo (detection, Jev routing, writer, verification, provenance composition).
- Phase 6 PR client + rate governor available for `docsync pr` (shared limits with CI); Phase 4 verification verdicts gate `fix`; Phase 0 config loader fails closed with line-precise manifest errors (D8).
- Entry gate: on the fixture repo — `check` produces the §12.1-format report with correct exit codes for clean/drift/config-error/runtime-error fixtures; `fix` applies the verified patch idempotently with a run-id provenance record; `pr` creates the remote doc PR using developer credentials and Phase 6 conventions.

### 8.3 Features & deliverables

**`docsync check` [ARCH §12.1; CTX §32]**

- Read-only pipeline run: detection, Jev routing, candidate report — no writes, no patch generation (patch generation is `fix`/`pr`/button-driven) [ARCH §12.1].
- Output format mirrors the §12.1 transcript exactly: `✓` consistent · `⚠` potentially stale (reason + confidence) · `🔒` affected-but-locked — surfaced, never touched [ARCH §12.1; CTX §32; D2].
- Fallback glyphs `[ok]` / `[!]` / `[locked]` for constrained terminals [ARCH §12.1].
- Displayed confidence pinned to the producing `(policy_version, model binding)`; the policy version is shown next to every percentage (D3) [ARCH §12.1, §10.8; R18].
- Unknown-coverage rows: unmapped-but-changed documents are reported as "unknown coverage", never silently ignored (D7) [ARCH §11.1; D7].
- Abstention visibility: abstained/REVIEW rows surface with evidence pointers, shown sparingly to avoid noise — proof the system evaluated and abstained is what makes the check trustworthy [ARCH §5.2, §12.2; CTX §13].
- Local/CI fact parity: output is rendered by the same shared renderer the CI check-run consumes (Phase 8) — local and CI never disagree about the facts [ARCH §12.1].
- Exit codes: `0` no drift; `1` drift detected (candidates found); `2` configuration/manifest error (fail-closed, D8 — halts before any AI stage); `3` runtime failure (API/model/verifier error — also fail-closed, D14) [ARCH §12.1].
- Flags: `--strict` promotes `⚠` to exit `1` even when the policy engine would only suggest; `--json` emits the machine-readable, versioned report schema shared with the CI surface [ARCH §12.1].

**`docsync fix` [ARCH §12.1]**

- Runs the full pipeline — detection, Jev routing, writer investigation, verification — and applies the **verified** patch to the local working tree; the developer reviews with `git diff`; committing remains entirely the human's act [ARCH §12.1].
- Idempotency: patches are minimal, content-anchored diffs against current file state; applying an already-applied patch is a no-op — the second run's detection sees docs consistent with sources and reports "no candidates" [ARCH §12.1; A9].
- Run-id provenance records: every run writes a provenance record keyed by run ID; re-invoking with the same run ID is detected and skipped, never re-applied [ARCH §12.1, §12.5; INV 10].
- Refuse-and-rerun: if a doc changed between runs and the stored patch no longer applies, `fix` refuses and re-runs detection against current content rather than force-applying [ARCH §12.1].
- 3-way merge attempt is allowed only if the merged result **re-passes verification** before being written [ARCH §12.1, §12.3; A9].
- Working-tree guard: docs with uncommitted local changes are skipped unless `--allow-dirty` [ARCH §12.1].
- Write scope: only manifest-listed documents in writable modes — never code files, never `.docsync/` internals, never locked/generated docs [ARCH §12.1; INV 2, 3, 4; D2].
- No patch is applied without passing verification — the confidence × risk gate applies even to `auto`-mode documents (Phase 4 + Phase 2 engines invoked, not reimplemented) [ARCH §12.1; INV 5].

**`docsync pr` [ARCH §12.1, §12.4]**

- Creates a documentation PR from the local analysis using the standard Phase 6 conventions: branch `docsync/<run-id>`, title `docs: synchronize documentation with PR #481` (source-PR-linked when applicable), provenance body [ARCH §12.1, §12.4; CTX §34].
- Same safety rails as the CI path: provenance required, allowlist-enforced, locked docs excluded, never pushed to the default branch [ARCH §12.1; INV 1, 2, 10].
- Uses the developer's existing push credentials; never requests elevated scopes; `OPENROUTER_API_KEY` stays env-only and is never logged or embedded in state [ARCH §12.1, §10.7; INV 8].
- Local PR creation passes through the Phase 6 rate/burst governor — local flows share the repository's limits with CI (INV 11) [ARCH §12.2, §12.4].

**Cross-cutting [ARCH §6.3, §7.1, §11.3]**

- Config errors surface fail-closed with line-precise diagnostics (exit 2); remediation is always a normal PR fixing `manifest.yml` — recovery stays inside the Git workflow [ARCH §11.3; D8].
- State CAS participation: local runs vs CI runs — the losing writer re-runs evaluation rather than overwriting (R15) [ARCH §6.3; R15].
- Verbose mode exposes Loop A stage timings (analysis / Jev / writer / verification) for latency-budget inspection [ARCH §7.1].

### 8.4 TODO list

- [ ] Scaffold @docsync/cli command surface (check/fix/pr) on Node ≥20 argument parsing; stateless commands over the Phase 5 orchestrator API [ARCH §12.1]
- [ ] Implement the shared output renderer (`✓`/`⚠`/`🔒` + `[ok]`/`[!]`/`[locked]` fallbacks) consumed by CLI and CI (Phase 8 seam) [ARCH §12.1; CTX §32]
- [ ] Implement D3-pinned confidence rendering (policy version + model binding adjacent to every percentage) [ARCH §12.1, §10.8; D3; R18]
- [ ] Implement unknown-coverage row rendering (D7) [ARCH §11.1; D7]
- [ ] Implement abstention-visibility rows with evidence pointers, sparse by default [ARCH §5.2, §12.2; CTX §13]
- [ ] Implement exit-code mapping (0/1/2/3) including fail-closed paths: manifest error before any AI stage → 2; runtime/API/verifier failure → 3 [ARCH §12.1; D8; D14]
- [ ] Implement `--strict` and `--json` flags; version the JSON report schema and share it with Phase 8 [ARCH §12.1]
- [ ] Implement fix run-id generation (ULID), provenance record write, and same-run-id skip [ARCH §12.1, §12.5; INV 10]
- [ ] Implement fix idempotency: already-applied patch → no-op; re-detection reports "no candidates" [ARCH §12.1; A9]
- [ ] Implement refuse-and-rerun on stale patch (no force-apply path exists) [ARCH §12.1]
- [ ] Implement 3-way-merge-only-if-re-verified gate in the fix path [ARCH §12.1, §12.3; A9]
- [ ] Implement working-tree guard + `--allow-dirty` [ARCH §12.1]
- [ ] Implement write-scope guard: manifest-writable docs only; block code files, `.docsync/` internals, locked/generated docs with an incident-log entry [ARCH §12.1; INV 2, 3, 4]
- [ ] Implement `docsync pr` over the Phase 6 client (branch/title/body conventions, allowlist filter, provenance gate, rate governor) [ARCH §12.1, §12.4, §12.2]
- [ ] Implement credential handling: developer push creds, no scope elevation, secret redaction in all CLI logs/output [ARCH §12.1, §10.7; INV 8]
- [ ] Implement state CAS participation (loser re-runs; never overwrites) [ARCH §6.3; R15]
- [ ] Implement verbose stage-timing output [ARCH §7.1]
- [ ] Tests: exit-code matrix, renderer snapshots, idempotency, same-run-id skip, dirty guard, scope guard, CAS race, redaction scan [ARCH §12.1]
- [ ] Manual QA script: fixture-repo walk-through reproducing the CTX §32 check transcript and §34 PR conventions [CTX §32, §34]

### 8.5 Verification list

- [ ] check fixture output matches the §12.1/CTX §32 format including the `🔒` locked row and fallback glyphs; `--json` output validates against the versioned report schema [ARCH §12.1; CTX §32]
- [ ] Every rendered confidence string carries the policy version + model binding (D3) [ARCH §10.8; D3; R18]
- [ ] Unknown-coverage fixture document appears as "unknown coverage", not silence (D7) [ARCH §11.1; D7]
- [ ] Exit codes: clean → 0; drift → 1; broken manifest → 2 before any AI stage; killed verifier/API → 3 [ARCH §12.1; D8; D14]
- [ ] `--strict` promotes `⚠` to exit 1 even when the policy outcome is only suggest [ARCH §12.1]
- [ ] fix: verified patch lands in the working tree; `git diff` shows a minimal change; a second fix run is a no-op and detection reports "no candidates" [ARCH §12.1; A9]
- [ ] fix: same run-id re-invocation is skipped; a new run id appends a new provenance record (append-only) [ARCH §12.1, §12.5; INV 10]
- [ ] fix: upstream doc change → refuse + re-detect (no force-apply); 3-way merge only after a re-verification pass [ARCH §12.1; A9]
- [ ] fix: doc with uncommitted changes is skipped without `--allow-dirty` [ARCH §12.1]
- [ ] fix scope: attempts to write a code file, `.docsync/` internals, or a locked doc are all blocked and incident-logged [ARCH §12.1; INV 2, 3, 4]
- [ ] pr: doc PR created with conventions + provenance body; never the default branch; no elevated scopes requested [ARCH §12.1, §12.4; INV 1]
- [ ] pr: local PR creation counts against the repo rate/burst governor [ARCH §12.2; INV 11]
- [ ] CAS race test: concurrent local fix + simulated CI write — one loses and re-runs; no overwrite [ARCH §6.3; R15]
- [ ] Secret scan: `OPENROUTER_API_KEY` never appears in logs, output, or `state/` files [ARCH §10.7; INV 8]
- [ ] Unverified-patch guard: a patch failing verification is never applied by `fix` (INV 5, fail-closed) [ARCH §12.1; INV 5, 9]

Gate: phase complete when all three verbs pass the fixture matrix (clean / drift / config-error / runtime-error), `fix` is provably idempotent, scope-limited, and provenance-recorded, `pr` reuses the Phase 6 rails with no local bypass, and CLI output is byte-comparable with the shared CI renderer on the same facts.

### 8.6 Risks, assumptions & decisions

- **A8:** the CLI reads/writes committed `.docsync/state/` (OD-3); if teams reject committed state in the trial, the CLI's state store seam must operate against a CI-maintained mode (D1 note) — storage access is abstracted behind one interface to keep both modes available [ARCH §16.1 A8; OD-3].
- **A9:** the `fix` idempotency guarantee depends on minimal-diff application surviving real-world Markdown (front-matter, fences, lists, renames) — 3-way-merge trials are the gate; refuse-and-rerun is the backstop so a failed trial degrades to re-detection, never force-apply [ARCH §16.1 A9].
- **R15** — local `fix` vs CI races: CAS writes + loser-re-runs [ARCH §6.3].
- **R6 (partial)** — local `pr` flows participate in the same dedup/supersede machinery as CI [ARCH §12.2, §12.3].
- **Decisions adopted here:** D2 (locked displayed, never touched), D3 (confidence pinning in every renderer), D7 (unknown coverage surfaced), D8 (exit 2 fail-closed), D10 (advisory posture: exit 1 only for real candidates; `--strict` is the explicit opt-in to harder enforcement), D14 (exit 3 fail-closed runtime degradation).
- Decisions taken: one shared renderer module for CLI + CI (fact parity is a hard requirement); run IDs are ULIDs; `fix` requires a verified patch — no `--no-verify` escape hatch exists (a proposed bypass flag was rejected as an invariant-5 violation); `pr` never elevates scopes and reuses developer credentials only.

> OPEN DECISION (OD-R15): should Phase 7 ship a minimal `docsync init` (manifest skeleton + `models.yml` template + `instructions.md` stub), given Phase 0 owns the config schema? — Recommendation: yes, ship a thin scaffolding command with the CLI; schema authority and validation remain with Phase 0; init output must validate cleanly against the Phase 0 loader before exit 0.

> OPEN DECISION (OD-R16): offline/reduced mode — `check` without `OPENROUTER_API_KEY` emitting a deterministic-only report. — Recommendation: support `check --offline` reporting deterministic analysis + candidate paths with explicit "decision unavailable" rows (never silently skipping Jev, per D14); useful for air-gapped dev loops; exit 1 with an offline marker so scripts can distinguish it.

> OPEN DECISION (OD-R17): interactive prompt for `fix` when multiple unrelated candidate docs exist (patch all vs per-doc selection). — Recommendation: default patches all verified docs (matches one-PR mental model); add `--doc <path>` selectors; no interactive wizard in V1 (scriptability over chat-like UX).

### 8.7 Architecture traceability

Covered: ARCH §12.1 (all three verbs, output format, fallback glyphs, exit codes, `--strict`/`--json`/`--allow-dirty`, fix idempotency guarantees, working-tree guard, write scope, pr rails/credentials); §12.5 (run-id provenance records, append-only, provenance-required rendering); §12.4 (pr conventions, `docsync/<run-id>` branches); §12.3 (3-way-merge re-verify in fix); §12.2 (rate/burst governor participation); §11.1 (D7 unknown coverage); §11.3 (fail-closed config errors, exit 2, PR-based remediation); §10.7 (env-only key, redaction); §6.3 (state CAS, local/CI races); §7.1 (stage-timing instrumentation); §5.2 (abstention visibility); §14 (invariants 1, 2, 3, 4, 5, 6, 8, 9, 10, 11); §16.1 (A8, A9); §16.2 (R6 partial, R15).
CTX: §32 (check/fix/pr transcripts), §30 (provenance records), §34 (PR title/conventions), §13 (abstain path visibility).
DEFERRED (ARCH §16.3): `state/` committed vs CI-only — pending A8 evidence (D1 note); integrated mode local flows — §12.4; org floors UX — V3 [§11.2]; policy-architect proposal UX and feedback recorder surfaces — Phase 10; check-run/UI parity surfaces — Phase 8.

### 8.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| ARCH | §12.1 | 7 | §8.3 all verb groups; §8.4 TODO; §8.5 verify |
| ARCH | §12.5 | 7 | §8.3 fix run-id records; §8.5 verify |
| ARCH | §12.4 | 7 | §8.3 pr conventions; §8.5 verify |
| ARCH | §12.3 | 7 | §8.3 3-way re-verify; §8.5 verify |
| ARCH | §12.2 | 7 | §8.3 rate governor participation; §8.5 test |
| ARCH | §11.1 | 7 | §8.3 unknown coverage (D7); §8.5 test |
| ARCH | §11.3 | 7 | §8.3 fail-closed config, exit 2 |
| ARCH | §10.7 | 7 | §8.3 credentials/redaction; §8.5 scan |
| ARCH | §6.3 | 7 | §8.3 state CAS; §8.5 race test |
| ARCH | §7.1 | 7 | §8.3 stage timings |
| ARCH | §5.2 | 7 | §8.3 abstention visibility |
| ARCH | §14 | 7 | §8.6 invariants 1–6, 8–11 |
| ARCH | §16.1 | 7 | §8.6 A8, A9 |
| ARCH | §16.2 | 7 | §8.6 R6 partial, R15 |
| CTX | §32 | 7 | §8.1/§8.3/§8.5 transcripts + QA script |
| CTX | §30 | 7 | §8.3 provenance records |
| CTX | §34 | 7 | §8.3 pr title/conventions; §8.5 test |
| CTX | §13 | 7 | §8.3 abstain-path visibility |
| D | D2 | 7 | §8.3 locked displayed never touched; §8.5 test |
| D | D3 | 7 | §8.3 pinned confidence; §8.5 test |
| D | D7 | 7 | §8.3 unknown coverage; §8.5 test |
| D | D8 | 7 | §8.3 exit 2 fail-closed; §8.5 test |
| D | D10 | 7 | §8.6 advisory posture + --strict opt-in |
| D | D14 | 7 | §8.3 exit 3 fail-closed; §8.5 test |
| A | A8 | 7 | §8.6 state-mode seam |
| A | A9 | 7 | §8.3/§8.5 fix idempotency + 3-way trials |
| R | R6 | 7 | §8.6 local pr dedup participation |
| R | R15 | 7 | §8.3 CAS; §8.5 race test |
| INV | 1 (§14) | 7 | §8.3 pr default-branch ban; §8.5 test |
| INV | 2 (§14) | 7 | §8.3 write scope; §8.5 test |
| INV | 3 (§14) | 7 | §8.3 write scope; §8.5 test |
| INV | 4 (§14) | 7 | §8.3 locked/generated blocked; §8.5 test |
| INV | 5 (§14) | 7 | §8.3 verified-patch-only apply; §8.5 test |
| INV | 6 (§14) | 7 | §8.3 abstention visibility |
| INV | 8 (§14) | 7 | §8.3 credentials; §8.5 secret scan |
| INV | 9 (§14) | 7 | §8.3 fail-closed exits; §8.5 test |
| INV | 10 (§14) | 7 | §8.3 run-id provenance; §8.5 test |
| INV | 11 (§14) | 7 | §8.3 pr rate limits; §8.5 test |
| P | P1 | 7 | §8.1 repo-local scope |
| P | P2 | 7 | §8.3 manifest-scoped check + D7 surfacing |
| P | P12 | 7 | §8.3 abstain visibility |
| P | P19 | 7 | §8.1 deterministic orchestrator behind CLI |
| P | P20 | 7 | §8.1 correctness-focused reporting |

## §9 Phase 8 — CI & GitHub Integration (Action + App)

### 9.1 Objective & scope

Wire the completed Loop-A pipeline (Phases 0–7) into GitHub so documentation consistency behaves like any other PR check. Governing DX principle [ARCH §12.2 intro] [CTX §33]: **the UI must make the system feel like Git, not a chatbot** — native GitHub primitives only (check runs, diffs, PRs); no conversation threads; no free-form AI comments; every AI output materialized as a reviewable diff a human reviews with normal review tooling. Scope: platform spikes A6/A7 (phase entry gate), the GitHub Action variant, the GitHub App (permissions, webhooks, auth, check-run lifecycle and UI, human-initiated escalation), doc-PR creation through CI with rate/burst/idempotency governance, fork-PR report-only mode, config-error fail-closed surfaces, post-merge handling, and secrets plumbing. Out of scope: provenance schema and enforcement gates (Phase 9 — but Phase 8 renders the PR-body summary), CLI verbs (Phase 7; their exit codes are consumed here), PR-creation internals and branch safety (Phase 6, consumed as primitives), policy lifecycle (Phase 10+).

### 9.2 Prerequisites & entry gate

- Phases 0–7 complete: deterministic analysis, decision layer, writer, verifier, orchestrator Loop-A E2E, Git/PR integration (branch + PR creation + safety rails), CLI `docsync check` with the exit-code contract 0/1/2/3 [ARCH §12.1].
- Policy engine exposes outcome → check-conclusion mapping inputs, including enforcement opt-in signals (`--strict`, high-risk/locked docs affected) [ARCH §12.2] [ARCH §0.3 D10].
- Decision objects carry `policy_version` and model-binding identity so displayed confidence can be pinned on every surface [ARCH §5.4] [ARCH §10.6] [ARCH §0.3 D3].
- **Entry gate (hard): platform spikes A6 and A7 are executed and written up BEFORE check-UI implementation begins** [ARCH §16.1 A6] [ARCH §16.1 A7]. An A6 no-go switches the escalation path to the decided fallback (see 9.6, OD-R18) and is a planned branch, not a mid-phase surprise.

### 9.3 Features & deliverables

**DX governing principle**
- Native GitHub primitives only — check runs, diffs, PRs; no conversation threads; no free-form AI comments; every AI output materialized as a reviewable diff [ARCH §12.2 intro] [CTX §33].
- CI surfaces mirror CLI facts exactly: the ✓/⚠/🔒 visual language and findings shown in CI are what `docsync check` shows locally, so local and CI never disagree about the facts [ARCH §12.1] [CTX §32].

**Platform spikes (executed first; phase entry gate)**
- A6 spike: verify the `check_run.requested_action` button works cleanly with Checks+Contents+Pull requests permissions on a scratch repo; record go/no-go; on no-go activate the comment-command fallback per its spec [ARCH §16.1 A6].
- A7 spike: verify `GITHUB_TOKEN`/fork semantics — (a) PRs created with `GITHUB_TOKEN` do not trigger downstream workflows, (b) fork head repos are unwritable, (c) fork-PR check-run visibility; record all three in the limitations doc [ARCH §16.1 A7].

**GitHub App (primary full-fidelity surface)**
- Minimum-viable-authority permission table: Contents read+write; Pull requests read+write; Checks read+write — nothing else [ARCH §12.2].
- Explicit NOT-granted list, enforced in the app manifest and asserted in tests: Administration, **Workflows (must never modify CI definitions)**, Members, Secrets, Deployments, Webhooks, Issues [ARCH §12.2] [ARCH §14 inv 3].
- Repo-scoped installations only (never "all repositories"); org admins opt repos in [ARCH §12.2].
- Short-lived installation tokens minted per run; no long-lived PATs anywhere in the product [ARCH §12.2].
- Tokens never reach the model layer: the deterministic orchestrator performs all Git/API writes; model payloads are asserted token-free at the gateway boundary [ARCH §12.2] [ARCH §13] [ARCH §10.7] [CTX §38].
- Webhook surface exactly: `pull_request` (opened/synchronize), `check_run.requested_action`, `push` (optional, post-merge checks) — nothing else; unhandled event types are no-ops with logged rejections [ARCH §12.2].
- Auth flows: installation-token minting/refresh per run; webhook signature verification; App private key managed in the CI/orchestrator secret store only [ARCH §10.7].
- Operational hardening: GitHub API rate-limit budget per run; idempotent check updates (find-or-create + update by id); concurrency-group interplay (`docsync-<repo>-<pr>`, supersede on new push, no collision with user CI groups); self-trigger suppression via App identity **and** `docsync/` branch prefix (not path alone) [ARCH §12.2].

**GitHub Action (zero-install fallback)**
- `action.yml` with documented workflow inputs; `GITHUB_TOKEN` with `contents: write`, `pull-requests: write`, `checks: write` [ARCH §12.2].
- Documented limitations: PRs created with `GITHUB_TOKEN` don't trigger downstream workflows; forks cannot be written [ARCH §12.2] [ARCH §16.1 A7].
- Positioned as the zero-install fallback; the App is recommended for full-fidelity write flows [ARCH §12.2].
- Report-only on fork PRs: the check shows mapping/candidates; **no patch generation, no PR creation** — a documented limitation, not a bug [ARCH §12.2].
- Exit-code mapping: CLI 0→success/no-drift, 1→drift mapping per policy, 2→`failure` configuration error, 3→neutral/inconclusive runtime failure [ARCH §12.1] [ARCH §12.2] [ARCH §0.3 D8] [ARCH §0.3 D14].

**Check-run lifecycle**
- ONE check named `docsync/check` per PR, upserted (find-or-create + update by id), never duplicated [ARCH §12.2].
- Conclusion mapping from the policy engine: `success` (clear/no action); **`neutral` (advisory findings — DEFAULT posture, D10)**; `failure` only when the repo opts into enforcement (high-risk/locked docs affected, `--strict`) [ARCH §12.2] [ARCH §0.3 D10].
- Fail-closed configuration surface: invalid manifest → check concludes `failure` titled `docsync: configuration error` listing offending lines; no AI stage runs [ARCH §11.3] [ARCH §0.3 D8].
- Inconclusive degradation: analyzer failure or model outage → advisory-neutral check marked inconclusive, never "no docs affected" [ARCH §3.4] [ARCH §0.3 D14].
- Confidence percentages are display projections pinned to `(policy_version, model binding)` on every surface [ARCH §12.2] [ARCH §0.3 D3] [ARCH §10.8].
- Early-signal lifecycle: check updated after Stage 1 (decision + candidates + confidence) and again after Stages 2/3 [ARCH §12.2] [ARCH §7.1].

**Check UI rendering**
- Source file → affected docs mapping with percentages (`src/payments/refund.ts → docs/payments.md 97%`) [ARCH §12.2] [CTX §33].
- Abstained/rejected candidate rows shown **sparingly** (e.g., `docs/architecture.md 11% — no action`): proves evaluation + abstention, making the check trustworthy rather than noisy [ARCH §12.2] [ARCH §5.2].
- Locked-doc rows: `🔒 docs/security.md — Potentially affected, locked` — surfaced, never touched [ARCH §11.4] [ARCH §12.1].
- `[Generate documentation patch]` button = explicit **human-initiated escalation**: `requested_action` → writer → verification → doc PR [ARCH §12.2].
- No patch generation without someone asking (V1 default); repos with reviewed manifests may opt into auto-proposal via an explicit flag [ARCH §12.2].

**Anti-noise engineering (R7)**
- Single aggregated check per PR (never per-document checks) [ARCH §16.2 R7].
- Quiet mode: no-action rows collapsed/bounded; default on [ARCH §16.2 R7].
- `neutral` default conclusion (D10) so documentation checks never break builds in V1 [ARCH §0.3 D10].

**Doc-PR creation through CI (consumes Phase 6 primitives)**
- One documentation PR per source PR (D4), aggregating all candidate docs, with per-document partial-pass semantics: a failing doc is dropped from the PR and reported, never blocking the others [ARCH §0.3 D4] [ARCH §12.2] [ARCH §12.4].
- Conventions: branch `docsync/pr-481-<head-sha7>`; title `docs: synchronize documentation with PR #481`; body = provenance summary (schema in Phase 9) + candidate mapping table + verification results + "generated by docsync — review like any code change" banner [ARCH §12.4].
- PR-body provenance summary rendered from the run record — canonical surface per D5; schema and enforcement gate land in Phase 9 [ARCH §12.5] [ARCH §0.3 D5] [CTX §30].
- Rate/burst governance: max 3 open docsync PRs per repo (configurable 1–10); at cap, downgrade to check-report + "patch ready, apply manually"; max 10 doc PRs per repo per hour (configurable); bursts queue, not fail [ARCH §12.2].
- Idempotency key = (source PR head SHA, doc-set content hash); re-runs update the existing check/doc PR instead of duplicating [ARCH §12.2] [ARCH §3.5].
- Concurrency group `docsync-<repo>-<pr>`: a new push supersedes the in-flight run [ARCH §12.2].
- 429/403 secondary-rate responses: exponential backoff with jitter, then fail-safe to report-only; the system never retries its way into a flood [ARCH §12.2].
- Stale doc PRs: if the source PR evolves, the prior doc PR is marked "superseded by newer analysis" and the idempotent re-run replaces it; deterministic branch names keep dedup simple; legitimate re-creation after a revert gets a deterministic disambiguator suffix [ARCH §12.2].
- Post-merge `push` handling (optional, default-off per repo): re-run evaluation after the code PR merges so a doc PR that no longer applies is superseded rather than conflicting; doc-PR checks re-run against the merge target with D12 baseline re-resolution [ARCH §12.3] [ARCH §0.3 D12].
- Self-trigger suppression: docsync ignores its own doc PRs by branch prefix `docsync/` **and** App identity; the system never creates a doc PR from its own doc PR [ARCH §12.2] [ARCH §14 inv 11].

**Secrets handling**
- `OPENROUTER_API_KEY` lives only in the CI secret store / orchestrator environment; never in prompts, never in `.docsync/` files, never in check or PR payloads [ARCH §10.7] [ARCH §14 inv 8].
- App webhook secret verification; private-key storage guidance; least-privilege token audit hook [ARCH §10.7] [ARCH §12.2].

**Packaging & docs**
- `@docsync/github-app` and `@docsync/action` packages per the decided stack [ARCH §12.2].
- Docs: App permission rationale + repo-scoped org opt-in guide; Action README with inputs and GITHUB_TOKEN/fork limitations; limitations page including the A6 fallback UX [ARCH §12.2] [ARCH §16.1].

### 9.4 TODO list

- [ ] (Spike) Execute A6 spike: minimal App with Checks+Contents+Pull requests permissions on a scratch repo; create `docsync/check` with a `requested_action` button; confirm the click delivers `check_run.requested_action`; record go/no-go [ARCH §16.1 A6]
- [ ] (Spike) Execute A7 spike: verify (a) PRs created with `GITHUB_TOKEN` do not trigger downstream workflows, (b) fork head repos unwritable, (c) fork-PR check-run visibility; record in the limitations doc [ARCH §16.1 A7]
- [ ] (Spike) If A6 no-go: write the comment-command fallback UX spec (single deterministic command; reply is a mechanical link/acknowledgment, never AI prose) and re-gate escalation implementation on it [ARCH §16.1 A6] [ARCH §12.2 intro]
- [ ] (App) Scaffold `@docsync/github-app`: app manifest with the exact permission table (Contents/Pull requests/Checks read+write) and webhook list (pull_request opened/synchronize, check_run.requested_action, push) [ARCH §12.2]
- [ ] (App) Implement webhook receiver: signature verification, event routing, reject/no-op for unhandled events [ARCH §12.2]
- [ ] (App) Implement installation-token minting/refresh per run; GitHub API client wrapper; token redaction in logs [ARCH §12.2] [ARCH §10.7]
- [ ] (App) Implement self-trigger suppression: skip events where actor == App identity or branch matches the `docsync/` prefix (both checks, not path alone) [ARCH §12.2]
- [ ] (Check) Implement check-run upsert service: find-or-create the single `docsync/check` per PR; update by id; idempotent updates [ARCH §12.2]
- [ ] (Check) Implement conclusion mapping: policy outcome → success/neutral/failure with enforcement opt-in (`--strict`, high-risk/locked affected); default `neutral` [ARCH §12.2] [ARCH §0.3 D10]
- [ ] (Check) Implement config-error surface: `failure` titled `docsync: configuration error` with offending lines; abort before any AI stage [ARCH §11.3] [ARCH §0.3 D8]
- [ ] (Check) Implement inconclusive degradation surface: analyzer failure/outage → `neutral` + inconclusive summary; never "no docs affected" [ARCH §3.4] [ARCH §0.3 D14]
- [ ] (Check) Implement early-signal lifecycle: post check after Stage 1 (decision + candidates + confidence pinned to `policy_version`/model binding), update after Stages 2/3 [ARCH §12.2] [ARCH §7.1] [ARCH §0.3 D3]
- [ ] (Check) Implement summary renderer: source→docs mapping with percentages; sparse abstained rows (e.g., `docs/architecture.md 11% — no action`); 🔒 locked rows; quiet-mode collapsing [ARCH §12.2] [ARCH §5.2] [ARCH §11.4] [CTX §33]
- [ ] (Check) Implement the `[Generate documentation patch]` `requested_action` handler: enqueue writer → verification → doc PR; no patch generation without the click (V1 default) [ARCH §12.2]
- [ ] (Check) Implement the auto-proposal opt-in flag (default off; reviewed-manifest gating; exact criteria finalized in Phase 10) [ARCH §12.2]
- [ ] (Action) Scaffold `@docsync/action`: `action.yml` inputs; document `GITHUB_TOKEN` scopes (`contents: write`, `pull-requests: write`, `checks: write`) [ARCH §12.2]
- [ ] (Action) Implement the runner wrapping the orchestrator/CLI; map exit codes 0/1/2/3 to check conclusions including config-error and runtime-failure surfaces [ARCH §12.1] [ARCH §12.2]
- [ ] (Action) Implement fork-PR detection → report-only mode: mapping/candidates check only; no writer invocation; no PR creation; limitation text in check output + README [ARCH §12.2]
- [ ] (Action) Document GITHUB_TOKEN limitations (no downstream workflows; forks unwritable) and zero-install fallback positioning in the README [ARCH §12.2] [ARCH §16.1 A7]
- [ ] (PR) Wire doc-PR creation through Phase 6 primitives with D4 aggregation and per-document partial-pass drop-and-report [ARCH §0.3 D4] [ARCH §12.2] [ARCH §12.4]
- [ ] (PR) Render the PR body: provenance summary (Phase 9 schema), candidate mapping table, verification results, generated-by banner; branch `docsync/pr-<n>-<sha7>`; title convention [ARCH §12.4] [ARCH §12.5]
- [ ] (Rate) Wire the Phase 6 rate/burst-limiter primitives into CI runs: consume ≤3 open docsync PRs (configurable 1–10) with downgrade-to-report at cap; ≤10/hour with queue-not-fail [ARCH §12.2]
- [ ] (Rate) Wire the Phase 6 idempotency-key primitive (source PR head SHA, doc-set content hash) into CI runs: re-runs update the existing check/doc PR [ARCH §12.2] [ARCH §3.5]
- [ ] (Rate) Wire the Phase 6 concurrency-group primitive `docsync-<repo>-<pr>` into CI runs with supersede-on-new-push; no collision with user CI concurrency groups [ARCH §12.2]
- [ ] (Rate) Wire the Phase 6 429/403 secondary-rate handling into CI runs: exponential backoff with jitter → fail-safe report-only; bounded attempts [ARCH §12.2]
- [ ] (Rate) Wire the Phase 6 stale-doc-PR supersede primitives into CI runs: supersede markers, deterministic branch dedup, and the revert re-creation disambiguator suffix [ARCH §12.2]
- [ ] (PR) Wire the Phase 6 post-merge `push` re-run trigger primitive to the App/Action `push` webhook surface (optional; default-off per repo, OD-R19): re-run evaluation after merge; supersede inapplicable doc PRs; merge-target re-run with D12 baseline re-resolution [ARCH §12.3] [ARCH §0.3 D12]
- [ ] (Secrets) Wire `OPENROUTER_API_KEY` from CI secret store/env only; assert absence from model payloads, check summaries, PR bodies, and `.docsync/` writes [ARCH §10.7] [ARCH §14 inv 8]
- [ ] (Ops) Structured logging + per-stage latency + OpenRouter cost-accounting hooks for CI runs [ARCH §10.6] [ARCH §7.1]
- [ ] (E2E) Build the GitHub fixture harness (recorded API/webhook replay): PR opened → Stage-1 check → Stage-2/3 update → escalation → doc PR; fork PR; config-error PR; self-trigger PR [ARCH §12.2]
- [ ] (Docs) App permission rationale + repo-scoped org opt-in guide; limitations page (A6 fallback, GITHUB_TOKEN, fork PRs) [ARCH §12.2] [ARCH §16.1]

### 9.5 Verification list

- [ ] A6 spike note exists with explicit go/no-go; on go, the button click delivered `requested_action` in a scratch repo; on no-go, the fallback UX spec is written and escalation implemented per fallback [ARCH §16.1 A6]
- [ ] A7 spike note documents all three semantics: no downstream workflows from `GITHUB_TOKEN` PRs, forks unwritable, fork-PR check visibility [ARCH §16.1 A7]
- [ ] Test: two webhook deliveries for the same PR head produce exactly ONE `docsync/check` run (upserted, never duplicated) [ARCH §12.2]
- [ ] Test: conclusion mapping — clean → `success`; advisory findings → `neutral` (default); enforcement opt-in with locked/high-risk doc affected → `failure`; `--strict` → `failure` [ARCH §12.2] [ARCH §0.3 D10]
- [ ] Test: invalid manifest → check `failure` titled `docsync: configuration error`, offending lines listed, zero AI calls issued [ARCH §11.3] [ARCH §0.3 D8]
- [ ] Test: analyzer failure and OpenRouter outage → `neutral` check labeled inconclusive; output never states "no docs affected" [ARCH §3.4] [ARCH §0.3 D14]
- [ ] Test: check summary renders `src/payments/refund.ts → docs/payments.md (97%)` with `policy_version` + model binding next to the percentages (D3), and one abstained row (`docs/architecture.md 11% — no action`) present but collapsed under quiet mode [ARCH §12.2] [ARCH §0.3 D3] [CTX §33]
- [ ] Test: locked doc affected → 🔒 row shown; no writer invocation occurs for it [ARCH §11.4] [ARCH §12.1]
- [ ] Test: escalation-button click runs writer → verification → doc PR exactly once per idempotency key; without the click and without opt-in, no patch generation occurs [ARCH §12.2]
- [ ] Test: auto-proposal disabled by default; enabled only behind the opt-in flag for reviewed manifests [ARCH §12.2]
- [ ] Test: at 3 open docsync PRs, a new patch downgrades to the check-report "patch ready, apply manually"; cap configurable 1–10 [ARCH §12.2]
- [ ] Test: >10 doc PRs/hour → the burst queues; the queue drains; nothing is dropped or failed [ARCH §12.2]
- [ ] Test: re-run with the same head SHA + doc-set hash updates the existing check/doc PR; zero duplicates [ARCH §12.2] [ARCH §3.5]
- [ ] Test: a push during an in-flight run supersedes it via the concurrency group; single winner per group [ARCH §12.2]
- [ ] Test: simulated 429/403 → backoff with jitter; bounded attempts; final state report-only; no retry storm [ARCH §12.2]
- [ ] Test: source-PR evolution → prior doc PR marked "superseded by newer analysis"; revert + re-create gets the disambiguator suffix; branch names deterministic [ARCH §12.2]
- [ ] Test: an App-created doc PR triggers no new docsync run (self-trigger suppression via identity + branch prefix) [ARCH §12.2]
- [ ] Test: webhook-surface audit — configured events equal exactly the three allowed; unhandled events no-op with a log line [ARCH §12.2]
- [ ] Test: fork PR → report-only check with mapping/candidates; assert no writer invocation and no PR creation [ARCH §12.2]
- [ ] Test: the App installation token is short-lived and scoped to the opted-in repo; an attempted `.github/workflows/**` write returns 403 [ARCH §12.2] [ARCH §14 inv 3]
- [ ] Test: tokens never appear in model request payloads, logs, check summaries, or PR bodies (gateway-boundary assertion) [ARCH §10.7] [ARCH §14 inv 8]
- [ ] E2E: the Stage-1 check update lands before Stage-2/3 completes (early signal); the final update carries verification results [ARCH §7.1]
- [ ] E2E: the doc PR body contains the provenance summary + mapping table + verification results + banner and links the source PR [ARCH §12.4] [ARCH §12.5]
- [ ] Metric: quiet-mode abstained-row density ≤ configured cap per check; ≤1 check update burst per stage transition (R7 monitored) [ARCH §16.2 R7]

Gate: phase complete when the A6/A7 spikes are resolved and written up; both App and Action pass the E2E suite (check lifecycle, conclusion mapping incl. the D10 default-neutral and D8 config-error fail-closed surfaces, escalation, doc-PR creation with D4 aggregation and the PR-body provenance summary handed to Phase 9); fork-PR report-only, self-trigger suppression, rate/burst/idempotency/supersede behaviors are green; and the secrets-boundary tests pass.

### 9.6 Risks, assumptions & decisions

Assigned gates:
- **A6 (assumption gate — spike-first)** [ARCH §16.1 A6]: resolved at phase entry. Go → `requested_action` button as designed. No-go → comment-command fallback spec; its tension with the no-chatbot DX principle is flagged in OD-R18. Risk if wrong is degraded escalation UX only — check/report flows are unaffected.
- **A7 (assumption gate — spike-first)** [ARCH §16.1 A7]: results are binding inputs to the Action README and fork-PR behavior; if fork visibility differs from expectation, only the report-only summary wording adjusts.
- **R7 (check-run UX noise → teams mute the check)** [ARCH §16.2 R7]: mitigations shipped — single aggregated check, quiet mode (default on), sparse no-action rows, `neutral` default (D10); monitored via the abstained-row-density metric in 9.5.

Decisions taken:
1. App = primary full-fidelity surface; Action = zero-install fallback; both shipped in V1 [ARCH §12.2].
2. Fork PRs are report-only by design — a documented limitation, not a bug [ARCH §12.2].
3. Patch generation is human-initiated by default (escalation button); auto-proposal opt-in exists but ships default-off [ARCH §12.2].
4. The post-merge `push` handler is implemented but default-off per repo (opt-in) [ARCH §12.2].
5. Check-run metadata is treated as an ephemeral projection only; canonical/archival provenance is Phase 9's D5 store [ARCH §0.3 D5].
6. The App never receives Issues/Webhooks/Workflows/Secrets/… permissions; incident surfacing and reporting therefore ride checks + structured logs only in V1 [ARCH §12.2].

Alternatives considered:
- Comment-command UX as primary escalation: rejected (violates the no-chatbot DX principle [ARCH §12.2 intro]); retained only as the A6 fallback.
- Issues-based drift reports: rejected for V1 (no Issues permission); candidate for V2 re-evaluation.
- Per-document check runs: rejected — noise; violates single-aggregated-check (R7).
- Integrated mode (doc commits in the code PR): rejected for V1 [ARCH §16.3].

Open decisions:
> OPEN DECISION (OD-R18): if A6 fails, what form may the comment-command fallback take without violating the "no chatbot" DX principle [ARCH §12.2 intro]? — Recommendation: a single deterministic slash command (e.g., `/docsync patch`) parsed by the App, whose only response is a mechanical acknowledgment + check link; no AI prose, no conversation. If even that reads as conversational to early users, ship check-only plus "run `docsync fix` locally" guidance instead.

> OPEN DECISION (OD-R19): default state of the post-merge `push` handler in V1 [ARCH §12.2] [ARCH §12.3]. — Recommendation: implemented but default-off per repo (opt-in via App settings); merge storms would otherwise double CI load and doc-PR churn. Revisit default-on once Phase 10 evaluation data exists.

> OPEN DECISION (OD-R20): operational criteria for the auto-proposal opt-in ("repos with reviewed manifests" [ARCH §12.2]). — Recommendation: require a reviewed-manifest marker (reviewer + review commit, schema-validated) AND zero coverage warnings AND an org-floor pass; exact gating finalized in Phase 10 when reviewer feedback lands. Until then the flag exists but ships default-off and out of the quickstart.

### 9.7 Architecture traceability

Covered in this phase: [ARCH §12.2] in full (DX principle, App permission model, webhook surface, check-run lifecycle + UI + escalation, Action variant, fork handling, self-trigger suppression, rate/burst limits, idempotency, concurrency); [ARCH §12.1] consumption (exit codes 0/1/2/3, visual-language mirror); [ARCH §12.3] (post-merge handling, supersede, merge-target re-runs, D12 re-resolution); [ARCH §12.4] (doc-PR conventions consumed; integrated mode deferred); [ARCH §7.1] (early-signal, latency hooks); [ARCH §11.3] (config-error check surface, D8); [ARCH §3.4] (degradation surfaces; executable tests land in Phase 9); [ARCH §5.2] (abstention visibility); [ARCH §3.5] (idempotency keys); [ARCH §10.7] (secrets); [ARCH §13] (orchestrator performs all writes); [ARCH §10.6] (cost/latency hooks); [ARCH §10.8] (D3 display pinning); [ARCH §16.1 A6, A7]; [ARCH §16.2 R6, R7, R18]; [ARCH §0.3 D3, D4, D8, D10, D12, D14]; [CTX §30] (summary surface), [CTX §32] (exit codes/visual language), [CTX §33] (primary), [CTX §34] (separate doc PR), [CTX §38] (tokens never reach the model layer).

DEFERRED:
- DEFERRED: integrated mode (doc commits appended to the code PR) [ARCH §16.3] — opt-in only after the trust loop is proven; not in any V1 phase.
- DEFERRED: `state/` committed vs CI-only (A8) [ARCH §16.3] — decided at Phase 0 (OD-3); Phase 8's check-run metadata is unaffected either way.
- DEFERRED: auto-proposal gating criteria finalization → Phase 10 (policy lifecycle).
- DEFERRED: Issues-based reporting surface — V2 candidate; requires a permission-rationale re-review.

### 9.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §30 | 8 | §9.3 (PR-body provenance summary rendering; schema/enforcement in Phase 9) |
| CTX | §32 | 8 | §9.3 (exit-code mapping, visual-language mirror) |
| CTX | §33 | 8 | §9.1, §9.3 (check UI, App/Action, escalation) — primary |
| CTX | §34 | 8 | §9.3 (separate doc PR consumed; creation internals Phase 6) |
| CTX | §38 | 8 | §9.3 (tokens never reach the model layer; orchestrator performs writes) |
| ARCH | §3.4 | 8 | §9.3, §9.5 (degradation surfaces; executable tests Phase 9) |
| ARCH | §3.5 | 8 | §9.3, §9.4 (idempotency keys) |
| ARCH | §5.2 | 8 | §9.3 (abstention visibility in the check UI) |
| ARCH | §7.1 | 8 | §9.3, §9.5 (early-signal check updates) |
| ARCH | §10.6 | 8 | §9.4 (cost/latency hooks) |
| ARCH | §10.7 | 8 | §9.3, §9.5 (secrets handling) |
| ARCH | §10.8 | 8 | §9.3, §9.5 (confidence display pinned to policy_version + binding) |
| ARCH | §11.3 | 8 | §9.3, §9.5 (config-error check surface, D8) |
| ARCH | §12.1 | 8 | §9.3 (exit codes + visual language consumption) |
| ARCH | §12.2 | 8 | §9.1–§9.5 (primary) |
| ARCH | §12.3 | 8 | §9.3 (post-merge, supersede, merge-target re-runs) |
| ARCH | §12.4 | 8 | §9.3 (PR conventions; integrated mode deferred) |
| ARCH | §13 | 8 | §9.3 (deterministic orchestrator performs all Git/API writes) |
| ARCH | §16.1 | 8 | A6/A7 spikes — §9.2, §9.4, §9.6 |
| ARCH | §16.2 | 8 | R6 (supersede), R7 (anti-noise), R18 (D3 display pinning) |
| ARCH | §16.3 | 8 | §9.7 (DEFERRED markers) |
| D | D3 | 8 | §9.3 (percentages pinned to policy_version + model binding) |
| D | D4 | 8 | §9.3 (one doc PR per source PR; per-document partial pass) |
| D | D8 | 8 | §9.3, §9.5 (config-error fail-closed surface) |
| D | D10 | 8 | §9.3, §9.5 (neutral default; failure on opt-in only) |
| D | D12 | 8 | §9.3 (post-merge baseline re-resolution; primary Phase 6) |
| D | D14 | 8 | §9.3, §9.5 (inconclusive degradation surfaces) |
| INV | 1 | 8 | §9.5 (E2E assertion; primary enforcement Phase 6) |
| INV | 3 | 8 | §9.3, §9.5 (permission-level guarantee: no Workflows grant) |
| INV | 8 | 8 | §9.3, §9.5 (tokens never reach the model layer) |
| INV | 10 | 8 | §9.3 (provenance summary rendered into PR body; gate in Phase 9) |
| INV | 11 | 8 | §9.3, §9.5 (rate limits, idempotency, self-trigger suppression) |
| P | P12 | 8 | §9.3 (abstain path visible in the check UI) |
| P | P19 | 8 | §9.3 (deterministic orchestrator performs writes) |
| A | A6 | 8 | §9.2 (entry gate), §9.4, §9.6 (fallback) |
| A | A7 | 8 | §9.2 (entry gate), §9.4, §9.6 (limitations) |
| R | R6 | 8 | §9.3 (stale doc PRs, supersede; shared with Phase 6) |
| R | R7 | 8 | §9.3, §9.5 (anti-noise engineering) |
| R | R18 | 8 | §9.3, §9.5 (D3 display pinning) |

## §10 Phase 9 — Provenance & Safety Hardening

### 10.1 Objective & scope

Harden the shipped system into an auditable, adversarially verified one: provenance as a first-class enforced record with D5 storage precedence [ARCH §12.5] [ARCH §0.3 D5]; the four-layer lock enforcement with dedicated adversarial tests (ownership as the floor, D2) [ARCH §11.4]; all 13 safety invariants from [ARCH §14] mapped to executable tests; a safety-incident taxonomy with alerting; prompt-injection defense-in-depth tests (R8) [ARCH §10.7] [ARCH §13]; audit replay tooling that answers "why was this doc changed on this commit" from append-only state alone [ARCH §6.3]; and the [ARCH §3.4] degradation matrix encoded as fail-closed executable tests. Out of scope: evaluation and policy-lifecycle mechanics (Phase 10+), V2 claim extraction (the provenance schema is versioned to leave room), and new UX surfaces (Phase 8 owns them; this phase adds incident surfacing to existing ones).

### 10.2 Prerequisites & entry gate

- Phases 0–8 complete: the PR-creation path is in production use and Phase 8 renders the PR-body provenance summary [ARCH §12.4] [ARCH §12.5].
- `@docsync/schemas` available for the versioned provenance schema; `.docsync/state/` writable with compare-and-swap semantics [ARCH §6.3].
- The model-access layer exposes the actual routed model ID per call (including fallback outcomes) [ARCH §10.6].
- Entry gate: the provenance schema is reviewed/signed off, and a one-to-one inventory mapping each of the 13 invariants [ARCH §14] to its planned executable test exists before hardening code lands.

### 10.3 Features & deliverables

**Provenance record & schema**
- Versioned provenance record `docsync.provenance/v1` [ARCH §12.5] [CTX §30]: `source_commit`; `source_files[]`; `detector {model: actual routed OpenRouter model ID, confidence}`; `writer {model: actual routed ID}`; `verification {status}`; `generated_at`; plus `run_id`, `policy_version`, `manifest_version`, and a schema tag [ARCH §5.4] [ARCH §10.6].
- `detector.model`/`writer.model` record the **actual routed** model IDs, never a hardcoded brand; fallback routing is reflected truthfully [ARCH §10.6].
- PR-body provenance summary format (canonical per D5): the docsync block + candidate mapping table + verification results + generated-by banner [ARCH §12.4] [ARCH §12.5].
- Storage precedence D5 implemented end to end: PR body = canonical; `.docsync/state/` = archival (content-addressed by run ID, append-only); check-run metadata = ephemeral view; **Markdown document bodies are NEVER modified for provenance** [ARCH §0.3 D5] [ARCH §12.5].
- Governance rules enforced in code [ARCH §12.5]:
  - provenance required — the orchestrator REFUSES to create a doc PR whose commits lack a provenance record (a provenance-less PR is a bug, not a format option);
  - append-only + content-addressed by run ID; re-running the same run is a no-op, never a history rewrite;
  - provenance reflects what actually ran (actual models, policy version, verification status);
  - not a secret container — no tokens, no prompt dumps, no reviewer identities beyond repo exposure; write-path secret scan.

**Four-layer lock enforcement (ownership is the floor, D2) + adversarial tests**
- Layer 1 candidate selection: locked/generated/external docs are detected + shown (🔒 row) but removed from the patch candidate set **before any writer invocation** [ARCH §11.4] [ARCH §0.3 D2] [CTX §31].
- Layer 2 writer constraints: protected paths are read-only in the constrained workspace; instructions permit analysis but forbid patch emission for protected paths [ARCH §11.4].
- Layer 3 application guard: the writable-path allowlist is computed **from the manifest itself, never from model output**; every patch hunk is validated against it before applying; a violation rejects the patch **whole** — no partial application [ARCH §11.4].
- Layer 4 PR filter: the branch is re-diffed against the allowlist before PR creation; any out-of-allowlist change aborts PR creation and logs a **SAFETY INCIDENT** (alert-worthy bug class, never silently swallowed) [ARCH §11.4].
- Ownership is the floor: locked resolves to blocked at any confidence; Jev/risk/confidence can never override ownership [ARCH §0.3 D2] [ARCH §5.3].

**Safety invariant enforcement suite — all 13 from [ARCH §14], each mapped to executable tests/gates**
- INV-1 never push to the default branch → test: the Git layer rejects default-branch pushes; all writes land on `docsync/*` branches [ARCH §14.1].
- INV-2 never modify code files → test: code-file hunks rejected at Layers 3/4 [ARCH §14.2].
- INV-3 never touch files outside the manifest allowlist — incl. `.docsync/` internals (except via the promotion pipeline) and `.github/workflows/**` → tests: allowlist computation + adversarial hunks; the App permission set corroborates [ARCH §14.3].
- INV-4 locked/generated/external detected + reported, never modified → the four-layer adversarial suite [ARCH §14.4] [ARCH §11.4].
- INV-5 no patch applied/shipped without passing verification + the confidence×risk gate → test: a failed-verification patch cannot reach the working tree or a PR, even for auto-mode docs [ARCH §14.5] [ARCH §5.3].
- INV-6 abstain, don't guess → test: uncertain outcomes resolve to review; never forced into update/ignore [ARCH §14.6] [ARCH §5.2].
- INV-7 models propose; humans promote → boundary test: no path from model output to production policy/mode via the write path (the promotion pipeline is the only route; mechanics land Phase 10+) [ARCH §14.7] [ARCH §8.5].
- INV-8 models never hold credentials → test: credential absence asserted at the gateway boundary; the orchestrator performs every Git/API operation [ARCH §14.8] [ARCH §10.7].
- INV-9 fail closed → the degradation-matrix executable tests (below) [ARCH §14.9] [ARCH §3.4] [ARCH §0.3 D8] [ARCH §0.3 D14].
- INV-10 provenance or it didn't happen → test: PR creation refused without a record [ARCH §14.10] [ARCH §12.5].
- INV-11 no flood → tests: rate-limited, idempotent, self-trigger-suppressed (shared with Phase 8; re-asserted at this boundary) [ARCH §14.11] [ARCH §12.2].
- INV-12 whole-document rewrites rejected at verification → test: a rewrite-shaped DocPatch is rejected regardless of content quality [ARCH §14.12] [ARCH §4.9].
- INV-13 anchors don't silently move → test: ambiguous claim re-anchoring → `UNANCHORED` + REVIEW, never a silent move [ARCH §14.13] [ARCH §4.3].

**Safety incident taxonomy & logging/alerting**
- Taxonomy: `lock_violation`, `provenance_missing`, `out_of_allowlist_write`, `model_influence_on_authority` (any model output influencing permissions/file selection/credentials), `policy_path_violation`, `injection_contained` [ARCH §11.4] [ARCH §13].
- Structured append-only incident records; alert-worthy classification; surfaced on existing Phase-8 surfaces (check annotation, `failure` conclusion on the offending run, structured log, optional outbound webhook); never silently swallowed [ARCH §11.4] [ARCH §13].

**Prompt-injection defense-in-depth (R8)**
- Untrusted repo content (code/docs/configs) is marked as untrusted input at the gateway; all model outputs are validated against typed schemas at the boundary; a schema-violating output is retried (bounded), then abstained to REVIEW [ARCH §10.7] [ARCH §10.4] [ARCH §13].
- Hard guarantee, tested behaviorally: no model output influences permissions, file selection, or Git credentials [ARCH §13] [ARCH §16.2 R8].
- Adversarial fixture corpus: docs/instructions embedding malicious directives (ignore locks, exfiltrate tokens, patch workflows, escalate modes); containment asserted end-to-end [ARCH §16.2 R8].

**Audit replay tooling**
- From append-only `.docsync/state/` (run records + decision objects + transition logs + provenance), reconstruct "why was this doc changed on this commit" end-to-end [ARCH §6.3] [ARCH §6.1] [ARCH §5.4] [ARCH §12.5].
- The tool consumes only committed state — no re-runs, no external services; the D5 archival surface must be sufficient on its own [ARCH §6.3] [ARCH §0.3 D5].

**Fail-closed degradation matrix as executable tests [ARCH §3.4]**
- Analyzer failure → check fails **inconclusive**, never "no docs affected" (D14) [ARCH §3.4] [ARCH §0.3 D14].
- Jev unavailability / schema-violating output → bounded retries, then REVIEW (abstain), never auto [ARCH §3.4] [ARCH §10.4].
- Sonnet timeout → a "potentially stale" finding without patch [ARCH §3.4].
- Verification failure → patch rejected; proposal surfaced for human review with evidence attached [ARCH §3.4] [ARCH §0.3 D13].
- OpenRouter outage → fail toward advisory/neutral check or REVIEW; never merge-blocking unless policy opts in [ARCH §3.4] [ARCH §0.3 D10].
- Aggregate assertion: every failure mode ends in **less automation** (D14) — encoded as a test [ARCH §3.4] [ARCH §0.3 D14].

### 10.4 TODO list

- [ ] (Schema) Define versioned provenance schema `docsync.provenance/v1` in `@docsync/schemas` (zod + JSON Schema export): source_commit, source_files, detector{model,confidence}, writer{model}, verification{status}, generated_at, run_id, policy_version, manifest_version [ARCH §12.5] [CTX §30] [ARCH §5.4]
- [ ] (Schema) Populate detector/writer model fields from the actual routed OpenRouter model IDs, including fallback outcomes [ARCH §10.6]
- [ ] (Provenance) Implement the PR-body provenance summary renderer (canonical surface): docsync block + mapping table + verification results + banner [ARCH §12.4] [ARCH §12.5]
- [ ] (Provenance) Implement the archival store under `.docsync/state/`: content-addressed by run ID, append-only, CAS writes; re-running the same run is a no-op [ARCH §6.3] [ARCH §12.5] [ARCH §0.3 D5]
- [ ] (Provenance) Implement the check-run metadata projection from the same record (ephemeral view only) [ARCH §12.5] [ARCH §0.3 D5]
- [ ] (Provenance) Implement the provenance-required gate in PR creation: refuse a PR whose commits lack a record (bug-class error, not a format option) [ARCH §12.5] [ARCH §14 inv 10]
- [ ] (Provenance) Implement the secret-scan on records pre-write: no tokens, prompt dumps, or reviewer identities [ARCH §12.5]
- [ ] (Lock L1) Candidate-selection filter: locked/generated/external removed from the patch candidate set before any writer invocation; 🔒 reporting preserved [ARCH §11.4] [ARCH §0.3 D2] [CTX §31]
- [ ] (Lock L2) Writer workspace constraints: protected paths read-only; instructions forbid patch emission for protected paths [ARCH §11.4]
- [ ] (Lock L3) Application guard: writable-path allowlist computed from the manifest only; per-hunk validation pre-apply; violation → whole-patch rejection, no partial application [ARCH §11.4]
- [ ] (Lock L4) PR filter: re-diff the doc branch against the allowlist before PR creation; violation → abort + SAFETY INCIDENT [ARCH §11.4]
- [ ] (Lock) Ownership-floor wiring: locked resolves blocked at any confidence; classifier/Jev cannot override [ARCH §0.3 D2] [ARCH §5.3]
- [ ] (Adversarial) Dedicated adversarial test suite for Layers 1–4: model-emitted hunks targeting locked docs, code files, `.docsync/**`, `.github/workflows/**` — each layer must catch/block its class [ARCH §11.4] [ARCH §14]
- [ ] (Invariants) Build the invariant enforcement suite mapping all 13 [ARCH §14] invariants to ≥1 executable test/gate each, with the inventory table in test docs [ARCH §14]
- [ ] (Invariants) INV-1 test: default-branch push rejected at the Git layer; writes only on `docsync/*` [ARCH §14.1]
- [ ] (Invariants) INV-5 test: an unverified patch cannot be applied to the working tree or shipped in a PR [ARCH §14.5]
- [ ] (Invariants) INV-7 boundary test: no path from model output to production policy/mode change via the write path [ARCH §14.7] [ARCH §8.5]
- [ ] (Invariants) INV-12 test: a whole-document-rewrite DocPatch is rejected at verification regardless of content quality [ARCH §14.12] [ARCH §4.9]
- [ ] (Invariants) INV-13 test: ambiguous claim re-anchor → `UNANCHORED` + REVIEW [ARCH §14.13] [ARCH §4.3]
- [ ] (Incidents) Implement the incident taxonomy + structured append-only incident records + alerting hooks (check annotation + failure conclusion + optional outbound webhook) [ARCH §11.4] [ARCH §13]
- [ ] (Injection) Build the adversarial prompt-injection fixture corpus (embedded directives in docs/instructions/configs); wire it into the gateway-boundary test suite [ARCH §10.7] [ARCH §13] [ARCH §16.2 R8]
- [ ] (Injection) Assert typed-schema validation of all model outputs at the gateway; malformed → bounded retry → abstain to REVIEW [ARCH §10.4] [ARCH §3.4]
- [ ] (Audit) Implement the audit replay tool (standalone binary from `@docsync/verifier`): reconstruct the decision trail ("why was this doc changed on this commit") from committed `.docsync/state/` alone [ARCH §6.3] [ARCH §6.1] [ARCH §5.4]
- [ ] (Degradation) Encode the [ARCH §3.4] degradation matrix as executable tests: analyzer failure, Jev schema-violation, Sonnet timeout, verification failure, provider outage — each ends in the mandated less-automated state [ARCH §3.4] [ARCH §0.3 D14]
- [ ] (Docs) Provenance guide (schema + D5 precedence), safety-incident runbook, audit-replay how-to [ARCH §12.5] [ARCH §11.4] [ARCH §6.3]

### 10.5 Verification list

- [ ] Test: a provenance record validates against `docsync.provenance/v1`; a missing/invalid field refuses the write [ARCH §12.5]
- [ ] Test: PR creation without a provenance record is refused with an explicit provenance-missing error + incident record (INV-10) [ARCH §12.5] [ARCH §14 inv 10]
- [ ] Test: re-executing the same run leaves archival state byte-identical (no duplicate, no rewrite) [ARCH §12.5] [ARCH §0.3 D5]
- [ ] Test: when an OpenRouter fallback engages, provenance names the actually-routed model ID, not the configured primary [ARCH §10.6]
- [ ] Test: doc-PR Markdown diffs contain only documentation content; no provenance blocks injected into document bodies (D5 rule 5) [ARCH §0.3 D5] [ARCH §12.5]
- [ ] Test: check-run metadata mirrors the record but is marked ephemeral; deleting it never loses the canonical/archival copies [ARCH §0.3 D5]
- [ ] Test: secret-scan gate — a record containing a token-like string is rejected pre-write [ARCH §12.5]
- [ ] Adversarial L1: a locked/generated/external doc in the candidate set is filtered before writer invocation; the 🔒 row still renders [ARCH §11.4] [CTX §31]
- [ ] Adversarial L2: a writer emitting a patch for a protected path despite instructions is not applied; workspace read-only enforcement holds [ARCH §11.4]
- [ ] Adversarial L3: a hunk touching a non-allowlisted path rejects the entire patch; zero partial application [ARCH §11.4]
- [ ] Adversarial L4: an out-of-allowlist change smuggled onto the branch aborts PR creation + logs a SAFETY INCIDENT [ARCH §11.4]
- [ ] Adversarial: hunks targeting `.github/workflows/**` and `.docsync/**` are rejected at L3+L4 (INV-3; only the promotion pipeline is exempt) [ARCH §14 inv 3]
- [ ] Test (INV-2): a code-file patch is rejected at L3/L4 [ARCH §14.2]
- [ ] Test (INV-1): a default-branch push attempt is rejected; all writes land on `docsync/*` [ARCH §14.1]
- [ ] Test (INV-5): a verification-failed patch is never applied/shipped; the confidence×risk gate holds even for auto-mode docs [ARCH §14.5] [ARCH §5.3]
- [ ] Test (INV-6): an uncertain case resolves to review; never forced into update/ignore [ARCH §14.6] [ARCH §5.2]
- [ ] Test (INV-7): a model-proposed policy/mode change cannot become production state via the doc-write path [ARCH §14.7] [ARCH §8.5]
- [ ] Test (INV-8): gateway-boundary scan finds no credentials in any model payload across the fixture corpus [ARCH §14.8] [ARCH §10.7]
- [ ] Test (INV-12): a rewrite-shaped DocPatch (full-document replacement) is rejected regardless of quality [ARCH §14.12] [ARCH §4.9]
- [ ] Test (INV-13): content-hash-first anchoring; an ambiguous re-anchor → UNANCHORED + REVIEW, never a silent move [ARCH §14.13] [ARCH §4.3]
- [ ] Injection: every fixture-corpus case is contained — outputs schema-validated or abstained; permissions/file-selection/credentials unchanged in all cases [ARCH §10.7] [ARCH §13] [ARCH §16.2 R8]
- [ ] Degradation: each [ARCH §3.4] row exercised → asserted end state (inconclusive / REVIEW / finding-without-patch / rejected-with-evidence / advisory-neutral); aggregate assertion: failure never widens automation [ARCH §3.4] [ARCH §0.3 D14]
- [ ] Audit: the replay tool reconstructs a full trail for a seeded historical change from committed state alone — decision object, policy version, provenance, verification results [ARCH §6.3] [ARCH §5.4] [ARCH §12.5]
- [ ] Incidents: each taxonomy class is triggered in tests → a structured record + alert signal is emitted; none silently swallowed [ARCH §11.4] [ARCH §13]

Gate: phase complete when all 13 invariants have green executable tests; the four-layer lock suite passes its adversarial fixtures at every layer with whole-patch rejection semantics; provenance is required and D5 precedence is enforced (canonical PR body, append-only content-addressed archival, ephemeral check view, untouched doc bodies); the [ARCH §3.4] degradation matrix passes as executable tests; the injection corpus is fully contained; and audit replay reproduces an end-to-end decision trail from archival state alone.

### 10.6 Risks, assumptions & decisions

Assigned decisions & enforcement:
- **D5 (storage precedence)** [ARCH §0.3 D5]: implemented as code + tests — PR body canonical; archival store content-addressed, append-only, CAS; check metadata a projection; doc bodies untouched. Alternatives rejected: git-notes provenance (history-rewrite risk vs governance rule 2), provenance blocks inside Markdown bodies (churn/merge conflicts vs rule 5).
- **D2 (ownership floor) enforcement** [ARCH §0.3 D2]: Layer-1 filtering + policy-engine conservative max; locked resolves blocked at any confidence [ARCH §5.3]; adversarially tested at all four layers.
- **R8 (prompt injection via repository content)** [ARCH §16.2 R8]: defense-in-depth — typed-schema validation at the gateway [ARCH §10.4], untrusted-input marking [ARCH §10.7], the hard no-permission-influence architecture [ARCH §13], and an adversarial corpus. Residual risk: schema-valid malicious outputs — mitigated structurally because any output can only become a reviewable diff inside the manifest allowlist; tests assert the boundary behaviorally, not model intent.

Decisions taken:
1. Provenance refusal is a hard bug-class failure, never a format fallback [ARCH §12.5].
2. The incident taxonomy is the fixed six-class set above; incidents are append-only records + alert signals on existing surfaces; never swallowed [ARCH §11.4] [ARCH §13].
3. The invariant suite lives in `@docsync/verifier` (dedicated suites + adversarial fixtures) with a cross-package integration harness, not distributed ad hoc [ARCH §14].
4. The audit tool ships as a standalone binary from `@docsync/verifier`, not a new core CLI verb (the CLI verb set is Phase 7's [ARCH §12.1] contract); CLI integration deferred.
5. The provenance schema adds `run_id` + `manifest_version` beyond the [CTX §30] illustration, per [ARCH §5.4]/[ARCH §10.6] requirements; all context example fields are preserved.

Open decisions:
> OPEN DECISION (merged into OD-R12, registry §0.5): how archival provenance/incident records under `.docsync/state/` get committed during check-only CI runs (no doc PR exists to carry them), given the OD-3 committed-state default and A8 [ARCH §6.3] [ARCH §16.1 A8]. — Resolution (reconciled with Phase 6's state-carriage question into the single registry entry OD-R12): doc-PR runs carry/commit their `state/` updates on the doc PR branch; check-only CI runs persist archival records out-of-band (CI artifact + optional repo-dispatch commit job); canonical truth remains the PR body per D5 either way. Revisit with A8 evidence after the real-world trial.

> OPEN DECISION (OD-R21): the safety-incident notification channel in V1, given the App holds no Issues permission [ARCH §12.2] [ARCH §11.4]. — Recommendation: structured incident record + check-run annotation + `failure` conclusion on the offending run + optional repo-configured outbound webhook; adding an Issues permission (or org-level alerting) is a V2 decision with its own permission-rationale review.

### 10.7 Architecture traceability

Covered: [ARCH §11.4] in full (four layers + incident logging); [ARCH §12.5] in full (schema, D5 precedence, five governance rules); [ARCH §14] all 13 invariants as executable tests; [ARCH §6.3] (append-only auditability, CAS, replay); [ARCH §6.1] (baselines as replay inputs); [ARCH §3.4] (degradation matrix as tests); [ARCH §4.3] (anchors, INV-13); [ARCH §4.8] (verification tiers consumed from Phase 4; enforcement asserted here via INV-5); [ARCH §4.9] (INV-12); [ARCH §5.2] (abstain, INV-6); [ARCH §5.3] (ownership floor, conservative max); [ARCH §5.4] (decision-object refs in replay); [ARCH §8.5] (INV-7 boundary); [ARCH §10.4] (typed-schema validation); [ARCH §10.6] (actual routed model IDs); [ARCH §10.7] (secrets, injection containment); [ARCH §11.2] (modes/ownership floor); [ARCH §12.4] (PR-body canonical surface); [ARCH §13] (authority split); [ARCH §16.2 R8, R15]; [ARCH §0.3 D2, D5, D8, D13, D14]; [CTX §30] (primary), [CTX §31] (ownership categories), [CTX §37] (production orchestrator determinism), [CTX §38] (LLM never root authority).

DEFERRED:
- DEFERRED: full org policy config + floors UI [ARCH §16.3] — V3; floors are validated in the Phase 0 config core.
- DEFERRED: claim-level edit objects as the patch representation [ARCH §16.3] — V2+; the provenance schema is versioned to absorb them.
- DEFERRED: promotion-pipeline mechanics (canary/shadow promotion flows) → Phase 10+; INV-7 is tested at the write-path boundary here.
- DEFERRED: Issues/org-level incident alerting (see OD-R21).

### 10.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §30 | 9 | §10.3 (provenance schema — primary) |
| CTX | §31 | 9 | §10.3 (ownership categories in lock enforcement) |
| CTX | §37 | 9 | §10.3, §10.7 (production orchestrator determinism) |
| CTX | §38 | 9 | §10.3 (LLM never root authority; INV-7/INV-8) |
| ARCH | §3.4 | 9 | §10.3, §10.4, §10.5 (degradation matrix as executable tests) |
| ARCH | §4.3 | 9 | §10.5 (INV-13 anchors) |
| ARCH | §4.8 | 9 | §10.5 (INV-5; tiers built in Phase 4, enforcement asserted here) |
| ARCH | §4.9 | 9 | §10.5 (INV-12 rewrites) |
| ARCH | §5.2 | 9 | §10.5 (INV-6 abstain) |
| ARCH | §5.3 | 9 | §10.3 (ownership floor; conservative max) |
| ARCH | §5.4 | 9 | §10.3, §10.5 (decision-object refs; replay) |
| ARCH | §6.1 | 9 | §10.3, §10.5 (baselines as audit-replay inputs) |
| ARCH | §6.3 | 9 | §10.3, §10.4, §10.5 (append-only audit + archival store + replay tool) |
| ARCH | §8.5 | 9 | §10.5 (INV-7 boundary test) |
| ARCH | §10.4 | 9 | §10.3, §10.4 (typed-schema validation at gateway) |
| ARCH | §10.6 | 9 | §10.3, §10.5 (actual routed model IDs in provenance) |
| ARCH | §10.7 | 9 | §10.3, §10.5 (secrets + injection containment) |
| ARCH | §11.2 | 9 | §10.3 (modes/ownership floor) |
| ARCH | §11.4 | 9 | §10.1–§10.5 (four-layer enforcement + incidents — primary) |
| ARCH | §12.4 | 9 | §10.3 (PR-body canonical provenance surface) |
| ARCH | §12.5 | 9 | §10.1–§10.5 (provenance — primary) |
| ARCH | §13 | 9 | §10.3, §10.5 (no model influence on permissions/selection/credentials) |
| ARCH | §14 | 9 | §10.3, §10.4, §10.5 (all 13 invariants as tests — primary) |
| ARCH | §16.2 | 9 | R8 (injection defense — primary), R15 (archival CAS) |
| ARCH | §16.3 | 9 | §10.7 (DEFERRED markers) |
| D | D2 | 9 | §10.3 (ownership-floor enforcement + tests) |
| D | D5 | 9 | §10.3, §10.5 (storage precedence — primary) |
| D | D8 | 9 | §10.5 (fail-closed degradation tests) |
| D | D13 | 9 | §10.3 (verification independence consumed; re-asserted in suite) |
| D | D14 | 9 | §10.5 (less-automation aggregate assertion) |
| INV | 1 | 9 | §10.4, §10.5 (default-branch push rejected) |
| INV | 2 | 9 | §10.4, §10.5 (code files never modified) |
| INV | 3 | 9 | §10.4, §10.5 (allowlist incl. `.docsync/**` + `.github/workflows/**`) |
| INV | 4 | 9 | §10.4, §10.5 (four-layer lock suite) |
| INV | 5 | 9 | §10.4, §10.5 (no patch without verification + confidence×risk) |
| INV | 6 | 9 | §10.5 (abstain, don't guess) |
| INV | 7 | 9 | §10.4, §10.5 (models propose; humans promote boundary) |
| INV | 8 | 9 | §10.5 (models never hold credentials) |
| INV | 9 | 9 | §10.5 (fail-closed degradation matrix) |
| INV | 10 | 9 | §10.4, §10.5 (provenance or it didn't happen) |
| INV | 11 | 9 | §10.5 (no flood; shared with Phase 8) |
| INV | 12 | 9 | §10.4, §10.5 (rewrites rejected) |
| INV | 13 | 9 | §10.4, §10.5 (anchors don't silently move) |
| P | P8 | 9 | §10.3 (per-document mutability/ownership modes enforced) |
| P | P9 | 9 | §10.3 (never unrestricted model write authority) |
| P | P10 | 9 | §10.5 (minimal-diff INV-12 test) |
| P | P12 | 9 | §10.5 (abstain path INV-6) |
| P | P14 | 9 | §10.5 (models propose, humans promote INV-7) |
| P | P19 | 9 | §10.3 (deterministic enforcement in orchestrator code) |
| R | R8 | 9 | §10.3, §10.4, §10.5 (injection defense-in-depth — primary) |
| R | R15 | 9 | §10.3 (archival CAS writes; shared with Phase 0/6) |

---

## §11 Phase 10 — Evaluation, Feedback & Policy Lifecycle Foundation

### 11.1 Objective & scope

Phase 10 builds the **control plane's learning machinery** [ARCH §3.1, §3.6]: everything required to turn Loop-A production outcomes into labeled evaluation data, evaluate candidate policies offline, and promote policy changes through a versioned, human-gated, one-op-rollback pipeline. It turns "autonomous improvement without autonomous degradation" into executable machinery [ARCH §8.6; CTX §21] — the EVALUATION wall between model proposals and production policy [ARCH §8.5].

In scope: feedback recorder with label hygiene [ARCH §8.7]; FN/FP capture flows [ARCH §8.7; CTX §22]; historical-PR benchmark dataset builder incl. true negatives and the A3 dataset-yield gate [ARCH §9, §16.1]; frozen + rolling eval splits [ARCH §9]; six-metric evaluation framework + policy improvement objective [ARCH §9; CTX §35–36]; abstention-health monitor [ARCH §5.2; R11]; auto-patch spot-checks [ARCH §9; R13]; Policy Architect proposal generation [ARCH §8.1; CTX §16]; offline evaluation harness + promotion pipeline v1 with gate enforcement [ARCH §8.4–§8.6]; memory subsystem [ARCH §8.8; CTX §25; D1]; `learning.mode` governance (off | propose | canary | auto; default propose) [ARCH §8.9; CTX §24]; A8 revisit checkpoint [ARCH §16.1 A8, §16.3; OD-3].

Out of scope (later phases): productionized live shadow/canary machinery and `learning.mode=auto` enablement (Phase 12 — [ARCH §8.6, §8.9] full); symbol/AST analysis, claim extraction, Loop B (Phase 11 — [ARCH §4.1, §7.2, §15.2]); org-wide routing policies and the full org-floor configuration layer (Phase 12 — [ARCH §10.3, §11.2]).

### 11.2 Prerequisites & entry gate

- Phases 0–9 complete: Loop A E2E emits decision objects carrying `policy_version`, `decision_id`, `trace` [ARCH §5.4]; documentation PRs with provenance exist [ARCH §12.4, §12.5]; run records keyed `(repo, base_sha, head_sha, manifest_version, policy_version)` are deterministic-replayable [ARCH §3.5, §6.3]; OpenRouter usage accounting (cost, latency, actual routed model) is available [ARCH §10.6; D9]; A8 state-churn counters shipped in Phase 0 are populated on pilot repos [OD-3].
- Policy Compiler from Phase 2 validates/compiles policy artifacts deterministically and returns diagnostics on failure [ARCH §8.2]; the `.docsync/policy-history/` convention is established [ARCH §8.4].
- Reviewer-facing surfaces exist for the five signals: doc-PR review/merge events and check-run `requested_action` [ARCH §11.4, §12.2; CTX §23].
- **Entry gate:** a small hand-labeled fixture dataset (including true negatives) replays offline end-to-end and produces a per-policy-version evaluation report — proving the replay → label → metrics loop before the full framework is built.

### 11.3 Features & deliverables

Feedback & data capture:
- **F10-1 Feedback recorder** [ARCH §8.7; CTX §22, §23] — captures the five reviewer signals **accept / edit / reject (with reason) / mark-as-intentional / always-ignore-this-pattern** as structured, append-only rows; every row carries `policy_version` + `decision_id` + signal type so evaluation can distinguish "the policy was wrong" from "the presentation was wrong" [ARCH §5.4, §8.7].
- **F10-2 Label-hygiene normalizer** [ARCH §8.7] — implicit signals normalized before counting: timeout ⇒ `unlabeled` (never `accepted`); edit ⇒ `corrected` with the **edit diff stored — the edit diff is gold** (it shows both what was wrong and what "minimal" meant to the human); reject ⇒ `false_positive` only when the stated reason is "doc already correct", otherwise `style_rejected`.
- **F10-3 FN/FP capture flows** [ARCH §8.7; CTX §22] — FALSE NEGATIVE: Jev returned `docs_affected = false` but the developer later edits the doc in the same PR ⇒ recorded FN bound to the originating decision; FALSE POSITIVE: patch rejected because the documentation was already correct ⇒ recorded FP. Human feedback becomes high-value evaluation data.
- **F10-4 Override aggregation** [ARCH §8.7, §11.4; CTX §23] — repeated identical corrections (e.g., 17 reviewer overrides of "`src/generated/**` is not documentation-relevant") aggregate into counted evidence for Policy-Architect proposals; the reviewer's click is acknowledged instantly while the policy change arrives later as a reviewable proposal — **feedback accelerates proposals; it never bypasses evaluation** [INV-7].

Dataset & evaluation:
- **F10-5 Historical-PR benchmark dataset builder** [ARCH §9; CTX §35] — offline replay of merged PRs produces labeled benchmark cases **including true negatives**; labels derive only from hygiene-normalized feedback; the **dataset-yield study** on pilot repositories is the A3 gate [ARCH §16.1 A3].
- **F10-6 Eval split management** [ARCH §9] — frozen, version-pinned, seed-reproducible evaluation split (**contamination prevention**: candidate-policy training feedback never overlaps the benchmark) plus a **rolling "recent PRs" split** for drift detection; all labels post-hygiene.
- **F10-7 Six-metric evaluation** [ARCH §9; CTX §35] — detection recall, precision, patch correctness (judged against **source anchors** per [ARCH §4.8]), minimality **measured** (edit distance / hunk count relative to the triggering diff — not eyeballed), false-update rate, abstention quality; computed **per policy version** on the version-pinned split [ARCH §8.4].
- **F10-8 Abstention-health monitor** [ARCH §5.2; R11] — abstain-rate × human-override-rate as a 2-D health metric per policy version: too high ⇒ policy gap; too low combined with high human-override rate ⇒ overconfidence [INV-6].
- **F10-9 Auto-patch spot-check sampler** [ARCH §9; R13] — periodic **spot-check rate over auto-applied patches** (selection-bias control: only reviewed cases produce labels otherwise); audit findings are ingested as evaluation labels.
- **F10-10 Policy improvement objective** [ARCH §9; CTX §36] — `score = 0.40×precision + 0.30×recall + 0.20×patch_correctness + 0.10×efficiency` with **repository-configurable weights**; high-risk repositories' scores **collapse if `unsafe_auto_updates > 0`** (safety dominates); the goal is "maximize correct documentation updates while minimizing unjustified edits", never "maximize docs changed" [PRIN-20]; `efficiency` is fed by OpenRouter usage accounting (cost + latency) [ARCH §10.6; D9].

Policy Architect:
- **F10-11 Policy Architect runner** [ARCH §8.1; CTX §16] — assembles the ten inputs (repository structure/profile, current Jev policy, manifest, concepts, historical PRs, prior Jev decisions, FPs, FNs, reviewer corrections, known exceptions) and emits structured proposal artifacts of the **eight proposal types** (new decision types; new Jev questions; better mappings; routing rules; thresholds; evidence requirements; new exceptions; repository-specific concepts); every proposal must carry **counted evidence** ("14/23 internal refactors changed generated API docs" style) — **policy learning, not prompt optimization** [ARCH §8.1]; proposals failing Policy-Compiler validation return to the architect with diagnostics — **no partial policies ever reach production** [ARCH §8.2]; the role is bound to `policy-architect` in `models.yml` [ARCH §10.2].

Promotion pipeline:
- **F10-12 Offline evaluation harness** [ARCH §8.5, §8.6; CTX §19] — replays candidate vs current policy on the fixed split; emits a comparison report (per-metric deltas, agreement summary, gate checklist); enforces the **promotion gates**: aggregate correctness win, **no regression** in false-update rate or abstention quality, **`unsafe_auto_updates = 0`**, minimum sample size + significance floor [ARCH §8.6] — machinery foundation here; full live shadow/canary productionization is Phase 12 per D6.
- **F10-13 Promotion pipeline v1** [ARCH §8.4, §8.5; CTX §19, §20] — propose → offline evaluation → **human approval** → versioned artifact in `.docsync/policy-history/` (self-contained: `previous` pointer, `reason`, evaluation block incl. precision/recall/`unsafe_auto_updates`, `status`) → promote; **models propose, humans promote** [INV-7; ARCH §13]; rollback = promoting the prior version, a one-operation metadata act [ARCH §8.4]; every promotion event is logged with full evaluation evidence; `unsafe_auto_updates` is a **gate metric** not promotable away regardless of precision/recall [ARCH §8.4].
- **F10-14 Gate library** [ARCH §8.6; R3 partial] — reusable gate-check component (correctness-delta, no-regression, unsafe-zero, sample-size/significance) shared by offline evaluation now and shadow/canary in Phase 12; per-metric, per-bucket computation preserved for later per-bucket gaming defenses.

Memory:
- **F10-15 Memory subsystem** [ARCH §8.8; CTX §25; D1] — `.docsync/memory/` holds **exactly** `decisions.md`, `known-patterns.md`, `exceptions.md`, `failures.md`; structured, evidence-backed records (pattern name, evidence PR list, observation, action, confidence — the `generated-sdk` record shape); unverifiable entries are stored with confidence **near the floor** (an anecdote is a hypothesis, not a fact); memory is **prunable** — contradictions land in `failures.md` and the pattern's confidence decays, so memory cannot ossify wrong beliefs [R12]; memory **feeds policy refinement and retrieval** (known-patterns → Jev context + counted evidence for the architect; exceptions → policy exceptions; failures → negative evaluation examples) [PRIN-18]; feedback events use the **shared structured record format** — the DX layer never invents its own event schema [ARCH §8.8].

Governance:
- **F10-16 `learning.mode` governance** [ARCH §8.9; CTX §24] — `off | propose | canary | auto`, **default `propose`**; a single config home with a permission story (repo owner vs org policy, per the precedence rules of [ARCH §11.2, §11.3]); `canary` and `auto` are **escalations of trust**, not separate algorithms — `canary` automates the shadow phase once a human approved the candidate; `auto` additionally automates promotion **when every gate passes**; the gates **cannot be bypassed by config**; even in `auto`, promotion stays application-owned, versioned, logged with evidence, one-op rollback [INV-7, INV-9; ARCH §8.5].
- **F10-17 A8 revisit checkpoint** [ARCH §16.1 A8, §16.3; OD-3] — consumes Phase-0 churn counters plus pilot-repo trial evidence on committed-`state/` diff noise; documents the decision point (keep `state/` committed — V1 default — vs move CI-only) in an explicit decision record; the CI-only alternative stays unbuilt unless evidence demands it.

### 11.4 TODO list

- [ ] P10-T01 — Schema set in `@docsync/schemas`: feedback event, hygiene-normalized label, labeled eval case, policy proposal artifact, memory record, evaluation report, promotion record (zod + JSON Schema export) [OD-2; ARCH §8.1, §8.4, §8.7, §8.8]
- [ ] P10-T02 — Feedback recorder: ingest the five signals from doc-PR review/merge events, CLI `docsync feedback`, and check-run actions; append-only rows keyed by `decision_id` carrying `policy_version` + signal type [ARCH §5.4, §8.7, §11.4; CTX §22, §23]
- [ ] P10-T03 — Label-hygiene normalizer (timeout⇒unlabeled; edit⇒corrected + edit-diff stored; reject-reason⇒false_positive|style_rejected) with per-rule unit tests [ARCH §8.7]
- [ ] P10-T04 — FN detector: post-merge PR scan comparing the Jev outcome against later same-PR doc edits ⇒ FALSE_NEGATIVE rows [ARCH §8.7; CTX §22]
- [ ] P10-T05 — FP capture: reject reason "doc already correct" ⇒ FALSE_POSITIVE linked to decision + patch id [ARCH §8.7; CTX §22]
- [ ] P10-T06 — Always-ignore-this-pattern override aggregation + instant acknowledgment + proposal-queue emission [ARCH §8.7, §11.4; CTX §23]
- [ ] P10-T07 — Historical-PR offline replay runner over run records (deterministic per run key) producing labeled cases incl. true negatives [ARCH §3.5, §9; CTX §35]
- [ ] P10-T08 — Dataset-yield study on pilot repos; yield report (labeled-case + true-negative counts); A3 verdict + fallback plan recorded [ARCH §16.1 A3]
- [ ] P10-T09 — Frozen eval-split builder (seeded, version-pinned, contamination-separated) + rolling recent-PR split [ARCH §9]
- [ ] P10-T10 — Six-metric computation incl. minimality (edit distance / hunk count vs triggering diff) and patch correctness judged against source anchors; per-policy-version report artifact [ARCH §4.8, §9; CTX §35]
- [ ] P10-T11 — Abstention-health monitor: 2-D abstain-rate × override-rate per policy version with alert thresholds [ARCH §5.2; R11]
- [ ] P10-T12 — Spot-check sampler over auto-applied patches; audit-findings→eval-label ingestion [ARCH §9; R13]
- [ ] P10-T13 — Improvement-objective scorer: repo-configurable weights, high-risk collapse on `unsafe_auto_updates>0`, efficiency from OpenRouter usage accounting [CTX §36; ARCH §9, §10.6]
- [ ] P10-T14 — Policy Architect runner: 10-input assembly, 8 proposal types, mandatory counted-evidence field, artifact validation [ARCH §8.1; CTX §16]
- [ ] P10-T15 — Proposal→Policy-Compiler loop: invalid proposals returned with diagnostics; no partial promotion [ARCH §8.2]
- [ ] P10-T16 — Offline evaluation harness: candidate vs current replay on the frozen split; comparison report + gate checklist [ARCH §8.5, §8.6; CTX §19]
- [ ] P10-T17 — Gate library: aggregate-correctness win, no-regression false-update/abstention, `unsafe_auto_updates=0`, min-sample + significance floor [ARCH §8.6; R3]
- [ ] P10-T18 — Promotion pipeline v1: propose → offline eval → human approval → versioned `policy-history/` artifact (previous/reason/evaluation/status) → promote [ARCH §8.4, §8.5; CTX §19, §20]
- [ ] P10-T19 — One-op rollback (promote prior version) + promotion event log with full evaluation evidence [ARCH §8.4, §8.9]
- [ ] P10-T20 — Memory subsystem: D1 layout enforcement; structured evidence-backed records; unverifiable ⇒ near-floor confidence; contradiction ⇒ `failures.md` + confidence decay [D1; ARCH §8.8; CTX §25; R12]
- [ ] P10-T21 — Memory feeds: known-patterns→Jev context + architect counted evidence; exceptions→policy exceptions; failures→negative eval examples [ARCH §8.8]
- [ ] P10-T22 — `learning.mode` resolution (off|propose|canary|auto; default propose) with single config home, permission story, bypass-proof gate checks [ARCH §8.9, §11.2, §11.3; CTX §24]
- [ ] P10-T23 — A8 revisit checkpoint: churn-counter aggregation + pilot trial; decision record keep-committed vs CI-only [ARCH §16.1 A8, §16.3; OD-3]
- [ ] P10-T24 — Verifier-vs-human disagreement tracked as its own labeled metric in the eval report (R5 mitigation: false-rejects must not poison feedback data) [ARCH §16.2 R5]
- [ ] P10-T25 — Tests + docs for recorder, normalizer, dataset builder, metrics, architect, harness, promotion, memory, modes [OD-2]

### 11.5 Verification list

- [ ] V-P10-1 — Fixture doc PR with accept/edit/reject/mark-as-intentional/always-ignore produces exactly one correctly normalized feedback row each, carrying `policy_version` + `decision_id` + signal type [ARCH §8.7; CTX §23]
- [ ] V-P10-2 — Timeout case stored `unlabeled` (never `accepted`); edit case stores the reviewer's edit diff with label `corrected` [ARCH §8.7]
- [ ] V-P10-3 — Reject reason "doc already correct" ⇒ `false_positive`; any other reason ⇒ `style_rejected` [ARCH §8.7]
- [ ] V-P10-4 — Fixture PR where Jev ignored docs and a later commit edits the doc ⇒ FALSE_NEGATIVE row bound to the originating decision [CTX §22]
- [ ] V-P10-5 — Dataset-yield report exists for pilot repos with labeled-case + true-negative counts; A3 verdict recorded [A3; ARCH §16.1]
- [ ] V-P10-6 — Frozen split reproducible byte-identically from seed + version pin; benchmark rows disjoint from candidate-training feedback (contamination test) [ARCH §9]
- [ ] V-P10-7 — Eval report for a pinned policy version contains all six metrics; minimality reported as measured numbers (edit distance / hunk count vs triggering diff), not prose [ARCH §9; CTX §35]
- [ ] V-P10-8 — 2-D abstain-health metric present per policy version with configured alert thresholds [ARCH §5.2; R11]
- [ ] V-P10-9 — Spot-check sample of auto-applied patches recorded; ingested audit findings appear as eval labels [ARCH §9; R13]
- [ ] V-P10-10 — Objective score computed with repo-configurable weights; high-risk fixture with `unsafe_auto_updates=1` collapses and fails the gate [CTX §36; ARCH §9]
- [ ] V-P10-11 — Policy Architect on a synthetic FP/FN corpus emits a valid proposal artifact carrying counted evidence (e.g., "14/23"); an invalid proposal returns with compiler diagnostics and is never promoted [ARCH §8.1, §8.2; CTX §16]
- [ ] V-P10-12 — Full promotion run creates a `policy-history/` entry (previous/reason/evaluation/status); rollback restores the prior version in one operation; the promotion log contains the evaluation evidence [ARCH §8.4, §8.5; CTX §20]
- [ ] V-P10-13 — A promotion attempt failing any gate (correctness win, no-regression, unsafe-zero, min-sample/significance) is rejected with the failing gate named [ARCH §8.6; R3]
- [ ] V-P10-14 — `.docsync/memory/` contains exactly the four files; a synthetic contradiction lands in `failures.md` and the pattern's confidence decays; an unverifiable record is stored near floor confidence [D1; ARCH §8.8; R12]
- [ ] V-P10-15 — `learning.mode` defaults to `propose`; enabling `canary`/`auto` without passing gates is a validation error; gate bypass via config is rejected by test [ARCH §8.9; CTX §24]
- [ ] V-P10-16 — A8 decision record exists with measured `state/` churn from pilot repos and the keep-committed vs CI-only decision [A8; OD-3]

Gate: phase complete when the replay → feedback → label → dataset → metrics → proposal → offline-eval → human-approved-promotion loop runs end-to-end on fixture data; hygiene rules, gates (incl. `unsafe_auto_updates=0` + significance floor), memory evidence/decay rules, and `learning.mode` bypass-proofing are enforced in code with tests; and the A8 decision point is documented with trial data.

### 11.6 Risks, assumptions & decisions

Assigned gates:
- **A3 (historical PRs yield a usable labeled benchmark incl. true negatives)** — the dataset-yield study is the explicit gate (P10-T08); fallback plan if yield is low: hand-labeled seed set + synthetic fixtures, supplemented by Loop-B/spot-check data [ARCH §16.1 A3].
- **A8-revisit (teams accept committed `state/`)** — Phase 0 shipped churn counters; Phase 10 aggregates pilot evidence and records the keep-committed vs CI-only decision [ARCH §16.1 A8, §16.3; OD-3].
- **R3-partial (canary metric gaming)** — the gate library enforces no-regression on false-update/abstention, `unsafe_auto_updates=0`, and min-sample + significance floor in offline eval; full live-shadow defenses (per-bucket metrics, time-boxed canaries) complete in Phase 12 [ARCH §16.2 R3, §8.6].
- **R11 (abstention pathologies)** — abstain-rate × override-rate 2-D monitor per policy version [ARCH §16.2 R11, §5.2].
- **R12 (memory poisoning / loop collapse)** — evidence-backed records, near-floor unverifiable confidence, contradiction recording + confidence decay, and the offline-evaluation wall between memory and production [ARCH §16.2 R12, §8.5, §8.8].
- **R13 (feedback selection bias)** — spot-check rate over auto-applied patches; audit findings become eval labels [ARCH §16.2 R13, §9].
- **R5 (verifier false-rejects poisoning feedback data)** — verifier-vs-human disagreement tracked as its own metric (P10-T24) so hygiene-normalized labels can discount verifier-caused rejects; rejects land in review, never silent [ARCH §16.2 R5; D13].

Decisions taken:
- Label-hygiene normalization table (`unlabeled` / `corrected` / `false_positive` / `style_rejected`) implemented once in the recorder, before any counting [ARCH §8.7].
- Eval data model: raw feedback rows + labeled cases are files under `.docsync/` (Git-native, files-only V1 posture); datasets are version-pinned by seed + split manifest [OD-2].
- Promotion pipeline v1 is human-approved by design; `canary`/`auto` only automate phases of this same single pipeline (one pipeline, escalating trust) [ARCH §8.5, §8.9].
- Memory file format: Markdown with structured front-matter records (the CTX §25 YAML shape), machine-parseable, evidence fields mandatory [D1; CTX §25].
- Counted evidence is mandatory for every proposal artifact; assertions without counts are rejected at artifact validation [ARCH §8.1].
- The efficiency term is sourced from OpenRouter usage accounting (cost + latency), never estimated [ARCH §10.6].

Alternatives considered:
- Database/external feedback store (rejected: files-only V1 posture; Git-native reviewability; D1) [OD-2].
- Full live shadow on every PR now (rejected: D6 — protects Loop-A latency and CI budget; sampled live shadow is Phase 12) [D6].
- Auto-promotion without a significance floor (rejected: R3 — lucky streaks must not promote) [ARCH §8.6].
- Free-text memory (rejected: PRIN-18 — memory must be evidence-backed and prunable) [ARCH §2.3; CTX §25].

> OPEN DECISION (OD-R22): storage location for raw feedback rows — `.docsync/state/feedback/` (system-written, append-only, CAS) vs `.docsync/evaluation/` (curated). — Recommendation: raw append-only rows under `state/feedback/` (system-written archival, D5-style precedence), curated datasets + eval reports under `evaluation/`; exact subpath settled at implementation review.

> OPEN DECISION (OD-R23): GitHub mechanism for the five reviewer signals — derive accept/edit/reject from doc-PR merge/review events with a structured reject-reason template vs explicit check-run buttons/comments for all five. — Recommendation: hybrid — accept/edit/reject derived from doc-PR events + reason template; mark-as-intentional + always-ignore via check-run `requested_action` with a deterministic comment-command fallback; parsing owned by the DX layer, records emitted in the shared structured format.

> OPEN DECISION (OD-R24): significance-floor defaults for promotion gates — minimum labeled comparison size and statistical test. — Recommendation: ≥100 labeled disagreements per comparison and a two-proportion test at α=0.05; repo-configurable upward, never downward (the floor cannot be disabled); revisit with Phase 12 data.

> OPEN DECISION (OD-R25): default spot-check rate over auto-applied patches. — Recommendation: 10% (minimum 1 per 50), budget-capped per repo; the rate is versioned policy data and findings feed eval labels.

### 11.7 Architecture traceability

Covered: [ARCH §5.2] (abstain metrics), [ARCH §5.4] (decision ids/policy_version on rows), [ARCH §8.1], [ARCH §8.2] (compiler diagnostics loop), [ARCH §8.4], [ARCH §8.5], [ARCH §8.6] (gate machinery foundation; DEFERRED full shadow → Phase 12), [ARCH §8.7], [ARCH §8.8], [ARCH §8.9], [ARCH §9], [ARCH §10.2] (policy-architect binding), [ARCH §10.6] (efficiency feed), [ARCH §11.2/§11.3] (learning.mode permission story + precedence), [ARCH §11.4] (override→proposal), [ARCH §12.2] (signal surfaces), [ARCH §16.1 A3, A8], [ARCH §16.2 R3, R5, R11, R12, R13], [ARCH §16.3] (A8/state revisit marker); [CTX §16], [CTX §19], [CTX §20], [CTX §21] (foundation only), [CTX §22], [CTX §23], [CTX §24], [CTX §25], [CTX §35], [CTX §36]; [D1], [D6] (full shadow deferred to Phase 12), [D9] (usage accounting), [OD-2], [OD-3]. Invariants: [INV-3] (memory/promotion writes via the system pipeline, never model writes), [INV-6] (abstention first-class ⇒ abstention-quality metric), [INV-7] (models propose; humans promote), [INV-9] (fail-closed promotion). Principles: [PRIN-12], [PRIN-13], [PRIN-14], [PRIN-15], [PRIN-16] (foundation), [PRIN-17], [PRIN-18], [PRIN-20].

DEFERRED:
- [ARCH §8.6 full; D6] Productionized live shadow/canary (identical inputs, disagreement matrix, sampled live shadow, time-boxing) → Phase 12 (§13).
- [ARCH §8.9 auto] `learning.mode=auto` enablement (automatic promotion) → Phase 12 (§13); Phase 10 ships the mode plumbing with gates, defaulting `propose`.
- [ARCH §16.3] Adaptive thresholds / per-policy-version calibration evolution → Phase 12 (§13); Phase 10 only computes per-policy-version metrics.

### 11.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §16 | 10 | §11.3 F10-11; §11.4 P10-T14; §11.5 V-P10-11 |
| CTX | §19 | 10 | §11.3 F10-12/F10-13; §11.4 P10-T16/T18 |
| CTX | §20 | 10 | §11.3 F10-13; §11.4 P10-T18/T19; §11.5 V-P10-12 |
| CTX | §21 | 10 | §11.3 F10-12 (offline gate machinery foundation; full → Phase 12) |
| CTX | §22 | 10 | §11.3 F10-1/F10-3; §11.4 P10-T02/T04/T05; §11.5 V-P10-4 |
| CTX | §23 | 10 | §11.3 F10-1/F10-4; §11.4 P10-T02/T06; §11.5 V-P10-1 |
| CTX | §24 | 10 | §11.3 F10-16; §11.4 P10-T22; §11.5 V-P10-15 |
| CTX | §25 | 10 | §11.3 F10-15; §11.4 P10-T20; §11.5 V-P10-14 |
| CTX | §35 | 10 | §11.3 F10-5/F10-7; §11.4 P10-T07/T10; §11.5 V-P10-7 |
| CTX | §36 | 10 | §11.3 F10-10; §11.4 P10-T13; §11.5 V-P10-10 |
| ARCH | §5.2 | 10 | §11.3 F10-8; §11.4 P10-T11; §11.5 V-P10-8 |
| ARCH | §5.4 | 10 | §11.3 F10-1 (rows carry policy_version/decision_id) |
| ARCH | §8.1 | 10 | §11.3 F10-11; §11.4 P10-T14 |
| ARCH | §8.2 | 10 | §11.4 P10-T15; §11.5 V-P10-11 (diagnostics loop) |
| ARCH | §8.4 | 10 | §11.3 F10-13; §11.4 P10-T18/T19; §11.5 V-P10-12 |
| ARCH | §8.5 | 10 | §11.3 F10-12/F10-13/F10-16; §11.5 V-P10-12/13/15 |
| ARCH | §8.6 | 10 | §11.3 F10-12/F10-14; §11.4 P10-T16/T17; §11.5 V-P10-13 (full shadow DEFERRED → 12) |
| ARCH | §8.7 | 10 | §11.3 F10-1–F10-4; §11.4 P10-T02–T06; §11.5 V-P10-1–4 |
| ARCH | §8.8 | 10 | §11.3 F10-15; §11.4 P10-T20/T21; §11.5 V-P10-14 |
| ARCH | §8.9 | 10 | §11.3 F10-16; §11.4 P10-T22; §11.5 V-P10-15 (auto enablement DEFERRED → 12) |
| ARCH | §9 | 10 | §11.3 F10-5–F10-10; §11.4 P10-T07–T13; §11.5 V-P10-5–10 |
| ARCH | §10.2 | 10 | §11.3 F10-11 (policy-architect binding) |
| ARCH | §10.6 | 10 | §11.3 F10-10 (efficiency from usage accounting); §11.4 P10-T13 |
| ARCH | §11.2/§11.3 | 10 | §11.3 F10-16 (learning.mode permission story/precedence) |
| ARCH | §11.4 | 10 | §11.3 F10-4 (override→proposal); §11.4 P10-T06 |
| ARCH | §16.1 A3 | 10 | §11.4 P10-T08; §11.5 V-P10-5 |
| ARCH | §16.1 A8 | 10 | §11.4 P10-T23; §11.5 V-P10-16 |
| ARCH | §16.2 R3 | 10 | §11.4 P10-T17 (gate library; full defenses → 12) |
| ARCH | §16.2 R5 | 10 | §11.4 P10-T24 |
| ARCH | §16.2 R11 | 10 | §11.4 P10-T11; §11.5 V-P10-8 |
| ARCH | §16.2 R12 | 10 | §11.4 P10-T20; §11.5 V-P10-14 |
| ARCH | §16.2 R13 | 10 | §11.4 P10-T12; §11.5 V-P10-9 |
| ARCH | §16.3 | 10 | §11.4 P10-T23 (A8/state revisit decision record) |
| D | D1 | 10 | §11.3 F10-15 (memory = exactly 4 files); §11.5 V-P10-14 |
| D | D6 | 10 | §11.6 alternatives (full live shadow rejected for Phase 10; → Phase 12) |
| D | D9 | 10 | §11.3 F10-10 (OpenRouter usage accounting as efficiency input) |
| INV | INV-3 | 10 | §11.3 F10-13/F10-15 (memory/promotion writes via system pipeline) |
| INV | INV-6 | 10 | §11.3 F10-8 (abstention first-class ⇒ monitored) |
| INV | INV-7 | 10 | §11.3 F10-13/F10-16; §11.5 V-P10-15 |
| INV | INV-9 | 10 | §11.6 decisions (fail-closed promotion) |
| PRIN | PRIN-12 | 10 | §11.3 F10-8 (explicit abstain path measured) |
| PRIN | PRIN-13 | 10 | §11.3 F10-13 (versioned policy artifacts) |
| PRIN | PRIN-14 | 10 | §11.3 F10-11/F10-13 (architect proposes, never mutates) |
| PRIN | PRIN-15 | 10 | §11.3 F10-12 (offline evaluation) |
| PRIN | PRIN-16 | 10 | §11.3 F10-12 (shadow/canary foundation; full → 12) |
| PRIN | PRIN-17 | 10 | §11.3 F10-1–F10-4 (learn from reviewer corrections) |
| PRIN | PRIN-18 | 10 | §11.3 F10-15 (evidence-backed, prunable memory) |
| PRIN | PRIN-20 | 10 | §11.3 F10-10 (correctness, not edit volume) |

## §12 Phase 11 — V2: Deeper Understanding

### 12.1 Objective & scope

Phase 11 upgrades detection **precision** and **understanding depth** without changing developer-facing primitives — still checks, diffs, PRs [ARCH §15.2]. It delivers: symbol mapping + per-language AST analysis (TypeScript first per OD-4) [ARCH §3.7, §4.1; CTX §41]; type-aware diffs enabling claim-level invalidation [ARCH §4.1, §4.3]; API/schema diff maturation; the document-graph upgrade with richer edges and mapping-proposal automation [ARCH §4.2, §4.4]; automated claim extraction as source-verified proposals [ARCH §4.3, §10.2]; claim-level edit objects as a scoped patch-representation refinement [ARCH §16.3, §15.2]; historical drift analysis + the headline claim-support query [ARCH §4.3, §6.1; CTX §11]; the Loop B background drift audit [ARCH §7.2; CTX §26]; the behavioral-proxy coverage audit (A5 gate) [ARCH §16.1]; and generated-file pattern wiring [ARCH §4.1, §8.8].

Out of scope (Phase 12): shadow/canary productionization, `learning.mode=auto` enablement, adaptive thresholds, model routing policies, full org floors, cross-repo consistency [ARCH §8.6, §8.9, §10.3, §11.2 full, §15.3].

### 12.2 Prerequisites & entry gate

- Phase 10 operating: evaluation framework + feedback recorder consume new analyzer signals and trial verdicts; dataset/split machinery available for A4/A5 studies [ARCH §9].
- Phase 1 `ChangeAnalysis` contract with `coverage_gap` / `deep_analysis: skipped` semantics in run records [ARCH §3.7, §4.1; D14]; claim storage `.docsync/state/claims/` from Phase 1 (consumed by Phase 4 verification; V1 fallback: manifest-declared/hand-anchored claims) [ARCH §4.3; INV-13]; baselines + `state/audit-cursor` from Phase 1 (state writers from Phase 0, orchestration Phase 5) [ARCH §6.1, §6.3].
- Retrieval hierarchy: levels 1–2, 5, 6 live from Phase 1 with levels 3–4 (symbol, API/schema) interface-ready but inactive (declared coverage gap); Phase 11 ACTIVATES/deepens levels 3–4 [ARCH §4.5; CTX §8].
- Entry gate: a TypeScript fixture repo demonstrates an end-to-end symbol→claim invalidation (a type-level change flips a mapped claim `verified → suspect`) with zero LLM involvement.

### 12.3 Features & deliverables

Symbol & AST analysis:
- **F11-1 `@docsync/analyzer-typescript` (tree-sitter, OD-4)** [ARCH §3.7, §4.1; CTX §41] — the first per-language AST analyzer package: symbol extraction into per-blob-SHA symbol tables; **symbol index cached per blob SHA, built incrementally per changed file** [ARCH §3.7].
- **F11-2 Symbol diffing + retrieval levels 3–4** [ARCH §4.1, §4.5; CTX §8] — symbol-level diffs (signature/type/visibility changes) enrich `ChangeAnalysis.symbols_changed`; symbol mapping (level 3) and API/schema mapping (level 4) join the retrieval hierarchy with per-level provenance.
- **F11-3 Declared degradation** [ARCH §3.7, §4.1; D14] — languages without tooling degrade to path/dependency-level analysis with a declared `coverage_gap` in the run record — **never silent** [PRIN-3].
- **F11-4 Type-aware diffs → claim-level invalidation** [ARCH §4.1, §4.3; CTX §11] — the target case `idempotencyKey: string → string?`: a type-level change directly flips the dependent claim's status `verified → suspect/stale` and drives the containing document through the state machine [ARCH §6.2] — much stronger than asking an LLM whether a file "looks related".
- **F11-5 API/schema diff maturation** [ARCH §4.1; CTX §41] — OpenAPI delta classification (breaking / behavior-annotating / non-breaking) feeding API/schema mapping; deeper semantic config diffs (key-level resolved values) feeding `config_diff`.
- **F11-6 Document graph upgrade** [ARCH §4.2; CTX §10] — richer edges (APIs, configs, workflows) alongside code/concepts/docs; **reverse and forward lookup** maintained [ARCH §4.2]; **contract-driven classification improvements** consuming the new diff kinds [ARCH §4.4; CTX §12]; **mapping-proposal automation** via policy refinement — mapping edits arrive as reviewable proposals/PRs, **never silent rewrites** [ARCH §4.2, §8.7; INV-3].

Claims:
- **F11-7 Automated claim extraction** [ARCH §4.3, §10.2] — the `claim-extractor` role (mid-tier model via OpenRouter) proposes claim sets with candidate anchors; the output is a **PROPOSAL** — it becomes truth only after verification against source evidence [ARCH §4.3]; hand-anchored/manifest-declared claims remain authoritative; the **A4 extractor-quality trial gate** runs on representative docs [ARCH §16.1 A4]; prose-heavy docs fall back to **section-level checking, declared in the run record** — never silent [ARCH §4.3; INV-13].
- **F11-8 Claim-level edit objects** [ARCH §16.3, §15.2] — the scope decision for the deferred V2+ patch-representation refinement (recommendation in §12.6, OD-R26): attach **verified claim references to DocPatch hunks** so verification and reviewers see claim-level semantics, while hunk-based application (with its tested merge behavior, A9/D12 heritage) is retained [ARCH §4.8, §4.9].

Drift & Loop B:
- **F11-9 Historical drift analysis** [ARCH §4.3, §6.1; CTX §11] — baseline differentials (current HEAD vs the baseline commit, intersected with the baseline's sources/symbols/claims [ARCH §6.1]) and the **claim-support sweep** — the headline query: *"Which documented claims lost support in the source of truth?"* [ARCH §4.3; PRIN-6].
- **F11-10 Loop B background drift audit** [ARCH §7.2; CTX §26] — trigger: scheduled (nightly) workflow or post-merge on the default branch — **never on the PR critical path**; **read-mostly** semantics; incremental since `state/audit-cursor`; chunked per document (manifest-scoped, no blanket scanning [PRIN-2; D7]); findings **deduplicated by claim ID** so nightly runs don't spam; background findings carry evidence (claim IDs, commits, confidence) and surface as **advisory artifacts** (default-branch check run, drift dashboard, issue) — **not auto-writes**; findings warranting patches become SUGGEST items **reusing the same verification + policy engine path as Loop A (one code path, two triggers)** [ARCH §7.2; INV-6]; the `drift-auditor` role is bound in `models.yml` [ARCH §10.2].
- **F11-11 Behavioral-proxy coverage audit (A5 gate)** [ARCH §16.1 A5] — measures how well "externally observable behavior changed" is approximated by signature/type/API-surface proxies; produces the **per-language AST coverage report**; recall gaps are declared per language and uncertain cases route to REVIEW [D14].
- **F11-12 Generated-file pattern integration** [ARCH §4.1, §8.8] — the memory pattern `generated-sdk` wired to the analyzer action `inspect_generated_output` ("changes under `src/api/generated` may alter public documentation indirectly" → route per policy); analyzer mispredictions feed closed-loop learning via the recorder [ARCH §4.1, §8.7].

### 12.4 TODO list

- [ ] P11-T01 — Scaffold `@docsync/analyzer-typescript` with tree-sitter grammars; symbol extraction → per-blob-SHA symbol tables [OD-4; ARCH §3.7, §4.1; CTX §41]
- [ ] P11-T02 — Symbol index cache keyed by blob SHA + incremental build per changed file; cache-invalidation tests [ARCH §3.7]
- [ ] P11-T03 — Symbol-level diff computation (signature/type/visibility) enriching `ChangeAnalysis.symbols_changed` [ARCH §4.1]
- [ ] P11-T04 — Wire retrieval levels 3–4: symbol mapping + API/schema mapping with per-level provenance [ARCH §4.5; CTX §8]
- [ ] P11-T05 — `coverage_gap` declaration for unsupported languages in run records + REVIEW routing (never silent) [ARCH §3.7, §4.1; D14]
- [ ] P11-T06 — Type-aware diff records driving claim-level invalidation (`idempotencyKey` fixture): claim status verified→suspect/stale + state-machine transition [ARCH §4.1, §4.3, §6.2; CTX §11]
- [ ] P11-T07 — OpenAPI delta classifier (breaking / behavior-annotating / non-breaking) feeding API/schema mapping + triggers [ARCH §4.1; CTX §41]
- [ ] P11-T08 — Deeper semantic config diffs (key-level resolved values) [ARCH §4.1]
- [ ] P11-T09 — Document graph richer edges (APIs, configs, workflows) + reverse/forward lookup invariant tests [ARCH §4.2; CTX §10]
- [ ] P11-T10 — Contract-driven classification improvements consuming symbol/API/config diffs [ARCH §4.4; CTX §12]
- [ ] P11-T11 — Mapping-proposal automation via policy refinement: reviewable mapping-edit proposals/PRs; test asserts no silent rewrite path exists [ARCH §4.2, §8.7]
- [ ] P11-T12 — `claim-extractor` role implementation (mid-tier OpenRouter binding): doc → PROPOSAL claim set with candidate anchors [ARCH §10.2, §4.3]
- [ ] P11-T13 — Proposal→truth gate: extracted claims verified against source evidence before driving any automatic action; hand-anchored claims remain authoritative [ARCH §4.3]
- [ ] P11-T14 — A4 extractor-quality trial on representative docs; verdict recorded; prose-heavy section-level fallback declared in run records [ARCH §16.1 A4, §4.3]
- [ ] P11-T15 — Claim-level edit objects: verified claim references attached to DocPatch hunks; verifier consumes them (scope per OD-R26) [ARCH §4.8, §15.2, §16.3]
- [ ] P11-T16 — Historical drift analysis: baseline differentials (HEAD vs baseline ∩ sources/symbols/claims) [ARCH §6.1]
- [ ] P11-T17 — Claim-support sweep query + report ("which documented claims lost support in the source of truth?") [ARCH §4.3; CTX §11]
- [ ] P11-T18 — Loop B scheduler: nightly scheduled workflow + optional post-merge default-branch trigger; hard assertion it never runs on the PR critical path [ARCH §7.2; CTX §26]
- [ ] P11-T19 — Incremental audit since `state/audit-cursor`; per-document chunking; findings deduplicated by claim ID [ARCH §6.3, §7.2]
- [ ] P11-T20 — Background-finding artifacts with evidence (claim IDs, commits, confidence): default-branch check run, dashboard/issue — advisory only [ARCH §7.2]
- [ ] P11-T21 — Findings→SUGGEST path reusing the Loop-A verification + policy engine (one code path, two triggers) + `drift-auditor` binding in `models.yml` [ARCH §7.2, §10.2]
- [ ] P11-T22 — A5 behavioral-proxy coverage audit + per-language AST coverage report; verdict recorded [ARCH §16.1 A5]
- [ ] P11-T23 — `generated-sdk` pattern → `inspect_generated_output` action wiring; misprediction feedback into the recorder [ARCH §4.1, §8.7, §8.8]
- [ ] P11-T24 — Tests + docs (analyzer package, claim proposals, Loop B, coverage reports) [OD-2]

### 12.5 Verification list

- [ ] V-P11-1 — TypeScript fixture: `idempotencyKey: string → string?` invalidates the mapped claim (verified→suspect) with zero LLM calls; the run record shows symbol-level evidence [ARCH §4.1, §4.3]
- [ ] V-P11-2 — Symbol index: unchanged blob SHAs hit cache; only changed files rebuild (asserted via build counters) [ARCH §3.7]
- [ ] V-P11-3 — Unsupported-language fixture: the run record contains `coverage_gap`; the run routes REVIEW — never silent degradation [D14; ARCH §3.7]
- [ ] V-P11-4 — OpenAPI fixture: a breaking delta is classified `breaking` and routed via API/schema mapping; a non-breaking delta does not rouse contracts [ARCH §4.1, §4.4]
- [ ] V-P11-5 — Extracted claims are stored as proposals; an unverified proposal never drives an auto action; a post-verification claim becomes authoritative with its source anchor [ARCH §4.3]
- [ ] V-P11-6 — Prose-heavy fixture falls back to section-level checking with the fallback declared in the run record [ARCH §4.3]
- [ ] V-P11-7 — Claim-support sweep on a fixture with known drift returns exactly the expected lost-support claims (headline query) [ARCH §4.3; CTX §11]
- [ ] V-P11-8 — Loop B: the nightly workflow runs only on the default branch; the audit-cursor advances; an identical re-run yields zero duplicate findings for the same claim ID [ARCH §7.2]
- [ ] V-P11-9 — Background findings carry claim IDs + commits + confidence and surface as advisory artifacts only (no doc writes outside the SUGGEST path) [ARCH §7.2]
- [ ] V-P11-10 — A finding on a suggest-mode doc becomes a SUGGEST item processed by the same verification + policy engine (assert shared code path id) [ARCH §7.2]
- [ ] V-P11-11 — A5 coverage report exists with per-language AST coverage numbers; verdict recorded [A5; ARCH §16.1]
- [ ] V-P11-12 — A mapping proposal appears as a reviewable PR; no code path silently rewrites mappings [ARCH §4.2]
- [ ] V-P11-13 — A `src/api/generated` change triggers `inspect_generated_output`; a recorded misprediction appears as feedback [ARCH §4.1, §8.7, §8.8]

Gate: phase complete when type-aware symbol analysis runs first-class for TypeScript with declared coverage gaps elsewhere, claims exist beyond hand-anchoring only as source-verified proposals, the headline claim-support query works on fixtures, Loop B produces deduplicated advisory findings that reuse the Loop-A verification/policy path, and A4/A5 trial verdicts are recorded.

### 12.6 Risks, assumptions & decisions

Assigned gates:
- **A4 (claim extraction + anchoring works on representative docs)** — the extractor-quality trial is the gate (P11-T14); if it fails, Tier-2 verification degrades to section-level checking — declared, never silent — and hand-anchored claims remain truth [ARCH §16.1 A4, §4.3].
- **A5 ("externally observable behavior changed" is deterministically approximable)** — the behavioral-proxy coverage audit is the gate (P11-T22); recall gaps are declared per language and uncertain cases route to REVIEW [ARCH §16.1 A5; D14].

Decisions taken:
- Tree-sitter (not per-repo compiler APIs) for the first language analyzer — uniform grammar model, incremental per-blob-SHA caching; OD-4 ordering honored (TypeScript first; the generic path-level analyzer always present) [OD-4; ARCH §3.7].
- Claim extraction is **proposal-only** in V2; extraction output crosses into truth only after source-evidence verification [ARCH §4.3].
- Claim-level edit objects: attach verified claim references to DocPatch hunks; a full claim-diff representation is held behind evaluation evidence (OD-R26) [ARCH §15.2, §16.3].
- Loop B is strictly read-mostly with SUGGEST-only escalation reusing Loop-A machinery — one code path, two triggers [ARCH §7.2].
- Mapping changes only ever arrive as proposals through the policy-refinement/promotion pipeline [ARCH §4.2, §8.7].

Alternatives considered:
- TS compiler API instead of tree-sitter (rejected for the V2 core: tree-sitter gives uniform cross-language incremental parsing; a compiler API can be a later TS-specific enrichment) [ARCH §3.7].
- External graph DB for the document graph (rejected: files under `.docsync/` are reviewable and Git-native; a DB is a Phase-12-deferred optimization, never truth) [ARCH §3.7, §16.3].
- Auto-applying Loop-B fixes (rejected: findings are advisory; patches only via the SUGGEST/verification path) [ARCH §7.2].

> OPEN DECISION (OD-R26): claim-level edit-object scope — hunk-attached claim references vs a full claim-diff patch representation. — Recommendation: V2 ships hunk-attached verified claim references (verifiable, low-risk, same application semantics); a full claim-diff representation only if Phase 12 evaluation shows verifier/reviewer benefit; until then [ARCH §16.3] stays formally deferred with this recommendation.

> OPEN DECISION (OD-R27): Loop B default trigger — nightly scheduled only vs nightly + post-merge default-branch run. — Recommendation: nightly default; post-merge optional per-repo config (cost control); both never on the PR critical path.

> OPEN DECISION (OD-R28): OpenAPI delta taxonomy granularity — binary breaking/non-breaking vs three-tier (breaking / behavior-annotating / non-breaking). — Recommendation: three-tier; the middle tier maps to "possible documentation impact" triggers so deprecations/annotations rouse docs without failing contracts.

### 12.7 Architecture traceability

Covered: [ARCH §3.7] (symbol index caching, degradation, graph-as-files), [ARCH §4.1] (type-aware diffs, claim-level invalidation, API/schema/config diffs, generated-file detection), [ARCH §4.2] (graph upgrade, reverse/forward lookup, mapping proposals), [ARCH §4.3] (claims as proposals, anchoring, section-level fallback, headline query), [ARCH §4.4] (contract-driven classification), [ARCH §4.5] (retrieval levels 3–4), [ARCH §6.1] (baseline differentials), [ARCH §6.2] (invalidation → state machine), [ARCH §6.3] (audit-cursor, claims storage), [ARCH §7.2] (Loop B), [ARCH §8.7] (misprediction feedback), [ARCH §8.8] (pattern wiring), [ARCH §10.2] (claim-extractor, drift-auditor roles), [ARCH §10.3] (role bindings), [ARCH §15.2], [ARCH §16.1 A4, A5], [ARCH §16.3] (claim-level edit objects decision; graph-DB deferral); [CTX §8] (retrieval hierarchy), [CTX §10], [CTX §11], [CTX §12], [CTX §26], [CTX §41]; [D7] (Loop B respects the manifest allowlist), [D14] (declared degradation). Invariants: [INV-3] (mapping changes via the pipeline), [INV-6] (uncertain → review), [INV-13] (anchors don't silently move — extraction anchoring). Principles: [PRIN-2] (no blanket scanning), [PRIN-3] (deterministic evidence before AI), [PRIN-6] (claims backed by sources), [PRIN-7] (explicit relationships), [PRIN-18] (evidence-backed patterns).

DEFERRED:
- [ARCH §16.3] External graph DB / index cache for very large monorepos — optimization only, never truth; the decision gate is documented in Phase 12 (§13).
- [ARCH §16.3] Full claim-diff patch representation — recommendation recorded (OD-R26); final adoption gate at Phase 12 evaluation.
- [ARCH §4.3] Automated claim anchoring as truth remains conditional on the A4 verdict; the V2 section-level fallback persists until the trial passes.

### 12.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §8 | 11 | §12.3 F11-2/F11-5 (hierarchy levels 3–4 fed) |
| CTX | §10 | 11 | §12.3 F11-6; §12.4 P11-T09 (reverse/forward lookup invariants) |
| CTX | §11 | 11 | §12.3 F11-4/F11-9; §12.4 P11-T06/T17; §12.5 V-P11-1/7 |
| CTX | §12 | 11 | §12.3 F11-6; §12.4 P11-T10 |
| CTX | §26 | 11 | §12.3 F11-10; §12.4 P11-T18–T21; §12.5 V-P11-8/9/10 |
| CTX | §41 | 11 | §12.1 + F11-1–F11-9 (V2 list item-by-item) |
| ARCH | §3.7 | 11 | §12.3 F11-1/F11-3; §12.4 P11-T01/T02/T05; §12.5 V-P11-2/3 |
| ARCH | §4.1 | 11 | §12.3 F11-1/F11-4/F11-5/F11-12; §12.5 V-P11-1/4/13 |
| ARCH | §4.2 | 11 | §12.3 F11-6; §12.4 P11-T09/T11; §12.5 V-P11-12 |
| ARCH | §4.3 | 11 | §12.3 F11-7/F11-9; §12.4 P11-T12–T14/T16/T17; §12.5 V-P11-5/6/7 |
| ARCH | §4.4 | 11 | §12.3 F11-6; §12.4 P11-T10; §12.5 V-P11-4 |
| ARCH | §4.5 | 11 | §12.3 F11-2; §12.4 P11-T04 |
| ARCH | §6.1 | 11 | §12.3 F11-9; §12.4 P11-T16 |
| ARCH | §6.2 | 11 | §12.4 P11-T06 (invalidation → state transition) |
| ARCH | §6.3 | 11 | §12.4 P11-T19 (audit-cursor; claims storage) |
| ARCH | §7.2 | 11 | §12.3 F11-10; §12.4 P11-T18–T21; §12.5 V-P11-8/9/10 |
| ARCH | §8.7 | 11 | §12.3 F11-12 (misprediction feedback); §12.4 P11-T23 |
| ARCH | §8.8 | 11 | §12.3 F11-12 (pattern wiring); §12.4 P11-T23 |
| ARCH | §10.2 | 11 | §12.3 F11-7/F11-10 (claim-extractor, drift-auditor roles) |
| ARCH | §15.2 | 11 | §12.1 (all V2 items); §12.3 F11-8 (claim-level edits) |
| ARCH | §16.1 A4 | 11 | §12.4 P11-T14; §12.5 V-P11-5/6 |
| ARCH | §16.1 A5 | 11 | §12.4 P11-T22; §12.5 V-P11-11 |
| ARCH | §16.3 | 11 | §12.3 F11-8 + OD-R26 (claim-level edit objects); graph-DB deferral → §13 |
| D | D7 | 11 | §12.3 F11-10 (Loop B scans manifest docs only) |
| D | D14 | 11 | §12.3 F11-3/F11-11 (declared degradation; uncertain → review) |
| INV | INV-3 | 11 | §12.3 F11-6 (mapping changes via pipeline only) |
| INV | INV-6 | 11 | §12.3 F11-11 (uncertain → REVIEW) |
| INV | INV-13 | 11 | §12.3 F11-7 (content-hash-first anchoring; UNANCHORED → REVIEW) |
| PRIN | PRIN-2 | 11 | §12.3 F11-10 (chunked per document, no blanket scanning) |
| PRIN | PRIN-3 | 11 | §12.3 F11-1–F11-5 (deterministic evidence before AI) |
| PRIN | PRIN-6 | 11 | §12.3 F11-7/F11-9 (claims backed by sources) |
| PRIN | PRIN-7 | 11 | §12.3 F11-6 (explicit relationships) |
| PRIN | PRIN-18 | 11 | §12.3 F11-12 (pattern evidence wiring) |

## §13 Phase 12 — V3: Autonomy Under Governance

### 13.1 Objective & scope

Phase 12 productionizes autonomy strictly under governance [ARCH §15.3]: full shadow/canary machinery per D6 [ARCH §8.6; CTX §21]; `learning.mode=auto` enablement [ARCH §8.9; CTX §24]; adaptive thresholds [D11; R4]; org-wide model routing policies [ARCH §10.3; CTX §39]; the full organizational policy-floor configuration layer [ARCH §11.2]; autonomous background-audit maturation and cross-repository documentation consistency [ARCH §15.3; CTX §42] — while the **write path never changes: branch → PR → human review** [ARCH §15.3; INV-1]. V3's autonomy items are exactly those requiring the promotion pipeline, shadow/canary evaluation, org floors, and audit-grade provenance already mature (Phases 10–11) [ARCH §15.3].

Out of scope / explicitly deferred with decision gates (F12-10): integrated mode [ARCH §12.4, §16.3]; external graph DB / index cache [ARCH §3.7, §16.3]; Jev model identity choice [D9, §16.3].

### 13.2 Prerequisites & entry gate

- Phase 10: promotion pipeline + gate library + significance floor live; feedback labels accumulating; A3 satisfied (a usable labeled benchmark exists) [ARCH §8.4–§8.6, §9, §16.1 A3].
- Phase 11: Loop B + deeper understanding live; claim-level artifacts available for richer disagreement analysis [ARCH §7.2, §15.2].
- `models.yml` role bindings and the per-binding calibration-check machinery exist [ARCH §10.3, §10.8]; memoization keyed by `(diff-subset, policy_version)` is available from Phase 5 [ARCH §7.1, §3.5].
- Org-level surface exists: the V1 forward-declared floors (org-floor inputs → validation errors, Phase 0) plus the org layer of the precedence rules [ARCH §11.2, §11.3].
- Entry gate: an offline replay of the frozen split produces a full candidate-vs-production agreement/disagreement matrix with every gate evaluated end-to-end.

### 13.3 Features & deliverables

Shadow/canary productionization:
- **F12-1 Shadow/canary productionization** [ARCH §8.6; CTX §21; D6] — evaluation runs **primarily as offline historical replay** plus a **sampled live shadow** (protects Loop-A latency and CI budget — D6; dual-Jev on every PR is explicitly rejected); shadow runs see **identical inputs** as production (same change analysis, same candidates) so differences are attributable to the policy; **decision-level agreement/disagreement matrix** (both-ignore, both-review, v17-review/v18-ignore, …) with every disagreement resolvable into a label via reviewer outcome; **time-boxed canaries** + **auto-expired stale shadows**; **memoization separation** — shadow runs keyed by `policy_version` so shadow decisions never poison memoized production results [ARCH §7.1].
- **F12-2 Full promotion gates + gaming defenses** [ARCH §8.6; R3] — aggregate correctness win; **no regression** in false-update rate or abstention quality (gating on accuracy alone is rejected as gameable); **per-bucket metrics**; **minimum sample size + significance floor** so lucky streaks cannot promote; `unsafe_auto_updates = 0`; time-boxed canaries.
- **F12-3 `learning.mode=auto` enablement** [ARCH §8.9; CTX §24] — when every gate passes: automatic promotion that remains **application-owned, versioned, logged with full evaluation evidence, one-op rollback** [ARCH §8.4, §8.9; PRIN-19]; any path where Sonnet output becomes live policy without crossing EVALUATION is vetoed [ARCH §8.5; INV-7].

Calibration & routing:
- **F12-4 Adaptive thresholds** [D11; R4; ARCH §5.3] — per-policy-version calibration evolution (e.g., the 0.94/0.72 thresholds) riding the **same versioned-promotion rails** as every other policy change — the confidence × risk matrix *shape* is fixed, its calibration evolves [ARCH §5.3; PRIN-11]; **time-split evaluation + rolling re-evaluation** guard against threshold overfitting to history [ARCH §16.2 R4]; **calibration re-check per model binding** — a binding change failing calibration on the evaluation split cannot promote; displayed confidence stays pinned to `(policy_version, model binding)` [ARCH §10.8; D3].
- **F12-5 Org-wide model routing policies** [ARCH §10.3; CTX §39; D9] — a declarative, versioned routing layer (e.g., "high-risk → stronger model") consumed by the agent router [ARCH §10.1]; routing changes are **policy changes, not code changes**; **replacement models are shadow-evaluated through the same canary machinery** — a model swap is treated like a policy change [R2; D9]; provenance always names the **actual routed model** [ARCH §10.6].

Organizational governance:
- **F12-6 Full org policy floors layer** [ARCH §11.2, §11.3] — org-owned configuration defining floors (e.g., "documents matching `docs/security/**` may not exceed `suggest`"; "all repos run report-only until their manifest is reviewed"); a repository manifest violating a floor is a **validation error** (fail closed, D8), never a silent clamp; **`locked` is terminal** — no flag or policy layer converts it [D2]; the only path to a mode change is a **human PR editing `manifest.yml`** — deliberately the most visible diff in the repository; **machine-proposed policy PRs never carry a mode transition to `auto`** [ARCH §11.4; INV-4, INV-7].
- **F12-7 Autonomous background audit maturation** [ARCH §15.3, §7.2] — Loop B matures to autonomous scheduled operation with org-level aggregation of findings; cadence/cost tuning; still advisory-only [D7].
- **F12-8 Cross-repository documentation consistency** [ARCH §15.3; CTX §42] — org-wide drift reports and consistency surfaces (shared-concept/terminology drift, org dashboards, scheduled org reports, per-repo issues); findings surface but **never auto-write** [INV-11].
- **F12-9 Write-path invariant suite** [ARCH §15.3; INV-1] — fuzz/property tests asserting every automated change, in every mode and learning level, lands as branch → PR → human merge.
- **F12-10 Deferred-item decision gates** [ARCH §16.3] — documented, measurable promotion gates for: **integrated mode** (opt-in only after the trust loop is proven [ARCH §12.4]); **external graph DB / index cache** (optimization only, never truth [ARCH §3.7]); **model identity for Jev** — the exact decision-model binding is a per-repository calibration choice; the Jev *role* is fixed [D9, §16.3].
- **F12-11 Steady-state monitoring** [ARCH §9, §5.2; R11, R13] — spot-check rate and abstain-health continue in production; per-policy-version cost attribution dashboards [ARCH §10.6].

### 13.4 TODO list

- [ ] P12-T01 — Offline historical replay engine for shadow: candidate vs production over the eval + recent splits with identical-input guarantees [ARCH §8.6; D6; CTX §21]
- [ ] P12-T02 — Sampled live-shadow runner (fraction-configurable); shadow decisions recorded, never surfaced to developers [ARCH §8.6; D6]
- [ ] P12-T03 — Memoization keying by `(diff-subset, policy_version)` (+ model binding); shadow-isolation test — shadow never poisons the production cache [ARCH §3.5, §7.1]
- [ ] P12-T04 — Agreement/disagreement matrix computation (both-ignore / both-review / mixed cells) + disagreement→label resolution via reviewer outcomes [ARCH §8.6; CTX §21]
- [ ] P12-T05 — Canary lifecycle: time-boxing, auto-expiry of stale shadows, canary status tracked in `policy-history/` [ARCH §8.6; CTX §21]
- [ ] P12-T06 — Full gate enforcement: per-bucket metrics, abstention-quality + false-update no-regression, significance floor + min sample (anti-lucky-streak), `unsafe_auto_updates=0` [ARCH §8.6; R3]
- [ ] P12-T07 — `learning.mode=auto` path: all-gates-pass automatic promotion, application-owned, evidence-logged, one-op rollback; bypass-veto tests [ARCH §8.5, §8.9; CTX §24]
- [ ] P12-T08 — Adaptive-threshold calibration workflow: per-policy-version threshold evolution via new versioned artifacts only; time-split + rolling re-evaluation reports [D11; ARCH §5.3; R4]
- [ ] P12-T09 — Per-binding calibration re-check: a binding change failing calibration on the eval split cannot promote; `(policy_version, model binding)`-pinned confidence display [ARCH §10.8; D3]
- [ ] P12-T10 — Org routing-policy layer: declarative versioned bucket→model routing; changes ride the promotion pipeline, not code [ARCH §10.3; CTX §39]
- [ ] P12-T11 — Replacement-model shadow evaluation through the same canary machinery (model swap = policy change) [ARCH §10.3; R2; D9]
- [ ] P12-T12 — Provenance enforcement: decision/writer/verifier records name the actual routed model ids [ARCH §10.6]
- [ ] P12-T13 — Org floors full config schema + enforcement: floor definitions (mode ceilings, report-only-until-manifest-reviewed); violation ⇒ fail-closed validation error [ARCH §11.2, §11.3; D8]
- [ ] P12-T14 — Locked-terminal + human-only mode transitions: machine-proposed PRs containing a mode transition to `auto` are rejected by construction [ARCH §11.2, §11.4; INV-4]
- [ ] P12-T15 — Org-wide drift reports + cross-repo consistency surfaces (scheduled org report, per-repo issues) — advisory only [ARCH §15.3; CTX §42]
- [ ] P12-T16 — Loop B autonomous maturation: cadence/cost tuning, org-level aggregation of background findings [ARCH §7.2, §15.3]
- [ ] P12-T17 — Write-path invariant property suite across all modes/learning levels [ARCH §15.3; INV-1]
- [ ] P12-T18 — Deferred-item decision-gate documents: integrated-mode criteria (OD-R31); graph-DB evaluation criteria (optimization only, never truth); Jev model identity per-repo calibration note [D9; ARCH §3.7, §12.4, §16.3]
- [ ] P12-T19 — Steady-state monitoring: spot-check rate, abstain-health, per-policy-version cost attribution dashboards [ARCH §5.2, §9, §10.6; R11, R13]
- [ ] P12-T20 — Tests + docs (shadow/canary, auto promotion, routing, org floors, cross-repo reports) [OD-2]

### 13.5 Verification list

- [ ] V-P12-1 — Enabling shadow on N sampled PRs produces byte-identical developer-visible outputs vs shadow-off (zero-effect test) [D6; ARCH §8.6]
- [ ] V-P12-2 — The shadow harness asserts candidate and production received identical `ChangeAnalysis` + candidate sets (recorded input hashes equal) [ARCH §8.6]
- [ ] V-P12-3 — A disagreement matrix is produced for a fixture promotion; every cell is resolvable to a label once reviewer outcomes exist [ARCH §8.6; CTX §21]
- [ ] V-P12-4 — A candidate with `unsafe_auto_updates = 1` is rejected regardless of aggregate accuracy [ARCH §8.6; R3]
- [ ] V-P12-5 — Lucky-streak test: a candidate winning below the min-sample/significance floor is not promotable [ARCH §8.6; R3]
- [ ] V-P12-6 — A candidate regressing abstention quality or false-update rate is rejected even with an accuracy win (anti-gaming) [R3]
- [ ] V-P12-7 — A production memo-cache hit for a `(diff-subset, policy_version)` key never returns a shadow decision [ARCH §7.1]
- [ ] V-P12-8 — With all gates passing and `learning.mode=auto`, promotion occurs automatically; the event log contains full evaluation evidence; rollback restores the prior version in one command [ARCH §8.9; CTX §24]
- [ ] V-P12-9 — Bypass attempts (config forcing promotion with a failing gate; model output writing policy) are rejected by veto tests [ARCH §8.5; INV-7]
- [ ] V-P12-10 — An adaptive threshold change produces a new policy version only (vN is never mutated in place); the time-split report shows no historical overfit; rolling re-eval detects a synthetic drift fixture [D11; R4]
- [ ] V-P12-11 — A routing-policy change requires a shadow/canary evaluation report before promotion; the high-risk bucket demonstrably routes to the stronger model; provenance names the actual routed model [ARCH §10.3, §10.6; R2]
- [ ] V-P12-12 — A manifest with `docs/security/**` at `auto` under a floor capping `suggest` ⇒ exit 2 / check failure naming the floor; the report-only floor is enforced for unreviewed manifests [ARCH §11.2, §11.3; D8]
- [ ] V-P12-13 — A machine-proposed policy PR containing a mode transition to `auto` is rejected by the PR builder (unit test) [ARCH §11.4; INV-7]
- [ ] V-P12-14 — An org drift report is generated across ≥2 fixture repos; advisory findings only; no writes outside advisory artifacts [ARCH §15.3]
- [ ] V-P12-15 — The write-path property suite: every automated change lands on a `docsync/*` branch via PR across all modes and learning levels [ARCH §15.3; INV-1]

Gate: phase complete when shadow/canary runs productionized under D6 (offline-first + sampled live) with trustworthy gates, `learning.mode=auto` can promote only through every gate with evidence and one-op rollback, adaptive thresholds and model routing (incl. replacement models) ride the versioned-promotion rails, org floors are enforced fail-closed with `locked` terminal, cross-repo consistency surfaces exist read-only, and the write path remains branch → PR → human review in every test.

### 13.6 Risks, assumptions & decisions

Assigned gates:
- **R3 (canary metric gaming)** — full defenses: gate on abstention quality + false-update rate (not accuracy alone), per-bucket metrics, significance floor, time-boxed canaries; auto-expiry prevents zombie shadows [ARCH §16.2 R3, §8.6].
- **R4 (threshold overfitting to history)** — time-split evaluation + rolling re-evaluation; thresholds are always per-policy-version data (D11), never hard-coded [ARCH §16.2 R4, §5.3; D11].
- **R2 (model/prompt drift on OpenRouter upgrades)** — model swaps shadow/canary evaluated like policy changes; pinned model IDs in `models.yml` [ARCH §16.2 R2, §10.3; D9].
- **R11 (abstention pathologies)** — abstain-health monitoring continues in steady state (F12-11) [ARCH §16.2 R11, §5.2].
- **R13 (feedback selection bias)** — the spot-check rate continues over auto-applied patches; audit findings remain eval data [ARCH §16.2 R13, §9].
- **D6 honored** — shadow/canary primarily offline historical replay + sampled live shadow; dual-Jev on every PR explicitly rejected [D6; ARCH §8.6].

Decisions taken:
- Shadow decision caches keyed by `policy_version` (+ model binding); production memoization can never observe shadow results [ARCH §7.1].
- `learning.mode=auto` reuses the single promotion pipeline — no separate "fast path"; gates are code, not config [ARCH §8.9].
- Thresholds/calibration change only through new versioned policy artifacts (shape fixed, calibration evolvable) [D11; ARCH §5.3].
- Routing policies are declarative data in org/repo config, evaluated like policy changes [ARCH §10.3].
- Org floors live in org-owned configuration; violations are validation errors (fail closed), and `locked` remains terminal [ARCH §11.2, §11.3; D2, D8].
- Deferred items get explicit, measurable decision gates rather than silent carry-over [ARCH §16.3].

Alternatives considered:
- Dual-Jev on every PR (rejected: D6 — latency/CI budget; sampled live shadow instead) [D6].
- Separate shadow-infrastructure service (rejected: files-only, Git-native posture; replay runs in-process over run records) [OD-2].
- Org floors enforced as silent clamps (rejected: [ARCH §11.2] mandates validation errors so misconfiguration stays visible) [ARCH §11.2].
- Machine-proposed mode transitions to `auto` (rejected: [ARCH §11.2, §11.4] — only a human PR editing `manifest.yml` changes modes) [INV-4, INV-7].

> OPEN DECISION (OD-R29): live-shadow sample fraction + eligibility — default fraction and whether it applies only to repos with `learning.mode=canary|auto`. — Recommendation: default 10% of PRs in repos opted into canary/auto (opt-out per repo), capped per org; the fraction is versioned policy data.

> OPEN DECISION (OD-R30): org-floor storage + distribution — GitHub App org settings vs an org policy repository file consumed at run start. — Recommendation: an org policy repository file (reviewable in PRs — "policy must be reviewable in Git"); the App fetches + hash-pins it into run records so enforcement is auditable.

> OPEN DECISION (OD-R31): integrated-mode promotion criteria [ARCH §12.4, §16.3] — what evidence suffices to opt in. — Recommendation: ≥90 days in `auto` with ≥200 auto-applied patches, false-update rate < 2%, zero safety incidents, and org-floor review; remains opt-in per repo and never the default.

> OPEN DECISION (OD-R32): cross-repo report surface — org-level repository reports vs per-repo issues vs external dashboard. — Recommendation: a scheduled report (issue/PR) on the org policy repository; per-repo issues only for actionable findings; a standalone dashboard deferred until org adoption justifies it.

### 13.7 Architecture traceability

Covered: [ARCH §5.2] (abstain-health steady state), [ARCH §5.3] (matrix shape fixed, calibration evolves), [ARCH §7.1] (memoization separation), [ARCH §8.4] (rollback semantics under auto), [ARCH §8.5] (no path bypassing EVALUATION), [ARCH §8.6] (full), [ARCH §8.9] (auto enablement), [ARCH §9] (spot-checks, significance, objective in promotion), [ARCH §10.1/§10.3] (routing policies, replacement models), [ARCH §10.6] (actual-model provenance, cost attribution), [ARCH §10.8] (calibration per binding; D3 pinning), [ARCH §11.2] (full org floors; locked terminal), [ARCH §11.3] (floor validation errors), [ARCH §11.4] (no machine mode transitions), [ARCH §12.4] (integrated-mode deferral), [ARCH §15.3], [ARCH §16.2 R2, R3, R4, R11, R13], [ARCH §16.3] (integrated mode, graph DB, Jev identity); [CTX §21], [CTX §24], [CTX §39], [CTX §42]; [D2], [D3], [D6], [D8], [D9], [D11]. Invariants: [INV-1] (never push to the default branch — write path unchanged), [INV-4] (locked/generated/external never modified — floors), [INV-7] (models propose; humans promote — even in auto), [INV-9] (fail closed), [INV-11] (no flood — org reports rate-limited/idempotent). Principles: [PRIN-11] (confidence × risk — calibration within the fixed shape), [PRIN-13], [PRIN-14], [PRIN-15], [PRIN-16], [PRIN-19].

DEFERRED (explicit, with decision gates — [ARCH §16.3]):
- **Integrated mode** (doc commits in the code PR): opt-in only after the trust loop is proven; gate criteria in OD-R31; never the default [ARCH §12.4, §16.3].
- **External graph DB / index cache** for very large monorepos: optimization only, never truth; evaluation criteria documented; adoption must never make correctness depend on it [ARCH §3.7, §16.3].
- **Model identity for Jev**: the exact decision-model binding remains a per-repository calibration choice (D9); the Jev *role* (typed decision layer, CTX §5) is fixed — decision documented, no default binding imposed [D9; ARCH §16.3].

### 13.8 Traceability contributions

| Item type | ID | Phase | Where covered |
|---|---|---|---|
| CTX | §21 | 12 | §13.3 F12-1/F12-2; §13.4 P12-T01–T06; §13.5 V-P12-1–7 |
| CTX | §24 | 12 | §13.3 F12-3; §13.4 P12-T07; §13.5 V-P12-8/9 |
| CTX | §39 | 12 | §13.3 F12-5; §13.4 P12-T10/T11; §13.5 V-P12-11 |
| CTX | §42 | 12 | §13.1 + F12-6–F12-8 (V3 list item-by-item) |
| ARCH | §5.2 | 12 | §13.3 F12-11; §13.4 P12-T19 (steady-state abstain monitoring) |
| ARCH | §5.3 | 12 | §13.3 F12-4 (fixed shape, evolving calibration) |
| ARCH | §7.1 | 12 | §13.3 F12-1 (memoization separation); §13.5 V-P12-7 |
| ARCH | §8.4 | 12 | §13.3 F12-3 (rollback under auto); §13.5 V-P12-8 |
| ARCH | §8.5 | 12 | §13.3 F12-3; §13.5 V-P12-9 (EVALUATION veto) |
| ARCH | §8.6 | 12 | §13.3 F12-1/F12-2; §13.4 P12-T01–T06; §13.5 V-P12-1–7 |
| ARCH | §8.9 | 12 | §13.3 F12-3; §13.4 P12-T07; §13.5 V-P12-8/9 |
| ARCH | §9 | 12 | §13.3 F12-11; §13.4 P12-T19 (spot-checks, significance) |
| ARCH | §10.1/§10.3 | 12 | §13.3 F12-5; §13.4 P12-T10/T11; §13.5 V-P12-11 |
| ARCH | §10.6 | 12 | §13.4 P12-T12/T19 (actual-model provenance; cost attribution) |
| ARCH | §10.8 | 12 | §13.3 F12-4; §13.4 P12-T09; §13.5 V-P12-10/11 |
| ARCH | §11.2 | 12 | §13.3 F12-6; §13.4 P12-T13/T14; §13.5 V-P12-12/13 |
| ARCH | §11.3 | 12 | §13.3 F12-6 (floor validation errors, precedence) |
| ARCH | §11.4 | 12 | §13.4 P12-T14 (no machine mode transitions); §13.5 V-P12-13 |
| ARCH | §12.4 | 12 | §13.3 F12-10 + OD-R31 (integrated-mode deferral gate) |
| ARCH | §15.3 | 12 | §13.3 F12-6–F12-9; §13.5 V-P12-14/15 |
| ARCH | §16.2 R2 | 12 | §13.4 P12-T11; §13.5 V-P12-11 |
| ARCH | §16.2 R3 | 12 | §13.3 F12-2; §13.4 P12-T06; §13.5 V-P12-4/5/6 |
| ARCH | §16.2 R4 | 12 | §13.3 F12-4; §13.4 P12-T08; §13.5 V-P12-10 |
| ARCH | §16.2 R11 | 12 | §13.4 P12-T19 |
| ARCH | §16.2 R13 | 12 | §13.4 P12-T19 |
| ARCH | §16.3 | 12 | §13.3 F12-10; §13.4 P12-T18 (deferred gates: integrated mode, graph DB, Jev identity) |
| D | D2 | 12 | §13.3 F12-6 (locked terminal, ownership floor) |
| D | D3 | 12 | §13.3 F12-4; §13.4 P12-T09 (confidence pinned to policy_version + binding) |
| D | D6 | 12 | §13.3 F12-1; §13.5 V-P12-1/2 (offline-first + sampled live shadow) |
| D | D8 | 12 | §13.3 F12-6 (floor violation fail-closed); §13.5 V-P12-12 |
| D | D9 | 12 | §13.3 F12-5/F12-10 (model swap like policy change; Jev identity calibration) |
| D | D11 | 12 | §13.3 F12-4; §13.5 V-P12-10 (per-policy-version calibration) |
| INV | INV-1 | 12 | §13.3 F12-9; §13.5 V-P12-15 (write path unchanged) |
| INV | INV-4 | 12 | §13.3 F12-6; §13.5 V-P12-12/13 |
| INV | INV-7 | 12 | §13.3 F12-3/F12-6; §13.5 V-P12-9/13 |
| INV | INV-9 | 12 | §13.6 decisions (fail-closed floors/promotion) |
| INV | INV-11 | 12 | §13.3 F12-8 (org reports rate-limited, advisory) |
| PRIN | PRIN-11 | 12 | §13.3 F12-4 (confidence × risk calibration within the fixed matrix) |
| PRIN | PRIN-13 | 12 | §13.3 F12-4/F12-5 (versioned policy artifacts only) |
| PRIN | PRIN-14 | 12 | §13.3 F12-3 (proposals never mutate production policy) |
| PRIN | PRIN-15 | 12 | §13.3 F12-1 (offline evaluation before promotion) |
| PRIN | PRIN-16 | 12 | §13.3 F12-1 (shadow/canary before promotion) |
| PRIN | PRIN-19 | 12 | §13.3 F12-3 (application-owned promotion; deterministic orchestrator) |

---

## §14 Traceability & Coverage Matrices

Compiled from the phase sections' N.8 contribution tables plus the binding mappings in ARCH Appendix B. Every row lists item → phase(s) → where covered (section references use PLAN.md's §N numbering). CTX items with no build phase map to plan-meta with a note.

### 14.1 CTX §1–§47 → phase

| ID | Item (topic) | Phase(s) | Where covered |
|---|---|---|---|
| CTX §1 | Original product idea, workflow, PR-first | 6 | §7.1 objective (PR-first write path) |
| CTX §2 | Ecosystem / research context + references | — (plan-meta) | Background research only; no build impact. Noted here for 100% coverage; competitors/links live in ARCH §1.2, not in any phase deliverable |
| CTX §3 | Core product thesis + question chain + positioning | — (plan-meta) | Thesis realized across §1–§13; the §1.3 question chain maps to the pipeline built in Phases 1–5 (§2–§6); no separate deliverable |
| CTX §4 | Central design principle + division of knowledge | — (plan-meta) | Enforced as the invariant system (Phases 0–9; §14.3) and the orchestrator/LLM split (Phase 5, §6.3); no separate deliverable |
| CTX §5 | Jev + Sonnet model split, workspace, duties, tripartite principle | 2, 3, 4, 5 | §3.3 (Jev runtime), §4.3 (workspace, five duties), §5.1 (Stage 3), §6.3 (stage integration) |
| CTX §6 | Three-layer intelligence pipeline | 2, 4, 5 | §3.3 (Stage 1), §5.1 (Stage 3), §6.3 (sequencing) |
| CTX §7 | High-level production architecture | 5 | §6.3 full architecture realized E2E (§6.5 V-P5-2) |
| CTX §8 | Deterministic pre-filtering + retrieval hierarchy + embeddings fallback | 1, 11 | §2.3 analyzer + retrieval; §12.3 levels 3–4 fed (F11-2/F11-5) |
| CTX §9 | Repository-scoped manifest + `.docsync/` + modes | 0 | §1.3 manifest/allowlist/modes; §1.4 P0-T04/T05; §1.5 V-P0-4 |
| CTX §10 | Bidirectional code ↔ concepts ↔ docs mapping | 1, 11 | §2.3 document graph; §12.3 graph upgrade (F11-6) |
| CTX §11 | Documentation as claims (+ invalidation, headline query) | 1, 4, 11 | §2.3 claims; §5.3 claims as verification unit; §2.5 V-P1-2 deterministic mapping-based invalidation (V1); §12.3 F11-4/F11-9 type-aware claim-level invalidation + headline query (Phase 11) |
| CTX §12 | Documentation contracts (+ classification) | 1, 4, 11 | §2.3 contracts; §5.3 contract checking; §12.3 F11-6 |
| CTX §13 | Policy states + abstain path | 2, 4, 5, 7 | §3.3/§3.4 (abstain semantics), §5.6 (data points), §6.3 (first-class outcome), §8.3 (visibility) |
| CTX §14 | Confidence × risk matrix + high-risk list | 2 | §3.3 matrix; §3.5 matrix tests |
| CTX §15 | Repository-specific Jev policies | 2 | §3.3/§3.4 universal + additive questions |
| CTX §16 | Policy Architect (+ 14/23 example) | 10 | §11.3 F10-11; §11.4 P10-T14; §11.5 V-P10-11 |
| CTX §17 | Policy compilation | 2 | §3.3/§3.4 compiler |
| CTX §18 | Example Jev policy structure (0.94/0.72) | 2 | §3.3 policy schema shape; thresholds as data (D11) |
| CTX §19 | No direct policy mutation | 10 | §11.3 F10-12/F10-13; §11.4 P10-T16/T18 |
| CTX §20 | Versioned policies (v17, canary) | 2, 10 | §3.3 versioning; §11.3 F10-13 promotion pipeline |
| CTX §21 | Shadow/canary | 10, 12 | §11.3 F10-12 (offline gate machinery foundation; full → Phase 12); §13.3 F12-1/F12-2 (productionization) |
| CTX §22 | Closed-loop learning (FN/FP) | 6, 10 | §7.3 FN/FP capture; §11.3 F10-1/F10-3 recorder |
| CTX §23 | Reviewer controls (5 actions) | 6, 10 | §7.3 five reviewer actions; §11.3 F10-1/F10-4 |
| CTX §24 | Self-improvement modes (off/propose/canary/auto) | 10, 12 | §11.3 F10-16 (`learning.mode` governance); §13.3 F12-3 (auto enablement) |
| CTX §25 | Persistent memory (layout + generated-sdk record) | 0, 10 | §1.3 memory layout reconciled by D1; §11.3 F10-15 memory subsystem |
| CTX §26 | Two loops (Loop A / Loop B) | 5, 11 | §6.3 Loop A E2E (Loop B interface only); §12.3 F11-10 Loop B audit |
| CTX §27 | Baselines (invalidation flow) | 1 | §2.3 baselines; §2.4 P1-T15 |
| CTX §28 | Documentation state machine | 1 | §2.3 state machine; §2.4 P1-T17 |
| CTX §29 | Patch strategy (minimal patch, no rewrites) | 3 | §4.3/§4.5 (DocPatch, structural rewrite rejection) |
| CTX §30 | Evidence/provenance record | 3, 4, 6, 7, 8, 9 | §4.3/§4.4 provenance capture; §5.5 evidence records; §7.3 rendering; §8.3 run-id records; §9.3 PR-body summary; §10.3 schema (primary) |
| CTX §31 | Generated documentation protection (5 categories) | 0, 9 | §1.3 ownership categories; §10.3 ownership categories in lock enforcement |
| CTX §32 | CLI check/fix/pr (+ transcripts) | 0, 7, 8 | §1.3 config diagnostics API (consumer prep); §8.3 verbs (primary); §9.3 exit-code/visual-language mirror |
| CTX §33 | CI/GitHub integration (+ check UI) | 8 | §9.1/§9.3 check UI, App/Action, escalation (primary) |
| CTX §34 | Separate documentation PR (5 reasons) | 6, 7, 8 | §7.3 separate doc PR; §8.3 pr conventions; §9.3 consumption |
| CTX §35 | Evaluation framework (6 metrics) | 10 | §11.3 F10-5/F10-7; §11.4 P10-T07/T10 |
| CTX §36 | Policy improvement objective (0.40/0.30/0.20/0.10) | 10 | §11.3 F10-10; §11.4 P10-T13 |
| CTX §37 | Orchestrator philosophy (three teams; research vs production) | 5, 9 — (plan-meta note) | §6.6 production vs research orchestrator distinction; §10.3/§10.7 determinism; methodology itself is ARCH Appendix A (plan-meta) |
| CTX §38 | Orchestrator vs LLM responsibility | 0, 5, 8, 9 | §1.3 write-boundary guards; §6.3 orchestrator owns permissions/files/thresholds; §9.3 tokens never reach models; §10.3 LLM never root authority |
| CTX §39 | Model routing pluggable | 2, 12 | §3.3/§3.6 pluggable roles; §13.3 F12-5 org routing policies |
| CTX §40 | Recommended V1 (components + flow) | 0–10 | §0.4 mapping; delivered across Phases 0–10 (§1–§11) |
| CTX §41 | Recommended V2 | 11 | §12.1 + F11-1–F11-9 (item-by-item) |
| CTX §42 | Recommended V3 | 12 | §13.1 + F12-6–F12-8 (item-by-item) |
| CTX §43 | Product moat / differentiation | — (plan-meta) | Product strategy (ARCH §1.4); no build phase; the moat infrastructure (policy, graph, claims, feedback) is built by Phases 0–10 |
| CTX §44 | Key strategic insight + other outputs | — (plan-meta) | Product strategy (ARCH §1.4); non-Markdown outputs (drift reports, health reports) arrive with Loop B (Phase 11) and cross-repo surfaces (Phase 12) |
| CTX §45 | Preferred ultimate architecture (full diagram) | 5 — (plan-meta note) | §6.3 realizes the production pipeline per ARCH §3.6; the full two-loop, all-stage diagram is the architectural end-state, reached progressively (Loop B → Phase 11) |
| CTX §46 | Final design principles (20) | — (plan-meta) | Traced as principles P1–P20 in §14.6, enforced by the phases listed there |
| CTX §47 | One-sentence product definition | — (plan-meta) | Restated in §0.1; no separate deliverable |

### 14.2 Decisions D1–D14 → phase

| ID | Decision | Phase(s) | Where covered |
|---|---|---|---|
| D1 | Canonical `.docsync/` layout | 0, 10 | §1.3 layout/loader (V-P0-3); §11.3 F10-15 memory = exactly 4 files (V-P10-14) |
| D2 | Ownership is the floor | 0, 2, 3, 4, 6, 9, 12 | §1.5 V-P0-5; §3.3 locked→blocked; §4.3/§4.5 protected read-only; §5.5 locked untouched; §7.3 layer-4 rejection; §10.3 four-layer enforcement; §13.3 F12-6 |
| D3 | Displayed confidence pinned to (policy_version, model binding) | 2, 4, 7, 8, 12 | §3.3/§3.6 pinning; §5.5 report pinning; §8.3 renderer; §9.3 check surfaces; §13.3 F12-4 (re-pinning) |
| D4 | One doc PR per source PR + partial-pass | 6, 8 | §7.3/§7.5 (primary); §9.3 CI consumption |
| D5 | Provenance precedence (PR body canonical) | 6, 8, 9 | §7.3 rendering; §9.3 PR-body summary; §10.3 D5 end-to-end (primary) |
| D6 | Shadow/canary primarily offline + sampled live | 2, 5, 10, 12 | §3.4 shadow memoization keying; §6.3 keying interface; §11.6 (deferral to 12); §13.3 F12-1 (productionization) |
| D7 | Manifest is an allowlist | 0, 7, 8, 11 | §1.5 V-P0-4; §8.3 unknown coverage; §9.3 consumption; §12.3 F11-10 (Loop B respects allowlist) |
| D8 | Fail-closed configuration | 0, 7, 8, 9, 12 | §1.5 V-P0-8; §8.3 exit 2; §9.5 config-error surface; §10.5 degradation tests; §13.3 F12-6 floor violations |
| D9 | All AI inference through OpenRouter | 2, 12 | §3.3/§3.6 single gateway; §13.3 F12-5 routing + model-swap discipline |
| D10 | Advisory (neutral) by default | 5, 7, 8 | §6.5 V-P5-7 outage posture; §8.6 advisory posture + `--strict` opt-in; §9.5 neutral-default conclusion mapping |
| D11 | Thresholds are per-policy-version calibrated data | 2, 12 | §3.3/§3.4 (bootstrap policy v1, calibration); §13.3 F12-4 adaptive thresholds |
| D12 | Baseline resolution ladder | 1, 6, 8 | §2.5 V-P1-9 ladder sims; §7.3 merge-queue re-resolution trigger; §9.3 post-merge D12 re-resolution |
| D13 | Verification never self-approves | 4, 5, 9 | §5.3–§5.6 judge independence (primary); §6.3 orchestration-level assertion; §10.3 re-asserted in suite |
| D14 | Failure → inconclusive; degrade to less automation | 1, 5, 7, 8, 9, 11 | §2.5 V-P1-10; §6.3 degradation matrix; §8.3 exit 3; §9.5 inconclusive surfaces; §10.5 aggregate less-automation assertion; §12.3 F11-3/F11-11 declared degradation |

### 14.3 Safety invariants 1–13 → phase

| ID | Invariant | Phase(s) | Where covered |
|---|---|---|---|
| INV-1 | Never push to the default branch | 6, 7, 8, 9, 12 | §7.3 branch factory guard; §8.3 pr ban; §9.5 E2E assertion; §10.4/§10.5 Git-layer test; §13.3 F12-9 write-path suite |
| INV-2 | Never modify code files | 3, 6, 7, 9 | §4.3/§4.5 guard; §7.3/§7.5 PR filter; §8.3/§8.5 write scope; §10.4/§10.5 L3/L4 tests |
| INV-3 | Never touch files outside the manifest allowlist | 3, 6, 7, 8, 9 | §4.3/§4.5; §7.3/§7.5 PR filter; §8.3/§8.5; §9.3 permission-level guarantee (no Workflows grant); §10.4/§10.5 allowlist tests |
| INV-4 | Locked/generated/external detected, never modified | 6, 7, 9, 12 | §7.3 whole-patch rejection; §8.3/§8.5 locked blocked; §10.3 four-layer suite; §13.3 F12-6 floors |
| INV-5 | No patch ships without verification | 4, 5, 7, 9 | §5.1/§5.5 three-tier gate; §6.4 P5-T07; §8.3 verified-patch-only apply; §10.4/§10.5 INV-5 test |
| INV-6 | Abstain, don't guess | 2, 4, 5, 7, 9, 11 | §3.3/§3.5 abstain semantics; §5.6 abstention data points; §6.3/§6.4 P5-T08 first-class outcome; §8.3 visibility; §10.5 INV-6 test; §12.3 F11-11 uncertain → REVIEW |
| INV-7 | Models propose; humans promote | 6, 9, 10, 11, 12 | §7.3 machine-PR guard; §10.4/§10.5 boundary test; §11.3 F10-13/F10-16; §12.3 F11-6 mapping proposals; §13.3 F12-3/F12-6 |
| INV-8 | Models never hold credentials | 2, 3, 5, 6, 7, 8, 9 | §3.3/§3.4 env-only key; §4.3 app-owned application; §6.3/§6.5 credential isolation; §7.3 credential-free PR layer; §8.3/§8.5 secret scan; §9.3/§9.5 gateway boundary; §10.5 INV-8 test |
| INV-9 | Fail closed | 5, 7, 9, 10, 12 | §6.5 V-P5-7/8; §8.3/§8.5 fail-closed exits; §10.5 degradation matrix (primary); §11.6 fail-closed promotion; §13.6 fail-closed floors/promotion |
| INV-10 | Provenance or it didn't happen | 6, 7, 8, 9 | §7.3/§7.5 provenance gate (primary); §8.3 run-id records; §9.3 summary rendering; §10.4/§10.5 refusal test |
| INV-11 | No flood | 6, 7, 8, 9, 12 | §7.3/§7.5 rate/burst (primary); §8.3 governor participation; §9.3/§9.5 CI-side limits; §10.5 re-asserted; §13.3 F12-8 org reports rate-limited |
| INV-12 | Whole-document rewrites rejected | 3, 4, 9 | §4.3/§4.5 structural rejection; §5.3/§5.5 verification rejection; §10.4/§10.5 INV-12 test |
| INV-13 | Anchors don't silently move | 1, 3, 4, 9, 11 | §2.5 V-P1-7; §4.3/§4.5 ambiguous-anchor abstain; §5.3 unanchored gating; §10.4/§10.5 UNANCHORED test; §12.3 F11-7 extraction anchoring |

### 14.4 Assumptions A1–A10 → verifying phase + gate type

| ID | Assumption | Verifying phase(s) | Gate type |
|---|---|---|---|
| A1 | Jev returns calibrated probabilities ≲1 s via OpenRouter | 2 | Calibration harness: ECE/reliability + latency budget per (policy_version, model binding) gates promotion (§3.5) |
| A2 | Writer reliably returns minimal anchored patches with evidence | 3 (interface + stubs), 10 (full execution) | Historical-PR replay benchmark measuring rewrite tendency + evidence quality (§4.5/§4.6; executed with the Phase 10 evaluation framework) |
| A3 | Historical PRs yield a usable labeled benchmark incl. true negatives | 10 | Dataset-yield study on pilot repos; fallback: hand-labeled seed set + synthetic fixtures (§11.5 V-P10-5) |
| A4 | Claim extraction + anchoring works on representative docs | 11 | Extractor-quality trial on representative docs; fallback: section-level checking, declared (§12.5 V-P11-5/6) |
| A5 | "Externally observable behavior changed" is deterministically approximable | 11 | Behavioral-proxy coverage audit; per-language AST coverage report; recall gaps declared (§12.5 V-P11-11) |
| A6 | `check_run.requested_action` button works cleanly | 8 | Platform spike executed before check-UI implementation (entry gate §9.2); no-go → comment-command fallback per OD-R18 |
| A7 | `GITHUB_TOKEN`/fork semantics as expected | 8 | Platform spike; results binding for Action README + fork-PR behavior (§9.2) |
| A8 | Teams accept committed `.docsync/state/` | 0 (instrumentation), 10 (decision point) | Churn counters in run records (§1.5 V-P0-14) + pilot-repo trial evidence; keep-committed vs CI-only decision record (§11.5 V-P10-16) |
| A9 | Minimal-diff application survives real-world Markdown | 3, 6, 7 | 3-way-merge trials with adversarial fixtures (front-matter, fences, lists, renames); refuse-and-rerun backstop (§4.5, §7.5, §8.5) |
| A10 | Latency envelope p50 ≤ ~60 s routed holds | 5 | Load probe on large-diff fixtures against real PR sizes + modeled OpenRouter latencies; breach exercises §3.7 downgrade (§6.5 V-P5-15) |

### 14.5 Risks R1–R18 → mitigating phase(s)

| ID | Risk | Phase(s) | Where covered |
|---|---|---|---|
| R1 | Miscalibrated high confidence feeding `auto` | 2 | §3.6 A1 harness gate + `unsafe_auto_updates = 0` gate struct + conservative-max risk |
| R2 | Model/prompt drift on OpenRouter upgrades | 2, 12 | §3.6 pinned IDs + swap-as-policy-change discipline; §13.3 F12-5 shadow-evaluated swaps |
| R3 | Canary metric gaming | 10, 12 | §11.3 F10-14 gate library (no-regression, unsafe-zero, significance floor); §13.3 F12-2 full defenses (per-bucket, time-boxed canaries) |
| R4 | Threshold overfitting to history | 2, 12 | §3.6 thresholds as policy-version data; §13.3 F12-4 time-split + rolling re-evaluation |
| R5 | Verifier false-rejects poisoning feedback | 4, 10 | §5.5/§5.6 disagreement tracking; §11.4 P10-T24 own-metric labeling |
| R6 | Doc PR ↔ code PR conflicts; stale doc PRs | 5, 6, 7, 8 | §6.6 interface (outcome record + idempotency key); §7.3 supersede/merge-order (primary); §8.6 local pr participation; §9.3 CI-side |
| R7 | Check-run UX noise | 8 | §9.3/§9.5 single aggregated check, quiet mode, sparse rows, neutral default |
| R8 | Prompt injection via repository content | 5, 9 | §6.4 P5-T16 containment at orchestrator boundary; §10.3/§10.4 defense-in-depth + adversarial corpus (primary) |
| R9 | Baseline orphaning on squash/force-push | 1 | §2.4 P1-T16 D12 ladder + simulators; §2.5 V-P1-9 |
| R10 | Generated-file churn / regeneration loops | 1 | §2.4 P1-T03/T04 short-circuits + dedup hook; §2.5 V-P1-3 |
| R11 | Abstention pathologies | 2, 4, 10, 12 | §3.3 2-D health metric; §5.6 abstention data points; §11.3 F10-8 monitor; §13.3 F12-11 steady state |
| R12 | Memory poisoning / loop collapse | 10 | §11.3 F10-15 evidence-backed records, near-floor confidence, contradiction decay, offline-eval wall |
| R13 | Feedback selection bias | 10, 12 | §11.3 F10-9 spot-check sampler; §13.3 F12-11 continues in production |
| R14 | Manifest drift (rotting globs) | 0 | §1.4 P0-T11 manifest lint (library; CI scheduling Phase 8) |
| R15 | Multi-writer state races | 0, 1, 5, 6, 7, 9 | §1.5 V-P0-11 CAS core; §2.5 V-P1-11; §6.5 V-P5-12; §7.5 race test; §8.5 CAS race; §10.3 archival CAS |
| R16 | Cross-patch inconsistency | 3, 4 | §4.3 claim-value export (groundwork); §5.3/§5.5 same-claim-different-value detection (completes) |
| R17 | Trigger-taxonomy drift | 0, 2 | §1.5 V-P0-9 versioned mapping layer; §3.3/§3.4 compiler consumption |
| R18 | Confidence-display trust erosion | 2, 7, 8 | §3.6 pinning data; §8.3 renderer pinning; §9.3 check-surface pinning |

### 14.6 Design principles 1–20 [ARCH §2.3] → phase

| ID | Principle | Phase(s) | Where covered |
|---|---|---|---|
| P1 | Project-scoped by default | 7 | §8.1 repo-local scope |
| P2 | Do not scan every Markdown file | 7, 11 | §8.3 manifest-scoped check + D7 surfacing; §12.3 F11-10 chunked per document |
| P3 | Deterministic evidence before AI | 1, 11 | §2.3 zero model calls in analysis; §12.3 F11-1–F11-5 |
| P4 | Jev for structured decisioning | 2 | §3.3 Jev runtime |
| P5 | Sonnet for investigation + patch | 3 | §4.1–§4.3 writer layer |
| P6 | Docs as claims backed by sources | 4, 11 | §5.3/§5.4; §12.3 F11-7/F11-9 |
| P7 | Explicit code↔concept↔doc relationships | 1, 11 | §2.3 document graph; §12.3 F11-6 |
| P8 | Per-document mutability/ownership modes | 0, 9 | §1.3 modes honored in resolution; §10.3 enforcement |
| P9 | Never unrestricted model write authority | 3, 9 | §4.3/§4.5; §10.3 |
| P10 | Minimal diffs, not rewrites | 3, 9 | §4.3–§4.5; §10.5 INV-12 test |
| P11 | Confidence × risk | 2, 12 | §3.3/§3.5; §13.3 F12-4 (calibration within fixed shape) |
| P12 | Explicit abstain/review path | 5, 7, 8, 9 | §6.3 surfaced; §8.3 visibility; §9.3 check UI; §10.5 INV-6 |
| P13 | Version the decision policy | 2, 10, 12 | §3.3/§3.4; §11.3 F10-13; §13.3 F12-4/F12-5 |
| P14 | Sonnet proposes policy, never mutates | 6, 9, 10, 12 | §7.3 overrides → proposals; §10.5 INV-7; §11.3 F10-11/F10-13; §13.3 F12-3 |
| P15 | Evaluate candidate policies offline | 10, 12 | §11.3 F10-12; §13.3 F12-1 |
| P16 | Shadow/canary before promotion | 10, 12 | §11.3 F10-12 (foundation); §13.3 F12-1 (full) |
| P17 | Learn from reviewer corrections | 6, 10 | §7.3 correction capture; §11.3 F10-1–F10-4 |
| P18 | Evidence-backed, prunable memory | 1, 10, 11 | §2.3 evidence-backed records; §11.3 F10-15; §12.3 F11-12 |
| P19 | Production orchestrator mostly deterministic | 0, 5, 6, 7, 8, 9 | §1.3 deterministic config core; §6.3 orchestrator; §7.3/§7.5 PR path; §8.1 CLI behind orchestrator; §9.3 orchestrator performs writes; §10.3 enforcement |
| P20 | Correctness over edit volume | 4, 7, 10 | §5.1/§5.5; §8.1 reporting; §11.3 F10-10 objective |

### 14.7 Glossary terms (ARCH Appendix C) → owning phase/component

| Term | Owning phase / component | Notes |
|---|---|---|
| Baseline | Phase 1 (`@docsync/core` state/baseline store) | Consumed by Phases 5, 6, 11 |
| Claim | Phase 1 (claim store, anchors) | Verification consumes in Phase 4; extraction in Phase 11 |
| Contract | Phase 1 (declarations + classification vectors) | Consumed by Phases 2, 4 |
| Constrained workspace | Phase 3 (assembly engine) | Orchestrated in Phase 5 |
| Document graph | Phase 1 (mapping records) | Upgraded in Phase 11 |
| DocPatch | Phase 3 (schema in `@docsync/schemas`; application engine) | Applied via Phases 6–7 |
| Jev | Phase 2 (runtime; role binding via `@docsync/openrouter`) | Sequenced by Phase 5 |
| Loop A / Loop B | Phase 5 (Loop A E2E; Loop B records groundwork) | Loop B operational in Phase 11; matures Phase 12 |
| Manifest | Phase 0 (schema, loader, validation) | Consumed everywhere |
| Policy Architect | Phase 10 (runner) | Role bound in `models.yml` (Phase 2) |
| Policy Compiler | Phase 2 (`@docsync/policy`) | Consumed by Phase 10 proposals |
| Provenance | Phase 9 (`docsync.provenance/v1`, D5 precedence — primary) | Rendered in Phases 6–8; schema drafted Phase 0 |
| Sonnet | Phase 3 (`patch-writer` role implementation) | Role interface defined Phase 2 |
| Shadow/canary | Phase 12 (productionized under D6) | Gate machinery foundation Phase 10 |
| State machine | Phase 1 (core transitions) | Orchestrated via Phase 5; consumed by Phases 6, 8 |

---

## §15 Global Verification & Release Checklist

### 15.1 Cross-phase integration checks

The full-system E2E contract assembled across Phases 0–12, verified on fixture repositories and (for Phases 8+) recorded GitHub API/webhook replay:

- [ ] E2E-1 — PR opened → deterministic analysis (Phase 1) → Stage-1 check posted (decision + candidates + confidence, pinned per D3) → patch button click (Phase 8) → writer → three-tier verification (Phase 4) → exactly one doc PR aggregating all candidates (D4) with provenance body (D5) and `state/` carriage (OD-R12).
- [ ] E2E-2 — Reviewer actions on the doc PR (accept / edit / reject-with-reason / mark-as-intentional / always-ignore) produce correctly hygiene-normalized feedback rows (Phase 6 capture → Phase 10 recorder) carrying `policy_version` + `decision_id` + signal type.
- [ ] E2E-3 — Feedback → dataset → six-metric evaluation → Policy Architect proposal → offline evaluation → human-approved promotion into `policy-history/` runs end-to-end on fixture data (Phase 10).
- [ ] E2E-4 — Deterministic replay: any historical run key `(repo, base_sha, head_sha, manifest_version, policy_version)` re-executed produces the identical decision trail from committed `.docsync/state/` alone (Phases 0/5; audit replay tool, Phase 9).
- [ ] E2E-5 — Idempotent re-runs: same idempotency key (source PR head SHA + doc-set content hash) updates the existing check/doc PR; zero duplicates across consecutive runs; CAS races lose gracefully and re-run (Phases 5–8).
- [ ] E2E-6 — Degradation coherence: every §3.4 failure mode (analyzer crash, Jev unavailability, writer timeout, verification failure, OpenRouter outage) ends in less automation on every surface — CLI exit 1/2/3 semantics, `neutral`/inconclusive checks, findings without patches (Phases 5, 7, 8, 9).
- [ ] E2E-7 — Surface fact parity: CLI text, check-run markdown, and PR-body provenance are renders of the same canonical report object / provenance record — local and CI never disagree about the facts (Phases 5, 7, 8, 9).
- [ ] E2E-8 — Config failure coherence: a broken manifest yields exit 2 / check `failure` titled `docsync: configuration error` with line-precise errors and zero AI-stage invocations on every surface (Phases 0, 7, 8).
- [ ] E2E-9 — Write-path coherence: every automated change in every mode and learning level lands as `docsync/*` branch → PR → human merge; never the default branch, never code files, never out-of-allowlist paths (Phases 6–9, 12).

### 15.2 V1 release gate

- [ ] All phase gates 0–10 (§1.5–§11.5 Gate lines) green.
- [ ] All 13 safety invariants [ARCH §14] enforced by executable tests — Phase 9 suite green, plus the per-phase invariant assertions (§10.5 inventory table).
- [ ] Assumption status recorded: A1 (calibration gate), A2 (benchmark executed), A3 (dataset-yield verdict), A6/A7 (spike write-ups), A8 (decision record), A9 (3-way-merge trials), A10 (load probe) — each with verdict + fallback disposition.
- [ ] Report-only-by-default posture confirmed (D10): check conclusion `neutral` by default; `failure` only on opt-in; no patch generation without the escalation click (auto-proposal default-off per OD-R20).
- [ ] `docsync check / fix / pr` CLI complete: fixture matrix green, exit codes 0/1/2/3 correct, `fix` idempotent + provenance-recorded, shared renderer parity with CI (Phase 7 gate).
- [ ] One-doc-PR-per-source-PR (D4) + rate limits active: open-PR cap (default 3), hourly burst limit (default 10), concurrency-group supersedence, self-trigger suppression, 429 backoff-to-report-only (Phases 6, 8).
- [ ] Provenance required end-to-end: no doc PR without a `docsync.provenance/v1` record; archival store content-addressed, append-only; secret-scan gate green (Phase 9).
- [ ] Feedback loop live from day one: the five reviewer actions captured with the first patches and flowing into the Phase 10 recorder [ARCH §15.1].

### 15.3 V2 gate (Phase 11)

- [ ] Phase 11 gate (§12.5) green: symbol/AST analysis first-class for TypeScript with declared coverage gaps elsewhere; claims beyond hand-anchoring exist only as source-verified proposals; the headline claim-support query works on fixtures; Loop B produces deduplicated advisory findings reusing the Loop-A verification/policy path; A4/A5 trial verdicts recorded.
- [ ] Shadow/canary gates live (D6): the Phase 10 gate library is in active use for every V2 policy artifact (claim-extractor bindings, analyzer routing changes); `unsafe_auto_updates = 0` maintained through every promotion.
- [ ] Developer-facing primitives unchanged from V1 — still checks, diffs, PRs; no new write paths (INV-1 re-asserted).

### 15.4 V3 gate (Phase 12)

- [ ] Phase 12 gate (§13.5) green: shadow/canary productionized under D6 (offline-first + sampled live shadow, zero-effect test on developer-visible outputs); `learning.mode=auto` promotes only through every gate with evidence and one-op rollback; adaptive thresholds and model routing ride the versioned-promotion rails; org floors enforced fail-closed with `locked` terminal; cross-repo consistency surfaces exist read-only.
- [ ] `unsafe_auto_updates = 0` maintained as a non-promotable gate metric (unsafe_auto_updates remains 0 in every promoted policy version — verified across the full `policy-history/`).
- [ ] Org floors enforced (D2/D8): floor violations are validation errors; machine-proposed PRs never carry mode transitions to `auto`; write-path property suite green across all modes and learning levels.
- [ ] Deferred-item decision gates documented with measurable criteria (OD-R31 integrated mode; graph DB; Jev model identity) — none activated without their gates.

### 15.5 Ongoing verification cadence

- [ ] Calibration monitoring: per (policy_version, model binding) ECE/reliability tracked; any binding change re-gated before promotion (Phases 2, 12).
- [ ] Abstain/override 2-D health metric: abstain-rate × human-override-rate monitored per policy version with alert thresholds; drift triggers a Policy-Architect proposal, never a direct policy edit (Phases 10, 12).
- [ ] Spot-check rate: 10% (min 1 per 50) of auto-applied patches audited per OD-R25; findings ingested as evaluation labels; rate itself is versioned policy data (Phases 10, 12).
- [ ] Manifest lint: scheduled lint for zero-match globs, dead entries, coverage defects (library from Phase 0; CI-scheduled from Phase 8 onward) — findings annotate and are actionable via normal PRs [R14].
- [ ] Promotion log review: every promotion event carries its full evaluation evidence; rollback drills (one-op restore) exercised per release.

