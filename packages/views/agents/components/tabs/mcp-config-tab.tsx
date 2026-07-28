"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  Loader2,
  Lock,
  Pencil,
  Plus,
  RefreshCw,
  Server,
  Trash2,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  UpdateAgentRequest,
} from "@multica/core/types";
import { ApiError } from "@multica/core/api";
import {
  runtimeCapabilitiesOptions,
  runtimeDisplayLabel,
} from "@multica/core/runtimes";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { useT } from "../../../i18n";
import {
  listManagedMcpServers,
  removeManagedMcpServer,
  upsertManagedMcpServer,
  type ManagedMcpServer,
} from "./mcp-config-model";
import { McpServerDialog } from "./mcp-server-dialog";

export function McpConfigTab({
  agent,
  runtime,
  onSave,
  onDirtyChange,
  canManage = false,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  onSave: (updates: UpdateAgentRequest) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  canManage?: boolean;
}) {
  const { t } = useT("agents");
  const runtimeId =
    runtime?.runtime_mode === "local" && runtime.status === "online"
      ? runtime.id
      : null;
  const runtimeQuery = useQuery(runtimeCapabilitiesOptions(runtimeId));
  const redacted = agent.mcp_config_redacted === true;
  const [stateAgentId, setStateAgentId] = useState(agent.id);
  const [replaceModeState, setReplaceMode] = useState(false);
  const [replacementConfigState, setReplacementConfig] = useState<unknown | null>(
    null,
  );
  const [editorOpenState, setEditorOpen] = useState(false);
  const [editingServerState, setEditingServer] = useState<ManagedMcpServer | null>(
    null,
  );
  const [deletingServerState, setDeletingServer] =
    useState<ManagedMcpServer | null>(null);
  const [deletingState, setDeleting] = useState(false);
  const [clearRequestedState, setClearRequested] = useState(false);
  const [clearingState, setClearing] = useState(false);
  const activeAgentIdRef = useRef(agent.id);
  const generationRef = useRef(0);

  const stateIsCurrent = stateAgentId === agent.id;
  const replaceMode = stateIsCurrent && replaceModeState;
  const replacementConfig = stateIsCurrent ? replacementConfigState : null;
  const editorOpen = stateIsCurrent && editorOpenState;
  const editingServer = stateIsCurrent ? editingServerState : null;
  const deletingServer = stateIsCurrent ? deletingServerState : null;
  const deleting = stateIsCurrent && deletingState;
  const clearRequested = stateIsCurrent && clearRequestedState;
  const clearing = stateIsCurrent && clearingState;
  const workingConfig = replaceMode ? replacementConfig : agent.mcp_config;
  const managedServers = useMemo(
    () => listManagedMcpServers(workingConfig),
    [workingConfig],
  );
  const managedNames = useMemo(
    () => new Set(managedServers.map((server) => server.name)),
    [managedServers],
  );
  useEffect(() => onDirtyChange?.(false), [onDirtyChange]);
  useLayoutEffect(() => {
    activeAgentIdRef.current = agent.id;
    generationRef.current += 1;
    setStateAgentId(agent.id);
    setReplaceMode(false);
    setReplacementConfig(null);
    setEditorOpen(false);
    setEditingServer(null);
    setDeletingServer(null);
    setDeleting(false);
    setClearRequested(false);
    setClearing(false);
  }, [agent.id]);

  const openAddDialog = () => {
    setEditingServer(null);
    setEditorOpen(true);
  };

  const openEditDialog = (server: ManagedMcpServer) => {
    setEditingServer(server);
    setEditorOpen(true);
  };

  const handleSaveServer = async (
    name: string,
    config: Record<string, unknown>,
  ) => {
    const requestAgentId = agent.id;
    const requestGeneration = generationRef.current;
    const requestEditingServer = editingServer;
    const requestReplaceMode = replaceMode;
    const next = upsertManagedMcpServer(
      workingConfig,
      requestEditingServer,
      name,
      config,
    );
    try {
      await onSave({
        mcp_config: next,
        mcp_config_intent: "replace",
      });
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      if (requestReplaceMode) setReplacementConfig(next);
      toast.success(
        requestEditingServer
          ? t(($) => $.tab_body.mcp_config.updated_toast)
          : t(($) => $.tab_body.mcp_config.added_toast),
      );
    } catch (error) {
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.save_failed_toast),
      );
      throw error;
    }
  };

  const handleDelete = async () => {
    if (!deletingServer) return;
    const requestAgentId = agent.id;
    const requestGeneration = generationRef.current;
    const requestReplaceMode = replaceMode;
    const requestDeletingServer = deletingServer;
    setDeleting(true);
    const next = removeManagedMcpServer(workingConfig, requestDeletingServer);
    try {
      await onSave({
        mcp_config: next,
        mcp_config_intent: next === null ? "clear" : "replace",
      });
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      if (requestReplaceMode) {
        setReplacementConfig(next);
      }
      toast.success(t(($) => $.tab_body.mcp_config.deleted_toast));
      setDeletingServer(null);
    } catch (error) {
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.delete_failed_toast),
      );
    } finally {
      if (
        activeAgentIdRef.current === requestAgentId &&
        generationRef.current === requestGeneration
      ) {
        setDeleting(false);
      }
    }
  };

  const startReplacement = () => {
    // Never seed the replacement editor from the masked response. The next
    // save is an intentional whole-config replacement built from fresh input.
    setReplacementConfig(null);
    setReplaceMode(true);
  };

  const cancelReplacement = () => {
    setReplaceMode(false);
    setReplacementConfig(null);
    setEditorOpen(false);
    setEditingServer(null);
  };

  const handleClear = async () => {
    const requestAgentId = agent.id;
    const requestGeneration = generationRef.current;
    setClearing(true);
    try {
      await onSave({
        mcp_config: null,
        mcp_config_intent: "clear",
      });
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      setClearRequested(false);
      setReplaceMode(false);
      setReplacementConfig(null);
      toast.success(t(($) => $.tab_body.mcp_config.cleared_toast));
    } catch (error) {
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.mcp_config.clear_failed_toast),
      );
    } finally {
      if (
        activeAgentIdRef.current === requestAgentId &&
        generationRef.current === requestGeneration
      ) {
        setClearing(false);
      }
    }
  };

  return (
    <div className="space-y-8">
      <p className="max-w-2xl break-words text-pretty text-sm leading-6 text-muted-foreground">
        {t(($) => $.tab_body.mcp_config.intro)}
      </p>

      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="text-sm font-medium">
              {t(($) => $.tab_body.mcp_config.managed_title)}
            </h3>
            <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
              {t(($) => $.tab_body.mcp_config.managed_hint)}
            </p>
          </div>
          {(!redacted || replaceMode) && (
            <Button size="sm" variant="outline" onClick={openAddDialog}>
              <Plus aria-hidden="true" />
              {t(($) => $.tab_body.mcp_config.add_action)}
            </Button>
          )}
        </div>

        {redacted && !replaceMode ? (
          <div className="space-y-3 rounded-lg border px-4 py-3">
            <div className="flex items-start gap-2">
              <Lock
                className="mt-0.5 h-4 w-4 text-muted-foreground"
                aria-hidden="true"
              />
              <div>
                <p className="text-sm font-medium">
                  {t(($) => $.tab_body.mcp_config.redacted_title)}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t(($) => $.tab_body.mcp_config.redacted_hint)}
                </p>
              </div>
            </div>
            {canManage ? (
              <div className="flex flex-wrap gap-2 pl-6">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={startReplacement}
                >
                  {t(($) => $.tab_body.mcp_config.replace_action)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="text-destructive"
                  onClick={() => setClearRequested(true)}
                >
                  {t(($) => $.tab_body.mcp_config.clear_action)}
                </Button>
              </div>
            ) : null}
          </div>
        ) : (
          <>
            {replaceMode ? (
              <div className="flex items-start justify-between gap-3 rounded-lg border border-dashed px-4 py-3">
                <p className="text-xs leading-5 text-muted-foreground">
                  {t(($) => $.tab_body.mcp_config.replace_hint)}
                </p>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={cancelReplacement}
                >
                  {t(($) => $.tab_body.mcp_config.dialog_cancel)}
                </Button>
              </div>
            ) : null}
            {managedServers.length > 0 ? (
              <McpServerList
                servers={managedServers}
                disabledLabel={t(
                  ($) => $.tab_body.mcp_config.agent_disabled_badge,
                )}
                onEdit={openEditDialog}
                onDelete={setDeletingServer}
                editLabel={t(($) => $.tab_body.mcp_config.edit_aria)}
                deleteLabel={t(($) => $.tab_body.mcp_config.delete_aria)}
              />
            ) : (
              <McpNotice
                text={t(($) => $.tab_body.mcp_config.managed_empty)}
              />
            )}
          </>
        )}
      </section>

      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="text-sm font-medium">
              {t(($) => $.tab_body.mcp_config.runtime_title)}
            </h3>
            <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
              {t(($) => $.tab_body.mcp_config.runtime_hint, {
                runtime: runtime ? runtimeDisplayLabel(runtime) : "Runtime",
              })}
            </p>
          </div>
          {runtimeId && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => runtimeQuery.refetch()}
              disabled={runtimeQuery.isFetching}
            >
              <RefreshCw
                className={
                  runtimeQuery.isFetching
                    ? "h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                    : "h-3.5 w-3.5"
                }
                aria-hidden="true"
              />
              {t(($) => $.tab_body.mcp_config.refresh_action)}
            </Button>
          )}
        </div>
        {!runtime ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_missing)} />
        ) : runtime.status !== "online" ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_offline)} />
        ) : runtimeQuery.isLoading ? (
          <McpNotice
            loading
            text={t(($) => $.tab_body.mcp_config.runtime_discovering)}
          />
        ) : runtimeQuery.isError ? (
          <McpNotice
            text={
              runtimeQuery.error instanceof ApiError &&
              runtimeQuery.error.status === 403
                ? t(($) => $.tab_body.mcp_config.runtime_forbidden)
                : t(($) => $.tab_body.mcp_config.runtime_failed)
            }
          />
        ) : runtimeQuery.data?.mcpSupported !== true ? (
          <McpNotice
            text={t(($) => $.tab_body.mcp_config.runtime_unsupported)}
          />
        ) : runtimeQuery.data.mcpServers.length === 0 ? (
          <McpNotice text={t(($) => $.tab_body.mcp_config.runtime_empty)} />
        ) : (
          <McpServerList
            servers={runtimeQuery.data.mcpServers.map((server) => ({
              name: server.name,
              transport: server.transport || "unknown",
              enabled: server.enabled,
              source: server.source,
              overridden: managedNames.has(server.name),
            }))}
            disabledLabel={t(($) => $.tab_body.mcp_config.runtime_disabled_badge)}
            overriddenLabel={t(($) => $.tab_body.mcp_config.runtime_overridden_badge)}
          />
        )}
      </section>

      {(!redacted || replaceMode) && (
        <McpServerDialog
          open={editorOpen}
          server={editingServer}
          existingNames={managedNames}
          onOpenChange={setEditorOpen}
          onSave={handleSaveServer}
        />
      )}

      <AlertDialog
        open={deletingServer !== null}
        onOpenChange={(open) => !open && !deleting && setDeletingServer(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.mcp_config.delete_dialog_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.mcp_config.delete_dialog_description, {
                name: deletingServer?.name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>
              {t(($) => $.tab_body.mcp_config.dialog_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleDelete}
              disabled={deleting}
            >
              {deleting && (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              )}
              {t(($) => $.tab_body.mcp_config.delete_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={clearRequested}
        onOpenChange={(open) => !open && !clearing && setClearRequested(false)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.mcp_config.clear_dialog_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.mcp_config.clear_dialog_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={clearing}>
              {t(($) => $.tab_body.mcp_config.dialog_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleClear}
              disabled={clearing}
            >
              {clearing ? (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              ) : null}
              {t(($) => $.tab_body.mcp_config.clear_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

type McpServerView = {
  name: string;
  transport: string;
  enabled: boolean;
  source?: string;
  overridden?: boolean;
};

function McpServerList({
  servers,
  disabledLabel,
  overriddenLabel,
  onEdit,
  onDelete,
  editLabel,
  deleteLabel,
}: {
  servers: McpServerView[];
  disabledLabel: string;
  overriddenLabel?: string;
  onEdit?: (server: ManagedMcpServer) => void;
  onDelete?: (server: ManagedMcpServer) => void;
  editLabel?: string;
  deleteLabel?: string;
}) {
  return (
    <ul className="divide-y rounded-lg border bg-surface-raised/40">
      {servers.map((server) => (
        <li key={server.name} className="flex items-center gap-3 p-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
            <Server className="h-4 w-4" aria-hidden="true" />
          </span>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">{server.name}</p>
            <p className="text-xs text-muted-foreground">
              <span className="uppercase">{server.transport}</span>
              {server.source ? ` · ${server.source}` : null}
            </p>
          </div>
          {server.overridden && overriddenLabel ? (
            <Badge variant="outline">{overriddenLabel}</Badge>
          ) : !server.enabled ? (
            <Badge variant="outline">{disabledLabel}</Badge>
          ) : null}
          {onEdit && onDelete && (
            <div className="flex items-center gap-1">
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`${editLabel} ${server.name}`}
                onClick={() => onEdit(server as ManagedMcpServer)}
              >
                <Pencil aria-hidden="true" />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`${deleteLabel} ${server.name}`}
                onClick={() => onDelete(server as ManagedMcpServer)}
              >
                <Trash2 aria-hidden="true" />
              </Button>
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

function McpNotice({ text, loading = false }: { text: string; loading?: boolean }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-xs text-muted-foreground">
      {loading ? (
        <Loader2
          className="h-4 w-4 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
      ) : (
        <Server className="h-4 w-4" aria-hidden="true" />
      )}
      {text}
    </div>
  );
}
