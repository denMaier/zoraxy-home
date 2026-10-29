package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Theme describes the look of the home screen. The client turns it into CSS
// variables; CSS is only injected verbatim for admin-authored custom themes.
type Theme struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Builtin    bool           `json:"builtin,omitempty"`
	Appearance string         `json:"appearance"` // dark (light text) | light (dark text)
	Wallpaper  ThemeWallpaper `json:"wallpaper"`
	Icons      ThemeIcons     `json:"icons"`
	Font       string         `json:"font"`   // system | rounded | serif | mono
	Accent     string         `json:"accent"` // #rrggbb
	CSS        string         `json:"css,omitempty"`
}

type ThemeWallpaper struct {
	Type   string   `json:"type"`   // blobs | gradient | solid | image
	Colors []string `json:"colors"` // base + up to 3 highlights
	Image  string   `json:"image,omitempty"`
	Dim    float64  `json:"dim"` // 0..0.8 darkening over the wallpaper
}

type ThemeIcons struct {
	Shape      string `json:"shape"` // squircle | rounded | circle | square
	Style      string `json:"style"` // light | dark | glass
	HideLabels bool   `json:"hide_labels,omitempty"`
}

func builtin(id, name, appearance, wpType string, colors []string, shape, style, font, accent string) Theme {
	return Theme{
		ID: id, Name: name, Builtin: true, Appearance: appearance,
		Wallpaper: ThemeWallpaper{Type: wpType, Colors: colors},
		Icons:     ThemeIcons{Shape: shape, Style: style},
		Font:      font, Accent: accent,
	}
}

// builtinThemes keep the ids of the original wallpapers so stored choices still resolve
var builtinThemes = []Theme{
	builtin("aurora", "Aurora", "dark", "blobs", []string{"#071423", "#1f7a8c", "#7b4dd6", "#19c39a"}, "squircle", "light", "system", "#2c90ff"),
	builtin("dusk", "Dusk", "dark", "blobs", []string{"#1a0c1f", "#ff6a3d", "#b8336a", "#ffb347"}, "squircle", "light", "system", "#ff8a4c"),
	builtin("lagoon", "Lagoon", "dark", "blobs", []string{"#03202b", "#00a6c7", "#2357d9", "#6be3c4"}, "squircle", "light", "system", "#22c3e6"),
	builtin("meadow", "Meadow", "dark", "blobs", []string{"#0d1a0f", "#6fae3c", "#1d6b52", "#e4c65b"}, "squircle", "light", "rounded", "#8bd450"),
	builtin("graphite", "Graphite", "dark", "blobs", []string{"#111214", "#3a3d44", "#22252b", "#5a5f6a"}, "squircle", "dark", "system", "#a0a7b4"),
	builtin("noir", "Noir", "dark", "solid", []string{"#000000"}, "circle", "glass", "system", "#ffffff"),
	builtin("paper", "Paper", "light", "gradient", []string{"#f3ede2", "#e6d8c3", "#d9e3e8"}, "rounded", "light", "serif", "#b4552d"),
	builtin("terminal", "Terminal", "dark", "solid", []string{"#0b0f0a"}, "square", "dark", "mono", "#39ff14"),
}

func findTheme(themes []Theme, id string) (Theme, bool) {
	for _, t := range themes {
		if t.ID == id {
			return t, true
		}
	}
	return Theme{}, false
}

// allThemes lists built-ins followed by the admin's custom themes
func allThemes(s Settings) []Theme {
	return append(append([]Theme{}, builtinThemes...), s.Themes...)
}

// resolveTheme picks the user's theme, falling back to the default and then to Aurora
func resolveTheme(s Settings, ids ...string) Theme {
	all := allThemes(s)
	for _, id := range append(ids, s.DefaultTheme) {
		if t, ok := findTheme(all, id); ok && id != "" {
			return t
		}
	}
	return builtinThemes[0]
}

var (
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	themeID  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// normalizeTheme validates an admin-submitted theme and fills defaults
func normalizeTheme(t Theme) (Theme, error) {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 40 {
		return t, errors.New("theme name must be 1–40 characters")
	}
	if t.ID == "" {
		t.ID = slugify(t.Name)
		if t.ID == "" {
			t.ID = "theme"
		}
		t.ID += "-" + randomHex(2)
	}
	if !themeID.MatchString(t.ID) {
		return t, errors.New("invalid theme id")
	}
	t.Builtin = false

	if !oneOf(t.Appearance, "dark", "light") {
		t.Appearance = "dark"
	}
	w := &t.Wallpaper
	if !oneOf(w.Type, "blobs", "gradient", "solid", "image") {
		w.Type = "blobs"
	}
	if len(w.Colors) == 0 {
		w.Colors = []string{"#071423"}
	}
	if len(w.Colors) > 4 {
		w.Colors = w.Colors[:4]
	}
	for _, c := range w.Colors {
		if !hexColor.MatchString(c) {
			return t, fmt.Errorf("wallpaper color %q must look like #1a2b3c", c)
		}
	}
	w.Image = strings.TrimSpace(w.Image)
	if w.Type == "image" {
		if !strings.HasPrefix(w.Image, "https://") && !strings.HasPrefix(w.Image, "http://") && !strings.HasPrefix(w.Image, "/") {
			return t, errors.New("wallpaper image must be an http(s) URL")
		}
		if strings.ContainsAny(w.Image, "\"'()\\ \n") {
			return t, errors.New("wallpaper image URL contains characters that are not allowed")
		}
	} else {
		w.Image = ""
	}
	if w.Dim < 0 {
		w.Dim = 0
	}
	if w.Dim > 0.8 {
		w.Dim = 0.8
	}

	if !oneOf(t.Icons.Shape, "squircle", "rounded", "circle", "square") {
		t.Icons.Shape = "squircle"
	}
	if !oneOf(t.Icons.Style, "light", "dark", "glass") {
		t.Icons.Style = "light"
	}
	if !oneOf(t.Font, "system", "rounded", "serif", "mono") {
		t.Font = "system"
	}
	if t.Accent == "" {
		t.Accent = "#2c90ff"
	}
	if !hexColor.MatchString(t.Accent) {
		return t, errors.New("accent must look like #1a2b3c")
	}
	if len(t.CSS) > 20000 {
		return t, errors.New("custom CSS is limited to 20 000 characters")
	}
	return t, nil
}
