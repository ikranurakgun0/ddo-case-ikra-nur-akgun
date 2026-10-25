import { api, qs, uuid } from '../api.js';
import { h, clear, money, parseMoney, parseQty, fmtQty, fmtDate, fmtDateTime, todayISO, addDays, toast, badge, table,
  loadInto, openModal, confirmDialog, combo, field, seg, ACCOUNTS } from '../ui.js';
import { pagedList } from '../paged.js';
import { renderAudit } from './auditview.js';

export function invoiceStatus(inv) {
  if (inv.status === 'cancelled') return badge('İptal', 'gray');
  if (inv.open === 0) return badge('Kapandı', 'ok');
  if (inv.paid > 0) return badge('Kısmi ödendi', 'warn');
  if (inv.due_date < todayISO()) return badge('Vadesi geçti', 'danger');
  return badge('Açık', 'info');
}

export const kindBadge = (k) => badge(k === 'sale' ? 'Satış' : 'Alış', k === 'sale' ? 'info' : 'gray');

export function invoiceTable(items, onRow, { hideContact } = {}) {
  const cols = [
    { label: 'Numara', render: (r) => h('span', { class: 'mono', text: r.number }) },
    { label: 'Tür', render: (r) => kindBadge(r.kind) },
    { label: 'Tarih', cls: 'nw', render: (r) => fmtDate(r.issue_date) },
    hideContact ? null : { label: 'Cari', render: (r) => r.contact_name },
    { label: 'Vade', cls: 'nw', render: (r) => fmtDate(r.due_date) },
    { label: 'Toplam', cls: 'num', render: (r) => money(r.gross_total) },
    { label: 'Açık tutar', cls: 'num', render: (r) => money(r.open) },
    { label: 'Durum', render: (r) => invoiceStatus(r) },
  ].filter(Boolean);
  return table(cols, items, { onRow });
}

// ---------- liste ----------
export function invoicesPage(root, ctx) {
  const filters = { kind: '', status: '' };
  const listBox = h('div', { class: 'card' });
  let list;
  const reload = () => list.reload();

  const head = h('div', { class: 'page-head' },
    h('div', {}, h('h1', { text: 'Faturalar' }), h('p', { class: 'subtle', text: 'Satış ve alış faturaları, yeniden eskiye' })),
    h('div', { class: 'actions' }, h('a', { class: 'btn', href: '#/faturalar/yeni', text: '+ Yeni fatura' })));
  const bar = h('div', { class: 'toolbar' },
    seg([['', 'Tümü'], ['sale', 'Satış'], ['purchase', 'Alış']], '', (v) => { filters.kind = v; reload(); }),
    seg([['', 'Hepsi'], ['issued', 'Geçerli'], ['cancelled', 'İptal']], '', (v) => { filters.status = v; reload(); }));
  root.append(head, bar, listBox);
  list = pagedList(listBox, {
    fetchPage: (after) => api('GET', '/invoices' + qs({ limit: 25, after, kind: filters.kind, status: filters.status })),
    render: (items) => invoiceTable(items, (r) => openInvoice(r.id, ctx, reload)),
    emptyText: 'Bu süzgeçle eşleşen fatura yok.',
  });
}

// ---------- detay ----------
export function openInvoice(id, ctx, onChange) {
  const box = h('div');
  const foot = h('div');
  const m = openModal({ title: 'Fatura', body: box, wide: true });
  m.foot.remove();
  const load = () => loadInto(box, () => api('GET', '/invoices/' + id), (inv) => render(inv));

  function render(inv) {
    clear(box);
    box.append(
      h('div', { class: 'page-head' },
        h('div', {}, h('h1', { class: 'mono', text: inv.number }),
          h('p', { class: 'subtle' }, kindBadge(inv.kind), ' ', invoiceStatus(inv))),
        h('div', { class: 'actions' },
          ctx.can.books && inv.status === 'issued' ? h('button', { class: 'btn danger', type: 'button', onClick: () => cancel(inv), text: 'İptal et' }) : null,
          ctx.can.books ? h('button', { class: 'btn secondary', type: 'button', onClick: () => journal(inv), text: 'Yevmiye kayıtları' }) : null,
          ctx.can.books ? h('button', { class: 'btn secondary', type: 'button', onClick: () => renderAudit(box.querySelector('.jbox'), 'invoice', id), text: 'Denetim izi' }) : null)),
      h('div', { class: 'row' },
        info('Cari', h('a', { href: '#/cariler/' + inv.contact_id, onClick: () => m.close(), text: inv.contact_name })),
        info('Fatura tarihi', fmtDate(inv.issue_date)),
        info('Vade', fmtDate(inv.due_date)),
        info('Fiyatlar', inv.prices_include_vat ? 'KDV dahil girildi' : 'KDV hariç girildi')),
      h('h3', { text: 'Kalemler' }),
      table([
        { label: 'Açıklama', render: (l) => l.description },
        { label: 'Miktar', cls: 'num', render: (l) => fmtQty(l.quantity_milli) },
        { label: 'Birim fiyat', cls: 'num', render: (l) => money(l.unit_price) },
        { label: 'KDV', cls: 'num', render: (l) => '%' + l.vat_rate },
        { label: 'Net', cls: 'num', render: (l) => money(l.net) },
        { label: 'KDV tutarı', cls: 'num', render: (l) => money(l.vat) },
        { label: 'Brüt', cls: 'num', render: (l) => money(l.gross) },
      ], inv.lines || []),
      h('div', { class: 'totals' },
        inv.general_discount > 0 ? row('Genel iskonto', money(inv.general_discount)) : null,
        row('Net toplam', money(inv.net_total)),
        row('KDV', money(inv.vat_total)),
        h('div', { class: 'grand' }, h('span', { text: 'Genel toplam' }), h('span', { text: money(inv.gross_total) })),
        row('Mahsup edilen', money(inv.paid)),
        row('Kalan', money(inv.open))),
      h('div', { class: 'jbox' }));
  }
  const info = (k, v) => h('div', { class: 'field' }, h('div', { class: 'muted', text: k }), h('div', {}, v));
  const row = (k, v) => h('div', {}, h('span', { text: k }), h('span', { text: v }));

  async function cancel(inv) {
    const ok = await confirmDialog(inv.number + ' numaralı faturayı iptal etmek istiyor musunuz? Fatura silinmez; yevmiye kaydı ters kayıtla kapatılır.', { confirmText: 'Faturayı iptal et', danger: true });
    if (!ok) return;
    try {
      await api('POST', '/invoices/' + id + '/cancel', {});
      toast('Fatura iptal edildi');
      if (onChange) onChange();
      load();
    } catch (e) { toast(e.message, 'error'); }
  }

  async function journal(inv) {
    const jb = box.querySelector('.jbox');
    clear(jb);
    await loadInto(jb, () => api('GET', '/invoices/' + id + '/journal'), (d) => {
      jb.appendChild(h('h3', { text: 'Yevmiye kayıtları' }));
      for (const e of d.entries) {
        jb.appendChild(h('div', { class: 'card' },
          h('div', {}, h('strong', { text: e.description }), ' ', h('span', { class: 'muted', text: fmtDate(e.date) }), ' ', e.source_type === 'reversal' ? badge('Ters kayıt', 'warn') : null),
          table([
            { label: 'Hesap', render: (l) => l.account + ' · ' + (ACCOUNTS[l.account] || '') },
            { label: 'Borç', cls: 'num', render: (l) => (l.debit ? money(l.debit) : '') },
            { label: 'Alacak', cls: 'num', render: (l) => (l.credit ? money(l.credit) : '') },
          ], e.lines)));
      }
    });
  }
  load();
  return m;
}

// ---------- yeni fatura ----------
export function newInvoicePage(root, ctx) {
  let kind = ctx.query.kind === 'purchase' ? 'purchase' : 'sale';
  const host = h('div');
  root.appendChild(host);
  const key = uuid(); // aynı form tekrar gönderilirse tek fatura oluşur (Idempotency-Key)

  function build() {
    clear(host);
    const err = h('div', { class: 'err hidden', role: 'alert' });
    const contact = combo({
      label: 'Cari', placeholder: kind === 'sale' ? 'Müşteri adı yazın…' : 'Tedarikçi adı yazın…',
      search: async (q) => (await api('GET', '/contacts' + qs({ q, kind: kind === 'sale' ? 'customer' : 'supplier', limit: 8 }))).items,
      describe: (c) => ({ title: c.name, sub: c.tax_no ? 'Vergi no: ' + c.tax_no : '' }),
    });
    if (ctx.query.contact) {
      api('GET', '/contacts/' + ctx.query.contact).then((c) => contact.set(c)).catch(() => {});
    }
    const issue = h('input', { type: 'date', value: todayISO(), required: true });
    const due = h('input', { type: 'date', value: addDays(todayISO(), 30), required: true });
    const incl = h('input', { type: 'checkbox' });
    const gdisc = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0,00' });
    const lines = [];
    const linesBox = h('div', { class: 'lines' });

    function addLine() {
      const l = lineEditor(kind, () => { lines.splice(lines.indexOf(l), 1); l.el.remove(); renumber(); });
      lines.push(l);
      linesBox.appendChild(l.el);
      renumber();
    }
    const renumber = () => lines.forEach((l, i) => l.setNo(i + 1, lines.length > 1));
    addLine();

    const submit = h('button', { class: 'btn', type: 'submit', text: 'Faturayı kes' });
    const form = h('form', { novalidate: true, onSubmit: async (e) => {
      e.preventDefault();
      err.classList.add('hidden');
      try {
        if (!contact.value) throw new Error('Lütfen bir cari seçin.');
        if (!issue.value || !due.value) throw new Error('Fatura ve vade tarihi zorunlu.');
        if (due.value < issue.value) throw new Error('Vade tarihi fatura tarihinden önce olamaz.');
        const gd = gdisc.value.trim() === '' ? 0 : parseMoney(gdisc.value);
        if (gd === null) throw new Error('Genel iskonto tutarı geçersiz (örnek: 12,50).');
        const body = {
          kind, contact_id: contact.value.id, issue_date: issue.value, due_date: due.value,
          prices_include_vat: incl.checked, general_discount: gd, lines: lines.map((l, i) => l.read(i + 1)),
        };
        submit.disabled = true;
        clear(submit);
        submit.append(h('span', { class: 'spinner' }), ' Kesiliyor…');
        const inv = await api('POST', '/invoices', body, { headers: { 'Idempotency-Key': key } });
        toast('Fatura ' + inv.number + ' oluşturuldu');
        ctx.navigate('#/faturalar');
        setTimeout(() => openInvoice(inv.id, ctx, null), 50);
      } catch (ex) {
        err.textContent = ex.message;
        err.classList.remove('hidden');
        err.scrollIntoView({ block: 'center' });
        submit.disabled = false;
        submit.textContent = 'Faturayı kes';
      }
    } },
      h('div', { class: 'card' },
        h('h2', { text: 'Fatura bilgileri' }),
        h('div', { class: 'field' }, h('label', { text: 'Fatura türü' }), seg([['sale', 'Satış faturası'], ['purchase', 'Alış faturası']], kind, (v) => { kind = v; build(); })),
        field(kind === 'sale' ? 'Müşteri' : 'Tedarikçi', contact.el),
        h('div', { class: 'row' }, field('Fatura tarihi', issue), field('Vade tarihi', due)),
        h('div', { class: 'inline' }, incl, h('label', { text: 'Girdiğim fiyatlar KDV dahildir' }))),
      h('div', { class: 'card' },
        h('h2', { text: 'Kalemler' }), linesBox,
        h('div', { class: 'toolbar' }, h('button', { class: 'btn secondary', type: 'button', onClick: addLine, text: '+ Kalem ekle' }))),
      h('div', { class: 'card' },
        h('div', { class: 'row' }, field('Genel iskonto (₺)', gdisc, 'Satırlara orantılı dağıtılır; toplamı bozmaz.')),
        h('div', { class: 'hint-box', text: 'KDV, iskonto dağıtımı ve toplamlar kayıt sırasında sunucuda hesaplanır; kuruş yuvarlaması toplamı bozmaz.' }),
        err,
        h('div', { class: 'toolbar' }, submit, h('a', { class: 'btn secondary', href: '#/faturalar', text: 'Vazgeç' }))));
    host.append(
      h('div', { class: 'page-head' }, h('div', {}, h('h1', { text: 'Yeni fatura' }), h('p', { class: 'subtle', text: 'Satış veya alış faturası kes' }))),
      form);
  }
  build();
}

function lineEditor(kind, onRemove) {
  const no = h('strong', { text: 'Kalem' });
  const rm = h('button', { class: 'btn secondary small hidden', type: 'button', onClick: onRemove, text: 'Kaldır' });
  let product = null;
  const prod = combo({
    label: 'Ürün veya hizmet', placeholder: 'Ürün/hizmet ara (isteğe bağlı; stok için gerekli)…',
    search: async (q) => (await api('GET', '/products' + qs({ q, limit: 8 }))).items,
    describe: (p) => ({ title: p.sku + ' · ' + p.name, sub: (p.kind === 'service' ? 'Hizmet' : 'Ürün') + ' · KDV %' + p.vat_rate }),
    onPick: (p) => {
      product = p;
      if (!p) return;
      desc.value = p.name;
      vat.value = String(p.vat_rate);
      if (kind === 'sale' && p.sale_price) price.value = String(Math.floor(p.sale_price / 100)) + ',' + String(p.sale_price % 100).padStart(2, '0');
    },
  });
  const desc = h('input', { type: 'text', maxlength: '200', placeholder: 'Kalem açıklaması' });
  const qty = h('input', { type: 'text', inputmode: 'decimal', value: '1' });
  const price = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0,00' });
  const vat = h('select', {}, [20, 10, 1].map((v) => h('option', { value: String(v), text: '%' + v })));
  const disc = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0,00' });
  const el = h('div', { class: 'line-card' },
    h('div', { class: 'toolbar' }, no, h('span', { style: 'flex:1' }), rm),
    field('Ürün / hizmet', prod.el),
    field('Açıklama', desc),
    h('div', { class: 'row' }, field('Miktar', qty), field('Birim fiyat (₺)', price), field('KDV', vat), field('Satır iskontosu (₺)', disc)));
  return {
    el,
    setNo(n, many) { no.textContent = 'Kalem ' + n; rm.classList.toggle('hidden', !many); },
    read(n) {
      const q = parseQty(qty.value);
      if (q === null) throw new Error('Kalem ' + n + ': miktar geçersiz (en fazla 3 ondalık, örnek: 2,5).');
      const p = parseMoney(price.value);
      if (p === null) throw new Error('Kalem ' + n + ': birim fiyat geçersiz (örnek: 125,90).');
      const d = disc.value.trim() === '' ? 0 : parseMoney(disc.value);
      if (d === null) throw new Error('Kalem ' + n + ': iskonto tutarı geçersiz.');
      if (!desc.value.trim()) throw new Error('Kalem ' + n + ': açıklama zorunlu.');
      return { product_id: product ? product.id : undefined, description: desc.value.trim(), quantity: q, unit_price: p, vat_rate: Number(vat.value), discount: d };
    },
  };
}
