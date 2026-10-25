// Ortak arayüz yardımcıları. Kullanıcı verisi asla innerHTML ile basılmaz (XSS'e karşı yalnızca textContent).

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === 'style') el.style.cssText = v; // CSSOM: CSP style-src 'self' ile uyumlu (satır içi style özniteliği değil)
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (v === true) el.setAttribute(k, '');
    else el.setAttribute(k, v);
  }
  append(el, children);
  return el;
}

export function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

// ---------- biçimlendirme ----------
export function money(kurus) {
  if (kurus === null || kurus === undefined) return '—';
  const neg = kurus < 0;
  const a = Math.abs(Math.round(kurus));
  const lira = String(Math.floor(a / 100)).replace(/\B(?=(\d{3})+(?!\d))/g, '.');
  return (neg ? '-' : '') + lira + ',' + String(a % 100).padStart(2, '0') + ' ₺';
}

// "1.234,56" / "1234,5" / "12.5" -> kuruş (tam sayı). Geçersizse null. Float aritmetiği kullanılmaz.
export function parseMoney(input) {
  if (input === null || input === undefined) return null;
  const s = String(input).replace(/[₺\s]/g, '');
  if (s === '' || !/^[0-9.,]+$/.test(s)) return null;
  let intPart, frac = '';
  if (s.includes(',')) {
    const parts = s.split(',');
    if (parts.length !== 2) return null;
    intPart = parts[0].replace(/\./g, '');
    frac = parts[1];
  } else if (/^\d+\.\d{1,2}$/.test(s)) {
    [intPart, frac] = s.split('.');
  } else {
    intPart = s.replace(/\./g, '');
  }
  if (!/^\d*$/.test(intPart) || !/^\d{0,2}$/.test(frac) || (intPart === '' && frac === '')) return null;
  return Number(intPart || '0') * 100 + Number((frac + '00').slice(0, 2));
}

// "2,5" -> "2.5" (en fazla 3 ondalık); geçersizse null.
export function parseQty(input) {
  const s = String(input || '').trim().replace(',', '.');
  if (!/^\d+(\.\d{1,3})?$/.test(s) || Number(s) <= 0) return null;
  return s;
}

export function fmtQty(milli) {
  const neg = milli < 0;
  const a = Math.abs(milli);
  const whole = Math.floor(a / 1000);
  const frac = String(a % 1000).padStart(3, '0').replace(/0+$/, '');
  return (neg ? '-' : '') + String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, '.') + (frac ? ',' + frac : '');
}

export function fmtDate(iso) {
  if (!iso) return '—';
  const [y, m, d] = String(iso).slice(0, 10).split('-');
  return d + '.' + m + '.' + y;
}

export function fmtTime(iso) {
  const d = new Date(iso);
  return isNaN(d) ? '' : d.toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function fmtDateTime(iso) {
  const d = new Date(iso);
  return isNaN(d) ? '' : d.toLocaleString('tr-TR', { dateStyle: 'short', timeStyle: 'short' });
}

export function todayISO() { return new Date().toISOString().slice(0, 10); }
export function addDays(iso, n) {
  const d = new Date(iso + 'T00:00:00Z');
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

// ---------- bildirim ----------
export function toast(message, type) {
  const box = document.getElementById('toasts');
  const t = h('div', { class: 'toast' + (type === 'error' ? ' error' : ''), text: message });
  box.appendChild(t);
  setTimeout(() => t.remove(), type === 'error' ? 7000 : 4000);
}

// ---------- durum bileşenleri ----------
export function skeleton() {
  return h('div', { class: 'skeleton', role: 'status', 'aria-label': 'Yükleniyor' }, h('i'), h('i'), h('i'), h('i'));
}

export function stateEmpty(text, icon) {
  return h('div', { class: 'state' }, h('span', { class: 'big', 'aria-hidden': 'true', text: icon || '📭' }), h('div', { text }));
}

export function stateError(err, retry) {
  const wrap = h('div', { class: 'state error', role: 'alert' },
    h('span', { class: 'big', 'aria-hidden': 'true', text: '⚠️' }),
    h('div', { text: err && err.message ? err.message : 'Bir şeyler ters gitti.' }));
  if (retry) wrap.appendChild(h('button', { class: 'btn secondary', type: 'button', onClick: retry, text: 'Tekrar dene' }));
  return wrap;
}

// loadInto: yükleniyor -> (boş | hata | içerik) akışını tek yerde yönetir.
export async function loadInto(container, loader, render, opts = {}) {
  const run = async () => {
    clear(container);
    container.appendChild(skeleton());
    try {
      const data = await loader();
      clear(container);
      if (opts.isEmpty && opts.isEmpty(data)) container.appendChild(stateEmpty(opts.emptyText || 'Kayıt bulunamadı.', opts.emptyIcon));
      else render(data, container);
      return data;
    } catch (e) {
      clear(container);
      container.appendChild(stateError(e, run));
      return null;
    }
  };
  return run();
}

// ---------- tablo ----------
// columns: [{label, render(row) -> Node|string, cls, key}]
export function table(columns, rows, opts = {}) {
  const thead = h('thead', {}, h('tr', {}, columns.map((c) => h('th', { class: c.cls || '', scope: 'col', text: c.label }))));
  const tbody = h('tbody');
  for (const r of rows) {
    const tr = h('tr', opts.onRow ? { class: 'click', tabindex: '0', role: 'button' } : {});
    if (opts.rowClass) { const rc = opts.rowClass(r); if (rc) tr.classList.add(...rc.split(' ')); }
    for (const c of columns) {
      const content = c.render ? c.render(r) : r[c.key];
      tr.appendChild(h('td', { class: c.cls || '', dataset: { label: c.label } }, content));
    }
    if (opts.onRow) {
      tr.addEventListener('click', () => opts.onRow(r));
      tr.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); opts.onRow(r); } });
    }
    tbody.appendChild(tr);
  }
  const children = [thead, tbody];
  if (opts.footer) children.push(h('tfoot', {}, h('tr', {}, opts.footer.map((c) => h('td', { class: c.cls || '', dataset: { label: c.label || '' } }, c.content)))));
  return h('div', { class: 'table-wrap' }, h('table', { class: 'cards' }, children));
}

export function badge(text, kind) { return h('span', { class: 'badge ' + (kind || 'gray'), text }); }

// ---------- modal ----------
export function openModal({ title, body, footer, wide, onClose }) {
  const prev = document.activeElement;
  const overlay = h('div', { class: 'overlay', role: 'presentation' });
  const modal = h('div', { class: 'modal' + (wide ? ' wide' : ''), role: 'dialog', 'aria-modal': 'true', 'aria-label': title });
  const close = () => {
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    document.body.style.overflow = '';
    if (prev && prev.focus) prev.focus();
    if (onClose) onClose();
  };
  const onKey = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', onKey);
  overlay.addEventListener('mousedown', (e) => { if (e.target === overlay) close(); });
  const bodyEl = h('div', { class: 'modal-body' }, body);
  const footEl = h('div', { class: 'modal-foot' }, footer || []);
  modal.append(
    h('div', { class: 'modal-head' }, h('h2', { text: title }), h('button', { class: 'x', type: 'button', 'aria-label': 'Kapat', onClick: close, text: '×' })),
    bodyEl, footEl);
  if (!footer || footer.length === 0) footEl.remove();
  overlay.appendChild(modal);
  document.body.appendChild(overlay);
  document.body.style.overflow = 'hidden';
  const first = modal.querySelector('input:not([type=hidden]), select, textarea, button.btn');
  if (first) first.focus();
  return { close, body: bodyEl, foot: footEl, modal };
}

export function confirmDialog(message, { confirmText = 'Onayla', danger = false } = {}) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (v) => { if (!done) { done = true; m.close(); resolve(v); } };
    const m = openModal({
      title: 'Emin misiniz?',
      body: h('p', { text: message }),
      footer: [
        h('button', { class: 'btn secondary', type: 'button', onClick: () => finish(false), text: 'Vazgeç' }),
        h('button', { class: 'btn' + (danger ? ' danger' : ''), type: 'button', onClick: () => finish(true), text: confirmText }),
      ],
      onClose: () => finish(false),
    });
  });
}

// ---------- arama kutusu (combo): yazdıkça sunucudan öneri getirir ----------
export function combo({ label, placeholder, search, describe, onPick, required }) {
  let picked = null;
  let timer = null;
  let seq = 0;
  const input = h('input', { type: 'text', placeholder: placeholder || 'Aramak için yazın…', autocomplete: 'off', role: 'combobox', 'aria-expanded': 'false', 'aria-label': label });
  const list = h('ul', { class: 'hidden', role: 'listbox' });
  const pill = h('div', { class: 'picked hidden' });
  const root = h('div', { class: 'combo' }, input, list, pill);
  let items = [];
  let active = -1;

  function hide() { list.classList.add('hidden'); input.setAttribute('aria-expanded', 'false'); active = -1; }
  function paint() {
    clear(list);
    items.forEach((it, i) => {
      const d = describe(it);
      const li = h('li', { role: 'option', 'aria-selected': i === active ? 'true' : 'false' }, d.title, d.sub ? h('br') : null, d.sub ? h('small', { text: d.sub }) : null);
      li.addEventListener('mousedown', (e) => { e.preventDefault(); pick(it); });
      list.appendChild(li);
    });
    if (items.length === 0) list.appendChild(h('li', { class: 'muted', text: 'Sonuç yok' }));
    list.classList.remove('hidden');
    input.setAttribute('aria-expanded', 'true');
  }
  function pick(it) {
    clearTimeout(timer);
    seq++; // yoldaki (bitmemiş) sorgular artık listeyi yeniden açmasın
    picked = it;
    const d = describe(it);
    clear(pill);
    pill.append(h('span', {}, h('strong', { text: d.title }), d.sub ? h('div', { class: 'muted', text: d.sub }) : null),
      h('button', { class: 'btn secondary small', type: 'button', onClick: reset, text: 'Değiştir' }));
    pill.classList.remove('hidden');
    input.classList.add('hidden');
    hide();
    if (onPick) onPick(it);
  }
  function reset() {
    picked = null;
    pill.classList.add('hidden');
    input.classList.remove('hidden');
    input.value = '';
    input.focus();
    if (onPick) onPick(null);
  }
  async function query() {
    const my = ++seq;
    try {
      const res = await search(input.value.trim());
      if (my !== seq) return;
      items = res;
      active = -1;
      paint();
    } catch { items = []; paint(); }
  }
  input.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(query, 250); });
  input.addEventListener('focus', () => { if (!picked) query(); });
  input.addEventListener('blur', () => setTimeout(hide, 120));
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(active + 1, items.length - 1); paint(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(active - 1, 0); paint(); }
    else if (e.key === 'Enter' && active >= 0) { e.preventDefault(); pick(items[active]); }
    else if (e.key === 'Escape') hide();
  });
  return {
    el: root,
    get value() { return picked; },
    set(it) { pick(it); },
    required,
  };
}

export function field(labelText, control, hint) {
  const id = 'f' + Math.random().toString(36).slice(2, 9);
  if (control instanceof HTMLElement && ['INPUT', 'SELECT', 'TEXTAREA'].includes(control.tagName)) control.id = id;
  return h('div', { class: 'field' }, h('label', { for: control instanceof HTMLElement ? id : undefined, text: labelText }), control, hint ? h('div', { class: 'muted', text: hint }) : null);
}

export function seg(options, value, onChange) {
  const wrap = h('div', { class: 'seg', role: 'group' });
  const paint = (v) => wrap.querySelectorAll('button').forEach((b) => b.setAttribute('aria-pressed', b.dataset.v === v ? 'true' : 'false'));
  for (const [v, text] of options) {
    wrap.appendChild(h('button', { type: 'button', dataset: { v }, 'aria-pressed': v === value ? 'true' : 'false', text, onClick: () => { paint(v); onChange(v); } }));
  }
  return wrap;
}

export const ACCOUNTS = {
  100: 'Kasa', 102: 'Bankalar', 120: 'Alıcılar', 153: 'Ticari Mallar', 159: 'Verilen Sipariş Avansları',
  191: 'İndirilecek KDV', 320: 'Satıcılar', 340: 'Alınan Sipariş Avansları', 391: 'Hesaplanan KDV',
  500: 'Sermaye', 600: 'Yurtiçi Satışlar', 621: 'Satılan Ticari Mallar Maliyeti', 770: 'Genel Yönetim Giderleri',
};

export const ROLE_LABEL = { owner: 'İşletme sahibi', accountant: 'Muhasebeci', employee: 'Çalışan', customer: 'Müşteri' };
