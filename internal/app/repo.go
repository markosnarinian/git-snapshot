package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Repository struct {
	Path         string
	TopLevel     string
	GitDir       string
	CommonDir    string
	IndexPath    string
	Bare         bool
	Linked       bool
	Head         string
	HeadUnborn   bool
	BranchRef    string
	ObjectFormat string
}

func DiscoverRepository(ctx context.Context, path string, verbose bool, traceWriter interface{ Write([]byte) (int, error) }) (*Repository, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fail(ExitUsage, "invalid repository path", "Choose an existing Git repository.", err)
	}
	git := Git{Repo: abs, Verbose: verbose, Trace: traceWriter}
	gitDir, err := git.Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return nil, fail(ExitNotFound, fmt.Sprintf("%s is not a Git repository", abs), "Pass --repo with a working tree or bare repository.", err)
	}
	commonDir, err := git.Run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fail(ExitFailure, "could not locate the common Git directory", "Run git rev-parse --git-common-dir to diagnose the repository.", err)
	}
	bareText, err := git.Run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil {
		return nil, fail(ExitFailure, "could not determine whether the repository is bare", "Check repository permissions and retry.", err)
	}
	repo := &Repository{
		Path:      abs,
		GitDir:    filepath.Clean(gitDir),
		CommonDir: filepath.Clean(commonDir),
		Bare:      bareText == "true",
	}
	repo.Linked = repo.GitDir != repo.CommonDir
	if !repo.Bare {
		repo.TopLevel, err = git.Run(ctx, "rev-parse", "--path-format=absolute", "--show-toplevel")
		if err != nil {
			return nil, fail(ExitFailure, "could not locate the working-tree root", "Check the linked worktree metadata and retry.", err)
		}
		repo.Path = repo.TopLevel
	}
	repo.IndexPath, _ = git.Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	repo.ObjectFormat, err = git.Run(ctx, "rev-parse", "--show-object-format")
	if err != nil {
		repo.ObjectFormat = "sha1"
	}
	repo.Head, err = git.Run(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		repo.Head = ""
		repo.HeadUnborn = true
	}
	repo.BranchRef, _ = git.Run(ctx, "symbolic-ref", "-q", "HEAD")
	return repo, nil
}

func (r *Repository) Git(verbose bool, traceWriter interface{ Write([]byte) (int, error) }) Git {
	return Git{Repo: r.Path, Verbose: verbose, Trace: traceWriter}
}

func (r *Repository) ZeroOID() string {
	if r.ObjectFormat == "sha256" {
		return strings.Repeat("0", 64)
	}
	return strings.Repeat("0", 40)
}

func (r *Repository) RequireWorktree() error {
	if r.Bare {
		return fail(ExitSafety, "this operation requires a working tree, but the repository is bare", "Use list, show, verify, drop, delete, or restore --destination instead.", nil)
	}
	return nil
}

func (r *Repository) CheckLocks(ref string, includeIndex bool) error {
	paths := []string{filepath.Join(r.CommonDir, "packed-refs.lock")}
	if includeIndex && r.IndexPath != "" {
		paths = append(paths, r.IndexPath+".lock")
	}
	if strings.HasPrefix(ref, "refs/") {
		paths = append(paths, filepath.Join(r.CommonDir, filepath.FromSlash(ref))+".lock")
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fail(ExitSafety, fmt.Sprintf("Git lock file exists: %s", path), "Wait for the other Git operation to finish; remove a stale lock only after confirming no Git process is active.", nil)
		}
	}
	return nil
}

func (r *Repository) InProgressOperations() []string {
	checks := map[string][]string{
		"merge":       {"MERGE_HEAD"},
		"rebase":      {"rebase-merge", "rebase-apply"},
		"cherry-pick": {"CHERRY_PICK_HEAD"},
		"revert":      {"REVERT_HEAD"},
		"bisect":      {"BISECT_LOG"},
		"sequencer":   {"sequencer"},
	}
	var active []string
	for name, markers := range checks {
		for _, marker := range markers {
			for _, root := range []string{r.GitDir, r.CommonDir} {
				if _, err := os.Lstat(filepath.Join(root, marker)); err == nil {
					active = append(active, name)
					goto nextOperation
				}
			}
		}
	nextOperation:
	}
	return active
}
