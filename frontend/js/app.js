import { api, login, logout, hasSession, setAuthLostHandler } from './api.js';
import { h, clear, toast, field, ROLE_LABEL, stateEmpty } from './ui.js';
import { dashboardPage } from './pages/dashboard.js';
import { invoicesPage, newInvoicePage } from './pages/invoices.js';
import { contactsPage, contactDetailPage } from './pages/contacts.js';
import { paymentsPage } from './pages/payments.js';
import { productsPage } from './pages/products.js';
import { reportsPage } from './pages/reports.js';
import { settingsPage } from './pages/settings.js';
import { statementPage } from './pages/statement.js';

const app = document.getElementById('app');
let me = null;
let leaveFns = [];

function can(role) {
  return { owner: role === 'owner', books: role === 'owner' || role === 'accountant', staff: role !== 'customer', customer: role === 'customer' };
}

// ---------- giriş ----------
function showLogin(message) {
  me = null;
  clear(app);
  const err = h('div', { class: 'err' + (message ? '' : ' hidden'), role: 'alert', text: message || '' });
  const email = h('input', { type: 'email', autocomplete: 'username', required: true, placeholder: 'ornek@isletme1.demo' });
  const pass = h('input', { type: 'password', autocomplete: 'current-password', required: true });
  const submit = h('button', { class: 'btn', type: 'submit', text: 'Giriş yap' });
  const biz = h('select', { 'aria-label': 'Demo işletme' }, [1, 2, 3, 4, 5].map((n) => h('option', { value: String(n), text: 'İşletme ' + n })));

  async function doLogin(em, pw) {
    err.classList.add('hidden');
    submit.disabled = true;
    submit.textContent = 'Giriş yapılıyor…';
    try {
      await login(em, pw);
      await boot();
    } catch (e) {
      err.textContent = e.status === 401 ? 'E-posta veya parola hatalı.' : e.message;
      err.classList.remove('hidden');
      submit.disabled = false;
      submit.textContent = 'Giriş yap';
    }
  }
  const demoBtn = (role, text) => h('button', { class: 'btn secondary', type: 'button', onClick: () => doLogin(role + '@isletme' + biz.value + '.demo', 'Demo123!'), text });

  app.appendChild(h('div', { class: 'login' }, h('div', { class: 'card' },
    h('h1', {}, h('span', { class: 'brand' }, h('span', { class: 'logo', text: 'd' })), 'EsnafDefter'),
    h('p', { class: 'muted', text: 'Küçük işletmeler için ön muhasebe, cari ve fatura sistemi' }),
    h('form', { onSubmit: (e) => { e.preventDefault(); doLogin(email.value.trim(), pass.value); } },
      field('E-posta', email), field('Parola', pass), err, submit),
    h('hr'),
    h('strong', { text: 'Hızlı demo girişi' }),
    h('p', { class: 'muted', text: 'Seed edilmiş demo verisi. Her rol yalnızca kendi ekranlarını ve uç noktalarını görür.' }),
    field('İşletme', biz),
    h('div', { class: 'demo' }, demoBtn('owner', 'İşletme sahibi'), demoBtn('accountant', 'Muhasebeci'), demoBtn('employee', 'Çalışan'), demoBtn('customer', 'Müşteri (ekstre)')))));
  email.focus();
}

setAuthLostHandler(() => showLogin('Oturum süresi doldu. Lütfen yeniden giriş yapın.'));

// ---------- yerleşim ----------
const NAV = [
  ['#/pano', 'Pano', (c) => c.staff],
  ['#/faturalar', 'Faturalar', (c) => c.staff],
  ['#/cariler', 'Cariler', (c) => c.staff],
  ['#/odemeler', 'Tahsilat / Ödeme', (c) => c.staff],
  ['#/urunler', 'Ürünler', (c) => c.staff],
  ['#/raporlar', 'Raporlar', (c) => c.books],
  ['#/ayarlar', 'Ayarlar', (c) => c.books],
  ['#/ekstre', 'Hesap ekstrem', (c) => c.customer],
];

function layout() {
  clear(app);
  const c = can(me.role);
  const nav = h('nav', { class: 'nav', 'aria-label': 'Ana menü' }, NAV.filter((n) => n[2](c)).map(([href, text]) => h('a', { href, text })));
  const main = h('main', { id: 'view', tabindex: '-1' });
  app.appendChild(h('div', { class: 'shell' },
    h('header', { class: 'topbar' },
      h('a', { class: 'brand', href: c.customer ? '#/ekstre' : '#/pano' }, h('span', { class: 'logo', text: 'd' }), 'EsnafDefter'),
      h('a', { class: 'apilink', href: '/docs/', target: '_blank', rel: 'noopener', text: 'API belgeleri' }),
      h('div', { class: 'who' }, h('strong', { text: me.business_name }), h('span', { text: ROLE_LABEL[me.role] + ' · ' + me.email })),
      h('button', { class: 'logout', type: 'button', text: 'Çıkış', onClick: async () => { await logout(); showLogin(); } })),
    nav, main));
  return main;
}

// ---------- yönlendirici ----------
function parseHash() {
  const raw = location.hash.replace(/^#/, '') || '/';
  const [path, query] = raw.split('?');
  const q = {};
  new URLSearchParams(query || '').forEach((v, k) => { q[k] = v; });
  return { path, query: q };
}

function navigate(hash) { if (location.hash === hash) route(); else location.hash = hash; }

function route() {
  if (!me) return;
  leaveFns.forEach((f) => { try { f(); } catch { /* yoksay */ } });
  leaveFns = [];
  const c = can(me.role);
  const main = document.getElementById('view');
  if (!main) return;
  clear(main);
  const { path, query } = parseHash();
  const ctx = { me, can: c, query, navigate, onLeave: (f) => leaveFns.push(f) };
  const home = c.customer ? '/ekstre' : '/pano';
  let m;
  const guard = (ok, fn) => { if (!ok) { main.appendChild(stateEmpty('Bu sayfaya erişim yetkiniz yok.', '🔒')); return; } fn(); };

  if (path === '/' || path === '') { location.hash = '#' + home; return; }
  if (path === '/pano') guard(c.staff, () => dashboardPage(main, ctx));
  else if (path === '/faturalar') guard(c.staff, () => invoicesPage(main, ctx));
  else if (path === '/faturalar/yeni') guard(c.staff, () => newInvoicePage(main, ctx));
  else if (path === '/cariler') guard(c.staff, () => contactsPage(main, ctx));
  else if ((m = path.match(/^\/cariler\/([0-9a-f-]{36})$/))) guard(c.staff, () => contactDetailPage(main, ctx, m[1]));
  else if (path === '/odemeler') guard(c.staff, () => paymentsPage(main, ctx));
  else if (path === '/urunler') guard(c.staff, () => productsPage(main, ctx));
  else if ((m = path.match(/^\/raporlar(?:\/(\w+))?$/))) guard(c.books, () => reportsPage(main, ctx, m[1]));
  else if (path === '/ayarlar') guard(c.books, () => settingsPage(main, ctx));
  else if (path === '/ekstre') guard(c.customer, () => statementPage(main, ctx));
  else main.appendChild(stateEmpty('Sayfa bulunamadı.', '🧭'));

  document.querySelectorAll('.nav a').forEach((a) => {
    const base = a.getAttribute('href');
    a.classList.toggle('active', location.hash === base || location.hash.startsWith(base + '/') || location.hash.startsWith(base + '?'));
  });
  window.scrollTo(0, 0);
}

// ---------- açılış ----------
async function boot() {
  if (!hasSession()) { showLogin(); return; }
  try {
    me = await api('GET', '/me');
  } catch (e) {
    showLogin(e.status === 401 || e.status === 0 ? undefined : e.message);
    return;
  }
  layout();
  if (!location.hash || location.hash === '#/' ) location.hash = can(me.role).customer ? '#/ekstre' : '#/pano';
  route();
}

window.addEventListener('hashchange', route);
boot();
