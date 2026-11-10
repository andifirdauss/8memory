package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"8memory/internal/tocli"
)

type slashCommand struct{ name, description string }

// slashCommands run in 8memory itself. Any other text starting with a slash
// goes to the provider's CLI like a normal prompt.
var slashCommands = []slashCommand{
	{"/help", "list commands and keys"},
	{"/new", "start a new conversation"},
	{"/clear", "clear the screen and start a new conversation"},
	{"/effort", "set the provider effort for this session"},
	{"/usage", "remaining provider quota, without using any"},
	{"/exit", "end this session"},
}

const slashHelpKeys = `Keys:
  enter            send prompt
  shift+enter      new line (also ctrl+j)
  alt+v            image from clipboard
  ctrl+o           add file
  ctrl+e           list attachments
  pgup, pgdn       scroll the conversation
  esc, ctrl+c      stop the running prompt
  esc twice        end the session`

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
	case "/effort":
		m.slashEffort()
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

// effortDefault leaves the effort to the CLI's own setting.
const effortDefault = "default"

func (m *Model) effortChoices() []string {
	return append([]string{effortDefault}, m.currentProvider().Efforts()...)
}

// effortArgument recognizes "/effort <level>". The menu only covers a single
// word, so without this the command would be sent to the CLI as a prompt.
func effortArgument(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) != 2 || fields[0] != "/effort" {
		return "", false
	}
	return strings.ToLower(fields[1]), true
}

// slashEffort opens the list of levels with the current one selected.
func (m *Model) slashEffort() {
	t := &m.tocli
	if t.running || t.sending {
		m.setEditorStatus(waitForReplyNote, false)
		return
	}
	t.effortCursor = max(slices.Index(m.effortChoices(), t.effort), 0)
	m.screen = screenEffortPick
}

// slashEffortLevel handles "/effort <level>". A wrong level stays in the
// editor so it can be corrected.
func (m *Model) slashEffortLevel(level string) {
	t := &m.tocli
	if t.running || t.sending {
		m.setEditorStatus(waitForReplyNote, false)
		return
	}
	choices := m.effortChoices()
	if !slices.Contains(choices, level) {
		m.setEditorStatus(fmt.Sprintf("Effort %q is not known to %s. Choices: %s.", level, m.currentProvider().Label(), strings.Join(choices, ", ")), true)
		return
	}
	t.editor.reset()
	m.setEffort(level)
}

// setEffort applies from the next prompt on, in the same conversation.
func (m *Model) setEffort(level string) {
	if level == effortDefault {
		m.tocli.effort = ""
		m.setEditorStatus("Effort reset to the default of "+m.currentProvider().Label()+".", false)
		return
	}
	m.tocli.effort = level
	m.setEditorStatus("Effort "+level+" applies from the next prompt.", false)
}

func (m *Model) updateEffortPick(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	t := &m.tocli
	choices := m.effortChoices()
	switch msg.String() {
	case "up", "k":
		t.effortCursor = max(t.effortCursor-1, 0)
	case "down", "j":
		t.effortCursor = min(t.effortCursor+1, len(choices)-1)
	case "enter":
		m.setEffort(choices[t.effortCursor])
		m.screen = screenEditor
	case "q", "esc":
		m.screen = screenEditor
	}
	return m, nil
}

func (m *Model) viewEffortPick(b *strings.Builder) {
	t := &m.tocli
	b.WriteString("Effort · " + m.currentProvider().Label() + "\n\n")
	for i, choice := range m.effortChoices() {
		marker := "  "
		if i == t.effortCursor {
			marker = "> "
		}
		if choice == effortDefault {
			choice += dimStyle.Render("  use the CLI setting")
		}
		b.WriteString(marker + choice + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ select · enter use · esc back") + "\n")
}

func (m *Model) slashHelp() string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, command := range slashCommands {
		fmt.Fprintf(&b, "  %-16s %s\n", command.name, command.description)
	}
	b.WriteString("Other text starting with / is passed to " + m.currentProvider().Label() + ".\n\n")
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
		m.addEntry(chatInfo, provider.Label()+" does not report remaining quota in this mode. Check its CLI directly.")
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
