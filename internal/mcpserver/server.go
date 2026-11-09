// Package mcpserver exposes a Store to AI clients as MCP tools.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"8memory/internal/gitinfo"
	"8memory/internal/refs"
	"8memory/internal/store"
)

const (
	serverVersion   = "0.2.0"
	callTimeout     = 15 * time.Second
	maxSearchLimit  = 50
	maxJourneyLimit = 200
	allScopes       = "*"
	maxContentBytes = 16 * 1024
	dumpInsertLimit = 100 // INSERT statements that mark text as a data dump
	maxFiles        = 20
	maxKindRunes    = 100
	maxHintRunes    = 2000
	timeLayout      = "2006-01-02 15:04"
	instructions    = `Persistent journey and memory shared across AI coding tools. Always pass cwd, the absolute path of the project.
At the start of a session, call journey_list to see recent prompts and memory_search to recall relevant decisions.
For each user prompt: if the context already gives an 8memory journey node id, use it; otherwise call journey_mark first with a short title.
When the prompt is finished, call memory_write once per feature touched, with change set to add, remove, modify or inspect, and with the node_id.
Never put file contents or database dumps in content: save the file outside the project and pass a reference in files.
Reads default to the current project and branch; pass branch "*" or project "*" to read across them.`
)

type writeInput struct {
	Change  string          `json:"change" jsonschema:"What this prompt did to the feature: add, remove, modify, or inspect when the user only asked for a check."`
	Feature string          `json:"feature" jsonschema:"Name of the feature that was added, removed, modified or inspected."`
	Content string          `json:"content" jsonschema:"What changed and why, or what the inspection found, written so a future session understands it without this conversation."`
	NodeID  string          `json:"node_id,omitempty" jsonschema:"UUID of the journey node for this prompt."`
	Cwd     string          `json:"cwd,omitempty" jsonschema:"Absolute path of the project. Project and branch are detected from it."`
	Project string          `json:"project,omitempty" jsonschema:"Overrides the detected project name."`
	Branch  string          `json:"branch,omitempty" jsonschema:"Overrides the detected git branch."`
	Tags    []string        `json:"tags,omitempty" jsonschema:"Extra search keywords. Include synonyms in both Indonesian and English, for example login and autentikasi."`
	Agent   string          `json:"agent,omitempty" jsonschema:"Tool writing this memory, for example claude, codex, cursor or antigravity."`
	Files   []store.FileRef `json:"files,omitempty" jsonschema:"References to large files saved outside the project, such as database dumps. Content stays a short summary."`
}

type searchInput struct {
	Query   string `json:"query,omitempty" jsonschema:"Words must all match. Supports \"exact phrase\", prefix*, and OR between alternatives, for example: auth OR login. Empty returns the newest memories."`
	Cwd     string `json:"cwd,omitempty" jsonschema:"Absolute path of the project. Project and branch are detected from it."`
	Project string `json:"project,omitempty" jsonschema:"Project to read. Defaults to the current one; * reads every project."`
	Branch  string `json:"branch,omitempty" jsonschema:"Branch to read. Defaults to the current one; * reads every branch."`
	Change  string `json:"change,omitempty" jsonschema:"Only memories with this change: add, remove, modify or inspect."`
	NodeID  string `json:"node_id,omitempty" jsonschema:"Only memories of this journey node. Ignores project and branch."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum results, default 10, at most 50."`
}

type searchOutput struct {
	Memories []store.Memory `json:"memories"`
}

type markInput struct {
	Title   string `json:"title" jsonschema:"Short title of the user's prompt, a few words."`
	Cwd     string `json:"cwd,omitempty" jsonschema:"Absolute path of the project. Project and branch are detected from it."`
	Project string `json:"project,omitempty" jsonschema:"Overrides the detected project name."`
	Branch  string `json:"branch,omitempty" jsonschema:"Overrides the detected git branch."`
	Agent   string `json:"agent,omitempty" jsonschema:"Tool creating this node, for example claude, codex, cursor or antigravity."`
}

type journeyInput struct {
	Period  string `json:"period,omitempty" jsonschema:"How far back: 1d, 7d, 30d or all. Default 1d."`
	Cwd     string `json:"cwd,omitempty" jsonschema:"Absolute path of the project. Project and branch are detected from it."`
	Project string `json:"project,omitempty" jsonschema:"Project to read. Defaults to the current one; * reads every project."`
	Branch  string `json:"branch,omitempty" jsonschema:"Branch to read. Defaults to the current one; * reads every branch."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum nodes, default 50, at most 200."`
}

var dumpMarkers = []string{
	"-- postgresql database dump",
	"-- mysql dump",
	"-- mariadb dump",
	"-- dumping data for table",
}

// checkContent keeps memories small enough to be read back cheaply. The error
// is written for the AI, so it says what to do instead.
func checkContent(content string, aliases map[string]string) error {
	guidance := fmt.Sprintf("Save the file outside the project and send a reference through the files parameter, keeping only a short summary in content. Available ref schemes: %s.",
		strings.Join(refs.Schemes(aliases), ", "))
	if size := len(content); size > maxContentBytes {
		return fmt.Errorf("content is %d bytes, over the %d byte limit. %s", size, maxContentBytes, guidance)
	}
	lower := strings.ToLower(content)
	for _, marker := range dumpMarkers {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("content looks like a database dump. %s", guidance)
		}
	}
	if strings.Count(lower, "insert into") >= dumpInsertLimit {
		return fmt.Errorf("content looks like a data dump, with %d or more INSERT INTO statements. %s", dumpInsertLimit, guidance)
	}
	return nil
}

// checkFiles validates references and records which machine they came from.
func checkFiles(files []store.FileRef, aliases map[string]string) ([]store.FileRef, error) {
	if len(files) > maxFiles {
		return nil, fmt.Errorf("at most %d files per memory", maxFiles)
	}
	hostname, _ := os.Hostname()
	out := make([]store.FileRef, len(files))
	for i, file := range files {
		file.Ref = strings.TrimSpace(file.Ref)
		if err := refs.Validate(file.Ref, aliases); err != nil {
			return nil, err
		}
		if file.Size < 0 {
			return nil, fmt.Errorf("size of %s must not be negative", file.Ref)
		}
		if utf8.RuneCountInString(file.Kind) > maxKindRunes || utf8.RuneCountInString(file.RestoreHint) > maxHintRunes {
			return nil, fmt.Errorf("kind and restore_hint of %s are limited to %d and %d characters", file.Ref, maxKindRunes, maxHintRunes)
		}
		file.Host = firstNonEmpty(file.Host, hostname)
		out[i] = file
	}
	return out, nil
}

// detect returns the location of the call. The explicit cwd is preferred
// because some clients start the server outside the project.
func detect(ctx context.Context, cwd string) gitinfo.Info {
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		return gitinfo.ForDir(ctx, cwd)
	}
	if wd, err := os.Getwd(); err == nil {
		info, _ := gitinfo.Detect(ctx, wd)
		return info
	}
	return gitinfo.Info{}
}

func firstNonEmpty(explicit, fallback string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return fallback
}

// readScope resolves read filters. An empty result means no filter. The
// current branch only applies to the current project, because it says nothing
// about a project the caller named explicitly.
func readScope(ctx context.Context, cwd, project, branch string) (string, string) {
	here := detect(ctx, cwd)
	resolvedProject := firstNonEmpty(project, here.Project)
	if project == allScopes {
		resolvedProject = ""
	}
	resolvedBranch := branch
	if strings.TrimSpace(branch) == "" && resolvedProject == here.Project {
		resolvedBranch = here.Branch
	}
	if branch == allScopes {
		resolvedBranch = ""
	}
	return resolvedProject, resolvedBranch
}

func parsePeriod(period string) (time.Time, error) {
	var span time.Duration
	switch strings.TrimSpace(period) {
	case "", "1d":
		span = 24 * time.Hour
	case "7d":
		span = 7 * 24 * time.Hour
	case "30d":
		span = 30 * 24 * time.Hour
	case "all":
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("period must be 1d, 7d, 30d or all, got %q", period)
	}
	return time.Now().Add(-span), nil
}

// formatJourney renders nodes as plain lines grouped by project and branch.
// A journey is read often, so it avoids repeating field names as JSON would.
func formatJourney(nodes []store.Node) string {
	if len(nodes) == 0 {
		return "No journey nodes in this period."
	}
	var b strings.Builder
	var heading string
	for _, n := range nodes {
		if h := n.Project + " @ " + n.Branch; h != heading {
			if heading != "" {
				b.WriteByte('\n')
			}
			heading = h
			fmt.Fprintf(&b, "## %s\n", h)
		}
		fmt.Fprintf(&b, "%s %s %s", n.CreatedAt.Local().Format(timeLayout), n.ID, n.Title)
		if n.Agent != "" {
			fmt.Fprintf(&b, " [%s]", n.Agent)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// New builds the MCP server with the memory and journey tools registered.
// aliases are this machine's ref schemes; only their names reach the AI.
func New(s store.Store, aliases map[string]string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "8memory", Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_write",
		Description: "Save what one prompt did to one feature: added, removed, modified, or only inspected. Call once per feature. Content is limited to 16 KB; reference large files through files.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writeInput) (*mcp.CallToolResult, store.Memory, error) {
		if strings.TrimSpace(in.Change) == "" || strings.TrimSpace(in.Feature) == "" {
			return nil, store.Memory{}, errors.New("change and feature are required")
		}
		if err := checkContent(in.Content, aliases); err != nil {
			return nil, store.Memory{}, err
		}
		files, err := checkFiles(in.Files, aliases)
		if err != nil {
			return nil, store.Memory{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		here := detect(ctx, in.Cwd)
		saved, err := s.Write(ctx, store.Memory{
			Change: in.Change, Feature: in.Feature, Content: in.Content, NodeID: in.NodeID, Tags: in.Tags, Agent: in.Agent, Files: files,
			Project: firstNonEmpty(in.Project, here.Project), Branch: firstNonEmpty(in.Branch, here.Branch),
		})
		return nil, saved, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "memory_search",
		Description: "Search saved memories by keyword, or list the newest when the query is empty. Defaults to the current project and branch; pass branch * to read other branches.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		q := store.Query{Text: in.Query, Change: in.Change, NodeID: in.NodeID, Limit: min(in.Limit, maxSearchLimit)}
		if strings.TrimSpace(in.NodeID) == "" {
			q.Project, q.Branch = readScope(ctx, in.Cwd, in.Project, in.Branch)
		}
		found, err := s.Search(ctx, q)
		return nil, searchOutput{Memories: found}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "journey_mark",
		Description: "Create the journey node for the current user prompt and return its UUID. Skip this when the context already gives a node id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in markInput) (*mcp.CallToolResult, store.Node, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		here := detect(ctx, in.Cwd)
		node, err := s.AddNode(ctx, store.Node{
			Title: in.Title, Agent: in.Agent,
			Project: firstNonEmpty(in.Project, here.Project), Branch: firstNonEmpty(in.Branch, here.Branch),
		})
		return nil, node, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "journey_list",
		Description: "List the timeline of user prompts, newest first, grouped by project and branch. Use memory_search with node_id for the details of one node.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in journeyInput) (*mcp.CallToolResult, any, error) {
		since, err := parsePeriod(in.Period)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		project, branch := readScope(ctx, in.Cwd, in.Project, in.Branch)
		nodes, err := s.ListNodes(ctx, store.NodeQuery{Project: project, Branch: branch, Since: since, Limit: min(in.Limit, maxJourneyLimit)})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: formatJourney(nodes)}}}, nil, nil
	})

	return server
}
