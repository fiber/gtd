// The smaller screens: areas, search, settings.
import { store } from './store.js';
import { t, loadLanguage } from './i18n.js';
import { View, html, nothing, row, select, toast } from './widgets.js';

class Areas extends View {
  static properties = { list: { state: true }, space: { state: true } };
  constructor() {
    super();
    this.space = store.me.user.id;
  }
  async reload() {
    this.list = await this.run(() => store.areas());
  }
  async add(e) {
    e.preventDefault();
    const input = e.target.querySelector('input');
    if (!input.value.trim()) return;
    await this.run(async () => {
      await store.create(this.space, { kind: 'area', title: input.value.trim() });
      input.value = '';
    });
  }
  render() {
    if (!this.list) return nothing;
    return html`
      <header class="bar"><h1>${t('nav.areas')}</h1></header>
      <form class="inline" @submit=${(e) => this.add(e)}>
        <input type="text" placeholder=${t('areas.new')}>
        ${store.me.spaces.length > 1 ? select(store.me.spaces.map((s) => ({ id: s.id, title: s.name })), this.space, (v) => { this.space = v; }) : nothing}
        <button>${t('action.add')}</button>
      </form>
      ${this.list.map((a) => html`
        <div class="row ${a.state === 'retired' ? 'finished' : ''}" tabindex="0">
          <a class="row-main" href=${'/i/' + a.id}>
            <span class="title">${a.title}</span>
            <span class="meta">
              <span class="chip">${t('areas.projects', { n: a.projects })}</span>
              <span class="chip">${t('areas.actions', { n: a.actions })}</span>
              ${a.state === 'active' && !a.projects ? html`<span class="chip stuck">${t('areas.no_projects')}</span>` : nothing}
              ${a.state === 'retired' ? html`<span class="chip">${t('state.retired')}</span>` : nothing}
            </span>
          </a>
          <span class="row-actions">
            <button @click=${() => this.run(() => store.transition(a, a.state === 'active' ? 'retired' : 'active'))}>
              ${t(a.state === 'active' ? 'transition.retired' : 'transition.active')}</button>
          </span>
        </div>`)}
      ${this.list.length ? nothing : html`<p class="empty">${t('areas.empty')}</p>`}`;
  }
}
customElements.define('gtd-areas', Areas);

class Search extends View {
  static properties = { hits: { state: true } };
  firstUpdated() { this.querySelector('input').focus(); }
  async search(e) {
    const q = e.target.value.trim();
    clearTimeout(this._wait);
    this._wait = setTimeout(async () => {
      this.hits = q ? await this.run(() => store.search(q)) : null;
    }, 150);
  }
  render() {
    return html`
      <header class="bar"><h1>${t('nav.search')}</h1></header>
      <input class="search" type="search" placeholder=${t('search.placeholder')} @input=${(e) => this.search(e)}>
      ${(this.hits || []).map((h) => row(h, { state: true }))}
      ${this.hits && !this.hits.length ? html`<p class="empty">${t('search.empty')}</p>` : nothing}`;
  }
}
customElements.define('gtd-search', Search);

class Settings extends View {
  static properties = { space: { state: true }, yaml: { state: true }, error: { state: true } };
  constructor() {
    super();
    this.space = store.me.user.id;
  }
  async reload() {
    const res = await this.run(() => store.templates(this.space));
    if (res) this.yaml = res.yaml;
  }
  async setLang(lang) {
    await this.run(async () => {
      await store.setMe({ lang });
      await loadLanguage(lang);
      window.dispatchEvent(new PopStateEvent('popstate')); // redraw everything in the new language
      document.querySelector('gtd-app').requestUpdate();
      this.requestUpdate();
    });
  }
  async setContexts(e) {
    const list = e.target.value.split(',').map((s) => s.trim()).filter(Boolean);
    await this.run(async () => {
      await store.setMe({ personal_contexts: list });
      toast(t('settings.saved'));
    });
    this.requestUpdate();
  }
  async saveTemplates() {
    this.error = '';
    try {
      const res = await store.saveTemplates(this.space, this.querySelector('textarea').value);
      this.yaml = res.yaml;
      toast(t('settings.saved'));
    } catch (e) {
      this.error = e.message;
    }
  }
  render() {
    const me = store.me;
    return html`
      <header class="bar"><h1>${t('nav.settings')}</h1></header>
      <section class="settings">
        <p>${t('settings.signed_in', { name: me.user.name, server: me.server })}
          <button @click=${() => store.logout()}>${t('settings.logout')}</button></p>
        <div class="field"><label>${t('settings.language')}</label>
          ${select([{ id: 'en', title: 'English' }, { id: 'de', title: 'Deutsch' }], me.lang, (v) => this.setLang(v))}</div>
        <div class="field"><label>${t('settings.contexts')}</label>
          <input type="text" .value=${me.user.personal_contexts.join(', ')} @change=${(e) => this.setContexts(e)}>
          <small>${t('settings.contexts_hint')}</small></div>
        <h2>${t('settings.templates')}</h2>
        ${me.spaces.length > 1 ? select(me.spaces.map((s) => ({ id: s.id, title: s.name })), this.space, (v) => { this.space = v; this.reload(); }) : nothing}
        <textarea class="editor yaml" rows="16" spellcheck="false" .value=${this.yaml || ''}></textarea>
        <small>${t('settings.templates_hint')}</small>
        ${this.error ? html`<p class="error">${this.error}</p>` : nothing}
        <div class="buttons"><button class="primary" @click=${() => this.saveTemplates()}>${t('action.save')}</button></div>
      </section>`;
  }
}
customElements.define('gtd-settings', Settings);
