package db

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// These tests are the reason the runner can be as simple as it is.
//
// The runner gives at-least-once, not exactly-once: a migration that fails
// partway is never recorded as applied and is retried in full. That is only safe
// if every statement can run twice, so re-runnability is enforced here rather
// than left to whoever writes the next file at 2am.

var (
	createTableRE  = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?`)
	createIndexRE  = regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX\s+(IF\s+NOT\s+EXISTS\s+)?`)
	addColumnRE    = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+(IF\s+NOT\s+EXISTS\s+)?`)
	dropColumnRE   = regexp.MustCompile(`(?i)DROP\s+COLUMN\s+(IF\s+EXISTS\s+)?`)
	migrationNameR = regexp.MustCompile(`^\d{3}_[a-z0-9_]+\.sql$`)
)

func migrationContents(t *testing.T) map[string]string {
	t.Helper()
	names, err := migrationFiles()
	if err != nil {
		t.Fatalf("failed to list migrations: %v", err)
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		content, err := migrationsFS.ReadFile(MIGRATIONS_DIR + "/" + name)
		if err != nil {
			t.Fatalf("failed to read %s: %v", name, err)
		}
		out[name] = string(content)
	}
	return out
}

// TestMigrationNamesAreOrderable pins the filename convention that makes lexical
// order equal apply order. Dropping the zero-padding sorts "10_" before "9_";
// widening it sorts "0100_" before "010_". Either would reorder migrations
// silently, which is the worst way for this to break.
func TestMigrationNamesAreOrderable(t *testing.T) {
	names, err := migrationFiles()
	if err != nil {
		t.Fatalf("failed to list migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations found — the embed pattern is probably wrong")
	}

	seen := map[string]string{}
	for _, name := range names {
		if !migrationNameR.MatchString(name) {
			t.Errorf("%q does not match NNN_lower_snake.sql — three digits, zero-padded", name)
			continue
		}
		prefix := name[:3]
		if other, dup := seen[prefix]; dup {
			t.Errorf("duplicate migration number %s: %q and %q", prefix, other, name)
		}
		seen[prefix] = name
	}
}

// stripSQLComments removes -- line comments so prose about SQL is not mistaken
// for SQL. Without this, a migration explaining *why* it uses IF NOT EXISTS gets
// flagged for not using it.
func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if idx := strings.Index(line, "--"); idx != -1 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestMigrationsAreGuarded is the one that keeps retries safe.
func TestMigrationsAreGuarded(t *testing.T) {
	for name, raw := range migrationContents(t) {
		content := stripSQLComments(raw)
		t.Run(name, func(t *testing.T) {
			for _, m := range createTableRE.FindAllStringSubmatch(content, -1) {
				if strings.TrimSpace(m[1]) == "" {
					t.Errorf("CREATE TABLE without IF NOT EXISTS: %q", strings.TrimSpace(m[0]))
				}
			}
			for _, m := range createIndexRE.FindAllStringSubmatch(content, -1) {
				if strings.TrimSpace(m[2]) == "" {
					t.Errorf("CREATE INDEX without IF NOT EXISTS: %q", strings.TrimSpace(m[0]))
				}
			}
			for _, m := range addColumnRE.FindAllStringSubmatch(content, -1) {
				if strings.TrimSpace(m[1]) == "" {
					t.Errorf("ADD COLUMN without IF NOT EXISTS: %q — a retry would fail with error 1060", strings.TrimSpace(m[0]))
				}
			}
			for _, m := range dropColumnRE.FindAllStringSubmatch(content, -1) {
				if strings.TrimSpace(m[1]) == "" {
					t.Errorf("DROP COLUMN without IF EXISTS: %q — a retry would fail with error 1091", strings.TrimSpace(m[0]))
				}
			}
		})
	}
}

// TestMigrationsAreNotEmpty catches a file added as a placeholder and never
// filled in. It would be recorded as applied and never revisited.
func TestMigrationsAreNotEmpty(t *testing.T) {
	for name, content := range migrationContents(t) {
		stripped := strings.TrimSpace(content)
		var meaningful int
		for _, line := range strings.Split(stripped, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				meaningful++
			}
		}
		if meaningful == 0 {
			t.Errorf("%s contains no statements — it would be recorded as applied and never run again", name)
		}
	}
}

// TestNewDDLDoesNotGoInTheLegacyBlock guards the freeze on db.go's inline
// migrate() calls. New DDL belongs in a numbered file where it is ordered,
// recorded, and fatal on failure — the legacy block is none of those things.
func TestNewDDLDoesNotGoInTheLegacyBlock(t *testing.T) {
	const legacyStatementCount = 52
	content, err := migrationsFS.ReadFile(MIGRATIONS_DIR + "/012_backup_guardrails.sql")
	if err != nil || len(content) == 0 {
		t.Fatalf("expected the first migration to exist: %v", err)
	}

	// A tripwire rather than a parser: if someone adds to the frozen block, this
	// count moves and they are made to read the comment explaining why not to.
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("failed to read db.go: %v", err)
	}
	if got := strings.Count(string(source), "migrate(db,"); got != legacyStatementCount {
		t.Errorf("the frozen legacy migrate() block in db.go changed (%d statements, expected %d).\n"+
			"New DDL must go in db/migrations/NNN_*.sql, not in Init — see the doc comment on RunMigrations.", got, legacyStatementCount)
	}
}
