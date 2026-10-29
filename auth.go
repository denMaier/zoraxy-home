package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "zh_session"
	flowCookie    = "zh_oidc"
	sessionTTL    = 14 * 24 * time.Hour
	flowTTL       = 10 * time.Minute
)

// Authenticator handles the Pocket ID login and signed session cookies
type Authenticator struct {
	cfg *Config

	mu        sync.Mutex
	provider  *oidc.Provider
	forIssuer string
}

func NewAuthenticator(cfg *Config) *Authenticator {
	a := &Authenticator{cfg: cfg}
	// Rediscover the provider whenever the OIDC settings change
	cfg.OnChange(func(Settings) {
		a.mu.Lock()
		a.provider = nil
		a.mu.Unlock()
	})
	return a
}

func (a *Authenticator) getProvider(s Settings) (*oidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	issuer := strings.TrimRight(s.OIDC.Issuer, "/")
	if a.provider != nil && a.forIssuer == issuer {
		return a.provider, nil
	}
	// go-oidc keeps this context for later JWKS refreshes, so it must outlive the request
	ctx := oidc.ClientContext(context.Background(), &http.Client{Timeout: 10 * time.Second})
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", issuer, err)
	}
	a.provider, a.forIssuer = p, issuer
	return p, nil
}

func (a *Authenticator) oauthConfig(p *oidc.Provider, s Settings) *oauth2.Config {
	scopes := []string{oidc.ScopeOpenID}
	for _, sc := range s.OIDC.Scopes {
		if sc != "" && sc != oidc.ScopeOpenID {
			scopes = append(scopes, sc)
		}
	}
	return &oauth2.Config{
		ClientID:     s.OIDC.ClientID,
		ClientSecret: s.OIDC.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  strings.TrimRight(s.PublicURL, "/") + "/auth/callback",
		Scopes:       scopes,
	}
}

/* Signed cookies */

func sign(secret string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verify(secret, token string, out any) error {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type sessionData struct {
	Sub     string `json:"sub"`
	Expires int64  `json:"exp"`
	IDToken string `json:"idt,omitempty"` // kept for RP-initiated logout
}

type flowData struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Expires  int64  `json:"exp"`
}

func secureCookies(s Settings) bool {
	return strings.HasPrefix(s.PublicURL, "https://")
}

func setCookie(w http.ResponseWriter, s Settings, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secureCookies(s),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, s Settings, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureCookies(s)})
}

// Session returns the signed-in subject, if any
func (a *Authenticator) Session(r *http.Request) (sessionData, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return sessionData{}, false
	}
	var sess sessionData
	if err := verify(a.cfg.Get().SessionSecret, c.Value, &sess); err != nil {
		return sessionData{}, false
	}
	if time.Now().Unix() > sess.Expires {
		return sessionData{}, false
	}
	return sess, true
}

// safeNext only allows local paths so the login cannot be used as an open redirect
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	s := a.cfg.Get()
	if !s.OIDCReady() {
		http.Error(w, "Sign-in is not configured yet. Open the Home plugin in Zoraxy to set up sign-in.", http.StatusServiceUnavailable)
		return
	}
	p, err := a.getProvider(s)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	flow := flowData{
		State:    randomHex(16),
		Nonce:    randomHex(16),
		Verifier: oauth2.GenerateVerifier(),
		Next:     safeNext(r.URL.Query().Get("next")),
		Expires:  time.Now().Add(flowTTL).Unix(),
	}
	token, err := sign(s.SessionSecret, flow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setCookie(w, s, flowCookie, token, flowTTL)
	target := a.oauthConfig(p, s).AuthCodeURL(flow.State, oidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.Verifier))
	http.Redirect(w, r, target, http.StatusFound)
}

// HandleCallback finishes the login. onLogin turns verified claims into a stored
// user and decides whether that user may sign in.
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request, onLogin func(User) error) {
	s := a.cfg.Get()
	fail := func(status int, msg string) {
		clearCookie(w, s, flowCookie)
		renderMessage(w, status, s.Title, msg)
	}

	c, err := r.Cookie(flowCookie)
	if err != nil {
		fail(http.StatusBadRequest, "Your sign-in attempt expired. Please try again.")
		return
	}
	var flow flowData
	if err := verify(s.SessionSecret, c.Value, &flow); err != nil || time.Now().Unix() > flow.Expires {
		fail(http.StatusBadRequest, "Your sign-in attempt expired. Please try again.")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(http.StatusUnauthorized, "The identity provider refused the sign-in: "+e)
		return
	}
	if r.URL.Query().Get("state") != flow.State {
		fail(http.StatusBadRequest, "Sign-in state mismatch. Please try again.")
		return
	}

	p, err := a.getProvider(s)
	if err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := a.oauthConfig(p, s).Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		fail(http.StatusBadGateway, "Token exchange failed: "+err.Error())
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		fail(http.StatusBadGateway, "The identity provider did not return an ID token.")
		return
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: s.OIDC.ClientID}).Verify(ctx, rawID)
	if err != nil {
		fail(http.StatusUnauthorized, "ID token rejected: "+err.Error())
		return
	}
	if idt.Nonce != flow.Nonce {
		fail(http.StatusUnauthorized, "ID token nonce mismatch.")
		return
	}

	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		fail(http.StatusBadGateway, err.Error())
		return
	}
	// Pocket ID only puts some claims into the userinfo response depending on scopes
	if ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
		extra := map[string]any{}
		if ui.Claims(&extra) == nil {
			for k, v := range extra {
				if _, exists := claims[k]; !exists {
					claims[k] = v
				}
			}
		}
	}

	user := User{
		Sub:      idt.Subject,
		Name:     claimString(claims, "name"),
		Username: claimString(claims, "preferred_username"),
		Email:    claimString(claims, "email"),
		Picture:  claimString(claims, "picture"),
		Groups:   claimStrings(claims, s.OIDC.GroupsClaim),
	}
	if user.Name == "" {
		user.Name = firstNonEmpty(user.Username, user.Email, user.Sub)
	}
	if err := onLogin(user); err != nil {
		fail(http.StatusForbidden, err.Error())
		return
	}

	sess, err := sign(s.SessionSecret, sessionData{Sub: user.Sub, Expires: time.Now().Add(sessionTTL).Unix(), IDToken: rawID})
	if err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	clearCookie(w, s, flowCookie)
	setCookie(w, s, sessionCookie, sess, sessionTTL)
	http.Redirect(w, r, safeNext(flow.Next), http.StatusFound)
}

func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	s := a.cfg.Get()
	sess, hadSession := a.Session(r)
	clearCookie(w, s, sessionCookie)

	// End the Pocket ID session too when the provider supports it
	if hadSession && s.OIDCReady() && !strings.HasPrefix(sess.Sub, localSubPrefix) {
		if p, err := a.getProvider(s); err == nil {
			var meta struct {
				EndSession string `json:"end_session_endpoint"`
			}
			if p.Claims(&meta) == nil && meta.EndSession != "" {
				target := meta.EndSession + "?post_logout_redirect_uri=" +
					url.QueryEscape(strings.TrimRight(s.PublicURL, "/")+"/") +
					"&client_id=" + url.QueryEscape(s.OIDC.ClientID)
				if sess.IDToken != "" {
					target += "&id_token_hint=" + url.QueryEscape(sess.IDToken)
				}
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func claimString(c map[string]any, key string) string {
	if v, ok := c[key].(string); ok {
		return v
	}
	return ""
}

func claimStrings(c map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	switch v := c[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
