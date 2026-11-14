import type { ReactNode } from "react";
import type { AccountUsage, CredentialView, PlanWindow, QuotaView, UsageWindow } from "../../api/types";
import type { T } from "../../i18n";
import { barView, credState, credSubline, displayName, isDirect, planWindows, USAGE_WINDOWS, windowTokens, type QuotaEntry } from "../../lib/credentials";
import { fmtCount, fmtTokens, fmtUSD, formatShort, formatTime } from "../../lib/format";
import { store } from "../../state/store";

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

/** Where the figures come from, and since when the log has them. */
function sourceTip(t: T, d: QuotaView, locale: string): string {
  const since = d.log_since && Date.parse(d.fetched_at) - Date.parse(d.log_since) < 30 * 24 * 3600 * 1000
    ? t("usage.logSince", { time: formatShort(d.log_since, locale) })
    : "";
  return [t("usage.source"), since].filter(Boolean).join("\n");
}

// One figure of a card's usage grid: label over value, details in the tooltip.
function Meter({ label, value, tip }: { label: string; value: string; tip?: string }) {
  return (
    <div className="meter">
      <div className="meter-label" title={tip ? label + "\n" + tip : label}>{label}</div>
      <div className="meter-value" title={tip ? value + "\n" + tip : value}>{value}</div>
    </div>
  );
}

// Four facts laid out like the usage figures; details are in the tooltip.
function Fact({ label, text, tip, extra, flag }: { label: string; text: string; tip?: string; extra?: ReactNode; flag?: string }) {
  return (
    <div className="meter">
      <div className="meter-label" title={label}>{label}{flag ? <span className="warn-text">{" · " + flag}</span> : null}</div>
      <div className="meter-value text" title={tip || text}>{text}{extra || null}</div>
    </div>
  );
}

function Facts({ t, cred }: { t: T; cred: CredentialView }) {
  const models = Array.isArray(cred.models) ? cred.models : [];
  const listed = cred.listed_models !== undefined && cred.listed_models !== null ? t("models.listed", { count: cred.listed_models }) : "";
  const missing = Array.isArray(cred.missing_models) ? cred.missing_models : [];
  const modelsText = cred.all_models ? t("models.all") : t("models.selected", { count: models.length });
  const prefixText = cred.effective_prefix || t("prefix.none");
  let proxyText = t("proxy.inherit");
  let proxyTip: string | undefined;
  if (cred.proxy_name) {
    proxyText = cred.proxy_name;
    proxyTip = cred.proxy_url;
  } else if (cred.proxy_url) {
    proxyText = isDirect(cred.proxy_url) ? t("proxy.direct") : cred.proxy_url;
  }
  const ok = cred.success || 0;
  const failed = cred.failed || 0;
  return (
    <div className="facts">
      <Fact
        label={t("card.models")}
        text={modelsText}
        tip={[
          modelsText,
          listed,
          cred.registered_known ? t("models.registered", { count: cred.registered_models }) : "",
          missing.length ? t("models.missing", { list: missing.join(", ") }) : "",
        ].filter(Boolean).join("\n")}
        flag={missing.length ? t("models.missingCount", { count: missing.length }) : ""}
      />
      <Fact
        label={t("card.prefix")}
        text={prefixText}
        tip={prefixText + "\n" + t(cred.prefix === null || cred.prefix === undefined ? "prefix.default" : "prefix.custom")}
      />
      <Fact label={t("card.proxy")} text={proxyText} tip={proxyTip} />
      <Fact
        label={t("card.requestsLabel")}
        text={ok + " / "}
        tip={t("card.requests", { ok, failed })}
        extra={<span className={failed ? "danger-text" : ""}>{String(failed)}</span>}
      />
    </div>
  );
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
          />
        ))}
      </div>
      {q && q.state === "error" ? <div className="usage-note danger-text">{t("usage.failed", { msg: q.error })}</div> : null}
    </>
  );
}

export function CredCard({ t, cred, q, armed, logoutArmed, busy, locale }: {
  t: T;
  cred: CredentialView;
  q: QuotaEntry | undefined;
  armed: boolean;
  logoutArmed: boolean;
  busy: Record<string, true>;
  locale: string;
}) {
  const [badgeClass, badgeKey] = credState(cred);
  // Why the key is off or cooling down, under the figures on its own line.
  const stateNote = cred.read_error || cred.status_message || "";
  const name = displayName(cred);
  return (
    <article className={"cred" + (cred.disabled ? " off" : "")}>
      <div className="cred-head">
        <div className="cred-row">
          <div className="cred-title">
            <span className="cred-name" title={name}>{name}</span>
            <span className={"badge " + badgeClass}>{t(badgeKey)}</span>
          </div>
        </div>
        <div className="cred-row">
          <div className="cred-file" title={cred.name}>{credSubline(cred, t("card.priority"))}</div>
        </div>
      </div>
      <div className="cred-body">
        {cred.read_error ? null : <Facts t={t} cred={cred} />}
        <UsageParts t={t} cred={cred} q={q} />
        <AccountParts t={t} cred={cred} a={q && q.data ? q.data.account : undefined} locale={locale} />
        {cred.note ? <div className="usage-note sub" title={cred.note}>{t("card.noteLine", { msg: cred.note })}</div> : null}
        {stateNote ? <div className={"usage-note " + (cred.read_error ? "danger-text" : "warn-text")}>{t("card.statusNote", { msg: stateNote })}</div> : null}
      </div>
      <div className="cred-foot">
        <div className="cred-meta">
          {q && q.data && q.data.fetched_at ? (
            <span title={sourceTip(t, q.data, locale)}>{t("usage.fetched", { time: formatTime(q.data.fetched_at, locale) })}</span>
          ) : null}
          {!cred.read_error && q && q.data ? (
            <button type="button" className="btn-link" disabled={q.state === "loading"} onClick={() => void store.loadQuota(cred.auth_index)}>
              {t(q.state === "loading" ? "usage.loading" : "usage.refresh")}
            </button>
          ) : null}
        </div>
        <div className="row-actions">
          {cred.read_error ? null : cred.account ? (
            <button
              type="button"
              className={"btn btn-secondary btn-sm" + (logoutArmed ? " armed" : "")}
              disabled={!!busy["logout:" + cred.name]}
              title={t("account.signedInAs", { email: cred.account.email || "—" })}
              onClick={() => void store.logoutAccount(cred)}
            >
              {t(logoutArmed ? "account.confirmSignOut" : "account.signOut")}
            </button>
          ) : (
            <button type="button" className="btn btn-secondary btn-sm" title={t("account.signInHint")} onClick={() => void store.startAccountLogin(cred)}>
              {t("account.signIn")}
            </button>
          )}
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!cred.read_error} onClick={() => store.openEditor("edit", cred)}>
            {t("row.edit")}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy["toggle:" + cred.name]} onClick={() => void store.toggleDisabled(cred)}>
            {t(cred.disabled ? "row.enable" : "row.disable")}
          </button>
          <button
            type="button"
            className={"btn btn-danger btn-sm" + (armed ? " armed" : "")}
            disabled={!!busy["delete:" + cred.name]}
            onClick={() => void store.deleteCredential(cred)}
          >
            {t(armed ? "row.confirmDelete" : "row.delete")}
          </button>
        </div>
      </div>
    </article>
  );
}
