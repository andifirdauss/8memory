package tocli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func parseAll(p Parser, lines ...string) []Event {
	var out []Event
	for _, line := range lines {
		out = append(out, p.Parse([]byte(line))...)
	}
	return out
}

func kinds(events []Event, kind EventKind) []string {
	var out []string
	for _, event := range events {
		if event.Kind == kind {
			out = append(out, event.Text)
		}
	}
	return out
}

func TestClaudeParserStreamsTextOnceAndListsTools(t *testing.T) {
	events := parseAll(provider(t, "claude").NewParser(),
		`{"type":"system","subtype":"init","session_id":"s1","parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git  status"}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":"plain text"},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"secret"}}]},"parent_tool_use_id":"toolu_1"}`,
		`not json`,
		`{"type":"result","subtype":"success","session_id":"s2","result":"Hello","permission_denials":[{"tool_name":"Write"}]}`,
	)
	if got := strings.Join(kinds(events, EventText), ""); got != "Hello" {
		t.Fatalf("text %q", got)
	}
	if got := kinds(events, EventTool); len(got) != 1 || got[0] != "Bash: git status" {
		t.Fatalf("tools %q", got)
	}
	if got := kinds(events, EventSession); len(got) != 2 || got[1] != "s2" {
		t.Fatalf("sessions %q", got)
	}
	if got := kinds(events, EventInfo); len(got) != 1 || !strings.Contains(got[0], "Write") {
		t.Fatalf("info %q", got)
	}
}

func TestClaudeParserReportsFailureAndResultOnlyReplies(t *testing.T) {
	events := parseAll(provider(t, "claude").NewParser(), `{"type":"result","is_error":true,"result":"Not logged in"}`)
	if got := kinds(events, EventError); len(got) != 1 || got[0] != "Not logged in" {
		t.Fatalf("%+v", events)
	}
	events = parseAll(provider(t, "claude").NewParser(), `{"type":"result","result":"status server"}`)
	if got := kinds(events, EventText); len(got) != 1 || got[0] != "status server" {
		t.Fatalf("%+v", events)
	}
}

func TestCodexParser(t *testing.T) {
	events := parseAll(provider(t, "codex").NewParser(),
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.started","item":{"type":"command_execution","command":"ls -la"}}`,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go","kind":"update"}]}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Done"}}`,
		`{"type":"error","message":"Reconnecting... 2/5 (timeout)"}`,
		`{"type":"turn.failed","error":{"message":"quota exhausted"}}`,
	)
	if got := kinds(events, EventSession); len(got) != 1 || got[0] != "t1" {
		t.Fatalf("%q", got)
	}
	if got := kinds(events, EventTool); len(got) != 2 || got[0] != "Command: ls -la" || got[1] != "File (update): a.go" {
		t.Fatalf("%q", got)
	}
	if got := kinds(events, EventText); len(got) != 1 || got[0] != "Done" {
		t.Fatalf("%q", got)
	}
	if got := kinds(events, EventInfo); len(got) != 1 {
		t.Fatalf("a retry is not a failure: %+v", events)
	}
	if got := kinds(events, EventError); len(got) != 1 || got[0] != "quota exhausted" {
		t.Fatalf("%q", got)
	}
}

func TestClaudeParserShowsLocalCommandOnce(t *testing.T) {
	events := parseAll(WithoutSession(provider(t, "claude").NewParser()),
		`{"type":"system","subtype":"init","session_id":"temporary","parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Current session: 20% used"}]},"parent_tool_use_id":null}`,
		`{"type":"result","subtype":"success","session_id":"temporary","result":"Current session: 20% used","local_command":"usage"}`,
	)
	if got := kinds(events, EventText); len(got) != 1 || got[0] != "Current session: 20% used" {
		t.Fatalf("%+v", events)
	}
	if got := kinds(events, EventSession); len(got) != 0 {
		t.Fatalf("session leaked: %q", got)
	}
}

func TestUsageCommand(t *testing.T) {
	cmd := BuildUsage(context.Background(), provider(t, "claude"), Executable{Path: "claude"}, t.TempDir())
	if got := cmd.Args[len(cmd.Args)-1]; got != "/usage" {
		t.Fatal(got)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "--resume") {
		t.Fatalf("%q", cmd.Args)
	}
	if provider(t, "codex").UsageArgs() != nil {
		t.Fatal("codex exec has no usage command")
	}
}

func TestCleanRemovesTerminalControl(t *testing.T) {
	if got := Clean("a\x1b[2Jb\r\nc\td\u009b"); got != "a[2Jb\nc\td" {
		t.Fatalf("%q", got)
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connectApprovals(t *testing.T, s *ApprovalServer, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: s.url, HTTPClient: &http.Client{Transport: bearer{token}}}
	return client.Connect(ctx, transport, nil)
}

func callApproval(t *testing.T, session *mcp.ClientSession, args map[string]any) approvalResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: approvalToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var decoded approvalResult
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestApprovalServerRelaysDecisions(t *testing.T) {
	requests := make(chan *Approval, 2)
	server, err := StartApprovals(func(a *Approval) { requests <- a })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	session, err := connectApprovals(t, server, server.token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// Fields this version does not know about must not be rejected.
	call := map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "go\ntest\x1b[2J", "timeout": 5}, "tool_use_id": "x", "future_field": true}
	go func() {
		request := <-requests
		if request.ToolName != "Bash" || !strings.Contains(request.Detail, "command: go\ntest[2J") || !strings.Contains(request.Detail, "timeout: 5") {
			t.Errorf("%+v", request)
		}
		request.Answer(true)
		(<-requests).Answer(false)
	}()
	allowed := callApproval(t, session, call)
	if allowed.Behavior != "allow" || !strings.Contains(string(allowed.UpdatedInput), `"command"`) {
		t.Fatalf("%+v", allowed)
	}
	if denied := callApproval(t, session, call); denied.Behavior != "deny" || denied.Message == "" {
		t.Fatalf("%+v", denied)
	}

	// The dialog cannot collect answers, so the model is told to ask in text.
	question := callApproval(t, session, map[string]any{"tool_name": askQuestionTool, "input": map[string]any{}})
	if question.Behavior != "deny" || !strings.Contains(question.Message, "plain text") {
		t.Fatalf("%+v", question)
	}
}

func TestApprovalServerDeniesWhenClosed(t *testing.T) {
	requested := make(chan struct{})
	server, err := StartApprovals(func(*Approval) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	result := make(chan *mcp.CallToolResult, 1)
	go func() {
		out, err := server.decide(context.Background(), json.RawMessage(`{"tool_name":"Bash","input":{}}`), func(a *Approval) { close(requested) })
		if err != nil {
			t.Error(err)
		}
		result <- out
	}()
	<-requested
	server.Close()
	text := (<-result).Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"deny"`) {
		t.Fatal(text)
	}
}

func TestApprovalServerRequiresToken(t *testing.T) {
	server, err := StartApprovals(func(*Approval) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := connectApprovals(t, server, "wrong"); err == nil {
		t.Fatal("connected with a wrong token")
	}
	if !strings.HasPrefix(server.url, "http://127.0.0.1:") {
		t.Fatal(server.url)
	}
	entry := server.Server()
	if entry.Type != "http" || entry.URL != server.url || entry.Headers["Authorization"] != "Bearer "+server.token {
		t.Fatalf("%+v", entry)
	}
}

// TestHelperCLI is not a test: it is the CLI started by the Run tests.
func TestHelperCLI(t *testing.T) {
	switch os.Getenv("TOCLI_HELPER") {
	case "ok":
		fmt.Print(`{"type":"item.completed","item":{"type":"agent_message","text":"one"}}` + "\n")
		fmt.Print(`{"type":"item.completed","item":{"type":"agent_message","text":"no-trailing-newline"}}`)
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "log in first")
		os.Exit(3)
	case "reported":
		fmt.Print(`{"type":"turn.failed","error":{"message":"quota exhausted"}}` + "\n")
		fmt.Fprint(os.Stderr, "other detail")
		os.Exit(3)
	case "hang":
		fmt.Print(`{"type":"thread.started","thread_id":"t1"}` + "\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func helperCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperCLI$")
	cmd.Env = append(os.Environ(), "TOCLI_HELPER="+mode)
	prepareProcess(cmd)
	return cmd
}

func runHelper(t *testing.T, mode string) ([]Event, error) {
	t.Helper()
	var events []Event
	err := Run(helperCommand(context.Background(), mode), provider(t, "codex").NewParser(), func(e Event) { events = append(events, e) })
	return events, err
}

func TestRunDeliversEveryLine(t *testing.T) {
	events, err := runHelper(t, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(events, EventText); len(got) != 2 || got[1] != "no-trailing-newline" {
		t.Fatalf("%+v", events)
	}
}

func TestRunExplainsFailure(t *testing.T) {
	if _, err := runHelper(t, "fail"); err == nil || !strings.Contains(err.Error(), "log in first") {
		t.Fatalf("%v", err)
	}
	events, err := runHelper(t, "reported")
	if err != nil || len(kinds(events, EventError)) != 1 {
		t.Fatalf("a failure must be reported once: %+v, %v", events, err)
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(helperCommand(ctx, "hang"), provider(t, "codex").NewParser(), func(Event) { close(started) })
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("helper did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a killed CLI cannot have succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancelling did not stop the CLI")
	}
}
