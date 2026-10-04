package main

import (
	"strings"
	"testing"
)

func TestCapabilityHelpStatesOwnerBoundary(t *testing.T) {
	help := capabilityCmd.Long
	for _, want := range []string{
		"human workspace owner",
		"cannot grant itself",
		"Task tokens and cloud-node credentials are rejected",
		"does not copy",
		"does not change runtime permissions",
		"not OS isolation",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("capability help missing %q", want)
		}
	}
	if !strings.Contains(capabilityGrantCmd.Use, "grant") {
		t.Fatalf("grant command use = %q", capabilityGrantCmd.Use)
	}
	if !strings.Contains(capabilityRevokeCmd.Use, "revoke") {
		t.Fatalf("revoke command use = %q", capabilityRevokeCmd.Use)
	}
}

func TestBuildCapabilityGrantBodyGroupsRuntimeModels(t *testing.T) {
	body, err := buildCapabilityGrantBody(
		"agent-1",
		[]string{"rt-1=gpt-6-astra", "rt-1=grok-4.7", "rt-2="},
		[]string{"skill-1"},
		nil,
		[]string{"squad-1"},
		[]string{"user-1"},
		8,
		1,
		"workspace",
		"2030-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	if body["agent_id"] != "agent-1" {
		t.Fatalf("agent_id = %#v", body["agent_id"])
	}
	if body["invocation_policy"] != "workspace" {
		t.Fatalf("policy = %#v", body["invocation_policy"])
	}
	if body["expires_at"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("expires_at = %#v", body["expires_at"])
	}
	runtimes, ok := body["runtimes"].([]capabilityRuntimeBody)
	if !ok {
		t.Fatalf("runtimes type = %T", body["runtimes"])
	}
	if len(runtimes) != 2 {
		t.Fatalf("runtimes = %#v", runtimes)
	}
	if runtimes[0].RuntimeID != "rt-1" || len(runtimes[0].Models) != 2 || runtimes[0].Models[0] != "gpt-6-astra" || runtimes[0].Models[1] != "grok-4.7" {
		t.Fatalf("first runtime = %#v", runtimes[0])
	}
	if runtimes[1].RuntimeID != "rt-2" || len(runtimes[1].Models) != 1 || runtimes[1].Models[0] != "" {
		t.Fatalf("default model pair = %#v", runtimes[1])
	}
	if got := body["managed_agent_ids"].([]string); len(got) != 0 {
		t.Fatalf("nil managed agents became %#v", got)
	}
}

func TestBuildCapabilityGrantBodyRejectsBadFlags(t *testing.T) {
	if _, err := buildCapabilityGrantBody("", nil, nil, nil, nil, nil, 0, 1, "private", ""); err == nil {
		t.Fatal("missing agent was accepted")
	}
	if _, err := buildCapabilityGrantBody("a", nil, nil, nil, nil, nil, 0, 1, "public", ""); err == nil {
		t.Fatal("bad policy was accepted")
	}
	if _, err := buildCapabilityGrantBody("a", []string{"no-equals"}, nil, nil, nil, nil, 0, 1, "private", ""); err == nil {
		t.Fatal("runtime-model without '=' was accepted")
	}
	if _, err := buildCapabilityGrantBody("a", nil, nil, nil, nil, nil, -1, 1, "private", ""); err == nil {
		t.Fatal("negative max-new-agents was accepted")
	}
}
