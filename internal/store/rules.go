package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Where a rule applies.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
	ScopeBranch  = "branch"
)

const (
	maxRuleRunes = 1000
	ruleColumns  = "r.id, r.rule_scope, r.project, r.branch, r.rule_text, r.created_at, r.updated_at"
)

// Rule is an instruction the user wants every session in its scope to follow.
// Unlike a memory it is written by the user and is meant to be obeyed.
type Rule struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Project   string    `json:"project,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewID returns a time-ordered UUID, for callers that must know a node's ID
// before saving it.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return id.String(), nil
}

func normalizeRule(r Rule) (Rule, error) {
	r.Scope = strings.ToLower(strings.TrimSpace(r.Scope))
	r.Project = strings.TrimSpace(r.Project)
	r.Branch = strings.TrimSpace(r.Branch)
	r.Text = strings.TrimSpace(r.Text)

	switch r.Scope {
	case ScopeGlobal:
		r.Project, r.Branch = "", ""
	case ScopeProject:
		r.Branch = ""
		if r.Project == "" {
			return Rule{}, errors.New("a project rule needs a project")
		}
	case ScopeBranch:
		if r.Project == "" || r.Branch == "" {
			return Rule{}, errors.New("a branch rule needs a project and a branch")
		}
	default:
		return Rule{}, fmt.Errorf("scope must be global, project or branch, got %q", r.Scope)
	}
	if r.Text == "" {
		return Rule{}, errors.New("rule text must not be empty")
	}
	if utf8.RuneCountInString(r.Text) > maxRuleRunes {
		return Rule{}, fmt.Errorf("rule text is limited to %d characters", maxRuleRunes)
	}
	if err := checkNames(r.Project, r.Branch); err != nil {
		return Rule{}, err
	}
	if r.ID == "" {
		id, err := NewID()
		if err != nil {
			return Rule{}, err
		}
		r.ID = id
	} else {
		id, err := uuid.Parse(strings.TrimSpace(r.ID))
		if err != nil {
			return Rule{}, fmt.Errorf("rule id is not a UUID: %q", r.ID)
		}
		r.ID = id.String()
	}
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	return r, nil
}

func (s *sqlStore) ruleByID(ctx context.Context, id string) (Rule, bool, error) {
	b := binder{bind: s.d.bind}
	found, err := s.queryRules(ctx, "SELECT "+ruleColumns+" FROM memory_rules r WHERE r.id = "+b.add(id), b.args)
	if err != nil || len(found) == 0 {
		return Rule{}, false, err
	}
	return found[0], true, nil
}

// AddRule saves a rule. Adding an ID that already exists returns the existing
// rule, which makes re-running a copy safe.
func (s *sqlStore) AddRule(ctx context.Context, in Rule) (Rule, error) {
	r, err := normalizeRule(in)
	if err != nil {
		return Rule{}, err
	}
	if existing, found, err := s.ruleByID(ctx, r.ID); err != nil || found {
		return existing, err
	}
	b := binder{bind: s.d.bind}
	query := fmt.Sprintf(
		"INSERT INTO memory_rules (id, rule_scope, project, branch, rule_text, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s)",
		b.add(r.ID), b.add(r.Scope), b.add(r.Project), b.add(r.Branch), b.add(r.Text), b.add(r.CreatedAt.UnixMilli()), b.add(r.UpdatedAt.UnixMilli()),
	)
	if _, err := s.db.ExecContext(ctx, query, b.args...); err != nil {
		return Rule{}, fmt.Errorf("insert rule: %w", err)
	}
	saved, _, err := s.ruleByID(ctx, r.ID)
	return saved, err
}

// UpdateRule replaces a rule's scope and text.
func (s *sqlStore) UpdateRule(ctx context.Context, in Rule) (Rule, error) {
	if strings.TrimSpace(in.ID) == "" {
		return Rule{}, errors.New("rule id is required")
	}
	in.UpdatedAt = time.Now()
	r, err := normalizeRule(in)
	if err != nil {
		return Rule{}, err
	}
	b := binder{bind: s.d.bind}
	query := fmt.Sprintf(
		"UPDATE memory_rules SET rule_scope = %s, project = %s, branch = %s, rule_text = %s, updated_at = %s WHERE id = %s",
		b.add(r.Scope), b.add(r.Project), b.add(r.Branch), b.add(r.Text), b.add(r.UpdatedAt.UnixMilli()), b.add(r.ID),
	)
	if err := s.execOne(ctx, query, b.args); err != nil {
		return Rule{}, err
	}
	saved, _, err := s.ruleByID(ctx, r.ID)
	return saved, err
}

// DeleteRule removes a rule by ID.
func (s *sqlStore) DeleteRule(ctx context.Context, id string) error {
	b := binder{bind: s.d.bind}
	return s.execOne(ctx, "DELETE FROM memory_rules WHERE id = "+b.add(strings.TrimSpace(id)), b.args)
}

// execOne runs a statement that must affect a rule that exists.
func (s *sqlStore) execOne(ctx context.Context, query string, args []any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("change rule: %w", err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return errors.New("rule not found")
	}
	return nil
}

// ListRules returns every rule, oldest first.
func (s *sqlStore) ListRules(ctx context.Context) ([]Rule, error) {
	return s.queryRules(ctx, "SELECT "+ruleColumns+" FROM memory_rules r ORDER BY r.created_at ASC, r.id ASC", nil)
}

// ApplicableRules returns the rules in force for a location, most specific
// first, so the rules closest to the work survive when space runs out.
func (s *sqlStore) ApplicableRules(ctx context.Context, project, branch string) ([]Rule, error) {
	b := binder{bind: s.d.bind}
	where := "r.rule_scope = " + b.add(ScopeGlobal)
	if project != "" {
		where += " OR (r.rule_scope = " + b.add(ScopeProject) + " AND r.project = " + b.add(project) + ")"
		if branch != "" {
			where += " OR (r.rule_scope = " + b.add(ScopeBranch) + " AND r.project = " + b.add(project) + " AND r.branch = " + b.add(branch) + ")"
		}
	}
	rules, err := s.queryRules(ctx, "SELECT "+ruleColumns+" FROM memory_rules r WHERE "+where+" ORDER BY r.created_at ASC, r.id ASC", b.args)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{ScopeBranch: 0, ScopeProject: 1, ScopeGlobal: 2}
	slices.SortStableFunc(rules, func(a, b Rule) int { return rank[a.Scope] - rank[b.Scope] })
	return rules, nil
}

// PageRules returns up to limit rules with ID > afterID, in ID order.
func (s *sqlStore) PageRules(ctx context.Context, afterID string, limit int) ([]Rule, error) {
	b := binder{bind: s.d.bind}
	query := "SELECT " + ruleColumns + " FROM memory_rules r"
	// Oracle treats '' as NULL, so the first page must not compare against it.
	if afterID != "" {
		query += " WHERE r.id > " + b.add(afterID)
	}
	return s.queryRules(ctx, query+" ORDER BY r.id ASC "+s.d.limit(limit), b.args)
}

// CountRules returns how many rules exist.
func (s *sqlStore) CountRules(ctx context.Context) (int, error) { return s.count(ctx, "memory_rules") }

func (s *sqlStore) queryRules(ctx context.Context, query string, args []any) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		var r Rule
		var project, branch sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(&r.ID, &r.Scope, &project, &branch, &r.Text, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("read rule: %w", err)
		}
		r.Project, r.Branch = project.String, branch.String
		r.CreatedAt, r.UpdatedAt = time.UnixMilli(createdAt).UTC(), time.UnixMilli(updatedAt).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
