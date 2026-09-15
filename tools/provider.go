// Package tools defines the ToolProvider interface and the DuckDB-backed
// Stage 1 implementation.
package tools

import (
	"context"
	"time"

	"github.com/org/incident-agent/models"
)

// ToolProvider is the pluggable data-access layer.
// Stage 1: DuckDBToolProvider. Stage 2: HTTPToolProvider.
type ToolProvider interface {
	GetIncident(ctx context.Context, incidentID string) (*models.IncidentRecord, error)
	GetRecentDeployments(ctx context.Context, from, to time.Time) ([]models.DeploymentRecord, error)
	SearchKnowledgeBase(ctx context.Context, services []string, symptomHint string) ([]models.KBResult, error)
	GetServiceHealth(ctx context.Context, service string) (*models.ServiceHealthRecord, error)
}
