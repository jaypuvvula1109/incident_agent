# Design Document

## Overview

The Incident Investigator Agent is a Go-based AI agent that accepts a natural-language incident prompt and autonomously orchestrates four read-only data tools — `GetIncident`, `GetRecentDeployments`, `SearchKnowledgeBase`, and `GetServiceHealth` — to gather evidence and synthesize a structured `InvestigationSummary`.

**Stage 1 (this document)**: All four tools are backed by a local DuckDB database using the `marcboeker/go-duckdb` driver. No external HTTP or API calls are made. The tool layer is accessed exclusively through a `ToolProvider` interface, making Stage 2 a clean swap of the implementation without touching the agent logic.

**Stage 2 (future, out of scope)**: The `ToolProvider` implementation is replaced with one that calls real external services. Nothing in the agent, evidence, or reasoning layers changes.

**Reasoning provider**: Stage 1 uses Azure OpenAI (Azure AI Foundry) as the reasoning provider, configured via the `AZURE_OPENAI_ENDPOINT`, `AZURE_OPENAI_API_KEY`, and `AZURE_OPENAI_DEPLOYMENT` environment variables.

The key design goals are:

- **Correctness** — the output schema is validated at the Go struct level; the LLM cannot produce a malformed response that passes the decoder.
- **Resilience** — any subset of tool failures still yields a useful summary; the agent never hard-fails due to a single tool error.
- **Speed** — all independent tools are called concurrently using `errgroup`; per-tool and global timeouts are enforced via `context.WithTimeout`.
- **Observability** — every tool call's result and failure metadata is captured before reasoning begins.
- **Pluggability** — the `ToolProvider` interface decouples the agent from the storage layer, enabling a clean Stage 2 swap.

---

## Architecture

The system follows a **fan-out / gather / reason** pattern:

```
User Prompt
    │
    ▼
┌──────────────────────────────────────────┐
│            InputValidator                │
│  (length check, whitespace rejection)    │
└──────────────┬───────────────────────────┘
               │ valid prompt
               ▼
┌──────────────────────────────────────────┐
│           PromptAnalyser                 │
│  (extract incident ID from free text)    │
└──────────────┬───────────────────────────┘
               │ IncidentContext
    ┌──────────┴────────────────────────┐
    │       ConcurrentToolRunner        │  (errgroup + context.WithTimeout)
    │                                   │
    │  ┌──────────────────────────┐     │
    │  │   GetIncident()          │     │
    │  ├──────────────────────────┤     │
    │  │ GetRecentDeployments()   │     │
    │  ├──────────────────────────┤     │
    │  │ SearchKnowledgeBase()    │     │
    │  ├──────────────────────────┤     │
    │  │  GetServiceHealth()      │     │
    │  │  (one goroutine/service) │     │
    │  └──────────────────────────┘     │
    └──────────┬────────────────────────┘
               │ []ToolResult (successes + failures)
               ▼
┌──────────────────────────────────────────┐
│         EvidenceAssembler                │
│  (normalise, tag source, collect         │
│   FailureMetadata for failed calls)      │
└──────────────┬───────────────────────────┘
               │ EvidenceList, FailedToolsList
               ▼
┌──────────────────────────────────────────┐
│       ReasoningEngine (LLM)              │
│  Azure OpenAI structured output (JSON schema) │
│  schema → decoded into InvestigationSummary │
└──────────────┬───────────────────────────┘
               │
               ▼
        InvestigationSummary
```

The `ConcurrentToolRunner` launches one goroutine per tool call inside an `errgroup.Group`. Each goroutine wraps its tool call in a child `context.WithTimeout(ctx, 10*time.Second)`. The group's parent context carries the global 30-second deadline. When the parent context is cancelled, any in-flight tool goroutines are interrupted at their next context check. Results and errors are collected into a `[]ToolResult` via a mutex-protected slice (or a buffered channel); the runner never returns an error itself — all failures are materialised as `ToolResult{Success: false}`.

---

## Components and Interfaces

### 1. `ToolProvider` Interface

The single seam between the agent and its data sources. Stage 1 implements this with DuckDB; Stage 2 replaces the implementation with HTTP clients.

```go
// ToolProvider is the pluggable data-access layer.
// Stage 1: DuckDBToolProvider. Stage 2: HTTPToolProvider.
type ToolProvider interface {
    GetIncident(ctx context.Context, incidentID string) (*IncidentRecord, error)
    GetRecentDeployments(ctx context.Context, from, to time.Time) ([]DeploymentRecord, error)
    SearchKnowledgeBase(ctx context.Context, services []string, symptomHint string) ([]KBResult, error)
    GetServiceHealth(ctx context.Context, service string) (*ServiceHealthRecord, error)
}
```

### 2. `DuckDBToolProvider` (Stage 1 implementation)

Holds a `*sql.DB` opened against the local DuckDB file. Each method executes a parameterised SQL query against its dedicated table.

```go
type DuckDBToolProvider struct {
    db *sql.DB
}

func NewDuckDBToolProvider(dbPath string) (*DuckDBToolProvider, error)
func (p *DuckDBToolProvider) GetIncident(ctx context.Context, incidentID string) (*IncidentRecord, error)
func (p *DuckDBToolProvider) GetRecentDeployments(ctx context.Context, from, to time.Time) ([]DeploymentRecord, error)
func (p *DuckDBToolProvider) SearchKnowledgeBase(ctx context.Context, services []string, symptomHint string) ([]KBResult, error)
func (p *DuckDBToolProvider) GetServiceHealth(ctx context.Context, service string) (*ServiceHealthRecord, error)
```

### 3. `InputValidator`

Stateless function. Called before any async work begins.

```go
// ValidatePrompt returns PromptValidationError on failure, nil otherwise.
func ValidatePrompt(prompt string) error
```

Returns `PromptValidationError` if:
- `prompt` is empty or whitespace-only (after `strings.TrimSpace`)
- `len(prompt) > 10_000`

### 4. `PromptAnalyser`

Extracts a structured `IncidentContext` from the raw prompt.

```go
// AnalysePrompt extracts incident_id (may be empty) and context text from the prompt.
func AnalysePrompt(prompt string) IncidentContext
```

Uses a regex heuristic (e.g., `INC-\d+`) first; falls back to an optional lightweight LLM call for more complex prompts.

```go
type IncidentContext struct {
    RawPrompt        string   `json:"raw_prompt"`
    IncidentID       string   `json:"incident_id"`        // empty string if not extractable
    ContextText      string   `json:"context_text"`       // prompt truncated to 500 chars for KB query
    AffectedServices []string `json:"affected_services"`  // extracted from prompt or get_incident result
    SymptomHint      string   `json:"symptom_hint"`       // key symptom phrase for KB lookup, <= 100 chars
}
```

### 5. `ConcurrentToolRunner`

Owns the fan-out logic. Takes a `ToolProvider` and an `IncidentContext`.

```go
type ConcurrentToolRunner struct {
    provider ToolProvider
}

// RunAll executes all tool calls concurrently and returns every result.
// The passed ctx carries the global 30-second deadline.
// Failures are returned as ToolResult{Success: false}, never as errors.
func (r *ConcurrentToolRunner) RunAll(ctx context.Context, ic IncidentContext) []ToolResult
```

Internal implementation sketch:

```go
func (r *ConcurrentToolRunner) RunAll(ctx context.Context, ic IncidentContext) []ToolResult {
    var mu sync.Mutex
    var results []ToolResult

    collect := func(res ToolResult) {
        mu.Lock()
        results = append(results, res)
        mu.Unlock()
    }

    var wg sync.WaitGroup

    launch := func(name string, fn func(context.Context) (any, error)) {
        wg.Add(1)
        go func() {
            defer wg.Done()
            tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
            defer cancel()
            payload, err := fn(tctx)
            if err != nil {
                collect(ToolResult{
                    ToolName:         name,
                    Success:          false,
                    ErrorMessage:     err.Error(),
                    FailureTimestamp: time.Now(),
                })
                return
            }
            collect(ToolResult{ToolName: name, Success: true, Payload: payload})
        }()
    }

    // Launch all four tools (plus one goroutine per affected service for GetServiceHealth)
    launch("get_incident", func(c context.Context) (any, error) {
        return r.provider.GetIncident(c, ic.IncidentID)
    })
    launch("get_recent_deployments", func(c context.Context) (any, error) {
        to := incidentTimestamp(ic)
        return r.provider.GetRecentDeployments(c, to.Add(-60*time.Minute), to)
    })
    launch("search_knowledge_base", func(c context.Context) (any, error) {
        return r.provider.SearchKnowledgeBase(c, ic.AffectedServices, ic.SymptomHint)
    })
    // Service health goroutines are added after GetIncident resolves;
    // in practice they are launched optimistically from the known services list
    // embedded in IncidentContext if pre-extracted, or deferred to EvidenceAssembler phase.

    wg.Wait()
    return results
}
```

### 6. `EvidenceAssembler`

Converts `[]ToolResult` into an `EvidenceList` and a `FailedToolsList`.

```go
func Assemble(results []ToolResult) (EvidenceList, FailedToolsList)
```

Responsibilities:
- Deployment records: sort by `|record.Timestamp - incidentTimestamp|`, keep at most 50, substitute `"unknown"` for missing `ServiceName`, `Version`, or `Timestamp`.
- KB results: include all returned TSG/SOP results; for each result, populate the Evidence item with the `ArticleType`, `Title`, `ServiceName`, and `ImpactSummary` (truncated to 200 characters). The `ImpactSummary` is the primary field the LLM uses to understand expected incident impact per service.
- Incident evidence: extract `IncidentID`, `Severity`, and `AffectedServices`.
- If evidence list would be empty after assembly, insert a sentinel item: `EvidenceItem{SourceTool: "assembler", Description: "no data was retrieved from any tool"}`.

### 7. `ReasoningEngine`

Wraps the LLM call. Receives `EvidenceList` + `FailedToolsList`, returns `InvestigationSummary`.

```go
type ReasoningEngine struct {
    client *openai.Client  // sashabaranov/go-openai (Azure config)
    model  string
}

func NewReasoningEngine(apiKey, endpoint, deployment string) *ReasoningEngine

func (e *ReasoningEngine) Reason(
    ctx context.Context,
    evidence EvidenceList,
    failed FailedToolsList,
    prompt string,
) (InvestigationSummary, error)
```

The client is built via `openai.DefaultAzureConfig(apiKey, endpoint)` with `APIVersion = "2024-08-01-preview"` and an `AzureModelMapperFunc` that maps the request model to the Azure deployment name.

Structured output is enforced by:
1. Providing a JSON schema for `InvestigationSummary` in the request via `ChatCompletionResponseFormatTypeJSONSchema` (strict `json_schema` mode, supported by go-openai v1.42.1).
2. Unmarshalling the response into `InvestigationSummary` using `encoding/json`.
3. Running `Validate()` on the result before returning (checks required field lengths and non-empty lists).

### 8. `InvestigatorAgent` (Facade)

Top-level entry point. Composes all components and enforces the global 30-second deadline.

```go
type InvestigatorAgent struct {
    provider ToolProvider
    engine   *ReasoningEngine
}

func NewInvestigatorAgent(provider ToolProvider, engine *ReasoningEngine) *InvestigatorAgent

// Investigate is the single public method. Returns PromptValidationError for invalid input.
func (a *InvestigatorAgent) Investigate(ctx context.Context, prompt string) (InvestigationSummary, error)
```

Internal flow:

```go
func (a *InvestigatorAgent) Investigate(ctx context.Context, prompt string) (InvestigationSummary, error) {
    if err := ValidatePrompt(prompt); err != nil {
        return InvestigationSummary{}, err
    }
    gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    ic := AnalysePrompt(prompt)
    runner := ConcurrentToolRunner{provider: a.provider}
    results := runner.RunAll(gctx, ic)
    evidence, failed := Assemble(results)
    return a.engine.Reason(gctx, evidence, failed, prompt)
}
```

---

## Data Models

All models are Go structs with JSON tags. Validation is performed via explicit `Validate() error` methods rather than struct tags, enabling rich error messages.

### Input Models

```go
// IncidentContext is the parsed representation of the user's prompt.
type IncidentContext struct {
    RawPrompt        string   `json:"raw_prompt"`
    IncidentID       string   `json:"incident_id"`        // empty if not found
    ContextText      string   `json:"context_text"`       // <= 500 chars
    AffectedServices []string `json:"affected_services"`  // extracted from prompt or get_incident result
    SymptomHint      string   `json:"symptom_hint"`       // key symptom phrase for KB lookup, <= 100 chars
}
```

### Tool Layer Models

```go
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
// Each row represents a TSG (Troubleshooting Guide) or SOP (Standard Operating Procedure)
// scoped to a specific service.
type KBResult struct {
    ArticleID     string    `json:"article_id"`
    Title         string    `json:"title"`
    ArticleType   string    `json:"article_type"`   // "TSG" or "SOP"
    ServiceName   string    `json:"service_name"`   // service this guide applies to
    SymptomTags   []string  `json:"symptom_tags"`   // e.g. ["high_latency", "5xx_errors"]
    ImpactSummary string    `json:"impact_summary"` // plain-language impact description for this service
    Body          string    `json:"body"`           // full runbook / procedure steps
    SeverityScope string    `json:"severity_scope"` // e.g. "P1,P2"
    LastUpdated   time.Time `json:"last_updated"`
}

// ServiceHealthRecord maps to the `service_health` DuckDB table.
type ServiceHealthRecord struct {
    ServiceName  string `json:"service_name"`
    HealthStatus string `json:"health_status"` // "healthy" | "degraded" | "down" | "unknown"
}
```

### Evidence Models

```go
type EvidenceItem struct {
    SourceTool  string         `json:"source_tool"`
    Description string         `json:"description"`
    RawData     map[string]any `json:"raw_data,omitempty"`
}

type EvidenceList struct {
    Items []EvidenceItem `json:"items"`
}

func (e EvidenceList) HasSuccessfulItems() bool
func (e EvidenceList) SuccessfulToolCount() int
```

### Failure Metadata

```go
type FailureMetadata struct {
    ToolName         string    `json:"tool_name"`
    ErrorMessage     string    `json:"error_message"`
    FailureTimestamp time.Time `json:"failure_timestamp"`
}

type FailedToolsList struct {
    Failures []FailureMetadata `json:"failures"`
}
```

### Output Model

```go
type ConfidenceLevel string

const (
    ConfidenceLow    ConfidenceLevel = "Low"
    ConfidenceMedium ConfidenceLevel = "Medium"
    ConfidenceHigh   ConfidenceLevel = "High"
)

type RecommendedAction struct {
    Priority int    `json:"priority"` // 1 = highest
    Action   string `json:"action"`
    Target   string `json:"target"` // component or owner
}

type InvestigationSummary struct {
    Summary           string              `json:"summary"`            // <= 200 words
    Evidence          []EvidenceItem      `json:"evidence"`           // len >= 1
    LikelyRootCause   string              `json:"likely_root_cause"`  // <= 100 words
    ConfidenceLevel   ConfidenceLevel     `json:"confidence_level"`
    RecommendedActions []RecommendedAction `json:"recommended_actions"` // len >= 1
    FailedTools       []FailureMetadata   `json:"failed_tools"`
}

// Validate checks invariants that the JSON decoder cannot enforce.
func (s InvestigationSummary) Validate() error
```

### Error Model

```go
// PromptValidationError is returned when the input prompt fails validation.
type PromptValidationError struct {
    Message string
}
func (e *PromptValidationError) Error() string { return e.Message }
```

---

## DuckDB Schema

All four tables live in a single DuckDB file (default path: `./data/incidents.duckdb`).

> The `knowledge_base` table is a **TSG/SOP store** — a collection of Troubleshooting Guides and Standard Operating Procedures scoped to individual services. When an incident is detected, `SearchKnowledgeBase` is called with the list of affected services extracted from the incident record, plus a symptom hint derived from the prompt. Matching TSGs/SOPs are surfaced as Evidence items; their `impact_summary` fields feed directly into the LLM reasoning step, providing service-specific context about what this type of incident pattern typically means and what actions to take.

```sql
-- Incident records
CREATE TABLE IF NOT EXISTS incidents (
    incident_id        TEXT PRIMARY KEY,
    title              TEXT NOT NULL,
    severity           TEXT NOT NULL CHECK (severity IN ('P1','P2','P3','P4')),
    affected_services  JSON NOT NULL,   -- JSON array of service name strings
    created_at         TIMESTAMPTZ NOT NULL
);

-- Deployment history
CREATE TABLE IF NOT EXISTS deployments (
    deployment_id  TEXT PRIMARY KEY,
    service_name   TEXT,               -- nullable; missing → "unknown" in assembler
    version        TEXT,               -- nullable
    deployed_at    TIMESTAMPTZ,        -- nullable
    deployed_by    TEXT
);

-- TSG/SOP store: troubleshooting guides and SOPs scoped per service
CREATE TABLE IF NOT EXISTS knowledge_base (
    article_id     TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    article_type   TEXT NOT NULL CHECK (article_type IN ('TSG', 'SOP')),
    service_name   TEXT NOT NULL,        -- which service this guide applies to
    symptom_tags   JSON NOT NULL,        -- e.g. ["high_latency", "5xx_errors", "db_timeout"]
    impact_summary TEXT NOT NULL,        -- plain-language description of incident impact for this service
    body           TEXT NOT NULL,        -- full runbook / procedure steps
    severity_scope TEXT,                 -- e.g. "P1,P2" — which severities this guide applies to
    last_updated   TIMESTAMPTZ NOT NULL
);

-- Live service health snapshots
CREATE TABLE IF NOT EXISTS service_health (
    service_name   TEXT NOT NULL,
    health_status  TEXT NOT NULL CHECK (health_status IN ('healthy','degraded','down','unknown')),
    last_checked   TIMESTAMPTZ NOT NULL,
    details        TEXT,
    PRIMARY KEY (service_name)
);
```

**Query patterns per tool:**

- `GetIncident`: `SELECT * FROM incidents WHERE incident_id = $1`
- `GetRecentDeployments`: `SELECT * FROM deployments WHERE deployed_at BETWEEN $1 AND $2 ORDER BY deployed_at DESC`
- `SearchKnowledgeBase`: `SELECT * FROM knowledge_base WHERE service_name = ANY($affected_services) AND (symptom_tags::text ILIKE $symptomHint OR title ILIKE $symptomHint) ORDER BY last_updated DESC`
- `GetServiceHealth`: `SELECT * FROM service_health WHERE service_name = $1`

---

## Correctness Properties

*A property is a characteristic or behavior that should hold true across all valid executions of a system — essentially, a formal statement about what the system should do. Properties serve as the bridge between human-readable specifications and machine-verifiable correctness guarantees.*

### Property 1: Whitespace and empty prompts are always rejected

*For any* string composed entirely of whitespace characters (including the empty string), calling `ValidatePrompt` SHALL return a `PromptValidationError` and the investigation workflow SHALL NOT begin.

**Validates: Requirements 1.2**

---

### Property 2: Over-length prompts are always rejected

*For any* string whose length exceeds 10,000 characters, calling `ValidatePrompt` SHALL return a `PromptValidationError` and the investigation workflow SHALL NOT begin.

**Validates: Requirements 1.3**

---

### Property 3: Tool failures never suppress investigation

*For any* subset of tools that return errors or timeout, the remaining successful tool results SHALL still appear in the `EvidenceList`, and an `InvestigationSummary` SHALL be returned (not an error).

**Validates: Requirements 2.2, 3.2, 4.2, 5.2, 8.1**

---

### Property 4: Evidence list is never empty

*For any* investigation run (regardless of tool outcomes), `InvestigationSummary.Evidence` SHALL contain at least one item — even if that item states that no data was retrieved.

**Validates: Requirements 7.3, 8.4**

---

### Property 5: Confidence level is determined by corroboration count

*For any* `EvidenceList`, the assigned `ConfidenceLevel` SHALL satisfy:
- `High` iff two or more evidence items independently support the same root cause without contradiction
- `Medium` iff at least one successful evidence item supports the root cause but others are absent or inconclusive
- `Low` iff no successful tool responses exist, all evidence is inconclusive, or two or more items attribute the root cause to mutually exclusive conditions

**Validates: Requirements 6.2, 6.3, 6.5, 7.5**

---

### Property 6: Deployment record truncation preserves recency

*For any* list of deployment records of arbitrary size, `EvidenceAssembler` SHALL include at most 50 records, and the selected records SHALL be the 50 with timestamps closest to the incident timestamp.

**Validates: Requirements 3.5**

---

### Property 7: Missing deployment fields are marked "unknown"

*For any* deployment record that is missing one or more of `ServiceName`, `Version`, or `DeployedAt`, the assembled `EvidenceItem` SHALL include the available fields and represent the missing fields as the string `"unknown"`.

**Validates: Requirements 3.4**

---

### Property 8: Knowledge base results are scoped to affected services

*For any* investigation run, all TSG/SOP results returned by `SearchKnowledgeBase` and included in the `EvidenceList` SHALL have a `ServiceName` that appears in the list of affected services passed to the call.

**Validates: Requirements 4.3**

---

### Property 9: All-tools-fail yields Low confidence and a connectivity action

*For any* investigation where every tool call fails, the returned `InvestigationSummary` SHALL have `ConfidenceLevel = Low`, an `Evidence` list with at least one item describing the failure, and a `RecommendedActions` list containing an action directing the user to verify tool connectivity.

**Validates: Requirements 8.4**

---

### Property 10: Recommended actions list is never empty

*For any* `InvestigationSummary`, the `RecommendedActions` field SHALL contain at least one item. When no actions can be derived from evidence, the list SHALL contain a default escalation step.

**Validates: Requirements 7.6, 7.7**

---

### Property 11: Failure metadata always captures all required fields

*For any* tool call that fails (via error return, timeout, or context cancellation), the resulting `FailureMetadata` SHALL contain a non-empty `ToolName`, a non-zero `FailureTimestamp`, and a non-empty `ErrorMessage`.

**Validates: Requirements 8.5**

---

### Property 12: Knowledge base is called at most once per investigation

*For any* investigation run — regardless of prompt content or other tool outcomes — `SearchKnowledgeBase()` SHALL be called exactly once and SHALL NOT be retried on error or timeout.

**Validates: Requirements 4.5**

---

### Property 13: InvestigationSummary always contains all five required fields

*For any* completed investigation (regardless of tool outcomes), the returned `InvestigationSummary` SHALL have all five fields present and non-zero: `Summary`, `Evidence`, `LikelyRootCause`, `ConfidenceLevel`, and `RecommendedActions`.

**Validates: Requirements 7.1**

---

## Error Handling

| Failure Scenario | Handling |
|---|---|
| Empty / whitespace prompt | `PromptValidationError` returned before any goroutine is launched |
| Prompt > 10,000 chars | `PromptValidationError` returned immediately |
| `GetIncident` error/empty | Record `ToolResult{Success: false}`; continue |
| No incident ID in prompt | `IncidentContext.IncidentID = ""`; skip `GetIncident` call or pass empty string and record missing-ID evidence |
| `GetRecentDeployments` error | Record failure `EvidenceItem` with status `"unavailable"`; continue |
| Deployment record missing fields | Substitute `"unknown"` for each nil/zero field during assembly |
| Deployment list > 50 records | Sort by `|deployed_at - incidentTimestamp|`; keep top 50 |
| `SearchKnowledgeBase` error | Record failure `EvidenceItem`; continue (no retry) |
| KB response timeout (> 10 s) | Child context cancelled; treat as failure; record timeout `EvidenceItem` |
| `GetServiceHealth` error per service | Record per-service failure `EvidenceItem`; continue |
| No services in incident record | Record `EvidenceItem` noting no services found; proceed |
| Single tool goroutine > 10 s | `context.WithTimeout` fires; goroutine unblocks on context check; failure recorded |
| Total investigation > 30 s | Global context cancelled; all remaining goroutines unblocked; partial results returned |
| All tools fail | Return `InvestigationSummary{ConfidenceLevel: Low, ...}` with connectivity action |
| LLM JSON decode failure | Return `error` wrapping the decode failure to caller; no partial summary emitted |
| LLM response fails `Validate()` | Return `error` describing which invariant was violated |
| DuckDB query error | Returned as tool error; handled by `ToolResult{Success: false}` path |

---

## Testing Strategy

### Dual Testing Approach

Testing uses both example-based unit tests and property-based tests (PBT). Unit tests cover specific concrete scenarios and integration points; property tests verify universal invariants across the full input space using generated data.

### Technology Choices

| Concern | Library |
|---|---|
| Standard test runner | `testing` (stdlib) |
| Assertions | `github.com/stretchr/testify/assert` and `/require` |
| Property-based testing | `pgregory.net/rapid` |
| LLM mocking | Interface substitution — inject a `mockReasoningEngine` that returns a canned `InvestigationSummary` |
| DuckDB in tests | In-memory DuckDB (`:memory:`) with `marcboeker/go-duckdb`; schema created via `TestMain` |
| Concurrency assertions | `go.uber.org/goleak` for goroutine leak detection |

### Unit Tests

**`ValidatePrompt`**
- Empty string → `PromptValidationError`
- String of spaces only → `PromptValidationError`
- String of tabs and newlines → `PromptValidationError`
- Exactly 10,000 chars → nil
- 10,001 chars → `PromptValidationError`
- Valid multi-line prompt → nil

**`EvidenceAssembler`**
- Deployment list of exactly 50, 51, and 0 records; verify count and recency ordering
- Record with all three fields missing → all three shown as `"unknown"`
- KB results with scores above, at, and below threshold → only qualifying results included
- Relevance summary > 200 chars → truncated to 200
- Empty `[]ToolResult` → sentinel evidence item inserted

**`ConcurrentToolRunner`**
- All tools succeed → 4 `ToolResult{Success: true}` returned
- One tool returns error → that tool's `ToolResult{Success: false}`, others succeed
- One tool times out (mock sleeps > 10 s) → timeout failure recorded, others complete
- Global context cancelled mid-flight → all in-flight goroutines return failure items

**`DuckDBToolProvider`** (integration, uses `:memory:` DB)
- `GetIncident` with known ID → correct `IncidentRecord` returned
- `GetIncident` with unknown ID → error returned
- `GetRecentDeployments` with window containing 2 records → both returned
- `SearchKnowledgeBase` with matching services and symptom hint → returns matching TSG/SOP results
- `GetServiceHealth` with known service → correct `HealthStatus` returned

**`InvestigationSummary.Validate()`**
- All five fields populated → nil
- Empty `Evidence` → error
- Empty `RecommendedActions` → error
- `ConfidenceLevel` outside enum → error

### Property-Based Tests (rapid)

Each test runs a minimum of **100 iterations** via `rapid.Check`.

```go
// Example: Property 1
func TestProperty1_WhitespacePromptsRejected(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        // Generate arbitrary whitespace-only strings
        ws := rapid.StringMatching(`^[\s]+$`).Draw(t, "whitespace_prompt")
        err := ValidatePrompt(ws)
        require.Error(t, err)
        var ve *PromptValidationError
        require.ErrorAs(t, err, &ve)
    })
}

// Example: Property 6
func TestProperty6_DeploymentTruncationPreservesRecency(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        n := rapid.IntRange(51, 200).Draw(t, "record_count")
        records := generateDeploymentRecords(t, n)
        incidentTs := rapid.Custom(drawTime).Draw(t, "incident_ts")
        result := assembleDeployments(records, incidentTs)
        require.LessOrEqual(t, len(result), 50)
        assertClosestToTimestamp(t, result, records, incidentTs)
    })
}
```

| Tag | Property |
|---|---|
| `Feature: incident-investigator, Property 1: Whitespace and empty prompts are always rejected` | For any whitespace-only string, `ValidatePrompt` returns `PromptValidationError` |
| `Feature: incident-investigator, Property 2: Over-length prompts are always rejected` | For any string with `len > 10_000`, `ValidatePrompt` returns `PromptValidationError` |
| `Feature: incident-investigator, Property 3: Tool failures never suppress investigation` | For any combination of failing/succeeding mock tools, `Investigate()` returns an `InvestigationSummary` (not an error) |
| `Feature: incident-investigator, Property 4: Evidence list is never empty` | For any tool outcome set, `len(InvestigationSummary.Evidence) >= 1` |
| `Feature: incident-investigator, Property 5: Confidence level is determined by corroboration count` | For any generated `EvidenceList`, the assigned `ConfidenceLevel` matches the corroboration rules |
| `Feature: incident-investigator, Property 6: Deployment record truncation preserves recency` | For any `[]DeploymentRecord` with `len > 50`, assembler produces `<= 50` records and they are the closest to the incident timestamp |
| `Feature: incident-investigator, Property 7: Missing deployment fields are marked "unknown"` | For any deployment record with arbitrary nil/zero fields, assembled `EvidenceItem` represents missing fields as `"unknown"` |
| `Feature: incident-investigator, Property 8: Knowledge base results are scoped to affected services` | For any KB result set, all included results have a `ServiceName` matching the affected services list |
| `Feature: incident-investigator, Property 9: All-tools-fail yields Low confidence` | For any input where all tool mocks return errors, summary has `ConfidenceLevel = Low` and at least one connectivity-related `RecommendedAction` |
| `Feature: incident-investigator, Property 10: Recommended actions list is never empty` | For any generated `InvestigationSummary`, `len(RecommendedActions) >= 1` |
| `Feature: incident-investigator, Property 11: Failure metadata always captures all required fields` | For any tool failure, `FailureMetadata` has non-empty `ToolName`, non-zero `FailureTimestamp`, and non-empty `ErrorMessage` |
| `Feature: incident-investigator, Property 12: Knowledge base called at most once per investigation` | For any investigation run, the mock `SearchKnowledgeBase` call counter equals exactly 1 |
| `Feature: incident-investigator, Property 13: InvestigationSummary always contains all five required fields` | For any tool outcome combination, all five summary fields are present and non-zero |

### Integration Tests

- End-to-end run against in-memory DuckDB seeded with fixture data: verify `InvestigationSummary` is returned within 30 seconds for a realistic prompt
- Partial failure scenario: 2 of 4 tool queries fail (e.g., table dropped mid-test) — verify `FailedTools` section lists exactly those 2 tools with names and error messages
- All-tools-fail scenario: empty DB with no tables — verify `ConfidenceLevel = Low` and connectivity `RecommendedAction`
- Concurrent execution: instrument `DuckDBToolProvider` methods with timestamps; assert all four queries started within 500 ms of each other
- Stage 2 pluggability smoke test: substitute a `MockHTTPToolProvider` (no real HTTP) implementing `ToolProvider` — verify the agent compiles and runs identically
