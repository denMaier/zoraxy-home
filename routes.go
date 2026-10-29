package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Route is the subset of a Zoraxy ProxyEndpoint the homepage cares about
type Route struct {
	Domain   string   `json:"domain"`
	Aliases  []string `json:"aliases"`
	Upstream string   `json:"upstream"`
	Tags     []string `json:"tags"`
	Disabled bool     `json:"disabled"`
}

// zoraxyEndpoint mirrors the fields read from /plugin/api/proxy/list?type=host
type zoraxyEndpoint struct {
	RootOrMatchingDomain string
	MatchingDomainAlias  []string
	Disabled             bool
	Tags                 []string
	ActiveOrigins        []struct {
		OriginIpOrDomain string
	}
}

type RouteSource struct {
	zoraxyPort int
	apiKey     string
	client     *http.Client

	mu       sync.RWMutex
	routes   []Route
	lastSync time.Time
	lastErr  error
}

func NewRouteSource(zoraxyPort int, apiKey string) *RouteSource {
	return &RouteSource{
		zoraxyPort: zoraxyPort,
		apiKey:     apiKey,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Run polls Zoraxy until the process exits
func (s *RouteSource) Run(interval time.Duration) {
	for {
		if err := s.Sync(); err != nil {
			log.Printf("[home] route sync failed: %v", err)
		}
		time.Sleep(interval)
	}
}

func (s *RouteSource) Sync() error {
	routes, err := s.fetch()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	if err != nil {
		return err
	}
	s.routes = routes
	s.lastSync = time.Now()
	return nil
}

func (s *RouteSource) fetch() ([]Route, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/plugin/api/proxy/list?type=host", s.zoraxyPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoraxy returned %s", resp.Status)
	}
	var endpoints []zoraxyEndpoint
	if err := json.NewDecoder(resp.Body).Decode(&endpoints); err != nil {
		return nil, fmt.Errorf("decoding proxy list: %w", err)
	}

	routes := make([]Route, 0, len(endpoints))
	for _, ep := range endpoints {
		domain := strings.ToLower(strings.TrimSpace(ep.RootOrMatchingDomain))
		if !isLinkableDomain(domain) {
			continue
		}
		r := Route{
			Domain:   domain,
			Aliases:  ep.MatchingDomainAlias,
			Tags:     ep.Tags,
			Disabled: ep.Disabled,
		}
		if len(ep.ActiveOrigins) > 0 {
			r.Upstream = ep.ActiveOrigins[0].OriginIpOrDomain
		}
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Domain < routes[j].Domain })
	return routes, nil
}

// isLinkableDomain filters out rules a browser cannot open directly, like wildcards
func isLinkableDomain(d string) bool {
	if d == "" || strings.ContainsAny(d, "*/ ") {
		return false
	}
	host := d
	if h, _, err := net.SplitHostPort(d); err == nil {
		host = h
	}
	return net.ParseIP(host) == nil
}

func (s *RouteSource) Routes() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Route(nil), s.routes...)
}

func (s *RouteSource) Status() (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSync, s.lastErr
}
