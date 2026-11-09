package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	selectColumns = "m.id, m.project, m.branch, m.agent, m.node_id, m.change_type, m.feature, m.content, m.tags, m.file_refs, m.created_at"
	nodeColumns   = "n.id, n.project, n.branch, n.agent, n.title, n.created_at"
)

// sqlStore implements Store for every database/sql backend. What differs
// between engines lives in dialect.
type sqlStore struct {
	db *sql.DB
	d  dialect
}

func (s *sqlStore) Close() error { return s.db.Close() }

// Write looks up the hash on both sides of the insert instead of using an
// engine-specific upsert. The second lookup also resolves a unique-key
// conflict when two agents write the same memory at the same moment.
func (s *sqlStore) Write(ctx context.Context, in Memory) (Memory, error) {
	m, hash, err := normalize(in)
	if err != nil {
		return Memory{}, err
	}
	if existing, found, err := s.byHash(ctx, hash); err != nil || found {
		return existing, err
	}

	files, err := encodeFiles(m.Files)
	if err != nil {
		return Memory{}, err
	}

	b := binder{bind: s.d.bind}
	query := fmt.Sprintf(
		"INSERT INTO memories (content_hash, project, branch, agent, node_id, change_type, feature, content, tags, file_refs, created_at)"+
			" VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)",
		b.add(hash), b.add(m.Project), b.add(m.Branch), b.add(m.Agent), b.add(m.NodeID), b.add(m.Change), b.add(m.Feature),
		b.add(m.Content), b.add(strings.Join(tagsWithFeature(m), ",")), b.add(files), b.add(m.CreatedAt.UnixMilli()),
	)
	_, insertErr := s.db.ExecContext(ctx, query, b.args...)

	saved, found, err := s.byHash(ctx, hash)
	switch {
	case err != nil:
		return Memory{}, err
	case found:
		return saved, nil
	case insertErr != nil:
		return Memory{}, fmt.Errorf("insert memory: %w", insertErr)
	}
	return Memory{}, fmt.Errorf("memory %s missing after insert", hash)
}

// tagsWithFeature stores the feature name in the indexed tags column, so every
// backend finds a memory by its feature without a separate index. Reads
// remove it again.
func tagsWithFeature(m Memory) []string {
	feature := normalizeTag(m.Feature)
	if feature == "" || slices.Contains(m.Tags, feature) {
		return m.Tags
	}
	return append(slices.Clone(m.Tags), feature)
}

func (s *sqlStore) byHash(ctx context.Context, hash string) (Memory, bool, error) {
	b := binder{bind: s.d.bind}
	query := "SELECT " + selectColumns + " FROM memories m WHERE m.content_hash = " + b.add(hash)
	found, err := s.query(ctx, query, b.args)
	if err != nil || len(found) == 0 {
		return Memory{}, false, err
	}
	return found[0], true, nil
}

func (s *sqlStore) Search(ctx context.Context, q Query) ([]Memory, error) {
	limit := clampLimit(q.Limit, defaultMemoryLimit)
	groups := parseQuery(q.Text)
	b := binder{bind: s.d.bind}
	// Called after the search expression is bound, because SQLite's positional
	// placeholders must appear in argument order.
	filters := func() string { return memoryFilters(&b, q) }

	if len(groups) > 0 {
		query := s.d.search(&b, groups, filters, limit)
		return s.query(ctx, query, b.args)
	}
	query := "SELECT " + selectColumns + " FROM memories m WHERE 1 = 1" + filters() + " ORDER BY m.id DESC " + s.d.limit(limit)
	return s.query(ctx, query, b.args)
}

func memoryFilters(b *binder, q Query) string {
	var f strings.Builder
	equal := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			f.WriteString(" AND m." + column + " = " + b.add(value))
		}
	}
	equal("project", q.Project)
	equal("branch", q.Branch)
	equal("change_type", q.Change)
	equal("node_id", q.NodeID)
	if !q.Since.IsZero() {
		f.WriteString(" AND m.created_at >= " + b.add(q.Since.UnixMilli()))
	}
	return f.String()
}

func (s *sqlStore) Page(ctx context.Context, afterID int64, limit int) ([]Memory, error) {
	b := binder{bind: s.d.bind}
	query := "SELECT " + selectColumns + " FROM memories m WHERE m.id > " + b.add(afterID) +
		" ORDER BY m.id ASC " + s.d.limit(limit)
	return s.query(ctx, query, b.args)
}

func (s *sqlStore) Count(ctx context.Context) (int, error) { return s.count(ctx, "memories") }

func (s *sqlStore) CountNodes(ctx context.Context) (int, error) { return s.count(ctx, "journey_nodes") }

func (s *sqlStore) count(ctx context.Context, table string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

func (s *sqlStore) query(ctx context.Context, query string, args []any) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	out := []Memory{}
	for rows.Next() {
		var m Memory
		// Oracle stores empty strings as NULL, so these columns can be NULL.
		var project, branch, agent, nodeID, change, feature, tags, files sql.NullString
		var createdAt int64
		if err := rows.Scan(&m.ID, &project, &branch, &agent, &nodeID, &change, &feature, &m.Content, &tags, &files, &createdAt); err != nil {
			return nil, fmt.Errorf("read memory: %w", err)
		}
		m.Project, m.Branch, m.Agent, m.NodeID, m.Change, m.Feature =
			project.String, branch.String, agent.String, nodeID.String, change.String, feature.String
		m.Tags = visibleTags(tags.String, m.Feature)
		if m.Files, err = decodeFiles(files.String); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(createdAt).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

func visibleTags(stored, feature string) []string {
	if stored == "" {
		return nil
	}
	hidden := normalizeTag(feature)
	var out []string
	for _, tag := range strings.Split(stored, ",") {
		if tag != hidden {
			out = append(out, tag)
		}
	}
	return out
}

// AddNode checks for the ID first so that re-running a copy skips existing
// nodes on every engine without an engine-specific upsert.
func (s *sqlStore) AddNode(ctx context.Context, in Node) (Node, error) {
	n, err := normalizeNode(in)
	if err != nil {
		return Node{}, err
	}
	lookup := binder{bind: s.d.bind}
	existing, err := s.queryNodes(ctx, "SELECT "+nodeColumns+" FROM journey_nodes n WHERE n.id = "+lookup.add(n.ID), lookup.args)
	if err != nil {
		return Node{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}

	b := binder{bind: s.d.bind}
	query := fmt.Sprintf(
		"INSERT INTO journey_nodes (id, project, branch, agent, title, created_at) VALUES (%s, %s, %s, %s, %s, %s)",
		b.add(n.ID), b.add(n.Project), b.add(n.Branch), b.add(n.Agent), b.add(n.Title), b.add(n.CreatedAt.UnixMilli()),
	)
	if _, err := s.db.ExecContext(ctx, query, b.args...); err != nil {
		return Node{}, fmt.Errorf("insert journey node: %w", err)
	}
	n.CreatedAt = time.UnixMilli(n.CreatedAt.UnixMilli()).UTC()
	return n, nil
}

func (s *sqlStore) ListNodes(ctx context.Context, q NodeQuery) ([]Node, error) {
	b := binder{bind: s.d.bind}
	query := "SELECT " + nodeColumns + " FROM journey_nodes n WHERE 1 = 1"
	if project := strings.TrimSpace(q.Project); project != "" {
		query += " AND n.project = " + b.add(project)
	}
	if branch := strings.TrimSpace(q.Branch); branch != "" {
		query += " AND n.branch = " + b.add(branch)
	}
	if !q.Since.IsZero() {
		query += " AND n.created_at >= " + b.add(q.Since.UnixMilli())
	}
	query += " ORDER BY n.created_at DESC, n.id DESC " + s.d.limit(clampLimit(q.Limit, defaultNodeLimit))
	return s.queryNodes(ctx, query, b.args)
}

func (s *sqlStore) PageNodes(ctx context.Context, afterID string, limit int) ([]Node, error) {
	b := binder{bind: s.d.bind}
	query := "SELECT " + nodeColumns + " FROM journey_nodes n"
	// Oracle treats '' as NULL, so the first page must not compare against it.
	if afterID != "" {
		query += " WHERE n.id > " + b.add(afterID)
	}
	return s.queryNodes(ctx, query+" ORDER BY n.id ASC "+s.d.limit(limit), b.args)
}

func (s *sqlStore) queryNodes(ctx context.Context, query string, args []any) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query journey nodes: %w", err)
	}
	defer rows.Close()

	out := []Node{}
	for rows.Next() {
		var n Node
		var project, branch, agent sql.NullString
		var createdAt int64
		if err := rows.Scan(&n.ID, &project, &branch, &agent, &n.Title, &createdAt); err != nil {
			return nil, fmt.Errorf("read journey node: %w", err)
		}
		n.Project, n.Branch, n.Agent = project.String, branch.String, agent.String
		n.CreatedAt = time.UnixMilli(createdAt).UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

// binder collects query arguments and renders their placeholders.
type binder struct {
	bind func(n int) string
	args []any
}

func (b *binder) add(v any) string {
	b.args = append(b.args, v)
	return b.bind(len(b.args))
}
