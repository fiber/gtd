// Notes: the home of note-taking. All notes of your spaces, newest first, and
// a second tab for ideas (someday themes and someday actions). Also the help
// page, which shows the tutorial.
import { store } from './store.js';
import { t, fmtTime } from './i18n.js';
import { View, html, nothing, row, select, go } from './widgets.js';

// newNote creates a note and opens it in the editor. Inside a project it is
// filed there; otherwise it starts unsorted in the personal space.
export async function newNote(space, project) {
  const res = await store.create(space || store.me.user.id, {
    kind: 'note', state: project ? 'filed' : 'unsorted', project: project || '',
    title: t('note.new_title', { date: fmtTime(new Date().toISOString()) }),
  });
  go('/i/' + res.ops[0].item + '?edit=1');
}

const STATES = ['', 'unsorted', 'filed', 'archived'];

class Notes extends View {
  static properties = {
    tab: { state: true }, state: { state: true }, project: { state: true }, q: { state: true }, similar: { state: true },
    data: { state: true }, ideas: { state: true },
  };

  constructor() {
    super();
    this.tab = new URLSearchParams(location.search).get('tab') === 'ideas' ? 'ideas' : 'notes';
    this.state = this.project = this.q = this.similar = '';
  }

  async reload() {
    if (this.tab === 'ideas') {
      this.ideas = await this.run(() => store.ideas());
    } else {
      this.data = await this.run(() => store.notes({ state: this.state, project: this.project, q: this.q, similar: this.similar }));
    }
  }

  set(key, value) {
    this[key] = value;
    this.reload();
  }

  search(e) {
    clearTimeout(this._wait);
    const q = e.target.value;
    this._wait = setTimeout(() => this.set('q', q), 200);
  }

  notesTab() {
    const d = this.data;
    if (!d) return nothing;
    const total = Object.values(d.counts).reduce((a, b) => a + b, 0);
    return html`
      <div class="filters notes-filters">
        <div class="tabs">${STATES.map((s) => html`
          <button class=${this.state === s ? 'on' : ''} @click=${() => this.set('state', s)}>
            ${s ? t('state.' + s) : t('notes.all')} <span class="n">${s ? d.counts[s] || 0 : total}</span></button>`)}</div>
        ${d.counts.unsorted > 1 ? html`<a class="button" href="sweep">${t('notes.sweep', { n: d.counts.unsorted })}</a>` : nothing}
        <input type="search" placeholder=${t('notes.filter')} @input=${(e) => this.search(e)}>
        ${select(d.projects, this.project, (v) => this.set('project', v), t('board.all_projects'))}
      </div>
      ${d.similar ? html`<p class="notice similar-to">
        <span>${t('notes.similar_to', { title: d.similar.title })}${d.similar.state === 'ok' ? '' : ' · ' + t('ai.state.' + d.similar.state)}</span>
        <button @click=${() => this.set('similar', '')}>${t('notes.similar_clear')}</button></p>` : nothing}
      ${d.notes.map((n) => html`
        <a class="note-card" href=${'i/' + n.id}>
          <span class="title">${n.title}</span>
          ${n.excerpt ? html`<span class="excerpt">${n.excerpt}</span>` : nothing}
          <span class="meta">
            ${n.state === 'unsorted' ? html`<span class="chip waiting">${t('state.unsorted')}</span>` : nothing}
            ${n.state === 'archived' ? html`<span class="chip">${t('state.archived')}</span>` : nothing}
            ${n.project_title ? html`<span class="chip project">${n.project_title}</span>` : nothing}
            ${n.area_title ? html`<span class="chip context">${n.area_title}</span>` : nothing}
            ${store.me.spaces.length > 1 ? html`<span class="chip">${n.space_name}</span>` : nothing}
            ${n.attachments ? html`<span class="chip">${t('item.files', { n: n.attachments })}</span>` : nothing}
            ${n.score ? html`<span class="chip link">${Math.round(n.score * 100)}%</span>` : nothing}
            <span class="when">${fmtTime(n.updated)}</span>
            <button class="similar" title=${t('notes.similar')} @click=${(e) => { e.preventDefault(); this.set('similar', n.id); window.scrollTo(0, 0); }}>≈ ${t('notes.similar')}</button>
          </span>
        </a>`)}
      ${d.notes.length ? nothing : html`<p class="empty">${t(d.similar ? 'notes.similar_none' : 'notes.empty')}</p>`}`;
  }

  ideasTab() {
    const d = this.ideas;
    if (!d) return nothing;
    const move = (b, state) => this.run(() => store.transition(b, state));
    return html`
      <h2>${t('ideas.themes')}</h2>
      ${d.themes.map((p) => row(p, { href: '/p/' + p.id, area: true, actions: html`
        <span class="chip">${t('ideas.notes', { n: p.notes || 0 })}</span>
        <button @click=${() => move(p, 'active')}>${t('transition.active')}</button>
        <button @click=${() => move(p, 'dropped')}>${t('transition.dropped')}</button>` }))}
      ${d.themes.length ? nothing : html`<p class="empty">${t('ideas.no_themes')}</p>`}
      <h2>${t('ideas.actions')}</h2>
      ${d.actions.map((a) => row(a, { actions: html`
        <button @click=${() => move(a, 'next')}>${t('transition.next')}</button>
        <button @click=${() => move(a, 'dropped')}>${t('transition.dropped')}</button>` }))}
      ${d.actions.length ? nothing : html`<p class="empty">${t('ideas.no_actions')}</p>`}`;
  }

  render() {
    const tab = (key) => html`<button class=${this.tab === key ? 'on' : ''} @click=${() => this.set('tab', key)}>${t('notes.tab.' + key)}</button>`;
    return html`
      <header class="bar">
        <h1>${t('nav.notes')}</h1>
        <div class="tabs">${tab('notes')}${tab('ideas')}</div>
        <button class="primary" @click=${() => this.run(() => newNote('', this.project))}>${t('note.new')}</button>
      </header>
      ${this.tab === 'ideas' ? this.ideasTab() : this.notesTab()}`;
  }
}
customElements.define('gtd-notes', Notes);

class Help extends View {
  static properties = { html: { state: true } };
  async reload() {
    const res = await this.run(() => store.help());
    if (res) this.html = res.html;
  }
  render() {
    return html`<article class="body help" .innerHTML=${this.html || ''}></article>`;
  }
}
customElements.define('gtd-help', Help);
