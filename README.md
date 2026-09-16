# Incident Investigator Agent

A Stage 1 incident investigator. Give it a natural-language incident prompt and it
gathers evidence from a local DuckDB via four tools (`get_incident`,
`get_recent_deployments`, `search_knowledge_base`, `get_service_health`), then uses
Azure OpenAI to reason over that evidence and return a structured
`InvestigationSummary` — Summary, Evidence, Likely Root Cause, Confidence Level,
Recommended Actions, and Failed Tools.

## How it works

- Validate the incident prompt before any tool call runs.
- Fetch the incident to derive its authoritative timestamp and the list of affected services.
- Fan out: concurrently call the four DuckDB-backed tools (one `get_service_health` per affected service), with a 10s per-tool timeout under a 30s global deadline. Tool failures are captured as evidence rather than aborting the run.
- Assemble the results into an evidence list plus failed-tool metadata.
- Azure OpenAI reasons over the assembled evidence and returns the structured summary.

This is a deterministic, fixed workflow (fan-out → gather → reason), not an autonomous
tool-calling agent. Go code orchestrates the tools; the LLM is a single reasoning step
over already-gathered evidence and never decides which tools to call.

## Prerequisites

- Go 1.26+.
- An Azure OpenAI (Azure AI Foundry) resource with a deployed chat model that supports structured outputs (`json_schema`), plus its endpoint, API key, and deployment name.
- No separate DuckDB install is required — DuckDB is pulled in as a Go module dependency. Note: if you open the `.duckdb` file in an external DuckDB CLI or a database viewer, it takes a lock and the app can't open it. Close it first, or point the app at a different `DUCKDB_PATH`.

## Setup

Clone the repo, then download dependencies:

```bash
go mod download
```

Create your local environment file and fill in your Azure OpenAI values:

```bash
cp local.env.example local.env
```

Set the three required variables in `local.env`:

- `AZURE_OPENAI_ENDPOINT` — the resource endpoint, in the form `https://<resource>.openai.azure.com` (this is the Azure OpenAI resource endpoint, **not** a Foundry project endpoint).
- `AZURE_OPENAI_DEPLOYMENT` — the deployment name.
- `AZURE_OPENAI_API_KEY` — the API key.

`local.env` is git-ignored, so your secrets stay out of version control.

Security note: never commit real keys. Keep them in the git-ignored `local.env`, and
rotate any key that gets exposed.

## Seed sample data

Populate the DuckDB with a sample correlated incident scenario — `INC-1001`
(checkout returning 5xx), a matching checkout deployment, TSG/SOP knowledge-base
articles, and service-health rows:

```bash
go run ./cmd/seed
```

The seed is idempotent (it clears each table before inserting), so you can re-run it
safely. It prints the DB path, the number of statements executed, and per-table row
counts. It honors `DUCKDB_PATH` too:

```bash
DUCKDB_PATH=/tmp/incident.duckdb go run ./cmd/seed
```

## Run

```bash
source local.env && go run . "Investigate INC-1001: checkout is returning 5xx errors"
```

The prompt can be passed as command-line arguments (joined with spaces) or piped in
via stdin. The output is a JSON `InvestigationSummary` printed to stdout.

Tip: to avoid the DuckDB file-lock issue during development, seed and run against the
same temporary database by setting `DUCKDB_PATH` for both commands:

```bash
export DUCKDB_PATH=/tmp/incident.duckdb
go run ./cmd/seed
source local.env && go run . "Investigate INC-1001: checkout is returning 5xx errors"
```

## Running tests

```bash
go test ./...          # full suite
go test ./... -race    # with the race detector (relevant for the concurrent tool runner)
go test ./... -v       # verbose
```

Tests run against an in-memory DuckDB and a mock LLM — they make no live Azure calls
and cost nothing. The suite includes property-based tests (`pgregory.net/rapid`) and
unit tests (`testify`); the `agent` package uses `goleak` to check for goroutine leaks.

## Project layout

```
main.go                                   Entry point: reads prompt, wires provider + engine, prints JSON
cmd/seed/                                 DuckDB seeder (embedded seed.sql, idempotent fixtures)
agent/                                    InvestigatorAgent facade, prompt validation & analysis, concurrent runner
tools/                                    ToolProvider interface + DuckDB implementation
evidence/                                 Evidence assembler
reasoning/                                Azure OpenAI reasoning engine
models/                                   Shared types (records, evidence, InvestigationSummary)
.kiro/specs/incident-investigator/        Requirements, design, and tasks
```

## Configuration reference

| Variable                  | Required | Default                  | Description                                                              |
| ------------------------- | -------- | ------------------------ | ------------------------------------------------------------------------ |
| `AZURE_OPENAI_ENDPOINT`   | Yes      | —                        | Azure resource endpoint (`https://<resource>.openai.azure.com`).         |
| `AZURE_OPENAI_API_KEY`    | Yes      | —                        | Azure OpenAI API key.                                                    |
| `AZURE_OPENAI_DEPLOYMENT` | Yes      | —                        | Azure deployment name.                                                   |
| `DUCKDB_PATH`             | No       | `./incidentagent.duckdb` | Path to the local DuckDB file (used by both the app and the seeder).     |

The Azure API version (`2024-08-01-preview`) and `json_schema` structured-output mode
are set internally and are not configurable via environment variables.

## Roadmap

The project is delivered in two stages that share the same `ToolProvider` interface —
the single seam between the orchestration/reasoning code and the data sources.

- **Stage 1 (current):** All four tools (`get_incident`, `get_recent_deployments`, `search_knowledge_base`, `get_service_health`) read from a **local DuckDB** database. No external network calls are made to gather evidence, which keeps the workflow fully local, fast, and easy to test.
- **Stage 2 (planned): Agent harness.** Evolve the current fixed workflow into a true LLM-driven agent. Instead of Go orchestrating a fixed fan-out and making a single summarizing call, the four tools are exposed to the LLM as callable functions (Azure OpenAI function/tool calling), and an **agent loop** lets the model decide which tools to call, in what order, and when it has gathered enough evidence to conclude. The harness adds: a tool registry + call dispatch, the iterate-until-done controller, guardrails (max iterations, wall-clock deadline, and loop/repeat detection), running conversation state across turns, structured final-output enforcement (the same `InvestigationSummary` schema), and an execution trace for observability. The existing `ToolProvider` tools, output schema, and Azure client are reused; the fixed runner is replaced by the agent loop. Tradeoff: more flexible and adaptive, at the cost of the deterministic single-call latency/cost guarantees of Stage 1.

## Status / limitations

- The DuckDB schema is auto-created when the database file is opened.
- The confidence level in the summary is currently produced by the LLM (not yet computed deterministically from the evidence).
