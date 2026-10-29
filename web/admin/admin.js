'use strict';

/* Admin page, served both inside Zoraxy (full) and at /settings/ for the admin group.
   All URLs are relative so it works under either prefix. */

const ICON_CDN = 'https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons';
const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

let state = null;

// Zoraxy embeds plugin pages in an iframe sandboxed without allow-forms and
// allow-modals, so this page uses no <form> submission and no confirm().
const fields = (root) => Object.fromEntries([...root.querySelectorAll('[name]')].map((n) => [n.name, n]));
let editing = null; // app being edited
let editingAccount = null; // { create, username, groups } while the account editor is open

async function api(path, body) {
  const headers = { 'X-Requested-With': 'zoraxy-home' };
  const csrf = document.querySelector('meta[name="zoraxy.csrf.Token"]')?.content;
  if (csrf) headers['X-CSRF-Token'] = csrf;
  const opts = { credentials: 'same-origin', headers };
  if (body !== undefined) {
    opts.method = 'POST';
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

let toastTimer = null;
function toast(msg, isError) {
  const t = $('toast');
  t.textContent = msg;
  t.classList.toggle('error', !!isError);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), isError ? 6000 : 2500);
}

/* ---------- Icons (same rules as the home screen) ---------- */

function iconSources(spec) {
  if (!spec) return [];
  if (/^(https?:)?\/\//.test(spec) || spec.startsWith('/') || spec.startsWith('data:image/')) return [spec];
  const slug = spec.toLowerCase().replace(/[^a-z0-9-]/g, '');
  return slug ? [`${ICON_CDN}/svg/${slug}.svg`, `${ICON_CDN}/png/${slug}.png`] : [];
}

function hue(str) {
  let h = 0;
  for (const c of str) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return h % 360;
}

function initials(name) {
  const words = (name || '?').trim().split(/\s+/).filter(Boolean);
  if (words.length > 1) return (words[0][0] + words[1][0]).toUpperCase();
  return ((name || '?').trim()[0] || '?').toUpperCase();
}

function iconEl(spec, name, id, big) {
  const box = el('div', big ? 'icon big' : 'icon');
  const fallback = () => {
    box.replaceChildren();
    box.classList.add('fallback');
    box.style.setProperty('--h', hue(id));
    box.textContent = initials(name);
  };
  const sources = iconSources(spec);
  if (!sources.length) {
    fallback();
    return box;
  }
  const img = el('img');
  img.alt = '';
  let i = 0;
  img.onerror = () => (++i < sources.length ? (img.src = sources[i]) : fallback());
  img.src = sources[0];
  box.append(img);
  return box;
}

/* ---------- Rendering ---------- */

function ago(iso) {
  const t = new Date(iso).getTime();
  if (!t || t < 0) return 'never';
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return new Date(iso).toLocaleDateString();
}

function renderSync() {
  const line = $('sync-line');
  const { last, error } = state.sync;
  const count = state.apps.length;
  if (error) {
    line.textContent = `Could not read Zoraxy routes: ${error}`;
    line.classList.add('error');
  } else {
    line.textContent = `${count} app${count === 1 ? '' : 's'} from Zoraxy · synced ${ago(last)}`;
    line.classList.remove('error');
  }
}

function renderApps() {
  const q = $('app-filter').value.trim().toLowerCase();
  const list = $('app-list');
  const apps = state.apps.filter((a) => !q || a.name.toLowerCase().includes(q) || a.id.includes(q));
  if (!apps.length) {
    list.replaceChildren(el('li', 'empty', state.apps.length ? 'No matching apps.' : 'No enabled HTTP proxy rules found yet.'));
    return;
  }
  list.replaceChildren(...apps.map((a) => {
    const li = el('li', 'item');
    li.tabIndex = 0;
    const main = el('div', 'item-main');
    main.append(el('div', 'item-title', a.name), el('div', 'item-sub', a.id + (a.route.upstream ? ` → ${a.route.upstream}` : '')));
    const side = el('div', 'item-side');
    if (a.hidden) side.append(el('span', 'badge warn', 'Hidden'));
    else if (a.groups?.length) side.append(el('span', 'badge', a.groups.join(', ')));
    side.append(el('span', 'chevron', '›'));
    li.append(iconEl(a.icon, a.name, a.id), main, side);
    li.addEventListener('click', () => openEditor(a));
    li.addEventListener('keydown', (e) => e.key === 'Enter' && openEditor(a));
    return li;
  }));
}

function renderUsers() {
  const list = $('user-list');
  if (!state.users.length) {
    list.replaceChildren(el('li', 'empty', 'Nobody has signed in yet.'));
    return;
  }
  list.replaceChildren(...state.users.map((u) => {
    const li = el('li', 'item');
    const av = el('div', 'avatar');
    if (u.picture) av.style.backgroundImage = `url("${encodeURI(u.picture)}")`;
    else av.textContent = initials(u.name);
    const main = el('div', 'item-main');
    const groups = u.groups?.length ? u.groups.join(', ') : 'no groups';
    main.append(el('div', 'item-title', u.name || u.username || u.sub), el('div', 'item-sub', `${u.email || u.username || ''} · ${groups} · ${ago(u.last_seen)}`));
    const side = el('div', 'item-side');
    if (u.groups?.includes(state.adminGroup)) side.append(el('span', 'badge', 'Admin'));

    const block = el('input');
    block.type = 'checkbox';
    block.setAttribute('role', 'switch');
    block.checked = !u.blocked;
    block.title = u.blocked ? 'Blocked — switch on to allow' : 'Allowed — switch off to block';
    block.setAttribute('aria-label', `Allow ${u.name}`);
    block.addEventListener('click', (e) => e.stopPropagation());
    block.addEventListener('change', async () => {
      try {
        await api('api/user', { sub: u.sub, blocked: !block.checked });
        toast(block.checked ? `${u.name} can sign in` : `${u.name} is blocked`);
        await load();
      } catch (e) {
        block.checked = !block.checked;
        toast(e.message, true);
      }
    });

    const forget = el('button', 'link danger', 'Forget');
    forget.type = 'button';
    forget.title = 'Remove this person and their layout. They reappear on next sign-in.';
    forget.addEventListener('click', async (e) => {
      e.stopPropagation();
      if (!forget.dataset.armed) {
        forget.dataset.armed = '1';
        forget.textContent = 'Tap again to forget';
        setTimeout(() => {
          delete forget.dataset.armed;
          forget.textContent = 'Forget';
        }, 4000);
        return;
      }
      try {
        await api('api/user', { sub: u.sub, forget: true });
        await load();
      } catch (err) {
        toast(err.message, true);
      }
    });
    side.append(forget, block);
    li.append(av, main, side);
    return li;
  }));
}

function renderSettings() {
  const root = $('settings-form');
  if (!root || !state.settings) return;
  const f = fields(root);
  const s = state.settings;
  f.public_url.value = s.public_url || '';
  f.title.value = s.title || '';
  f.listen_addr.value = s.listen_addr || '';
  f.issuer.value = s.oidc.issuer || '';
  f.client_id.value = s.oidc.client_id || '';
  f.client_secret.value = s.oidc.client_secret || '';
  f.scopes.value = (s.oidc.scopes || []).join(' ');
  f.groups_claim.value = s.oidc.groups_claim || '';
  f.provider_name.value = s.oidc.provider_name || '';
  f.admin_group.value = s.admin_group || '';
  f.allowed_groups.value = (s.allowed_groups || []).join(', ');
  f.password_login.checked = !!s.password_login;
  $('callback-url').textContent = s.public_url ? s.callback_url : 'Set the homepage URL first';
}

async function load() {
  state = await api('api/state');
  renderSync();
  renderApps();
  renderThemes();
  renderAccounts();
  renderUsers();
}

/* ---------- Local accounts ---------- */

function renderAccounts() {
  const list = $('account-list');
  const accounts = state.accounts || [];
  if (!accounts.length) list.replaceChildren(el('li', 'empty', 'No local accounts. Add one to sign in with a username and password.'));
  else list.replaceChildren(...accounts.map((acc) => {
    const li = el('li', 'item');
    li.tabIndex = 0;
    const av = el('div', 'avatar', initials(acc.name));
    const main = el('div', 'item-main');
    const groups = acc.groups?.length ? acc.groups.join(', ') : 'no groups';
    main.append(el('div', 'item-title', acc.name), el('div', 'item-sub', `${acc.username} · ${groups}`));
    const side = el('div', 'item-side');
    if (acc.groups?.includes(state.adminGroup)) side.append(el('span', 'badge', 'Admin'));
    side.append(el('span', 'chevron', '›'));
    li.append(av, main, side);
    li.addEventListener('click', () => openAccountEditor(acc));
    li.addEventListener('keydown', (e) => e.key === 'Enter' && openAccountEditor(acc));
    return li;
  }));
  $('account-foot').textContent = state.passwordLogin
    ? 'These people sign in on the lock screen with their username and password.'
    : state.full
      ? 'Password sign-in is off. Turn it on under Sign-in above.'
      : 'Password sign-in is off. Turn it on in Zoraxy → Plugins → Home.';
}

function openAccountEditor(acc) {
  const create = !acc;
  editingAccount = { create, username: acc?.username || '', groups: new Set(acc?.groups || []) };
  const f = fields($('account-form'));
  f.username.value = acc?.username || '';
  f.username.disabled = !create;
  f.name.value = acc?.name || '';
  f.email.value = acc?.email || '';
  f.password.value = '';
  f.password.placeholder = create ? 'Required' : 'Leave empty to keep';
  f.groups_extra.value = '';
  $('account-title').textContent = create ? 'New account' : acc.name;
  const del = $('account-delete');
  del.hidden = create;
  delete del.dataset.armed;
  del.textContent = 'Delete account';

  const known = new Set([...state.groups, ...editingAccount.groups]);
  if (state.adminGroup) known.add(state.adminGroup);
  const chips = $('account-chips');
  chips.replaceChildren(...[...known].sort().map((g) => {
    const c = el('button', 'chip', g);
    c.type = 'button';
    c.setAttribute('aria-pressed', String(editingAccount.groups.has(g)));
    c.addEventListener('click', () => {
      editingAccount.groups.has(g) ? editingAccount.groups.delete(g) : editingAccount.groups.add(g);
      c.setAttribute('aria-pressed', String(editingAccount.groups.has(g)));
    });
    return c;
  }));
  $('account-editor').showModal();
  (create ? f.username : f.name).focus();
}

async function saveAccount() {
  if (!editingAccount) return;
  const f = fields($('account-form'));
  const extra = f.groups_extra.value.split(',').map((g) => g.trim()).filter(Boolean);
  const { create } = editingAccount;
  try {
    await api('api/account', {
      create,
      username: editingAccount.create ? f.username.value.trim() : editingAccount.username,
      name: f.name.value.trim(),
      email: f.email.value.trim(),
      password: f.password.value,
      groups: [...new Set([...editingAccount.groups, ...extra])],
    });
    $('account-editor').close();
    toast(create ? 'Account created' : 'Account saved');
    await load();
  } catch (e) {
    toast(e.message, true);
  }
}

async function deleteAccount() {
  const del = $('account-delete');
  if (!editingAccount || editingAccount.create) return;
  if (!del.dataset.armed) {
    del.dataset.armed = '1';
    del.textContent = 'Tap again to delete';
    setTimeout(() => {
      delete del.dataset.armed;
      del.textContent = 'Delete account';
    }, 4000);
    return;
  }
  try {
    await api('api/account/delete', { username: editingAccount.username });
    $('account-editor').close();
    toast('Account deleted');
    await load();
  } catch (e) {
    toast(e.message, true);
  }
}

/* ---------- Themes ---------- */

const SHAPES = { squircle: 0.225, rounded: 0.16, circle: 0.5, square: 0.07 };
const FONTS = {
  system: 'var(--font)',
  rounded: '"SF Pro Rounded", ui-rounded, var(--font)',
  serif: '"New York", ui-serif, "Iowan Old Style", Georgia, serif',
  mono: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace',
};
let themeDraft = null; // theme being edited

function thumbBackground(t) {
  const [c0, c1 = c0, c2 = c1] = t.wallpaper.colors;
  if (t.wallpaper.type === 'image' && t.wallpaper.image) return `url("${t.wallpaper.image}") center / cover, ${c0}`;
  if (t.wallpaper.type === 'solid') return c0;
  if (t.wallpaper.type === 'gradient') return `linear-gradient(160deg, ${c0}, ${c1} 55%, ${c2})`;
  return `radial-gradient(circle at 25% 20%, ${c1}, transparent 65%), radial-gradient(circle at 80% 70%, ${c2}, transparent 60%), ${c0}`;
}

function renderThemes() {
  const list = $('theme-list');
  list.replaceChildren(...state.themes.map((t) => {
    const li = el('li', 'item');
    li.tabIndex = 0;
    const thumb = el('div', 'theme-thumb');
    thumb.style.background = thumbBackground(t);
    const main = el('div', 'item-main');
    const bits = [t.builtin ? 'Built-in' : 'Custom', t.font, `${t.icons.shape} icons`];
    main.append(el('div', 'item-title', t.name), el('div', 'item-sub', bits.join(' · ')));
    const side = el('div', 'item-side');
    if (t.id === state.defaultTheme) {
      side.append(el('span', 'badge', 'Default'));
    } else {
      const make = el('button', 'link', 'Make default');
      make.type = 'button';
      make.addEventListener('click', async (e) => {
        e.stopPropagation();
        try {
          await api('api/theme/default', { id: t.id });
          toast(`${t.name} is now the default`);
          await load();
        } catch (err) {
          toast(err.message, true);
        }
      });
      side.append(make);
    }
    side.append(el('span', 'chevron', '›'));
    li.append(thumb, main, side);
    li.addEventListener('click', () => openThemeEditor(t));
    li.addEventListener('keydown', (e) => e.key === 'Enter' && openThemeEditor(t));
    return li;
  }));
}

function openThemeEditor(t) {
  const copy = JSON.parse(JSON.stringify(t || state.themes[0]));
  if (!t || t.builtin) {
    copy.id = ''; // built-ins are saved as a new custom theme
    copy.name = t ? `${t.name} copy` : 'New theme';
    copy.builtin = false;
  }
  themeDraft = copy;
  const f = fields($('theme-form'));
  const c = copy.wallpaper.colors;
  f.name.value = copy.name;
  f.appearance.value = copy.appearance;
  f.font.value = copy.font;
  f.accent.value = copy.accent;
  f.wp_type.value = copy.wallpaper.type;
  ['c0', 'c1', 'c2', 'c3'].forEach((k, i) => (f[k].value = c[i] || c[c.length - 1] || '#000000'));
  f.image.value = copy.wallpaper.image || '';
  f.dim.value = copy.wallpaper.dim || 0;
  f.shape.value = copy.icons.shape;
  f.style.value = copy.icons.style;
  f.labels.checked = !copy.icons.hide_labels;
  f.css.value = copy.css || '';
  $('theme-title').textContent = t && !t.builtin ? t.name : 'New theme';
  const del = $('theme-delete');
  del.hidden = !copy.id;
  delete del.dataset.armed;
  del.textContent = 'Delete theme';
  renderThemePreview();
  $('theme-editor').showModal();
}

function readThemeForm() {
  const f = fields($('theme-form'));
  const type = f.wp_type.value;
  // Solid uses one color, gradient three, soft light all four
  const count = { solid: 1, image: 1, gradient: 3, blobs: 4 }[type];
  return {
    id: themeDraft.id,
    name: f.name.value.trim(),
    appearance: f.appearance.value,
    font: f.font.value,
    accent: f.accent.value,
    wallpaper: {
      type,
      colors: ['c0', 'c1', 'c2', 'c3'].slice(0, count).map((k) => f[k].value),
      image: f.image.value.trim(),
      dim: Number(f.dim.value),
    },
    icons: { shape: f.shape.value, style: f.style.value, hide_labels: !f.labels.checked },
    css: f.css.value,
  };
}

function renderThemePreview() {
  const t = readThemeForm();
  const f = fields($('theme-form'));
  document.querySelectorAll('#theme-form [data-when]').forEach((n) => n.classList.toggle('show', n.dataset.when === t.wallpaper.type));
  ['c1', 'c2', 'c3'].forEach((k, i) => (f[k].hidden = t.wallpaper.colors.length < i + 2));

  const p = $('theme-preview');
  const all = [f.c0.value, f.c1.value, f.c2.value, f.c3.value];
  const light = t.appearance === 'light';
  const vars = {
    '--tp-0': all[0], '--tp-1': all[1], '--tp-2': all[2], '--tp-3': all[3],
    '--tp-image': t.wallpaper.image ? `url("${t.wallpaper.image.replace(/["\\]/g, '')}")` : 'none',
    '--tp-dim': String(t.wallpaper.dim),
    '--tp-k': String(SHAPES[t.icons.shape]),
    '--tp-font': FONTS[t.font],
    '--tp-accent': t.accent,
    '--tp-text': light ? '#16161a' : '#fff',
    '--tp-shadow': light ? 'none' : '0 1px 2px rgba(0,0,0,.4)',
    '--tp-glass': light ? 'rgba(255,255,255,.45)' : 'rgba(255,255,255,.18)',
    '--tp-icon-bg': { light: 'rgba(255,255,255,.94)', dark: '#1d1e22', glass: 'rgba(255,255,255,.18)' }[t.icons.style],
  };
  for (const [k, v] of Object.entries(vars)) p.style.setProperty(k, v);
  p.dataset.wp = t.wallpaper.type;
  p.dataset.labels = t.icons.hide_labels ? 'hidden' : 'shown';

  const sample = state.apps.slice(0, 14);
  const tile = (a) => {
    const w = el('div', 'tp-app');
    w.append(iconEl(a.icon, a.name, a.id), el('span', null, a.name));
    return w;
  };
  $('tp-grid').replaceChildren(...sample.slice(0, 10).map(tile));
  $('tp-dock').replaceChildren(...sample.slice(10, 14).map(tile));
  let badge = p.querySelector('.tp-accent');
  if (!badge) {
    badge = el('span', 'tp-accent', 'Accent');
    p.append(badge);
  }
}

async function saveTheme() {
  try {
    const saved = await api('api/theme', readThemeForm());
    $('theme-editor').close();
    toast(`Saved ${saved.name}`);
    await load();
  } catch (e) {
    toast(e.message, true);
  }
}

async function deleteTheme() {
  const del = $('theme-delete');
  if (!del.dataset.armed) {
    del.dataset.armed = '1';
    del.textContent = 'Tap again to delete';
    return;
  }
  try {
    await api('api/theme/delete', { id: themeDraft.id });
    $('theme-editor').close();
    toast('Theme deleted');
    await load();
  } catch (e) {
    toast(e.message, true);
  }
}

/* ---------- App editor ---------- */

function editorPreview() {
  if (!editing) return;
  const f = fields($('app-form'));
  const name = f.name.value.trim() || editing.defaultName;
  $('editor-icon').replaceChildren(iconEl(f.icon.value.trim() || editing.defaultIcon, name, editing.id, true));
  $('editor-name').textContent = name;
}

function openEditor(app) {
  const o = app.override || {};
  editing = {
    id: app.id,
    defaultName: app.override?.name ? defaultName(app.id) : app.name,
    defaultIcon: app.override?.icon ? app.id.split('.')[0] : app.icon,
    groups: new Set(o.groups || []),
  };
  const f = fields($('app-form'));
  f.name.value = o.name || '';
  f.name.placeholder = editing.defaultName;
  f.icon.value = o.icon || '';
  f.icon.placeholder = editing.defaultIcon;
  f.url.value = o.url || '';
  f.url.placeholder = `https://${app.id}`;
  f.visible.checked = !o.hidden;
  $('editor-title').textContent = app.name;
  $('editor-domain').textContent = app.id;

  const known = new Set([...state.groups, ...editing.groups]);
  const chips = $('group-chips');
  chips.replaceChildren(...[...known].sort().map((g) => {
    const c = el('button', 'chip', g);
    c.type = 'button';
    c.setAttribute('aria-pressed', String(editing.groups.has(g)));
    c.addEventListener('click', () => {
      editing.groups.has(g) ? editing.groups.delete(g) : editing.groups.add(g);
      c.setAttribute('aria-pressed', String(editing.groups.has(g)));
    });
    return c;
  }));
  if (!known.size) chips.append(el('span', 'muted', 'No groups seen yet — type them below.'));
  f.groups_extra.value = '';
  editorPreview();
  $('app-editor').showModal();
}

function defaultName(domain) {
  const label = domain.split('.')[0].replace(/[-_]/g, ' ');
  return label ? label[0].toUpperCase() + label.slice(1) : domain;
}

function closeEditor() {
  editing = null;
  $('app-editor').close();
}

async function saveEditor(action) {
  if (!editing) return;
  const f = fields($('app-form'));
  const extra = f.groups_extra.value.split(',').map((g) => g.trim()).filter(Boolean);
  const override = action === 'reset' ? {} : {
    name: f.name.value.trim(),
    icon: f.icon.value.trim(),
    url: f.url.value.trim(),
    hidden: !f.visible.checked,
    groups: [...new Set([...editing.groups, ...extra])],
  };
  try {
    await api('api/app', { id: editing.id, override });
    closeEditor();
    toast(action === 'reset' ? 'Reset to defaults' : 'Saved');
    await load();
  } catch (e) {
    toast(e.message, true); // keep the editor open so nothing typed is lost
  }
}

/* ---------- Wiring ---------- */

function wire() {
  $('app-filter').addEventListener('input', renderApps);
  $('app-form').addEventListener('input', editorPreview);
  $('editor-cancel').addEventListener('click', closeEditor);
  $('editor-save').addEventListener('click', () => saveEditor('save'));
  $('editor-reset').addEventListener('click', () => saveEditor('reset'));
  $('app-editor').addEventListener('close', () => (editing = null));
  $('app-form').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.target.matches('input:not([type="checkbox"])')) {
      e.preventDefault();
      saveEditor('save');
    }
  });
  $('new-theme').addEventListener('click', () => openThemeEditor(null));
  $('new-account').addEventListener('click', () => openAccountEditor(null));
  $('account-cancel').addEventListener('click', () => $('account-editor').close());
  $('account-save').addEventListener('click', saveAccount);
  $('account-delete').addEventListener('click', deleteAccount);
  $('account-editor').addEventListener('close', () => (editingAccount = null));
  $('account-form').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.target.matches('input')) {
      e.preventDefault();
      saveAccount();
    }
  });
  $('theme-cancel').addEventListener('click', () => $('theme-editor').close());
  $('theme-save').addEventListener('click', saveTheme);
  $('theme-delete').addEventListener('click', deleteTheme);
  $('theme-form').addEventListener('input', renderThemePreview);
  $('theme-form').addEventListener('change', renderThemePreview);
  $('sync-now').addEventListener('click', async () => {
    try {
      await api('api/sync', {});
      await load();
      toast('Routes synced');
    } catch (e) {
      toast(e.message, true);
    }
  });

  const root = $('settings-form');
  if (!root) return;
  const form = fields(root);
  const saveSettings = async () => {
    const split = (v) => v.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);
    try {
      await api('api/settings', {
        public_url: form.public_url.value.trim(),
        title: form.title.value.trim(),
        listen_addr: form.listen_addr.value.trim(),
        oidc: {
          issuer: form.issuer.value.trim(),
          client_id: form.client_id.value.trim(),
          client_secret: form.client_secret.value,
          scopes: split(form.scopes.value),
          groups_claim: form.groups_claim.value.trim(),
          provider_name: form.provider_name.value.trim(),
        },
        admin_group: form.admin_group.value.trim(),
        allowed_groups: split(form.allowed_groups.value),
        password_login: form.password_login.checked,
      });
      await load();
      renderSettings();
      toast('Settings saved');
    } catch (err) {
      toast(err.message, true);
    }
  };
  $('save-settings').addEventListener('click', saveSettings);
  root.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.target.matches('input')) {
      e.preventDefault();
      saveSettings();
    }
  });
  $('test-oidc').addEventListener('click', async () => {
    try {
      const r = await api('api/test-oidc', { issuer: form.issuer.value.trim() });
      toast(r.ok);
    } catch (e) {
      toast(e.message, true);
    }
  });
  $('copy-callback').addEventListener('click', async () => {
    const text = $('callback-url').textContent;
    try {
      await navigator.clipboard.writeText(text);
      toast('Callback URL copied');
    } catch {
      getSelection().selectAllChildren($('callback-url'));
      toast('Press ⌘C / Ctrl+C to copy');
    }
  });
}

wire();
load().then(renderSettings).catch((e) => {
  $('sync-line').textContent = e.message;
  $('sync-line').classList.add('error');
});
