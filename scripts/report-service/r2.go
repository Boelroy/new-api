package main

// Cloudflare R2 client for storing test-run artifacts (trace.md / report.md /
// stderr.log / result.json). Uses the AWS S3 v2 SDK pointed at the R2
// account-scoped endpoint. All objects are kept in a single private bucket
// and surfaced to the UI via short-lived presigned GET URLs.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	r2EnvAccountID       = "R2_ACCOUNT_ID"
	r2EnvAccessKeyID     = "R2_ACCESS_KEY_ID"
	r2EnvSecretAccessKey = "R2_SECRET_ACCESS_KEY"
	r2EnvBucket          = "R2_BUCKET"
	r2EnvRegion          = "R2_REGION" // optional, defaults to "auto"

	r2DefaultRegion       = "auto"
	r2SignedURLDefaultTTL = 5 * time.Minute
)

var (
	r2ClientOnce  sync.Once
	r2Client      *s3.Client
	r2Presigner   *s3.PresignClient
	r2BucketName  string
	r2InitErr     error
)

// r2Configured reports whether all R2 env vars are present. UI uses this to
// gate the Provider Testing nav item, mirroring profit_enabled.
func r2Configured() bool {
	return strings.TrimSpace(os.Getenv(r2EnvAccountID)) != "" &&
		strings.TrimSpace(os.Getenv(r2EnvAccessKeyID)) != "" &&
		strings.TrimSpace(os.Getenv(r2EnvSecretAccessKey)) != "" &&
		strings.TrimSpace(os.Getenv(r2EnvBucket)) != ""
}

// r2InitOnce builds the singleton S3 client + presigner pointed at R2.
// Subsequent calls reuse the same client. Returns an error if any required
// env var is missing.
func r2InitOnce() error {
	r2ClientOnce.Do(func() {
		accountID := strings.TrimSpace(os.Getenv(r2EnvAccountID))
		accessKey := strings.TrimSpace(os.Getenv(r2EnvAccessKeyID))
		secretKey := strings.TrimSpace(os.Getenv(r2EnvSecretAccessKey))
		bucket := strings.TrimSpace(os.Getenv(r2EnvBucket))
		region := strings.TrimSpace(os.Getenv(r2EnvRegion))
		if region == "" {
			region = r2DefaultRegion
		}
		if accountID == "" || accessKey == "" || secretKey == "" || bucket == "" {
			r2InitErr = errors.New("R2 env not configured (need R2_ACCOUNT_ID/R2_ACCESS_KEY_ID/R2_SECRET_ACCESS_KEY/R2_BUCKET)")
			return
		}
		endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
		cfg := aws.Config{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		}
		client := s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			// R2 uses path-style addressing rather than virtual-hosted style.
			o.UsePathStyle = true
		})
		r2Client = client
		r2Presigner = s3.NewPresignClient(client)
		r2BucketName = bucket
	})
	return r2InitErr
}

// r2PutObject uploads bytes under the given key.
func r2PutObject(ctx context.Context, key, contentType string, data []byte) error {
	if err := r2InitOnce(); err != nil {
		return err
	}
	_, err := r2Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r2BucketName),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	return err
}

// r2SignedGetURL returns a presigned GET URL valid for ttl. The browser can
// then fetch the object directly from R2.
func r2SignedGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := r2InitOnce(); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = r2SignedURLDefaultTTL
	}
	req, err := r2Presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r2BucketName),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// r2DeleteObjects removes the given keys. Missing keys are silently ignored.
// Up to 1000 keys per call (S3 DeleteObjects limit); chunks beyond that are
// batched automatically.
func r2DeleteObjects(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := r2InitOnce(); err != nil {
		return err
	}
	const chunkSize = 1000
	for start := 0; start < len(keys); start += chunkSize {
		end := start + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		ids := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k)})
		}
		if _, err := r2Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(r2BucketName),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		}); err != nil {
			return err
		}
	}
	return nil
}

// r2RunKey returns the conventional object key for a run artifact.
// name is one of: trace.md / report.md / stderr.log / result.json
func r2RunKey(runID, name string) string {
	return fmt.Sprintf("runs/%s/%s", runID, name)
}
