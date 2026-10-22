// Package quality verifies evidence for the implementation's behavioral gates.
// It is development tooling, not application or release authorization.
package quality

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const maxEvidenceBytes = 64 << 20

type Report struct {
	Package string   `json:"package"`
	Passed  []string `json:"passed"`
}

// VerifyEvidence requires actual run/pass events for every named test, a
// successful single-package outcome, and no skipped/failed required descendants.
// It intentionally does not infer behavioral correctness from a passing name;
// callers must also retain fixture assertions and independent review evidence.
func VerifyEvidence(r io.Reader, required []string) (Report, error) {
	var report Report
	if len(required) == 0 {
		return report, fmt.Errorf("no required tests declared")
	}
	want := make(map[string]bool, len(required))
	for _, name := range required {
		if !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, "/\n\r\x00") || want[name] {
			return report, fmt.Errorf("invalid or duplicate required test %q", name)
		}
		want[name] = true
	}
	ran, passed := map[string]bool{}, map[string]bool{}
	packagePassed := false
	limited := &io.LimitedReader{R: r, N: maxEvidenceBytes + 1}
	scan := bufio.NewScanner(limited)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var event struct{ Action, Package, Test string }
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			return report, fmt.Errorf("invalid Go event: %w", err)
		}
		if event.Package == "" || event.Action == "" {
			return report, fmt.Errorf("event missing package/action")
		}
		if report.Package == "" {
			report.Package = event.Package
		}
		if event.Package != report.Package {
			return report, fmt.Errorf("mixed-package evidence")
		}
		if event.Test == "" {
			if event.Action == "fail" || event.Action == "skip" {
				return report, fmt.Errorf("package %s did not pass", event.Package)
			}
			if event.Action == "pass" {
				packagePassed = true
			}
			continue
		}
		parent := strings.SplitN(event.Test, "/", 2)[0]
		if !want[parent] {
			continue
		}
		if event.Action == "skip" || event.Action == "fail" {
			return report, fmt.Errorf("required test/subcase %s %s", event.Test, event.Action)
		}
		if event.Test == parent {
			if event.Action == "run" {
				ran[parent] = true
			}
			if event.Action == "pass" {
				if !ran[parent] {
					return report, fmt.Errorf("test %s passed without execution evidence", parent)
				}
				passed[parent] = true
			}
		}
	}
	if err := scan.Err(); err != nil {
		return report, fmt.Errorf("read Go events: %w", err)
	}
	if limited.N <= 0 {
		return report, fmt.Errorf("Go evidence exceeds limit")
	}
	if !packagePassed {
		return report, fmt.Errorf("missing successful package outcome")
	}
	for name := range want {
		if !ran[name] || !passed[name] {
			return report, fmt.Errorf("required test %s did not execute and pass", name)
		}
		report.Passed = append(report.Passed, name)
	}
	sort.Strings(report.Passed)
	return report, nil
}
