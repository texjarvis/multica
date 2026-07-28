package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// addCommonProfileFlags wires the persistent-style flags the run functions
// resolve (server-url, workspace-id, profile, token) onto a detached test
// command so the helpers can be invoked directly.
func addCommonProfileFlags(cmd *cobra.Command) {
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("token", "", "")
}

func newProfileListTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newProfileCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("protocol-family", "", "")
	cmd.Flags().String("command-name", "", "")
	cmd.Flags().String("display-name", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("fixed-args", "", "")
	cmd.Flags().Bool("fixed-args-stdin", false, "")
	cmd.Flags().String("fixed-args-file", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newProfileUpdateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "update"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("display-name", "", "")
	cmd.Flags().String("command-name", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("fixed-args", "", "")
	cmd.Flags().Bool("fixed-args-stdin", false, "")
	cmd.Flags().String("fixed-args-file", "", "")
	cmd.Flags().Bool("clear-fixed-args", false, "")
	cmd.Flags().Bool("enabled", true, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newProfileDeleteTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "delete"}
	addCommonProfileFlags(cmd)
	return cmd
}

func newProfileSetPathTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "set-path"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("path", "", "")
	return cmd
}

func newProfileUnsetPathTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "unset-path"}
	addCommonProfileFlags(cmd)
	return cmd
}

// TestRuntimeProfileCommandsRegistered verifies the subcommands are wired
// under `runtime profile`.
func TestRuntimeProfileCommandsRegistered(t *testing.T) {
	for _, name := range []string{"list", "create", "update", "delete", "set-path", "unset-path"} {
		cmd, _, err := runtimeProfileCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %q: %v", name, err)
		}
		if cmd == nil || cmd.Name() != name {
			t.Fatalf("%q not registered under `runtime profile`; got %#v", name, cmd)
		}
	}
	// And `profile` itself must hang off `runtime`.
	cmd, _, err := runtimeCmd.Find([]string{"profile", "list"})
	if err != nil || cmd == nil || cmd.Name() != "list" {
		t.Fatalf("`runtime profile list` not reachable from runtime command: %v / %#v", err, cmd)
	}
	for _, command := range []*cobra.Command{runtimeProfileCreateCmd, runtimeProfileUpdateCmd} {
		for _, flag := range []string{"fixed-args", "fixed-args-stdin", "fixed-args-file"} {
			if command.Flags().Lookup(flag) == nil {
				t.Fatalf("%s missing --%s", command.CommandPath(), flag)
			}
		}
	}
}

func TestRunRuntimeProfileList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runtime_profiles": []map[string]any{
				{"id": "prof-1", "display_name": "Company Codex", "protocol_family": "codex", "command_name": "company-codex", "visibility": "workspace", "enabled": true},
			},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProfileListTestCmd()
	_ = cmd.Flags().Set("output", "json")
	if err := runRuntimeProfileList(cmd, nil); err != nil {
		t.Fatalf("runRuntimeProfileList: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", gotMethod)
	}
	if gotPath != "/api/workspaces/ws-123/runtime-profiles" {
		t.Errorf("path = %q, want /api/workspaces/ws-123/runtime-profiles", gotPath)
	}
}

func TestRuntimeProfileListOutputRedactsLegacyFixedArgs(t *testing.T) {
	const secret = "sentinel-cli-runtime-profile-secret"
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runtime_profiles": []map[string]any{{
				"id":              "prof-1",
				"display_name":    "Safe profile",
				"protocol_family": "codex",
				"command_name":    "company-codex",
				"fixed_args":      []string{"--token", secret},
				"unknown_secret":  secret,
				"enabled":         true,
			}},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	for _, outputMode := range []string{"json", "table"} {
		t.Run(outputMode, func(t *testing.T) {
			cmd := newProfileListTestCmd()
			if err := cmd.Flags().Set("output", outputMode); err != nil {
				t.Fatalf("set output: %v", err)
			}
			output, err := captureStdout(t, func() error {
				return runRuntimeProfileList(cmd, nil)
			})
			if err != nil {
				t.Fatalf("runtime profile list failed: %v", err)
			}
			if strings.Contains(output, secret) {
				t.Fatalf("runtime profile output leaked fixed args: %s", output)
			}
			if !strings.Contains(output, "Safe profile") {
				t.Fatalf("runtime profile output lost safe fields: %s", output)
			}
		})
	}
}

func TestRuntimeProfileUpdateCLIAddsExplicitFixedArgsIntent(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	tests := []struct {
		name       string
		configure  func(*cobra.Command)
		wantIntent string
		wantArgs   []any
	}{
		{
			name: "replace",
			configure: func(cmd *cobra.Command) {
				_ = cmd.Flags().Set("fixed-args", `["--profile","fresh"]`)
			},
			wantIntent: "replace",
			wantArgs:   []any{"--profile", "fresh"},
		},
		{
			name: "clear",
			configure: func(cmd *cobra.Command) {
				_ = cmd.Flags().Set("clear-fixed-args", "true")
			},
			wantIntent: "clear",
			wantArgs:   []any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":           "prof-1",
					"display_name": "Safe profile",
				})
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)

			cmd := newProfileUpdateTestCmd()
			tc.configure(cmd)
			if _, err := captureStdout(t, func() error {
				return runRuntimeProfileUpdate(cmd, []string{"prof-1"})
			}); err != nil {
				t.Fatalf("profile update: %v", err)
			}
			if gotBody["fixed_args_intent"] != tc.wantIntent {
				t.Fatalf("intent = %#v, want %q; body=%v", gotBody["fixed_args_intent"], tc.wantIntent, gotBody)
			}
			if !reflect.DeepEqual(gotBody["fixed_args"], tc.wantArgs) {
				t.Fatalf("fixed_args = %#v, want %#v", gotBody["fixed_args"], tc.wantArgs)
			}
		})
	}
}

func TestRunRuntimeProfileCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")

	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              "prof-1",
			"workspace_id":    "ws-123",
			"display_name":    "Company Codex",
			"protocol_family": "codex",
			"command_name":    "company-codex",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProfileCreateTestCmd()
	_ = cmd.Flags().Set("protocol-family", "codex")
	_ = cmd.Flags().Set("command-name", "company-codex")
	_ = cmd.Flags().Set("display-name", "Company Codex")

	if err := runRuntimeProfileCreate(cmd, nil); err != nil {
		t.Fatalf("runRuntimeProfileCreate: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/workspaces/ws-123/runtime-profiles" {
		t.Errorf("path = %q, want /api/workspaces/ws-123/runtime-profiles", gotPath)
	}
	if gotBody["protocol_family"] != "codex" || gotBody["command_name"] != "company-codex" || gotBody["display_name"] != "Company Codex" {
		t.Errorf("unexpected body: %#v", gotBody)
	}
	// Omitted fixed_args must stay omitted.
	if _, present := gotBody["fixed_args"]; present {
		t.Errorf("fixed_args must not be sent when omitted, got %#v", gotBody["fixed_args"])
	}
	// visibility is intentionally NOT exposed by the CLI in v1 (server forces
	// 'workspace'), so it must never be sent.
	if _, present := gotBody["visibility"]; present {
		t.Errorf("visibility must not be sent by the CLI, got %#v", gotBody["visibility"])
	}
}

func TestRuntimeProfileCreateRejectsUnreadableCommittedIdentityWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response map[string]any
	}{
		{
			name: "empty id",
			response: map[string]any{
				"id":              "",
				"workspace_id":    "ws-123",
				"display_name":    "Company Codex",
				"protocol_family": "codex",
				"command_name":    "company-codex",
			},
		},
		{
			name: "mismatched workspace identity",
			response: map[string]any{
				"id":              "prof-1",
				"workspace_id":    "ws-other",
				"display_name":    "Different Profile",
				"protocol_family": "claude",
				"command_name":    "different-command",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MULTICA_TOKEN", "test-token")
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
			t.Setenv("MULTICA_AGENT_ID", "")
			t.Setenv("MULTICA_TASK_ID", "")

			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				_ = json.NewEncoder(w).Encode(tc.response)
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)

			cmd := newProfileCreateTestCmd()
			_ = cmd.Flags().Set("protocol-family", "codex")
			_ = cmd.Flags().Set("command-name", "company-codex")
			_ = cmd.Flags().Set("display-name", "Company Codex")

			err := runRuntimeProfileCreate(cmd, nil)
			var committedErr *committedCreateResponseUnreadableError
			if !errors.As(err, &committedErr) {
				t.Fatalf("error=%v, want committedCreateResponseUnreadableError", err)
			}
			if !strings.Contains(err.Error(), "may have committed") ||
				!strings.Contains(err.Error(), "multica runtime profile list") {
				t.Fatalf("error lacks inspect-before-retry guidance: %v", err)
			}
			if requests != 1 {
				t.Fatalf("request count=%d, want exactly one (no automatic retry)", requests)
			}
		})
	}
}

func TestRuntimeProfileFixedArgsSafeInputChannels(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	tests := []struct {
		name      string
		makeCmd   func() *cobra.Command
		configure func(*testing.T, *cobra.Command)
		run       func(*cobra.Command) error
	}{
		{
			name:    "create file",
			makeCmd: newProfileCreateTestCmd,
			configure: func(t *testing.T, cmd *cobra.Command) {
				path := filepath.Join(t.TempDir(), "fixed-args.json")
				if err := os.WriteFile(path, []byte(`["--token","file-secret"]`), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Flags().Set("protocol-family", "codex")
				_ = cmd.Flags().Set("command-name", "safe-codex")
				_ = cmd.Flags().Set("display-name", "Safe Codex")
				_ = cmd.Flags().Set("fixed-args-file", path)
			},
			run: func(cmd *cobra.Command) error {
				return runRuntimeProfileCreate(cmd, nil)
			},
		},
		{
			name:    "update stdin",
			makeCmd: newProfileUpdateTestCmd,
			configure: func(_ *testing.T, cmd *cobra.Command) {
				cmd.SetIn(strings.NewReader(`["--token","stdin-secret"]`))
				_ = cmd.Flags().Set("fixed-args-stdin", "true")
			},
			run: func(cmd *cobra.Command) error {
				return runRuntimeProfileUpdate(cmd, []string{"prof-1"})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				response := map[string]any{"id": "prof-1"}
				if r.Method == http.MethodPost {
					response["workspace_id"] = "ws-123"
					response["display_name"] = gotBody["display_name"]
					response["protocol_family"] = gotBody["protocol_family"]
					response["command_name"] = gotBody["command_name"]
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)

			cmd := tc.makeCmd()
			tc.configure(t, cmd)
			if _, err := captureStdout(t, func() error { return tc.run(cmd) }); err != nil {
				t.Fatalf("command failed: %v", err)
			}
			wantSecret := "stdin-secret"
			if tc.name == "create file" {
				wantSecret = "file-secret"
			}
			if got := gotBody["fixed_args"]; !reflect.DeepEqual(got, []any{"--token", wantSecret}) {
				t.Fatalf("fixed_args = %#v, want [--token %s]", got, wantSecret)
			}
			if tc.name == "update stdin" && gotBody["fixed_args_intent"] != "replace" {
				t.Fatalf("fixed_args_intent = %#v, want replace", gotBody["fixed_args_intent"])
			}
		})
	}
}

func TestRuntimeProfileFixedArgsInputValidationIsContentFree(t *testing.T) {
	const secret = "sentinel-fixed-args-cli-secret"

	t.Run("channels are mutually exclusive", func(t *testing.T) {
		cmd := newProfileCreateTestCmd()
		_ = cmd.Flags().Set("fixed-args", `["`+secret+`"]`)
		_ = cmd.Flags().Set("fixed-args-file", filepath.Join(t.TempDir(), "args.json"))
		_, _, err := resolveJSONInput(cmd, "fixed-args", "pass '[]' for no fixed arguments")
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected mutual-exclusion error, got %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validation error leaked input: %v", err)
		}
	})

	t.Run("empty stdin is rejected", func(t *testing.T) {
		cmd := newProfileUpdateTestCmd()
		cmd.SetIn(strings.NewReader(" \n"))
		_ = cmd.Flags().Set("fixed-args-stdin", "true")
		_, _, err := resolveJSONInput(cmd, "fixed-args", "use --clear-fixed-args to clear")
		if err == nil || !strings.Contains(err.Error(), "empty input") {
			t.Fatalf("expected empty-input error, got %v", err)
		}
	})

	t.Run("inline input warns without echoing payload", func(t *testing.T) {
		cmd := newProfileCreateTestCmd()
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		_ = cmd.Flags().Set("fixed-args", `["`+secret+`"]`)
		raw, ok, err := resolveJSONInput(cmd, "fixed-args", "pass '[]' for no fixed arguments")
		if err != nil || !ok || !strings.Contains(raw, secret) {
			t.Fatalf("resolve inline input: ok=%v err=%v", ok, err)
		}
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("warning leaked inline payload: %q", stderr.String())
		}
		if !strings.HasSuffix(stderr.String(), "\n") || strings.Contains(stderr.String(), `\n`) {
			t.Fatalf("warning must use a real newline: %q", stderr.String())
		}
	})

	t.Run("clear conflicts with every replacement channel", func(t *testing.T) {
		for _, channel := range []string{"fixed-args", "fixed-args-stdin", "fixed-args-file"} {
			cmd := newProfileUpdateTestCmd()
			if channel == "fixed-args-stdin" {
				cmd.SetIn(strings.NewReader(`["fresh"]`))
				_ = cmd.Flags().Set(channel, "true")
			} else if channel == "fixed-args-file" {
				path := filepath.Join(t.TempDir(), "args.json")
				if err := os.WriteFile(path, []byte(`["fresh"]`), 0o600); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Flags().Set(channel, path)
			} else {
				_ = cmd.Flags().Set(channel, `["fresh"]`)
			}
			_ = cmd.Flags().Set("clear-fixed-args", "true")
			err := runRuntimeProfileUpdate(cmd, []string{"prof-1"})
			if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
				t.Fatalf("%s: expected mutual-exclusion error, got %v", channel, err)
			}
		}
	})
}

func TestRunRuntimeProfileCreateRejectsBadFamily(t *testing.T) {
	cmd := newProfileCreateTestCmd()
	_ = cmd.Flags().Set("protocol-family", "not-a-real-backend")
	_ = cmd.Flags().Set("command-name", "x")
	_ = cmd.Flags().Set("display-name", "X")
	// No server should ever be contacted; this must fail client-side.
	if err := runRuntimeProfileCreate(cmd, nil); err == nil {
		t.Fatal("expected invalid --protocol-family error")
	}
}

func TestRunRuntimeProfileCreateRequiresFlags(t *testing.T) {
	cmd := newProfileCreateTestCmd()
	if err := runRuntimeProfileCreate(cmd, nil); err == nil {
		t.Fatal("expected missing --protocol-family error")
	}
}

func TestRunRuntimeProfileUpdateOnlySendsChangedFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")

	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "prof-1"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProfileUpdateTestCmd()
	_ = cmd.Flags().Set("command-name", "new-codex")
	_ = cmd.Flags().Set("enabled", "false")

	if err := runRuntimeProfileUpdate(cmd, []string{"prof-1"}); err != nil {
		t.Fatalf("runRuntimeProfileUpdate: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/api/workspaces/ws-123/runtime-profiles/prof-1" {
		t.Errorf("path = %q, want .../runtime-profiles/prof-1", gotPath)
	}
	// Only the two changed flags must be present.
	if gotBody["command_name"] != "new-codex" {
		t.Errorf("command_name = %v, want new-codex", gotBody["command_name"])
	}
	if gotBody["enabled"] != false {
		t.Errorf("enabled = %v, want false", gotBody["enabled"])
	}
	if _, ok := gotBody["display_name"]; ok {
		t.Errorf("display_name should not be sent when unchanged: %#v", gotBody)
	}
	if _, ok := gotBody["visibility"]; ok {
		t.Errorf("visibility should not be sent when unchanged: %#v", gotBody)
	}
}

func TestRunRuntimeProfileUpdateNoFieldsErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:0")

	cmd := newProfileUpdateTestCmd()
	if err := runRuntimeProfileUpdate(cmd, []string{"prof-1"}); err == nil {
		t.Fatal("expected 'no fields to update' error")
	}
}

func TestRunRuntimeProfileDeleteSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProfileDeleteTestCmd()
	if err := runRuntimeProfileDelete(cmd, []string{"prof-1"}); err != nil {
		t.Fatalf("runRuntimeProfileDelete: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if gotPath != "/api/workspaces/ws-123/runtime-profiles/prof-1" {
		t.Errorf("path = %q, want .../runtime-profiles/prof-1", gotPath)
	}
}

func TestRunRuntimeProfileDeleteConflictSurfacesServerMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("2 active agents are bound to this profile"))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProfileDeleteTestCmd()
	err := runRuntimeProfileDelete(cmd, []string{"prof-1"})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := err.Error(); !strings.Contains(got, "2 active agents are bound to this profile") {
		t.Errorf("error %q should surface the server message", got)
	}
}

func TestRunRuntimeProfileSetAndUnsetPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// set-path
	setCmd := newProfileSetPathTestCmd()
	_ = setCmd.Flags().Set("path", "/opt/bin/company-codex")
	if err := runRuntimeProfileSetPath(setCmd, []string{"prof-1"}); err != nil {
		t.Fatalf("runRuntimeProfileSetPath: %v", err)
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig: %v", err)
	}
	if got := cfg.ProfileCommandOverrides["prof-1"]; got != "/opt/bin/company-codex" {
		t.Fatalf("override after set = %q, want /opt/bin/company-codex", got)
	}

	// unset-path
	unsetCmd := newProfileUnsetPathTestCmd()
	if err := runRuntimeProfileUnsetPath(unsetCmd, []string{"prof-1"}); err != nil {
		t.Fatalf("runRuntimeProfileUnsetPath: %v", err)
	}
	cfg, err = cli.LoadCLIConfig()
	if err != nil {
		t.Fatalf("LoadCLIConfig after unset: %v", err)
	}
	if _, ok := cfg.ProfileCommandOverrides["prof-1"]; ok {
		t.Fatalf("override should be removed after unset, got %#v", cfg.ProfileCommandOverrides)
	}
}

func TestRunRuntimeProfileSetPathRejectsRelative(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newProfileSetPathTestCmd()
	_ = cmd.Flags().Set("path", "relative/path")
	if err := runRuntimeProfileSetPath(cmd, []string{"prof-1"}); err == nil {
		t.Fatal("expected absolute-path error")
	}
}

func TestRunRuntimeProfileSetPathPreservesExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed an existing config with unrelated fields.
	seed := cli.CLIConfig{ServerURL: "https://api.multica.ai", WorkspaceID: "ws-123", Token: "mul_xyz"}
	if err := cli.SaveCLIConfig(seed); err != nil {
		t.Fatal(err)
	}

	cmd := newProfileSetPathTestCmd()
	_ = cmd.Flags().Set("path", "/opt/bin/company-codex")
	if err := runRuntimeProfileSetPath(cmd, []string{"prof-1"}); err != nil {
		t.Fatalf("runRuntimeProfileSetPath: %v", err)
	}

	cfg, err := cli.LoadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://api.multica.ai" || cfg.WorkspaceID != "ws-123" || cfg.Token != "mul_xyz" {
		t.Errorf("set-path clobbered existing config: %#v", cfg)
	}
	if cfg.ProfileCommandOverrides["prof-1"] != "/opt/bin/company-codex" {
		t.Errorf("override not written: %#v", cfg.ProfileCommandOverrides)
	}
}
