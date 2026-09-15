package evidence_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org/incident-agent/evidence"
	"github.com/org/incident-agent/models"
)

// fixedIncidentTS is a stable reference timestamp used across tests.
var fixedIncidentTS = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

// countBySource returns how many evidence items originate from the given tool.
func countBySource(items []models.EvidenceItem, source string) int {
	n := 0
	for _, it := range items {
		if it.SourceTool == source {
			n++
		}
	}
	return n
}

// -----------------------------------------------------------------------------
// Task 7.6 — Unit tests
// -----------------------------------------------------------------------------

func TestAssemble_DeploymentListExactly50(t *testing.T) {
	recs := make([]models.DeploymentRecord, 50)
	for i := range recs {
		recs[i] = models.DeploymentRecord{
			ServiceName: fmt.Sprintf("svc-%d", i),
			Version:     "v1",
			DeployedAt:  fixedIncidentTS.Add(time.Duration(i) * time.Minute),
		}
	}
	results := []models.ToolResult{
		{ToolName: "get_recent_deployments", Success: true, Payload: recs},
	}

	el, _ := evidence.Assemble(results, fixedIncidentTS)
	assert.Equal(t, 50, countBySource(el.Items, "get_recent_deployments"))
}

func TestAssemble_DeploymentList51KeepsOnly50(t *testing.T) {
	recs := make([]models.DeploymentRecord, 51)
	for i := range recs {
		recs[i] = models.DeploymentRecord{
			ServiceName: fmt.Sprintf("svc-%d", i),
			Version:     "v1",
			DeployedAt:  fixedIncidentTS.Add(time.Duration(i) * time.Minute),
		}
	}
	results := []models.ToolResult{
		{ToolName: "get_recent_deployments", Success: true, Payload: recs},
	}

	el, _ := evidence.Assemble(results, fixedIncidentTS)
	assert.Equal(t, 50, countBySource(el.Items, "get_recent_deployments"))
}

func TestAssemble_DeploymentListEmpty(t *testing.T) {
	results := []models.ToolResult{
		{ToolName: "get_recent_deployments", Success: true, Payload: []models.DeploymentRecord{}},
	}

	el, fl := evidence.Assemble(results, fixedIncidentTS)
	assert.Equal(t, 0, countBySource(el.Items, "get_recent_deployments"))
	assert.Empty(t, fl.Failures)
	// No evidence produced → sentinel inserted.
	require.Len(t, el.Items, 1)
	assert.Equal(t, "assembler", el.Items[0].SourceTool)
}

func TestAssemble_DeploymentAllFieldsMissingMarkedUnknown(t *testing.T) {
	recs := []models.DeploymentRecord{
		{ServiceName: "", Version: "", DeployedAt: time.Time{}},
	}
	results := []models.ToolResult{
		{ToolName: "get_recent_deployments", Success: true, Payload: recs},
	}

	el, _ := evidence.Assemble(results, fixedIncidentTS)
	require.Equal(t, 1, countBySource(el.Items, "get_recent_deployments"))

	var item models.EvidenceItem
	for _, it := range el.Items {
		if it.SourceTool == "get_recent_deployments" {
			item = it
		}
	}

	assert.Equal(t, "unknown", item.RawData["service_name"])
	assert.Equal(t, "unknown", item.RawData["version"])
	assert.Equal(t, "unknown", item.RawData["deployed_at"])
	assert.Contains(t, item.Description, "unknown")
}

func TestAssemble_KBImpactSummaryTruncatedTo200Runes(t *testing.T) {
	long := strings.Repeat("a", 500)
	kb := []models.KBResult{
		{
			ArticleID:     "kb-1",
			Title:         "High latency runbook",
			ArticleType:   "TSG",
			ServiceName:   "payments-api",
			ImpactSummary: long,
		},
	}
	results := []models.ToolResult{
		{ToolName: "search_knowledge_base", Success: true, Payload: kb},
	}

	el, _ := evidence.Assemble(results, fixedIncidentTS)
	require.Equal(t, 1, countBySource(el.Items, "search_knowledge_base"))

	var item models.EvidenceItem
	for _, it := range el.Items {
		if it.SourceTool == "search_knowledge_base" {
			item = it
		}
	}

	impact, ok := item.RawData["impact_summary"].(string)
	require.True(t, ok)
	assert.Equal(t, 200, len([]rune(impact)))
}

func TestAssemble_EmptyResultsInsertsSentinel(t *testing.T) {
	el, fl := evidence.Assemble(nil, fixedIncidentTS)
	require.Len(t, el.Items, 1)
	assert.Equal(t, "assembler", el.Items[0].SourceTool)
	assert.Equal(t, "no data was retrieved from any tool", el.Items[0].Description)
	assert.Empty(t, fl.Failures)
}

// -----------------------------------------------------------------------------
// Task 7.2 — Property 4
// -----------------------------------------------------------------------------

// Feature: incident-investigator, Property 4: Evidence list is never empty
func TestProperty4_EvidenceListNeverEmpty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 10).Draw(t, "num_results")
		results := make([]models.ToolResult, 0, n)
		for i := 0; i < n; i++ {
			success := rapid.Bool().Draw(t, fmt.Sprintf("success_%d", i))
			name := rapid.SampledFrom([]string{
				"get_incident",
				"get_recent_deployments",
				"search_knowledge_base",
				"get_service_health:svc",
				"mystery_tool",
			}).Draw(t, fmt.Sprintf("name_%d", i))

			if !success {
				results = append(results, models.ToolResult{
					ToolName:         name,
					Success:          false,
					ErrorMessage:     "boom",
					FailureTimestamp: fixedIncidentTS,
				})
				continue
			}
			// Successful results with nil/simple payloads: assembler should
			// safely ignore payloads it cannot type-assert.
			results = append(results, models.ToolResult{
				ToolName: name,
				Success:  true,
				Payload:  nil,
			})
		}

		el, _ := evidence.Assemble(results, fixedIncidentTS)
		require.GreaterOrEqual(t, len(el.Items), 1)
	})
}

// -----------------------------------------------------------------------------
// Task 7.3 — Property 6
// -----------------------------------------------------------------------------

// Feature: incident-investigator, Property 6: Deployment record truncation preserves recency
func TestProperty6_DeploymentTruncationPreservesRecency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(51, 300).Draw(t, "record_count")

		type stamp struct {
			name string
			at   time.Time
			dist time.Duration
		}
		recs := make([]models.DeploymentRecord, n)
		stamps := make([]stamp, n)
		for i := 0; i < n; i++ {
			// Offset in minutes, both before and after the incident.
			offMin := rapid.IntRange(-100000, 100000).Draw(t, fmt.Sprintf("off_%d", i))
			at := fixedIncidentTS.Add(time.Duration(offMin) * time.Minute)
			name := fmt.Sprintf("svc-%d", i)
			recs[i] = models.DeploymentRecord{
				ServiceName: name,
				Version:     "v1",
				DeployedAt:  at,
			}
			d := at.Sub(fixedIncidentTS)
			if d < 0 {
				d = -d
			}
			stamps[i] = stamp{name: name, at: at, dist: d}
		}

		results := []models.ToolResult{
			{ToolName: "get_recent_deployments", Success: true, Payload: recs},
		}
		el, _ := evidence.Assemble(results, fixedIncidentTS)

		// Collect kept deployment names.
		kept := map[string]bool{}
		keptCount := 0
		for _, it := range el.Items {
			if it.SourceTool != "get_recent_deployments" {
				continue
			}
			keptCount++
			sn, _ := it.RawData["service_name"].(string)
			kept[sn] = true
		}
		require.LessOrEqual(t, keptCount, 50)
		require.Equal(t, 50, keptCount)

		// Independently compute the expected closest-50 by distance (stable).
		byDist := make([]stamp, len(stamps))
		copy(byDist, stamps)
		// stable sort by distance ascending
		for i := 1; i < len(byDist); i++ {
			for j := i; j > 0 && byDist[j-1].dist > byDist[j].dist; j-- {
				byDist[j-1], byDist[j] = byDist[j], byDist[j-1]
			}
		}

		// The kept set must be the smallest-50 by distance. Because ties can
		// make individual membership ambiguous, verify by the distance
		// boundary: every kept record's distance must be <= the 50th smallest
		// distance, and no unkept record has a strictly smaller distance than
		// any kept one.
		cutoff := byDist[49].dist
		for _, s := range stamps {
			if kept[s.name] {
				require.LessOrEqual(t, s.dist, cutoff,
					"kept record %s has distance beyond cutoff", s.name)
			}
		}
		// Also ensure no dropped record is strictly closer than the cutoff
		// while a kept record sits at the cutoff — i.e. all kept distances are
		// among the 50 smallest. Count records strictly below cutoff; they must
		// all be kept.
		for _, s := range stamps {
			if s.dist < cutoff {
				require.True(t, kept[s.name],
					"record %s strictly closer than cutoff was dropped", s.name)
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Task 7.4 — Property 7
// -----------------------------------------------------------------------------

// Feature: incident-investigator, Property 7: Missing deployment fields are marked "unknown"
func TestProperty7_MissingDeploymentFieldsMarkedUnknown(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		hasService := rapid.Bool().Draw(t, "has_service")
		hasVersion := rapid.Bool().Draw(t, "has_version")
		hasDeployedAt := rapid.Bool().Draw(t, "has_deployed_at")

		var rec models.DeploymentRecord
		wantService := "unknown"
		wantVersion := "unknown"

		if hasService {
			rec.ServiceName = "payments-api"
			wantService = "payments-api"
		}
		if hasVersion {
			rec.Version = "v2.3.1"
			wantVersion = "v2.3.1"
		}
		if hasDeployedAt {
			rec.DeployedAt = fixedIncidentTS.Add(5 * time.Minute)
		}

		results := []models.ToolResult{
			{ToolName: "get_recent_deployments", Success: true,
				Payload: []models.DeploymentRecord{rec}},
		}
		el, _ := evidence.Assemble(results, fixedIncidentTS)

		var item models.EvidenceItem
		found := false
		for _, it := range el.Items {
			if it.SourceTool == "get_recent_deployments" {
				item = it
				found = true
			}
		}
		require.True(t, found)

		require.Equal(t, wantService, item.RawData["service_name"])
		require.Equal(t, wantVersion, item.RawData["version"])

		if hasDeployedAt {
			require.Equal(t, rec.DeployedAt.Format(time.RFC3339), item.RawData["deployed_at"])
		} else {
			require.Equal(t, "unknown", item.RawData["deployed_at"])
		}
	})
}

// -----------------------------------------------------------------------------
// Task 7.5 — Property 8
// -----------------------------------------------------------------------------

// Feature: incident-investigator, Property 8: Knowledge base results are scoped to affected services
func TestProperty8_KBResultsScopedToAffectedServices(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		numServices := rapid.IntRange(1, 5).Draw(t, "num_services")
		affected := make([]string, numServices)
		for i := 0; i < numServices; i++ {
			affected[i] = fmt.Sprintf("service-%d", i)
		}
		affectedSet := map[string]bool{}
		for _, s := range affected {
			affectedSet[s] = true
		}

		numKB := rapid.IntRange(0, 10).Draw(t, "num_kb")
		kbs := make([]models.KBResult, numKB)
		for i := 0; i < numKB; i++ {
			svc := rapid.SampledFrom(affected).Draw(t, fmt.Sprintf("kb_svc_%d", i))
			kbs[i] = models.KBResult{
				ArticleID:     fmt.Sprintf("kb-%d", i),
				Title:         fmt.Sprintf("Article %d", i),
				ArticleType:   rapid.SampledFrom([]string{"TSG", "SOP"}).Draw(t, fmt.Sprintf("type_%d", i)),
				ServiceName:   svc,
				ImpactSummary: "some impact",
			}
		}

		results := []models.ToolResult{
			{ToolName: "search_knowledge_base", Success: true, Payload: kbs},
		}
		el, _ := evidence.Assemble(results, fixedIncidentTS)

		for _, it := range el.Items {
			if it.SourceTool != "search_knowledge_base" {
				continue
			}
			sn, ok := it.RawData["service_name"].(string)
			require.True(t, ok)
			require.True(t, affectedSet[sn],
				"KB evidence service %q not in affected services %v", sn, affected)
		}
	})
}
