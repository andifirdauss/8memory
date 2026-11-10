package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// tocliPlan is what was shown in the preview and will be sent.
type tocliPlan struct {
	nodeID   string
	typed    string
	briefing compose.Result
	warnings []string
	shown    bool // the user saw this plan before it was sent
}

type tocliState struct {
	provider       int
	providerCursor int
	focus          int
	folder         textinput.Model
	formErr        string

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

	plan tocliPlan

	chatCtx    context.Context
	chatCancel context.CancelFunc
	inbox      chan tocliInboxMsg
	approvals  *tocli.ApprovalServer

	entries  []*chatEntry
	scroll   int // rows above the bottom of the transcript
	session  string
	sending  bool // a prompt is being prepared
	running  bool // the CLI is working on a prompt
	stop     context.CancelFunc
	turnCtx  context.Context
	finished chan struct{}

	exitArmed   bool
	slashCursor int

	effort       string // empty means the CLI's own setting
	effortCursor int

	pending        []*tocli.Approval
	approvalOffset int
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
)

func (m *Model) configDir() string { return filepath.Dir(m.configPath) }

func (m *Model) attachmentsDir() string { return filepath.Join(m.configDir(), "attachments") }

func (m *Model) currentProvider() tocli.Provider { return tocli.Providers[m.tocli.provider] }

func (m *Model) openToCLI() (tea.Model, tea.Cmd) {
	wd, _ := os.Getwd()
	folder := newInput(wd, "project folder")
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
		if t.focus == tocliFieldProvider {
			t.providerCursor = t.provider
			m.screen = screenProviderPick
			return m, nil
		}
		t.formErr = ""
		return m, m.prepareToCLI()
	}
	if t.focus == tocliFieldProvider {
		return m, nil
	}
	var cmd tea.Cmd
	t.folder, cmd = t.folder.Update(msg)
	return m, cmd
}

// updateProviderPick moves on to the folder after a choice, since that is the
// only field left before the chat opens.
func (m *Model) updateProviderPick(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "up", "k":
		if t.providerCursor > 0 {
			t.providerCursor--
		}
	case "down", "j":
		if t.providerCursor < len(tocli.Providers)-1 {
			t.providerCursor++
		}
	case "enter":
		t.provider, t.formErr = t.providerCursor, ""
		t.focus = tocliFieldFolder
		t.folder.Focus()
		m.screen = screenToCLIForm
	case "q", "esc":
		m.screen = screenToCLIForm
	}
	return m, nil
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
			return tocliReadyMsg{err: fmt.Errorf("folder not found: %s", dir)}
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
	t.status, t.statusErr, t.effort = "", false, ""
	if err := m.openChat(); err != nil {
		t.formErr = err.Error()
		return m, nil
	}
	m.screen = screenEditor
	return m, tea.Batch(setWin32Input(true), m.listen(t.inbox, t.chatCtx))
}

func (m *Model) setEditorStatus(text string, isError bool) {
	m.tocli.status, m.tocli.statusErr = text, isError
}

func (m *Model) updateEditor(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	if len(t.pending) > 0 {
		return m.updateApproval(msg)
	}
	if t.asking {
		return m.updatePathInput(msg)
	}
	if matches := m.slashMatches(); len(matches) > 0 {
		if model, cmd, handled := m.updateSlashMenu(msg, matches); handled {
			return model, cmd
		}
	}
	if msg.String() != "esc" {
		t.exitArmed = false
	}
	switch msg.String() {
	case "enter":
		if level, ok := effortArgument(t.editor.value()); ok {
			m.slashEffortLevel(level)
			return m, nil
		}
		return m, m.startPlan()
	case "pgup":
		t.scroll += m.bodyHeight()
	case "pgdown":
		t.scroll = max(t.scroll-m.bodyHeight(), 0)
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
		t.pathInput = newInput("", "file path")
		t.pathInput.SetWidth(60)
		t.pathInput.Focus()
	case "alt+v", "ctrl+v":
		m.setEditorStatus("Reading image from clipboard...", false)
		parent := m.ctx
		return m, func() tea.Msg {
			data, err := tocli.ReadClipboardImage(parent)
			return tocliImageMsg{data: data, err: err}
		}
	case "ctrl+e":
		if len(t.attachments) == 0 {
			m.setEditorStatus("No attachments yet.", false)
			return m, nil
		}
		t.listCursor = min(t.listCursor, len(t.attachments)-1)
		m.screen = screenAttachments
	case "esc":
		return m.escapeChat()
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

// escapeChat stops a running prompt. Otherwise it leaves the session, asking
// once first because the transcript is only kept on screen.
func (m *Model) escapeChat() (tea.Model, tea.Cmd) {
	t := &m.tocli
	if t.running {
		m.stopTurn()
		m.setEditorStatus("Stopping...", false)
		return m, nil
	}
	if len(t.entries) > 0 && !t.exitArmed {
		t.exitArmed = true
		m.setEditorStatus("Press esc again to end this session.", false)
		return m, nil
	}
	m.closeChat()
	m.screen = screenToCLIForm
	return m, setWin32Input(false)
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
	if m.screen == screenEditor && len(m.tocli.pending) > 0 {
		return m, nil
	}
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
		m.setEditorStatus("Clipboard does not contain an image.", true)
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
	if t.running || t.sending {
		m.setEditorStatus(waitForReplyNote, false)
		return nil
	}
	typed := t.editor.value()
	clean := tocli.StripMarkers(typed, t.attachments)
	if strings.TrimSpace(clean) == "" && len(t.attachments) == 0 {
		m.setEditorStatus("Prompt is empty.", true)
		return nil
	}
	t.sending = true
	warnings := tocli.Warnings(m.currentProvider(), t.attachments)
	project, branch := t.project, t.branch
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		nodeID, err := store.NewID()
		if err != nil {
			return tocliPlanMsg{err: err}
		}
		briefing, err := compose.Build(ctx, s, compose.Input{Project: project, Branch: branch, NodeID: nodeID})
		if err != nil {
			return tocliPlanMsg{err: err}
		}
		return tocliPlanMsg{plan: tocliPlan{nodeID: nodeID, typed: typed, briefing: briefing, warnings: warnings}}
	}, func(err error) tea.Msg { return tocliPlanMsg{err: err} })
}

// handlePlan sends the prompt straight away, as a CLI does. It only stops
// first when an attachment would not arrive, so nothing is dropped silently.
func (m *Model) handlePlan(msg tocliPlanMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	if msg.err != nil {
		t.sending = false
		m.setEditorStatus(msg.err.Error(), true)
		return m, nil
	}
	t.plan, t.offset = msg.plan, 0
	if len(msg.plan.warnings) == 0 {
		return m, m.launch()
	}
	t.sending, t.plan.shown = false, true
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
		t.sending = true
		m.screen = screenEditor
		return m, m.launch()
	case "esc", "q":
		m.screen = screenEditor
	}
	return m, nil
}

// memoryServer describes this program as the MCP server of the CLI, so the AI
// has its memory tools without the user registering anything.
func (m *Model) memoryServer() (*tocli.MCPServer, error) {
	program, err := os.Executable()
	if err != nil {
		return nil, err
	}
	config, err := filepath.Abs(m.configPath)
	if err != nil {
		return nil, err
	}
	return &tocli.MCPServer{Type: "stdio", Command: program, Args: []string{"serve", "--config", config}}, nil
}

// launch saves attachments, then records the node and pending marker last, so
// a command that cannot be built leaves no trace in the journey.
func (m *Model) launch() tea.Cmd {
	t := &m.tocli
	plan, project, branch, dir, exe, session, effort := t.plan, t.project, t.branch, t.dir, t.exe, t.session, t.effort
	provider, attachments := m.currentProvider(), slices.Clone(t.attachments)
	attachmentsDir, configDir := m.attachmentsDir(), m.configDir()
	var approval *tocli.MCPServer
	if t.approvals != nil {
		approval = t.approvals.Server()
	}
	memory, memoryErr := m.memoryServer()
	turnCtx, stop := context.WithCancel(t.chatCtx)
	t.stop, t.turnCtx = stop, turnCtx
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		fail := func(err error) tea.Msg { return tocliLaunchMsg{err: err} }
		if memoryErr != nil {
			return fail(memoryErr)
		}
		saved, err := tocli.Save(attachmentsDir, plan.nodeID, attachments)
		if err != nil {
			return fail(err)
		}
		request := tocli.Request{
			Full: plan.briefing.Text, System: plan.briefing.System, Turn: plan.briefing.Turn,
			Prompt: plan.typed, Attachments: saved, Session: session, Effort: effort, Memory: memory, Approval: approval,
		}
		if len(saved) > 0 {
			request.AttachDir, _ = tocli.NodeDir(attachmentsDir, plan.nodeID)
		}
		cmd, err := tocli.Build(turnCtx, provider, exe, dir, plan.nodeID, request)
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
	t := &m.tocli
	t.sending = false
	if msg.err != nil {
		m.stopTurn()
		m.setEditorStatus(msg.err.Error(), true)
		return m, nil
	}
	m.addEntry(chatUser, t.plan.typed)
	if !t.plan.shown {
		for _, line := range m.cutReport(t.plan.briefing) {
			m.addEntry(chatInfo, line)
		}
	}
	t.editor.reset()
	t.attachments, t.nextID, t.scroll = nil, 0, 0
	m.setEditorStatus("", false)
	m.startTurn(msg.cmd, msg.nodeID, m.currentProvider().NewParser())
	return m, nil
}

func (m *Model) viewToCLIForm(b *strings.Builder) {
	t := &m.tocli
	b.WriteString("Chat\n\n")
	providerMark, folderMark, hint := "> ", "  ", "tab next · enter choose provider · esc back"
	if t.focus == tocliFieldFolder {
		providerMark, folderMark, hint = "  ", "> ", "tab next · enter start chat · esc back"
	}
	fmt.Fprintf(b, "%s%-9s %s ▸\n", providerMark, "Provider", m.currentProvider().Label())
	fmt.Fprintf(b, "%s%-9s %s\n", folderMark, "Folder", t.folder.View())
	if t.formErr != "" {
		b.WriteString("\n" + errorStyle.Width(m.viewWidth()-2).Render(t.formErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render(hint) + "\n")
}

func (m *Model) viewProviderPick(b *strings.Builder) {
	b.WriteString("Provider\n\n")
	for i, provider := range tocli.Providers {
		marker := "  "
		if i == m.tocli.providerCursor {
			marker = "> "
		}
		b.WriteString(marker + provider.Label() + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ select · enter use · esc back") + "\n")
}

func (m *Model) location() string {
	t := &m.tocli
	return orDash(t.project) + " @ " + orDash(t.branch)
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
	b.WriteString("\n" + dimStyle.Render("↑/↓ select · enter preview · d delete · esc back") + "\n")
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
	b.WriteString("\n" + dimStyle.Render("↑/↓ scroll · esc back") + "\n")
}

func (m *Model) viewCLIPreview(b *strings.Builder) {
	t := &m.tocli
	briefing := t.plan.briefing
	fmt.Fprintf(b, "Preview · %s · %s · %d bytes\n\n", m.currentProvider().Label(), m.location(), len(briefing.Text))
	m.viewScrolled(b, t.previewText, t.offset)
	b.WriteString("\n")
	width := m.viewWidth() - 2
	for _, line := range m.cutReport(briefing) {
		b.WriteString(errorStyle.Width(width).Render(line) + "\n")
	}
	for _, warning := range t.plan.warnings {
		b.WriteString(errorStyle.Width(width).Render("Warning: "+warning) + "\n")
	}
	if len(t.attachments) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("%d attachment(s) sent separately, not counted in the briefing limit.", len(t.attachments))) + "\n")
	}
	b.WriteString(dimStyle.Render("enter send · ↑/↓ scroll · esc back") + "\n")
}

func (m *Model) cutReport(r compose.Result) []string {
	var out []string
	if len(r.DroppedRules) > 0 {
		out = append(out, fmt.Sprintf("Truncated, %d rule(s) did not fit: %s", len(r.DroppedRules), summarize(r.DroppedRules)))
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
		text += fmt.Sprintf(", and %d more", len(names)-shown)
	}
	return text
}
