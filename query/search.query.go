package query

import (
	"fmt"
	"sort"
	"strings"
	"sync"

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

// matchScore returns a relevance score for how well name matches query.
// Lower is better: 0 = exact, 1 = prefix, 2 = word-boundary, 3 = substring.
func matchScore(name, query string) int {
	lower := strings.ToLower(name)
	lq := strings.ToLower(query)
	if lower == lq {
		return 0
	}
	if strings.HasPrefix(lower, lq) {
		return 1
	}
	for _, sep := range []string{"-", "_", ".", " ", "/"} {
		if strings.Contains(lower, sep+lq) {
			return 2
		}
	}
	return 3
}

// bestScore returns the best (lowest) match score across multiple fields.
func bestScore(query string, fields ...string) int {
	best := 4
	for _, f := range fields {
		if f == "" {
			continue
		}
		if s := matchScore(f, query); s < best {
			best = s
		}
	}
	return best
}

func Search(engine db.Queryable, q string, limit int) (*SearchResults, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	// Fetch more than needed so we can re-rank and truncate
	fetchLimit := limit * 3

	pattern := "%" + escapeLike(q) + "%"
	results := &SearchResults{}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	wg.Add(3)

	// Workers — match on name, hostname, or IP
	go func() {
		defer wg.Done()
		query, args, err := sq.Select("id", "name", "hostname", "status").
			From("workers").
			Where(sq.Eq{"active": true}).
			Where(sq.Or{
				sq.Like{"name": pattern},
				sq.Like{"hostname": pattern},
				sq.Like{"COALESCE(ip_address, '')": pattern},
			}).
			Limit(uint64(fetchLimit)).
			ToSql()
		if err != nil {
			setErr(fmt.Errorf("workers search query: %w", err))
			return
		}
		rows, err := engine.Query(query, args...)
		if err != nil {
			setErr(fmt.Errorf("workers search: %w", err))
			return
		}
		defer rows.Close()
		var workers []SearchWorker
		for rows.Next() {
			var w SearchWorker
			if err := rows.Scan(&w.ID, &w.Name, &w.Hostname, &w.Status); err != nil {
				setErr(fmt.Errorf("scan worker: %w", err))
				return
			}
			workers = append(workers, w)
		}
		if err := rows.Err(); err != nil {
			setErr(err)
			return
		}
		sort.Slice(workers, func(i, j int) bool {
			is := bestScore(q, workers[i].Name, workers[i].Hostname)
			js := bestScore(q, workers[j].Name, workers[j].Hostname)
			if is != js {
				return is < js
			}
			return len(workers[i].Name) < len(workers[j].Name)
		})
		if len(workers) > limit {
			workers = workers[:limit]
		}
		mu.Lock()
		results.Workers = workers
		mu.Unlock()
	}()

	// Stacks — match on name or description
	go func() {
		defer wg.Done()
		query, args, err := sq.Select("id", "name", "description", "status").
			From("stacks").
			Where(sq.Eq{"active": true}).
			Where(sq.Or{
				sq.Like{"name": pattern},
				sq.Like{"COALESCE(description, '')": pattern},
			}).
			Limit(uint64(fetchLimit)).
			ToSql()
		if err != nil {
			setErr(fmt.Errorf("stacks search query: %w", err))
			return
		}
		rows, err := engine.Query(query, args...)
		if err != nil {
			setErr(fmt.Errorf("stacks search: %w", err))
			return
		}
		defer rows.Close()
		var stacks []SearchStack
		for rows.Next() {
			var s SearchStack
			if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Status); err != nil {
				setErr(fmt.Errorf("scan stack: %w", err))
				return
			}
			stacks = append(stacks, s)
		}
		if err := rows.Err(); err != nil {
			setErr(err)
			return
		}
		sort.Slice(stacks, func(i, j int) bool {
			di, dj := "", ""
			if stacks[i].Description != nil {
				di = *stacks[i].Description
			}
			if stacks[j].Description != nil {
				dj = *stacks[j].Description
			}
			is := bestScore(q, stacks[i].Name, di)
			js := bestScore(q, stacks[j].Name, dj)
			if is != js {
				return is < js
			}
			return len(stacks[i].Name) < len(stacks[j].Name)
		})
		if len(stacks) > limit {
			stacks = stacks[:limit]
		}
		mu.Lock()
		results.Stacks = stacks
		mu.Unlock()
	}()

	// Containers — match on name, image, or tag
	go func() {
		defer wg.Done()
		query, args, err := sq.Select("id", "stack_id", "name", "image", "tag", "status").
			From("containers").
			Where(sq.Eq{"active": true}).
			Where(sq.Or{
				sq.Like{"name": pattern},
				sq.Like{"image": pattern},
				sq.Like{"tag": pattern},
			}).
			Limit(uint64(fetchLimit)).
			ToSql()
		if err != nil {
			setErr(fmt.Errorf("containers search query: %w", err))
			return
		}
		rows, err := engine.Query(query, args...)
		if err != nil {
			setErr(fmt.Errorf("containers search: %w", err))
			return
		}
		defer rows.Close()
		var containers []SearchContainer
		for rows.Next() {
			var c SearchContainer
			if err := rows.Scan(&c.ID, &c.StackID, &c.Name, &c.Image, &c.Tag, &c.Status); err != nil {
				setErr(fmt.Errorf("scan container: %w", err))
				return
			}
			containers = append(containers, c)
		}
		if err := rows.Err(); err != nil {
			setErr(err)
			return
		}
		sort.Slice(containers, func(i, j int) bool {
			is := bestScore(q, containers[i].Name, containers[i].Image, containers[i].Tag)
			js := bestScore(q, containers[j].Name, containers[j].Image, containers[j].Tag)
			if is != js {
				return is < js
			}
			return len(containers[i].Name) < len(containers[j].Name)
		})
		if len(containers) > limit {
			containers = containers[:limit]
		}
		mu.Lock()
		results.Containers = containers
		mu.Unlock()
	}()

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}
