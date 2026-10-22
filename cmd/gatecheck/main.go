// gatecheck executes named task gates without accepting empty regex matches.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gitvault/internal/quality"
)

func main() {
	taskID := flag.String("task", "", "task ID, e.g. I01")
	plan := flag.String("plan", "docs/implementation/tasks.json", "trusted repository gate specification")
	timeout := flag.Duration("timeout", 10*time.Minute, "timeout per gate")
	flag.Parse()
	if err := run(*plan, *taskID, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, id string, timeout time.Duration) error {
	if id == "" || timeout <= 0 || timeout > time.Hour {
		return fmt.Errorf("provide -task and a finite timeout no larger than 1h")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var spec struct {
		Tasks []struct {
			ID    string `json:"id"`
			Gates []struct {
				Name, Package string
				Tests         []string
			} `json:"test_gates"`
		}
	}
	if err = json.NewDecoder(f).Decode(&spec); err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	for _, task := range spec.Tasks {
		if task.ID != id {
			continue
		}
		if len(task.Gates) == 0 {
			return fmt.Errorf("%s has no declared gates", id)
		}
		for _, gate := range task.Gates {
			if !strings.HasPrefix(gate.Package, "./internal/") || strings.Contains(gate.Package, "..") || len(gate.Tests) == 0 {
				return fmt.Errorf("invalid package/gate in %s", id)
			}
			quoted := make([]string, len(gate.Tests))
			for i, n := range gate.Tests {
				quoted[i] = regexp.QuoteMeta(n)
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			cmd := exec.CommandContext(ctx, "go", "test", gate.Package, "-run", "^("+strings.Join(quoted, "|")+")$", "-count=1", "-json")
			configureProcess(cmd)
			cmd.WaitDelay = 2 * time.Second
			cmd.Stderr = os.Stderr
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				cancel()
				return err
			}
			if err = cmd.Start(); err != nil {
				cancel()
				return err
			}
			// A test descendant may keep the inherited pipe open after go dies.
			// Bound our own read independently of exec's child cancellation.
			stopClosing := context.AfterFunc(ctx, func() { _ = pipe.Close() })
			report, verifyErr := quality.VerifyEvidence(pipe, gate.Tests)
			if verifyErr != nil {
				cancel()
				_ = pipe.Close()
			}
			waitErr := cmd.Wait()
			stopClosing()
			cancel()
			if verifyErr != nil {
				return fmt.Errorf("%s/%s: %w", id, gate.Name, verifyErr)
			}
			if waitErr != nil {
				return fmt.Errorf("%s/%s: go test: %w", id, gate.Name, waitErr)
			}
			if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown task %q", id)
}
