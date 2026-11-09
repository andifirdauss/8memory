// Package gitinfo works out which project and branch a directory belongs to.
package gitinfo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitTimeout = 3 * time.Second

// Info identifies where work is happening. Branch is empty outside git.
type Info struct {
	Project string
	Branch  string
}

// Detect reads the project and current branch of dir, reporting false when
// dir is not inside a git work tree or git is unavailable.
func Detect(ctx context.Context, dir string) (Info, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir", "--show-toplevel")
	if err != nil {
		return Info{}, false
	}
	paths := strings.Split(out, "\n")
	if len(paths) != 2 {
		return Info{}, false
	}
	// A linked worktree has its own top-level folder, but it is still the same
	// project; the common .git directory names the main checkout.
	project := filepath.Base(paths[1])
	if filepath.Base(paths[0]) == ".git" {
		project = filepath.Base(filepath.Dir(paths[0]))
	}

	branch, _ := git(ctx, dir, "branch", "--show-current")
	if branch == "" {
		if sha, err := git(ctx, dir, "rev-parse", "--short", "HEAD"); err == nil && sha != "" {
			branch = "detached-" + sha
		}
	}
	return Info{Project: project, Branch: branch}, true
}

// ForDir is Detect for a directory known to be a project: outside git, the
// folder name becomes the project.
func ForDir(ctx context.Context, dir string) Info {
	if info, ok := Detect(ctx, dir); ok {
		return info
	}
	return Info{Project: filepath.Base(filepath.Clean(dir))}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	hideConsole(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), err
}
