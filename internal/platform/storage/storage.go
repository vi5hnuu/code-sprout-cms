// Package storage talks to S3-compatible object storage (SeaweedFS in dev, R2 or S3 in
// production). The API never proxies file bytes: clients upload with presigned URLs, and
// readers fetch public objects from the CDN.
package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"code-sprout-cms/internal/platform/config"
)

type Config struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
}

// PresignedUpload is what a client needs to PUT bytes directly to storage. Headers must be
// sent exactly as given: they are part of the signature, so storage rejects an upload whose
// type, length or checksum differs from what the API approved.
type PresignedUpload struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

type ObjectInfo struct {
	Size        int64
	ContentType string
	// SHA256 is the checksum storage verified on upload, or nil if the backend did not record
	// one; callers then verify by reading the object.
	SHA256 []byte
}

var ErrNotFound = errors.New("object not found")

// Store is the subset of object storage the domains use, as an interface so services can be
// tested without a storage server.
type Store interface {
	PresignPut(ctx context.Context, bucket, key, contentType string, size int64, sha256 []byte, ttl time.Duration) (PresignedUpload, error)
	Head(ctx context.Context, bucket, key string) (ObjectInfo, error)
	// Open streams an object; limit > 0 reads only its first `limit` bytes.
	Open(ctx context.Context, bucket, key string, limit int64) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
}

type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
}

func NewS3(cfg Config) *S3 {
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		// Path-style (endpoint/bucket/key) works on every S3-compatible server; virtual-host
		// style needs wildcard DNS that self-hosted storage usually lacks.
		UsePathStyle: true,
		// Only send/expect checksums when an operation asks for one. The default
		// ("when supported") adds CRC32 trailers some S3-compatible servers reject.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3{client: client, presign: s3.NewPresignClient(client)}
}

func (s *S3) PresignPut(ctx context.Context, bucket, key, contentType string, size int64, sha256 []byte, ttl time.Duration) (PresignedUpload, error) {
	checksum := base64.StdEncoding.EncodeToString(sha256)
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		ContentType:    aws.String(contentType),
		ContentLength:  aws.Int64(size),
		ChecksumSHA256: aws.String(checksum),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedUpload{}, fmt.Errorf("presign put %s/%s: %w", bucket, key, err)
	}
	headers := map[string]string{}
	for name, values := range req.SignedHeader {
		// Host and Content-Length are set by the HTTP client itself.
		if name == "Host" || name == "Content-Length" || len(values) == 0 {
			continue
		}
		headers[name] = values[0]
	}
	return PresignedUpload{URL: req.URL, Method: http.MethodPut, Headers: headers, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (s *S3) Head(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, fmt.Errorf("head %s/%s: %w", bucket, key, err)
	}
	info := ObjectInfo{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}
	if c := aws.ToString(out.ChecksumSHA256); c != "" {
		if sum, err := base64.StdEncoding.DecodeString(c); err == nil {
			info.SHA256 = sum
		}
	}
	return info, nil
}

func (s *S3) Open(ctx context.Context, bucket, key string, limit int64) (io.ReadCloser, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if limit > 0 {
		in.Range = aws.String(fmt.Sprintf("bytes=0-%d", limit-1))
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		var nk *types.NoSuchKey
		if errors.As(err, &nk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get %s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}

func (s *S3) Delete(ctx context.Context, bucket, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete %s/%s: %w", bucket, key, err)
	}
	return nil
}

// FromConfig builds the S3 client from process configuration.
func FromConfig(c config.StorageConfig) *S3 {
	return NewS3(Config{Endpoint: c.Endpoint, Region: c.Region, AccessKey: c.AccessKey, SecretKey: c.SecretKey})
}
