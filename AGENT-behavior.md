# AGENT-behavior.md — Living Behavioral Guidance

> **Purpose:** Distilled, reusable behavioral rules for every agent working in this
> repository — learned from real failures and corrections, not a diary of events.
> Read this file BEFORE any substantive work and apply it throughout the session.
> Update it when a new, generalizable lesson is discovered during execution
> (protocol below). This file complements `AGENTS.md` (role & process rules); it
> holds learned _behavioral_ guidance only.

## Maintenance protocol

When a meaningful failure, repeated mistake, or useful correction is discovered:

1. **Observe** — record what actually happened (facts, not intentions).
2. **Diagnose root cause** — why it happened, not just what surfaced.
3. **Generalize** — would this recur in a different task? If not, drop it.
4. **Add/Refine** — update the matching rule below; merge duplicates; delete rules
   made obsolete by better guidance. Prefer refining an existing rule over adding one.
5. **Apply** — change your behavior immediately in the current work.
6. **Verify** — confirm the change worked; refine again if it did not.

Rules must be concise, actionable, and reusable. No one-off events, no noise.

## Delegation & sub-agents

- Decompose large objectives into SMALL, single-unit delegate tasks. Units sharing
  files run sequentially; only disjoint files run in parallel.
- Oversized delegate tasks fail — timeouts, connection resets, silent runs — and they
  typically fail with NOTHING persisted. Split before the failure pattern appears,
  not after the second or third occurrence. Forensic refinement (2026-10-08):
  the failure is provider-side stream breakage amplified by the delegate's
  context-payload size, NOT unit size alone — see 'Delegate mid-turn death'
  below for the root cause and the standing solution hierarchy.
- **Parallelize by dependency, not by phase:** at any moment at least one delegate
  should be running per disjoint file-cluster — idle orchestrator time is the
  slowest failure mode of a long session. Before launching, list every remaining
  unit's file-set; launch EVERY unit whose files are disjoint from all in-flight
  work (packages are the natural clusters; `package.json`, barrels, root configs,
  trackers, and shared docs stay single-writer). Cross-phase parallelism is allowed
  when the user batches phases and the branch strategy permits (shared-branch
  precedent) — quality comes from many small verified units running concurrently
  plus a full gate at wave boundaries, never from serializing independent work.
- Require delegates to persist each file to disk immediately as composed; never
  batch all output to the end (long silent runs are what die).
- Delegate prompts must be self-contained: exact file paths, binding constraints,
  distilled API context, the tests to write (with contractual names), scoped gate
  commands, and a mandatory report format. A delegate should never rediscover context.
- An empty or TRUNCATED delegate report is a FAILED unit, not a completed one
  (the runtime persists a dead turn as 'completed' with a cut-off body — the
  state flag lies). Check the tree state on disk before deciding anything.
- On a delegate connection reset: check disk first, then resume the preserved
  session from the last persisted point. If a session repeatedly returns empty
  results, abandon it and relaunch the same small task fresh ONCE; if the same
  authoring shape dies again, switch to mechanical delegation (the orchestrator
  composes the exact content; the delegate only writes + gates) — see
  'Delegate mid-turn death' below.
- Parallel delegates never run concurrent full `pnpm -r` builds or concurrent
  `pnpm install`; they use scoped gates only; the orchestrator runs the full gate
  after each wave.
- Replay/recording test doubles shared across CONCURRENT code paths must key matches
  by REQUEST IDENTITY (e.g. doc path), never positional FIFO — completion-order
  consumption cross-wires fixtures and manifests as load-dependent flake (phase-6:
  verifier composition test 2/5 red under parallel load; fixed 4c68f4b by per-doc
  replay routing). When a composed component may call a dependency concurrently, the
  double MUST be dispatch-by-input. Full symptom→cause→fix entry: see the
  Test-failure playbook (§ below).
- Single-writer per wave for shared files: `package.json`, barrels (`index.ts`),
  root configs, and trackers.
- Orchestrator state can corrupt across long multi-phase sessions — phantom
  commit refs, wrong branch/phase labels, imagined prior work have all appeared
  in prompts. Before EVERY wave-gate action (commit, gate run, phase close):
  reconcile claims against disk + `git log`/`git show --stat` via a delegate
  probe; derive commit messages from the ACTUAL staged diff (never from
  memory); treat any memory-vs-disk mismatch as a stop-and-reconcile event,
  not a continue-and-hope.

### Delegate mid-turn death — root cause + standing solution (2026-10-08 forensic evidence)

Subagent sessions die mid-turn and the runtime reports them `completed` with a
truncated body. Death signature: the unit's last streamed line is a "writing X
now" transition right after a heavy same-turn read/recon phase; it strikes
regardless of unit size (even a 1-file scaffold unit died) and is
non-deterministic (identical prompt shapes died twice; equally heavy units
sometimes survived). Root cause (forensics on ~/.local/share/kilo/log/opencode.log):
provider-side stream failures on the subagent's model — 504 "Upstream idle
timeout exceeded", "The socket connection was closed unexpectedly", 502
"Network connection lost.", plus OpenRouter key-credit clamps (402 payment
required) — each immediately followed by a message=process error, after which
the runtime persists the partial turn. The amplifier is runaway context
payloads: 852 payload-limit pruning events, 827 "payload still large after
pruning" warnings, observed payloads up to ~1.7 MB. The bigger the delegate's
same-turn context, the likelier a stream failure/clamp kills it at the next
output event — which is why deaths cluster at the first composition after a
long recon.

Standing solution hierarchy (apply in order):

1. MECHANICAL delegation is the default for composition-heavy units: the
   orchestrator reads the ground truth itself and composes the EXACT file
   content into the prompt; the delegate only writes it (quoted heredoc) and
   runs the scoped gates. Evidence: 7/7 mechanical units survived this
   session; every authoring-unit death was avoidable this way.
2. When the delegate must author: persist-first ordering — a trivial artifact
   to disk BEFORE any deep recon, then one file at a time. Necessary but NOT
   sufficient alone (a scaffold-first unit still died once its recon grew).
3. Never retry an identical authoring shape more than once — the retry
   re-accumulates the same payload and dies the same way (observed three
   times). On the second death of the same shape: mechanical delegation.
4. Truncated report == empty report: probe the tree on disk, decide resume
   vs relaunch from persisted state, and BANK any recon facts the dead unit
   reported in its partial output — those facts are often its most valuable
   product (they cost the most payload to produce).
5. Keep delegate recon cheap: hand distilled API context in the prompt (never
   "read the whole file"); prefer grep-of-exports over whole-file reads when
   the delegate only needs signatures — payload size is the real budget.
6. Machine-level secondary factors (swap pressure, disk 84% full) are real
   but not per-session actionable; do not amplify them (no concurrent full
   builds, clean temp state dirs promptly).

## Verification & completion honesty

- Done = artifact exists on disk AND the full gate is green AND the item's named
  verification test passes. Never infer completion from a delegate's word or a
  status flag.
- Read the actual persisted artifact yourself before any step that depends on it.
- Review findings are ruled on, not collected: ratify or reject each with a written
  rationale; a delegate that violates a binding decision is redone, not partially
  accepted.
- Fix → re-verify loop: apply corrections, then have the SAME independent verifier
  re-check only the deltas; release the verifier explicitly after its final verdict.
- Run a whole-change-set review (not per-wave snippets) before declaring a phase
  ready; classify findings BLOCKER / MAJOR / MINOR.
- Surface assumptions and disagreements; record genuinely new decision points with
  an OD-R# id and a recommendation instead of silently choosing.

## Speed vs quality

- Time matters: batch moderately, let scoped gates be the primary per-unit
  verification, and reserve deep file reading for wave boundaries and the review
  pass — speed comes from fewer, better-verified steps, not from skipping checks.
- Never trade correctness for speed: fail-closed defaults, minimal diffs, and
  verification gates hold even under time pressure ("time is money, but don't slack
  off on quality").

## Failure recovery

- Persist first: the smallest viable unit goes to disk before the next is composed.
- Resume, never restart: inspect existing files and continue from the last persisted
  point (worst case: the last wave commit).
- Fail closed when in doubt: degrade to less automation, surface the blocker, and
  keep moving on a recorded recommended default rather than improvising silently.

## Test-failure playbook (observed failure classes — symptom → cause → fix)

Each entry names a failure actually observed in this repo, its root cause, and the
standing solution. When a failure matches an entry, apply the solution directly —
do not re-diagnose from scratch; when it matches none, diagnose honestly and add
the class here (protocol above).

- **Load-dependent flake (green alone, red under `pnpm -r test` parallel load).**
  Observed: verifier composition test 2/5 red across full-suite runs, green in
  isolation (4c68f4b). Cause: a positional-FIFO replay transport shared by two
  CONCURRENT writer-pool calls — completion-order consumption fed each doc the
  other's recorded body (cross-wired patches → abstains → assertion fail).
  Fix: key every replay/recording double by REQUEST IDENTITY and dispatch per
  call; fail loud on unexpected identities. Verification protocol before
  declaring fixed: 3× package suite + 2× full `pnpm -r test` + stress run —
  all green. See also the replay rule in `Delegation & sub-agents`.
- **Cross-package typecheck ripple from a required field.**
  Observed: adding `maxConcurrentDocs` to `PipelineBudgets` (core) broke
  test-fixture literals in openrouter AND verifier at the wave gate.
  Cause: a required field added to a shared schema/type invalidates every
  literal constructing it, in any package. Fix: expect the ripple at the wave
  gate; grep all constructors of the type repo-wide; mechanical one-line
  additions in fixtures. NEVER loosen the type or make the field optional to
  dodge the ripple — the compile error is the design working.
- **TS2308 'Module has already exported a member' at a barrel.**
  Observed twice: `concurrencyGroupFor` (orchestrator/run.ts vs
  gitpr/governors.ts) and the self-trigger trio redeclared in doc-pr-flow.ts.
  Cause: two modules export the same name and a barrel star-exports both.
  Fix: dedupe to a SINGLE source — the dependent module imports from the
  canonical one and deletes its copy (preferred; doc-pr-flow fix) — or, when
  both exports are legitimate and distinct, an explicit named re-export in the
  merging barrel (src/index.ts:30 precedent). Renaming is last (breaks API).
- **Existing tests fail after a new flow pre-condition.**
  Observed: the dirty-worktree pre-flight made composed-flow tests fail on
  fixture trees, not logic. Cause: the flow now refuses environments the old
  fixtures created. Fix: adapt the FIXTURES to satisfy the pre-condition
  (clean trees, gitignored flow bookkeeping) and widen only mechanical
  helpers (e.g. an abort-reason union in an assert helper). Assertions stay.
- **CLI/platform tooling fails on a deprecated remote API.**
  Observed: `gh pr edit` died on GraphQL `projectCards` (GitHub sunset), the
  mutation never ran. Cause: the tool's query carries a removed field — the
  failure is the TOOLING, not the underlying work. Fix: verify state
  read-only first (is the title/body actually unchanged?), then retry via an
  alternate path (REST `PATCH /repos/{o}/{r}/pulls/{n}`) or upgrade the tool.
  Never read a failed CLI call as "the work failed".
- **Delegate empty/truncated reports and orchestrator phantom state.**
  Standing rules: `Delegation & sub-agents` (empty report = failed unit; disk
  check before deciding; micro-unit split + one-shot resume; reconcile against
  git/disk before wave-gate actions). The 2026-10-07 corruption incident
  (phantom commit refs in prompts, an inaccurate commit message caught at the
  gate, amended pre-push) is the canonical example — DX-P6-3.
- **Unanchored .gitignore rules silently swallow new directories.**
  Observed: phase-11 created `packages/core/src/coverage/` and a
  fixtures/coverage dir; the root rule `coverage/` (unanchored, meant for
  vitest output) matched them at ANY depth — files were composed on disk but
  never tracked, discovered only at commit-probe time (renamed to
  coverage-audit/). Fix: before composing into any NEW directory, check
  .gitignore for unanchored segment rules
  (`rg -n "^[^/#]*coverage" .gitignore`-style probe); prefer anchoring
  existing rules (`/coverage/`) only when their intent is unambiguous,
  otherwise add a negation or pick a non-colliding path and record the
  deviation.

## Traceability & documentation discipline

- Cite BACKLOG ids (`P#-T##`) directly in doc-comments; never re-derive PLAN §-bullet
  indices — the mapping drifts (one phase accumulated ~15 wrong citations that cost
  a mechanical fix pass).
- Every automated change carries provenance; every failing check carries a
  non-empty evidence detail.

## Session hygiene

- When work spans multiple phases, keep a todo list and execute ONE phase at a time
  unless the user explicitly batches; queue later phases as pending items; when
  batched, start each later phase's phase-independent units (disjoint packages/docs)
  as soon as its branch strategy is settled (OD-R39 shared-branch precedent) rather
  than idling behind the earlier phase's close.
- Update trackers (`BACKLOG.md` / `SESSIONS.md`) only with proven facts (named
  tests, executed gates) — never anticipatorily.
- Keep handoffs zero-memory-resumable: the newest `SESSIONS.md` entry must contain
  the exact next step.
