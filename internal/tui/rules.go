package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"8memory/internal/gitinfo"
	"8memory/internal/store"
)

var ruleScopes = []struct{ value, label string }{
	{store.ScopeGlobal, "global"},
	{store.ScopeProject, "project"},
	{store.ScopeBranch, "branch"},
}

func ruleScopeIndex(scope string) int {
	for i, option := range ruleScopes {
		if option.value == scope {
			return i
		}
	}
	return 0
}

// Rule form fields. The scope selector is not a text input.
const (
	ruleFieldScope = iota
	ruleFieldProject
	ruleFieldBranch
	ruleFieldText
)

type rulesState struct {
	list          []store.Rule
	cursor        int
	loading       bool
	err           string
	confirmDelete bool

	editing *store.Rule
	scope   int
	focus   int
	inputs  []textinput.Model // project, branch, text
	formErr string
}

type (
	rulesLoadedMsg struct {
		rules []store.Rule
		err   error
	}
	ruleChangedMsg struct{ err error }
)

// useStore runs fn against a short-lived connection in the background.
func (m *Model) useStore(fn func(ctx context.Context, s store.Store) tea.Msg, onError func(error) tea.Msg) tea.Cmd {
	cfg, parent := m.current, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, connectTimeout)
		defer cancel()
		s, err := store.Open(ctx, cfg)
		if err != nil {
			return onError(err)
		}
		defer s.Close()
		return fn(ctx, s)
	}
}

func (m *Model) loadRules() tea.Cmd {
	m.rules.loading, m.rules.err = true, ""
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		rules, err := s.ListRules(ctx)
		return rulesLoadedMsg{rules: rules, err: err}
	}, func(err error) tea.Msg { return rulesLoadedMsg{err: err} })
}

func (m *Model) openRules() (tea.Model, tea.Cmd) {
	m.rules = rulesState{}
	m.screen = screenRules
	return m, m.loadRules()
}

func (m *Model) handleRulesLoaded(msg rulesLoadedMsg) (tea.Model, tea.Cmd) {
	m.rules.loading = false
	if msg.err != nil {
		m.rules.err = msg.err.Error()
		return m, nil
	}
	m.rules.list = msg.rules
	m.rules.cursor = min(m.rules.cursor, max(len(msg.rules)-1, 0))
	return m, nil
}

func (m *Model) handleRuleChanged(msg ruleChangedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.screen == screenRuleForm {
			m.rules.formErr = msg.err.Error()
		} else {
			m.rules.err = msg.err.Error()
		}
		return m, nil
	}
	m.screen = screenRules
	return m, m.loadRules()
}

func (m *Model) updateRules(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r := &m.rules
	if r.confirmDelete {
		r.confirmDelete = false
		if msg.String() != "y" || r.cursor >= len(r.list) {
			return m, nil
		}
		id := r.list[r.cursor].ID
		return m, m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
			return ruleChangedMsg{err: s.DeleteRule(ctx, id)}
		}, func(err error) tea.Msg { return ruleChangedMsg{err: err} })
	}
	switch msg.String() {
	case "up", "k":
		if r.cursor > 0 {
			r.cursor--
		}
	case "down", "j":
		if r.cursor < len(r.list)-1 {
			r.cursor++
		}
	case "a":
		return m, m.openRuleForm(nil)
	case "enter", "e":
		if r.cursor < len(r.list) {
			rule := r.list[r.cursor]
			return m, m.openRuleForm(&rule)
		}
	case "d":
		r.confirmDelete = len(r.list) > 0
	case "esc", "q":
		m.screen = screenMenu
	}
	return m, nil
}

// openRuleForm prepares the form. A new rule starts from the current git
// location, since that is usually what it should apply to.
func (m *Model) openRuleForm(existing *store.Rule) tea.Cmd {
	r := &m.rules
	r.editing, r.focus, r.formErr = existing, ruleFieldScope, ""
	var project, branch, text string
	if existing != nil {
		r.scope = ruleScopeIndex(existing.Scope)
		project, branch, text = existing.Project, existing.Branch, existing.Text
	} else {
		r.scope = 0
		if wd, err := os.Getwd(); err == nil {
			here, _ := gitinfo.Detect(m.ctx, wd)
			project, branch = here.Project, here.Branch
		}
	}
	r.inputs = []textinput.Model{newInput(project, ""), newInput(branch, ""), newInput(text, "write a rule")}
	r.inputs[2].SetWidth(60)
	m.screen = screenRuleForm
	return nil
}

func (m *Model) moveRuleFocus(delta int) {
	r := &m.rules
	if r.focus > ruleFieldScope {
		r.inputs[r.focus-1].Blur()
	}
	r.focus = (r.focus + delta + 4) % 4
	if r.focus > ruleFieldScope {
		r.inputs[r.focus-1].Focus()
	}
}

func (m *Model) updateRuleForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r := &m.rules
	switch msg.String() {
	case "esc":
		m.screen = screenRules
		return m, nil
	case "tab", "down":
		m.moveRuleFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.moveRuleFocus(-1)
		return m, nil
	case "ctrl+s":
		return m, m.saveRule()
	case "enter":
		if r.focus == ruleFieldText {
			return m, m.saveRule()
		}
		m.moveRuleFocus(1)
		return m, nil
	}
	if r.focus == ruleFieldScope {
		switch msg.String() {
		case "left", "h":
			r.scope = (r.scope + len(ruleScopes) - 1) % len(ruleScopes)
		case "right", "l", "space":
			r.scope = (r.scope + 1) % len(ruleScopes)
		}
		return m, nil
	}
	var cmd tea.Cmd
	r.inputs[r.focus-1], cmd = r.inputs[r.focus-1].Update(msg)
	return m, cmd
}

func (m *Model) saveRule() tea.Cmd {
	r := &m.rules
	rule := store.Rule{
		Scope:   ruleScopes[r.scope].value,
		Project: r.inputs[0].Value(),
		Branch:  r.inputs[1].Value(),
		Text:    r.inputs[2].Value(),
	}
	if r.editing != nil {
		rule.ID, rule.CreatedAt = r.editing.ID, r.editing.CreatedAt
	}
	isEdit := r.editing != nil
	return m.useStore(func(ctx context.Context, s store.Store) tea.Msg {
		var err error
		if isEdit {
			_, err = s.UpdateRule(ctx, rule)
		} else {
			_, err = s.AddRule(ctx, rule)
		}
		return ruleChangedMsg{err: err}
	}, func(err error) tea.Msg { return ruleChangedMsg{err: err} })
}

func ruleLocation(rule store.Rule) string {
	switch rule.Scope {
	case store.ScopeProject:
		return rule.Project
	case store.ScopeBranch:
		return rule.Project + " @ " + rule.Branch
	}
	return "global"
}

func (m *Model) viewRules(b *strings.Builder) {
	r := &m.rules
	b.WriteString("Rules\n\n")
	switch {
	case r.loading:
		b.WriteString("Loading...\n")
	case r.err != "":
		b.WriteString(errorStyle.Width(m.viewWidth()-2).Render(r.err) + "\n")
	case len(r.list) == 0:
		b.WriteString(dimStyle.Render("No rules yet.") + "\n")
	default:
		height := m.bodyHeight()
		start := max(r.cursor-height+1, 0)
		width := m.viewWidth() - 4
		for i := start; i < min(start+height, len(r.list)); i++ {
			rule := r.list[i]
			marker := "  "
			if i == r.cursor {
				marker = "> "
			}
			text := fmt.Sprintf("[%s] %s", ruleLocation(rule), strings.Join(strings.Fields(rule.Text), " "))
			b.WriteString(marker + runewidth.Truncate(text, width, "…") + "\n")
		}
	}
	b.WriteString("\n")
	if r.confirmDelete {
		b.WriteString(errorStyle.Render("Delete this rule? y yes · any other key cancels") + "\n")
		return
	}
	b.WriteString(dimStyle.Render("↑/↓ select · a add · enter edit · d delete · esc back") + "\n")
}

func (m *Model) viewRuleForm(b *strings.Builder) {
	r := &m.rules
	if r.editing != nil {
		b.WriteString("Edit rule\n\n")
	} else {
		b.WriteString("Add rule\n\n")
	}
	marker := func(field int) string {
		if r.focus == field {
			return "> "
		}
		return "  "
	}
	var scopes []string
	for i, option := range ruleScopes {
		label := option.label
		if i == r.scope {
			label = "[" + label + "]"
		}
		scopes = append(scopes, label)
	}
	fmt.Fprintf(b, "%s%-9s %s\n", marker(ruleFieldScope), "Scope", strings.Join(scopes, " "))
	fmt.Fprintf(b, "%s%-9s %s\n", marker(ruleFieldProject), "Project", r.inputs[0].View())
	fmt.Fprintf(b, "%s%-9s %s\n", marker(ruleFieldBranch), "Branch", r.inputs[1].View())
	fmt.Fprintf(b, "%s%-9s %s\n", marker(ruleFieldText), "Rule", r.inputs[2].View())
	if r.formErr != "" {
		b.WriteString("\n" + errorStyle.Width(m.viewWidth()-2).Render(r.formErr) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("tab next · ←/→ change scope · ctrl+s save · esc back") + "\n")
}
