package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"8memory/internal/tocli"
)

type slashCommand struct{ name, description string }

// slashCommands run in 8memory itself. Any other text starting with a slash
// goes to the provider's CLI like a normal prompt.
var slashCommands = []slashCommand{
	{"/help", "daftar perintah dan tombol"},
	{"/new", "mulai percakapan baru"},
	{"/clear", "bersihkan layar dan mulai percakapan baru"},
	{"/usage", "sisa kuota provider, tanpa memakai kuota"},
	{"/exit", "akhiri sesi ini"},
}

const slashHelpKeys = `Tombol:
  enter            kirim prompt
  shift+enter      baris baru (juga ctrl+j)
  alt+v            gambar dari clipboard
  ctrl+o           tambah file
  ctrl+e           daftar attachment
  pgup, pgdn       gulir percakapan
  esc, ctrl+c      hentikan prompt yang berjalan
  esc dua kali     akhiri sesi`

// slashMatches lists the commands the editor's text could become. The menu
// only applies to a single word, so a prompt that merely starts with a path
// or a command with arguments is left alone.
func (m *Model) slashMatches() []slashCommand {
	query := strings.TrimSpace(m.tocli.editor.value())
	if !strings.HasPrefix(query, "/") || strings.ContainsAny(query, " \t\n") {
		return nil
	}
	var matches []slashCommand
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, query) {
			matches = append(matches, command)
		}
	}
	return matches
}

// updateSlashMenu handles the keys the open menu takes over from the editor.
func (m *Model) updateSlashMenu(msg tea.KeyPressMsg, matches []slashCommand) (tea.Model, tea.Cmd, bool) {
	t := &m.tocli
	selected := matches[min(t.slashCursor, len(matches)-1)]
	switch msg.String() {
	case "up":
		t.slashCursor = max(min(t.slashCursor, len(matches)-1)-1, 0)
	case "down":
		t.slashCursor = min(t.slashCursor+1, len(matches)-1)
	case "tab":
		t.editor.reset()
		t.editor.insert(selected.name)
	case "esc":
		t.editor.reset()
		t.slashCursor = 0
	case "enter":
		t.editor.reset()
		t.slashCursor = 0
		model, cmd := m.runSlash(selected.name)
		return model, cmd, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m *Model) runSlash(name string) (tea.Model, tea.Cmd) {
	t := &m.tocli
	t.exitArmed, t.scroll = false, 0
	m.setEditorStatus("", false)
	switch name {
	case "/help":
		m.addEntry(chatUser, name)
		m.addEntry(chatInfo, m.slashHelp())
	case "/new", "/clear":
		m.slashNew()
	case "/usage":
		m.slashUsage()
	case "/exit":
		m.closeChat()
		m.screen = screenToCLIForm
		return m, setWin32Input(false)
	}
	return m, nil
}

// slashNew forgets the conversation on this screen. The provider keeps its
// own copy, and the session's folder and permission server stay as they are.
func (m *Model) slashNew() {
	t := &m.tocli
	if t.running || t.sending {
		m.setEditorStatus(waitForReplyNote, false)
		return
	}
	t.entries, t.session = nil, ""
}

func (m *Model) slashHelp() string {
	var b strings.Builder
	b.WriteString("Perintah:\n")
	for _, command := range slashCommands {
		fmt.Fprintf(&b, "  %-16s %s\n", command.name, command.description)
	}
	b.WriteString("Teks lain yang diawali / diteruskan ke " + m.currentProvider().Label() + ".\n\n")
	return b.String() + slashHelpKeys
}

// slashUsage asks the CLI directly. The result carries no conversation, so
// the session being resumed by later prompts stays as it is.
func (m *Model) slashUsage() {
	t := &m.tocli
	if t.running || t.sending {
		m.setEditorStatus(waitForReplyNote, false)
		return
	}
	provider := m.currentProvider()
	m.addEntry(chatUser, "/usage")
	if provider.UsageArgs() == nil {
		m.addEntry(chatInfo, provider.Label()+" tidak menyediakan sisa kuota di mode ini. Lihat langsung di CLI-nya.")
		return
	}
	turnCtx, stop := context.WithCancel(t.chatCtx)
	cmd := tocli.BuildUsage(turnCtx, provider, t.exe, t.dir)
	t.stop, t.turnCtx = stop, turnCtx
	m.startTurn(cmd, "", tocli.WithoutSession(provider.NewParser()))
}

func (m *Model) slashMenu(matches []slashCommand) string {
	var b strings.Builder
	selected := min(m.tocli.slashCursor, len(matches)-1)
	for i, command := range matches {
		marker := "  "
		if i == selected {
			marker = "> "
		}
		b.WriteString(fmt.Sprintf("%s%-8s", marker, command.name) + dimStyle.Render(command.description) + "\n")
	}
	return b.String()
}
