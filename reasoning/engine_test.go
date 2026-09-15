package reasoning

import (
	"testing"

	"github.com/org/incident-agent/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeSummary_ValidFullJSON(t *testing.T) {
	content := `{
		"summary": "Checkout latency spiked following a deployment.",
		"evidence": [
			{"source_tool": "get_incident", "description": "INC-42 severity P1 affecting checkout"},
			{"source_tool": "get_recent_deployments", "description": "checkout v1.2.3 deployed 5m before incident"}
		],
		"likely_root_cause": "Regression introduced by checkout v1.2.3 deployment.",
		"confidence_level": "High",
		"recommended_actions": [
			{"priority": 1, "action": "Roll back checkout to v1.2.2", "target": "checkout"}
		],
		"failed_tools": []
	}`

	summary, err := decodeSummary(content)
	require.NoError(t, err)
	assert.Equal(t, "Checkout latency spiked following a deployment.", summary.Summary)
	assert.Len(t, summary.Evidence, 2)
	assert.Equal(t, "get_incident", summary.Evidence[0].SourceTool)
	assert.Equal(t, "Regression introduced by checkout v1.2.3 deployment.", summary.LikelyRootCause)
	assert.Equal(t, models.ConfidenceHigh, summary.ConfidenceLevel)
	require.Len(t, summary.RecommendedActions, 1)
	assert.Equal(t, 1, summary.RecommendedActions[0].Priority)
	assert.Equal(t, "checkout", summary.RecommendedActions[0].Target)
}

func TestDecodeSummary_EmptyEvidenceFailsValidation(t *testing.T) {
	content := `{
		"summary": "Something happened.",
		"evidence": [],
		"likely_root_cause": "Undetermined.",
		"confidence_level": "Low",
		"recommended_actions": [
			{"priority": 1, "action": "Escalate to on-call", "target": "on-call"}
		],
		"failed_tools": []
	}`

	_, err := decodeSummary(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate summary")
}

func TestDecodeSummary_BogusConfidenceLevelFailsValidation(t *testing.T) {
	content := `{
		"summary": "Something happened.",
		"evidence": [
			{"source_tool": "get_incident", "description": "INC-1"}
		],
		"likely_root_cause": "Undetermined.",
		"confidence_level": "Bogus",
		"recommended_actions": [
			{"priority": 1, "action": "Escalate to on-call", "target": "on-call"}
		],
		"failed_tools": []
	}`

	_, err := decodeSummary(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate summary")
}

func TestDecodeSummary_MalformedJSONFailsDecode(t *testing.T) {
	_, err := decodeSummary("{not json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode summary")
}
