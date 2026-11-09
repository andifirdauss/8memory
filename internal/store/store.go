// Package store persists memories and journey nodes behind one interface,
// with SQLite, PostgreSQL, SQL Server and Oracle implementations.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"8memory/internal/config"
)

// What a prompt did to a feature.
const (
	ChangeAdd     = "add"
	ChangeRemove  = "remove"
	ChangeModify  = "modify"
	ChangeInspect = "inspect"
)

const (
	defaultMemoryLimit = 10
	defaultNodeLimit   = 50
	maxLimit           = 500
	maxNameRunes       = 200
	maxTagsRunes       = 1000
	copyPageSize       = 500
)

// FileRef points to a large file kept outside memory.
type FileRef struct {
	Ref         string `json:"ref" jsonschema:"Where the file is: an alias such as backup://project/2026-11-09/database.sql, or s3://bucket/key. Never a local path."`
	Size        int64  `json:"size,omitempty" jsonschema:"File size in bytes."`
	Kind        string `json:"kind,omitempty" jsonschema:"What the file is, for example postgres-dump or log-archive."`
	RestoreHint string `json:"restore_hint,omitempty" jsonschema:"How to use or restore the file, without any local path."`
	Host        string `json:"host,omitempty" jsonschema:"Machine the file came from. Defaults to this machine."`
}

// Memory records in detail what one prompt did to one feature.
type Memory struct {
	ID        int64     `json:"id"`
	Project   string    `json:"project,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	Change    string    `json:"change,omitempty"`
	Feature   string    `json:"feature,omitempty"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Files     []FileRef `json:"files,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Node is a journey marker: one prompt, with only a title and a time.
type Node struct {
	ID        string    `json:"id"`
	Project   string    `json:"project,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// Query filters memories. Empty Text returns the newest; every other empty
// field means no filter on that field.
type Query struct {
	Text    string
	Project string
	Branch  string
	Change  string
	NodeID  string
	Since   time.Time
	Limit   int
}

// NodeQuery filters journey nodes, newest first. Empty fields mean no filter.
type NodeQuery struct {
	Project string
	Branch  string
	Since   time.Time
	Limit   int
}

// Store is the storage contract every backend implements.
type Store interface {
	// Write saves a memory. Writing the same content to the same project and
	// branch again returns the existing row instead of adding a duplicate.
	Write(ctx context.Context, m Memory) (Memory, error)
	Search(ctx context.Context, q Query) ([]Memory, error)
	// Page returns up to limit memories with ID > afterID, oldest first.
	Page(ctx context.Context, afterID int64, limit int) ([]Memory, error)
	Count(ctx context.Context) (int, error)

	// AddNode saves a journey node, assigning a UUID when ID is empty. Adding
	// an ID that already exists returns the existing node.
	AddNode(ctx context.Context, n Node) (Node, error)
	ListNodes(ctx context.Context, q NodeQuery) ([]Node, error)
	// PageNodes returns up to limit nodes with ID > afterID, in ID order.
	PageNodes(ctx context.Context, afterID string, limit int) ([]Node, error)
	CountNodes(ctx context.Context) (int, error)

	// AddRule saves a rule, assigning a UUID when ID is empty.
	AddRule(ctx context.Context, r Rule) (Rule, error)
	UpdateRule(ctx context.Context, r Rule) (Rule, error)
	DeleteRule(ctx context.Context, id string) error
	ListRules(ctx context.Context) ([]Rule, error)
	// ApplicableRules returns the rules in force for a project and branch.
	ApplicableRules(ctx context.Context, project, branch string) ([]Rule, error)
	PageRules(ctx context.Context, afterID string, limit int) ([]Rule, error)
	CountRules(ctx context.Context) (int, error)

	Close() error
}

// Ping checks that the target is reachable without creating the schema.
func Ping(ctx context.Context, cfg config.Config) error {
	db, _, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	return db.Close()
}

// Open connects to the configured backend and creates its schema if needed.
func Open(ctx context.Context, cfg config.Config) (Store, error) {
	db, d, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	for _, stmt := range d.schema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, redact(fmt.Errorf("create schema: %w", err), cfg.Password)
		}
	}
	return &sqlStore{db: db, d: d}, nil
}

// CopyResult counts the rows the destination gained.
type CopyResult struct {
	Memories int
	Nodes    int
	Rules    int
}

// Copy transfers all rules, journey nodes and memories from src to dst. Node IDs are
// kept, so memories stay linked to their nodes. Existing rows are skipped,
// which makes re-running a partial copy safe.
func Copy(ctx context.Context, src, dst Store) (CopyResult, error) {
	memoriesBefore, err := dst.Count(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	nodesBefore, err := dst.CountNodes(ctx)
	if err != nil {
		return CopyResult{}, err
	}

	rulesBefore, err := dst.CountRules(ctx)
	if err != nil {
		return CopyResult{}, err
	}

	var afterRule string
	for {
		page, err := src.PageRules(ctx, afterRule, copyPageSize)
		if err != nil {
			return CopyResult{}, fmt.Errorf("read source rules: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			if _, err := dst.AddRule(ctx, r); err != nil {
				return CopyResult{}, fmt.Errorf("write rule %s: %w", r.ID, err)
			}
			afterRule = r.ID
		}
	}

	var afterNode string
	for {
		page, err := src.PageNodes(ctx, afterNode, copyPageSize)
		if err != nil {
			return CopyResult{}, fmt.Errorf("read source nodes: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, n := range page {
			if _, err := dst.AddNode(ctx, n); err != nil {
				return CopyResult{}, fmt.Errorf("write node %s: %w", n.ID, err)
			}
			afterNode = n.ID
		}
	}

	var after int64
	for {
		page, err := src.Page(ctx, after, copyPageSize)
		if err != nil {
			return CopyResult{}, fmt.Errorf("read source: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			if _, err := dst.Write(ctx, m); err != nil {
				return CopyResult{}, fmt.Errorf("write memory %d: %w", m.ID, err)
			}
			after = m.ID
		}
	}

	memories, err := dst.Count(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	nodes, err := dst.CountNodes(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	rules, err := dst.CountRules(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	return CopyResult{Memories: memories - memoriesBefore, Nodes: nodes - nodesBefore, Rules: rules - rulesBefore}, nil
}

func connect(ctx context.Context, cfg config.Config) (*sql.DB, dialect, error) {
	d, ok := dialects[cfg.Backend]
	if !ok {
		return nil, dialect{}, fmt.Errorf("unknown backend %q", cfg.Backend)
	}
	dsn, err := cfg.DSN()
	if err != nil {
		return nil, dialect{}, err
	}
	if cfg.Backend == config.SQLite {
		if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
			return nil, dialect{}, fmt.Errorf("create data directory: %w", err)
		}
	}
	db, err := sql.Open(d.driver, dsn)
	if err != nil {
		return nil, dialect{}, redact(err, cfg.Password)
	}
	// A long-lived stdio server is idle most of the time; dropping idle
	// connections avoids reusing ones a remote server has already closed.
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, dialect{}, redact(fmt.Errorf("connect to %s: %w", cfg.Describe(), err), cfg.Password)
	}
	return db, d, nil
}

// redact removes the password from driver errors, which reach the TUI and logs.
func redact(err error, password string) error {
	if err == nil || password == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), password, "***")
	msg = strings.ReplaceAll(msg, url.QueryEscape(password), "***")
	msg = strings.ReplaceAll(msg, url.PathEscape(password), "***")
	return errors.New(msg)
}

// normalize validates a memory and returns it with its dedup hash.
func normalize(m Memory) (Memory, string, error) {
	m.Content = strings.TrimSpace(strings.ReplaceAll(m.Content, "\x00", "")) // PostgreSQL rejects NUL
	m.Project = strings.TrimSpace(m.Project)
	m.Branch = strings.TrimSpace(m.Branch)
	m.Agent = strings.TrimSpace(m.Agent)
	m.Feature = strings.TrimSpace(m.Feature)
	m.Change = strings.ToLower(strings.TrimSpace(m.Change))
	m.Tags = normalizeTags(m.Tags)
	if m.Content == "" {
		return Memory{}, "", errors.New("content must not be empty")
	}
	switch m.Change {
	case "", ChangeAdd, ChangeRemove, ChangeModify, ChangeInspect:
	default:
		return Memory{}, "", fmt.Errorf("change must be one of add, remove, modify, inspect, got %q", m.Change)
	}
	if m.NodeID != "" {
		id, err := uuid.Parse(strings.TrimSpace(m.NodeID))
		if err != nil {
			return Memory{}, "", fmt.Errorf("node_id is not a UUID: %q", m.NodeID)
		}
		m.NodeID = id.String()
	}
	if err := checkNames(m.Project, m.Branch, m.Agent, m.Feature); err != nil {
		return Memory{}, "", err
	}
	if utf8.RuneCountInString(strings.Join(m.Tags, ",")) > maxTagsRunes {
		return Memory{}, "", fmt.Errorf("tags are limited to %d characters in total", maxTagsRunes)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	// File refs are part of the identity: the same note about a new backup
	// must not collapse into the entry for an older one.
	identity := m.Project + "\x00" + m.Branch + "\x00" + m.Content
	for _, file := range m.Files {
		identity += "\x00" + file.Ref
	}
	sum := sha256.Sum256([]byte(identity))
	return m, hex.EncodeToString(sum[:]), nil
}

func encodeFiles(files []FileRef) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	data, err := json.Marshal(files)
	if err != nil {
		return "", fmt.Errorf("encode file refs: %w", err)
	}
	return string(data), nil
}

func decodeFiles(stored string) ([]FileRef, error) {
	if stored == "" {
		return nil, nil
	}
	var files []FileRef
	if err := json.Unmarshal([]byte(stored), &files); err != nil {
		return nil, fmt.Errorf("decode file refs: %w", err)
	}
	return files, nil
}

// normalizeNode validates a node, assigning a time-ordered UUID if it has none.
func normalizeNode(n Node) (Node, error) {
	n.Project = strings.TrimSpace(n.Project)
	n.Branch = strings.TrimSpace(n.Branch)
	n.Agent = strings.TrimSpace(n.Agent)
	n.Title = truncateRunes(strings.Join(strings.Fields(n.Title), " "), maxNameRunes)
	if n.Title == "" {
		return Node{}, errors.New("title must not be empty")
	}
	if err := checkNames(n.Project, n.Branch, n.Agent); err != nil {
		return Node{}, err
	}
	if n.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return Node{}, fmt.Errorf("generate node id: %w", err)
		}
		n.ID = id.String()
	} else {
		id, err := uuid.Parse(strings.TrimSpace(n.ID))
		if err != nil {
			return Node{}, fmt.Errorf("node id is not a UUID: %q", n.ID)
		}
		n.ID = id.String()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	return n, nil
}

func checkNames(names ...string) error {
	for _, name := range names {
		if utf8.RuneCountInString(name) > maxNameRunes {
			return fmt.Errorf("%.40q... exceeds the %d character limit", name, maxNameRunes)
		}
	}
	return nil
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

func normalizeTag(tag string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, ",", " ")))
}

func normalizeTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = normalizeTag(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func clampLimit(n, fallback int) int {
	switch {
	case n <= 0:
		return fallback
	case n > maxLimit:
		return maxLimit
	}
	return n
}
