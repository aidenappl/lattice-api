package query

import (
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
)

type SearchResults struct {
	Workers    []SearchWorker    `json:"workers"`
	Stacks     []SearchStack     `json:"stacks"`
	Containers []SearchContainer `json:"containers"`
}

type SearchWorker struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

type SearchStack struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
}

type SearchContainer struct {
	ID      int    `json:"id"`
	StackID int    `json:"stack_id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Tag     string `json:"tag"`
	Status  string `json:"status"`
}

// escapeLike escapes SQL LIKE wildcards in user input.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

func Search(engine db.Queryable, q string, limit int) (*SearchResults, error) {
	if limit <= 0 || limit > 10 {
		limit = 5
	}

	pattern := "%" + escapeLike(q) + "%"
	results := &SearchResults{}

	// Workers — match on name or hostname
	wQuery, wArgs, err := sq.Select("id", "name", "hostname", "status").
		From("workers").
		Where(sq.Eq{"active": true}).
		Where(sq.Or{
			sq.Like{"name": pattern},
			sq.Like{"hostname": pattern},
		}).
		OrderBy("name ASC").
		Limit(uint64(limit)).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build workers search query: %w", err)
	}

	wRows, err := engine.Query(wQuery, wArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to search workers: %w", err)
	}
	defer wRows.Close()

	for wRows.Next() {
		var w SearchWorker
		if err := wRows.Scan(&w.ID, &w.Name, &w.Hostname, &w.Status); err != nil {
			return nil, fmt.Errorf("failed to scan worker search result: %w", err)
		}
		results.Workers = append(results.Workers, w)
	}
	if err := wRows.Err(); err != nil {
		return nil, err
	}

	// Stacks — match on name or description
	sQuery, sArgs, err := sq.Select("id", "name", "description", "status").
		From("stacks").
		Where(sq.Eq{"active": true}).
		Where(sq.Or{
			sq.Like{"name": pattern},
			sq.Like{"COALESCE(description, '')": pattern},
		}).
		OrderBy("name ASC").
		Limit(uint64(limit)).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build stacks search query: %w", err)
	}

	sRows, err := engine.Query(sQuery, sArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to search stacks: %w", err)
	}
	defer sRows.Close()

	for sRows.Next() {
		var s SearchStack
		if err := sRows.Scan(&s.ID, &s.Name, &s.Description, &s.Status); err != nil {
			return nil, fmt.Errorf("failed to scan stack search result: %w", err)
		}
		results.Stacks = append(results.Stacks, s)
	}
	if err := sRows.Err(); err != nil {
		return nil, err
	}

	// Containers — match on name or image
	cQuery, cArgs, err := sq.Select("id", "stack_id", "name", "image", "tag", "status").
		From("containers").
		Where(sq.Eq{"active": true}).
		Where(sq.Or{
			sq.Like{"name": pattern},
			sq.Like{"image": pattern},
		}).
		OrderBy("name ASC").
		Limit(uint64(limit)).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build containers search query: %w", err)
	}

	cRows, err := engine.Query(cQuery, cArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to search containers: %w", err)
	}
	defer cRows.Close()

	for cRows.Next() {
		var c SearchContainer
		if err := cRows.Scan(&c.ID, &c.StackID, &c.Name, &c.Image, &c.Tag, &c.Status); err != nil {
			return nil, fmt.Errorf("failed to scan container search result: %w", err)
		}
		results.Containers = append(results.Containers, c)
	}
	if err := cRows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}
