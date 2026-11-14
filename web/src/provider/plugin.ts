import type { ProviderPlugin } from "@core/contract";
import { fmtTokens } from "@core/lib/format";
import { listedFor, modelTags, modelTier } from "./lib";

export const plugin: ProviderPlugin = {
  id: "opendesign",
  name: "OpenDesign",
  repo: "https://github.com/berry-shake/cpa-opendesign-plugin",
  billionTokens: true,
  // The usage is summed from the plugin's own log, so every key loads side
  // by side; reloading all asks the plugin to refetch the account windows.
  quotaGroups: (creds, quota, force) =>
    creds.filter((c) => !c.read_error && (force || !quota[c.auth_index])).map((c) => [c.auth_index]),
  quotaRefresh: true,
  models: {
    matches: (m, filter) => (Array.isArray(m.inputs) ? m.inputs : []).some((c) => c.toLowerCase().includes(filter)),
    // "All models" covers the chat models of the key's plan only.
    inAll: (m, cred) => m.chat !== false && listedFor(m, cred),
    hint: (_t, meta) => ({ params: { keys: meta.keys || 0 } }),
  },
  editor: {
    init: (editing) => ({
      scope: editing && editing.key_id && editing.listed_models !== undefined && editing.listed_models !== null
        ? { key_id: editing.key_id, listed_models: editing.listed_models }
        : undefined,
    }),
    // When editing, only the models of the key's plan.
    pickable: (models, ed) => {
      const scope = ed.scope;
      return scope ? models.filter((m) => listedFor(m, scope)) : models;
    },
    countTip: (t, ed) => (ed.scope ? t("picker.scope") : undefined),
    pickName: (t, m) => ({ text: m.chat === false ? t("picker.notChat") : modelTags(t, m), title: modelTier(t, m) }),
    // Context length and output limit of a model, e.g. "262K context · 66K out".
    modelMeta(t, m) {
      const parts: string[] = [];
      if (m.context_length) parts.push(t("picker.ctx", { ctx: fmtTokens(m.context_length) }));
      if (m.max_output) parts.push(t("picker.out", { out: fmtTokens(m.max_output) }));
      return parts.join(" · ");
    },
  },
};
