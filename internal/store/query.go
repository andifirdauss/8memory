package store

import (
	"strings"
	"unicode"
)

// term is one search unit. A phrase must match its words in order; a prefix
// matches any word starting with text.
type term struct {
	text   string
	prefix bool
}

// parseQuery turns the portable query syntax into OR-ed groups of AND-ed
// terms: `auth "refresh token" OR sess*`. Every backend compiles this same
// structure, so a query means the same thing after switching storage, and
// quoting at compile time means no input can cause a syntax error.
func parseQuery(input string) [][]term {
	var groups [][]term
	var current []term
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
		}
	}

	rest := strings.TrimSpace(input)
	for rest != "" {
		var token string
		quoted := rest[0] == '"'
		if quoted {
			rest = rest[1:]
			if end := strings.IndexByte(rest, '"'); end >= 0 {
				token, rest = rest[:end], rest[end+1:]
			} else {
				token, rest = rest, ""
			}
		} else if end := strings.IndexFunc(rest, unicode.IsSpace); end >= 0 {
			token, rest = rest[:end], rest[end:]
		} else {
			token, rest = rest, ""
		}
		rest = strings.TrimSpace(rest)

		if !quoted && token == "OR" {
			flush()
			continue
		}
		t := term{text: strings.ToLower(strings.TrimSpace(token))}
		if !quoted && strings.HasSuffix(t.text, "*") {
			t.text, t.prefix = strings.TrimRight(t.text, "*"), true
		}
		// A term with no letters or digits has nothing to index.
		if strings.IndexFunc(t.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			current = append(current, t)
		}
	}
	flush()
	return groups
}

// compileBoolean renders groups as (a AND b) OR (c) using render for terms.
func compileBoolean(groups [][]term, and, or string, render func(term) string) string {
	parts := make([]string, len(groups))
	for i, group := range groups {
		rendered := make([]string, len(group))
		for j, t := range group {
			rendered[j] = render(t)
		}
		parts[i] = "(" + strings.Join(rendered, and) + ")"
	}
	return strings.Join(parts, or)
}

// fts5Expr compiles to an FTS5 MATCH expression.
func fts5Expr(groups [][]term) string {
	return compileBoolean(groups, " AND ", " OR ", func(t term) string {
		quoted := `"` + strings.ReplaceAll(t.text, `"`, `""`) + `"`
		if t.prefix {
			return quoted + "*"
		}
		return quoted
	})
}

// tsqueryExpr compiles to PostgreSQL to_tsquery input. A quoted multi-word
// lexeme is treated as a phrase by to_tsquery.
func tsqueryExpr(groups [][]term) string {
	return compileBoolean(groups, " & ", " | ", func(t term) string {
		escaped := strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(t.text)
		if t.prefix {
			return "'" + escaped + "':*"
		}
		return "'" + escaped + "'"
	})
}
