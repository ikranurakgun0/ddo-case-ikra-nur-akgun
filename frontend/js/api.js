// API istemcisi: erişim belirteci, otomatik yenileme (refresh) ve tutarlı hata nesnesi.
const BASE = '/api';
const KEY = 'ed_tokens';

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || 'Beklenmeyen bir hata oluştu');
    this.status = status;
    this.code = code;
  }
}

let tokens = null;
try { tokens = JSON.parse(sessionStorage.getItem(KEY) || 'null'); } catch { tokens = null; }
let onAuthLost = () => {};
let refreshing = null;

export function setAuthLostHandler(fn) { onAuthLost = fn; }
export function hasSession() { return !!(tokens && tokens.access); }

function save(t) {
  tokens = t;
  try { t ? sessionStorage.setItem(KEY, JSON.stringify(t)) : sessionStorage.removeItem(KEY); } catch { /* özel mod */ }
}

export function uuid() {
  if (globalThis.crypto && crypto.randomUUID) return crypto.randomUUID();
  return 'k-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 12);
}

async function raw(method, path, body, headers, auth) {
  const h = { ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...(headers || {}) };
  if (auth && tokens && tokens.access) h.Authorization = 'Bearer ' + tokens.access;
  let res;
  try {
    res = await fetch(BASE + path, { method, headers: h, body: body !== undefined ? JSON.stringify(body) : undefined });
  } catch {
    throw new ApiError(0, 'network', 'Sunucuya ulaşılamıyor. Bağlantınızı kontrol edip tekrar deneyin.');
  }
  const text = await res.text();
  let data = null;
  if (text) { try { data = JSON.parse(text); } catch { data = null; } }
  return { res, data };
}

async function refresh() {
  if (!tokens || !tokens.refresh) return false;
  if (!refreshing) {
    refreshing = (async () => {
      const { res, data } = await raw('POST', '/auth/refresh', { refresh_token: tokens.refresh }, null, false);
      if (!res.ok) { save(null); return false; }
      save({ access: data.access_token, refresh: data.refresh_token });
      return true;
    })().finally(() => { refreshing = null; });
  }
  return refreshing;
}

// api istek atar; 401'de bir kez belirteci yeniler. Hata durumunda ApiError fırlatır.
export async function api(method, path, body, opts = {}) {
  let { res, data } = await raw(method, path, body, opts.headers, true);
  if (res.status === 401 && !opts.noRetry && tokens && tokens.refresh) {
    if (await refresh()) ({ res, data } = await raw(method, path, body, opts.headers, true));
    else { onAuthLost(); throw new ApiError(401, 'session_expired', 'Oturum süresi doldu. Lütfen yeniden giriş yapın.'); }
  }
  if (!res.ok) {
    const e = data && data.error ? data.error : {};
    throw new ApiError(res.status, e.code, e.message || ('İstek başarısız (' + res.status + ')'));
  }
  return data;
}

export async function login(email, password) {
  const { res, data } = await raw('POST', '/auth/login', { email, password }, null, false);
  if (!res.ok) {
    const e = data && data.error ? data.error : {};
    throw new ApiError(res.status, e.code, e.message || 'Giriş başarısız');
  }
  save({ access: data.access_token, refresh: data.refresh_token });
  return data;
}

export async function logout() {
  const r = tokens && tokens.refresh;
  save(null);
  if (r) { try { await raw('POST', '/auth/logout', { refresh_token: r }, null, false); } catch { /* yoksay */ } }
}

export function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) if (v !== undefined && v !== null && v !== '') p.set(k, v);
  const s = p.toString();
  return s ? '?' + s : '';
}
