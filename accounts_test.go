package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAccountStore(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadAccountStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Account{Username: "Alex"}, "short", true); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.Put(Account{Username: "bad name"}, "long enough", true); err == nil {
		t.Fatal("invalid username accepted")
	}
	if err := s.Put(Account{Username: " Alex ", Groups: []string{"family", " ", "family"}}, "correct horse", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Account{Username: "alex"}, "another one", true); err == nil {
		t.Fatal("duplicate account created")
	}
	acc, ok := s.Verify("ALEX", "correct horse")
	if !ok || acc.Username != "alex" || acc.Name != "alex" || len(acc.Groups) != 1 {
		t.Fatalf("Verify = %+v, %v", acc, ok)
	}
	if _, ok := s.Verify("alex", "wrong password"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := s.Verify("nobody", "correct horse"); ok {
		t.Fatal("unknown user accepted")
	}

	// Updating without a password keeps the old one
	if err := s.Put(Account{Username: "alex", Name: "Alex Doe"}, "", false); err != nil {
		t.Fatal(err)
	}
	if acc, ok := s.Verify("alex", "correct horse"); !ok || acc.Name != "Alex Doe" {
		t.Fatalf("after update: %+v, %v", acc, ok)
	}

	// Hashes are persisted with 0600 and never listed
	info, err := os.Stat(filepath.Join(dir, "accounts.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json mode = %v, %v", info.Mode(), err)
	}
	if l := s.List(); len(l) != 1 || l[0].Hash != "" {
		t.Fatalf("List leaks hashes: %+v", l)
	}
	reloaded, err := LoadAccountStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Verify("alex", "correct horse"); !ok {
		t.Fatal("account lost after reload")
	}
	if err := reloaded.Delete("alex"); err != nil || reloaded.Count() != 0 {
		t.Fatalf("Delete: %v, %d left", err, reloaded.Count())
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allowed("k") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("k")
	}
	if l.Allowed("k") {
		t.Fatal("not blocked after 3 failures")
	}
	if !l.Allowed("other") {
		t.Fatal("unrelated key blocked")
	}
	l.Reset("k")
	if !l.Allowed("k") {
		t.Fatal("still blocked after reset")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/auth/password", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("behind Zoraxy: %q", got)
	}
	r.RemoteAddr = "198.51.100.4:5000"
	if got := clientIP(r); got != "198.51.100.4" {
		t.Fatalf("direct client spoofed its IP: %q", got)
	}
}

func TestLocalSessionEndsOnPasswordChange(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	users, _ := LoadUserStore(dir)
	accounts, _ := LoadAccountStore(dir)
	a := &App{cfg: cfg, users: users, accounts: accounts, auth: NewAuthenticator(cfg)}
	if err := accounts.Put(Account{Username: "alex", Groups: []string{"family"}}, "correct horse", true); err != nil {
		t.Fatal(err)
	}
	users.Upsert(User{Sub: localSub("alex"), Name: "alex"})

	request := func(issued time.Time) *httptest.ResponseRecorder {
		tok, _ := sign(cfg.Get().SessionSecret, sessionData{Sub: localSub("alex"), Expires: issued.Add(sessionTTL).Unix()})
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Header.Set("Cookie", sessionCookie+"="+tok)
		w := httptest.NewRecorder()
		if u, ok := a.currentUser(r); ok {
			w.WriteString(strings.Join(u.Groups, ","))
		}
		return w
	}
	if got := request(time.Now()).Body.String(); got != "family" {
		t.Fatalf("fresh session: %q", got)
	}
	if got := request(time.Now().Add(-time.Hour)).Body.String(); got != "" {
		t.Fatal("session issued before the password was set is still valid")
	}
	accounts.Delete("alex")
	if got := request(time.Now()).Body.String(); got != "" {
		t.Fatal("session of a deleted account is still valid")
	}
}
