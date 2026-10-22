package backup

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureManifest() Manifest {
	return Manifest{
		FormatVersion:      1,
		SnapshotID:         "12345678-1234-4234-8234-123456789abc",
		CreatedAt:          time.Date(2026, 10, 22, 10, 30, 0, 123456789, time.UTC),
		ApplicationVersion: "different-installation-version",
		Repository:         RepositoryIdentity{"github", "123456", "synthetic/project"},
		GitObjectFormat:    "sha1",
		Bundle:             Bundle{BundleFilename, "full", strings.Repeat("a", 64), 123456, []string{}},
		Head:               Head{Kind: "symbolic", Target: "refs/heads/trunk"},
		Refs: map[string]string{
			"refs/heads/trunk":    strings.Repeat("1", 40),
			"refs/tags/annotated": strings.Repeat("2", 40),
			"refs/notes/commits":  strings.Repeat("3", 40),
		},
		Coverage: FullCoverage(),
	}
}

func manifestBytes(t *testing.T, m Manifest) []byte {
	t.Helper()
	b, err := m.Marshal(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestManifestRoundTrip(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			m := fixtureManifest()
			m.GitObjectFormat = format
			if format == "sha256" {
				for k, v := range m.Refs {
					m.Refs[k] = v + strings.Repeat("4", 24)
				}
			}
			b := manifestBytes(t, m)
			got, err := ParseManifest(b, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m, got) {
				t.Fatalf("round trip changed manifest: %#v", got)
			}
			reread, err := ReadManifest(bytes.NewReader(b), DefaultLimits())
			if err != nil || !reflect.DeepEqual(got, reread) {
				t.Fatalf("reader round trip: %#v, %v", reread, err)
			}
			second := manifestBytes(t, got)
			if !bytes.Equal(b, second) {
				t.Fatal("wire round trip changed bytes")
			}
		})
	}
	m := fixtureManifest()
	m.Repository = RepositoryIdentity{"local", m.SnapshotID, "temporary local source"}
	if _, err := ParseManifest(manifestBytes(t, m), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for range 100 {
		id, err := NewID()
		if err != nil || !validID(id) || ids[id] {
			t.Fatalf("generated invalid/repeated ID %q: %v", id, err)
		}
		ids[id] = true
	}
}

func mutatedJSON(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(manifestBytes(t, fixtureManifest()), &obj); err != nil {
		t.Fatal(err)
	}
	mutate(obj)
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestManifestRejectsInvalidInput(t *testing.T) {
	base := manifestBytes(t, fixtureManifest())
	cases := map[string][]byte{
		"truncated":               base[:len(base)-1],
		"trailing document":       append(append([]byte{}, base...), []byte(" {}")...),
		"trailing scalar":         append(append([]byte{}, base...), []byte(" true")...),
		"trailing garbage":        append(append([]byte{}, base...), 'x'),
		"invalid UTF8":            bytes.Replace(base, []byte("synthetic"), []byte{0xff}, 1),
		"unpaired high surrogate": bytes.Replace(base, []byte("synthetic"), []byte(`\ud800`), 1),
		"unpaired low surrogate":  bytes.Replace(base, []byte("synthetic"), []byte(`\udfff`), 1),
		"bad surrogate pair":      bytes.Replace(base, []byte("synthetic"), []byte(`\ud800\u1234`), 1),
		"escaped duplicate":       bytes.Replace(base, []byte(`"format_version":1`), []byte(`"format_version":1,"format_\u0076ersion":1`), 1),
		"integer overflow":        bytes.Replace(base, []byte(`"size_bytes":123456`), []byte(`"size_bytes":9223372036854775808`), 1),
		"fraction size":           bytes.Replace(base, []byte(`"size_bytes":123456`), []byte(`"size_bytes":1.0`), 1),
		"exponent size":           bytes.Replace(base, []byte(`"size_bytes":123456`), []byte(`"size_bytes":1e2`), 1),
		"array root":              []byte(`[]`),
		"null root":               []byte(`null`),
	}
	for _, path := range []string{"manifest", "repository", "bundle", "head", "coverage", "refs"} {
		var key, value string
		switch path {
		case "manifest":
			key, value = "format_version", "1"
		case "repository":
			key, value = "provider", `"github"`
		case "bundle":
			key, value = "kind", `"full"`
		case "head":
			key, value = "kind", `"symbolic"`
		case "coverage":
			key, value = "git_refs", `"advertised_and_fetched"`
		case "refs":
			key, value = "refs/heads/trunk", `"`+strings.Repeat("1", 40)+`"`
		}
		needle := `"` + key + `":` + value
		// kind occurs in bundle and head, but the chosen values disambiguate.
		cases["duplicate "+path] = bytes.Replace(base, []byte(needle), []byte(needle+","+needle), 1)
	}
	for _, kind := range []string{"manifest", "repository", "bundle", "coverage", "head"} {
		for _, field := range schema(kind) {
			if kind == "head" && field == "oid" {
				continue
			}
			cases["missing "+kind+"."+field] = mutatedJSON(t, func(obj map[string]any) {
				if kind != "manifest" {
					obj = obj[kind].(map[string]any)
				}
				delete(obj, field)
			})
			cases["null "+kind+"."+field] = mutatedJSON(t, func(obj map[string]any) {
				if kind != "manifest" {
					obj = obj[kind].(map[string]any)
				}
				obj[field] = nil
			})
		}
		cases["unknown "+kind] = mutatedJSON(t, func(obj map[string]any) {
			if kind != "manifest" {
				obj = obj[kind].(map[string]any)
			}
			obj["future_field"] = 1
		})
	}
	mutations := []struct {
		path, field string
		values      []any
	}{
		{"manifest", "format_version", []any{0, 2, -1, "1", true}},
		{"manifest", "snapshot_id", []any{"../escape", "/absolute", "12345678-1234-1234-8234-123456789abc", "12345678-1234-4234-7234-123456789abc", "12345678-1234-4234-8234-123456789ABC"}},
		{"manifest", "created_at", []any{"2026-10-22T10:30:00+00:00", "2026-10-22T10:30:00+01:00", "2026-02-30T10:30:00Z", "2026-10-22T1:30:00Z", "2026-10-22T10:30:00,1Z", "not time"}},
		{"manifest", "application_version", []any{"", strings.Repeat("v", 257), "bad\nvalue"}},
		{"manifest", "git_object_format", []any{"sha512", "SHA1", ""}},
		{"repository", "provider", []any{"../github", "future", "github/evil", "https://example.invalid"}},
		{"repository", "id", []any{"../1", "/1", "0", "01", "1/2", "-1", "1?x", "", strings.Repeat("1", 257)}},
		{"repository", "display_name", []any{"", strings.Repeat("x", 1025), "bad\x00name"}},
		{"bundle", "file", []any{"/repository.bundle", "../repository.bundle", "sub/repository.bundle", "repository.bundle/..", "other.bundle"}},
		{"bundle", "kind", []any{"incremental", ""}},
		{"bundle", "sha256", []any{strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)}},
		{"bundle", "size_bytes", []any{-1, float64(1 << 40)}},
		{"bundle", "prerequisites", []any{[]any{"some-base"}, "", map[string]any{}}},
		{"coverage", "lfs_objects", []any{"included", ""}},
		{"coverage", "submodule_repositories", []any{"included"}},
		{"coverage", "github_metadata", []any{"included"}},
		{"coverage", "git_refs", []any{"all"}},
	}
	for _, mutation := range mutations {
		for i, value := range mutation.values {
			name := mutation.path + "." + mutation.field + "/" + string(rune('a'+i))
			cases[name] = mutatedJSON(t, func(obj map[string]any) {
				if mutation.path != "manifest" {
					obj = obj[mutation.path].(map[string]any)
				}
				obj[mutation.field] = value
			})
		}
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseManifest(b, DefaultLimits())
			if err == nil {
				t.Fatalf("accepted invalid input: %s", b)
			}
			if !reflect.DeepEqual(got, Manifest{}) {
				t.Fatal("error leaked partial manifest")
			}
		})
	}
	// Real surrogate pairs, literal replacement characters and escaped backslashes
	// must not be rejected by the encoding preflight.
	for _, encoded := range []string{`\ud83d\ude80`, `\ufffd`, `\\ud800`} {
		b := bytes.Replace(base, []byte("synthetic"), []byte(encoded), 1)
		if _, err := ParseManifest(b, DefaultLimits()); err != nil {
			t.Fatalf("valid encoding %s rejected: %v", encoded, err)
		}
	}
	// The writer cannot bypass the parser by constructing an invalid Go value.
	for name, mutate := range map[string]func(*Manifest){
		"zero version":          func(m *Manifest) { m.FormatVersion = 0 },
		"missing refs":          func(m *Manifest) { m.Refs = nil },
		"missing prerequisites": func(m *Manifest) { m.Bundle.Prerequisites = nil },
		"prerequisites":         func(m *Manifest) { m.Bundle.Prerequisites = []string{"base"} },
		"non UTC":               func(m *Manifest) { m.CreatedAt = m.CreatedAt.In(time.FixedZone("synthetic", 3600)) },
		"zero time":             func(m *Manifest) { m.CreatedAt = time.Time{} },
		"bad ref":               func(m *Manifest) { m.Refs["refs/bad:name"] = strings.Repeat("a", 40) },
		"bad raw oid":           func(m *Manifest) { m.Refs["refs/heads/trunk"] = strings.Repeat("a", 64) },
		"contradictory absent":  func(m *Manifest) { m.Head = Head{Kind: "absent", Target: "refs/heads/trunk"} },
		"invalid UTF8":          func(m *Manifest) { m.Repository.DisplayName = string([]byte{0xff}) },
	} {
		t.Run("typed writer/"+name, func(t *testing.T) {
			m := fixtureManifest()
			mutate(&m)
			if err := m.Validate(DefaultLimits()); err == nil {
				t.Fatal("validator accepted invalid Go value")
			}
			if b, err := m.Marshal(DefaultLimits()); err == nil || b != nil {
				t.Fatal("writer emitted invalid or partial manifest")
			}
		})
	}
}

type endlessReader struct{ read int }

func (r *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.read += len(p)
	return len(p), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestManifestBounds(t *testing.T) {
	m := fixtureManifest()
	b := manifestBytes(t, m)
	limits := DefaultLimits()
	limits.ManifestBytes = int64(len(b))
	if _, err := ParseManifest(b, limits); err != nil {
		t.Fatal("exact byte boundary:", err)
	}
	limits.ManifestBytes--
	if _, err := ParseManifest(b, limits); err == nil {
		t.Fatal("accepted byte overflow")
	}
	if _, err := m.Marshal(limits); err == nil {
		t.Fatal("writer accepted byte overflow")
	}
	if err := m.Validate(limits); err == nil {
		t.Fatal("validator accepted wire byte overflow")
	}
	r := &endlessReader{}
	if _, err := ReadManifest(r, limits); err == nil || int64(r.read) != limits.ManifestBytes+1 {
		t.Fatalf("reader not bounded: %d, %v", r.read, err)
	}
	if _, err := ReadManifest(failingReader{}, DefaultLimits()); err == nil {
		t.Fatal("lost reader error")
	}
	limits = DefaultLimits()
	limits.ReferenceCount = len(m.Refs)
	if _, err := ParseManifest(b, limits); err != nil {
		t.Fatal("exact ref count:", err)
	}
	limits.ReferenceCount--
	if _, err := ParseManifest(b, limits); err == nil {
		t.Fatal("accepted excess ref count")
	}
	if err := m.Validate(limits); err == nil {
		t.Fatal("validate accepted excess refs")
	}
	limits = DefaultLimits()
	m.Head = Head{Kind: "absent"}
	m.Refs = map[string]string{"refs/heads/long-boundary-name": strings.Repeat("1", 40)}
	b = manifestBytes(t, m)
	limits.ReferenceNameBytes = len("refs/heads/long-boundary-name")
	if _, err := ParseManifest(b, limits); err != nil {
		t.Fatal(err)
	}
	limits.ReferenceNameBytes--
	if _, err := ParseManifest(b, limits); err == nil {
		t.Fatal("accepted oversized name")
	}
	limits = DefaultLimits()
	limits.BundleBytes = m.Bundle.SizeBytes
	if _, err := ParseManifest(b, limits); err != nil {
		t.Fatal(err)
	}
	limits.BundleBytes--
	if _, err := ParseManifest(b, limits); err == nil {
		t.Fatal("accepted oversized bundle")
	}
	limits = DefaultLimits()
	// Find the exact aggregate wire metadata boundary. Every key and string value
	// counts, including escaped strings after decoding, not just ref names.
	low, high := int64(1), int64(len(b))
	for low < high {
		mid := (low + high) / 2
		limits.MetadataBytes = mid
		if _, err := ParseManifest(b, limits); err != nil {
			low = mid + 1
		} else {
			high = mid
		}
	}
	limits.MetadataBytes = low
	if _, err := ParseManifest(b, limits); err != nil {
		t.Fatal(err)
	}
	limits.MetadataBytes--
	if _, err := ParseManifest(b, limits); err == nil {
		t.Fatal("accepted metadata overflow")
	}
	invalidLimits := []ManifestLimits{
		{}, {ManifestBytes: 17 << 20}, DefaultLimits(), DefaultLimits(), DefaultLimits(), DefaultLimits(), DefaultLimits(),
	}
	invalidLimits[2].ReferenceCount = 1000001
	invalidLimits[3].ReferenceNameBytes = 16385
	invalidLimits[4].MetadataBytes = 17 << 20
	invalidLimits[5].BundleBytes = 1<<40 + 1
	invalidLimits[6].ManifestBytes = -1
	for _, l := range invalidLimits {
		if _, err := ParseManifest(b, l); err == nil {
			t.Fatalf("accepted invalid limits: %+v", l)
		}
	}
	deep := bytes.Replace(b, []byte(`"kind":"absent"`), []byte(`"kind":`+strings.Repeat("[", 1000)+`0`+strings.Repeat("]", 1000)), 1)
	if _, err := ParseManifest(deep, DefaultLimits()); err == nil {
		t.Fatal("accepted arbitrary nested allocation")
	}
}

func TestManifestRefsAndHead(t *testing.T) {
	for _, name := range []string{"refs/heads/trunk", "refs/notes/commits", "refs/custom/éclair", "refs/tags/a+b", "refs/heads/a@b", "refs/foo/-bar", "refs/foo/a.locked"} {
		t.Run("valid ref/"+name, func(t *testing.T) {
			m := fixtureManifest()
			m.Refs[name] = strings.Repeat("a", 40)
			if _, err := ParseManifest(manifestBytes(t, m), DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, name := range []string{"HEAD", "main", "-refs/heads/x", "refs/", "refs//a", "refs/a/", "refs/.a", "refs/a/.b", "refs/a..b", "refs/a.", "refs/a.lock", "refs/a.lock/b", "refs/a@{b", "refs/a b", "refs/a\tb", "refs/a\x7fb", "refs/a\x00b", "refs/a~b", "refs/a^b", "refs/a:b", "refs/a?b", "refs/a*b", "refs/a[b", "refs/a\\b", "refs/tags/v1^{}"} {
		t.Run("invalid ref/"+name, func(t *testing.T) {
			if ValidRefName(name) {
				t.Fatalf("invalid grammar accepted %q", name)
			}
			b := mutatedJSON(t, func(obj map[string]any) { obj["refs"].(map[string]any)[name] = strings.Repeat("a", 40) })
			if _, err := ParseManifest(b, DefaultLimits()); err == nil {
				t.Fatal("parser accepted invalid ref")
			}
		})
	}
	for _, format := range []string{"sha1", "sha256"} {
		n := 40
		if format == "sha256" {
			n = 64
		}
		for _, oid := range []string{strings.Repeat("a", n-1), strings.Repeat("a", n+1), strings.Repeat("g", n), ""} {
			b := mutatedJSON(t, func(obj map[string]any) {
				obj["git_object_format"] = format
				obj["refs"] = map[string]any{"refs/heads/trunk": oid}
			})
			if _, err := ParseManifest(b, DefaultLimits()); err == nil {
				t.Fatalf("accepted invalid %s OID %q", format, oid)
			}
		}
	}
	for _, h := range []Head{
		{Kind: "symbolic", Target: "refs/heads/trunk"},
		{Kind: "detached", OID: strings.Repeat("f", 40)},
		{Kind: "unborn", Target: "refs/heads/not-yet-created"},
		{Kind: "absent"},
	} {
		t.Run("HEAD/"+h.Kind, func(t *testing.T) {
			m := fixtureManifest()
			m.Head = h
			b := manifestBytes(t, m)
			got, err := ParseManifest(b, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if got.Head != h || !reflect.DeepEqual(got.Refs, m.Refs) {
				t.Fatal("HEAD/ref identity changed")
			}
			if h.Kind == "detached" && got.Head.OID == got.Refs["refs/heads/trunk"] {
				t.Fatal("detached-only root collapsed into branch")
			}
			if got.Refs["refs/tags/annotated"] != strings.Repeat("2", 40) {
				t.Fatal("raw annotated tag replaced by peeled commit")
			}
			if h.Kind == "absent" {
				if !bytes.Contains(b, []byte(`"head":{"kind":"absent"}`)) {
					t.Fatal("absent HEAD not exact wire shape")
				}
				// Parsing asserts representation only, not any Git execution or restore.
				r, err := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: NotPublished, Verification: Verification{Manifest: CheckPassed}})
				if err != nil || r.Verification().OfflineRestore != CheckNotRun || r.Artifact() != (ArtifactIdentity{}) {
					t.Fatal("schema acceptance incorrectly claims restore support")
				}
			}
		})
	}
	badHeads := []map[string]any{
		{"kind": "absent", "target": ""}, {"kind": "absent", "oid": ""}, {"kind": "absent", "target": "refs/heads/trunk"}, {"kind": "absent", "oid": strings.Repeat("1", 40)},
		{"kind": "detached", "oid": strings.Repeat("1", 40), "target": ""}, {"kind": "symbolic", "target": "refs/heads/trunk", "oid": ""},
		{"kind": "symbolic", "target": "refs/heads/missing"}, {"kind": "symbolic", "target": "../bad"}, {"kind": "detached", "oid": "bad"},
		{"kind": "unborn", "target": "refs/heads/trunk"}, {"kind": "unborn", "target": "refs/tags/missing"}, {"kind": "unborn"},
		{"kind": "detached"}, {"kind": "unknown"}, {"kind": "symbolic", "target": nil},
	}
	for i, h := range badHeads {
		b := mutatedJSON(t, func(obj map[string]any) { obj["head"] = h })
		if _, err := ParseManifest(b, DefaultLimits()); err == nil {
			t.Fatalf("accepted contradictory/invalid HEAD %d: %s", i, b)
		}
	}
	// Empty refs need not imply empty repository: a detached root is independent.
	m := fixtureManifest()
	m.Refs = map[string]string{}
	m.Head = Head{Kind: "detached", OID: strings.Repeat("A", 40)}
	if _, err := ParseManifest(manifestBytes(t, m), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}

func FuzzParseManifest(f *testing.F) {
	b, _ := fixtureManifest().Marshal(DefaultLimits())
	f.Add(b)
	f.Add([]byte(`{"format_version":1,"format_version":1}`))
	f.Add([]byte(`null`))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseManifest(b, DefaultLimits())
		if err != nil {
			return
		}
		wire, err := m.Marshal(DefaultLimits())
		if err != nil {
			t.Fatalf("accepted manifest cannot marshal: %v", err)
		}
		got, err := ParseManifest(wire, DefaultLimits())
		if err != nil || !reflect.DeepEqual(got, m) {
			t.Fatalf("round trip changed accepted input: %v", err)
		}
	})
}
