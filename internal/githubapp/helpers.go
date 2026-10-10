package githubapp

import "github.com/DUDE616/docSync-go/internal/orchestrator"

func intPtr(i int) *int { return &i }

func conclusionPtr(conclusion orchestrator.CheckRunConclusion) *orchestrator.CheckRunConclusion {
	return &conclusion
}
