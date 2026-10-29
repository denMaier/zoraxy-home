package main

import (
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type App struct {
	cfg      *Config
	users    *UserStore
	accounts *AccountStore
	routes   *RouteSource
	auth     *Authenticator

	ipLimit   *limiter // failed password sign-ins per client IP
	userLimit *limiter // failed password sign-ins per username
}

// HomeApp is one icon on the home screen
type HomeApp struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	URL  string   `json:"url"`
	Icon string   `json:"icon"` // URL or dashboard-icons slug
	Tags []string `json:"tags,omitempty"`
}

// AdminApp is a proxy rule together with its homepage override
type AdminApp struct {
	HomeApp
	Route    Route       `json:"route"`
	Override AppOverride `json:"override"`
	Hidden   bool        `json:"hidden"`
	Groups   []string    `json:"groups"`
}

/* App catalog */

func defaultName(domain string) string {
	label, _, _ := strings.Cut(domain, ".")
	if label == "" {
		return domain
	}
	label = strings.NewReplacer("-", " ", "_", " ").Replace(label)
	return strings.ToUpper(label[:1]) + label[1:]
}

func defaultIcon(domain string) string {
	label, _, _ := strings.Cut(domain, ".")
	return label
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// catalog merges live Zoraxy routes with admin overrides. Disabled rules and the
// homepage's own domain are left out.
func (a *App) catalog(s Settings) []AdminApp {
	self := hostOf(s.PublicURL)
	var out []AdminApp
	for _, r := range a.routes.Routes() {
		if r.Disabled || r.Domain == self {
			continue
		}
		o := s.Apps[r.Domain]
		app := AdminApp{
			HomeApp: HomeApp{
				ID:   r.Domain,
				Name: firstNonEmpty(o.Name, defaultName(r.Domain)),
				URL:  firstNonEmpty(o.URL, "https://"+r.Domain),
				Icon: firstNonEmpty(o.Icon, defaultIcon(r.Domain)),
				Tags: r.Tags,
			},
			Route:    r,
			Override: o,
			Hidden:   o.Hidden,
			Groups:   o.Groups,
		}
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func hasAny(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

func isAdmin(s Settings, u User) bool {
	return s.AdminGroup != "" && hasAny(u.Groups, []string{s.AdminGroup})
}

func (a *App) visibleApps(s Settings, u User) []HomeApp {
	admin := isAdmin(s, u)
	var out []HomeApp
	for _, app := range a.catalog(s) {
		if app.Hidden {
			continue
		}
		if len(app.Groups) > 0 && !admin && !hasAny(u.Groups, app.Groups) {
			continue
		}
		out = append(out, app.HomeApp)
	}
	return out
}

var folderID = regexp.MustCompile(`^f_[a-z0-9_-]{1,40}$`)

const maxFolderName = 40

// reconcile keeps a stored layout in step with the apps the user can currently
// see: removed apps disappear, new ones are appended to the end, folders that
// end up with a single app dissolve into that app, empty ones vanish.
func reconcile(l *Layout, apps []HomeApp) Layout {
	if l == nil {
		return defaultLayout(apps)
	}
	visible := make(map[string]bool, len(apps))
	for _, a := range apps {
		visible[a.ID] = true
	}
	out := Layout{Order: []string{}, Dock: []string{}, Folders: map[string]Folder{}, Theme: firstNonEmpty(l.Theme, l.Wallpaper)}
	seen := map[string]bool{}
	place := func(dst *[]string, ids []string) {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			if !folderID.MatchString(id) {
				if visible[id] {
					*dst = append(*dst, id)
					seen[id] = true
				}
				continue
			}
			f, ok := l.Folders[id]
			if !ok {
				continue
			}
			seen[id] = true
			var members []string
			for _, a := range f.Apps {
				if visible[a] && !seen[a] {
					members = append(members, a)
					seen[a] = true
				}
			}
			switch len(members) {
			case 0:
			case 1:
				*dst = append(*dst, members[0])
			default:
				name := strings.TrimSpace(f.Name)
				if name == "" {
					name = "Folder"
				}
				if len(name) > maxFolderName {
					name = name[:maxFolderName]
				}
				out.Folders[id] = Folder{Name: name, Apps: members}
				*dst = append(*dst, id)
			}
		}
	}
	place(&out.Dock, l.Dock)
	place(&out.Order, l.Order)
	for _, a := range apps {
		if !seen[a.ID] {
			out.Order = append(out.Order, a.ID)
		}
	}
	return out
}

// defaultLayout gives a first-time user a starting point: apps sharing a Zoraxy
// tag go into a folder named after it, four untagged apps go into the dock.
func defaultLayout(apps []HomeApp) Layout {
	out := Layout{Order: []string{}, Dock: []string{}, Folders: map[string]Folder{}}
	byTag := map[string][]string{}
	var tags []string
	for _, a := range apps {
		if len(a.Tags) == 0 {
			continue
		}
		t := strings.TrimSpace(a.Tags[0])
		if t == "" {
			continue
		}
		if _, ok := byTag[t]; !ok {
			tags = append(tags, t)
		}
		byTag[t] = append(byTag[t], a.ID)
	}
	inFolder := map[string]string{}
	for _, t := range tags {
		if len(byTag[t]) < 2 {
			continue
		}
		id := "f_" + slugify(t)
		if id == "f_" {
			id = "f_" + randomHex(3)
		}
		if _, dup := out.Folders[id]; dup {
			id += "_" + randomHex(2)
		}
		name := t
		if len(name) > maxFolderName {
			name = name[:maxFolderName]
		}
		out.Folders[id] = Folder{Name: name, Apps: byTag[t]}
		for _, a := range byTag[t] {
			inFolder[a] = id
		}
	}
	placed := map[string]bool{}
	for _, a := range apps {
		if fid, ok := inFolder[a.ID]; ok {
			if !placed[fid] {
				out.Order = append(out.Order, fid)
				placed[fid] = true
			}
		} else if len(out.Dock) < 4 {
			out.Dock = append(out.Dock, a.ID)
		} else {
			out.Order = append(out.Order, a.ID)
		}
	}
	return out
}

/* Login policy */

func (a *App) onLogin(u User) error {
	s := a.cfg.Get()
	if existing, ok := a.users.Get(u.Sub); ok && existing.Blocked {
		return errors.New("Your account has been blocked on this homepage. Ask an administrator for access.")
	}
	if len(s.AllowedGroups) > 0 && !isAdmin(s, u) && !hasAny(u.Groups, s.AllowedGroups) {
		return errors.New("Your account is not in a group that may use this homepage.")
	}
	_, err := a.users.Upsert(u)
	return err
}

// currentUser resolves the session cookie to a stored, unblocked user
func (a *App) currentUser(r *http.Request) (User, bool) {
	sess, ok := a.auth.Session(r)
	if !ok {
		return User{}, false
	}
	u, ok := a.users.Get(sess.Sub)
	if !ok || u.Blocked {
		return User{}, false
	}
	// Local accounts: deleting the account or changing its password ends sessions,
	// and group or name changes apply right away
	if username, isLocal := strings.CutPrefix(sess.Sub, localSubPrefix); isLocal {
		acc, ok := a.accounts.Get(username)
		issued := time.Unix(sess.Expires, 0).Add(-sessionTTL)
		if !ok || acc.PasswordChanged.After(issued.Add(time.Second)) {
			return User{}, false
		}
		u.Name, u.Email, u.Groups = acc.Name, acc.Email, acc.Groups
	}
	return u, true
}

// handlePasswordLogin signs in a local account. It answers with JSON for the
// lock screen form instead of redirecting.
func (a *App) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	s := a.cfg.Get()
	if r.Header.Get("X-Requested-With") != "zoraxy-home" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing request header"})
		return
	}
	if !s.PasswordReady(a.accounts.Count()) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Password sign-in is turned off."})
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Next     string `json:"next"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	username := normalizeUsername(in.Username)
	ipKey, userKey := clientIP(r), username
	if !a.ipLimit.Allowed(ipKey) || !a.userLimit.Allowed(userKey) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many failed attempts. Try again in a few minutes."})
		return
	}
	acc, ok := a.accounts.Verify(username, in.Password)
	if !ok {
		a.ipLimit.Fail(ipKey)
		a.userLimit.Fail(userKey)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Wrong username or password."})
		return
	}
	a.userLimit.Reset(userKey)
	user := User{Sub: localSub(acc.Username), Name: acc.Name, Username: acc.Username, Email: acc.Email, Groups: acc.Groups}
	if err := a.onLogin(user); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	sess, err := sign(s.SessionSecret, sessionData{Sub: user.Sub, Expires: time.Now().Add(sessionTTL).Unix()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setCookie(w, s, sessionCookie, sess, sessionTTL)
	writeJSON(w, http.StatusOK, map[string]string{"next": safeNext(in.Next)})
}

/* Homepage */

func (a *App) RegisterHomepage(mux *http.ServeMux) {
	home, _ := fs.Sub(webFS, "web/home")
	static := http.FileServer(http.FS(home))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, home, "index.html")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		s := a.cfg.Get()
		w.Header().Set("Content-Type", "application/manifest+json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":             s.Title,
			"short_name":       s.Title,
			"start_url":        "/",
			"display":          "standalone",
			"background_color": "#0b1020",
			"theme_color":      "#0b1020",
			"icons":            []map[string]string{{"src": "/static/icon.svg", "sizes": "any", "type": "image/svg+xml"}},
		})
	})

	mux.HandleFunc("GET /auth/login", a.auth.HandleLogin)
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		a.auth.HandleCallback(w, r, a.onLogin)
	})
	mux.HandleFunc("GET /auth/logout", a.auth.HandleLogout)
	mux.HandleFunc("POST /auth/password", a.handlePasswordLogin)

	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		s := a.cfg.Get()
		u, ok := a.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"title":      s.Title,
				"configured": s.OIDCReady() || s.PasswordReady(a.accounts.Count()),
				"oidc":       s.OIDCReady(),
				"password":   s.PasswordReady(a.accounts.Count()),
				"theme":      resolveTheme(s),
				"provider":   s.OIDC.ProviderName,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"title":   s.Title,
			"user":    map[string]any{"name": u.Name, "username": u.Username, "email": u.Email, "picture": u.Picture, "groups": u.Groups},
			"isAdmin": isAdmin(s, u),
		})
	})

	mux.HandleFunc("GET /api/home", a.requireUser(func(w http.ResponseWriter, r *http.Request, u User) {
		s := a.cfg.Get()
		apps := a.visibleApps(s, u)
		layout := reconcile(u.Layout, apps)
		writeJSON(w, http.StatusOK, map[string]any{
			"apps":   apps,
			"layout": layout,
			"themes": allThemes(s),
			"theme":  resolveTheme(s, layout.Theme),
		})
	}))

	mux.HandleFunc("POST /api/layout", a.requireUser(func(w http.ResponseWriter, r *http.Request, u User) {
		var l Layout
		if !readJSON(w, r, &l) {
			return
		}
		if len(l.Dock) > 6 {
			l.Dock = l.Dock[:6]
		}
		if len(l.Folders) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many folders"})
			return
		}
		// Only ever persist ids the user can actually see
		apps := a.visibleApps(a.cfg.Get(), u)
		clean := reconcile(&l, apps)
		if err := a.users.SetLayout(u.Sub, clean); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, clean)
	}))

	// Admin pages for members of the admin group, without the OIDC settings
	adminPage := a.adminHandler("/settings", false)
	mux.Handle("/settings/", a.requireAdminHandler(adminPage))
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/settings/", http.StatusMovedPermanently)
	})
}

func (a *App) requireUser(next func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "zoraxy-home" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing request header"})
			return
		}
		next(w, r, u)
	}
}

func (a *App) requireAdminHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.currentUser(r)
		if !ok {
			if strings.Contains(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
			} else {
				http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
			}
			return
		}
		if !isAdmin(a.cfg.Get(), u) {
			http.Error(w, "Only members of the admin group can open settings.", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "zoraxy-home" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing request header"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

/* Plugin UI inside Zoraxy */

// RegisterPluginUI serves the full admin page, including OIDC settings. It is only
// reachable on 127.0.0.1 through Zoraxy's authenticated plugin UI proxy.
func (a *App) RegisterPluginUI(mux *http.ServeMux) {
	mux.Handle(UI_PATH+"/", a.adminHandler(UI_PATH, true))
}

var adminTmpl = template.Must(template.ParseFS(webFS, "web/admin/index.html"))

// adminHandler serves the admin page and its API below prefix
func (a *App) adminHandler(prefix string, full bool) http.Handler {
	assets, _ := fs.Sub(webFS, "web/admin")
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		adminTmpl.Execute(w, map[string]any{
			"csrfToken": r.Header.Get("X-Zoraxy-Csrf"),
			"embedded":  full,
			"title":     a.cfg.Get().Title,
		})
	})
	mux.Handle("GET /admin.js", http.FileServer(http.FS(assets)))
	mux.Handle("GET /admin.css", http.FileServer(http.FS(assets)))

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		s := a.cfg.Get()
		last, syncErr := a.routes.Status()
		groupSet := map[string]bool{}
		users := a.users.List()
		for _, u := range users {
			for _, g := range u.Groups {
				groupSet[g] = true
			}
		}
		accounts := a.accounts.List()
		for _, acc := range accounts {
			for _, g := range acc.Groups {
				groupSet[g] = true
			}
		}
		for _, g := range append(s.AllowedGroups, s.AdminGroup) {
			if g != "" {
				groupSet[g] = true
			}
		}
		groups := make([]string, 0, len(groupSet))
		for g := range groupSet {
			groups = append(groups, g)
		}
		sort.Strings(groups)

		state := map[string]any{
			"apps":          a.catalog(s),
			"users":         users,
			"accounts":      accounts,
			"passwordLogin": s.PasswordLogin,
			"groups":        groups,
			"adminGroup":    s.AdminGroup,
			"sync":          map[string]any{"last": last, "error": errString(syncErr)},
			"full":          full,
			"themes":        allThemes(s),
			"defaultTheme":  resolveTheme(s).ID,
		}
		if full {
			state["settings"] = publicSettings(s)
		}
		writeJSON(w, http.StatusOK, state)
	})

	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := a.routes.Sync(); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/app", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID       string      `json:"id"`
			Override AppOverride `json:"override"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if req.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
			return
		}
		o := req.Override
		o.Name, o.Icon, o.URL = strings.TrimSpace(o.Name), strings.TrimSpace(o.Icon), strings.TrimSpace(o.URL)
		if o.URL != "" && !strings.HasPrefix(o.URL, "https://") && !strings.HasPrefix(o.URL, "http://") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "link must start with http:// or https://"})
			return
		}
		o.Groups = cleanList(o.Groups)
		err := a.cfg.Update(func(s *Settings) {
			if o.Name == "" && o.Icon == "" && o.URL == "" && !o.Hidden && len(o.Groups) == 0 {
				delete(s.Apps, req.ID)
			} else {
				s.Apps[req.ID] = o
			}
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/user", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Sub     string `json:"sub"`
			Blocked *bool  `json:"blocked"`
			Forget  bool   `json:"forget"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		var err error
		switch {
		case req.Forget:
			err = a.users.Delete(req.Sub)
		case req.Blocked != nil:
			err = a.users.SetBlocked(req.Sub, *req.Blocked)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/account", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account
			Password string `json:"password"`
			Create   bool   `json:"create"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := a.accounts.Put(req.Account, req.Password, req.Create); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Password != "" {
			a.userLimit.Reset(normalizeUsername(req.Username))
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/account/delete", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		username := normalizeUsername(req.Username)
		if err := a.accounts.Delete(username); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := a.users.Delete(localSub(username)); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/theme", func(w http.ResponseWriter, r *http.Request) {
		var in Theme
		if !readJSON(w, r, &in) {
			return
		}
		if _, isBuiltin := findTheme(builtinThemes, in.ID); isBuiltin {
			in.ID = "" // editing a built-in saves a copy
		}
		t, err := normalizeTheme(in)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		err = a.cfg.Update(func(s *Settings) {
			for i := range s.Themes {
				if s.Themes[i].ID == t.ID {
					s.Themes[i] = t
					return
				}
			}
			s.Themes = append(s.Themes, t)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	})

	mux.HandleFunc("POST /api/theme/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		err := a.cfg.Update(func(s *Settings) {
			kept := s.Themes[:0]
			for _, t := range s.Themes {
				if t.ID != in.ID {
					kept = append(kept, t)
				}
			}
			s.Themes = kept
			if s.DefaultTheme == in.ID {
				s.DefaultTheme = builtinThemes[0].ID
			}
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/theme/default", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if _, ok := findTheme(allThemes(a.cfg.Get()), in.ID); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown theme"})
			return
		}
		if err := a.cfg.Update(func(s *Settings) { s.DefaultTheme = in.ID }); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	if full {
		mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
			var in Settings
			if !readJSON(w, r, &in) {
				return
			}
			if in.OIDC.Issuer != "" {
				if err := validIssuer(strings.TrimSpace(in.OIDC.Issuer)); err != nil {
					writeError(w, http.StatusBadRequest, err)
					return
				}
			}
			if in.PublicURL != "" {
				if u, err := url.Parse(in.PublicURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "public URL must look like https://home.example.com"})
					return
				}
			}
			err := a.cfg.Update(func(s *Settings) {
				s.ListenAddr = firstNonEmpty(strings.TrimSpace(in.ListenAddr), s.ListenAddr)
				s.PublicURL = strings.TrimRight(strings.TrimSpace(in.PublicURL), "/")
				s.Title = firstNonEmpty(strings.TrimSpace(in.Title), "Home")
				s.OIDC.Issuer = strings.TrimRight(strings.TrimSpace(in.OIDC.Issuer), "/")
				s.OIDC.ClientID = strings.TrimSpace(in.OIDC.ClientID)
				if in.OIDC.ClientSecret != secretPlaceholder {
					s.OIDC.ClientSecret = strings.TrimSpace(in.OIDC.ClientSecret)
				}
				if scopes := cleanList(in.OIDC.Scopes); len(scopes) > 0 {
					s.OIDC.Scopes = scopes
				}
				s.OIDC.GroupsClaim = strings.TrimSpace(in.OIDC.GroupsClaim)
				s.OIDC.ProviderName = strings.TrimSpace(in.OIDC.ProviderName)
				s.AdminGroup = strings.TrimSpace(in.AdminGroup)
				s.AllowedGroups = cleanList(in.AllowedGroups)
				s.PasswordLogin = in.PasswordLogin
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		})

		mux.HandleFunc("POST /api/test-oidc", func(w http.ResponseWriter, r *http.Request) {
			// Test the issuer typed into the form, falling back to the saved one
			var in struct {
				Issuer string `json:"issuer"`
			}
			if !readJSON(w, r, &in) {
				return
			}
			s := a.cfg.Get()
			if issuer := strings.TrimRight(strings.TrimSpace(in.Issuer), "/"); issuer != "" {
				s.OIDC.Issuer = issuer
			}
			if err := validIssuer(s.OIDC.Issuer); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if _, err := oidc.NewProvider(r.Context(), s.OIDC.Issuer); err != nil {
				writeError(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"ok": "Discovery document and keys reachable"})
		})
	}

	return http.StripPrefix(prefix, mux)
}

const secretPlaceholder = "••••••••"

func validIssuer(issuer string) error {
	if issuer == "" {
		return errors.New("issuer is empty: enter your identity provider URL, e.g. https://auth.example.com")
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("issuer must be a full URL including https://, e.g. https://auth.example.com")
	}
	return nil
}

// publicSettings never sends the client secret or session key to the browser
func publicSettings(s Settings) map[string]any {
	secret := ""
	if s.OIDC.ClientSecret != "" {
		secret = secretPlaceholder
	}
	return map[string]any{
		"listen_addr": s.ListenAddr,
		"public_url":  s.PublicURL,
		"title":       s.Title,
		"oidc": map[string]any{
			"issuer":        s.OIDC.Issuer,
			"client_id":     s.OIDC.ClientID,
			"client_secret": secret,
			"scopes":        s.OIDC.Scopes,
			"groups_claim":  s.OIDC.GroupsClaim,
			"provider_name": s.OIDC.ProviderName,
		},
		"admin_group":    s.AdminGroup,
		"allowed_groups": s.AllowedGroups,
		"password_login": s.PasswordLogin,
		"callback_url":   strings.TrimRight(s.PublicURL, "/") + "/auth/callback",
	}
}

/* Helpers */

func cleanList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src * data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

var messageTmpl = template.Must(template.New("msg").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title><link rel="stylesheet" href="/static/style.css"></head>
<body class="message-page"><main class="message-card"><h1>{{.Title}}</h1><p>{{.Message}}</p>
<a class="pill-button" href="/">Back to lock screen</a></main></body></html>`))

func renderMessage(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	messageTmpl.Execute(w, map[string]string{"Title": firstNonEmpty(title, "Home"), "Message": msg})
}
