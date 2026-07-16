package app

import (
	"context"
	"fmt"
	"strings"
)

var protectedRefPrefixes = []string{
	"refs/heads/",
	"refs/tags/",
	"refs/remotes/",
	"refs/notes/",
	"refs/bisect/",
	"refs/replace/",
	"refs/rewritten/",
}

func ValidateNamespace(ctx context.Context, git Git, namespace string) error {
	if namespace == "" || !strings.HasPrefix(namespace, "refs/") || !strings.HasSuffix(namespace, "/") {
		return fail(ExitUsage, "snapshot namespace must be a full refs/ prefix ending in /", "Use a namespace such as refs/snapshots/.", nil)
	}
	probe := namespace + "validation"
	if err := rejectProtectedRef(probe); err != nil {
		return fail(ExitSafety, "snapshot namespace overlaps a protected Git namespace", "Choose a dedicated custom namespace such as refs/snapshots/.", err)
	}
	if _, err := git.Run(ctx, "check-ref-format", probe); err != nil {
		return fail(ExitUsage, fmt.Sprintf("invalid snapshot namespace %q", namespace), "Use a well-formed full ref prefix such as refs/snapshots/.", err)
	}
	return nil
}

func ValidateSnapshotRef(ctx context.Context, git Git, ref, namespace string) error {
	if ref == "" || ref == "HEAD" || !strings.HasPrefix(ref, "refs/") {
		return fail(ExitUsage, "snapshot ref must be a full ref name", "Use a name such as refs/snapshots/default.", nil)
	}
	if err := rejectProtectedRef(ref); err != nil {
		return err
	}
	if err := ValidateNamespace(ctx, git, namespace); err != nil {
		return err
	}
	if !strings.HasPrefix(ref, namespace) || ref == strings.TrimSuffix(namespace, "/") {
		return fail(ExitSafety, fmt.Sprintf("snapshot ref %q is outside permitted namespace %q", ref, namespace), "Select a ref inside the configured snapshot namespace.", nil)
	}
	if _, err := git.Run(ctx, "check-ref-format", ref); err != nil {
		return fail(ExitUsage, fmt.Sprintf("invalid snapshot ref %q", ref), "Use a well-formed full ref such as refs/snapshots/default.", err)
	}
	if target, err := git.Run(ctx, "symbolic-ref", "-q", ref); err == nil && target != "" {
		return fail(ExitSafety, fmt.Sprintf("snapshot ref %q is symbolic", ref), "Choose a direct ref inside the snapshot namespace.", nil)
	}
	return nil
}

func rejectProtectedRef(ref string) error {
	if ref == "refs/stash" || strings.HasPrefix(ref, "refs/stash/") {
		return fail(ExitSafety, fmt.Sprintf("ref %q is protected", ref), "Use a dedicated custom snapshot namespace.", nil)
	}
	for _, prefix := range protectedRefPrefixes {
		if strings.HasPrefix(ref, prefix) {
			return fail(ExitSafety, fmt.Sprintf("ref %q is in protected namespace %q", ref, prefix), "Use a dedicated custom snapshot namespace.", nil)
		}
	}
	return nil
}

func ReadRef(ctx context.Context, git Git, ref string) (string, bool, error) {
	oid, err := git.Run(ctx, "show-ref", "--verify", "--hash", ref)
	if err != nil {
		if isGitExit(err, 1) || isGitExit(err, 128) {
			return "", false, nil
		}
		return "", false, err
	}
	return oid, true, nil
}
