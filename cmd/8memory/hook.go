package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"8memory/internal/config"
	"8memory/internal/gitinfo"
	"8memory/internal/store"
	"8memory/internal/tocli"
)

const hookTimeout = 5 * time.Second

// hookInput is the JSON a prompt-submit hook receives on stdin.
type hookInput struct {
	Prompt string `json:"prompt"`
	Cwd    string `json:"cwd"`
}

// runHook creates the journey node for a prompt without involving the model,
// so the journey stays complete and costs no tokens. What it prints is added
// to the model's context.
func runHook(configPath, agent string, stdin io.Reader, stdout io.Writer) error {
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	title := firstLine(in.Prompt)
	if title == "" || in.Cwd == "" {
		return nil
	}

	// ToCLI created the node for a session's first prompt already. The claim
	// succeeds once, so later prompts in the session still get their own node.
	if nodeID := os.Getenv(tocli.EnvNode); nodeID != "" && tocli.ClaimPending(filepath.Dir(configPath), nodeID) {
		return printNodeContext(stdout, nodeID)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	here := gitinfo.ForDir(ctx, in.Cwd)
	node, err := s.AddNode(ctx, store.Node{Project: here.Project, Branch: here.Branch, Agent: agent, Title: title})
	if err != nil {
		return err
	}
	return printNodeContext(stdout, node.ID)
}

func printNodeContext(stdout io.Writer, nodeID string) error {
	_, err := fmt.Fprintf(stdout, "8memory journey node for this prompt: %s. Pass it as node_id to memory_write; do not call journey_mark.\n", nodeID)
	return err
}

func firstLine(text string) string {
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
