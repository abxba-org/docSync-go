package cli_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

func TestDecideRegisteredHandler(t *testing.T) {
	fake := testkit.NewDecisionStageFake()
	runID := "run-1"
	sentinel := orchestrator.DecisionStageResult{}
	fake.On(runID, sentinel)
	input := orchestrator.DecisionStageInput{Ctx: orchestrator.RunContext{RunID: runID}}
	got, err := fake.Decide(input)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != sentinel {
		t.Fatalf("expected sentinel result")
	}
	if len(fake.Calls()) != 1 {
		t.Fatalf("expected handler called once")
	}
}

func TestDecideDefaultHandler(t *testing.T) {
	fake := testkit.NewDecisionStageFake()
	sentinel := orchestrator.DecisionStageResult{}
	fake.OnDefault(sentinel)
	input := orchestrator.DecisionStageInput{Ctx: orchestrator.RunContext{RunID: "unregistered"}}
	got, err := fake.Decide(input)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != sentinel {
		t.Fatalf("expected default result")
	}
}

func TestDecideErrorHandler(t *testing.T) {
	fake := testkit.NewDecisionStageFake()
	runID := "run-err"
	wantErr := errors.New("boom")
	fake.OnError(runID, wantErr)
	input := orchestrator.DecisionStageInput{Ctx: orchestrator.RunContext{RunID: runID}}
	_, err := fake.Decide(input)
	if err == nil || err.Error() != wantErr.Error() {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}
