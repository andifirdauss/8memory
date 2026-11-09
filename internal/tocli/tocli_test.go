package tocli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const testNode = "0195f3a2-7c10-7000-8000-000000000001"

func provider(t *testing.T, id string) Provider {
	t.Helper()
	for _, p := range Providers {
		if p.ID() == id {
			return p
		}
	}
	t.Fatalf("no provider %s", id)
	return nil
}

func TestClaudeArguments(t *testing.T) {
	paste := NewPaste(1, "isi")
	paste.SavedPath = "/a/paste-1.txt"
	image := NewImage(2, []byte("x"))
	image.SavedPath = "/a/image-2.png"
	args := provider(t, "claude").Args(Request{
		System: "ATURAN", Turn: "KONTEKS", AttachDir: "/a", Attachments: []Attachment{paste, image},
		Prompt: "lihat " + paste.Marker + " dan " + image.Marker,
	})
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", "auto",
		"--append-system-prompt", "ATURAN", "--add-dir", "/a", "--", "KONTEKS\n\nlihat @/a/paste-1.txt dan /a/image-2.png",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("%q", args)
	}
}

func TestClaudeContinuesSessionAndAsksThroughTheTUI(t *testing.T) {
	args := provider(t, "claude").Args(Request{
		Prompt: "lanjut", Session: "sesi-1",
		Memory:   &MCPServer{Type: "stdio", Command: `C:\a\8memory.exe`, Args: []string{"serve", "--config", `C:\a\c.json`}},
		Approval: &MCPServer{Type: "http", URL: "http://127.0.0.1:1/mcp"},
	})
	for _, pair := range [][2]string{{"--resume", "sesi-1"}, {"--permission-prompt-tool", approvalToolRef}, {"--allowedTools", memoryTools}} {
		index := slices.Index(args, pair[0])
		if index < 0 || args[index+1] != pair[1] {
			t.Fatalf("%v missing in %q", pair, args)
		}
	}
	var config struct {
		Servers map[string]MCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[slices.Index(args, "--mcp-config")+1]), &config); err != nil {
		t.Fatal(err)
	}
	if memory := config.Servers[memoryServerName]; memory.Type != "stdio" || !slices.Equal(memory.Args, []string{"serve", "--config", `C:\a\c.json`}) {
		t.Fatalf("%+v", config)
	}
	if config.Servers[approvalServerName].URL == "" {
		t.Fatalf("%+v", config)
	}
	if strings.Contains(memoryTools, "journey_mark") {
		t.Fatal("ToCLI creates the node itself")
	}
	if slices.Contains(args, "--append-system-prompt") {
		t.Fatalf("empty rules must not be sent: %q", args)
	}
}

func TestClaudeKeepsSlashCommandFirst(t *testing.T) {
	args := provider(t, "claude").Args(Request{Turn: "KONTEKS", Prompt: "/mcp"})
	if args[len(args)-1] != "/mcp" {
		t.Fatalf("%q", args)
	}
}

func TestClaudeOmitsAddDirWithoutAttachments(t *testing.T) {
	args := provider(t, "claude").Args(Request{System: "K", Prompt: "- mulai dengan strip"})
	if slices.Contains(args, "--add-dir") || args[len(args)-2] != "--" {
		t.Fatalf("%q", args)
	}
}

func TestClaudeQuotesPathsWithSpaces(t *testing.T) {
	paste := NewPaste(1, "isi")
	paste.SavedPath = "/nama saya/p.txt"
	args := provider(t, "claude").Args(Request{Prompt: paste.Marker, Attachments: []Attachment{paste}, AttachDir: "/d"})
	if got := args[len(args)-1]; got != `"/nama saya/p.txt"` {
		t.Fatal(got)
	}
}

func TestCodexArguments(t *testing.T) {
	image := NewImage(1, []byte("x"))
	image.SavedPath = "/a/image-1.png"
	paste := NewPaste(2, "isi")
	paste.SavedPath = "/a/paste-2.txt"
	request := Request{
		Full: "KONTEKS", Attachments: []Attachment{image, paste},
		Prompt: "lihat " + image.Marker + " " + paste.Marker,
	}
	common := []string{"--json", "--skip-git-repo-check", "-c", `sandbox_mode="workspace-write"`, "--image", "/a/image-1.png", "--"}
	want := append(append([]string{"exec"}, common...), "KONTEKS\n\nlihat [image 1]")
	if args := provider(t, "codex").Args(request); !slices.Equal(args, want) {
		t.Fatalf("%q", args)
	}
	request.Session = "sesi-1"
	want = append(append([]string{"exec", "resume"}, common...), "sesi-1", "KONTEKS\n\nlihat [image 1]")
	if args := provider(t, "codex").Args(request); !slices.Equal(args, want) {
		t.Fatalf("%q", args)
	}
}

func TestCodexConnectsMemoryThroughConfigOverrides(t *testing.T) {
	args := provider(t, "codex").Args(Request{
		Full: "K", Prompt: "p",
		Memory: &MCPServer{Type: "stdio", Command: `C:\a\8memory.exe`, Args: []string{"serve"}},
	})
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		`mcp_servers.8memory.command="C:\\a\\8memory.exe"`,
		`mcp_servers.8memory.args=["serve"]`,
		`mcp_servers.8memory.default_tools_approval_mode="approve"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %q", want, args)
		}
	}
	if slices.Index(args, "--") < slices.Index(args, "-c") {
		t.Fatalf("%q", args)
	}
}

func TestWarningsNameEveryUnsupportedAttachment(t *testing.T) {
	paste := NewPaste(1, "isi")
	image := NewImage(2, []byte("x"))
	warnings := Warnings(provider(t, "codex"), []Attachment{paste, image})
	if len(warnings) != 1 || !strings.Contains(warnings[0], paste.Marker) || !strings.Contains(warnings[0], "TIDAK dikirim") {
		t.Fatalf("%q", warnings)
	}
	if got := Warnings(provider(t, "claude"), []Attachment{paste, image}); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	big := NewPaste(3, strings.Repeat("x", claudeMentionLimit+1))
	if got := Warnings(provider(t, "claude"), []Attachment{big}); len(got) != 1 || !strings.Contains(got[0], "256 KB") {
		t.Fatalf("%q", got)
	}
}

func fakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func isolate(t *testing.T) (path, home string) {
	t.Helper()
	path, home = t.TempDir(), t.TempDir()
	t.Setenv("PATH", path)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return path, home
}

func TestFindReportsMissingCLI(t *testing.T) {
	isolate(t)
	_, err := Find(provider(t, "codex"))
	var missing *NotFoundError
	if !errors.As(err, &missing) || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Fatal(err)
	}
}

func TestFindUsesPathThenInstallFolder(t *testing.T) {
	path, home := isolate(t)
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeBinary(t, local, "claude")
	found, err := Find(provider(t, "claude"))
	if err != nil || filepath.Dir(found.Path) != local {
		t.Fatalf("%+v, %v", found, err)
	}
	fakeBinary(t, path, "claude")
	found, err = Find(provider(t, "claude"))
	if err != nil || filepath.Dir(found.Path) != path {
		t.Fatalf("%+v, %v", found, err)
	}
}

func TestResolveShimRunsNodeDirectly(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "codex.cmd")
	content := "@ECHO off\r\nSET dp0=%~dp0\r\nendLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n"
	if err := os.WriteFile(shim, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := resolveShim(shim)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "node_modules", "@openai", "codex", "bin", "codex.js")
	if !strings.HasSuffix(strings.ToLower(exe.Path), "node.exe") && filepath.Base(exe.Path) != "node" {
		t.Fatal(exe.Path)
	}
	if len(exe.Prefix) != 1 || exe.Prefix[0] != want {
		t.Fatalf("%q", exe.Prefix)
	}
}

func TestResolveShimRefusesUnknownBatch(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "tool.bat")
	if err := os.WriteFile(shim, []byte("@echo %*"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveShim(shim); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatal(err)
	}
}

func TestBuildSetsDirectoryAndNodeEnv(t *testing.T) {
	dir := t.TempDir()
	cmd, err := Build(context.Background(), provider(t, "claude"), Executable{Path: "claude"}, dir, testNode, Request{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != dir || !slices.Contains(cmd.Env, EnvNode+"="+testNode) {
		t.Fatalf("%+v", cmd)
	}
	if _, err := Build(context.Background(), provider(t, "claude"), Executable{Path: "claude"}, dir, testNode, Request{Prompt: strings.Repeat("x", maxCommandLine)}); err == nil {
		t.Fatal("accepted a command line over the limit")
	}
}

func TestLongPasteThresholds(t *testing.T) {
	if IsLongPaste("a\nb", 20, 2048) {
		t.Fatal("short paste")
	}
	if !IsLongPaste(strings.Repeat("a\n", 20), 20, 2048) {
		t.Fatal("20 lines")
	}
	if !IsLongPaste(strings.Repeat("a", 2048), 20, 2048) {
		t.Fatal("2 KB")
	}
	if !IsLongPaste("a\nb\nc", 3, 2048) {
		t.Fatal("configured lines")
	}
}

func TestNormalizePaste(t *testing.T) {
	if got := NormalizePaste("a\r\nb\rc\x00d"); got != "a\nb\ncd" {
		t.Fatalf("%q", got)
	}
}

func TestPasteMarkerCountsLines(t *testing.T) {
	if got := NewPaste(1, strings.Repeat("x\n", 120)).Marker; got != "[Paste #1 · 120 baris]" {
		t.Fatal(got)
	}
}

func TestNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cat[a]tan 1.TXT")
	if err := os.WriteFile(path, []byte("isi"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewFile(4, `"`+path+`"`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindFile || a.SourcePath != path || a.Marker != "[File #4 · cat(a)tan 1.TXT]" || strings.ContainsAny(a.Name, " []") {
		t.Fatalf("%+v", a)
	}
	image := filepath.Join(dir, "layar.PNG")
	if err := os.WriteFile(image, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := NewFile(5, image); err != nil || b.Kind != KindImage {
		t.Fatalf("%+v, %v", b, err)
	}
	for _, bad := range []string{"", dir, filepath.Join(dir, "tidak-ada")} {
		if _, err := NewFile(6, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSaveAndRemove(t *testing.T) {
	base, source := t.TempDir(), filepath.Join(t.TempDir(), "asal.txt")
	if err := os.WriteFile(source, []byte("isi file"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := NewFile(2, source)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Save(base, testNode, []Attachment{NewPaste(1, "isi paste"), file})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"isi paste", "isi file"} {
		if filepath.Dir(saved[i].SavedPath) != filepath.Join(base, testNode) {
			t.Fatal(saved[i].SavedPath)
		}
		if data, err := os.ReadFile(saved[i].SavedPath); err != nil || string(data) != want {
			t.Fatalf("%q, %v", data, err)
		}
	}
	if !HasSaved(base, testNode) {
		t.Fatal("HasSaved")
	}
	if err := RemoveSaved(base, testNode); err != nil || HasSaved(base, testNode) {
		t.Fatal(err)
	}
}

func TestSavedPathsCannotEscape(t *testing.T) {
	base := t.TempDir()
	if err := RemoveSaved(base, "../.."); err == nil {
		t.Fatal("accepted a traversal id")
	}
	if _, err := Save(base, "../x", []Attachment{NewPaste(1, "a")}); err == nil {
		t.Fatal("accepted a traversal id")
	}
	if ClaimPending(base, "../x") {
		t.Fatal("claimed a traversal id")
	}
}

func TestPendingIsClaimedOnce(t *testing.T) {
	base := t.TempDir()
	if ClaimPending(base, testNode) {
		t.Fatal("claimed without a marker")
	}
	if err := MarkPending(base, testNode); err != nil {
		t.Fatal(err)
	}
	if !ClaimPending(base, testNode) || ClaimPending(base, testNode) {
		t.Fatal("marker must be claimed exactly once")
	}
}

func TestTitleUsesTypedTextOnly(t *testing.T) {
	paste := NewPaste(1, "BARIS RAHASIA DARI PASTE")
	if got := Title("\n"+paste.Marker+"  perbaiki   bug\nbaris dua", []Attachment{paste}); got != "perbaiki bug" {
		t.Fatal(got)
	}
	if got := Title(paste.Marker, []Attachment{paste}); got != "(hanya attachment)" {
		t.Fatal(got)
	}
}

func TestPreview(t *testing.T) {
	if got := Preview(NewPaste(1, "halo dunia")); !strings.Contains(got, "halo dunia") {
		t.Fatal(got)
	}
	if got := Preview(NewImage(2, []byte("\x89PNG"))); strings.Contains(got, "PNG") || !strings.Contains(got, "tidak bisa ditampilkan") {
		t.Fatal(got)
	}
	long := filepath.Join(t.TempDir(), "panjang.txt")
	if err := os.WriteFile(long, []byte(strings.Repeat("认", previewBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewFile(3, long)
	if err != nil {
		t.Fatal(err)
	}
	if got := Preview(a); !strings.Contains(got, "认") {
		t.Fatal(got)
	}
}

func TestValidatePNG(t *testing.T) {
	if _, err := validatePNG(nil); !errors.Is(err, ErrNoImage) {
		t.Fatal(err)
	}
	if _, err := validatePNG([]byte("bukan")); err == nil {
		t.Fatal("accepted non-PNG data")
	}
	if got, err := validatePNG([]byte(string(pngSignature) + "x")); err != nil || len(got) != 9 {
		t.Fatal(err)
	}
}

func TestReadClipboardDoesNotFailWhenNoImage(t *testing.T) {
	if os.Getenv("MEMORY8_TEST_CLIPBOARD") == "" {
		t.Skip("reads the real clipboard")
	}
	data, err := ReadClipboardImage(context.Background())
	if err != nil && !errors.Is(err, ErrNoImage) {
		t.Fatal(err)
	}
	t.Logf("%d bytes", len(data))
}
