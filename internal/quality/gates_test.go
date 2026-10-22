package quality_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitvault/internal/quality"
)

func TestGateRejectsMissingSkippedAndFailedEvidence(t *testing.T) {
	tests := []struct{ name, stream string }{
		{"missing", `{"Action":"pass","Package":"fixture"}`},
		{"skipped", "{\"Action\":\"run\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}"},
		{"failed-package", "{\"Action\":\"run\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"fail\",\"Package\":\"fixture\"}"},
		{"pass-without-run", "{\"Action\":\"pass\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}"},
		{"skipped-subcase", "{\"Action\":\"run\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestBehavior/recovery\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\",\"Test\":\"TestBehavior\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}"},
		{"malformed", "not-json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := quality.VerifyEvidence(strings.NewReader(tt.stream), []string{"TestBehavior"}); err == nil {
				t.Fatal("accepted incomplete, skipped, or failed evidence")
			}
		})
	}
}

func TestGateConsumesActualGoTestEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module gatefixture\n\ngo 1.27.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "behavior_test.go")
	source := `package gatefixture
import ("testing"; "os"; "path/filepath")
func TestBehavior(t *testing.T) { t.Run("recovery",func(t *testing.T){
 p:=filepath.Join(t.TempDir(),"restored")
 if err:=os.WriteFile(p,[]byte("retained history"),0600);err!=nil {t.Fatal(err)}
 b,err:=os.ReadFile(p);if err!=nil || string(b)!="retained history" {t.Fatalf("read: %q %v",b,err)}
}) }
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(pattern string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-run", pattern, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture go test: %v\n%s", err, out)
		}
		return out
	}
	out := run("^TestBehavior$")
	r, err := quality.VerifyEvidence(bytes.NewReader(out), []string{"TestBehavior"})
	if err != nil || r.Package != "gatefixture" || len(r.Passed) != 1 {
		t.Fatalf("real events rejected: %+v %v\n%s", r, err, out)
	}
	if _, err = quality.VerifyEvidence(bytes.NewReader(run("^NoSuchTest$")), []string{"TestBehavior"}); err == nil {
		t.Fatal("no-tests-to-run was accepted")
	}
	source = strings.Replace(source, "p:=filepath.Join(t.TempDir(),\"restored\")", "t.Skip(\"missing platform\"); p:=filepath.Join(t.TempDir(),\"restored\")", 1)
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = quality.VerifyEvidence(bytes.NewReader(run("^TestBehavior$")), []string{"TestBehavior"}); err == nil {
		t.Fatal("a skipped real subcase was accepted")
	}
}
