package app

import (
	"context"
	"fmt"
	"time"
)

type DropResult struct {
	Ref     string      `json:"ref"`
	OldTip  string      `json:"oldTip"`
	NewTip  string      `json:"newTip,omitempty"`
	Removed []*Snapshot `json:"removed"`
}

func Drop(ctx context.Context, repo *Repository, git Git, ref, namespace, expectedTip string, count int, lockTimeout time.Duration, createReflog bool) (*DropResult, error) {
	if count < 1 {
		return nil, fail(ExitUsage, "drop count must be at least 1", "Pass --count with a positive integer.", nil)
	}
	if err := ValidateSnapshotRef(ctx, git, ref, namespace); err != nil {
		return nil, err
	}
	if err := repo.CheckLocks(ref, false); err != nil {
		return nil, err
	}
	stream, err := VerifyStream(ctx, repo, git, ref)
	if err != nil {
		return nil, err
	}
	if expectedTip == "" || stream.Tip != expectedTip {
		return nil, fail(ExitConcurrent, fmt.Sprintf("snapshot ref %q changed after the drop preview", ref), "Review the updated stream and confirm again; no ref was changed.", nil)
	}
	if count >= len(stream.Snapshots) {
		return nil, fail(ExitSafety, fmt.Sprintf("dropping %d snapshots would empty this %d-snapshot stream", count, len(stream.Snapshots)), "Use delete to remove the complete stream; drop always leaves a CLI-owned snapshot tip and writes a reflog entry.", nil)
	}
	result := &DropResult{Ref: ref, OldTip: stream.Tip, Removed: stream.Snapshots[:count]}
	result.NewTip = stream.Snapshots[count].OID
	if err := updateRefCAS(ctx, git, ref, result.NewTip, stream.Tip, createReflog, lockTimeout, fmt.Sprintf("git-snapshot: drop %d", count)); err != nil {
		return nil, err
	}
	return result, nil
}

type DeleteResult struct {
	Ref     string      `json:"ref"`
	OldTip  string      `json:"oldTip"`
	Removed []*Snapshot `json:"removed"`
}

func Delete(ctx context.Context, repo *Repository, git Git, ref, namespace, expectedTip string, lockTimeout time.Duration) (*DeleteResult, error) {
	if err := ValidateSnapshotRef(ctx, git, ref, namespace); err != nil {
		return nil, err
	}
	if err := repo.CheckLocks(ref, false); err != nil {
		return nil, err
	}
	stream, err := VerifyStream(ctx, repo, git, ref)
	if err != nil {
		return nil, err
	}
	if expectedTip == "" || stream.Tip != expectedTip {
		return nil, fail(ExitConcurrent, fmt.Sprintf("snapshot ref %q changed after the delete preview", ref), "Review the updated stream and confirm again; no ref was changed.", nil)
	}
	if err := deleteRefCAS(ctx, git, ref, stream.Tip, lockTimeout, "git-snapshot: delete stream"); err != nil {
		return nil, err
	}
	return &DeleteResult{Ref: ref, OldTip: stream.Tip, Removed: stream.Snapshots}, nil
}
