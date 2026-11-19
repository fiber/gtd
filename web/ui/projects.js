// Projects: the project tree per space, and the plan view of one project
// (SPEC §6.4). Roll-ups, liveness and warnings come from the server.
import { store } from './store.js';
import { t, fmtTime } from './i18n.js';
import { View, html, nothing, row, meta, go, toast, select } from './widgets.js';

// tree orders briefs depth-first by parent and returns [brief, depth] pairs.
function tree(list, rootParent = '') {
  const kids = new Map();
  const ids = new Set(list.map((p) => p.id));
  for (const p of list) {
    const parent = ids.has(p.parent) ? p.parent : rootParent;
    kids.set(parent, [...(kids.get(parent) || []), p]);
  }
  const out = [];
  const walk = (parent, depth) => {
    for (const p of kids.get(parent) || []) {
      out.push([p, depth]);
      walk(p.id, depth + 1);
    }
  };
  walk(rootParent, 0);
  return out;
}

class Projects extends View {
  static properties = { list: { state: true }, finished: { state: true }, space: { state: true } };

  constructor() {
    super();
    this.space = store.me.user.id;
  }

  async reload() {
    this.list = await this.run(() => store.projects());
  }

  async add(e) {
    e.preventDefault();
    const input = e.target.querySelector('input');
    const title = input.value.trim();
    if (!title) return;
    await this.run(async () => {
      await store.create(this.space, { kind: 'project', title });
      input.value = '';
    });
  }

  render() {
    if (!this.list) return nothing;
    const open = (p) => this.finished || !['done', 'dropped'].includes(p.state);
    return html`
      <header class="bar">
        <h1>${t('nav.projects')}</h1>
        <label class="check"><input type="checkbox" .checked=${!!this.finished}
          @change=${(e) => { this.finished = e.target.checked; }}> ${t('projects.show_finished')}</label>
      </header>
      <form class="inline" @submit=${(e) => this.add(e)}>
        <input type="text" placeholder=${t('projects.new')}>
        ${store.me.spaces.length > 1 ? select(store.me.spaces.map((s) => ({ id: s.id, title: s.name })), this.space, (v) => { this.space = v; }) : nothing}
        <button>${t('action.add')}</button>
      </form>
      ${store.me.spaces.map((sp) => {
        const mine = this.list.filter((p) => p.space === sp.id && open(p));
        if (!mine.length) return nothing;
        return html`
          ${store.me.spaces.length > 1 ? html`<h2>${sp.name}</h2>` : nothing}
          ${tree(mine).map(([p, depth]) => html`
            <div class="indent" style=${`--depth:${depth}`}>${row(p, { href: '/p/' + p.id, state: p.state !== 'active', area: true })}</div>`)}`;
      })}
      ${this.list.length ? nothing : html`<p class="empty">${t('projects.empty')}</p>`}`;
  }
}
customElements.define('gtd-projects', Projects);

class Plan extends View {
  static properties = { project: {}, plan: { state: true }, comments: { state: true }, closed: { state: true }, over: { state: true } };

  constructor() {
    super();
    this.closed = [];
  }

  willUpdate(changed) {
    if (changed.has('project') && changed.get('project') !== undefined) this.reload();
  }

  async reload() {
    this.plan = await this.run(() => store.plan(this.project));
    this.comments = (await this.run(() => store.comments(this.project))) || [];
  }

  get root() { return this.plan.projects[0]; }

  title(id) {
    const all = [...this.plan.projects, ...this.plan.actions];
    return all.find((x) => x.id === id)?.title || '';
  }

  async addAction(e, project) {
    e.preventDefault();
    const input = e.target.querySelector('input');
    const title = input.value.trim();
    if (!title) return;
    await this.run(async () => {
      await store.create(this.plan.space, { kind: 'action', title, project: project.id, area: project.area || '' });
      input.value = '';
    });
  }

  // Thread quick-add: a line into the project's thread, e.g. "call Brent
  // about Equinix API", and one tap to turn a thread entry into an action.
  async addComment(e) {
    e.preventDefault();
    const input = e.target.querySelector('input');
    if (!input.value.trim()) return;
    await this.run(async () => {
      await store.comment(this.root, input.value.trim());
      input.value = '';
    });
  }

  async makeAction(c) {
    const title = c.body.split('\n')[0].slice(0, 200);
    await this.run(async () => {
      await store.create(this.plan.space, { kind: 'action', title, project: this.root.id, area: this.root.area || '' });
      toast(t('thread.action_created'));
    });
  }

  // One tap files a suggested note into this project.
  async fileHere(n) {
    const ops = [{ type: 'patch', item: n.id, base_rev: n.rev, changes: [{ field: 'project', new: this.root.id }] }];
    if (n.state === 'unsorted') {
      ops.push({ type: 'transition', item: n.id, base_rev: n.rev + 1, changes: [{ field: 'state', new: 'filed' }] });
    }
    await this.run(() => store.ops(this.plan.space, ops));
  }

  async newNote() {
    const res = await this.run(() => store.create(this.plan.space, {
      kind: 'note', state: 'filed', project: this.root.id, title: t('note.new_title', { date: fmtTime(new Date().toISOString()) }),
    }));
    if (res) go('/i/' + res.ops[0].item + '?edit=1');
  }

  // Drag and drop: an action dropped on an action waits for it; a project
  // dropped on a project becomes its child. The server checks both.
  drag(e, kind, id) {
    e.dataTransfer.setData('text/plain', kind + ':' + id);
    e.dataTransfer.effectAllowed = 'link';
  }

  async drop(e, kind, target) {
    e.preventDefault();
    this.over = null;
    const [k, id] = (e.dataTransfer.getData('text/plain') || '').split(':');
    if (k !== kind || !id || id === target.id) return;
    await this.run(async () => {
      const dragged = await store.item(id);
      if (kind === 'action') {
        await store.patch(dragged, { blocked_by: [...new Set([...(dragged.blocked_by || []), target.id])] });
        toast(t('plan.now_waits', { a: dragged.title, b: target.title }));
      } else {
        await store.patch(dragged, { parent: target.id });
      }
    });
  }

  dropTarget(kind, b) {
    return {
      over: (e) => { e.preventDefault(); this.over = b.id; },
      leave: () => { if (this.over === b.id) this.over = null; },
      drop: (e) => this.drop(e, kind, b),
    };
  }

  node(p, depth) {
    const actions = this.plan.actions.filter((a) => a.project === p.id);
    const areas = [...new Set(actions.map((a) => a.area_title || ''))].sort();
    const closed = this.closed.includes(p.id);
    const dt = this.dropTarget('project', p);
    return html`
      <section class="plan-node indent ${this.over === p.id ? 'over' : ''}" style=${`--depth:${depth}`}
        @dragover=${dt.over} @dragleave=${dt.leave} @drop=${dt.drop}>
        <div class="plan-head" draggable="true" @dragstart=${(e) => this.drag(e, 'project', p.id)}>
          <button class="toggle" aria-expanded=${!closed}
            @click=${() => { this.closed = closed ? this.closed.filter((x) => x !== p.id) : [...this.closed, p.id]; }}>${closed ? '▸' : '▾'}</button>
          <a class="title" href=${'/i/' + p.id}>${p.title}</a>
          <span class="meta">${meta(p, { state: p.state !== 'active', area: true })}</span>
        </div>
        ${closed ? nothing : html`
          ${areas.map((area) => html`
            ${area ? html`<h3>${area}</h3>` : nothing}
            ${actions.filter((a) => (a.area_title || '') === area).map((a) => {
              const at = this.dropTarget('action', a);
              return html`
                <div class="plan-action ${this.over === a.id ? 'over' : ''} ${['done', 'dropped'].includes(a.state) ? 'finished' : ''}"
                  draggable="true" @dragstart=${(e) => { e.stopPropagation(); this.drag(e, 'action', a.id); }}
                  @dragover=${(e) => { e.stopPropagation(); at.over(e); }} @dragleave=${at.leave}
                  @drop=${(e) => { e.stopPropagation(); at.drop(e); }}>
                  ${row(a, { state: true, noProject: true })}
                </div>`;
            })}`)}
          <form class="inline" @submit=${(e) => this.addAction(e, p)}>
            <input type="text" placeholder=${t('plan.add_action')}><button>${t('action.add')}</button>
          </form>`}
      </section>`;
  }

  render() {
    const pl = this.plan;
    if (!pl) return nothing;
    return html`
      <header class="bar">
        <a class="back" href="/projects">‹ ${t('nav.projects')}</a>
        <a class="button" href=${'/i/' + this.root.id}>${t('plan.details')}</a>
      </header>
      ${pl.warnings.length ? html`<ul class="warnings">${pl.warnings.map((w) => html`
        <li>${t('warning.' + w.Type, { item: this.title(w.Item), other: this.title(w.Other) })}</li>`)}</ul>` : nothing}
      ${tree(pl.projects).map(([p, depth]) => this.node(p, depth))}

      ${pl.suggested.length ? html`
        <h2>${t('plan.belong_here')}</h2>
        ${pl.suggested.map((n) => row(n, { actions: html`<button @click=${() => this.fileHere(n)}>${t('plan.file_here')}</button>` }))}` : nothing}
      <h2>${t('plan.notes')}</h2>
      ${pl.notes.map((n) => row(n, { noProject: true }))}
      <button @click=${() => this.newNote()}>${t('note.new')}</button>

      <h2>${t('thread.title')}</h2>
      <form class="inline" @submit=${(e) => this.addComment(e)}>
        <input type="text" placeholder=${t('thread.quick_add')}><button>${t('action.add')}</button>
      </form>
      ${[...(this.comments || [])].reverse().filter((c) => !c.deleted).slice(0, 8).map((c) => html`
        <div class="comment">
          <div class="comment-head"><b>${c.author_name}</b> <span>${fmtTime(c.ts)}</span>
            <button class="link" @click=${() => this.makeAction(c)}>${t('thread.make_action')}</button></div>
          <div class="body" .innerHTML=${c.body_html}></div>
        </div>`)}`;
  }
}
customElements.define('gtd-plan', Plan);
