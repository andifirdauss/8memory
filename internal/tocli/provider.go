// Package tocli sends each prompt typed in the TUI, with its briefing and
// attachments, to the real CLI of an AI provider and reads the reply back.
package tocli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvNode tells a prompt-submit hook that ToCLI already created the node for
// the prompt being run.
const EnvNode = "MEMORY8_TOCLI_NODE"

// maxCommandLine stays under the 32,767 character limit of Windows.
const maxCommandLine = 30000

// Request is everything a provider needs to run one prompt.
type Request struct {
	Full        string // the whole briefing
	System      string // the part that stays the same between prompts
	Turn        string // the part specific to this prompt
	Prompt      string // typed text, still containing attachment markers
	Attachments []Attachment
	AttachDir   string
	Session     string     // the conversation to continue, empty for a new one
	Memory      *MCPServer // connects 8memory's own tools to the CLI
	Approval    *MCPServer // the permission host, if there is one
}

// MCPServer describes one server a CLI should connect to for a prompt.
type MCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

const (
	memoryServerName = "8memory"
	// memoryTools run without asking, so remembering never interrupts the user.
	// journey_mark is left out because ToCLI already created the node.
	memoryTools = "mcp__8memory__memory_search,mcp__8memory__memory_write,mcp__8memory__journey_list"
)

// marshal returns value as JSON. Only strings, slices and maps are passed.
func marshal(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// Provider adapts one AI CLI. To add a provider, implement this interface and
// add it to Providers.
type Provider interface {
	ID() string
	Label() string
	Binary() string
	InstallHint() string
	FallbackDirs(home string) []string
	// Supports reports whether the CLI has an official way to receive this kind.
	Supports(a Attachment) bool
	// Note warns about a supported attachment that may still not arrive in full.
	Note(a Attachment) string
	// AsksApproval reports whether the CLI can ask the TUI about a tool call.
	AsksApproval() bool
	Args(r Request) []string
	NewParser() Parser
	// UsageArgs returns the command that reports usage limits, nil if there is none.
	UsageArgs() []string
}

// Providers lists the CLIs offered in the TUI.
var Providers = []Provider{claudeCode{}, codex{}}

// NotFoundError means a provider's CLI is not installed or not reachable.
type NotFoundError struct{ Provider Provider }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("CLI %s (%s) tidak ditemukan di PATH. %s", e.Provider.Label(), e.Provider.Binary(), e.Provider.InstallHint())
}

// Executable is a resolved program and the arguments that must precede any
// others.
type Executable struct {
	Path   string
	Prefix []string
}

// Find locates a provider's CLI on PATH, then in its usual install folders.
func Find(p Provider) (Executable, error) {
	path, err := exec.LookPath(p.Binary())
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return Executable{}, &NotFoundError{Provider: p}
		}
		found := false
		for _, dir := range p.FallbackDirs(home) {
			if candidate, lookErr := exec.LookPath(filepath.Join(dir, p.Binary())); lookErr == nil {
				path, found = candidate, true
				break
			}
		}
		if !found {
			return Executable{}, &NotFoundError{Provider: p}
		}
	}
	return resolveShim(path)
}

var npmShimTarget = regexp.MustCompile(`(?i)%~?dp0%?\\([^"\r\n]+\.[cm]?js)"`)

// resolveShim turns an npm .cmd wrapper into the Node command behind it.
// Windows runs a batch file through cmd.exe, which reinterprets characters
// such as & and % inside arguments, so a prompt could run commands. Arbitrary
// prompt text must therefore never reach a batch file.
func resolveShim(path string) (Executable, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".cmd" && extension != ".bat" {
		return Executable{Path: path}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Executable{}, fmt.Errorf("baca %s: %w", path, err)
	}
	match := npmShimTarget.FindSubmatch(content)
	if match == nil {
		return Executable{}, fmt.Errorf("%s adalah skrip batch yang tidak dikenal, dan prompt tidak aman dikirim lewat batch", path)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return Executable{}, errors.New("node tidak ditemukan, padahal CLI ini dipasang lewat npm")
	}
	script := filepath.Join(filepath.Dir(path), filepath.FromSlash(strings.ReplaceAll(string(match[1]), `\`, "/")))
	return Executable{Path: node, Prefix: []string{script}}, nil
}

// Warnings lists what will not reach the CLI as the user expects. It is shown
// before sending, so nothing is dropped silently.
func Warnings(p Provider, attachments []Attachment) []string {
	var out []string
	for _, a := range attachments {
		switch {
		case !p.Supports(a):
			out = append(out, fmt.Sprintf("%s: %s belum punya cara resmi menerima %s, jadi attachment ini TIDAK dikirim.", a.Marker, p.Label(), a.Kind))
		case p.Note(a) != "":
			out = append(out, a.Marker+": "+p.Note(a))
		}
	}
	return out
}

// Build creates the command that runs one prompt in dir. Cancelling ctx stops
// the CLI and whatever it started.
func Build(ctx context.Context, p Provider, exe Executable, dir, nodeID string, r Request) (*exec.Cmd, error) {
	args := append(append([]string{}, exe.Prefix...), p.Args(r)...)
	total := len(exe.Path)
	for _, arg := range args {
		total += len(arg) + 1
	}
	if total > maxCommandLine {
		return nil, fmt.Errorf("prompt terlalu panjang untuk baris perintah (%d karakter). Tempel teks panjang sebagai attachment", total)
	}
	cmd := exec.CommandContext(ctx, exe.Path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), EnvNode+"="+nodeID)
	prepareProcess(cmd)
	return cmd, nil
}

// BuildUsage creates the command that reports the provider's usage limits.
func BuildUsage(ctx context.Context, p Provider, exe Executable, dir string) *exec.Cmd {
	args := append(append([]string{}, exe.Prefix...), p.UsageArgs()...)
	cmd := exec.CommandContext(ctx, exe.Path, args...)
	cmd.Dir = dir
	prepareProcess(cmd)
	return cmd
}

// expandMarkers replaces each attachment marker using reference and removes
// the markers of attachments the provider cannot receive.
func expandMarkers(p Provider, r Request, reference func(Attachment) string) string {
	prompt := r.Prompt
	for _, a := range r.Attachments {
		replacement := ""
		if p.Supports(a) {
			replacement = reference(a)
		}
		prompt = strings.ReplaceAll(prompt, a.Marker, replacement)
	}
	return strings.TrimSpace(prompt)
}

type claudeCode struct{}

func (claudeCode) ID() string     { return "claude" }
func (claudeCode) Label() string  { return "Claude Code" }
func (claudeCode) Binary() string { return "claude" }
func (claudeCode) InstallHint() string {
	return "Pasang dari https://code.claude.com/docs/en/setup atau tambahkan foldernya ke PATH."
}

// The native installer puts the binary here without always updating PATH.
func (claudeCode) FallbackDirs(home string) []string {
	return []string{filepath.Join(home, ".local", "bin")}
}

func (claudeCode) Supports(Attachment) bool { return true }

const claudeMentionLimit = 256 * 1024

func (claudeCode) Note(a Attachment) string {
	if a.Kind != KindImage && a.Size > claudeMentionLimit {
		return "lebih dari 256 KB, jadi Claude Code tidak memasukkannya otomatis dan harus membacanya sendiri."
	}
	return ""
}

func (claudeCode) AsksApproval() bool { return true }

func (claudeCode) NewParser() Parser { return &claudeParser{} }

// UsageArgs runs /usage, which Claude Code answers locally without a model
// turn. Persistence is off because it is not part of any conversation.
func (claudeCode) UsageArgs() []string {
	return []string{"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--", "/usage"}
}

// Args uses @path for text and a plain path for images, the two forms Claude
// Code documents. "--" keeps the prompt from being read as a value of the
// flags that take several.
//
// Only rules go in the system prompt. A system prompt that changed on every
// prompt would invalidate the cache of the whole conversation, so what varies
// goes at the start of the message instead.
func (c claudeCode) Args(r Request) []string {
	prompt := expandMarkers(c, r, func(a Attachment) string {
		quoted := a.SavedPath
		if strings.Contains(quoted, " ") {
			return `"` + quoted + `"`
		}
		if a.Kind == KindImage {
			return quoted
		}
		return "@" + quoted
	})
	// Claude Code only recognizes a slash command at the very start.
	if r.Turn != "" && !strings.HasPrefix(prompt, "/") {
		prompt = r.Turn + "\n\n" + prompt
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", "auto"}
	if r.Session != "" {
		args = append(args, "--resume", r.Session)
	}
	servers := map[string]*MCPServer{}
	if r.Memory != nil {
		servers[memoryServerName] = r.Memory
		args = append(args, "--allowedTools", memoryTools)
	}
	if r.Approval != nil {
		servers[approvalServerName] = r.Approval
		args = append(args, "--permission-prompt-tool", approvalToolRef)
	}
	if len(servers) > 0 {
		args = append(args, "--mcp-config", marshal(map[string]any{"mcpServers": servers}))
	}
	if r.System != "" {
		args = append(args, "--append-system-prompt", r.System)
	}
	if len(r.Attachments) > 0 {
		args = append(args, "--add-dir", r.AttachDir)
	}
	return append(args, "--", prompt)
}

type codex struct{}

func (codex) ID() string     { return "codex" }
func (codex) Label() string  { return "Codex" }
func (codex) Binary() string { return "codex" }
func (codex) InstallHint() string {
	return "Pasang dengan: npm install -g @openai/codex"
}
func (codex) FallbackDirs(string) []string { return nil }

func (codex) Supports(a Attachment) bool { return a.Kind == KindImage }

func (codex) Note(Attachment) string { return "" }

// codex exec cannot ask anyone about a command; the sandbox is its limit.
func (codex) AsksApproval() bool { return false }

func (codex) NewParser() Parser { return &codexParser{} }

func (codex) UsageArgs() []string { return nil }

// Args sets the sandbox through -c because "exec resume" has no --sandbox
// flag. "--" ends --image, which takes several values and would otherwise
// swallow the prompt.
func (c codex) Args(r Request) []string {
	number := 0
	prompt := expandMarkers(c, r, func(Attachment) string {
		number++
		return fmt.Sprintf("[image %d]", number)
	})
	args := []string{"exec"}
	if r.Session != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check", "-c", `sandbox_mode="workspace-write"`)
	if r.Memory != nil {
		// A JSON string or list of strings is also valid TOML.
		args = append(args,
			"-c", "mcp_servers."+memoryServerName+".command="+marshal(r.Memory.Command),
			"-c", "mcp_servers."+memoryServerName+".args="+marshal(r.Memory.Args),
			"-c", "mcp_servers."+memoryServerName+`.default_tools_approval_mode="approve"`,
		)
	}
	for _, a := range r.Attachments {
		if c.Supports(a) {
			args = append(args, "--image", a.SavedPath)
		}
	}
	args = append(args, "--")
	if r.Session != "" {
		args = append(args, r.Session)
	}
	return append(args, r.Full+"\n\n"+prompt)
}
