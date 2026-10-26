// Package workproduct snapshots task outputs and prepares isolated Git workspaces.
package workproduct

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const MaxArtifactBytes = 64 << 20

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Artifact struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Snapshot string `json:"snapshot"`
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Capture copies bytes into a content-addressed task snapshot. OpenRoot confines
// reads and writes even when a path traverses symlinks; originals stay untouched.
func Capture(ctx context.Context, taskRoot, cwd, path string) (Artifact, error) {
	var artifact Artifact
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	rel, err := filepath.Rel(taskRoot, path)
	if err != nil {
		return artifact, err
	}
	root, err := os.OpenRoot(taskRoot)
	if err != nil {
		return artifact, err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return artifact, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
		return artifact, fmt.Errorf("仅支持不超过 64 MiB 的普通文件")
	}
	source, err := root.Open(rel)
	if err != nil {
		return artifact, fmt.Errorf("产物必须位于本任务目录：%w", err)
	}
	defer source.Close()
	info, err = source.Stat()
	if err != nil {
		return artifact, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
		return artifact, fmt.Errorf("仅支持不超过 64 MiB 的普通文件")
	}
	if err = root.MkdirAll(".artifacts", 0700); err != nil {
		return artifact, err
	}
	// An unpredictable temporary name avoids races between concurrent writers.
	tmp, err := os.CreateTemp(taskRoot, ".artifact-")
	if err != nil {
		return artifact, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(contextReader{ctx, source}, MaxArtifactBytes+1))
	if err != nil {
		return artifact, err
	}
	if size > MaxArtifactBytes {
		return artifact, fmt.Errorf("文件读取期间超过 64 MiB")
	}
	if err = tmp.Sync(); err != nil {
		return artifact, err
	}
	if err = tmp.Close(); err != nil {
		return artifact, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	snapshot := filepath.Join(".artifacts", digest)
	if err = root.Rename(filepath.Base(tmp.Name()), snapshot); err != nil {
		return artifact, err
	}
	artifact = Artifact{filepath.Base(path), digest, size, filepath.ToSlash(snapshot)}
	return artifact, nil
}

// OpenVerified detects modified/missing snapshots before download or validation.
func OpenVerified(taskRoot string, artifact Artifact) (*os.File, error) {
	if !digestPattern.MatchString(artifact.SHA256) || artifact.Size < 0 || artifact.Size > MaxArtifactBytes {
		return nil, fmt.Errorf("产物摘要无效")
	}
	root, err := os.OpenRoot(taskRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	snapshot := filepath.Join(".artifacts", artifact.SHA256)
	info, err := root.Stat(snapshot)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("产物快照不是有效文件")
	}
	f, err := root.Open(snapshot)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, MaxArtifactBytes+1))
	if err != nil || n != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		f.Close()
		return nil, fmt.Errorf("产物快照已改变，不能沿用旧版本的验证")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
