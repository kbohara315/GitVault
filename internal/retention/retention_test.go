package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func date(s string) time.Time {
	v, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return v
}
func snap(id, when string) Snapshot {
	return Snapshot{ID: id, RepoID: "repo", CreatedAt: date(when), LocalPath: id, LocalReady: true}
}
func policy() Policy { return Policy{Revision: "p1", Mode: "explicit_delete"} }
func preview(t *testing.T, in Inventory, p Policy) Preview {
	t.Helper()
	v, e := BuildPreview(in, p)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func protected(v Preview) map[string]bool {
	m := map[string]bool{}
	for _, p := range v.Protected {
		m[p.SnapshotID] = true
	}
	return m
}
func candidates(v Preview) map[string]bool {
	m := map[string]bool{}
	for _, e := range v.Effects {
		m[e.SnapshotID] = true
	}
	return m
}
func TestRetentionBucketUnion(t *testing.T) {
	in := Inventory{Revision: "i1", Snapshots: []Snapshot{
		snap("june-new", "2025-06-10T22:00:00Z"), snap("june-tie-b", "2025-06-10T21:00:00Z"), snap("june-tie-a", "2025-06-10T21:00:00Z"),
		snap("june-9", "2025-06-09T23:59:59Z"), snap("june-8", "2025-06-08T23:59:59Z"), snap("may", "2025-05-31T23:59:59Z"), snap("april", "2025-04-30T23:59:59Z"),
	}}
	p := policy()
	p.KeepDaily = 2
	p.KeepWeekly = 2
	p.KeepMonthly = 2
	v := preview(t, in, p)
	want := map[string]bool{"june-new": true, "june-9": true, "june-8": true, "may": true}
	if !reflect.DeepEqual(protected(v), want) {
		t.Fatalf("union got %v want %v", protected(v), want)
	}
	if !reflect.DeepEqual(candidates(v), map[string]bool{"june-tie-a": true, "june-tie-b": true, "april": true}) {
		t.Fatal(v.Effects)
	}
	// Tie choice is deterministic, and UTC—not input offset—owns boundaries.
	in = Inventory{Revision: "i2", Snapshots: []Snapshot{snap("b", "2025-01-01T00:30:00+02:00"), snap("a", "2024-12-31T22:30:00Z"), snap("new", "2025-01-01T00:00:00Z")}}
	p.KeepDaily = 2
	p.KeepWeekly = 0
	p.KeepMonthly = 0
	v = preview(t, in, p)
	if !reflect.DeepEqual(protected(v), map[string]bool{"a": true, "new": true}) {
		t.Fatal(v.Protected)
	}
	// ISO week belongs to its ISO year at the calendar-year boundary.
	in = Inventory{Revision: "i3", Snapshots: []Snapshot{snap("sun", "2021-01-03T23:59:59Z"), snap("mon", "2021-01-04T00:00:00Z"), snap("dec", "2020-12-31T23:59:59Z")}}
	p.KeepDaily = 0
	p.KeepWeekly = 2
	v = preview(t, in, p)
	if !reflect.DeepEqual(protected(v), map[string]bool{"sun": true, "mon": true}) {
		t.Fatal(v.Protected)
	}
}
func TestRetentionProtectsPinsLeasesLastLocal(t *testing.T) {
	old := snap("old-valid", "2025-01-01T00:00:00Z")
	bad := snap("corrupt", "2025-06-01T00:00:00Z")
	bad.LocalReady = false
	pin := snap("pin", "2024-01-01T00:00:00Z")
	pin.Pinned = true
	lease := snap("lease", "2024-02-01T00:00:00Z")
	lease.Leased = true
	remote := snap("remote", "2025-07-01T00:00:00Z")
	remote.LocalPath = ""
	remote.LocalReady = false
	remote.CloudCopies = []CloudCopy{copyFixture()}
	missing := snap("missing", "2025-08-01T00:00:00Z")
	missing.LocalReady = false
	missing.LocalPath = ""
	other := snap("other-repo", "2025-01-01T00:00:00Z")
	other.RepoID = "other"
	in := Inventory{Revision: "i1", Snapshots: []Snapshot{old, bad, pin, lease, remote, missing, other}}
	v := preview(t, in, policy())
	if !reflect.DeepEqual(protected(v), map[string]bool{"old-valid": true, "pin": true, "lease": true, "other-repo": true}) {
		t.Fatal(v.Protected)
	}
	if !candidates(v)["corrupt"] || !candidates(v)["remote"] {
		t.Fatal(v.Effects)
	}
	// A newer remote receipt never displaces the only valid local snapshot.
	for _, e := range v.Effects {
		if e.SnapshotID == old.ID {
			t.Fatal("last valid local selected")
		}
	}
	p := policy()
	p.KeepDaily = 2
	v = preview(t, in, p)
	if !protected(v)["remote"] || !protected(v)["old-valid"] {
		t.Fatal("bucket union lost remote copy or last valid local", v.Protected)
	}
}
func TestRetentionPreviewExactRevisions(t *testing.T) {
	in := Inventory{Revision: "i1", Snapshots: []Snapshot{snap("old", "2025-01-01T00:00:00Z"), snap("new", "2025-02-01T00:00:00Z")}}
	p := policy()
	original := preview(t, in, p)
	// Normalization ignores ordering/timezone representation, not facts.
	reversed := Inventory{Revision: in.Revision, Snapshots: []Snapshot{in.Snapshots[1], in.Snapshots[0]}}
	reversed.Snapshots[1].CreatedAt = in.Snapshots[0].CreatedAt.In(time.FixedZone("fixture", 3600))
	if preview(t, reversed, p).ID != original.ID {
		t.Fatal("noncanonical identity")
	}
	for _, change := range []func(*Inventory, *Policy){func(i *Inventory, p *Policy) { i.Revision = "i2" }, func(i *Inventory, p *Policy) { p.Revision = "p2" }, func(i *Inventory, p *Policy) { i.Snapshots[0].Pinned = true }, func(i *Inventory, p *Policy) { i.Snapshots[0].Leased = true }, func(i *Inventory, p *Policy) { i.Snapshots[0].LocalPath = "renamed" }, func(i *Inventory, p *Policy) { p.KeepDaily = 1 }, func(i *Inventory, p *Policy) { i.Snapshots[0].CloudCopies = []CloudCopy{copyFixture()} }} {
		copyIn := Inventory{Revision: in.Revision, Snapshots: append([]Snapshot(nil), in.Snapshots...)}
		copyP := p
		change(&copyIn, &copyP)
		fresh := preview(t, copyIn, copyP)
		if fresh.ID == original.ID {
			t.Fatal("fact change did not invalidate ID")
		}
		if validatePreview(original, copyIn, copyP, Approve(original)) == nil {
			t.Fatal("stale approval accepted")
		}
	}
	for _, paths := range [][]string{{"old", "old/child"}, {"../escape", "new"}, {"old", "old"}, {"a", "a-b", "a/child"}} {
		bad := Inventory{Revision: "bad"}
		for n, path := range paths {
			s := snap(string(rune('a'+n)), "2025-01-01T00:00:00Z")
			s.LocalPath = path
			bad.Snapshots = append(bad.Snapshots, s)
		}
		if _, e := BuildPreview(bad, p); e == nil {
			t.Fatal("unsafe paths accepted", paths)
		}
	}
}
func writeSnapshot(t *testing.T, root, path, contents string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Join(root, path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, path, "bundle"), []byte(contents), 0600); e != nil {
		t.Fatal(e)
	}
}
func requireContents(t *testing.T, root, path, contents string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(root, path, "bundle"))
	if e != nil || string(b) != contents {
		t.Fatalf("%s: %q %v", path, b, e)
	}
}
func requireAbsent(t *testing.T, root, path string) {
	t.Helper()
	if _, e := os.Lstat(filepath.Join(root, path)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("%s still exists: %v", path, e)
	}
}
func fixture(t *testing.T) (Executor, Inventory, Policy, Preview) {
	t.Helper()
	root := t.TempDir()
	writeSnapshot(t, root, "old", "old bytes")
	writeSnapshot(t, root, "new", "protected bytes")
	jdir := t.TempDir()
	if e := os.Chmod(jdir, 0700); e != nil {
		t.Fatal(e)
	}
	j, e := NewFileJournal(jdir)
	if e != nil {
		t.Fatal(e)
	}
	in := Inventory{Revision: "i1", Snapshots: []Snapshot{snap("old", "2025-01-01T00:00:00Z"), snap("new", "2025-02-01T00:00:00Z")}}
	p := policy()
	v := preview(t, in, p)
	return Executor{Root: root, Journal: j}, in, p, v
}
func TestRetentionPreviewHasNoEffects(t *testing.T) {
	x, in, p, _ := fixture(t)
	for _, mode := range []string{"preview_only", "explicit_delete"} {
		p.Mode = mode
		for n := 0; n < 3; n++ {
			v := preview(t, in, p)
			if len(v.Effects) != 1 {
				t.Fatal(v)
			}
			requireContents(t, x.Root, "old", "old bytes")
			requireContents(t, x.Root, "new", "protected bytes")
		}
	}
	j := x.Journal.(*FileJournal)
	entries, e := os.ReadDir(j.directory)
	if e != nil || len(entries) != 0 {
		t.Fatal("preview created intent", entries, e)
	}
}
func TestDeleteRequiresExactApproval(t *testing.T) {
	for _, kind := range []string{"absent", "preview_id", "inventory_revision", "policy_revision", "scope_path", "scope_extra", "tampered_preview", "preview_only"} {
		t.Run(kind, func(t *testing.T) {
			x, in, p, v := fixture(t)
			a := Approve(v)
			switch kind {
			case "absent":
				a = Approval{}
			case "preview_id":
				a.PreviewID = "other"
			case "inventory_revision":
				a.InventoryRevision = "other"
			case "policy_revision":
				a.PolicyRevision = "other"
			case "scope_path":
				a.Effects[0].LocalPath = "new"
			case "scope_extra":
				a.Effects = append(a.Effects, Effect{Kind: "local", LocalPath: "new"})
			case "tampered_preview":
				v.Effects[0].LocalPath = "new"
			case "preview_only":
				p.Mode = "preview_only"
				v = preview(t, in, p)
				a = Approve(v)
			}
			if _, e := x.Execute(context.Background(), v, a, in, p); e == nil {
				t.Fatal("invalid approval accepted")
			}
			requireContents(t, x.Root, "old", "old bytes")
			requireContents(t, x.Root, "new", "protected bytes")
			entries, _ := os.ReadDir(x.Journal.(*FileJournal).directory)
			if len(entries) != 0 {
				t.Fatal("invalid approval persisted intent")
			}
		})
	}
	for _, field := range []string{"target", "key", "version"} {
		t.Run("remote_"+field, func(t *testing.T) {
			x, in, p, v, remote := withRemote(t)
			a := Approve(v)
			switch field {
			case "target":
				a.Effects[0].TargetRevision = "other"
			case "key":
				a.Effects[0].Key = "other/key"
			case "version":
				a.Effects[0].Version = "other-version"
			}
			if _, e := x.Execute(context.Background(), v, a, in, p); e == nil {
				t.Fatal("modified remote scope accepted")
			}
			if remote.objects["snap/manifest"] != "manifest bytes" || remote.objects["snap/bundle"] != "bundle bytes" {
				t.Fatal("remote bytes changed")
			}
			requireContents(t, x.Root, "old", "old bytes")
			requireContents(t, x.Root, "new", "protected bytes")
		})
	}
	x, in, p, v := fixture(t)
	r, e := x.Execute(context.Background(), v, Approve(v), in, p)
	if e != nil || r.CleanupRequired || r.Results[0].Status != "deleted" {
		t.Fatal(r, e)
	}
	requireAbsent(t, x.Root, "old")
	requireContents(t, x.Root, "new", "protected bytes")
}
func TestDeleteRevalidatesProtection(t *testing.T) {
	for _, kind := range []string{"pin", "lease", "last_local", "revision", "target_revision"} {
		t.Run(kind, func(t *testing.T) {
			x, in, p, v := fixture(t)
			switch kind {
			case "pin":
				in.Snapshots[0].Pinned = true
			case "lease":
				in.Snapshots[0].Leased = true
			case "last_local":
				in.Snapshots[1].LocalReady = false
			case "revision":
				in.Revision = "changed"
			case "target_revision":
				in.Snapshots[0].CloudCopies = []CloudCopy{copyFixture()}
			}
			if _, e := x.Execute(context.Background(), v, Approve(v), in, p); e == nil {
				t.Fatal("changed protection accepted")
			}
			requireContents(t, x.Root, "old", "old bytes")
			requireContents(t, x.Root, "new", "protected bytes")
		})
	}
	for _, kind := range []string{"escaping_symlink", "internal_symlink", "ancestor_symlink"} {
		t.Run(kind, func(t *testing.T) {
			x, in, p, v := fixture(t)
			outside := t.TempDir()
			writeSnapshot(t, outside, "outside", "untouched")
			path := filepath.Join(x.Root, "old", "link")
			target := filepath.Join(outside, "outside")
			if kind == "internal_symlink" {
				target = filepath.Join(x.Root, "new")
			}
			if kind == "ancestor_symlink" {
				if e := os.Mkdir(filepath.Join(x.Root, "ancestor"), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(filepath.Join(x.Root, "old"), filepath.Join(x.Root, "ancestor", "old")); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(filepath.Join(x.Root, "ancestor"), filepath.Join(x.Root, "real")); e != nil {
					t.Fatal(e)
				}
				path = filepath.Join(x.Root, "ancestor")
				target = filepath.Join(x.Root, "real")
				in.Snapshots[0].LocalPath = "ancestor/old"
				v = preview(t, in, p)
			}
			if e := os.Symlink(target, path); e != nil {
				t.Fatal(e)
			}
			r, e := x.Execute(context.Background(), v, Approve(v), in, p)
			if e == nil || !r.CleanupRequired {
				t.Fatal("symlink accepted", r, e)
			}
			if kind != "ancestor_symlink" {
				requireContents(t, x.Root, "old", "old bytes")
			} else {
				requireContents(t, x.Root, "real/old", "old bytes")
			}
			requireContents(t, x.Root, "new", "protected bytes")
			requireContents(t, outside, "outside", "untouched")
		})
	}
}
func copyFixture() CloudCopy {
	return CloudCopy{ID: "copy1", TargetID: "target1", TargetRevision: "target-r1", ManifestKey: "snap/manifest", PayloadKey: "snap/bundle", ManifestVersion: "mv1", PayloadVersion: "bv1"}
}

type fakeRemote struct {
	objects     map[string]string
	failPayload bool
	versioned   bool
	history     map[string]string
	afterDelete func(Effect)
}

func remoteFixture() *fakeRemote {
	return &fakeRemote{objects: map[string]string{"snap/manifest": "manifest bytes", "snap/bundle": "bundle bytes"}, history: map[string]string{}}
}
func (r *fakeRemote) Inspect(ctx context.Context, e Effect) (EffectResult, error) {
	if err := ctx.Err(); err != nil {
		return EffectResult{}, err
	}
	status := "pending"
	if _, ok := r.objects[e.Key]; !ok {
		status = "missing"
	}
	return EffectResult{Effect: e, Status: status}, nil
}
func (r *fakeRemote) Delete(ctx context.Context, e Effect) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if e.TargetID != "target1" || e.TargetRevision != "target-r1" || (e.Kind == "remote_manifest" && e.Version != "" && e.Version != "mv1") || (e.Kind == "remote_payload" && e.Version != "" && e.Version != "bv1") {
		return Receipt{}, errors.New("wrong exact target/version")
	}
	if _, ok := r.objects[e.Key]; !ok {
		return Receipt{}, errors.New("confirmed missing key must not be deleted again")
	}
	if e.Kind == "remote_payload" {
		if _, ok := r.objects["snap/manifest"]; ok {
			return Receipt{}, errors.New("payload before manifest")
		}
		if r.failPayload {
			return Receipt{}, errors.New("provider denied payload")
		}
	}
	if r.versioned {
		r.history[e.Key] = r.objects[e.Key]
	}
	delete(r.objects, e.Key)
	if r.afterDelete != nil {
		r.afterDelete(e)
	}
	return Receipt{ProviderReceipt: "synthetic-delete-receipt", DeleteMarker: r.versioned, RetainedVersions: r.versioned}, nil
}
func withRemote(t *testing.T) (Executor, Inventory, Policy, Preview, *fakeRemote) {
	x, in, p, _ := fixture(t)
	in.Snapshots[0].CloudCopies = []CloudCopy{copyFixture()}
	v := preview(t, in, p)
	remote := remoteFixture()
	x.Remote = remote
	return x, in, p, v, remote
}
func TestDeletePartialFailureRecovery(t *testing.T) {
	x, in, p, v, remote := withRemote(t)
	remote.failPayload = true
	r, e := x.Execute(context.Background(), v, Approve(v), in, p)
	if e == nil || !r.CleanupRequired || r.Results[0].Status != "deleted" || r.Results[1].Status != "unknown" {
		t.Fatal(r, e)
	}
	if _, ok := remote.objects["snap/manifest"]; ok {
		t.Fatal("manifest remains visible")
	}
	if remote.objects["snap/bundle"] != "bundle bytes" {
		t.Fatal("payload not preserved on failure")
	}
	requireContents(t, x.Root, "old", "old bytes")
	requireContents(t, x.Root, "new", "protected bytes")
	// Reopen journal as a new executor; retry only the still-approved effects.
	j, e := NewFileJournal(x.Journal.(*FileJournal).directory)
	if e != nil {
		t.Fatal(e)
	}
	x.Journal = j
	remote.failPayload = false
	// The transport refuses a second delete of any missing key, so a repeated
	// confirmed manifest would prevent the remaining payload/local effects.
	r, e = x.Execute(context.Background(), v, Approve(v), in, p)
	if e != nil || r.CleanupRequired {
		t.Fatal(r, e)
	}
	if len(remote.objects) != 0 {
		t.Fatal(remote.objects)
	}
	requireAbsent(t, x.Root, "old")
	requireContents(t, x.Root, "new", "protected bytes")
	stored, e := j.Load(context.Background(), v.ID)
	if e != nil || len(stored.Results) != 3 || stored.Results[0].Status != "deleted" || stored.Results[1].Status != "deleted" || stored.Results[2].Status != "deleted" {
		t.Fatal(stored, e)
	}
}
func TestDeleteCancellationAndRestart(t *testing.T) {
	x, in, p, v, remote := withRemote(t)
	ctx, cancel := context.WithCancel(context.Background())
	remote.afterDelete = func(e Effect) {
		if e.Kind == "remote_manifest" {
			cancel()
		}
	}
	r, e := x.Execute(ctx, v, Approve(v), in, p)
	if !errors.Is(e, context.Canceled) || !r.Cancelled || !r.CleanupRequired || r.Results[0].Status != "deleted" {
		t.Fatal(r, e)
	}
	if _, ok := remote.objects["snap/manifest"]; ok {
		t.Fatal("confirmed effect lost")
	}
	if remote.objects["snap/bundle"] != "bundle bytes" {
		t.Fatal("cancel continued effects")
	}
	requireContents(t, x.Root, "old", "old bytes")
	j, e := NewFileJournal(x.Journal.(*FileJournal).directory)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := j.Load(context.Background(), v.ID)
	if e != nil || saved.Results[0].Status != "deleted" {
		t.Fatal(saved, e)
	}
	x.Journal = j
	remote.afterDelete = nil
	r, e = x.Execute(context.Background(), v, Approve(v), in, p)
	if e != nil || r.CleanupRequired {
		t.Fatal(r, e)
	}
	requireAbsent(t, x.Root, "old")
	requireContents(t, x.Root, "new", "protected bytes")
	if len(remote.objects) != 0 {
		t.Fatal(remote.objects)
	}
	// Cancellation before first effect leaves all bytes unchanged.
	x, in, p, v = fixture(t)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, e = x.Execute(ctx, v, Approve(v), in, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	requireContents(t, x.Root, "old", "old bytes")
}
func TestVersionedDeleteDoesNotClaimErasure(t *testing.T) {
	x, in, p, v, remote := withRemote(t)
	// No explicit version: ordinary versioned deletion creates markers rather
	// than purging historical bytes. Explicit versions stay exact in scope.
	in.Snapshots[0].CloudCopies[0].ManifestVersion = ""
	in.Snapshots[0].CloudCopies[0].PayloadVersion = ""
	v = preview(t, in, p)
	remote.versioned = true
	r, e := x.Execute(context.Background(), v, Approve(v), in, p)
	if e != nil || r.CleanupRequired {
		t.Fatal(r, e)
	}
	if remote.history["snap/manifest"] != "manifest bytes" || remote.history["snap/bundle"] != "bundle bytes" {
		t.Fatal("older versions not retained")
	}
	if len(remote.objects) != 0 {
		t.Fatal("copy remains discoverable")
	}
	for _, result := range r.Results {
		if result.Effect.Kind != "local" && (!result.Receipt.DeleteMarker || !result.Receipt.RetainedVersions || result.Receipt.ProviderReceipt == "") {
			t.Fatal(result)
		}
	}
	requireAbsent(t, x.Root, "old")
	requireContents(t, x.Root, "new", "protected bytes")
}

type failingJournal struct {
	Journal
	fail func(Intent) bool
}

func (j failingJournal) Save(ctx context.Context, in Intent) error {
	if j.fail(in) {
		return errors.New("injected journal persistence failure")
	}
	return j.Journal.Save(ctx, in)
}

func TestDeleteJournalBoundariesAndUncertainRecovery(t *testing.T) {
	for _, boundary := range []string{"intent", "before_effect", "after_effect"} {
		t.Run(boundary, func(t *testing.T) {
			x, in, p, v := fixture(t)
			durable := x.Journal
			x.Journal = failingJournal{Journal: durable, fail: func(i Intent) bool {
				status := i.Results[0].Status
				return (boundary == "intent" && status == "pending") || (boundary == "before_effect" && status == "unknown") || (boundary == "after_effect" && status == "deleted")
			}}
			r, e := x.Execute(context.Background(), v, Approve(v), in, p)
			if e == nil {
				t.Fatal("journal failure ignored")
			}
			if boundary == "after_effect" {
				requireAbsent(t, x.Root, "old")
				if r.JournalError == "" || r.Results[0].Status != "deleted" {
					t.Fatal("lost known effect", r)
				}
			} else {
				requireContents(t, x.Root, "old", "old bytes")
			}
			requireContents(t, x.Root, "new", "protected bytes")
			x.Journal = durable
			if boundary == "after_effect" {
				in.Snapshots[0].LocalReady = false
			}
			r, e = x.Execute(context.Background(), v, Approve(v), in, p)
			if e != nil || r.CleanupRequired {
				t.Fatal(r, e)
			}
			requireAbsent(t, x.Root, "old")
			requireContents(t, x.Root, "new", "protected bytes")
			if boundary == "after_effect" && r.Results[0].Status != "missing" {
				t.Fatal("did not reconcile uncertain removal", r)
			}
		})
	}
	// Lost remote receipt after the effect: the persisted unknown state must
	// reconcile an absent exact key, not blindly issue another provider delete.
	x, in, p, v, remote := withRemote(t)
	durable := x.Journal
	x.Journal = failingJournal{Journal: durable, fail: func(i Intent) bool { return i.Results[0].Status == "deleted" }}
	r, e := x.Execute(context.Background(), v, Approve(v), in, p)
	if e == nil || r.Results[0].Status != "deleted" {
		t.Fatal(r, e)
	}
	if _, ok := remote.objects["snap/manifest"]; ok {
		t.Fatal("effect did not occur")
	}
	requireContents(t, x.Root, "old", "old bytes")
	x.Journal = durable
	r, e = x.Execute(context.Background(), v, Approve(v), in, p)
	if e != nil || r.CleanupRequired || r.Results[0].Status != "missing" {
		t.Fatal(r, e)
	}
	requireAbsent(t, x.Root, "old")
	requireContents(t, x.Root, "new", "protected bytes")
}

func TestDeleteEveryEffectPersistenceBoundary(t *testing.T) {
	for _, kind := range []string{"remote_manifest", "remote_payload", "local"} {
		for _, after := range []bool{false, true} {
			name := kind + "_before"
			if after {
				name = kind + "_after"
			}
			t.Run(name, func(t *testing.T) {
				x, in, p, v, remote := withRemote(t)
				durable := x.Journal
				status := "unknown"
				if after {
					status = "deleted"
				}
				x.Journal = failingJournal{Journal: durable, fail: func(i Intent) bool {
					for _, r := range i.Results {
						if r.Effect.Kind == kind && r.Status == status {
							return true
						}
					}
					return false
				}}
				r, e := x.Execute(context.Background(), v, Approve(v), in, p)
				if e == nil {
					t.Fatal("injected persistence error ignored")
				}
				manifestGone := kind != "remote_manifest" || after
				payloadGone := kind == "local" || (kind == "remote_payload" && after)
				localGone := kind == "local" && after
				if _, ok := remote.objects["snap/manifest"]; ok == manifestGone {
					t.Fatal("wrong manifest visibility", r)
				}
				if _, ok := remote.objects["snap/bundle"]; ok == payloadGone {
					t.Fatal("wrong payload effect", r)
				}
				if localGone {
					requireAbsent(t, x.Root, "old")
					in.Snapshots[0].LocalReady = false
				} else {
					requireContents(t, x.Root, "old", "old bytes")
				}
				requireContents(t, x.Root, "new", "protected bytes")
				x.Journal = durable
				r, e = x.Execute(context.Background(), v, Approve(v), in, p)
				if e != nil || r.CleanupRequired {
					t.Fatal(r, e)
				}
				requireAbsent(t, x.Root, "old")
				requireContents(t, x.Root, "new", "protected bytes")
				if len(remote.objects) != 0 {
					t.Fatal("remaining approved effects not completed", remote.objects)
				}
				// Truthful readiness after deletion does not invalidate the intent's
				// own tombstone; new protection still invalidates resumed approval.
				in.Snapshots[0].LocalReady = false
				if _, e := x.Execute(context.Background(), v, Approve(v), in, p); e != nil {
					t.Fatal("own completed effect made restart impossible", e)
				}
				in.Snapshots[0].Pinned = true
				if _, e := x.Execute(context.Background(), v, Approve(v), in, p); e == nil {
					t.Fatal("new pin ignored on restart")
				}
			})
		}
	}
}
