// Package partitions keeps the high-volume log tables partitioned by day so
// retention frees disk (APL-120).
//
// A DELETE never shrinks an InnoDB file: freed pages are reused but never
// returned to the filesystem, so container_logs.ibd grew to its 45 GB peak and
// filled the control-plane host. Dropping a day partition removes that day's
// file outright, instantly and without needing free space.
//
// Layout, for each table in Tables:
//
//	p_old      everything before the first day partition (late replays land here)
//	pYYYYMMDD  one UTC day each, VALUES LESS THAN the next midnight
//	pmax       MAXVALUE, kept empty by creating days ahead
//
// Retention (Maintain) drops day partitions older than the table's keep window,
// truncates p_old once it is entirely older, and splits pmax so the next
// AheadDays days already exist when their rows arrive.
package partitions

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

// Kind is how a table's partition column is compared.
type Kind int

const (
	// KindDatetime is a DATETIME column: RANGE COLUMNS with date literals.
	KindDatetime Kind = iota
	// KindTimestamp is a TIMESTAMP column, which RANGE COLUMNS does not accept:
	// RANGE on UNIX_TIMESTAMP(column) with epoch seconds.
	KindTimestamp
)

// Table is one partitioned table and how long it keeps rows.
type Table struct {
	Name     string
	Column   string
	Kind     Kind
	KeepDays int
	// Modify is the MODIFY clause (without the keyword) that makes the key
	// columns fit the new primary key, run in the same ALTER as the partitioning.
	Modify string
}

// Tables are the tables kept partitioned by day. Their retention matches the
// DELETE-based retention they had before (retention/retention.go).
var Tables = []Table{
	{Name: "container_logs", Column: "recorded_at", Kind: KindDatetime, KeepDays: 7,
		// INT runs out at ~2.1 B rows, which the 6 GB/day of APL-120 reaches.
		Modify: "id BIGINT NOT NULL AUTO_INCREMENT"},
	{Name: "container_metrics", Column: "recorded_at", Kind: KindTimestamp, KeepDays: 7,
		Modify: "recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP"},
}

const (
	// AheadDays is how many days past today always have a partition.
	AheadDays = 2
	// MaxAutoBytes is the largest table migration 027 partitions on its own.
	// Partitioning rewrites the whole table and blocks writes while it copies,
	// so a larger table is left for an operator (see AGENTS.md, "Log table
	// partitions") and reported at every boot until then.
	MaxAutoBytes = int64(1) << 30

	oldPartition = "p_old"
	maxPartition = "pmax"
	dayLayout    = "20060102"
)

// Event is the Monitor event raised while a table is still unpartitioned.
const Event = "retention.unpartitioned"

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func dayName(d time.Time) string { return "p" + d.Format(dayLayout) }

// bound is the VALUES LESS THAN value for the partition ending at d (midnight UTC).
func (t Table) bound(d time.Time) string {
	if t.Kind == KindTimestamp {
		return fmt.Sprintf("(%d)", d.Unix())
	}
	return "('" + d.Format("2006-01-02 15:04:05") + "')"
}

func (t Table) maxBound() string {
	if t.Kind == KindTimestamp {
		return "MAXVALUE"
	}
	return "(MAXVALUE)"
}

func (t Table) dayDef(d time.Time) string {
	return fmt.Sprintf("PARTITION %s VALUES LESS THAN %s", dayName(d), t.bound(d.AddDate(0, 0, 1)))
}

func (t Table) maxDef() string {
	return fmt.Sprintf("PARTITION %s VALUES LESS THAN %s", maxPartition, t.maxBound())
}

// PartitionDDL is the one ALTER that turns an unpartitioned table into the
// layout above as of now: p_old, the keep window through AheadDays, pmax. The
// primary key gains the partition column, which MariaDB requires of every
// unique key; nothing looks a row up by id alone.
func (t Table) PartitionDDL(now time.Time) string {
	today := dayStart(now)
	first := today.AddDate(0, 0, -t.KeepDays)
	parts := []string{fmt.Sprintf("PARTITION %s VALUES LESS THAN %s", oldPartition, t.bound(first))}
	for d := first; !d.After(today.AddDate(0, 0, AheadDays)); d = d.AddDate(0, 0, 1) {
		parts = append(parts, t.dayDef(d))
	}
	parts = append(parts, t.maxDef())
	by := fmt.Sprintf("RANGE COLUMNS(%s)", t.Column)
	if t.Kind == KindTimestamp {
		by = fmt.Sprintf("RANGE (UNIX_TIMESTAMP(%s))", t.Column)
	}
	return fmt.Sprintf("ALTER TABLE %s MODIFY %s, DROP PRIMARY KEY, ADD PRIMARY KEY (id, %s) PARTITION BY %s (\n    %s)",
		t.Name, t.Modify, t.Column, by, strings.Join(parts, ",\n    "))
}

// Plan is the maintenance one pass would do.
type Plan struct {
	Drop        []string    // day partitions entirely older than the keep window
	TruncateOld bool        // p_old is entirely older than the keep window
	Add         []time.Time // days to split out of pmax
}

// Empty reports whether the plan does nothing.
func (p Plan) Empty() bool { return len(p.Drop) == 0 && !p.TruncateOld && len(p.Add) == 0 }

// PlanMaintenance decides what to drop and add given the table's current
// partition names. A day partition is dropped once its whole day is older than
// KeepDays (the same cutoff as the DELETE it replaces, rounded to whole days);
// days are only ever added after the newest existing one, so pmax stays the
// last partition.
func (t Table) PlanMaintenance(names []string, now time.Time) Plan {
	today := dayStart(now)
	cutoff := today.AddDate(0, 0, -t.KeepDays)
	var p Plan
	var days []time.Time
	hasOld := false
	for _, n := range names {
		switch {
		case n == oldPartition:
			hasOld = true
		case n == maxPartition:
		case strings.HasPrefix(n, "p"):
			d, err := time.Parse(dayLayout, n[1:])
			if err != nil {
				continue // not ours; leave it alone
			}
			days = append(days, d)
			if d.Before(cutoff) {
				p.Drop = append(p.Drop, n)
			}
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	// p_old's bound is fixed when the table is partitioned, at that day's
	// cutoff, and the cutoff only moves forward: everything in it is expired.
	// It only holds late replays of old lines, so it is emptied every pass.
	p.TruncateOld = hasOld
	next := today
	if len(days) > 0 && !days[len(days)-1].Before(today) {
		next = days[len(days)-1].AddDate(0, 0, 1)
	}
	for d := next; !d.After(today.AddDate(0, 0, AheadDays)); d = d.AddDate(0, 0, 1) {
		p.Add = append(p.Add, d)
	}
	return p
}

// Statements renders a plan as DDL. Adds come first, so a failure part way
// through never leaves today without a partition.
func (t Table) Statements(p Plan) []string {
	var out []string
	if len(p.Add) > 0 {
		defs := make([]string, 0, len(p.Add)+1)
		for _, d := range p.Add {
			defs = append(defs, t.dayDef(d))
		}
		defs = append(defs, t.maxDef())
		out = append(out, fmt.Sprintf("ALTER TABLE %s REORGANIZE PARTITION %s INTO (%s)", t.Name, maxPartition, strings.Join(defs, ", ")))
	}
	if len(p.Drop) > 0 {
		out = append(out, fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", t.Name, strings.Join(p.Drop, ", ")))
	}
	if p.TruncateOld {
		out = append(out, fmt.Sprintf("ALTER TABLE %s TRUNCATE PARTITION %s", t.Name, oldPartition))
	}
	return out
}

// Partitions lists a table's partition names in order; nil when unpartitioned.
func Partitions(engine *sql.DB, table string) ([]string, error) {
	rows, err := engine.Query(`SELECT PARTITION_NAME FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL
		ORDER BY PARTITION_ORDINAL_POSITION`, table)
	if err != nil {
		return nil, fmt.Errorf("listing partitions of %s: %w", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// TableBytes is a table's data plus index size as InnoDB reports it.
func TableBytes(engine *sql.DB, table string) (int64, error) {
	var n sql.NullInt64
	err := engine.QueryRow(`SELECT COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0)
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sizing %s: %w", table, err)
	}
	return n.Int64, nil
}

// Maintain runs one maintenance pass on a partitioned table.
func Maintain(ctx context.Context, engine *sql.DB, t Table, now time.Time) error {
	names, err := Partitions(engine, t.Name)
	if err != nil {
		return err
	}
	plan := t.PlanMaintenance(names, now)
	for _, stmt := range t.Statements(plan) {
		if _, err := engine.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("partition maintenance on %s: %w", t.Name, err)
		}
	}
	if len(plan.Drop) > 0 || plan.TruncateOld {
		logger.InfoCtx(ctx, "retention", "dropped old log partitions", logger.F{"table": t.Name, "partitions": strings.Join(plan.Drop, ","), "truncated_old": plan.TruncateOld})
	}
	return nil
}

// Migrate partitions every unpartitioned table in Tables that is at most
// MaxAutoBytes; a larger one is reported and left for an operator. Already
// partitioned tables are skipped, so a manual run followed by this is
// harmless. It is migration 027's hook (db/migrate.go). It always returns nil.
//
// It never fails the boot: a failed migration leaves lattice-api serving on
// the old schema, but this one is optional (retention falls back to DELETE), so
// a table it can't read or can't partition (unexpected live DDL, a lock, no
// disk for the copy) is reported as retention.unpartitioned with the error and
// skipped. 027 is then recorded, and CheckStartup keeps reporting the table at
// every boot until an operator partitions it.
func Migrate(engine *sql.DB, now time.Time) error {
	return migrate(engine, now, MaxAutoBytes)
}

func migrate(engine *sql.DB, now time.Time, maxBytes int64) error {
	ctx := context.Background()
	for _, t := range Tables {
		names, err := Partitions(engine, t.Name)
		if err != nil {
			reportUnpartitioned(ctx, t, -1, err)
			continue
		}
		if len(names) > 0 {
			continue
		}
		size, err := TableBytes(engine, t.Name)
		if err != nil {
			reportUnpartitioned(ctx, t, -1, err)
			continue
		}
		if size > maxBytes {
			reportUnpartitioned(ctx, t, size, nil)
			continue
		}
		if _, err := engine.Exec(t.PartitionDDL(now)); err != nil {
			reportUnpartitioned(ctx, t, size, fmt.Errorf("partitioning failed: %w", err))
			continue
		}
		logger.InfoCtx(ctx, "retention", "partitioned log table by day", logger.F{"table": t.Name, "table_bytes": size})
	}
	return nil
}

// CheckStartup warns, and raises Event, for every table still unpartitioned —
// at every boot, so a table migration 027 skipped is never forgotten.
func CheckStartup(engine *sql.DB) {
	ctx := context.Background()
	for _, t := range Tables {
		names, err := Partitions(engine, t.Name)
		if err != nil || len(names) > 0 {
			continue
		}
		size, err := TableBytes(engine, t.Name)
		if err != nil {
			continue
		}
		reportUnpartitioned(ctx, t, size, nil)
	}
}

// reportUnpartitioned raises Event for a table left unpartitioned: too large
// (err nil), or err when reading or partitioning it failed. size is -1 when
// unknown.
func reportUnpartitioned(ctx context.Context, t Table, size int64, err error) {
	fields := logger.F{"table": t.Name, "table_bytes": size, "max_auto_bytes": MaxAutoBytes, "runbook": "lattice-api AGENTS.md: Log table partitions"}
	if err != nil {
		fields["error"] = err.Error()
	}
	logger.EventCtx(ctx, logger.LevelWarn, Event, "retention",
		"log table is not partitioned; retention cannot free its disk until an operator partitions it", fields)
}
