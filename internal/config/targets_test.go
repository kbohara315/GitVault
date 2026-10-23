package config_test

import (
	"strings"
	"testing"

	"gitvault/internal/config"
)

func testTargetIdentityTLSPrefixAndCollision(t *testing.T) {
	target := config.Target{ID: "target-1", Provider: "s3", Bucket: "synthetic-bucket", Region: "us-east-1", Prefix: "gitvault/", Credential: config.Credential{Reference: "synthetic-handle", Generation: 1}}
	for name, change := range map[string]func(*config.Target){
		"unknown_provider":     func(t *config.Target) { t.Provider = "other" },
		"bad_id":               func(t *config.Target) { t.ID = "target/escape" },
		"bad_bucket":           func(t *config.Target) { t.Bucket = "Bucket" },
		"ip_bucket":            func(t *config.Target) { t.Bucket = "192.168.1.1" },
		"reserved_bucket":      func(t *config.Target) { t.Bucket = "xn--reserved" },
		"r2_region":            func(t *config.Target) { t.Provider = "r2" },
		"r2_no_endpoint":       func(t *config.Target) { t.Provider = "r2"; t.Region = "auto" },
		"http":                 func(t *config.Target) { v := "http://s3.us-east-1.amazonaws.com"; t.Endpoint = &v },
		"endpoint_credentials": func(t *config.Target) { v := "https://synthetic-user@s3.us-east-1.amazonaws.com"; t.Endpoint = &v },
		"endpoint_query":       func(t *config.Target) { v := "https://s3.us-east-1.amazonaws.com?signature=synthetic"; t.Endpoint = &v },
		"endpoint_fragment":    func(t *config.Target) { v := "https://s3.us-east-1.amazonaws.com#fragment"; t.Endpoint = &v },
		"endpoint_path":        func(t *config.Target) { v := "https://s3.us-east-1.amazonaws.com/path"; t.Endpoint = &v },
		"unexpected_host":      func(t *config.Target) { v := "https://arbitrary.invalid"; t.Endpoint = &v },
		"prefix_leading_slash": func(t *config.Target) { t.Prefix = "/gitvault/" },
		"prefix_empty_segment": func(t *config.Target) { t.Prefix = "gitvault//" },
		"prefix_dot_segment":   func(t *config.Target) { t.Prefix = "gitvault/../" },
		"prefix_no_slash":      func(t *config.Target) { t.Prefix = "gitvault" },
		"prefix_backslash":     func(t *config.Target) { t.Prefix = "gitvault\\/" },
		"credential_url":       func(t *config.Target) { t.Credential.Reference = "https://synthetic.invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			c := config.Default()
			v := target
			change(&v)
			c.Cloud.Targets = []config.Target{v}
			if _, err := config.Normalize(c); err == nil {
				t.Fatal("invalid target accepted")
			}
		})
	}
	c := config.Default()
	c.Cloud.Targets = []config.Target{target}
	id := target.ID
	c.Cloud.ActiveTargetID = &id
	c.Cloud.Enabled = true
	if _, err := config.Normalize(c); err != nil {
		t.Fatal(err)
	}
	r2 := target
	r2.ID = "target-r2"
	r2.Provider = "r2"
	r2.Region = "auto"
	endpoint := "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	r2.Endpoint = &endpoint
	c.Cloud.Targets = append(c.Cloud.Targets, r2)
	if _, err := config.Normalize(c); err != nil {
		t.Fatal("valid r2 target:", err)
	}
	overlapping := target
	overlapping.ID = "target-overlap"
	overlapping.Prefix = "gitvault/nested/"
	c.Cloud.Targets = append(c.Cloud.Targets, overlapping)
	if _, err := config.Normalize(c); err == nil {
		t.Fatal("overlapping target destination accepted")
	}
	c = config.Default()
	for i := 0; i < 10000; i++ {
		c.Selection.RepositoryIDs = append(c.Selection.RepositoryIDs, strings.Repeat("a", 250)+string(rune(0x1000+i)))
	}
	if _, err := config.Normalize(c); err != nil {
		t.Fatal("maximum selection:", err)
	}
	c.Selection.RepositoryIDs = append(c.Selection.RepositoryIDs, "excess")
	if _, err := config.Normalize(c); err == nil {
		t.Fatal("unbounded selection")
	}
}
