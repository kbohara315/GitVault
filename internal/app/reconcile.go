package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitvault/internal/backup"
	"gitvault/internal/store"
)

type ReconciliationReport struct {
	Interrupted         []store.Run
	MissingSnapshots    []string
	CorruptSnapshots    []string
	UnverifiedSnapshots []string
	DeletionIssues      []DeletionIssue
	InspectionRequired  []string
	UnownedStaging      []string
	Conflicts           []string
}

// DeletionIssue is an observed deletion-state row that lacks an I13-owned
// effect ledger. I04 reports it and blocks admission; it never resumes or
// guesses deletion effects.
type DeletionIssue struct {
	SnapshotID, RelativePath, SnapshotState, Detail string
}

var ErrDeletionReconciliationRequired = errors.New("snapshot deletion state requires explicit intent reconciliation")

// ReconcileStartup runs only after acquiring the same process/root ownership
// used for admission. It recovers only task-reserved artifact IDs. Unknown
// output and staging are reported for inspection and are never adopted/deleted.
func (c *Coordinator) ReconcileStartup(ctx context.Context) (ReconciliationReport, error) {
	var report ReconciliationReport
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return report, ErrCoordinatorClosed
	}
	if c.starting || c.active != nil || c.reconciling {
		c.mu.Unlock()
		return report, &store.BusyError{}
	}
	c.reconciling = true
	c.ready = false
	c.mu.Unlock()
	completed := false
	defer func() {
		c.mu.Lock()
		c.reconciling = false
		if completed && !c.closed {
			c.ready = true
		}
		c.mu.Unlock()
	}()
	releaseRun, err := lockCoordinatorRun(ctx, c.root.Path)
	if err != nil {
		return report, err
	}
	defer releaseRun()
	releaseRoot, err := lockManagedRoot(ctx, c.root.Path)
	if err != nil {
		return report, err
	}
	defer releaseRoot()
	canonical, fsID, err := canonicalManagedRoot(c.root.Path)
	if err != nil {
		return report, err
	}
	marker, err := readManagedRootMarker(filepath.Join(canonical, managedRootMarkerName))
	if err != nil {
		return report, err
	}
	binding, err := validateManagedRootLocked(ctx, c.store, canonical, fsID, marker)
	if err != nil {
		return report, err
	}
	if binding != c.root {
		return report, store.ErrForeignRoot
	}

	knownPaths := make(map[string]bool)
	snapshots, err := c.store.ListSnapshots(ctx, "", 10000)
	if err != nil {
		return report, err
	}
	if len(snapshots) == 10000 {
		return report, errors.New("snapshot reconciliation reached its bounded inventory limit")
	}
	for _, snapshot := range snapshots {
		if snapshot.RootID != c.root.ID {
			continue
		}
		knownPaths[filepath.ToSlash(snapshot.RelativePath)] = true
		if snapshot.State == "deleting" || snapshot.State == "deleted" {
			report.DeletionIssues = append(report.DeletionIssues, DeletionIssue{
				SnapshotID:    snapshot.ID,
				RelativePath:  snapshot.RelativePath,
				SnapshotState: snapshot.State,
				Detail:        "I04 has no approved effect ledger; deletion outcome is ambiguous and must be resolved by I13",
			})
			continue
		}
		full, ok := managedChildPath(c.root.Path, snapshot.RelativePath)
		if !ok {
			report.CorruptSnapshots = append(report.CorruptSnapshots, snapshot.RelativePath)
			if snapshot.State != "corrupt" && snapshot.State != "missing" {
				if err = c.store.MarkSnapshotState(ctx, snapshot.ID, "corrupt"); err != nil {
					return report, err
				}
			}
			continue
		}
		state, verifyErr := c.verifyIndexedSnapshot(ctx, full, snapshot)
		switch state {
		case "missing":
			report.MissingSnapshots = append(report.MissingSnapshots, snapshot.RelativePath)
			if snapshot.State != "missing" && snapshot.State != "corrupt" {
				if err = c.store.MarkSnapshotState(ctx, snapshot.ID, "missing"); err != nil {
					return report, err
				}
			}
		case "corrupt":
			report.CorruptSnapshots = append(report.CorruptSnapshots, snapshot.RelativePath)
			if snapshot.State != "corrupt" && snapshot.State != "missing" {
				if err = c.store.MarkSnapshotState(ctx, snapshot.ID, "corrupt"); err != nil {
					return report, err
				}
			}
		case "unverified":
			report.UnverifiedSnapshots = append(report.UnverifiedSnapshots, snapshot.RelativePath)
			if verifyErr != nil && ctx.Err() != nil {
				return report, ctx.Err()
			}
		}
	}

	runs, err := c.store.QueryRuns(ctx, 1000)
	if err != nil {
		return report, err
	}
	if len(runs) == 1000 {
		return report, errors.New("run reconciliation reached its bounded inventory limit")
	}
	for _, run := range runs {
		if run.State != "preparing" && run.State != "running" && run.State != "cancelling" {
			continue
		}
		tasks, listErr := c.store.ListTasks(ctx, run.ID, 10000)
		if listErr != nil {
			return report, listErr
		}
		if len(tasks) == 10000 {
			return report, errors.New("task reconciliation reached its bounded inventory limit")
		}
		for _, task := range tasks {
			if task.Outcome != "pending" {
				continue
			}
			if task.Provider != "local" || !validUUIDv4(task.OperationID) || !validUUIDv4(task.RepositoryID) {
				continue
			}
			relative := filepath.Join("snapshots", task.Provider, task.RepositoryID, task.OperationID)
			full, ok := managedChildPath(c.root.Path, relative)
			if !ok {
				return report, errors.New("reserved snapshot path is unsafe")
			}
			knownPaths[filepath.ToSlash(relative)] = true
			if err = c.reconcileReservedTask(ctx, task, full, &report); err != nil {
				return report, err
			}
		}
	}

	unknown, err := scanSnapshotLeaves(c.root.Path, 10000)
	if err != nil {
		return report, err
	}
	for _, relative := range unknown {
		if !knownPaths[filepath.ToSlash(relative)] {
			report.InspectionRequired = append(report.InspectionRequired, relative)
		}
	}
	staging, err := scanStaging(c.root.Path, 10000)
	if err != nil {
		return report, err
	}
	report.UnownedStaging = staging

	report.Interrupted, err = c.store.ReconcileInterrupted(ctx)
	if err != nil {
		return report, err
	}
	if len(report.DeletionIssues) > 0 {
		return report, ErrDeletionReconciliationRequired
	}
	completed = true
	return report, nil
}

func (c *Coordinator) reconcileReservedTask(ctx context.Context, task store.Task, path string, report *ReconciliationReport) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		report.MissingSnapshots = append(report.MissingSnapshots, filepath.ToSlash(mustRelative(c.root.Path, path)))
		return nil // store reconciliation records owner loss; nothing is published.
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		report.Conflicts = append(report.Conflicts, filepath.ToSlash(mustRelative(c.root.Path, path)))
		return c.recordAmbiguousTask(ctx, task, path)
	}
	manifest, err := c.engine.Inspect(ctx, path)
	if err != nil || manifest.SnapshotID != task.OperationID || manifest.Repository.Provider != task.Provider || manifest.Repository.ID != task.RepositoryID {
		report.Conflicts = append(report.Conflicts, filepath.ToSlash(mustRelative(c.root.Path, path)))
		return c.recordAmbiguousTask(ctx, task, path)
	}
	verification, err := c.engine.Verify(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report.Conflicts = append(report.Conflicts, filepath.ToSlash(mustRelative(c.root.Path, path)))
		return c.recordAmbiguousTask(ctx, task, path)
	}
	relative := mustRelative(c.root.Path, path)
	snapshot := snapshotRecord(c.root.ID, relative, manifest, string(backup.PublishedDurabilityUncertain), verification)
	if err = c.store.IndexSnapshot(ctx, snapshot); err != nil {
		report.Conflicts = append(report.Conflicts, filepath.ToSlash(relative))
		return c.recordAmbiguousTask(ctx, task, path)
	}
	update := store.TaskUpdate{Stage: "local_ready", Outcome: "pending", Attempt: task.Attempt, SnapshotID: task.OperationID, ArtifactID: task.OperationID, ArtifactPath: path, PublicationState: string(backup.PublishedDurabilityUncertain), CloudOutcome: "not_requested"}
	return c.advanceTaskTo(ctx, task, update, task.Stage, "succeeded", "succeeded")
}

func (c *Coordinator) recordAmbiguousTask(ctx context.Context, task store.Task, path string) error {
	update := store.TaskUpdate{Stage: task.Stage, Outcome: "interrupted", Attempt: task.Attempt, ArtifactID: task.OperationID, ArtifactPath: path, PublicationState: string(backup.PublicationUnknown), CloudOutcome: task.CloudOutcome, ErrorCode: "publication_ambiguous"}
	return c.store.UpdateTask(ctx, task.ID, update)
}

func (c *Coordinator) verifyIndexedSnapshot(ctx context.Context, path string, saved store.Snapshot) (state string, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "unverified", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "corrupt", nil
	}
	manifest, err := c.engine.Inspect(ctx, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "corrupt", nil
		}
		return "unverified", err
	}
	if manifest.SnapshotID != saved.ID || manifest.Repository.ID != saved.RepositoryID || manifest.Repository.Provider != saved.Provider || manifest.Bundle.SizeBytes != saved.SizeBytes || manifest.Bundle.SHA256 != saved.SHA256 {
		return "corrupt", nil
	}
	verification, err := c.engine.Verify(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return "unverified", ctx.Err()
		}
		// A deterministic offline verification failure reduces readiness. The
		// artifact is retained; reconciliation never repairs or deletes bytes.
		return "corrupt", err
	}
	if verification != (backup.Verification{Manifest: backup.CheckPassed, Hash: backup.CheckPassed, IndependentRefs: backup.CheckPassed, Head: backup.CheckPassed, ObjectIntegrity: backup.CheckPassed, OfflineRestore: backup.CheckPassed}) {
		return "unverified", errors.New("snapshot verification returned incomplete evidence")
	}
	return "ready", nil
}

func managedChildPath(root, relative string) (string, bool) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, 0) {
		return "", false
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Clean(path) != path {
		return "", false
	}
	return path, true
}

func mustRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "inspection-required"
	}
	return relative
}

func scanSnapshotLeaves(root string, limit int) ([]string, error) {
	base := filepath.Join(root, "snapshots")
	remaining := limit
	providers, err := readManagedDirectory(base, &remaining)
	if err != nil {
		return nil, err
	}
	out := []string{}
	add := func(path string) error {
		out = append(out, mustRelative(root, path))
		if len(out) > limit {
			return errors.New("snapshot directory scan reached its bounded limit")
		}
		return nil
	}
	for _, provider := range providers {
		p := filepath.Join(base, provider.Name())
		if err = walkManagedDirs(root, p, provider, 2, &remaining, add); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func walkManagedDirs(root, path string, entry os.DirEntry, levels int, remaining *int, add func(string) error) error {
	if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
		return add(path)
	}
	if levels == 0 {
		return add(path)
	}
	children, err := readManagedDirectory(path, remaining)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err = walkManagedDirs(root, filepath.Join(path, child.Name()), child, levels-1, remaining, add); err != nil {
			return err
		}
	}
	return nil
}

func scanStaging(root string, limit int) ([]string, error) {
	base := filepath.Join(root, "staging")
	remaining := limit
	entries, err := readManagedDirectory(base, &remaining)
	if err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("staging inspection reached its bounded limit")
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, filepath.ToSlash(filepath.Join("staging", entry.Name())))
	}
	return out, nil
}

func readManagedDirectory(path string, remaining *int) ([]os.DirEntry, error) {
	if remaining == nil || *remaining < 0 {
		return nil, errors.New("managed directory scan has no remaining budget")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("managed directory is not a real directory")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.IsDir() || !os.SameFile(info, opened) {
		return nil, errors.New("managed directory identity changed while opening")
	}
	entries := make([]os.DirEntry, 0, min(*remaining, 128))
	for {
		batchSize := min(128, *remaining+1)
		batch, readErr := f.ReadDir(batchSize)
		if len(batch) > *remaining {
			return nil, errors.New("managed directory scan reached its bounded entry limit")
		}
		*remaining -= len(batch)
		entries = append(entries, batch...)
		if errors.Is(readErr, io.EOF) {
			return entries, nil
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(batch) == 0 {
			return nil, errors.New("managed directory scan made no progress")
		}
	}
}

func (r ReconciliationReport) String() string {
	return fmt.Sprintf("interrupted=%d missing=%d corrupt=%d unverified=%d deletion_issues=%d inspection_required=%d unowned_staging=%d conflicts=%d", len(r.Interrupted), len(r.MissingSnapshots), len(r.CorruptSnapshots), len(r.UnverifiedSnapshots), len(r.DeletionIssues), len(r.InspectionRequired), len(r.UnownedStaging), len(r.Conflicts))
}
