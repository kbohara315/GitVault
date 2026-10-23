//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildUsesSafeUmask(t *testing.T) {
	dir := t.TempDir()
	// This fake compiler models the inherited executable creation mode. No
	// network, real packaging or application data is needed for the regression.
	compiler := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > \"$BUILD_TEST_ARGS\"\nprintf 'fixture' > \"$BUILD_TEST_OUTPUT\"\nchmod +x \"$BUILD_TEST_OUTPUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(compiler), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "gitvault")
	args := filepath.Join(dir, "args")
	// Reproduce the developer environment's group-writable default without
	// changing the Go test process's global umask.
	cmd := exec.Command("sh", "-c", "umask 002; exec sh scripts/build.sh -platform linux/amd64")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "BUILD_TEST_OUTPUT="+output, "BUILD_TEST_ARGS="+args)
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wrapper: %v %s", err, body)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("helper executable mode=%o; want 755", info.Mode().Perm())
	}
	body, err := os.ReadFile(args)
	if err != nil || strings.TrimSpace(string(body)) != "tool\nmygo\nbuild\n-platform\nlinux/amd64" {
		t.Fatalf("build arguments=%q err=%v", body, err)
	}
}
