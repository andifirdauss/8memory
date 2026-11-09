package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"8memory/internal/tocli"
)

const (
	chatEditorRows   = 6
	approvalRows     = 10
	chatTopLines     = 3 // the title block drawn by render
	shutdownWait     = 5 * time.Second
	stoppedMessage   = "Dihentikan."
	waitForReplyNote = "Tunggu balasan selesai, atau tekan esc untuk menghentikannya."
)

type chatKind int

const (
	chatUser chatKind = iota
	chatReply
	chatTool
	chatInfo
	chatError
)

// chatEntry is one block of the transcript. Its wrapped form is kept because
// the screen is redrawn for every piece of a streaming reply.
type chatEntry struct {
	kind  chatKind
	text  string
	width int
	lines []string
}

// tocliInboxMsg carries everything that happens outside the UI goroutine. It
// names its inbox so a late message from a closed session can be ignored.
type tocliInboxMsg struct {
	inbox   chan tocliInboxMsg
	payload any
}

type tocliTurnDone struct{ err error }

// turnRunner runs one prompt to completion.
type turnRunner func(*exec.Cmd, tocli.Parser, func(tocli.Event)) error

func listenInbox(inbox chan tocliInboxMsg, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-inbox:
			return msg
		case <-ctx.Done():
			return nil
		}
	}
}

func post(ctx context.Context, inbox chan tocliInboxMsg, payload any) {
	select {
	case inbox <- tocliInboxMsg{inbox: inbox, payload: payload}:
	case <-ctx.Done():
	}
}

// openChat starts the session's inbox and, for a CLI that can ask, the local
// server its permission requests arrive at.
func (m *Model) openChat() error {
	t := &m.tocli
	ctx, cancel := context.WithCancel(m.ctx)
	inbox := make(chan tocliInboxMsg, 64)
	if m.currentProvider().AsksApproval() {
		server, err := tocli.StartApprovals(func(a *tocli.Approval) { post(ctx, inbox, a) })
		if err != nil {
			cancel()
			return err
		}
		t.approvals = server
	}
	t.chatCtx, t.chatCancel, t.inbox = ctx, cancel, inbox
	return nil
}

// closeChat ends the session and stops any prompt still running.
func (m *Model) closeChat() {
	t := &m.tocli
	if t.chatCancel != nil {
		t.chatCancel()
		t.chatCancel = nil
	}
	if t.approvals != nil {
		t.approvals.Close()
		t.approvals = nil
	}
	t.inbox, t.pending, t.running, t.sending = nil, nil, false, false
}

// shutdown waits for a running CLI to be stopped before the program exits, so
// it does not keep working unseen.
func (m *Model) shutdown() {
	m.cancel()
	m.closeChat()
	if finished := m.tocli.finished; finished != nil {
		select {
		case <-finished:
		case <-time.After(shutdownWait):
		}
	}
}

func (m *Model) handleInbox(msg tocliInboxMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	if msg.inbox != t.inbox || t.inbox == nil {
		return m, nil
	}
	switch payload := msg.payload.(type) {
	case tocli.Event:
		m.applyEvent(payload)
	case *tocli.Approval:
		t.pending = append(t.pending, payload)
	case tocliTurnDone:
		m.finishTurn(payload.err)
	}
	return m, m.listen(t.inbox, t.chatCtx)
}

func (m *Model) addEntry(kind chatKind, text string) {
	m.tocli.entries = append(m.tocli.entries, &chatEntry{kind: kind, text: tocli.Clean(text)})
}

func (m *Model) applyEvent(event tocli.Event) {
	t := &m.tocli
	switch event.Kind {
	case tocli.EventSession:
		t.session = event.Text
	case tocli.EventText:
		if count := len(t.entries); count > 0 && !event.Fresh && t.entries[count-1].kind == chatReply {
			last := t.entries[count-1]
			last.text += tocli.Clean(event.Text)
			last.lines = nil
			return
		}
		m.addEntry(chatReply, event.Text)
	case tocli.EventTool:
		m.addEntry(chatTool, event.Text)
	case tocli.EventInfo:
		m.addEntry(chatInfo, event.Text)
	case tocli.EventError:
		m.addEntry(chatError, event.Text)
	}
}

// startTurn runs the built command in the background.
func (m *Model) startTurn(cmd *exec.Cmd, nodeID string, parser tocli.Parser) {
	t := &m.tocli
	finished := make(chan struct{})
	t.finished, t.running = finished, true
	ctx, turnCtx, inbox, configDir := t.chatCtx, t.turnCtx, t.inbox, m.configDir()
	run := m.runTurn
	go func() {
		defer close(finished)
		err := run(cmd, parser, func(event tocli.Event) { post(ctx, inbox, event) })
		if nodeID != "" {
			tocli.ClearPending(configDir, nodeID)
		}
		if turnCtx.Err() != nil {
			err = context.Canceled
		}
		post(ctx, inbox, tocliTurnDone{err: err})
	}()
}

func (m *Model) stopTurn() {
	if m.tocli.stop != nil {
		m.tocli.stop()
	}
}

func (m *Model) finishTurn(err error) {
	t := &m.tocli
	t.running = false
	m.stopTurn() // releases the turn's context
	// A request left over from the finished prompt has nobody waiting for it.
	for _, approval := range t.pending {
		approval.Answer(false)
	}
	t.pending, t.approvalOffset = nil, 0
	if errors.Is(err, context.Canceled) {
		m.addEntry(chatInfo, stoppedMessage)
	} else if err != nil {
		m.addEntry(chatError, err.Error())
	}
	m.setEditorStatus("", false)
}

// updateApproval decides a tool call. Allowing takes ctrl+y rather than y,
// because the dialog can open while the user is typing the next prompt.
func (m *Model) updateApproval(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	switch msg.String() {
	case "ctrl+y":
		m.answerApproval(true)
	case "esc":
		m.answerApproval(false)
	case "up":
		if t.approvalOffset > 0 {
			t.approvalOffset--
		}
	case "down":
		t.approvalOffset++
	}
	return m, nil
}

func (m *Model) answerApproval(allow bool) {
	t := &m.tocli
	current := t.pending[0]
	current.Answer(allow)
	t.pending, t.approvalOffset = t.pending[1:], 0
	verdict := "Ditolak: "
	if allow {
		verdict = "Diizinkan: "
	}
	m.addEntry(chatInfo, verdict+current.ToolName)
}

func wrapText(text string, width int) []string {
	wrapped := lipgloss.NewStyle().Width(width).Render(text)
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

func (e *chatEntry) wrapped(width int) []string {
	if e.lines != nil && e.width == width {
		return e.lines
	}
	e.width = width
	switch e.kind {
	case chatUser:
		lines := wrapText(e.text, width-2)
		for i := range lines {
			prefix := "  "
			if i == 0 {
				prefix = "> "
			}
			lines[i] = titleStyle.Render(prefix + lines[i])
		}
		e.lines = lines
	case chatReply:
		e.lines = wrapText(strings.Trim(e.text, "\n"), width)
	case chatError:
		e.lines = styledLines(errorStyle, e.text, width)
	default:
		e.lines = styledLines(dimStyle, e.text, width)
	}
	return e.lines
}

func styledLines(style lipgloss.Style, text string, width int) []string {
	lines := wrapText(text, width)
	for i := range lines {
		lines[i] = style.Render(lines[i])
	}
	return lines
}

// transcript returns exactly height rows, ending scroll rows above the bottom.
func (m *Model) transcript(width, height int) string {
	t := &m.tocli
	var all []string
	for i, entry := range t.entries {
		if i > 0 && !(entry.kind == chatTool && t.entries[i-1].kind == chatTool) {
			all = append(all, "")
		}
		all = append(all, entry.wrapped(width)...)
	}
	if len(all) == 0 {
		all = []string{dimStyle.Render("Tulis prompt di bawah. Setiap prompt diperkaya aturan dan memori dari 8memory.")}
	}
	t.scroll = min(t.scroll, max(len(all)-height, 0))
	end := len(all) - t.scroll
	window := all[max(end-height, 0):end]
	padding := strings.Repeat("\n", height-len(window))
	return padding + strings.Join(window, "\n")
}

func (m *Model) chatStatusLines(width int) string {
	t := &m.tocli
	var b strings.Builder
	if t.running && len(t.pending) == 0 {
		b.WriteString(dimStyle.Render(m.currentProvider().Label()+" bekerja...") + "\n")
	}
	if len(t.attachments) > 0 {
		b.WriteString(dimStyle.Render(strconv.Itoa(len(t.attachments))+" attachment") + "\n")
	}
	if t.status != "" {
		style := dimStyle
		if t.statusErr {
			style = errorStyle
		}
		b.WriteString(style.Width(width).Render(t.status) + "\n")
	}
	return b.String()
}

func (m *Model) approvalFooter(width int) string {
	t := &m.tocli
	current := t.pending[0]
	var b strings.Builder
	heading := "Izin diminta: " + current.ToolName
	if count := len(t.pending); count > 1 {
		heading += fmt.Sprintf(" (%d lagi menunggu)", count-1)
	}
	b.WriteString(errorStyle.Render(heading) + "\n")
	lines := wrapText(current.Detail, width)
	t.approvalOffset = min(t.approvalOffset, max(len(lines)-approvalRows, 0))
	end := min(t.approvalOffset+approvalRows, len(lines))
	b.WriteString(strings.Join(lines[t.approvalOffset:end], "\n") + "\n")
	hint := "ctrl+y izinkan · esc tolak"
	if len(lines) > approvalRows {
		hint += fmt.Sprintf(" · ↑/↓ gulir (baris %d-%d dari %d)", t.approvalOffset+1, end, len(lines))
	}
	b.WriteString(dimStyle.Width(width).Render(hint) + "\n")
	return b.String()
}

func (m *Model) chatFooter(width int) string {
	t := &m.tocli
	if len(t.pending) > 0 {
		return m.approvalFooter(width)
	}
	var b strings.Builder
	if matches := m.slashMatches(); len(matches) > 0 {
		b.WriteString(m.slashMenu(matches))
	}
	b.WriteString(t.editor.render(width, chatEditorRows) + "\n")
	if t.asking {
		b.WriteString("Path file: " + t.pathInput.View() + "\n")
	}
	b.WriteString(m.chatStatusLines(width))
	hint := "/ perintah · enter kirim · shift+enter atau ctrl+j baris baru · pgup/pgdn gulir · alt+v gambar · ctrl+o file · ctrl+e attachment · esc keluar"
	switch {
	case t.asking:
		hint = "enter tambahkan · esc batal"
	case t.running:
		hint = "esc atau ctrl+c hentikan · pgup/pgdn gulir"
	}
	b.WriteString(dimStyle.Width(width).Render(hint) + "\n")
	return b.String()
}

func (m *Model) viewEditor(b *strings.Builder) {
	width := m.viewWidth() - 2
	fmt.Fprintf(b, "Chat · %s · %s\n\n", m.currentProvider().Label(), m.location())
	footer := m.chatFooter(width)
	height := m.height
	if height == 0 {
		height = defaultHeight
	}
	rows := max(height-chatTopLines-2-1-strings.Count(footer, "\n")-1, 3)
	b.WriteString(m.transcript(width, rows) + "\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString(footer)
}
