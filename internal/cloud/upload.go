package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gitvault/internal/backup"
)

func digest(ctx context.Context, r io.Reader, w io.Writer, size int64, expected string) error {
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(w, h), io.LimitReader(contextReader{ctx, r}, size+1))
	if e != nil {
		return fmt.Errorf("artifact read failed")
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != expected {
		return fmt.Errorf("artifact size/checksum mismatch")
	}
	return nil
}

// Upload consumes a caller-verified immutable ready bundle. It checks file bytes,
// but intentionally does not repeat Git bundle, fsck, or restoration verification.
// On uncertain publication retry only the SAME manifest/key, never a fresh ID.
func (c *Client) Upload(ctx context.Context, m backup.Manifest, bundlePath string) (Receipt, error) {
	r := Receipt{SnapshotID: m.SnapshotID, Publication: "not_published", Verification: "upload_receipt"}
	wire, e := m.Marshal(c.o.Limits)
	if e != nil {
		return r, fmt.Errorf("invalid ready manifest")
	}
	ctx, cancel := context.WithTimeout(ctx, c.o.TransferTimeout)
	defer cancel()
	if e = acquire(ctx, c.transfers); e != nil {
		return r, safeError("admission", e)
	}
	defer func() { <-c.transfers }()
	f, e := os.Open(bundlePath)
	if e != nil {
		return r, fmt.Errorf("ready bundle unavailable")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != m.Bundle.SizeBytes {
		return r, fmt.Errorf("ready bundle is not a fixed regular file")
	}
	if e = digest(ctx, io.NewSectionReader(f, 0, st.Size()), io.Discard, st.Size(), m.Bundle.SHA256); e != nil {
		return r, e
	}
	r.ManifestKey, r.BundleKey = c.keys(m)
	obj, e := c.uploadFile(ctx, r.BundleKey, f, m.Bundle.SizeBytes, m.Bundle.SHA256, &r)
	if e != nil {
		return r, e
	}
	r.Objects = append(r.Objects, obj)
	// Recheck local input before publishing the marker. An immutable ready snapshot
	// is a caller precondition; this also catches ordinary in-place mutation.
	if e = digest(ctx, io.NewSectionReader(f, 0, st.Size()), io.Discard, st.Size(), m.Bundle.SHA256); e != nil {
		return r, e
	}
	if c.o.ReadBack {
		if e = c.verify(ctx, r.BundleKey, m.Bundle.SizeBytes, m.Bundle.SHA256); e != nil {
			return r, e
		}
	}
	sum := sha256.Sum256(wire)
	hash := hex.EncodeToString(sum[:])
	r.Publication = "outcome_unknown_requires_reconciliation"
	obj, e = c.put(ctx, r.ManifestKey, func() io.ReadSeeker { return bytes.NewReader(wire) }, int64(len(wire)), hash)
	if e != nil {
		return r, e
	}
	r.Objects = append(r.Objects, obj)
	r.Publication = "published"
	if c.o.ReadBack {
		if e = c.verify(ctx, r.ManifestKey, int64(len(wire)), hash); e != nil {
			return r, e
		}
		r.Verification = "read_back_checksum"
	}
	return r, nil
}
func (c *Client) put(ctx context.Context, key string, body func() io.ReadSeeker, size int64, hash string) (ObjectReceipt, error) {
	r := ObjectReceipt{Key: key, Size: size}
	e := c.attempt(ctx, "put", func() error {
		out, err := c.sdk.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key), Body: body(), ContentLength: aws.Int64(size), IfNoneMatch: aws.String("*")})
		if err == nil {
			r.ETag = aws.ToString(out.ETag)
			r.VersionID = aws.ToString(out.VersionId)
			return nil
		}
		// Includes conflicts and a committed write whose response was lost. Never
		// accept caller metadata or HEAD as proof of a matching existing object.
		if ctx.Err() == nil && c.verify(ctx, key, size, hash) == nil {
			return nil
		}
		return err
	})
	return r, e
}
func (c *Client) verify(ctx context.Context, key string, size int64, hash string) error {
	_, e := c.getBytes(ctx, key, size, hash, io.Discard)
	return e
}
func (c *Client) uploadFile(ctx context.Context, key string, f *os.File, size int64, hash string, r *Receipt) (ObjectReceipt, error) {
	if size < c.o.MultipartThreshold {
		return c.put(ctx, key, func() io.ReadSeeker { return io.NewSectionReader(f, 0, size) }, size, hash)
	}
	// Exact pinned SDK exposes CompleteMultipartUploadInput.IfNoneMatch. Providers
	// that reject this condition fail closed; no unconditional fallback exists.
	partSize := c.o.PartSize
	min := ((size+9999)/10000 + (1 << 20) - 1) / (1 << 20) * (1 << 20)
	if min > partSize {
		partSize = min
	}
	if partSize > 128<<20 {
		return ObjectReceipt{}, fmt.Errorf("provider part-count budget exceeded")
	}
	var start *s3.CreateMultipartUploadOutput
	// Creation is not retried: a lost response may leave an upload with unknown ID.
	// Surface that crash window rather than manufacture duplicate uploads.
	var e error
	start, e = c.sdk.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key)})
	if e != nil {
		r.Multipart = append(r.Multipart, MultipartFact{Key: key, Cleanup: "creation_unknown"})
		return ObjectReceipt{}, safeError("multipart_create", e)
	}
	id := aws.ToString(start.UploadId)
	if id == "" || len(id) > 2048 {
		r.Multipart = append(r.Multipart, MultipartFact{Key: key, Cleanup: "creation_unknown"})
		return ObjectReceipt{}, fmt.Errorf("invalid multipart identity")
	}
	fact := MultipartFact{Key: key, UploadID: id, Cleanup: "pending"}
	r.Multipart = append(r.Multipart, fact)
	index := len(r.Multipart) - 1
	committed := false
	defer func() {
		if committed {
			return
		}
		r.Multipart[index], _ = c.AbortPending(ctx, fact)
	}()
	if c.o.Journal != nil {
		if e = c.o.Journal(ctx, fact); e != nil {
			return ObjectReceipt{}, fmt.Errorf("multipart journal failed")
		}
	}
	count := int((size + partSize - 1) / partSize)
	completed := make([]types.CompletedPart, count)
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	jobs := make(chan int)
	for w := 0; w < c.o.Parts; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := acquire(work, c.parts); err != nil {
					mu.Lock()
					if first == nil {
						first = safeError("part", err)
					}
					mu.Unlock()
					return
				}
				offset := int64(i) * partSize
				n := min64(partSize, size-offset)
				err := c.attempt(work, "part", func() error {
					out, err := c.sdk.UploadPart(work, &s3.UploadPartInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(int32(i + 1)), Body: io.NewSectionReader(f, offset, n), ContentLength: aws.Int64(n)})
					if err == nil {
						if aws.ToString(out.ETag) == "" {
							return fmt.Errorf("part receipt missing")
						}
						completed[i] = types.CompletedPart{PartNumber: aws.Int32(int32(i + 1)), ETag: out.ETag}
					}
					return err
				})
				<-c.parts
				if err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					cancel()
					return
				}
			}
		}()
	}
feed:
	for i := 0; i < count; i++ {
		select {
		case jobs <- i:
		case <-work.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return ObjectReceipt{}, first
	}
	if e = ctx.Err(); e != nil {
		return ObjectReceipt{}, safeError("multipart", e)
	}
	result := ObjectReceipt{Key: key, Size: size}
	e = c.attempt(ctx, "complete", func() error {
		out, err := c.sdk.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(key), UploadId: aws.String(id), IfNoneMatch: aws.String("*"), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
		if err == nil {
			result.ETag = aws.ToString(out.ETag)
			result.VersionID = aws.ToString(out.VersionId)
			return nil
		}
		if ctx.Err() == nil && c.verify(ctx, key, size, hash) == nil {
			return nil
		}
		return err
	})
	if e != nil {
		return result, e
	}
	committed = true
	fact.Cleanup = "completed"
	r.Multipart[index] = fact
	if c.o.Journal != nil {
		if c.o.Journal(ctx, fact) != nil {
			return result, fmt.Errorf("multipart completion journal failed")
		}
	}
	return result, nil
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// AbortPending reconciles one persisted application-owned multipart identity.
// It never lists/sweeps uploads, deletes an object, or removes local artifacts.
// Caller must recover the fact from its own durable operation intent, not remote
// inventory. The cleanup context intentionally survives transfer cancellation.
func (c *Client) AbortPending(ctx context.Context, fact MultipartFact) (MultipartFact, error) {
	if !c.validKey(fact.Key) || len(fact.UploadID) == 0 || len(fact.UploadID) > 2048 {
		return fact, fmt.Errorf("invalid owned multipart cleanup scope")
	}
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.o.CleanupTimeout)
	defer cancel()
	e := c.attempt(clean, "abort", func() error {
		_, err := c.sdk.AbortMultipartUpload(clean, &s3.AbortMultipartUploadInput{Bucket: aws.String(c.o.Target.Bucket), Key: aws.String(fact.Key), UploadId: aws.String(fact.UploadID)})
		return err
	})
	fact.Cleanup = "aborted"
	if e != nil {
		fact.Cleanup = "cleanup_required"
	}
	if c.o.Journal != nil {
		if c.o.Journal(clean, fact) != nil {
			fact.Cleanup += "_journal_failed"
			if e == nil {
				e = fmt.Errorf("multipart cleanup journal failed")
			}
		}
	}
	return fact, e
}
