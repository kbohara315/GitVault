package cloud

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCloudExplicitCredentialsAndTLS(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, nil)
	base := c.o
	for _, edit := range []func(*Options){
		func(o *Options) { o.Credentials = nil },
		func(o *Options) { o.Target.Endpoint = "http://example.test" },
		func(o *Options) { o.Target.Endpoint = "https://user:password@example.test" },
		func(o *Options) { o.Target.Endpoint = "https://example.test?token=secret" },
		func(o *Options) { o.Target.Prefix = "gitvault/../" },
		func(o *Options) { o.Target.Prefix = "/gitvault/" },
		func(o *Options) { o.Target.ID = "../escape" },
		func(o *Options) { o.Target.Provider = "r2"; o.Target.Region = "us-east-1" },
		func(o *Options) { o.Parts = 4; o.Transfers = 4 },
		func(o *Options) { o.Attempts = 9 },
		func(o *Options) { o.PartSize = 1 },
		func(o *Options) { o.Limits.ManifestBytes = 0 },
		func(o *Options) {
			o.HTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		},
	} {
		o := base
		edit(&o)
		if _, e := New(o); e == nil {
			t.Fatal("unsafe options accepted")
		}
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-synthetic")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-synthetic")
	o := base
	var calls atomic.Int32
	o.Credentials = func(context.Context) (Credentials, error) {
		calls.Add(1)
		return Credentials{}, errors.New("secret-sentinel")
	}
	c, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	m, path := ready(t, 1024)
	_, e = c.Upload(context.Background(), m, path)
	if e == nil || strings.Contains(e.Error(), "secret-sentinel") || f.signed.Load() != 0 || calls.Load() > int32(o.Attempts*2) {
		t.Fatal("ambient credentials used or diagnostics leaked", e, calls.Load())
	}
}

func TestCloudRejectsRedirects(t *testing.T) {
	var redirected atomic.Int32
	sink := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer sink.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", sink.URL+r.URL.Path)
		w.WriteHeader(307)
	}))
	defer redirect.Close()
	f := newFixture(t)
	c := f.client(t, func(o *Options) { o.Target.Endpoint = redirect.URL; o.HTTPClient = redirect.Client() })
	m, path := ready(t, 1024)
	if _, e := c.Upload(context.Background(), m, path); e == nil || redirected.Load() != 0 {
		t.Fatal("redirect followed", e)
	}
}

func TestCloudAbortFailureReturnsCleanupFacts(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	f.abortFail = true
	c := f.client(t, func(o *Options) { o.PartSize = 8 << 20; o.MultipartThreshold = 8 << 20 })
	m, path := ready(t, 16<<20)
	r, e := c.Upload(ctx, m, path)
	if e == nil || len(r.Multipart) != 1 || r.Multipart[0].UploadID != "upload-1" || r.Multipart[0].Cleanup != "cleanup_required" || f.aborts.Load() != 3 {
		t.Fatal(r, e, f.aborts.Load())
	}
	f.mu.Lock()
	f.abortFail = false
	f.mu.Unlock()
	fact, e := c.AbortPending(ctx, r.Multipart[0])
	if e != nil || fact.Cleanup != "aborted" || f.aborts.Load() != 4 {
		t.Fatal("exact persisted cleanup did not reconcile", fact, e)
	}
}

func TestCloudDeleteStopsAfterJournalFailure(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, nil)
	m, path := ready(t, 1024)
	r, e := c.Upload(context.Background(), m, path)
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := c.DeleteCopy(context.Background(), DeleteScope{TargetID: "target-1", ManifestKey: r.ManifestKey, BundleKey: r.BundleKey}, func(context.Context, DeleteEffect) error { return errors.New("secret-sentinel") })
	if e == nil || !receipt.CleanupRequired || len(receipt.Effects) != 1 || !receipt.Effects[0].Confirmed || strings.Contains(e.Error(), "secret-sentinel") {
		t.Fatal(receipt, e)
	}
	if f.objects[r.BundleKey] == "" {
		t.Fatal("payload deleted after journal failure")
	}
	if _, e = c.Download(context.Background(), r.ManifestKey, filepath.Join(t.TempDir(), "download")); e == nil {
		t.Fatal("deleted marker remained discoverable")
	}
	// After the parent durably records that confirmed marker effect, resume ONLY
	// the remaining approved payload effect (no additional version delete marker).
	resumed, e := c.DeleteCopy(context.Background(), DeleteScope{TargetID: "target-1", ManifestKey: r.ManifestKey, BundleKey: r.BundleKey, Confirmed: []string{r.ManifestKey}}, func(context.Context, DeleteEffect) error { return nil })
	if e != nil || resumed.CleanupRequired || len(resumed.Effects) != 1 || resumed.Effects[0].Key != r.BundleKey {
		t.Fatal(resumed, e)
	}
}
