package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"8memory/internal/config"
)

// serverBackends maps env vars holding a JSON config.Config to backend names.
// Rows are isolated by a unique project per run, so nothing is ever deleted.
var serverBackends = map[string]string{
	"postgres":  "MEMORY8_TEST_POSTGRES",
	"sqlserver": "MEMORY8_TEST_SQLSERVER",
	"oracle":    "MEMORY8_TEST_ORACLE",
}

func TestBackends(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		runConformance(t, sqliteConfig(t))
	})
	for name, env := range serverBackends {
		t.Run(name, func(t *testing.T) {
			raw := os.Getenv(env)
			if raw == "" {
				t.Skipf("%s not set", env)
			}
			var cfg config.Config
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatal(err)
			}
			runConformance(t, cfg)
		})
	}
}

func sqliteConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Backend: config.SQLite, Path: filepath.Join(t.TempDir(), "folder with spaces", "memory.db")}
}

func runConformance(t *testing.T, cfg config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Opening an existing schema a second time must work.
	again, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	again.Close()

	project := fmt.Sprintf("t-%d", time.Now().UnixNano())
	write := func(content string, tags ...string) Memory {
		t.Helper()
		m, err := s.Write(ctx, Memory{Project: project, Agent: "claude", Content: content, Tags: tags})
		if err != nil {
			t.Fatalf("write %q: %v", content, err)
		}
		return m
	}
	search := func(text string) []Memory {
		t.Helper()
		found, err := s.Search(ctx, Query{Text: text, Project: project, Limit: 50})
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		return found
	}
	expect := func(text string, want ...Memory) {
		t.Helper()
		found := search(text)
		ids := map[int64]bool{}
		for _, m := range found {
			ids[m.ID] = true
		}
		if len(found) != len(want) {
			t.Errorf("search %q: got %d results, want %d", text, len(found), len(want))
			return
		}
		for _, m := range want {
			if !ids[m.ID] {
				t.Errorf("search %q: missing memory %d (%q)", text, m.ID, m.Content)
			}
		}
	}

	auth := write("Auth moved to session cookie because the refresh token was faulty", "Login", "authentication")
	cache := write("Redis cache removed for simplicity")
	percent := write("Coverage rose 100% after refactor")
	write("Coverage rose 100 points in another module")
	underscore := write("Column user_id added")
	bracket := write("Regex [abc] fixed")
	unicode := write("Schema change 认证 with résumé")

	t.Run("round trip", func(t *testing.T) {
		if auth.ID == 0 || auth.Project != project || auth.Agent != "claude" || auth.CreatedAt.IsZero() {
			t.Fatalf("unexpected memory: %+v", auth)
		}
		if strings.Join(auth.Tags, "|") != "login|authentication" {
			t.Fatalf("tags: %v", auth.Tags)
		}
		if unicode.Content != "Schema change 认证 with résumé" {
			t.Fatalf("content: %q", unicode.Content)
		}
	})
	t.Run("deduplicates", func(t *testing.T) {
		if dup := write("  Redis cache removed for simplicity  "); dup.ID != cache.ID {
			t.Fatalf("got new id %d, want %d", dup.ID, cache.ID)
		}
	})
	t.Run("empty optional fields", func(t *testing.T) {
		m, err := s.Write(ctx, Memory{Content: "no project " + project})
		if err != nil {
			t.Fatal(err)
		}
		if m.Project != "" || m.Agent != "" || len(m.Tags) != 0 {
			t.Fatalf("unexpected: %+v", m)
		}
	})
	t.Run("rejects empty content", func(t *testing.T) {
		if _, err := s.Write(ctx, Memory{Content: "   "}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("query syntax", func(t *testing.T) {
		expect("session cookie", auth)
		expect("SESSION", auth)
		expect("session redis")
		expect("session OR redis", auth, cache)
		expect(`"refresh token"`, auth)
		expect(`"token refresh"`)
		expect("simpl*", cache)
		expect("authentication", auth) // tag
		expect("认证", unicode)
	})
	t.Run("hostile queries do not fail", func(t *testing.T) {
		for _, q := range []string{`"unterminated`, `foo:bar`, `a-b`, `---`, `*`, `OR`, `it's`, `\`, `NEAR(`, `'; DROP TABLE memories; --`, `%`, `_`, `[`} {
			search(q)
		}
		if n, err := s.Count(ctx); err != nil || n == 0 {
			t.Fatalf("count after hostile queries: %d, %v", n, err)
		}
	})
	t.Run("wildcards are literal", func(t *testing.T) {
		if cfg.Backend != config.SQLServer && cfg.Backend != config.Oracle {
			t.Skip("tokenizing backends ignore punctuation")
		}
		expect("100%", percent)
		expect("user_id", underscore)
		expect("[abc]", bracket)
	})
	t.Run("recent and limit", func(t *testing.T) {
		found, err := s.Search(ctx, Query{Project: project, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 2 || found[0].ID != unicode.ID {
			t.Fatalf("got %+v", found)
		}
	})
	t.Run("project filter", func(t *testing.T) {
		found, err := s.Search(ctx, Query{Text: "session", Project: project + "-other"})
		if err != nil || len(found) != 0 {
			t.Fatalf("got %v, %v", found, err)
		}
	})
	t.Run("long content", func(t *testing.T) {
		long := strings.Repeat("a long sentence ", 8000) + "endmarker"
		saved := write(long)
		if saved.Content != strings.TrimSpace(long) {
			t.Fatalf("stored %d bytes, want %d", len(saved.Content), len(long))
		}
		expect("endmarker", saved)
	})
	t.Run("concurrent identical and distinct writes", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 40)
		for i := range 40 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				content := "concurrent same"
				if i%2 == 0 {
					content = fmt.Sprintf("concurrent other %d", i)
				}
				_, err := s.Write(ctx, Memory{Project: project, Content: content})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
		expect(`"concurrent same"`, search(`"concurrent same"`)[0])
	})
	t.Run("branches are separate but readable", func(t *testing.T) {
		const content = "same note on two branches"
		main, err := s.Write(ctx, Memory{Project: project, Branch: "main", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Write(ctx, Memory{Project: project, Branch: "feature/x", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		if main.ID == other.ID || other.Branch != "feature/x" {
			t.Fatalf("branches merged: %+v %+v", main, other)
		}
		only, err := s.Search(ctx, Query{Text: "note", Project: project, Branch: "feature/x"})
		if err != nil || len(only) != 1 || only[0].ID != other.ID {
			t.Fatalf("one branch: %+v, %v", only, err)
		}
		if every := search(`"same note"`); len(every) != 2 {
			t.Fatalf("every branch: %+v", every)
		}
	})
	t.Run("change details", func(t *testing.T) {
		nodeID := fmt.Sprintf("0195f3a2-7c10-7000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
		added, err := s.Write(ctx, Memory{Project: project, Change: ChangeAdd, Feature: "Export CSV", Content: "new button in the report", NodeID: nodeID, Tags: []string{"report"}})
		if err != nil {
			t.Fatal(err)
		}
		if added.Change != ChangeAdd || added.Feature != "Export CSV" || added.NodeID != nodeID || strings.Join(added.Tags, "|") != "report" {
			t.Fatalf("%+v", added)
		}
		inspected, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Audit log", Content: "no issues", NodeID: nodeID})
		if err != nil {
			t.Fatal(err)
		}
		expect(`"export csv"`, added) // found by feature, which is not in the content
		byChange, err := s.Search(ctx, Query{Project: project, Change: ChangeInspect})
		if err != nil || len(byChange) != 1 || byChange[0].ID != inspected.ID {
			t.Fatalf("by change: %+v, %v", byChange, err)
		}
		byNode, err := s.Search(ctx, Query{NodeID: nodeID, Limit: 50})
		if err != nil || len(byNode) != 2 {
			t.Fatalf("by node: %+v, %v", byNode, err)
		}
		if _, err := s.Write(ctx, Memory{Content: "x", Change: "delete"}); err == nil {
			t.Fatal("accepted an unknown change")
		}
		if _, err := s.Write(ctx, Memory{Content: "x", NodeID: "not-uuid"}); err == nil {
			t.Fatal("accepted a malformed node id")
		}
	})
	t.Run("file refs", func(t *testing.T) {
		files := []FileRef{
			{Ref: "backup://project/2026-11-09/database.sql", Size: 5368709120, Kind: "postgres-dump", RestoreHint: "psql -f database.sql — restore to 认证", Host: "laptop"},
			{Ref: "s3://archive/uploads.tar", Host: "vps"},
		}
		saved, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Backup", Content: "backup before migration", Files: files})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(saved.Files, files) {
			t.Fatalf("%+v", saved.Files)
		}
		if plain := write("note without files"); plain.Files != nil {
			t.Fatalf("%+v", plain.Files)
		}
		other, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Backup", Content: "backup before migration", Files: files[:1]})
		if err != nil || other.ID == saved.ID {
			t.Fatalf("different refs must be a new memory: %+v, %v", other, err)
		}

		src, err := Open(ctx, sqliteConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		copyProject := project + "-files"
		if _, err := src.Write(ctx, Memory{Project: copyProject, Content: "copied with files", Files: files}); err != nil {
			t.Fatal(err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 1 {
			t.Fatalf("copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		copied, err := s.Search(ctx, Query{Project: copyProject})
		if err != nil || len(copied) != 1 || !reflect.DeepEqual(copied[0].Files, files) {
			t.Fatalf("%+v, %v", copied, err)
		}
	})
	t.Run("rules", func(t *testing.T) {
		rulesProject := project + "-rules"
		add := func(scope, proj, branch, text string) Rule {
			t.Helper()
			r, err := s.AddRule(ctx, Rule{Scope: scope, Project: proj, Branch: branch, Text: text})
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		global := add(ScopeGlobal, "ignored", "ignored", "global rule "+project)
		if global.Project != "" || global.Branch != "" {
			t.Fatalf("%+v", global)
		}
		add(ScopeProject, rulesProject, "ignored", "project rule")
		onMain := add(ScopeBranch, rulesProject, "main", "main branch rule 认证")
		add(ScopeBranch, rulesProject, "feature", "feature branch rule")
		add(ScopeProject, rulesProject+"-other", "", "other project rule")

		texts := func(project, branch string) string {
			t.Helper()
			rules, err := s.ApplicableRules(ctx, project, branch)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, r := range rules {
				if r.Scope != ScopeGlobal || r.Text == global.Text {
					out = append(out, r.Text)
				}
			}
			return strings.Join(out, "|")
		}
		if got := texts(rulesProject, "main"); got != "main branch rule 认证|project rule|"+global.Text {
			t.Fatal(got)
		}
		if got := texts(rulesProject, ""); got != "project rule|"+global.Text {
			t.Fatal(got)
		}
		if got := texts("", ""); got != global.Text {
			t.Fatal(got)
		}

		onMain.Text, onMain.Scope, onMain.Project, onMain.Branch = "changed rule", ScopeProject, rulesProject, ""
		updated, err := s.UpdateRule(ctx, onMain)
		if err != nil || updated.Text != "changed rule" || updated.Scope != ScopeProject || updated.Branch != "" {
			t.Fatalf("%+v, %v", updated, err)
		}
		if err := s.DeleteRule(ctx, updated.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRule(ctx, updated.ID); err == nil {
			t.Fatal("deleted a rule that no longer exists")
		}
		if _, err := s.UpdateRule(ctx, updated); err == nil {
			t.Fatal("updated a rule that no longer exists")
		}
		for _, bad := range []Rule{
			{Scope: "all", Text: "x"},
			{Scope: ScopeProject, Text: "x"},
			{Scope: ScopeBranch, Project: "p", Text: "x"},
			{Scope: ScopeGlobal, Text: "  "},
			{Scope: ScopeGlobal, Text: strings.Repeat("x", maxRuleRunes+1)},
		} {
			if _, err := s.AddRule(ctx, bad); err == nil {
				t.Errorf("accepted %+v", bad)
			}
		}
		if listed, err := s.ListRules(ctx); err != nil || len(listed) == 0 {
			t.Fatalf("%+v, %v", listed, err)
		}

		src, err := Open(ctx, sqliteConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		original, err := src.AddRule(ctx, Rule{Scope: ScopeBranch, Project: project + "-copied", Branch: "main", Text: "copied"})
		if err != nil {
			t.Fatal(err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Rules != 1 {
			t.Fatalf("copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Rules != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		copied, err := s.ApplicableRules(ctx, project+"-copied", "main")
		if err != nil {
			t.Fatal(err)
		}
		if copied[0].ID != original.ID || copied[0].Text != "copied" {
			t.Fatalf("%+v", copied[0])
		}
	})
	t.Run("since filter", func(t *testing.T) {
		old, err := s.Write(ctx, Memory{Project: project + "-since", Content: "old note", CreatedAt: time.Now().Add(-48 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		found, err := s.Search(ctx, Query{Text: "old", Project: old.Project, Since: time.Now().Add(-24 * time.Hour)})
		if err != nil || len(found) != 0 {
			t.Fatalf("%+v, %v", found, err)
		}
		found, err = s.Search(ctx, Query{Text: "old", Project: old.Project, Since: time.Now().Add(-72 * time.Hour)})
		if err != nil || len(found) != 1 {
			t.Fatalf("%+v, %v", found, err)
		}
	})
	t.Run("journey", func(t *testing.T) {
		journey := project + "-journey"
		add := func(branch, title string, age time.Duration) Node {
			t.Helper()
			n, err := s.AddNode(ctx, Node{Project: journey, Branch: branch, Agent: "codex", Title: title, CreatedAt: time.Now().Add(-age)})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		add("main", "a month ago", 20*24*time.Hour)
		week := add("feature", "this week", 3*24*time.Hour)
		today := add("main", "today", time.Minute)
		if _, err := uuid.Parse(today.ID); err != nil {
			t.Fatal(err)
		}
		titles := func(q NodeQuery) string {
			t.Helper()
			q.Project = journey
			nodes, err := s.ListNodes(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, n := range nodes {
				out = append(out, n.Title)
			}
			return strings.Join(out, "|")
		}
		if got := titles(NodeQuery{}); got != "today|this week|a month ago" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Since: time.Now().Add(-24 * time.Hour)}); got != "today" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Since: time.Now().Add(-7 * 24 * time.Hour)}); got != "today|this week" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Branch: "main"}); got != "today|a month ago" {
			t.Fatal(got)
		}
		again, err := s.AddNode(ctx, Node{ID: week.ID, Title: "other title"})
		if err != nil || again.Title != "this week" {
			t.Fatalf("existing id: %+v, %v", again, err)
		}

		noBranch, err := s.AddNode(ctx, Node{Title: strings.Repeat("lengthy\n", 100)})
		if err != nil || noBranch.Branch != "" || utf8.RuneCountInString(noBranch.Title) != maxNameRunes || strings.Contains(noBranch.Title, "\n") {
			t.Fatalf("%+v, %v", noBranch, err)
		}
		if _, err := s.AddNode(ctx, Node{Title: "   "}); err == nil {
			t.Fatal("accepted an empty title")
		}
		if _, err := s.AddNode(ctx, Node{ID: "not-uuid", Title: "x"}); err == nil {
			t.Fatal("accepted a malformed id")
		}
	})
	t.Run("copy keeps journey links", func(t *testing.T) {
		src, err := Open(ctx, sqliteConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		copyProject := project + "-linked"
		var last Node
		for i := range 502 {
			if last, err = src.AddNode(ctx, Node{Project: copyProject, Branch: "main", Title: fmt.Sprintf("prompt %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := src.Write(ctx, Memory{Project: copyProject, Branch: "main", NodeID: last.ID, Change: ChangeModify, Feature: "Linked feature", Content: "linked text"}); err != nil {
			t.Fatal(err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Nodes != 502 || n.Memories != 1 {
			t.Fatalf("first copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Nodes != 0 || n.Memories != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		linked, err := s.Search(ctx, Query{NodeID: last.ID})
		if err != nil || len(linked) != 1 || linked[0].Feature != "Linked feature" || linked[0].Change != ChangeModify || linked[0].Branch != "main" || len(linked[0].Tags) != 0 {
			t.Fatalf("%+v, %v", linked, err)
		}
	})
	t.Run("copy from sqlite is repeatable", func(t *testing.T) {
		src, err := Open(ctx, sqliteConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		copyProject := project + "-copy"
		created := time.UnixMilli(1700000000123).UTC()
		for i := range 1203 {
			if _, err := src.Write(ctx, Memory{Project: copyProject, Content: fmt.Sprintf("copy %d", i), Tags: []string{"moved"}, CreatedAt: created}); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 1203 {
			t.Fatalf("first copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		found, err := s.Search(ctx, Query{Text: "moved", Project: copyProject, Limit: 1})
		if err != nil || len(found) != 1 {
			t.Fatalf("search copy: %v, %v", found, err)
		}
		if !found[0].CreatedAt.Equal(created) {
			t.Fatalf("created_at %v, want %v", found[0].CreatedAt, created)
		}
	})
}

// Each MCP client starts its own server process, so several connections hit
// one SQLite file at once.
func TestSQLiteSharedBetweenConnections(t *testing.T) {
	ctx := context.Background()
	cfg := sqliteConfig(t)
	var wg sync.WaitGroup
	errs := make(chan error, 300)
	for c := range 3 {
		s, err := Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				_, err := s.Write(ctx, Memory{Content: fmt.Sprintf("client %d note %d", c, i)})
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.Count(ctx); err != nil || n != 300 {
		t.Fatalf("count: %d, %v", n, err)
	}
}

func TestRedactRemovesPassword(t *testing.T) {
	err := redact(fmt.Errorf("dial oracle://u:p%%40ss@h and p@ss failed"), "p@ss")
	if strings.Contains(err.Error(), "p@ss") || strings.Contains(err.Error(), "p%40ss") {
		t.Fatal(err)
	}
}
