package main

import (
	"reflect"
	"testing"
)

func TestReconcile(t *testing.T) {
	apps := []HomeApp{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}}

	stored := &Layout{Order: []string{"c", "gone", "a", "c"}, Dock: []string{"e", "gone"}, Wallpaper: "dusk"}
	got := reconcile(stored, apps)
	want := Layout{Order: []string{"c", "a", "b", "d"}, Dock: []string{"e"}, Folders: map[string]Folder{}, Theme: "dusk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconcile = %+v, want %+v", got, want)
	}
}

func TestReconcileFolders(t *testing.T) {
	apps := []HomeApp{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	stored := &Layout{
		Order: []string{"f_media", "f_single", "f_empty", "f_missing", "bad id"},
		Dock:  []string{"d"},
		Folders: map[string]Folder{
			"f_media":  {Name: "  ", Apps: []string{"a", "b", "gone", "a"}},
			"f_single": {Name: "Solo", Apps: []string{"c", "d"}}, // d is already in the dock
			"f_empty":  {Name: "Empty", Apps: []string{"gone"}},
		},
	}
	got := reconcile(stored, apps)
	want := Layout{
		Order:   []string{"f_media", "c"},
		Dock:    []string{"d"},
		Folders: map[string]Folder{"f_media": {Name: "Folder", Apps: []string{"a", "b"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconcile = %+v, want %+v", got, want)
	}
}

func TestDefaultLayoutGroupsByTag(t *testing.T) {
	apps := []HomeApp{
		{ID: "sonarr", Tags: []string{"Media"}}, {ID: "radarr", Tags: []string{"Media"}},
		{ID: "lonely", Tags: []string{"Solo"}}, {ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"},
	}
	got := reconcile(nil, apps)
	if f, ok := got.Folders["f_media"]; !ok || !reflect.DeepEqual(f.Apps, []string{"sonarr", "radarr"}) || f.Name != "Media" {
		t.Fatalf("media folder = %+v", got.Folders)
	}
	if !reflect.DeepEqual(got.Dock, []string{"lonely", "a", "b", "c"}) {
		t.Fatalf("dock = %v", got.Dock)
	}
	if !reflect.DeepEqual(got.Order, []string{"f_media", "d", "e"}) {
		t.Fatalf("order = %v", got.Order)
	}
}

func TestNormalizeTheme(t *testing.T) {
	th, err := normalizeTheme(Theme{Name: "My Theme", Wallpaper: ThemeWallpaper{Colors: []string{"#112233"}, Dim: 3}, Icons: ThemeIcons{Shape: "blob"}})
	if err != nil {
		t.Fatal(err)
	}
	if th.Icons.Shape != "squircle" || th.Wallpaper.Dim != 0.8 || th.Wallpaper.Type != "blobs" || !themeID.MatchString(th.ID) {
		t.Fatalf("normalized = %+v", th)
	}
	bad := []Theme{
		{Name: ""},
		{Name: "x", Wallpaper: ThemeWallpaper{Colors: []string{"red"}}},
		{Name: "x", Wallpaper: ThemeWallpaper{Type: "image", Image: "javascript:alert(1)"}},
		{Name: "x", Wallpaper: ThemeWallpaper{Type: "image", Image: "https://x/a.png\") ;background:red"}},
		{Name: "x", Accent: "blue"},
	}
	for _, b := range bad {
		if _, err := normalizeTheme(b); err == nil {
			t.Errorf("accepted %+v", b)
		}
	}
	s := Settings{Themes: []Theme{{ID: "mine", Name: "Mine"}}, DefaultTheme: "mine"}
	if resolveTheme(s, "nope").ID != "mine" || resolveTheme(s, "noir").ID != "noir" || resolveTheme(Settings{}).ID != "aurora" {
		t.Fatal("resolveTheme fallback order wrong")
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":               "/",
		"/settings/":     "/settings/",
		"//evil.example": "/",
		"/\\evil":        "/",
		"https://evil":   "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignVerify(t *testing.T) {
	tok, err := sign("secret", sessionData{Sub: "u1", Expires: 42})
	if err != nil {
		t.Fatal(err)
	}
	var out sessionData
	if err := verify("secret", tok, &out); err != nil || out.Sub != "u1" {
		t.Fatalf("verify = %v, %+v", err, out)
	}
	if err := verify("other", tok, &out); err == nil {
		t.Fatal("token verified with the wrong secret")
	}
	if err := verify("secret", tok+"x", &out); err == nil {
		t.Fatal("tampered token verified")
	}
}

func TestIsLinkableDomain(t *testing.T) {
	for d, want := range map[string]bool{
		"media.example.com": true,
		"example.com:8443":  true,
		"*.example.com":     false,
		"192.168.1.10":      false,
		"192.168.1.10:8080": false,
		"":                  false,
		"example.com/foo":   false,
	} {
		if got := isLinkableDomain(d); got != want {
			t.Errorf("isLinkableDomain(%q) = %v, want %v", d, got, want)
		}
	}
}

func TestVisibleApps(t *testing.T) {
	s := Settings{AdminGroup: "admins", Apps: map[string]AppOverride{
		"secret.example.com": {Groups: []string{"ops"}},
		"gone.example.com":   {Hidden: true},
	}}
	a := &App{routes: &RouteSource{routes: []Route{
		{Domain: "open.example.com"}, {Domain: "secret.example.com"}, {Domain: "gone.example.com"}, {Domain: "off.example.com", Disabled: true},
	}}}
	ids := func(apps []HomeApp) (out []string) {
		for _, x := range apps {
			out = append(out, x.ID)
		}
		return
	}
	if got := ids(a.visibleApps(s, User{Groups: []string{"family"}})); !reflect.DeepEqual(got, []string{"open.example.com"}) {
		t.Errorf("family sees %v", got)
	}
	if got := ids(a.visibleApps(s, User{Groups: []string{"ops"}})); len(got) != 2 {
		t.Errorf("ops sees %v", got)
	}
	if got := ids(a.visibleApps(s, User{Groups: []string{"admins"}})); len(got) != 2 {
		t.Errorf("admin sees %v", got)
	}
}
