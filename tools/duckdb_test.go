// duckdb_test.go contains integration tests for DuckDBToolProvider that run
// against a shared in-memory DuckDB database seeded with fixture data in
// TestMain.
package tools

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb" // register DuckDB driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProvider is the shared provider backed by the in-memory DuckDB seeded in
// TestMain. All test functions query through this single handle.
var testProvider *DuckDBToolProvider

// mustParse parses a fixture timestamp in the "2006-01-02 15:04:05" layout and
// panics on failure (fixtures are hard-coded, so a parse error is a test bug).
func mustParse(value string) time.Time {
	ts, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic("duckdb_test: invalid fixture timestamp " + value + ": " + err.Error())
	}
	return ts
}

// TestMain opens a single in-memory DuckDB, creates the schema, seeds fixture
// data into all four tables, and exposes a provider sharing the same *sql.DB.
func TestMain(m *testing.M) {
	// An empty DSN opens an in-memory DuckDB for the marcboeker/go-duckdb driver.
	db, err := sql.Open("duckdb", "")
	if err != nil {
		panic("duckdb_test: open in-memory duckdb: " + err.Error())
	}
	if err := db.Ping(); err != nil {
		panic("duckdb_test: ping in-memory duckdb: " + err.Error())
	}
	if err := CreateSchema(db); err != nil {
		panic("duckdb_test: create schema: " + err.Error())
	}
	if err := seedFixtures(db); err != nil {
		panic("duckdb_test: seed fixtures: " + err.Error())
	}

	testProvider = NewDuckDBToolProviderFromDB(db)

	code := m.Run()

	_ = db.Close()
	os.Exit(code)
}

// seedFixtures inserts the fixture rows into all four tables.
func seedFixtures(db *sql.DB) error {
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: "INSERT INTO incidents (incident_id, title, severity, affected_services, created_at) VALUES (?, ?, ?, ?, ?)",
			args:  []any{"INC-1001", "Checkout 5xx spike", "P1", `["checkout","payments"]`, mustParse("2024-01-15 10:00:00")},
		},
		{
			query: "INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by) VALUES (?, ?, ?, ?, ?)",
			args:  []any{"dep-1", "checkout", "v2.3.1", mustParse("2024-01-15 09:30:00"), "alice"},
		},
		{
			query: "INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by) VALUES (?, ?, ?, ?, ?)",
			args:  []any{"dep-2", "payments", "v1.0.5", mustParse("2024-01-15 09:45:00"), "bob"},
		},
		{
			query: "INSERT INTO deployments (deployment_id, service_name, version, deployed_at, deployed_by) VALUES (?, ?, ?, ?, ?)",
			args:  []any{"dep-3", "billing", "v9", mustParse("2020-01-01 00:00:00"), "carol"},
		},
		{
			query: "INSERT INTO knowledge_base (article_id, title, article_type, service_name, symptom_tags, impact_summary, body, severity_scope, last_updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			args: []any{
				"kb-1", "Checkout 5xx troubleshooting", "TSG", "checkout",
				`["5xx_errors","high_latency"]`, "Checkout returns 5xx when DB pool exhausted",
				"...", "P1,P2", mustParse("2024-01-10 00:00:00"),
			},
		},
		{
			query: "INSERT INTO service_health (service_name, health_status, last_checked, details) VALUES (?, ?, ?, ?)",
			args:  []any{"checkout", "degraded", mustParse("2024-01-15 10:01:00"), "pool exhaustion"},
		},
	}

	for _, s := range statements {
		if _, err := db.Exec(s.query, s.args...); err != nil {
			return err
		}
	}
	return nil
}

func TestGetIncident_KnownID(t *testing.T) {
	rec, err := testProvider.GetIncident(context.Background(), "INC-1001")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "INC-1001", rec.IncidentID)
	assert.Equal(t, "P1", rec.Severity)
	assert.Equal(t, []string{"checkout", "payments"}, rec.AffectedServices)
}

func TestGetIncident_UnknownID(t *testing.T) {
	rec, err := testProvider.GetIncident(context.Background(), "INC-9999")
	require.Error(t, err)
	assert.Nil(t, rec)
}

func TestGetRecentDeployments_WindowWithTwoRecords(t *testing.T) {
	from := mustParse("2024-01-15 09:00:00")
	to := mustParse("2024-01-15 10:00:00")

	records, err := testProvider.GetRecentDeployments(context.Background(), from, to)
	require.NoError(t, err)
	require.Len(t, records, 2)

	services := []string{records[0].ServiceName, records[1].ServiceName}
	assert.ElementsMatch(t, []string{"checkout", "payments"}, services)
	assert.NotContains(t, services, "billing")
}

func TestSearchKnowledgeBase_Matching(t *testing.T) {
	results, err := testProvider.SearchKnowledgeBase(context.Background(), []string{"checkout"}, "5xx")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 1)
	assert.Equal(t, "checkout", results[0].ServiceName)
	assert.Contains(t, results[0].SymptomTags, "5xx_errors")
}

func TestSearchKnowledgeBase_EmptyServices(t *testing.T) {
	results, err := testProvider.SearchKnowledgeBase(context.Background(), []string{}, "5xx")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestGetServiceHealth_Known(t *testing.T) {
	rec, err := testProvider.GetServiceHealth(context.Background(), "checkout")
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "degraded", rec.HealthStatus)
}

func TestGetServiceHealth_Unknown(t *testing.T) {
	rec, err := testProvider.GetServiceHealth(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.Nil(t, rec)
}
