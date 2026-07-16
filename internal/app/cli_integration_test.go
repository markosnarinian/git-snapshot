package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIEndToEnd(t *testing.T) {
	repoPath := repositoryWithInitialCommit(t)
	t.Setenv("GIT_SNAPSHOT_REPO", filepath.Join(t.TempDir(), "wrong-repository"))
	writeFile(t, repoPath, "file.txt", "snapshot one\n", 0o644)
	stdout, _, err := runTestCLI(t, "", "create", "--repo", repoPath, "--message", "one", "--json")
	must(t, err)
	var first CreateResult
	must(t, json.Unmarshal([]byte(stdout), &first))
	if first.OID == "" {
		t.Fatal("create JSON omitted object ID")
	}

	writeFile(t, repoPath, "file.txt", "snapshot two\n", 0o644)
	stdout, _, err = runTestCLI(t, "", "create", "--message", "two", "--json", "--repo", repoPath)
	must(t, err)
	var second CreateResult
	must(t, json.Unmarshal([]byte(stdout), &second))

	stdout, _, err = runTestCLI(t, "", "list", "--repo", repoPath, "--reverse", "--format", "%d")
	must(t, err)
	if got := strings.Fields(stdout); len(got) != 2 || got[0] != "1" || got[1] != "0" {
		t.Fatalf("reverse list distances = %q", stdout)
	}

	stdout, _, err = runTestCLI(t, "", "show", "latest", "--repo", repoPath, "--name-only")
	must(t, err)
	if !strings.Contains(stdout, "file.txt") {
		t.Fatalf("show --name-only output = %q", stdout)
	}
	stdout, _, err = runTestCLI(t, "", "diff", "1", "0", "--repo", repoPath, "--name-only")
	must(t, err)
	if !strings.Contains(stdout, "file.txt") {
		t.Fatalf("diff --name-only output = %q", stdout)
	}
	_, _, err = runTestCLI(t, "", "verify", "0", "--repo", repoPath)
	must(t, err)

	destination := filepath.Join(t.TempDir(), "restored")
	_, _, err = runTestCLI(t, "", "restore", "latest", "--destination", destination, "--repo", repoPath)
	must(t, err)
	assertFileContent(t, filepath.Join(destination, "file.txt"), "snapshot two\n")

	_, _, err = runTestCLI(t, "", "config", "set", "--repo", repoPath, "snapshot.color", "never")
	must(t, err)
	stdout, _, err = runTestCLI(t, "", "config", "get", "--repo", repoPath, "snapshot.color")
	must(t, err)
	if strings.TrimSpace(stdout) != "never" {
		t.Fatalf("config get output = %q", stdout)
	}
	_, _, err = runTestCLI(t, "", "config", "unset", "--repo", repoPath, "snapshot.color")
	must(t, err)

	stdout, _, err = runTestCLI(t, "", "drop", "--count", "1", "--yes", "--json", "--repo", repoPath)
	must(t, err)
	var dropped DropResult
	must(t, json.Unmarshal([]byte(stdout), &dropped))
	if dropped.NewTip != first.OID || len(dropped.Removed) != 1 || dropped.Removed[0].OID != second.OID {
		t.Fatalf("drop JSON = %#v", dropped)
	}
	stdout, _, err = runTestCLI(t, "", "delete", "--yes", "--json", "--repo", repoPath)
	must(t, err)
	var deleted DeleteResult
	must(t, json.Unmarshal([]byte(stdout), &deleted))
	if len(deleted.Removed) != 1 || deleted.Removed[0].OID != first.OID {
		t.Fatalf("delete JSON = %#v", deleted)
	}
}

func runTestCLI(t *testing.T, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errorOutput bytes.Buffer
	cli := &App{In: strings.NewReader(input), Out: &out, Err: &errorOutput}
	err = cli.Run(context.Background(), args)
	return out.String(), errorOutput.String(), err
}
