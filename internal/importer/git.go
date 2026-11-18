package importer

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CloneOrPullRepo clones a remote Git repository (shallow depth=1) into cacheDir,
// or updates an existing cached clone via `git pull`.
func CloneOrPullRepo(ctx context.Context, repoURL, cacheDir string) (string, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", errors.New("repository URL cannot be empty")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	slug := sanitizeRepoSlug(repoURL)
	targetDir := filepath.Join(cacheDir, slug)

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}

	if _, err := os.Stat(filepath.Join(targetDir, ".git")); err == nil {
		cmd := exec.CommandContext(ctx, "git", "-C", targetDir, "pull", "--ff-only")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// Fall back to fresh clone if pull fails (e.g., force-pushed history)
			_ = os.RemoveAll(targetDir)
		} else {
			return targetDir, nil
		}
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", repoURL, targetDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %q: %w (%s)", repoURL, err, strings.TrimSpace(stderr.String()))
	}
	return targetDir, nil
}

// ScanGitRepo clones/pulls repoURL into cacheDir and scans all KiCad symbols, footprints, and 3D models inside it.
func ScanGitRepo(ctx context.Context, repoURL, cacheDir, kicadCLIPath string) (*ScanResult, string, error) {
	repoDir, err := CloneOrPullRepo(ctx, repoURL, cacheDir)
	if err != nil {
		return nil, "", err
	}
	res, err := ScanDirectory(repoDir, kicadCLIPath, "git", repoURL)
	if err != nil {
		return nil, repoDir, err
	}
	return res, repoDir, nil
}

func sanitizeRepoSlug(repoURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(repoURL), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	base := filepath.Base(trimmed)
	var sb strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	h := sha1.Sum([]byte(repoURL))
	shortHash := hex.EncodeToString(h[:4])
	name := sb.String()
	if name == "" || name == "." {
		name = "repo"
	}
	return name + "-" + shortHash
}
