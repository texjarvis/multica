package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var capabilityCmd = &cobra.Command{
	Use:   "capability",
	Short: "Manage owner-granted agent provisioning",
	Long: `Grant, inspect, and revoke a workspace-and-agent provisioning capability.

Only a human workspace owner can create, inspect, or revoke a grant. An agent
cannot grant itself, and a grant cannot be expanded, transferred, or renewed.
Task tokens and cloud-node credentials are rejected.

A grant allows one agent to create and configure non-secret specialist
settings, bind skills already on the grant, add those specialists to an
allowlisted squad, and read a minimized runtime catalog. It does not copy
secrets and it does not change runtime permissions. Instructions and role
names are not OS isolation.

The grantee then uses the existing agent, skill, squad, and runtime commands.
Nothing in this command activates a grant by itself.`,
}

var capabilityGrantCmd = &cobra.Command{
	Use:   "grant",
	Short: "Create an owner-granted provisioning capability",
	Args:  cobra.NoArgs,
	RunE:  runCapabilityGrant,
}

var capabilityInspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Show the latest provisioning grant for an agent",
	Args:  cobra.NoArgs,
	RunE:  runCapabilityInspect,
}

var capabilityRevokeCmd = &cobra.Command{
	Use:   "revoke <grant-id>",
	Short: "Revoke an active provisioning grant",
	Args:  exactArgs(1),
	RunE:  runCapabilityRevoke,
}

func init() {
	capabilityCmd.AddCommand(capabilityGrantCmd)
	capabilityCmd.AddCommand(capabilityInspectCmd)
	capabilityCmd.AddCommand(capabilityRevokeCmd)

	capabilityGrantCmd.Flags().String("agent", "", "Grantee agent ID (required)")
	capabilityGrantCmd.Flags().StringArray("runtime-model", nil, "Allowed runtime and model as runtime_id=model; repeat for each pair. A trailing '=' allows only the runtime default model")
	capabilityGrantCmd.Flags().StringArray("skill", nil, "Workspace skill ID the grantee may bind")
	capabilityGrantCmd.Flags().StringArray("managed-agent", nil, "Existing agent ID the grantee may update")
	capabilityGrantCmd.Flags().StringArray("squad", nil, "Squad ID the grantee may add managed agents to")
	capabilityGrantCmd.Flags().StringArray("originator", nil, "Human user ID allowed to originate a provisioning task")
	capabilityGrantCmd.Flags().Int("max-new-agents", 0, "Maximum agents the grantee may create")
	capabilityGrantCmd.Flags().Int("max-concurrent-tasks", 1, "Maximum max_concurrent_tasks on a provisioned agent")
	capabilityGrantCmd.Flags().String("invocation-policy", "private", "Invocation ceiling: private or workspace")
	capabilityGrantCmd.Flags().String("expires-at", "", "Optional RFC3339 expiry")
	capabilityGrantCmd.Flags().String("output", "json", "Output format: json")

	capabilityInspectCmd.Flags().String("agent", "", "Grantee agent ID (required)")
	capabilityInspectCmd.Flags().String("output", "json", "Output format: json")
	capabilityRevokeCmd.Flags().String("output", "json", "Output format: json")
}

func runCapabilityGrant(cmd *cobra.Command, _ []string) error {
	agentID, _ := cmd.Flags().GetString("agent")
	runtimeModels, _ := cmd.Flags().GetStringArray("runtime-model")
	skills, _ := cmd.Flags().GetStringArray("skill")
	managed, _ := cmd.Flags().GetStringArray("managed-agent")
	squads, _ := cmd.Flags().GetStringArray("squad")
	originators, _ := cmd.Flags().GetStringArray("originator")
	maxNew, _ := cmd.Flags().GetInt("max-new-agents")
	maxConcurrent, _ := cmd.Flags().GetInt("max-concurrent-tasks")
	policy, _ := cmd.Flags().GetString("invocation-policy")
	expires, _ := cmd.Flags().GetString("expires-at")
	body, err := buildCapabilityGrantBody(agentID, runtimeModels, skills, managed, squads, originators, maxNew, maxConcurrent, policy, expires)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/agent-provisioning-grants", body, &resp); err != nil {
		return fmt.Errorf("grant provisioning capability: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runCapabilityInspect(cmd *cobra.Command, _ []string) error {
	agentID, _ := cmd.Flags().GetString("agent")
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("--agent is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp map[string]any
	path := "/api/agent-provisioning-grants?agent_id=" + url.QueryEscape(agentID)
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return fmt.Errorf("inspect provisioning capability: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runCapabilityRevoke(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp map[string]any
	path := "/api/agent-provisioning-grants/" + url.PathEscape(args[0]) + "/revoke"
	if err := client.PostJSON(ctx, path, map[string]any{}, &resp); err != nil {
		return fmt.Errorf("revoke provisioning capability: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

type capabilityRuntimeBody struct {
	RuntimeID string   `json:"runtime_id"`
	Models    []string `json:"models"`
}

func buildCapabilityGrantBody(agentID string, runtimeModels, skills, managed, squads, originators []string, maxNew, maxConcurrent int, policy, expires string) (map[string]any, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("--agent is required")
	}
	if policy != "private" && policy != "workspace" {
		return nil, fmt.Errorf("--invocation-policy must be private or workspace")
	}
	if maxNew < 0 || maxNew > 100 {
		return nil, fmt.Errorf("--max-new-agents must be between 0 and 100")
	}
	if maxConcurrent < 1 || maxConcurrent > 100 {
		return nil, fmt.Errorf("--max-concurrent-tasks must be between 1 and 100")
	}
	grouped := make([]capabilityRuntimeBody, 0)
	index := map[string]int{}
	for _, pair := range runtimeModels {
		runtimeID, model, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("--runtime-model must be runtime_id=model")
		}
		runtimeID = strings.TrimSpace(runtimeID)
		if runtimeID == "" {
			return nil, fmt.Errorf("--runtime-model requires a runtime id")
		}
		pos, seen := index[runtimeID]
		if !seen {
			index[runtimeID] = len(grouped)
			grouped = append(grouped, capabilityRuntimeBody{RuntimeID: runtimeID, Models: []string{model}})
			continue
		}
		grouped[pos].Models = append(grouped[pos].Models, model)
	}
	body := map[string]any{
		"agent_id":             agentID,
		"max_new_agents":       maxNew,
		"max_concurrent_tasks": maxConcurrent,
		"invocation_policy":    policy,
		"runtimes":             grouped,
		"skill_ids":            nonNil(skills),
		"managed_agent_ids":    nonNil(managed),
		"squad_ids":            nonNil(squads),
		"originator_user_ids":  nonNil(originators),
	}
	if strings.TrimSpace(expires) != "" {
		body["expires_at"] = strings.TrimSpace(expires)
	}
	return body, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
