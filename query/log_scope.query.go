package query

import (
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"
)

// logScope selects one container's rows from a log table (container_logs or
// lifecycle_logs), newest first, so that MariaDB can read them straight off an
// index instead of sorting every row the container has ever logged (APL-119).
//
// A container's rows are matched by container_id, or, for lines stored before
// the name resolved to an ID, by container_id IS NULL AND container_name. As one
// WHERE ... OR ... that needs a filesort over all of the container's rows,
// TEXT message included: ~6s per request for nexus-api. Split into a UNION ALL
// of two branches, each walks its own index — (container_id, recorded_at) and
// (container_name, container_id, recorded_at), migration 026 — backwards and
// stops after limit+offset rows; only those are sorted.
type logScope struct {
	table         string
	columns       []string
	containerID   *int
	containerName *string
	filters       sq.And
	limit         int
	offset        int
}

func (s logScope) branch(where sq.Sqlizer, limit int) sq.SelectBuilder {
	q := sq.Select(s.columns...).From(s.table).Where(where)
	if len(s.filters) > 0 {
		q = q.Where(s.filters)
	}
	return q.OrderBy(s.table + ".recorded_at DESC").Limit(uint64(limit))
}

// ToSql builds the query. With both an ID and a name it is a UNION ALL;
// otherwise a single ordered, limited SELECT.
func (s logScope) ToSql() (string, []any, error) {
	idCol, nameCol := s.table+".container_id", s.table+".container_name"

	var single sq.Sqlizer
	switch {
	case s.containerID != nil && s.containerName != nil:
		// handled below
	case s.containerID != nil:
		single = sq.Eq{idCol: *s.containerID}
	case s.containerName != nil:
		single = sq.Eq{nameCol: *s.containerName}
	default:
		single = sq.Expr("1 = 1")
	}
	if single != nil {
		q := s.branch(single, s.limit)
		if s.offset > 0 {
			q = q.Offset(uint64(s.offset))
		}
		return q.ToSql()
	}

	window := s.limit + s.offset
	byID, idArgs, err := s.branch(sq.Eq{idCol: *s.containerID}, window).ToSql()
	if err != nil {
		return "", nil, err
	}
	byName, nameArgs, err := s.branch(sq.And{sq.Expr(idCol + " IS NULL"), sq.Eq{nameCol: *s.containerName}}, window).ToSql()
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(%s) UNION ALL (%s) ORDER BY recorded_at DESC LIMIT %d", byID, byName, s.limit)
	if s.offset > 0 {
		fmt.Fprintf(&b, " OFFSET %d", s.offset)
	}
	return b.String(), append(idArgs, nameArgs...), nil
}
