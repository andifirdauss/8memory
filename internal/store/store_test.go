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
	return config.Config{Backend: config.SQLite, Path: filepath.Join(t.TempDir(), "folder dengan spasi", "memory.db")}
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

	auth := write("Auth dipindah ke session cookie karena refresh token bermasalah", "Login", "autentikasi")
	cache := write("Cache Redis dihapus demi kesederhanaan")
	percent := write("Coverage naik 100% setelah refactor")
	write("Coverage naik 100 poin di modul lain")
	underscore := write("Kolom user_id ditambahkan")
	bracket := write("Regex [abc] diperbaiki")
	unicode := write("Perubahan skema 认证 dengan résumé")

	t.Run("round trip", func(t *testing.T) {
		if auth.ID == 0 || auth.Project != project || auth.Agent != "claude" || auth.CreatedAt.IsZero() {
			t.Fatalf("unexpected memory: %+v", auth)
		}
		if strings.Join(auth.Tags, "|") != "login|autentikasi" {
			t.Fatalf("tags: %v", auth.Tags)
		}
		if unicode.Content != "Perubahan skema 认证 dengan résumé" {
			t.Fatalf("content: %q", unicode.Content)
		}
	})
	t.Run("deduplicates", func(t *testing.T) {
		if dup := write("  Cache Redis dihapus demi kesederhanaan  "); dup.ID != cache.ID {
			t.Fatalf("got new id %d, want %d", dup.ID, cache.ID)
		}
	})
	t.Run("empty optional fields", func(t *testing.T) {
		m, err := s.Write(ctx, Memory{Content: "tanpa project " + project})
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
		expect("kesederhana*", cache)
		expect("autentikasi", auth) // tag
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
		long := strings.Repeat("kalimat panjang ", 8000) + "penandaakhir"
		saved := write(long)
		if saved.Content != strings.TrimSpace(long) {
			t.Fatalf("stored %d bytes, want %d", len(saved.Content), len(long))
		}
		expect("penandaakhir", saved)
	})
	t.Run("concurrent identical and distinct writes", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 40)
		for i := range 40 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				content := "serentak sama"
				if i%2 == 0 {
					content = fmt.Sprintf("serentak beda %d", i)
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
		expect(`"serentak sama"`, search(`"serentak sama"`)[0])
	})
	t.Run("branches are separate but readable", func(t *testing.T) {
		const content = "catatan yang sama di dua branch"
		main, err := s.Write(ctx, Memory{Project: project, Branch: "main", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Write(ctx, Memory{Project: project, Branch: "fitur/x", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		if main.ID == other.ID || other.Branch != "fitur/x" {
			t.Fatalf("branches merged: %+v %+v", main, other)
		}
		only, err := s.Search(ctx, Query{Text: "catatan", Project: project, Branch: "fitur/x"})
		if err != nil || len(only) != 1 || only[0].ID != other.ID {
			t.Fatalf("one branch: %+v, %v", only, err)
		}
		if every := search(`"catatan yang sama"`); len(every) != 2 {
			t.Fatalf("every branch: %+v", every)
		}
	})
	t.Run("change details", func(t *testing.T) {
		nodeID := fmt.Sprintf("0195f3a2-7c10-7000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
		added, err := s.Write(ctx, Memory{Project: project, Change: ChangeAdd, Feature: "Ekspor CSV", Content: "tombol baru di laporan", NodeID: nodeID, Tags: []string{"laporan"}})
		if err != nil {
			t.Fatal(err)
		}
		if added.Change != ChangeAdd || added.Feature != "Ekspor CSV" || added.NodeID != nodeID || strings.Join(added.Tags, "|") != "laporan" {
			t.Fatalf("%+v", added)
		}
		inspected, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Audit log", Content: "tidak ada masalah", NodeID: nodeID})
		if err != nil {
			t.Fatal(err)
		}
		expect(`"ekspor csv"`, added) // found by feature, which is not in the content
		byChange, err := s.Search(ctx, Query{Project: project, Change: ChangeInspect})
		if err != nil || len(byChange) != 1 || byChange[0].ID != inspected.ID {
			t.Fatalf("by change: %+v, %v", byChange, err)
		}
		byNode, err := s.Search(ctx, Query{NodeID: nodeID, Limit: 50})
		if err != nil || len(byNode) != 2 {
			t.Fatalf("by node: %+v, %v", byNode, err)
		}
		if _, err := s.Write(ctx, Memory{Content: "x", Change: "hapus"}); err == nil {
			t.Fatal("accepted an unknown change")
		}
		if _, err := s.Write(ctx, Memory{Content: "x", NodeID: "bukan-uuid"}); err == nil {
			t.Fatal("accepted a malformed node id")
		}
	})
	t.Run("file refs", func(t *testing.T) {
		files := []FileRef{
			{Ref: "backup://proyek/2026-11-09/database.sql", Size: 5368709120, Kind: "postgres-dump", RestoreHint: "psql -f database.sql — pulihkan ke 认证", Host: "laptop"},
			{Ref: "s3://arsip/uploads.tar", Host: "vps"},
		}
		saved, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Backup", Content: "cadangan sebelum migrasi", Files: files})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(saved.Files, files) {
			t.Fatalf("%+v", saved.Files)
		}
		if plain := write("catatan tanpa berkas"); plain.Files != nil {
			t.Fatalf("%+v", plain.Files)
		}
		other, err := s.Write(ctx, Memory{Project: project, Change: ChangeInspect, Feature: "Backup", Content: "cadangan sebelum migrasi", Files: files[:1]})
		if err != nil || other.ID == saved.ID {
			t.Fatalf("different refs must be a new memory: %+v, %v", other, err)
		}

		src, err := Open(ctx, sqliteConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		copyProject := project + "-files"
		if _, err := src.Write(ctx, Memory{Project: copyProject, Content: "disalin bersama berkas", Files: files}); err != nil {
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
	t.Run("since filter", func(t *testing.T) {
		old, err := s.Write(ctx, Memory{Project: project + "-since", Content: "catatan lawas", CreatedAt: time.Now().Add(-48 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		found, err := s.Search(ctx, Query{Text: "lawas", Project: old.Project, Since: time.Now().Add(-24 * time.Hour)})
		if err != nil || len(found) != 0 {
			t.Fatalf("%+v, %v", found, err)
		}
		found, err = s.Search(ctx, Query{Text: "lawas", Project: old.Project, Since: time.Now().Add(-72 * time.Hour)})
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
		add("main", "sebulan lalu", 20*24*time.Hour)
		week := add("fitur", "minggu ini", 3*24*time.Hour)
		today := add("main", "hari ini", time.Minute)
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
		if got := titles(NodeQuery{}); got != "hari ini|minggu ini|sebulan lalu" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Since: time.Now().Add(-24 * time.Hour)}); got != "hari ini" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Since: time.Now().Add(-7 * 24 * time.Hour)}); got != "hari ini|minggu ini" {
			t.Fatal(got)
		}
		if got := titles(NodeQuery{Branch: "main"}); got != "hari ini|sebulan lalu" {
			t.Fatal(got)
		}
		again, err := s.AddNode(ctx, Node{ID: week.ID, Title: "judul lain"})
		if err != nil || again.Title != "minggu ini" {
			t.Fatalf("existing id: %+v, %v", again, err)
		}

		noBranch, err := s.AddNode(ctx, Node{Title: strings.Repeat("panjang\n", 100)})
		if err != nil || noBranch.Branch != "" || utf8.RuneCountInString(noBranch.Title) != maxNameRunes || strings.Contains(noBranch.Title, "\n") {
			t.Fatalf("%+v, %v", noBranch, err)
		}
		if _, err := s.AddNode(ctx, Node{Title: "   "}); err == nil {
			t.Fatal("accepted an empty title")
		}
		if _, err := s.AddNode(ctx, Node{ID: "bukan-uuid", Title: "x"}); err == nil {
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
		if _, err := src.Write(ctx, Memory{Project: copyProject, Branch: "main", NodeID: last.ID, Change: ChangeModify, Feature: "Fitur tertaut", Content: "isi tertaut"}); err != nil {
			t.Fatal(err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Nodes != 502 || n.Memories != 1 {
			t.Fatalf("first copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Nodes != 0 || n.Memories != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		linked, err := s.Search(ctx, Query{NodeID: last.ID})
		if err != nil || len(linked) != 1 || linked[0].Feature != "Fitur tertaut" || linked[0].Change != ChangeModify || linked[0].Branch != "main" || len(linked[0].Tags) != 0 {
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
			if _, err := src.Write(ctx, Memory{Project: copyProject, Content: fmt.Sprintf("salinan %d", i), Tags: []string{"pindah"}, CreatedAt: created}); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 1203 {
			t.Fatalf("first copy: %+v, %v", n, err)
		}
		if n, err := Copy(ctx, src, s); err != nil || n.Memories != 0 {
			t.Fatalf("second copy: %+v, %v", n, err)
		}
		found, err := s.Search(ctx, Query{Text: "pindah", Project: copyProject, Limit: 1})
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
				_, err := s.Write(ctx, Memory{Content: fmt.Sprintf("klien %d catatan %d", c, i)})
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
