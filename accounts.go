package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Local accounts are an alternative to OIDC: an admin creates them in the
// settings page and people sign in with a username and password.
const (
	localSubPrefix = "local:"
	minPassword    = 8
	maxPassword    = 72 // bcrypt ignores everything after 72 bytes
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

type Account struct {
	Username        string    `json:"username"`
	Name            string    `json:"name"`
	Email           string    `json:"email,omitempty"`
	Groups          []string  `json:"groups"`
	Hash            string    `json:"hash"`
	Created         time.Time `json:"created"`
	PasswordChanged time.Time `json:"password_changed"` // sessions issued before this are rejected
}

// AccountStore keeps local accounts in data/accounts.json (0600), separate from
// users.json so password hashes never mix with profile and layout data.
type AccountStore struct {
	path     string
	mu       sync.RWMutex
	accounts map[string]*Account
}

func LoadAccountStore(dir string) (*AccountStore, error) {
	s := &AccountStore{path: filepath.Join(dir, "accounts.json"), accounts: map[string]*Account{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(raw, &s.accounts)
}

func localSub(username string) string { return localSubPrefix + username }

func normalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

func checkPassword(pw string) error {
	if len(pw) < minPassword {
		return errors.New("password must be at least 8 characters")
	}
	if len(pw) > maxPassword {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

func (s *AccountStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.accounts)
}

// List returns all accounts without their password hashes
func (s *AccountStore) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		c := *a
		c.Hash = ""
		c.Groups = append([]string(nil), a.Groups...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func (s *AccountStore) Get(username string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[username]
	if !ok {
		return Account{}, false
	}
	c := *a
	c.Groups = append([]string(nil), a.Groups...)
	return c, true
}

// Put creates or updates an account. An empty password keeps the current one,
// but a new account needs one.
func (s *AccountStore) Put(in Account, password string, create bool) error {
	in.Username = normalizeUsername(in.Username)
	if !usernameRe.MatchString(in.Username) {
		return errors.New("username: 1–40 characters, lowercase letters, digits, dot, dash or underscore")
	}
	if password != "" || create {
		if err := checkPassword(password); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, exists := s.accounts[in.Username]
	switch {
	case create && exists:
		return errors.New("an account with that username already exists")
	case !create && !exists:
		return errors.New("unknown account")
	case create:
		a = &Account{Username: in.Username, Created: time.Now()}
	}
	next := *a
	next.Name = firstNonEmpty(strings.TrimSpace(in.Name), in.Username)
	next.Email = strings.TrimSpace(in.Email)
	next.Groups = cleanList(in.Groups)
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		next.Hash = string(hash)
		next.PasswordChanged = time.Now()
	}
	s.accounts[in.Username] = &next
	if err := s.save(); err != nil {
		if exists {
			s.accounts[in.Username] = a
		} else {
			delete(s.accounts, in.Username)
		}
		return err
	}
	return nil
}

func (s *AccountStore) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.accounts, username)
	return s.save()
}

// dummyHash keeps the timing of unknown usernames close to wrong passwords
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("zoraxy-home-dummy"), bcrypt.DefaultCost)

// Verify checks a username and password
func (s *AccountStore) Verify(username, password string) (Account, bool) {
	a, ok := s.Get(normalizeUsername(username))
	if !ok || len(password) > maxPassword {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return Account{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(a.Hash), []byte(password)) != nil {
		return Account{}, false
	}
	return a, true
}

// save must be called with the lock held
func (s *AccountStore) save() error {
	return writeJSONAtomic(s.path, s.accounts)
}

/* Brute-force protection */

// limiter counts failed sign-ins per key (client IP and username) and refuses
// further attempts once a key has too many failures inside the window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

// Allowed reports whether a key may try again
func (l *limiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, time.Now())) < l.max
}

func (l *limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.fails[key] = append(l.recent(key, now), now)
}

func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// clientIP trusts Zoraxy's forwarding headers only when the request comes from
// loopback, which is how Zoraxy reaches the homepage listener.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if v := strings.TrimSpace(r.Header.Get("X-Real-Ip")); v != "" {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}
