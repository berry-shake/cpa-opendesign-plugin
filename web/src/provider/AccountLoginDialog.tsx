import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import type { T } from "@core/i18n";
import { credentialsOf } from "@core/lib/credentials";
import type { State } from "@core/state/store";
import { account, type AccountLoginState } from "./account";

function remaining(expiresAt: string | undefined, now: number): string {
  const end = expiresAt ? Date.parse(expiresAt) : NaN;
  if (isNaN(end)) return "";
  const sec = Math.max(0, Math.round((end - now) / 1000));
  return Math.floor(sec / 60) + ":" + String(sec % 60).padStart(2, "0");
}

function copyText(text: string, done: () => void): void {
  if (!text || !navigator.clipboard) return;
  navigator.clipboard.writeText(text).then(done, () => {});
}

function Body({ t, login, now }: { t: T; login: AccountLoginState; now: number }) {
  const [copied, setCopied] = useState("");
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const view = login.view;
  if (!view) {
    return login.error
      ? <p className="danger-text">{t("signin.startFailed", { msg: login.error })}</p>
      : <p className="sub">{t("signin.starting")}</p>;
  }
  switch (view.state) {
    case "pending":
      return (
        <div className="signin">
          <p>{t("signin.step1")}</p>
          <div className="signin-row">
            <a className="btn btn-primary" href={view.activation_url} target="_blank" rel="noopener noreferrer">{t("signin.open")}</a>
            <button type="button" className="btn btn-secondary" onClick={() => copyText(view.activation_url || "", () => setCopied("url"))}>
              {t(copied === "url" ? "signin.copied" : "signin.copyLink")}
            </button>
          </div>
          <p>{t("signin.step2")}</p>
          <div className="signin-row">
            <code className="signin-code" aria-label={t("signin.code")}>{view.user_code}</code>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => copyText(view.user_code || "", () => setCopied("code"))}>
              {t(copied === "code" ? "signin.copied" : "signin.copyCode")}
            </button>
          </div>
          <p className="sub">{t("signin.waiting", { time: remaining(view.expires_at, now) })}</p>
          {login.error || view.message ? <p className="warn-text signin-note">{login.error || view.message}</p> : null}
        </div>
      );
    case "approved":
      return <p className="ok-text">{t("signin.approved", { email: view.email || "" })}</p>;
    case "denied":
      return <p className="warn-text">{t("signin.denied")}</p>;
    case "expired":
      return <p className="warn-text">{t("signin.expired")}</p>;
    case "cancelled":
      return <p className="sub">{t("signin.cancelled")}</p>;
    default:
      return <p className="danger-text">{t("signin.failed", { msg: view.message || login.error || "" })}</p>;
  }
}

/**
 * Signs an OpenDesign account in on a key card. It stays mounted; opening a
 * sign-in shows it and closing it cancels a sign-in still waiting.
 */
export function AccountLoginDialog({ t, s }: { t: T; s: State }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const login = useSyncExternalStore(account.subscribe, account.getSnapshot);
  const seq = login ? login.seq : 0;
  const [now, setNow] = useState(() => Date.now());

  useLayoutEffect(() => {
    const box = dialog.current;
    if (!box) return;
    if (!seq) {
      if (box.open) box.close();
      return;
    }
    if (!box.open) box.showModal();
  }, [seq]);

  useEffect(() => {
    const box = dialog.current;
    if (!box) return;
    const onClose = () => {
      if (!box.open) account.closeLogin();
    };
    box.addEventListener("close", onClose);
    return () => box.removeEventListener("close", onClose);
  }, []);

  const pending = !!login && (!login.view || login.view.state === "pending");
  useEffect(() => {
    if (!pending) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [pending]);

  const cred = login ? credentialsOf(s.status).find((c) => c.auth_index === login.authIndex) : undefined;
  const ended = !!login && ((!login.view && !!login.error) || (!!login.view && login.view.state !== "pending" && login.view.state !== "approved"));
  return (
    <dialog className="modal modal-sm" id="signin-dialog" aria-labelledby="signin-title" ref={dialog}>
      <div className="modal-form">
        <div className="modal-head">
          <div>
            <h2 id="signin-title">{t("signin.title")}</h2>
            <p className="sub">{t("signin.sub", { name: login ? login.name : "" })}</p>
          </div>
          <button className="icon-btn" type="button" aria-label={t("form.close")} title={t("form.close")} onClick={() => account.closeLogin()}>
            ×
          </button>
        </div>
        <div className="modal-body">{login ? <Body t={t} login={login} now={now} /> : null}</div>
        <div className="modal-foot">
          {ended && cred ? (
            <button className="btn btn-secondary" type="button" onClick={() => void account.startLogin(cred)}>
              {t("signin.retry")}
            </button>
          ) : null}
          <button className={"btn " + (login && login.view && login.view.state === "approved" ? "btn-primary" : "btn-secondary")} type="button" onClick={() => account.closeLogin()}>
            {t(pending ? "signin.cancel" : login && login.view && login.view.state === "approved" ? "signin.done" : "signin.close")}
          </button>
        </div>
      </div>
    </dialog>
  );
}
