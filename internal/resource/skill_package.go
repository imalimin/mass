package resource

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/imalimin/mass/internal/domain/storage"
)

const SkillPackageKind = "definition.skill-package"
const SkillFileMaxBytes = 8 << 20
const SkillDocumentPath = "SKILL.md"
const SkillManifestPath = "bundle.json"

func ValidSkillPath(path string) bool {
	root, _, _ := strings.Cut(path, "/")
	return fs.ValidPath(path) && path != "." && len(path) <= 512 && !strings.ContainsAny(path, "\\:\x00") &&
		!strings.EqualFold(root, SkillDocumentPath) && !strings.EqualFold(root, SkillManifestPath)
}

func OpenSkillPackage(f *File, o storage.Object) (*zip.Reader, error) {
	if o.Kind != SkillPackageKind {
		return nil, storage.ErrInvalid
	}
	z, err := zip.NewReader(f, o.Size)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Skill package", storage.ErrIntegrity)
	}
	return z, nil
}

func ReadSkillEntry(z *zip.Reader, name string, max int64) ([]byte, error) {
	r, err := z.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: Skill entry %s", storage.ErrIntegrity, name)
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, storage.ErrIntegrity
	}
	return raw, nil
}

func ReadSkillPackageFile(ctx context.Context, content *ContentStore, actor, space, id, path string) ([]byte, storage.Object, error) {
	if !ValidSkillPath(path) {
		return nil, storage.Object{}, storage.ErrInvalid
	}
	o, f, err := content.Read(ctx, actor, space, id)
	if err != nil {
		return nil, o, err
	}
	defer f.Close()
	z, err := OpenSkillPackage(f, o)
	if err != nil {
		return nil, o, err
	}
	raw, err := ReadSkillEntry(z, path, SkillFileMaxBytes)
	return raw, o, err
}
