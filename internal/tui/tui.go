// Package tui is the terminal app for browsing the journey and memories,
// switching the storage backend, and carrying existing data over to it.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"8memory/internal/config"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

const connectTimeout = 20 * time.Second

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

type screen int

const (
	screenMenu screen = iota
	screenList
	screenDetail
	screenPick
	screenForm
	screenBusy
	screenConfirmCopy
	screenDone
	screenRules
	screenRuleForm
	screenToCLIForm
	screenProviderPick
	screenEditor
	screenEffortPick
	screenAttachments
	screenAttachPreview
	screenCLIPreview
)

type backendOption struct {
	backend     config.Backend
	label, note string
	port        int
	database    string
}

var backendOptions = []backendOption{
	{backend: config.SQLite, label: "SQLite", note: "local file, no server, FTS5 search"},
	{backend: config.Postgres, label: "PostgreSQL", note: "server, full-text with relevance ranking", port: 5432, database: "postgres"},
	{backend: config.SQLServer, label: "SQL Server", note: "server, LIKE search without ranking", port: 1433, database: "master"},
	{backend: config.Oracle, label: "Oracle", note: "server, LIKE search without ranking", port: 1521, database: "FREEPDB1"},
}

// Field indexes in the network form.
const (
	fieldHost = iota
	fieldPort
	fieldUser
	fieldPassword
	fieldDatabase
	fieldOptions
)

// Messages delivered by background commands.
type (
	testedMsg struct{ err error }
	// preparedMsg reports that the new target is ready, and how many memories
	// the current backend holds. sourceErr is non-fatal: it means the current
	// backend could not be read, so nothing can be offered for copying.
	preparedMsg struct {
		memories  int
		nodes     int
		rules     int
		sourceErr error
		err       error
	}
	savedMsg struct {
		copied store.CopyResult
		err    error
	}
)

// Model is the Bubble Tea state of the TUI.
type Model struct {
	ctx        context.Context
	cancel     context.CancelFunc
	configPath string
	current    config.Config
	draft      config.Config

	screen       screen
	menu         int
	width        int
	height       int
	browser      browser
	parent       *browser // the journey list while viewing one node's memories
	detail       *store.Memory
	detailOffset int
	cursor       int
	option       backendOption
	inputs       []textinput.Model
	labels       []string
	focus        int
	busyLabel    string
	status       string
	statusErr    bool
	memories     int
	nodes        int
	warning      string
	ruleCount    int
	notice       string
	noticeErr    bool
	rules        rulesState
	tocli        tocliState
	listen       func(chan tocliInboxMsg, context.Context) tea.Cmd
	runTurn      turnRunner
}

// New creates the TUI model for the given config.
func New(configPath string, current config.Config) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{ctx: ctx, cancel: cancel, configPath: configPath, current: current, listen: listenInbox, runTurn: tocli.Run}
	for i, option := range backendOptions {
		if option.backend == current.Backend {
			m.cursor = i
		}
	}
	return m
}

// Run shows the TUI until the user exits.
func Run(configPath string) error {
	current, err := config.Load(configPath)
	if err != nil {
		return err
	}
	model := New(configPath, current)
	_, err = tea.NewProgram(model).Run()
	model.shutdown()
	// The editor may be open when the program ends.
	if runtime.GOOS == "windows" {
		os.Stdout.WriteString(win32InputOff)
	}
	return err
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		return m.handleLoaded(msg)
	case tea.PasteMsg:
		return m.handlePaste(msg)
	case rulesLoadedMsg:
		return m.handleRulesLoaded(msg)
	case ruleChangedMsg:
		return m.handleRuleChanged(msg)
	case tocliReadyMsg:
		return m.handleToCLIReady(msg)
	case tocliImageMsg:
		return m.handleImage(msg)
	case tocliPlanMsg:
		return m.handlePlan(msg)
	case tocliLaunchMsg:
		return m.handleLaunch(msg)
	case tocliInboxMsg:
		return m.handleInbox(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if m.screen == screenEditor && m.tocli.running {
				m.stopTurn()
				return m, nil
			}
			m.cancel()
			return m, tea.Quit
		}
		switch m.screen {
		case screenMenu:
			return m.updateMenu(msg)
		case screenList:
			return m.updateList(msg)
		case screenDetail:
			return m.updateDetail(msg)
		case screenRules:
			return m.updateRules(msg)
		case screenRuleForm:
			return m.updateRuleForm(msg)
		case screenToCLIForm:
			return m.updateToCLIForm(msg)
		case screenProviderPick:
			return m.updateProviderPick(msg)
		case screenEditor:
			return m.updateEditor(msg)
		case screenEffortPick:
			return m.updateEffortPick(msg)
		case screenAttachments:
			return m.updateAttachments(msg)
		case screenAttachPreview:
			return m.updateAttachPreview(msg)
		case screenCLIPreview:
			return m.updateCLIPreview(msg)
		case screenPick:
			return m.updatePick(msg)
		case screenForm:
			return m.updateForm(msg)
		case screenConfirmCopy:
			return m.updateConfirm(msg)
		case screenDone:
			m.screen = screenMenu
		}
	case testedMsg:
		m.screen = screenForm
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
		} else {
			m.setStatus("Connection successful.", false)
		}
	case preparedMsg:
		return m.handlePrepared(msg)
	case savedMsg:
		if msg.err != nil {
			m.screen = screenForm
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		m.current = m.draft
		m.screen = screenDone
		m.setStatus(fmt.Sprintf("Copied %d new memories, %d nodes and %d rules.", msg.copied.Memories, msg.copied.Nodes, msg.copied.Rules), false)
	}
	return m, nil
}

type menuItem struct{ label, description string }

var menuItems = []menuItem{
	{"Journey", "Timeline of every prompt"},
	{"Memory", "Search what the AI recorded"},
	{"Rules", "Instructions sent with every prompt"},
	{"Chat", "Talk to the AI through Claude Code or Codex"},
	{"Storage", "Choose the database backend"},
}

const (
	menuJourney = iota
	menuMemory
	menuRules
	menuToCLI
	menuStorage
)

func (m *Model) updateMenu(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.menu > 0 {
			m.menu--
		}
	case "down", "j":
		if m.menu < len(menuItems)-1 {
			m.menu++
		}
	case "enter":
		m.notice = ""
		switch m.menu {
		case menuJourney:
			return m.openList(listJourney)
		case menuMemory:
			return m.openList(listMemory)
		case menuRules:
			return m.openRules()
		case menuToCLI:
			return m.openToCLI()
		case menuStorage:
			m.screen = screenPick
		}
	case "q", "esc":
		m.cancel()
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) updatePick(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(backendOptions)-1 {
			m.cursor++
		}
	case "enter":
		m.openForm(backendOptions[m.cursor])
	case "q", "esc":
		m.screen = screenMenu
	}
	return m, nil
}

// openForm builds the inputs, reusing saved values when the backend is the
// one already active.
func (m *Model) openForm(option backendOption) {
	m.option, m.focus, m.status = option, 0, ""
	saved := config.Config{Backend: option.backend, Port: option.port, Database: option.database, Host: "localhost"}
	if option.backend == m.current.Backend {
		saved = m.current
	}

	if option.backend == config.SQLite {
		if saved.Path == "" {
			saved.Path = config.Default(m.configPath).Path
		}
		m.labels = []string{"File path"}
		m.inputs = []textinput.Model{newInput(saved.Path, "")}
	} else {
		databaseLabel := "Database"
		if option.backend == config.Oracle {
			databaseLabel = "Service name"
		}
		m.labels = []string{"Host", "Port", "User", "Password", databaseLabel, "Options"}
		m.inputs = []textinput.Model{
			newInput(saved.Host, ""),
			newInput(strconv.Itoa(saved.Port), ""),
			newInput(saved.User, ""),
			newInput(saved.Password, ""),
			newInput(saved.Database, ""),
			newInput(saved.Options, optionsHint(option.backend)),
		}
		m.inputs[fieldPassword].EchoMode = textinput.EchoPassword
		m.inputs[fieldPassword].EchoCharacter = '•'
	}
	m.inputs[0].Focus()
	m.screen = screenForm
}

func newInput(value, placeholder string) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.SetWidth(48)
	input.SetValue(value)
	input.Placeholder = placeholder
	styles := input.Styles()
	styles.Cursor.Blink = false // a blink timer would redraw the screen forever
	input.SetStyles(styles)
	return input
}

func optionsHint(backend config.Backend) string {
	switch backend {
	case config.Postgres:
		return "sslmode=require"
	case config.SQLServer:
		return "encrypt=true&trustservercertificate=false"
	case config.Oracle:
		return "SSL=true"
	}
	return ""
}

func (m *Model) updateForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenPick
		return m, nil
	case "tab", "down":
		m.moveFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.moveFocus(-1)
		return m, nil
	case "ctrl+t":
		return m.startTest()
	case "ctrl+s":
		return m.startSave()
	case "enter":
		if m.focus == len(m.inputs)-1 {
			return m.startSave()
		}
		m.moveFocus(1)
		return m, nil
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *Model) moveFocus(delta int) {
	m.inputs[m.focus].Blur()
	m.focus = (m.focus + delta + len(m.inputs)) % len(m.inputs)
	m.inputs[m.focus].Focus()
}

// readForm converts the inputs to a config and validates it.
func (m *Model) readForm() (config.Config, error) {
	value := func(i int) string { return strings.TrimSpace(m.inputs[i].Value()) }
	// Aliases are per-machine settings, unrelated to the storage being chosen.
	cfg := config.Config{Backend: m.option.backend, Aliases: m.current.Aliases, PasteLines: m.current.PasteLines, PasteBytes: m.current.PasteBytes}

	if m.option.backend == config.SQLite {
		path, err := absolutePath(value(0))
		if err != nil {
			return config.Config{}, err
		}
		cfg.Path = path
		return cfg, cfg.Validate()
	}

	port, err := strconv.Atoi(value(fieldPort))
	if err != nil {
		return config.Config{}, errors.New("port must be a number")
	}
	cfg.Host, cfg.Port, cfg.User, cfg.Database, cfg.Options =
		value(fieldHost), port, value(fieldUser), value(fieldDatabase), value(fieldOptions)
	cfg.Password = m.inputs[fieldPassword].Value() // spaces can be part of a password
	return cfg, cfg.Validate()
}

// absolutePath expands ~ and anchors relative paths, because MCP clients start
// the server from arbitrary working directories.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	return filepath.Abs(path)
}

func (m *Model) setStatus(text string, isError bool) {
	m.status, m.statusErr = text, isError
}

func (m *Model) startTest() (tea.Model, tea.Cmd) {
	cfg, err := m.readForm()
	if err != nil {
		m.setStatus(err.Error(), true)
		return m, nil
	}
	m.draft, m.screen, m.busyLabel = cfg, screenBusy, "Testing connection..."
	ctx := m.ctx
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		return testedMsg{err: store.Ping(ctx, cfg)}
	}
}

func (m *Model) startSave() (tea.Model, tea.Cmd) {
	cfg, err := m.readForm()
	if err != nil {
		m.setStatus(err.Error(), true)
		return m, nil
	}
	m.draft, m.screen, m.busyLabel = cfg, screenBusy, "Preparing storage..."
	ctx, current := m.ctx, m.current
	return m, func() tea.Msg { return prepare(ctx, current, cfg) }
}

// prepare creates the schema on the new target, then counts what the current
// backend holds so the user can decide whether to copy it.
func prepare(ctx context.Context, current, target config.Config) preparedMsg {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	dst, err := store.Open(ctx, target)
	if err != nil {
		return preparedMsg{err: err}
	}
	dst.Close()

	if current.Describe() == target.Describe() {
		return preparedMsg{}
	}
	src, err := store.Open(ctx, current)
	if err != nil {
		return preparedMsg{sourceErr: err}
	}
	defer src.Close()
	memories, err := src.Count(ctx)
	if err != nil {
		return preparedMsg{sourceErr: err}
	}
	nodes, err := src.CountNodes(ctx)
	if err != nil {
		return preparedMsg{sourceErr: err}
	}
	rules, err := src.CountRules(ctx)
	if err != nil {
		return preparedMsg{sourceErr: err}
	}
	return preparedMsg{memories: memories, nodes: nodes, rules: rules}
}

func (m *Model) handlePrepared(msg preparedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenForm
		m.setStatus(msg.err.Error(), true)
		return m, nil
	}
	m.warning = ""
	if msg.sourceErr != nil {
		m.warning = "The old backend could not be read, so no memories were copied: " + msg.sourceErr.Error()
	}
	if msg.memories+msg.nodes+msg.rules > 0 {
		m.memories, m.nodes, m.ruleCount, m.screen = msg.memories, msg.nodes, msg.rules, screenConfirmCopy
		return m, nil
	}
	return m.finish(false)
}

func (m *Model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		return m.finish(true)
	case "n":
		return m.finish(false)
	case "esc":
		m.screen = screenForm
	}
	return m, nil
}

// finish saves the config only after any copy succeeded, so a failed copy
// leaves every client on the backend that still has the data.
func (m *Model) finish(copyData bool) (tea.Model, tea.Cmd) {
	m.screen, m.busyLabel = screenBusy, "Saving..."
	if copyData {
		m.busyLabel = fmt.Sprintf("Copying %d memories, %d nodes and %d rules...", m.memories, m.nodes, m.ruleCount)
	}
	ctx, source, target, path := m.ctx, m.current, m.draft, m.configPath
	return m, func() tea.Msg {
		var copied store.CopyResult
		if copyData {
			var err error
			if copied, err = copyAll(ctx, source, target); err != nil {
				return savedMsg{err: fmt.Errorf("copy failed, configuration unchanged: %w", err)}
			}
		}
		return savedMsg{copied: copied, err: config.Save(path, target)}
	}
}

func copyAll(ctx context.Context, source, target config.Config) (store.CopyResult, error) {
	src, err := store.Open(ctx, source)
	if err != nil {
		return store.CopyResult{}, err
	}
	defer src.Close()
	dst, err := store.Open(ctx, target)
	if err != nil {
		return store.CopyResult{}, err
	}
	defer dst.Close()
	return store.Copy(ctx, src, dst)
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

func (m *Model) render() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("8memory") + "\n")
	b.WriteString(dimStyle.Render("Active: "+m.current.Describe()) + "\n\n")

	switch m.screen {
	case screenMenu:
		m.viewMenu(&b)
	case screenList:
		m.viewList(&b)
	case screenDetail:
		m.viewDetail(&b)
	case screenRules:
		m.viewRules(&b)
	case screenRuleForm:
		m.viewRuleForm(&b)
	case screenToCLIForm:
		m.viewToCLIForm(&b)
	case screenProviderPick:
		m.viewProviderPick(&b)
	case screenEditor:
		m.viewEditor(&b)
	case screenEffortPick:
		m.viewEffortPick(&b)
	case screenAttachments:
		m.viewAttachments(&b)
	case screenAttachPreview:
		m.viewAttachPreview(&b)
	case screenCLIPreview:
		m.viewCLIPreview(&b)
	case screenPick:
		m.viewPick(&b)
	case screenForm:
		m.viewForm(&b)
	case screenBusy:
		b.WriteString(m.busyLabel + "\n")
	case screenConfirmCopy:
		fmt.Fprintf(&b, "The active backend has %d memories, %d journey nodes and %d rules.\nCopy to %s?\n\n", m.memories, m.nodes, m.ruleCount, m.draft.Describe())
		b.WriteString(dimStyle.Render("y copy · n switch without copying · esc back") + "\n")
	case screenDone:
		b.WriteString(okStyle.Render("Saved.") + " " + m.status + "\n")
		if m.warning != "" {
			b.WriteString(errorStyle.Render(m.warning) + "\n")
		}
		b.WriteString("Restart any running AI client so it uses the new backend.\n\n")
		b.WriteString(dimStyle.Render("press any key to return to the menu") + "\n")
	}
	return b.String()
}

func (m *Model) viewMenu(b *strings.Builder) {
	const labelWidth = 9
	room := max(m.viewWidth()-labelWidth-4, 0)
	for i, item := range menuItems {
		marker, label := "  ", fmt.Sprintf("%-*s", labelWidth, item.label)
		if i == m.menu {
			marker, label = "> ", titleStyle.Render(label)
		}
		b.WriteString(marker + label + " " + dimStyle.Render(runewidth.Truncate(item.description, room, "…")) + "\n")
	}
	if m.notice != "" {
		style := okStyle
		if m.noticeErr {
			style = errorStyle
		}
		b.WriteString("\n" + style.Width(m.viewWidth()-2).Render(m.notice) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ select · enter open · q quit") + "\n")
}

func (m *Model) viewPick(b *strings.Builder) {
	for i, option := range backendOptions {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		fmt.Fprintf(b, "%s%-12s %s\n", marker, option.label, dimStyle.Render(option.note))
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ select · enter continue · esc back") + "\n")
}

func (m *Model) viewForm(b *strings.Builder) {
	b.WriteString(m.option.label + "\n\n")
	for i, input := range m.inputs {
		marker := "  "
		if i == m.focus {
			marker = "> "
		}
		fmt.Fprintf(b, "%s%-13s %s\n", marker, m.labels[i], input.View())
	}
	if m.status != "" {
		style := okStyle
		if m.statusErr {
			style = errorStyle
		}
		b.WriteString("\n" + style.Width(76).Render(m.status) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("tab next · ctrl+t test connection · ctrl+s save · esc back") + "\n")
}
