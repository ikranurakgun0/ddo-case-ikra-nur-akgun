import { h, clear, loadInto, toast } from './ui.js';

// pagedList: sunucu taraflı keyset sayfalamayı "Daha fazla yükle" düğmesiyle gösterir.
// fetchPage(after) -> {items, next_after}; render(items) -> Node
export function pagedList(container, { fetchPage, render, emptyText, emptyIcon }) {
  let items = [];
  let next = null;
  const body = h('div');
  const more = h('div', { class: 'toolbar' });
  container.append(body, more);

  function paint() {
    clear(body);
    body.appendChild(render(items));
    clear(more);
    more.appendChild(h('span', { class: 'muted', text: items.length + ' kayıt gösteriliyor' + (next ? '' : ' (hepsi)') }));
    if (next) more.appendChild(h('button', { class: 'btn secondary', type: 'button', onClick: loadMore, text: 'Daha fazla yükle' }));
  }

  async function loadMore(e) {
    const btn = e.currentTarget;
    btn.disabled = true;
    btn.textContent = 'Yükleniyor…';
    try {
      const d = await fetchPage(next);
      items = items.concat(d.items);
      next = d.next_after;
      paint();
    } catch (err) {
      toast(err.message, 'error');
      btn.disabled = false;
      btn.textContent = 'Tekrar dene';
    }
  }

  async function reload() {
    items = [];
    next = null;
    clear(more);
    await loadInto(body, () => fetchPage(''), (d) => { items = d.items; next = d.next_after; paint(); },
      { isEmpty: (d) => d.items.length === 0, emptyText, emptyIcon });
  }
  reload();
  return { reload };
}
