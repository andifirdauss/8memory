package gitinfo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my-project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-b", "main")
	run(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func TestDetectFromSubdirectory(t *testing.T) {
	dir := repo(t)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	info, ok := Detect(context.Background(), sub)
	if !ok || info != (Info{Project: "my-project", Branch: "main"}) {
		t.Fatalf("%+v, %v", info, ok)
	}
}

func TestDetectFollowsBranchSwitch(t *testing.T) {
	dir := repo(t)
	run(t, dir, "switch", "-c", "feature/journey")
	info, _ := Detect(context.Background(), dir)
	if info.Branch != "feature/journey" {
		t.Fatalf("%+v", info)
	}
}

func TestWorktreeKeepsProjectName(t *testing.T) {
	dir := repo(t)
	tree := filepath.Join(t.TempDir(), "folder-worktree")
	run(t, dir, "worktree", "add", "-b", "wt-branch", tree)
	info, ok := Detect(context.Background(), tree)
	if !ok || info != (Info{Project: "my-project", Branch: "wt-branch"}) {
		t.Fatalf("%+v, %v", info, ok)
	}
}

func TestDetachedHeadGetsStableName(t *testing.T) {
	dir := repo(t)
	run(t, dir, "switch", "--detach")
	info, _ := Detect(context.Background(), dir)
	if len(info.Branch) <= len("detached-") || info.Branch[:9] != "detached-" {
		t.Fatalf("%+v", info)
	}
}

func TestForDirOutsideGitUsesFolderName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-git")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := Detect(context.Background(), dir); ok {
		t.Fatal("detected git outside a repository")
	}
	if info := ForDir(context.Background(), dir); info != (Info{Project: "not-git"}) {
		t.Fatalf("%+v", info)
	}
}
