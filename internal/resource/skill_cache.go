package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

const SkillsDirEnv = "MASS_SKILLS_DIR"

// SkillCache unpacks immutable bound Skill packages into a content-addressed
// read-only cache outside the session workspace so both read_file and
// run_command can access SKILL.md and skill resources without polluting snapshots.
type SkillCache struct {
	Root               string
	SharedSkillPackage func(context.Context, string, run.Skill) (storage.Object, *File, error)
	mu                 sync.Mutex
}

// Mounts ensures every Skill bound to the run is unpacked in the cache and
// returns a map of mount folder name -> resolved absolute cache directory.
func (c *SkillCache) Mounts(ctx context.Context, content *ContentStore, s run.Snapshot) (map[string]string, error) {
	if c == nil || c.Root == "" || len(s.Definition.Skills) == 0 {
		return nil, nil
	}
	names := run.SkillMountNames(s.Definition.Skills)
	mounts := make(map[string]string, len(s.Definition.Skills))
	for i, skill := range s.Definition.Skills {
		dir, err := c.ensure(ctx, content, s, skill)
		if err != nil {
			return nil, err
		}
		mounts[names[i]] = dir
	}
	return mounts, nil
}

func (c *SkillCache) ensure(ctx context.Context, content *ContentStore, s run.Snapshot, skill run.Skill) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := skill.SHA256
	if key == "" {
		return "", storage.ErrIntegrity
	}
	if err := os.MkdirAll(c.Root, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(c.Root, key)
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return resolveDir(target)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return resolveDir(target)
	}

	temp, err := os.MkdirTemp(c.Root, ".unpack-"+uuid.NewString()+"-")
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temp)
		}
	}()

	if err := c.unpackPackage(ctx, content, s, skill, temp); err != nil {
		return "", err
	}
	if err := os.Rename(temp, target); err != nil {
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			return resolveDir(target)
		}
		return "", err
	}
	cleanup = false
	return resolveDir(target)
}

func (c *SkillCache) unpackPackage(ctx context.Context, content *ContentStore, s run.Snapshot, skill run.Skill, dst string) error {
	var object storage.Object
	var file *File
	var err error
	if s.AgentSourceSpaceID != "" {
		if c.SharedSkillPackage == nil {
			return storage.ErrIntegrity
		}
		object, file, err = c.SharedSkillPackage(ctx, s.AgentSourceSpaceID, skill)
	} else {
		if content == nil || skill.ObjectID == "" {
			return storage.ErrIntegrity
		}
		object, file, err = content.Read(ctx, s.ActorID, s.SpaceID, skill.ObjectID)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if object.SHA256 != skill.SHA256 {
		return storage.ErrIntegrity
	}
	zipped, err := OpenSkillPackage(file, object)
	if err != nil {
		return err
	}
	manifestRaw, err := ReadSkillEntry(zipped, SkillManifestPath, 4<<20)
	if err != nil {
		return err
	}
	var bundle struct {
		Document struct {
			Assets      []run.SkillResource `json:"assets"`
			Directories []string            `json:"directories"`
		} `json:"document"`
	}
	if err := json.Unmarshal(manifestRaw, &bundle); err != nil {
		return storage.ErrIntegrity
	}
	docRaw, err := ReadSkillEntry(zipped, SkillDocumentPath, 256<<10)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := writeSkillCacheFile(root, SkillDocumentPath, docRaw); err != nil {
		return err
	}
	for _, dir := range bundle.Document.Directories {
		if !ValidSkillPath(dir) {
			return storage.ErrIntegrity
		}
		if err := root.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	for _, asset := range bundle.Document.Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ValidSkillPath(asset.Path) {
			return storage.ErrIntegrity
		}
		raw, err := ReadSkillEntry(zipped, asset.Path, SkillFileMaxBytes)
		if err != nil {
			return err
		}
		if int64(len(raw)) != asset.Size || hashBytes(raw) != asset.SHA256 {
			return storage.ErrIntegrity
		}
		if err := writeSkillCacheFile(root, asset.Path, raw); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillCacheFile(root *os.Root, rel string, data []byte) error {
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0700); err != nil {
			return err
		}
	}
	mode := os.FileMode(0600)
	if bytes.HasPrefix(data, []byte("#!")) || strings.HasPrefix(rel, "scripts/") {
		mode = 0700
	}
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

func resolveDir(dir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
