// Ported from packages/core/src/orchestrator/graph.ts (P5-T01 part 2; PLAN
// §6.4; ARCH §3.5, §7.1).
//
// Canonical Loop-A stage graph: the orchestrator's control flow is DATA, not
// imperative branching (ARCH §13: deterministic stage machine). DeclareStageGraph
// returns the frozen canonical edge list the runner walks. Edge semantics:
//
//   - a plain edge {From, To} is the unconditional sequential path;
//   - SkipIf marks a CONDITIONAL shortcut edge: it is traversed only when the
//     run's routing equals SkipIf.Equals (with SkipIf.Routing naming the
//     compared subject); otherwise the stage's unconditional edge applies.
//
// The three-way JEV branch (ARCH §7.1): on CONTINUE the machine proceeds to
// `retrieval` (the plain jev→retrieval edge — reachable ONLY on CONTINUE);
// on REVIEW it goes straight to `policy` (review resolution, no patch path);
// on IGNORE it jumps to `persist` via the jev→persist shortcut. The
// ignore-routed MAJORITY therefore never invokes the writer (ARCH §7.1):
// retrieval, writer, and verification are all unreachable under IGNORE.
//
// Pure and deterministic: no I/O, no clock, same input → same frozen list.

package orchestrator

// StageEdge is one directed edge of the canonical stage graph (see the module
// doc-comment).
type StageEdge struct {
	// From is the source stage.
	From StageName
	// To is the target stage.
	To StageName
	// SkipIf is the conditional-shortcut guard: the edge is traversed only
	// when the run's routing equals Equals; otherwise the stage's
	// unconditional edge is taken. Nil for the unconditional path.
	SkipIf *StageEdgeGuard
}

// StageEdgeGuard is the conditional-shortcut guard of one edge.
type StageEdgeGuard struct {
	// Routing names the compared subject.
	Routing Routing
	// Equals is the routing value the guard matches.
	Equals Routing
}

// DeclareStageGraph returns the frozen canonical Loop-A edge list (P5-T01;
// ARCH §3.5, §7.1). From `jev` the edges are ordered so the conditional
// shortcuts (IGNORE→persist, REVIEW→policy) resolve before the plain
// jev→retrieval path — the runner takes the first edge whose guard matches.
func DeclareStageGraph() []StageEdge {
	return []StageEdge{
		{From: StageAnalyze, To: StageMap},
		{From: StageMap, To: StageInvalidate},
		{From: StageInvalidate, To: StageJev},
		{From: StageJev, To: StagePersist, SkipIf: &StageEdgeGuard{Routing: RoutingIgnore, Equals: RoutingIgnore}},
		{From: StageJev, To: StagePolicy, SkipIf: &StageEdgeGuard{Routing: RoutingReview, Equals: RoutingReview}},
		{From: StageJev, To: StageRetrieval},
		{From: StageRetrieval, To: StageWriter},
		{From: StageWriter, To: StageVerification},
		{From: StageVerification, To: StagePolicy},
		{From: StagePolicy, To: StagePersist},
	}
}

// NextStage is the next stage from the declared graph: the first edge whose
// skipIf guard passes (TS nextStage). The empty stage name means the walk
// terminated.
func NextStage(from StageName, routing Routing) StageName {
	for _, edge := range DeclareStageGraph() {
		if edge.From != from {
			continue
		}
		if edge.SkipIf != nil && routing != edge.SkipIf.Equals {
			continue
		}
		return edge.To
	}
	return ""
}
