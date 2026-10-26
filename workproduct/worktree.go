package workproduct

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Worktree struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	BaseCommit  string `json:"base_commit"`
	SourceDirty bool   `json:"source_dirty"`
}

// PrepareWorktree creates a branch per intent; it never resets/cleans the source
// repo and refuses an existing destination instead of overwriting user changes.
func PrepareWorktree(ctx context.Context, repo, taskRoot string, intentID int64) (Worktree, error) {
	return PrepareWorktreeAt(ctx, repo, taskRoot, intentID, "")
}

// PrepareWorktreeAt can resume an existing project or a preceding work item's
// committed change without silently switching its baseline to the current HEAD.
func PrepareWorktreeAt(ctx context.Context, repo, taskRoot string, intentID int64, revision string) (Worktree, error) {
	var result Worktree
	if !filepath.IsAbs(repo) || intentID <= 0 {
		return result, fmt.Errorf("repo 必须是绝对路径，且需要当前工作 ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git: %w: %.2000s", err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if revision == "" {
		revision = "HEAD"
	}
	base, err := git("rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return result, err
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return result, err
	}
	result = Worktree{Path: filepath.Join(taskRoot, fmt.Sprintf("worktree-%d", intentID)), Branch: fmt.Sprintf("artex/task-%s/work-%d", filepath.Base(taskRoot), intentID), BaseCommit: base, SourceDirty: status != ""}
	_, err = git("worktree", "add", "-b", result.Branch, "--", result.Path, base)
	return result, err
}
