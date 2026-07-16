package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CreateOptions struct {
	Ref                  string
	Namespace            string
	Base                 string
	Message              string
	MessageFile          string
	MessageTemplate      string
	IncludeUntracked     bool
	IncludeIgnored       bool
	AllowInProgress      bool
	AllowDirtySubmodules bool
	Sign                 bool
	SigningKey           string
	CreateReflog         bool
	DryRun               bool
	LockTimeout          time.Duration
}

type CreateResult struct {
	OID       string    `json:"oid,omitempty"`
	Tree      string    `json:"tree"`
	Ref       string    `json:"ref"`
	Parent    string    `json:"parent,omitempty"`
	Base      string    `json:"base"`
	CreatedAt time.Time `json:"createdAt"`
	DryRun    bool      `json:"dryRun"`
}

func Create(ctx context.Context, repo *Repository, git Git, opts CreateOptions) (*CreateResult, error) {
	if err := PreflightCreate(ctx, repo, git, opts.Ref, opts.Namespace, opts.AllowInProgress, opts.AllowDirtySubmodules); err != nil {
		return nil, err
	}
	tip, exists, err := ReadRef(ctx, git, opts.Ref)
	if err != nil {
		return nil, fail(ExitFailure, "could not read the snapshot ref", "Check repository permissions and retry.", err)
	}
	parent := ""
	base := ""
	if exists {
		stream, verifyErr := VerifyStream(ctx, repo, git, opts.Ref)
		if verifyErr != nil {
			return nil, verifyErr
		}
		parent = tip
		base = stream.Base
	} else {
		base, err = resolveBase(ctx, repo, git, opts.Base)
		if err != nil {
			return nil, err
		}
		if base != "unborn" {
			parent = base
		}
	}
	tree, cleanup, err := captureTree(ctx, repo, git, opts.IncludeUntracked, opts.IncludeIgnored)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	createdAt := time.Now().UTC().Truncate(time.Second)
	result := &CreateResult{Tree: tree, Ref: opts.Ref, Parent: parent, Base: base, CreatedAt: createdAt, DryRun: opts.DryRun}
	if opts.DryRun {
		return result, nil
	}
	message, err := createMessage(opts, createdAt, base)
	if err != nil {
		return nil, err
	}
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	if opts.SigningKey != "" {
		opts.Sign = true
	}
	if opts.Sign {
		if opts.SigningKey != "" {
			args = append(args, "-S"+opts.SigningKey)
		} else {
			args = append(args, "-S")
		}
	}
	args = append(args, "-F", "-")
	oid, err := git.RunInput(ctx, []byte(message), args...)
	if err != nil {
		return nil, fail(ExitFailure, "could not create the snapshot commit object", "Configure user.name/user.email and signing settings, then retry. The snapshot ref was not changed.", err)
	}
	result.OID = oid
	expected := tip
	if !exists {
		expected = repo.ZeroOID()
	}
	if err := updateRefCAS(ctx, git, opts.Ref, oid, expected, opts.CreateReflog, opts.LockTimeout, "git-snapshot: create"); err != nil {
		return nil, err
	}
	return result, nil
}

func resolveBase(ctx context.Context, repo *Repository, git Git, revision string) (string, error) {
	if revision == "" {
		revision = "HEAD"
	}
	if revision == "HEAD" && repo.HeadUnborn {
		return "unborn", nil
	}
	oid, err := git.Run(ctx, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", fail(ExitNotFound, fmt.Sprintf("base revision %q is not a commit", revision), "Choose an existing commit with --base, or use HEAD in an unborn repository.", err)
	}
	return oid, nil
}

func captureTree(ctx context.Context, repo *Repository, git Git, includeUntracked, includeIgnored bool) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "git-snapshot-")
	if err != nil {
		return "", func() {}, fail(ExitFailure, "could not create a temporary index directory", "Check temporary-directory permissions and free space.", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	if err := os.Chmod(tempDir, 0o700); err != nil {
		cleanup()
		return "", func() {}, fail(ExitFailure, "could not restrict temporary directory permissions", "Choose a temporary filesystem that supports private permissions.", err)
	}
	tempIndex := filepath.Join(tempDir, "index")
	if info, statErr := os.Stat(repo.IndexPath); statErr == nil && info.Mode().IsRegular() {
		if err := copyFile(repo.IndexPath, tempIndex, 0o600); err != nil {
			cleanup()
			return "", func() {}, fail(ExitFailure, "could not copy the real Git index safely", "Check index and temporary-directory permissions. The real index was not modified.", err)
		}
	}
	indexGit := git.WithEnv(map[string]string{"GIT_INDEX_FILE": tempIndex})
	if _, err := os.Stat(tempIndex); errors.Is(err, os.ErrNotExist) {
		if _, runErr := indexGit.Run(ctx, "read-tree", "--empty"); runErr != nil {
			cleanup()
			return "", func() {}, fail(ExitFailure, "could not initialize the temporary index", "Check repository object integrity and retry.", runErr)
		}
	} else {
		// Materialize split-index entries into the private index so later writes
		// cannot create or update shared-index state in the common Git directory.
		_, _ = indexGit.Run(ctx, "update-index", "--no-split-index")
	}
	addArgs := []string{"add"}
	switch {
	case includeIgnored:
		addArgs = append(addArgs, "--all", "--force")
	case includeUntracked:
		addArgs = append(addArgs, "--all")
	default:
		addArgs = append(addArgs, "--update")
	}
	addArgs = append(addArgs, "--", ".")
	if _, err := indexGit.Run(ctx, addArgs...); err != nil {
		cleanup()
		return "", func() {}, fail(ExitFailure, "could not populate the temporary index", "Check file permissions, conflicts, and submodule state. The real index and working tree were not changed.", err)
	}
	tree, err := indexGit.Run(ctx, "write-tree")
	if err != nil {
		cleanup()
		return "", func() {}, fail(ExitFailure, "could not write the snapshot tree", "Run git fsck and verify repository permissions. The snapshot ref was not changed.", err)
	}
	return tree, cleanup, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
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

func createMessage(opts CreateOptions, createdAt time.Time, base string) (string, error) {
	message := opts.Message
	if opts.MessageFile != "" {
		if opts.Message != "" {
			return "", fail(ExitUsage, "--message and --message-file cannot be used together", "Choose one source for the snapshot message.", nil)
		}
		data, err := os.ReadFile(opts.MessageFile)
		if err != nil {
			return "", fail(ExitUsage, "could not read --message-file", "Check the path and permissions, then retry.", err)
		}
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = opts.MessageTemplate
	}
	message = strings.ReplaceAll(message, "{createdAt}", createdAt.Format(time.RFC3339))
	message = strings.ReplaceAll(message, "{ref}", opts.Ref)
	message = strings.ReplaceAll(message, "{base}", base)
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Git-Snapshot-") {
			return "", fail(ExitUsage, "snapshot message contains a reserved Git-Snapshot trailer", "Remove reserved metadata lines from the custom message.", nil)
		}
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "git-snapshot"
	}
	return fmt.Sprintf("%s\n\nGit-Snapshot-Version: %s\nGit-Snapshot-Ref: %s\nGit-Snapshot-Base: %s\nGit-Snapshot-Created-At: %s\n", message, snapshotVersion, opts.Ref, base, createdAt.Format(time.RFC3339)), nil
}
