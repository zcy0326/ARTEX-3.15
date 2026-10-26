package workproduct

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneralArtifactSnapshotIntegrityAndContainment(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "output.bin")
	if err := os.WriteFile(source, []byte("first\x00version"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Capture(t.Context(), root, root, "output.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("modified original"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenVerified(root, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(got) != "first\x00version" {
		t.Fatalf("snapshot changed: %q %v", got, err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err = os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Capture(t.Context(), root, root, outside); err == nil {
		t.Fatal("accepted outside path")
	}
	if err = os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = Capture(t.Context(), root, root, "link"); err == nil {
		t.Fatal("accepted escaping symlink")
	}
	if err = os.WriteFile(filepath.Join(root, filepath.FromSlash(a.Snapshot)), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenVerified(root, a); err == nil {
		t.Fatal("accepted tampered snapshot")
	}
}

func TestGeneralArtifactCancellationAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Capture(t.Context(), root, root, "."); err == nil {
		t.Fatal("accepted directory")
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Capture(ctx, root, root, "file"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestGeneralWorktreePreservesSourceAndPinsBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	repo, root := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return string(out)
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.txt")
	git("commit", "-qm", "baseline")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("user edits"), 0600); err != nil {
		t.Fatal(err)
	}
	work, err := PrepareWorktree(t.Context(), repo, root, 17)
	if err != nil {
		t.Fatal(err)
	}
	if !work.SourceDirty || work.BaseCommit+"\n" != base {
		t.Fatalf("wrong base: %+v", work)
	}
	got, _ := os.ReadFile(filepath.Join(work.Path, "main.txt"))
	if string(got) != "baseline" {
		t.Fatalf("worktree: %s", got)
	}
	got, _ = os.ReadFile(filepath.Join(repo, "main.txt"))
	if string(got) != "user edits" {
		t.Fatal("lost user edits")
	}
	if _, err = PrepareWorktree(t.Context(), repo, root, 17); err == nil {
		t.Fatal("overwrote existing worktree")
	}
	git("worktree", "remove", "--force", work.Path)
	// A later repository HEAD must not replace a requested predecessor baseline.
	git("add", "main.txt")
	git("commit", "-qm", "later project change")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("more user edits"), 0600); err != nil {
		t.Fatal(err)
	}
	work, err = PrepareWorktreeAt(t.Context(), repo, root, 18, strings.TrimSpace(base))
	if err != nil || work.BaseCommit != strings.TrimSpace(base) || !work.SourceDirty {
		t.Fatalf("explicit base: %+v %v", work, err)
	}
	got, _ = os.ReadFile(filepath.Join(work.Path, "main.txt"))
	if string(got) != "baseline" {
		t.Fatalf("used later HEAD instead of requested base: %s", got)
	}
	got, _ = os.ReadFile(filepath.Join(repo, "main.txt"))
	if string(got) != "more user edits" {
		t.Fatal("explicit base lost source edits")
	}
	git("worktree", "remove", "--force", work.Path)
	for _, invalid := range []string{"missing-ref", "--help"} {
		if _, err := PrepareWorktreeAt(t.Context(), repo, root, 19, invalid); err == nil {
			t.Fatalf("accepted invalid baseline %q", invalid)
		}
	}
}
