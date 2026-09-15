// Package reasoning wraps the Azure OpenAI LLM call and decodes the structured
// InvestigationSummary response.
package reasoning

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/org/incident-agent/models"
	"github.com/sashabaranov/go-openai"
)

// azureAPIVersion is the Azure OpenAI REST API version used for chat completions.
const azureAPIVersion = "2024-08-01-preview"

// ReasoningEngine wraps an Azure OpenAI client and produces an InvestigationSummary.
type ReasoningEngine struct {
	client *openai.Client
	model  string // Azure deployment name
}

// NewReasoningEngine constructs a ReasoningEngine backed by Azure OpenAI (Azure AI
// Foundry). The endpoint is the Azure resource endpoint (e.g.
// https://my-resource.openai.azure.com) and deployment is the Azure deployment name,
// which is used both as the request model and via the Azure model mapper.
func NewReasoningEngine(apiKey, endpoint, deployment string) *ReasoningEngine {
	cfg := openai.DefaultAzureConfig(apiKey, endpoint)
	cfg.APIVersion = azureAPIVersion
	cfg.AzureModelMapperFunc = func(model string) string { return deployment }
	return &ReasoningEngine{
		client: openai.NewClientWithConfig(cfg),
		model:  deployment,
	}
}

// systemPrompt instructs the model to behave as an incident investigator and emit
// only a JSON object matching the InvestigationSummary schema.
const systemPrompt = `You are an incident investigator. Analyse the provided evidence and produce an investigation summary.

Return ONLY a single JSON object (no prose, no markdown) with exactly these fields:
- "summary": string. A plain-language narrative of the incident context, affected components, and key findings.
- "evidence": array of objects, each with:
    - "source_tool": string (the tool that produced the data point)
    - "description": string (plain-language description of the data point)
- "likely_root_cause": string. The most probable cause. If undetermined, say so explicitly.
- "confidence_level": string, one of exactly "Low", "Medium", or "High".
- "recommended_actions": array of objects, each with:
    - "priority": integer (1 = highest priority)
    - "action": string (a concrete, actionable step)
    - "target": string (the target component or owner)
- "failed_tools": array of objects, each with:
    - "tool_name": string
    - "error_message": string
    - "failure_timestamp": string (RFC 3339 timestamp)

Rules:
- Base all findings ONLY on the evidence provided. Do not invent data.
- If the evidence is empty or insufficient to support a conclusion, set "confidence_level" to "Low".
- Always include at least one entry in "recommended_actions". If no action can be derived from the evidence, default to escalating to the service owner (or the on-call team if no owner is known).`

// investigationSummarySchema is the JSON schema describing InvestigationSummary,
// used for strict structured-output mode.
var investigationSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": { "type": "string" },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "source_tool": { "type": "string" },
          "description": { "type": "string" }
        },
        "required": ["source_tool", "description"],
        "additionalProperties": false
      }
    },
    "likely_root_cause": { "type": "string" },
    "confidence_level": { "type": "string", "enum": ["Low", "Medium", "High"] },
    "recommended_actions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "priority": { "type": "integer" },
          "action": { "type": "string" },
          "target": { "type": "string" }
        },
        "required": ["priority", "action", "target"],
        "additionalProperties": false
      }
    },
    "failed_tools": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "tool_name": { "type": "string" },
          "error_message": { "type": "string" },
          "failure_timestamp": { "type": "string" }
        },
        "required": ["tool_name", "error_message", "failure_timestamp"],
        "additionalProperties": false
      }
    }
  },
  "required": ["summary", "evidence", "likely_root_cause", "confidence_level", "recommended_actions", "failed_tools"],
  "additionalProperties": false
}`)

// Reason calls the Azure OpenAI chat completion endpoint with the assembled
// evidence and failure metadata, then decodes and validates the structured
// InvestigationSummary response.
func (e *ReasoningEngine) Reason(
	ctx context.Context,
	evidence models.EvidenceList,
	failed models.FailedToolsList,
	prompt string,
) (models.InvestigationSummary, error) {
	evidenceJSON, err := json.Marshal(evidence.Items)
	if err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: marshal evidence: %w", err)
	}
	failedJSON, err := json.Marshal(failed.Failures)
	if err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: marshal failed tools: %w", err)
	}

	userMessage := fmt.Sprintf(
		"INCIDENT PROMPT:\n%s\n\nEVIDENCE (JSON array):\n%s\n\nFAILED TOOLS (JSON array):\n%s",
		prompt, string(evidenceJSON), string(failedJSON),
	)

	req := openai.ChatCompletionRequest{
		Model: e.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userMessage},
		},
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   "investigation_summary",
				Schema: investigationSummarySchema,
				Strict: true,
			},
		},
	}

	resp, err := e.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: azure chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: no choices returned")
	}

	return decodeSummary(resp.Choices[0].Message.Content)
}

// decodeSummary unmarshals the LLM response content into an InvestigationSummary
// and validates it. It returns a wrapped decode error on malformed JSON and a
// wrapped validation error when the summary violates its invariants.
func decodeSummary(content string) (models.InvestigationSummary, error) {
	var summary models.InvestigationSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: decode summary: %w", err)
	}
	if err := summary.Validate(); err != nil {
		return models.InvestigationSummary{}, fmt.Errorf("reasoning: validate summary: %w", err)
	}
	return summary, nil
}
