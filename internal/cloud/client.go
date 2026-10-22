// Package cloud copies already verified full snapshots. It does not verify Git
// objects or grant upload/deletion authority; those remain caller responsibilities.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"gitvault/internal/backup"
)

type Credentials struct{ AccessKey, SecretKey, SessionToken string }

// CredentialSource must honor its bounded context. New never loads ambient AWS
// configuration. The first resolution (including failure) is fixed per Client;
// credential rotation/retry after an unavailable secret needs a new Client.
type CredentialSource func(context.Context) (Credentials, error)
type Target struct{ ID, Provider, Bucket, Region, Endpoint, Prefix string }
type Options struct {
	Target                                                               Target
	Credentials                                                          CredentialSource
	HTTPClient                                                           *http.Client
	Limits                                                               backup.ManifestLimits
	PartSize, MultipartThreshold                                         int64
	Transfers, Parts, Attempts, ListLimit                                int
	RequestTimeout, TransferTimeout, CleanupTimeout, Backoff, MaxBackoff time.Duration
	ReadBack                                                             bool
	// Journal must durably record each multipart identity before returning nil.
	// Failure stops the transfer; returned Receipt also retains cleanup facts.
	Journal func(context.Context, MultipartFact) error
}
type MultipartFact struct{ Key, UploadID, Cleanup string }
type ObjectReceipt struct {
	Key, ETag, VersionID string
	Size                 int64
}
type Receipt struct {
	SnapshotID, ManifestKey, BundleKey, Publication, Verification string
	Objects                                                       []ObjectReceipt
	Multipart                                                     []MultipartFact
}
type Error struct {
	Operation, Code string
	Status          int
	Cancelled       bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("cloud %s: %s (status %d)", e.Operation, e.Code, e.Status)
}
func (e *Error) Is(target error) bool {
	return e.Cancelled && target == context.Canceled || e.Code == "deadline" && target == context.DeadlineExceeded
}
func safeError(op string, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	var r *smithyhttp.ResponseError
	if errors.As(err, &r) {
		status = r.HTTPStatusCode()
	}
	code := "request_failed"
	if errors.Is(err, context.Canceled) {
		code = "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline"
	}
	return &Error{Operation: op, Code: code, Status: status, Cancelled: errors.Is(err, context.Canceled)}
}

type Client struct {
	o                Options
	sdk              *s3.Client
	transfers, parts chan struct{}
}

func DefaultOptions() Options {
	return Options{Limits: backup.DefaultLimits(), PartSize: 16 << 20, MultipartThreshold: 32 << 20, Transfers: 1, Parts: 2, Attempts: 3, ListLimit: 10000, RequestTimeout: 60 * time.Second, TransferTimeout: 4 * time.Hour, CleanupTimeout: 30 * time.Second, Backoff: time.Second, MaxBackoff: 30 * time.Second}
}

var segment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func New(o Options) (*Client, error) {
	t := o.Target
	if !segment.MatchString(t.ID) || len(t.ID) > 64 || !bucketName.MatchString(t.Bucket) || strings.Contains(t.Bucket, "..") || o.Credentials == nil {
		return nil, fmt.Errorf("invalid cloud target or credential source")
	}
	if t.Provider != "s3" && t.Provider != "r2" || !segment.MatchString(t.Region) || len(t.Region) > 64 || t.Provider == "r2" && t.Region != "auto" {
		return nil, fmt.Errorf("invalid provider/region")
	}
	if len(t.Prefix) < 1 || len(t.Prefix) > 256 || !strings.HasSuffix(t.Prefix, "/") {
		return nil, fmt.Errorf("invalid prefix")
	}
	for _, s := range strings.Split(strings.TrimSuffix(t.Prefix, "/"), "/") {
		if !segment.MatchString(s) {
			return nil, fmt.Errorf("invalid prefix")
		}
	}
	if t.Endpoint != "" {
		u, e := url.Parse(t.Endpoint)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || len(t.Endpoint) > 2048 {
			return nil, fmt.Errorf("invalid HTTPS endpoint")
		}
	} else if t.Provider == "r2" {
		return nil, fmt.Errorf("R2 requires explicit endpoint")
	}
	if o.PartSize < 8<<20 || o.PartSize > 128<<20 || o.PartSize%(1<<20) != 0 || o.MultipartThreshold < o.PartSize || o.MultipartThreshold > 256<<20 || o.Transfers < 1 || o.Transfers > 4 || o.Parts < 1 || o.Parts > 4 || o.Transfers*o.Parts > 8 || o.Attempts < 1 || o.Attempts > 8 || o.ListLimit < 1 || o.ListLimit > 100000 || o.RequestTimeout <= 0 || o.RequestTimeout > 300*time.Second || o.TransferTimeout < o.RequestTimeout || o.TransferTimeout > 24*time.Hour || o.CleanupTimeout <= 0 || o.CleanupTimeout > 120*time.Second || o.Backoff <= 0 || o.MaxBackoff < o.Backoff || o.MaxBackoff > 120*time.Second {
		return nil, fmt.Errorf("invalid cloud budgets")
	}
	l := o.Limits
	if l.ManifestBytes < 1 || l.ManifestBytes > 16<<20 || l.ReferenceCount < 1 || l.ReferenceCount > 1000000 || l.ReferenceNameBytes < 1 || l.ReferenceNameBytes > 16384 || l.MetadataBytes < 1 || l.MetadataBytes > 16<<20 || l.BundleBytes < 1 || l.BundleBytes > 1<<40 {
		return nil, fmt.Errorf("invalid manifest budgets")
	}
	hc := http.Client{}
	if o.HTTPClient != nil {
		hc = *o.HTTPClient
	}
	tr := hc.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	transport, ok := tr.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("trusted HTTP transport required")
	}
	transport = transport.Clone()
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.InsecureSkipVerify {
			return nil, fmt.Errorf("TLS verification required")
		}
	}
	hc.Transport = boundedTransport{base: transport}
	hc.Timeout = o.RequestTimeout
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	credentialGate := make(chan struct{}, 1)
	var fixedCredentials *aws.Credentials
	credentialFailed := false
	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		ctx, cancel := context.WithTimeout(ctx, o.RequestTimeout)
		defer cancel()
		if e := acquire(ctx, credentialGate); e != nil {
			return aws.Credentials{}, fmt.Errorf("explicit credentials unavailable")
		}
		defer func() { <-credentialGate }()
		if fixedCredentials != nil {
			return *fixedCredentials, nil
		}
		if credentialFailed {
			return aws.Credentials{}, fmt.Errorf("explicit credentials unavailable")
		}
		v, e := o.Credentials(ctx)
		if e != nil || ctx.Err() != nil || len(v.AccessKey) == 0 || len(v.SecretKey) == 0 || len(v.AccessKey) > 256 || len(v.SecretKey) > 1024 || len(v.SessionToken) > 4096 {
			credentialFailed = true
			return aws.Credentials{}, fmt.Errorf("explicit credentials unavailable")
		}
		value := aws.Credentials{AccessKeyID: v.AccessKey, SecretAccessKey: v.SecretKey, SessionToken: v.SessionToken, Source: "explicit"}
		fixedCredentials = &value
		return value, nil
	})
	// One explicit, bounded secret identity per client; never consult SDK config,
	// environment, instance metadata, shared profiles, or refresh to a new identity.
	cfg := aws.Config{Region: t.Region, Credentials: provider, HTTPClient: &hc, Retryer: func() aws.Retryer { return aws.NopRetryer{} }, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}
	sdk := s3.NewFromConfig(cfg, func(s *s3.Options) {
		s.UsePathStyle = true
		if t.Endpoint != "" {
			s.BaseEndpoint = aws.String(t.Endpoint)
		}
	})
	return &Client{o: o, sdk: sdk, transfers: make(chan struct{}, o.Transfers), parts: make(chan struct{}, o.Transfers*o.Parts)}, nil
}

// SDK XML error/list parsers must not allocate from arbitrary server bodies.
// Artifact GETs are bounded and counted by getBytes instead.
type boundedTransport struct{ base *http.Transport }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out, e := t.base.RoundTrip(r)
	if e != nil {
		return out, e
	}
	if r.Method != http.MethodGet || r.URL.Query().Has("list-type") || out.StatusCode >= 300 {
		limit := int64(1 << 20)
		if r.URL.Query().Has("list-type") {
			limit = 16 << 20
		}
		out.Body = &limitedBody{Reader: io.LimitReader(out.Body, limit), closer: out.Body}
	}
	return out, nil
}

type limitedBody struct {
	io.Reader
	closer io.Closer
}

func (b *limitedBody) Close() error { return b.closer.Close() }
func acquire(ctx context.Context, ch chan struct{}) error {
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) attempt(ctx context.Context, op string, fn func() error) error {
	var err error
	for n := 0; n < c.o.Attempts; n++ {
		if ctx.Err() != nil {
			return safeError(op, ctx.Err())
		}
		err = fn()
		if err == nil {
			return nil
		}
		var r *smithyhttp.ResponseError
		if errors.As(err, &r) && r.HTTPStatusCode() != 429 && r.HTTPStatusCode() < 500 {
			break
		}
		if n+1 == c.o.Attempts {
			break
		}
		d := c.o.Backoff
		for i := 0; i < n && d < c.o.MaxBackoff; i++ {
			d *= 2
			if d > c.o.MaxBackoff {
				d = c.o.MaxBackoff
			}
		}
		if d > c.o.MaxBackoff {
			d = c.o.MaxBackoff
		}
		timer := time.NewTimer(time.Duration(rand.Int64N(int64(d) + 1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return safeError(op, ctx.Err())
		case <-timer.C:
		}
	}
	return safeError(op, err)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func (c *Client) keys(m backup.Manifest) (string, string) {
	base := c.o.Target.Prefix + c.o.Target.ID + "/" + m.Repository.Provider + "/" + m.Repository.ID + "/" + m.SnapshotID + "/"
	return base + "manifest.json", base + backup.BundleFilename
}
