// duckdb.go contains the DuckDB-backed Stage 1 implementation of ToolProvider.
package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb" // register DuckDB driver
	"github.com/org/incident-agent/models"
)

// DuckDBToolProvider implements ToolProvider using a local DuckDB database.
type DuckDBToolProvider struct {
	db *sql.DB
}

// NewDuckDBToolProvider opens a DuckDB connection at dbPath and returns a
// ready-to-use provider. The caller is responsible for calling Close() when done.
func NewDuckDBToolProvider(dbPath string) (*DuckDBToolProvider, error) {
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open duckdb %q: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping duckdb %q: %w", dbPath, err)
	}
	if err := CreateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap schema for duckdb %q: %w", dbPath, err)
	}
	return &DuckDBToolProvider{db: db}, nil
}

// CreateSchema runs the CREATE TABLE IF NOT EXISTS DDL for all four tables
// (incidents, deployments, knowledge_base, service_health). It is safe to call
// repeatedly: existing tables are left untouched. On the first Exec failure it
// returns a wrapped error identifying which table's DDL failed.
func CreateSchema(db *sql.DB) error {
	ddl := []struct {
		table string
		stmt  string
	}{
		{
			table: "incidents",
			stmt: `CREATE TABLE IF NOT EXISTS incidents (
    incident_id        TEXT PRIMARY KEY,
    title              TEXT NOT NULL,
    severity           TEXT NOT NULL CHECK (severity IN ('P1','P2','P3','P4')),
    affected_services  JSON NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL
);`,
		},
		{
			table: "deployments",
			stmt: `CREATE TABLE IF NOT EXISTS deployments (
    deployment_id  TEXT PRIMARY KEY,
    service_name   TEXT,
    version        TEXT,
    deployed_at    TIMESTAMPTZ,
    deployed_by    TEXT
);`,
		},
		{
			table: "knowledge_base",
			stmt: `CREATE TABLE IF NOT EXISTS knowledge_base (
    article_id     TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    article_type   TEXT NOT NULL CHECK (article_type IN ('TSG', 'SOP')),
    service_name   TEXT NOT NULL,
    symptom_tags   JSON NOT NULL,
    impact_summary TEXT NOT NULL,
    body           TEXT NOT NULL,
    severity_scope TEXT,
    last_updated   TIMESTAMPTZ NOT NULL
);`,
		},
		{
			table: "service_health",
			stmt: `CREATE TABLE IF NOT EXISTS service_health (
    service_name   TEXT NOT NULL,
    health_status  TEXT NOT NULL CHECK (health_status IN ('healthy','degraded','down','unknown')),
    last_checked   TIMESTAMPTZ NOT NULL,
    details        TEXT,
    PRIMARY KEY (service_name)
);`,
		},
	}

	for _, d := range ddl {
		if _, err := db.Exec(d.stmt); err != nil {
			return fmt.Errorf("create table %q: %w", d.table, err)
		}
	}
	return nil
}

// NewDuckDBToolProviderFromDB wraps an already-open *sql.DB in a
// DuckDBToolProvider. It is useful for dependency injection and for tests that
// need to seed and query through the same underlying database handle (an
// in-memory DuckDB opened via a second sql.Open is a distinct database, so the
// seeding connection and the provider must share one *sql.DB). The caller
// retains ownership of the db and is responsible for closing it.
func NewDuckDBToolProviderFromDB(db *sql.DB) *DuckDBToolProvider {
	return &DuckDBToolProvider{db: db}
}

// Close releases the underlying database connection.
func (p *DuckDBToolProvider) Close() error {
	return p.db.Close()
}

// GetIncident fetches a single incident by ID. The affected_services column is
// stored as a JSON array and unmarshalled into the record's AffectedServices
// field. An unknown ID yields a descriptive not-found error.
func (p *DuckDBToolProvider) GetIncident(ctx context.Context, incidentID string) (*models.IncidentRecord, error) {
	// affected_services is a DuckDB JSON column. The driver decodes JSON columns
	// into Go values (e.g. []interface{}) rather than raw text, which cannot be
	// scanned into a string. Cast to VARCHAR so we receive the raw JSON text and
	// can unmarshal it ourselves.
	row := p.db.QueryRowContext(ctx,
		"SELECT incident_id, title, severity, CAST(affected_services AS VARCHAR), created_at FROM incidents WHERE incident_id = ?",
		incidentID)

	var (
		rec              models.IncidentRecord
		affectedServices string
	)
	if err := row.Scan(&rec.IncidentID, &rec.Title, &rec.Severity, &affectedServices, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("incident %q not found", incidentID)
		}
		return nil, fmt.Errorf("GetIncident %q: %w", incidentID, err)
	}

	if err := json.Unmarshal([]byte(affectedServices), &rec.AffectedServices); err != nil {
		return nil, fmt.Errorf("GetIncident %q: unmarshal affected_services: %w", incidentID, err)
	}

	return &rec, nil
}

// GetRecentDeployments returns deployment records whose deployed_at timestamp
// falls within [from, to], ordered most-recent-first. The service_name, version,
// and deployed_at columns are nullable; invalid values are mapped to their zero
// value (empty string / zero time). Substituting "unknown" for missing fields is
// the responsibility of the EvidenceAssembler, not this method. An empty result
// set is not an error.
func (p *DuckDBToolProvider) GetRecentDeployments(ctx context.Context, from, to time.Time) ([]models.DeploymentRecord, error) {
	rows, err := p.db.QueryContext(ctx,
		"SELECT service_name, version, deployed_at FROM deployments WHERE deployed_at BETWEEN ? AND ? ORDER BY deployed_at DESC",
		from, to)
	if err != nil {
		return nil, fmt.Errorf("GetRecentDeployments: %w", err)
	}
	defer rows.Close()

	var records []models.DeploymentRecord
	for rows.Next() {
		var (
			serviceName sql.NullString
			version     sql.NullString
			deployedAt  sql.NullTime
		)
		if err := rows.Scan(&serviceName, &version, &deployedAt); err != nil {
			return nil, fmt.Errorf("GetRecentDeployments: scan row: %w", err)
		}

		var rec models.DeploymentRecord
		if serviceName.Valid {
			rec.ServiceName = serviceName.String
		}
		if version.Valid {
			rec.Version = version.String
		}
		if deployedAt.Valid {
			rec.DeployedAt = deployedAt.Time
		}
		records = append(records, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetRecentDeployments: iterate rows: %w", err)
	}

	return records, nil
}

// SearchKnowledgeBase returns TSG/SOP articles from the knowledge_base table
// whose service_name is one of the supplied services AND whose symptom_tags or
// title matches symptomHint (case-insensitive substring), ordered most-recently
// updated first. The symptom_tags JSON column is unmarshalled into each result's
// SymptomTags slice. When services is empty, no query is run and an empty result
// is returned (an SQL IN () clause with zero elements is invalid).
func (p *DuckDBToolProvider) SearchKnowledgeBase(ctx context.Context, services []string, symptomHint string) ([]models.KBResult, error) {
	if len(services) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(services))
	args := make([]any, 0, len(services)+2)
	for i, svc := range services {
		placeholders[i] = "?"
		args = append(args, svc)
	}
	likePattern := "%" + symptomHint + "%"
	args = append(args, likePattern, likePattern)

	// symptom_tags is a DuckDB JSON column; cast it to VARCHAR so we receive raw
	// JSON text that can be unmarshalled, rather than a driver-decoded value that
	// cannot be scanned into a string.
	query := fmt.Sprintf(
		"SELECT article_id, title, article_type, service_name, CAST(symptom_tags AS VARCHAR), impact_summary, body, severity_scope, last_updated "+
			"FROM knowledge_base WHERE service_name IN (%s) AND (CAST(symptom_tags AS VARCHAR) ILIKE ? OR title ILIKE ?) ORDER BY last_updated DESC",
		strings.Join(placeholders, ", "))

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("SearchKnowledgeBase: %w", err)
	}
	defer rows.Close()

	var results []models.KBResult
	for rows.Next() {
		var (
			rec           models.KBResult
			symptomTags   string
			severityScope sql.NullString
		)
		if err := rows.Scan(
			&rec.ArticleID,
			&rec.Title,
			&rec.ArticleType,
			&rec.ServiceName,
			&symptomTags,
			&rec.ImpactSummary,
			&rec.Body,
			&severityScope,
			&rec.LastUpdated,
		); err != nil {
			return nil, fmt.Errorf("SearchKnowledgeBase: scan row: %w", err)
		}

		if err := json.Unmarshal([]byte(symptomTags), &rec.SymptomTags); err != nil {
			return nil, fmt.Errorf("SearchKnowledgeBase: unmarshal symptom_tags: %w", err)
		}
		if severityScope.Valid {
			rec.SeverityScope = severityScope.String
		}

		results = append(results, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SearchKnowledgeBase: iterate rows: %w", err)
	}

	return results, nil
}

// GetServiceHealth fetches the current health snapshot for a single service by
// name. The service_name and health_status columns are plain strings. An unknown
// service yields a descriptive not-found error.
func (p *DuckDBToolProvider) GetServiceHealth(ctx context.Context, service string) (*models.ServiceHealthRecord, error) {
	row := p.db.QueryRowContext(ctx,
		"SELECT service_name, health_status FROM service_health WHERE service_name = ?",
		service)

	var rec models.ServiceHealthRecord
	if err := row.Scan(&rec.ServiceName, &rec.HealthStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("service health for %q not found", service)
		}
		return nil, fmt.Errorf("GetServiceHealth %q: %w", service, err)
	}

	return &rec, nil
}
