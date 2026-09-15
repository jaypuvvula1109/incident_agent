// runner.go implements ConcurrentToolRunner, which fans out all tool calls.
package agent

import (
	"context"
	"sync"
	"time"

	"github.com/org/incident-agent/models"
	"github.com/org/incident-agent/tools"
)

// perToolTimeout bounds each individual tool call. The caller supplies the
// global deadline (30s) via the parent context; this per-tool timeout ensures
// a single slow tool cannot consume the entire budget.
const perToolTimeout = 10 * time.Second

// serviceHealthNamePrefix is prepended to per-service get_service_health tool
// result names, yielding names like "get_service_health:payments-api".
const serviceHealthNamePrefix = "get_service_health:"

// ConcurrentToolRunner owns the fan-out logic.
type ConcurrentToolRunner struct {
	provider tools.ToolProvider
}

// NewConcurrentToolRunner constructs a ConcurrentToolRunner backed by the given
// ToolProvider. It exists so external callers and the external test package can
// build a runner without reaching into the unexported provider field.
func NewConcurrentToolRunner(provider tools.ToolProvider) *ConcurrentToolRunner {
	return &ConcurrentToolRunner{provider: provider}
}

// RunAll executes all tool calls concurrently and returns every result.
//
// It launches one goroutine per tool call (get_incident, get_recent_deployments,
// search_knowledge_base, and one get_service_health per affected service),
// wrapping each in a per-tool context.WithTimeout(ctx, perToolTimeout). The
// passed ctx carries the global 30-second deadline set by the caller; when it
// is cancelled, in-flight goroutines unblock at their next context check.
//
// incidentTS is the authoritative incident timestamp supplied by the
// InvestigatorAgent facade (derived from the get_incident record's CreatedAt,
// falling back to time.Now()). It anchors the deployment lookback window.
//
// All failures — errors, per-tool timeouts, and parent-context cancellation —
// are materialised as ToolResult{Success: false} with ErrorMessage and
// FailureTimestamp populated. RunAll itself never returns an error.
//
// Concurrency safety: results are appended under a mutex, and every goroutine
// defers wg.Done, so wg.Wait blocks until all goroutines have returned. No
// goroutine outlives this call.
func (r *ConcurrentToolRunner) RunAll(ctx context.Context, ic models.IncidentContext, incidentTS time.Time) []models.ToolResult {
	var mu sync.Mutex
	var results []models.ToolResult

	collect := func(res models.ToolResult) {
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	}

	launch := func(name string, fn func(context.Context) (any, error)) {
		tctx, cancel := context.WithTimeout(ctx, perToolTimeout)
		defer cancel()
		payload, err := fn(tctx)
		if err != nil {
			collect(models.ToolResult{
				ToolName:         name,
				Success:          false,
				ErrorMessage:     err.Error(),
				FailureTimestamp: time.Now(),
			})
			return
		}
		collect(models.ToolResult{ToolName: name, Success: true, Payload: payload})
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		launch("get_incident", func(c context.Context) (any, error) {
			return r.provider.GetIncident(c, ic.IncidentID)
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		launch("get_recent_deployments", func(c context.Context) (any, error) {
			// The authoritative incident timestamp is passed in by the
			// InvestigatorAgent facade, which derives it from the get_incident
			// record's CreatedAt (falling back to time.Now()). The deployment
			// window spans the 60 minutes leading up to the incident timestamp
			// (Requirement 3.1). The 60-minute lookback here is a coarse
			// pre-filter; final truncation to the 50 records closest to the
			// incident timestamp happens in the EvidenceAssembler using the
			// same authoritative timestamp.
			to := incidentTS
			return r.provider.GetRecentDeployments(c, to.Add(-60*time.Minute), to)
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		launch("search_knowledge_base", func(c context.Context) (any, error) {
			return r.provider.SearchKnowledgeBase(c, ic.AffectedServices, ic.SymptomHint)
		})
	}()

	for _, svc := range ic.AffectedServices {
		svc := svc
		wg.Add(1)
		go func() {
			defer wg.Done()
			launch(serviceHealthNamePrefix+svc, func(c context.Context) (any, error) {
				return r.provider.GetServiceHealth(c, svc)
			})
		}()
	}

	wg.Wait()
	return results
}
