import { api, qs } from '../api.js';
import { h, clear, fmtDateTime, table, loadInto } from '../ui.js';

const LABEL = {
  invoice_create: 'Fatura oluşturuldu', invoice_cancel: 'Fatura iptal edildi', invoice_correct: 'Fatura düzeltildi',
  payment_create: 'Ödeme kaydedildi', payment_delete: 'Ödeme silindi', payment_apply_advance: 'Avans mahsup edildi',
  period_close: 'Dönem kapatıldı', contact_create: 'Cari oluşturuldu', product_create: 'Ürün oluşturuldu',
  settings_update: 'Ayar değiştirildi', stock_cost_recalc: 'Stok maliyeti yeniden hesaplandı', login: 'Giriş yapıldı',
};

// Değerleri okunur kısa metne çevirir: {status: "issued"} -> status: issued
function brief(v) {
  if (!v) return '—';
  const s = Object.entries(v).map(([k, x]) => k + ': ' + (typeof x === 'object' ? JSON.stringify(x) : x)).join(' · ');
  return s.length > 140 ? s.slice(0, 137) + '…' : s;
}

// renderAudit bir kaydın (fatura, ödeme...) denetim izini gösterir: kim, ne zaman, ne yaptı, eski -> yeni değer.
export function renderAudit(container, entity, id) {
  clear(container);
  container.appendChild(h('h3', { text: 'Denetim izi' }));
  const box = h('div');
  container.appendChild(box);
  loadInto(box, () => api('GET', '/audit-log' + qs({ entity, entity_id: id, limit: 50 })), (d) => {
    box.appendChild(table([
      { label: 'Zaman', cls: 'nw', render: (r) => fmtDateTime(r.at) },
      { label: 'Kim', render: (r) => r.actor_email || 'sistem' },
      { label: 'İşlem', render: (r) => LABEL[r.action] || r.action },
      { label: 'Eski değer', render: (r) => h('code', { text: brief(r.old_value) }) },
      { label: 'Yeni değer', render: (r) => h('code', { text: brief(r.new_value) }) },
    ], d.items));
    box.appendChild(h('p', { class: 'muted', text: 'Denetim kaydı eklemeli-yalnızdır: değiştirilemez ve silinemez.' }));
  }, { isEmpty: (d) => d.items.length === 0, emptyText: 'Bu kayıt için denetim izi yok.', emptyIcon: '🧾' });
}
