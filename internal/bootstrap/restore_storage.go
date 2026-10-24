package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/domain/storage"
)

// RestoreStorage copies retained TOS bytes into a distinct namespace and then
// atomically changes the restored database's location indexes. No model runs.
func RestoreStorage(ctx context.Context, source, target Config, expectedDatabase string) (int, error) {
	fromDB, err := mysql.ParseDSN(os.Getenv(source.Database.DSNEnv))
	if err != nil {
		return 0, errors.New("invalid source database configuration")
	}
	toDB, err := mysql.ParseDSN(os.Getenv(target.Database.DSNEnv))
	if err != nil {
		return 0, errors.New("invalid target database configuration")
	}
	if toDB.DBName == "" || fromDB.DBName == toDB.DBName || expectedDatabase != toDB.DBName {
		return 0, errors.New("restore requires a distinct database matching --expect-database")
	}
	fromPrefix, toPrefix := strings.TrimSuffix(source.TOS.Prefix, "/")+"/", strings.TrimSuffix(target.TOS.Prefix, "/")+"/"
	if strings.HasPrefix(fromPrefix, toPrefix) || strings.HasPrefix(toPrefix, fromPrefix) {
		return 0, errors.New("restore requires disjoint source and target object prefixes")
	}
	open := func(c Config) (*tos.Store, error) {
		return tos.New(os.Getenv(c.TOS.EndpointEnv), os.Getenv(c.TOS.RegionEnv), os.Getenv(c.TOS.BucketEnv), os.Getenv(c.TOS.AccessKeyEnv), os.Getenv(c.TOS.SecretKeyEnv))
	}
	from, err := open(source)
	if err != nil {
		return 0, err
	}
	defer from.Client.Close()
	to, err := open(target)
	if err != nil {
		return 0, err
	}
	defer to.Client.Close()
	key, _ := base64.StdEncoding.DecodeString(os.Getenv(target.Secrets.MasterKeyEnv))
	cipher, err := secrets.New(key)
	if err != nil {
		return 0, err
	}
	store, err := mariadb.Open(ctx, os.Getenv(target.Database.DSNEnv))
	if err != nil {
		return 0, err
	}
	defer store.DB.Close()
	return store.RelocateStorage(ctx, expectedDatabase, func(space, id, key string, ciphertext []byte) error {
		plain, err := cipher.Open(space, id, key, ciphertext)
		clear(plain)
		return err
	}, func(ctx context.Context, o storage.Object) (storage.Object, error) {
		if !storage.ValidID(o.SpaceID) || !storage.ValidID(o.ID) || o.Size < 0 || o.Size > target.Storage.MaxObjectBytes {
			return o, storage.ErrIntegrity
		}
		next := o
		next.Key = toPrefix + o.SpaceID + "/objects/" + o.ID
		next.URI = to.URI(next.Key)
		next.VersionID = ""
		next.ETag = ""
		if o.Key == next.Key && o.URI == next.URI {
			r, err := to.Open(ctx, o.Key, o.VersionID)
			if err != nil {
				return o, err
			}
			defer r.Close()
			return o, checkObject(ctx, r, o, io.Discard)
		}
		// A retained backup may live in another bucket. Its keys and bytes must
		// match the original index; original version IDs apply only in the
		// original bucket. A backup copy is verified by size/hash instead.
		location, validURI := strings.CutPrefix(o.URI, "tos://")
		bucket, logicalKey, hasKey := strings.Cut(location, "/")
		if !strings.HasPrefix(o.Key, fromPrefix) || !validURI || !hasKey || bucket == "" || logicalKey != o.Key {
			return o, errors.New("object is outside the declared source namespace")
		}
		sourceVersion := o.VersionID
		if o.URI != from.URI(o.Key) {
			sourceVersion = ""
		}
		r, err := from.Open(ctx, o.Key, sourceVersion)
		if err != nil {
			return o, err
		}
		file, err := os.CreateTemp("", "mass-restore-*")
		if err != nil {
			r.Close()
			return o, err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		err = checkObject(ctx, r, o, file)
		r.Close()
		if err != nil {
			return o, err
		}
		if _, err = file.Seek(0, 0); err != nil {
			return o, err
		}
		info, putErr := to.PutNew(ctx, next.Key, storage.ContextReader{Context: ctx, Reader: file}, o.Size, o.MediaType)
		// Read-back resolves a lost acknowledgment or an immutable retry; mismatched
		// existing bytes fail before any database location is changed.
		verified, err := to.Open(ctx, next.Key, "")
		if err != nil {
			return o, errors.Join(putErr, err)
		}
		err = checkObject(ctx, verified, next, io.Discard)
		verified.Close()
		if err != nil {
			return o, err
		}
		next.ETag, next.VersionID = info.ETag, info.VersionID
		return next, nil
	})
}

func checkObject(ctx context.Context, r io.Reader, o storage.Object, destination io.Writer) error {
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(storage.ContextReader{Context: ctx, Reader: r}, o.Size+1))
	if err != nil {
		return err
	}
	if n != o.Size || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
		return storage.ErrIntegrity
	}
	return nil
}
