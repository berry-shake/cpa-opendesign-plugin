// The OpenDesign shapes of the management API: the catalog model with the
// keys whose plans list it, a key's usage from the request log, and the
// account signed in on a key.

/** A signed-in OpenDesign account; its key never reaches the page. */
export interface AccountLink {
  email?: string;
  name?: string;
  workspace_name?: string;
  logged_in_at?: string;
}

/** One coding plan window of the account, in credits. */
export interface PlanWindow {
  /** "5h", "7d" or "<seconds>s". */
  kind: string;
  duration_seconds: number;
  used: number;
  limit: number;
  remaining: number;
  window_start?: string;
  resets_at?: string;
}

/** The account's plan windows and balance, read with its login. */
export interface AccountUsage {
  email?: string;
  workspace_name?: string;
  /** go, plus, pro or max. */
  tier?: string;
  eligible: boolean;
  funding?: string;
  balance_usd?: string;
  windows: PlanWindow[];
  subscription_status?: string;
  period_end?: string;
  cancel_at_period_end?: boolean;
  fetched_at?: string;
  error?: string;
  /** The login expired or was revoked. */
  expired?: boolean;
}

export type LoginState = "pending" | "approved" | "denied" | "expired" | "cancelled" | "failed";

/** A sign-in started from a key card. */
export interface LoginView {
  id: string;
  auth_index: string;
  state: LoginState;
  user_code?: string;
  activation_url?: string;
  expires_at?: string;
  email?: string;
  message?: string;
}

export interface LogoutResult {
  ok: boolean;
  revoked: boolean;
  revoke_error?: string;
}

/** An OpenDesign model from /v1/models of one or more keys. */
export interface ModelView {
  id: string;
  /** False for models that do not generate text; "all models" leaves them out. */
  chat: boolean;
  enabled: boolean;
  default?: boolean;
  /** Accepted inputs: text, image, file, audio, video. */
  inputs?: string[];
  outputs?: string[];
  context_length?: number;
  /** What one request may use of the context window. */
  context_budget?: number;
  max_output?: number;
  /** low / medium / high. */
  cost_tier?: string;
  /** standard, top, … */
  capability?: string;
  /** key_id of every credential whose plan lists the model. */
  keys: string[];
}

/** One credential's logged requests over a period. */
export interface UsageWindow {
  requests: number;
  failed: number;
  /** Includes cached_tokens. */
  input_tokens: number;
  cached_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
}

/** A credential's usage summed from the plugin's request log. */
export interface QuotaView {
  five_hours: UsageWindow;
  day: UsageWindow;
  week: UsageWindow;
  month: UsageWindow;
  /** When the oldest entry of the log was made; absent for an empty log. */
  log_since?: string;
  fetched_at: string;
  /** The signed-in account's plan windows. */
  account?: AccountUsage;
  /** Why the log figures are missing while the account's windows show. */
  log_error?: string;
}

export interface CredentialExtra {
  /** Names the key in ModelView.keys without revealing it. */
  key_id?: string;
  /** Chat models the key's plan lists; absent while its list is not loaded. */
  listed_models?: number;
  /** The OpenDesign account signed in on the card, if any. */
  account?: AccountLink;
}

export interface CatalogExtra {
  /** Keys whose model lists the catalog holds; the catalog is their union. */
  keys: number;
}

export interface CredentialSaveExtra {}

export interface EditorExtra {
  /**
   * The key being edited, for listing only the models of its plan; absent
   * when adding or while its list is not loaded.
   */
  scope?: Pick<CredentialExtra, "key_id" | "listed_models">;
}
