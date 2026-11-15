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
	"github.com/imalimin/mass/internal/adapter/archive"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/sessionlog"
)

// RestoreStorage copies retained TOS bytes into a distinct namespace.
// Session and catalog keys are resolved using the target configuration.
func RestoreStorage(ctx context.Context, source, target Config, expectedDatabase string) (result storage.RestoreResult, err error) {
	fromDB, err := mysql.ParseDSN(os.Getenv(source.Database.DSNEnv))
	if err != nil {
		return result, errors.New("invalid source database configuration")
	}
	toDB, err := mysql.ParseDSN(os.Getenv(target.Database.DSNEnv))
	if err != nil {
		return result, errors.New("invalid target database configuration")
	}
	if toDB.DBName == "" || fromDB.DBName == toDB.DBName || expectedDatabase != toDB.DBName {
		return result, errors.New("restore requires a distinct database matching --expect-database")
	}
	fromPrefix, toPrefix := strings.TrimSuffix(source.TOS.Prefix, "/")+"/", strings.TrimSuffix(target.TOS.Prefix, "/")+"/"
	if strings.HasPrefix(fromPrefix, toPrefix) || strings.HasPrefix(toPrefix, fromPrefix) {
		return result, errors.New("restore requires disjoint source and target object prefixes")
	}
	open := func(c Config) (*tos.Store, error) {
		return tos.New(os.Getenv(c.TOS.EndpointEnv), os.Getenv(c.TOS.RegionEnv), os.Getenv(c.TOS.BucketEnv), os.Getenv(c.TOS.AccessKeyEnv), os.Getenv(c.TOS.SecretKeyEnv))
	}
	from, err := open(source)
	if err != nil {
		return result, err
	}
	defer from.Client.Close()
	to, err := open(target)
	if err != nil {
		return result, err
	}
	defer to.Client.Close()
	key, _ := base64.StdEncoding.DecodeString(os.Getenv(target.Secrets.MasterKeyEnv))
	cipher, err := secrets.New(key)
	if err != nil {
		return result, err
	}
	store, err := mariadb.Open(ctx, os.Getenv(target.Database.DSNEnv))
	if err != nil {
		return result, err
	}
	defer store.DB.Close()
	if err := store.CheckSchema(ctx); err != nil {
		return result, err
	}
	lease, err := store.OpenRuntime(ctx)
	if err != nil {
		return result, errors.New("restore requires the target service to be stopped")
	}
	defer lease.Close()
	var active int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE status IN ('queued','running')").Scan(&active); err != nil {
		return result, err
	}
	if active != 0 {
		return result, errors.New("restore requires a quiescent backup with no queued or running runs")
	}
	rows, err := store.DB.QueryContext(ctx, "SELECT space_id,secret_binding_id,key_id,ciphertext FROM model_configs")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var space, id, key string
		var ciphertext []byte
		if err = rows.Scan(&space, &id, &key, &ciphertext); err == nil {
			plain, err := cipher.Open(space, id, key, ciphertext)
			clear(plain)
			if err != nil {
				rows.Close()
				return result, errors.New("restore master key cannot decrypt all model keys")
			}
		}
		if err != nil {
			rows.Close()
			return result, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	copyObject := func(ctx context.Context, o storage.Object) (storage.Object, error) {
		if err := lease.Check(ctx); err != nil {
			return o, err
		}
		if !storage.ValidShortID(o.SpaceID) || !storage.ValidID(o.ID) || o.Size < 0 || o.Size > target.Storage.MaxObjectBytes {
			return o, storage.ErrIntegrity
		}
		if strings.HasPrefix(o.Key, toPrefix+o.SpaceID+"/") && o.URI == to.URI(o.Key) {
			r, err := to.Open(ctx, o.Key, o.VersionID)
			if err != nil {
				return o, err
			}
			defer r.Close()
			return o, checkObject(ctx, r, o, io.Discard)
		}
		next := o
		next.Key = toPrefix + strings.TrimPrefix(o.Key, fromPrefix)
		next.URI = to.URI(next.Key)
		next.VersionID = ""
		next.ETag = ""
		// A retained backup may live in another bucket. Its keys and bytes must
		// match the original index; original version IDs apply only in the
		// original bucket. A backup copy is verified by size/hash instead.
		location, validURI := strings.CutPrefix(o.URI, "tos://")
		bucket, logicalKey, hasKey := strings.Cut(location, "/")
		if !strings.HasPrefix(o.Key, fromPrefix+o.SpaceID+"/") || !validURI || !hasKey || bucket == "" || logicalKey != o.Key {
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
	}
	catalog := &archive.Catalog{Blobs: from, Prefix: source.TOS.Prefix}
	runtime := &archive.Runtime{Log: &sessionlog.Store{Blobs: from, Prefix: source.TOS.Prefix}}
	rows, err = store.DB.QueryContext(ctx, "SELECT id FROM spaces ORDER BY id")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		count, err := catalog.CopyCatalog(ctx, to, target.TOS.Prefix, id, copyObject)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Relocated += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = store.DB.QueryContext(ctx, "SELECT space_id,id FROM sessions ORDER BY space_id,id")
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var space, id string
		if err = rows.Scan(&space, &id); err != nil {
			return result, err
		}
		count, err := runtime.CopySession(ctx, to, target.TOS.Prefix, space, id, copyObject)
		if err != nil {
			return result, err
		}
		result.Relocated += count
	}
	return result, rows.Err()
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
