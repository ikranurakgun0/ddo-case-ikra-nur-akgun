import { api, qs } from '../api.js';
import { h, clear, money, fmtDate, toast, badge, table, loadInto, openModal, field, seg } from '../ui.js';
import { pagedList } from '../paged.js';
import { invoiceTable, openInvoice } from './invoices.js';
import { paymentTable, openPayment, openPaymentForm } from './payments.js';

const KIND = { customer: ['Müşteri', 'info'], supplier: ['Tedarikçi', 'gray'], both: ['Müşteri + Tedarikçi', 'warn'] };
export const contactKind = (k) => badge(KIND[k][0], KIND[k][1]);

export function contactsPage(root, ctx) {
  const f = { q: '', kind: '' };
  const box = h('div', { class: 'card' });
  let list;
  const search = h('input', { type: 'search', placeholder: 'Cari adı ara…', 'aria-label': 'Cari ara' });
  let t;
  search.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => { f.q = search.value.trim(); list.reload(); }, 300); });
  root.append(
    h('div', { class: 'page-head' },
      h('div', {}, h('h1', { text: 'Cariler' }), h('p', { class: 'subtle', text: 'Müşteri ve tedarikçi hesapları' })),
      h('div', { class: 'actions' }, h('button', { class: 'btn', type: 'button', onClick: () => newContact(() => list.reload()), text: '+ Yeni cari' }))),
    h('div', { class: 'toolbar' }, search, seg([['', 'Tümü'], ['customer', 'Müşteri'], ['supplier', 'Tedarikçi']], '', (v) => { f.kind = v; list.reload(); })),
    box);
  list = pagedList(box, {
    fetchPage: (after) => api('GET', '/contacts' + qs({ limit: 30, after, q: f.q, kind: f.kind })),
    render: (items) => table([
      { label: 'Ad', render: (c) => h('strong', { text: c.name }) },
      { label: 'Tür', render: (c) => contactKind(c.kind) },
      { label: 'Vergi no', render: (c) => c.tax_no || '—' },
      { label: 'E-posta', render: (c) => c.email || '—' },
    ], items, { onRow: (c) => ctx.navigate('#/cariler/' + c.id) }),
    emptyText: 'Aramanızla eşleşen cari yok.',
  });
}

function newContact(done) {
  const err = h('div', { class: 'err hidden', role: 'alert' });
  const kind = h('select', {}, [['customer', 'Müşteri'], ['supplier', 'Tedarikçi'], ['both', 'Müşteri ve tedarikçi']].map(([v, t]) => h('option', { value: v, text: t })));
  const name = h('input', { type: 'text', maxlength: '200', required: true });
  const tax = h('input', { type: 'text', maxlength: '20', inputmode: 'numeric' });
  const email = h('input', { type: 'email' });
  const phone = h('input', { type: 'tel' });
  const submit = h('button', { class: 'btn', type: 'submit', text: 'Kaydet' });
  const form = h('form', { novalidate: true, onSubmit: async (e) => {
    e.preventDefault();
    err.classList.add('hidden');
    if (!name.value.trim()) { err.textContent = 'Ad zorunlu.'; err.classList.remove('hidden'); return; }
    submit.disabled = true;
    try {
      await api('POST', '/contacts', { kind: kind.value, name: name.value.trim(), tax_no: tax.value.trim(), email: email.value.trim(), phone: phone.value.trim() });
      toast('Cari eklendi');
      m.close();
      done();
    } catch (ex) { err.textContent = ex.message; err.classList.remove('hidden'); submit.disabled = false; }
  } }, field('Ad / unvan', name), field('Tür', kind), h('div', { class: 'row' }, field('Vergi no', tax), field('Telefon', phone)), field('E-posta', email), err,
  h('div', { class: 'toolbar' }, submit, h('button', { class: 'btn secondary', type: 'button', onClick: () => m.close(), text: 'Vazgeç' })));
  const m = openModal({ title: 'Yeni cari', body: form });
}

// ---------- cari detayı ----------
export function contactDetailPage(root, ctx, id) {
  const top = h('div');
  const tabs = h('div');
  root.append(h('p', {}, h('a', { href: '#/cariler', text: '← Cariler' })), top, tabs);
  loadInto(top, () => api('GET', '/contacts/' + id), (c) => {
    const isCustomer = c.kind !== 'supplier';
    const isSupplier = c.kind !== 'customer';
    clear(top);
    top.append(h('div', { class: 'page-head' },
      h('div', {}, h('h1', { text: c.name }), h('p', { class: 'subtle' }, contactKind(c.kind), c.tax_no ? '  ·  Vergi no: ' + c.tax_no : '', c.email ? '  ·  ' + c.email : '')),
      h('div', { class: 'actions' },
        isCustomer ? h('a', { class: 'btn secondary', href: '#/faturalar/yeni?kind=sale&contact=' + id, text: 'Satış faturası' }) : null,
        isSupplier ? h('a', { class: 'btn secondary', href: '#/faturalar/yeni?kind=purchase&contact=' + id, text: 'Alış faturası' }) : null,
        isCustomer ? h('button', { class: 'btn', type: 'button', onClick: () => openPaymentForm(ctx, { kind: 'receipt', contact: c }, () => show(tab)), text: 'Tahsilat al' }) : null,
        isSupplier ? h('button', { class: 'btn', type: 'button', onClick: () => openPaymentForm(ctx, { kind: 'disbursement', contact: c }, () => show(tab)), text: 'Ödeme yap' }) : null)));
    let tab = isCustomer ? 'receivable' : 'payable';
    const opts = [];
    if (isCustomer) opts.push(['receivable', 'Alacak ekstresi']);
    if (isSupplier) opts.push(['payable', 'Borç ekstresi']);
    opts.push(['invoices', 'Faturalar'], ['payments', 'Ödemeler']);
    const body = h('div', { class: 'card' });
    clear(tabs);
    tabs.append(h('div', { class: 'toolbar' }, seg(opts, tab, (v) => { tab = v; show(v); })), body);
    function show(v) {
      clear(body);
      if (v === 'receivable' || v === 'payable') {
        loadInto(body, () => api('GET', '/contacts/' + id + '/statement' + qs({ kind: v })), (st) => {
          body.append(h('div', { class: 'kpi' },
            h('div', { class: 'label', text: v === 'receivable' ? 'Cari bakiye (bize borcu)' : 'Cari bakiye (borcumuz)' }),
            h('div', { class: 'value' + (st.balance < 0 ? ' neg' : ''), text: money(st.balance) }),
            h('div', { class: 'hint', text: st.balance < 0 ? 'Eksi: avans / fazla ödeme var' : 'Pozitif: ödenmemiş tutar' })),
          table([
            { label: 'Tarih', render: (r) => fmtDate(r.date) },
            { label: 'Hareket', render: (r) => (r.type === 'invoice' ? 'Fatura ' : 'Ödeme ') + (r.type === 'invoice' ? r.ref : '') },
            { label: 'Durum', render: (r) => (r.status === 'cancelled' || r.status === 'deleted' ? badge(r.status === 'cancelled' ? 'İptal' : 'Silindi', 'gray') : '') },
            { label: 'Borçlanma', cls: 'num', render: (r) => (r.charge ? money(r.charge) : '') },
            { label: 'Ödeme', cls: 'num', render: (r) => (r.payment ? money(r.payment) : '') },
            { label: 'Bakiye', cls: 'num', render: (r) => money(r.balance) },
          ], st.rows.slice().reverse()));
        }, { isEmpty: (st) => st.rows.length === 0, emptyText: 'Bu cariyle henüz hareket yok.' });
      } else if (v === 'invoices') {
        pagedList(body, { fetchPage: (a) => api('GET', '/invoices' + qs({ limit: 20, after: a, contact_id: id })),
          render: (items) => invoiceTable(items, (r) => openInvoice(r.id, ctx, () => show(v)), { hideContact: true }), emptyText: 'Bu cariye ait fatura yok.' });
      } else {
        pagedList(body, { fetchPage: (a) => api('GET', '/payments' + qs({ limit: 20, after: a, contact_id: id })),
          render: (items) => paymentTable(items, (r) => openPayment(r.id, ctx, () => show(v)), { hideContact: true }), emptyText: 'Bu cariye ait ödeme yok.' });
      }
    }
    show(tab);
  });
}
