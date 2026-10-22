package cloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gitvault/internal/backup"
)

type Published struct {
	Manifest                     backup.Manifest
	ManifestKey, ETag, VersionID string
}
type LocalArtifact struct {
	Directory, ManifestPath, BundlePath string
	Manifest                            backup.Manifest
}

func (c *Client) validKey(key string) bool {
	base := c.o.Target.Prefix + c.o.Target.ID + "/"
	if len(key) > 1024 || !strings.HasPrefix(key, base) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), "/")
	if len(parts) != 4 || parts[0] != "local" && parts[0] != "github" || !segment.MatchString(parts[1]) || !segment.MatchString(parts[2]) || parts[3] != "manifest.json" && parts[3] != backup.BundleFilename {
		return false
	}
	return true
}

// getBytes pins object identity with HEAD and conditional GET. No ranged resume
// or combining bytes across retries is allowed. ETags are opaque conditions,
// never interpreted as digests. The expected hash comes from independent input.
func (c *Client) getBytes(ctx context.Context, key string, size int64, hash string, w io.Writer) (ObjectReceipt, error) {
	var h *s3.HeadObjectOutput
	e := c.attempt(ctx, "head", func() error {
		var err error
		h, err = c.sdk.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key)})
		return err
	})
	if e != nil {
		return ObjectReceipt{}, e
	}
	r := ObjectReceipt{Key: key, ETag: aws.ToString(h.ETag), VersionID: aws.ToString(h.VersionId), Size: aws.ToInt64(h.ContentLength)}
	if h.ContentLength == nil || r.Size < 0 || r.Size > size || hash != "" && r.Size != size || r.ETag == "" {
		return r, fmt.Errorf("remote object identity/size invalid")
	}
	in := &s3.GetObjectInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key), IfMatch: h.ETag}
	if r.VersionID != "" && r.VersionID != "null" {
		in.VersionId = h.VersionId
	}
	var out *s3.GetObjectOutput
	e = c.attempt(ctx, "get", func() error { var err error; out, err = c.sdk.GetObject(ctx, in); return err })
	if e != nil {
		return r, e
	}
	defer out.Body.Close()
	if aws.ToString(out.ETag) != r.ETag || r.VersionID != "" && aws.ToString(out.VersionId) != r.VersionID || out.ContentLength == nil || aws.ToInt64(out.ContentLength) != r.Size {
		return r, fmt.Errorf("remote object changed")
	}
	if hash != "" {
		e = digest(ctx, out.Body, w, r.Size, hash)
	} else {
		var n int64
		n, e = io.Copy(w, io.LimitReader(contextReader{ctx, out.Body}, r.Size+1))
		if n != r.Size {
			e = fmt.Errorf("remote object truncated")
		}
	}
	if e != nil {
		return r, fmt.Errorf("remote byte integrity failed")
	}
	return r, nil
}
func (c *Client) readManifest(ctx context.Context, key string) (Published, error) {
	if !c.validKey(key) || !strings.HasSuffix(key, "/manifest.json") {
		return Published{}, fmt.Errorf("invalid manifest key")
	}
	var buf bytes.Buffer
	r, e := c.getBytes(ctx, key, c.o.Limits.ManifestBytes, "", &buf)
	if e != nil {
		return Published{}, e
	}
	m, e := backup.ParseManifest(buf.Bytes(), c.o.Limits)
	if e != nil {
		return Published{}, fmt.Errorf("invalid remote manifest")
	}
	expected, _ := c.keys(m)
	if expected != key {
		return Published{}, fmt.Errorf("remote manifest identity/key mismatch")
	}
	return Published{Manifest: m, ManifestKey: key, ETag: r.ETag, VersionID: r.VersionID}, nil
}

// ListPublished returns only validated manifest markers. It does not claim that
// their bundle bytes are verified, authenticated, or Git-restorable.
func (c *Client) ListPublished(ctx context.Context) ([]Published, error) {
	ctx, cancel := context.WithTimeout(ctx, c.o.TransferTimeout)
	defer cancel()
	if e := acquire(ctx, c.transfers); e != nil {
		return nil, safeError("admission", e)
	}
	defer func() { <-c.transfers }()
	var result []Published
	var token *string
	seen := 0
	for {
		var out *s3.ListObjectsV2Output
		e := c.attempt(ctx, "list", func() error {
			var err error
			out, err = c.sdk.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.o.Target.Bucket), Prefix: aws.String(c.o.Target.Prefix + c.o.Target.ID + "/"), MaxKeys: aws.Int32(1000), ContinuationToken: token})
			return err
		})
		if e != nil {
			return result, e
		}
		for _, obj := range out.Contents {
			seen++
			if seen > c.o.ListLimit {
				return result, fmt.Errorf("remote listing budget exceeded")
			}
			key := aws.ToString(obj.Key)
			if !c.validKey(key) || !strings.HasSuffix(key, "/manifest.json") {
				continue
			}
			p, err := c.readManifest(ctx, key)
			if err != nil {
				return result, err
			}
			result = append(result, p)
		}
		if !aws.ToBool(out.IsTruncated) {
			return result, nil
		}
		next := aws.ToString(out.NextContinuationToken)
		if next == "" || len(next) > 4096 || next == aws.ToString(token) || len(out.Contents) == 0 {
			return result, fmt.Errorf("invalid listing continuation")
		}
		token = aws.String(next)
	}
}

// Download creates a new private directory, never replaces an existing path,
// and returns bytes for the parent's COMMON backup import/Restore path. The
// returned artifact has checksum evidence only, not Git verification evidence.
func (c *Client) Download(ctx context.Context, manifestKey, newdst string) (LocalArtifact, error) {
	var artifact LocalArtifact
	ctx, cancel := context.WithTimeout(ctx, c.o.TransferTimeout)
	defer cancel()
	if e := acquire(ctx, c.transfers); e != nil {
		return artifact, safeError("admission", e)
	}
	defer func() { <-c.transfers }()
	p, e := c.readManifest(ctx, manifestKey)
	if e != nil {
		return artifact, e
	}
	if !filepath.IsAbs(newdst) || filepath.Clean(newdst) != newdst {
		return artifact, fmt.Errorf("absolute new staging directory required")
	}
	if e = os.Mkdir(newdst, 0700); e != nil {
		return artifact, fmt.Errorf("new staging directory unavailable")
	}
	root, e := os.OpenRoot(newdst)
	if e != nil {
		return artifact, fmt.Errorf("private staging root unavailable")
	}
	defer root.Close()
	identity, e := root.Stat(".")
	if e != nil {
		return artifact, fmt.Errorf("private staging identity unavailable")
	}
	manifestPath := filepath.Join(newdst, "manifest.json")
	bundlePath := filepath.Join(newdst, backup.BundleFilename)
	success := false
	defer func() {
		if !success {
			_ = root.Remove("manifest.json")
			_ = root.Remove(backup.BundleFilename)
			if current, err := os.Lstat(newdst); err == nil && os.SameFile(identity, current) {
				_ = os.Remove(newdst)
			}
		}
	}()
	_, bundleKey := c.keys(p.Manifest)
	f, e := root.OpenFile(backup.BundleFilename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return artifact, fmt.Errorf("private bundle staging failed")
	}
	_, e = c.getBytes(ctx, bundleKey, p.Manifest.Bundle.SizeBytes, p.Manifest.Bundle.SHA256, f)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return artifact, fmt.Errorf("bundle download integrity/storage failed")
	}
	wire, e := p.Manifest.Marshal(c.o.Limits)
	if e != nil {
		return artifact, e
	}
	mf, e := root.OpenFile("manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return artifact, fmt.Errorf("private manifest staging failed")
	}
	_, e = mf.Write(wire)
	if e == nil {
		e = mf.Sync()
	}
	closeErr = mf.Close()
	if e != nil || closeErr != nil {
		return artifact, fmt.Errorf("manifest staging failed")
	}
	if current, err := os.Lstat(newdst); err != nil || !os.SameFile(identity, current) {
		return artifact, fmt.Errorf("private staging directory changed")
	}
	success = true
	return LocalArtifact{Directory: newdst, ManifestPath: manifestPath, BundlePath: bundlePath, Manifest: p.Manifest}, nil
}

// DeleteScope contains ONLY the exact target and two approved snapshot keys.
// Authorization, leases and persisted intent belong to the parent coordinator.
type DeleteScope struct {
	TargetID, ManifestKey, BundleKey string
	// Confirmed contains effects already durably recorded under this same intent.
	// Only the two declared keys are accepted; payload cannot precede manifest.
	Confirmed []string
}
type DeleteEffect struct {
	Key          string
	Confirmed    bool
	DeleteMarker bool
	VersionID    string
}
type DeleteReceipt struct {
	Effects         []DeleteEffect
	CleanupRequired bool
}

// DeleteCopy hides the manifest first. Journal must durably persist each effect;
// failure stops further effects. Ordinary deletion is not version-history purge,
// secure erasure, or evidence of reclaimed storage bytes.
func (c *Client) DeleteCopy(ctx context.Context, scope DeleteScope, journal func(context.Context, DeleteEffect) error) (DeleteReceipt, error) {
	r := DeleteReceipt{CleanupRequired: true}
	if scope.TargetID != c.o.Target.ID || !c.validKey(scope.ManifestKey) || !strings.HasSuffix(scope.ManifestKey, "/manifest.json") || scope.BundleKey != strings.TrimSuffix(scope.ManifestKey, "manifest.json")+backup.BundleFilename || journal == nil {
		return r, fmt.Errorf("invalid exact delete scope/journal")
	}
	confirmed := map[string]bool{}
	for _, key := range scope.Confirmed {
		if key != scope.ManifestKey && key != scope.BundleKey || confirmed[key] {
			return r, fmt.Errorf("invalid confirmed deletion scope")
		}
		confirmed[key] = true
	}
	if confirmed[scope.BundleKey] && !confirmed[scope.ManifestKey] {
		return r, fmt.Errorf("invalid deletion effect order")
	}
	ctx, cancel := context.WithTimeout(ctx, c.o.TransferTimeout)
	defer cancel()
	for _, key := range []string{scope.ManifestKey, scope.BundleKey} {
		if confirmed[key] {
			continue
		}
		// No automatic retry of a destructive effect whose response may be lost.
		out, e := c.sdk.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key)})
		effect := DeleteEffect{Key: key, Confirmed: e == nil}
		if e == nil {
			effect.DeleteMarker = aws.ToBool(out.DeleteMarker)
			effect.VersionID = aws.ToString(out.VersionId)
		}
		r.Effects = append(r.Effects, effect)
		clean, done := context.WithTimeout(context.Background(), c.o.CleanupTimeout)
		je := journal(clean, effect)
		done()
		if e != nil {
			return r, safeError("delete", e)
		}
		if je != nil {
			return r, fmt.Errorf("delete effect journal failed")
		}
	}
	r.CleanupRequired = false
	return r, nil
}
