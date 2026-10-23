package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const identity = `{"id":101,"login":"alice","name":"Alice","type":"User"}`

func repo(id int, name string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"full_name":%q,"owner":{"id":101,"login":"alice","type":"User"},"clone_url":%q,"private":true,"fork":false,"description":"synthetic repository"}`, id, name, "alice/"+name, "https://github.com/alice/"+name+".git")
}
func repoPageLink(page int, relation string) string {
	return fmt.Sprintf(`</user/repos?affiliation=owner%%2Ccollaborator%%2Corganization_member&per_page=100&sort=full_name&page=%d>; rel="%s"`, page, relation)
}
func clientFor(t *testing.T, h http.HandlerFunc, edit func(*Options)) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	o := DefaultOptions()
	o.Origin = s.URL
	o.HTTPClient = s.Client()
	o.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	if edit != nil {
		edit(&o)
	}
	c, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	return c, s
}
func wantKind(t *testing.T, e error, k Kind) {
	t.Helper()
	var api *Error
	if !errors.As(e, &api) || api.Kind != k {
		t.Fatalf("want %s, got %v", k, e)
	}
	if strings.Contains(e.Error(), "SYNTHETIC_TOKEN") {
		t.Fatal("token in diagnostic")
	}
}
func TestGitHubPagination(t *testing.T) {
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_TOKEN" {
			t.Error("missing authentication")
		}
		if r.URL.Path == "/user" {
			fmt.Fprint(w, identity)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, "[%s,%s]", repo(201, "renamed"), repo(202, "second"))
			return
		}
		if r.URL.Query().Get("affiliation") != "owner,collaborator,organization_member" {
			t.Error("wrong affiliation")
		}
		w.Header().Set("Link", repoPageLink(2, "next")+", "+repoPageLink(2, "last"))
		fmt.Fprintf(w, "[%s]", repo(201, "first"))
	}, nil)
	result, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
	if e != nil || !result.Complete || result.User.ID != 101 || len(result.Repositories) != 2 || result.Repositories[0].ID != 201 || result.Repositories[1].ID != 202 {
		t.Fatalf("unexpected result %+v %v", result, e)
	}
	t.Run("partial retains prior metadata", func(t *testing.T) {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" {
				fmt.Fprint(w, identity)
				return
			}
			if r.URL.Query().Get("page") == "2" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"deleted SYNTHETIC_TOKEN"}`)
				return
			}
			w.Header().Set("Link", repoPageLink(2, "next"))
			fmt.Fprintf(w, "[%s]", repo(201, "saved"))
		}, nil)
		r, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		wantKind(t, e, NotFound)
		if r.Complete || len(r.Repositories) != 1 || r.Repositories[0].ID != 201 {
			t.Fatal("partial inventory lost")
		}
	})
	t.Run("empty inventory not permission", func(t *testing.T) {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" {
				fmt.Fprint(w, identity)
			} else {
				fmt.Fprint(w, "[]")
			}
		}, nil)
		r, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		if e != nil || !r.Complete || len(r.Repositories) != 0 {
			t.Fatal(e)
		}
	})
}

func TestGitHubDiscoveryBytes(t *testing.T) {
	var calls atomic.Int32
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_TOKEN" {
			t.Error("byte credential was not sent only as the bearer header")
		}
		if r.URL.Path == "/user" {
			fmt.Fprint(w, identity)
			return
		}
		fmt.Fprint(w, "[]")
	}, nil)
	token := []byte("SYNTHETIC_TOKEN")
	result, err := c.DiscoverBytes(context.Background(), token)
	if err != nil || !result.Complete || calls.Load() != 2 || string(token) != "SYNTHETIC_TOKEN" {
		t.Fatalf("byte discovery failed or mutated caller bytes: result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	invalid := []byte("synthetic token with spaces")
	_, err = c.DiscoverBytes(context.Background(), invalid)
	wantKind(t, err, Authentication)
	if calls.Load() != 2 {
		t.Fatal("invalid byte credential caused an HTTP request")
	}
}

func TestGitHubRateAndAuthErrors(t *testing.T) {
	t.Run("disabled rate wait fails fast", func(t *testing.T) {
		now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
		for _, tc := range []struct{ name, retryAfter string }{
			{"zero seconds", "0"},
			{"expired HTTP date", now.Add(-time.Hour).Format(http.TimeFormat)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var calls atomic.Int32
				var sleeps atomic.Int32
				c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer SYNTHETIC_TOKEN" {
						t.Error("missing authentication")
					}
					w.Header().Set("Retry-After", tc.retryAfter)
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"message":"SYNTHETIC_TOKEN response-body-sentinel"}`)
				}, func(o *Options) {
					o.RateWait = 0
					o.Now = func() time.Time { return now }
					o.Sleep = func(ctx context.Context, d time.Duration) error {
						sleeps.Add(1)
						return ctx.Err()
					}
				})
				result, err := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
				wantKind(t, err, RateLimit)
				var api *Error
				if !errors.As(err, &api) || api.Status != http.StatusTooManyRequests || err.Error() != "GitHub discovery: rate_limit" {
					t.Fatalf("expected sanitized classified HTTP 429 error, got %v", err)
				}
				if result.Complete || calls.Load() != 1 || sleeps.Load() != 0 {
					t.Fatalf("disabled rate wait: complete=%v requests=%d sleeps=%d", result.Complete, calls.Load(), sleeps.Load())
				}
			})
		}
	})
	t.Run("403 rate reset budget", func(t *testing.T) {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "2000")
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"SYNTHETIC_TOKEN"}`)
		}, func(o *Options) { o.Now = func() time.Time { return time.Unix(1000, 0) }; o.RateWait = time.Second })
		_, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		wantKind(t, e, RateLimit)
	})
	for _, tc := range []struct {
		s int
		k Kind
	}{{401, Authentication}, {403, Permission}, {404, NotFound}, {429, RateLimit}, {503, Transport}} {
		t.Run(fmt.Sprint(tc.s), func(t *testing.T) {
			var calls atomic.Int32
			c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "2")
				if tc.s == 403 {
					w.Header().Del("Retry-After")
				}
				w.WriteHeader(tc.s)
				fmt.Fprint(w, `{"message":"SYNTHETIC_TOKEN"}`)
			}, func(o *Options) { o.RateWait = 0 })
			_, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
			wantKind(t, e, tc.k)
			if calls.Load() > 3 {
				t.Fatal("retry cap exceeded")
			}
			if tc.s != 503 && calls.Load() != 1 {
				t.Fatal("unexpected retry")
			}
		})
	}
	t.Run("bounded wait recovery", func(t *testing.T) {
		var calls atomic.Int32
		var slept time.Duration
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"message":"wait"}`)
				return
			}
			if r.URL.Path == "/user" {
				fmt.Fprint(w, identity)
			} else {
				fmt.Fprint(w, "[]")
			}
		}, func(o *Options) {
			o.RateWait = 2 * time.Second
			o.Sleep = func(ctx context.Context, d time.Duration) error { slept += d; return ctx.Err() }
		})
		r, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		if e != nil || !r.Complete || slept != 2*time.Second {
			t.Fatalf("rate recovery %v %v", slept, e)
		}
	})
	t.Run("request timeout", func(t *testing.T) {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, func(o *Options) { o.RequestTimeout = 10 * time.Millisecond; o.MaxAttempts = 1 })
		_, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		wantKind(t, e, Transport)
	})
	t.Run("body timeout", func(t *testing.T) {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}, func(o *Options) { o.RequestTimeout = 10 * time.Millisecond; o.MaxAttempts = 1 })
		_, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		wantKind(t, e, Transport)
	})
}
func TestGitHubDiscoveryCancellation(t *testing.T) {
	t.Run("in flight", func(t *testing.T) {
		started := make(chan struct{})
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, e := c.Discover(ctx, "SYNTHETIC_TOKEN"); done <- e }()
		<-started
		cancel()
		select {
		case e := <-done:
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		case <-time.After(time.Second):
			t.Fatal("cancel blocked")
		}
	})
	t.Run("rate sleep", func(t *testing.T) {
		sleeping := make(chan struct{})
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "1"); w.WriteHeader(429) }, func(o *Options) {
			o.Sleep = func(ctx context.Context, d time.Duration) error { close(sleeping); <-ctx.Done(); return ctx.Err() }
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, e := c.Discover(ctx, "SYNTHETIC_TOKEN"); done <- e }()
		<-sleeping
		cancel()
		if e := <-done; !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	})
}
func TestGitHubOriginAndResponseBounds(t *testing.T) {
	for _, link := range []string{`<https://evil.invalid/user/repos>; rel="next"`, `<http://api.github.com/user/repos>; rel="next"`, `garbage`, `</user/repos?page=2>; rel="next`, `</user/repos?page=2>; rel="next", </user/repos?page=3>; rel="next"`, `</user/repos?affiliation=owner&per_page=100&sort=full_name&page=2>; rel="next"`, `</user/repos?affiliation=owner%2Ccollaborator%2Corganization_member&per_page=100&sort=full_name&page=3>; rel="next"`, `</other>; rel="next"`, `<https://u:p@api.github.com/user/repos>; rel="next"`} {
		t.Run(link, func(t *testing.T) {
			c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/user" {
					fmt.Fprint(w, identity)
					return
				}
				w.Header().Set("Link", link)
				fmt.Fprint(w, "[]")
			}, nil)
			r, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
			wantKind(t, e, InvalidResponse)
			if r.Complete {
				t.Fatal("invalid next treated complete")
			}
		})
	}
	t.Run("cross origin redirect", func(t *testing.T) {
		var received atomic.Int32
		other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
		defer other.Close()
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }, func(o *Options) { o.MaxAttempts = 1 })
		_, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
		wantKind(t, e, Transport)
		if received.Load() != 0 {
			t.Fatal("redirect escaped origin")
		}
	})
	for _, mode := range []string{"bytes", "cycle", "count", "pages", "malformed", "credential URL", "trailing", "null", "duplicate JSON key"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "bytes" {
					fmt.Fprint(w, strings.Repeat("x", 257))
					return
				}
				if r.URL.Path == "/user" {
					if mode == "duplicate JSON key" {
						fmt.Fprint(w, `{"id":101,"id":102,"login":"alice","type":"User"}`)
					} else {
						fmt.Fprint(w, identity)
					}
					return
				}
				switch mode {
				case "cycle":
					w.Header().Set("Link", repoPageLink(2, "next"))
					fmt.Fprint(w, "[]")
				case "pages":
					w.Header().Set("Link", repoPageLink(2, "next"))
					fmt.Fprint(w, "[]")
				case "count":
					fmt.Fprintf(w, "[%s,%s]", repo(1, "one"), repo(2, "two"))
				case "malformed":
					fmt.Fprint(w, "{")
				case "credential URL":
					fmt.Fprint(w, "["+strings.ReplaceAll(repo(1, "one"), "https://github.com", "https://SYNTHETIC_TOKEN@github.com")+"]")
				case "trailing":
					fmt.Fprint(w, "[] {}")
				case "null":
					fmt.Fprint(w, "null")
				}
			}, func(o *Options) {
				if mode == "bytes" {
					o.ResponseBytes = 256
				}
				if mode == "count" {
					o.MaxRepositories = 1
				}
				if mode == "pages" {
					o.MaxPages = 1
				}
			})
			r, e := c.Discover(context.Background(), "SYNTHETIC_TOKEN")
			k := InvalidResponse
			if mode == "bytes" || mode == "count" || mode == "pages" {
				k = Bounds
			}
			wantKind(t, e, k)
			if r.Complete {
				t.Fatal("bounds reported complete")
			}
		})
	}
	o := DefaultOptions()
	o.Origin = "http://api.github.com"
	if _, e := New(o); e == nil {
		t.Fatal("HTTP accepted")
	}
}
func TestGitHubDisplayFilters(t *testing.T) {
	u := User{ID: 1}
	owned := Repository{Owner: User{ID: 1, Type: "User"}}
	org := Repository{Owner: User{ID: 2, Type: "Organization"}}
	collab := Repository{Owner: User{ID: 3, Type: "User"}}
	if !(Filter{}).Match(org, u) || !(Filter{Owned: true}).Match(owned, u) || (Filter{Owned: true}).Match(org, u) || !(Filter{Organizations: true}).Match(org, u) || !(Filter{Collaborating: true}).Match(collab, u) {
		t.Fatal("filter semantics")
	}
	owned.Fork = true
	if (Filter{ExcludeForks: true}).Match(owned, u) {
		t.Fatal("fork filtering")
	}
}
