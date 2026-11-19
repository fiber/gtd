// Board: a Trello-style overview (SPEC §5.8). Columns come from the server.
// Dragging a card to a column sends the same ops as everywhere else, and the
// server rejects moves the rules do not allow.
import { store } from './store.js';
import { t } from './i18n.js';
import { View, html, nothing, meta, select } from './widgets.js';

const KEY = 'gtd.board'; // the space the board showed last, on this device
const ALL = '*';

class Board extends View {
  static properties = {
    space: { state: true }, area: { state: true }, project: { state: true }, field: { state: true },
    data: { state: true }, over: { state: true }, ask: { state: true },
  };

  constructor() {
    super();
    // The board opens on the space it showed last on this device.
    let last = '';
    try { last = localStorage.getItem(KEY) || ''; } catch { /* private browsing */ }
    this.space = last === ALL || store.me.spaces.some((sp) => sp.id === last) ? last : store.me.user.id;
    this.area = this.project = this.field = '';
  }

  async reload() {
    this.data = await this.run(() => store.board({ space: this.space, area: this.area, project: this.project, field: this.field }));
  }

  filter(key, value) {
    this[key] = value;
    if (key === 'space') {
      this.area = this.project = this.field = '';
      try { localStorage.setItem(KEY, value); } catch { /* private browsing */ }
    }
    this.reload();
  }

  find(id) {
    for (const c of this.data.columns) {
      const b = c.items.find((x) => x.id === id);
      if (b) return [b, c.key];
    }
    return [];
  }

  async drop(e, column) {
    e.preventDefault();
    this.over = null;
    const [b, from] = this.find(e.dataTransfer.getData('text/plain'));
    if (!b || from === column) return;
    if (column === 'deferred') {
      this.ask = { item: b, date: '' }; // deferring needs a date
      return;
    }
    await this.run(async () => {
      let item = b;
      if (from === 'deferred') {
        await store.patch(item, { defer_until: '' });
        item = { ...item, rev: item.rev + 1 };
      }
      if (item.state !== column) await store.transition(item, column);
    });
  }

  async defer() {
    const { item, date } = this.ask;
    this.ask = null;
    if (date) await this.run(() => store.patch(item, { defer_until: date }));
  }

  render() {
    const d = this.data;
    if (!d) return nothing;
    const all = this.space === ALL;
    const spaces = [{ id: ALL, title: t('board.all_spaces') }].concat(
      store.me.spaces.map((s) => ({ id: s.id, title: s.id === store.me.user.id ? t('clarify.opt.personal') : s.name })));
    return html`
      <header class="bar">
        <h1>${t('nav.board')}</h1>
        <div class="filters">
          ${spaces.length > 2 ? select(spaces, this.space, (v) => this.filter('space', v)) : nothing}
          ${all ? nothing : select(d.options.areas, this.area, (v) => this.filter('area', v), t('board.all_areas'))}
          ${all ? nothing : select(d.options.projects, this.project, (v) => this.filter('project', v), t('board.all_projects'))}
          ${this.field ? html`<button class="chip badge on" @click=${() => this.filter('field', '')}>${this.field.replace(':', ' = ')} ×</button>` : nothing}
        </div>
      </header>
      <div class="board">
        ${d.columns.map((c) => html`
          <section class="column ${this.over === c.key ? 'over' : ''}"
            @dragover=${(e) => { e.preventDefault(); this.over = c.key; }}
            @dragleave=${() => { if (this.over === c.key) this.over = null; }}
            @drop=${(e) => this.drop(e, c.key)}>
            <h2>${t('board.' + c.key)} <span class="count">${c.items.length}</span></h2>
            ${c.items.map((b) => html`
              <div class="card row" tabindex="0" draggable="true" @dragstart=${(e) => e.dataTransfer.setData('text/plain', b.id)}>
                <a class="title" href=${'i/' + b.id}>${b.title}</a>
                <span class="meta">${all ? html`<span class="chip">${b.space === store.me.user.id ? t('clarify.opt.personal') : b.space_name}</span>` : nothing}${meta({ ...b, badges: [] })}
                  ${(b.badges || []).map((x) => html`
                    <button class="chip badge" title=${t('board.filter_badge')} @click=${() => this.filter('field', x)}>${x.split(':').slice(1).join(':')}</button>`)}</span>
              </div>`)}
          </section>`)}
      </div>
      ${this.ask ? html`
        <div class="overlay">
          <form class="dialog" @submit=${(e) => { e.preventDefault(); this.defer(); }}>
            <h2>${t('board.defer_until')}</h2>
            <p class="subject">${this.ask.item.title}</p>
            <input type="date" required min=${store.me.today} @change=${(e) => { this.ask.date = e.target.value; }}>
            <div class="chips">${store.me.presets.map((p) => html`
              <button type="button" class="chip pick" @click=${() => { this.ask.date = p.value; this.defer(); }}>${t(p.label)}</button>`)}</div>
            <div class="buttons">
              <button class="primary">${t('action.defer')}</button>
              <button type="button" class="link" @click=${() => { this.ask = null; }}>${t('action.cancel')}</button>
            </div>
          </form>
        </div>` : nothing}`;
  }
}
customElements.define('gtd-board', Board);

