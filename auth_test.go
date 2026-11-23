package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Authentik publishes its issuer with a trailing slash; discovery must use it verbatim
func TestIssuerTrailingSlashKept(t *testing.T) {
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "authorize/",
			"token_endpoint":         issuer + "token/",
			"jwks_uri":               issuer + "jwks/",
		})
	}))
	defer idp.Close()
	issuer = idp.URL + "/application/o/home/"

	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthenticator(cfg)

	s := cfg.Get()
	s.OIDC.Issuer = issuer
	if _, err := auth.getProvider(s); err != nil {
		t.Fatalf("issuer with trailing slash: %v", err)
	}

	s.OIDC.Issuer = strings.TrimSuffix(issuer, "/")
	_, err = auth.getProvider(s)
	if err == nil || !strings.Contains(err.Error(), "trailing slash") {
		t.Fatalf("mismatched issuer should explain the trailing slash, got %v", err)
	}
}

func TestValidIssuer(t *testing.T) {
	for _, ok := range []string{
		"https://auth.example.com",
		"https://auth.example.com/application/o/home/",
		"http://10.0.0.5:9000/realms/home",
	} {
		if err := validIssuer(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"auth.example.com",
		"javascript:alert(1)",
		"ftp://auth.example.com",
		"https://user:pass@auth.example.com",
		"https://auth.example.com/?x=1",
		"https://auth.example.com/?",
		"https://auth.example.com/#frag",
		"https://auth.example.com/#",
		"https://auth.example.com/a b",
		"https://auth.example.com/a\tb",
		"https://auth.example.com/\r\nX-Injected: 1",
		"https://auth.example.com/<script>",
		`https://auth.example.com/"onmouseover="x`,
		"https://auth.exämple.com",
	} {
		if validIssuer(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
