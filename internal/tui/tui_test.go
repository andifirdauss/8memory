package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"8memory/internal/config"
	"8memory/internal/store"
)

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	save  = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	test  = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
)

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// send applies a message and runs any resulting command to completion.
func send(m *Model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	for cmd != nil {
		next := cmd()
		if next == nil {
			return
		}
		if _, quit := next.(tea.QuitMsg); quit {
			return
		}
		_, cmd = m.Update(next)
	}
}

func typeText(m *Model, text string) {
	for _, r := range text {
		send(m, key(r))
	}
}

func clearInput(m *Model) { m.inputs[m.focus].SetValue("") }

func seed(t *testing.T, cfg config.Config, contents ...string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, content := range contents {
		if _, err := s.Write(ctx, store.Memory{Project: "p", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
}

func count(t *testing.T, cfg config.Config) int {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type fixture struct {
	configPath string
	source     config.Config
	targetPath string
	model      *Model
}

func newFixture(t *testing.T, contents ...string) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		configPath: filepath.Join(dir, "config.json"),
		source:     config.Config{Backend: config.SQLite, Path: filepath.Join(dir, "old.db")},
		targetPath: filepath.Join(dir, "new", "new.db"),
	}
	seed(t, f.source, contents...)
	f.model = New(f.configPath, f.source)
	f.model.screen = screenPick
	return f
}

func (f *fixture) submitNewPath() {
	send(f.model, enter)
	clearInput(f.model)
	typeText(f.model, f.targetPath)
	send(f.model, save)
}

func TestSwitchWithCopy(t *testing.T) {
	f := newFixture(t, "one", "two", "three")
	f.submitNewPath()

	if f.model.screen != screenConfirmCopy || f.model.memories != 3 {
		t.Fatalf("screen %d, memories %d, status %q", f.model.screen, f.model.memories, f.model.status)
	}
	if !strings.Contains(f.model.render(), "has 3 memories") {
		t.Fatal(f.model.render())
	}
	send(f.model, key('y'))

	if f.model.screen != screenDone {
		t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
	}
	if !strings.Contains(f.model.render(), "Copied 3 new memories, 0 nodes and 0 rules") {
		t.Fatal(f.model.render())
	}
	saved, err := config.Load(f.configPath)
	if err != nil || saved.Path != f.targetPath {
		t.Fatalf("%+v, %v", saved, err)
	}
	if n := count(t, saved); n != 3 {
		t.Fatalf("target has %d memories", n)
	}
	if n := count(t, f.source); n != 3 {
		t.Fatalf("source changed to %d memories", n)
	}
}

func TestSwitchingStorageKeepsAliases(t *testing.T) {
	f := newFixture(t)
	f.model.current.Aliases = map[string]string{"backup": "/srv/backup"}
	f.submitNewPath()
	saved, err := config.Load(f.configPath)
	if err != nil || saved.Aliases["backup"] != "/srv/backup" {
		t.Fatalf("%+v, %v", saved, err)
	}
}

func TestSwitchWithoutCopy(t *testing.T) {
	f := newFixture(t, "one")
	f.submitNewPath()
	send(f.model, key('n'))

	saved, err := config.Load(f.configPath)
	if err != nil || saved.Path != f.targetPath {
		t.Fatalf("%+v, %v", saved, err)
	}
	if n := count(t, saved); n != 0 {
		t.Fatalf("target has %d memories", n)
	}
}

func TestEmptySourceSkipsCopyPrompt(t *testing.T) {
	f := newFixture(t)
	f.submitNewPath()
	if f.model.screen != screenDone {
		t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
	}
}

func TestSamePathSkipsCopyPrompt(t *testing.T) {
	f := newFixture(t, "one")
	send(f.model, enter)
	send(f.model, save)
	if f.model.screen != screenDone {
		t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
	}
}

func TestFailedConnectionKeepsConfigUntouched(t *testing.T) {
	f := newFixture(t, "one")
	send(f.model, down) // PostgreSQL
	send(f.model, enter)
	if f.model.option.backend != config.Postgres {
		t.Fatalf("backend %s", f.model.option.backend)
	}
	f.model.inputs[fieldHost].SetValue("127.0.0.1")
	f.model.inputs[fieldPort].SetValue("1") // nothing listens here
	f.model.inputs[fieldUser].SetValue("u")
	f.model.inputs[fieldPassword].SetValue("very-secret")

	send(f.model, test)
	if f.model.screen != screenForm || !f.model.statusErr {
		t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
	}
	send(f.model, save)
	if f.model.screen != screenForm || !f.model.statusErr {
		t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
	}
	if strings.Contains(f.model.status, "very-secret") {
		t.Fatal("password leaked into status")
	}
	saved, err := config.Load(f.configPath)
	if err != nil || saved.Backend != config.SQLite || saved.Path != filepath.Join(filepath.Dir(f.configPath), "memory.db") {
		t.Fatalf("config was changed: %+v, %v", saved, err)
	}
}

func TestInvalidPortShowsError(t *testing.T) {
	f := newFixture(t)
	send(f.model, down)
	send(f.model, enter)
	f.model.inputs[fieldPort].SetValue("abc")
	send(f.model, test)
	if !f.model.statusErr || !strings.Contains(f.model.status, "port") {
		t.Fatalf("status %q", f.model.status)
	}
}

func TestFormMasksPassword(t *testing.T) {
	f := newFixture(t)
	f.model.current = config.Config{Backend: config.Postgres, Host: "h", Port: 5432, User: "u", Password: "secret", Database: "d"}
	f.model.openForm(backendOptions[1])
	if strings.Contains(f.model.render(), "secret") {
		t.Fatal(f.model.render())
	}
}

// TestSwitchToServerBackend walks the form against real servers configured
// through the same env vars as the store tests.
func TestSwitchToServerBackend(t *testing.T) {
	for i, env := range map[int]string{1: "MEMORY8_TEST_POSTGRES", 2: "MEMORY8_TEST_SQLSERVER", 3: "MEMORY8_TEST_ORACLE"} {
		t.Run(string(backendOptions[i].backend), func(t *testing.T) {
			raw := os.Getenv(env)
			if raw == "" {
				t.Skipf("%s not set", env)
			}
			var target config.Config
			if err := json.Unmarshal([]byte(raw), &target); err != nil {
				t.Fatal(err)
			}

			marker := fmt.Sprintf("marker%d", time.Now().UnixNano())
			f := newFixture(t, marker)
			f.model.openForm(backendOptions[i])
			f.model.inputs[fieldHost].SetValue(target.Host)
			f.model.inputs[fieldPort].SetValue(strconv.Itoa(target.Port))
			f.model.inputs[fieldUser].SetValue(target.User)
			f.model.inputs[fieldPassword].SetValue(target.Password)
			f.model.inputs[fieldDatabase].SetValue(target.Database)
			f.model.inputs[fieldOptions].SetValue(target.Options)

			send(f.model, test)
			if f.model.statusErr {
				t.Fatalf("test connection: %s", f.model.status)
			}
			send(f.model, save)
			if f.model.screen != screenConfirmCopy {
				t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
			}
			send(f.model, key('y'))
			if f.model.screen != screenDone {
				t.Fatalf("screen %d, status %q", f.model.screen, f.model.status)
			}

			saved, err := config.Load(f.configPath)
			if err != nil || !reflect.DeepEqual(saved, target) {
				t.Fatalf("saved %+v, %v", saved, err)
			}
			ctx := context.Background()
			s, err := store.Open(ctx, saved)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			found, err := s.Search(ctx, store.Query{Text: marker, Project: "p"})
			if err != nil || len(found) != 1 {
				t.Fatalf("copied memory: %v, %v", found, err)
			}
		})
	}
}

func TestProgramStartsAndQuits(t *testing.T) {
	f := newFixture(t)
	model := New(f.configPath, f.source)
	if view := model.render(); !strings.Contains(view, "Storage") || !strings.Contains(view, "Chat") {
		t.Fatal(view)
	}
	program := tea.NewProgram(model, tea.WithInput(strings.NewReader("q")), tea.WithOutput(io.Discard))
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
}
