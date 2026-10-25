import { api, qs } from '../api.js';
import { h, clear, money, parseMoney, fmtQty, fmtDate, toast, badge, table, loadInto, openModal, field, seg } from '../ui.js';
import { pagedList } from '../paged.js';

export function productsPage(root, ctx) {
  const f = { q: '', kind: '' };
  const box = h('div', { class: 'card' });
  let list;
  const search = h('input', { type: 'search', placeholder: 'SKU veya ad ara…', 'aria-label': 'Ürün ara' });
  let t;
  search.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => { f.q = search.value.trim(); list.reload(); }, 300); });
  root.append(
    h('div', { class: 'page-head' },
      h('div', {}, h('h1', { text: 'Ürün ve hizmetler' }), h('p', { class: 'subtle', text: 'Stok, ağırlıklı ortalama maliyetle (AOM) değerlenir' })),
      h('div', { class: 'actions' }, h('button', { class: 'btn', type: 'button', onClick: () => newProduct(() => list.reload()), text: '+ Yeni ürün / hizmet' }))),
    h('div', { class: 'toolbar' }, search, seg([['', 'Tümü'], ['product', 'Ürün'], ['service', 'Hizmet']], '', (v) => { f.kind = v; list.reload(); })),
    box);
  list = pagedList(box, {
    fetchPage: (after) => api('GET', '/products' + qs({ limit: 30, after, q: f.q, kind: f.kind })),
    render: (items) => table([
      { label: 'SKU', render: (p) => h('span', { class: 'mono', text: p.sku }) },
      { label: 'Ad', render: (p) => h('strong', { text: p.name }) },
      { label: 'Tür', render: (p) => badge(p.kind === 'service' ? 'Hizmet' : 'Ürün', p.kind === 'service' ? 'gray' : 'info') },
      { label: 'KDV', cls: 'num', render: (p) => '%' + p.vat_rate },
      { label: 'Satış fiyatı', cls: 'num', render: (p) => money(p.sale_price) },
    ], items, { onRow: (p) => openProduct(p.id) }),
    emptyText: 'Aramanızla eşleşen ürün yok.',
  });
}

function newProduct(done) {
  const err = h('div', { class: 'err hidden', role: 'alert' });
  const sku = h('input', { type: 'text', maxlength: '64' });
  const name = h('input', { type: 'text', maxlength: '200' });
  const kind = h('select', {}, h('option', { value: 'product', text: 'Ürün (stok takipli)' }), h('option', { value: 'service', text: 'Hizmet (stok yok)' }));
  const vat = h('select', {}, [20, 10, 1].map((v) => h('option', { value: String(v), text: '%' + v })));
  const price = h('input', { type: 'text', inputmode: 'decimal', placeholder: '0,00' });
  const submit = h('button', { class: 'btn', type: 'submit', text: 'Kaydet' });
  const form = h('form', { novalidate: true, onSubmit: async (e) => {
    e.preventDefault();
    err.classList.add('hidden');
    const p = price.value.trim() === '' ? 0 : parseMoney(price.value);
    if (!sku.value.trim() || !name.value.trim()) { err.textContent = 'SKU ve ad zorunlu.'; err.classList.remove('hidden'); return; }
    if (p === null) { err.textContent = 'Satış fiyatı geçersiz.'; err.classList.remove('hidden'); return; }
    submit.disabled = true;
    try {
      await api('POST', '/products', { sku: sku.value.trim(), name: name.value.trim(), kind: kind.value, vat_rate: Number(vat.value), sale_price: p });
      toast('Kaydedildi');
      m.close();
      done();
    } catch (ex) { err.textContent = ex.message; err.classList.remove('hidden'); submit.disabled = false; }
  } }, h('div', { class: 'row' }, field('SKU', sku), field('Ad', name)), h('div', { class: 'row' }, field('Tür', kind), field('KDV', vat), field('Satış fiyatı (₺)', price)), err,
  h('div', { class: 'toolbar' }, submit, h('button', { class: 'btn secondary', type: 'button', onClick: () => m.close(), text: 'Vazgeç' })));
  const m = openModal({ title: 'Yeni ürün / hizmet', body: form });
}

export function openProduct(id) {
  const box = h('div');
  const m = openModal({ title: 'Ürün kartı', body: box, wide: true });
  m.foot.remove();
  loadInto(box, () => api('GET', '/products/' + id), (p) => {
    box.append(
      h('div', { class: 'page-head' }, h('div', {}, h('h1', { text: p.name }), h('p', { class: 'subtle' }, h('span', { class: 'mono', text: p.sku }), '  ·  KDV %' + p.vat_rate + '  ·  Satış ' + money(p.sale_price)))),
      p.stock ? h('div', { class: 'grid kpis' },
        kpi('Eldeki stok', fmtQty(p.stock.quantity_milli) + ' ' + (p.unit || 'adet')),
        kpi('Stok değeri', money(p.stock.value)),
        kpi('Ortalama birim maliyet', money(p.stock.avg_unit_cost))) : h('p', { class: 'muted', text: 'Hizmet kartlarında stok tutulmaz.' }));
    if (p.stock) {
      const mv = h('div');
      box.append(h('h3', { text: 'Son stok hareketleri' }), mv);
      loadInto(mv, () => api('GET', '/stock-movements' + qs({ product_id: id, limit: 15, order: 'desc' })), (d) => {
        mv.appendChild(table([
          { label: 'Tarih', render: (r) => fmtDate(r.date) },
          { label: 'Yön', render: (r) => badge(r.direction === 'in' ? 'Giriş' : 'Çıkış', r.direction === 'in' ? 'ok' : 'warn') },
          { label: 'Miktar', cls: 'num', render: (r) => fmtQty(r.quantity_milli) },
          { label: 'Değer / maliyet', cls: 'num', render: (r) => money(r.value) },
          { label: 'Durum', render: (r) => (r.active ? '' : badge('İptal', 'gray')) },
        ], d.items));
      }, { isEmpty: (d) => d.items.length === 0, emptyText: 'Bu ürün için henüz stok hareketi yok.' });
    }
  });
  const kpi = (l, v) => h('div', { class: 'card kpi' }, h('div', { class: 'label', text: l }), h('div', { class: 'value', text: v }));
}
