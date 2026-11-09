// Package tocli hands a prompt, its briefing and its attachments to the real
// CLI of an AI provider.
package tocli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvNode tells a prompt-submit hook that ToCLI already created the node for
// the session's first prompt.
const EnvNode = "MEMORY8_TOCLI_NODE"

// maxCommandLine stays under the 32,767 character limit of Windows.
const maxCommandLine = 30000

// Request is everything a provider needs to start a session.
type Request struct {
	Context     string
	Prompt      string // typed text, still containing attachment markers
	Attachments []Attachment
	AttachDir   string
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
	Args(r Request) []string
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

// Build creates the command for a session in dir.
func Build(p Provider, exe Executable, dir, nodeID string, r Request) (*exec.Cmd, error) {
	args := append(append([]string{}, exe.Prefix...), p.Args(r)...)
	total := len(exe.Path)
	for _, arg := range args {
		total += len(arg) + 1
	}
	if total > maxCommandLine {
		return nil, fmt.Errorf("prompt terlalu panjang untuk baris perintah (%d karakter). Tempel teks panjang sebagai attachment", total)
	}
	cmd := exec.Command(exe.Path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), EnvNode+"="+nodeID)
	return cmd, nil
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

// Args uses @path for text and a plain path for images, the two forms Claude
// Code documents. "--" keeps the prompt from being read as a --add-dir value.
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
	args := []string{"--append-system-prompt", r.Context}
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

// Args puts the prompt first, because --image takes one or more values and
// would otherwise treat the prompt as another image path.
func (c codex) Args(r Request) []string {
	number := 0
	prompt := expandMarkers(c, r, func(Attachment) string {
		number++
		return fmt.Sprintf("[image %d]", number)
	})
	args := []string{r.Context + "\n\n" + prompt}
	for _, a := range r.Attachments {
		if c.Supports(a) {
			args = append(args, "--image", a.SavedPath)
		}
	}
	return args
}
