package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"8memory/internal/config"
	"8memory/internal/store"
)

func connect(t *testing.T) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, config.Config{Backend: config.SQLite, Path: filepath.Join(t.TempDir(), "m.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := New(s, nil).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return ctx, session
}

func TestToolsWriteThenSearch(t *testing.T) {
	ctx, session := connect(t)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}

	written, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{
		"change": "modify", "feature": "Auth", "content": "Auth dipindah ke cookie", "project": "demo", "tags": []string{"login"}, "agent": "claude",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if written.IsError {
		t.Fatalf("write failed: %+v", written.Content[0])
	}

	for _, query := range []string{"login", ""} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_search", Arguments: map[string]any{"query": query, "project": "demo"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("search failed: %+v", result.Content[0])
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var out searchOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Memories) != 1 || out.Memories[0].Content != "Auth dipindah ke cookie" {
			t.Fatalf("query %q: %s", query, raw)
		}
	}
}

func TestSearchWithNoResultsIsNotAnError(t *testing.T) {
	ctx, session := connect(t)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_search", Arguments: map[string]any{"query": "tidakada"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%+v", result.Content[0])
	}
}

func TestWriteRejectsEmptyContentAsToolError(t *testing.T) {
	ctx, session := connect(t)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_write", Arguments: map[string]any{"change": "add", "feature": "x", "content": "  "}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("want tool error")
	}
}
