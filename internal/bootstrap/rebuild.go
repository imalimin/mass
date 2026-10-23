package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/domain/storage"
)

func RebuildViews(ctx context.Context, c Config) error {
	store, err := mariadb.Open(ctx, os.Getenv(c.Database.DSNEnv))
	if err != nil {
		return err
	}
	defer store.DB.Close()
	if err = store.CheckSchema(ctx); err != nil {
		return err
	}
	blobs, err := tos.New(os.Getenv(c.TOS.EndpointEnv), os.Getenv(c.TOS.RegionEnv), os.Getenv(c.TOS.BucketEnv), os.Getenv(c.TOS.AccessKeyEnv), os.Getenv(c.TOS.SecretKeyEnv))
	if err != nil {
		return err
	}
	defer blobs.Client.Close()
	return store.RebuildViews(ctx, func(ctx context.Context, o storage.Object) error {
		reader, err := blobs.Open(ctx, o.Key, o.VersionID)
		if err != nil {
			return err
		}
		defer reader.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(storage.ContextReader{Context: ctx, Reader: reader}, o.Size+1))
		if err != nil {
			return err
		}
		if n != o.Size || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
			return storage.ErrIntegrity
		}
		return nil
	})
}
