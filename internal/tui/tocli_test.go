package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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

// turns replaces the CLI: each prompt waits until the test finishes it.
type turns struct {
	commands chan *exec.Cmd
	results  chan error
}

func (h *turns) next(t *testing.T) *exec.Cmd {
	t.Helper()
	select {
	case cmd := <-h.commands:
		return cmd
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt was run")
		return nil
	}
}

// finish ends the running prompt and delivers its completion to the model.
func (h *turns) finish(t *testing.T, m *Model, err error) {
	t.Helper()
	h.results <- err
	select {
	case msg := <-m.tocli.inbox:
		send(m, msg)
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not finish")
	}
}

func emit(m *Model, payload any) {
	send(m, tocliInboxMsg{inbox: m.tocli.inbox, payload: payload})
}

// toEditor opens ToCLI for a provider index and a project folder.
func toEditor(t *testing.T, provider int) (*Model, *fixture, string) {
	t.Helper()
	f := newFixture(t)
	m := New(f.configPath, f.source)
	m.listen = func(chan tocliInboxMsg, context.Context) tea.Cmd { return nil }
	m.runTurn = func(*exec.Cmd, tocli.Parser, func(tocli.Event)) error { return nil }
	t.Cleanup(m.closeChat)
	project := filepath.Join(t.TempDir(), "proyek-ku")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	m.openToCLI()
	m.tocli.provider = provider
	m.tocli.folder.SetValue(project)
	send(m, tab)
	send(m, enter)
	if m.screen != screenEditor {
		t.Fatalf("screen %d, error %q", m.screen, m.tocli.formErr)
	}
	return m, f, project
}

func captureTurns(m *Model) *turns {
	h := &turns{commands: make(chan *exec.Cmd, 4), results: make(chan error, 4)}
	m.runTurn = func(cmd *exec.Cmd, _ tocli.Parser, _ func(tocli.Event)) error {
		h.commands <- cmd
		return <-h.results
	}
	return h
}

func TestMissingCLIShowsClearMessage(t *testing.T) {
	installFake(t)
	f := newFixture(t)
	m := New(f.configPath, f.source)
	m.openToCLI()
	m.tocli.provider = 1
	send(m, tab)
	send(m, enter)
	if m.screen != screenToCLIForm {
		t.Fatalf("screen %d", m.screen)
	}
	expectView(t, m, []string{"tidak ditemukan", "npm install -g"}, nil)
}

func TestProviderSubMenuPicksFromList(t *testing.T) {
	f := newFixture(t)
	m := New(f.configPath, f.source)
	m.openToCLI()
	expectView(t, m, []string{"Chat", "Claude Code ▸"}, []string{"ToCLI", "Codex"})

	send(m, enter)
	if m.screen != screenProviderPick {
		t.Fatalf("screen %d", m.screen)
	}
	expectView(t, m, []string{"> Claude Code", "  Codex"}, nil)

	send(m, down)
	send(m, escape)
	if m.screen != screenToCLIForm || m.tocli.provider != 0 {
		t.Fatalf("esc must keep the provider: screen %d, provider %d", m.screen, m.tocli.provider)
	}

	send(m, enter)
	send(m, down)
	send(m, enter)
	if m.screen != screenToCLIForm || m.tocli.provider != 1 || m.tocli.focus != tocliFieldFolder {
		t.Fatalf("screen %d, provider %d, focus %d", m.screen, m.tocli.provider, m.tocli.focus)
	}
	expectView(t, m, []string{"Codex ▸"}, nil)
}

func TestEditorEnterSendsAndShiftEnterBreaksLine(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	typeText(m, "satu")
	send(m, shiftEnter)
	typeText(m, "dua")
	send(m, ctrl('j'))
	typeText(m, "tiga")
	if got := m.tocli.editor.value(); got != "satu\ndua\ntiga" {
		t.Fatalf("%q", got)
	}
	send(m, enter)
	h.next(t)
	if m.screen != screenEditor || !m.tocli.running {
		t.Fatalf("prompt must go out without a preview: screen %d, status %q", m.screen, m.tocli.status)
	}
	h.finish(t, m, nil)
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

	h := captureTurns(m)
	typeText(m, "Perbaiki bug login ")
	send(m, tea.PasteMsg{Content: "RAHASIA-PASTE\n" + strings.Repeat("x\n", 30)})
	send(m, enter)
	cmd := h.next(t)
	if strings.Contains(m.tocli.plan.briefing.Text, "RAHASIA-PASTE") || !strings.Contains(m.tocli.plan.briefing.System, "Selalu tulis test") {
		t.Fatalf("briefing %q", m.tocli.plan.briefing.Text)
	}
	mcpConfig := cmd.Args[slices.Index(cmd.Args, "--mcp-config")+1]
	if !strings.Contains(mcpConfig, `"8memory"`) || !strings.Contains(mcpConfig, `"serve"`) || !slices.Contains(cmd.Args, "--allowedTools") {
		t.Fatalf("memory tools are not connected: %q", cmd.Args)
	}
	if m.screen != screenEditor || !m.tocli.running || m.tocli.editor.value() != "" {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
	nodeID := m.tocli.plan.nodeID
	if cmd.Dir != project || !slices.Contains(cmd.Env, tocli.EnvNode+"="+nodeID) {
		t.Fatalf("%+v", cmd)
	}
	if !slices.Contains(cmd.Args, "--append-system-prompt") || !slices.Contains(cmd.Args, m.tocli.plan.briefing.System) {
		t.Fatalf("%q", cmd.Args)
	}
	if strings.Contains(m.tocli.plan.briefing.System, nodeID) {
		t.Fatal("the system prompt must not change between prompts")
	}
	saved := filepath.Join(filepath.Dir(f.configPath), "attachments", nodeID, "paste-1.txt")
	data, err := os.ReadFile(saved)
	if err != nil || !strings.Contains(string(data), "RAHASIA-PASTE") {
		t.Fatalf("%q, %v", data, err)
	}
	prompt := cmd.Args[len(cmd.Args)-1]
	if prompt != m.tocli.plan.briefing.Turn+"\n\nPerbaiki bug login @"+saved {
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
	h.finish(t, m, nil)
	if m.screen != screenEditor || m.tocli.running {
		t.Fatalf("screen %d, running %v", m.screen, m.tocli.running)
	}
}

// startPrompt types a prompt and passes the first-prompt preview if it opens.
func startPrompt(t *testing.T, m *Model, h *turns, text string) *exec.Cmd {
	t.Helper()
	typeText(m, text)
	send(m, enter)
	return h.next(t)
}

func TestLaterPromptsResumeWithoutPreview(t *testing.T) {
	installFake(t, "claude")
	m, f, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	emit(m, tocli.Event{Kind: tocli.EventSession, Text: "sesi-1"})
	emit(m, tocli.Event{Kind: tocli.EventText, Text: "Hal", Fresh: true})
	emit(m, tocli.Event{Kind: tocli.EventText, Text: "o dunia"})
	emit(m, tocli.Event{Kind: tocli.EventTool, Text: "Bash: git status"})
	expectView(t, m, []string{"> pertama", "Halo dunia", "Bash: git status", "bekerja"}, nil)
	h.finish(t, m, nil)

	typeText(m, "kedua")
	send(m, enter)
	if m.screen != screenEditor {
		t.Fatalf("screen %d", m.screen)
	}
	cmd := h.next(t)
	if index := slices.Index(cmd.Args, "--resume"); index < 0 || cmd.Args[index+1] != "sesi-1" {
		t.Fatalf("%q", cmd.Args)
	}
	h.finish(t, m, nil)

	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(ctx, store.NodeQuery{})
	if err != nil || len(nodes) != 2 {
		t.Fatalf("every prompt needs its own node: %+v, %v", nodes, err)
	}
}

func TestRulesThatDoNotFitAreReportedInChat(t *testing.T) {
	installFake(t, "claude")
	m, f, _ := toEditor(t, 0)
	h := captureTurns(m)
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if _, err := s.AddRule(ctx, store.Rule{Scope: store.ScopeGlobal, Text: string(rune('A'+i)) + strings.Repeat("r", 400)}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	startPrompt(t, m, h, "pertama")
	expectView(t, m, []string{"Terpotong", "aturan tidak muat"}, nil)
	h.finish(t, m, nil)
}

func TestPromptIsRefusedWhileOneIsRunning(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	typeText(m, "kedua")
	send(m, enter)
	if !strings.Contains(m.tocli.status, "Tunggu") || m.tocli.editor.value() != "kedua" {
		t.Fatalf("status %q, editor %q", m.tocli.status, m.tocli.editor.value())
	}
	h.finish(t, m, nil)
}

func TestApprovalNeedsCtrlYAndHidesNothing(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	first, second := tocli.NewApproval("Bash", "command: rm -rf build"), tocli.NewApproval("Write", "file_path: a.txt")
	emit(m, first)
	emit(m, second)
	expectView(t, m, []string{"Izin diminta: Bash", "rm -rf build", "1 lagi menunggu", "ctrl+y izinkan"}, nil)

	typeText(m, "ya")
	send(m, enter)
	select {
	case <-first.Reply():
		t.Fatal("typing answered the dialog")
	default:
	}
	if m.tocli.editor.value() != "" {
		t.Fatalf("keys reached the editor: %q", m.tocli.editor.value())
	}

	send(m, ctrl('y'))
	if !<-first.Reply() {
		t.Fatal("ctrl+y must allow")
	}
	expectView(t, m, []string{"Izin diminta: Write"}, []string{"rm -rf build"})
	send(m, escape)
	if <-second.Reply() {
		t.Fatal("esc must deny")
	}
	expectView(t, m, []string{"Diizinkan: Bash", "Ditolak: Write"}, nil)
	h.finish(t, m, nil)
}

func TestLongApprovalCanBeReadToTheEnd(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "baris"
	}
	lines[39] = "EKOR-PERINTAH"
	emit(m, tocli.NewApproval("Bash", strings.Join(lines, "\n")))
	expectView(t, m, []string{"baris 1-10 dari 40"}, []string{"EKOR-PERINTAH"})
	for range 39 {
		send(m, down)
	}
	expectView(t, m, []string{"EKOR-PERINTAH"}, nil)
	send(m, escape)
	h.finish(t, m, nil)
}

func TestFinishedPromptDeniesWhatIsStillWaiting(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	waiting := tocli.NewApproval("Bash", "command: x")
	emit(m, waiting)
	h.finish(t, m, nil)
	if <-waiting.Reply() || len(m.tocli.pending) != 0 {
		t.Fatal("a dialog outlived its prompt")
	}
}

func TestEscStopsThenLeavesAfterConfirmation(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	turn := m.tocli.turnCtx
	send(m, escape)
	if turn.Err() == nil || m.screen != screenEditor {
		t.Fatalf("esc must stop the prompt: %v, screen %d", turn.Err(), m.screen)
	}
	h.finish(t, m, nil)
	expectView(t, m, []string{"Dihentikan"}, nil)

	send(m, escape)
	if m.screen != screenEditor || !strings.Contains(m.tocli.status, "sekali lagi") {
		t.Fatalf("screen %d, status %q", m.screen, m.tocli.status)
	}
	send(m, escape)
	if m.screen != screenToCLIForm || m.tocli.inbox != nil {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestCtrlCStopsPromptInsteadOfQuitting(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	send(m, ctrl('c'))
	if m.tocli.turnCtx.Err() == nil || m.ctx.Err() != nil {
		t.Fatal("ctrl+c must stop only the prompt")
	}
	h.finish(t, m, nil)
}

func TestFailedTurnIsShown(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	h.finish(t, m, errors.New("login dulu"))
	expectView(t, m, []string{"login dulu"}, nil)
}

func TestLateMessageFromClosedSessionIsIgnored(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	old := m.tocli.inbox
	m.closeChat()
	send(m, tocliInboxMsg{inbox: old, payload: tocli.Event{Kind: tocli.EventText, Text: "basi"}})
	if len(m.tocli.entries) != 0 {
		t.Fatal("stale event was applied")
	}
}

func TestTranscriptScrollsAndModelTextCannotDrawOnScreen(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	emit(m, tocli.Event{Kind: tocli.EventText, Text: "AWAL\x1b[2J" + strings.Repeat("\nisi", 200), Fresh: true})
	h.finish(t, m, nil)
	expectView(t, m, nil, []string{"AWAL", "\x1b[2J"})
	for range 40 {
		send(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	expectView(t, m, []string{"AWAL[2J"}, nil)
}

func TestSlashMenuFiltersAndCompletes(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	typeText(m, "/")
	expectView(t, m, []string{"/help", "/usage", "/exit"}, nil)
	typeText(m, "u")
	expectView(t, m, []string{"> /usage"}, []string{"/help", "/exit"})
	send(m, tab)
	if m.tocli.editor.value() != "/usage" {
		t.Fatalf("%q", m.tocli.editor.value())
	}
	send(m, escape)
	if m.tocli.editor.value() != "" || m.screen != screenEditor {
		t.Fatalf("esc must only close the menu: %q, screen %d", m.tocli.editor.value(), m.screen)
	}
}

func TestSlashHelpRunsLocally(t *testing.T) {
	installFake(t, "claude")
	m, f, _ := toEditor(t, 0)
	h := captureTurns(m)
	m.height = 50
	typeText(m, "/he")
	send(m, enter)
	expectView(t, m, []string{"> /help", "/usage", "shift+enter"}, nil)
	if m.tocli.editor.value() != "" || m.screen != screenEditor {
		t.Fatalf("%q, screen %d", m.tocli.editor.value(), m.screen)
	}
	select {
	case <-h.commands:
		t.Fatal("/help reached the CLI")
	default:
	}
	assertNodes(t, f, 0)
}

func TestSlashMenuSelectsWithArrows(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	typeText(m, "/")
	for range len(slashCommands) {
		send(m, down)
	}
	expectView(t, m, []string{"> /exit"}, nil)
	send(m, tea.KeyPressMsg{Code: tea.KeyUp})
	expectView(t, m, []string{"> /usage"}, nil)
}

func TestSlashExitLeavesWithoutConfirmation(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	h.finish(t, m, nil)
	typeText(m, "/exit")
	send(m, enter)
	if m.screen != screenToCLIForm || m.tocli.inbox != nil {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestSlashNewAndClearStartAFreshConversation(t *testing.T) {
	for _, command := range []string{"/new", "/clear"} {
		t.Run(command, func(t *testing.T) {
			installFake(t, "claude")
			m, f, _ := toEditor(t, 0)
			h := captureTurns(m)
			startPrompt(t, m, h, "pertama")
			emit(m, tocli.Event{Kind: tocli.EventSession, Text: "sesi-1"})
			h.finish(t, m, nil)

			typeText(m, command)
			send(m, enter)
			expectView(t, m, []string{"Tulis prompt"}, []string{"pertama"})
			if m.tocli.session != "" || m.screen != screenEditor {
				t.Fatalf("session %q, screen %d", m.tocli.session, m.screen)
			}
			assertNodes(t, f, 1)

			typeText(m, "kedua")
			send(m, enter)
			if cmd := h.next(t); slices.Contains(cmd.Args, "--resume") {
				t.Fatalf("%q", cmd.Args)
			}
			h.finish(t, m, nil)
		})
	}
}

func TestSlashNewWaitsForRunningPrompt(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	emit(m, tocli.Event{Kind: tocli.EventSession, Text: "sesi-1"})
	typeText(m, "/new")
	send(m, enter)
	if m.tocli.session != "sesi-1" || len(m.tocli.entries) == 0 || !strings.Contains(m.tocli.status, "Tunggu") {
		t.Fatalf("session %q, status %q", m.tocli.session, m.tocli.status)
	}
	h.finish(t, m, nil)
}

func TestSlashUsageKeepsSessionAndCreatesNoNode(t *testing.T) {
	installFake(t, "claude")
	m, f, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	emit(m, tocli.Event{Kind: tocli.EventSession, Text: "sesi-1"})
	h.finish(t, m, nil)

	typeText(m, "/usage")
	send(m, enter)
	cmd := h.next(t)
	if cmd.Args[len(cmd.Args)-1] != "/usage" || slices.Contains(cmd.Args, "--resume") {
		t.Fatalf("%q", cmd.Args)
	}
	if !m.tocli.running {
		t.Fatal("/usage must be running")
	}
	h.finish(t, m, nil)
	if m.tocli.session != "sesi-1" {
		t.Fatalf("session %q", m.tocli.session)
	}
	assertNodes(t, f, 1)
}

func TestSlashUsageWaitsForRunningPrompt(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	startPrompt(t, m, h, "pertama")
	typeText(m, "/usage")
	send(m, enter)
	if !strings.Contains(m.tocli.status, "Tunggu") {
		t.Fatalf("%q", m.tocli.status)
	}
	h.finish(t, m, nil)
}

func TestSlashUsageExplainsCodex(t *testing.T) {
	installFake(t, "codex")
	m, _, _ := toEditor(t, 1)
	typeText(m, "/usage")
	send(m, enter)
	expectView(t, m, []string{"tidak menyediakan sisa kuota"}, nil)
	if m.tocli.running {
		t.Fatal("codex has nothing to run")
	}
}

func TestOtherSlashTextStillReachesTheCLI(t *testing.T) {
	installFake(t, "claude")
	m, _, _ := toEditor(t, 0)
	h := captureTurns(m)
	cmd := startPrompt(t, m, h, "/compact")
	if cmd.Args[len(cmd.Args)-1] != "/compact" {
		t.Fatalf("%q", cmd.Args)
	}
	h.finish(t, m, nil)
	cmd = startPrompt(t, m, h, "/home/saya/app rapikan")
	if !strings.HasSuffix(cmd.Args[len(cmd.Args)-1], "/home/saya/app rapikan") {
		t.Fatalf("%q", cmd.Args)
	}
	h.finish(t, m, nil)
}

func assertNodes(t *testing.T, f *fixture, want int) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(ctx, store.NodeQuery{})
	if err != nil || len(nodes) != want {
		t.Fatalf("want %d nodes: %+v, %v", want, nodes, err)
	}
}

func TestUnsupportedAttachmentIsWarnedBeforeSending(t *testing.T) {
	installFake(t, "codex")
	m, _, _ := toEditor(t, 1)
	typeText(m, "lihat ")
	send(m, tea.PasteMsg{Content: strings.Repeat("x\n", 30)})
	h := captureTurns(m)
	send(m, enter)
	expectView(t, m, []string{"Peringatan", "TIDAK dikirim"}, nil)
	send(m, enter)
	cmd := h.next(t)
	prompt := cmd.Args[len(cmd.Args)-1]
	if strings.Contains(prompt, "[Paste") || !strings.HasPrefix(prompt, "<8memory-context>") || !strings.HasSuffix(prompt, "lihat") {
		t.Fatalf("%q", cmd.Args)
	}
	h.finish(t, m, nil)

	// The warning stops a later prompt too, even though it would skip the preview.
	typeText(m, "lagi ")
	send(m, tea.PasteMsg{Content: strings.Repeat("y\n", 30)})
	send(m, enter)
	if m.screen != screenCLIPreview {
		t.Fatalf("screen %d", m.screen)
	}
}

func TestPreviewReportsWhatWasCut(t *testing.T) {
	m := &Model{}
	lines := m.cutReport(compose.Result{DroppedRules: []string{"a", "b", "c", "d"}})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"4 aturan", "dan 1 lainnya"} {
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
