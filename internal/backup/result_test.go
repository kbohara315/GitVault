package backup

import "testing"

func TestResultPublicationFacts(t *testing.T) {
	id := ArtifactIdentity{fixtureManifest().SnapshotID, "github", "123456", "/synthetic/snapshots/id"}
	checks := Verification{CheckPassed, CheckPassed, CheckPassed, CheckPassed, CheckPassed, CheckPassed}
	for _, outcome := range []OperationOutcome{OutcomeSucceeded, OutcomeFailed, OutcomeCancelled, OutcomeInterrupted} {
		for _, publication := range []PublicationState{Published, PublishedDurabilityUncertain, PublicationUnknown} {
			t.Run(string(outcome)+"/"+string(publication), func(t *testing.T) {
				warnings := []Warning{{"durability_uncertain", "synthetic directory sync failure"}, {"bookkeeping_uncertain", "synthetic recording failure"}}
				r, err := NewResult(ResultFacts{outcome, publication, id, checks, warnings})
				if err != nil {
					t.Fatal(err)
				}
				warnings[0].Code = "changed"
				if r.Outcome() != outcome || r.Publication() != publication || r.Artifact() != id || r.Verification() != checks || r.Warnings()[0].Code != "durability_uncertain" {
					t.Fatal("independent result facts lost or aliased")
				}
				copy := r.Warnings()
				copy[0].Message = "changed"
				if r.Warnings()[0].Message == "changed" {
					t.Fatal("getter exposed mutable warning storage")
				}
				a := r.Artifact()
				a.Path = "changed"
				if r.Artifact().Path == "changed" {
					t.Fatal("artifact getter not a value copy")
				}
			})
		}
	}
	empty, err := NewResult(ResultFacts{Outcome: OutcomeEmptyRepository, Publication: NotPublished})
	if err != nil || empty.Outcome() != OutcomeEmptyRepository || empty.Artifact() != (ArtifactIdentity{}) || empty.Verification() != (Verification{}) {
		t.Fatalf("empty no-artifact result: %+v %v", empty, err)
	}
	// Hash success is deliberately independent of ref/HEAD/integrity/offline facts.
	r, err := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: NotPublished, Verification: Verification{Manifest: CheckPassed, Hash: CheckPassed, IndependentRefs: CheckFailed}})
	if err != nil || r.Verification().IndependentRefs != CheckFailed || r.Verification().Head != CheckNotRun || r.Verification().ObjectIntegrity != CheckNotRun || r.Verification().OfflineRestore != CheckNotRun {
		t.Fatal("verification dimensions conflated")
	}
	bad := []ResultFacts{
		{}, {Outcome: OutcomeFailed, Publication: "future"}, {Outcome: "future", Publication: NotPublished},
		{Outcome: OutcomeFailed, Publication: Published}, {Outcome: OutcomeFailed, Publication: PublicationUnknown},
		{Outcome: OutcomeEmptyRepository, Publication: Published, Artifact: id},
		{Outcome: OutcomeEmptyRepository, Publication: NotPublished, Artifact: id},
		{Outcome: OutcomeEmptyRepository, Publication: NotPublished, Verification: Verification{OfflineRestore: CheckPassed}},
		{Outcome: OutcomeFailed, Publication: NotPublished, Verification: Verification{Hash: "verified"}},
		{Outcome: OutcomeFailed, Publication: NotPublished, Warnings: []Warning{{"", "bad"}}},
		{Outcome: OutcomeFailed, Publication: NotPublished, Warnings: make([]Warning, 257)},
	}
	for i, f := range bad {
		if _, err := NewResult(f); err == nil {
			t.Fatalf("accepted invalid facts %d: %+v", i, f)
		}
	}
	for _, mutate := range []func(*ArtifactIdentity){func(a *ArtifactIdentity) { a.SnapshotID = "../bad" }, func(a *ArtifactIdentity) { a.RepositoryID = "../bad" }, func(a *ArtifactIdentity) { a.Provider = "../bad" }, func(a *ArtifactIdentity) { a.Path = "" }} {
		a := id
		mutate(&a)
		if _, err := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: PublishedDurabilityUncertain, Artifact: a}); err == nil {
			t.Fatalf("accepted incomplete/unsafe retained identity: %+v", a)
		}
	}
}
