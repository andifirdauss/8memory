package tocli

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// EventKind says what a CLI reported.
type EventKind int

const (
	EventSession EventKind = iota // Text is the conversation ID
	EventText                     // Text is part of the reply
	EventTool                     // Text describes a tool being used
	EventInfo                     // Text is a note for the user
	EventError                    // Text says why the prompt failed
)

// Event is one step of a provider's reply, in a form the TUI can draw.
type Event struct {
	Kind EventKind
	Text string
	// Fresh marks reply text that starts a new block rather than continuing one.
	Fresh bool
}

// Parser turns the output lines of one prompt into events.
type Parser interface {
	Parse(line []byte) []Event
}

type withoutSession struct{ Parser }

func (p withoutSession) Parse(line []byte) []Event {
	events := p.Parser.Parse(line)
	out := events[:0]
	for _, event := range events {
		if event.Kind != EventSession {
			out = append(out, event)
		}
	}
	return out
}

// WithoutSession hides conversation IDs, for a command whose run must not
// replace the conversation being continued.
func WithoutSession(p Parser) Parser { return withoutSession{p} }

const toolSummaryRunes = 200

// Clean removes control characters, so text written by a model or a tool
// cannot move the cursor or redraw the TUI, for example to fake a dialog.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

// toolSummary picks the input field that best says what a call does.
func toolSummary(name string, input json.RawMessage) string {
	var fields map[string]any
	json.Unmarshal(input, &fields)
	for _, key := range []string{"command", "file_path", "notebook_path", "pattern", "url", "query", "description", "prompt", "path"} {
		if value, ok := fields[key].(string); ok && value != "" {
			return name + ": " + oneLine(value, toolSummaryRunes)
		}
	}
	return name
}

type claudeParser struct {
	streamed bool // reply text arrived as deltas, so its full copy must be skipped
	sawText  bool
	fresh    bool
}

type claudeLine struct {
	Type      string           `json:"type"`
	Subtype   string           `json:"subtype"`
	SessionID string           `json:"session_id"`
	Parent    *string          `json:"parent_tool_use_id"`
	Message   *json.RawMessage `json:"message"`
	Event     *struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	IsError           bool     `json:"is_error"`
	Result            string   `json:"result"`
	Errors            []string `json:"errors"`
	Attempt           int      `json:"attempt"`
	MaxRetries        int      `json:"max_retries"`
	Error             string   `json:"error"`
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
}

// Parse skips subagent traffic: it is detail of a single tool call, and the
// main conversation already shows that call.
func (p *claudeParser) Parse(raw []byte) []Event {
	var line claudeLine
	if json.Unmarshal(raw, &line) != nil || line.Parent != nil {
		return nil
	}
	switch line.Type {
	case "system":
		switch line.Subtype {
		case "init":
			return []Event{{Kind: EventSession, Text: line.SessionID}}
		case "api_retry":
			return []Event{{Kind: EventInfo, Text: fmt.Sprintf("Mencoba lagi (%d/%d): %s", line.Attempt, line.MaxRetries, line.Error)}}
		}
	case "stream_event":
		return p.parseStream(line)
	case "assistant":
		return p.parseAssistant(line)
	case "result":
		return p.parseResult(line)
	}
	return nil
}

func (p *claudeParser) parseStream(line claudeLine) []Event {
	if line.Event == nil {
		return nil
	}
	if line.Event.Type == "content_block_start" && line.Event.ContentBlock.Type == "text" {
		p.fresh = true
		return nil
	}
	if line.Event.Type != "content_block_delta" || line.Event.Delta.Type != "text_delta" {
		return nil
	}
	p.streamed, p.sawText = true, true
	event := Event{Kind: EventText, Text: line.Event.Delta.Text, Fresh: p.fresh}
	p.fresh = false
	return []Event{event}
}

func (p *claudeParser) parseAssistant(line claudeLine) []Event {
	if line.Message == nil {
		return nil
	}
	var message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(*line.Message, &message) != nil {
		return nil
	}
	var out []Event
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if p.streamed {
				p.streamed = false
				continue
			}
			p.sawText = true
			out = append(out, Event{Kind: EventText, Text: block.Text, Fresh: true})
		case "tool_use":
			out = append(out, Event{Kind: EventTool, Text: toolSummary(block.Name, block.Input)})
		}
	}
	return out
}

func (p *claudeParser) parseResult(line claudeLine) []Event {
	var out []Event
	if line.SessionID != "" {
		out = append(out, Event{Kind: EventSession, Text: line.SessionID})
	}
	if len(line.PermissionDenials) > 0 {
		names := make([]string, 0, len(line.PermissionDenials))
		for _, denial := range line.PermissionDenials {
			names = append(names, denial.ToolName)
		}
		out = append(out, Event{Kind: EventInfo, Text: fmt.Sprintf("%d tindakan ditolak: %s", len(names), strings.Join(names, ", "))})
	}
	switch {
	case line.IsError:
		reason := line.Result
		if reason == "" {
			reason = strings.Join(line.Errors, "; ")
		}
		if reason == "" {
			reason = line.Subtype
		}
		out = append(out, Event{Kind: EventError, Text: reason})
	case !p.sawText && line.Result != "":
		// A command such as /mcp answers only through the result.
		out = append(out, Event{Kind: EventText, Text: line.Result, Fresh: true})
	}
	return out
}

type codexParser struct{}

type codexItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Message string `json:"message"`
	Query   string `json:"query"`
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	Changes []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
}

type codexLine struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Message  string     `json:"message"`
	Item     *codexItem `json:"item"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (codexParser) Parse(raw []byte) []Event {
	var line codexLine
	if json.Unmarshal(raw, &line) != nil {
		return nil
	}
	switch line.Type {
	case "thread.started":
		return []Event{{Kind: EventSession, Text: line.ThreadID}}
	case "error":
		// Codex reports each retry as an error; only turn.failed is final.
		if strings.HasPrefix(line.Message, "Reconnecting") {
			return []Event{{Kind: EventInfo, Text: line.Message}}
		}
		return []Event{{Kind: EventError, Text: line.Message}}
	case "turn.failed":
		if line.Error != nil {
			return []Event{{Kind: EventError, Text: line.Error.Message}}
		}
	case "item.started":
		if line.Item != nil && line.Item.Type == "command_execution" {
			return []Event{{Kind: EventTool, Text: "Perintah: " + oneLine(line.Item.Command, toolSummaryRunes)}}
		}
	case "item.completed":
		if line.Item != nil {
			return codexCompleted(*line.Item)
		}
	}
	return nil
}

func codexCompleted(item codexItem) []Event {
	switch item.Type {
	case "agent_message":
		return []Event{{Kind: EventText, Text: item.Text, Fresh: true}}
	case "file_change":
		out := make([]Event, 0, len(item.Changes))
		for _, change := range item.Changes {
			out = append(out, Event{Kind: EventTool, Text: "File (" + change.Kind + "): " + change.Path})
		}
		return out
	case "web_search":
		return []Event{{Kind: EventTool, Text: "Pencarian: " + oneLine(item.Query, toolSummaryRunes)}}
	case "mcp_tool_call":
		return []Event{{Kind: EventTool, Text: "MCP: " + item.Server + "/" + item.Tool}}
	case "error":
		return []Event{{Kind: EventInfo, Text: item.Message}}
	}
	return nil
}
