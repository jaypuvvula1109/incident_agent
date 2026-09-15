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

// Investigate runs the full investigation pipeline for the given prompt using a
// two-phase flow:
//
//  1. Validate the prompt (returns PromptValidationError on failure, before any
//     tool call).
//  2. Wrap ctx with a 30-second global deadline (Requirement 9.1).
//  3. Analyse the prompt to extract an IncidentContext (incident ID, etc.).
//  4. PHASE 1 — prime context: fetch the incident to derive the authoritative
//     incident timestamp and the list of affected services, so the KB search and
//     per-service health tools have services to operate on.
//  5. PHASE 2 — fan out: run all tools concurrently anchored on the incident
//     timestamp.
//  6. Assemble evidence and failure metadata.
//  7. Reason over the evidence with the LLM.
func (a *InvestigatorAgent) Investigate(ctx context.Context, prompt string) (models.InvestigationSummary, error) {
	if err := ValidatePrompt(prompt); err != nil {
		return models.InvestigationSummary{}, err
	}

	gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ic := AnalysePrompt(prompt)

	// PHASE 1 — prime context. This get_incident read is deliberately separate
	// from (and in addition to) the one performed inside RunAll below. Its sole
	// purpose is to derive the authoritative incident timestamp and to enrich
	// ic.AffectedServices so the knowledge-base and service-health tools have
	// services to work with. Its result is otherwise discarded: the uniform
	// get_incident evidence (or failure) is produced by the fan-out in phase 2
	// and normalised by the assembler, keeping the assembler logic unchanged.
	// The extra read is cheap against local DuckDB and is idempotent.
	incidentTS := time.Now()
	if incident, err := a.provider.GetIncident(gctx, ic.IncidentID); err == nil && incident != nil {
		if !incident.CreatedAt.IsZero() {
			incidentTS = incident.CreatedAt
		}
		ic.AffectedServices = incident.AffectedServices
	}
	// If the priming fetch fails, we leave AffectedServices as-is (empty) and
	// continue: the investigation must still proceed, and the get_incident
	// failure will be recorded as evidence by the fan-out in phase 2.

	// PHASE 2 — fan out all tools concurrently, anchored on incidentTS.
	runner := &ConcurrentToolRunner{provider: a.provider}
	results := runner.RunAll(gctx, ic, incidentTS)

	ev, failed := evidence.Assemble(results, incidentTS)

	summary, err := a.engine.Reason(gctx, ev, failed, prompt)
	if err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: %w", err)
	}
	return summary, nil
}
