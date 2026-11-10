package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServeOverStdio runs the real binary the way an AI client does. Anything
// stray on stdout would corrupt the protocol and fail this test.
func TestServeOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	binary := filepath.Join(dir, "8memory.exe")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	configPath := filepath.Join(dir, "config.json")

	call := func(tool string, args map[string]any) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, "serve", "--config", configPath)
		cmd.Stderr = os.Stderr
		session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s: %+v", tool, result.Content[0])
		}
		return result.Content[0].(*mcp.TextContent).Text
	}

	// Two separate processes: the memory must outlive the first one.
	call("memory_write", map[string]any{"change": "add", "feature": "Cross process", "content": "Cross process decision", "project": "demo", "agent": "claude"})
	if got := call("memory_search", map[string]any{"query": "cross", "project": "demo"}); !strings.Contains(got, "Cross process decision") {
		t.Fatal(got)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("want error")
	}
}

func TestResolvePerMachineAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	root := filepath.Join(dir, "Backup")
	raw, err := json.Marshal(map[string]any{"aliases": map[string]string{"backup": root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"resolve", "--config", configPath, "backup://project/2026-11-09/database.sql"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"resolve", "--config", configPath, "backup://../secret"}); err == nil {
		t.Fatal("resolved a path outside the alias")
	}
	if err := run([]string{"resolve", "--config", configPath}); err == nil {
		t.Fatal("resolved an empty ref")
	}
}
