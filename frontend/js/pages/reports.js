import { api, qs } from '../api.js';
import { h, clear, money, fmtQty, fmtDate, todayISO, badge, table, loadInto, field, seg } from '../ui.js';

const TABS = [['mizan', 'Mizan'], ['kdv', 'KDV özeti'], ['yaslandirma', 'Vade yaşlandırma'], ['stok', 'Stok']];

export function reportsPage(root, ctx, tab) {
  tab = TABS.some(([k]) => k === tab) ? tab : 'mizan';
  const nav = h('div', { class: 'toolbar' }, seg(TABS, tab, (v) => ctx.navigate('#/raporlar/' + v)));
  const body = h('div');
  root.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', { text: 'Raporlar' }), h('p', { class: 'subtle', text: 'Defter ve operasyon raporları' }))), nav, body);
  ({ mizan, kdv, yaslandirma, stok }[tab])(body, ctx);
}

function mizan(root) {
  const asOf = h('input', { type: 'date', 'aria-label': 'Tarih' });
  const out = h('div', { class: 'card' });
  const go = () => loadInto(out, () => api('GET', '/reports/trial-balance' + qs({ as_of: asOf.value })), (tb) => {
    out.append(
      h('div', { class: 'toolbar' },
        tb.balanced ? badge('✓ Mizan dengeli: toplam borç = toplam alacak', 'ok') : badge('✗ Mizan dengesiz!', 'danger'),
        h('span', { class: 'muted', text: tb.as_of ? fmtDate(tb.as_of) + ' tarihine kadar' : 'Tüm zamanlar' })),
      table([
        { label: 'Kod', render: (a) => h('span', { class: 'mono', text: a.code }) },
        { label: 'Hesap', render: (a) => a.name },
        { label: 'Borç', cls: 'num', render: (a) => money(a.debit) },
        { label: 'Alacak', cls: 'num', render: (a) => money(a.credit) },
        { label: 'Bakiye (B−A)', cls: 'num', render: (a) => h('span', { class: a.balance < 0 ? 'neg' : '', text: money(a.balance) }) },
      ], tb.accounts, { footer: [{ content: '' }, { content: 'TOPLAM' }, { cls: 'num', label: 'Borç', content: money(tb.total_debit) }, { cls: 'num', label: 'Alacak', content: money(tb.total_credit) }, { content: '' }] }));
  });
  root.append(h('div', { class: 'toolbar' }, field('Şu tarihe kadar', asOf), h('button', { class: 'btn secondary', type: 'button', onClick: go, text: 'Uygula' })), out);
  go();
}

function kdv(root) {
  const now = new Date();
  const first = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)).toISOString().slice(0, 10);
  const from = h('input', { type: 'date', value: first });
  const to = h('input', { type: 'date', value: todayISO() });
  const out = h('div', { class: 'card' });
  const lineTable = (rows) => table([
    { label: 'KDV oranı', render: (r) => '%' + r.rate },
    { label: 'Matrah (net)', cls: 'num', render: (r) => money(r.net) },
    { label: 'KDV', cls: 'num', render: (r) => money(r.vat) },
  ], rows);
  const go = () => loadInto(out, () => api('GET', '/reports/vat-summary' + qs({ from: from.value, to: to.value })), (d) => {
    out.append(
      h('div', { class: 'grid kpis' },
        h('div', { class: 'card kpi' }, h('div', { class: 'label', text: 'Hesaplanan KDV (satış)' }), h('div', { class: 'value', text: money(d.output_vat) })),
        h('div', { class: 'card kpi' }, h('div', { class: 'label', text: 'İndirilecek KDV (alış)' }), h('div', { class: 'value', text: money(d.input_vat) })),
        h('div', { class: 'card kpi' }, h('div', { class: 'label', text: d.payable >= 0 ? 'Ödenecek KDV' : 'Devreden KDV' }), h('div', { class: 'value', text: money(Math.abs(d.payable)) }))),
      h('h3', { text: 'Satışlar' }), d.sales.length ? lineTable(d.sales) : h('p', { class: 'muted', text: 'Bu aralıkta satış yok.' }),
      h('h3', { text: 'Alışlar' }), d.purchases.length ? lineTable(d.purchases) : h('p', { class: 'muted', text: 'Bu aralıkta alış yok.' }),
      h('p', { class: 'muted', text: 'İptal edilen faturalar dahil değildir.' }));
  });
  root.append(h('div', { class: 'toolbar' }, field('Başlangıç', from), field('Bitiş', to), h('button', { class: 'btn secondary', type: 'button', onClick: go, text: 'Uygula' })), out);
  go();
}

function yaslandirma(root) {
  let kind = 'receivable';
  const asOf = h('input', { type: 'date', value: todayISO() });
  const out = h('div', { class: 'card' });
  const names = ['0-30', '31-60', '61-90', '90+'];
  const go = () => loadInto(out, () => api('GET', '/reports/aging' + qs({ kind, as_of: asOf.value })), (r) => {
    const max = Math.max(1, ...names.map((n) => r.buckets[n]));
    out.append(
      h('div', { class: 'kpi' }, h('div', { class: 'label', text: kind === 'receivable' ? 'Toplam açık alacak' : 'Toplam açık borç' }), h('div', { class: 'value', text: money(r.total) })),
      h('div', { class: 'buckets' }, names.map((n) => h('div', { class: 'bucket' },
        h('div', { class: 't', text: n + ' gün' + (n === '0-30' ? ' (vadesi gelmemiş dahil)' : '') }),
        h('div', { class: 'v', text: money(r.buckets[n]) }),
        h('div', { class: 'bar', role: 'img', 'aria-label': n + ' gün: ' + money(r.buckets[n]) }, h('b', { style: 'width:' + Math.round((r.buckets[n] / max) * 100) + '%' }))))),
      h('h3', { style: 'margin-top:1rem', text: 'Cariler (toplamı en yüksek 50)' }),
      table([
        { label: 'Cari', render: (c) => h('a', { href: '#/cariler/' + c.contact_id, text: c.name }) },
        ...names.map((n) => ({ label: n, cls: 'num', render: (c) => (c.buckets[n] ? money(c.buckets[n]) : '—') })),
        { label: 'Toplam', cls: 'num', render: (c) => h('strong', { text: money(c.total) }) },
      ], r.contacts.slice().sort((a, b) => b.total - a.total).slice(0, 50)),
      h('p', { class: 'muted', text: 'Gün sayısı = rapor tarihi − vade tarihi. Kısmi ödemelerde yalnızca kalan tutar gösterilir.' }));
  }, { isEmpty: (r) => r.total === 0, emptyText: 'Açık fatura yok. 🎉', emptyIcon: '✅' });
  root.append(h('div', { class: 'toolbar' },
    seg([['receivable', 'Alacaklar'], ['payable', 'Borçlar']], kind, (v) => { kind = v; go(); }),
    field('Rapor tarihi', asOf), h('button', { class: 'btn secondary', type: 'button', onClick: go, text: 'Uygula' })), out);
  go();
}

function stok(root) {
  const out = h('div', { class: 'card' });
  root.append(out);
  loadInto(out, () => api('GET', '/reports/stock'), (r) => {
    out.append(
      h('div', { class: 'kpi' }, h('div', { class: 'label', text: 'Toplam stok değeri' }), h('div', { class: 'value', text: money(r.total_value) }),
        h('div', { class: 'hint', text: 'Defterdeki 153 Ticari Mallar hesabına eşittir.' })),
      table([
        { label: 'SKU', render: (p) => h('span', { class: 'mono', text: p.sku }) },
        { label: 'Ürün', render: (p) => p.name },
        { label: 'Miktar', cls: 'num', render: (p) => fmtQty(p.quantity_milli) + ' ' + p.unit },
        { label: 'Ort. birim maliyet', cls: 'num', render: (p) => money(p.avg_unit_cost) },
        { label: 'Değer', cls: 'num', render: (p) => money(p.value) },
      ], r.products));
  }, { isEmpty: (r) => r.products.length === 0, emptyText: 'Stoklu ürün yok.' });
}
