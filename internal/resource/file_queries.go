package resource

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func (f *FileService) Query(ctx context.Context, s run.Snapshot, kind string, in PageInput) (run.ToolResult, error) {
	if err := f.Check(ctx, s); err != nil {
		return run.ToolResult{}, err
	}
	if in.Directory == "" {
		in.Directory = "."
	}
	if in.PathGlob == "" {
		in.PathGlob = "**"
	}
	if in.Directory != "." && !filePath(in.Directory) || len(in.Pattern) > 512 || len(in.PathGlob) > 512 {
		return run.ToolResult{}, storage.ErrInvalid
	}
	if kind == "glob_files" {
		if !doublestar.ValidatePattern(in.Pattern) {
			return run.ToolResult{}, storage.ErrInvalid
		}
	}
	var expression *regexp.Regexp
	if kind == "search_files" {
		if in.Pattern == "" || !doublestar.ValidatePattern(in.PathGlob) {
			return run.ToolResult{}, storage.ErrInvalid
		}
		if in.Mode == "" {
			in.Mode = "literal"
		}
		if in.Mode != "literal" && in.Mode != "regex" {
			return run.ToolResult{}, storage.ErrInvalid
		}
		if in.Mode == "regex" {
			var err error
			expression, err = regexp.Compile(in.Pattern)
			if err != nil {
				return run.ToolResult{}, storage.ErrInvalid
			}
		}
	}
	return f.page(ctx, s, kind, in, func() (q queryResult, err error) {
		q = queryResult{Items: []json.RawMessage{}, Complete: true}
		err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
			if err := f.Check(ctx, s); err != nil {
				return err
			}
			if in.Directory != "." {
				if err := checkParents(dir.Root(), in.Directory); err != nil {
					return err
				}
			}
			names := []string{}
			maxEntries := min(f.Limits.MaxEntries, 10000)
			var walk func(string) error
			walk = func(directory string) error {
				file, err := dir.Root().Open(directory)
				if err != nil {
					return err
				}
				defer file.Close()
				for {
					entries, e := file.ReadDir(min(100, maxEntries-q.Scanned+1))
					if e != nil && !errors.Is(e, io.EOF) {
						return e
					}
					for _, entry := range entries {
						if err := ctx.Err(); err != nil {
							return err
						}
						q.Scanned++
						if q.Scanned > maxEntries {
							q.Scanned--
							q.Complete = false
							return fs.SkipAll
						}
						name := entry.Name()
						if directory != "." {
							name = path.Join(directory, name)
						}
						if err := checkParents(dir.Root(), name); err != nil {
							return err
						}
						names = append(names, name)
						if entry.IsDir() && kind != "list_files" {
							if err = walk(name); err != nil {
								return err
							}
						}
					}
					if errors.Is(e, io.EOF) {
						break
					}
				}
				return nil
			}
			if err := walk(in.Directory); err != nil && !errors.Is(err, fs.SkipAll) {
				return err
			}
			sort.Strings(names)
			for _, name := range names {
				if len(q.Items) >= 1000 {
					q.Complete = false
					break
				}
				if err := f.Check(ctx, s); err != nil {
					return err
				}
				info, err := dir.Root().Lstat(name)
				if err != nil {
					return err
				}
				if hasMultipleLinks(info) || !info.IsDir() && !info.Mode().IsRegular() {
					return storage.ErrWorkspace
				}
				if kind == "glob_files" {
					match, _ := doublestar.Match(in.Pattern, name)
					if info.IsDir() || !match {
						continue
					}
				}
				if kind == "search_files" {
					match, _ := doublestar.Match(in.PathGlob, name)
					if info.IsDir() || !match {
						continue
					}
					if info.Size() > 8<<20-q.Bytes {
						q.Complete = false
						continue
					}
					raw, err := f.read(ctx, dir, name)
					if err != nil {
						return err
					}
					q.Bytes += int64(len(raw))
					if !utf8.Valid(raw) {
						continue
					}
					scanner := bufio.NewScanner(strings.NewReader(string(raw)))
					scanner.Buffer(make([]byte, 4096), 64<<10)
					line := 0
					sha := hashBytes(raw)
					for scanner.Scan() {
						if err := ctx.Err(); err != nil {
							return err
						}
						line++
						text := scanner.Text()
						found := strings.Contains(text, in.Pattern)
						if expression != nil {
							found = expression.MatchString(text)
						}
						if !found {
							continue
						}
						snippet := text
						truncated := false
						if len(snippet) > 1024 {
							snippet = snippet[:1024]
							for !utf8.ValidString(snippet) {
								snippet = snippet[:len(snippet)-1]
							}
							truncated = true
						}
						item, _ := json.Marshal(map[string]any{"path": name, "sha256": sha, "line": line, "snippet": snippet, "truncated": truncated})
						q.Items = append(q.Items, item)
						if len(q.Items) >= 1000 {
							q.Complete = false
							break
						}
					}
					if scanner.Err() != nil {
						q.Complete = false
					}
					continue
				}
				entryType := "file"
				if info.IsDir() {
					entryType = "directory"
				}
				item := map[string]any{"path": name, "type": entryType, "size": info.Size()}
				if !info.IsDir() {
					if info.Size() <= 8<<20-q.Bytes {
						raw, err := f.read(ctx, dir, name)
						if err != nil {
							return err
						}
						q.Bytes += int64(len(raw))
						item["sha256"] = hashBytes(raw)
					} else {
						item["sha256"] = nil
						q.Complete = false
					}
				}
				raw, _ := json.Marshal(item)
				q.Items = append(q.Items, raw)
			}
			return nil
		})
		return
	})
}
