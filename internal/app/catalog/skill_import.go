package catalog

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

var ErrSkillDocumentMissing = errors.New("skill_document_missing")
var skillImportSeparator = regexp.MustCompile(`[^a-z0-9_-]+`)

func (s *Service) ImportSkill(ctx context.Context, actor, space, filename string, source io.Reader) (Draft, error) {
	input, err := os.CreateTemp("", "mass-skill-import-*")
	if err != nil {
		return Draft{}, err
	}
	defer os.Remove(input.Name())
	defer input.Close()
	size, err := io.Copy(input, io.LimitReader(storage.ContextReader{Context: ctx, Reader: source}, s.Content.MaxBytes+1))
	if err != nil {
		return Draft{}, err
	}
	if size > s.Content.MaxBytes {
		return Draft{}, ErrConfig
	}
	z, err := zip.NewReader(input, size)
	// Some ZIP writers prefix archive-relative paths with "./". The importer
	// validates each normalized entry before using it.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return Draft{}, ErrConfig
	}
	var document *zip.File
	for _, entry := range z.File {
		if isIgnoredSkillImportPath(entry.Name) {
			continue
		}
		if strings.TrimPrefix(entry.Name, "./") == resource.SkillDocumentPath && entry.Mode().IsRegular() {
			if document != nil {
				return Draft{}, ErrConfig
			}
			document = entry
		}
	}
	if document == nil {
		return Draft{}, ErrSkillDocumentMissing
	}
	entries := map[string]*zip.File{}
	var directories []string
	for _, entry := range z.File {
		if entry == document || isIgnoredSkillImportPath(entry.Name) {
			continue
		}
		name := strings.TrimPrefix(entry.Name, "./")
		mode := entry.Mode()
		if mode.IsDir() && name == "" {
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if !resource.ValidSkillPath(name) {
			return Draft{}, ErrConfig
		}
		switch {
		case mode.IsDir():
			directories = append(directories, name)
		case mode.IsRegular():
			if entries[name] != nil {
				return Draft{}, ErrConfig
			}
			entries[name] = entry
		default:
			return Draft{}, ErrConfig
		}
		if len(entries) > maxSkillEntries || len(directories) > maxSkillEntries {
			return Draft{}, ErrConfig
		}
	}
	markdown, err := readImportedSkillEntry(ctx, document, MaxDocument)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{Definition: newDefinition("skill", ""), Revision: 1}
	d.Name = importedSkillName(filename, d.ID)
	d.Document = Document{Markdown: string(markdown), Assets: make([]Asset, 0, len(entries)), Directories: directories}
	packageID := uuid.NewString()
	for _, entry := range z.File {
		name := strings.TrimPrefix(entry.Name, "./")
		if entries[name] != entry {
			continue
		}
		data, err := readImportedSkillEntry(ctx, entry, MaxResource)
		if err != nil {
			return Draft{}, err
		}
		media := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
		if media == "" {
			media = "application/octet-stream"
		}
		sum := sha256.Sum256(data)
		d.Document.Assets = append(d.Document.Assets, Asset{Path: name, ObjectID: packageID, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), MediaType: media})
	}
	if _, err := ValidateDocument(d.Kind, d.Name, d.Document, false); err != nil {
		return Draft{}, err
	}
	d, err = s.writeSkillPackage(ctx, actor, space, d, d.Revision, packageID, func(asset Asset) ([]byte, error) {
		return readImportedSkillEntry(ctx, entries[asset.Path], MaxResource)
	})
	if err != nil {
		return Draft{}, err
	}
	return d, s.Repo.CatalogCreate(ctx, actor, space, d)
}

func isIgnoredSkillImportPath(name string) bool {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if trimmed == "" {
		return false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "__MACOSX" {
			return true
		}
	}
	return false
}

func importedSkillName(filename, id string) string {
	name := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	name = strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	name = strings.Trim(skillImportSeparator.ReplaceAllString(name, "-"), "-_")
	if name == "" {
		return "skill-" + id[:8]
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "skill-" + name
	}
	return name[:min(len(name), 64)]
}

func readImportedSkillEntry(ctx context.Context, entry *zip.File, max int64) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(max) {
		return nil, ErrConfig
	}
	in, err := entry.Open()
	if err != nil {
		return nil, ErrConfig
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: in}, max+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || uint64(len(data)) != entry.UncompressedSize64 || int64(len(data)) > max {
		return nil, ErrConfig
	}
	return data, nil
}
