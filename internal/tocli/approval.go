package tocli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	approvalServerName = "memory8approval"
	approvalToolName   = "approve"
	approvalToolRef    = "mcp__" + approvalServerName + "__" + approvalToolName

	deniedByUser      = "Denied by the user in 8memory."
	askQuestionTool   = "AskUserQuestion"
	askQuestionDenial = "8memory cannot show AskUserQuestion yet. Ask the question as plain text and end your turn; the user will answer in the next prompt."
)

// Approval is one tool call waiting for the user's decision.
type Approval struct {
	ToolName string
	Detail   string // every input field, so nothing approved is hidden
	reply    chan bool
}

// NewApproval creates a request for the user to decide.
func NewApproval(toolName, detail string) *Approval {
	return &Approval{ToolName: Clean(toolName), Detail: detail, reply: make(chan bool, 1)}
}

// Reply delivers the decision.
func (a *Approval) Reply() <-chan bool { return a.reply }

// Answer records the decision. Only the first answer counts.
func (a *Approval) Answer(allow bool) {
	select {
	case a.reply <- allow:
	default:
	}
}

// ApprovalServer is the localhost MCP server a CLI asks about tool calls.
type ApprovalServer struct {
	server    *http.Server
	url       string
	token     string
	closed    chan struct{}
	closeOnce sync.Once
}

// StartApprovals listens on a random localhost port and passes every request
// to onRequest. A request nobody answers is denied when the server closes.
func StartApprovals(onRequest func(*Approval)) (*ApprovalServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not start the permission server: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		listener.Close()
		return nil, err
	}
	s := &ApprovalServer{
		url:    "http://" + listener.Addr().String() + "/mcp",
		token:  hex.EncodeToString(secret),
		closed: make(chan struct{}),
	}

	tools := mcp.NewServer(&mcp.Implementation{Name: "8memory-approval", Version: "1"}, nil)
	// A permissive schema, because the CLI may add input fields in any version.
	tools.AddTool(&mcp.Tool{
		Name:        approvalToolName,
		Description: "Ask the user at the 8memory TUI whether a tool call may run.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.decide(ctx, request.Params.Arguments, onRequest)
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools }, &mcp.StreamableHTTPOptions{Stateless: true})
	s.server = &http.Server{Handler: s.authorize(handler)}
	go s.server.Serve(listener)
	return s, nil
}

func (s *ApprovalServer) authorize(next http.Handler) http.Handler {
	expected := []byte("Bearer " + s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *ApprovalServer) decide(ctx context.Context, raw json.RawMessage, onRequest func(*Approval)) (*mcp.CallToolResult, error) {
	var call struct {
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return denial(err.Error())
	}
	if call.ToolName == askQuestionTool {
		return denial(askQuestionDenial)
	}
	approval := NewApproval(call.ToolName, describeInput(call.Input))
	onRequest(approval)
	select {
	case allow := <-approval.Reply():
		if allow {
			return allowance(call.Input)
		}
		return denial(deniedByUser)
	case <-ctx.Done():
		return denial(deniedByUser)
	case <-s.closed:
		return denial(deniedByUser)
	}
}

// describeInput lists every field in a stable order, with strings unquoted so
// a multi-line command reads as it will run.
func describeInput(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Clean(string(raw))
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, key := range keys {
		value := string(fields[key])
		var text string
		if json.Unmarshal(fields[key], &text) == nil {
			value = text
		}
		fmt.Fprintf(&b, "%s: %s\n", key, value)
	}
	return Clean(strings.TrimRight(b.String(), "\n"))
}

type approvalResult struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

func allowance(input json.RawMessage) (*mcp.CallToolResult, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	return approvalReply(approvalResult{Behavior: "allow", UpdatedInput: input})
}

func denial(message string) (*mcp.CallToolResult, error) {
	return approvalReply(approvalResult{Behavior: "deny", Message: message})
}

func approvalReply(result approvalResult) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}

// Server describes how a CLI connects to this server.
func (s *ApprovalServer) Server() *MCPServer {
	return &MCPServer{Type: "http", URL: s.url, Headers: map[string]string{"Authorization": "Bearer " + s.token}}
}

// Close stops the server and denies every request still waiting.
func (s *ApprovalServer) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.server.Close()
	})
}
