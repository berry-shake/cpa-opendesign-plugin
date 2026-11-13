import { localeTag, type T } from "../../i18n";
import { credentialsOf, credMatches, summarizeKeys, windowTokens } from "../../lib/credentials";
import { fmtTokens } from "../../lib/format";
import { store, type State } from "../../state/store";
import { CredCard, windowTip } from "./CredCard";

// One cell of a stat strip: label above, value with an optional note beside it.
function StatItem({ label, value, note, tip }: { label: string; value: string; note?: string; tip?: string }) {
  return (
    <div className="strip-item">
      <div className="strip-label" title={tip ? label + "\n" + tip : label}>{label}</div>
      <div className="strip-line">
        <span className="strip-value" title={tip || undefined}>{value}</span>
        {note ? <span className="strip-note" title={note}>{note}</span> : null}
      </div>
    </div>
  );
}

// Key count and the usage of all keys whose figures have loaded, over the
// windows the Go plan limits (5 hours, 7 days, a month).
function KeySummary({ t, s }: { t: T; s: State }) {
  const creds = credentialsOf(s.status);
  if (!creds.length) return <div className="summary strip" id="key-summary" hidden></div>;
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

function KeysBody({ t, s }: { t: T; s: State }) {
  const data = s.status;
  const creds = credentialsOf(data);
  if (!data) return <div className="empty">{t("keys.notLoaded")}</div>;
  if (data.list_error) return <div className="empty danger-text">{t("keys.listError", { msg: data.list_error })}</div>;
  if (creds.length === 0) {
    return (
      <div className="empty-state">
        <strong>{t("keys.emptyTitle")}</strong>
        <div>{t("keys.empty")}</div>
        <button type="button" className="btn btn-primary" onClick={() => store.openEditor("add")}>{t("actions.add")}</button>
      </div>
    );
  }
  const filter = s.keysFilter.trim().toLowerCase();
  const visible = creds.filter((c) => credMatches(c, filter));
  if (visible.length === 0) return <div className="empty">{t("keys.noMatch")}</div>;
  const locale = localeTag(s.lang);
  return (
    <>
      {visible.map((cred) => (
        <CredCard
          key={cred.auth_index || cred.name}
          t={t}
          cred={cred}
          q={s.quota[cred.auth_index]}
          armed={s.armedDelete === cred.name}
          busy={s.busy}
          locale={locale}
        />
      ))}
    </>
  );
}

export function KeysPanel({ t, s }: { t: T; s: State }) {
  const data = s.status;
  const creds = credentialsOf(data);
  const filter = s.keysFilter.trim().toLowerCase();
  let count = "";
  if (creds.length) {
    const shown = creds.filter((c) => credMatches(c, filter)).length;
    count = filter ? t("keys.shown", { shown, total: creds.length }) : t("keys.count", { count: creds.length });
  }
  return (
    <section className="panel" role="tabpanel" id="panel-keys" aria-labelledby="tab-keys" hidden={s.tab !== "keys"}>
      <KeySummary t={t} s={s} />
      <section className="card">
        <div className="card-header">
          <div className="card-title">
            <h2>{t("keys.title")}</h2>
            <span className="hint">{t("keys.hint")}</span>
          </div>
          <div className="actions">
            <button id="quota-button" className="btn btn-secondary btn-sm" type="button" hidden={creds.length === 0} disabled={!!s.busy.quota} onClick={() => void store.refreshQuotas()}>
              {t("actions.reloadQuota")}
            </button>
            {/* The empty state carries its own add button. */}
            <button id="add-button" className="btn btn-primary btn-sm" type="button" hidden={!!data && !data.list_error && creds.length === 0} onClick={() => store.openEditor("add")}>
              {t("actions.add")}
            </button>
          </div>
        </div>
        <div className="toolbar" id="keys-toolbar" hidden={creds.length === 0}>
          <input
            className="input input-sm search"
            id="keys-search"
            type="search"
            spellCheck={false}
            placeholder={t("keys.search")}
            value={s.keysFilter}
            onChange={(event) => store.setKeysFilter(event.target.value)}
          />
          <span className="count" id="keys-count">{count}</span>
        </div>
        <div className="cred-list" id="keys-body">
          <KeysBody t={t} s={s} />
        </div>
      </section>
    </section>
  );
}
