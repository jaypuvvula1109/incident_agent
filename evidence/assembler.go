// Package evidence converts raw []ToolResult values into a normalised
// EvidenceList and FailedToolsList ready for the ReasoningEngine.
package evidence

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/org/incident-agent/models"
)

// maxDeploymentRecords bounds how many deployment records are surfaced as
// evidence. When more are returned, only the records closest in time to the
// incident timestamp are kept (Requirement 3.5).
const maxDeploymentRecords = 50

// maxImpactSummaryRunes bounds the length of a KB result's impact summary in
// the evidence description (Requirement 4.3).
const maxImpactSummaryRunes = 200

// unknown is substituted for missing deployment fields (Requirement 3.4).
const unknown = "unknown"

// serviceHealthPrefix identifies get_service_health tool results, which carry a
// ":<service>" suffix on their tool name.
const serviceHealthPrefix = "get_service_health"

// Assemble converts tool results into evidence and failure lists.
//
// Failed tool calls are recorded as FailureMetadata in the FailedToolsList.
// Successful tool calls are dispatched by tool name and normalised into
// EvidenceItems. If no evidence is produced, a sentinel item is inserted so the
// evidence list is never empty.
func Assemble(results []models.ToolResult, incidentTS time.Time) (models.EvidenceList, models.FailedToolsList) {
	var el models.EvidenceList
	var fl models.FailedToolsList

	for _, r := range results {
		if !r.Success {
			fl.Failures = append(fl.Failures, models.FailureMetadata{
				ToolName:         r.ToolName,
				ErrorMessage:     r.ErrorMessage,
				FailureTimestamp: r.FailureTimestamp,
			})
			continue
		}

		switch {
		case r.ToolName == "get_incident":
			if rec, ok := r.Payload.(*models.IncidentRecord); ok && rec != nil {
				el.Items = append(el.Items, assembleIncident(rec))
			}

		case r.ToolName == "get_recent_deployments":
			if recs, ok := r.Payload.([]models.DeploymentRecord); ok {
				el.Items = append(el.Items, assembleDeployments(recs, incidentTS)...)
			}

		case r.ToolName == "search_knowledge_base":
			if recs, ok := r.Payload.([]models.KBResult); ok {
				for _, kb := range recs {
					el.Items = append(el.Items, assembleKBResult(kb))
				}
			}

		case strings.HasPrefix(r.ToolName, serviceHealthPrefix):
			if rec, ok := r.Payload.(*models.ServiceHealthRecord); ok && rec != nil {
				el.Items = append(el.Items, assembleServiceHealth(rec))
			}
		}
	}

	// Sentinel: always return at least one evidence item.
	if len(el.Items) == 0 {
		el.Items = append(el.Items, models.EvidenceItem{
			SourceTool:  "assembler",
			Description: "no data was retrieved from any tool",
		})
	}

	return el, fl
}

// assembleIncident produces a single evidence item summarising the incident
// record's identifier, severity, and affected services (Requirement 2.3).
func assembleIncident(rec *models.IncidentRecord) models.EvidenceItem {
	return models.EvidenceItem{
		SourceTool: "get_incident",
		Description: fmt.Sprintf("Incident %s severity %s affecting %v",
			rec.IncidentID, rec.Severity, rec.AffectedServices),
		RawData: map[string]any{
			"incident_id":       rec.IncidentID,
			"severity":          rec.Severity,
			"affected_services": rec.AffectedServices,
		},
	}
}

// assembleDeployments sorts deployment records by their absolute time distance
// from the incident timestamp (closest first), keeps at most
// maxDeploymentRecords, and produces one evidence item per kept record,
// substituting "unknown" for any missing field (Requirements 3.3, 3.4, 3.5).
func assembleDeployments(recs []models.DeploymentRecord, incidentTS time.Time) []models.EvidenceItem {
	sorted := make([]models.DeploymentRecord, len(recs))
	copy(sorted, recs)

	sort.SliceStable(sorted, func(i, j int) bool {
		return absDuration(sorted[i].DeployedAt.Sub(incidentTS)) <
			absDuration(sorted[j].DeployedAt.Sub(incidentTS))
	})

	if len(sorted) > maxDeploymentRecords {
		sorted = sorted[:maxDeploymentRecords]
	}

	items := make([]models.EvidenceItem, 0, len(sorted))
	for _, d := range sorted {
		serviceName := unknown
		if d.ServiceName != "" {
			serviceName = d.ServiceName
		}
		version := unknown
		if d.Version != "" {
			version = d.Version
		}
		deployedAt := unknown
		if !d.DeployedAt.IsZero() {
			deployedAt = d.DeployedAt.Format(time.RFC3339)
		}

		items = append(items, models.EvidenceItem{
			SourceTool: "get_recent_deployments",
			Description: fmt.Sprintf("Deployment of %s version %s at %s",
				serviceName, version, deployedAt),
			RawData: map[string]any{
				"service_name": serviceName,
				"version":      version,
				"deployed_at":  deployedAt,
			},
		})
	}
	return items
}

// assembleKBResult produces a single evidence item for a knowledge-base result,
// truncating the impact summary to maxImpactSummaryRunes and recording the
// service name in RawData so downstream consumers can verify scoping
// (Requirement 4.3).
func assembleKBResult(kb models.KBResult) models.EvidenceItem {
	impact := truncateRunes(kb.ImpactSummary, maxImpactSummaryRunes)
	return models.EvidenceItem{
		SourceTool: "search_knowledge_base",
		Description: fmt.Sprintf("%s %q for %s: %s",
			kb.ArticleType, kb.Title, kb.ServiceName, impact),
		RawData: map[string]any{
			"article_type":   kb.ArticleType,
			"title":          kb.Title,
			"service_name":   kb.ServiceName,
			"impact_summary": impact,
		},
	}
}

// assembleServiceHealth produces a single evidence item describing a service's
// reported health status (Requirement 5.3).
func assembleServiceHealth(rec *models.ServiceHealthRecord) models.EvidenceItem {
	return models.EvidenceItem{
		SourceTool: "get_service_health",
		Description: fmt.Sprintf("Service %s health status: %s",
			rec.ServiceName, rec.HealthStatus),
		RawData: map[string]any{
			"service_name":  rec.ServiceName,
			"health_status": rec.HealthStatus,
		},
	}
}

// absDuration returns the absolute value of a time.Duration.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// truncateRunes returns s truncated to at most n runes, avoiding splitting a
// multibyte character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
