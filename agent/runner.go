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
// All failures — errors, per-tool timeouts, and parent-context cancellation —
// are materialised as ToolResult{Success: false} with ErrorMessage and
// FailureTimestamp populated. RunAll itself never returns an error.
//
// Concurrency safety: results are appended under a mutex, and every goroutine
// defers wg.Done, so wg.Wait blocks until all goroutines have returned. No
// goroutine outlives this call.
func (r *ConcurrentToolRunner) RunAll(ctx context.Context, ic models.IncidentContext) []models.ToolResult {
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
			// Stage 1 limitation: the authoritative incident timestamp comes
			// from the get_incident record's CreatedAt, which is only known at
			// runtime after that tool resolves. IncidentContext does not carry
			// a timestamp field, and adding one here would ripple into the
			// prompt-analysis and agent-facade tasks. So the runner anchors the
			// deployment window on time.Now() as a Stage 1 approximation.
			//
			// The InvestigatorAgent facade (task 10.1) is responsible for the
			// authoritative incident timestamp: it derives incidentTS from the
			// get_incident result (falling back to time.Now()) and passes it to
			// the EvidenceAssembler, which performs the recency-based selection
			// of deployment records. The 60-minute lookahead here is a coarse
			// pre-filter; final truncation to the 50 closest records happens in
			// the assembler using the authoritative timestamp.
			to := time.Now()
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
