"use client";

import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Loader2, Lock, Save } from "lucide-react";
import type { Agent } from "@multica/core/types";
import {
  type OpenclawRoutingMode,
  type OpenclawRuntimeConfig,
  openclawRuntimeConfigEquals,
  parseOpenclawRuntimeConfig,
  serializeOpenclawRuntimeConfig,
} from "@multica/core/agents";
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
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { useT } from "../../../i18n";

type RuntimeConfigUpdate = {
  runtime_config: Record<string, unknown>;
  runtime_config_intent: "replace" | "clear";
};

interface FormState {
  mode: OpenclawRoutingMode;
  host: string;
  port: string;
  token: string;
  tls: boolean;
}

function configToForm(config: OpenclawRuntimeConfig): FormState {
  return {
    mode: config.mode ?? "local",
    host: config.gateway?.host ?? "",
    port: config.gateway?.port ? String(config.gateway.port) : "",
    token: config.gateway?.token ?? "",
    tls: config.gateway?.tls === true,
  };
}

function formToConfig(state: FormState): OpenclawRuntimeConfig {
  const config: OpenclawRuntimeConfig = { mode: state.mode };
  if (state.mode !== "gateway") return config;

  const gateway: NonNullable<OpenclawRuntimeConfig["gateway"]> = {};
  if (state.host.trim()) gateway.host = state.host.trim();
  const port = Number.parseInt(state.port, 10);
  if (Number.isFinite(port) && port > 0) gateway.port = port;
  if (state.token) gateway.token = state.token;
  if (state.tls) gateway.tls = true;
  if (Object.keys(gateway).length > 0) config.gateway = gateway;
  return config;
}

export function RuntimeConfigTab({
  agent,
  onSave,
  onDirtyChange,
  canManage = false,
}: {
  agent: Agent;
  onSave: (updates: RuntimeConfigUpdate) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  canManage?: boolean;
}) {
  const { t } = useT("agents");
  const redacted = agent.runtime_config_redacted === true;
  const original = useMemo(
    () => parseOpenclawRuntimeConfig(agent.runtime_config),
    [agent.runtime_config],
  );
  const originalForm = useMemo(() => configToForm(original), [original]);
  const [stateAgentId, setStateAgentId] = useState(agent.id);
  const [formState, setState] = useState<FormState>(originalForm);
  const [replaceModeState, setReplaceMode] = useState(false);
  const [replacementBaselineState, setReplacementBaseline] =
    useState<OpenclawRuntimeConfig | null>(null);
  const [clearRequestedState, setClearRequested] = useState(false);
  const [savingState, setSaving] = useState(false);
  const [clearingState, setClearing] = useState(false);
  const previousAgentIdRef = useRef(agent.id);
  const previousOriginalRef = useRef(original);
  const activeAgentIdRef = useRef(agent.id);
  const generationRef = useRef(0);

  const stateIsCurrent = stateAgentId === agent.id;
  const state = stateIsCurrent ? formState : originalForm;
  const replaceMode = stateIsCurrent && replaceModeState;
  const replacementBaseline = stateIsCurrent
    ? replacementBaselineState
    : null;
  const clearRequested = stateIsCurrent && clearRequestedState;
  const saving = stateIsCurrent && savingState;
  const clearing = stateIsCurrent && clearingState;

  // Switching records is the only unconditional reset: no local token or
  // replacement draft may cross agent ids.
  useLayoutEffect(() => {
    if (previousAgentIdRef.current !== agent.id) {
      activeAgentIdRef.current = agent.id;
      generationRef.current += 1;
      setStateAgentId(agent.id);
      previousAgentIdRef.current = agent.id;
      previousOriginalRef.current = original;
      setState(originalForm);
      setReplaceMode(false);
      setReplacementBaseline(null);
      setClearRequested(false);
      setSaving(false);
      setClearing(false);
    }
  }, [agent.id, original, originalForm]);

  const currentConfig = useMemo(() => formToConfig(state), [state]);
  const dirty =
    redacted && !replaceMode
      ? false
      : replaceMode
        ? replacementBaseline === null ||
          !openclawRuntimeConfigEquals(replacementBaseline, currentConfig)
        : !openclawRuntimeConfigEquals(original, currentConfig);

  // Status/invalidation refetches routinely replace the Agent object. Compare
  // the form against the *previous* server baseline synchronously, rather than
  // a dirty flag set by a later effect: this preserves an edit even if an input
  // event and a refetch are batched into the same render. Clean forms follow a
  // genuinely new server config; explicit replacement drafts always survive.
  useEffect(() => {
    const wasCleanBeforeRefetch = openclawRuntimeConfigEquals(
      previousOriginalRef.current,
      currentConfig,
    );
    if (
      previousAgentIdRef.current === agent.id &&
      wasCleanBeforeRefetch &&
      !replaceMode
    ) {
      setState(originalForm);
    }
    previousOriginalRef.current = original;
  }, [agent.id, currentConfig, original, originalForm, replaceMode]);

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const portValid =
    state.port === "" ||
    (/^\d+$/.test(state.port) &&
      Number(state.port) >= 1 &&
      Number(state.port) <= 65535);
  const canSave = canManage && portValid && !saving;

  const startReplacement = () => {
    // The public projection is display-only and may omit arbitrary provider
    // fields. A replacement must begin from a blank authoritative config.
    setState(configToForm({ mode: "local" }));
    setReplacementBaseline(null);
    setReplaceMode(true);
  };

  const cancelReplacement = () => {
    setState(originalForm);
    setReplacementBaseline(null);
    setReplaceMode(false);
  };

  const handleSave = async () => {
    if (!dirty || !canSave) return;
    const requestAgentId = agent.id;
    const requestGeneration = generationRef.current;
    const submittedConfig = currentConfig;
    const submittedReplaceMode = replaceMode;
    setSaving(true);
    try {
      await onSave({
        runtime_config: serializeOpenclawRuntimeConfig(submittedConfig),
        runtime_config_intent: "replace",
      });
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      if (submittedReplaceMode) setReplacementBaseline(submittedConfig);
      toast.success(t(($) => $.tab_body.runtime_config.saved_toast));
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
          : t(($) => $.tab_body.runtime_config.save_failed_toast),
      );
    } finally {
      if (
        activeAgentIdRef.current === requestAgentId &&
        generationRef.current === requestGeneration
      ) {
        setSaving(false);
      }
    }
  };

  const handleClear = async () => {
    const requestAgentId = agent.id;
    const requestGeneration = generationRef.current;
    setClearing(true);
    try {
      await onSave({
        runtime_config: {},
        runtime_config_intent: "clear",
      });
      if (
        activeAgentIdRef.current !== requestAgentId ||
        generationRef.current !== requestGeneration
      ) {
        return;
      }
      setClearRequested(false);
      setReplaceMode(false);
      setReplacementBaseline(null);
      setState(configToForm({ mode: "local" }));
      toast.success(t(($) => $.tab_body.runtime_config.cleared_toast));
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
          : t(($) => $.tab_body.runtime_config.clear_failed_toast),
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

  if (redacted && !replaceMode) {
    return (
      <div className="space-y-6">
        <p className="max-w-2xl text-pretty text-sm leading-6 text-muted-foreground">
          {t(($) => $.tab_body.runtime_config.intro)}
        </p>
        <div className="rounded-lg border p-4">
          <div className="flex items-start gap-2">
            <Lock
              className="mt-0.5 size-4 text-muted-foreground"
              aria-hidden="true"
            />
            <div>
              <p className="text-sm font-medium">
                {t(($) => $.tab_body.runtime_config.redacted_title)}
              </p>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t(($) => $.tab_body.runtime_config.redacted_hint)}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {t(($) => $.tab_body.runtime_config.configured_count, {
                  count: agent.runtime_config_key_count ?? 0,
                })}
              </p>
            </div>
          </div>
          {canManage ? (
            <div className="mt-3 flex flex-wrap gap-2 pl-6">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={startReplacement}
              >
                {t(($) => $.tab_body.runtime_config.replace_action)}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="text-destructive"
                onClick={() => setClearRequested(true)}
              >
                {t(($) => $.tab_body.runtime_config.clear_action)}
              </Button>
            </div>
          ) : null}
        </div>
        <AlertDialog
          open={clearRequested}
          onOpenChange={(open) =>
            !open && !clearing && setClearRequested(false)
          }
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t(($) => $.tab_body.runtime_config.clear_dialog_title)}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.tab_body.runtime_config.clear_dialog_description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={clearing}>
                {t(($) => $.tab_body.runtime_config.cancel_action)}
              </AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={handleClear}
                disabled={clearing}
              >
                {clearing ? (
                  <Loader2
                    className="size-3.5 animate-spin motion-reduce:animate-none"
                    aria-hidden="true"
                  />
                ) : null}
                {t(($) => $.tab_body.runtime_config.clear_confirm_action)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    );
  }

  const isGateway = state.mode === "gateway";

  return (
    <div className="flex h-full flex-col space-y-4">
      <p className="text-xs text-muted-foreground">
        {t(($) => $.tab_body.runtime_config.intro)}
      </p>

      {replaceMode ? (
        <div className="flex items-start justify-between gap-3 rounded-lg border border-dashed px-4 py-3">
          <p className="text-xs leading-5 text-muted-foreground">
            {t(($) => $.tab_body.runtime_config.replace_hint)}
          </p>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={cancelReplacement}
          >
            {t(($) => $.tab_body.runtime_config.cancel_action)}
          </Button>
        </div>
      ) : null}

      <fieldset className="space-y-2" disabled={!canManage}>
        <Label className="text-xs font-medium">
          {t(($) => $.tab_body.runtime_config.mode_label)}
        </Label>
        <div className="flex gap-2">
          {(["local", "gateway"] as const).map((mode) => (
            <button
              key={mode}
              type="button"
              disabled={!canManage}
              onClick={() => setState((current) => ({ ...current, mode }))}
              className={`rounded-md border px-3 py-1.5 text-xs ${
                state.mode === mode
                  ? "border-foreground bg-foreground text-background"
                  : "border-border bg-background text-foreground hover:bg-muted"
              }`}
            >
              {t(($) => $.tab_body.runtime_config[`mode_${mode}`])}
            </button>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">
          {isGateway
            ? t(($) => $.tab_body.runtime_config.mode_gateway_hint)
            : t(($) => $.tab_body.runtime_config.mode_local_hint)}
        </p>
      </fieldset>

      <fieldset
        className={`space-y-3 rounded-md border p-3 ${isGateway ? "" : "opacity-50"}`}
        disabled={!isGateway || !canManage}
      >
        <legend className="px-1 text-xs font-medium">
          {t(($) => $.tab_body.runtime_config.gateway_legend)}
        </legend>

        <div className="space-y-1.5">
          <Label htmlFor="openclaw-gw-host" className="text-xs">
            {t(($) => $.tab_body.runtime_config.host_label)}
          </Label>
          <Input
            id="openclaw-gw-host"
            value={state.host}
            onChange={(event) =>
              setState((current) => ({
                ...current,
                host: event.target.value,
              }))
            }
            placeholder={t(($) => $.tab_body.runtime_config.host_placeholder)}
            autoComplete="off"
            className="font-mono text-xs"
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="openclaw-gw-port" className="text-xs">
            {t(($) => $.tab_body.runtime_config.port_label)}
          </Label>
          <Input
            id="openclaw-gw-port"
            value={state.port}
            onChange={(event) =>
              setState((current) => ({
                ...current,
                port: event.target.value,
              }))
            }
            placeholder="18789"
            inputMode="numeric"
            aria-invalid={!portValid || undefined}
            className="font-mono text-xs"
          />
          {!portValid ? (
            <p className="text-xs text-destructive">
              {t(($) => $.tab_body.runtime_config.port_invalid)}
            </p>
          ) : null}
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="openclaw-gw-token" className="text-xs">
            {t(($) => $.tab_body.runtime_config.token_label)}
          </Label>
          <Input
            id="openclaw-gw-token"
            type="password"
            value={state.token}
            onChange={(event) =>
              setState((current) => ({
                ...current,
                token: event.target.value,
              }))
            }
            placeholder={t(($) => $.tab_body.runtime_config.token_placeholder)}
            autoComplete="off"
            className="font-mono text-xs"
          />
        </div>

        <div className="flex items-center justify-between gap-2 pt-1">
          <div>
            <Label htmlFor="openclaw-gw-tls" className="text-xs">
              {t(($) => $.tab_body.runtime_config.tls_label)}
            </Label>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.runtime_config.tls_hint)}
            </p>
          </div>
          <Switch
            id="openclaw-gw-tls"
            checked={state.tls}
            disabled={!isGateway || !canManage}
            onCheckedChange={(checked: boolean) =>
              setState((current) => ({ ...current, tls: checked }))
            }
          />
        </div>
      </fieldset>

      <div className="flex items-center justify-end gap-3 pt-2">
        {dirty ? (
          <span className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        ) : null}
        <Button onClick={handleSave} disabled={!dirty || !canSave} size="sm">
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Save className="h-3.5 w-3.5" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}
