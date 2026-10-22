package backup

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Each object is scanned against its closed schema before typed decoding. No
// recursive arbitrary JSON tree or unbounded collection is constructed.
type manifestScanner struct {
	d        *json.Decoder
	limits   ManifestLimits
	metadata int64
}

func schema(kind string) []string {
	switch kind {
	case "manifest":
		return []string{"format_version", "snapshot_id", "created_at", "application_version", "repository", "git_object_format", "bundle", "head", "refs", "coverage"}
	case "repository":
		return []string{"provider", "id", "display_name"}
	case "bundle":
		return []string{"file", "kind", "sha256", "size_bytes", "prerequisites"}
	case "coverage":
		return []string{"git_refs", "lfs_objects", "submodule_repositories", "github_metadata"}
	case "head":
		return []string{"kind", "target", "oid"}
	}
	return nil
}

func (p *manifestScanner) stringToken(t json.Token, max int) (string, error) {
	s, ok := t.(string)
	if !ok || len(s) > max {
		return "", fmt.Errorf("expected bounded string")
	}
	p.metadata += int64(len(s))
	if p.metadata > p.limits.MetadataBytes {
		return "", fmt.Errorf("metadata byte limit exceeded")
	}
	return s, nil
}

func (p *manifestScanner) object(kind string) error {
	t, err := p.d.Token()
	if err != nil || t != json.Delim('{') {
		return fmt.Errorf("expected %s object", kind)
	}
	seen := make(map[string]bool)
	var headKind string
	for p.d.More() {
		if kind == "refs" && len(seen) >= p.limits.ReferenceCount {
			return fmt.Errorf("reference count limit exceeded")
		}
		t, err = p.d.Token()
		if err != nil {
			return err
		}
		max := 64
		if kind == "refs" {
			max = p.limits.ReferenceNameBytes
		}
		key, err := p.stringToken(t, max)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("duplicate JSON key")
		}
		if kind != "refs" {
			allowed := false
			for _, s := range schema(kind) {
				if s == key {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("unknown %s field", kind)
			}
		}
		seen[key] = true
		if kind == "manifest" && (key == "repository" || key == "bundle" || key == "head" || key == "refs" || key == "coverage") {
			if err := p.object(key); err != nil {
				return err
			}
			continue
		}
		t, err = p.d.Token()
		if err != nil {
			return err
		}
		if kind == "bundle" && key == "prerequisites" {
			if t != json.Delim('[') || p.d.More() {
				return fmt.Errorf("full bundle requires empty prerequisites array")
			}
			t, err = p.d.Token()
			if err != nil || t != json.Delim(']') {
				return fmt.Errorf("invalid prerequisites")
			}
			continue
		}
		if kind == "manifest" && key == "format_version" || kind == "bundle" && key == "size_bytes" {
			n, ok := t.(json.Number)
			if !ok {
				return fmt.Errorf("expected integer")
			}
			if _, err := strconv.ParseInt(string(n), 10, 64); err != nil {
				return fmt.Errorf("invalid integer: %w", err)
			}
			continue
		}
		max = 1024
		if kind == "refs" {
			max = 64
		}
		if kind == "head" && key == "target" {
			max = p.limits.ReferenceNameBytes
		}
		s, err := p.stringToken(t, max)
		if err != nil {
			return err
		}
		if kind == "head" && key == "kind" {
			headKind = s
		}
		if kind == "manifest" && key == "created_at" && !utcTimestampSyntax(s) {
			return fmt.Errorf("timestamp must use RFC3339 UTC Z syntax")
		}
	}
	t, err = p.d.Token()
	if err != nil || t != json.Delim('}') {
		return fmt.Errorf("unterminated object")
	}
	if kind == "head" {
		if !seen["kind"] {
			return fmt.Errorf("missing head kind")
		}
		switch headKind {
		case "absent":
			if len(seen) != 1 {
				return fmt.Errorf("absent HEAD cannot have target or oid")
			}
		case "symbolic", "unborn":
			if len(seen) != 2 || !seen["target"] {
				return fmt.Errorf("HEAD requires only target")
			}
		case "detached":
			if len(seen) != 2 || !seen["oid"] {
				return fmt.Errorf("HEAD requires only oid")
			}
		default:
			return fmt.Errorf("unknown HEAD kind")
		}
	} else if kind != "refs" {
		for _, s := range schema(kind) {
			if !seen[s] {
				return fmt.Errorf("missing %s field %s", kind, s)
			}
		}
	}
	return nil
}

// time.Parse accepts some non-RFC3339 spellings (for example a comma fraction
// and one-digit hour). Enforce wire grammar before time.Time validates dates.
func utcTimestampSyntax(s string) bool {
	if len(s) < 20 || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' || s[len(s)-1] != 'Z' {
		return false
	}
	for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	if len(s) == 20 {
		return true
	}
	if len(s) < 22 || s[19] != '.' {
		return false
	}
	for i := 20; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// encoding/json replaces unpaired surrogate escapes with U+FFFD. Reject them
// instead, so malformed encoding cannot silently change a saved ref or name.
func validJSONSurrogates(b []byte) bool {
	inString := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n < 0xd800 || n > 0xdbff {
			continue
		}
		if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
			return false
		}
		n, err = strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
		if err != nil || n < 0xdc00 || n > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
