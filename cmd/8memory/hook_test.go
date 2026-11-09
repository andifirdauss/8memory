package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"8memory/internal/config"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

func hookNodes(t *testing.T, configPath string) []store.Node {
	t.Helper()
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(context.Background(), store.NodeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func hookPayload(t *testing.T, prompt, cwd string) *bytes.Reader {
	t.Helper()
	// The extra fields match what Claude Code sends.
	raw, err := json.Marshal(map[string]string{"session_id": "s1", "hook_event_name": "UserPromptSubmit", "prompt": prompt, "cwd": cwd})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

func TestHookCreatesNodeInGitBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proyek")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "develop")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")

	var out bytes.Buffer
	if err := runHook(configPath, "claude", hookPayload(t, "\n  Tambah fitur ekspor\nDetail panjang di baris lain", dir), &out); err != nil {
		t.Fatal(err)
	}
	nodes := hookNodes(t, configPath)
	if len(nodes) != 1 {
		t.Fatalf("%+v", nodes)
	}
	n := nodes[0]
	if n.Project != "proyek" || n.Branch != "develop" || n.Title != "Tambah fitur ekspor" || n.Agent != "claude" {
		t.Fatalf("%+v", n)
	}
	if !strings.Contains(out.String(), n.ID) {
		t.Fatalf("node id not given to the model: %q", out.String())
	}
}

func TestHookSkipsEmptyPrompt(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer
	if err := runHook(configPath, "claude", hookPayload(t, "   \n", t.TempDir()), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || len(hookNodes(t, configPath)) != 0 {
		t.Fatalf("created a node: %q", out.String())
	}
}

func TestHookCommandNeverBlocksPrompt(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "8memory.exe")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(binary, "hook", "--config", filepath.Join(dir, "config.json"))
	cmd.Stdin = strings.NewReader("ini bukan json")
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook must exit 0 on bad input: %v", err)
	}
}

func TestHookDoesNotDuplicateToCLINode(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	const nodeID = "0195f3a2-7c10-7000-8000-000000000042"
	if err := tocli.MarkPending(filepath.Dir(configPath), nodeID); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tocli.EnvNode, nodeID)
	dir := t.TempDir()

	var first bytes.Buffer
	if err := runHook(configPath, "claude", hookPayload(t, "prompt pertama", dir), &first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), nodeID) || len(hookNodes(t, configPath)) != 0 {
		t.Fatalf("first prompt must reuse the ToCLI node: %q", first.String())
	}

	var second bytes.Buffer
	if err := runHook(configPath, "claude", hookPayload(t, "prompt kedua", dir), &second); err != nil {
		t.Fatal(err)
	}
	nodes := hookNodes(t, configPath)
	if len(nodes) != 1 || nodes[0].Title != "prompt kedua" || strings.Contains(second.String(), nodeID) {
		t.Fatalf("later prompts need their own node: %+v %q", nodes, second.String())
	}
}
