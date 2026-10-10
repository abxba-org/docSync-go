# AGENTS.md — docSync Development Session Guide

> **Purpose:** This file is the entry point for every AI developing session on docSync.
> It tells you what this project is, what is binding, how to work, and how to keep
> `BACKLOG.md` and `SESSIONS.md` in sync so any future session can pick up cleanly.

---

## 0. Behavioral guidance — read `AGENT-behavior.md` first (mandatory)

- Before any substantive work, read **`AGENT-behavior.md`** (repo root) and apply its
  behavioral rules for the entire session — delegation sizing, persistence discipline,
  verify-on-disk honesty, failure recovery, speed-vs-quality tradeoffs, traceability.
- When you discover a NEW, generalizable behavioral lesson during execution (a failure,
  a repeated mistake, a user correction), update `AGENT-behavior.md` using its
  maintenance protocol (Observe → Diagnose → Generalize → Add/Refine → Apply → Verify).
  Record only reusable lessons; refine existing rules instead of accumulating one-offs.
- This file (`AGENTS.md`) stays focused on the project's role, decisions, and process;
  learned behavioral guidance lives in `AGENT-behavior.md`.

---

## 1. What is docSync?

A **Git-native documentation consistency engine** (NOT an AI docs generator):
it detects when code changes make documentation stale, identifies *only* the relevant
documentation (manifest allowlist), proposes *minimal* evidence-backed patches, verifies
them, and ships them through the normal Git/PR workflow. Every repository develops its
own decision policy over time (repository-specific, self-calibrating documentation
policy engine).

**The one-sentence definition (binding):**

> A Git-native documentation consistency engine that builds a repository-specific
> decision policy, detects documentation drift from code changes, uses Jev for
> structured routing and Claude Sonnet (patch-writer role) for evidence-backed minimal
> patches, and continuously improves its policy through evaluated feedback and safe
> versioned promotion.

---

## 2. Source-of-truth documents (READ FIRST)

| File | Role |
|---|---|
| `docs/docSync_CONTEXT.md` | Original 47-section working context (product thesis, decisions) |
| `docs/ARCHITECTURE.md` | **Decision-ready v1.0 architecture** — binding design; 14 decisions (D1–D14), 20 principles, 13 safety invariants |
| `docs/PLAN.md` | **Decision-ready v1.0 implementation plan** — Phases 0–12, per-phase TODOs (P#-T##), verification gates (V-P#-##), OD-R01–OD-R32 decision registry |
| `BACKLOG.md` | Granular feature/checkbox queue — the working tracker |
| `SESSIONS.md` | Memory sync log — handoff notes between sessions |
| `AGENT-behavior.md` | Learned behavioral rules for agents (living file — read first, update on new lessons) |

Precedence: CONTEXT < ARCHITECTURE < PLAN for *how to build*; ARCHITECTURE §0.3 (D1–D14)
and §14 (13 invariants) are **binding non-negotiables**. Never implement anything that
contradicts them; if a conflict is discovered, stop and surface it instead of improvising.

---

## 3. Non-negotiables (violating any of these is a bug)

Core principle: **Git owns the documentation. AI proposes changes to it. The
application controls whether AI is allowed to make those changes.**

1. Never push to the default branch — writes go to `docsync/*` branches → PRs.
2. Never modify code files — only manifest-listed docs in writable modes.
3. Never touch files outside the manifest allowlist (incl. `.docsync/` internals,
   `.github/workflows/**`).
4. Locked / generated / external docs: detected and reported, never modified
   (ownership is the floor — locked resolves `blocked` at ANY confidence).
5. No patch applied or shipped without passing three-tier verification.
6. Abstain, don't guess — uncertain routes to REVIEW, never forced into update/ignore.
7. Models propose; humans promote — no path from model output to production policy.
8. Models never hold credentials — `OPENROUTER_API_KEY` env-only, orchestrator writes.
9. Fail closed — degrade to less automation, never more (analyzer failure is
   *inconclusive*, never "no docs affected").
10. Provenance or it didn't happen — every automated change carries its evidence record.
11. No flood — PR creation rate-limited, idempotent, self-trigger-suppressed.
12. Whole-document rewrites are rejected at verification — minimal diffs only.
13. Anchors don't silently move — ambiguous claim re-anchoring → `UNANCHORED` → REVIEW.

Also binding: Jev decides whether/where/how risky; the writer determines the patch;
**the application decides whether the patch is permitted**. Never let a generative
model rediscover a fact the system can represent explicitly. Production orchestration
is deterministic code — LLMs are bounded leaf stages with schema-validated I/O.

---

## 4. Stack & conventions (OD-1..OD-4 — already decided, do not re-litigate)

- **TypeScript on Node ≥ 20**, pnpm-workspaces monorepo, ten packages:
  `@docsync/core`, `@docsync/cli`, `@docsync/github-app`, `@docsync/action`,
  `@docsync/schemas`, `@docsync/openrouter`, `@docsync/verifier`, `@docsync/policy`,
  `@docsync/analyzer-typescript`, `@docsync/analyzer-generic`.
- **Tooling:** vitest, eslint, prettier; **zod is the single schema source of truth**
  (with automated JSON Schema export in `@docsync/schemas`).
- **V1 is files-only** — repository files, no external database. `.docsync/state/`
  is committed to Git in V1 (OD-3).
- **Analyzer plugin order:** TypeScript (tree-sitter) first; generic path-level always
  present; degradation to path-level is *declared in run records*, never silent.
- TypeScript strict mode; no network-dependent unit tests (use recorded doubles).
- Git accessed via the system `git` binary through a typed exec wrapper (rename
  detection + `patch-id` fidelity required by decision D12).
- Thresholds (0.94/0.72) are per-policy-version calibrated data — **never hard-coded**
  (D11).
- All AI inference goes through OpenRouter (D9); Jev/Sonnet are logical roles bound
  via versioned `models.yml`.

---

## 5. Build phase map (PLAN.md §0.2)

```
Phase 0  Foundation & Configuration Core      (monorepo, schemas, .docsync/, fail-closed, state store)
Phase 1  Deterministic Analysis Core          (ChangeAnalysis, graph, claims, baselines, state machine)
Phase 2  Model Access & Decision Layer        (OpenRouter client, Jev, policy engine)
Phase 3  Patch Layer (Writer)                 (constrained workspace, DocPatch, application engine)
Phase 4  Verification Engine                  (three tiers, judge independence)
Phase 5  Orchestrator & Loop A E2E            (deterministic stage graph, RunOutcome API — FROZEN)
Phase 6  Git & PR Integration                 (doc PR write path, rate limits, feedback capture)
Phase 7  CLI                                  (check / fix / pr; exit codes 0/1/2/3)
Phase 8  CI & GitHub Integration              (App + Action, check runs, spikes A6/A7 FIRST)
Phase 9  Provenance & Safety Hardening        (13 invariants as executable tests)
Phase 10 Evaluation, Feedback & Policy      (recorder, metrics, promotion pipeline v1)
Phase 11 V2 — Deeper Understanding          (symbol/AST, claim extraction, Loop B)
Phase 12 V3 — Autonomy Under Governance     (shadow/canary, learning.mode=auto, org floors)
```

Dependencies: 0→1→2→(3∥4)→5→(6∥7∥8)→9→10→11→12. A red phase gate stops progression.
Exit codes: `0` no drift · `1` drift · `2` config error (fail-closed before any AI
stage) · `3` runtime failure (also fail-closed).

---

## 6. Session workflow (every session)

1. **Bootstrap:** read this file + `AGENT-behavior.md` (mandatory, §0) +
   `SESSIONS.md` (latest entry) + the current phase
   section in `docs/PLAN.md` + the relevant `BACKLOG.md` section.
2. **Pick work** from `BACKLOG.md` — take the highest-priority unchecked item whose
   dependencies are green. Do not skip ahead of a red gate.
3. **Work in small increments.** Each backlog item should end in a state where
   `pnpm install && pnpm -r lint && pnpm -r typecheck && pnpm -r test && pnpm -r build`
   is green (once the scaffold exists).
4. **Commit cadence (binding):** after every completed orchestration wave, commit
   the wave's changes LOCALLY on the active `docsync/*` branch (one commit per wave,
   message naming the wave's backlog ids). Never commit to the default branch.
   At phase end: push the `docsync/*` branch to origin and open/refresh the PR —
   pushing to the default branch is never allowed (§3 rule 1).
5. **Update the trackers before ending the session:**
   - Tick completed checkboxes in `BACKLOG.md` (only when actually done + verified).
   - Append a new entry to `SESSIONS.md` (template at the top of that file).
6. **Handoff:** the last SESSIONS.md entry must let a fresh session (with zero memory)
   resume in < 2 minutes: what's done, what's in flight, what's next, open decisions.

> **Session opener:** paste `docs/METAPROMPT.md` as the first prompt of any phase
> session (set `PHASE: <n>` or let it derive the phase from SESSIONS.md/BACKLOG.md).

---

## 7. Rules for `BACKLOG.md`

- Granular checkbox queue per phase, IDs matching PLAN.md (`P0-T01`, `V-P0-1`, gates).
- Statuses: `[ ]` todo · `[~]` in progress (note who/when) · `[x]` done+verified ·
  `[!]` blocked (link the blocker in SESSIONS.md).
- Do NOT renumber or delete items; if scope changes, strike through (`~~item~~`) with
  a one-line reason.
- Phase gate lines are checkboxes too — a phase is done only when its gate is `[x]`.
- New discovered work gets appended under "Discovered work" in that phase with a
  `DX-<phase>-<n>` id and a note in SESSIONS.md.

## 8. Rules for `SESSIONS.md`

- Append-only log; newest entry on top (after the header/template).
- One entry per session minimum; more if the session spans distinct work blocks.
- Record: date, phase, items completed (backlog ids), key decisions (with the D#/OD-R#
  reference or `NEW-DECISION:` tag), test/build status, blockers, exact next step.
- This file is the memory sync mechanism — assume the next prompt/session reads
  nothing else first.

---

## 9. Open-decision discipline

The PLAN.md §0.5 registry (OD-R01–OD-R32) lists resolved recommendations. If work
hits a genuinely new decision point: add `OD-R<next>` in SESSIONS.md with a
recommendation, choose the recommended default to keep moving, and flag it for the
user. Do not silently change any D1–D14 decision — that requires the user.

---

## 10. Quick commands

```bash
pnpm install                                 # bootstrap
pnpm -r lint && pnpm -r typecheck            # static checks
pnpm -r test                                 # vitest suites (no network)
pnpm -r build                                # build all packages
```

When in doubt: fail closed, prefer the conservative path, keep diffs minimal, and
leave a clean handoff.
