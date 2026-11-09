package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"8memory/internal/compose"
	"8memory/internal/config"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

var (
	shiftEnter = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	backspace  = tea.KeyPressMsg{Code: tea.KeyBackspace}
	escape     = tea.KeyPressMsg{Code: tea.KeyEscape}
	tab        = tea.KeyPressMsg{Code: tea.KeyTab}
)

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func installFake(t *testing.T, names ...string) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range names {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// toEditor opens ToCLI for a provider index and a project folder.
func toEditor(t *testing.T, provider int) (*Model, *fixture, string) {
	t.Helper()
	f := newFixture(t)
	m := New(f.configPath, f.source)
	project := filepath.Join(t.TempDir(), "proyek-ku")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	m.openToCLI()
	m.tocli.provider = provider
	m.tocli.folder.SetValue(project)
	send(m, enter)
	if m.screen != screenEditor {
		t.Fatalf("screen %d, error %q", m.screen, m.tocli.formErr)
	}
	return m, f, project
}

func TestMissingCLIShowsClearMessage(t *testing.T) {
	installFake(t)
	f := newFixture(t)
	m := New(f.configPath, f.source)
	m.openToCLI()
	m.tocli.provider = 1
	send(m, enter)
	if m.screen != screenToCLIForm {
		t.Fatalf("screen %d", m.screen)
	}
	expectView(t, m, []string{"tidak ditemukan", "npm install -g"}, nil)
}

func TestEditorEnterSendsAndShiftEnterBreaksLine(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	typeText(m, "satu")
	send(m, shiftEnter)
	typeText(m, "dua")
	send(m, ctrl('j'))
	typeText(m, "tiga")
	if got := m.tocli.editor.value(); got != "satu\ndua\ntiga" {
		t.Fatalf("%q", got)
	}
	send(m, enter)
	if m.screen != screenCLIPreview {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
}

func TestEmptyPromptIsRejected(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	send(m, enter)
	if m.screen != screenEditor || !strings.Contains(m.tocli.status, "kosong") {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
}

func TestLongPasteBecomesAttachment(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	send(m, tea.PasteMsg{Content: strings.Repeat("baris\r\n", 120)})
	if len(m.tocli.attachments) != 1 || m.tocli.attachments[0].Lines != 120 {
		t.Fatalf("%+v", m.tocli.attachments)
	}
	if got := m.tocli.editor.value(); got != "[Paste #1 · 120 baris]" {
		t.Fatalf("%q", got)
	}
	send(m, backspace)
	if m.tocli.editor.value() != "" || len(m.tocli.attachments) != 0 {
		t.Fatalf("marker must delete whole: %q %d", m.tocli.editor.value(), len(m.tocli.attachments))
	}
}

func TestShortPasteStaysAsText(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	send(m, tea.PasteMsg{Content: "a\r\nb"})
	if m.tocli.editor.value() != "a\nb" || len(m.tocli.attachments) != 0 {
		t.Fatalf("%q", m.tocli.editor.value())
	}
}

func TestPasteThresholdIsConfigurable(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	m.current.PasteLines = 2
	send(m, tea.PasteMsg{Content: "a\nb"})
	if len(m.tocli.attachments) != 1 {
		t.Fatalf("%q", m.tocli.editor.value())
	}
}

func TestAttachmentPreviewAndDelete(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	typeText(m, "awal ")
	send(m, tea.PasteMsg{Content: "ISI-TEMPELAN\n" + strings.Repeat("x\n", 30)})
	send(m, ctrl('e'))
	if m.screen != screenAttachments {
		t.Fatalf("screen %d", m.screen)
	}
	send(m, enter)
	expectView(t, m, []string{"ISI-TEMPELAN"}, nil)
	send(m, escape)
	send(m, key('d'))
	if m.screen != screenEditor || len(m.tocli.attachments) != 0 || m.tocli.editor.value() != "awal " {
		t.Fatalf("screen %d, %q", m.screen, m.tocli.editor.value())
	}
}

func TestFileAttachmentByPath(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	path := filepath.Join(t.TempDir(), "catatan.txt")
	if err := os.WriteFile(path, []byte("isi"), 0o600); err != nil {
		t.Fatal(err)
	}
	send(m, ctrl('o'))
	send(m, tea.PasteMsg{Content: path})
	send(m, enter)
	if len(m.tocli.attachments) != 1 || !strings.Contains(m.tocli.editor.value(), "[File #1 · catatan.txt]") {
		t.Fatalf("%q, status %q", m.tocli.editor.value(), m.tocli.status)
	}
	send(m, ctrl('o'))
	typeText(m, "tidak-ada.txt")
	send(m, enter)
	if !m.tocli.statusErr || !m.tocli.asking {
		t.Fatalf("status %q", m.tocli.status)
	}
}

func TestClipboardImageResults(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	send(m, tocliImageMsg{err: tocli.ErrNoImage})
	if !m.tocli.statusErr || !strings.Contains(m.tocli.status, "tidak berisi gambar") {
		t.Fatalf("%q", m.tocli.status)
	}
	send(m, tocliImageMsg{data: []byte("\x89PNG")})
	if len(m.tocli.attachments) != 1 || m.tocli.attachments[0].Kind != tocli.KindImage {
		t.Fatalf("%+v", m.tocli.attachments)
	}
}

func TestSendCreatesNodeSavesAttachmentsAndBuildsCommand(t *testing.T) {
	installFake(t, "claude")
	m, f, project := toEditor(t, 0)
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule(ctx, store.Rule{Scope: store.ScopeGlobal, Text: "Selalu tulis test"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	typeText(m, "Perbaiki bug login ")
	send(m, tea.PasteMsg{Content: "RAHASIA-PASTE\n" + strings.Repeat("x\n", 30)})
	send(m, enter)
	if m.screen != screenCLIPreview {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
	expectView(t, m, []string{"Selalu tulis test", "tidak dihitung"}, []string{"RAHASIA-PASTE"})
	if strings.Contains(m.tocli.plan.briefing.Text, "RAHASIA-PASTE") {
		t.Fatal("attachment content reached the briefing")
	}

	send(m, enter)
	cmd := m.tocli.lastCommand
	if cmd == nil {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
	nodeID := m.tocli.plan.nodeID
	if cmd.Dir != project || !slices.Contains(cmd.Env, tocli.EnvNode+"="+nodeID) {
		t.Fatalf("%+v", cmd)
	}
	if !slices.Contains(cmd.Args, "--append-system-prompt") || !slices.Contains(cmd.Args, m.tocli.plan.briefing.Text) {
		t.Fatalf("%q", cmd.Args)
	}
	saved := filepath.Join(filepath.Dir(f.configPath), "attachments", nodeID, "paste-1.txt")
	data, err := os.ReadFile(saved)
	if err != nil || !strings.Contains(string(data), "RAHASIA-PASTE") {
		t.Fatalf("%q, %v", data, err)
	}
	prompt := cmd.Args[len(cmd.Args)-1]
	if prompt != "Perbaiki bug login @"+saved {
		t.Fatalf("%q", prompt)
	}

	s, err = store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(ctx, store.NodeQuery{})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("%+v, %v", nodes, err)
	}
	if nodes[0].ID != nodeID || nodes[0].Title != "Perbaiki bug login" || nodes[0].Agent != "claude" || nodes[0].Project != "proyek-ku" {
		t.Fatalf("%+v", nodes[0])
	}
	memories, err := s.Search(ctx, store.Query{Text: "RAHASIA"})
	if err != nil || len(memories) != 0 {
		t.Fatalf("attachment reached memory: %+v, %v", memories, err)
	}

	if !tocli.ClaimPending(filepath.Dir(f.configPath), nodeID) {
		t.Fatal("hook marker was not created")
	}
	send(m, tocliDoneMsg{nodeID: nodeID})
	if m.screen != screenMenu || !strings.Contains(m.notice, "ditutup") || m.tocli.editor.value() != "" {
		t.Fatalf("screen %d, notice %q", m.screen, m.notice)
	}
}

func TestUnsupportedAttachmentIsWarnedBeforeSending(t *testing.T) {
	installFake(t, "codex")
	m, _, _ := toEditor(t, 1)
	typeText(m, "lihat ")
	send(m, tea.PasteMsg{Content: strings.Repeat("x\n", 30)})
	send(m, enter)
	expectView(t, m, []string{"Peringatan", "TIDAK dikirim"}, nil)
	send(m, enter)
	cmd := m.tocli.lastCommand
	if cmd == nil {
		t.Fatalf("status %q", m.tocli.status)
	}
	if strings.Contains(cmd.Args[1], "[Paste") || !strings.HasPrefix(cmd.Args[1], "<8memory-context>") || !strings.HasSuffix(cmd.Args[1], "lihat") {
		t.Fatalf("%q", cmd.Args)
	}
}

func TestPreviewReportsWhatWasCut(t *testing.T) {
	m := &Model{}
	lines := m.cutReport(compose.Result{DroppedRules: []string{"a"}, DroppedMemories: []string{"b", "c", "d", "e"}, ShortenedMemories: []string{"f"}})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"1 aturan", "4 memori", "dan 1 lainnya", "Dipendekkan: 1 memori"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestRulesCrud(t *testing.T) {
	f := newFixture(t)
	m := New(f.configPath, f.source)
	send(m, down)
	send(m, down)
	send(m, enter)
	expectView(t, m, []string{"Belum ada aturan"}, nil)

	send(m, key('a'))
	for range 3 {
		send(m, tab)
	}
	typeText(m, "Jangan pakai ORM")
	send(m, enter)
	expectView(t, m, []string{"[global] Jangan pakai ORM"}, nil)

	send(m, enter)
	m.rules.inputs[2].SetValue("Jangan pakai ORM apa pun")
	send(m, key(0x00)) // no-op key while the scope selector has focus
	send(m, ctrl('s'))
	expectView(t, m, []string{"Jangan pakai ORM apa pun"}, nil)

	send(m, key('d'))
	expectView(t, m, []string{"Hapus aturan"}, nil)
	send(m, key('n'))
	if len(m.rules.list) != 1 {
		t.Fatal("deleted without confirmation")
	}
	send(m, key('d'))
	send(m, key('y'))
	expectView(t, m, []string{"Belum ada aturan"}, nil)
}

func TestRuleFormShowsValidationError(t *testing.T) {
	f := newFixture(t)
	m := New(f.configPath, f.source)
	m.openRules()
	send(m, key('a'))
	send(m, ctrl('s'))
	if m.screen != screenRuleForm || m.rules.formErr == "" {
		t.Fatalf("screen %d, %q", m.screen, m.rules.formErr)
	}
}

func TestJourneyAttachmentsCanBeRemoved(t *testing.T) {
	m := browseFixture(t)
	send(m, enter)
	node := m.browser.selected().node
	base := m.attachmentsDir()
	if _, err := tocli.Save(base, node.ID, []tocli.Attachment{tocli.NewPaste(1, "isi")}); err != nil {
		t.Fatal(err)
	}
	send(m, key('x'))
	expectView(t, m, []string{"Hapus attachment"}, nil)
	send(m, key('n'))
	if !tocli.HasSaved(base, node.ID) {
		t.Fatal("removed without confirmation")
	}
	send(m, key('x'))
	send(m, key('y'))
	if tocli.HasSaved(base, node.ID) {
		t.Fatal("attachments still exist")
	}
	send(m, key('x'))
	expectView(t, m, []string{"tidak punya attachment"}, nil)
}

func TestEditorMarkerIsAtomic(t *testing.T) {
	e := editor{}
	e.markers = []string{"[Paste #1 · 5 baris]"}
	e.insert("ab[Paste #1 · 5 baris]cd")
	e.cursor = 2
	e.right()
	if e.cursor != 2+len([]rune(e.markers[0])) {
		t.Fatalf("right: %d", e.cursor)
	}
	e.left()
	if e.cursor != 2 {
		t.Fatalf("left: %d", e.cursor)
	}
	if removed := e.del(); removed != e.markers[0] || e.value() != "abcd" {
		t.Fatalf("%q %q", removed, e.value())
	}
}

func TestEditorVerticalMovement(t *testing.T) {
	e := editor{}
	e.insert("panjang sekali\nab\nlagi panjang")
	e.up()
	if e.cursor != len("panjang sekali\nab") {
		t.Fatalf("up clamps to the shorter line: %d", e.cursor)
	}
	e.up()
	e.down()
	e.down()
	if e.lineStart(e.cursor) != len("panjang sekali\nab\n") {
		t.Fatalf("down: %d", e.cursor)
	}
	e.home()
	e.up()
	if e.cursor != len("panjang sekali\n") {
		t.Fatalf("home+up: %d", e.cursor)
	}
	e.end()
	if e.cursor != len("panjang sekali\nab") {
		t.Fatalf("end: %d", e.cursor)
	}
}

func TestEditorRenderWrapsAndScrolls(t *testing.T) {
	e := editor{}
	e.insert(strings.Repeat("x", 25) + "\n" + "akhir")
	rows := strings.Split(e.render(10, 2), "\n")
	if len(rows) != 2 || !strings.HasPrefix(rows[1], "akhir") {
		t.Fatalf("%q", rows)
	}
}

func TestStorageSwitchKeepsPasteSettings(t *testing.T) {
	f := newFixture(t)
	f.model.current.PasteLines, f.model.current.PasteBytes = 5, 100
	f.submitNewPath()
	saved, err := config.Load(f.configPath)
	if err != nil || saved.PasteLines != 5 || saved.PasteBytes != 100 {
		t.Fatalf("%+v, %v", saved, err)
	}
}
