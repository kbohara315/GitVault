package config_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitvault/internal/config"
)

func testEveryFiniteConfigBound(t *testing.T) {
	type budget struct {
		name   string
		lo, hi int64
		set    func(*config.Config, int64)
	}
	budgets := []budget{
		{"repository_concurrency", 1, 8, func(c *config.Config, n int64) { c.Backup.RepositoryConcurrency = int(n) }},
		{"manifest_bytes", 64 << 10, 16 << 20, func(c *config.Config, n int64) { c.Limits.ManifestBytes = n }},
		{"reference_count", 1, 1000000, func(c *config.Config, n int64) { c.Limits.ReferenceCount = int(n) }},
		{"reference_name_bytes", 256, 16384, func(c *config.Config, n int64) { c.Limits.ReferenceNameBytes = int(n) }},
		{"machine_output_bytes", 1 << 20, 256 << 20, func(c *config.Config, n int64) { c.Limits.MachineOutputBytes = n }},
		{"diagnostic_bytes", 4 << 10, 1 << 20, func(c *config.Config, n int64) { c.Limits.DiagnosticBytes = n }},
		{"bundle_bytes", 1 << 20, 1 << 40, func(c *config.Config, n int64) { c.Limits.BundleBytes = n }},
		{"verification_scratch_bytes", 16 << 20, 4 << 40, func(c *config.Config, n int64) { c.Limits.VerificationScratchBytes = n; c.Limits.BundleBytes = 1 << 20 }},
		{"discovery_repositories", 1, 100000, func(c *config.Config, n int64) { c.Limits.DiscoveryRepositories = int(n) }},
		{"http_response_bytes", 1 << 20, 64 << 20, func(c *config.Config, n int64) { c.Limits.HTTPResponseBytes = n }},
		{"event_capacity", 32, 4096, func(c *config.Config, n int64) { c.Limits.EventCapacity = int(n) }},
		{"ui_batch_size", 1, 256, func(c *config.Config, n int64) { c.Limits.UIBatchSize = int(n) }},
		{"log_file_bytes", 1 << 20, 64 << 20, func(c *config.Config, n int64) { c.Limits.LogFileBytes = n }},
		{"log_files", 1, 16, func(c *config.Config, n int64) { c.Limits.LogFiles = int(n) }},
		{"rate_limit_wait", 0, int64(time.Hour), func(c *config.Config, n int64) { c.GitHub.RateLimitWait = config.Duration(n) }},
		{"git_timeout", int64(time.Minute), int64(24 * time.Hour), func(c *config.Config, n int64) { c.Timeouts.Git = config.Duration(n) }},
		{"http_timeout", int64(5 * time.Second), int64(300 * time.Second), func(c *config.Config, n int64) { c.Timeouts.HTTPRequest = config.Duration(n) }},
		{"transfer_timeout", int64(time.Minute), int64(24 * time.Hour), func(c *config.Config, n int64) { c.Timeouts.Transfer = config.Duration(n) }},
		{"cleanup_timeout", int64(5 * time.Second), int64(120 * time.Second), func(c *config.Config, n int64) { c.Timeouts.Cleanup = config.Duration(n) }},
		{"retry_attempts", 1, 8, func(c *config.Config, n int64) { c.Retry.MaxAttempts = int(n) }},
		{"initial_backoff", int64(100 * time.Millisecond), int64(10 * time.Second), func(c *config.Config, n int64) { c.Retry.InitialBackoff = config.Duration(n) }},
		{"max_backoff", int64(time.Second), int64(120 * time.Second), func(c *config.Config, n int64) { c.Retry.MaxBackoff = config.Duration(n) }},
		{"upload_concurrency", 1, 4, func(c *config.Config, n int64) { c.Cloud.UploadConcurrency = int(n) }},
		{"part_concurrency", 1, 4, func(c *config.Config, n int64) { c.Cloud.PartConcurrency = int(n) }},
		{"part_size_bytes", 8 << 20, 128 << 20, func(c *config.Config, n int64) { c.Cloud.PartSizeBytes = n }},
		{"multipart_threshold", 8 << 20, 256 << 20, func(c *config.Config, n int64) { c.Cloud.MultipartThresholdBytes = n }},
		{"keep_daily", 0, 365, func(c *config.Config, n int64) { c.Retention.KeepDaily = int(n) }},
		{"keep_weekly", 0, 104, func(c *config.Config, n int64) { c.Retention.KeepWeekly = int(n) }},
		{"keep_monthly", 0, 120, func(c *config.Config, n int64) { c.Retention.KeepMonthly = int(n) }},
	}
	for _, b := range budgets {
		t.Run(b.name, func(t *testing.T) {
			for _, sample := range []struct {
				n     int64
				valid bool
			}{{b.lo, true}, {b.hi, true}, {b.lo - 1, false}, {b.hi + 1, false}} {
				c := config.Default()
				c.Limits.VerificationScratchBytes = 4 << 40
				c.Limits.EventCapacity = 4096
				c.Limits.UIBatchSize = 1
				c.Timeouts.Transfer = config.Duration(24 * time.Hour)
				c.Retry.MaxBackoff = config.Duration(120 * time.Second)
				c.Cloud.PartConcurrency = 1
				c.Cloud.UploadConcurrency = 1
				c.Cloud.PartSizeBytes = 8 << 20
				c.Cloud.MultipartThresholdBytes = 256 << 20
				b.set(&c, sample.n)
				_, err := config.Normalize(c)
				if (err == nil) != sample.valid {
					t.Fatalf("value %d valid=%v error=%v", sample.n, sample.valid, err)
				}
			}
		})
	}
}

func testStrictConfigJSON(t *testing.T) {
	invalid := []string{
		`{"schema_version":1,"backup":{"root":null,"root":null}}`,
		`{"schema_version":1,"Backup":{}}`,
		`{"schema_version":1,"backup":{"repository_concurrency":null}}`,
		`{"schema_version":1,"backup":null}`,
		`{"schema_version":1,"selection":{"repository_ids":null}}`,
		`{"schema_version":1,"limits":{"reference_count":1.5}}`,
		`{"schema_version":1,"limits":{"reference_count":1e3}}`,
		`{"schema_version":1,"limits":{"bundle_bytes":9223372036854775808}}`,
		`{"schema_version":1,"limits":{"bundle_bytes":-1}}`,
		`{"schema_version":1,"github":{"credential":{"reference":"key","generation":0}}}`,
		`{"schema_version":1,"github":{"credential":{"reference":"key","generation":2147483648}}}`,
		`{"schema_version":1,"github":{"credential":{"reference":"key","generation":1,"token":"synthetic"}}}`,
		`{"schema_version":1,"timeouts":{"git":"1.5h"}}`,
		`{"schema_version":1,"timeouts":{"git":"-1h"}}`,
		`{"schema_version":1,"timeouts":{"git":"999999999999999999h"}}`,
		`{"schema_version":1,"selection":{"repository_ids":["\ud800"]}}`,
		`{"schema_version":1,"selection":{"repository_ids":["\udc00"]}}`,
		`{"schema_version":1,"selection":{"repository_ids":["\ud800\u0041"]}}`,
	}
	for _, b := range invalid {
		if _, err := config.Decode(strings.NewReader(b)); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
	if _, err := config.Decode(strings.NewReader(`{"schema_version":1,"selection":{"repository_ids":["\ud83d\ude80"]}}`)); err != nil {
		t.Fatal("rejected valid Unicode pair:", err)
	}
	if _, err := config.Decode(bytes.NewReader(bytes.Repeat([]byte(" "), config.MaxJSONBytes+1))); err == nil {
		t.Fatal("accepted excessive JSON")
	}
	c, err := config.Decode(strings.NewReader(`{"schema_version":1,"retry":{"initial_backoff":"1000ms"}}`))
	if err != nil || c.Retry.InitialBackoff.Value() != time.Second {
		t.Fatalf("duration normalization: %v", err)
	}
	data, err := json.Marshal(c)
	if err != nil || !bytes.Contains(data, []byte(`"initial_backoff":"1s"`)) {
		t.Fatalf("noncanonical duration %s %v", data, err)
	}
}

func testConfigCrossFieldsAndExplicitSelection(t *testing.T) {
	for name, change := range map[string]func(*config.Config){
		"incremental":         func(c *config.Config) { c.Backup.SnapshotKind = "incremental" },
		"relative_root":       func(c *config.Config) { p := "relative"; c.Backup.Root = &p },
		"duplicate_selection": func(c *config.Config) { c.Selection.RepositoryIDs = []string{"id", "id"} },
		"selection_exceeds_discovery": func(c *config.Config) {
			c.Limits.DiscoveryRepositories = 1
			c.Selection.RepositoryIDs = []string{"id", "other"}
		},
		"ui_batch": func(c *config.Config) { c.Limits.EventCapacity = 32; c.Limits.UIBatchSize = 64 },
		"scratch":  func(c *config.Config) { c.Limits.VerificationScratchBytes = 16 << 20 },
		"retry_order": func(c *config.Config) {
			c.Retry.InitialBackoff = config.Duration(10 * time.Second)
			c.Retry.MaxBackoff = config.Duration(time.Second)
		},
		"http_exceeds_transfer": func(c *config.Config) {
			c.Timeouts.Transfer = config.Duration(time.Minute)
			c.Timeouts.HTTPRequest = config.Duration(120 * time.Second)
		},
		"cleanup_exceeds_transfer": func(c *config.Config) {
			c.Timeouts.Transfer = config.Duration(time.Minute)
			c.Timeouts.Cleanup = config.Duration(120 * time.Second)
		},
		"global_parts":             func(c *config.Config) { c.Cloud.UploadConcurrency = 4; c.Cloud.PartConcurrency = 4 },
		"part_alignment":           func(c *config.Config) { c.Cloud.PartSizeBytes++ },
		"threshold":                func(c *config.Config) { c.Cloud.MultipartThresholdBytes = 8 << 20 },
		"cloud_no_target":          func(c *config.Config) { c.Cloud.Enabled = true },
		"unknown_retention":        func(c *config.Config) { c.Retention.Mode = "automatic" },
		"schedule_no_confirmation": func(c *config.Config) { c.Schedule.Enabled = true },
		"timezone_local":           func(c *config.Config) { z := "Local"; c.Schedule.Timezone = &z },
		"timezone_unknown":         func(c *config.Config) { z := "Moon/Unknown"; c.Schedule.Timezone = &z },
		"localtime_nonexact":       func(c *config.Config) { c.Schedule.LocalTime = "2:00" },
		"localtime_invalid":        func(c *config.Config) { c.Schedule.LocalTime = "24:00" },
		"daily_with_weekday":       func(c *config.Config) { d := 1; c.Schedule.Weekday = &d },
		"weekly_without_weekday":   func(c *config.Config) { c.Schedule.Frequency = "weekly" },
		"monthly_day_zero":         func(c *config.Config) { d := 0; c.Schedule.Frequency = "monthly"; c.Schedule.Monthday = &d },
		"monthly_day_32":           func(c *config.Config) { d := 32; c.Schedule.Frequency = "monthly"; c.Schedule.Monthday = &d },
		"automatic_catchup":        func(c *config.Config) { c.Schedule.CatchUp = "automatic" },
	} {
		t.Run(name, func(t *testing.T) {
			c := config.Default()
			change(&c)
			if _, err := config.Normalize(c); err == nil {
				t.Fatal("invalid cross-field combination accepted")
			}
		})
	}
	c := config.Default()
	root := "/synthetic/managed"
	z := "Asia/Kathmandu"
	c.Backup.Root = &root
	c.Selection.RepositoryIDs = []string{"github:101"}
	c.GitHub.Credential = &config.Credential{Reference: "synthetic-handle", Generation: 1}
	c.Schedule.Timezone = &z
	c.Schedule.Enabled = true
	c.Retention.Mode = "explicit_delete"
	for _, d := range []int{1, 28, 29, 30, 31} {
		c.Schedule.Frequency = "monthly"
		c.Schedule.Monthday = &d
		if _, err := config.Normalize(c); err != nil {
			t.Fatalf("valid monthly day %d: %v", d, err)
		}
	}
	for _, d := range []int{1, 7} {
		c.Schedule.Frequency = "weekly"
		c.Schedule.Monthday = nil
		c.Schedule.Weekday = &d
		if _, err := config.Normalize(c); err != nil {
			t.Fatalf("valid weekday %d: %v", d, err)
		}
	}
	c.Schedule.Frequency = "daily"
	c.Schedule.Weekday = nil
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := config.Import(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if imported.Schedule.Enabled || imported.Cloud.Enabled || imported.Retention.Mode != "preview_only" {
		t.Fatal("import retained execution policy")
	}
}
