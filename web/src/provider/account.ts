// Signing an OpenDesign account in and out on a key card. The sign-in dialog
// has its own small store; it uses the shared store for requests, notices,
// the two-click confirm and reloading the cards.

import { errorMessage, errorStatus, isAuthError } from "@core/api/client";
import type { CredentialView } from "@core/api/types";
import { displayName } from "@core/lib/credentials";
import { store } from "@core/state/store";
import type { LoginView, LogoutResult } from "./types";

/** How often an open sign-in dialog asks whether the user confirmed. */
const LOGIN_POLL_MS = 2000;

/** The account sign-in dialog of a key card. */
export interface AccountLoginState {
  seq: number;
  authIndex: string;
  /** The card's display name. */
  name: string;
  /** null while the sign-in is being started. */
  view: LoginView | null;
  /** Why starting or checking the sign-in failed. */
  error: string | null;
}

const api = {
  startLogin: (authIndex: string) => store.api.request<LoginView>("/account/login", { method: "POST", body: { auth_index: authIndex } }),
  loginStatus: (id: string) => store.api.request<LoginView>("/account/login?id=" + encodeURIComponent(id)),
  cancelLogin: (id: string) => store.api.request<LoginView>("/account/login?id=" + encodeURIComponent(id), { method: "DELETE" }),
  logout: (authIndex: string) => store.api.request<LogoutResult>("/account?auth_index=" + encodeURIComponent(authIndex), { method: "DELETE" }),
};

class AccountStore {
  /** The open sign-in dialog; null while closed. */
  login: AccountLoginState | null = null;
  private listeners = new Set<() => void>();
  private loginSeq = 0;
  private loginTimer: ReturnType<typeof setTimeout> | null = null;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): AccountLoginState | null => this.login;

  private set(login: AccountLoginState | null): void {
    this.login = login;
    for (const listener of this.listeners) listener();
  }

  private loginOpen(seq: number): boolean {
    return !!this.login && this.login.seq === seq;
  }

  private patchLogin(seq: number, patch: Partial<AccountLoginState>): void {
    if (this.login && this.login.seq === seq) this.set({ ...this.login, ...patch });
  }

  private stopLoginPoll(): void {
    if (this.loginTimer) clearTimeout(this.loginTimer);
    this.loginTimer = null;
  }

  /**
   * Signs an OpenDesign account in on a card: the plugin starts a device
   * authorization and the dialog shows its link and code until the user
   * confirms on the website.
   */
  async startLogin(cred: CredentialView): Promise<void> {
    if (!cred.auth_index) return;
    this.stopLoginPoll();
    const previous = this.login;
    if (previous && previous.view && previous.view.state === "pending") void api.cancelLogin(previous.view.id).catch(() => {});
    const seq = ++this.loginSeq;
    this.set({ seq, authIndex: cred.auth_index, name: displayName(cred), view: null, error: null });
    try {
      const view = await api.startLogin(cred.auth_index);
      if (!this.loginOpen(seq)) {
        void api.cancelLogin(view.id).catch(() => {});
        return;
      }
      this.patchLogin(seq, { view });
      this.scheduleLoginPoll(seq);
    } catch (error) {
      if (isAuthError(error)) {
        this.set(null);
        store.showLogin("login.expired", errorMessage(error));
        return;
      }
      this.patchLogin(seq, { error: errorMessage(error) });
    }
  }

  private scheduleLoginPoll(seq: number): void {
    this.stopLoginPoll();
    this.loginTimer = setTimeout(() => void this.pollLogin(seq), LOGIN_POLL_MS);
  }

  private async pollLogin(seq: number): Promise<void> {
    const login = this.login;
    if (!login || login.seq !== seq || !login.view) return;
    try {
      const view = await api.loginStatus(login.view.id);
      if (!this.loginOpen(seq)) return;
      this.patchLogin(seq, { view, error: null });
      if (view.state === "pending") {
        this.scheduleLoginPoll(seq);
        return;
      }
      if (view.state === "approved") {
        store.notify("ok", "log.accountSignedIn", { name: login.name, email: view.email || "" });
        // Dropping the old figures makes the refresh load them anew, windows included.
        store.setQuota(login.authIndex, undefined);
        await store.refresh(true);
      }
    } catch (error) {
      if (!this.loginOpen(seq)) return;
      if (isAuthError(error)) {
        this.set(null);
        store.showLogin("login.expired", errorMessage(error));
        return;
      }
      this.patchLogin(seq, { error: errorMessage(error) });
      // The plugin forgot the sign-in (it restarted): stop asking.
      if (errorStatus(error) !== 404) this.scheduleLoginPoll(seq);
    }
  }

  /** Closes the sign-in dialog; a sign-in still waiting is cancelled. */
  closeLogin(): void {
    const login = this.login;
    this.stopLoginPoll();
    if (!login) return;
    this.set(null);
    if (login.view && login.view.state === "pending") void api.cancelLogin(login.view.id).catch(() => {});
  }

  /** Signs the account out of a card; like deleting, it takes two clicks. */
  async logout(cred: CredentialView): Promise<void> {
    if (!cred.auth_index || !cred.name) return;
    if (!store.confirmTwice("logout:" + cred.name)) return;
    const busy = "logout:" + cred.name;
    store.setBusy(busy, true);
    try {
      const result = await api.logout(cred.auth_index);
      if (result.revoked) store.notify("ok", "log.accountSignedOut", { name: displayName(cred) });
      else store.notify("warn", "log.accountSignOutUnrevoked", { name: displayName(cred), msg: result.revoke_error || "" });
      store.setQuota(cred.auth_index, undefined);
      await store.refresh(true);
    } catch (error) {
      store.handleError(error);
    } finally {
      store.setBusy(busy, false);
    }
  }
}

export const account = new AccountStore();
