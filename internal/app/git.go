package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type GitError struct {
	Args   []string
	Stderr string
	Code   int
}

func (e *GitError) Error() string {
	message := strings.TrimSpace(e.Stderr)
	if message == "" {
		message = fmt.Sprintf("git exited with status %d", e.Code)
	}
	return message
}

type Git struct {
	Repo    string
	Env     map[string]string
	Verbose bool
	Trace   io.Writer
}

func (g Git) WithEnv(values map[string]string) Git {
	clone := g
	clone.Env = make(map[string]string, len(g.Env)+len(values))
	for key, value := range g.Env {
		clone.Env[key] = value
	}
	for key, value := range values {
		clone.Env[key] = value
	}
	return clone
}

func (g Git) command(ctx context.Context, input io.Reader, stdout, stderr io.Writer, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if g.Repo != "" {
		full = append(full, "-C", g.Repo)
	}
	full = append(full, args...)
	if g.Verbose && g.Trace != nil {
		fmt.Fprintf(g.Trace, "+ git %s\n", quoteArgs(full))
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Stdin = input
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	environment := make(map[string]string, len(g.Env)+1)
	// Read-only Git commands may otherwise refresh and rewrite the real index.
	// Required locks (update-ref, read-tree, alternate-index writes) are not
	// disabled by GIT_OPTIONAL_LOCKS=0.
	environment["GIT_OPTIONAL_LOCKS"] = "0"
	for key, value := range g.Env {
		environment[key] = value
	}
	cmd.Env = mergeEnv(os.Environ(), environment)
	return cmd
}

func (g Git) Run(ctx context.Context, args ...string) (string, error) {
	data, err := g.RunBytes(ctx, args...)
	return strings.TrimSpace(string(data)), err
}

func (g Git) RunBytes(ctx context.Context, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	err := g.command(ctx, nil, &stdout, &stderr, args...).Run()
	if err != nil {
		return nil, makeGitError(args, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// RunBytesExit returns stdout when Git succeeds or exits with one of the
// explicitly accepted nonzero statuses. This is useful for read-only commands
// such as git diff --no-index, where status 1 means "different", not failure.
func (g Git) RunBytesExit(ctx context.Context, acceptedExit int, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	err := g.command(ctx, nil, &stdout, &stderr, args...).Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	gitErr := makeGitError(args, stderr.String(), err)
	if isGitExit(gitErr, acceptedExit) {
		return stdout.Bytes(), nil
	}
	return nil, gitErr
}

func (g Git) RunInput(ctx context.Context, input []byte, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := g.command(ctx, bytes.NewReader(input), &stdout, &stderr, args...).Run()
	if err != nil {
		return "", makeGitError(args, stderr.String(), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (g Git) RunInputBytesExit(ctx context.Context, input []byte, acceptedExit int, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	err := g.command(ctx, bytes.NewReader(input), &stdout, &stderr, args...).Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	gitErr := makeGitError(args, stderr.String(), err)
	if isGitExit(gitErr, acceptedExit) {
		return stdout.Bytes(), nil
	}
	return nil, gitErr
}

func (g Git) Stream(ctx context.Context, stdout io.Writer, args ...string) error {
	var stderr bytes.Buffer
	err := g.command(ctx, nil, stdout, &stderr, args...).Run()
	if err != nil {
		return makeGitError(args, stderr.String(), err)
	}
	return nil
}

func makeGitError(args []string, stderr string, err error) error {
	code := 1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	return &GitError{Args: append([]string(nil), args...), Stderr: stderr, Code: code}
}

func mergeEnv(base []string, changes map[string]string) []string {
	if len(changes) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(changes))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := changes[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range changes {
		result = append(result, key+"="+value)
	}
	return result
}

func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			quoted[i] = fmt.Sprintf("%q", arg)
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}

func isGitExit(err error, code int) bool {
	var ge *GitError
	return errors.As(err, &ge) && ge.Code == code
}
