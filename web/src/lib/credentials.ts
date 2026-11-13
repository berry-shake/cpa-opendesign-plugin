import type { CredentialView, ModelView, PluginConfigView, QuotaView, StatusView, UsageWindow } from "../api/types";
import { fileStem } from "./format";

export interface QuotaEntry {
  state: "loading" | "ok" | "error";
  data?: QuotaView;
  error?: string;
}

export type QuotaMap = Record<string, QuotaEntry | undefined>;

export function displayName(cred: Pick<CredentialView, "label" | "name" | "key_masked">): string {
  return cred.label || fileStem(cred.name) || cred.key_masked;
}

export function isDirect(proxy: string | undefined | null): boolean {
  const value = String(proxy || "").trim().toLowerCase();
  return value === "direct" || value === "none";
}

/** The credentials of a status that listed fine. */
export function credentialsOf(status: StatusView | null): CredentialView[] {
  return status && !status.list_error && Array.isArray(status.credentials) ? status.credentials : [];
}

/** Every credential of a status, even when listing reported an error. */
export function allCredentials(status: StatusView | null): CredentialView[] {
  return status && Array.isArray(status.credentials) ? status.credentials : [];
}

/** The plugin's default model prefix; "opendesign" before the status arrived. */
export function defaultPrefix(status: StatusView | null): string {
  return status && status.config ? status.config.default_prefix || "" : "opendesign";
}

export type BadgeClass = "danger" | "" | "warn" | "ok";

/** Badge class and message key of a credential's state. */
export function credState(cred: CredentialView): [BadgeClass, string] {
  if (cred.read_error) return ["danger", "state.readError"];
  if (cred.disabled) return ["", "state.disabled"];
  if (cred.unavailable) return ["warn", "state.unavailable"];
  return ["ok", "state.enabled"];
}

/** filter is already trimmed and lower-cased. */
export function credMatches(cred: CredentialView, filter: string): boolean {
  if (!filter) return true;
  return [cred.label, cred.key_masked, cred.name, cred.effective_prefix, cred.note]
    .some((value) => String(value || "").toLowerCase().includes(filter));
}

export type HealthState = "unknown" | "ok" | "warn" | "error";

export interface Health {
  state: HealthState;
  key: string;
  params: Record<string, number> | null;
}

export function statusHealth(data: StatusView): Health {
  const creds = Array.isArray(data.credentials) ? data.credentials : [];
  const enabled = creds.filter((c) => !c.disabled).length;
  if (data.list_error) return { state: "error", key: "health.error", params: null };
  if (!data.catalog || !data.catalog.count) return { state: "warn", key: "health.catalog", params: null };
  if (creds.length === 0) return { state: "warn", key: "health.noKeys", params: null };
  return { state: "ok", key: "health.ok", params: { enabled, total: creds.length } };
}

export type ForceState = "On" | "Off" | "Unknown";

export function forceState(config: Partial<PluginConfigView> | null | undefined): ForceState {
  const cfg = config || {};
  if (cfg.force_model_prefix === true) return "On";
  if (cfg.force_model_prefix === false) return "Off";
  return "Unknown";
}

/** The usage windows a card and the summary show, shortest first. */
export const USAGE_WINDOWS = ["five_hours", "day", "week", "month"] as const;
export type UsageWindowKey = (typeof USAGE_WINDOWS)[number];

/** Input plus output tokens of a window. */
export function windowTokens(w: UsageWindow | undefined): number {
  return w ? (Number(w.input_tokens) || 0) + (Number(w.output_tokens) || 0) : 0;
}

/** Totals for the summary strip of the credentials tab. */
export interface KeySummary {
  total: number;
  enabled: number;
  /** Sums over the keys whose usage loaded. */
  windows: Record<UsageWindowKey, UsageWindow>;
  loaded: number;
  failed: number;
  loading: boolean;
}

function emptyWindow(): UsageWindow {
  return { requests: 0, failed: 0, input_tokens: 0, cached_tokens: 0, output_tokens: 0, reasoning_tokens: 0 };
}

export function summarizeKeys(creds: CredentialView[], quota: QuotaMap): KeySummary {
  const sum: KeySummary = {
    total: 0, enabled: 0, loaded: 0, failed: 0, loading: false,
    windows: { five_hours: emptyWindow(), day: emptyWindow(), week: emptyWindow(), month: emptyWindow() },
  };
  for (const cred of creds) {
    sum.total++;
    if (!cred.disabled && !cred.read_error) sum.enabled++;
    if (cred.read_error) continue;
    const q = quota[cred.auth_index];
    if (q && q.state === "loading") sum.loading = true;
    const d = q && q.data;
    if (!d) {
      if (q && q.state === "error") sum.failed++;
      continue;
    }
    sum.loaded++;
    for (const key of USAGE_WINDOWS) {
      const from = d[key];
      if (!from) continue;
      const to = sum.windows[key];
      to.requests += Number(from.requests) || 0;
      to.failed += Number(from.failed) || 0;
      to.input_tokens += Number(from.input_tokens) || 0;
      to.cached_tokens += Number(from.cached_tokens) || 0;
      to.output_tokens += Number(from.output_tokens) || 0;
      to.reasoning_tokens += Number(from.reasoning_tokens) || 0;
    }
  }
  return sum;
}

/**
 * The keys whose usage to load, disabled ones included. The usage is summed
 * from the plugin's own log, so they load side by side. Without force only
 * keys with no figures yet; keys whose file cannot be read are skipped.
 */
export function quotaKeys(creds: CredentialView[], quota: QuotaMap, force: boolean): string[] {
  return creds.filter((c) => !c.read_error && (force || !quota[c.auth_index])).map((c) => c.auth_index);
}

/**
 * The line under a credential's name: the file name when a custom label
 * hides it, the masked key and the priority.
 */
export function credSubline(cred: CredentialView, priorityLabel: string): string {
  return [
    cred.label && cred.label !== fileStem(cred.name) ? fileStem(cred.name) : "",
    cred.key_masked,
    cred.priority ? priorityLabel + " " + cred.priority : "",
  ].filter(Boolean).join(" · ");
}

export interface ModelUse {
  keys: number;
  names: Set<string>;
}

/** Whether a model is in the plan of a key; true while the key's list is unknown. */
export function listedFor(model: Pick<ModelView, "keys">, cred: Pick<CredentialView, "key_id" | "listed_models">): boolean {
  if (!cred.key_id || cred.listed_models === undefined || cred.listed_models === null) return true;
  return Array.isArray(model.keys) && model.keys.includes(cred.key_id);
}

/**
 * For each model ID: the names it is registered under in CPA and how many
 * enabled keys serve it. "All models" covers the chat models of the key's
 * plan only.
 */
export function modelUsage(creds: CredentialView[], models: ModelView[]): Map<string, ModelUse> {
  const usage = new Map<string, ModelUse>();
  for (const cred of creds) {
    if (cred.disabled || cred.read_error) continue;
    const ids = cred.all_models
      ? models.filter((m) => m.chat !== false && listedFor(m, cred)).map((m) => m.id)
      : Array.isArray(cred.models) ? cred.models : [];
    const prefix = cred.effective_prefix || "";
    for (const id of ids) {
      let entry = usage.get(id);
      if (!entry) {
        entry = { keys: 0, names: new Set() };
        usage.set(id, entry);
      }
      entry.keys++;
      entry.names.add(prefix ? prefix + "/" + id : id);
    }
  }
  return usage;
}

/** Models whose id or one of whose input kinds contains the filter. */
export function filterModels<M extends { id: string; inputs?: string[] }>(models: M[], rawFilter: string): M[] {
  const filter = rawFilter.trim().toLowerCase();
  return models.filter((m) => !filter ||
    m.id.toLowerCase().includes(filter) ||
    (Array.isArray(m.inputs) ? m.inputs : []).some((c) => c.toLowerCase().includes(filter)));
}
