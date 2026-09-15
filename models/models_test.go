package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validSummary returns an InvestigationSummary with all five required fields
// populated correctly. Individual test cases mutate a copy to exercise a single
// violated invariant at a time.
func validSummary() InvestigationSummary {
	return InvestigationSummary{
		Summary: "Checkout service returned 5xx errors following a recent deployment.",
		Evidence: []EvidenceItem{
			{SourceTool: "get_incident", Description: "INC-1234 severity P1 affecting checkout"},
		},
		LikelyRootCause: "Deployment v2.3.1 of checkout introduced a regression.",
		ConfidenceLevel: ConfidenceHigh,
		RecommendedActions: []RecommendedAction{
			{Priority: 1, Action: "Roll back checkout to v2.3.0", Target: "checkout"},
		},
	}
}

func TestValidate_AllFieldsPopulated_ReturnsNil(t *testing.T) {
	s := validSummary()
	require.NoError(t, s.Validate())
}

func TestValidate_EmptyEvidence_ReturnsError(t *testing.T) {
	s := validSummary()
	s.Evidence = nil

	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Evidence")
}

func TestValidate_EmptyRecommendedActions_ReturnsError(t *testing.T) {
	s := validSummary()
	s.RecommendedActions = nil

	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RecommendedActions")
}

func TestValidate_InvalidConfidenceLevel_ReturnsError(t *testing.T) {
	s := validSummary()
	s.ConfidenceLevel = ConfidenceLevel("Bogus")

	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfidenceLevel")
}

func TestValidate_EmptySummary_ReturnsError(t *testing.T) {
	s := validSummary()
	s.Summary = "   "

	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Summary")
}

func TestValidate_EmptyLikelyRootCause_ReturnsError(t *testing.T) {
	s := validSummary()
	s.LikelyRootCause = "  \t\n "

	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LikelyRootCause")
}
