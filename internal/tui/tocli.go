package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"8memory/internal/compose"
	"8memory/internal/gitinfo"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

// Windows consoles only report Shift+Enter in win32-input-mode. The mode must
// be off whenever anything else reads the console, including the provider CLI.
const (
	win32InputOn  = "\x1b[?9001h"
	win32InputOff = "\x1b[?9001l"
)

func setWin32Input(on bool) tea.Cmd {
	if runtime.GOOS != "windows" {
		return nil
	}
	if on {
		return tea.Raw(win32InputOn)
	}
	return tea.Raw(win32InputOff)
}

const (
	tocliFieldProvider = iota
	tocliFieldFolder
)

const editorRows = 8

// tocliPlan is what was shown in the preview and will be sent.
type tocliPlan struct {
	nodeID   string
	typed    string
	briefing compose.Result
	warnings []string
}

type tocliState struct {
	provider int
	focus    int
	folder   textinput.Model
	formErr  string

	exe     tocli.Executable
	dir     string
	project string
	branch  string

	editor      editor
	attachments []tocli.Attachment
	nextID      int
	asking      bool
	pathInput   textinput.Model
	status      string
	statusErr   bool

	listCursor  int
	previewOf   string
	previewText []string
	offset      int

	plan        tocliPlan
	lastCommand *exec.Cmd
}

type (
	tocliReadyMsg struct {
		exe  tocli.Executable
		dir  string
		here gitinfo.Info
		err  error
	}
	tocliImageMsg struct {
		data []byte
		err  error
	}
	tocliPlanMsg struct {
		plan tocliPlan
		err  error
	}
	tocliLaunchMsg struct {
		cmd    *exec.Cmd
		nodeID string
		err    error
	}
	tocliDoneMsg struct {
		nodeID string
		err    error
	}
)

func (m *Model) configDir() string { return filepath.Dir(m.configPath) }

func (m *Model) attachmentsDir() string { return filepath.Join(m.configDir(), "attachments") }

func (m *Model) currentProvider() tocli.Provider { return tocli.Providers[m.tocli.provider] }

func (m *Model) openToCLI() (tea.Model, tea.Cmd) {
	wd, _ := os.Getwd()
	folder := newInput(wd, "folder proyek")
	folder.SetWidth(60)
	m.tocli = tocliState{provider: m.tocli.provider, folder: folder}
	m.screen = screenToCLIForm
	return m, nil
}

func (m *Model) updateToCLIForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "esc":
		m.screen = screenMenu
		return m, nil
	case "tab", "shift+tab", "up", "down":
		if t.focus == tocliFieldProvider {
			t.focus = tocliFieldFolder
			t.folder.Focus()
		} else {
			t.focus = tocliFieldProvider
			t.folder.Blur()
		}
		return m, nil
	case "enter":
		t.formErr = ""
		return m, m.prepareToCLI()
	}
	if t.focus == tocliFieldProvider {
		count := len(tocli.Providers)
		switch msg.String() {
		case "left", "h":
			t.provider = (t.provider + count - 1) % count
		case "right", "l", "space":
			t.provider = (t.provider + 1) % count
		}
		return m, nil
	}
	var cmd tea.Cmd
	t.folder, cmd = t.folder.Update(msg)
	return m, cmd
}

// prepareToCLI checks the CLI and folder before any typing is wasted on them.
func (m *Model) prepareToCLI() tea.Cmd {
	provider, folder, parent := m.currentProvider(), strings.TrimSpace(m.tocli.folder.Value()), m.ctx
	return func() tea.Msg {
		exe, err := tocli.Find(provider)
		if err != nil {
			return tocliReadyMsg{err: err}
		}
		dir, err := filepath.Abs(folder)
		if err != nil {
			return tocliReadyMsg{err: err}
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return tocliReadyMsg{err: fmt.Errorf("folder tidak ditemukan: %s", dir)}
		}
		return tocliReadyMsg{exe: exe, dir: dir, here: gitinfo.ForDir(parent, dir)}
	}
}

func (m *Model) handleToCLIReady(msg tocliReadyMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	if msg.err != nil {
		t.formErr = msg.err.Error()
		return m, nil
	}
	t.exe, t.dir, t.project, t.branch = msg.exe, msg.dir, msg.here.Project, msg.here.Branch
	t.status, t.statusErr = "", false
	m.screen = screenEditor
	return m, setWin32Input(true)
}

func (m *Model) setEditorStatus(text string, isError bool) {
	m.tocli.status, m.tocli.statusErr = text, isError
}

func (m *Model) updateEditor(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	if t.asking {
		return m.updatePathInput(msg)
	}
	switch msg.String() {
	case "enter":
		return m, m.startPlan()
	case "shift+enter", "alt+enter", "ctrl+enter", "ctrl+j":
		t.editor.insert("\n")
	case "left":
		t.editor.left()
	case "right":
		t.editor.right()
	case "up":
		t.editor.up()
	case "down":
		t.editor.down()
	case "home":
		t.editor.home()
	case "end":
		t.editor.end()
	case "backspace":
		m.discardAttachment(t.editor.backspace())
	case "delete":
		m.discardAttachment(t.editor.del())
	case "ctrl+o":
		t.asking = true
		t.pathInput = newInput("", "path file")
		t.pathInput.SetWidth(60)
		t.pathInput.Focus()
	case "alt+v", "ctrl+v":
		m.setEditorStatus("Membaca gambar dari clipboard...", false)
		parent := m.ctx
		return m, func() tea.Msg {
			data, err := tocli.ReadClipboardImage(parent)
			return tocliImageMsg{data: data, err: err}
		}
	case "ctrl+e":
		if len(t.attachments) == 0 {
			m.setEditorStatus("Belum ada attachment.", false)
			return m, nil
		}
		t.listCursor = min(t.listCursor, len(t.attachments)-1)
		m.screen = screenAttachments
	case "esc":
		m.screen = screenToCLIForm
		return m, setWin32Input(false)
	default:
		// A bare Ctrl press arrives as ctrl+space on some consoles; Text is
		// only set for keys that actually type something.
		if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			t.editor.insert(msg.Text)
		}
	}
	return m, nil
}

func (m *Model) updatePathInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "esc":
		t.asking = false
		return m, nil
	case "enter":
		attachment, err := tocli.NewFile(t.nextID+1, t.pathInput.Value())
		if err != nil {
			m.setEditorStatus(err.Error(), true)
			return m, nil
		}
		t.asking = false
		m.addAttachment(attachment)
		return m, nil
	}
	var cmd tea.Cmd
	t.pathInput, cmd = t.pathInput.Update(msg)
	return m, cmd
}

// addAttachment registers a new attachment and places its marker at the cursor.
func (m *Model) addAttachment(a tocli.Attachment) {
	t := &m.tocli
	t.nextID = a.ID
	t.attachments = append(t.attachments, a)
	t.editor.markers = append(t.editor.markers, a.Marker)
	t.editor.insert(a.Marker)
	m.setEditorStatus("", false)
}

// discardAttachment forgets the attachment whose marker was removed.
func (m *Model) discardAttachment(marker string) {
	if marker == "" {
		return
	}
	t := &m.tocli
	t.attachments = slices.DeleteFunc(t.attachments, func(a tocli.Attachment) bool { return a.Marker == marker })
	t.editor.markers = slices.DeleteFunc(t.editor.markers, func(known string) bool { return known == marker })
}

func (m *Model) pasteThresholds() (int, int) {
	lines, bytes := m.current.PasteLines, m.current.PasteBytes
	if lines == 0 {
		lines = tocli.DefaultPasteLines
	}
	if bytes == 0 {
		bytes = tocli.DefaultPasteBytes
	}
	return lines, bytes
}

// handlePaste sends pasted text to whatever is focused; in the editor a long
// paste collapses into an attachment.
func (m *Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.screen == screenEditor && !m.tocli.asking {
		text := tocli.NormalizePaste(msg.Content)
		if lines, bytes := m.pasteThresholds(); tocli.IsLongPaste(text, lines, bytes) {
			m.addAttachment(tocli.NewPaste(m.tocli.nextID+1, text))
			return m, nil
		}
		m.tocli.editor.insert(text)
		return m, nil
	}
	input := m.activeInput()
	if input == nil {
		return m, nil
	}
	var cmd tea.Cmd
	*input, cmd = input.Update(msg)
	return m, cmd
}

// activeInput returns the single-line input with focus, if there is one.
func (m *Model) activeInput() *textinput.Model {
	switch m.screen {
	case screenForm:
		return &m.inputs[m.focus]
	case screenList:
		if m.browser.searching {
			return &m.browser.search
		}
	case screenRuleForm:
		if m.rules.focus > ruleFieldScope {
			return &m.rules.inputs[m.rules.focus-1]
		}
	case screenToCLIForm:
		if m.tocli.focus == tocliFieldFolder {
			return &m.tocli.folder
		}
	case screenEditor:
		if m.tocli.asking {
			return &m.tocli.pathInput
		}
	}
	return nil
}

func (m *Model) handleImage(msg tocliImageMsg) (tea.Model, tea.Cmd) {
	switch {
	case errors.Is(msg.err, tocli.ErrNoImage):
		m.setEditorStatus("Clipboard tidak berisi gambar.", true)
	case msg.err != nil:
		m.setEditorStatus(msg.err.Error(), true)
	default:
		m.addAttachment(tocli.NewImage(m.tocli.nextID+1, msg.data))
	}
	return m, nil
}

func (m *Model) updateAttachments(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "up", "k":
		if t.listCursor > 0 {
			t.listCursor--
		}
	case "down", "j":
		if t.listCursor < len(t.attachments)-1 {
			t.listCursor++
		}
	case "enter":
		attachment := t.attachments[t.listCursor]
		t.previewOf, t.offset = attachment.Marker, 0
		wrapped := lipgloss.NewStyle().Width(m.viewWidth() - 2).Render(tocli.Preview(attachment))
		t.previewText = strings.Split(wrapped, "\n")
		m.screen = screenAttachPreview
	case "d":
		marker := t.attachments[t.listCursor].Marker
		t.editor.removeMarker(marker)
		m.discardAttachment(marker)
		if len(t.attachments) == 0 {
			m.screen = screenEditor
			return m, nil
		}
		t.listCursor = min(t.listCursor, len(t.attachments)-1)
	case "esc":
		m.screen = screenEditor
	}
	return m, nil
}

func (m *Model) updateAttachPreview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "up", "k":
		if t.offset > 0 {
			t.offset--
		}
	case "down", "j":
		if t.offset < len(t.previewText)-1 {
			t.offset++
		}
	case "esc", "q":
		m.screen = screenAttachments
	}
	return m, nil
}

// startPlan builds the briefing for preview. The node ID is chosen now so the
// preview shows exactly the text that will be sent.
func (m *Model) startPlan() tea.Cmd {
	t := &m.tocli
	typed := t.editor.value()
	clean := tocli.StripMarkers(typed, t.attachments)
	if strings.TrimSpace(clean) == "" && len(t.attachments) == 0 {
		m.setEditorStatus("Prompt masih kosong.", true)
		return nil
	}
	warnings := tocli.Warnings(m.currentProvider(), t.attachments)
	project, branch := t.project, t.branch
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		nodeID, err := store.NewID()
		if err != nil {
			return tocliPlanMsg{err: err}
		}
		briefing, err := compose.Build(ctx, s, compose.Input{Project: project, Branch: branch, Prompt: clean, NodeID: nodeID})
		if err != nil {
			return tocliPlanMsg{err: err}
		}
		return tocliPlanMsg{plan: tocliPlan{nodeID: nodeID, typed: typed, briefing: briefing, warnings: warnings}}
	}, func(err error) tea.Msg { return tocliPlanMsg{err: err} })
}

func (m *Model) handlePlan(msg tocliPlanMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setEditorStatus(msg.err.Error(), true)
		return m, nil
	}
	t := &m.tocli
	t.plan, t.offset = msg.plan, 0
	body := lipgloss.NewStyle().Width(m.viewWidth() - 2).Render(msg.plan.briefing.Text)
	t.previewText = strings.Split(body, "\n")
	m.screen = screenCLIPreview
	return m, nil
}

func (m *Model) updateCLIPreview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "up", "k":
		if t.offset > 0 {
			t.offset--
		}
	case "down", "j":
		if t.offset < len(t.previewText)-1 {
			t.offset++
		}
	case "enter":
		m.busyLabel = "Menjalankan " + m.currentProvider().Label() + "..."
		m.screen = screenBusy
		return m, m.launch()
	case "esc", "q":
		m.screen = screenEditor
	}
	return m, nil
}

// launch saves attachments, then records the node and pending marker last, so
// a command that cannot be built leaves no trace in the journey.
func (m *Model) launch() tea.Cmd {
	t := &m.tocli
	plan, project, branch, dir, exe := t.plan, t.project, t.branch, t.dir, t.exe
	provider, attachments := m.currentProvider(), slices.Clone(t.attachments)
	attachmentsDir, configDir := m.attachmentsDir(), m.configDir()
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		fail := func(err error) tea.Msg { return tocliLaunchMsg{err: err} }
		saved, err := tocli.Save(attachmentsDir, plan.nodeID, attachments)
		if err != nil {
			return fail(err)
		}
		request := tocli.Request{Context: plan.briefing.Text, Prompt: plan.typed, Attachments: saved}
		if len(saved) > 0 {
			request.AttachDir, _ = tocli.NodeDir(attachmentsDir, plan.nodeID)
		}
		cmd, err := tocli.Build(provider, exe, dir, plan.nodeID, request)
		if err != nil {
			tocli.RemoveSaved(attachmentsDir, plan.nodeID)
			return fail(err)
		}
		node := store.Node{
			ID: plan.nodeID, Project: project, Branch: branch, Agent: provider.ID(),
			Title: tocli.Title(plan.typed, attachments),
		}
		if _, err := s.AddNode(ctx, node); err != nil {
			tocli.RemoveSaved(attachmentsDir, plan.nodeID)
			return fail(err)
		}
		if err := tocli.MarkPending(configDir, plan.nodeID); err != nil {
			return fail(err)
		}
		return tocliLaunchMsg{cmd: cmd, nodeID: plan.nodeID}
	}, func(err error) tea.Msg { return tocliLaunchMsg{err: err} })
}

func (m *Model) handleLaunch(msg tocliLaunchMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenEditor
		m.setEditorStatus(msg.err.Error(), true)
		return m, nil
	}
	m.tocli.lastCommand = msg.cmd
	nodeID := msg.nodeID
	return m, tea.Exec(&handoff{Cmd: msg.cmd}, func(err error) tea.Msg {
		return tocliDoneMsg{nodeID: nodeID, err: err}
	})
}

func (m *Model) handleDone(msg tocliDoneMsg) (tea.Model, tea.Cmd) {
	tocli.ClearPending(m.configDir(), msg.nodeID)
	label := m.currentProvider().Label()
	m.tocli.editor.reset()
	m.tocli.attachments = nil
	m.screen = screenMenu
	m.notice, m.noticeErr = label+" ditutup.", false
	if msg.err != nil {
		m.notice, m.noticeErr = label+" berakhir dengan pesan: "+msg.err.Error(), true
	}
	return m, nil
}

// handoff gives the terminal to the provider CLI after switching off the
// input mode that only this TUI understands.
type handoff struct{ *exec.Cmd }

func (h *handoff) Run() error {
	if runtime.GOOS == "windows" {
		os.Stdout.WriteString(win32InputOff)
	}
	return h.Cmd.Run()
}

func (h *handoff) SetStdin(r io.Reader) {
	if h.Stdin == nil {
		h.Stdin = r
	}
}

func (h *handoff) SetStdout(w io.Writer) {
	if h.Stdout == nil {
		h.Stdout = w
	}
}

func (h *handoff) SetStderr(w io.Writer) {
	if h.Stderr == nil {
		h.Stderr = w
	}
}

func (m *Model) viewToCLIForm(b *strings.Builder) {
	t := &m.tocli
	b.WriteString("ToCLI\n\n")
	var names []string
	for i, provider := range tocli.Providers {
		label := provider.Label()
		if i == t.provider {
			label = "[" + label + "]"
		}
		names = append(names, label)
	}
	providerMark, folderMark := "> ", "  "
	if t.focus == tocliFieldFolder {
		providerMark, folderMark = "  ", "> "
	}
	fmt.Fprintf(b, "%s%-9s %s\n", providerMark, "Provider", strings.Join(names, " "))
	fmt.Fprintf(b, "%s%-9s %s\n", folderMark, "Folder", t.folder.View())
	if t.formErr != "" {
		b.WriteString("\n" + errorStyle.Width(m.viewWidth()-2).Render(t.formErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("tab pindah · ←/→ ganti provider · enter lanjut · esc kembali") + "\n")
}

func (m *Model) location() string {
	t := &m.tocli
	return orDash(t.project) + " @ " + orDash(t.branch)
}

func (m *Model) viewEditor(b *strings.Builder) {
	t := &m.tocli
	fmt.Fprintf(b, "ToCLI · %s · %s\n\n", m.currentProvider().Label(), m.location())
	b.WriteString(t.editor.render(m.viewWidth()-2, editorRows) + "\n\n")
	if t.asking {
		b.WriteString("Path file: " + t.pathInput.View() + "\n")
	}
	if len(t.attachments) > 0 {
		b.WriteString(dimStyle.Render(strconv.Itoa(len(t.attachments))+" attachment") + "\n")
	}
	if t.status != "" {
		style := dimStyle
		if t.statusErr {
			style = errorStyle
		}
		b.WriteString(style.Width(m.viewWidth()-2).Render(t.status) + "\n")
	}
	if t.asking {
		b.WriteString(dimStyle.Render("enter tambahkan · esc batal") + "\n")
		return
	}
	b.WriteString(dimStyle.Width(m.viewWidth()-2).Render("enter kirim · shift+enter atau ctrl+j baris baru · alt+v gambar clipboard · ctrl+o file · ctrl+e attachment · esc kembali") + "\n")
}

func (m *Model) viewAttachments(b *strings.Builder) {
	t := &m.tocli
	b.WriteString("Attachment\n\n")
	width := m.viewWidth() - 4
	for i, attachment := range t.attachments {
		marker := "  "
		if i == t.listCursor {
			marker = "> "
		}
		b.WriteString(marker + runewidth.Truncate(attachment.Marker, width, "…") + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ pilih · enter pratinjau · d hapus · esc kembali") + "\n")
}

func (m *Model) viewScrolled(b *strings.Builder, lines []string, offset int) {
	start := min(offset, max(len(lines)-1, 0))
	end := min(start+m.bodyHeight(), len(lines))
	b.WriteString(strings.Join(lines[start:end], "\n") + "\n")
}

func (m *Model) viewAttachPreview(b *strings.Builder) {
	t := &m.tocli
	b.WriteString(t.previewOf + "\n\n")
	m.viewScrolled(b, t.previewText, t.offset)
	b.WriteString("\n" + dimStyle.Render("↑/↓ gulir · esc kembali") + "\n")
}

func (m *Model) viewCLIPreview(b *strings.Builder) {
	t := &m.tocli
	briefing := t.plan.briefing
	fmt.Fprintf(b, "Pratinjau · %s · %s · %d byte\n\n", m.currentProvider().Label(), m.location(), len(briefing.Text))
	m.viewScrolled(b, t.previewText, t.offset)
	b.WriteString("\n")
	width := m.viewWidth() - 2
	for _, line := range m.cutReport(briefing) {
		b.WriteString(errorStyle.Width(width).Render(line) + "\n")
	}
	for _, warning := range t.plan.warnings {
		b.WriteString(errorStyle.Width(width).Render("Peringatan: "+warning) + "\n")
	}
	if len(t.attachments) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("%d attachment dikirim terpisah, tidak dihitung dalam batas ringkasan.", len(t.attachments))) + "\n")
	}
	b.WriteString(dimStyle.Render("enter kirim · ↑/↓ gulir · esc kembali") + "\n")
}

func (m *Model) cutReport(r compose.Result) []string {
	var out []string
	if len(r.DroppedRules) > 0 {
		out = append(out, fmt.Sprintf("Terpotong: %d aturan tidak muat: %s", len(r.DroppedRules), summarize(r.DroppedRules)))
	}
	if len(r.DroppedMemories) > 0 {
		out = append(out, fmt.Sprintf("Terpotong: %d memori tidak muat: %s", len(r.DroppedMemories), summarize(r.DroppedMemories)))
	}
	if len(r.ShortenedMemories) > 0 {
		out = append(out, fmt.Sprintf("Dipendekkan: %d memori: %s", len(r.ShortenedMemories), summarize(r.ShortenedMemories)))
	}
	return out
}

func summarize(names []string) string {
	const shown = 3
	short := make([]string, 0, shown)
	for _, name := range names[:min(shown, len(names))] {
		short = append(short, runewidth.Truncate(strings.Join(strings.Fields(name), " "), 40, "…"))
	}
	text := strings.Join(short, ", ")
	if len(names) > shown {
		text += fmt.Sprintf(", dan %d lainnya", len(names)-shown)
	}
	return text
}
