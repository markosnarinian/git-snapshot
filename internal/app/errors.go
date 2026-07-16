package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ExitOK           = 0
	ExitFailure      = 1
	ExitUsage        = 2
	ExitSafety       = 3
	ExitNotFound     = 4
	ExitConcurrent   = 5
	ExitVerification = 6
)

// ExitError carries the stable process exit code and actionable safety context.
type ExitError struct {
	Code    int
	Message string
	Hint    string
	Changed bool
	Cause   error
}

func (e *ExitError) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *ExitError) Unwrap() error { return e.Cause }

func fail(code int, message, hint string, cause error) error {
	return &ExitError{Code: code, Message: message, Hint: hint, Cause: cause}
}

func failChanged(code int, message, hint string, cause error) error {
	return &ExitError{Code: code, Message: message, Hint: hint, Changed: true, Cause: cause}
}

func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitFailure
}

func WriteError(w io.Writer, err error, asJSON bool) {
	var ee *ExitError
	if !errors.As(err, &ee) {
		ee = &ExitError{Code: ExitFailure, Message: "operation failed", Cause: err}
	}
	if asJSON {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      false,
			"code":    ee.Code,
			"error":   ee.Error(),
			"changed": ee.Changed,
			"hint":    ee.Hint,
		})
		return
	}
	fmt.Fprintf(w, "git-snapshot: %s\n", ee.Error())
	if ee.Changed {
		fmt.Fprintf(w, "The destination or working tree may have changed before the failure.\n")
	} else {
		fmt.Fprintf(w, "Nothing was changed.\n")
	}
	if ee.Hint != "" {
		fmt.Fprintf(w, "Next: %s\n", ee.Hint)
	}
}
