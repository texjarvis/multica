package main

import (
	"fmt"
	"strings"
)

// committedCreateResponseUnreadableError distinguishes an HTTP/API failure
// (safe to retry according to normal client policy) from a 2xx response whose
// committed resource identity cannot be trusted. The server may already have
// created the resource, so callers must inspect before retrying.
type committedCreateResponseUnreadableError struct {
	operation      string
	inspectCommand string
}

func (e *committedCreateResponseUnreadableError) Error() string {
	return fmt.Sprintf(
		"%s may have committed, but the success response identity was empty or mismatched; inspect with %q before retrying",
		e.operation,
		e.inspectCommand,
	)
}

func newCommittedCreateResponseUnreadableError(operation, inspectCommand string) error {
	return &committedCreateResponseUnreadableError{
		operation:      operation,
		inspectCommand: inspectCommand,
	}
}

func validateCommittedAgentCreateResponse(
	result map[string]any,
	expectedName string,
	expectedRuntimeID string,
	disallowedID string,
	operation string,
) error {
	id := strings.TrimSpace(strVal(result, "id"))
	if id == "" ||
		(disallowedID != "" && id == disallowedID) ||
		strVal(result, "name") != expectedName ||
		strVal(result, "runtime_id") != expectedRuntimeID {
		return newCommittedCreateResponseUnreadableError(operation, "multica agent list")
	}
	return nil
}

func validateCommittedRuntimeProfileCreateResponse(
	profile map[string]any,
	expectedWorkspaceID string,
	expectedDisplayName string,
	expectedProtocolFamily string,
	expectedCommandName string,
) error {
	if strings.TrimSpace(strVal(profile, "id")) == "" ||
		strVal(profile, "workspace_id") != expectedWorkspaceID ||
		strVal(profile, "display_name") != expectedDisplayName ||
		strVal(profile, "protocol_family") != expectedProtocolFamily ||
		strVal(profile, "command_name") != expectedCommandName {
		return newCommittedCreateResponseUnreadableError(
			"create runtime profile",
			"multica runtime profile list",
		)
	}
	return nil
}
