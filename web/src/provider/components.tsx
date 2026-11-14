import { Meter, StatItem } from "@core/components/keys/parts";
import type { KeyCardProps, ProviderComponents } from "@core/contract";
import type { CredentialView, QuotaView } from "@core/api/types";
import type { T } from "@core/i18n";
import { barView, displayName, type QuotaEntry } from "@core/lib/credentials";
import { fmtCount, fmtTokens, fmtUSD, formatShort, formatTime } from "@core/lib/format";
import { store, type State } from "@core/state/store";
import { account } from "./account";
import { AccountLoginDialog } from "./AccountLoginDialog";
import { modelTags, modelTier, planWindows, summarizeKeys, USAGE_WINDOWS, windowTokens } from "./lib";
import type { AccountUsage, ModelView, PlanWindow, UsageWindow } from "./types";

/** Requests and the token breakdown of a window, for its tooltip. */
export function windowTip(t: T, w: UsageWindow | undefined): string {
  if (!w) return "";
  return t("usage.tip", {
    n: w.requests || 0,
    f: w.failed || 0,
    i: fmtTokens(w.input_tokens || 0),
    c: fmtTokens(w.cached_tokens || 0),
    o: fmtTokens(w.output_tokens || 0),
    r: fmtTokens(w.reasoning_tokens || 0),
  });
}

// Key count and the usage of all keys whose figures have loaded, over the
// windows the Go plan limits (5 hours, 7 days, a month).
function KeySummary({ t, s, creds }: { t: T; s: State; creds: CredentialView[] }) {
  const sum = summarizeKeys(creds, s.quota);
  const blank = sum.loading ? "…" : "—";
  const failed = sum.failed ? t("summary.failed", { n: sum.failed }) : "";
  const cell = (key: "five_hours" | "week" | "month", note?: string) => {
    const w = sum.windows[key];
    return (
      <StatItem
        label={t("usage.tokens", { w: t("usage.w." + key) })}
        value={sum.loaded ? fmtTokens(windowTokens(w)) : blank}
        note={[sum.loaded ? t("summary.requests", { n: w.requests }) : "", note || ""].filter(Boolean).join(" · ")}
        tip={sum.loaded ? windowTip(t, w) : ""}
      />
    );
  };
  return (
    <div className="summary strip" id="key-summary">
      <StatItem label={t("summary.keys")} value={String(sum.total)} note={t("summary.enabled", { n: sum.enabled })} />
      {cell("five_hours")}
      {cell("week")}
      {cell("month", failed)}
    </div>
  );
}

/** Where the figures come from, and since when the log has them. */
function sourceTip(t: T, d: QuotaView, locale: string): string {
  const since = d.log_since && Date.parse(d.fetched_at) - Date.parse(d.log_since) < 30 * 24 * 3600 * 1000
    ? t("usage.logSince", { time: formatShort(d.log_since, locale) })
    : "";
  return [t("usage.source"), since].filter(Boolean).join("\n");
}

/** The chat models the key's plan lists, for the models fact's tooltip. */
function modelsTip(t: T, cred: CredentialView): string {
  return cred.listed_models !== undefined && cred.listed_models !== null ? t("models.listed", { count: cred.listed_models }) : "";
}

// A remaining-allowance bar: the window and when it resets on the left, what
// is left on the right, coloured like the bar.
function Bar({ t, label, left, total, value, tip }: { t: T; label: string; left: number; total: number; value: string; tip?: string }) {
  const bar = barView(left, total);
  return (
    <div className={"cap" + bar.level} title={tip}>
      <div className="cap-head">
        <span className="cap-label">{label}</span>
        <span className="cap-left">{t("account.left")} <strong>{value}</strong></span>
      </div>
      <div className="track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(bar.fraction * 100)}>
        <span className="fill" style={{ width: bar.width }}></span>
      </div>
    </div>
  );
}

function windowLabel(t: T, w: PlanWindow, locale: string): string {
  const name = w.kind === "5h" || w.kind === "7d"
    ? t("account.window." + w.kind)
    : t("account.window.other", { h: Math.round((w.duration_seconds / 3600) * 10) / 10 });
  return w.resets_at ? name + " · " + t("account.resets", { time: formatShort(w.resets_at, locale) }) : name;
}

// The plan windows of the account signed in on the card, then a line with
// the account, plan and balance.
function AccountParts({ t, cred, a, locale }: { t: T; cred: CredentialView; a: AccountUsage | undefined; locale: string }) {
  if (!cred.account) return null;
  if (!a) return null;
  if (a.expired) return <div className="usage-note warn-text" title={a.error}>{t("account.expired")}</div>;
  const facts = [
    t("account.line", { email: a.email || cred.account.email || "—" }),
    a.tier ? t("account.tier", { tier: a.tier.charAt(0).toUpperCase() + a.tier.slice(1) }) : "",
    a.balance_usd ? t("account.balance", { v: fmtUSD(a.balance_usd) }) : "",
  ].filter(Boolean).join(" · ");
  const tip = [
    a.workspace_name ? t("account.workspace", { name: a.workspace_name }) : "",
    a.period_end ? t(a.cancel_at_period_end ? "account.periodCancel" : "account.periodEnd", { time: formatShort(a.period_end, locale) }) : "",
    a.fetched_at ? t("usage.fetched", { time: formatTime(a.fetched_at, locale) }) : "",
  ].filter(Boolean).join("\n");
  const windows = planWindows(a);
  return (
    <>
      {windows.map((w) => (
        <Bar
          key={w.kind}
          t={t}
          label={windowLabel(t, w, locale)}
          left={w.remaining}
          total={w.limit}
          value={w.limit > 0 ? Math.round((Math.max(0, w.remaining) / w.limit) * 100) + "%" : "—"}
          tip={t("account.used", { used: fmtCount(w.used), limit: fmtCount(w.limit), left: fmtCount(w.remaining) })}
        />
      ))}
      {!a.error && !windows.length ? <div className="usage-note sub">{t(a.eligible ? "account.noWindows" : "account.noPlan")}</div> : null}
      <div className="usage-note sub" title={tip}>{facts}</div>
      {a.error ? <div className="usage-note danger-text">{t("account.failed", { msg: a.error })}</div> : null}
    </>
  );
}

// The card's figures (or why there are none), then a note when they failed
// to reload.
function UsageParts({ t, cred, q }: { t: T; cred: CredentialView; q: QuotaEntry | undefined }) {
  if (cred.read_error) return null;
  const d = q && q.data;
  if (!d) {
    const load = () => void store.loadQuota(cred.auth_index);
    const failed = q && q.state === "error" ? <div className="usage-note danger-text">{q.error}</div> : null;
    return (
      <>
        <div className="usage-state">
          {q && q.state === "loading" ? (
            <span>{t("usage.loading")}</span>
          ) : q && q.state === "error" ? (
            <>
              <span className="danger-text" title={q.error}>{t("usage.failedShort")}</span>
              <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.retry")}</button>
            </>
          ) : (
            <>
              <span>{t("usage.notLoaded")}</span>
              <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.check")}</button>
            </>
          )}
        </div>
        {failed}
      </>
    );
  }
  if (d.log_error) {
    return (
      <>
        <div className="usage-state"><span className="warn-text" title={d.log_error}>{t("usage.logOff")}</span></div>
        {q && q.state === "error" ? <div className="usage-note danger-text">{t("usage.failed", { msg: q.error })}</div> : null}
      </>
    );
  }
  return (
    <>
      <div className="meters">
        {USAGE_WINDOWS.map((key) => (
          <Meter
            key={key}
            label={t("usage.tokens", { w: t("usage.w." + key) })}
            value={fmtTokens(windowTokens(d[key]))}
            tip={windowTip(t, d[key])}
            sep={"\n"}
          />
        ))}
      </div>
      {q && q.state === "error" ? <div className="usage-note danger-text">{t("usage.failed", { msg: q.error })}</div> : null}
    </>
  );
}

function KeyUsage({ t, cred, q, locale }: KeyCardProps) {
  return (
    <>
      <UsageParts t={t} cred={cred} q={q} />
      <AccountParts t={t} cred={cred} a={q && q.data ? q.data.account : undefined} locale={locale} />
    </>
  );
}

// Sign in, or (two clicks) sign out the account on the card.
function KeyActions({ t, cred, busy, armedDelete }: { t: T; cred: CredentialView; busy: Record<string, true>; armedDelete: string | null }) {
  if (cred.read_error) return null;
  if (cred.account) {
    const armed = armedDelete === "logout:" + cred.name;
    return (
      <button
        type="button"
        className={"btn btn-secondary btn-sm" + (armed ? " armed" : "")}
        disabled={!!busy["logout:" + cred.name]}
        title={t("account.signedInAs", { email: cred.account.email || "—" })}
        onClick={() => void account.logout(cred)}
      >
        {t(armed ? "account.confirmSignOut" : "account.signOut")}
      </button>
    );
  }
  return (
    <button type="button" className="btn btn-secondary btn-sm" title={t("account.signInHint")} onClick={() => void account.startLogin(cred)}>
      {t("account.signIn")}
    </button>
  );
}

// The names of the keys whose plans list a model, one per line, for the
// "in plans of" column.
function keysTip(ids: string[], creds: CredentialView[]): string {
  const names = new Map<string, string[]>();
  for (const cred of creds) {
    if (!cred.key_id) continue;
    names.set(cred.key_id, [...(names.get(cred.key_id) || []), displayName(cred)]);
  }
  return ids.flatMap((id) => names.get(id) || [id]).join("\n");
}

function ModelCells({ t, m, creds }: { t: T; m: ModelView; creds: CredentialView[] }) {
  return (
    <>
      <td>
        <div className="mono strong">{m.id}</div>
        {m.default ? <div className="sub">{t("catalog.default")}</div> : null}
      </td>
      <td>
        <div>{modelTags(t, m) || (m.chat === false ? "—" : t("cap.textOnly"))}</div>
        {modelTier(t, m) ? <div className="sub">{modelTier(t, m)}</div> : null}
        {m.enabled === false ? <div className="sub warn-text">{t("catalog.disabled")}</div> : m.chat === false ? <div className="sub warn-text">{t("catalog.notChat")}</div> : null}
      </td>
      <td className="nowrap">
        <div>{m.context_length ? fmtTokens(m.context_length) : "—"}</div>
        {m.context_budget ? <div className="sub">{t("catalog.budget", { v: fmtTokens(m.context_budget) })}</div> : null}
        {m.max_output ? <div className="sub">{t("catalog.maxOutput", { v: fmtTokens(m.max_output) })}</div> : null}
      </td>
      <td>
        {Array.isArray(m.keys) && m.keys.length ? (
          <span title={keysTip(m.keys, creds)}>{t("catalog.listedBy", { count: m.keys.length })}</span>
        ) : (
          <span className="sub">{t("catalog.listedNone")}</span>
        )}
      </td>
    </>
  );
}

export const components: ProviderComponents = {
  KeySummary,
  KeyUsage,
  modelsTip,
  fetchedTip: sourceTip,
  KeyActions,
  modelColumns: ["col.model", "col.capabilities", "col.context", "col.keys"],
  ModelCells,
  Dialogs: AccountLoginDialog,
};
