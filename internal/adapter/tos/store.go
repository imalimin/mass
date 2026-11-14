// Package tos implements immutable content access with the pinned official SDK.
package tos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
	sdk "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type Store struct {
	Client *sdk.ClientV2
	Bucket string
}

func New(endpoint, region, bucket, accessKey, secretKey string) (*Store, error) {
	client, err := sdk.NewClientV2(endpoint, sdk.WithRegion(region), sdk.WithCredentials(sdk.NewStaticCredentials(accessKey, secretKey)), sdk.WithMaxRetryCount(0), sdk.WithRequestTimeout(30*time.Second))
	if err != nil {
		return nil, errors.New("invalid TOS client configuration")
	}
	return &Store{client, bucket}, nil
}
func (s *Store) URI(key string) string {
	return "tos://" + s.Bucket + "/" + strings.TrimPrefix(key, "/")
}
func (s *Store) PutNew(ctx context.Context, key string, r io.Reader, size int64, media string) (storage.BlobInfo, error) {
	return s.put(ctx, key, r, size, media, true)
}
func (s *Store) Overwrite(ctx context.Context, key string, r io.Reader, size int64, media string) (storage.BlobInfo, error) {
	return s.put(ctx, key, r, size, media, false)
}
func (s *Store) put(ctx context.Context, key string, r io.Reader, size int64, media string, immutable bool) (storage.BlobInfo, error) {
	out, err := s.Client.PutObjectV2(ctx, &sdk.PutObjectV2Input{PutObjectBasicInput: sdk.PutObjectBasicInput{Bucket: s.Bucket, Key: key, ContentLength: size, ContentType: media, ForbidOverwrite: immutable}, Content: r})
	if err != nil {
		return storage.BlobInfo{}, safeError("upload", err)
	}
	return storage.BlobInfo{ETag: out.ETag, VersionID: out.VersionID}, nil
}
func (s *Store) Open(ctx context.Context, key, version string) (io.ReadCloser, error) {
	out, err := s.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: s.Bucket, Key: key, VersionID: version})
	if err != nil {
		if sdk.StatusCode(err) == 404 {
			return nil, storage.ErrBlobNotFound
		}
		return nil, safeError("read", err)
	}
	return out.Content, nil
}
func (s *Store) Check(ctx context.Context) error {
	_, err := s.Client.HeadBucket(ctx, &sdk.HeadBucketInput{Bucket: s.Bucket})
	return safeError("head_bucket", err)
}

// Error deliberately omits signed requests, headers, provider messages and secrets.
type Error struct {
	Operation string
	Status    int
}

func (e *Error) Error() string { return fmt.Sprintf("TOS %s failed (HTTP %d)", e.Operation, e.Status) }
func safeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{Operation: operation, Status: sdk.StatusCode(err)}
}
