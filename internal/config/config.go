// Package config owns the non-secret, versioned settings contract. It never
// contacts a filesystem root, secret service, or provider to validate settings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const SchemaVersion = 1
const MaxJSONBytes = 4 << 20

// Duration is a checked integral duration, serialized in ms, s, m, or h.
type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }
func (d Duration) MarshalJSON() ([]byte, error) {
	if int64(d)%int64(time.Millisecond) != 0 {
		return nil, errors.New("duration must be a whole millisecond")
	}
	n := int64(d)
	unit := "ms"
	div := int64(time.Millisecond)
	for _, u := range []struct {
		s string
		n time.Duration
	}{{"h", time.Hour}, {"m", time.Minute}, {"s", time.Second}} {
		if n != 0 && n%int64(u.n) == 0 {
			unit = u.s
			div = int64(u.n)
			break
		}
	}
	return json.Marshal(strconv.FormatInt(n/div, 10) + unit)
}

var durationPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(ms|s|m|h)$`)

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if !durationPattern.MatchString(s) {
		return errors.New("duration requires integer ms, s, m, or h")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

type Credential struct {
	Reference  string `json:"reference"`
	Generation int64  `json:"generation"`
}
type Backup struct {
	Root                  *string `json:"root"`
	RepositoryConcurrency int     `json:"repository_concurrency"`
	SnapshotKind          string  `json:"snapshot_kind"`
	KeepMirror            bool    `json:"keep_mirror"`
}
type Selection struct {
	RepositoryIDs []string `json:"repository_ids"`
}
type GitHub struct {
	Credential    *Credential `json:"credential"`
	RateLimitWait Duration    `json:"rate_limit_wait"`
}
type Limits struct {
	ManifestBytes            int64 `json:"manifest_bytes"`
	ReferenceCount           int   `json:"reference_count"`
	ReferenceNameBytes       int   `json:"reference_name_bytes"`
	MachineOutputBytes       int64 `json:"machine_output_bytes"`
	DiagnosticBytes          int64 `json:"diagnostic_bytes"`
	BundleBytes              int64 `json:"bundle_bytes"`
	VerificationScratchBytes int64 `json:"verification_scratch_bytes"`
	DiscoveryRepositories    int   `json:"discovery_repositories"`
	HTTPResponseBytes        int64 `json:"http_response_bytes"`
	EventCapacity            int   `json:"event_capacity"`
	UIBatchSize              int   `json:"ui_batch_size"`
	LogFileBytes             int64 `json:"log_file_bytes"`
	LogFiles                 int   `json:"log_files"`
}
type Timeouts struct {
	Git         Duration `json:"git"`
	HTTPRequest Duration `json:"http_request"`
	Transfer    Duration `json:"transfer"`
	Cleanup     Duration `json:"cleanup"`
}
type Retry struct {
	MaxAttempts    int      `json:"max_attempts"`
	InitialBackoff Duration `json:"initial_backoff"`
	MaxBackoff     Duration `json:"max_backoff"`
}
type Target struct {
	ID         string     `json:"id"`
	Provider   string     `json:"provider"`
	Bucket     string     `json:"bucket"`
	Region     string     `json:"region"`
	Endpoint   *string    `json:"endpoint"`
	Prefix     string     `json:"prefix"`
	Credential Credential `json:"credential"`
}
type Cloud struct {
	Enabled                 bool     `json:"enabled"`
	ActiveTargetID          *string  `json:"active_target_id"`
	Targets                 []Target `json:"targets"`
	UploadConcurrency       int      `json:"upload_concurrency"`
	PartConcurrency         int      `json:"part_concurrency"`
	PartSizeBytes           int64    `json:"part_size_bytes"`
	MultipartThresholdBytes int64    `json:"multipart_threshold_bytes"`
	ReadBack                bool     `json:"read_back"`
}
type Retention struct {
	Mode        string `json:"mode"`
	KeepDaily   int    `json:"keep_daily"`
	KeepWeekly  int    `json:"keep_weekly"`
	KeepMonthly int    `json:"keep_monthly"`
}
type Schedule struct {
	Enabled   bool    `json:"enabled"`
	Timezone  *string `json:"timezone"`
	LocalTime string  `json:"local_time"`
	Frequency string  `json:"frequency"`
	Weekday   *int    `json:"weekday"`
	Monthday  *int    `json:"monthday"`
	CatchUp   string  `json:"catch_up"`
}
type UI struct {
	CloseToTray bool `json:"close_to_tray"`
}
type Config struct {
	SchemaVersion int       `json:"schema_version"`
	Backup        Backup    `json:"backup"`
	Selection     Selection `json:"selection"`
	GitHub        GitHub    `json:"github"`
	Limits        Limits    `json:"limits"`
	Timeouts      Timeouts  `json:"timeouts"`
	Retry         Retry     `json:"retry"`
	Cloud         Cloud     `json:"cloud"`
	Retention     Retention `json:"retention"`
	Schedule      Schedule  `json:"schedule"`
	UI            UI        `json:"ui"`
}

func Default() Config {
	return Config{
		SchemaVersion: 1, Backup: Backup{RepositoryConcurrency: 2, SnapshotKind: "full", KeepMirror: true}, Selection: Selection{RepositoryIDs: []string{}}, GitHub: GitHub{RateLimitWait: Duration(15 * time.Minute)},
		Limits:   Limits{4 << 20, 100000, 4096, 32 << 20, 64 << 10, 100 << 30, 250 << 30, 10000, 16 << 20, 256, 64, 8 << 20, 4},
		Timeouts: Timeouts{Duration(time.Hour), Duration(time.Minute), Duration(4 * time.Hour), Duration(30 * time.Second)}, Retry: Retry{3, Duration(time.Second), Duration(30 * time.Second)},
		Cloud: Cloud{Targets: []Target{}, UploadConcurrency: 1, PartConcurrency: 2, PartSizeBytes: 16 << 20, MultipartThresholdBytes: 32 << 20}, Retention: Retention{"preview_only", 7, 4, 6}, Schedule: Schedule{LocalTime: "02:00", Frequency: "daily", CatchUp: "offer_once"},
	}
}

// Decode applies schema-one defaults to omitted fields. A schema version is
// mandatory; no pre-schema or future migration is currently supported.
func Decode(r io.Reader) (Config, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxJSONBytes+1))
	if err != nil {
		return Config{}, err
	}
	if len(b) > MaxJSONBytes {
		return Config{}, errors.New("configuration exceeds JSON budget")
	}
	if !utf8.Valid(b) || !validEscapes(b) {
		return Config{}, errors.New("invalid Unicode encoding")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err = checkTokens(dec); err != nil {
		return Config{}, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return Config{}, errors.New("trailing JSON")
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(b, &raw); err != nil {
		return Config{}, err
	}
	if _, ok := raw["schema_version"]; !ok {
		return Config{}, errors.New("schema_version required; migration unsupported")
	}
	if err = checkShape(b, reflect.TypeOf(Config{})); err != nil {
		return Config{}, err
	}
	c := Default()
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return Config{}, err
	}
	return Normalize(c)
}
func checkTokens(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[s] = true
			if e = checkTokens(d); e != nil {
				return e
			}
		}
		_, err = d.Token()
	case json.Delim('['):
		for d.More() {
			if err = checkTokens(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}
func checkShape(b []byte, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(b, []byte("null")) {
			return nil
		}
		return checkShape(b, t.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return errors.New("null is only valid for nullable fields")
	}
	if t.Kind() == reflect.Struct {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[f.Tag.Get("json")] = f.Type
		}
		for k, v := range m {
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("unknown field %q", k)
			}
			if err := checkShape(v, ft); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	} else if t.Kind() == reflect.Slice {
		var a []json.RawMessage
		if err := json.Unmarshal(b, &a); err != nil {
			return err
		}
		for _, v := range a {
			if err := checkShape(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// encoding/json deliberately replaces lone UTF-16 surrogates. Settings instead
// reject them, before the standard decoder can erase the malformed evidence.
func validEscapes(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		i++
		for i < len(b) && b[i] != '"' {
			if b[i] == '\\' {
				i++
				if i >= len(b) {
					return false
				}
				if b[i] == 'u' {
					if i+4 >= len(b) {
						return false
					}
					n, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
					if e != nil {
						return false
					}
					i += 4
					if n >= 0xD800 && n <= 0xDBFF {
						if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
							return false
						}
						lo, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
						if e != nil || lo < 0xDC00 || lo > 0xDFFF {
							return false
						}
						i += 6
					} else if n >= 0xDC00 && n <= 0xDFFF {
						return false
					}
				}
			}
			i++
		}
	}
	return true
}

// Clone gives the caller exclusive ownership of every pointer and slice.
func (c Config) Clone() Config {
	if c.Backup.Root != nil {
		v := *c.Backup.Root
		c.Backup.Root = &v
	}
	if c.Selection.RepositoryIDs != nil {
		c.Selection.RepositoryIDs = append([]string{}, c.Selection.RepositoryIDs...)
	}
	if c.GitHub.Credential != nil {
		v := *c.GitHub.Credential
		c.GitHub.Credential = &v
	}
	if c.Cloud.ActiveTargetID != nil {
		v := *c.Cloud.ActiveTargetID
		c.Cloud.ActiveTargetID = &v
	}
	if c.Cloud.Targets != nil {
		c.Cloud.Targets = append([]Target{}, c.Cloud.Targets...)
		for i := range c.Cloud.Targets {
			if c.Cloud.Targets[i].Endpoint != nil {
				v := *c.Cloud.Targets[i].Endpoint
				c.Cloud.Targets[i].Endpoint = &v
			}
		}
	}
	if c.Schedule.Timezone != nil {
		v := *c.Schedule.Timezone
		c.Schedule.Timezone = &v
	}
	if c.Schedule.Weekday != nil {
		v := *c.Schedule.Weekday
		c.Schedule.Weekday = &v
	}
	if c.Schedule.Monthday != nil {
		v := *c.Schedule.Monthday
		c.Schedule.Monthday = &v
	}
	return c
}

// Revision exposes only copies; neither settings edits nor callers can mutate a
// previously captured effective configuration.
type Revision struct {
	id        int64
	createdAt time.Time
	values    Config
}

func NewRevision(id int64, createdAt time.Time, c Config) (Revision, error) {
	n, err := Normalize(c)
	if err != nil {
		return Revision{}, err
	}
	if id < 1 {
		return Revision{}, errors.New("invalid revision ID")
	}
	return Revision{id, createdAt.UTC(), n}, nil
}
func (r Revision) ID() int64            { return r.id }
func (r Revision) CreatedAt() time.Time { return r.createdAt }
func (r Revision) Config() Config       { return r.values.Clone() }

var opaque = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var regionPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)
var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9_/-]+/$`)

func validCredential(c Credential) bool {
	return len(c.Reference) >= 1 && len(c.Reference) <= 128 && opaque.MatchString(c.Reference) && c.Generation >= 1 && c.Generation <= 2147483647
}
func Normalize(c Config) (Config, error) {
	fail := func(s string) (Config, error) { return Config{}, errors.New(s) }
	if c.SchemaVersion != 1 {
		return fail("unsupported configuration schema; migration unsupported")
	}
	if c.Backup.Root != nil {
		p := *c.Backup.Root
		if !utf8.ValidString(p) || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fail("root must be a clean absolute local path")
		}
	}
	if c.Backup.SnapshotKind != "full" {
		return fail("only full snapshots are supported")
	}
	type bound struct {
		name      string
		v, lo, hi int64
	}
	bounds := []bound{
		{"repository_concurrency", int64(c.Backup.RepositoryConcurrency), 1, 8}, {"manifest_bytes", c.Limits.ManifestBytes, 64 << 10, 16 << 20}, {"reference_count", int64(c.Limits.ReferenceCount), 1, 1000000}, {"reference_name_bytes", int64(c.Limits.ReferenceNameBytes), 256, 16384}, {"machine_output_bytes", c.Limits.MachineOutputBytes, 1 << 20, 256 << 20}, {"diagnostic_bytes", c.Limits.DiagnosticBytes, 4 << 10, 1 << 20}, {"bundle_bytes", c.Limits.BundleBytes, 1 << 20, 1 << 40}, {"verification_scratch_bytes", c.Limits.VerificationScratchBytes, 16 << 20, 4 << 40}, {"discovery_repositories", int64(c.Limits.DiscoveryRepositories), 1, 100000}, {"http_response_bytes", c.Limits.HTTPResponseBytes, 1 << 20, 64 << 20}, {"event_capacity", int64(c.Limits.EventCapacity), 32, 4096}, {"ui_batch_size", int64(c.Limits.UIBatchSize), 1, 256}, {"log_file_bytes", c.Limits.LogFileBytes, 1 << 20, 64 << 20}, {"log_files", int64(c.Limits.LogFiles), 1, 16},
		{"rate_limit_wait", int64(c.GitHub.RateLimitWait), 0, int64(time.Hour)}, {"git", int64(c.Timeouts.Git), int64(time.Minute), int64(24 * time.Hour)}, {"http_request", int64(c.Timeouts.HTTPRequest), int64(5 * time.Second), int64(300 * time.Second)}, {"transfer", int64(c.Timeouts.Transfer), int64(time.Minute), int64(24 * time.Hour)}, {"cleanup", int64(c.Timeouts.Cleanup), int64(5 * time.Second), int64(120 * time.Second)}, {"max_attempts", int64(c.Retry.MaxAttempts), 1, 8}, {"initial_backoff", int64(c.Retry.InitialBackoff), int64(100 * time.Millisecond), int64(10 * time.Second)}, {"max_backoff", int64(c.Retry.MaxBackoff), int64(c.Retry.InitialBackoff), int64(120 * time.Second)},
		{"upload_concurrency", int64(c.Cloud.UploadConcurrency), 1, 4}, {"part_concurrency", int64(c.Cloud.PartConcurrency), 1, 4}, {"part_size_bytes", c.Cloud.PartSizeBytes, 8 << 20, 128 << 20}, {"multipart_threshold_bytes", c.Cloud.MultipartThresholdBytes, 8 << 20, 256 << 20}, {"keep_daily", int64(c.Retention.KeepDaily), 0, 365}, {"keep_weekly", int64(c.Retention.KeepWeekly), 0, 104}, {"keep_monthly", int64(c.Retention.KeepMonthly), 0, 120},
	}
	for _, b := range bounds {
		if b.v < b.lo || b.v > b.hi {
			return fail(b.name + " outside finite bounds")
		}
	}
	for _, d := range []Duration{c.GitHub.RateLimitWait, c.Timeouts.Git, c.Timeouts.HTTPRequest, c.Timeouts.Transfer, c.Timeouts.Cleanup, c.Retry.InitialBackoff, c.Retry.MaxBackoff} {
		if int64(d)%int64(time.Millisecond) != 0 {
			return fail("duration must be a whole millisecond")
		}
	}
	if c.Limits.UIBatchSize > c.Limits.EventCapacity || c.Timeouts.HTTPRequest > c.Timeouts.Transfer || c.Timeouts.Cleanup > c.Timeouts.Transfer || c.Limits.VerificationScratchBytes < c.Limits.BundleBytes {
		return fail("inconsistent resource budgets")
	}
	if len(c.Selection.RepositoryIDs) > 10000 || len(c.Selection.RepositoryIDs) > c.Limits.DiscoveryRepositories {
		return fail("selection exceeds discovery or selection limit")
	}
	seen := map[string]bool{}
	for _, id := range c.Selection.RepositoryIDs {
		if len(id) < 1 || len(id) > 256 || !utf8.ValidString(id) || strings.ContainsRune(id, 0) || seen[id] {
			return fail("invalid or duplicate stable repository ID")
		}
		seen[id] = true
	}
	if c.GitHub.Credential != nil && !validCredential(*c.GitHub.Credential) {
		return fail("invalid GitHub credential handle")
	}
	if c.Cloud.UploadConcurrency*c.Cloud.PartConcurrency > 8 || c.Cloud.PartSizeBytes%(1<<20) != 0 || c.Cloud.MultipartThresholdBytes < c.Cloud.PartSizeBytes {
		return fail("invalid cloud part budget")
	}
	if len(c.Cloud.Targets) > 16 {
		return fail("too many cloud targets")
	}
	ids := map[string]bool{}
	destinations := []string{}
	for _, t := range c.Cloud.Targets {
		if len(t.ID) < 1 || len(t.ID) > 64 || !opaque.MatchString(t.ID) || ids[t.ID] {
			return fail("invalid or duplicate target ID")
		}
		ids[t.ID] = true
		if t.Provider != "s3" && t.Provider != "r2" {
			return fail("unknown provider")
		}
		if !validBucket(t.Provider, t.Bucket) {
			return fail("invalid bucket")
		}
		if len(t.Region) < 1 || len(t.Region) > 64 || !regionPattern.MatchString(t.Region) || (t.Provider == "r2" && t.Region != "auto") {
			return fail("invalid provider region")
		}
		endpoint := "https://s3." + t.Region + ".amazonaws.com"
		if t.Endpoint != nil {
			endpoint = *t.Endpoint
			if len(endpoint) > 2048 {
				return fail("endpoint too long")
			}
			u, err := url.Parse(endpoint)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Port() != "" {
				return fail("invalid HTTPS provider endpoint")
			}
			host := strings.ToLower(u.Hostname())
			if t.Provider == "r2" {
				account := strings.TrimSuffix(host, ".r2.cloudflarestorage.com")
				if account == host || len(account) != 32 || strings.Trim(account, "0123456789abcdef") != "" {
					return fail("r2 requires an account endpoint")
				}
			} else if host != "s3.amazonaws.com" && host != "s3."+t.Region+".amazonaws.com" && host != "s3."+t.Region+".amazonaws.com.cn" {
				return fail("unapproved s3 endpoint")
			}
			endpoint = "https://" + host
		} else if t.Provider == "r2" {
			return fail("r2 requires endpoint")
		}
		if len(t.Prefix) < 1 || len(t.Prefix) > 256 || !prefixPattern.MatchString(t.Prefix) || strings.HasPrefix(t.Prefix, "/") || strings.Contains(t.Prefix, "//") {
			return fail("invalid target prefix")
		}
		if !validCredential(t.Credential) {
			return fail("invalid target credential")
		}
		dest := endpoint + "/" + t.Bucket + "/" + t.Prefix
		for _, old := range destinations {
			if strings.HasPrefix(old, dest) || strings.HasPrefix(dest, old) {
				return fail("overlapping cloud target destinations")
			}
		}
		destinations = append(destinations, dest)
	}
	if c.Cloud.ActiveTargetID != nil && !ids[*c.Cloud.ActiveTargetID] {
		return fail("unknown active target")
	}
	if c.Cloud.Enabled && c.Cloud.ActiveTargetID == nil {
		return fail("enabled cloud requires active target; consent and handle resolution checked at admission")
	}
	if c.Retention.Mode != "preview_only" && c.Retention.Mode != "explicit_delete" {
		return fail("unsupported retention mode")
	}
	if c.Schedule.CatchUp != "offer_once" {
		return fail("unsupported catch-up policy")
	}
	if len(c.Schedule.LocalTime) != 5 || c.Schedule.LocalTime[2] != ':' {
		return fail("local time requires HH:MM")
	}
	parsed, err := time.Parse("15:04", c.Schedule.LocalTime)
	if err != nil || parsed.Format("15:04") != c.Schedule.LocalTime {
		return fail("invalid local time")
	}
	if c.Schedule.Timezone != nil {
		z := *c.Schedule.Timezone
		if len(z) < 1 || len(z) > 128 || z == "Local" || strings.HasPrefix(z, "/") || strings.Contains(z, "..") || strings.ContainsRune(z, 0) {
			return fail("invalid IANA timezone")
		}
		if _, err := time.LoadLocation(z); err != nil {
			return fail("unknown IANA timezone")
		}
	}
	switch c.Schedule.Frequency {
	case "daily":
		if c.Schedule.Weekday != nil || c.Schedule.Monthday != nil {
			return fail("daily requires null day selectors")
		}
	case "weekly":
		if c.Schedule.Weekday == nil || *c.Schedule.Weekday < 1 || *c.Schedule.Weekday > 7 || c.Schedule.Monthday != nil {
			return fail("weekly requires weekday 1..7 only")
		}
	case "monthly":
		if c.Schedule.Monthday == nil || *c.Schedule.Monthday < 1 || *c.Schedule.Monthday > 31 || c.Schedule.Weekday != nil {
			return fail("monthly requires monthday 1..31 only (short months use last valid day)")
		}
	default:
		return fail("invalid schedule frequency")
	}
	if c.Schedule.Enabled && (c.Backup.Root == nil || len(c.Selection.RepositoryIDs) == 0 || c.GitHub.Credential == nil || c.Schedule.Timezone == nil) {
		return fail("enabled schedule requires root, explicit selection, handle and confirmed timezone")
	}
	if c.Selection.RepositoryIDs == nil {
		c.Selection.RepositoryIDs = []string{}
	}
	if c.Cloud.Targets == nil {
		c.Cloud.Targets = []Target{}
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	if len(encoded) > MaxJSONBytes {
		return fail("configuration exceeds encoded JSON budget")
	}
	return c.Clone(), nil
}

// Import never imports execution permissions, even from a valid export.
func Import(r io.Reader) (Config, error) {
	c, err := Decode(r)
	if err != nil {
		return Config{}, err
	}
	c.Cloud.Enabled = false
	c.Schedule.Enabled = false
	c.Retention.Mode = "preview_only"
	return c, nil
}

func validBucket(provider, bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || !bucketPattern.MatchString(bucket) || strings.Contains(bucket, "..") || strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") || net.ParseIP(bucket) != nil {
		return false
	}
	if provider == "r2" {
		return !strings.Contains(bucket, ".")
	}
	for _, prefix := range []string{"xn--", "sthree-", "amzn-s3-demo-"} {
		if strings.HasPrefix(bucket, prefix) {
			return false
		}
	}
	for _, suffix := range []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"} {
		if strings.HasSuffix(bucket, suffix) {
			return false
		}
	}
	return true
}
