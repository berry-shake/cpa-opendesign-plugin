import { describe, expect, it } from "vitest";
import type { CredentialView, ModelView, QuotaView, UsageWindow } from "../api/types";
import { listedFor, modelUsage, quotaKeys, summarizeKeys, windowTokens } from "./credentials";
import { pickerRows } from "./editor";

function cred(over: Partial<CredentialView>): CredentialView {
  return {
    auth_index: "a1", id: "f1", name: "opendesign-1.json", label: "", key_masked: "sk-…1", disabled: false, unavailable: false,
    priority: 0, prefix: null, effective_prefix: "opendesign", all_models: true, models: null, registered_models: 0,
    registered_known: true, success: 0, failed: 0, ...over,
  };
}

function model(id: string, keys: string[], chat = true): ModelView {
  return { id, chat, enabled: true, keys };
}

function win(requests: number, input: number, output: number): UsageWindow {
  return { requests, failed: 0, input_tokens: input, cached_tokens: 0, output_tokens: output, reasoning_tokens: 0 };
}

const models = [model("deepseek-v4-pro", ["k1", "k2"]), model("claude-top-5", ["k2"]), model("embed", ["k1"], false)];

describe("per-key model lists", () => {
  it("lists a model for a key only when its plan does", () => {
    expect(listedFor(models[1], { key_id: "k1", listed_models: 1 })).toBe(false);
    expect(listedFor(models[1], { key_id: "k2", listed_models: 2 })).toBe(true);
    // Unknown list: everything counts.
    expect(listedFor(models[1], { key_id: "k1" })).toBe(true);
  });

  it("counts all-model keys only for the chat models of their plan", () => {
    const usage = modelUsage([cred({ auth_index: "a1", key_id: "k1", listed_models: 1 }), cred({ auth_index: "a2", key_id: "k2", listed_models: 2 })], models);
    expect(usage.get("deepseek-v4-pro")?.keys).toBe(2);
    expect(usage.get("claude-top-5")?.keys).toBe(1);
    expect(usage.has("embed")).toBe(false);
  });

  it("scopes the picker to the edited key and keeps selected models outside it", () => {
    const rows = pickerRows(models, ["claude-top-5"], "", { key_id: "k1", listed_models: 1 });
    expect(rows.map((r) => r.id)).toEqual(["claude-top-5", "deepseek-v4-pro", "embed"]);
    expect(rows[0].missing).toBe(true);
    expect(pickerRows(models, [], "").length).toBe(3);
  });
});

describe("usage", () => {
  it("sums loaded keys per window", () => {
    const usage = (n: number): QuotaView => ({
      five_hours: win(n, 100 * n, 10 * n), day: win(2 * n, 0, 0), week: win(3 * n, 0, 0), month: win(4 * n, 1000, 0), fetched_at: "",
    });
    const creds = [cred({ auth_index: "a1" }), cred({ auth_index: "a2", disabled: true }), cred({ auth_index: "a3" })];
    const sum = summarizeKeys(creds, {
      a1: { state: "ok", data: usage(1) },
      a2: { state: "ok", data: usage(2) },
      a3: { state: "error", error: "log off" },
    });
    expect(sum.total).toBe(3);
    expect(sum.enabled).toBe(2);
    expect(sum.loaded).toBe(2);
    expect(sum.failed).toBe(1);
    expect(sum.windows.five_hours.requests).toBe(3);
    expect(windowTokens(sum.windows.five_hours)).toBe(330);
    expect(windowTokens(sum.windows.month)).toBe(2000);
  });

  it("loads every readable key, or only the missing ones", () => {
    const creds = [cred({ auth_index: "a1" }), cred({ auth_index: "a2", read_error: "bad" }), cred({ auth_index: "a3" })];
    expect(quotaKeys(creds, {}, false)).toEqual(["a1", "a3"]);
    expect(quotaKeys(creds, { a1: { state: "ok" } }, false)).toEqual(["a3"]);
    expect(quotaKeys(creds, { a1: { state: "ok" } }, true)).toEqual(["a1", "a3"]);
  });
});
