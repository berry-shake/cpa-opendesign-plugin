/// <reference types="vite/client" />
// Signing an OpenDesign account in and out on a key card, seen from outside:
// the whole page runs from its entry module, the user types the management
// key and clicks, and fetch is answered at the network level. The tests read
// only the DOM and the requests sent, so they hold however the page keeps its
// state (they passed unchanged before and after the shared page code moved
// to cpa-provider-core).

import { act } from "react";
import type { Root } from "react-dom/client";

const mounted = vi.hoisted(() => ({ roots: [] as Root[] }));

// The entry renders into #root itself; keep its root to unmount it afterwards.
vi.mock("react-dom/client", async (importOriginal) => {
  const mod = await importOriginal<typeof import("react-dom/client")>();
  return {
    ...mod,
    createRoot: (...args: Parameters<typeof mod.createRoot>) => {
      const root = mod.createRoot(...args);
      mounted.roots.push(root);
      return root;
    },
  };
});

// The page's entry: src/main.tsx before the move, src/core/main.tsx after.
const entries = Object.values(import.meta.glob(["../main.tsx", "../core/main.tsx"]));

const P = "/v0/management/plugins/opendesign";
const KEY = "test-management-key";
const T0 = Date.parse("2026-11-14T08:00:00Z");
const ACTIVATE = "https://opendesign.example/activate?code=ABCD-EFGH";

// ---- the fake CPA ----

interface Sent {
  method: string;
  path: string;
  body: unknown;
  headers: Record<string, string>;
}
interface Reply {
  status: number;
  body: unknown;
}

const usage = { requests: 2, failed: 0, input_tokens: 1200, cached_tokens: 0, output_tokens: 300, reasoning_tokens: 0 };
const alpha = {
  auth_index: "a1", id: "od-alpha.json", name: "od-alpha.json", label: "Alpha", key_masked: "od-****aaaa",
  disabled: false, unavailable: false, priority: 0, prefix: null, effective_prefix: "opendesign",
  all_models: true, models: null, registered_models: 0, registered_known: false, success: 2, failed: 0,
};
const beta = {
  ...alpha, auth_index: "b2", id: "od-beta.json", name: "od-beta.json", label: "Beta", key_masked: "od-****bbbb",
  account: { email: "beta@example.com" },
};

let sent: Sent[] = [];
let creds: object[] = [];
let scripted = new Map<string, Array<Reply | Promise<Reply>>>();

/** Queues answers for "METHOD /path" under the plugin's management base. */
function script(route: string, ...replies: Array<Reply | Promise<Reply>>): void {
  scripted.set(route, [...(scripted.get(route) || []), ...replies]);
}

function deferred(): { promise: Promise<Reply>; resolve: (reply: Reply) => void } {
  let resolve: (reply: Reply) => void = () => {};
  const promise = new Promise<Reply>((done) => (resolve = done));
  return { promise, resolve };
}

const ok = (body: unknown): Reply => ({ status: 200, body });
// A card's usage from the request log, plus the signed-in account's figures if given.
const quota = (account?: object): Reply =>
  ok({ five_hours: usage, day: usage, week: usage, month: usage, fetched_at: new Date().toISOString(), ...(account ? { account } : {}) });
// Every sign-in's code expires five minutes after the page opened.
const pending = (id: string, extra: object = {}): Reply =>
  ok({ id, auth_index: "a1", state: "pending", user_code: "ABCD-EFGH", activation_url: ACTIVATE, expires_at: new Date(T0 + 300_000).toISOString(), ...extra });

function defaultReply(method: string, url: URL): Reply {
  const route = method + " " + url.pathname.slice(P.length);
  if (route === "GET /status") {
    return ok({
      plugin: "opendesign", version: "test", now: new Date().toISOString(),
      config: { base_url: "https://opendesign.example", default_prefix: "opendesign", models_refresh_minutes: 60, request_timeout_seconds: 60, stream_idle_timeout_seconds: 60 },
      catalog: { count: 1, keys: 2, revision: "r1" },
      credentials: creds,
    });
  }
  if (route === "GET /credentials/quota") return quota();
  if (route === "GET /proxies") return ok({ proxies: [] });
  if (route === "GET /logs") {
    return ok({
      items: [], total: 0, offset: 0, retention: 1000, retention_days: 30,
      summary: { requests: 0, success: 0, failed: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, cache_hit_rate: null, output_tokens_per_second: null, e2e_tokens_per_second: null },
    });
  }
  return { status: 500, body: { message: "unscripted " + route } };
}

async function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input), "http://cpa.test");
  const method = init?.method || "GET";
  sent.push({
    method,
    path: url.pathname + url.search,
    body: init?.body === undefined ? undefined : JSON.parse(String(init.body)),
    headers: { ...(init?.headers as Record<string, string>) },
  });
  const queue = scripted.get(method + " " + url.pathname.slice(P.length));
  const reply = queue && queue.length ? await queue.shift()! : defaultReply(method, url);
  const text = typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body);
  return { ok: reply.status >= 200 && reply.status < 300, status: reply.status, text: () => Promise.resolve(text) } as unknown as Response;
}

/** Requests since mark as "METHOD /path?query", the plugin base left out. */
function calls(mark: number): string[] {
  return sent.slice(mark).map((r) => r.method + " " + (r.path.startsWith(P) ? r.path.slice(P.length) : r.path));
}

function accountCalls(mark: number): string[] {
  return calls(mark).filter((c) => c.includes(" /account"));
}

// ---- driving the page ----

async function run(fn: () => unknown, ms = 0): Promise<void> {
  await act(async () => {
    await fn();
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const wait = (ms: number) => run(() => {}, ms);
const click = (el: HTMLElement) => run(() => el.click());

function nameOf(el: Element): string {
  return (el.getAttribute("aria-label") || el.textContent || "").trim();
}

function text(el: Element): string {
  return (el.textContent || "").replace(/\s+/g, " ").trim();
}

function buttonsIn(el: ParentNode): HTMLButtonElement[] {
  return Array.from(el.querySelectorAll("button")).filter((b) => !b.closest("[hidden]"));
}

function buttonNames(el: ParentNode): string[] {
  return buttonsIn(el).map(nameOf);
}

function button(el: ParentNode, name: string, nth = 0): HTMLButtonElement {
  const found = buttonsIn(el).filter((b) => nameOf(b) === name)[nth];
  if (!found) throw new Error(`no button "${name}" #${nth} among: ${buttonNames(el).join(" | ")}`);
  return found;
}

function card(name: string): HTMLElement {
  const found = Array.from(document.querySelectorAll<HTMLElement>("article")).find((a) => a.querySelector(".cred-name")?.textContent === name);
  if (!found) throw new Error(`no card "${name}"`);
  return found;
}

function signinDialog(): HTMLDialogElement {
  const found = Array.from(document.querySelectorAll("dialog")).find((d) => text(d).includes("Sign in an OpenDesign account"));
  if (!found) throw new Error("no sign-in dialog");
  return found;
}

const toast = () => text(document.getElementById("toast")!);
const notice = () => text(document.getElementById("log-text")!);
const keyPromptShown = () => !document.getElementById("login-card")!.classList.contains("hidden");
const keyPromptError = () => text(document.getElementById("login-card")!.querySelector('[role="alert"]')!);

function typeInto(input: HTMLInputElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

/** Loads the page, signs in with the management key and opens the credentials tab. */
async function openPage(): Promise<void> {
  expect(entries).toHaveLength(1);
  document.body.innerHTML = '<main id="root"></main>';
  await run(() => entries[0]());
  const input = document.querySelector<HTMLInputElement>('input[type="password"]')!;
  await run(() => typeInto(input, KEY));
  await click(input.form!.querySelector<HTMLButtonElement>('button[type="submit"]')!);
  const tab = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((b) => nameOf(b).startsWith("Credentials"))!;
  await click(tab);
  expect(keyPromptShown()).toBe(false);
  card("Alpha");
}

/** Clicks Sign in on Alpha and answers with a waiting sign-in "L1"; returns the mark before the click. */
async function startWaiting(): Promise<number> {
  script("POST /account/login", pending("L1"));
  const mark = sent.length;
  await click(button(card("Alpha"), "Sign in"));
  expect(text(signinDialog())).toContain("ABCD-EFGH");
  return mark;
}

// jsdom has no showModal; this does what the page relies on, including the
// close event a browser fires when the dialog closes (also on Esc).
const dialogProto = HTMLDialogElement.prototype as unknown as Record<string, unknown>;
const nativeDialog = { showModal: dialogProto.showModal, close: dialogProto.close };

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  dialogProto.showModal = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
  };
  dialogProto.close = function (this: HTMLDialogElement) {
    if (!this.hasAttribute("open")) return;
    this.removeAttribute("open");
    this.dispatchEvent(new Event("close"));
  };
});

afterAll(() => {
  dialogProto.showModal = nativeDialog.showModal;
  dialogProto.close = nativeDialog.close;
});

beforeEach(() => {
  // setImmediate stays real: React's scheduler and act use it.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
  vi.setSystemTime(T0);
  vi.resetModules();
  localStorage.clear();
  sent = [];
  creds = [alpha, beta];
  scripted = new Map();
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
});

afterEach(async () => {
  await act(async () => {
    for (const root of mounted.roots.splice(0)) root.unmount();
  });
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("signing an account in on a key card", () => {
  it("starts a device sign-in, checks 2 s after each answer, and refreshes the cards and the card's usage once approved", async () => {
    await openPage();
    const start = deferred();
    script("POST /account/login", start.promise);
    const mark = sent.length;
    await click(button(card("Alpha"), "Sign in"));

    const dialog = signinDialog();
    expect(dialog.open).toBe(true);
    expect(text(dialog)).toContain("Sign an account in on “Alpha”");
    expect(text(dialog)).toContain("Starting the sign-in with OpenDesign…");
    expect(buttonNames(dialog)).toEqual(["Close", "Cancel sign-in"]);
    expect(calls(mark)).toEqual(["POST /account/login"]);
    expect(sent[mark].body).toEqual({ auth_index: "a1" });
    expect(sent[mark].headers).toEqual({ Authorization: "Bearer " + KEY, "Content-Type": "application/json" });

    await run(() => start.resolve(pending("L1")));
    expect(text(dialog)).toContain("1. Open the authorization page and sign in to the OpenDesign account.");
    expect(text(dialog)).toContain("2. Check that the page shows the code below, then approve.");
    const link = dialog.querySelector("a")!;
    expect([text(link), link.getAttribute("href"), link.getAttribute("target")]).toEqual(["Open authorization page", ACTIVATE, "_blank"]);
    expect(text(dialog.querySelector("code")!)).toBe("ABCD-EFGH");
    expect(text(dialog)).toContain("Waiting for you to approve on the website… 5:00 left");
    expect(buttonNames(dialog)).toEqual(["Close", "Copy link", "Copy", "Cancel sign-in"]);

    // The first check comes 2 s after the sign-in started; the countdown ticks every second.
    await wait(1999);
    expect(accountCalls(mark)).toEqual(["POST /account/login"]);
    expect(text(dialog)).toContain("4:59 left");
    script("GET /account/login", pending("L1"));
    await wait(1);
    expect(accountCalls(mark)).toEqual(["POST /account/login", "GET /account/login?id=L1"]);
    expect(sent[sent.length - 1].body).toBeUndefined();
    expect(text(dialog)).toContain("4:58 left");

    // A slow answer: nothing is sent while it is out, the next check is 2 s after it.
    const slow = deferred();
    script("GET /account/login", slow.promise);
    await wait(2000);
    expect(accountCalls(mark)).toHaveLength(3);
    await wait(5000);
    expect(accountCalls(mark)).toHaveLength(3);
    await run(() => slow.resolve(pending("L1", { message: "Still waiting for the browser" })));
    expect(text(dialog)).toContain("Still waiting for the browser");
    await wait(1999);
    expect(accountCalls(mark)).toHaveLength(3);

    // Approved: the dialog says so, a toast confirms, the card's figures are
    // dropped, the cards reload (proxies, then status) and the card's usage
    // loads anew, the account's windows included; checking stops.
    expect(text(card("Alpha"))).toContain("5h tokens");
    creds = [{ ...alpha, account: { email: "alice@example.com" } }, beta];
    script("GET /account/login", ok({ id: "L1", auth_index: "a1", state: "approved", email: "alice@example.com" }));
    const figures = deferred();
    script("GET /credentials/quota", figures.promise);
    const approvedMark = sent.length;
    await wait(1);
    expect(calls(approvedMark)).toEqual(["GET /account/login?id=L1", "GET /proxies", "GET /status", "GET /credentials/quota?auth_index=a1"]);
    expect(text(dialog)).toContain("Signed in as alice@example.com");
    expect(buttonNames(dialog)).toEqual(["Close", "Done"]);
    expect(button(dialog, "Done").className).toContain("btn-primary");
    expect(toast()).toBe("Alpha is signed in as alice@example.com");
    expect(text(card("Alpha"))).toContain("Loading…");
    expect(text(card("Alpha"))).not.toContain("5h tokens");
    expect(buttonNames(card("Alpha"))).toContain("Sign out");
    expect(buttonNames(card("Alpha"))).not.toContain("Sign in");

    const window5h = { kind: "5h", duration_seconds: 18_000, used: 25, limit: 100, remaining: 75 };
    await run(() => figures.resolve(quota({ email: "alice@example.com", tier: "pro", eligible: true, windows: [window5h] })));
    expect(text(card("Alpha"))).toContain("5-hour window");
    expect(text(card("Alpha"))).toContain("Account alice@example.com · Plan Pro");
    expect(text(card("Alpha"))).toContain("5h tokens");
    expect(text(card("Alpha"))).not.toContain("Loading…");
    expect(text(card("Alpha"))).not.toContain("Usage not loaded yet.");

    await wait(10_000);
    expect(calls(approvedMark)).toHaveLength(4);
    await click(button(dialog, "Done"));
    expect(dialog.open).toBe(false);
    expect(calls(approvedMark)).toHaveLength(4);
  });

  it.each<[string, (d: HTMLDialogElement) => Promise<void>]>([
    ["the × button", (d) => click(button(d, "Close", 0))],
    ["Cancel sign-in", (d) => click(button(d, "Cancel sign-in"))],
    ["Esc (the dialog closes itself)", (d) => run(() => d.close())],
  ])("closing a waiting sign-in with %s cancels it and stops checking", async (_how, close) => {
    await openPage();
    await startWaiting();
    script("GET /account/login", pending("L1"));
    await wait(2000);
    script("DELETE /account/login", ok({ id: "L1", auth_index: "a1", state: "cancelled" }));
    const mark = sent.length;
    const dialog = signinDialog();
    await close(dialog);
    expect(dialog.open).toBe(false);
    expect(calls(mark)).toEqual(["DELETE /account/login?id=L1"]);
    expect(sent[mark].body).toBeUndefined();
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(["DELETE /account/login?id=L1"]);
  });

  it("closing while the sign-in is still starting cancels it as soon as it started", async () => {
    await openPage();
    const start = deferred();
    script("POST /account/login", start.promise);
    const mark = sent.length;
    await click(button(card("Alpha"), "Sign in"));
    const dialog = signinDialog();
    await click(button(dialog, "Cancel sign-in"));
    expect(dialog.open).toBe(false);
    expect(accountCalls(mark)).toEqual(["POST /account/login"]);
    script("DELETE /account/login", ok({ id: "L1", auth_index: "a1", state: "cancelled" }));
    await run(() => start.resolve(pending("L1")));
    expect(accountCalls(mark)).toEqual(["POST /account/login", "DELETE /account/login?id=L1"]);
    expect(dialog.open).toBe(false);
    await wait(10_000);
    expect(accountCalls(mark)).toHaveLength(2);
  });

  it.each<[string, object, string]>([
    ["denied", {}, "The authorization was refused on the website."],
    ["expired", {}, "The code expired; sign in again."],
    ["cancelled", {}, "The sign-in was cancelled."],
    ["failed", { message: "device code rejected" }, "Sign-in failed: device code rejected"],
  ])("stops checking once the sign-in is %s; closing then sends nothing", async (state, extra, message) => {
    await openPage();
    const mark = await startWaiting();
    script("GET /account/login", ok({ id: "L1", auth_index: "a1", state, ...extra }));
    await wait(2000);
    const dialog = signinDialog();
    expect(text(dialog)).toContain(message);
    expect(text(dialog)).not.toContain("ABCD-EFGH");
    expect(buttonNames(dialog)).toEqual(["Close", "Sign in again", "Close"]);
    expect(button(dialog, "Close", 1).className).toContain("btn-secondary");
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(["POST /account/login", "GET /account/login?id=L1"]);
    await click(button(dialog, "Close", 1));
    expect(dialog.open).toBe(false);
    expect(accountCalls(mark)).toHaveLength(2);
    expect(toast()).toBe("");
  });

  it("Sign in again starts a new sign-in on the same card", async () => {
    await openPage();
    const mark = await startWaiting();
    script("GET /account/login", ok({ id: "L1", auth_index: "a1", state: "expired" }));
    await wait(2000);
    const dialog = signinDialog();
    script("POST /account/login", pending("L2", { user_code: "WXYZ-2345" }));
    await click(button(dialog, "Sign in again"));
    expect(accountCalls(mark)).toEqual(["POST /account/login", "GET /account/login?id=L1", "POST /account/login"]);
    expect(sent[sent.length - 1].body).toEqual({ auth_index: "a1" });
    expect(dialog.open).toBe(true);
    expect(text(dialog.querySelector("code")!)).toBe("WXYZ-2345");
    expect(buttonNames(dialog)).toEqual(["Close", "Copy link", "Copy", "Cancel sign-in"]);
    script("GET /account/login", pending("L2"));
    await wait(2000);
    expect(accountCalls(mark).slice(3)).toEqual(["GET /account/login?id=L2"]);
  });

  it("keeps checking through a failed check and shows its error until one succeeds", async () => {
    await openPage();
    const mark = await startWaiting();
    script("GET /account/login", { status: 500, body: { message: "upstream timeout" } });
    await wait(2000);
    const dialog = signinDialog();
    expect(text(dialog)).toContain("upstream timeout");
    expect(text(dialog)).toContain("ABCD-EFGH");
    expect(buttonNames(dialog)).toEqual(["Close", "Copy link", "Copy", "Cancel sign-in"]);
    script("GET /account/login", pending("L1"));
    await wait(2000);
    expect(accountCalls(mark)).toEqual(["POST /account/login", "GET /account/login?id=L1", "GET /account/login?id=L1"]);
    expect(text(dialog)).not.toContain("upstream timeout");
  });

  it("stops checking when the plugin no longer knows the sign-in (404), but cancelling still asks", async () => {
    await openPage();
    const mark = await startWaiting();
    script("GET /account/login", { status: 404, body: { message: "sign-in not found" } });
    await wait(2000);
    const dialog = signinDialog();
    expect(text(dialog)).toContain("sign-in not found");
    expect(buttonNames(dialog)).toEqual(["Close", "Copy link", "Copy", "Cancel sign-in"]);
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(["POST /account/login", "GET /account/login?id=L1"]);
    script("DELETE /account/login", { status: 404, body: { message: "sign-in not found" } });
    await click(button(dialog, "Cancel sign-in"));
    expect(dialog.open).toBe(false);
    expect(accountCalls(mark).slice(2)).toEqual(["DELETE /account/login?id=L1"]);
  });

  it.each<[string, Reply, string]>([
    ["a JSON error", { status: 502, body: { message: "OpenDesign unreachable" } }, "OpenDesign unreachable"],
    ["a plain-text error", { status: 502, body: "Bad Gateway" }, "Bad Gateway"],
    ["an empty error", { status: 503, body: "" }, "HTTP 503"],
  ])("a start that fails with %s shows why and offers to retry", async (_kind, reply, msg) => {
    await openPage();
    script("POST /account/login", reply);
    const mark = sent.length;
    await click(button(card("Alpha"), "Sign in"));
    const dialog = signinDialog();
    expect(text(dialog)).toContain("Could not start the sign-in: " + msg);
    // Nothing started, so there is nothing to cancel: the footer reads "Close".
    expect(buttonNames(dialog)).toEqual(["Close", "Sign in again", "Close"]);
    expect(button(dialog, "Close", 1).className).toContain("btn-secondary");
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(["POST /account/login"]);
    script("POST /account/login", pending("L2"));
    await click(button(dialog, "Sign in again"));
    expect(accountCalls(mark)).toEqual(["POST /account/login", "POST /account/login"]);
    expect(text(dialog)).toContain("Waiting for you to approve on the website…");
  });

  it("closing a start that failed just closes the dialog", async () => {
    await openPage();
    script("POST /account/login", { status: 502, body: { message: "OpenDesign unreachable" } });
    const mark = sent.length;
    await click(button(card("Alpha"), "Sign in"));
    const dialog = signinDialog();
    await click(button(dialog, "Close", 1));
    expect(dialog.open).toBe(false);
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(["POST /account/login"]);
  });

  it.each<[string, boolean, Reply]>([
    ["starting", false, { status: 401, body: { message: "invalid management key" } }],
    ["checking", true, { status: 403, body: { message: "invalid management key" } }],
  ])("a management key rejected while %s closes the dialog and asks for the key", async (_when, waiting, reply) => {
    await openPage();
    let mark = sent.length;
    if (waiting) {
      mark = await startWaiting();
      script("GET /account/login", reply);
      await wait(2000);
    } else {
      script("POST /account/login", reply);
      await click(button(card("Alpha"), "Sign in"));
    }
    expect(signinDialog().open).toBe(false);
    expect(keyPromptShown()).toBe(true);
    expect(keyPromptError()).toBe("The management key was rejected: invalid management key");
    await wait(10_000);
    expect(accountCalls(mark)).toEqual(waiting ? ["POST /account/login", "GET /account/login?id=L1"] : ["POST /account/login"]);
  });
});

describe("signing an account out of a key card", () => {
  it("takes two clicks, then revokes, confirms, refreshes the cards and reloads the card's usage", async () => {
    await openPage();
    const signOut = () => button(card("Beta"), "Sign out");
    expect(signOut().getAttribute("title")).toBe("Signed in as beta@example.com");
    expect(buttonNames(card("Alpha"))).toContain("Sign in");

    const mark = sent.length;
    await click(signOut());
    const armed = button(card("Beta"), "Click to confirm");
    expect(armed.className).toContain("armed");
    expect(calls(mark)).toEqual([]);

    const answer = deferred();
    script("DELETE /account", answer.promise);
    await click(armed);
    expect(calls(mark)).toEqual(["DELETE /account?auth_index=b2"]);
    expect(sent[mark].body).toBeUndefined();
    expect(sent[mark].headers).toEqual({ Authorization: "Bearer " + KEY });
    expect(signOut().disabled).toBe(true);
    expect(signOut().className).not.toContain("armed");

    creds = [alpha, { ...beta, account: undefined }];
    const figures = deferred();
    script("GET /credentials/quota", figures.promise);
    await run(() => answer.resolve(ok({ ok: true, revoked: true })));
    expect(calls(mark)).toEqual(["DELETE /account?auth_index=b2", "GET /proxies", "GET /status", "GET /credentials/quota?auth_index=b2"]);
    expect(toast()).toBe("Beta is signed out");
    expect(notice()).toBe("");
    expect(text(card("Beta"))).toContain("Loading…");
    expect(text(card("Beta"))).not.toContain("5h tokens");
    expect(buttonNames(card("Beta"))).toContain("Sign in");
    expect(buttonNames(card("Beta"))).not.toContain("Sign out");

    await run(() => figures.resolve(quota()));
    expect(text(card("Beta"))).toContain("5h tokens");
    expect(text(card("Beta"))).not.toContain("Loading…");
    expect(text(card("Beta"))).not.toContain("Usage not loaded yet.");
    await wait(10_000);
    expect(calls(mark)).toHaveLength(4);
  });

  it("an armed sign-out disarms after 4 s, or when another two-click action is armed", async () => {
    await openPage();
    const mark = sent.length;
    await click(button(card("Beta"), "Sign out"));
    await wait(3999);
    button(card("Beta"), "Click to confirm");
    await wait(1);
    await click(button(card("Beta"), "Sign out"));
    expect(button(card("Beta"), "Click to confirm").className).toContain("armed");

    await click(button(card("Beta"), "Delete"));
    expect(buttonNames(card("Beta"))).toContain("Sign out");
    expect(buttonNames(card("Beta"))).toContain("Click again");
    expect(buttonNames(card("Beta"))).not.toContain("Click to confirm");
    expect(calls(mark)).toEqual([]);
  });

  it("warns in the notice bar when the key could not be revoked, and still refreshes and reloads the usage", async () => {
    await openPage();
    const mark = sent.length;
    await click(button(card("Beta"), "Sign out"));
    creds = [alpha, { ...beta, account: undefined }];
    script("DELETE /account", ok({ ok: true, revoked: false, revoke_error: "device not found" }));
    await click(button(card("Beta"), "Click to confirm"));
    expect(calls(mark)).toEqual(["DELETE /account?auth_index=b2", "GET /proxies", "GET /status", "GET /credentials/quota?auth_index=b2"]);
    expect(notice()).toBe(
      "Beta is signed out, but its key could not be revoked: device not found. Remove the device from the OpenDesign website's device list.",
    );
    expect(toast()).toBe("");
    expect(buttonNames(card("Beta"))).toContain("Sign in");
  });

  it("a failed sign-out shows the error and leaves the card as it was", async () => {
    await openPage();
    const mark = sent.length;
    await click(button(card("Beta"), "Sign out"));
    script("DELETE /account", { status: 500, body: { message: "revoke failed" } });
    await click(button(card("Beta"), "Click to confirm"));
    expect(calls(mark)).toEqual(["DELETE /account?auth_index=b2"]);
    expect(notice()).toBe("Failed: revoke failed");
    expect(toast()).toBe("");
    expect(button(card("Beta"), "Sign out").disabled).toBe(false);
    expect(text(card("Beta"))).not.toContain("Usage not loaded yet.");
  });

  it("a management key rejected on sign-out asks for the key", async () => {
    await openPage();
    const mark = sent.length;
    await click(button(card("Beta"), "Sign out"));
    script("DELETE /account", { status: 401, body: { message: "invalid management key" } });
    await click(button(card("Beta"), "Click to confirm"));
    expect(calls(mark)).toEqual(["DELETE /account?auth_index=b2"]);
    expect(keyPromptShown()).toBe(true);
    expect(keyPromptError()).toBe("The management key was rejected: invalid management key");
  });
});
