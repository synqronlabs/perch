// Package blob reads raw messages from S3-compatible object storage.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/synqronlabs/perch/internal/config"
)

const maxObjectBytes = 64 << 20

// ErrObjectNotFound reports a missing S3 object.
var ErrObjectNotFound = errors.New("blob: object not found")

// Object is one listed S3 object.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// S3 reads objects from an S3-compatible bucket. It is safe for concurrent
// use.
type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3 creates an S3 client from configuration. The endpoint may be empty for
// AWS S3; for R2, B2, or self-hosted storage it must include the scheme.
func NewS3(ctx context.Context, cfg config.S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("blob: s3 bucket is required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.Secret(), "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})
	return &S3{client: client, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// Get downloads an object. key is the object key as stored in the message's
// s3_key column, without a bucket prefix.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, ErrObjectNotFound
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("get s3 object %q: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read s3 object %q: %w", key, err)
	}
	if len(data) > maxObjectBytes {
		return nil, fmt.Errorf("blob: object %q exceeds %d bytes", key, maxObjectBytes)
	}
	return data, nil
}

// Delete removes one object. It is used only by the explicit gc command.
func (s *S3) Delete(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete s3 object %q: %w", key, err)
	}
	return nil
}

// Head reports whether an object exists and its size.
func (s *S3) Head(ctx context.Context, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return 0, ErrObjectNotFound
		}
		return 0, fmt.Errorf("head s3 object %q: %w", key, err)
	}
	if out.ContentLength == nil {
		return 0, nil
	}
	return *out.ContentLength, nil
}

// List returns every object under prefix.
func (s *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	fullPrefix := s.prefix + strings.TrimPrefix(prefix, "/")
	var out []Object
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3 objects: %w", err)
		}
		for _, item := range page.Contents {
			if item.Key == nil {
				continue
			}
			obj := Object{Key: *item.Key}
			if item.Size != nil {
				obj.Size = *item.Size
			}
			if item.LastModified != nil {
				obj.LastModified = *item.LastModified
			}
			out = append(out, obj)
		}
	}
	return out, nil
}

// Ping verifies credentials and bucket access.
func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("head s3 bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Bucket returns the configured bucket name.
func (s *S3) Bucket() string { return s.bucket }

// Prefix returns the configured key prefix.
func (s *S3) Prefix() string { return s.prefix }

func isNotFound(err error) bool {
	var noSuchKey *s3types.NoSuchKey
	var notFound *s3types.NotFound
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
		return true
	}
	// Some S3-compatible providers return a generic API error with a 404 code.
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
