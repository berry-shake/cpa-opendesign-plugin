// OpenDesign figures: usage windows from the request log, the summary
// totals, per-key model lists, plan windows and model tags.

import type { CredentialView } from "@core/api/types";
import type { T } from "@core/i18n";
import type { QuotaMap } from "@core/lib/credentials";
import type { AccountUsage, CredentialExtra, ModelView, PlanWindow, UsageWindow } from "./types";

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

/** Whether a model is in the plan of a key; true while the key's list is unknown. */
export function listedFor(model: Pick<ModelView, "keys">, cred: Pick<CredentialExtra, "key_id" | "listed_models">): boolean {
  if (!cred.key_id || cred.listed_models === undefined || cred.listed_models === null) return true;
  return Array.isArray(model.keys) && model.keys.includes(cred.key_id);
}

/** The account's plan windows with a limit, shortest first. */
export function planWindows(a: AccountUsage | undefined): PlanWindow[] {
  if (!a || !Array.isArray(a.windows)) return [];
  return a.windows.filter((w) => Number(w.limit) > 0).sort((x, y) => x.duration_seconds - y.duration_seconds);
}

/** The inputs shown next to a model besides text. */
export const SHOWN_INPUTS = ["image", "file", "audio", "video"];

/** A model's extra inputs, e.g. "Image · File". */
export function modelTags(t: T, model: Pick<ModelView, "inputs">): string {
  const inputs = Array.isArray(model.inputs) ? model.inputs : [];
  return SHOWN_INPUTS.filter((c) => inputs.includes(c)).map((c) => t("cap." + c)).join(" · ");
}

/** Cost tier and capability, e.g. "Low cost · Standard"; "" when neither is known. */
export function modelTier(t: T, model: Pick<ModelView, "cost_tier" | "capability">): string {
  if (!model.cost_tier && !model.capability) return "";
  const named = (prefix: string, value: string | undefined) => {
    if (!value) return "—";
    const text = t(prefix + value);
    return text === prefix + value ? value : text;
  };
  return t("catalog.tier", { cost: named("cost.", model.cost_tier), capability: named("capability.", model.capability) });
}
