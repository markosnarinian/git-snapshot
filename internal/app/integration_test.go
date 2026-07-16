package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	global, err := os.CreateTemp("", "git-snapshot-test-global-")
	if err != nil {
		panic(err)
	}
	_ = global.Close()
	_ = os.Setenv("GIT_CONFIG_GLOBAL", global.Name())
	code := m.Run()
	_ = os.Remove(global.Name())
	os.Exit(code)
}

func TestCreateCapturesMixedStateWithoutMutatingRepository(t *testing.T) {
	repoPath := newRepository(t, "sha1")
	writeFile(t, repoPath, "tracked.txt", "base\n", 0o644)
	writeFile(t, repoPath, "unstaged.txt", "base\n", 0o644)
	writeFile(t, repoPath, "deleted.txt", "delete\n", 0o644)
	writeFile(t, repoPath, "run.sh", "#!/bin/sh\necho base\n", 0o755)
	if runtime.GOOS != "windows" {
		must(t, os.Symlink("tracked.txt", filepath.Join(repoPath, "link")))
	}
	git(t, repoPath, "add", ".")
	git(t, repoPath, "commit", "-m", "initial")

	// Preserve an existing stash and then build staged, unstaged, mixed,
	// renamed, deleted, untracked, ignored, executable, and symlink state.
	writeFile(t, repoPath, "stash.txt", "stash\n", 0o644)
	git(t, repoPath, "add", "stash.txt")
	git(t, repoPath, "stash", "push", "-m", "existing")
	writeFile(t, repoPath, "tracked.txt", "staged\n", 0o644)
	git(t, repoPath, "add", "tracked.txt")
	writeFile(t, repoPath, "tracked.txt", "working\n", 0o644)
	writeFile(t, repoPath, "unstaged.txt", "unstaged\n", 0o644)
	must(t, os.Remove(filepath.Join(repoPath, "deleted.txt")))
	git(t, repoPath, "mv", "run.sh", "moved.sh")
	writeFile(t, repoPath, "moved.sh", "#!/bin/sh\necho changed\n", 0o755)
	writeFile(t, repoPath, "untracked.txt", "new\n", 0o644)
	writeFile(t, repoPath, ".gitignore", "secret.env\n", 0o644)
	git(t, repoPath, "add", ".gitignore")
	writeFile(t, repoPath, "secret.env", "credential\n", 0o600)
	if runtime.GOOS != "windows" {
		must(t, os.Remove(filepath.Join(repoPath, "link")))
		must(t, os.Symlink("untracked.txt", filepath.Join(repoPath, "link")))
	}

	before := captureRepositoryState(t, repoPath)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	repo, runner := repositoryAPI(t, repoPath)
	result, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	afterIndex := hashFile(t, filepath.Join(repoPath, ".git", "index"))
	if before.IndexHash != afterIndex {
		t.Fatalf("real index changed: before %s after %s", before.IndexHash, afterIndex)
	}
	after := captureRepositoryState(t, repoPath)
	assertRepositoryStateEqual(t, before, after)

	assertBlob(t, repoPath, result.OID, "tracked.txt", "working\n")
	assertBlob(t, repoPath, result.OID, "unstaged.txt", "unstaged\n")
	assertBlob(t, repoPath, result.OID, "untracked.txt", "new\n")
	assertBlob(t, repoPath, result.OID, "moved.sh", "#!/bin/sh\necho changed\n")
	assertMissingPath(t, repoPath, result.OID, "deleted.txt")
	assertMissingPath(t, repoPath, result.OID, "run.sh")
	assertMissingPath(t, repoPath, result.OID, "secret.env")
	if mode := treeMode(t, repoPath, result.OID, "moved.sh"); mode != "100755" {
		t.Fatalf("executable mode = %s", mode)
	}
	if runtime.GOOS != "windows" {
		if mode := treeMode(t, repoPath, result.OID, "link"); mode != "120000" {
			t.Fatalf("symlink mode = %s", mode)
		}
		assertBlob(t, repoPath, result.OID, "link", "untracked.txt")
	}
	stream, err := VerifyStream(context.Background(), repo, runner, DefaultRef)
	must(t, err)
	if len(stream.Snapshots) != 1 || stream.Tip != result.OID || stream.Base != before.Head {
		t.Fatalf("unexpected stream: %#v", stream)
	}
	entries, err := os.ReadDir(tempRoot)
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}

func TestCreateIgnoredAndUntrackedPolicies(t *testing.T) {
	repoPath := newRepository(t, "sha1")
	writeFile(t, repoPath, ".gitignore", "ignored.txt\n", 0o644)
	writeFile(t, repoPath, "tracked.txt", "base\n", 0o644)
	git(t, repoPath, "add", ".")
	git(t, repoPath, "commit", "-m", "initial")
	writeFile(t, repoPath, "untracked.txt", "untracked\n", 0o644)
	writeFile(t, repoPath, "ignored.txt", "ignored\n", 0o600)
	repo, runner := repositoryAPI(t, repoPath)

	opts := defaultCreateOptions()
	opts.IncludeUntracked = false
	first, err := Create(context.Background(), repo, runner, opts)
	must(t, err)
	assertMissingPath(t, repoPath, first.OID, "untracked.txt")
	assertMissingPath(t, repoPath, first.OID, "ignored.txt")

	opts.IncludeIgnored = true
	second, err := Create(context.Background(), repo, runner, opts)
	must(t, err)
	assertBlob(t, repoPath, second.OID, "untracked.txt", "untracked\n")
	assertBlob(t, repoPath, second.OID, "ignored.txt", "ignored\n")
}

func TestCleanCreateRetentionAndExplicitStagedRestore(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	before := captureRepositoryState(t, repoPath)
	repo, runner := repositoryAPI(t, repoPath)
	opts := defaultCreateOptions()
	opts.Retention = 1
	result, err := Create(context.Background(), repo, runner, opts)
	must(t, err)
	if got := git(t, repoPath, "rev-parse", result.OID+"^{tree}"); got != git(t, repoPath, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("clean snapshot tree %s differs from HEAD tree", got)
	}
	assertRepositoryStateEqual(t, before, captureRepositoryState(t, repoPath))

	_, err = Create(context.Background(), repo, runner, opts)
	if ExitCode(err) != ExitSafety {
		t.Fatalf("retention refusal exit=%d err=%v", ExitCode(err), err)
	}
	writeFile(t, repoPath, "file.txt", "replace me\n", 0o644)
	snapshot, _, err := ResolveSelector(context.Background(), repo, runner, DefaultRef, result.OID, false)
	must(t, err)
	_, err = Restore(context.Background(), repo, runner, snapshot, RestoreOptions{Worktree: true, Staged: true, Force: true})
	must(t, err)
	assertFileContent(t, filepath.Join(repoPath, "file.txt"), "initial\n")
	if got := git(t, repoPath, "write-tree"); got != snapshot.Tree {
		t.Fatalf("explicit staged restore index tree=%s want=%s", got, snapshot.Tree)
	}
}

func TestUnbornAndDetachedHead(t *testing.T) {
	t.Run("unborn", func(t *testing.T) {
		repoPath := newRepository(t, "sha1")
		writeFile(t, repoPath, "first.txt", "first\n", 0o644)
		repo, runner := repositoryAPI(t, repoPath)
		result, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		must(t, err)
		if result.Base != "unborn" || result.Parent != "" {
			t.Fatalf("unexpected unborn result: %#v", result)
		}
		if _, err := rawGit(repoPath, "rev-parse", "--verify", "HEAD"); err == nil {
			t.Fatal("create unexpectedly gave unborn HEAD a commit")
		}
		stream, err := VerifyStream(context.Background(), repo, runner, DefaultRef)
		must(t, err)
		if stream.Base != "unborn" || len(stream.Snapshots[0].Parents) != 0 {
			t.Fatalf("invalid unborn stream: %#v", stream)
		}
	})

	t.Run("detached", func(t *testing.T) {
		repoPath := repositoryWithInitialCommit(t)
		head := git(t, repoPath, "rev-parse", "HEAD")
		git(t, repoPath, "checkout", "--detach", head)
		writeFile(t, repoPath, "file.txt", "detached change\n", 0o644)
		repo, runner := repositoryAPI(t, repoPath)
		_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		must(t, err)
		if _, err := rawGit(repoPath, "symbolic-ref", "-q", "HEAD"); err == nil {
			t.Fatal("HEAD is no longer detached")
		}
		if got := git(t, repoPath, "rev-parse", "HEAD"); got != head {
			t.Fatalf("detached HEAD changed from %s to %s", head, got)
		}
	})
}

func TestLinkedWorktreeAndBareReadOnlyCommands(t *testing.T) {
	mainRepo := repositoryWithInitialCommit(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, mainRepo, "worktree", "add", "-b", "linked", linked)
	writeFile(t, linked, "linked.txt", "linked\n", 0o644)
	repo, runner := repositoryAPI(t, linked)
	if !repo.Linked {
		t.Fatal("linked worktree was not detected")
	}
	result, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	if got := git(t, mainRepo, "rev-parse", DefaultRef); got != result.OID {
		t.Fatalf("snapshot ref was not shared through common dir: %s", got)
	}

	bare := filepath.Join(t.TempDir(), "bare.git")
	git(t, "", "clone", "--bare", mainRepo, bare)
	git(t, bare, "fetch", mainRepo, DefaultRef+":"+DefaultRef)
	bareRepo, bareGit := repositoryAPI(t, bare)
	if !bareRepo.Bare {
		t.Fatal("bare repository was not detected")
	}
	_, err = VerifyStream(context.Background(), bareRepo, bareGit, DefaultRef)
	must(t, err)
	if _, err := Create(context.Background(), bareRepo, bareGit, defaultCreateOptions()); ExitCode(err) != ExitSafety {
		t.Fatalf("bare create exit = %d, err=%v", ExitCode(err), err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	snapshot, _, err := ResolveSelector(context.Background(), bareRepo, bareGit, DefaultRef, "latest", false)
	must(t, err)
	_, err = Restore(context.Background(), bareRepo, bareGit, snapshot, RestoreOptions{Destination: destination})
	must(t, err)
	assertFileContent(t, filepath.Join(destination, "linked.txt"), "linked\n")
}

func TestInProgressConflictAndDirtySubmoduleSafety(t *testing.T) {
	t.Run("conflict and override", func(t *testing.T) {
		repoPath := repositoryWithInitialCommit(t)
		git(t, repoPath, "checkout", "-b", "side")
		writeFile(t, repoPath, "file.txt", "side\n", 0o644)
		git(t, repoPath, "commit", "-am", "side")
		git(t, repoPath, "checkout", "main")
		writeFile(t, repoPath, "file.txt", "main\n", 0o644)
		git(t, repoPath, "commit", "-am", "main")
		if _, err := rawGit(repoPath, "merge", "side"); err == nil {
			t.Fatal("expected merge conflict")
		}
		repo, runner := repositoryAPI(t, repoPath)
		_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		if ExitCode(err) != ExitSafety {
			t.Fatalf("conflict create exit=%d err=%v", ExitCode(err), err)
		}
		opts := defaultCreateOptions()
		opts.AllowInProgress = true
		_, err = Create(context.Background(), repo, runner, opts)
		must(t, err)
		if _, err := os.Stat(filepath.Join(repoPath, ".git", "MERGE_HEAD")); err != nil {
			t.Fatal("create disturbed merge state")
		}
	})

	t.Run("sequencer markers", func(t *testing.T) {
		repoPath := repositoryWithInitialCommit(t)
		repo, runner := repositoryAPI(t, repoPath)
		for _, marker := range []string{"CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
			writeFile(t, repo.GitDir, marker, repo.Head+"\n", 0o644)
			_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
			if ExitCode(err) != ExitSafety {
				t.Fatalf("marker %s exit=%d err=%v", marker, ExitCode(err), err)
			}
			must(t, os.Remove(filepath.Join(repo.GitDir, marker)))
		}
		must(t, os.Mkdir(filepath.Join(repo.GitDir, "rebase-merge"), 0o755))
		_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		if ExitCode(err) != ExitSafety {
			t.Fatalf("rebase marker exit=%d err=%v", ExitCode(err), err)
		}
	})

	t.Run("dirty submodule", func(t *testing.T) {
		child := repositoryWithInitialCommit(t)
		parent := repositoryWithInitialCommit(t)
		gitWithEnv(t, parent, []string{"GIT_ALLOW_PROTOCOL=file"}, "submodule", "add", child, "sub")
		git(t, parent, "commit", "-am", "submodule")
		writeFile(t, filepath.Join(parent, "sub"), "file.txt", "dirty\n", 0o644)
		repo, runner := repositoryAPI(t, parent)
		_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		if ExitCode(err) != ExitSafety {
			t.Fatalf("dirty submodule exit=%d err=%v", ExitCode(err), err)
		}
		opts := defaultCreateOptions()
		opts.AllowDirtySubmodules = true
		_, err = Create(context.Background(), repo, runner, opts)
		must(t, err)
	})
}

func TestRefSafetyOwnershipAndCAS(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	repo, runner := repositoryAPI(t, repoPath)
	for _, ref := range []string{
		"HEAD", "main", "refs/heads/snapshot", "refs/tags/snapshot", "refs/remotes/origin/snapshot",
		"refs/stash", "refs/notes/snapshot", "refs/bisect/snapshot", "refs/replace/snapshot", "refs/rewritten/snapshot",
	} {
		if err := ValidateSnapshotRef(context.Background(), runner, ref, "refs/"); ExitCode(err) != ExitSafety && ExitCode(err) != ExitUsage {
			t.Errorf("ref %q was not rejected safely: code=%d err=%v", ref, ExitCode(err), err)
		}
	}

	head := git(t, repoPath, "rev-parse", "HEAD")
	git(t, repoPath, "update-ref", DefaultRef, head)
	_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	if ExitCode(err) != ExitVerification {
		t.Fatalf("unrelated ref create exit=%d err=%v", ExitCode(err), err)
	}
	if got := git(t, repoPath, "rev-parse", DefaultRef); got != head {
		t.Fatalf("unrelated ref changed from %s to %s", head, got)
	}

	// A stale expected value must not overwrite a concurrent update.
	second := git(t, repoPath, "commit-tree", git(t, repoPath, "rev-parse", "HEAD^{tree}"), "-p", head, "-m", "second")
	git(t, repoPath, "update-ref", DefaultRef, second, head)
	err = updateRefCAS(context.Background(), runner, DefaultRef, head, head, true, 0, "stale")
	if ExitCode(err) != ExitConcurrent {
		t.Fatalf("stale CAS exit=%d err=%v", ExitCode(err), err)
	}
	if got := git(t, repoPath, "rev-parse", DefaultRef); got != second {
		t.Fatalf("CAS clobbered concurrent value: %s", got)
	}
}

func TestRestoreDropAndDelete(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	repo, runner := repositoryAPI(t, repoPath)
	writeFile(t, repoPath, ".gitattributes", "new.txt export-ignore\nsubst.txt export-subst\n", 0o644)
	writeFile(t, repoPath, "file.txt", "snapshot one\n", 0o644)
	writeFile(t, repoPath, "new.txt", "one\n", 0o644)
	writeFile(t, repoPath, "subst.txt", "$Format:%H$\n", 0o644)
	first, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	writeFile(t, repoPath, "file.txt", "snapshot two\n", 0o644)
	second, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)

	destination := filepath.Join(t.TempDir(), "restore")
	firstSnapshot, _, err := ResolveSelector(context.Background(), repo, runner, DefaultRef, "1", false)
	must(t, err)
	_, err = Restore(context.Background(), repo, runner, firstSnapshot, RestoreOptions{Destination: destination, DryRun: true})
	must(t, err)
	if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("dry-run created destination")
	}
	_, err = Restore(context.Background(), repo, runner, firstSnapshot, RestoreOptions{Destination: destination})
	must(t, err)
	assertFileContent(t, filepath.Join(destination, "file.txt"), "snapshot one\n")
	assertFileContent(t, filepath.Join(destination, "new.txt"), "one\n")
	assertFileContent(t, filepath.Join(destination, "subst.txt"), "$Format:%H$\n")

	indexBefore := hashFile(t, repo.IndexPath)
	writeFile(t, repoPath, "file.txt", "valuable current data\n", 0o644)
	_, err = Restore(context.Background(), repo, runner, firstSnapshot, RestoreOptions{Worktree: true})
	if ExitCode(err) != ExitSafety {
		t.Fatalf("unsafe worktree restore exit=%d err=%v", ExitCode(err), err)
	}
	assertFileContent(t, filepath.Join(repoPath, "file.txt"), "valuable current data\n")
	_, err = Restore(context.Background(), repo, runner, firstSnapshot, RestoreOptions{Worktree: true, Force: true})
	must(t, err)
	assertFileContent(t, filepath.Join(repoPath, "file.txt"), "snapshot one\n")
	if got := hashFile(t, repo.IndexPath); got != indexBefore {
		t.Fatalf("default worktree restore changed real index: %s -> %s", indexBefore, got)
	}

	dropped, err := Drop(context.Background(), repo, runner, DefaultRef, DefaultNamespace, second.OID, 1, time.Second, true)
	must(t, err)
	if dropped.NewTip != first.OID || dropped.Removed[0].OID != second.OID {
		t.Fatalf("unexpected drop result: %#v", dropped)
	}
	deleted, err := Delete(context.Background(), repo, runner, DefaultRef, DefaultNamespace, first.OID, time.Second)
	must(t, err)
	if len(deleted.Removed) != 1 || deleted.Removed[0].OID != first.OID {
		t.Fatalf("unexpected delete result: %#v", deleted)
	}
	if _, exists, err := ReadRef(context.Background(), runner, DefaultRef); err != nil || exists {
		t.Fatalf("snapshot ref still exists: exists=%v err=%v", exists, err)
	}
}

func TestRestoreProtectsIgnoredFilesAndNestedRepositories(t *testing.T) {
	t.Run("ignored collision", func(t *testing.T) {
		repoPath := repositoryWithInitialCommit(t)
		writeFile(t, repoPath, ".gitignore", "secret.env\n", 0o644)
		git(t, repoPath, "add", ".gitignore")
		git(t, repoPath, "commit", "-m", "ignore secret")
		writeFile(t, repoPath, "secret.env", "snapshot secret\n", 0o600)
		repo, runner := repositoryAPI(t, repoPath)
		opts := defaultCreateOptions()
		opts.IncludeIgnored = true
		result, err := Create(context.Background(), repo, runner, opts)
		must(t, err)
		writeFile(t, repoPath, "secret.env", "valuable current secret\n", 0o600)
		snapshot, _, err := ResolveSelector(context.Background(), repo, runner, DefaultRef, result.OID, false)
		must(t, err)
		_, err = Restore(context.Background(), repo, runner, snapshot, RestoreOptions{Worktree: true})
		if ExitCode(err) != ExitSafety {
			t.Fatalf("ignored collision restore exit=%d err=%v", ExitCode(err), err)
		}
		assertFileContent(t, filepath.Join(repoPath, "secret.env"), "valuable current secret\n")
	})

	t.Run("nested repository deletion", func(t *testing.T) {
		repoPath := repositoryWithInitialCommit(t)
		repo, runner := repositoryAPI(t, repoPath)
		result, err := Create(context.Background(), repo, runner, defaultCreateOptions())
		must(t, err)
		nested := filepath.Join(repoPath, "nested")
		git(t, "", "init", "-b", "main", nested)
		configureIdentity(t, nested)
		writeFile(t, nested, "valuable.txt", "unpushed\n", 0o644)
		git(t, nested, "add", ".")
		git(t, nested, "commit", "-m", "valuable")
		snapshot, _, err := ResolveSelector(context.Background(), repo, runner, DefaultRef, result.OID, false)
		must(t, err)
		_, err = Restore(context.Background(), repo, runner, snapshot, RestoreOptions{Worktree: true, Force: true})
		if ExitCode(err) != ExitSafety {
			t.Fatalf("nested repository restore exit=%d err=%v", ExitCode(err), err)
		}
		assertFileContent(t, filepath.Join(nested, "valuable.txt"), "unpushed\n")
	})
}

func TestEmbeddedRepositoryCaptureAndPreviewTipSafety(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	nested := filepath.Join(repoPath, "embedded")
	git(t, "", "init", "-b", "main", nested)
	configureIdentity(t, nested)
	writeFile(t, nested, "file.txt", "nested\n", 0o644)
	git(t, nested, "add", ".")
	git(t, nested, "commit", "-m", "nested")
	repo, runner := repositoryAPI(t, repoPath)
	_, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	if ExitCode(err) != ExitSafety {
		t.Fatalf("embedded repository create exit=%d err=%v", ExitCode(err), err)
	}
	if _, exists, readErr := ReadRef(context.Background(), runner, DefaultRef); readErr != nil || exists {
		t.Fatalf("embedded repository refusal changed ref: exists=%v err=%v", exists, readErr)
	}

	must(t, os.RemoveAll(nested))
	first, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	writeFile(t, repoPath, "file.txt", "second\n", 0o644)
	second, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	_, err = Drop(context.Background(), repo, runner, DefaultRef, DefaultNamespace, first.OID, 1, time.Second, true)
	if ExitCode(err) != ExitConcurrent {
		t.Fatalf("stale preview drop exit=%d err=%v", ExitCode(err), err)
	}
	if got := git(t, repoPath, "rev-parse", DefaultRef); got != second.OID {
		t.Fatalf("stale preview changed tip to %s", got)
	}
	_, err = Delete(context.Background(), repo, runner, DefaultRef, DefaultNamespace, first.OID, time.Second)
	if ExitCode(err) != ExitConcurrent {
		t.Fatalf("stale preview delete exit=%d err=%v", ExitCode(err), err)
	}
}

func TestCancellationCleansTemporaryIndex(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	repo, runner := repositoryAPI(t, repoPath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Create(ctx, repo, runner, defaultCreateOptions())
	if err == nil {
		t.Fatal("cancelled create succeeded")
	}
	entries, readErr := os.ReadDir(tempRoot)
	must(t, readErr)
	if len(entries) != 0 {
		t.Fatalf("cancelled create leaked temporary files: %v", entries)
	}
	if _, exists, readErr := ReadRef(context.Background(), runner, DefaultRef); readErr != nil || exists {
		t.Fatalf("cancelled create changed ref: exists=%v err=%v", exists, readErr)
	}
}

func TestSHA256WhenSupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sha256")
	if _, err := rawGit("", "init", "--object-format=sha256", "-b", "main", path); err != nil {
		t.Skipf("Git does not support SHA-256 repositories: %v", err)
	}
	configureIdentity(t, path)
	writeFile(t, path, "file.txt", "sha256\n", 0o644)
	git(t, path, "add", ".")
	git(t, path, "commit", "-m", "initial")
	repo, runner := repositoryAPI(t, path)
	if repo.ObjectFormat != "sha256" || len(repo.Head) != 64 {
		t.Fatalf("unexpected object format %s head %s", repo.ObjectFormat, repo.Head)
	}
	result, err := Create(context.Background(), repo, runner, defaultCreateOptions())
	must(t, err)
	if len(result.OID) != 64 {
		t.Fatalf("snapshot SHA-256 length = %d", len(result.OID))
	}
	_, err = VerifyStream(context.Background(), repo, runner, DefaultRef)
	must(t, err)
}

func TestConfigPrecedenceAndCLIConfirmation(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	global := filepath.Join(t.TempDir(), "global-config")
	writeFile(t, filepath.Dir(global), filepath.Base(global), "[snapshot]\n\tref = refs/snapshots/global\n", 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git(t, repoPath, "config", "snapshot.ref", "refs/snapshots/local")
	t.Setenv("GIT_SNAPSHOT_REF", "refs/snapshots/environment")
	cfg, err := LoadConfig(context.Background(), repoPath, "")
	must(t, err)
	if cfg.Ref != "refs/snapshots/environment" || cfg.Values["snapshot.ref"].Origin != "env:GIT_SNAPSHOT_REF" {
		t.Fatalf("unexpected config precedence: %#v", cfg.Values["snapshot.ref"])
	}

	writeFile(t, repoPath, ".gitignore", "ignored.txt\n", 0o644)
	writeFile(t, repoPath, "ignored.txt", "secret\n", 0o600)
	var stdout, stderr bytes.Buffer
	cli := &App{In: strings.NewReader("no\n"), Out: &stdout, Err: &stderr}
	err = cli.Run(context.Background(), []string{"create", "--repo", repoPath, "--ref", DefaultRef, "--include-ignored"})
	if ExitCode(err) != ExitSafety || !strings.Contains(stderr.String(), "WARNING") {
		t.Fatalf("ignored confirmation exit=%d stderr=%q err=%v", ExitCode(err), stderr.String(), err)
	}
	if _, err := rawGit(repoPath, "show-ref", "--verify", DefaultRef); err == nil {
		t.Fatal("declined include-ignored create changed ref")
	}
}

type repositoryState struct {
	Head       string
	Branch     string
	Refs       string
	Status     string
	Diff       string
	CachedDiff string
	IndexHash  string
	Files      map[string]string
}

func captureRepositoryState(t *testing.T, repo string) repositoryState {
	t.Helper()
	state := repositoryState{
		Head:       git(t, repo, "rev-parse", "HEAD"),
		Refs:       git(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/remotes", "refs/notes", "refs/stash"),
		Status:     git(t, repo, "status", "--porcelain=v2", "--untracked-files=all", "--ignore-submodules=none"),
		Diff:       git(t, repo, "diff", "--no-ext-diff"),
		CachedDiff: git(t, repo, "diff", "--cached", "--no-ext-diff"),
		Files:      map[string]string{},
	}
	state.Branch, _ = rawGit(repo, "symbolic-ref", "-q", "HEAD")
	state.IndexHash = hashFile(t, filepath.Join(repo, ".git", "index"))
	must(t, filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == filepath.Join(repo, ".git") {
			return filepath.SkipDir
		}
		if path == repo || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			state.Files[rel] = fmt.Sprintf("symlink:%s:%v", target, info.Mode())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		state.Files[rel] = fmt.Sprintf("file:%s:%x", info.Mode(), data)
		return nil
	}))
	return state
}

func assertRepositoryStateEqual(t *testing.T, before, after repositoryState) {
	t.Helper()
	if before.Head != after.Head || before.Branch != after.Branch || before.Refs != after.Refs || before.Status != after.Status || before.Diff != after.Diff || before.CachedDiff != after.CachedDiff || before.IndexHash != after.IndexHash {
		t.Fatalf("repository metadata changed\nbefore=%#v\nafter=%#v", before, after)
	}
	if fmt.Sprint(before.Files) != fmt.Sprint(after.Files) {
		t.Fatalf("working-tree files changed\nbefore=%v\nafter=%v", before.Files, after.Files)
	}
}

func defaultCreateOptions() CreateOptions {
	return CreateOptions{
		Ref: DefaultRef, Namespace: DefaultNamespace, Base: DefaultBase,
		MessageTemplate: DefaultMessageTemplate, IncludeUntracked: true,
		CreateReflog: true, LockTimeout: time.Second,
	}
}

func repositoryWithInitialCommit(t *testing.T) string {
	t.Helper()
	repo := newRepository(t, "sha1")
	writeFile(t, repo, "file.txt", "initial\n", 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func newRepository(t *testing.T, format string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo")
	args := []string{"init", "-b", "main"}
	if format == "sha256" {
		args = append(args, "--object-format=sha256")
	}
	args = append(args, path)
	git(t, "", args...)
	configureIdentity(t, path)
	return path
}

func configureIdentity(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "config", "user.name", "Snapshot Test")
	git(t, repo, "config", "user.email", "snapshot@example.invalid")
	git(t, repo, "config", "commit.gpgSign", "false")
}

func repositoryAPI(t *testing.T, path string) (*Repository, Git) {
	t.Helper()
	repo, err := DiscoverRepository(context.Background(), path, false, nil)
	must(t, err)
	return repo, repo.Git(false, nil)
}

func writeFile(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), mode))
	must(t, os.Chmod(path, mode))
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func assertBlob(t *testing.T, repo, oid, path, expected string) {
	t.Helper()
	if got := git(t, repo, "show", oid+":"+path); got+"\n" != expected && got != expected {
		t.Fatalf("%s:%s = %q, want %q", oid, path, got, expected)
	}
}

func assertMissingPath(t *testing.T, repo, oid, path string) {
	t.Helper()
	if _, err := rawGit(repo, "cat-file", "-e", oid+":"+path); err == nil {
		t.Fatalf("unexpected path %s:%s", oid, path)
	}
}

func treeMode(t *testing.T, repo, oid, path string) string {
	t.Helper()
	line := git(t, repo, "ls-tree", oid, "--", path)
	fields := strings.Fields(line)
	if len(fields) == 0 {
		t.Fatalf("tree path missing: %s", path)
	}
	return fields[0]
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	if string(data) != expected {
		t.Fatalf("%s = %q, want %q", path, data, expected)
	}
}

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	output, err := rawGit(repo, args...)
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func gitWithEnv(t *testing.T, repo string, env []string, args ...string) string {
	t.Helper()
	full := append([]string(nil), args...)
	if repo != "" {
		full = append([]string{"-C", repo}, full...)
	}
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), env...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(full, " "), err, data)
	}
	return strings.TrimSpace(string(data))
}

func rawGit(repo string, args ...string) (string, error) {
	full := append([]string(nil), args...)
	if repo != "" {
		full = append([]string{"-C", repo}, full...)
	}
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	data, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(data)), err
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
