package resource

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/imalimin/mass/internal/domain/storage"
)

type ProjectFileError struct{ Path, Reason string }

func (e *ProjectFileError) Error() string { return fmt.Sprintf("%s：%s", e.Path, e.Reason) }

type ProjectFiles struct {
	Files       map[string][]byte
	Directories []string
	LocalFiles  []string
	Oversized   []string
}

func projectPath(name string) bool {
	return name == "AGENTS.md" || name == "agent.yaml" || name == "proj.yaml" || strings.Count(name, "/") >= 1 && strings.HasPrefix(name, "skills/")
}

func (s *SnapshotStore) ReadProjectFiles(ctx context.Context, actor, space string, snapshot storage.Snapshot) (ProjectFiles, error) {
	result := ProjectFiles{Files: make(map[string][]byte)}
	manifest, err := readWorkspaceManifest(ctx, s.Content, actor, space, snapshot)
	if err != nil {
		return result, err
	}
	for _, name := range manifest.LocalFiles {
		if projectPath(name) || name == "skills" {
			result.LocalFiles = append(result.LocalFiles, name)
		}
	}
	expected := make(map[string]storage.Entry)
	for _, entry := range manifest.Entries {
		if projectPath(entry.Path) {
			expected[entry.Path] = entry
		}
	}
	object, archive, err := s.Content.Read(ctx, actor, space, snapshot.ObjectID)
	if err != nil {
		return result, err
	}
	defer archive.Close()
	if object.SHA256 != snapshot.SHA256 {
		return result, storage.ErrIntegrity
	}
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return result, storage.ErrIntegrity
	}
	defer gzipReader.Close()
	reader := tar.NewReader(storage.ContextReader{Context: ctx, Reader: gzipReader})
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, err
		}
		if !projectPath(header.Name) {
			continue
		}
		entry, found := expected[header.Name]
		if !found || !filePath(header.Name) || entry.Size != header.Size {
			return result, storage.ErrIntegrity
		}
		delete(expected, header.Name)
		switch {
		case entry.Type == "directory" && header.Typeflag == tar.TypeDir:
			result.Directories = append(result.Directories, header.Name)
		case entry.Type == "file" && header.Typeflag == tar.TypeReg:
			if entry.Size > SkillFileMaxBytes || total+entry.Size > storage.MaxContentObjectBytes {
				result.Oversized = append(result.Oversized, header.Name)
				continue
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return result, err
			}
			if entry.Size != int64(len(raw)) || hashBytes(raw) != entry.SHA256 {
				return result, storage.ErrIntegrity
			}
			total += entry.Size
			result.Files[header.Name] = raw
		default:
			return result, storage.ErrIntegrity
		}
	}
	if len(expected) != 0 {
		return result, storage.ErrIntegrity
	}
	return result, nil
}
