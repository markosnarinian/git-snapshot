package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func CheckConflicts(ctx context.Context, git Git) (bool, error) {
	data, err := git.RunBytes(ctx, "ls-files", "--unmerged", "-z")
	if err != nil {
		return false, fail(ExitFailure, "could not inspect the index for unresolved conflicts", "Run git status to diagnose the index.", err)
	}
	return len(data) > 0, nil
}

func CheckDirtySubmodules(ctx context.Context, git Git) ([]string, error) {
	data, err := git.RunBytes(ctx, "status", "--porcelain=v2", "-z", "--ignore-submodules=none")
	if err != nil {
		return nil, fail(ExitFailure, "could not inspect submodule state", "Run git status --ignore-submodules=none and resolve any errors.", err)
	}
	var dirty []string
	for _, record := range strings.Split(string(data), "\x00") {
		if !strings.HasPrefix(record, "1 ") && !strings.HasPrefix(record, "2 ") {
			continue
		}
		fields := strings.Fields(record)
		if len(fields) < 3 || len(fields[2]) != 4 || fields[2][0] != 'S' || fields[2] == "S..." {
			continue
		}
		path := fields[len(fields)-1]
		dirty = append(dirty, path)
	}
	sort.Strings(dirty)
	return dirty, nil
}

func PreflightCreate(ctx context.Context, repo *Repository, git Git, ref, namespace string, allowInProgress, allowDirtySubmodules bool) error {
	if err := repo.RequireWorktree(); err != nil {
		return err
	}
	if err := ValidateSnapshotRef(ctx, git, ref, namespace); err != nil {
		return err
	}
	if err := repo.CheckLocks(ref, true); err != nil {
		return err
	}
	active := repo.InProgressOperations()
	sort.Strings(active)
	conflicts, err := CheckConflicts(ctx, git)
	if err != nil {
		return err
	}
	if (len(active) > 0 || conflicts) && !allowInProgress {
		details := strings.Join(active, ", ")
		if conflicts {
			if details != "" {
				details += "; "
			}
			details += "unresolved conflicts"
		}
		return fail(ExitSafety, fmt.Sprintf("snapshot creation refused during %s", details), "Finish or abort the Git operation, or explicitly pass --allow-in-progress after reviewing the risk.", nil)
	}
	dirty, err := CheckDirtySubmodules(ctx, git)
	if err != nil {
		return err
	}
	if len(dirty) > 0 && !allowDirtySubmodules {
		return fail(ExitSafety, fmt.Sprintf("dirty submodules cannot be represented fully: %s", strings.Join(dirty, ", ")), "Snapshot or clean each submodule first, or pass --allow-dirty-submodules to record only their gitlink commits.", nil)
	}
	return nil
}
