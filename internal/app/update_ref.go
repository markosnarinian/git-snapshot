package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func updateRefCAS(ctx context.Context, git Git, ref, newOID, expectedOID string, createReflog bool, timeout time.Duration, message string) error {
	args := []string{"update-ref"}
	if createReflog {
		args = append(args, "--create-reflog")
	} else {
		args = append(args, "--no-create-reflog")
	}
	args = append(args, "-m", message, ref, newOID, expectedOID)
	return runRefUpdate(ctx, git, args, ref, timeout)
}

func deleteRefCAS(ctx context.Context, git Git, ref, expectedOID string, timeout time.Duration, message string) error {
	args := []string{"update-ref", "-m", message, "-d", ref, expectedOID}
	return runRefUpdate(ctx, git, args, ref, timeout)
}

func runRefUpdate(ctx context.Context, git Git, args []string, ref string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := git.Run(ctx, args...)
		if err == nil {
			return nil
		}
		var ge *GitError
		if !errors.As(err, &ge) {
			return fail(ExitFailure, fmt.Sprintf("could not atomically update %q", ref), "Check repository permissions and retry. No ref update was committed.", err)
		}
		message := strings.ToLower(ge.Stderr)
		if strings.Contains(message, "but expected") || strings.Contains(message, "is at") && strings.Contains(message, "expected") || strings.Contains(message, "reference already exists") {
			return fail(ExitConcurrent, fmt.Sprintf("snapshot ref %q changed concurrently", ref), "Re-run the command against the new stream tip; the concurrent value was not overwritten.", err)
		}
		locked := strings.Contains(message, "cannot lock ref") || strings.Contains(message, "unable to create") && strings.Contains(message, ".lock")
		if !locked || timeout <= 0 || time.Now().After(deadline) {
			code := ExitFailure
			hint := "Check repository permissions and lock files, then retry. No partial ref update occurred."
			if locked {
				code = ExitConcurrent
				hint = "Wait for the other Git process to finish, then retry."
			}
			return fail(code, fmt.Sprintf("could not atomically update %q", ref), hint, err)
		}
		select {
		case <-ctx.Done():
			return fail(ExitFailure, "snapshot ref update was cancelled", "Retry when ready; no partial ref update occurred.", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
