# Implementation Plan: Incident Investigator Agent

## Overview

Build the Incident Investigator Agent in Go using a fan-out/gather/reason architecture. The implementation proceeds in layers: shared data models first, then the DuckDB tool provider, then input validation and prompt analysis, then concurrent tool execution, then evidence assembly, then the reasoning engine, and finally the top-level agent facade that wires everything together. Property-based tests with `pgregory.net/rapid` are placed close to the components they validate.

## Tasks

- [x] 1. Initialise Go module and project structure
  - Run `go mod init` with a suitable module path (e.g. `github.com/org/incident-agent`)
  - Create package directories: `models/`, `tools/`, `evidence/`, `agent/`, `reasoning/`
  - Add `go.mod` dependencies: `github.com/marcboeker/go-duckdb`, `github.com/sashabaranov/go-openai`, `github.com/stretchr/testify`, `pgregory.net/rapid`, `golang.org/x/sync` (for `errgroup`), `go.uber.org/goleak`
  - Create a top-level `main.go` stub (accepts a prompt via `os.Args` or `stdin`, calls `InvestigatorAgent.Investigate`, prints JSON summary)
  - _Requirements: 1.1_

- [x] 2. Define shared data models
  - [x] 2.1 Write all Go structs, constants, and error types in `models/`
    - `IncidentContext`, `ToolResult`, `IncidentRecord`, `DeploymentRecord`, `KBResult`, `ServiceHealthRecord`
    - `EvidenceItem`, `EvidenceList` (with `HasSuccessfulItems()` and `SuccessfulToolCount()` methods)
    - `FailureMetadata`, `FailedToolsList`
    - `ConfidenceLevel` constants (`Low`, `Medium`, `High`), `RecommendedAction`, `InvestigationSummary`
    - `InvestigationSummary.Validate()` method: checks non-empty `Evidence`, non-empty `RecommendedActions`, valid `ConfidenceLevel`, non-empty `Summary`, non-empty `LikelyRootCause`
    - `PromptValidationError` struct with `Error() string`
    - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 8.2, 8.5_

  - [x] 2.2 Write unit tests for `InvestigationSummary.Validate()`
    - All five fields populated → nil
    - Empty `Evidence` slice → error
    - Empty `RecommendedActions` slice → error
    - `ConfidenceLevel` not in enum → error
    - Empty `Summary` → error
    - _Requirements: 7.1_

- [ ] 3. Implement `ToolProvider` interface and `DuckDBToolProvider`
  - [ ] 3.1 Define `ToolProvider` interface in `tools/provider.go`
    - `GetIncident(ctx, incidentID string) (*IncidentRecord, error)`
    - `GetRecentDeployments(ctx, from, to time.Time) ([]DeploymentRecord, error)`
    - `SearchKnowledgeBase(ctx, services []string, symptomHint string) ([]KBResult, error)`
    - `GetServiceHealth(ctx, service string) (*ServiceHealthRecord, error)`
    - _Requirements: 2.1, 3.1, 4.1, 5.1_

  - [ ] 3.2 Implement DuckDB schema bootstrap in `tools/duckdb.go`
    - `NewDuckDBToolProvider(dbPath string) (*DuckDBToolProvider, error)` opens `*sql.DB` via `marcboeker/go-duckdb`
    - `CreateSchema(db *sql.DB) error` runs `CREATE TABLE IF NOT EXISTS` DDL for all four tables: `incidents`, `deployments`, `knowledge_base`, `service_health` (exact schema from design)
    - _Requirements: 2.1, 3.1, 4.1, 5.1_

  - [ ] 3.3 Implement `GetIncident` method
    - Parameterised query: `SELECT * FROM incidents WHERE incident_id = $1`
    - Unmarshal `affected_services` JSON column into `[]string`
    - Return `error` for unknown ID (no rows)
    - _Requirements: 2.1, 2.3_

  - [ ] 3.4 Implement `GetRecentDeployments` method
    - Parameterised query: `SELECT * FROM deployments WHERE deployed_at BETWEEN $1 AND $2 ORDER BY deployed_at DESC`
    - Scan nullable columns safely (service_name, version, deployed_at may be NULL)
    - _Requirements: 3.1, 3.3_

  - [ ] 3.5 Implement `SearchKnowledgeBase` method
    - Query: `SELECT * FROM knowledge_base WHERE service_name = ANY($1) AND (symptom_tags::text ILIKE $2 OR title ILIKE $2) ORDER BY last_updated DESC`
    - Unmarshal `symptom_tags` JSON column into `[]string`
    - _Requirements: 4.1, 4.3_

  - [ ] 3.6 Implement `GetServiceHealth` method
    - Parameterised query: `SELECT * FROM service_health WHERE service_name = $1`
    - _Requirements: 5.1, 5.3_

  - [ ]* 3.7 Write integration tests for `DuckDBToolProvider` using in-memory DuckDB (`:memory:`)
    - `TestMain` creates schema and seeds fixture data
    - `GetIncident` with known ID → correct `IncidentRecord`
    - `GetIncident` with unknown ID → error
    - `GetRecentDeployments` with window containing 2 records → both returned
    - `SearchKnowledgeBase` with matching services and symptom hint → returns matching TSG/SOP rows
    - `GetServiceHealth` with known service → correct `HealthStatus`
    - _Requirements: 2.1, 3.1, 4.1, 5.1_

- [ ] 4. Implement input validation and prompt analysis
  - [ ] 4.1 Implement `ValidatePrompt` in `agent/validate.go`
    - Return `PromptValidationError` for empty string or whitespace-only (after `strings.TrimSpace`)
    - Return `PromptValidationError` for `len(prompt) > 10_000`
    - _Requirements: 1.1, 1.2, 1.3_

  - [ ]* 4.2 Write property test for `ValidatePrompt` — Property 1
    - **Property 1: Whitespace and empty prompts are always rejected**
    - Generate arbitrary whitespace-only strings via `rapid.StringMatching(`^[\s]+$`)`; assert `PromptValidationError` returned
    - **Validates: Requirements 1.2**

  - [ ]* 4.3 Write property test for `ValidatePrompt` — Property 2
    - **Property 2: Over-length prompts are always rejected**
    - Generate strings with `len > 10_000`; assert `PromptValidationError` returned
    - **Validates: Requirements 1.3**

  - [ ]* 4.4 Write unit tests for `ValidatePrompt`
    - Empty string → `PromptValidationError`
    - String of spaces only → `PromptValidationError`
    - String of tabs and newlines → `PromptValidationError`
    - Exactly 10,000 chars → nil
    - 10,001 chars → `PromptValidationError`
    - Valid multi-line prompt → nil
    - _Requirements: 1.1, 1.2, 1.3_

  - [ ] 4.5 Implement `AnalysePrompt` in `agent/analyse.go`
    - Regex heuristic to extract `INC-\d+` as `IncidentID`; empty string if not found
    - Populate `ContextText` (prompt truncated to 500 chars), `SymptomHint` (key phrase ≤ 100 chars)
    - `AffectedServices` defaults to empty slice (populated later from `GetIncident` result if empty)
    - _Requirements: 2.1, 2.4, 4.1_

- [ ] 5. Checkpoint — validate models, tools, and validation layer
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 6. Implement `ConcurrentToolRunner`
  - [ ] 6.1 Write `ConcurrentToolRunner` in `agent/runner.go`
    - `RunAll(ctx context.Context, ic IncidentContext) []ToolResult`
    - Use `sync.WaitGroup` with one goroutine per tool call; wrap each in `context.WithTimeout(ctx, 10*time.Second)`
    - Launch: `get_incident`, `get_recent_deployments`, `search_knowledge_base`, and one goroutine per service in `ic.AffectedServices` for `get_service_health`
    - Collect results into `[]ToolResult` via a mutex-protected slice
    - All goroutine failures materialise as `ToolResult{Success: false}` — `RunAll` itself never returns an error
    - Populate `FailureTimestamp` with `time.Now()` on failure
    - _Requirements: 9.2, 9.3, 9.4, 8.1_

  - [ ]* 6.2 Write unit tests for `ConcurrentToolRunner`
    - All tools succeed → 4+ `ToolResult{Success: true}` returned
    - One tool returns error → that result has `Success: false`, others succeed
    - One tool mock sleeps > 10 s → timeout failure recorded, others complete
    - Global context cancelled mid-flight → all in-flight goroutines return failure items
    - _Requirements: 9.2, 9.3, 8.1_

  - [ ]* 6.3 Write property test for `ConcurrentToolRunner` — Property 3
    - **Property 3: Tool failures never suppress investigation**
    - For any random subset of tool mocks returning errors, `RunAll` returns a `[]ToolResult` (never panics or returns error) and successful tool results are present
    - **Validates: Requirements 2.2, 3.2, 4.2, 5.2, 8.1**

  - [ ]* 6.4 Write property test for `ConcurrentToolRunner` — Property 11
    - **Property 11: Failure metadata always captures all required fields**
    - For any tool mock that returns an error or context timeout, the resulting `ToolResult` has non-empty `ToolName`, non-zero `FailureTimestamp`, and non-empty `ErrorMessage`
    - **Validates: Requirements 8.5**

  - [ ]* 6.5 Write property test for `ConcurrentToolRunner` — Property 12
    - **Property 12: Knowledge base is called at most once per investigation**
    - Instrument a counting mock `ToolProvider`; run `RunAll` with arbitrary `IncidentContext` values; assert `SearchKnowledgeBase` call count equals exactly 1
    - **Validates: Requirements 4.5**

- [ ] 7. Implement `EvidenceAssembler`
  - [ ] 7.1 Write `Assemble` in `evidence/assembler.go`
    - Signature: `Assemble(results []ToolResult, incidentTS time.Time) (EvidenceList, FailedToolsList)`
    - Incident evidence: extract `IncidentID`, `Severity`, `AffectedServices` from `get_incident` payload
    - Deployment records: sort by `|record.DeployedAt - incidentTS|`, keep at most 50; substitute `"unknown"` for nil/zero `ServiceName`, `Version`, `DeployedAt`
    - KB results: include all returned TSG/SOP rows; populate `EvidenceItem` with `ArticleType`, `Title`, `ServiceName`, and `ImpactSummary` truncated to 200 chars
    - Service health: map each `ServiceHealthRecord` to an `EvidenceItem`
    - Failed tool calls: populate `FailedToolsList` with `FailureMetadata` for each `ToolResult{Success: false}`
    - Sentinel: if assembled `EvidenceList.Items` is empty, insert `EvidenceItem{SourceTool: "assembler", Description: "no data was retrieved from any tool"}`
    - _Requirements: 2.3, 3.3, 3.4, 3.5, 4.3, 5.3, 7.3, 8.2, 8.5_

  - [ ]* 7.2 Write property test for `EvidenceAssembler` — Property 4
    - **Property 4: Evidence list is never empty**
    - For any generated `[]ToolResult` (including all-failure), `Assemble` returns an `EvidenceList` with `len(Items) >= 1`
    - **Validates: Requirements 7.3, 8.4**

  - [ ]* 7.3 Write property test for `EvidenceAssembler` — Property 6
    - **Property 6: Deployment record truncation preserves recency**
    - Generate arbitrary `[]DeploymentRecord` with `len > 50` and a random `incidentTS`; assert result has `<= 50` records and they are the 50 closest to `incidentTS`
    - **Validates: Requirements 3.5**

  - [ ]* 7.4 Write property test for `EvidenceAssembler` — Property 7
    - **Property 7: Missing deployment fields are marked "unknown"**
    - Generate `DeploymentRecord` values with arbitrary nil/zero combinations of `ServiceName`, `Version`, `DeployedAt`; assert assembled `EvidenceItem` represents each missing field as `"unknown"`
    - **Validates: Requirements 3.4**

  - [ ]* 7.5 Write property test for `EvidenceAssembler` — Property 8
    - **Property 8: Knowledge base results are scoped to affected services**
    - For any KB result set returned by tool mocks, all included `EvidenceItem` records have a `ServiceName` that appears in the `AffectedServices` list passed to the runner
    - **Validates: Requirements 4.3**

  - [ ]* 7.6 Write unit tests for `EvidenceAssembler`
    - Deployment list of exactly 50, 51, and 0 records; verify count and recency ordering
    - Record with all three fields missing → all shown as `"unknown"`
    - `ImpactSummary` > 200 chars → truncated to exactly 200
    - Empty `[]ToolResult` → sentinel evidence item inserted
    - _Requirements: 3.3, 3.4, 3.5, 7.3_

- [ ] 8. Checkpoint — validate runner and assembler
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 9. Implement `ReasoningEngine`
  - [ ] 9.1 Write `ReasoningEngine` in `reasoning/engine.go`
    - `NewReasoningEngine(apiKey, model string) *ReasoningEngine` — wraps `sashabaranov/go-openai`
    - `Reason(ctx context.Context, evidence EvidenceList, failed FailedToolsList, prompt string) (InvestigationSummary, error)`
    - Build system prompt instructing the LLM to produce a structured `InvestigationSummary`; include evidence and failure metadata as JSON in the user message
    - Set `ResponseFormat` to `openai.ResponseFormatJSONSchema` with the `InvestigationSummary` JSON schema
    - Unmarshal response content into `InvestigationSummary` using `encoding/json`
    - Call `summary.Validate()` before returning; wrap validation error and return it if non-nil
    - Handle LLM call error and JSON decode error: return wrapped error, no partial summary emitted
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7_

  - [ ]* 9.2 Write unit tests for `ReasoningEngine` using a mock LLM client
    - Inject a `mockReasoningEngine` (interface substitution) returning a canned `InvestigationSummary`
    - Valid canned response → `InvestigationSummary` returned, no error
    - Canned response missing `Evidence` → `Validate()` error propagated
    - Canned response with invalid `ConfidenceLevel` → error
    - _Requirements: 7.1, 6.2_

- [ ] 10. Implement `InvestigatorAgent` facade and wire all components
  - [ ] 10.1 Write `InvestigatorAgent` in `agent/agent.go`
    - `NewInvestigatorAgent(provider ToolProvider, engine *ReasoningEngine) *InvestigatorAgent`
    - `Investigate(ctx context.Context, prompt string) (InvestigationSummary, error)`:
      1. Call `ValidatePrompt` — return `PromptValidationError` immediately on failure
      2. Derive `incidentTS` (use `time.Now()` as default; override from `GetIncident` result if available)
      3. Wrap `ctx` with `context.WithTimeout(ctx, 30*time.Second)` as global deadline
      4. Call `AnalysePrompt` → `IncidentContext`
      5. Call `runner.RunAll` with the 30-second context
      6. Call `evidence.Assemble` with tool results and `incidentTS`
      7. Call `engine.Reason` with evidence, failed tools, and raw prompt
      8. Return `InvestigationSummary` (or wrapped `error` from `Reason`)
    - _Requirements: 1.1, 1.2, 1.3, 1.4, 9.1, 9.4_

  - [ ]* 10.2 Write property test for `InvestigatorAgent` — Property 9
    - **Property 9: All-tools-fail yields Low confidence and a connectivity action**
    - Inject a `ToolProvider` mock where all methods return errors; inject a mock `ReasoningEngine` that reflects `ConfidenceLevel = Low` and a connectivity `RecommendedAction`; run `Investigate` with arbitrary valid prompts; assert `ConfidenceLevel = Low` and at least one `RecommendedAction` present
    - **Validates: Requirements 8.4**

  - [ ]* 10.3 Write property test for `InvestigatorAgent` — Property 10
    - **Property 10: Recommended actions list is never empty**
    - For any generated tool outcome combination, the returned `InvestigationSummary.RecommendedActions` has `len >= 1`
    - **Validates: Requirements 7.6, 7.7**

  - [ ]* 10.4 Write property test for `InvestigatorAgent` — Property 13
    - **Property 13: InvestigationSummary always contains all five required fields**
    - For any tool outcome combination, all five fields (`Summary`, `Evidence`, `LikelyRootCause`, `ConfidenceLevel`, `RecommendedActions`) are present and non-zero
    - **Validates: Requirements 7.1**

  - [ ]* 10.5 Write property test for `InvestigatorAgent` — Property 5
    - **Property 5: Confidence level is determined by corroboration count**
    - Generate arbitrary `EvidenceList` values with varying corroboration counts; verify the `ConfidenceLevel` assignment logic in the reasoning engine mock matches the rules (High / Medium / Low) as defined in the design
    - **Validates: Requirements 6.2, 6.3, 6.5, 7.5**

  - [ ]* 10.6 Write unit tests for `InvestigatorAgent`
    - Empty prompt → `PromptValidationError`, no tool calls made
    - Whitespace-only prompt → `PromptValidationError`
    - Valid prompt with all tools succeeding → `InvestigationSummary` returned
    - Valid prompt with one tool failing → summary still returned, `FailedTools` contains that tool
    - _Requirements: 1.2, 1.3, 8.1, 8.2_

- [ ] 11. Write integration tests
  - [ ] 11.1 End-to-end integration test against in-memory DuckDB with fixture data
    - Seed all four tables; call `InvestigatorAgent.Investigate` with a realistic prompt containing `INC-\d+`; assert `InvestigationSummary` is returned within 30 seconds
    - _Requirements: 9.1_

  - [ ]* 11.2 Partial failure integration test
    - Seed DB with 2 of 4 tables dropped; call `Investigate`; assert `FailedTools` lists exactly those 2 tools with names and non-empty error messages
    - _Requirements: 8.1, 8.2_

  - [ ]* 11.3 All-tools-fail integration test
    - Empty DB (no tables); call `Investigate`; assert `ConfidenceLevel = Low` and `RecommendedActions` contains a connectivity step
    - _Requirements: 8.4_

  - [ ]* 11.4 Concurrent execution timing test
    - Instrument `DuckDBToolProvider` methods with timestamps; call `Investigate`; assert all four queries started within 500 ms of each other
    - _Requirements: 9.2_

  - [ ]* 11.5 Stage 2 pluggability smoke test
    - Implement a `MockHTTPToolProvider` (no real HTTP) satisfying the `ToolProvider` interface; inject it into `InvestigatorAgent`; call `Investigate`; assert it compiles and returns an `InvestigationSummary`
    - _Requirements: implicitly validates interface pluggability_

- [ ] 12. Goroutine leak detection
  - [ ] 12.1 Add `go.uber.org/goleak` leak check to runner and integration tests
    - Call `goleak.VerifyNone(t)` (or use `goleak.VerifyTestMain`) in `TestMain` for the `agent` package
    - Confirm no goroutines leak after `RunAll` returns or when the global context is cancelled
    - _Requirements: 9.3, 9.4_

- [ ] 13. Final checkpoint — full test suite passes
  - Run `go test ./...` and confirm all tests pass with no goroutine leaks, ask the user if questions arise.

## Notes

- Tasks marked with `*` are optional and can be skipped for a faster MVP build
- Each task references specific requirements for traceability
- Property tests use `pgregory.net/rapid` and must run a minimum of 100 iterations (`rapid.Check`)
- Unit tests use `github.com/stretchr/testify/assert` and `/require`
- Integration tests use an in-memory DuckDB instance (`:memory:`) seeded in `TestMain`
- `SearchKnowledgeBase` must never be retried — Property 12 enforces exactly-once semantics
- The `ToolProvider` interface is the sole seam between the agent and its data sources; Stage 2 is a drop-in swap of the implementation with no changes to agent, evidence, or reasoning code
- `EvidenceAssembler` requires `incidentTS` — derive it from the `GetIncident` result if available, otherwise fall back to `time.Now()` at the start of `Investigate`

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1"] },
    { "id": 1, "tasks": ["2.1", "3.1"] },
    { "id": 2, "tasks": ["2.2", "3.2", "4.1", "4.5"] },
    { "id": 3, "tasks": ["3.3", "3.4", "3.5", "3.6", "4.2", "4.3", "4.4"] },
    { "id": 4, "tasks": ["3.7", "6.1"] },
    { "id": 5, "tasks": ["6.2", "6.3", "6.4", "6.5", "7.1"] },
    { "id": 6, "tasks": ["7.2", "7.3", "7.4", "7.5", "7.6", "9.1"] },
    { "id": 7, "tasks": ["9.2", "10.1"] },
    { "id": 8, "tasks": ["10.2", "10.3", "10.4", "10.5", "10.6"] },
    { "id": 9, "tasks": ["11.1", "12.1"] },
    { "id": 10, "tasks": ["11.2", "11.3", "11.4", "11.5"] }
  ]
}
```
