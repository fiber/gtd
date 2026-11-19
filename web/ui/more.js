// The smaller screens: areas, search, settings.
import { store } from './store.js';
import { t, loadLanguage, fmtDay } from './i18n.js';
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
          <a class="row-main" href=${'i/' + a.id}>
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
  static properties = { res: { state: true } };
  firstUpdated() { this.querySelector('input').focus(); }
  async search(e) {
    const q = e.target.value.trim();
    clearTimeout(this._wait);
    this._wait = setTimeout(async () => {
      this.res = q ? await this.run(() => store.search(q)) : null;
    }, 200);
  }
  render() {
    const r = this.res;
    const exact = (r?.hits || []).filter((h) => !h.similar);
    const similar = (r?.hits || []).filter((h) => h.similar);
    return html`
      <header class="bar"><h1>${t('nav.search')}</h1></header>
      <input class="search" type="search" placeholder=${t('search.placeholder')} @input=${(e) => this.search(e)}>
      ${exact.map((h) => row(h, { state: true }))}
      ${similar.length ? html`<h2>${t('search.similar')}</h2>${similar.map((h) => row(h, { state: true }))}` : nothing}
      ${r && !r.hits.length ? html`<p class="empty">${t('search.empty')}</p>` : nothing}
      ${r && (r.semantic === 'offline' || r.semantic === 'building') ? html`<p class="notice">${t('ai.note.' + r.semantic)}</p>` : nothing}`;
  }
}
customElements.define('gtd-search', Search);

class Settings extends View {
  static properties = { space: { state: true }, yaml: { state: true }, error: { state: true }, ai: { state: true }, links: { state: true }, linkError: { state: true }, newToken: { state: true }, tokens: { state: true }, madeToken: { state: true } };
  constructor() {
    super();
    this.space = store.me.user.id;
  }
  async reload() {
    const res = await this.run(() => store.templates(this.space));
    if (res) this.yaml = res.yaml;
    this.ai = await this.run(() => store.aiStatus());
    this.links = await this.run(() => store.links());
    this.tokens = await this.run(() => store.tokens());
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
  async setSpaceContexts(sp, e) {
    const list = e.target.value.split(',').map((s) => s.trim()).filter(Boolean);
    await this.run(async () => {
      sp.contexts = (await store.setSpaceContexts(sp.id, list)).contexts;
      toast(t('settings.saved'));
    });
    this.requestUpdate();
  }
  async addToken(e) {
    e.preventDefault();
    const f = e.target;
    this.madeToken = await this.run(() => store.addToken(f.label.value));
    f.reset();
    this.tokens = await this.run(() => store.tokens());
  }
  async removeToken(id) {
    await this.run(() => store.removeToken(id));
    this.tokens = await this.run(() => store.tokens());
  }
  // A linked server: without an address the other side connects to us and we
  // show the token to enter there; with one, this server connects.
  async addLink(e) {
    e.preventDefault();
    const f = e.target;
    this.linkError = this.newToken = null;
    try {
      const res = await store.addLink({ name: f.name.value, ssh: f.ssh.value, target: f.target.value, token: f.token.value });
      this.links = res;
      this.newToken = res.links.find((l) => l.token);
      f.reset();
    } catch (err) {
      this.linkError = t(err.message);
    }
  }
  async removeLink(name) {
    this.links = await this.run(() => store.removeLink(name));
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
  async changePassword(e) {
    e.preventDefault();
    const f = e.target;
    this.pwError = null;
    if (f.password.value !== f.repeat.value) {
      this.pwError = { field: 'repeat', key: 'password.mismatch' };
    } else {
      try {
        await store.changePassword(f.current.value, f.password.value);
        f.reset();
        toast(t('settings.password_changed'));
      } catch (err) {
        this.pwError = { field: err.field || 'current', key: err.message };
      }
    }
    this.requestUpdate();
  }
  render() {
    const me = store.me;
    return html`
      <header class="bar"><h1>${t('nav.settings')}</h1></header>
      <section class="settings">
        <p>${t('settings.signed_in', { name: me.user.name, server: me.server })}
          <button @click=${() => store.logout()}>${t('settings.logout')}</button></p>
        <form class="password" @submit=${(e) => this.changePassword(e)}>
          <div class="field ${this.pwError?.field === 'current' ? 'invalid' : ''}"><label>${t('settings.password_current')}</label>
            <input type="password" name="current" autocomplete="current-password" required></div>
          <div class="field ${this.pwError?.field === 'password' ? 'invalid' : ''}"><label>${t('settings.password_new')}</label>
            <input type="password" name="password" autocomplete="new-password" minlength="8" maxlength="72" required></div>
          <div class="field ${this.pwError?.field === 'repeat' ? 'invalid' : ''}"><label>${t('settings.password_repeat')}</label>
            <input type="password" name="repeat" autocomplete="new-password" required></div>
          ${this.pwError ? html`<p class="error">${t(this.pwError.key)}</p>` : nothing}
          <button type="submit">${t('settings.password_change')}</button>
        </form>
        <div class="field"><label>${t('settings.language')}</label>
          ${select([{ id: 'en', title: 'English' }, { id: 'de', title: 'Deutsch' }], me.lang, (v) => this.setLang(v))}</div>
        <div class="field"><label>${t('settings.review_weekday')}</label>
          ${select([1, 2, 3, 4, 5, 6, 7].map((d) => ({ id: String(d), title: new Intl.DateTimeFormat(me.lang, { weekday: 'long' }).format(new Date(2024, 0, d)) })),
            String(me.review.weekday || ''), async (v) => { await this.run(() => store.setMe({ review_weekday: Number(v) })); this.requestUpdate(); document.querySelector('gtd-app').requestUpdate(); },
            t('option.none'))}
          <small>${t('settings.review_hint')}</small></div>
        <div class="field"><label>${t('nav.theme')}</label>
          ${select([{ id: 'light', title: t('theme.light') }, { id: 'dark', title: t('theme.dark') }, { id: 'system', title: t('theme.system') }],
            window.gtdTheme(), (v) => { window.gtdTheme(v); document.querySelector('gtd-app').theme = v; })}</div>
        <div class="field"><label>${t('settings.contexts')}</label>
          <input type="text" .value=${me.user.personal_contexts.join(', ')} @change=${(e) => this.setContexts(e)}>
          <small>${t('settings.contexts_hint')}</small></div>
        <div class="field"><label>${t('settings.aliases')}</label>
          <input type="text" .value=${(me.user.aliases || []).join(', ')} @change=${async (e) => {
            const list = e.target.value.split(',').map((x) => x.trim()).filter(Boolean);
            await this.run(async () => { await store.setMe({ aliases: list }); toast(t('settings.saved')); });
          }}>
          <small>${t('settings.aliases_hint')}</small></div>
        <h2>${t('settings.ai')}</h2>
        <p><small>${me.ai?.on ? t('settings.ai_about', { model: me.ai.name }) : t('settings.ai_none')}</small></p>
        ${me.ai?.on ? me.spaces.map((sp) => html`
          <div class="field"><label>${sp.id === me.user.id ? t('clarify.opt.personal') : sp.name}</label>
            ${select(['off', 'local', 'any'].map((x) => ({ id: x, title: t('ai.policy.' + x) })), sp.ai || 'local', async (v) => {
              await this.run(async () => { await store.setAIPolicy(sp.id, v); sp.ai = v; toast(t('settings.saved')); });
              this.requestUpdate();
            })}</div>`) : nothing}
        ${me.spaces.filter((sp) => sp.kind === 'shared').map((sp) => html`
          <div class="field"><label>${t('settings.space_contexts', { name: sp.name })}</label>
            <input type="text" .value=${(sp.contexts || []).join(', ')} @change=${(e) => this.setSpaceContexts(sp, e)}>
            <small>${t('settings.space_contexts_hint')}</small></div>`)}
        ${this.ai ? html`<p class="notice">${t('ai.label')}: ${t('ai.state.' + this.ai.state)}
          ${this.ai.state === 'off' ? '' : ' · ' + t('ai.counts', { vectors: this.ai.vectors, queued: this.ai.queued })}
          ${this.ai.error ? html`<br><small>${this.ai.error}</small>` : nothing}</p>` : nothing}
        ${me.user.admin ? html`<p><a class="button" href="admin">${t('nav.admin')}</a></p>` : nothing}
        <h2>${t('tokens.title')}</h2>
        <p><small>${t('tokens.about')}</small></p>
        ${(this.tokens || []).filter((k) => !k.link).map((k) => html`
          <div class="row"><span class="row-main"><span class="title">${k.label || k.id}</span>
            <span class="meta"><span class="chip">${k.id}</span>
              <span class="chip">${k.last_used ? t('tokens.used', { date: fmtDay(k.last_used) }) : t('tokens.never_used')}</span></span></span>
            <span class="row-actions"><button @click=${() => this.removeToken(k.id)}>${t('tokens.revoke')}</button></span></div>`)}
        ${this.madeToken ? html`<p class="notice">${t('tokens.once')}<br><code class="token">${this.madeToken.token}</code></p>` : nothing}
        <form class="password" @submit=${(e) => this.addToken(e)}>
          <div class="field"><label>${t('tokens.label')}</label><input type="text" name="label" required maxlength="40"></div>
          <button type="submit">${t('tokens.add')}</button>
        </form>
        <h2>${t('links.title')}</h2>
        <p><small>${t('links.about')}</small></p>
        ${(this.links?.links || []).map((l) => html`
          <div class="row"><span class="row-main"><span class="title">${l.name}</span>
            <span class="meta"><span class="chip">${l.ssh ? t('links.dials', { ssh: l.ssh }) : t('links.waits')}</span>
              ${l.fingerprint ? html`<span class="chip">${l.fingerprint}</span>` : nothing}
              ${l.waiting ? html`<span class="chip waiting">${t('links.waiting', { n: l.waiting })}</span>` : nothing}</span></span>
            <span class="row-actions"><button @click=${() => this.removeLink(l.name)}>${t('links.remove')}</button></span></div>`)}
        ${this.newToken ? html`<p class="notice">${t('links.token_once', { name: this.newToken.name })}<br><code class="token">${this.newToken.token}</code></p>` : nothing}
        <form class="password" @submit=${(e) => this.addLink(e)}>
          <div class="field"><label>${t('links.name')}</label><input type="text" name="name" required maxlength="30"></div>
          <div class="field"><label>${t('links.ssh')}</label><input type="text" name="ssh" placeholder="gtd@host" autocapitalize="off">
            <small>${t('links.ssh_hint')}</small></div>
          <div class="field"><label>${t('links.target')}</label><input type="text" name="target" placeholder="127.0.0.1:8640" autocapitalize="off"></div>
          <div class="field"><label>${t('links.token')}</label><input type="text" name="token" autocomplete="off" autocapitalize="off"></div>
          ${this.linkError ? html`<p class="error">${this.linkError}</p>` : nothing}
          <button type="submit">${t('links.add')}</button>
        </form>
        ${this.links?.key ? html`<p><small>${t('links.key')}</small><br><code class="token">${this.links.key}</code></p>` : nothing}
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
