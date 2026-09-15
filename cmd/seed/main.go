// Command seed populates a DuckDB database with a realistic, correlated
// incident scenario so the incident-agent binary can be exercised end-to-end.
//
// It reads the DB path from DUCKDB_PATH (default ./incidentagent.duckdb),
// ensures the schema exists via tools.CreateSchema, then executes the embedded
// seed.sql inside a single transaction. The seed script is idempotent (it
// DELETEs each table before inserting), so the command may be run repeatedly.
package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"strings"

	_ "github.com/marcboeker/go-duckdb" // register DuckDB driver
	"github.com/org/incident-agent/tools"
)

//go:embed seed.sql
var seedFS embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := os.Getenv("DUCKDB_PATH")
	if dbPath == "" {
		dbPath = "./incidentagent.duckdb"
	}

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return fmt.Errorf("open duckdb %q: %w", dbPath, err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping duckdb %q: %w", dbPath, err)
	}

	// Guarantee the schema exists before seeding.
	if err := tools.CreateSchema(db); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	seedSQL, err := seedFS.ReadFile("seed.sql")
	if err != nil {
		return fmt.Errorf("read embedded seed.sql: %w", err)
	}

	statements := splitStatements(string(seedSQL))
	if len(statements) == 0 {
		return fmt.Errorf("seed.sql contained no executable statements")
	}

	// DELETEs and INSERTs are committed in separate transactions. DuckDB's index
	// enforcement does not let an INSERT observe a DELETE of the same primary key
	// within the same transaction, so clearing and re-inserting a key in one
	// transaction raises a spurious duplicate-key error. Committing the deletes
	// first makes the seed reliably idempotent across repeated runs.
	ctx := context.Background()

	deletes, inserts := partitionStatements(statements)

	if err := execInTx(ctx, db, deletes); err != nil {
		return fmt.Errorf("clear tables: %w", err)
	}
	if err := execInTx(ctx, db, inserts); err != nil {
		return fmt.Errorf("insert fixtures: %w", err)
	}

	fmt.Printf("Seeded %q: executed %d statement(s).\n", dbPath, len(statements))

	if err := printRowCounts(ctx, db); err != nil {
		return err
	}

	return nil
}

// partitionStatements separates DELETE statements from the rest, preserving
// order within each group. This lets the caller commit deletes before inserts.
func partitionStatements(statements []string) (deletes, others []string) {
	for _, stmt := range statements {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "DELETE") {
			deletes = append(deletes, stmt)
		} else {
			others = append(others, stmt)
		}
	}
	return deletes, others
}

// execInTx runs the given statements inside a single transaction, rolling back
// on the first failure.
func execInTx(ctx context.Context, db *sql.DB, statements []string) error {
	if len(statements) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	for i, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec statement %d (%s): %w", i+1, firstLine(stmt), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// splitStatements breaks a SQL script into individual statements on ";",
// trimming whitespace and skipping empty statements and comment-only lines.
func splitStatements(script string) []string {
	raw := strings.Split(script, ";")
	statements := make([]string, 0, len(raw))
	for _, part := range raw {
		stmt := stripComments(part)
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		statements = append(statements, strings.TrimSpace(stmt))
	}
	return statements
}

// stripComments removes full-line SQL comments (lines starting with "--") from
// a statement fragment, preserving the remaining SQL.
func stripComments(fragment string) string {
	lines := strings.Split(fragment, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// firstLine returns the first non-empty line of a statement for error context.
func firstLine(stmt string) string {
	for _, line := range strings.Split(stmt, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return stmt
}

// printRowCounts reports SELECT COUNT(*) for each seeded table.
func printRowCounts(ctx context.Context, db *sql.DB) error {
	tables := []string{"incidents", "deployments", "knowledge_base", "service_health"}
	fmt.Println("Row counts:")
	for _, table := range tables {
		var count int
		// Table names are hard-coded constants, not user input, so this fmt
		// interpolation is safe from injection.
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		fmt.Printf("  %-15s %d\n", table, count)
	}
	return nil
}
