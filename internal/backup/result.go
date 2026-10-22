package backup

import "fmt"

type OperationOutcome string

const (
	OutcomeSucceeded       OperationOutcome = "succeeded"
	OutcomeFailed          OperationOutcome = "failed"
	OutcomeCancelled       OperationOutcome = "cancelled"
	OutcomeInterrupted     OperationOutcome = "interrupted"
	OutcomeEmptyRepository OperationOutcome = "empty_repository"
)

type PublicationState string

const (
	NotPublished                 PublicationState = "not_published"
	Published                    PublicationState = "published"
	PublishedDurabilityUncertain PublicationState = "published_durability_uncertain"
	PublicationUnknown           PublicationState = "outcome_unknown_requires_reconciliation"
)

// CheckState's zero value is no evidence, never a successful check.
type CheckState string

const (
	CheckNotRun CheckState = ""
	CheckPassed CheckState = "passed"
	CheckFailed CheckState = "failed"
)

// Verification records independent evidence, not a single "verified" boolean.
// A parsed manifest is not a hash, refs, HEAD, integrity or restoration check.
type Verification struct {
	Manifest        CheckState
	Hash            CheckState
	IndependentRefs CheckState
	Head            CheckState
	ObjectIntegrity CheckState
	OfflineRestore  CheckState
}

// ArtifactIdentity is a value copy. Path names the intended/published output;
// PublicationUnknown retains that identity for reconciliation, not blind retry.
type ArtifactIdentity struct {
	SnapshotID   string
	Provider     string
	RepositoryID string
	Path         string
}

type Warning struct {
	Code    string
	Message string
}

// ResultFacts is construction input. NewResult copies warnings so later caller
// mutations cannot alter a result delivered to another goroutine.
type ResultFacts struct {
	Outcome      OperationOutcome
	Publication  PublicationState
	Artifact     ArtifactIdentity
	Verification Verification
	Warnings     []Warning
}

// Result is immutable after construction. Operation cancellation/failure does
// not erase a published artifact or turn uncertainty into not_published.
// Cloud-copy outcomes belong to the separate cloud operation, not local success.
type Result struct {
	outcome      OperationOutcome
	publication  PublicationState
	artifact     ArtifactIdentity
	verification Verification
	warnings     []Warning
}

func NewResult(f ResultFacts) (Result, error) {
	var zero Result
	switch f.Outcome {
	case OutcomeSucceeded, OutcomeFailed, OutcomeCancelled, OutcomeInterrupted, OutcomeEmptyRepository:
	default:
		return zero, fmt.Errorf("invalid operation outcome")
	}
	switch f.Publication {
	case NotPublished, Published, PublishedDurabilityUncertain, PublicationUnknown:
	default:
		return zero, fmt.Errorf("invalid publication state")
	}
	for _, s := range []CheckState{f.Verification.Manifest, f.Verification.Hash, f.Verification.IndependentRefs, f.Verification.Head, f.Verification.ObjectIntegrity, f.Verification.OfflineRestore} {
		if s != CheckNotRun && s != CheckPassed && s != CheckFailed {
			return zero, fmt.Errorf("invalid verification state")
		}
	}
	if f.Artifact != (ArtifactIdentity{}) {
		if !validID(f.Artifact.SnapshotID) || !safeRepository(RepositoryIdentity{Provider: f.Artifact.Provider, ID: f.Artifact.RepositoryID}) || !text(f.Artifact.Path, 32768) {
			return zero, fmt.Errorf("invalid artifact identity")
		}
	} else if f.Publication != NotPublished {
		return zero, fmt.Errorf("publication requires retained artifact identity")
	}
	if f.Outcome == OutcomeEmptyRepository && (f.Publication != NotPublished || f.Artifact != (ArtifactIdentity{}) || f.Verification != (Verification{})) {
		return zero, fmt.Errorf("empty repository is a no-artifact outcome")
	}
	if len(f.Warnings) > 256 {
		return zero, fmt.Errorf("warning count limit exceeded")
	}
	for _, w := range f.Warnings {
		if !text(w.Code, 128) || !text(w.Message, 4096) {
			return zero, fmt.Errorf("invalid warning")
		}
	}
	return Result{f.Outcome, f.Publication, f.Artifact, f.Verification, append([]Warning(nil), f.Warnings...)}, nil
}

func (r Result) Outcome() OperationOutcome     { return r.outcome }
func (r Result) Publication() PublicationState { return r.publication }
func (r Result) Artifact() ArtifactIdentity    { return r.artifact }
func (r Result) Verification() Verification    { return r.verification }
func (r Result) Warnings() []Warning           { return append([]Warning(nil), r.warnings...) }
