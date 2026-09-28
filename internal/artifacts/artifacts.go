// Package artifacts stores bundles/<name>/<ver>/{src.zip,handler,sha256}
// plus user blobs. Two backends: local dir (dev default) and S3-compat
// (RustFS in compose). NewFromEnv picks S3 when RUSTFS_ENDPOINT is set,
// otherwise ARTIFACTS_DIR or ./data/bundles.
package artifacts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

func BundleKey(name, ver, file string) string {
	return "bundles/" + name + "/" + ver + "/" + file
}

func NewFromEnv() Store {
	endpoint := os.Getenv("RUSTFS_ENDPOINT")
	if endpoint != "" {
		bucket := os.Getenv("RUSTFS_BUCKET")
		if bucket == "" {
			bucket = "actions"
		}
		s, err := NewS3(endpoint, bucket,
			os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"))
		if err == nil {
			return s
		}
	}
	dir := os.Getenv("ARTIFACTS_DIR")
	if dir == "" {
		dir = filepath.Join("data", "bundles")
	}
	return NewLocal(dir)
}

// --- local ---

type Local struct{ dir string }

func NewLocal(dir string) *Local { return &Local{dir: dir} }

func (l *Local) path(key string) string {
	clean := filepath.Clean("/" + strings.ReplaceAll(key, "\\", "/"))
	return filepath.Join(l.dir, clean)
}

func (l *Local) Put(_ context.Context, key string, data []byte) error {
	p := l.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (l *Local) Get(_ context.Context, key string) ([]byte, error) {
	return os.ReadFile(l.path(key))
}

// --- S3 (RustFS) ---

type S3 struct {
	client *s3.Client
	bucket string
}

func NewS3(endpoint, bucket, accessKey, secretKey string) (*S3, error) {
	if bucket == "" {
		return nil, fmt.Errorf("bucket required")
	}
	client := s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
	})
	return &S3{client: client, bucket: bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, 512<<20))
}
