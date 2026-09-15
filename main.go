// main is the entry point for the Incident Investigator Agent.
// It accepts an incident prompt from either command-line arguments or stdin,
// calls InvestigatorAgent.Investigate, and prints the JSON summary to stdout.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/org/incident-agent/agent"
	"github.com/org/incident-agent/reasoning"
	"github.com/org/incident-agent/tools"
)

func main() {
	prompt := readPrompt()
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt provided (pass via args or stdin)")
		os.Exit(1)
	}

	dbPath := os.Getenv("DUCKDB_PATH")
	if dbPath == "" {
		dbPath = "./incidentagent.duckdb"
	}

	provider, err := tools.NewDuckDBToolProvider(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to open DuckDB: %v\n", err)
		os.Exit(1)
	}

	endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")
	apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
	deployment := os.Getenv("AZURE_OPENAI_DEPLOYMENT")

	var missing []string
	if endpoint == "" {
		missing = append(missing, "AZURE_OPENAI_ENDPOINT")
	}
	if apiKey == "" {
		missing = append(missing, "AZURE_OPENAI_API_KEY")
	}
	if deployment == "" {
		missing = append(missing, "AZURE_OPENAI_DEPLOYMENT")
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "error: missing required environment variable(s): %s\n", strings.Join(missing, ", "))
		os.Exit(1)
	}

	engine := reasoning.NewReasoningEngine(apiKey, endpoint, deployment)
	a := agent.NewInvestigatorAgent(provider, engine)

	ctx := context.Background()
	summary, err := a.Investigate(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: investigation failed: %v\n", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summary); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to encode summary: %v\n", err)
		os.Exit(1)
	}
}

// readPrompt reads the incident prompt from os.Args[1:] (joined with spaces)
// or, if no args are given, from stdin.
func readPrompt() string {
	if len(os.Args) > 1 {
		return strings.Join(os.Args[1:], " ")
	}
	scanner := bufio.NewScanner(os.Stdin)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}
