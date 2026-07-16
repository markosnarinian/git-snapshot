package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const snapshotVersion = "1"

var errNotSnapshot = errors.New("commit was not created by git-snapshot")

type Snapshot struct {
	OID       string    `json:"oid"`
	Tree      string    `json:"tree"`
	Parents   []string  `json:"parents"`
	Subject   string    `json:"subject"`
	Message   string    `json:"message,omitempty"`
	Version   string    `json:"version"`
	Ref       string    `json:"ref"`
	Base      string    `json:"base"`
	CreatedAt time.Time `json:"createdAt"`
	Distance  int       `json:"distance"`
	Size      int64     `json:"size,omitempty"`
}

type Stream struct {
	Ref       string      `json:"ref"`
	Tip       string      `json:"tip"`
	Base      string      `json:"base"`
	Snapshots []*Snapshot `json:"snapshots"`
}

type rawCommit struct {
	Tree    string
	Parents []string
	Message string
}

func readRawCommit(ctx context.Context, git Git, oid string) (*rawCommit, error) {
	typeName, err := git.Run(ctx, "cat-file", "-t", oid)
	if err != nil {
		return nil, fail(ExitNotFound, fmt.Sprintf("object %s does not exist", oid), "Select a snapshot that still exists in this repository.", err)
	}
	if typeName != "commit" {
		return nil, fail(ExitVerification, fmt.Sprintf("object %s is a %s, not a commit", oid, typeName), "Choose a snapshot commit object.", nil)
	}
	data, err := git.RunBytes(ctx, "cat-file", "-p", oid)
	if err != nil {
		return nil, fail(ExitVerification, fmt.Sprintf("could not read commit %s", oid), "Run git fsck to inspect the object database.", err)
	}
	header, message, found := strings.Cut(string(data), "\n\n")
	if !found {
		return nil, fail(ExitVerification, fmt.Sprintf("commit %s has malformed contents", oid), "Run git fsck to inspect the object database.", nil)
	}
	commit := &rawCommit{Message: strings.TrimSuffix(message, "\n")}
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "tree ") {
			commit.Tree = strings.TrimPrefix(line, "tree ")
		}
		if strings.HasPrefix(line, "parent ") {
			commit.Parents = append(commit.Parents, strings.TrimPrefix(line, "parent "))
		}
	}
	if commit.Tree == "" {
		return nil, fail(ExitVerification, fmt.Sprintf("commit %s has no tree", oid), "Run git fsck to inspect the object database.", nil)
	}
	return commit, nil
}

func ReadSnapshot(ctx context.Context, git Git, oid, expectedRef, objectFormat string) (*Snapshot, error) {
	commit, err := readRawCommit(ctx, git, oid)
	if err != nil {
		return nil, err
	}
	trailers := map[string][]string{}
	for _, line := range strings.Split(commit.Message, "\n") {
		for _, key := range []string{"Git-Snapshot-Version", "Git-Snapshot-Ref", "Git-Snapshot-Base", "Git-Snapshot-Created-At"} {
			if value, ok := strings.CutPrefix(line, key+":"); ok {
				trailers[key] = append(trailers[key], strings.TrimSpace(value))
			}
		}
	}
	if len(trailers) == 0 {
		return nil, errNotSnapshot
	}
	for _, key := range []string{"Git-Snapshot-Version", "Git-Snapshot-Ref", "Git-Snapshot-Base", "Git-Snapshot-Created-At"} {
		if len(trailers[key]) != 1 || trailers[key][0] == "" {
			return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s has missing or duplicate %s metadata", oid, key), "Do not update this ref until its ownership has been investigated.", nil)
		}
	}
	if trailers["Git-Snapshot-Version"][0] != snapshotVersion {
		return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s uses unsupported metadata version %q", oid, trailers["Git-Snapshot-Version"][0]), "Upgrade git-snapshot or select a compatible stream.", nil)
	}
	claimedRef := trailers["Git-Snapshot-Ref"][0]
	if claimedRef != expectedRef {
		return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s claims ref %q, expected %q", oid, claimedRef, expectedRef), "Select the owning ref; never move commits between streams by hand.", nil)
	}
	base := trailers["Git-Snapshot-Base"][0]
	if base != "unborn" && !isFullOID(base, objectFormat) {
		return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s has invalid base object ID %q", oid, base), "Do not update this stream until the metadata is repaired or recovered.", nil)
	}
	createdAt, err := time.Parse(time.RFC3339, trailers["Git-Snapshot-Created-At"][0])
	if err != nil {
		return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s has invalid creation time", oid), "Do not update this stream until the metadata is repaired or recovered.", err)
	}
	lines := strings.Split(commit.Message, "\n")
	subject := ""
	if len(lines) > 0 {
		subject = lines[0]
	}
	return &Snapshot{
		OID:       oid,
		Tree:      commit.Tree,
		Parents:   commit.Parents,
		Subject:   subject,
		Message:   commit.Message,
		Version:   snapshotVersion,
		Ref:       claimedRef,
		Base:      base,
		CreatedAt: createdAt,
	}, nil
}

func VerifyStream(ctx context.Context, repo *Repository, git Git, ref string) (*Stream, error) {
	tip, exists, err := ReadRef(ctx, git, ref)
	if err != nil {
		return nil, fail(ExitFailure, fmt.Sprintf("could not read snapshot ref %q", ref), "Check repository permissions and retry.", err)
	}
	if !exists {
		return nil, fail(ExitNotFound, fmt.Sprintf("snapshot ref %q does not exist", ref), "Create a snapshot first or select another --ref.", nil)
	}
	stream := &Stream{Ref: ref, Tip: tip}
	current := tip
	for {
		snapshot, readErr := ReadSnapshot(ctx, git, current, ref, repo.ObjectFormat)
		if errors.Is(readErr, errNotSnapshot) {
			return nil, fail(ExitVerification, fmt.Sprintf("ref %q points into an unrelated commit chain at %s", ref, current), "Choose another ref; this ref will not be modified without an explicit future adoption workflow.", nil)
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(snapshot.Parents) > 1 {
			return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s has %d parents", current, len(snapshot.Parents)), "A snapshot stream must be a linear first-parent chain.", nil)
		}
		if stream.Base == "" {
			stream.Base = snapshot.Base
		} else if snapshot.Base != stream.Base {
			return nil, fail(ExitVerification, fmt.Sprintf("snapshot %s claims base %s, expected %s", current, snapshot.Base, stream.Base), "Do not update a stream whose base metadata changes mid-chain.", nil)
		}
		if err := requireObjectType(ctx, git, snapshot.Tree, "tree"); err != nil {
			return nil, err
		}
		snapshot.Distance = len(stream.Snapshots)
		stream.Snapshots = append(stream.Snapshots, snapshot)
		if len(snapshot.Parents) == 0 {
			if stream.Base != "unborn" {
				return nil, fail(ExitVerification, fmt.Sprintf("snapshot chain ended without its claimed base %s", stream.Base), "Run git fsck and inspect the stream before taking further action.", nil)
			}
			break
		}
		parent := snapshot.Parents[0]
		if stream.Base != "unborn" && parent == stream.Base {
			if err := requireObjectType(ctx, git, parent, "commit"); err != nil {
				return nil, err
			}
			break
		}
		current = parent
	}
	return stream, nil
}

func requireObjectType(ctx context.Context, git Git, oid, expected string) error {
	typeName, err := git.Run(ctx, "cat-file", "-t", oid)
	if err != nil {
		return fail(ExitVerification, fmt.Sprintf("required %s object %s is missing", expected, oid), "Run git fsck and recover the missing object before modifying this stream.", err)
	}
	if typeName != expected {
		return fail(ExitVerification, fmt.Sprintf("object %s is a %s, expected %s", oid, typeName, expected), "Do not modify this stream until its structure is understood.", nil)
	}
	return nil
}

func ResolveSelector(ctx context.Context, repo *Repository, git Git, ref, selector string, allowUnreachable bool) (*Snapshot, *Stream, error) {
	if selector == "" || selector == "latest" {
		selector = "0"
	}
	stream, err := VerifyStream(ctx, repo, git, ref)
	if err != nil {
		return nil, nil, err
	}
	if distance, parseErr := strconv.Atoi(selector); parseErr == nil {
		if distance < 0 || distance >= len(stream.Snapshots) {
			return nil, stream, fail(ExitNotFound, fmt.Sprintf("snapshot selector %q is outside this stream", selector), fmt.Sprintf("Choose a distance from 0 through %d.", len(stream.Snapshots)-1), nil)
		}
		return stream.Snapshots[distance], stream, nil
	}
	if !isFullOID(selector, repo.ObjectFormat) {
		return nil, stream, fail(ExitUsage, fmt.Sprintf("invalid snapshot selector %q", selector), "Use latest, a non-negative first-parent distance, or a full object ID.", nil)
	}
	for _, snapshot := range stream.Snapshots {
		if snapshot.OID == selector {
			return snapshot, stream, nil
		}
	}
	if !allowUnreachable {
		return nil, stream, fail(ExitSafety, fmt.Sprintf("object %s does not belong to snapshot stream %q", selector, ref), "Select the owning stream or use --allow-unreachable for a read-only inspection.", nil)
	}
	snapshot, readErr := ReadSnapshot(ctx, git, selector, ref, repo.ObjectFormat)
	if readErr != nil {
		if errors.Is(readErr, errNotSnapshot) {
			return nil, stream, fail(ExitVerification, fmt.Sprintf("object %s is not a git-snapshot commit", selector), "Choose a CLI-owned snapshot commit.", nil)
		}
		return nil, stream, readErr
	}
	return snapshot, stream, nil
}

func isFullOID(value, objectFormat string) bool {
	length := 40
	if objectFormat == "sha256" {
		length = 64
	}
	if len(value) != length {
		return false
	}
	matched, _ := regexp.MatchString("^[0-9a-fA-F]+$", value)
	return matched
}
