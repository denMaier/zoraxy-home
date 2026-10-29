'use strict';

/* Home — client for the Zoraxy Home plugin.
   Lock screen -> OIDC or password sign-in -> paged icon grid with folders, a dock, search,
   jiggle-mode editing and server-defined themes. */

const ICON_CDN = 'https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons';
const LONG_PRESS_MS = 520;
const DRAG_SLOP = 6;
const FOLDER_LEAVE_MS = 380;
const ICON_SHAPES = { squircle: 0.225, rounded: 0.16, circle: 0.5, square: 0.07 };
const FONTS = {
  system: 'var(--font-system)',
  rounded: '"SF Pro Rounded", ui-rounded, var(--font-system)',
  serif: '"New York", ui-serif, "Iowan Old Style", Georgia, serif',
  mono: 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace',
};

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};
const isFolder = (id) => id.startsWith('f_');

const state = {
  me: null,
  apps: new Map(), // id -> app
  layout: { order: [], dock: [], folders: {}, theme: '' },
  themes: [],
  editing: false,
  page: 0,
  perPage: 24,
  maxDock: 4,
  openFolder: null,
  lock: null, // { configured, oidc, password, provider } from a signed-out /api/me
};

/* ---------- API ---------- */

async function api(path, body) {
  const opts = { credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.method = 'POST';
    opts.headers['Content-Type'] = 'application/json';
    opts.headers['X-Requested-With'] = 'zoraxy-home';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

let saveTimer = null;
let layoutVersion = 0;
function saveLayout() {
  layoutVersion += 1;
  clearTimeout(saveTimer);
  saveTimer = setTimeout(async () => {
    const sent = layoutVersion;
    try {
      const clean = await api('/api/layout', state.layout);
      // Adopt the server's cleaned layout unless the user changed something meanwhile
      if (sent === layoutVersion && !dragging) {
        state.layout = normalizeLayout(clean);
        if (!state.editing && !state.openFolder) render();
      }
    } catch (e) {
      if (e.status === 401) showLock();
    }
  }, 400);
}

function normalizeLayout(l) {
  return { order: l.order || [], dock: l.dock || [], folders: l.folders || {}, theme: l.theme || '' };
}

/* ---------- Clock ---------- */

function tick() {
  const now = new Date();
  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  $('status-time').textContent = time;
  $('lock-time').textContent = time.replace(/\s?[AP]M$/i, '');
  $('lock-date').textContent = now.toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long' });
}

/* ---------- Themes ---------- */

function applyTheme(t) {
  if (!t) return;
  const r = document.documentElement.style;
  const c = t.wallpaper.colors?.length ? t.wallpaper.colors : ['#071423'];
  r.setProperty('--wp-base', c[0]);
  r.setProperty('--wp-1', c[1] || c[0]);
  r.setProperty('--wp-2', c[2] || c[1] || c[0]);
  r.setProperty('--wp-3', c[3] || c[1] || c[0]);
  r.setProperty('--wp-image', t.wallpaper.image ? `url("${t.wallpaper.image}")` : 'none');
  r.setProperty('--wp-dim', String(t.wallpaper.dim || 0));
  r.setProperty('--icon-k', String(ICON_SHAPES[t.icons.shape] ?? ICON_SHAPES.squircle));
  r.setProperty('--font', FONTS[t.font] || FONTS.system);
  r.setProperty('--accent', t.accent || '#2c90ff');
  const d = document.body.dataset;
  d.wp = t.wallpaper.type;
  d.appearance = t.appearance;
  d.iconStyle = t.icons.style;
  d.labels = t.icons.hide_labels ? 'hidden' : 'shown';
  d.theme = t.id;
  $('theme-css').textContent = t.css || '';
  document.querySelector('meta[name="theme-color"]').content = c[0];
}

function themeById(id) {
  return state.themes.find((t) => t.id === id);
}

/* ---------- Icons ---------- */

const iconCache = new Map(); // icon spec -> resolved src, or null for "use fallback"

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
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length > 1) return (words[0][0] + words[1][0]).toUpperCase();
  return (name.trim()[0] || '?').toUpperCase();
}

function makeFallback(box, app) {
  box.replaceChildren();
  box.classList.add('fallback');
  box.style.setProperty('--h', hue(app.id));
  box.textContent = initials(app.name);
}

function iconEl(app, extraClass) {
  const box = el('div', extraClass ? `app-icon ${extraClass}` : 'app-icon');
  const cached = iconCache.get(app.icon);
  if (cached === null) {
    makeFallback(box, app);
    return box;
  }
  const sources = cached ? [cached] : iconSources(app.icon);
  if (!sources.length) {
    makeFallback(box, app);
    return box;
  }
  const img = el('img');
  img.alt = '';
  img.decoding = 'async';
  img.draggable = false;
  let i = 0;
  img.onload = () => iconCache.set(app.icon, img.src);
  img.onerror = () => {
    i += 1;
    if (i < sources.length) {
      img.src = sources[i];
    } else {
      iconCache.set(app.icon, null);
      makeFallback(box, app);
    }
  };
  img.src = sources[0];
  box.append(img);
  return box;
}

function setAvatar(node, user) {
  node.textContent = '';
  node.style.backgroundImage = '';
  if (user.picture) {
    node.style.backgroundImage = `url("${encodeURI(user.picture)}")`;
  } else {
    node.textContent = initials(user.name || '?');
  }
}

/* ---------- Grid metrics ---------- */

function measure() {
  const pages = $('pages');
  const root = getComputedStyle(document.documentElement);
  const icon = parseFloat(root.getPropertyValue('--icon')) || 62;
  const w = pages.clientWidth;
  const h = pages.clientHeight;
  const phone = window.innerWidth < 700;
  const gridMax = phone ? w : Math.min(w, 1080);
  const cols = Math.max(4, Math.min(phone ? 4 : 8, Math.floor((gridMax - 28) / (icon + 44))));
  const rowGap = phone ? 18 : 28;
  const cellH = icon + 34;
  const rows = Math.max(1, Math.min(6, Math.floor((h - 14 + rowGap) / (cellH + rowGap))));
  const s = document.documentElement.style;
  s.setProperty('--cols', cols);
  s.setProperty('--rows', rows);
  s.setProperty('--row-gap', `${rowGap}px`);
  s.setProperty('--grid-max', `${gridMax}px`);
  state.perPage = cols * rows;
  state.maxDock = phone ? 4 : 6;
}

/* ---------- Rendering ---------- */

function exists(id) {
  return isFolder(id) ? !!state.layout.folders[id] : state.apps.has(id);
}

function appEl(app, index) {
  const a = el('a', 'app');
  a.href = app.url;
  a.draggable = false; // pointer events handle dragging, not native DnD
  a.dataset.id = app.id;
  a.style.setProperty('--i', index);
  a.title = app.name;
  a.append(iconEl(app), el('span', 'app-label', app.name));
  return a;
}

function folderEl(id, index) {
  const f = state.layout.folders[id];
  const a = el('a', 'app folder');
  a.href = '#';
  a.draggable = false;
  a.dataset.id = id;
  a.style.setProperty('--i', index);
  a.title = f.name;
  a.setAttribute('aria-label', `${f.name} folder, ${f.apps.length} apps`);
  const box = el('div', 'app-icon folder-icon');
  const grid = el('div', 'folder-grid');
  f.apps.slice(0, 9).forEach((aid) => {
    const app = state.apps.get(aid);
    if (app) grid.append(iconEl(app, 'mini'));
  });
  box.append(grid);
  a.append(box, el('span', 'app-label', f.name));
  return a;
}

function itemEl(id, index) {
  return isFolder(id) ? folderEl(id, index) : appEl(state.apps.get(id), index);
}

function pagesOf(order) {
  const out = [];
  for (let i = 0; i < order.length; i += state.perPage) out.push(order.slice(i, i + state.perPage));
  if (!out.length) out.push([]);
  // While editing, offer one blank page to move apps onto
  if (state.editing && out[out.length - 1].length) out.push([]);
  return out;
}

function render() {
  measure();
  const pagesEl = $('pages');
  const order = state.layout.order.filter(exists);
  const pages = pagesOf(order);

  const frag = document.createDocumentFragment();
  let n = 0;
  pages.forEach((ids, p) => {
    const page = el('div', 'page');
    page.dataset.page = p;
    ids.forEach((id) => page.append(itemEl(id, n++)));
    if (!state.apps.size && p === 0) {
      page.append(el('p', 'empty', 'No apps yet. Every enabled HTTP proxy rule in Zoraxy that you are allowed to see shows up here automatically.'));
    }
    frag.append(page);
  });
  pagesEl.replaceChildren(frag);

  const dock = $('dock');
  dock.replaceChildren(...state.layout.dock.filter(exists).map((id, i) => itemEl(id, i)));

  state.page = Math.min(state.page, pages.length - 1);
  pagesEl.style.scrollBehavior = 'auto';
  pagesEl.scrollLeft = state.page * pagesEl.clientWidth;
  pagesEl.style.scrollBehavior = '';
  renderDots(pages.length);
  if (state.openFolder) renderFolder();
}

function renderDots(count) {
  const dots = $('dots');
  dots.replaceChildren();
  if (count < 2) return;
  for (let i = 0; i < count; i++) {
    const d = el('button', 'dot');
    d.type = 'button';
    d.setAttribute('role', 'tab');
    d.setAttribute('aria-label', `Page ${i + 1}`);
    d.setAttribute('aria-selected', String(i === state.page));
    d.addEventListener('click', () => goToPage(i));
    dots.append(d);
  }
}

function goToPage(i) {
  const pagesEl = $('pages');
  const count = pagesEl.children.length;
  state.page = Math.max(0, Math.min(count - 1, i));
  pagesEl.scrollTo({ left: state.page * pagesEl.clientWidth });
}

function onPagesScroll() {
  const pagesEl = $('pages');
  const p = Math.round(pagesEl.scrollLeft / Math.max(1, pagesEl.clientWidth));
  if (p !== state.page) state.page = p;
  [...$('dots').children].forEach((d, i) => d.setAttribute('aria-selected', String(i === p)));
}

/* ---------- Folders ---------- */

function openFolder(id, focusName) {
  const tile = document.querySelector(`.app[data-id="${CSS.escape(id)}"] .app-icon`);
  state.openFolder = id;
  const overlay = $('folder');
  const panel = $('folder-panel');
  if (tile) {
    // Grow the panel out of the folder icon
    const t = tile.getBoundingClientRect();
    panel.style.setProperty('--from-x', `${t.left + t.width / 2 - innerWidth / 2}px`);
    panel.style.setProperty('--from-y', `${t.top + t.height / 2 - innerHeight / 2}px`);
  }
  renderFolder();
  overlay.hidden = false;
  if (focusName) {
    const name = $('folder-name');
    name.focus();
    name.select();
  }
}

function renderFolder() {
  const f = state.layout.folders[state.openFolder];
  if (!f) return closeFolder();
  const name = $('folder-name');
  if (document.activeElement !== name) name.value = f.name;
  name.readOnly = !state.editing;
  $('folder-apps').replaceChildren(...f.apps.filter((id) => state.apps.has(id)).map((id, i) => appEl(state.apps.get(id), i)));
}

function commitFolderName() {
  const f = state.layout.folders[state.openFolder];
  if (!f) return;
  const name = $('folder-name').value.trim().slice(0, 40) || 'Folder';
  if (name !== f.name) {
    f.name = name;
    saveLayout();
  }
}

function closeFolder() {
  if (!state.openFolder) return;
  commitFolderName();
  state.openFolder = null;
  $('folder').hidden = true;
  $('folder-name').blur();
  render();
}

function newFolderId() {
  const rand = Array.from(crypto.getRandomValues(new Uint8Array(6)), (b) => (b % 36).toString(36)).join('');
  return `f_${rand}`;
}

function suggestFolderName(a, b) {
  const tags = new Set(state.apps.get(a)?.tags || []);
  const shared = (state.apps.get(b)?.tags || []).find((t) => tags.has(t));
  return shared || 'Folder';
}

/* ---------- Lock / home ---------- */

// showLock takes the signed-out /api/me answer; without one (a session that
// expired while the page was open) it asks the server again.
function showLock(info) {
  $('home').hidden = true;
  $('folder').hidden = true;
  $('lock').hidden = false;
  $('lock').classList.remove('unlocking', 'pw-open');
  $('pw-form').hidden = true;
  if (!info) {
    api('/api/me').catch((e) => e.status === 401 && showLock(e.data));
    info = state.lock || { configured: true, oidc: true };
  }
  state.lock = info;
  const next = encodeURIComponent(location.pathname);
  const unlock = $('unlock');
  unlock.href = '/auth/login?next=' + next;
  $('pw-sso').href = unlock.href;
  $('pw-sso').hidden = !info.oidc;
  $('pw-sso').textContent = 'Sign in with ' + (info.provider || 'single sign-on');
  $('unlock-sub').textContent = info.password && !info.oidc ? 'with your password' : 'with ' + (info.provider || 'single sign-on');
  if (info.configured === false) {
    unlock.setAttribute('aria-disabled', 'true');
    $('lock-note').textContent = 'Sign-in is not set up yet. Open Zoraxy → Plugins → Home and set up sign-in.';
  } else {
    unlock.removeAttribute('aria-disabled');
    $('lock-note').textContent = '';
  }
}

function unlock() {
  const link = $('unlock');
  if (link.getAttribute('aria-disabled') === 'true') return;
  if (state.lock?.password) {
    // Reveal the username/password form in place of the unlock button
    $('lock').classList.add('pw-open');
    $('pw-form').hidden = false;
    $('pw-error').textContent = '';
    $('pw-user').focus();
    return;
  }
  $('lock').classList.add('unlocking');
  setTimeout(() => location.assign(link.href), 320);
}

async function passwordLogin(e) {
  e.preventDefault();
  const go = $('pw-go');
  if (go.disabled) return;
  go.disabled = true;
  $('pw-error').textContent = '';
  try {
    const res = await api('/auth/password', { username: $('pw-user').value, password: $('pw-pass').value, next: location.pathname });
    $('lock').classList.add('unlocking');
    setTimeout(() => location.assign(res.next || '/'), 320);
  } catch (err) {
    $('pw-pass').value = '';
    $('pw-pass').focus();
    $('pw-error').textContent = err.message;
    $('pw-form').classList.remove('shake');
    void $('pw-form').offsetWidth;
    $('pw-form').classList.add('shake');
  } finally {
    go.disabled = false;
  }
}

function setHomeData(data) {
  state.apps = new Map(data.apps.map((a) => [a.id, a]));
  state.layout = normalizeLayout(data.layout);
  state.themes = data.themes || [];
  applyTheme(themeById(state.layout.theme) || data.theme);
}

async function showHome(entering) {
  const data = await api('/api/home');
  setHomeData(data);
  $('lock').hidden = true;
  const home = $('home');
  home.hidden = false;
  setAvatar($('avatar'), state.me.user);
  render();
  if (entering) {
    home.classList.add('entering');
    setTimeout(() => home.classList.remove('entering'), 1400);
  }
}

async function refresh() {
  if (document.hidden || state.editing || state.openFolder || $('home').hidden || dragging) return;
  try {
    const data = await api('/api/home');
    const before = JSON.stringify([[...state.apps.values()], state.layout, state.themes]);
    setHomeData(data);
    if (JSON.stringify([[...state.apps.values()], state.layout, state.themes]) !== before) render();
  } catch (e) {
    if (e.status === 401) showLock();
  }
}

/* ---------- Launching ---------- */

function launch(appNode, url) {
  const icon = appNode.querySelector('.app-icon');
  const r = icon.getBoundingClientRect();
  const fx = $('launch');
  const cs = getComputedStyle(icon);
  fx.className = 'launch';
  fx.style.background = cs.backgroundImage !== 'none' ? cs.backgroundImage : cs.backgroundColor;
  Object.assign(fx.style, { left: `${r.left}px`, top: `${r.top}px`, width: `${r.width}px`, height: `${r.height}px`, borderRadius: cs.borderRadius });
  fx.getBoundingClientRect(); // commit start frame
  fx.classList.add('go');
  Object.assign(fx.style, { left: '0px', top: '0px', width: `${innerWidth}px`, height: `${innerHeight}px`, borderRadius: '0px' });
  setTimeout(() => location.assign(url), 300);
}

window.addEventListener('pageshow', () => {
  const fx = $('launch');
  fx.className = 'launch';
  fx.removeAttribute('style');
});

/* ---------- Edit mode ---------- */

function setEditing(on) {
  if (state.editing === on) return;
  state.editing = on;
  document.body.classList.toggle('editing', on);
  $('done').hidden = !on;
  if (!on) commitFolderName();
  render();
  if (!on) saveLayout();
}

/* ---------- Drag & drop ---------- */

let press = null; // pending long-press / drag candidate
let dragging = null;

function onPointerDown(e) {
  const node = e.target.closest('#home .app, #folder .app');
  if (!node || e.button > 0) return;
  press = { id: node.dataset.id, node, x: e.clientX, y: e.clientY, pointerId: e.pointerId, timer: null };
  node.classList.add('pressed');
  if (!state.editing) {
    press.timer = setTimeout(() => {
      if (!press) return;
      node.classList.remove('pressed');
      if (navigator.vibrate) navigator.vibrate(10);
      setEditing(true);
      // render() replaced the node; keep following the same item
      press.node = document.querySelector(`.app[data-id="${CSS.escape(press.id)}"]`);
    }, LONG_PRESS_MS);
  }
}

function onPointerMove(e) {
  if (dragging) {
    moveDrag(e.clientX, e.clientY);
    return;
  }
  if (!press || e.pointerId !== press.pointerId) return;
  if (Math.hypot(e.clientX - press.x, e.clientY - press.y) <= DRAG_SLOP) return;
  if (state.editing && press.node) startDrag(press, e.clientX, e.clientY);
  else cancelPress(); // a swipe, not a press
}

function onPointerUp() {
  if (dragging) {
    endDrag();
    press = null;
    return;
  }
  cancelPress();
}

function cancelPress() {
  if (!press) return;
  clearTimeout(press.timer);
  press.node?.classList.remove('pressed');
  press = null;
}

function startDrag(p, x, y) {
  clearTimeout(p.timer);
  const ghost = p.node.cloneNode(true);
  ghost.className = `${p.node.className} ghost`;
  ghost.classList.remove('pressed', 'dragging');
  document.body.append(ghost);
  p.node.classList.add('dragging');
  dragging = { id: p.id, ghost, edgeTimer: null, edgeDir: 0, leaveTimer: null, target: null };
  moveDrag(x, y);
}

function clearMarkers() {
  document.querySelectorAll('.drop-before, .drop-after, .drop-merge').forEach((n) => n.classList.remove('drop-before', 'drop-after', 'drop-merge'));
  $('dock').classList.remove('drop-target');
}

function dropTargetAt(x, y) {
  const hit = document.elementFromPoint(x, y);
  if (!hit) return null;

  if (state.openFolder) {
    if (!hit.closest('#folder-panel')) return { kind: 'outside-folder' };
    const app = hit.closest('#folder-apps .app');
    if (app && app.dataset.id !== dragging.id) {
      const r = app.getBoundingClientRect();
      return { kind: 'app', node: app, ctx: 'folder', zone: x < r.left + r.width / 2 ? 'before' : 'after' };
    }
    return { kind: 'folder-end' };
  }

  const app = hit.closest('.app:not(.ghost)');
  if (app && app.dataset.id !== dragging.id) {
    const r = app.querySelector('.app-icon').getBoundingClientRect();
    const inset = r.width * 0.22;
    const overCenter = x > r.left + inset && x < r.right - inset && y > r.top && y < r.bottom;
    const zone = overCenter && !isFolder(dragging.id) ? 'merge' : x < r.left + r.width / 2 ? 'before' : 'after';
    return { kind: 'app', node: app, ctx: app.closest('.dock') ? 'dock' : 'home', zone };
  }
  if (hit.closest('.dock')) return { kind: 'dock' };
  const page = hit.closest('.page');
  if (page) return { kind: 'page', page: Number(page.dataset.page) };
  return null;
}

function moveDrag(x, y) {
  const d = dragging;
  d.ghost.style.left = `${x}px`;
  d.ghost.style.top = `${y}px`;
  clearMarkers();
  d.target = dropTargetAt(x, y);
  const t = d.target;
  if (t?.kind === 'app') t.node.classList.add(`drop-${t.zone}`);
  if (t?.kind === 'dock' || t?.ctx === 'dock') $('dock').classList.add('drop-target');

  // Dragging out of an open folder: after a moment the folder closes and the
  // item continues as a loose app on the home screen
  if (state.openFolder) {
    if (t?.kind === 'outside-folder' && !d.leaveTimer) {
      d.leaveTimer = setTimeout(() => detachFromFolder(d), FOLDER_LEAVE_MS);
    } else if (t?.kind !== 'outside-folder') {
      clearTimeout(d.leaveTimer);
      d.leaveTimer = null;
    }
    return;
  }

  // Hold at a screen edge to flip pages
  const dir = x < 36 ? -1 : x > innerWidth - 36 ? 1 : 0;
  if (dir !== d.edgeDir) {
    clearTimeout(d.edgeTimer);
    d.edgeDir = dir;
    if (dir) {
      const flip = () => {
        goToPage(state.page + dir);
        d.edgeTimer = setTimeout(flip, 800);
      };
      d.edgeTimer = setTimeout(flip, 550);
    }
  }
}

function detachFromFolder(d) {
  d.leaveTimer = null;
  if (dragging !== d || !state.openFolder) return;
  const L = state.layout;
  const f = L.folders[state.openFolder];
  f.apps = f.apps.filter((x) => x !== d.id);
  commitFolderName();
  state.openFolder = null;
  $('folder').hidden = true;
  normalizeFolders(L);
  render(); // the dragged app is now "in hand", placed on drop
  d.target = null;
}

function endDrag() {
  const d = dragging;
  dragging = null;
  clearTimeout(d.edgeTimer);
  clearTimeout(d.leaveTimer);
  d.ghost.remove();
  clearMarkers();
  applyDrop(d.id, d.target);
}

function removeEverywhere(L, id) {
  L.order = L.order.filter((x) => x !== id);
  L.dock = L.dock.filter((x) => x !== id);
  for (const f of Object.values(L.folders)) f.apps = f.apps.filter((x) => x !== id);
}

function isPlaced(L, id) {
  return L.order.includes(id) || L.dock.includes(id) || Object.values(L.folders).some((f) => f.apps.includes(id));
}

// Empty folders vanish; a folder left with one app becomes that app, like on the phone
function normalizeFolders(L) {
  for (const [fid, f] of Object.entries(L.folders)) {
    if (f.apps.length > 1) continue;
    const swap = (list) => list.flatMap((x) => (x === fid ? f.apps.slice(0, 1) : [x]));
    L.order = swap(L.order);
    L.dock = swap(L.dock);
    delete L.folders[fid];
    if (state.openFolder === fid) {
      state.openFolder = null;
      $('folder').hidden = true;
    }
  }
}

function insertAtPageEnd(L, id, page) {
  const pageStart = page * state.perPage;
  const onPage = L.order.slice(pageStart, pageStart + state.perPage).length;
  L.order.splice(Math.min(L.order.length, pageStart + onPage), 0, id);
}

function applyDrop(id, target) {
  const L = state.layout;
  const fromDock = L.dock.includes(id);
  let openAfter = null;

  if (!target) {
    if (isPlaced(L, id)) return render();
    insertAtPageEnd(L, id, state.page); // dropped in no-man's land after leaving a folder
  } else if (target.kind === 'outside-folder') {
    removeEverywhere(L, id);
    insertAtPageEnd(L, id, state.page);
  } else if (target.kind === 'folder-end') {
    const f = L.folders[state.openFolder];
    f.apps = f.apps.filter((x) => x !== id).concat(id);
  } else if (target.kind === 'app' && target.ctx === 'folder') {
    const f = L.folders[state.openFolder];
    const apps = f.apps.filter((x) => x !== id);
    apps.splice(apps.indexOf(target.node.dataset.id) + (target.zone === 'after' ? 1 : 0), 0, id);
    f.apps = apps;
  } else if (target.kind === 'app' && target.zone === 'merge') {
    const tid = target.node.dataset.id;
    removeEverywhere(L, id);
    if (isFolder(tid)) {
      L.folders[tid].apps.push(id);
    } else {
      const fid = newFolderId();
      L.folders[fid] = { name: suggestFolderName(tid, id), apps: [tid, id] };
      L.order = L.order.map((x) => (x === tid ? fid : x));
      L.dock = L.dock.map((x) => (x === tid ? fid : x));
      openAfter = fid;
    }
  } else if (target.kind === 'app' || target.kind === 'dock') {
    const toDock = target.kind === 'dock' || target.ctx === 'dock';
    if (toDock && !fromDock && L.dock.length >= state.maxDock) return render();
    removeEverywhere(L, id);
    const list = toDock ? L.dock : L.order;
    const at = target.kind === 'dock' ? list.length : list.indexOf(target.node.dataset.id) + (target.zone === 'after' ? 1 : 0);
    list.splice(at, 0, id);
  } else if (target.kind === 'page') {
    removeEverywhere(L, id);
    insertAtPageEnd(L, id, target.page);
  }

  normalizeFolders(L);
  render();
  saveLayout();
  if (openAfter) openFolder(openAfter, true);
}

/* ---------- Spotlight ---------- */

let results = [];
let selected = 0;

function openSpotlight(initial = '') {
  if (state.editing || state.openFolder || $('home').hidden) return;
  $('spotlight').hidden = false;
  const input = $('spotlight-input');
  input.value = initial;
  input.focus();
  search();
}

function closeSpotlight() {
  $('spotlight').hidden = true;
}

function search() {
  const q = $('spotlight-input').value.trim().toLowerCase();
  const all = [...state.apps.values()];
  results = !q ? all.slice(0, 8) : all
    .map((a) => {
      const name = a.name.toLowerCase();
      const score = name.startsWith(q) ? 0 : name.includes(q) ? 1 : a.id.includes(q) ? 2 : -1;
      return { a, score };
    })
    .filter((r) => r.score >= 0)
    .sort((x, y) => x.score - y.score || x.a.name.localeCompare(y.a.name))
    .map((r) => r.a);
  selected = 0;
  const list = $('spotlight-results');
  list.replaceChildren(...results.map((a, i) => {
    const li = el('li');
    const link = el('a', 'result');
    link.href = a.url;
    link.setAttribute('role', 'option');
    link.setAttribute('aria-selected', String(i === selected));
    const text = el('div');
    text.append(el('div', 'result-name', a.name), el('div', 'result-host', a.id));
    link.append(iconEl(a), text);
    li.append(link);
    return li;
  }));
}

function moveSelection(delta) {
  if (!results.length) return;
  selected = (selected + delta + results.length) % results.length;
  [...$('spotlight-results').querySelectorAll('.result')].forEach((n, i) => {
    n.setAttribute('aria-selected', String(i === selected));
    if (i === selected) n.scrollIntoView({ block: 'nearest' });
  });
}

/* ---------- Account sheet ---------- */

function themeSwatch(t, active) {
  const b = el('button', 'swatch');
  b.type = 'button';
  b.title = t.name;
  b.setAttribute('aria-label', `${t.name} theme`);
  b.setAttribute('aria-pressed', String(active));
  const c = t.wallpaper.colors;
  b.style.setProperty('--s0', c[0]);
  b.style.setProperty('--s1', c[1] || c[0]);
  b.style.setProperty('--s2', c[2] || c[1] || c[0]);
  b.style.setProperty('--s-accent', t.accent);
  b.style.setProperty('--s-k', String(ICON_SHAPES[t.icons.shape] ?? 0.225));
  if (t.wallpaper.type === 'image' && t.wallpaper.image) b.style.backgroundImage = `url("${t.wallpaper.image}")`;
  b.dataset.wp = t.wallpaper.type;
  b.dataset.appearance = t.appearance;
  const dots = el('span', 'swatch-icons');
  for (let i = 0; i < 4; i++) dots.append(el('i'));
  b.append(dots, el('span', 'swatch-name', t.name));
  return b;
}

function openSheet() {
  const u = state.me.user;
  setAvatar($('sheet-avatar'), u);
  $('sheet-name').textContent = u.name;
  $('sheet-email').textContent = u.email || u.username || '';
  $('open-settings').hidden = !state.me.isAdmin;
  const current = document.body.dataset.theme;
  $('themes').replaceChildren(...state.themes.map((t) => {
    const b = themeSwatch(t, t.id === current);
    b.addEventListener('click', () => {
      applyTheme(t);
      state.layout.theme = t.id;
      saveLayout();
      $('themes').querySelectorAll('.swatch').forEach((s) => s.setAttribute('aria-pressed', String(s === b)));
    });
    return b;
  }));
  $('sheet').hidden = false;
}

function closeSheet() {
  $('sheet').hidden = true;
}

/* ---------- Wiring ---------- */

function wire() {
  const pagesEl = $('pages');
  const home = $('home');

  pagesEl.addEventListener('scroll', onPagesScroll, { passive: true });
  document.addEventListener('pointerdown', onPointerDown);
  window.addEventListener('pointermove', onPointerMove);
  window.addEventListener('pointerup', onPointerUp);
  window.addEventListener('pointercancel', () => (dragging ? endDrag() : cancelPress()));

  document.addEventListener('contextmenu', (e) => {
    if (e.target.closest('#home .app, #home .pages, #folder .app')) {
      e.preventDefault();
      setEditing(true);
    }
  });

  const onItemClick = (e) => {
    const node = e.target.closest('.app');
    if (!node) return false;
    if (isFolder(node.dataset.id)) {
      e.preventDefault();
      openFolder(node.dataset.id);
      return true;
    }
    if (state.editing) {
      e.preventDefault();
      return true;
    }
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return true; // let the browser open a tab
    e.preventDefault();
    launch(node, node.href);
    return true;
  };

  home.addEventListener('click', (e) => {
    if (onItemClick(e)) return;
    // Tapping the wallpaper ends edit mode, like on the phone
    if (state.editing && (e.target === pagesEl || e.target.classList.contains('page'))) setEditing(false);
  });

  $('folder').addEventListener('click', (e) => {
    if (onItemClick(e)) return;
    if (!e.target.closest('#folder-panel')) closeFolder();
  });
  $('folder-name').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      commitFolderName();
      $('folder-name').blur();
    }
  });
  $('folder-name').addEventListener('blur', commitFolderName);

  // Mouse wheels page horizontally; trackpads already scroll natively
  let wheelLock = 0;
  pagesEl.addEventListener('wheel', (e) => {
    if (Math.abs(e.deltaX) > Math.abs(e.deltaY) || Math.abs(e.deltaY) < 30) return;
    e.preventDefault();
    const now = Date.now();
    if (now < wheelLock) return;
    wheelLock = now + 450;
    goToPage(state.page + Math.sign(e.deltaY));
  }, { passive: false });

  // Swipe down on the wallpaper opens search
  let swipe = null;
  pagesEl.addEventListener('touchstart', (e) => {
    if (!state.editing && e.touches.length === 1) swipe = { x: e.touches[0].clientX, y: e.touches[0].clientY };
  }, { passive: true });
  pagesEl.addEventListener('touchend', (e) => {
    if (!swipe) return;
    const t = e.changedTouches[0];
    const dy = t.clientY - swipe.y;
    const dx = Math.abs(t.clientX - swipe.x);
    swipe = null;
    if (dy > 80 && dx < 40) openSpotlight();
  }, { passive: true });

  $('done').addEventListener('click', () => setEditing(false));
  $('avatar').addEventListener('click', openSheet);
  $('search-pill').addEventListener('click', () => openSpotlight());
  $('edit-home').addEventListener('click', () => {
    closeSheet();
    setEditing(true);
  });
  $('sheet').addEventListener('click', (e) => {
    if (e.target === $('sheet')) closeSheet();
  });

  $('spotlight').addEventListener('click', (e) => {
    if (e.target === $('spotlight')) closeSpotlight();
  });
  $('spotlight-input').addEventListener('input', search);
  $('spotlight-input').addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); moveSelection(1); }
    if (e.key === 'ArrowUp') { e.preventDefault(); moveSelection(-1); }
    if (e.key === 'Enter' && results[selected]) {
      e.preventDefault();
      location.assign(results[selected].url);
    }
  });

  // Lock screen: tap, Enter or swipe up
  const lock = $('lock');
  $('unlock').addEventListener('click', (e) => {
    e.preventDefault();
    unlock();
  });
  $('pw-form').addEventListener('submit', passwordLogin);
  let lockStart = null;
  lock.addEventListener('pointerdown', (e) => (lockStart = e.target.closest('.pw-form') ? null : e.clientY));
  lock.addEventListener('pointerup', (e) => {
    if (lockStart != null && lockStart - e.clientY > 70) unlock();
    lockStart = null;
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (!$('spotlight').hidden) return closeSpotlight();
      if (!$('sheet').hidden) return closeSheet();
      if (state.openFolder) return closeFolder();
      if (state.editing) return setEditing(false);
    }
    if (!$('lock').hidden) {
      if (e.key === 'Escape' && !$('pw-form').hidden) showLock(state.lock);
      else if ((e.key === 'Enter' || e.key === ' ') && !e.target.closest('input, button, a')) unlock();
      return;
    }
    if (e.target.closest('input, textarea') || !$('sheet').hidden || !$('spotlight').hidden || state.openFolder) return;
    if (e.key === 'ArrowRight') goToPage(state.page + 1);
    else if (e.key === 'ArrowLeft') goToPage(state.page - 1);
    else if (e.key === '/' || ((e.metaKey || e.ctrlKey) && e.key === 'k')) {
      e.preventDefault();
      openSpotlight();
    } else if (e.key.length === 1 && /\S/.test(e.key) && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      openSpotlight(e.key);
    }
  });

  let resizeTimer = null;
  window.addEventListener('resize', () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => !$('home').hidden && render(), 120);
  });

  document.addEventListener('visibilitychange', refresh);
  setInterval(refresh, 60_000);
}

async function boot() {
  tick();
  setInterval(tick, 1000);
  wire();
  try {
    const me = await api('/api/me');
    state.me = me;
    document.title = me.title;
    const fromLogin = document.referrer === '' || !document.referrer.startsWith(location.origin);
    await showHome(fromLogin);
  } catch (e) {
    if (e.data?.title) document.title = e.data.title;
    applyTheme(e.data?.theme);
    showLock(e.status === 401 ? e.data : { configured: true, oidc: true });
  } finally {
    document.body.classList.remove('booting');
  }
}

boot();
