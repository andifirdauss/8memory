package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"8memory/internal/refs"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

const (
	listLimit     = 500
	defaultWidth  = 80
	defaultHeight = 24
	dateLayout    = "2006-01-02"
	rowTimeLayout = "01-02 15:04"
	fullTime      = "2006-01-02 15:04:05"
	emptyLabel    = "-"
)

type listKind int

const (
	listJourney listKind = iota
	listMemory
)

type period struct {
	label string
	span  time.Duration
}

var periods = []period{
	{"1 day", 24 * time.Hour},
	{"7 days", 7 * 24 * time.Hour},
	{"1 month", 30 * 24 * time.Hour},
	{"all", 0},
}

const periodAll = 3

// entry is the part of a node or memory that lists group and sort on.
type entry struct {
	project, branch, agent string
	at                     time.Time
	node                   *store.Node
	memory                 *store.Memory
}

func orDash(s string) string {
	if s == "" {
		return emptyLabel
	}
	return s
}

var groupings = []struct {
	label string
	key   func(entry) string
}{
	{"project/branch", func(e entry) string { return orDash(e.project) + " @ " + orDash(e.branch) }},
	{"project", func(e entry) string { return orDash(e.project) }},
	{"branch", func(e entry) string { return orDash(e.branch) }},
	{"day", func(e entry) string { return e.at.Local().Format(dateLayout) }},
	{"agent", func(e entry) string { return orDash(e.agent) }},
}

var changeFilters = []struct{ value, label string }{
	{"", "all"},
	{store.ChangeAdd, "add"},
	{store.ChangeRemove, "remove"},
	{store.ChangeModify, "modify"},
	{store.ChangeInspect, "inspect"},
}

func changeLabel(change string) string {
	for _, filter := range changeFilters[1:] {
		if filter.value == change {
			return filter.label
		}
	}
	return emptyLabel
}

// row is a line in a list: a group header when entry is nil.
type row struct {
	header string
	entry  *entry
}

// browser is the state of one list screen.
type browser struct {
	kind                  listKind
	period, group, change int
	node                  *store.Node // limits a memory list to one journey node
	query                 string
	searching             bool
	search                textinput.Model
	entries               []entry
	rows                  []row
	cursor, offset        int
	loading               bool
	loadErr               string
	seq                   int
	confirmRemove         bool
	notice                string
}

type loadedMsg struct {
	seq     int
	entries []entry
	err     error
}

func (m *Model) openList(kind listKind) (tea.Model, tea.Cmd) {
	m.browser = browser{kind: kind}
	m.parent = nil
	m.screen = screenList
	return m, m.load()
}

// load fetches the list in the background. The sequence number lets a stale
// response be discarded when the filters changed while it was running.
func (m *Model) load() tea.Cmd {
	m.browser.seq++
	m.browser.loading = true
	m.browser.loadErr = ""
	seq, b, cfg, ctx := m.browser.seq, m.browser, m.current, m.ctx

	var since time.Time
	if span := periods[b.period].span; span > 0 {
		since = time.Now().Add(-span)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		s, err := store.Open(ctx, cfg)
		if err != nil {
			return loadedMsg{seq: seq, err: err}
		}
		defer s.Close()

		if b.kind == listJourney {
			nodes, err := s.ListNodes(ctx, store.NodeQuery{Since: since, Limit: listLimit})
			return loadedMsg{seq: seq, entries: nodeEntries(nodes), err: err}
		}
		q := store.Query{Text: b.query, Change: changeFilters[b.change].value, Since: since, Limit: listLimit}
		if b.node != nil {
			q.NodeID = b.node.ID
		}
		memories, err := s.Search(ctx, q)
		return loadedMsg{seq: seq, entries: memoryEntries(memories), err: err}
	}
}

func nodeEntries(nodes []store.Node) []entry {
	out := make([]entry, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		out[i] = entry{project: n.Project, branch: n.Branch, agent: n.Agent, at: n.CreatedAt, node: n}
	}
	return out
}

func memoryEntries(memories []store.Memory) []entry {
	out := make([]entry, len(memories))
	for i := range memories {
		mem := &memories[i]
		out[i] = entry{project: mem.Project, branch: mem.Branch, agent: mem.Agent, at: mem.CreatedAt, memory: mem}
	}
	return out
}

func (m *Model) handleLoaded(msg loadedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.browser.seq {
		return m, nil
	}
	b := &m.browser
	b.loading = false
	if msg.err != nil {
		b.loadErr = msg.err.Error()
		b.entries, b.rows = nil, nil
		return m, nil
	}
	b.entries = msg.entries
	b.rebuildRows()
	return m, nil
}

// rebuildRows groups entries under headers, keeping groups in the order they
// first appear so the most recent activity stays on top.
func (b *browser) rebuildRows() {
	key := groupings[b.group].key
	var order []string
	groups := map[string][]*entry{}
	for i := range b.entries {
		k := key(b.entries[i])
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], &b.entries[i])
	}
	b.rows = b.rows[:0]
	for _, k := range order {
		b.rows = append(b.rows, row{header: fmt.Sprintf("%s (%d)", k, len(groups[k]))})
		for _, e := range groups[k] {
			b.rows = append(b.rows, row{entry: e})
		}
	}
	b.cursor, b.offset = 0, 0
	b.moveCursor(1)
}

// moveCursor steps to the next selectable row in the given direction,
// staying put when there is none.
func (b *browser) moveCursor(delta int) {
	for i := b.cursor + delta; i >= 0 && i < len(b.rows); i += delta {
		if b.rows[i].entry != nil {
			b.cursor = i
			return
		}
	}
}

func (b *browser) selected() *entry {
	if b.cursor < len(b.rows) {
		return b.rows[b.cursor].entry
	}
	return nil
}

func (m *Model) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := &m.browser
	if b.searching {
		return m.updateSearch(msg)
	}
	b.notice = ""
	if b.confirmRemove {
		b.confirmRemove = false
		if msg.String() == "y" {
			m.removeSelectedAttachments()
		}
		return m, nil
	}
	switch msg.String() {
	case "x":
		if selected := b.selected(); b.kind == listJourney && selected != nil {
			if tocli.HasSaved(m.attachmentsDir(), selected.node.ID) {
				b.confirmRemove = true
			} else {
				b.notice = "This node has no attachments."
			}
		}
	case "up", "k":
		b.moveCursor(-1)
	case "down", "j":
		b.moveCursor(1)
	case "p":
		if b.node == nil {
			b.period = (b.period + 1) % len(periods)
			return m, m.load()
		}
	case "g":
		b.group = (b.group + 1) % len(groupings)
		b.rebuildRows()
	case "c":
		if b.kind == listMemory {
			b.change = (b.change + 1) % len(changeFilters)
			return m, m.load()
		}
	case "/":
		if b.kind == listMemory {
			b.searching = true
			b.search = newInput(b.query, "keyword")
			b.search.Focus()
		}
	case "r":
		return m, m.load()
	case "enter":
		return m.openSelected()
	case "esc", "q":
		if m.parent != nil {
			m.browser, m.parent = *m.parent, nil
			return m, nil
		}
		m.screen = screenMenu
	}
	return m, nil
}

func (m *Model) removeSelectedAttachments() {
	b := &m.browser
	selected := b.selected()
	if selected == nil {
		return
	}
	if err := tocli.RemoveSaved(m.attachmentsDir(), selected.node.ID); err != nil {
		b.notice = err.Error()
		return
	}
	b.notice = "Attachments deleted."
}

func (m *Model) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := &m.browser
	switch msg.String() {
	case "enter":
		b.query, b.searching = strings.TrimSpace(b.search.Value()), false
		return m, m.load()
	case "esc":
		b.searching = false
		return m, nil
	}
	var cmd tea.Cmd
	b.search, cmd = b.search.Update(msg)
	return m, cmd
}

// openSelected shows a node's memories or a memory's full text.
func (m *Model) openSelected() (tea.Model, tea.Cmd) {
	selected := m.browser.selected()
	switch {
	case selected == nil:
		return m, nil
	case selected.node != nil:
		saved := m.browser
		m.parent = &saved
		m.browser = browser{kind: listMemory, node: selected.node, period: periodAll}
		return m, m.load()
	}
	m.detail, m.detailOffset = selected.memory, 0
	m.screen = screenDetail
	return m, nil
}

func (m *Model) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.detailOffset > 0 {
			m.detailOffset--
		}
	case "down", "j":
		if m.detailOffset < len(m.detailLines())-1 {
			m.detailOffset++
		}
	case "esc", "q":
		m.screen = screenList
	}
	return m, nil
}

func (m *Model) detailLines() []string {
	mem := m.detail
	lines := []string{
		"Project   " + orDash(mem.Project),
		"Branch    " + orDash(mem.Branch),
		"Time      " + mem.CreatedAt.Local().Format(fullTime),
		"Agent     " + orDash(mem.Agent),
		"Change    " + changeLabel(mem.Change),
		"Feature   " + orDash(mem.Feature),
		"Tag       " + orDash(strings.Join(mem.Tags, ", ")),
		"Node      " + orDash(mem.NodeID),
		"",
	}
	for _, file := range mem.Files {
		lines = append(lines, m.fileLines(file)...)
		lines = append(lines, "")
	}
	body := lipgloss.NewStyle().Width(m.viewWidth() - 2).Render(mem.Content)
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return lines
}

// fileLines shows a reference with its location on this machine. Only the
// person at the terminal sees the local path; the AI gets the ref alone.
func (m *Model) fileLines(file store.FileRef) []string {
	location, err := refs.Resolve(file.Ref, m.current.Aliases)
	if err != nil {
		location = "alias unknown on this machine"
	}
	lines := []string{
		"File      " + file.Ref,
		"Location  " + location,
		fmt.Sprintf("Size      %d bytes", file.Size),
		"Kind      " + orDash(file.Kind),
		"From host " + orDash(file.Host),
	}
	if file.RestoreHint != "" {
		lines = append(lines, "Restore   "+file.RestoreHint)
	}
	return lines
}

func (m *Model) viewWidth() int {
	if m.width > 0 {
		return m.width
	}
	return defaultWidth
}

// bodyHeight is how many lines a scrolling area can use below the title and
// above the footer.
func (m *Model) bodyHeight() int {
	height := m.height
	if height == 0 {
		height = defaultHeight
	}
	return max(height-8, 3)
}

func (m *Model) viewList(b *strings.Builder) {
	state := &m.browser
	b.WriteString(m.listHeading() + "\n")
	if state.searching {
		b.WriteString("Search: " + state.search.View() + "\n")
	}
	b.WriteString("\n")

	switch {
	case state.loading:
		b.WriteString("Loading...\n")
	case state.loadErr != "":
		b.WriteString(errorStyle.Width(m.viewWidth()-2).Render(state.loadErr) + "\n")
	case len(state.rows) == 0:
		b.WriteString(dimStyle.Render("No data yet.") + "\n")
	default:
		m.viewRows(b)
	}
	if state.notice != "" {
		b.WriteString("\n" + okStyle.Render(state.notice))
	}
	if state.confirmRemove {
		b.WriteString("\n" + errorStyle.Render("Delete this node's attachments? y yes · any other key cancels") + "\n")
		return
	}
	b.WriteString("\n" + dimStyle.Render(m.listFooter()) + "\n")
}

func (m *Model) listHeading() string {
	state := &m.browser
	switch {
	case state.node != nil:
		return "Memories for: " + state.node.Title
	case state.kind == listJourney:
		return fmt.Sprintf("Journey · %s · group %s", periods[state.period].label, groupings[state.group].label)
	}
	heading := fmt.Sprintf("Memory · %s · group %s · change %s", periods[state.period].label, groupings[state.group].label, changeFilters[state.change].label)
	if state.query != "" {
		heading += fmt.Sprintf(" · search %q", state.query)
	}
	return heading
}

func (m *Model) listFooter() string {
	state := &m.browser
	if state.searching {
		return "enter search · esc cancel"
	}
	parts := []string{"↑/↓ select", "enter open", "g group"}
	if state.kind == listJourney {
		parts = append(parts, "x delete attachments")
	}
	if state.node == nil {
		parts = append(parts, "p period")
	}
	if state.kind == listMemory {
		parts = append(parts, "c change", "/ search")
	}
	parts = append(parts, "r reload", "esc back")
	footer := strings.Join(parts, " · ")
	if len(state.entries) >= listLimit {
		footer = fmt.Sprintf("only the latest %d · ", listLimit) + footer
	}
	return footer
}

func (m *Model) viewRows(b *strings.Builder) {
	state := &m.browser
	height := m.bodyHeight()
	if state.cursor < state.offset {
		state.offset = state.cursor
	}
	if state.cursor >= state.offset+height {
		state.offset = state.cursor - height + 1
	}
	// Keep the group header in view when the first entry is selected.
	if state.offset > 0 && state.offset == state.cursor && state.rows[state.offset-1].entry == nil {
		state.offset--
	}

	end := min(state.offset+height, len(state.rows))
	width := m.viewWidth() - 4
	for i := state.offset; i < end; i++ {
		current := state.rows[i]
		if current.entry == nil {
			b.WriteString(titleStyle.Render(runewidth.Truncate(current.header, width, "…")) + "\n")
			continue
		}
		marker := "  "
		if i == state.cursor {
			marker = "> "
		}
		b.WriteString(marker + runewidth.Truncate(rowText(current.entry), width, "…") + "\n")
	}
}

func rowText(e *entry) string {
	at := e.at.Local().Format(rowTimeLayout)
	if e.node != nil {
		text := at + "  " + e.node.Title
		if e.node.Agent != "" {
			text += " [" + e.node.Agent + "]"
		}
		return text
	}
	summary := strings.Join(strings.Fields(e.memory.Content), " ")
	if e.memory.Feature != "" {
		summary = e.memory.Feature + ": " + summary
	}
	return fmt.Sprintf("%s  [%-7s] %s", at, changeLabel(e.memory.Change), summary)
}

func (m *Model) viewDetail(b *strings.Builder) {
	lines := m.detailLines()
	start := min(m.detailOffset, len(lines)-1)
	end := min(start+m.bodyHeight()+1, len(lines))
	b.WriteString(strings.Join(lines[start:end], "\n") + "\n\n")
	b.WriteString(dimStyle.Render("↑/↓ scroll · esc back") + "\n")
}
