package app

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type ListOptions struct {
	Limit    int
	Reverse  bool
	ShowSize bool
}

func List(ctx context.Context, repo *Repository, git Git, ref string, opts ListOptions) (*Stream, error) {
	stream, err := VerifyStream(ctx, repo, git, ref)
	if err != nil {
		return nil, err
	}
	items := append([]*Snapshot(nil), stream.Snapshots...)
	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	if opts.ShowSize {
		for _, snapshot := range items {
			size, sizeErr := git.Run(ctx, "cat-file", "-s", snapshot.OID)
			if sizeErr != nil {
				return nil, fail(ExitVerification, fmt.Sprintf("could not read object size for %s", snapshot.OID), "Run git fsck to inspect the object database.", sizeErr)
			}
			snapshot.Size, _ = strconv.ParseInt(size, 10, 64)
		}
	}
	if opts.Reverse {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	stream.Snapshots = items
	return stream, nil
}

type DiffOptions struct {
	Stat     bool
	NameOnly bool
	Patch    bool
}

func SnapshotDiff(ctx context.Context, git Git, snapshot *Snapshot, opts DiffOptions) (string, error) {
	args := diffArgs(opts)
	if len(snapshot.Parents) == 0 {
		args = append(args, "--root", snapshot.OID)
	} else {
		args = append(args, snapshot.Parents[0], snapshot.OID)
	}
	data, err := git.RunBytes(ctx, args...)
	if err != nil {
		return "", fail(ExitFailure, "could not render the snapshot diff", "Run git diff-tree manually to inspect the objects.", err)
	}
	return string(data), nil
}

func Diff(ctx context.Context, repo *Repository, git Git, ref, from, to string, allowUnreachable bool, opts DiffOptions) (string, error) {
	if from == "" {
		from = "latest"
	}
	if to == "" {
		if repo.Bare {
			to = "HEAD"
		} else {
			to = "worktree"
		}
	}
	fromOID, err := resolveDiffEndpoint(ctx, repo, git, ref, from, allowUnreachable, false)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(to, "worktree") {
		if err := repo.RequireWorktree(); err != nil {
			return "", err
		}
		return diffWorkingTree(ctx, repo, git, fromOID, opts)
	}
	toOID, err := resolveDiffEndpoint(ctx, repo, git, ref, to, allowUnreachable, true)
	if err != nil {
		return "", err
	}
	args := diffArgs(opts)
	args = append(args, fromOID, toOID)
	data, err := git.RunBytes(ctx, args...)
	if err != nil {
		return "", fail(ExitFailure, "could not compare snapshot objects", "Verify both objects and retry.", err)
	}
	return string(data), nil
}

func resolveDiffEndpoint(ctx context.Context, repo *Repository, git Git, ref, selector string, allowUnreachable, allowHead bool) (string, error) {
	if strings.EqualFold(selector, "HEAD") {
		if !allowHead {
			return "", fail(ExitUsage, "HEAD is supported only as the diff destination", "Use a snapshot selector as the first endpoint.", nil)
		}
		if repo.HeadUnborn {
			return "", fail(ExitNotFound, "HEAD is unborn", "Create an initial commit or compare with another snapshot.", nil)
		}
		return repo.Head, nil
	}
	if strings.EqualFold(selector, "worktree") {
		return "", fail(ExitUsage, "the working tree is supported only as the diff destination", "Place worktree second, or omit the second selector.", nil)
	}
	snapshot, _, err := ResolveSelector(ctx, repo, git, ref, selector, allowUnreachable)
	if err != nil {
		return "", err
	}
	return snapshot.OID, nil
}

func diffArgs(opts DiffOptions) []string {
	args := []string{"diff-tree", "--no-commit-id", "-r", "-M"}
	switch {
	case opts.Stat:
		args = append(args, "--stat")
	case opts.NameOnly:
		args = append(args, "--name-only")
	default:
		args = append(args, "--patch")
	}
	return args
}

func diffWorkingTree(ctx context.Context, repo *Repository, git Git, fromOID string, opts DiffOptions) (string, error) {
	args := []string{"diff", "-M"}
	switch {
	case opts.Stat:
		args = append(args, "--stat")
	case opts.NameOnly:
		args = append(args, "--name-only")
	default:
		args = append(args, "--patch")
	}
	args = append(args, fromOID, "--")
	data, err := git.RunBytes(ctx, args...)
	if err != nil {
		return "", fail(ExitFailure, "could not compare the snapshot with the working tree", "Run git status and verify working-tree permissions.", err)
	}
	// git diff <tree> covers every path present in the snapshot tree. Add
	// currently untracked paths absent from that tree without writing objects.
	treeData, err := git.RunBytes(ctx, "ls-tree", "-r", "--name-only", "-z", fromOID)
	if err != nil {
		return "", fail(ExitVerification, "could not enumerate the snapshot tree", "Run git fsck and retry.", err)
	}
	present := make(map[string]bool)
	for _, path := range splitNUL(treeData) {
		present[path] = true
	}
	untrackedData, err := git.RunBytes(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fail(ExitFailure, "could not enumerate untracked files", "Run git status and retry.", err)
	}
	var extra []string
	for _, path := range splitNUL(untrackedData) {
		if !present[path] {
			extra = append(extra, path)
		}
	}
	sort.Strings(extra)
	if opts.NameOnly {
		var out bytes.Buffer
		out.Write(data)
		if out.Len() > 0 && out.Bytes()[out.Len()-1] != '\n' {
			out.WriteByte('\n')
		}
		for _, path := range extra {
			fmt.Fprintln(&out, path)
		}
		return out.String(), nil
	}
	// Patch/stat output for untracked additions is intentionally generated by
	// git diff --no-index. Exit status 1 means a normal difference.
	var out bytes.Buffer
	out.Write(data)
	for _, path := range extra {
		args := []string{"diff", "--no-index"}
		if opts.Stat {
			args = append(args, "--stat")
		} else {
			args = append(args, "--patch")
		}
		args = append(args, "--", "/dev/null", path)
		piece, runErr := git.RunBytesExit(ctx, 1, args...)
		if runErr != nil {
			return "", fail(ExitFailure, fmt.Sprintf("could not render untracked file %q", path), "Check file permissions and retry.", runErr)
		}
		out.Write(piece)
	}
	return out.String(), nil
}

func splitNUL(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	result := parts[:0]
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func FormatSnapshot(format string, snapshot *Snapshot, distance int) string {
	if format == "" {
		format = "%d %H %cI %s"
	}
	replacements := []string{
		"%H", snapshot.OID,
		"%h", shortOID(snapshot.OID),
		"%cI", snapshot.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"%s", snapshot.Subject,
		"%r", snapshot.Ref,
		"%b", snapshot.Base,
		"%d", strconv.Itoa(distance),
	}
	return strings.NewReplacer(replacements...).Replace(format)
}

func shortOID(oid string) string {
	if len(oid) <= 12 {
		return oid
	}
	return oid[:12]
}
