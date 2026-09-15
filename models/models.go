// Package models defines shared data structures, constants, and error types
// used across all layers of the Incident Investigator Agent.
package models

import (
	"fmt"
	"strings"
	"time"
)

// IncidentContext is the parsed representation of the user's prompt.
type IncidentContext struct {
	RawPrompt        string   `json:"raw_prompt"`
	IncidentID       string   `json:"incident_id"`       // empty if not found
	ContextText      string   `json:"context_text"`      // <= 500 chars
	AffectedServices []string `json:"affected_services"` // extracted from prompt or get_incident result
	SymptomHint      string   `json:"symptom_hint"`      // key symptom phrase for KB lookup, <= 100 chars
}

// ToolResult captures the outcome of a single tool invocation.
type ToolResult struct {
	ToolName         string    `json:"tool_name"`
	Success          bool      `json:"success"`
	Payload          any       `json:"payload,omitempty"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	FailureTimestamp time.Time `json:"failure_timestamp,omitempty"`
}

// IncidentRecord maps to the `incidents` DuckDB table.
type IncidentRecord struct {
	IncidentID       string    `json:"incident_id"`
	Title            string    `json:"title"`
	Severity         string    `json:"severity"`
	AffectedServices []string  `json:"affected_services"` // stored as JSON array in DuckDB
	CreatedAt        time.Time `json:"created_at"`
}

// DeploymentRecord maps to the `deployments` DuckDB table.
type DeploymentRecord struct {
	ServiceName string    `json:"service_name"` // empty string → "unknown" after assembly
	Version     string    `json:"version"`
	DeployedAt  time.Time `json:"deployed_at"`
}

// KBResult maps to the `knowledge_base` DuckDB table.
// Each row represents a TSG or SOP scoped to a specific service.
type KBResult struct {
	ArticleID     string    `json:"article_id"`
	Title         string    `json:"title"`
	ArticleType   string    `json:"article_type"`   // "TSG" or "SOP"
	ServiceName   string    `json:"service_name"`   // service this guide applies to
	SymptomTags   []string  `json:"symptom_tags"`   // e.g. ["high_latency", "5xx_errors"]
	ImpactSummary string    `json:"impact_summary"` // plain-language impact description
	Body          string    `json:"body"`           // full runbook / procedure steps
	SeverityScope string    `json:"severity_scope"` // e.g. "P1,P2"
	LastUpdated   time.Time `json:"last_updated"`
}

// ServiceHealthRecord maps to the `service_health` DuckDB table.
type ServiceHealthRecord struct {
	ServiceName  string `json:"service_name"`
	HealthStatus string `json:"health_status"` // "healthy" | "degraded" | "down" | "unknown"
}

// EvidenceItem is a single normalised piece of evidence derived from a tool result.
type EvidenceItem struct {
	SourceTool  string         `json:"source_tool"`
	Description string         `json:"description"`
	RawData     map[string]any `json:"raw_data,omitempty"`
}

// EvidenceList holds all assembled evidence items.
type EvidenceList struct {
	Items []EvidenceItem `json:"items"`
}

// HasSuccessfulItems returns true when at least one evidence item exists.
func (e EvidenceList) HasSuccessfulItems() bool {
	return len(e.Items) > 0
}

// SuccessfulToolCount returns the number of evidence items.
func (e EvidenceList) SuccessfulToolCount() int {
	return len(e.Items)
}

// FailureMetadata holds details about a failed tool invocation.
type FailureMetadata struct {
	ToolName         string    `json:"tool_name"`
	ErrorMessage     string    `json:"error_message"`
	FailureTimestamp time.Time `json:"failure_timestamp"`
}

// FailedToolsList aggregates failure metadata for all failed tool calls.
type FailedToolsList struct {
	Failures []FailureMetadata `json:"failures"`
}

// ConfidenceLevel indicates how strongly the evidence supports the root cause.
type ConfidenceLevel string

const (
	ConfidenceLow    ConfidenceLevel = "Low"
	ConfidenceMedium ConfidenceLevel = "Medium"
	ConfidenceHigh   ConfidenceLevel = "High"
)

// RecommendedAction is a prioritised action for the on-call responder.
type RecommendedAction struct {
	Priority int    `json:"priority"` // 1 = highest
	Action   string `json:"action"`
	Target   string `json:"target"` // component or owner
}

// InvestigationSummary is the final structured output of an investigation.
type InvestigationSummary struct {
	Summary            string              `json:"summary"`           // <= 200 words
	Evidence           []EvidenceItem      `json:"evidence"`          // len >= 1
	LikelyRootCause    string              `json:"likely_root_cause"` // <= 100 words
	ConfidenceLevel    ConfidenceLevel     `json:"confidence_level"`
	RecommendedActions []RecommendedAction `json:"recommended_actions"` // len >= 1
	FailedTools        []FailureMetadata   `json:"failed_tools"`
}

// Validate checks invariants that the JSON decoder cannot enforce.
func (s InvestigationSummary) Validate() error {
	if strings.TrimSpace(s.Summary) == "" {
		return fmt.Errorf("invalid InvestigationSummary: Summary must not be empty")
	}
	if len(s.Evidence) < 1 {
		return fmt.Errorf("invalid InvestigationSummary: Evidence must contain at least one item")
	}
	if strings.TrimSpace(s.LikelyRootCause) == "" {
		return fmt.Errorf("invalid InvestigationSummary: LikelyRootCause must not be empty")
	}
	switch s.ConfidenceLevel {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		// valid
	default:
		return fmt.Errorf("invalid InvestigationSummary: ConfidenceLevel %q is not one of Low, Medium, High", s.ConfidenceLevel)
	}
	if len(s.RecommendedActions) < 1 {
		return fmt.Errorf("invalid InvestigationSummary: RecommendedActions must contain at least one item")
	}
	return nil
}

// PromptValidationError is returned when the input prompt fails validation.
type PromptValidationError struct {
	Message string
}

func (e *PromptValidationError) Error() string { return e.Message }
