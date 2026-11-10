package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"8memory/internal/store"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo-demo")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-b", "main")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func callText(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustCall(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result := callText(t, ctx, session, tool, args)
	if result.IsError {
		t.Fatalf("%s: %+v", tool, result.Content[0].(*mcp.TextContent).Text)
	}
	return result
}

func decode[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func features(out searchOutput) string {
	var names []string
	for _, m := range out.Memories {
		names = append(names, m.Feature)
	}
	return strings.Join(names, "|")
}

func TestMemoriesSeparatedByBranchButReadable(t *testing.T) {
	ctx, session := connect(t)
	dir := gitRepo(t)

	write := func(feature string) store.Memory {
		return decode[store.Memory](t, mustCall(t, ctx, session, "memory_write", map[string]any{
			"change": "add", "feature": feature, "content": "text " + feature, "cwd": dir,
		}))
	}
	onMain := write("On main")
	if onMain.Project != "repo-demo" || onMain.Branch != "main" {
		t.Fatalf("%+v", onMain)
	}
	gitRun(t, dir, "switch", "-c", "feature")
	if onFeature := write("On feature"); onFeature.Branch != "feature" {
		t.Fatalf("%+v", onFeature)
	}

	search := func(args map[string]any) string {
		args["cwd"] = dir
		return features(decode[searchOutput](t, mustCall(t, ctx, session, "memory_search", args)))
	}
	if got := search(map[string]any{}); got != "On feature" {
		t.Fatalf("current branch: %q", got)
	}
	if got := search(map[string]any{"branch": "main"}); got != "On main" {
		t.Fatalf("named branch: %q", got)
	}
	if got := search(map[string]any{"branch": "*"}); got != "On feature|On main" {
		t.Fatalf("every branch: %q", got)
	}
	if got := search(map[string]any{"project": "other-project"}); got != "" {
		t.Fatalf("other project: %q", got)
	}
}

func TestNamedProjectIgnoresCurrentBranch(t *testing.T) {
	ctx, session := connect(t)
	dir := gitRepo(t)
	mustCall(t, ctx, session, "memory_write", map[string]any{"change": "add", "feature": "F", "content": "text", "project": "other", "branch": "develop", "cwd": dir})
	got := decode[searchOutput](t, mustCall(t, ctx, session, "memory_search", map[string]any{"project": "other", "cwd": dir}))
	if len(got.Memories) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestMemoryWriteRequiresChangeAndFeature(t *testing.T) {
	ctx, session := connect(t)
	for _, args := range []map[string]any{
		{"change": "", "feature": "F", "content": "text"},
		{"change": "add", "feature": " ", "content": "text"},
		{"change": "delete", "feature": "F", "content": "text"},
	} {
		if result := callText(t, ctx, session, "memory_write", args); !result.IsError {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestJourneyMarkLinksMemories(t *testing.T) {
	ctx, session := connect(t)
	dir := gitRepo(t)

	node := decode[store.Node](t, mustCall(t, ctx, session, "journey_mark", map[string]any{"title": "Add export", "cwd": dir, "agent": "codex"}))
	if node.ID == "" || node.Project != "repo-demo" || node.Branch != "main" {
		t.Fatalf("%+v", node)
	}
	mustCall(t, ctx, session, "memory_write", map[string]any{"change": "add", "feature": "Export", "content": "text", "cwd": dir, "node_id": node.ID})
	mustCall(t, ctx, session, "memory_write", map[string]any{"change": "inspect", "feature": "Audit", "content": "other text", "cwd": dir, "node_id": node.ID})
	mustCall(t, ctx, session, "memory_write", map[string]any{"change": "add", "feature": "Unlinked", "content": "third text", "cwd": dir})

	// node_id must work from anywhere, regardless of the caller's branch.
	linked := decode[searchOutput](t, mustCall(t, ctx, session, "memory_search", map[string]any{"node_id": node.ID, "cwd": t.TempDir()}))
	if got := features(linked); got != "Audit|Export" {
		t.Fatalf("%q", got)
	}

	list := mustCall(t, ctx, session, "journey_list", map[string]any{"cwd": dir}).Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"## repo-demo @ main", node.ID, "Add export", "[codex]"} {
		if !strings.Contains(list, want) {
			t.Fatalf("missing %q in:\n%s", want, list)
		}
	}
}

func TestJourneyListPeriods(t *testing.T) {
	ctx, session := connect(t)
	dir := gitRepo(t)
	if got := mustCall(t, ctx, session, "journey_list", map[string]any{"cwd": dir}).Content[0].(*mcp.TextContent).Text; !strings.Contains(got, "No journey nodes") {
		t.Fatal(got)
	}
	for _, period := range []string{"1d", "7d", "30d", "all"} {
		mustCall(t, ctx, session, "journey_list", map[string]any{"cwd": dir, "period": period})
	}
	if result := callText(t, ctx, session, "journey_list", map[string]any{"period": "2w"}); !result.IsError {
		t.Fatal("accepted an unknown period")
	}
}
