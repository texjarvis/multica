package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agentCopyCmd forks an existing agent's portable configuration into a brand-new
// agent, optionally on a different runtime, leaving the source untouched. It is
// the CLI/headless equivalent of the web "Duplicate" action (MUL-5279). The
// command is a thin composition over existing endpoints — GET the source, then
// POST a create — so it needs no dedicated server API: `POST /api/agents`
// already binds skill_ids in the same DB transaction as the agent row, so the
// copy never becomes visible in a partially configured state.
var agentCopyCmd = &cobra.Command{
	Use:   "copy <source-agent-id>",
	Short: "Copy an existing agent into a new one (optionally on a different runtime)",
	Long: `Copy an existing agent's portable configuration into a brand-new agent.

The source agent is left untouched. By default the copy lands on the same
runtime as the source; pass --runtime-id to fork it onto a different runtime.

Copied by default, each overridable with the matching flag: name (suffixed
" (copy)"), description, instructions, avatar, max_concurrent_tasks,
invocation permission (permission_mode + allow-list), assigned workspace skills,
and — only when the target runtime is unchanged — model, thinking_level and
service_tier.

Secret-bearing and machine-local fields are never copied: custom_args,
custom_env, mcp_config and runtime_config. Supply fresh values for the copy
with the matching explicit flags when the target agent needs them.

Runtime-specific fields do not travel across a runtime change: when --runtime-id
selects a different runtime, --model is required (pass --model "" to accept the
target runtime's default), and thinking_level / service_tier are dropped unless
set explicitly.`,
	Args: exactArgs(1),
	RunE: runAgentCopy,
}

func init() {
	agentCmd.AddCommand(agentCopyCmd)
	registerAgentCopyFlags(agentCopyCmd)
}

// registerAgentCopyFlags registers every flag runAgentCopy reads. It is shared
// between init() and the tests so both stay in lockstep.
func registerAgentCopyFlags(cmd *cobra.Command) {
	cmd.Flags().String("name", "", "Name for the new agent (default: \"<source name> (copy)\")")
	cmd.Flags().String("runtime-id", "", "Target runtime ID (default: the source agent's runtime). A different value forks the agent onto that runtime.")
	cmd.Flags().String("description", "", "Override the copied description")
	cmd.Flags().String("instructions", "", "Override the copied instructions")
	cmd.Flags().String("model", "", "Model identifier for the copy. Required when --runtime-id selects a different runtime (pass \"\" to accept the target runtime default). Empty otherwise = runtime default.")
	cmd.Flags().String("thinking-level", "", "Override thinking level. Not carried across a runtime change unless set here.")
	cmd.Flags().String("service-tier", "", "Override Codex service tier. Not carried across a runtime change unless set here.")
	cmd.Flags().String("custom-args", "", "Set fresh custom CLI arguments from inline JSON (unsafe for credentials; prefer --custom-args-file or --custom-args-stdin).")
	cmd.Flags().Bool("custom-args-stdin", false, "Read fresh custom CLI arguments from stdin. Mutually exclusive with --custom-args and --custom-args-file.")
	cmd.Flags().String("custom-args-file", "", "Read fresh custom CLI arguments from a file (suggested mode: 0600). Mutually exclusive with --custom-args and --custom-args-stdin.")
	cmd.Flags().Int32("max-concurrent-tasks", 6, "Override maximum concurrent tasks")
	cmd.Flags().String("visibility", "", "Override visibility: private or workspace (legacy; mapped to --permission-mode)")
	cmd.Flags().String("permission-mode", "", "Override invocation permission mode: private or public_to. Authoritative over --visibility.")
	cmd.Flags().Bool("public-to-workspace", false, "public_to: allow every workspace member to invoke the copy.")
	cmd.Flags().StringSlice("public-to-member", nil, "public_to: allow the given member user id(s) to invoke the copy. Repeatable.")
	cmd.Flags().Bool("no-skills", false, "Do not copy the source agent's workspace skill assignments.")
	// Secret / machine-local fields are never copied from the source; these
	// flags provide fresh values for the copy, mirroring 'agent create'.
	cmd.Flags().String("custom-env", "", "Set custom_env on the copy as a JSON object (never copied from the source). Prefer --custom-env-stdin/--custom-env-file for secrets. Pass '{}' for an empty map.")
	cmd.Flags().Bool("custom-env-stdin", false, "Read --custom-env from stdin. Mutually exclusive with --custom-env and --custom-env-file.")
	cmd.Flags().String("custom-env-file", "", "Read --custom-env from a file path (suggested mode: 0600). Mutually exclusive with --custom-env and --custom-env-stdin.")
	cmd.Flags().String("mcp-config", "", "Set mcp_config on the copy as a JSON object (never copied from the source). Prefer --mcp-config-stdin/--mcp-config-file for secrets.")
	cmd.Flags().Bool("mcp-config-stdin", false, "Read --mcp-config from stdin. Mutually exclusive with --mcp-config and --mcp-config-file.")
	cmd.Flags().String("mcp-config-file", "", "Read --mcp-config from a file path (suggested mode: 0600). Mutually exclusive with --mcp-config and --mcp-config-stdin.")
	cmd.Flags().String("runtime-config", "", "Set fresh runtime_config from inline JSON (unsafe for credentials; prefer --runtime-config-file or --runtime-config-stdin).")
	cmd.Flags().Bool("runtime-config-stdin", false, "Read fresh runtime_config JSON from stdin. Mutually exclusive with --runtime-config and --runtime-config-file.")
	cmd.Flags().String("runtime-config-file", "", "Read fresh runtime_config JSON from a file (suggested mode: 0600). Mutually exclusive with --runtime-config and --runtime-config-stdin.")
	cmd.Flags().String("output", "json", "Output format: table or json")
}

// runAgentCopy reads the source agent and assembles a create request from its
// portable fields, then POSTs it. custom_args, custom_env, mcp_config and
// runtime_config are never read back from the source — GET redacts / masks
// them anyway — and are set only when supplied explicitly on the command line.
func runAgentCopy(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if err := validateSingleJSONStdin(cmd, "runtime-config", "custom-args", "custom-env", "mcp-config"); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var src map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0], &src); err != nil {
		return fmt.Errorf("get source agent: %w", err)
	}
	src = sanitizeAgentForCLIOutput(src)

	srcRuntimeID := strVal(src, "runtime_id")

	// Resolve the target runtime: default to the source's runtime, override
	// with --runtime-id. A different value is a cross-runtime fork.
	targetRuntimeID := srcRuntimeID
	if cmd.Flags().Changed("runtime-id") {
		v, _ := cmd.Flags().GetString("runtime-id")
		if v == "" {
			return fmt.Errorf("--runtime-id must not be empty")
		}
		targetRuntimeID = v
	}
	if targetRuntimeID == "" {
		return fmt.Errorf("source agent has no runtime; pass --runtime-id to choose a target runtime")
	}
	sameRuntime := targetRuntimeID == srcRuntimeID

	// Name: default "<source name> (copy)", override with --name.
	name := strVal(src, "name") + " (copy)"
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		if v == "" {
			return fmt.Errorf("--name must not be empty")
		}
		name = v
	}

	body := map[string]any{
		"name":       name,
		"runtime_id": targetRuntimeID,
	}

	// Plain-text fields: copy from source, override with the matching flag.
	body["description"] = strVal(src, "description")
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	body["instructions"] = strVal(src, "instructions")
	if cmd.Flags().Changed("instructions") {
		v, _ := cmd.Flags().GetString("instructions")
		body["instructions"] = v
	}

	// Avatar reference travels with the copy (both agents point at the same
	// uploaded image URL), matching the web Duplicate flow.
	if av, ok := src["avatar_url"]; ok && av != nil {
		body["avatar_url"] = av
	}

	// custom_args are arbitrary secret-capable argv and generic GET never
	// returns their values. Set them only from fresh explicit input.
	if raw, ok, err := resolveJSONInput(cmd, "custom-args", "pass '[]' for no custom arguments"); err != nil {
		return err
	} else if ok {
		ca, err := parseCustomArgs(raw)
		if err != nil {
			return err
		}
		body["custom_args"] = ca
	}

	// max_concurrent_tasks: copy the source's value, override with the flag.
	if v, ok := src["max_concurrent_tasks"]; ok && v != nil {
		body["max_concurrent_tasks"] = v
	}
	if cmd.Flags().Changed("max-concurrent-tasks") {
		v, _ := cmd.Flags().GetInt32("max-concurrent-tasks")
		body["max_concurrent_tasks"] = v
	}

	// Runtime-specific fields (model / thinking_level / service_tier) only make
	// sense on the runtime they were chosen for. Copy them when staying on the
	// same runtime; when forking to a different runtime, drop them and require
	// an explicit --model (pass --model "" to accept the target default) —
	// mirroring the web Duplicate clearing model on a runtime switch.
	if sameRuntime {
		if v := strVal(src, "model"); v != "" {
			body["model"] = v
		}
		if v := strVal(src, "thinking_level"); v != "" {
			body["thinking_level"] = v
		}
		if v := strVal(src, "service_tier"); v != "" {
			body["service_tier"] = v
		}
	} else if !cmd.Flags().Changed("model") {
		return fmt.Errorf("copying to a different runtime (--runtime-id) requires --model, because the source model may not exist on the target runtime; pass --model \"\" to accept the target runtime default")
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	if cmd.Flags().Changed("service-tier") {
		v, _ := cmd.Flags().GetString("service-tier")
		body["service_tier"] = v
	}

	// Invocation permission: copy the source's permission_mode + allow-list by
	// default; any permission flag (or legacy --visibility) fully defines it
	// instead, so the two never mix.
	permOverride := cmd.Flags().Changed("permission-mode") ||
		cmd.Flags().Changed("public-to-workspace") ||
		cmd.Flags().Changed("public-to-member") ||
		cmd.Flags().Changed("visibility")
	if permOverride {
		if cmd.Flags().Changed("visibility") {
			v, _ := cmd.Flags().GetString("visibility")
			body["visibility"] = v
		}
		applyAgentPermissionFlags(cmd, body)
	} else {
		if pm := strVal(src, "permission_mode"); pm != "" {
			body["permission_mode"] = pm
		}
		if it, ok := src["invocation_targets"]; ok && it != nil {
			body["invocation_targets"] = it
		}
	}

	// Workspace skills: copy the source's bindings so they attach in the same
	// create transaction, unless --no-skills.
	if noSkills, _ := cmd.Flags().GetBool("no-skills"); !noSkills {
		if skills, ok := src["skills"].([]any); ok {
			ids := make([]string, 0, len(skills))
			for _, s := range skills {
				m, ok := s.(map[string]any)
				if !ok {
					continue
				}
				if id := strVal(m, "id"); id != "" {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				body["skill_ids"] = ids
			}
		}
	}

	// Secret / machine-local fields are never copied from the source (they are
	// redacted or masked on GET). Set them only when supplied explicitly, via
	// the same secret-safe channels as 'agent create'.
	if ce, ok, err := resolveCustomEnv(cmd); err != nil {
		return err
	} else if ok {
		body["custom_env"] = ce
	}
	if mc, ok, err := resolveMcpConfig(cmd); err != nil {
		return err
	} else if ok {
		body["mcp_config"] = mc
	}
	if raw, ok, err := resolveJSONInput(cmd, "runtime-config", "pass '{}' for an empty config"); err != nil {
		return err
	} else if ok {
		rc, err := parseRuntimeConfig(raw)
		if err != nil {
			return err
		}
		body["runtime_config"] = rc
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/agents", body, &result); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	if err := validateCommittedAgentCreateResponse(
		result,
		name,
		targetRuntimeID,
		args[0],
		"copy agent",
	); err != nil {
		return err
	}
	result = sanitizeAgentForCLIOutput(result)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent copied: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}
