package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

var cursorStyle = lipgloss.NewStyle().Reverse(true)

// editor is a multi-line prompt buffer. Attachment markers are ordinary text
// in the buffer, but the cursor treats each one as a single character, so a
// marker can only exist whole.
type editor struct {
	text    []rune
	cursor  int
	markers []string
}

func (e *editor) value() string { return string(e.text) }

func (e *editor) reset() { *e = editor{} }

func (e *editor) insert(s string) {
	runes := []rune(s)
	e.text = slices.Insert(e.text, e.cursor, runes...)
	e.cursor += len(runes)
}

// spans returns the rune range of every marker occurrence.
func (e *editor) spans() [][2]int {
	var out [][2]int
	for _, marker := range e.markers {
		runes := []rune(marker)
		for i := 0; i+len(runes) <= len(e.text); i++ {
			if slices.Equal(e.text[i:i+len(runes)], runes) {
				out = append(out, [2]int{i, i + len(runes)})
				i += len(runes) - 1
			}
		}
	}
	return out
}

func (e *editor) left() {
	if e.cursor == 0 {
		return
	}
	e.cursor--
	for _, span := range e.spans() {
		if e.cursor > span[0] && e.cursor < span[1] {
			e.cursor = span[0]
		}
	}
}

func (e *editor) right() {
	if e.cursor == len(e.text) {
		return
	}
	e.cursor++
	for _, span := range e.spans() {
		if e.cursor > span[0] && e.cursor < span[1] {
			e.cursor = span[1]
		}
	}
}

func (e *editor) lineStart(position int) int {
	for position > 0 && e.text[position-1] != '\n' {
		position--
	}
	return position
}

func (e *editor) lineEnd(position int) int {
	for position < len(e.text) && e.text[position] != '\n' {
		position++
	}
	return position
}

func (e *editor) home() { e.cursor = e.lineStart(e.cursor) }

func (e *editor) end() { e.cursor = e.lineEnd(e.cursor) }

func (e *editor) up() {
	start := e.lineStart(e.cursor)
	if start == 0 {
		e.cursor = 0
		return
	}
	column := e.cursor - start
	previous := e.lineStart(start - 1)
	e.moveTo(min(previous+column, start-1))
}

func (e *editor) down() {
	end := e.lineEnd(e.cursor)
	if end == len(e.text) {
		e.cursor = end
		return
	}
	column := e.cursor - e.lineStart(e.cursor)
	e.moveTo(min(end+1+column, e.lineEnd(end+1)))
}

// moveTo places the cursor, stepping out of any marker it lands inside.
func (e *editor) moveTo(position int) {
	e.cursor = position
	for _, span := range e.spans() {
		if position > span[0] && position < span[1] {
			e.cursor = span[0]
		}
	}
}

func (e *editor) cut(from, to int) {
	e.text = slices.Delete(e.text, from, to)
	e.cursor = from
}

// backspace deletes before the cursor and returns the marker it removed, if
// any, so the matching attachment can be discarded.
func (e *editor) backspace() string {
	if e.cursor == 0 {
		return ""
	}
	for _, span := range e.spans() {
		if span[1] == e.cursor {
			marker := string(e.text[span[0]:span[1]])
			e.cut(span[0], span[1])
			return marker
		}
	}
	e.cut(e.cursor-1, e.cursor)
	return ""
}

// del deletes after the cursor and returns the marker it removed, if any.
func (e *editor) del() string {
	if e.cursor == len(e.text) {
		return ""
	}
	for _, span := range e.spans() {
		if span[0] == e.cursor {
			marker := string(e.text[span[0]:span[1]])
			e.cut(span[0], span[1])
			return marker
		}
	}
	e.cut(e.cursor, e.cursor+1)
	return ""
}

// removeMarker deletes every occurrence of a marker from the buffer.
func (e *editor) removeMarker(marker string) {
	saved := e.markers
	e.markers = []string{marker}
	spans := e.spans()
	e.markers = saved
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		e.text = slices.Delete(e.text, span[0], span[1])
		switch {
		case e.cursor >= span[1]:
			e.cursor -= span[1] - span[0]
		case e.cursor > span[0]:
			e.cursor = span[0]
		}
	}
}

// render draws the buffer wrapped to width, scrolled to keep the cursor on one
// of the last height rows.
func (e *editor) render(width, height int) string {
	var rows []string
	var row strings.Builder
	rowWidth, cursorRow := 0, 0
	flush := func() {
		rows = append(rows, row.String())
		row.Reset()
		rowWidth = 0
	}
	for i, r := range e.text {
		w := runewidth.RuneWidth(r)
		if r != '\n' && rowWidth+w > width {
			flush()
		}
		if i == e.cursor {
			cursorRow = len(rows)
			glyph := string(r)
			if r == '\n' {
				glyph = " "
			}
			row.WriteString(cursorStyle.Render(glyph))
		} else if r != '\n' {
			row.WriteRune(r)
		}
		if r == '\n' {
			flush()
			continue
		}
		rowWidth += w
	}
	if e.cursor == len(e.text) {
		if rowWidth >= width {
			flush()
		}
		cursorRow = len(rows)
		row.WriteString(cursorStyle.Render(" "))
	}
	flush()

	start := max(cursorRow-height+1, 0)
	end := min(start+height, len(rows))
	return strings.Join(rows[start:end], "\n")
}
