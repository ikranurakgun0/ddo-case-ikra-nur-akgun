import { api } from '../api.js';
import { h, money, fmtDate, badge, table, loadInto } from '../ui.js';

// Müşteri rolünün salt okunur ekstresi
export function statementPage(root) {
  root.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', { text: 'Hesap ekstrem' }), h('p', { class: 'subtle', text: 'Faturalarınız ve ödemeleriniz (salt okunur)' }))));
  const box = h('div', { class: 'card' });
  root.append(box);
  loadInto(box, () => api('GET', '/me/statement'), (st) => {
    box.append(
      h('div', { class: 'kpi' }, h('div', { class: 'label', text: st.name + ' · güncel bakiye' }), h('div', { class: 'value' + (st.balance < 0 ? ' neg' : ''), text: money(st.balance) }),
        h('div', { class: 'hint', text: st.balance > 0 ? 'Ödenmemiş tutar' : st.balance < 0 ? 'Avans / fazla ödeme' : 'Borcunuz yok' })),
      table([
        { label: 'Tarih', render: (r) => fmtDate(r.date) },
        { label: 'Hareket', render: (r) => (r.type === 'invoice' ? 'Fatura ' + r.ref : 'Ödeme') },
        { label: 'Durum', render: (r) => (r.status === 'cancelled' || r.status === 'deleted' ? badge(r.status === 'cancelled' ? 'İptal' : 'Silindi', 'gray') : '') },
        { label: 'Fatura tutarı', cls: 'num', render: (r) => (r.charge ? money(r.charge) : '') },
        { label: 'Ödeme', cls: 'num', render: (r) => (r.payment ? money(r.payment) : '') },
        { label: 'Bakiye', cls: 'num', render: (r) => money(r.balance) },
      ], st.rows.slice().reverse()));
  }, { isEmpty: (st) => st.rows.length === 0, emptyText: 'Henüz fatura veya ödemeniz yok.' });
}
