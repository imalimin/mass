// Package tos implements immutable content access with the pinned official SDK.
package tos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
	sdk "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
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

func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	out, err := s.Client.HeadObjectV2(ctx, &sdk.HeadObjectV2Input{Bucket: s.Bucket, Key: key})
	if err != nil {
		if sdk.StatusCode(err) == 404 {
			return 0, storage.ErrBlobNotFound
		}
		return 0, safeError("head_object", err)
	}
	return out.ContentLength, nil
}

func (s *Store) OpenRange(ctx context.Context, key string, start, size int64) (io.ReadCloser, error) {
	end := start + size - 1
	out, err := s.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: s.Bucket, Key: key, Range: fmt.Sprintf("bytes=%d-%d", start, end)})
	if err != nil {
		return nil, safeError("read_range", err)
	}
	if out.StatusCode != 206 || !strings.HasPrefix(out.ContentRange, fmt.Sprintf("bytes %d-%d/", start, end)) {
		out.Content.Close()
		return nil, storage.ErrIntegrity
	}
	return out.Content, nil
}

func (s *Store) Append(ctx context.Context, key string, offset int64, data []byte, media string) (int64, error) {
	out, err := s.Client.AppendObjectV2(ctx, &sdk.AppendObjectV2Input{Bucket: s.Bucket, Key: key, Offset: offset, Content: bytes.NewReader(data), ContentLength: int64(len(data)), ContentType: media, StorageClass: enum.StorageClassStandard})
	if err != nil {
		return 0, safeError("append", err)
	}
	return out.NextAppendOffset, nil
}

// Error deliberately omits signed requests, headers, provider messages and secrets.
type Error struct {
	Operation string
	Status    int
	Cause     string
}

func (e *Error) Error() string {
	if e.Status == 0 && e.Cause != "" {
		return fmt.Sprintf("TOS %s failed (transport: %s)", e.Operation, e.Cause)
	}
	return fmt.Sprintf("TOS %s failed (HTTP %d)", e.Operation, e.Status)
}
func (e *Error) HTTPStatus() int { return e.Status }
func safeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	status := sdk.StatusCode(err)
	cause := ""
	if status == 0 {
		cause = err.Error()
		if idx := strings.Index(cause, "?"); idx >= 0 {
			cause = cause[:idx]
		}
		if len(cause) > 240 {
			cause = cause[:240]
		}
	}
	return &Error{Operation: operation, Status: status, Cause: cause}
}
