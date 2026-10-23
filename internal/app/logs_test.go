package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gitvault/internal/backup"
)

func TestSafeEngineErrorCodeDoesNotExposeRawError(t *testing.T) {
	got := safeEngineErrorCode(errors.Join(errors.New("private token and URL"), backup.ErrGitTransportDenied))
	if got != "engine_failed_git_transport_denied" {
		t.Fatalf("safe error code = %q", got)
	}
	if got = safeEngineErrorCode(errors.New("arbitrary secret text")); got != "engine_failed" {
		t.Fatalf("unknown error leaked or misclassified: %q", got)
	}
}

func TestOperationalLogsSurviveRestartAndRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operational-logs.json")
	entries := []LogEntry{{Level: "info", Message: "saved safe detail"}}
	body, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	got := loadOperationalLogs(path)
	if len(got) != 1 || got[0].Message != "saved safe detail" {
		t.Fatalf("persisted logs = %+v", got)
	}
}

func TestCopyViewCopiesLogs(t *testing.T) {
	state := ViewState{Logs: []LogEntry{{Level: "info", Message: "started"}}}
	copy := CopyView(state)
	copy.Logs[0].Message = "changed"
	if state.Logs[0].Message != "started" {
		t.Fatal("copied view shares its log slice")
	}
}
