// Package agent contains the InvestigatorAgent facade, input validation,
// prompt analysis, and the ConcurrentToolRunner.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/org/incident-agent/evidence"
	"github.com/org/incident-agent/models"
	"github.com/org/incident-agent/tools"
)

// ReasoningEngine is the interface the agent uses to call the LLM.
// This indirection allows mock injection in tests.
type ReasoningEngine interface {
	Reason(ctx context.Context, ev models.EvidenceList, failed models.FailedToolsList, prompt string) (models.InvestigationSummary, error)
}

// InvestigatorAgent is the top-level facade that orchestrates the investigation pipeline.
type InvestigatorAgent struct {
	provider tools.ToolProvider
	engine   ReasoningEngine
}

// NewInvestigatorAgent constructs an InvestigatorAgent with the given provider and engine.
func NewInvestigatorAgent(provider tools.ToolProvider, engine ReasoningEngine) *InvestigatorAgent {
	return &InvestigatorAgent{provider: provider, engine: engine}
}

// Investigate runs the full investigation pipeline for the given prompt.
//  1. Validate the prompt (returns PromptValidationError on failure).
//  2. Wrap ctx with a 30-second global deadline.
//  3. Analyse the prompt to extract IncidentContext.
//  4. Run all tools concurrently.
//  5. Assemble evidence.
//  6. Reason with the LLM.
//
// Task 10.1 provides the complete implementation; this is a compilable stub.
func (a *InvestigatorAgent) Investigate(ctx context.Context, prompt string) (models.InvestigationSummary, error) {
	if err := ValidatePrompt(prompt); err != nil {
		return models.InvestigationSummary{}, err
	}

	gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ic := AnalysePrompt(prompt)

	runner := &ConcurrentToolRunner{provider: a.provider}
	results := runner.RunAll(gctx, ic)

	incidentTS := time.Now()
	ev, failed := evidence.Assemble(results, incidentTS)

	summary, err := a.engine.Reason(gctx, ev, failed, prompt)
	if err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: %w", err)
	}
	return summary, nil
}
