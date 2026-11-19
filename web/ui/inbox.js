// Inbox: captures (oldest first) and "From others" (SPEC §5.1).
import { store } from './store.js';
import { t, fmtTime } from './i18n.js';
import { View, html, nothing, row } from './widgets.js';

class Inbox extends View {
  static properties = { data: { state: true } };

  async reload() {
    this.data = await this.run(() => store.inbox());
  }

  render() {
    const d = this.data;
    if (!d) return nothing;
    return html`
      <header class="bar">
        <h1>${t('nav.inbox')}</h1>
        ${d.captures.length ? html`<a class="button primary" href="/clarify">${t('inbox.process', { n: d.captures.length })}</a>` : nothing}
      </header>
      <h2>${t('inbox.captures')}</h2>
      ${d.captures.length ? d.captures.map((c) => row(c)) : html`<p class="empty">${t('inbox.zero')}</p>`}
      <h2>${t('inbox.from_others')}</h2>
      ${d.notifications.length ? d.notifications.map((n) => html`
        <div class="row">
          <a class="row-main" href=${'/i/' + n.item}>
            <span class="title">${n.title}</span>
            <span class="meta"><span class="chip unblocked">${t('notification.' + n.type)}</span>
              <span class="chip">${n.from}</span><span class="chip">${fmtTime(n.ts)}</span></span>
          </a>
          <span class="row-actions"><button @click=${() => this.run(() => store.dismiss(n.seq))}>${t('action.dismiss')}</button></span>
        </div>`) : html`<p class="empty">${t('inbox.nothing_from_others')}</p>`}`;
  }
}
customElements.define('gtd-inbox', Inbox);
