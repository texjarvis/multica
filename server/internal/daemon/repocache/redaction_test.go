package repocache

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestSafeGitCommandErrorNeverIncludesCommandOutput(t *testing.T) {
	const sentinel = "repo-git-output-secret-sentinel"
	output := []byte("remote rejected credential " + sentinel)

	err := safeGitCommandError("git fetch", output, errors.New("exit status 1"))
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), string(output)) {
		t.Fatalf("safe git error exposed command output: %v", err)
	}
	if !strings.Contains(err.Error(), "output bytes: "+strconv.Itoa(len(output))) {
		t.Fatalf("safe git error omitted output length: %v", err)
	}
}

func TestGitBranchCollisionClassificationDoesNotExposeBranchName(t *testing.T) {
	const sentinel = "branch-secret-sentinel"
	output := []byte("fatal: a branch named '" + sentinel + "' already exists")

	err := gitCommandErrorWithBranchCollision("git worktree add", output, errors.New("exit status 128"))
	if !isBranchCollisionError(err) {
		t.Fatalf("branch collision was not classified: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), string(output)) {
		t.Fatalf("branch collision error exposed git output: %v", err)
	}
}
