package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// GitRepository is a checked location, not a lease or permission to mutate the
// source. Recheck under the engine's source/mirror lock before capture. Linked
// worktrees, external object stores and lazy-fetch repositories are unsupported.
type GitRepository struct {
	Path         string
	GitDir       string
	Bare         bool
	ObjectFormat string
}

func (r *GitRunner) ValidateGitRepository(ctx context.Context, path string) (GitRepository, error) {
	var repo GitRepository
	if err := ctx.Err(); err != nil {
		return repo, err
	}
	root, err := safeDirectory(path)
	if err != nil {
		return repo, ErrUnsafeRepository
	}
	gitdir := root
	bare := true
	if info, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return repo, ErrUnsafeRepository
		}
		gitdir = filepath.Join(root, ".git")
		bare = false
	} else if !os.IsNotExist(err) {
		return repo, ErrUnsafeRepository
	}
	for _, name := range []string{"HEAD", "config", "objects", "objects/info", "objects/pack", "refs"} {
		info, err := os.Lstat(filepath.Join(gitdir, filepath.FromSlash(name)))
		if os.IsNotExist(err) && (name == "objects/info" || name == "objects/pack") {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return repo, ErrUnsafeRepository
		}
		if (name == "HEAD" || name == "config") && !info.Mode().IsRegular() {
			return repo, ErrUnsafeRepository
		}
		if name == "config" && info.Size() > 1<<20 {
			return repo, ErrUnsafeRepository
		}
		if name != "HEAD" && name != "config" && !info.IsDir() {
			return repo, ErrUnsafeRepository
		}
	}
	for _, name := range []string{"commondir", "config.worktree", "shallow", "objects/info/alternates", "objects/info/http-alternates"} {
		if _, err := os.Lstat(filepath.Join(gitdir, filepath.FromSlash(name))); !os.IsNotExist(err) {
			return repo, ErrUnsafeRepository
		}
	}
	// --file and --no-includes inspect only this bounded config. Do not first
	// run rev-parse in an uninspected repository (which reads local includes).
	out, err := r.command(ctx, "", gitOffline, nil, "config", "--file", filepath.Join(gitdir, "config"), "--no-includes", "--null", "--list")
	if err != nil {
		return repo, err
	}
	format := "sha1"
	for _, record := range strings.Split(string(out.stdout), "\x00") {
		if record == "" {
			continue
		}
		key, value, _ := strings.Cut(record, "\n")
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") || key == "extensions.worktreeconfig" || key == "extensions.partialclone" || key == "core.worktree" || strings.HasSuffix(key, ".promisor") || strings.HasSuffix(key, ".partialclonefilter") || key == "core.alternaterefscommand" || strings.HasPrefix(key, "extensions.") && key != "extensions.objectformat" {
			return repo, ErrUnsafeRepository
		}
		if key == "extensions.objectformat" {
			if value != "sha1" && value != "sha256" {
				return repo, ErrGitUnsupported
			}
			format = value
		}
	}
	out, err = r.command(ctx, root, gitOffline, nil, "rev-parse", "--absolute-git-dir", "--is-bare-repository", "--show-object-format")
	if err != nil {
		return repo, err
	}
	expectedBare := "false"
	if bare {
		expectedBare = "true"
	}
	if string(out.stdout) != gitdir+"\n"+expectedBare+"\n"+format+"\n" {
		return repo, ErrUnsafeRepository
	}
	return GitRepository{root, gitdir, bare, format}, nil
}
