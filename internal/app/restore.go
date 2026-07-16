package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RestoreOptions struct {
	Destination string
	Worktree    bool
	Overwrite   bool
	Force       bool
	Staged      bool
	DryRun      bool
}

type RestoreResult struct {
	OID         string   `json:"oid"`
	Destination string   `json:"destination"`
	Worktree    bool     `json:"worktree"`
	Staged      bool     `json:"staged"`
	DryRun      bool     `json:"dryRun"`
	Paths       []string `json:"paths"`
}

type treeEntry struct {
	Path string
	Mode string
}

func Restore(ctx context.Context, repo *Repository, git Git, snapshot *Snapshot, opts RestoreOptions) (*RestoreResult, error) {
	if opts.Worktree && opts.Destination != "" {
		return nil, fail(ExitUsage, "--worktree and --destination are mutually exclusive", "Choose either the current working tree or a separate destination.", nil)
	}
	if opts.Staged && !opts.Worktree {
		return nil, fail(ExitUsage, "--staged requires --worktree", "Use --worktree --staged to update both the working tree and real index.", nil)
	}
	if opts.Worktree {
		return restoreWorktree(ctx, repo, git, snapshot, opts)
	}
	if opts.Destination == "" {
		return nil, fail(ExitUsage, "safe restore requires --destination", "Pass a new destination directory, or explicitly choose --worktree.", nil)
	}
	return restoreDestination(ctx, repo, git, snapshot, opts)
}

func restoreDestination(ctx context.Context, repo *Repository, git Git, snapshot *Snapshot, opts RestoreOptions) (*RestoreResult, error) {
	destination, err := validateRestoreDestination(ctx, repo, opts.Destination, opts.Overwrite)
	if err != nil {
		return nil, err
	}
	treeEntries, err := listTreeEntries(ctx, git, snapshot.OID)
	if err != nil {
		return nil, err
	}
	entries := sortedTreePaths(treeEntries)
	result := &RestoreResult{OID: snapshot.OID, Destination: destination, DryRun: opts.DryRun, Paths: entries}
	if opts.DryRun {
		return result, nil
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fail(ExitFailure, "could not create the destination parent directory", "Check destination permissions and retry.", err)
	}
	stage, err := os.MkdirTemp(parent, ".git-snapshot-restore-")
	if err != nil {
		return nil, fail(ExitFailure, "could not create a private restore staging directory", "Check destination filesystem permissions and free space.", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return nil, fail(ExitFailure, "could not restrict restore staging permissions", "Use a filesystem supporting private directory permissions.", err)
	}
	if err := extractTree(ctx, git, snapshot.OID, stage); err != nil {
		return nil, err
	}
	info, statErr := os.Stat(destination)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := os.Rename(stage, destination); err != nil {
			return nil, fail(ExitFailure, "could not publish the restored destination", "Check that the destination is on a writable local filesystem.", err)
		}
		return result, nil
	}
	if statErr != nil {
		return nil, fail(ExitFailure, "could not inspect the restore destination", "Check destination permissions and retry.", statErr)
	}
	if !info.IsDir() {
		return nil, fail(ExitSafety, "restore destination is not a directory", "Choose a new directory path.", nil)
	}
	if err := copyTree(stage, destination); err != nil {
		return nil, failChanged(ExitFailure, "could not copy all restored files into the destination", "Inspect the destination; files copied before the filesystem error may have changed.", err)
	}
	return result, nil
}

func validateRestoreDestination(ctx context.Context, repo *Repository, destination string, overwrite bool) (string, error) {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return "", fail(ExitUsage, "invalid restore destination", "Choose a normal filesystem path.", err)
	}
	abs = filepath.Clean(abs)
	for _, metadata := range []string{repo.GitDir, repo.CommonDir} {
		if pathWithin(abs, metadata) {
			return "", fail(ExitSafety, fmt.Sprintf("restore destination %s is inside Git metadata %s", abs, metadata), "Choose a directory outside every Git metadata directory.", nil)
		}
	}
	ancestor := abs
	for {
		if _, err := os.Stat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	if metadata, err := (Git{Repo: ancestor}).Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir"); err == nil && pathWithin(abs, metadata) {
		return "", fail(ExitSafety, fmt.Sprintf("restore destination %s is inside repository metadata %s", abs, metadata), "Choose a directory outside every Git metadata directory.", nil)
	}
	if info, statErr := os.Stat(abs); statErr == nil {
		if !info.IsDir() {
			return "", fail(ExitSafety, "restore destination exists and is not a directory", "Choose a new or empty directory.", nil)
		}
		if _, gitErr := os.Lstat(filepath.Join(abs, ".git")); gitErr == nil {
			return "", fail(ExitSafety, "restore destination contains Git metadata", "Choose a directory that is not a repository metadata root.", nil)
		}
		entries, readErr := os.ReadDir(abs)
		if readErr != nil {
			return "", fail(ExitFailure, "could not inspect the restore destination", "Check destination permissions and retry.", readErr)
		}
		if len(entries) > 0 && !overwrite {
			return "", fail(ExitSafety, "restore destination is not empty", "Choose a new or empty directory, or explicitly pass --overwrite.", nil)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fail(ExitFailure, "could not inspect the restore destination", "Check parent directory permissions.", statErr)
	}
	return abs, nil
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func extractTree(ctx context.Context, git Git, oid, destination string) error {
	tempDir, err := os.MkdirTemp("", "git-snapshot-restore-index-")
	if err != nil {
		return fail(ExitFailure, "could not create a temporary restore index", "Check temporary-directory permissions and free space.", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return fail(ExitFailure, "could not restrict temporary restore-index permissions", "Use a temporary filesystem supporting private permissions.", err)
	}
	indexPath := filepath.Join(tempDir, "index")
	indexGit := git.WithEnv(map[string]string{
		"GIT_INDEX_FILE": indexPath,
		"GIT_WORK_TREE":  destination,
	})
	if _, err := indexGit.Run(ctx, "read-tree", oid); err != nil {
		return fail(ExitVerification, "could not load the snapshot tree into a temporary index", "Run git fsck and verify the snapshot objects.", err)
	}
	prefix := destination + string(filepath.Separator)
	if _, err := indexGit.Run(ctx, "checkout-index", "--all", "--force", "--prefix="+prefix); err != nil {
		return fail(ExitFailure, "could not materialize the snapshot tree", "Check destination permissions and filesystem capabilities.", err)
	}
	return nil
}

func secureArchivePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, filepath.Clean(name))
	if !pathWithin(target, root) || target == root {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	parent := filepath.Dir(target)
	for parent != root && pathWithin(parent, root) {
		if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("archive path traverses symlink: %q", name)
		}
		parent = filepath.Dir(parent)
	}
	return target, nil
}

func removeNonRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return os.RemoveAll(path)
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target, err := secureArchivePath(destination, rel)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := removeNonRegular(target); err != nil {
			return err
		}
		return copyFileReplace(path, target, info.Mode().Perm())
	})
}

func copyFileReplace(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func restoreWorktree(ctx context.Context, repo *Repository, git Git, snapshot *Snapshot, opts RestoreOptions) (*RestoreResult, error) {
	if err := repo.RequireWorktree(); err != nil {
		return nil, err
	}
	if err := repo.CheckLocks(snapshot.Ref, true); err != nil {
		return nil, err
	}
	target, err := listTreeEntries(ctx, git, snapshot.OID)
	if err != nil {
		return nil, err
	}
	status, err := git.RunBytes(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, fail(ExitFailure, "could not inspect current working-tree changes", "Run git status and retry.", err)
	}
	active := repo.InProgressOperations()
	conflicts, err := CheckConflicts(ctx, git)
	if err != nil {
		return nil, err
	}
	ignoredCollisions, err := ignoredTargetCollisions(ctx, repo, git, target)
	if err != nil {
		return nil, err
	}
	if (len(status) > 0 || len(active) > 0 || conflicts || len(ignoredCollisions) > 0) && !opts.Force {
		return nil, fail(ExitSafety, "working-tree restore could overwrite current data", "Review the dry-run preview, then pass --force and confirm only if the current data may be discarded.", nil)
	}
	paths, err := previewWorktreeRestore(ctx, git, snapshot.OID)
	if err != nil {
		return nil, err
	}
	paths = mergePaths(paths, ignoredCollisions)
	result := &RestoreResult{OID: snapshot.OID, Destination: repo.TopLevel, Worktree: true, Staged: opts.Staged, DryRun: opts.DryRun, Paths: paths}
	if opts.DryRun {
		return result, nil
	}
	stage, err := os.MkdirTemp("", "git-snapshot-worktree-")
	if err != nil {
		return nil, fail(ExitFailure, "could not create a restore staging directory", "Check temporary-directory permissions and free space.", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return nil, fail(ExitFailure, "could not restrict restore staging permissions", "Use a temporary filesystem supporting private permissions.", err)
	}
	if err := extractTree(ctx, git, snapshot.OID, stage); err != nil {
		return nil, err
	}
	trackedData, err := git.RunBytes(ctx, "ls-files", "-z")
	if err != nil {
		return nil, fail(ExitFailure, "could not enumerate tracked files", "Run git ls-files and retry.", err)
	}
	untrackedData, err := git.RunBytes(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fail(ExitFailure, "could not enumerate untracked files", "Run git status and retry.", err)
	}
	var remove []string
	for _, path := range append(splitNUL(trackedData), splitNUL(untrackedData)...) {
		if _, exists := target[path]; !exists {
			remove = append(remove, path)
		}
	}
	sort.Slice(remove, func(i, j int) bool { return len(remove[i]) > len(remove[j]) })
	for _, path := range remove {
		targetPath, joinErr := secureArchivePath(repo.TopLevel, filepath.FromSlash(path))
		if joinErr != nil {
			return nil, fail(ExitSafety, "refused an unsafe working-tree deletion path", "Inspect repository path names with git ls-files.", joinErr)
		}
		if containsRepositoryMetadata(ctx, targetPath) {
			return nil, fail(ExitSafety, fmt.Sprintf("refused to delete nested repository %q", path), "Move or back up the nested repository before restoring the working tree.", nil)
		}
	}
	changed := false
	for _, path := range remove {
		targetPath, joinErr := secureArchivePath(repo.TopLevel, filepath.FromSlash(path))
		if joinErr != nil {
			if changed {
				return nil, failChanged(ExitSafety, "refused an unsafe working-tree deletion path", "Inspect repository path names with git ls-files.", joinErr)
			}
			return nil, fail(ExitSafety, "refused an unsafe working-tree deletion path", "Inspect repository path names with git ls-files.", joinErr)
		}
		if err := os.RemoveAll(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, failChanged(ExitFailure, fmt.Sprintf("could not remove %q during restore", path), "Inspect the working tree; earlier paths may already have changed.", err)
		}
		changed = true
	}
	if err := copyTree(stage, repo.TopLevel); err != nil {
		return nil, failChanged(ExitFailure, "could not apply all snapshot files to the working tree", "Inspect the working tree; earlier paths may already have changed.", err)
	}
	changed = true
	if opts.Staged {
		if _, err := git.Run(ctx, "read-tree", "--reset", snapshot.OID); err != nil {
			return nil, failChanged(ExitFailure, "working tree was restored but the real index could not be updated", "Inspect the working tree, then run git read-tree only if updating the index is still desired.", err)
		}
	}
	return result, nil
}

func previewWorktreeRestore(ctx context.Context, git Git, oid string) ([]string, error) {
	data, err := git.RunBytes(ctx, "diff", "--name-only", "-z", oid, "--")
	if err != nil {
		return nil, fail(ExitFailure, "could not preview working-tree changes", "Run git diff manually and retry.", err)
	}
	seen := make(map[string]bool)
	var paths []string
	for _, path := range splitNUL(data) {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	untracked, err := git.RunBytes(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fail(ExitFailure, "could not preview untracked-file changes", "Run git status manually and retry.", err)
	}
	for _, path := range splitNUL(untracked) {
		if !seen[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func listTreeEntries(ctx context.Context, git Git, oid string) (map[string]treeEntry, error) {
	data, err := git.RunBytes(ctx, "ls-tree", "-r", "-z", oid)
	if err != nil {
		return nil, fail(ExitVerification, "could not enumerate snapshot tree entries", "Run git fsck and retry.", err)
	}
	entries := make(map[string]treeEntry)
	for _, record := range splitNUL(data) {
		metadata, path, found := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) < 1 {
			return nil, fail(ExitVerification, "snapshot tree contains a malformed entry", "Run git fsck and inspect the tree.", nil)
		}
		entries[path] = treeEntry{Path: path, Mode: fields[0]}
	}
	return entries, nil
}

func sortedTreePaths(entries map[string]treeEntry) []string {
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func ignoredTargetCollisions(ctx context.Context, repo *Repository, git Git, entries map[string]treeEntry) ([]string, error) {
	var input bytes.Buffer
	for path := range entries {
		absolute := filepath.Join(repo.TopLevel, filepath.FromSlash(path))
		if _, err := os.Lstat(absolute); err == nil {
			input.WriteString(path)
			input.WriteByte(0)
		}
	}
	if input.Len() == 0 {
		return nil, nil
	}
	data, err := git.RunInputBytesExit(ctx, input.Bytes(), 1, "check-ignore", "--stdin", "-z")
	if err != nil {
		return nil, fail(ExitFailure, "could not inspect ignored-file restore collisions", "Run git check-ignore for the target paths and retry.", err)
	}
	paths := splitNUL(data)
	sort.Strings(paths)
	return paths, nil
}

func mergePaths(groups ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, group := range groups {
		for _, path := range group {
			if !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	sort.Strings(result)
	return result
}

func containsRepositoryMetadata(ctx context.Context, path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	metadata, err := (Git{Repo: path}).Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir")
	return err == nil && pathWithin(metadata, path)
}
