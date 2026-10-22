package backup

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const BundleFilename = "repository.bundle"

// ManifestLimits are per-operation resource budgets, not format bypasses.
// Zero and out-of-range limits are errors; use DefaultLimits for defaults.
type ManifestLimits struct {
	ManifestBytes      int64
	ReferenceCount     int
	ReferenceNameBytes int
	MetadataBytes      int64
	BundleBytes        int64
}

func DefaultLimits() ManifestLimits {
	return ManifestLimits{4 << 20, 100000, 4096, 4 << 20, 100 << 30}
}

func (l ManifestLimits) validate() error {
	if l.ManifestBytes < 1 || l.ManifestBytes > 16<<20 || l.ReferenceCount < 1 || l.ReferenceCount > 1000000 || l.ReferenceNameBytes < 1 || l.ReferenceNameBytes > 16384 || l.MetadataBytes < 1 || l.MetadataBytes > 16<<20 || l.BundleBytes < 1 || l.BundleBytes > 1<<40 {
		return fmt.Errorf("invalid manifest resource limits")
	}
	return nil
}

type RepositoryIdentity struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type Bundle struct {
	File          string   `json:"file"`
	Kind          string   `json:"kind"`
	SHA256        string   `json:"sha256"`
	SizeBytes     int64    `json:"size_bytes"`
	Prerequisites []string `json:"prerequisites"`
}

// Head is a discriminated value. Absent is representable but not a promise that
// an installed Git can recognize or preserve it. Engines must qualify that.
type Head struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	OID    string `json:"oid,omitempty"`
}

type Coverage struct {
	GitRefs               string `json:"git_refs"`
	LFSObjects            string `json:"lfs_objects"`
	SubmoduleRepositories string `json:"submodule_repositories"`
	GitHubMetadata        string `json:"github_metadata"`
}

func FullCoverage() Coverage {
	return Coverage{"advertised_and_fetched", "not_included", "not_included", "not_included"}
}

// Manifest stores raw, unpeeled object IDs, including annotated tag IDs. Refs
// and HEAD are independent; neither branch count nor refs imply empty history.
type Manifest struct {
	FormatVersion      int                `json:"format_version"`
	SnapshotID         string             `json:"snapshot_id"`
	CreatedAt          time.Time          `json:"created_at"`
	ApplicationVersion string             `json:"application_version"`
	Repository         RepositoryIdentity `json:"repository"`
	GitObjectFormat    string             `json:"git_object_format"`
	Bundle             Bundle             `json:"bundle"`
	Head               Head               `json:"head"`
	Refs               map[string]string  `json:"refs"`
	Coverage           Coverage           `json:"coverage"`
}

// NewID generates a lowercase random version-four UUID without external state.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("snapshot ID: %w", err)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func validID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s[14] != '4' || !strings.ContainsRune("89ab", rune(s[19])) {
		return false
	}
	return lowerHex(strings.ReplaceAll(s, "-", ""), 32)
}

func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validOID(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ValidRefName implements complete refs/... names (no one-level names,
// refspecs, wildcards, revision expressions, peeled advertisements or flags).
func ValidRefName(s string) bool {
	if !utf8.ValidString(s) || !strings.HasPrefix(s, "refs/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for _, c := range s {
		if c <= 32 || c == 127 || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func safeRepository(r RepositoryIdentity) bool {
	if r.Provider == "local" {
		return validID(r.ID)
	}
	if r.Provider != "github" || len(r.ID) == 0 || len(r.ID) > 256 || r.ID[0] == '0' {
		return false
	}
	for _, c := range r.ID {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func text(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

// Validate checks both semantic values and the canonical serialized resource
// bounds. MetadataBytes counts decoded JSON keys and string values together.
func (m Manifest) Validate(l ManifestLimits) error {
	_, err := m.Marshal(l)
	return err
}

func (m Manifest) validateValues(l ManifestLimits) error {
	if err := l.validate(); err != nil {
		return err
	}
	bad := func(field string) error { return fmt.Errorf("invalid manifest %s", field) }
	if m.FormatVersion != 1 {
		return bad("format_version")
	}
	if !validID(m.SnapshotID) {
		return bad("snapshot_id")
	}
	_, offset := m.CreatedAt.Zone()
	if m.CreatedAt.IsZero() || offset != 0 || m.CreatedAt.Year() < 1 || m.CreatedAt.Year() > 9999 {
		return bad("created_at")
	}
	if !text(m.ApplicationVersion, 256) {
		return bad("application_version")
	}
	if !safeRepository(m.Repository) || !text(m.Repository.DisplayName, 1024) {
		return bad("repository")
	}
	n := 40
	if m.GitObjectFormat == "sha256" {
		n = 64
	} else if m.GitObjectFormat != "sha1" {
		return bad("git_object_format")
	}
	if m.Bundle.File != BundleFilename || m.Bundle.Kind != "full" || !lowerHex(m.Bundle.SHA256, 64) || m.Bundle.SizeBytes < 0 || m.Bundle.SizeBytes > l.BundleBytes || m.Bundle.Prerequisites == nil || len(m.Bundle.Prerequisites) != 0 {
		return bad("bundle")
	}
	if m.Refs == nil || len(m.Refs) > l.ReferenceCount {
		return bad("refs")
	}
	var metadata int64
	add := func(s string) { metadata += int64(len(s)) }
	for k, v := range m.Refs {
		if len(k) > l.ReferenceNameBytes || !ValidRefName(k) || !validOID(v, n) {
			return bad("ref name or object ID")
		}
		add(k)
		add(v)
	}
	h := m.Head
	switch h.Kind {
	case "symbolic", "unborn":
		if h.OID != "" || len(h.Target) > l.ReferenceNameBytes || !ValidRefName(h.Target) {
			return bad("head")
		}
		_, exists := m.Refs[h.Target]
		if h.Kind == "symbolic" && !exists {
			return bad("symbolic head target missing")
		}
		if h.Kind == "unborn" && (exists || !strings.HasPrefix(h.Target, "refs/heads/")) {
			return bad("unborn head target")
		}
	case "detached":
		if h.Target != "" || !validOID(h.OID, n) {
			return bad("detached head")
		}
	case "absent":
		if h.Target != "" || h.OID != "" {
			return bad("absent head")
		}
	default:
		return bad("head kind")
	}
	if m.Coverage != FullCoverage() {
		return bad("coverage")
	}
	for _, s := range []string{m.SnapshotID, m.CreatedAt.Format(time.RFC3339Nano), m.ApplicationVersion, m.Repository.Provider, m.Repository.ID, m.Repository.DisplayName, m.GitObjectFormat, m.Bundle.File, m.Bundle.Kind, m.Bundle.SHA256, h.Kind, h.Target, h.OID, m.Coverage.GitRefs, m.Coverage.LFSObjects, m.Coverage.SubmoduleRepositories, m.Coverage.GitHubMetadata} {
		add(s)
	}
	if metadata > l.MetadataBytes {
		return bad("metadata byte limit")
	}
	return nil
}

// Marshal validates before emitting bytes. Parse applies the same wire bounds;
// no partial or invalid JSON is returned on a limit failure.
func (m Manifest) Marshal(l ManifestLimits) ([]byte, error) {
	if err := m.validateValues(l); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err = scanManifest(b, l); err != nil {
		return nil, err
	}
	return b, nil
}

// ReadManifest reads at most ManifestBytes+1, without allocating the configured
// budget eagerly. Callers own cancellation and filesystem identity checks.
func ReadManifest(r io.Reader, l ManifestLimits) (Manifest, error) {
	if err := l.validate(); err != nil {
		return Manifest{}, err
	}
	b, err := io.ReadAll(io.LimitReader(r, l.ManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	return ParseManifest(b, l)
}

func ParseManifest(b []byte, l ManifestLimits) (Manifest, error) {
	var m Manifest
	if err := scanManifest(b, l); err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := m.validateValues(l); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func scanManifest(b []byte, l ManifestLimits) error {
	if err := l.validate(); err != nil {
		return err
	}
	if int64(len(b)) > l.ManifestBytes {
		return fmt.Errorf("manifest byte limit exceeded")
	}
	if !utf8.Valid(b) || !validJSONSurrogates(b) {
		return fmt.Errorf("invalid manifest encoding")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	p := manifestScanner{d: d, limits: l}
	if err := p.object("manifest"); err != nil {
		return fmt.Errorf("manifest schema: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing manifest data")
	}
	return nil
}
