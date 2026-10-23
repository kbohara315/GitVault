package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gitvault/internal/config"
)

func TestConfigBoundsAndMigration(t *testing.T) {
	d := config.Default()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Backup.RepositoryConcurrency != 2 || got.Limits.BundleBytes != 107374182400 || got.Schedule.LocalTime != "02:00" {
		t.Fatalf("incorrect defaults: %+v", got)
	}
	doc, err := os.ReadFile("../../docs/CONFIGURATION.md")
	if err != nil {
		t.Fatal(err)
	}
	example := strings.Split(strings.Split(string(doc), "```json\n")[1], "```")[0]
	exampleConfig, err := config.Decode(strings.NewReader(example))
	if err != nil {
		t.Fatal(err)
	}
	exampleJSON, _ := json.Marshal(exampleConfig)
	if !bytes.Equal(b, exampleJSON) {
		t.Fatalf("defaults differ from documented example\n%s\n%s", b, exampleJSON)
	}
	for _, bad := range []string{`{}`, `{"schema_version":0}`, `{"schema_version":2}`, `{"schema_version":1,"schema_version":1}`, `{"schema_version":1,"insecure":true}`, `{"schema_version":1,"backup":{"repository_concurrency":0}}`, `{"schema_version":1} {}`, string([]byte{'{', '"', 0xff, '"', ':', '1', '}'})} {
		if _, err := config.Decode(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := config.Decode(strings.NewReader(`{"schema_version":1}`)); err != nil {
		t.Fatalf("schema-one partial document defaults: %v", err)
	}
	t.Run("every_finite_bound", testEveryFiniteConfigBound)
	t.Run("strict_encoding_and_schema", testStrictConfigJSON)
	t.Run("cross_fields_and_schedule", testConfigCrossFieldsAndExplicitSelection)
	t.Run("targets_and_selection", testTargetIdentityTLSPrefixAndCollision)
}
