// The app shell: login gate, client-side routing, navigation, keyboard, toast,
// and the "next action?" prompt. The server returns the same shell for every
// UI route.
import { LitElement, html, nothing } from 'lit';
import { store } from './store.js';
import { loadLanguage, t } from './i18n.js';
import { go, toast, icon } from './widgets.js';
import './capture.js';
import './inbox.js';
import './wizard.js';
import './engage.js';
import './projects.js';
import './item.js';
import './board.js';
import './more.js';
import { newNote } from './notes.js';

const NAV = [
  ['/', 'nav.capture', 'capture'], ['/inbox', 'nav.inbox', 'inbox'], ['/engage', 'nav.engage', 'engage'], ['/projects', 'nav.projects', 'projects'],
  ['/notes', 'nav.notes', 'notes'], ['/board', 'nav.board', 'board'], ['/search', 'nav.search', 'search'], ['/areas', 'nav.areas', 'areas'],
  ['/settings', 'nav.settings', 'settings'],
];

class App extends LitElement {
  static properties = {
    path: { state: true }, ready: { state: true }, inbox: { state: true },
    toastMsg: { state: true }, prompt: { state: true }, menu: { state: true }, zen: { state: true }, theme: { state: true },
  };

  createRenderRoot() { return this; }

  constructor() {
    super();
    this.path = location.pathname;
    this.ready = false;
    this.inbox = 0;
    this.theme = window.gtdTheme();
  }

  async connectedCallback() {
    super.connectedCallback();
    window.addEventListener('popstate', () => { this.path = location.pathname; this.menu = false; });
    window.addEventListener('gtd-auth', () => this.requestUpdate());
    document.addEventListener('fullscreenchange', () => { this.zen = !!document.fullscreenElement; });
    window.addEventListener('gtd-changed', (e) => this.onChanged(e.detail));
    window.addEventListener('gtd-toast', (e) => this.showToast(e.detail));
    document.addEventListener('click', (e) => this.onClick(e));
    document.addEventListener('keydown', (e) => this.onKey(e));
    try {
      await store.loadMe();
    } catch { /* not logged in */ }
    await loadLanguage(store.me ? store.me.lang : (navigator.language || 'en').startsWith('de') ? 'de' : 'en');
    this.ready = true;
    if (store.me) this.count();
  }

  // Internal links navigate without a page load. This includes the item
  // chips inside server-rendered Markdown.
  onClick(e) {
    const a = e.target.closest?.('a[href]');
    if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || a.target || a.hasAttribute('download')) return;
    const href = a.getAttribute('href');
    if (!href.startsWith('/') || href.startsWith('/api/') || href.startsWith('/ui/')) return;
    e.preventDefault();
    go(href);
  }

  onKey(e) {
    if (e.metaKey || e.ctrlKey || e.altKey || !store.me) return;
    const el = e.target;
    if (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)) {
      if (e.key === 'Escape') el.blur();
      return;
    }
    const rows = [...this.querySelectorAll('main .row')];
    const at = rows.indexOf(document.activeElement);
    switch (e.key) {
      case 'c': go('/'); break;
      case '/': go('/search'); break;
      case 'e': go('/engage'); break;
      case 'n': newNote().catch((err) => toast(t(err.message), true)); break;
      case 'f': this.toggleZen(); break;
      case 'j': rows[Math.min(at + 1, rows.length - 1)]?.focus(); break;
      case 'k': rows[Math.max(at - 1, 0)]?.focus(); break;
      case 'x': rows[at]?.querySelector('[data-done]')?.click(); break;
      case 'Enter': if (at >= 0 && el === rows[at]) rows[at].querySelector('a')?.click(); break;
      default: return;
    }
    e.preventDefault();
  }

  // Full screen: the browser's own full screen, so that tabs, address bar and
  // the desktop are out of sight. The app's bar stays.
  toggleZen() {
    this.menu = false;
    try {
      if (document.fullscreenElement) document.exitFullscreen?.();
      else document.documentElement.requestFullscreen?.().catch(() => {});
    } catch { /* not available on this device */ }
  }

  cycleTheme() {
    const next = { light: 'dark', dark: 'system', system: 'light' }[this.theme] || 'light';
    this.theme = window.gtdTheme(next);
  }

  async count() {
    try {
      const inbox = await store.inbox();
      this.inbox = inbox.captures.length + inbox.notifications.length;
    } catch { /* shown elsewhere */ }
  }

  onChanged(detail) {
    this.count();
    const p = detail?.prompts?.[0];
    if (p) this.prompt = { ...p, text: '' };
  }

  showToast(detail) {
    this.toastMsg = detail;
    clearTimeout(this._toast);
    this._toast = setTimeout(() => { this.toastMsg = null; }, detail.isError || detail.action ? 7000 : 1800);
  }

  async toastAction() {
    const action = this.toastMsg.action;
    this.toastMsg = null;
    try {
      await action.run();
    } catch (e) {
      toast(t(e.message), true);
    }
  }

  async onLogin() {
    await loadLanguage(store.me.lang);
    this.count();
    this.requestUpdate();
  }

  view() {
    const p = this.path;
    const q = new URLSearchParams(location.search);
    let m;
    if (p === '/') return html`<gtd-capture></gtd-capture>`;
    if (p === '/inbox') return html`<gtd-inbox></gtd-inbox>`;
    if (p === '/clarify') return html`<gtd-clarify .item=${q.get('item') || ''}></gtd-clarify>`;
    if ((m = p.match(/^\/process\/([^/]+)$/))) return html`<gtd-process .item=${m[1]}></gtd-process>`;
    if (p === '/engage') return html`<gtd-engage></gtd-engage>`;
    if (p === '/projects') return html`<gtd-projects></gtd-projects>`;
    if ((m = p.match(/^\/p\/([^/]+)$/))) return html`<gtd-plan .project=${m[1]}></gtd-plan>`;
    if ((m = p.match(/^\/i\/([^/]+)$/))) return html`<gtd-note .itemId=${m[1]}></gtd-note>`;
    if (p === '/notes') return html`<gtd-notes></gtd-notes>`;
    if (p === '/help') return html`<gtd-help></gtd-help>`;
    if (p === '/board') return html`<gtd-board></gtd-board>`;
    if (p === '/areas') return html`<gtd-areas></gtd-areas>`;
    if (p === '/search') return html`<gtd-search></gtd-search>`;
    if (p === '/settings') return html`<gtd-settings></gtd-settings>`;
    return html`<p class="empty">${t('app.not_found')}</p>`;
  }

  // The prompt after completing a project's last open action (SPEC §5.3).
  async answerPrompt(kind) {
    const p = this.prompt;
    this.prompt = null;
    try {
      if (kind === 'add' && p.text.trim()) {
        await store.create(p.space, { kind: 'action', title: p.text.trim(), project: p.project });
      } else if (kind === 'complete') {
        await store.ops(p.space, [{ type: 'transition', item: p.project, base_rev: p.rev, changes: [{ field: 'state', new: 'done' }] }]);
      }
    } catch (e) {
      toast(t(e.message), true);
    }
  }

  render() {
    if (!this.ready) return nothing;
    if (!store.me) return html`<gtd-login @login=${() => this.onLogin()}></gtd-login>`;
    const active = (href) => (href === '/' ? this.path === '/' : this.path.startsWith(href));
    const item = ([href, key, name]) => html`
      <a href=${href} class="nav-item ${active(href) ? 'on' : ''}">
        ${icon[name]}<span class="text">${t(key)}</span>
        ${href === '/inbox' && this.inbox ? html`<span class="count">${this.inbox}</span>` : nothing}
      </a>`;
    return html`
      <nav>
        <a class="brand" href="/">gtd</a>
        ${NAV.slice(0, 4).map(item)}
        <div class="nav-more ${this.menu ? 'open' : ''}">
          ${NAV.slice(4).map(item)}
          <span class="nav-gap"></span>
          <a class="nav-item tool ${active('/help') ? 'on' : ''}" href="/help" title=${t('nav.help')}>${icon.help}<span class="text">${t('nav.help')}</span></a>
          <button class="nav-item tool" title=${t('nav.theme')} @click=${() => this.cycleTheme()}>
            ${this.theme === 'dark' ? icon.moon : icon.sun}<span class="text">${t('nav.theme')}: ${t('theme.' + this.theme)}</span></button>
          <button class="nav-item tool ${this.zen ? 'on' : ''}" title=${t('nav.fullscreen')} @click=${() => this.toggleZen()}>
            ${icon.expand}<span class="text">${t('nav.fullscreen')}</span></button>
        </div>
        <button class="nav-item more ${NAV.slice(4).some(([h]) => active(h)) ? 'on' : ''}" aria-expanded=${!!this.menu}
          @click=${() => { this.menu = !this.menu; }}>
          ${icon.more}<span class="text">${t('nav.more')}</span>
        </button>
      </nav>
      ${this.menu ? html`<div class="scrim" @click=${() => { this.menu = false; }}></div>` : nothing}
      <main>${this.view()}</main>
      ${this.toastMsg ? html`<div class="toast ${this.toastMsg.isError ? 'error' : ''}" role="status">${this.toastMsg.message}
        ${this.toastMsg.action ? html`<button @click=${() => this.toastAction()}>${this.toastMsg.action.label}</button>` : nothing}</div>` : nothing}
      ${this.prompt ? html`
        <div class="overlay">
          <form class="dialog" @submit=${(e) => { e.preventDefault(); this.answerPrompt('add'); }}>
            <h2>${t('prompt.next_action')}</h2>
            <p class="subject">${this.prompt.title}</p>
            <input type="text" autofocus placeholder=${t('field.action')} .value=${this.prompt.text}
              @input=${(e) => { this.prompt.text = e.target.value; }}>
            <div class="buttons">
              <button class="primary" type="submit">${t('prompt.add')}</button>
              <button type="button" @click=${() => this.answerPrompt('complete')}>${t('prompt.complete')}</button>
              <button type="button" class="link" @click=${() => this.answerPrompt('later')}>${t('prompt.later')}</button>
            </div>
          </form>
        </div>` : nothing}`;
  }
}
customElements.define('gtd-app', App);

class Login extends LitElement {
  static properties = { error: { state: true }, busy: { state: true } };
  createRenderRoot() { return this; }
  async submit(e) {
    e.preventDefault();
    const f = new FormData(e.target);
    this.busy = true;
    this.error = '';
    try {
      await store.login(f.get('user'), f.get('password'));
      this.dispatchEvent(new CustomEvent('login'));
    } catch (err) {
      this.error = t(err.message);
    }
    this.busy = false;
  }
  render() {
    return html`
      <form class="login" @submit=${(e) => this.submit(e)}>
        <h1>gtd</h1>
        <label>${t('login.user')}<input name="user" autocomplete="username" autocapitalize="none" required autofocus></label>
        <label>${t('login.password')}<input name="password" type="password" autocomplete="current-password" required></label>
        ${this.error ? html`<p class="error">${this.error}</p>` : nothing}
        <button class="primary" ?disabled=${this.busy}>${t('login.submit')}</button>
      </form>`;
  }
}
customElements.define('gtd-login', Login);
