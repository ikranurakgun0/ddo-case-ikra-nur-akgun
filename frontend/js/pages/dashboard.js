import { api } from '../api.js';
import { h, clear, money, fmtTime, fmtDateTime, toast, table, stateError, skeleton } from '../ui.js';
import { invoiceTable, openInvoice } from './invoices.js';
import { payKind, openPayment } from './payments.js';

// Pano: tek bir /dashboard çağrısıyla göstergeler + son hareketler; 10 sn'de bir kendini yeniler (canlı akış).
export function dashboardPage(root, ctx) {
  const liveText = h('span', { text: 'Yükleniyor…' });
  root.append(h('div', { class: 'page-head' },
    h('div', {}, h('h1', { text: 'Pano' }), h('p', { class: 'subtle', text: ctx.me.business_name })),
    h('div', { class: 'live', title: 'Veriler 10 saniyede bir kendiliğinden yenilenir' }, h('i'), liveText)));
  const body = h('div');
  root.appendChild(body);
  body.appendChild(skeleton());
  const seenInv = new Set();
  const seenPay = new Set();
  let first = true;
  let alive = true;
  ctx.onLeave(() => { alive = false; clearInterval(timer); });

  async function refresh() {
    try {
      const d = await api('GET', '/dashboard');
      if (!alive) return;
      draw(d);
      liveText.textContent = 'Canlı · son güncelleme ' + fmtTime(d.generated_at);
      first = false;
    } catch (e) {
      if (!alive) return;
      if (first) { clear(body); body.appendChild(stateError(e, () => { clear(body); body.appendChild(skeleton()); refresh(); })); }
      else liveText.textContent = 'Bağlantı sorunu · yeniden deneniyor…';
    }
  }

  function kpi(label, value, hint, neg) {
    return h('div', { class: 'card kpi' }, h('div', { class: 'label', text: label }), h('div', { class: 'value' + (neg ? ' neg' : ''), text: value }), hint ? h('div', { class: 'hint', text: hint }) : null);
  }

  function draw(d) {
    clear(body);
    body.append(
      h('div', { class: 'grid kpis' },
        kpi('Açık alacak', money(d.receivable), 'Müşterilerden tahsil edilecek', false),
        kpi('Açık borç', money(d.payable), 'Tedarikçilere ödenecek', false),
        kpi('Kasa + banka', money(d.cash), '100 + 102 hesapları', d.cash < 0),
        kpi('Stok değeri', money(d.stock_value), 'Ağırlıklı ortalama maliyetle', false),
        kpi('Bu ay net satış', money(d.month_sales), 'KDV hariç', false),
        kpi(d.month_vat_payable >= 0 ? 'Bu ay ödenecek KDV' : 'Bu ay devreden KDV', money(Math.abs(d.month_vat_payable)), 'Hesaplanan − indirilecek', false)),
      h('p', { class: 'muted', text: d.counts.invoices.toLocaleString('tr-TR') + ' fatura · ' + d.counts.payments.toLocaleString('tr-TR') + ' tahsilat/ödeme · ' + d.counts.contacts.toLocaleString('tr-TR') + ' cari · ' + d.counts.products.toLocaleString('tr-TR') + ' ürün/hizmet' }),
      h('div', { class: 'grid two' },
        h('div', { class: 'card' }, h('div', { class: 'page-head' }, h('h2', { text: 'Son faturalar' }), h('a', { href: '#/faturalar', text: 'Tümü →' })),
          table([
            { label: 'Numara', render: (r) => h('span', { class: 'mono', text: r.number }) },
            { label: 'Cari', render: (r) => r.contact_name },
            { label: 'Toplam', cls: 'num', render: (r) => money(r.gross_total) },
          ], d.recent_invoices, { onRow: (r) => openInvoice(r.id, ctx, refresh), rowClass: (r) => (!first && !seenInv.has(r.id) ? 'flash' : '') })),
        h('div', { class: 'card' }, h('div', { class: 'page-head' }, h('h2', { text: 'Son tahsilat ve ödemeler' }), h('a', { href: '#/odemeler', text: 'Tümü →' })),
          table([
            { label: 'Zaman', cls: 'nw', render: (r) => fmtDateTime(r.created_at) },
            { label: 'Cari', render: (r) => r.contact_name },
            { label: 'Tür', render: (r) => payKind(r.kind) },
            { label: 'Tutar', cls: 'num', render: (r) => money(r.amount) },
          ], d.recent_payments, { onRow: (r) => openPayment(r.id, ctx, refresh), rowClass: (r) => (!first && !seenPay.has(r.id) ? 'flash' : '') }))));
    d.recent_invoices.forEach((r) => seenInv.add(r.id));
    d.recent_payments.forEach((r) => seenPay.add(r.id));
  }
  refresh();
  const timer = setInterval(refresh, 10000);
}
