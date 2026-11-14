// JSON shapes served by the plugin's management API. Field names match the Go
// structs' json tags; optional fields are the ones tagged omitempty.

export interface PluginConfigView {
  base_url: string;
  default_prefix: string;
  /** Mirrors CPA's routing.force-model-prefix; absent when unknown. */
  force_model_prefix?: boolean;
  models_refresh_minutes: number;
  request_timeout_seconds: number;
  stream_idle_timeout_seconds: number;
  global_proxy?: string;
}

export interface CatalogMeta {
  count: number;
  /** Keys whose model lists the catalog holds; the catalog is their union. */
  keys: number;
  revision?: string;
  fetched_at?: string;
  checked_at?: string;
  last_error?: string;
}

export interface CredentialView {
  auth_index: string;
  id: string;
  name: string;
  label: string;
  key_masked: string;
  /** Names the key in ModelView.keys without revealing it. */
  key_id?: string;
  /** Chat models the key's plan lists; absent while its list is not loaded. */
  listed_models?: number;
  disabled: boolean;
  unavailable: boolean;
  status?: string;
  status_message?: string;
  priority: number;
  note?: string;
  base_url?: string;
  proxy_url?: string;
  /** Set when proxy_url is exactly the address of a proxy in the list. */
  proxy_id?: string;
  proxy_name?: string;
  /** null means the plugin's default prefix applies. */
  prefix: string | null;
  effective_prefix: string;
  all_models: boolean;
  models: string[] | null;
  missing_models?: string[];
  registered_models: number;
  registered_known: boolean;
  success: number;
  failed: number;
  read_error?: string;
  /** The OpenDesign account signed in on the card, if any. */
  account?: AccountLink;
}

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

export interface StatusView {
  plugin: string;
  version: string;
  now: string;
  config: PluginConfigView;
  catalog: CatalogMeta;
  credentials: CredentialView[];
  list_error?: string;
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

export interface ModelsResponse {
  catalog: CatalogMeta;
  models: ModelView[];
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

/** Body of POST (add) and PATCH (edit) /credentials. */
export interface CredentialSaveRequest {
  auth_index?: string;
  name?: string;
  api_key?: string;
  skip_verify?: boolean;
  label?: string;
  /** "" removes the note. */
  note?: string;
  proxy_url?: string;
  /** Uses the address of this list proxy; wins over proxy_url. */
  proxy_id?: string;
  /** null resets to the plugin default. */
  prefix?: string | null;
  /** null means all models. */
  models?: string[] | null;
  /** null clears the priority. */
  priority?: number | null;
}

export interface CredentialSaveResponse {
  ok: boolean;
  credential?: CredentialView;
  unknown_models?: string[];
}

export interface LogEntry {
  id: string;
  request_id?: string;
  time: string;
  endpoint: string;
  model: string;
  upstream_model?: string;
  served_model?: string;
  reasoning_effort?: string;
  stream: boolean;
  status: string;
  http_status?: number;
  credential?: string;
  auth_id?: string;
  duration_ms: number;
  ttft_ms?: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
  /** Generation speed: (output − 1) ÷ (total − TTFT); streams only. */
  output_tokens_per_second?: number;
  /** End-to-end speed: output ÷ total, the wait for the first token included. */
  e2e_tokens_per_second?: number;
  error_code?: string;
  error?: string;
  error_phase?: string;
  // Absent when CPA did not report them.
  client_path?: string;
  user_agent?: string;
  auth_index?: string;
  /** "direct", "env" or the proxy as scheme://host:port. */
  egress?: string;
  /** What OpenDesign answered; 0 or absent when no response arrived. */
  upstream_status?: number;
  upstream_request_id?: string;
  retry_after?: number;
  finish_reason?: string;
  frames?: number;
  last_frame_ms?: number;
  /** Set only once OpenDesign answered 2xx: whether the response carried usage. */
  usage_reported?: boolean;
  /** How many entries share the request ID (credentials CPA tried). */
  attempts?: number;
}

export interface LogSummary {
  requests: number;
  success: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_hit_rate: number | null;
  /** Medians of the two speeds over the successful requests. */
  output_tokens_per_second: number | null;
  e2e_tokens_per_second: number | null;
}

export interface LogPage {
  items: LogEntry[];
  total: number;
  offset: number;
  retention: number;
  retention_days: number;
  summary: LogSummary;
  error?: string;
}

/** A saved proxy; url has its password redacted. */
export interface ProxyView {
  id: string;
  name: string;
  url: string;
  /** scheme://host:port */
  label: string;
  created_at?: string;
  /** The latest check of the current address, saved by the plugin. */
  last_check?: ProxyCheckResult;
}

export interface ProxiesResponse {
  proxies: ProxyView[];
  /** CPA's global proxy-url, redacted; absent when not set. */
  global_proxy?: string;
  /** The latest checks of the two built-in rows. */
  builtin_checks?: Partial<Record<"direct" | "inherit", ProxyCheckResult>>;
  load_error?: string;
}

/** Body of POST (add, without id) and PATCH (edit) /proxies. */
export interface ProxySaveRequest {
  id?: string;
  name?: string;
  /** Absent when editing keeps the saved address. */
  url?: string;
}

export interface ProxySaveResponse {
  ok: boolean;
  proxy?: ProxyView;
  /** Keys moved to the new address when the address changed. */
  updated_credentials?: number;
  failed_credentials?: string[];
}

export interface ProxyImportResponse {
  ok: boolean;
  added: number;
  proxies: ProxyView[];
}

/** Response of POST /proxies/order: the list in its saved order. */
export interface ProxyOrderResponse {
  ok: boolean;
  proxies: ProxyView[];
}

export type ProxyCheckTarget = { id: string } | { target: "direct" | "inherit" };

/** An egress check; a failed check is ok: false with error, still HTTP 200. */
export interface ProxyCheckResult {
  ok: boolean;
  /** "direct", "env" or the proxy as scheme://host:port. */
  egress?: string;
  ip?: string;
  /** ISO 3166-1 alpha-2 code when the lookup service gave one. */
  country?: string;
  region?: string;
  city?: string;
  latency_ms?: number;
  source?: string;
  checked_at?: string;
  error?: string;
}

export interface ErrorResponse {
  error?: string;
  message?: string;
}
