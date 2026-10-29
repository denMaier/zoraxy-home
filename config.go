package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// OIDCSettings describes the OpenID Connect client registered in the identity provider
type OIDCSettings struct {
	Issuer       string   `json:"issuer"` // e.g. https://auth.example.com
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`        // openid is always requested
	GroupsClaim  string   `json:"groups_claim"`  // Pocket ID uses "groups"
	ProviderName string   `json:"provider_name"` // shown on the lock screen, e.g. "Pocket ID"
}

// AppOverride lets an admin adjust how a proxy rule is shown on the homepage
type AppOverride struct {
	Name   string   `json:"name,omitempty"`
	Icon   string   `json:"icon,omitempty"`   // URL or dashboard-icons slug
	URL    string   `json:"url,omitempty"`    // replaces https://<domain>
	Hidden bool     `json:"hidden,omitempty"` // never shown to anyone
	Groups []string `json:"groups,omitempty"` // empty = every signed-in user
}

type Settings struct {
	ListenAddr    string                 `json:"listen_addr"` // homepage listener, needs restart
	PublicURL     string                 `json:"public_url"`  // https://home.example.com, used for the OIDC callback
	Title         string                 `json:"title"`
	OIDC          OIDCSettings           `json:"oidc"`
	AdminGroup    string                 `json:"admin_group"`    // members can manage apps and users from the homepage
	AllowedGroups []string               `json:"allowed_groups"` // empty = anyone the identity provider lets through
	PasswordLogin bool                   `json:"password_login"` // allow local accounts (accounts.json)
	SessionSecret string                 `json:"session_secret"`
	Apps          map[string]AppOverride `json:"apps"`          // keyed by the rule's matching domain
	Themes        []Theme                `json:"themes"`        // custom themes, built-ins live in themes.go
	DefaultTheme  string                 `json:"default_theme"` // for the lock screen and new users
}

// PasswordReady reports whether local accounts may sign in
func (s Settings) PasswordReady(accounts int) bool {
	return s.PasswordLogin && accounts > 0
}

// OIDCReady reports whether enough is configured to attempt a login
func (s Settings) OIDCReady() bool {
	return s.PublicURL != "" && s.OIDC.Issuer != "" && s.OIDC.ClientID != ""
}

type Config struct {
	path     string
	mu       sync.RWMutex
	settings Settings
	onChange []func(Settings)
}

func defaultSettings() Settings {
	return Settings{
		ListenAddr: "127.0.0.1:8099",
		Title:      "Home",
		OIDC: OIDCSettings{
			Scopes:      []string{"openid", "profile", "email", "groups"},
			GroupsClaim: "groups",
		},
		AdminGroup:   "home_admins",
		Apps:         map[string]AppOverride{},
		DefaultTheme: "aurora",
	}
}

func LoadConfig(dir string) (*Config, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &Config{path: filepath.Join(dir, "config.json"), settings: defaultSettings()}
	raw, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &c.settings); err != nil {
			return nil, err
		}
	}
	if c.settings.Apps == nil {
		c.settings.Apps = map[string]AppOverride{}
	}
	if c.settings.SessionSecret == "" {
		c.settings.SessionSecret = randomHex(32)
	}
	return c, c.save()
}

// Get returns a copy of the current settings
func (c *Config) Get() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.settings
	s.Apps = make(map[string]AppOverride, len(c.settings.Apps))
	for k, v := range c.settings.Apps {
		s.Apps[k] = v
	}
	s.Themes = append([]Theme(nil), c.settings.Themes...)
	return s
}

// Update applies fn to the settings, persists them and notifies listeners
func (c *Config) Update(fn func(*Settings)) error {
	c.mu.Lock()
	fn(&c.settings)
	err := c.save()
	listeners := c.onChange
	c.mu.Unlock()
	if err != nil {
		return err
	}
	s := c.Get()
	for _, l := range listeners {
		l(s)
	}
	return nil
}

func (c *Config) OnChange(fn func(Settings)) {
	c.mu.Lock()
	c.onChange = append(c.onChange, fn)
	c.mu.Unlock()
}

// save must be called with the lock held
func (c *Config) save() error {
	return writeJSONAtomic(c.path, c.settings)
}

func writeJSONAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
