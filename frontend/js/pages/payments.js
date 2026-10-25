import { api, qs, uuid } from '../api.js';
import { h, clear, money, parseMoney, fmtDate, todayISO, toast, badge, table, loadInto, openModal, confirmDialog, combo, field, seg } from '../ui.js';
import { pagedList } from '../paged.js';
import { openInvoice } from './invoices.js';
import { renderAudit } from './auditview.js';

const METHOD = { cash: 'Kasa', bank: 'Banka' };
export const payKind = (k) => badge(k === 'receipt' ? 'Tahsilat' : 'Ödeme', k === 'receipt' ? 'ok' : 'warn');

export function paymentStatus(p) {
  if (p.status === 'deleted') return badge('Silindi', 'gray');
  if (p.advance > 0) return badge('Avans var', 'info');
  return badge('Mahsup edildi', 'ok');
}

export function paymentTable(items, onRow, { hideContact } = {}) {
  return table([
    { label: 'Tarih', cls: 'nw', render: (p) => fmtDate(p.date) },
    { label: 'Tür', render: (p) => payKind(p.kind) },
    hideContact ? null : { label: 'Cari', render: (p) => p.contact_name },
    { label: 'Yöntem', render: (p) => METHOD[p.method] },
    { label: 'Tutar', cls: 'num', render: (p) => money(p.amount) },
    { label: 'Mahsup', cls: 'num', render: (p) => money(p.allocated) },
    { label: 'Avans', cls: 'num', render: (p) => (p.advance ? money(p.advance) : '—') },
    { label: 'Durum', render: (p) => paymentStatus(p) },
  ].filter(Boolean), items, { onRow });
}

export function paymentsPage(root, ctx) {
  const f = { kind: '' };
  const box = h('div', { class: 'card' });
  let list;
  root.append(
    h('div', { class: 'page-head' },
      h('div', {}, h('h1', { text: 'Tahsilat ve ödemeler' }), h('p', { class: 'subtle', text: 'Tahsilatlar açık faturalara en eskiden başlayarak (FIFO) mahsup edilir' })),
      h('div', { class: 'actions' },
        h('button', { class: 'btn', type: 'button', onClick: () => openPaymentForm(ctx, { kind: 'receipt' }, () => list.reload()), text: '+ Tahsilat al' }),
        h('button', { class: 'btn secondary', type: 'button', onClick: () => openPaymentForm(ctx, { kind: 'disbursement' }, () => list.reload()), text: '+ Ödeme yap' }))),
    h('div', { class: 'toolbar' }, seg([['', 'Tümü'], ['receipt', 'Tahsilat'], ['disbursement', 'Ödeme']], '', (v) => { f.kind = v; list.reload(); })),
    box);
  list = pagedList(box, {
    fetchPage: (after) => api('GET', '/payments' + qs({ limit: 25, after, kind: f.kind })),
    render: (items) => paymentTable(items, (p) => openPayment(p.id, ctx, () => list.reload())),
    emptyText: 'Henüz tahsilat ya da ödeme yok.', emptyIcon: '💸',
  });
}

export function openPayment(id, ctx, onChange) {
  const box = h('div');
  const m = openModal({ title: 'Ödeme ayrıntısı', body: box });
  m.foot.remove();
  const load = () => loadInto(box, () => api('GET', '/payments/' + id), render);
  function render(p) {
    clear(box);
    box.append(
      h('div', { class: 'page-head' },
        h('div', {}, h('h1', { text: money(p.amount) }), h('p', { class: 'subtle' }, payKind(p.kind), ' ', paymentStatus(p))),
        h('div', { class: 'actions' },
          ctx.can.books && p.status === 'active' && p.advance > 0 ? h('button', { class: 'btn secondary', type: 'button', onClick: () => applyAdvance(p), text: 'Avansı mahsup et' }) : null,
          ctx.can.books && p.status === 'active' ? h('button', { class: 'btn danger', type: 'button', onClick: () => del(p), text: 'Sil' }) : null,
          ctx.can.books ? h('button', { class: 'btn secondary', type: 'button', onClick: () => renderAudit(box.querySelector('.abox'), 'payment', id), text: 'Denetim izi' }) : null)),
      h('div', { class: 'row' },
        h('div', { class: 'field' }, h('div', { class: 'muted', text: 'Cari' }), h('a', { href: '#/cariler/' + p.contact_id, onClick: () => m.close(), text: p.contact_name })),
        h('div', { class: 'field' }, h('div', { class: 'muted', text: 'Tarih' }), fmtDate(p.date)),
        h('div', { class: 'field' }, h('div', { class: 'muted', text: 'Yöntem' }), METHOD[p.method])),
      p.advance > 0 ? h('div', { class: 'hint-box', text: money(p.advance) + ' tutarı hiçbir faturaya mahsup edilmedi; avans (' + (p.kind === 'receipt' ? '340' : '159') + ') hesabında bekliyor.' }) : null,
      p.status === 'deleted' ? h('div', { class: 'err', text: 'Bu ödeme silindi: yevmiye kayıtları ters kayıtla kapatıldı, mahsuplar geri alındı.' }) : null,
      h('h3', { text: 'Mahsup edilen faturalar (FIFO sırası)' }),
      (p.allocations || []).length === 0 ? h('p', { class: 'muted', text: 'Mahsup kaydı yok.' }) :
        table([
          { label: 'Fatura', render: (a) => h('a', { href: '#', class: 'mono', onClick: (e) => { e.preventDefault(); openInvoice(a.invoice_id, ctx, load); }, text: a.invoice_number }) },
          { label: 'Mahsup tutarı', cls: 'num', render: (a) => money(a.amount) },
        ], p.allocations),
      h('div', { class: 'abox' }));
  }
  async function del(p) {
    if (!(await confirmDialog('Bu ödeme silinsin mi? Mahsuplar geri alınır ve faturalar yeniden açılır.', { confirmText: 'Ödemeyi sil', danger: true }))) return;
    try { await api('DELETE', '/payments/' + id); toast('Ödeme silindi'); if (onChange) onChange(); load(); } catch (e) { toast(e.message, 'error'); }
  }
  async function applyAdvance() {
    try { await api('POST', '/payments/' + id + '/apply-advance', {}); toast('Avans mahsup edildi'); if (onChange) onChange(); load(); } catch (e) { toast(e.message, 'error'); }
  }
  load();
}

// Yeni tahsilat/ödeme formu. opts: {kind, contact}
export function openPaymentForm(ctx, opts, onDone) {
  let kind = opts.kind || 'receipt';
  const key = uuid();
  const body = h('div');
  const m = openModal({ title: 'Tahsilat / ödeme', body });
  function build() {
    clear(body);
    const err = h('div', { class: 'err hidden', role: 'alert' });
    const contact = combo({
      label: 'Cari', placeholder: kind === 'receipt' ? 'Müşteri ara…' : 'Tedarikçi ara…',
      search: async (q) => (await api('GET', '/contacts' + qs({ q, kind: kind === 'receipt' ? 'customer' : 'supplier', limit: 8 }))).items,
      describe: (c) => ({ title: c.name, sub: c.tax_no ? 'Vergi no: ' + c.tax_no : '' }),
    });
    if (opts.contact) contact.set(opts.contact);
    const method = h('select', {}, h('option', { value: 'bank', text: 'Banka' }), h('option', { value: 'cash', text: 'Kasa' }));
    const date = h('input', { type: 'date', value: todayISO() });
    const amount = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0,00' });
    const submit = h('button', { class: 'btn', type: 'submit', text: kind === 'receipt' ? 'Tahsilatı kaydet' : 'Ödemeyi kaydet' });
    const form = h('form', { novalidate: true, onSubmit: async (e) => {
      e.preventDefault();
      err.classList.add('hidden');
      try {
        if (!contact.value) throw new Error('Lütfen bir cari seçin.');
        const amt = parseMoney(amount.value);
        if (amt === null || amt <= 0) throw new Error('Tutar geçersiz (örnek: 1.250,00).');
        submit.disabled = true;
        const p = await api('POST', '/payments', { kind, contact_id: contact.value.id, method: method.value, date: date.value, amount: amt }, { headers: { 'Idempotency-Key': key } });
        showResult(p);
        if (onDone) onDone();
      } catch (ex) { err.textContent = ex.message; err.classList.remove('hidden'); submit.disabled = false; }
    } },
    opts.kind ? null : h('div', { class: 'field' }, h('label', { text: 'Tür' }), seg([['receipt', 'Tahsilat (müşteriden)'], ['disbursement', 'Ödeme (tedarikçiye)']], kind, (v) => { kind = v; build(); })),
    field(kind === 'receipt' ? 'Müşteri' : 'Tedarikçi', contact.el),
    h('div', { class: 'row' }, field('Yöntem', method), field('Tarih', date), field('Tutar (₺)', amount)),
    err, h('div', { class: 'toolbar' }, submit, h('button', { class: 'btn secondary', type: 'button', onClick: () => m.close(), text: 'Vazgeç' })));
    body.appendChild(form);
  }
  function showResult(p) {
    clear(body);
    const allocs = p.allocations || []; // API, mahsup yoksa alanı hiç göndermez
    body.append(
      h('div', { class: 'ok-box', text: money(p.amount) + ' kaydedildi.' }),
      allocs.length
        ? h('div', {}, h('h3', { text: 'En eski faturadan başlayarak mahsup edildi' }),
            table([{ label: 'Fatura', render: (a) => h('span', { class: 'mono', text: a.invoice_number }) }, { label: 'Tutar', cls: 'num', render: (a) => money(a.amount) }], allocs))
        : h('p', { class: 'muted', text: 'Açık fatura bulunamadı; tutarın tamamı avans olarak tutuluyor.' }),
      p.advance > 0 && allocs.length ? h('div', { class: 'hint-box', text: money(p.advance) + ' faturaları aştığı için avans olarak tutuluyor.' }) : null,
      h('div', { class: 'toolbar' }, h('button', { class: 'btn', type: 'button', onClick: () => m.close(), text: 'Tamam' })));
  }
  build();
}
