package db

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/aidenappl/lattice-api/env"
	"github.com/go-sql-driver/mysql"
)

const (
	// MIGRATIONS_TABLE tracks which migration files have already been applied.
	MIGRATIONS_TABLE = "schema_migrations"
	// MIGRATIONS_DIR is the embedded directory holding the numbered .sql files.
	MIGRATIONS_DIR = "migrations"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies every embedded migration in filename order, recording
// each applied file in schema_migrations so it runs once under normal operation.
// The guarantee is at-least-once, not exactly-once — see the non-atomicity note
// below. Ported from the same runner in forta-api, deliberately, so the two
// services do not diverge on something this load-bearing.
//
// # Why this exists alongside the legacy migrate() block in Init
//
// db.Init still executes ~52 inline idempotent DDL statements on every boot.
// That mechanism has three properties that make it unsuitable for anything new:
//
//   - It has no record of what ran, so nothing can tell an applied change from
//     one that silently failed, and there is no ordering.
//   - It swallows a fixed list of MySQL error numbers (1060, 1062, 1054 …) to
//     achieve idempotence. A genuinely wrong statement that happens to produce
//     one of those codes is indistinguishable from a no-op.
//   - Every other failure is logged as a *warning* and the server starts anyway,
//     serving traffic against a schema it assumes exists. That is not
//     hypothetical: sso_sessions was referenced by code that no statement in that
//     block ever created, and the resulting error was caught as a generic DB
//     failure and turned into an unbounded fail-open on a revocation check.
//
// The legacy block is therefore **frozen**: it stays because it is what builds a
// fresh database today and every statement in it has already been applied
// everywhere, but no new DDL goes in it. New schema changes are numbered files
// here, applied in order, recorded, and **fatal on failure**.
//
// # Running on boot
//
// Migrations run both from the `migrate` subcommand and at startup before the
// router is mounted. Running on boot is what makes a deploy self-contained: a
// migration file only exists inside the image that carries it, and the code
// depending on a new column ships in that same image. A failed migration must
// stop the boot rather than let the server answer requests against a schema the
// migration could not reach.
//
// # Non-atomicity
//
// A migration is NOT atomic and this runner does not pretend otherwise. DDL in
// MySQL/MariaDB commits implicitly, so wrapping a file in a transaction buys
// nothing: if the process dies after statement 3 of 5, those three are already
// durable. A file is recorded only after its Exec returns cleanly, so a failed
// migration is never marked applied and is retried in full next time — which is
// safe precisely because every statement must be written re-runnably.
// TestMigrationsAreGuarded enforces that.
func RunMigrations() error {
	conn, err := openMigrationsConn()
	if err != nil {
		return err
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			fmt.Printf("warning: failed to close migrations connection: %v\n", cerr)
		}
	}()

	if err := conn.Ping(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := EnsureMigrationsTable(conn); err != nil {
		return err
	}

	names, err := migrationFiles()
	if err != nil {
		return err
	}

	applied := 0
	for _, name := range names {
		done, err := MigrationApplied(conn, name)
		if err != nil {
			return err
		}
		if done {
			continue
		}

		content, err := migrationsFS.ReadFile(MIGRATIONS_DIR + "/" + name)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}

		if _, err := conn.Exec(string(content)); err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", name, err)
		}

		if err := RecordMigration(conn, name); err != nil {
			return err
		}

		fmt.Printf("  ✅ %s applied\n", name)
		applied++
	}

	if applied > 0 {
		fmt.Printf("✅ %d migration(s) applied\n", applied)
	} else {
		fmt.Println("✅ Schema up to date")
	}
	return nil
}

// MigrationStatus is one row of the `migrate status` report.
type MigrationStatus struct {
	Name    string
	Applied bool
}

// MigrationsStatus reports every embedded migration and whether it has run,
// without applying anything.
func MigrationsStatus() ([]MigrationStatus, error) {
	conn, err := openMigrationsConn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := EnsureMigrationsTable(conn); err != nil {
		return nil, err
	}

	names, err := migrationFiles()
	if err != nil {
		return nil, err
	}

	out := make([]MigrationStatus, 0, len(names))
	for _, name := range names {
		done, err := MigrationApplied(conn, name)
		if err != nil {
			return nil, err
		}
		out = append(out, MigrationStatus{Name: name, Applied: done})
	}
	return out, nil
}

// EnsureMigrationsTable creates the bookkeeping table if it does not exist.
func EnsureMigrationsTable(q Queryable) error {
	_, err := q.Exec(`CREATE TABLE IF NOT EXISTS ` + MIGRATIONS_TABLE + ` (
		version VARCHAR(255) NOT NULL PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT current_timestamp()
	)`)
	if err != nil {
		return fmt.Errorf("failed to create %s table: %w", MIGRATIONS_TABLE, err)
	}
	return nil
}

// MigrationApplied reports whether the named migration file has already run.
func MigrationApplied(q Queryable, version string) (bool, error) {
	var count int
	err := q.QueryRow("SELECT COUNT(*) FROM "+MIGRATIONS_TABLE+" WHERE version = ?", version).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check migration status for %s: %w", version, err)
	}
	return count > 0, nil
}

// RecordMigration marks the named migration file as applied.
func RecordMigration(q Queryable, version string) error {
	if _, err := q.Exec("INSERT INTO "+MIGRATIONS_TABLE+" (version) VALUES (?)", version); err != nil {
		return fmt.Errorf("failed to record migration %s: %w", version, err)
	}
	return nil
}

// migrationFiles returns the embedded migration filenames in lexical order.
//
// Lexical order is apply order ONLY because every filename carries the same
// fixed-width three-digit zero-padded prefix: 001_, 002_, … 010_, … 100_. With
// that scheme lexical and numeric order agree.
//
// Two deviations would silently break ordering, which is why the scheme is
// enforced by a test rather than assumed:
//   - dropping the padding — "10_x.sql" sorts BEFORE "9_x.sql", since '1' < '9'
//   - changing the width — "0100_x.sql" sorts BEFORE "010_x.sql", since '0' < '_'
func migrationFiles() ([]string, error) {
	entries, err := migrationsFS.ReadDir(MIGRATIONS_DIR)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// openMigrationsConn opens a dedicated single-purpose pool for the migration
// run.
//
// The shared pool in DB is intentionally not reused: migration files contain
// multiple statements per Exec, which requires multiStatements on the DSN — a
// parameter the request-serving pool must never carry, because it turns any
// successful SQL injection into an arbitrary-statement primitive.
func openMigrationsConn() (*sql.DB, error) {
	dsn, err := ensureMigrationDSNParams(env.CoreDBDSN)
	if err != nil {
		return nil, err
	}

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	return conn, nil
}

// ensureMigrationDSNParams returns a copy of the DSN carrying the parameters the
// migration runner requires — including the `lattice` schema, which the
// configured DSN does not necessarily name (Init appends it too).
//
// Parsing and re-rendering is delegated to the driver's own mysql.Config so the
// DSN grammar is interpreted exactly as the driver will interpret it; a
// hand-rolled split on "?" mis-parses a DSN whose password contains one.
func ensureMigrationDSNParams(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(normaliseDSNSchema(dsn))
	if err != nil {
		return "", fmt.Errorf("failed to parse database DSN: %w", err)
	}

	// Both are requirements, not defaults: migration files are multi-statement,
	// and schema_migrations.applied_at is scanned as a time.Time.
	cfg.MultiStatements = true
	cfg.ParseTime = true
	cfg.DBName = schema

	return cfg.FormatDSN(), nil
}

// normaliseDSNSchema mirrors Init's handling: strip any path/params the operator
// supplied and point the DSN at this service's schema.
func normaliseDSNSchema(dsn string) string {
	base := dsn
	if idx := strings.IndexAny(base, "/?"); idx != -1 {
		base = base[:idx]
	}
	return base + "/" + schema
}
