package retention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Receipt struct {
	ProviderReceipt  string
	DeleteMarker     bool
	RetainedVersions bool
	// Ordinary deletion never proves secure erasure or reclaimed bytes.
}
type EffectResult struct {
	Effect  Effect
	Status  string // pending, deleted, missing, failed, unknown
	Receipt Receipt
	Error   string
}
type Intent struct {
	Preview  Preview
	Approval Approval
	Results  []EffectResult
}

// Journal.Save must atomically durably replace the record; Load must distinguish
// absent (os.ErrNotExist) from unavailable/corrupt. Never start effects on error.
type Journal interface {
	Load(context.Context, string) (Intent, error)
	Save(context.Context, Intent) error
}

// Remote reconciles exact uncertain keys before retry. Inspect returns pending
// only when safe to issue the same authorized delete, or deleted/missing when
// confirmed. Unknown must remain unknown, not permission to blindly retry.
type Remote interface {
	Inspect(context.Context, Effect) (EffectResult, error)
	Delete(context.Context, Effect) (Receipt, error)
}
type Execution struct {
	Results         []EffectResult
	CleanupRequired bool
	Cancelled       bool
	JournalError    string
}
type Executor struct {
	Root    string
	Journal Journal
	Remote  Remote
}

// Execute requires fresh inventory under the caller's root/metadata ownership.
// On resume retain identity tombstones for intentionally-deleting records and
// the inventory revision reserved by this intent. LocalReady may become false
// for this intent's confirmed/uncertain local removals; no other fact change is
// accepted. Outside mutations must change the revision. External changes
// (including pins/leases) invalidate scope. The caller must not release
// ownership until Execute returns. Root must be the validated bound root.
func (x Executor) Execute(ctx context.Context, p Preview, a Approval, in Inventory, policy Policy) (Execution, error) {
	if x.Journal == nil {
		return Execution{}, errors.New("durable deletion journal required")
	}
	if err := ctx.Err(); err != nil {
		return Execution{Cancelled: true, CleanupRequired: len(p.Effects) > 0}, err
	}
	intent, err := x.Journal.Load(ctx, p.ID)
	if errors.Is(err, os.ErrNotExist) {
		if err := validatePreview(p, in, policy, a); err != nil {
			return Execution{}, err
		}
		intent = Intent{Preview: p, Approval: a}
		for _, e := range p.Effects {
			intent.Results = append(intent.Results, EffectResult{Effect: e, Status: "pending"})
		}
		if err = x.Journal.Save(ctx, intent); err != nil {
			return execution(intent, ctx.Err() != nil, err.Error()), err
		}
	} else if err != nil {
		return Execution{CleanupRequired: len(p.Effects) != 0, JournalError: err.Error()}, err
	}
	if digest(intent.Preview) != digest(p) || digest(intent.Approval) != digest(a) || len(intent.Results) != len(p.Effects) {
		return Execution{}, errors.New("journal scope mismatch")
	}
	for i, r := range intent.Results {
		if r.Effect != p.Effects[i] || !knownStatus(r.Status) {
			return Execution{}, errors.New("invalid journal effect")
		}
	}
	// Reconstruct only readiness changed by an already journaled local effect.
	// All identity, revision, protection and copy facts still match exactly.
	check := Inventory{Revision: in.Revision, Snapshots: append([]Snapshot(nil), in.Snapshots...)}
	for _, r := range intent.Results {
		if r.Effect.Kind != "local" || (r.Status != "deleted" && r.Status != "missing" && r.Status != "unknown") {
			continue
		}
		for i := range check.Snapshots {
			s := &check.Snapshots[i]
			if s.ID == r.Effect.SnapshotID && !s.LocalReady && r.Effect.LocalWasReady {
				s.LocalReady = true
			}
		}
	}
	if err := validatePreview(p, check, policy, a); err != nil {
		return execution(intent, false, ""), err
	}
	var root *os.Root
	for _, e := range p.Effects {
		if e.Kind == "local" {
			if !filepath.IsAbs(x.Root) {
				return execution(intent, false, ""), errors.New("absolute owned root required")
			}
			info, e := os.Lstat(x.Root)
			if e != nil {
				return execution(intent, false, ""), e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return execution(intent, false, ""), errors.New("unsafe root")
			}
			root, err = os.OpenRoot(x.Root)
			if err != nil {
				return execution(intent, false, ""), err
			}
			defer root.Close()
			break
		}
	}
	for i := range intent.Results {
		r := &intent.Results[i]
		if r.Status == "deleted" || r.Status == "missing" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return execution(intent, true, ""), err
		}
		if r.Effect.Kind != "local" && x.Remote == nil {
			return execution(intent, false, ""), errors.New("remote adapter required")
		}
		if r.Effect.Kind != "local" && (r.Status == "failed" || r.Status == "unknown") {
			observed, e := x.Remote.Inspect(ctx, r.Effect)
			if e != nil || (observed.Status != "deleted" && observed.Status != "missing" && observed.Status != "pending") {
				return execution(intent, false, ""), errors.New("remote outcome requires reconciliation")
			}
			r.Status, r.Receipt, r.Error = observed.Status, observed.Receipt, ""
			if err := x.Journal.Save(ctx, intent); err != nil {
				return execution(intent, false, err.Error()), err
			}
			if r.Status != "pending" {
				continue
			}
		}
		// Persist unknown before each effect. A crash after bytes disappear but
		// before the receipt forces exact reconciliation on the next attempt.
		r.Status, r.Error = "unknown", ""
		if err := x.Journal.Save(ctx, intent); err != nil {
			return execution(intent, false, err.Error()), err
		}
		if r.Effect.Kind == "local" {
			r.Status, err = deleteLocal(ctx, root, r.Effect.LocalPath)
		} else {
			r.Receipt, err = x.Remote.Delete(ctx, r.Effect)
			if err == nil {
				r.Status = "deleted"
			}
		}
		if err != nil {
			r.Error = err.Error()
			if r.Status != "unknown" {
				r.Status = "failed"
			}
		}
		// Cancellation must not prevent recording a confirmed effect. The
		// journal gets a fresh bounded cleanup context, never unbounded work.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalTimeout)
		saveErr := x.Journal.Save(saveCtx, intent)
		cancel()
		if saveErr != nil {
			return execution(intent, ctx.Err() != nil, saveErr.Error()), saveErr
		}
		if err != nil {
			return execution(intent, ctx.Err() != nil, ""), err
		}
	}
	return execution(intent, ctx.Err() != nil, ""), ctx.Err()
}
func knownStatus(s string) bool {
	return s == "pending" || s == "deleted" || s == "missing" || s == "failed" || s == "unknown"
}
func execution(i Intent, cancelled bool, journalErr string) Execution {
	r := Execution{Results: append([]EffectResult(nil), i.Results...), Cancelled: cancelled, JournalError: journalErr, CleanupRequired: journalErr != ""}
	for _, e := range r.Results {
		if e.Status != "deleted" && e.Status != "missing" {
			r.CleanupRequired = true
		}
	}
	return r
}

// os.Root confines every traversal, including concurrent symlink replacement.
// Preflight refuses all symlinks and special files; no target outside Root can
// be followed by removal. Missing files are reconciled, not called erased.
func deleteLocal(ctx context.Context, root *os.Root, path string) (string, error) {
	if !validRelative(path) {
		return "failed", errors.New("unsafe local scope")
	}
	// Check each ancestor too, not merely the final subtree.
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		info, err := root.Lstat(parent)
		if err != nil {
			return "failed", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "failed", errors.New("symlink or non-directory ancestor")
		}
	}
	var paths []string
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "failed", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "failed", errors.New("snapshot scope must be a real directory")
	}
	// Bind recursive work to the approved directory inode, not a path whose
	// ancestors might be replaced. Symlinks cannot redirect recursive deletion
	// to another protected snapshot under the larger managed root.
	sub, err := root.OpenRoot(path)
	if err != nil {
		return "failed", err
	}
	defer sub.Close()
	opened, err := sub.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return "failed", errors.New("snapshot directory identity changed")
	}
	var scan func(string) error
	scan = func(p string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := sub.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsafe snapshot entry: %s", p)
		}
		if info.IsDir() {
			f, err := sub.Open(p)
			if err != nil {
				return err
			}
			entries, err := f.ReadDir(-1)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			for _, e := range entries {
				if err := scan(filepath.Join(p, e.Name())); err != nil {
					return err
				}
			}
		}
		if p != "." {
			paths = append(paths, p)
		}
		return nil
	}
	if err := scan("."); err != nil {
		return "failed", err
	}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return "unknown", err
		}
		if err := sub.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "unknown", err
		}
	}
	current, err := root.Lstat(path)
	if err != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 {
		return "unknown", errors.New("snapshot directory changed before final removal")
	}
	if err := ctx.Err(); err != nil {
		return "unknown", err
	}
	if err := root.Remove(path); err != nil {
		return "unknown", err
	}
	parent, err := root.Open(filepath.Dir(path))
	if err != nil {
		return "unknown", err
	}
	err = parent.Sync()
	closeErr := parent.Close()
	if err != nil {
		return "unknown", err
	}
	if closeErr != nil {
		return "unknown", closeErr
	}
	return "deleted", nil
}
