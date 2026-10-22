package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGateTimeoutClosesInheritedPipe(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	fixture := filepath.Join(dir, "internal", "fixture")
	if err := os.MkdirAll(fixture, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module timeoutfixture\n\ngo 1.27.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package fixture
import("testing";"os";"os/exec";"time")
func TestHang(t *testing.T) {
 child:=exec.Command(os.Args[0],"-test.run=^TestDescendant$")
 child.Env=append(os.Environ(),"HOLD_GATE_PIPE=1")
 child.Stdout=os.Stdout;child.Stderr=os.Stderr
 if err:=child.Start();err!=nil {t.Fatal(err)}
 if err:=os.WriteFile("started",[]byte("descendant owns stdout"),0600);err!=nil {t.Fatal(err)}
 time.Sleep(30*time.Second)
 _=child.Wait()
}
func TestDescendant(t *testing.T) {if os.Getenv("HOLD_GATE_PIPE")=="1" {time.Sleep(30*time.Second)}}
`
	if err := os.WriteFile(filepath.Join(fixture, "fixture_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	warm := exec.Command("go", "test", "./internal/fixture", "-run", "^$", "-count=1")
	warm.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if out, err := warm.CombinedOutput(); err != nil {
		t.Fatalf("warm fixture: %v\n%s", err, out)
	}
	plan := `{"tasks":[{"id":"HANG","test_gates":[{"name":"timeout","package":"./internal/fixture","tests":["TestHang"]}]}]}`
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, []byte(plan), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := run(path, "HANG", 2*time.Second); err == nil {
		t.Fatal("timed-out fixture accepted")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("evidence read outlived timeout: %v", elapsed)
	}
	if _, err := os.Stat(filepath.Join(fixture, "started")); err != nil {
		t.Fatalf("fixture never reached inherited-pipe scenario: %v", err)
	}
}
