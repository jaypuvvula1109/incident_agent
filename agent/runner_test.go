// runner_test.go contains unit and property-based tests for
// ConcurrentToolRunner.RunAll (tasks 6.2–6.5).
package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/org/incident-agent/agent"
	"github.com/org/incident-agent/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// resultsByName indexes ToolResults by their ToolName for convenient assertion.
func resultsByName(results []models.ToolResult) map[string]models.ToolResult {
	m := make(map[string]models.ToolResult, len(results))
	for _, r := range results {
		m[r.ToolName] = r
	}
	return m
}

// --- Task 6.2: unit tests ---

func TestRunAll_AllToolsSucceed(t *testing.T) {
	provider := &mockToolProvider{}
	runner := agent.NewConcurrentToolRunner(provider)

	ic := models.IncidentContext{
		IncidentID:       "INC-1001",
		AffectedServices: []string{"checkout", "payments"},
		SymptomHint:      "5xx",
	}

	results := runner.RunAll(context.Background(), ic, time.Now())

	// 3 base tools + one get_service_health per affected service (2) = 5.
	require.Len(t, results, 5)
	for _, r := range results {
		assert.Truef(t, r.Success, "expected %s to succeed, got error %q", r.ToolName, r.ErrorMessage)
	}

	byName := resultsByName(results)
	assert.Contains(t, byName, "get_incident")
	assert.Contains(t, byName, "get_recent_deployments")
	assert.Contains(t, byName, "search_knowledge_base")
	assert.Contains(t, byName, "get_service_health:checkout")
	assert.Contains(t, byName, "get_service_health:payments")
}

func TestRunAll_OneToolReturnsError(t *testing.T) {
	provider := &mockToolProvider{
		GetIncidentErr: errTool("incident store unavailable"),
	}
	runner := agent.NewConcurrentToolRunner(provider)

	ic := models.IncidentContext{
		IncidentID:       "INC-1001",
		AffectedServices: []string{"checkout"},
		SymptomHint:      "5xx",
	}

	results := runner.RunAll(context.Background(), ic, time.Now())
	require.Len(t, results, 4) // 3 base + 1 service

	byName := resultsByName(results)

	failed := byName["get_incident"]
	require.False(t, failed.Success)
	assert.Equal(t, "incident store unavailable", failed.ErrorMessage)
	assert.False(t, failed.FailureTimestamp.IsZero())

	// Every other tool still succeeds.
	for name, r := range byName {
		if name == "get_incident" {
			continue
		}
		assert.Truef(t, r.Success, "expected %s to succeed", name)
	}
}

func TestRunAll_OneToolTimesOut(t *testing.T) {
	// The runner hardcodes a 10s per-tool timeout, so instead we drive the
	// timeout through a short parent context. The slow tool sleeps 200ms while
	// respecting context cancellation; fast tools return immediately.
	provider := &mockToolProvider{
		SearchKnowledgeBaseDelay: 200 * time.Millisecond,
	}
	runner := agent.NewConcurrentToolRunner(provider)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ic := models.IncidentContext{
		IncidentID:       "INC-1001",
		AffectedServices: []string{"checkout"},
		SymptomHint:      "5xx",
	}

	start := time.Now()
	results := runner.RunAll(ctx, ic, time.Now())
	elapsed := time.Since(start)

	// RunAll must return promptly (not wait out the 200ms slow tool much
	// beyond the 50ms deadline). Give generous headroom for CI slowness.
	assert.Less(t, elapsed, 200*time.Millisecond, "RunAll should return near the context deadline")

	require.Len(t, results, 4)
	byName := resultsByName(results)

	slow := byName["search_knowledge_base"]
	require.False(t, slow.Success, "slow tool should be recorded as a failure")
	assert.NotEmpty(t, slow.ErrorMessage)
	assert.False(t, slow.FailureTimestamp.IsZero())
}

func TestRunAll_GlobalContextCancelled(t *testing.T) {
	// An already-cancelled parent context: every tool observes cancellation.
	provider := &mockToolProvider{
		// Small delays force the tools to hit the ctx.Done() branch.
		GetIncidentDelay:          10 * time.Millisecond,
		GetRecentDeploymentsDelay: 10 * time.Millisecond,
		SearchKnowledgeBaseDelay:  10 * time.Millisecond,
		GetServiceHealthDelay:     10 * time.Millisecond,
	}
	runner := agent.NewConcurrentToolRunner(provider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before running

	ic := models.IncidentContext{
		IncidentID:       "INC-1001",
		AffectedServices: []string{"checkout", "payments"},
		SymptomHint:      "5xx",
	}

	results := runner.RunAll(ctx, ic, time.Now())

	// RunAll still returns all tool slots, each as a failure.
	require.Len(t, results, 5)
	for _, r := range results {
		assert.Falsef(t, r.Success, "expected %s to fail under cancelled context", r.ToolName)
		assert.NotEmpty(t, r.ErrorMessage)
		assert.False(t, r.FailureTimestamp.IsZero())
	}
}

// --- Task 6.3: Property 3 ---

// Feature: incident-investigator, Property 3: Tool failures never suppress investigation
func TestProperty3_ToolFailuresNeverSuppressInvestigation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		failIncident := rapid.Bool().Draw(t, "fail_get_incident")
		failDeployments := rapid.Bool().Draw(t, "fail_get_recent_deployments")
		failKB := rapid.Bool().Draw(t, "fail_search_knowledge_base")

		provider := &mockToolProvider{}
		if failIncident {
			provider.GetIncidentErr = errTool("get_incident failed")
		}
		if failDeployments {
			provider.GetRecentDeploymentsErr = errTool("get_recent_deployments failed")
		}
		if failKB {
			provider.SearchKnowledgeBaseErr = errTool("search_knowledge_base failed")
		}

		runner := agent.NewConcurrentToolRunner(provider)
		ic := models.IncidentContext{
			IncidentID:       "INC-1001",
			AffectedServices: []string{"checkout"}, // health tool always succeeds here
			SymptomHint:      "5xx",
		}

		results := runner.RunAll(context.Background(), ic, time.Now())

		// RunAll always returns a non-nil slice and never panics.
		require.NotNil(t, results)
		require.Len(t, results, 4)

		byName := resultsByName(results)

		// Every tool NOT set to fail must appear with Success=true.
		if !failIncident {
			assert.True(t, byName["get_incident"].Success)
		}
		if !failDeployments {
			assert.True(t, byName["get_recent_deployments"].Success)
		}
		if !failKB {
			assert.True(t, byName["search_knowledge_base"].Success)
		}
		// The service-health tool is never configured to fail in this property.
		assert.True(t, byName["get_service_health:checkout"].Success)
	})
}

// --- Task 6.4: Property 11 ---

// Feature: incident-investigator, Property 11: Failure metadata always captures all required fields
func TestProperty11_FailureMetadataAlwaysComplete(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Arbitrary non-empty error messages for each tool.
		msgIncident := rapid.StringMatching(`[a-zA-Z0-9 ]{1,40}`).Draw(t, "msg_incident")
		msgDeploy := rapid.StringMatching(`[a-zA-Z0-9 ]{1,40}`).Draw(t, "msg_deploy")
		msgKB := rapid.StringMatching(`[a-zA-Z0-9 ]{1,40}`).Draw(t, "msg_kb")
		msgHealth := rapid.StringMatching(`[a-zA-Z0-9 ]{1,40}`).Draw(t, "msg_health")

		provider := &mockToolProvider{
			GetIncidentErr:          errTool(msgIncident),
			GetRecentDeploymentsErr: errTool(msgDeploy),
			SearchKnowledgeBaseErr:  errTool(msgKB),
			GetServiceHealthErr:     errTool(msgHealth),
		}

		runner := agent.NewConcurrentToolRunner(provider)
		ic := models.IncidentContext{
			IncidentID:       "INC-1001",
			AffectedServices: []string{"checkout", "payments"},
			SymptomHint:      "5xx",
		}

		results := runner.RunAll(context.Background(), ic, time.Now())
		require.NotEmpty(t, results)

		for _, r := range results {
			require.False(t, r.Success, "all tools were configured to fail")
			assert.NotEmpty(t, r.ToolName, "failure metadata must include tool name")
			assert.False(t, r.FailureTimestamp.IsZero(), "failure metadata must include a timestamp")
			assert.NotEmpty(t, r.ErrorMessage, "failure metadata must include an error message")
		}
	})
}

// --- Task 6.5: Property 12 ---

// Feature: incident-investigator, Property 12: Knowledge base is called at most once per investigation
func TestProperty12_KnowledgeBaseCalledExactlyOnce(t *testing.T) {
	serviceGen := rapid.StringMatching(`[a-z][a-z0-9-]{0,15}`)

	rapid.Check(t, func(t *rapid.T) {
		incidentID := rapid.StringMatching(`(INC-[0-9]{1,6})?`).Draw(t, "incident_id")
		services := rapid.SliceOfN(serviceGen, 0, 5).Draw(t, "affected_services")
		symptomHint := rapid.StringMatching(`[a-z_]{0,20}`).Draw(t, "symptom_hint")

		provider := &mockToolProvider{}
		runner := agent.NewConcurrentToolRunner(provider)

		ic := models.IncidentContext{
			IncidentID:       incidentID,
			AffectedServices: services,
			SymptomHint:      symptomHint,
		}

		results := runner.RunAll(context.Background(), ic, time.Now())

		// Sanity: 3 base tools + one health tool per service.
		require.Len(t, results, 3+len(services))

		// The core invariant: SearchKnowledgeBase invoked exactly once.
		assert.Equal(t, int64(1), provider.SearchKnowledgeBaseCalls())
	})
}
