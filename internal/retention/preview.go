// Package retention owns pure retention previews and exact, approved deletion
// effects. The caller must hold managed-root ownership and serialize inventory,
// leases and pins throughout Execute. No setting grants deletion permission.
package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

type CloudCopy struct {
	ID, TargetID, TargetRevision                             string
	ManifestKey, ManifestVersion, PayloadKey, PayloadVersion string
}
type Snapshot struct {
	ID, RepoID                 string
	CreatedAt                  time.Time
	LocalPath                  string // Snapshot directory relative to the bound owned root.
	LocalReady, Pinned, Leased bool
	CloudCopies                []CloudCopy
}
type Inventory struct {
	Revision  string
	Snapshots []Snapshot
}
type Policy struct {
	Revision, Mode                     string
	KeepDaily, KeepWeekly, KeepMonthly int
}

func DefaultPolicy() Policy {
	return Policy{Mode: "preview_only", KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}
}

type Effect struct {
	ID, SnapshotID, Kind                           string // local, remote_manifest, remote_payload
	LocalPath                                      string
	LocalWasReady                                  bool // Original fact for reconciliation of this intent's own removal.
	CopyID, TargetID, TargetRevision, Key, Version string
}
type Protection struct {
	SnapshotID string
	Reasons    []string
}
type Preview struct {
	ID, InventoryRevision, PolicyRevision, InventoryDigest string
	Policy                                                 Policy
	Effects                                                []Effect
	Protected                                              []Protection
}
type Approval struct {
	PreviewID, InventoryRevision, PolicyRevision string
	Effects                                      []Effect
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validRelative(p string) bool {
	return p != "" && p != "." && filepath.IsLocal(p) && filepath.Clean(p) == p && p != ".."
}
func normalize(in Inventory) (Inventory, error) {
	if in.Revision == "" || len(in.Snapshots) > 100000 {
		return Inventory{}, errors.New("inventory revision required or inventory exceeds limit")
	}
	n := Inventory{Revision: in.Revision, Snapshots: make([]Snapshot, len(in.Snapshots))}
	ids, paths, targets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, s := range in.Snapshots {
		if s.ID == "" || s.RepoID == "" || s.CreatedAt.IsZero() || ids[s.ID] {
			return Inventory{}, errors.New("invalid or duplicate snapshot identity")
		}
		ids[s.ID] = true
		if s.LocalReady && s.LocalPath == "" {
			return Inventory{}, errors.New("ready local snapshot has no path")
		}
		if s.LocalPath != "" {
			if !validRelative(s.LocalPath) || paths[s.LocalPath] {
				return Inventory{}, errors.New("unsafe or duplicate local path")
			}
			paths[s.LocalPath] = true
		}
		s.CreatedAt = s.CreatedAt.UTC()
		s.CloudCopies = append([]CloudCopy(nil), s.CloudCopies...)
		copies := map[string]bool{}
		for _, c := range s.CloudCopies {
			if c.ID == "" || c.TargetID == "" || c.TargetRevision == "" || c.ManifestKey == "" || c.PayloadKey == "" || c.ManifestKey == c.PayloadKey || copies[c.ID] {
				return Inventory{}, errors.New("invalid cloud copy identity")
			}
			copies[c.ID] = true
			for _, key := range []string{c.ManifestKey, c.PayloadKey} {
				k := digest([]string{c.TargetID, key})
				if targets[k] {
					return Inventory{}, errors.New("overlapping remote scope")
				}
				targets[k] = true
			}
		}
		sort.Slice(s.CloudCopies, func(i, j int) bool { return s.CloudCopies[i].ID < s.CloudCopies[j].ID })
		n.Snapshots[i] = s
	}
	sort.Slice(n.Snapshots, func(i, j int) bool { return n.Snapshots[i].ID < n.Snapshots[j].ID })
	// Parent/child snapshot paths must never authorize recursive deletion of a
	// different snapshot (including a pinned descendant).
	for p := range paths {
		for parent := filepath.Dir(p); parent != "."; parent = filepath.Dir(parent) {
			if paths[parent] {
				return Inventory{}, errors.New("overlapping local snapshot paths")
			}
		}
	}
	return n, nil
}
func validatePolicy(p Policy) error {
	if p.Revision == "" || (p.Mode != "preview_only" && p.Mode != "explicit_delete") || p.KeepDaily < 0 || p.KeepDaily > 365 || p.KeepWeekly < 0 || p.KeepWeekly > 104 || p.KeepMonthly < 0 || p.KeepMonthly > 120 {
		return errors.New("invalid retention policy")
	}
	return nil
}

// BuildPreview has no filesystem or remote effects. Inventory is authoritative:
// LocalReady must mean verified, present, non-corrupt local bytes, not a DB row.
// Each repository retains the newest representative of its newest N distinct
// occupied UTC buckets. Equal timestamps choose the lexicographically lower ID.
func BuildPreview(in Inventory, p Policy) (Preview, error) {
	if err := validatePolicy(p); err != nil {
		return Preview{}, err
	}
	n, err := normalize(in)
	if err != nil {
		return Preview{}, err
	}
	keep := map[string][]string{}
	repos := map[string][]Snapshot{}
	for _, s := range n.Snapshots {
		if s.Pinned {
			keep[s.ID] = append(keep[s.ID], "pinned")
		}
		if s.Leased {
			keep[s.ID] = append(keep[s.ID], "leased")
		}
		repos[s.RepoID] = append(repos[s.RepoID], s)
	}
	for _, ss := range repos {
		sort.Slice(ss, func(i, j int) bool {
			if ss[i].CreatedAt.Equal(ss[j].CreatedAt) {
				return ss[i].ID < ss[j].ID
			}
			return ss[i].CreatedAt.After(ss[j].CreatedAt)
		})
		// Bucket membership does not certify recoverability. In particular a
		// remote-only or corrupt newest entry cannot satisfy local protection.
		for _, s := range ss {
			if s.LocalReady {
				keep[s.ID] = append(keep[s.ID], "last_valid_local")
				break
			}
		}
		for _, b := range []struct {
			name  string
			count int
		}{{"daily", p.KeepDaily}, {"weekly", p.KeepWeekly}, {"monthly", p.KeepMonthly}} {
			seen := map[string]bool{}
			for _, s := range ss {
				key := s.CreatedAt.Format("2006-01-02")
				if b.name == "weekly" {
					y, w := s.CreatedAt.ISOWeek()
					key = fmt.Sprintf("%04d-%02d", y, w)
				}
				if b.name == "monthly" {
					key = s.CreatedAt.Format("2006-01")
				}
				if !seen[key] && len(seen) < b.count {
					seen[key] = true
					keep[s.ID] = append(keep[s.ID], b.name)
				}
			}
		}
	}
	v := Preview{InventoryRevision: n.Revision, PolicyRevision: p.Revision, InventoryDigest: digest(n), Policy: p}
	for _, s := range n.Snapshots {
		if reasons := keep[s.ID]; len(reasons) > 0 {
			sort.Strings(reasons)
			v.Protected = append(v.Protected, Protection{s.ID, reasons})
			continue
		}
		for _, c := range s.CloudCopies {
			for _, e := range []Effect{{SnapshotID: s.ID, Kind: "remote_manifest", CopyID: c.ID, TargetID: c.TargetID, TargetRevision: c.TargetRevision, Key: c.ManifestKey, Version: c.ManifestVersion}, {SnapshotID: s.ID, Kind: "remote_payload", CopyID: c.ID, TargetID: c.TargetID, TargetRevision: c.TargetRevision, Key: c.PayloadKey, Version: c.PayloadVersion}} {
				e.ID = digest(e)
				v.Effects = append(v.Effects, e)
			}
		}
		if s.LocalPath != "" {
			e := Effect{SnapshotID: s.ID, Kind: "local", LocalPath: s.LocalPath, LocalWasReady: s.LocalReady}
			e.ID = digest(e)
			v.Effects = append(v.Effects, e)
		}
	}
	v.ID = digest(v)
	return v, nil
}

// Approve copies the exact scope for a UI/coordinator to record after explicit
// user review. Calling this function is not itself proof of human consent.
func Approve(p Preview) Approval {
	return Approval{p.ID, p.InventoryRevision, p.PolicyRevision, append([]Effect(nil), p.Effects...)}
}
func validatePreview(p Preview, in Inventory, policy Policy, a Approval) error {
	fresh, err := BuildPreview(in, policy)
	if err != nil {
		return err
	}
	if policy.Mode != "explicit_delete" || digest(fresh) != digest(p) || a.PreviewID != p.ID || a.InventoryRevision != p.InventoryRevision || a.PolicyRevision != p.PolicyRevision || digest(a.Effects) != digest(p.Effects) {
		return errors.New("stale, modified, or unapproved deletion scope")
	}
	return nil
}
