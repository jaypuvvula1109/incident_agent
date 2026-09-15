// mock_provider_test.go provides a configurable, concurrency-safe mock
// implementation of tools.ToolProvider for exercising ConcurrentToolRunner and
// the InvestigatorAgent facade. It supports per-method canned results, injected
// errors, and context-respecting delays, and it tracks per-method call counts
// so tests can assert invocation invariants (e.g. Property 12).
package agent_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/org/incident-agent/models"
	"github.com/org/incident-agent/tools"
)

// mockToolProvider is a configurable ToolProvider for tests.
//
// Each method has an optional *Err. When set, the method returns that error
// (after honouring *Delay). When nil, the method returns canned success data.
//
// *Delay simulates a slow tool. The delay respects context cancellation: if the
// context is cancelled or its deadline elapses before the delay completes, the
// method returns the context error, mimicking a real timeout.
//
// Call counts are tracked atomically and are safe to read concurrently.
type mockToolProvider struct {
	// Injected errors (nil ⇒ success).
	GetIncidentErr          error
	GetRecentDeploymentsErr error
	SearchKnowledgeBaseErr  error
	GetServiceHealthErr     error

	// Simulated latency per method (zero ⇒ return immediately).
	GetIncidentDelay          time.Duration
	GetRecentDeploymentsDelay time.Duration
	SearchKnowledgeBaseDelay  time.Duration
	GetServiceHealthDelay     time.Duration

	// Call counters (atomic).
	getIncidentCalls          int64
	getRecentDeploymentsCalls int64
	searchKnowledgeBaseCalls  int64
	getServiceHealthCalls     int64

	// mu guards nothing structural today but future-proofs mutable fields set
	// mid-test; the counters and config fields above are otherwise set before
	// concurrent use begins.
	mu sync.Mutex
}

// compile-time assertion that mockToolProvider satisfies the interface.
var _ tools.ToolProvider = (*mockToolProvider)(nil)

// wait sleeps for d while honouring ctx cancellation. It returns the context
// error if the context is done first, otherwise nil once the delay elapses.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		// Still respect an already-cancelled context.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *mockToolProvider) GetIncident(ctx context.Context, incidentID string) (*models.IncidentRecord, error) {
	atomic.AddInt64(&m.getIncidentCalls, 1)
	if err := wait(ctx, m.GetIncidentDelay); err != nil {
		return nil, err
	}
	if m.GetIncidentErr != nil {
		return nil, m.GetIncidentErr
	}
	return &models.IncidentRecord{
		IncidentID:       incidentID,
		Title:            "mock incident",
		Severity:         "P1",
		AffectedServices: []string{"checkout", "payments"},
		CreatedAt:        time.Now(),
	}, nil
}

func (m *mockToolProvider) GetRecentDeployments(ctx context.Context, from, to time.Time) ([]models.DeploymentRecord, error) {
	atomic.AddInt64(&m.getRecentDeploymentsCalls, 1)
	if err := wait(ctx, m.GetRecentDeploymentsDelay); err != nil {
		return nil, err
	}
	if m.GetRecentDeploymentsErr != nil {
		return nil, m.GetRecentDeploymentsErr
	}
	return []models.DeploymentRecord{
		{ServiceName: "checkout", Version: "v2.3.1", DeployedAt: to.Add(-15 * time.Minute)},
	}, nil
}

func (m *mockToolProvider) SearchKnowledgeBase(ctx context.Context, services []string, symptomHint string) ([]models.KBResult, error) {
	atomic.AddInt64(&m.searchKnowledgeBaseCalls, 1)
	if err := wait(ctx, m.SearchKnowledgeBaseDelay); err != nil {
		return nil, err
	}
	if m.SearchKnowledgeBaseErr != nil {
		return nil, m.SearchKnowledgeBaseErr
	}
	return []models.KBResult{
		{
			ArticleID:     "kb-1",
			Title:         "mock TSG",
			ArticleType:   "TSG",
			ServiceName:   "checkout",
			SymptomTags:   []string{"5xx_errors"},
			ImpactSummary: "mock impact",
			LastUpdated:   time.Now(),
		},
	}, nil
}

func (m *mockToolProvider) GetServiceHealth(ctx context.Context, service string) (*models.ServiceHealthRecord, error) {
	atomic.AddInt64(&m.getServiceHealthCalls, 1)
	if err := wait(ctx, m.GetServiceHealthDelay); err != nil {
		return nil, err
	}
	if m.GetServiceHealthErr != nil {
		return nil, m.GetServiceHealthErr
	}
	return &models.ServiceHealthRecord{
		ServiceName:  service,
		HealthStatus: "degraded",
	}, nil
}

// --- call-count accessors (safe to call after RunAll returns) ---

func (m *mockToolProvider) GetIncidentCalls() int64 {
	return atomic.LoadInt64(&m.getIncidentCalls)
}

func (m *mockToolProvider) GetRecentDeploymentsCalls() int64 {
	return atomic.LoadInt64(&m.getRecentDeploymentsCalls)
}

func (m *mockToolProvider) SearchKnowledgeBaseCalls() int64 {
	return atomic.LoadInt64(&m.searchKnowledgeBaseCalls)
}

func (m *mockToolProvider) GetServiceHealthCalls() int64 {
	return atomic.LoadInt64(&m.getServiceHealthCalls)
}

// errTool is a convenience error constructor for tests.
func errTool(msg string) error { return errors.New(msg) }
