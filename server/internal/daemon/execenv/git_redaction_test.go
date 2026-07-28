package execenv

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestLogGitCommandFailureNeverLogsOutputOrErrorContent(t *testing.T) {
	const sentinel = "git-output-secret-sentinel"
	output := []byte("remote rejected credential " + sentinel)
	err := errors.New("git error containing " + sentinel)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logGitCommandFailure(logger, "worktree_remove", output, err)

	logged := buf.String()
	if strings.Contains(logged, sentinel) ||
		strings.Contains(logged, string(output)) ||
		strings.Contains(logged, err.Error()) {
		t.Fatalf("git failure log exposed command output or error content: %s", logged)
	}
	if !strings.Contains(logged, "output_byte_count="+strconv.Itoa(len(output))) {
		t.Fatalf("git failure log omitted safe output length metadata: %s", logged)
	}
}
