package artifacts

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ImageKey is the artifacts key for a persisted handler image tarball
// (`docker save`), used to restore images after daemon restarts.
func ImageKey(image string) string {
	var b strings.Builder
	for _, c := range image {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return "images/" + b.String() + ".tar"
}

// Presigned upload/download URLs for user blobs (sidecar Blob service).
func (s *S3) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	pc := s3.NewPresignClient(s.client)
	out, err := pc.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key},
		s3.WithPresignExpires(expires))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}

func (s *S3) PresignGet(ctx context.Context, key string, expires time.Duration) (string, error) {
	pc := s3.NewPresignClient(s.client)
	out, err := pc.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key},
		s3.WithPresignExpires(expires))
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
