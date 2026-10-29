package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Folder groups apps on the home screen; its id lives in Order or Dock
type Folder struct {
	Name string   `json:"name"`
	Apps []string `json:"apps"`
}

// Layout is a user's own arrangement of their home screen
type Layout struct {
	Order     []string          `json:"order"` // app or folder ids, paginated by the client
	Dock      []string          `json:"dock"`
	Folders   map[string]Folder `json:"folders"`
	Theme     string            `json:"theme,omitempty"`
	Wallpaper string            `json:"wallpaper,omitempty"` // pre-theme setting, read as a theme id
}

// User is someone who signed in through Pocket ID at least once.
// Identity and groups are owned by Pocket ID; only layout and the
// local block flag live here.
type User struct {
	Sub       string    `json:"sub"`
	Name      string    `json:"name"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Picture   string    `json:"picture,omitempty"`
	Groups    []string  `json:"groups"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Blocked   bool      `json:"blocked"`
	Layout    *Layout   `json:"layout,omitempty"`
}

type UserStore struct {
	path  string
	mu    sync.RWMutex
	users map[string]*User
}

func LoadUserStore(dir string) (*UserStore, error) {
	s := &UserStore{path: filepath.Join(dir, "users.json"), users: map[string]*User{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(raw, &s.users)
}

func (s *UserStore) save() error {
	return writeJSONAtomic(s.path, s.users)
}

func copyUser(u *User) User {
	c := *u
	c.Groups = append([]string(nil), u.Groups...)
	if u.Layout != nil {
		l := *u.Layout
		l.Order = append([]string(nil), u.Layout.Order...)
		l.Dock = append([]string(nil), u.Layout.Dock...)
		l.Folders = make(map[string]Folder, len(u.Layout.Folders))
		for id, f := range u.Layout.Folders {
			l.Folders[id] = Folder{Name: f.Name, Apps: append([]string(nil), f.Apps...)}
		}
		c.Layout = &l
	}
	return c
}

// Upsert records a login and refreshes the identity from the ID token
func (s *UserStore) Upsert(in User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	u, ok := s.users[in.Sub]
	if !ok {
		u = &User{Sub: in.Sub, FirstSeen: now}
		s.users[in.Sub] = u
	}
	u.Name, u.Username, u.Email, u.Picture, u.Groups = in.Name, in.Username, in.Email, in.Picture, in.Groups
	u.LastSeen = now
	return copyUser(u), s.save()
}

func (s *UserStore) Get(sub string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[sub]
	if !ok {
		return User{}, false
	}
	return copyUser(u), true
}

func (s *UserStore) List() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		c := copyUser(u)
		c.Layout = nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (s *UserStore) SetLayout(sub string, l Layout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[sub]
	if !ok {
		return errors.New("unknown user")
	}
	u.Layout = &l
	return s.save()
}

func (s *UserStore) SetBlocked(sub string, blocked bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[sub]
	if !ok {
		return errors.New("unknown user")
	}
	u.Blocked = blocked
	return s.save()
}

// Delete forgets a user and their layout; they reappear on next login
func (s *UserStore) Delete(sub string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, sub)
	return s.save()
}
