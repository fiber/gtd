// Administration for the users listed under `admins` in the server's config:
// users and shared spaces. It asks for the admin's password and stays open
// for a quarter of an hour. Backups, export and import stay on the command
// line.
import { store } from './store.js';
import { t } from './i18n.js';
import { View, html, nothing, toast } from './widgets.js';

class Admin extends View {
  static properties = { locked: { state: true }, users: { state: true }, spaces: { state: true }, error: { state: true } };

  async reload() {
    try {
      [this.users, this.spaces] = await Promise.all([store.adminUsers(), store.adminSpaces()]);
      this.locked = false;
    } catch (e) {
      this.locked = true;
      if (e.message !== 'admin.locked') this.error = t(e.message);
    }
  }

  // act runs an admin call, shows what went wrong, and reloads the lists.
  async act(f, done) {
    this.error = null;
    try {
      await f();
      if (done) toast(t(done));
    } catch (e) {
      if (e.message === 'admin.locked') this.locked = true;
      else this.error = t(e.message);
    }
    if (!this.locked) await this.reload();
  }

  async unlock(e) {
    e.preventDefault();
    this.error = null;
    try {
      await store.adminUnlock(e.target.password.value);
      await this.reload();
    } catch (err) {
      this.error = t(err.message);
    }
  }

  addUser(e) {
    e.preventDefault();
    const f = e.target;
    const id = f.id.value.trim().toLowerCase();
    this.act(async () => {
      await store.adminAddUser({ id, name: f.name.value.trim() });
      if (f.password.value) await store.adminSetPassword(id, f.password.value);
      f.reset();
    }, 'admin.user_added');
  }

  setPassword(e, id) {
    e.preventDefault();
    const f = e.target;
    this.act(async () => { await store.adminSetPassword(id, f.password.value); f.reset(); }, 'admin.password_set');
  }

  when(ts) {
    return new Date(ts).toLocaleString(document.documentElement.lang || undefined, { dateStyle: 'medium', timeStyle: 'short' });
  }

  addSpace(e) {
    e.preventDefault();
    const f = e.target;
    const members = [...f.querySelectorAll('input[name=member]:checked')].map((x) => x.value);
    this.act(async () => {
      await store.adminAddSpace({ id: f.id.value.trim().toLowerCase(), name: f.name.value.trim(), members });
      f.reset();
    }, 'admin.space_added');
  }

  render() {
    if (this.locked === undefined) return nothing;
    if (this.locked) {
      return html`
        <header class="bar"><h1>${t('nav.admin')}</h1></header>
        <form class="password" @submit=${(e) => this.unlock(e)}>
          <p>${t('admin.unlock_why')}</p>
          <div class="field"><label>${t('settings.password_current')}</label>
            <input type="password" name="password" autocomplete="current-password" required></div>
          ${this.error ? html`<p class="error">${this.error}</p>` : nothing}
          <button type="submit" class="primary">${t('admin.unlock')}</button>
        </form>`;
    }
    const users = this.users || [];
    return html`
      <header class="bar"><h1>${t('nav.admin')}</h1></header>
      ${this.error ? html`<p class="error">${this.error}</p>` : nothing}
      <section class="settings admin">
        <h2>${t('admin.users')}</h2>
        ${users.map((u) => html`
          <div class="row"><span class="row-main"><span class="title">${u.name || u.id}</span>
            <span class="meta"><span class="chip">${u.id}</span>
              ${u.admin ? html`<span class="chip owner">${t('admin.is_admin')}</span>` : nothing}
              ${u.disabled ? html`<span class="chip stuck">${t('admin.deactivated')}</span>` : nothing}
              ${u.password ? nothing : html`<span class="chip waiting">${t('admin.no_password')}</span>`}
              <small>${u.login ? t('admin.presence', { login: this.when(u.login), seen: this.when(u.seen) }) : t('admin.never')}</small></span></span>
            ${u.id === store.me.user.id ? nothing : html`<button type="button"
              @click=${() => this.act(() => store.adminSetUser(u.id, { disabled: !u.disabled }))}>${t(u.disabled ? 'admin.activate' : 'admin.deactivate')}</button>`}
            <form class="row-actions" @submit=${(e) => this.setPassword(e, u.id)}>
              <input type="password" name="password" minlength="8" maxlength="72" required placeholder=${t('settings.password_new')} autocomplete="new-password">
              <button type="submit">${t('admin.set_password')}</button></form></div>`)}
        <form class="password" @submit=${(e) => this.addUser(e)}>
          <div class="field"><label>${t('admin.user_id')}</label><input type="text" name="id" required pattern="[a-z0-9]([a-z0-9]|-)*" maxlength="64" autocapitalize="off">
            <small>${t('admin.id_hint')}</small></div>
          <div class="field"><label>${t('admin.user_name')}</label><input type="text" name="name" required></div>
          <div class="field"><label>${t('admin.first_password')}</label><input type="password" name="password" minlength="8" maxlength="72" autocomplete="new-password"></div>
          <button type="submit">${t('admin.add_user')}</button>
          <p><small>${t('admin.operator_note')}</small></p>
        </form>

        <h2>${t('admin.spaces')}</h2>
        ${(this.spaces || []).map((sp) => html`
          <div class="row"><span class="row-main"><span class="title">${sp.name}</span>
            <span class="meta"><span class="chip">${sp.id}</span></span></span></div>
          <div class="chips members">${users.filter((u) => !u.service).map((u) => {
            const on = sp.members.includes(u.id);
            return html`<button type="button" class="chip pick ${on ? 'on' : ''}" title=${t(on ? 'admin.remove_member' : 'admin.add_member')}
              @click=${() => this.act(() => store.adminSetMember(sp.id, u.id, !on))}>${u.name || u.id}</button>`;
          })}</div>`)}
        <form class="password" @submit=${(e) => this.addSpace(e)}>
          <div class="field"><label>${t('admin.space_id')}</label><input type="text" name="id" required pattern="[a-z0-9]([a-z0-9]|-)*" maxlength="64" autocapitalize="off">
            <small>${t('admin.id_hint')}</small></div>
          <div class="field"><label>${t('admin.space_name')}</label><input type="text" name="name" required></div>
          <div class="field"><label>${t('admin.members')}</label>
            <div class="chips">${users.filter((u) => !u.service).map((u) => html`
              <label class="check"><input type="checkbox" name="member" value=${u.id}> ${u.name || u.id}</label>`)}</div>
            <small>${t('admin.members_hint')}</small></div>
          <button type="submit">${t('admin.add_space')}</button>
        </form>
      </section>`;
  }
}
customElements.define('gtd-admin', Admin);
