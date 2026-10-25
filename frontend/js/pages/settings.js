import { api } from '../api.js';
import { h, clear, fmtDate, toast, badge, loadInto, confirmDialog, field } from '../ui.js';

export function settingsPage(root, ctx) {
  root.append(h('div', { class: 'page-head' }, h('div', {}, h('h1', { text: 'Ayarlar' }), h('p', { class: 'subtle', text: 'Dönem kapatma ve stok politikası' }))));
  const period = h('div', { class: 'card' });
  const stock = h('div', { class: 'card' });
  root.append(period, stock);

  const loadPeriod = () => loadInto(period, () => api('GET', '/periods'), (p) => {
    const date = h('input', { type: 'date', max: new Date().toISOString().slice(0, 10) });
    period.append(
      h('h2', { text: 'Dönem kapatma' }),
      h('p', {}, p.closed_through ? ['Kapalı dönem: ', h('strong', { text: fmtDate(p.closed_through) }), ' tarihine kadar (dahil). Bu tarihlere kayıt girilemez ve kapanış geri alınamaz.'] : 'Henüz kapatılmış dönem yok.'),
      h('div', { class: 'toolbar' }, field('Şu tarihe kadar kapat', date),
        h('button', { class: 'btn', type: 'button', text: 'Dönemi kapat', onClick: async () => {
          if (!date.value) { toast('Bir tarih seçin', 'error'); return; }
          if (!(await confirmDialog(fmtDate(date.value) + ' tarihine kadar olan dönem kapatılsın mı? Bu işlem geri alınamaz.', { confirmText: 'Dönemi kapat', danger: true }))) return;
          try { await api('POST', '/periods/close', { through: date.value }); toast('Dönem kapatıldı'); loadPeriod(); } catch (e) { toast(e.message, 'error'); }
        } })));
  });

  const loadStock = () => loadInto(stock, () => api('GET', '/settings'), (s) => {
    const cb = h('input', { type: 'checkbox', disabled: !ctx.can.owner });
    cb.checked = s.allow_negative_stock;
    cb.addEventListener('change', async () => {
      try { await api('PUT', '/settings', { allow_negative_stock: cb.checked }); toast('Ayar kaydedildi'); } catch (e) { cb.checked = !cb.checked; toast(e.message, 'error'); }
    });
    stock.append(h('h2', { text: 'Negatif stok politikası' }),
      h('div', { class: 'inline' }, cb, h('label', { text: 'Stok yetersizken de satışa izin ver (stok eksiye düşebilir)' })),
      h('p', { class: 'muted', text: ctx.can.owner ? 'Kapalıyken yetersiz stoklu satış 409 ile reddedilir.' : 'Bu ayarı yalnızca işletme sahibi değiştirebilir.' }));
  });
  if (ctx.can.books) loadPeriod(); else period.remove();
  if (ctx.can.owner) loadStock(); else stock.remove();
}
