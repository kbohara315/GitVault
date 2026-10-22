// Package github discovers accessible metadata, never selects repositories or
// authorizes a backup. An incomplete Result must not replace saved selection.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind string

const (
	Authentication  Kind = "authentication"
	Permission      Kind = "permission_or_organization_policy"
	NotFound        Kind = "not_found"
	RateLimit       Kind = "rate_limit"
	Transport       Kind = "transport"
	InvalidResponse Kind = "invalid_response"
	Bounds          Kind = "resource_limit"
)

// Error intentionally never includes response bodies, URLs or transport text.
type Error struct {
	Kind   Kind
	Status int
}

func (e *Error) Error() string { return "GitHub discovery: " + string(e.Kind) }

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Type  string `json:"type"`
}
type Repository struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Owner       User   `json:"owner"`
	CloneURL    string `json:"clone_url"`
	Private     bool   `json:"private"`
	Fork        bool   `json:"fork"`
	Archived    bool   `json:"archived"`
	Description string `json:"description"`
}
type Result struct {
	User         User
	Repositories []Repository
	Complete     bool
}

// Filter is display-only: Owned matches the authenticated user's stable ID;
// Organizations matches owner type Organization; Collaborating matches other
// user-owned repositories accessible through /user/repos (not an assertion of
// direct collaborator status). Forks are independently included/excluded.
// Zero Filter includes every accessible repository, including forks. Starred
// repositories are deliberately not fetched: starring does not grant access.
type Filter struct {
	Owned, Organizations, Collaborating bool
	ExcludeForks                        bool
}

func (f Filter) Match(r Repository, u User) bool {
	if f.ExcludeForks && r.Fork {
		return false
	}
	if !f.Owned && !f.Organizations && !f.Collaborating {
		return true
	}
	return f.Owned && r.Owner.ID == u.ID || f.Organizations && r.Owner.Type == "Organization" || f.Collaborating && r.Owner.ID != u.ID && r.Owner.Type != "Organization"
}

type Options struct {
	Origin                               string
	HTTPClient                           *http.Client
	MaxRepositories, MaxPages            int
	ResponseBytes                        int64
	RequestTimeout                       time.Duration
	MaxAttempts                          int
	RateWait, InitialBackoff, MaxBackoff time.Duration
	Sleep                                func(context.Context, time.Duration) error
	Now                                  func() time.Time
}
type Client struct {
	origin *url.URL
	http   *http.Client
	o      Options
}

func DefaultOptions() Options {
	return Options{Origin: "https://api.github.com", MaxRepositories: 10000, MaxPages: 1000, ResponseBytes: 16 << 20, RequestTimeout: 60 * time.Second, MaxAttempts: 3, RateWait: 15 * time.Minute, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}
}

// New accepts only an HTTPS origin. A custom origin and trusted TLS client are
// test/private-deployment seams; production composition uses DefaultOptions.
func New(o Options) (*Client, error) {
	u, e := url.Parse(o.Origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, &Error{Kind: InvalidResponse}
	}
	if o.MaxRepositories < 1 || o.MaxRepositories > 100000 || o.MaxPages < 1 || o.MaxPages > 10000 || o.ResponseBytes < 1 || o.ResponseBytes > 64<<20 || o.RequestTimeout <= 0 || o.RequestTimeout > 5*time.Minute || o.MaxAttempts < 1 || o.MaxAttempts > 8 || o.RateWait < 0 || o.RateWait > time.Hour || o.InitialBackoff <= 0 || o.MaxBackoff < o.InitialBackoff || o.MaxBackoff > 120*time.Second {
		return nil, &Error{Kind: Bounds}
	}
	h := http.Client{}
	if o.HTTPClient != nil {
		h = *o.HTTPClient
	}
	if transport, ok := h.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		return nil, &Error{Kind: InvalidResponse}
	}
	h.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !sameOrigin(u, req.URL) {
			return &Error{Kind: InvalidResponse}
		}
		return nil
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Client{u, &h, o}, nil
}
func sameOrigin(a, b *url.URL) bool {
	return b.Scheme == "https" && strings.EqualFold(a.Host, b.Host) && b.User == nil && b.Fragment == ""
}
func validToken(t string) bool {
	if len(t) < 1 || len(t) > 2560 {
		return false
	}
	for _, c := range []byte(t) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func (c *Client) get(ctx context.Context, u *url.URL, token string, budget *time.Duration) ([]byte, http.Header, error) {
	for attempt := 0; attempt < c.o.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !sameOrigin(c.origin, u) {
			return nil, nil, &Error{Kind: InvalidResponse}
		}
		ac, cancel := context.WithTimeout(ctx, c.o.RequestTimeout)
		req, e := http.NewRequestWithContext(ac, http.MethodGet, u.String(), nil)
		if e != nil {
			cancel()
			return nil, nil, &Error{Kind: InvalidResponse}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, e := c.http.Do(req)
		if e != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if attempt+1 == c.o.MaxAttempts {
				return nil, nil, &Error{Kind: Transport}
			}
			if e = c.backoff(ctx, attempt); e != nil {
				return nil, nil, e
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, c.o.ResponseBytes+1))
		resp.Body.Close()
		cancel()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if int64(len(data)) > c.o.ResponseBytes {
			return nil, nil, &Error{Kind: Bounds}
		}
		if readErr != nil {
			return nil, nil, &Error{Kind: Transport}
		}
		s := resp.StatusCode
		if s == 200 {
			return data, resp.Header, nil
		}
		kind := InvalidResponse
		retry := false
		wait := time.Duration(0)
		switch {
		case s == 401:
			kind = Authentication
		case s == 404:
			kind = NotFound
		case s == 429 || s == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""):
			kind = RateLimit
			wait = c.rateDelay(resp.Header)
			retry = true
		case s == 403:
			kind = Permission
		case s >= 500 && s <= 599:
			kind = Transport
			retry = true
		}
		if !retry || attempt+1 == c.o.MaxAttempts {
			return nil, nil, &Error{kind, s}
		}
		if kind == RateLimit {
			if c.o.RateWait == 0 || wait < 0 || wait > *budget {
				return nil, nil, &Error{kind, s}
			}
			*budget -= wait
			if e = c.o.Sleep(ctx, wait); e != nil {
				return nil, nil, e
			}
		} else if e = c.backoff(ctx, attempt); e != nil {
			return nil, nil, e
		}
	}
	return nil, nil, &Error{Kind: Transport}
}
func (c *Client) backoff(ctx context.Context, n int) error {
	d := c.o.InitialBackoff
	for i := 0; i < n && d < c.o.MaxBackoff; i++ {
		if d > c.o.MaxBackoff/2 {
			d = c.o.MaxBackoff
			break
		}
		d *= 2
	}
	if d > c.o.MaxBackoff {
		d = c.o.MaxBackoff
	}
	return c.o.Sleep(ctx, time.Duration(rand.Int64N(int64(d)+1)))
}
func (c *Client) rateDelay(h http.Header) time.Duration {
	if s := h.Get("Retry-After"); s != "" {
		if n, e := strconv.ParseInt(s, 10, 32); e == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, e := http.ParseTime(s); e == nil {
			return max(time.Duration(0), t.Sub(c.o.Now()))
		}
		return -1
	}
	if n, e := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); e == nil && n > 0 {
		t := time.Unix(n, 0)
		if t.After(c.o.Now().Add(time.Hour)) {
			return -1
		}
		return max(time.Duration(0), t.Sub(c.o.Now()))
	}
	return time.Second
}
func decode(b []byte, v any) error {
	if !utf8.Valid(b) {
		return &Error{Kind: InvalidResponse}
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	if e := d.Decode(v); e != nil {
		return &Error{Kind: InvalidResponse}
	}
	var extra any
	if e := d.Decode(&extra); !errors.Is(e, io.EOF) {
		return &Error{Kind: InvalidResponse}
	}
	return nil
}

// nextLink rejects malformed or ambiguous Link syntax instead of falsely
// reporting a complete inventory. Relative next links are resolved safely.
func (c *Client) nextLink(h http.Header, current *url.URL) (*url.URL, error) {
	var next *url.URL
	headerBytes := 0
	for _, line := range h.Values("Link") {
		headerBytes += len(line)
		if headerBytes > 64<<10 {
			return nil, &Error{Kind: Bounds}
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			end := strings.Index(part, ">")
			if !strings.HasPrefix(part, "<") || end < 2 {
				return nil, &Error{Kind: InvalidResponse}
			}
			raw := part[1:end]
			params := strings.Split(part[end+1:], ";")
			found := false
			relSeen := false
			for _, p := range params {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				kv := strings.SplitN(p, "=", 2)
				if len(kv) != 2 {
					return nil, &Error{Kind: InvalidResponse}
				}
				if strings.TrimSpace(kv[0]) == "rel" {
					if relSeen {
						return nil, &Error{Kind: InvalidResponse}
					}
					relSeen = true
					value := strings.TrimSpace(kv[1])
					if strings.HasPrefix(value, "\"") {
						unquoted, err := strconv.Unquote(value)
						if err != nil {
							return nil, &Error{Kind: InvalidResponse}
						}
						value = unquoted
					} else if strings.ContainsAny(value, " \t\"'") || value == "" {
						return nil, &Error{Kind: InvalidResponse}
					}
					for _, rel := range strings.Fields(value) {
						if rel == "next" {
							found = true
						}
					}
				}
			}
			if !relSeen {
				return nil, &Error{Kind: InvalidResponse}
			}
			if found {
				if next != nil {
					return nil, &Error{Kind: InvalidResponse}
				}
				u, e := url.Parse(raw)
				if e != nil {
					return nil, &Error{Kind: InvalidResponse}
				}
				u = current.ResolveReference(u)
				if !sameOrigin(c.origin, u) || u.Path != "/user/repos" {
					return nil, &Error{Kind: InvalidResponse}
				}
				next = u
			}
		}
	}
	return next, nil
}
func cleanText(s string, maxBytes int) bool {
	if len(s) > maxBytes {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func validUser(u User) bool {
	return u.ID > 0 && loginPattern.MatchString(u.Login) && cleanText(u.Name, 1024) && cleanText(u.Type, 64)
}

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

func validRepo(r Repository) bool {
	u, e := url.Parse(r.CloneURL)
	return r.ID > 0 && validUser(r.Owner) && namePattern.MatchString(r.Name) && r.Name != "." && r.Name != ".." && r.FullName == r.Owner.Login+"/"+r.Name && cleanText(r.Description, 4096) && e == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && u.Path == "/"+r.Owner.Login+"/"+r.Name+".git"
}

// Discover validates PAT identity then traverses /user/repos with affiliation
// owner,collaborator,organization_member. Results are unselected metadata only.
// On any page failure it returns prior validated repositories and Complete=false.
func (c *Client) Discover(ctx context.Context, token string) (Result, error) {
	var result Result
	if !validToken(token) {
		return result, &Error{Kind: Authentication}
	}
	budget := c.o.RateWait
	u := c.origin.ResolveReference(&url.URL{Path: "/user"})
	data, _, e := c.get(ctx, u, token, &budget)
	if e != nil {
		return result, e
	}
	if e = decode(data, &result.User); e != nil || !validUser(result.User) {
		return result, &Error{Kind: InvalidResponse}
	}
	u = c.origin.ResolveReference(&url.URL{Path: "/user/repos", RawQuery: "affiliation=owner%2Ccollaborator%2Corganization_member&per_page=100&sort=full_name"})
	seenURL := map[string]bool{}
	seenID := map[int64]bool{}
	for page := 0; u != nil; page++ {
		if page >= c.o.MaxPages || seenURL[u.String()] {
			return result, &Error{Kind: Bounds}
		}
		seenURL[u.String()] = true
		data, h, err := c.get(ctx, u, token, &budget)
		if err != nil {
			return result, err
		}
		var repos []Repository
		if err = decode(data, &repos); err != nil {
			return result, err
		}
		if repos == nil {
			return result, &Error{Kind: InvalidResponse}
		}
		for _, r := range repos {
			if !validRepo(r) {
				return result, &Error{Kind: InvalidResponse}
			}
			if !seenID[r.ID] {
				if len(result.Repositories) >= c.o.MaxRepositories {
					return result, &Error{Kind: Bounds}
				}
				seenID[r.ID] = true
				result.Repositories = append(result.Repositories, r)
			}
		}
		u, err = c.nextLink(h, u)
		if err != nil {
			return result, err
		}
	}
	result.Complete = true
	return result, nil
}
