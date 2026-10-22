package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitvault/internal/backup"
)

// This fixture serves real SDK signed HTTPS requests and stores actual payload
// and part bytes on disk. Large multipart fixtures do not allocate part buffers.
type fixture struct {
	t         *testing.T
	root      string
	server    *httptest.Server
	mu        sync.Mutex
	objects   map[string]string
	parts     map[string]string
	events    []string
	puts      int
	failures  int
	lose      bool
	corrupt   bool
	truncate  bool
	mutate    bool
	mutateKey string
	abortFail bool
	cancel    context.CancelFunc
	partGate  chan struct{}
	started   chan struct{}
	active    atomic.Int32
	peak      atomic.Int32
	aborts    atomic.Int32
	signed    atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, root: t.TempDir(), objects: map[string]string{}, parts: map[string]string{}, started: make(chan struct{}, 16)}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fixture) store(r io.Reader) string {
	file, e := os.CreateTemp(f.root, "object-")
	if e != nil {
		f.t.Error(e)
		return ""
	}
	if _, e = io.Copy(file, r); e != nil {
		f.t.Error(e)
	}
	if e = file.Close(); e != nil {
		f.t.Error(e)
	}
	return file.Name()
}
func (f *fixture) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>redaction-sentinel-secret</Message></Error>", code)
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		f.t.Error("request not signed")
	}
	f.signed.Add(1)
	key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
	q := r.URL.Query()
	if r.Method == http.MethodPut && q.Get("partNumber") != "" {
		n := f.active.Add(1)
		defer f.active.Add(-1)
		for {
			old := f.peak.Load()
			if n <= old || f.peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case f.started <- struct{}{}:
		default:
		}
		if f.partGate != nil {
			select {
			case <-f.partGate:
			case <-r.Context().Done():
				return
			}
		}
		if f.cancel != nil {
			f.cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		part, _ := strconv.Atoi(q.Get("partNumber"))
		path := f.store(r.Body)
		f.mu.Lock()
		f.parts[key+"/"+strconv.Itoa(part)] = path
		f.events = append(f.events, "part")
		f.mu.Unlock()
		w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", part))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		f.events = append(f.events, "create")
		fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>test-bucket</Bucket><Key>`+key+`</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "POST" && q.Get("uploadId") != "":
		if r.Header.Get("If-None-Match") != "*" {
			f.t.Error("unconditional complete")
		}
		if f.objects[key] != "" {
			f.fail(w, 412, "PreconditionFailed")
			return
		}
		var in struct {
			Parts []struct {
				Number int `xml:"PartNumber"`
			} `xml:"Part"`
		}
		if e := xml.NewDecoder(r.Body).Decode(&in); e != nil {
			f.t.Error(e)
		}
		file, e := os.CreateTemp(f.root, "complete-")
		if e != nil {
			f.t.Error(e)
			return
		}
		for i, p := range in.Parts {
			if p.Number != i+1 {
				f.t.Error("parts not sorted")
			}
			part, e := os.Open(f.parts[key+"/"+strconv.Itoa(p.Number)])
			if e != nil {
				f.t.Error(e)
				continue
			}
			_, e = io.Copy(file, part)
			part.Close()
			if e != nil {
				f.t.Error(e)
			}
		}
		file.Close()
		f.objects[key] = file.Name()
		f.events = append(f.events, "complete:"+key)
		fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"opaque-composite-2"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == "DELETE" && q.Get("uploadId") != "":
		f.aborts.Add(1)
		f.events = append(f.events, "abort")
		if f.abortFail {
			f.fail(w, 503, "SlowDown")
			return
		}
		w.WriteHeader(204)
	case r.Method == "PUT":
		f.puts++
		if f.failures > 0 {
			f.failures--
			f.fail(w, 503, "SlowDown")
			return
		}
		if r.Header.Get("If-None-Match") != "*" {
			f.t.Error("unconditional put")
		}
		if f.objects[key] != "" {
			f.fail(w, 412, "PreconditionFailed")
			return
		}
		f.objects[key] = f.store(r.Body)
		f.events = append(f.events, "put:"+key)
		if f.lose {
			f.lose = false
			f.fail(w, 500, "InternalError")
			return
		}
		w.Header().Set("ETag", `"not-a-hash"`)
	case r.Method == "GET" && q.Get("list-type") == "2":
		fmt.Fprint(w, "<ListBucketResult><IsTruncated>false</IsTruncated>")
		keys := make([]string, 0, len(f.objects))
		for key := range f.objects {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprint(w, "<Contents><Key>")
			xml.EscapeText(w, []byte(key))
			fmt.Fprint(w, "</Key></Contents>")
		}
		fmt.Fprint(w, "</ListBucketResult>")
	case r.Method == "HEAD" || r.Method == "GET":
		path := f.objects[key]
		if path == "" {
			f.fail(w, 404, "NoSuchKey")
			return
		}
		if r.Method == "GET" && f.mutate && (f.mutateKey == "" || f.mutateKey == key) {
			f.fail(w, 412, "PreconditionFailed")
			return
		}
		file, e := os.Open(path)
		if e != nil {
			f.t.Error(e)
			return
		}
		defer file.Close()
		st, _ := file.Stat()
		w.Header().Set("ETag", `"opaque-composite-2"`)
		w.Header().Set("x-amz-version-id", "version-1")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		if r.Method == "HEAD" {
			return
		}
		if r.Header.Get("If-Match") != `"opaque-composite-2"` || q.Get("versionId") != "version-1" {
			f.t.Error("GET not pinned to object identity")
		}
		f.events = append(f.events, "get:"+key)
		if f.corrupt && strings.HasSuffix(key, backup.BundleFilename) {
			io.Copy(w, io.LimitReader(file, st.Size()-1))
			w.Write([]byte{255})
			return
		}
		if f.truncate && strings.HasSuffix(key, backup.BundleFilename) {
			io.Copy(w, io.LimitReader(file, st.Size()-1))
			return
		}
		io.Copy(w, file)
	case r.Method == "DELETE":
		delete(f.objects, key)
		f.events = append(f.events, "delete:"+key)
		w.WriteHeader(204)
	default:
		f.fail(w, 400, "BadRequest")
	}
}
func (f *fixture) client(t *testing.T, edit func(*Options)) *Client {
	o := DefaultOptions()
	o.Target = Target{ID: "target-1", Provider: "s3", Bucket: "test-bucket", Region: "us-east-1", Endpoint: f.server.URL, Prefix: "gitvault/"}
	o.HTTPClient = f.server.Client()
	o.Credentials = func(context.Context) (Credentials, error) {
		return Credentials{AccessKey: "SYNTHETIC", SecretKey: "synthetic-not-real"}, nil
	}
	o.Backoff = time.Millisecond
	o.MaxBackoff = 2 * time.Millisecond
	if edit != nil {
		edit(&o)
	}
	c, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func ready(t *testing.T, size int64) (backup.Manifest, string) {
	path := filepath.Join(t.TempDir(), backup.BundleFilename)
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.New()
	chunk := make([]byte, 65536)
	for i := range chunk {
		chunk[i] = byte(i % 251)
	}
	for remaining := size; remaining > 0; {
		binary.LittleEndian.PutUint64(chunk, uint64(size-remaining))
		n := min64(remaining, int64(len(chunk)))
		if _, e = io.MultiWriter(f, h).Write(chunk[:n]); e != nil {
			t.Fatal(e)
		}
		remaining -= n
	}
	f.Close()
	id, e := backup.NewID()
	if e != nil {
		t.Fatal(e)
	}
	m := backup.Manifest{FormatVersion: 1, SnapshotID: id, CreatedAt: time.Now().UTC(), ApplicationVersion: "synthetic", Repository: backup.RepositoryIdentity{Provider: "github", ID: "123", DisplayName: "synthetic/repository"}, GitObjectFormat: "sha1", Bundle: backup.Bundle{File: backup.BundleFilename, Kind: "full", SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil)), Prerequisites: []string{}}, Head: backup.Head{Kind: "symbolic", Target: "refs/heads/main"}, Refs: map[string]string{"refs/heads/main": strings.Repeat("a", 40)}, Coverage: backup.FullCoverage()}
	if e = m.Validate(backup.DefaultLimits()); e != nil {
		t.Fatal(e)
	}
	return m, path
}
func TestCloudManifestLast(t *testing.T) {
	for _, size := range []int64{4096, 16 << 20} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			f := newFixture(t)
			c := f.client(t, func(o *Options) { o.PartSize = 8 << 20; o.MultipartThreshold = 8 << 20 })
			m, path := ready(t, size)
			r, e := c.Upload(context.Background(), m, path)
			if e != nil {
				t.Fatal(e)
			}
			if r.Publication != "published" || r.Verification != "upload_receipt" {
				t.Fatal(r)
			}
			f.mu.Lock()
			events := append([]string{}, f.events...)
			f.mu.Unlock()
			if events[len(events)-1] != "put:"+r.ManifestKey {
				t.Fatal(events)
			}
			_, key := c.keys(m)
			if e = c.verify(context.Background(), key, size, m.Bundle.SHA256); e != nil {
				t.Fatal(e)
			}
			if f.signed.Load() == 0 {
				t.Fatal("no signed HTTP")
			}
		})
	}
}
func TestCloudNoReplaceAndLostResponse(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, nil)
	m, path := ready(t, 4096)
	f.lose = true
	r, e := c.Upload(context.Background(), m, path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Upload(context.Background(), m, path); e != nil {
		t.Fatal("exact duplicate should reconcile", e)
	}
	f.mu.Lock()
	old := f.objects[r.BundleKey]
	f.objects[r.BundleKey] = f.store(strings.NewReader(strings.Repeat("X", 4096)))
	f.mu.Unlock()
	if _, e = c.Upload(context.Background(), m, path); e == nil {
		t.Fatal("unrelated object accepted")
	}
	got, _ := os.ReadFile(f.objects[r.BundleKey])
	if got[0] != 'X' || f.objects[r.BundleKey] == old {
		t.Fatal("preexisting object replaced")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("local artifact removed")
	}
}
func TestMultipartCancelFreshAbort(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	var facts []MultipartFact
	c := f.client(t, func(o *Options) {
		o.PartSize = 8 << 20
		o.MultipartThreshold = 8 << 20
		o.Journal = func(ctx context.Context, v MultipartFact) error {
			if ctx.Err() != nil {
				t.Error("cancelled cleanup context")
			}
			facts = append(facts, v)
			return nil
		}
	})
	m, path := ready(t, 16<<20)
	r, e := c.Upload(ctx, m, path)
	if e == nil {
		t.Fatal("cancel succeeded")
	}
	if f.aborts.Load() != 1 || len(r.Multipart) != 1 || r.Multipart[0].Cleanup != "aborted" || len(facts) != 2 {
		t.Fatal(r, facts, f.aborts.Load())
	}
	_, mk := c.keys(m)
	_ = mk
	if _, e = os.Stat(path); e != nil {
		t.Fatal("local artifact lost")
	}
}
func TestCloudRetryAndConcurrencyBounds(t *testing.T) {
	t.Run("attempt-cap", func(t *testing.T) {
		f := newFixture(t)
		f.failures = 10
		c := f.client(t, nil)
		m, path := ready(t, 1024)
		_, e := c.Upload(context.Background(), m, path)
		if e == nil || f.puts != 3 {
			t.Fatal(e, f.puts)
		}
		if strings.Contains(e.Error(), "sentinel") {
			t.Fatal("raw provider error leaked")
		}
	})
	t.Run("global-parts", func(t *testing.T) {
		f := newFixture(t)
		f.partGate = make(chan struct{})
		c := f.client(t, func(o *Options) { o.Transfers = 2; o.Parts = 1; o.PartSize = 8 << 20; o.MultipartThreshold = 8 << 20 })
		m1, p1 := ready(t, 16<<20)
		m2, p2 := ready(t, 16<<20)
		errs := make(chan error, 2)
		go func() { _, e := c.Upload(context.Background(), m1, p1); errs <- e }()
		go func() { _, e := c.Upload(context.Background(), m2, p2); errs <- e }()
		for i := 0; i < 2; i++ {
			select {
			case <-f.started:
			case <-time.After(10 * time.Second):
				t.Fatal("parts did not start")
			}
		}
		if f.peak.Load() > 2 {
			t.Fatal("unbounded parts")
		}
		close(f.partGate)
		for i := 0; i < 2; i++ {
			if e := <-errs; e != nil {
				t.Fatal(e)
			}
		}
	})
}
func TestCloudReadBackNotETag(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(strconv.FormatBool(corrupt), func(t *testing.T) {
			f := newFixture(t)
			f.corrupt = corrupt
			c := f.client(t, func(o *Options) { o.ReadBack = true; o.PartSize = 8 << 20; o.MultipartThreshold = 8 << 20 })
			m, path := ready(t, 16<<20)
			r, e := c.Upload(context.Background(), m, path)
			if corrupt {
				if e == nil || r.Publication == "published" {
					t.Fatal("same-length corruption accepted", r, e)
				}
			} else if e != nil || r.Verification != "read_back_checksum" {
				t.Fatal(r, e)
			}
		})
	}
}
func TestRemoteListingRequiresManifest(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, nil)
	m, path := ready(t, 1024)
	_, bk := c.keys(m)
	f.objects[bk] = f.store(strings.NewReader("orphan"))
	list, e := c.ListPublished(context.Background())
	if e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	delete(f.objects, bk)
	if _, e = c.Upload(context.Background(), m, path); e != nil {
		t.Fatal(e)
	}
	list, e = c.ListPublished(context.Background())
	if e != nil || len(list) != 1 || list[0].Manifest.SnapshotID != m.SnapshotID {
		t.Fatal(list, e)
	}
	f.objects[c.o.Target.Prefix+"target-1/github/123/unsafe/manifest.json"] = f.store(strings.NewReader(`{"format_version":999}`))
	if _, e = c.ListPublished(context.Background()); e == nil {
		t.Fatal("invalid manifest accepted")
	}
}
func TestDownloadBoundsAndIntegrity(t *testing.T) {
	for _, mode := range []string{"valid", "corrupt", "truncated", "oversize", "existing"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			c := f.client(t, nil)
			m, path := ready(t, 1024)
			r, e := c.Upload(context.Background(), m, path)
			if e != nil {
				t.Fatal(e)
			}
			dst := filepath.Join(t.TempDir(), "download")
			switch mode {
			case "corrupt":
				f.corrupt = true
			case "truncated":
				f.truncate = true
			case "oversize":
				f.objects[r.BundleKey] = f.store(strings.NewReader(strings.Repeat("x", 1025)))
			case "existing":
				os.Mkdir(dst, 0700)
				os.WriteFile(filepath.Join(dst, "sentinel"), []byte("keep"), 0600)
			}
			artifact, e := c.Download(context.Background(), r.ManifestKey, dst)
			if mode == "valid" {
				if e != nil {
					t.Fatal(e)
				}
				got, _ := os.ReadFile(artifact.BundlePath)
				want, _ := os.ReadFile(path)
				if string(got) != string(want) {
					t.Fatal("wrong bytes")
				}
				if _, e = c.Download(context.Background(), r.ManifestKey, dst); e == nil {
					t.Fatal("existing destination replaced")
				}
			} else {
				if e == nil {
					t.Fatal("invalid download accepted")
				}
				if mode == "existing" {
					b, _ := os.ReadFile(filepath.Join(dst, "sentinel"))
					if string(b) != "keep" {
						t.Fatal("existing destination damaged")
					}
				} else if _, e = os.Stat(dst); !os.IsNotExist(e) {
					t.Fatal("failed staging not removed")
				}
			}
		})
	}
}
func TestDownloadVersionMutation(t *testing.T) {
	for _, which := range []string{"manifest", "bundle"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			c := f.client(t, nil)
			m, path := ready(t, 1024)
			r, e := c.Upload(context.Background(), m, path)
			if e != nil {
				t.Fatal(e)
			}
			f.mutate = true
			if which == "bundle" {
				f.mutateKey = r.BundleKey
			}
			if _, e = c.Download(context.Background(), r.ManifestKey, filepath.Join(t.TempDir(), "download")); e == nil {
				t.Fatal("stale object identity accepted")
			}
		})
	}
}
func TestDeleteExactScopeManifestFirst(t *testing.T) {
	f := newFixture(t)
	c := f.client(t, nil)
	m, path := ready(t, 1024)
	r, e := c.Upload(context.Background(), m, path)
	if e != nil {
		t.Fatal(e)
	}
	var effects []DeleteEffect
	receipt, e := c.DeleteCopy(context.Background(), DeleteScope{TargetID: "target-1", ManifestKey: r.ManifestKey, BundleKey: r.BundleKey}, func(_ context.Context, v DeleteEffect) error { effects = append(effects, v); return nil })
	if e != nil || receipt.CleanupRequired || len(effects) != 2 || effects[0].Key != r.ManifestKey || effects[1].Key != r.BundleKey {
		t.Fatal(receipt, e, effects)
	}
	if _, e = c.DeleteCopy(context.Background(), DeleteScope{TargetID: "other", ManifestKey: r.ManifestKey, BundleKey: r.BundleKey}, func(context.Context, DeleteEffect) error { return nil }); e == nil {
		t.Fatal("foreign target accepted")
	}
}
