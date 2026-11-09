package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"8memory/internal/store"
)

// browseFixture seeds three prompts of different ages; the newest has a
// feature memory attached.
func browseFixture(t *testing.T) *Model {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	addNode := func(project, branch, title string, age time.Duration) store.Node {
		n, err := s.AddNode(ctx, store.Node{Project: project, Branch: branch, Agent: "claude", Title: title, CreatedAt: time.Now().Add(-age)})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	addNode("alpha", "main", "prompt sebulan", 20*24*time.Hour)
	addNode("beta", "fitur", "prompt minggu", 3*24*time.Hour)
	today := addNode("alpha", "main", "prompt hari ini", time.Minute)

	for _, m := range []store.Memory{
		{Project: "alpha", Branch: "main", NodeID: today.ID, Change: store.ChangeAdd, Feature: "Ekspor CSV", Content: "Isi lengkap tentang tombol ekspor"},
		{Project: "beta", Branch: "fitur", Change: store.ChangeInspect, Feature: "Audit", Content: "Pemeriksaan tanpa temuan"},
	} {
		if _, err := s.Write(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	return New(f.configPath, f.source)
}

func expectView(t *testing.T, m *Model, present []string, absent []string) {
	t.Helper()
	view := m.View()
	for _, text := range present {
		if !strings.Contains(view, text) {
			t.Errorf("view lacks %q:\n%s", text, view)
		}
	}
	for _, text := range absent {
		if strings.Contains(view, text) {
			t.Errorf("view should not show %q:\n%s", text, view)
		}
	}
}

func TestJourneyPeriods(t *testing.T) {
	m := browseFixture(t)
	send(m, enter)
	expectView(t, m, []string{"1 hari", "prompt hari ini", "alpha @ main"}, []string{"prompt minggu", "prompt sebulan"})
	send(m, key('p'))
	expectView(t, m, []string{"7 hari", "prompt hari ini", "prompt minggu", "beta @ fitur"}, []string{"prompt sebulan"})
	send(m, key('p'))
	expectView(t, m, []string{"1 bulan", "prompt sebulan"}, nil)
}

func TestJourneyGrouping(t *testing.T) {
	m := browseFixture(t)
	send(m, enter)
	send(m, key('p')) // 7 days, two projects
	send(m, key('g'))
	expectView(t, m, []string{"grup proyek", "alpha (1)", "beta (1)"}, nil)
	send(m, key('g'))
	expectView(t, m, []string{"grup branch", "main (1)", "fitur (1)"}, nil)
	send(m, key('g'))
	expectView(t, m, []string{"grup hari", time.Now().Format(dateLayout)}, nil)
	send(m, key('g'))
	expectView(t, m, []string{"grup agent", "claude (2)"}, nil)
}

func TestNodeOpensItsMemoriesAndBack(t *testing.T) {
	m := browseFixture(t)
	send(m, enter)
	send(m, enter)
	expectView(t, m, []string{"Memori untuk: prompt hari ini", "tambah", "Ekspor CSV"}, []string{"Audit"})

	send(m, enter)
	if m.screen != screenDetail {
		t.Fatalf("screen %d", m.screen)
	}
	expectView(t, m, []string{"Fitur     Ekspor CSV", "Perubahan tambah", "Branch    main", "Isi lengkap tentang tombol ekspor"}, nil)

	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Fatalf("screen %d", m.screen)
	}
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	expectView(t, m, []string{"Journey", "prompt hari ini"}, []string{"Memori untuk"})
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenMenu {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestMemoryFiltersAndSearch(t *testing.T) {
	m := browseFixture(t)
	send(m, down)
	send(m, enter)
	expectView(t, m, []string{"Ekspor CSV", "Audit"}, nil)

	for range 4 { // semua -> tambah -> kurang -> ubah -> periksa
		send(m, key('c'))
	}
	expectView(t, m, []string{"perubahan periksa", "Audit"}, []string{"Ekspor CSV"})
	send(m, key('c')) // back to all

	send(m, key('/'))
	typeText(m, "ekspor")
	send(m, enter)
	expectView(t, m, []string{`cari "ekspor"`, "Ekspor CSV"}, []string{"Audit"})
}

func TestSearchKeysDoNotTriggerShortcuts(t *testing.T) {
	m := browseFixture(t)
	send(m, down)
	send(m, enter)
	send(m, key('/'))
	typeText(m, "pgcq")
	if m.browser.period != 0 || m.browser.group != 0 || m.browser.change != 0 || m.screen != screenList {
		t.Fatalf("shortcut fired while typing: %+v", m.browser)
	}
	if m.browser.search.Value() != "pgcq" {
		t.Fatalf("typed %q", m.browser.search.Value())
	}
}

func TestLongListScrollsWithinWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if _, err := s.AddNode(ctx, store.Node{Project: "p", Branch: "main", Title: fmt.Sprintf("judul-%02d", i), CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	m := New(f.configPath, f.source)
	send(m, tea.WindowSizeMsg{Width: 60, Height: 14})
	send(m, enter)
	for range 30 {
		send(m, down)
	}
	view := m.View()
	if lines := strings.Count(view, "\n"); lines > 14 {
		t.Fatalf("%d lines in a 14 line window:\n%s", lines, view)
	}
	if !strings.Contains(view, "> ") || !strings.Contains(view, "judul-30") {
		t.Fatalf("selection scrolled out of view:\n%s", view)
	}
	for range 30 {
		send(m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if !strings.Contains(m.View(), "p @ main (40)") {
		t.Fatalf("group header not visible at the top:\n%s", m.View())
	}
}

func TestEmptyListsAreNotErrors(t *testing.T) {
	m := New(newFixture(t).configPath, newFixture(t).source)
	send(m, enter)
	expectView(t, m, []string{"Belum ada data."}, nil)
	send(m, enter) // nothing selected
	if m.screen != screenList {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestStaleLoadIsDiscarded(t *testing.T) {
	m := browseFixture(t)
	send(m, enter)
	stale := loadedMsg{seq: m.browser.seq - 1, entries: nil}
	send(m, stale)
	expectView(t, m, []string{"prompt hari ini"}, nil)
}

func TestMemoryDetailShowsFileLocationForThisMachine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Write(ctx, store.Memory{Project: "p", Change: store.ChangeInspect, Feature: "Backup", Content: "cadangan", Files: []store.FileRef{
		{Ref: "backup://p/db.sql", Size: 2048, Kind: "postgres-dump", RestoreHint: "psql -f db.sql", Host: "laptop"},
		{Ref: "nas://p/lain.sql"},
	}})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}

	f.source.Aliases = map[string]string{"backup": "root-lokal"}
	m := New(f.configPath, f.source)
	send(m, down)
	send(m, enter)
	send(m, enter)
	m.height = 40
	expectView(t, m, []string{"backup://p/db.sql", filepath.Join("root-lokal", "p", "db.sql"), "2048 byte", "postgres-dump", "laptop", "psql -f db.sql", "alias tidak dikenal"}, nil)
}
