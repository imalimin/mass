package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/storage"
	"go.yaml.in/yaml/v4"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var Tools = []string{"list_files", "glob_files", "search_files", "read_file", "write_file", "edit_file", "move_file", "delete_file", "list_skill_resources", "read_skill_resource", "read_attachment", "run_code", "update_plan", "publish_artifact"}

func ValidName(s string) bool { return namePattern.MatchString(s) }
func ValidLabel(s string) bool {
	return versionPattern.MatchString(s) && !strings.EqualFold(s, "latest")
}
func ValidPath(s string) bool {
	return utf8.ValidString(s) && fs.ValidPath(s) && strings.HasPrefix(s, "assets/") && len(s) <= 512 && !strings.ContainsAny(s, "\\:\x00") && !strings.HasSuffix(s, "/")
}
func ValidateDocument(kind, name string, doc Document, publish bool) (*Config, error) {
	if !ValidName(name) || (kind != "agent" && kind != "skill") || !utf8.ValidString(doc.Markdown) || !utf8.ValidString(doc.ConfigYAML) || len(doc.Markdown) > MaxDocument || len(doc.ConfigYAML) > 64<<10 || len(doc.Assets) > 100 {
		return nil, ErrConfig
	}
	seen := map[string]bool{}
	for _, a := range doc.Assets {
		key := strings.ToLower(a.Path)
		if !ValidPath(a.Path) || seen[key] || !storage.ValidID(a.ObjectID) || !hashPattern.MatchString(a.SHA256) || a.Size < 0 || a.Size > MaxResource {
			return nil, ErrConfig
		}
		seen[key] = true
	}
	for p := range seen {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			if seen[strings.Join(parts[:i], "/")] {
				return nil, ErrConfig
			}
		}
	}
	if publish && strings.TrimSpace(doc.Markdown) == "" {
		return nil, ErrConfig
	}
	if kind == "skill" {
		if doc.ConfigYAML != "" {
			return nil, ErrConfig
		}
		return nil, nil
	}
	if !publish && strings.TrimSpace(doc.ConfigYAML) == "" {
		return nil, nil
	}
	var tree yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(doc.ConfigYAML))
	if err := dec.Decode(&tree); err != nil {
		return nil, ErrConfig
	}
	var walk func(*yaml.Node, int) bool
	walk = func(n *yaml.Node, depth int) bool {
		if depth > 12 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Tag != "!!str" || k.Value == "<<" || keys[k.Value] {
					return false
				}
				keys[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if !walk(c, depth+1) {
				return false
			}
		}
		return true
	}
	if !walk(&tree, 0) {
		return nil, ErrConfig
	}
	var extra yaml.Node
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrConfig
	}
	dec = yaml.NewDecoder(strings.NewReader(doc.ConfigYAML))
	dec.KnownFields(true)
	var c Config
	if dec.Decode(&c) != nil || c.SchemaVersion != 1 || c.Name != name || !storage.ValidID(c.Model) || len(c.Skills) > 32 || len(c.Tools) > len(Tools) {
		return nil, ErrConfig
	}
	ids := map[string]bool{}
	for _, r := range c.Skills {
		if !storage.ValidID(r.ID) || !ValidLabel(r.Version) || ids[r.ID] {
			return nil, ErrConfig
		}
		ids[r.ID] = true
	}
	allowed := map[string]bool{}
	for _, t := range Tools {
		allowed[t] = true
	}
	used := map[string]bool{}
	for _, t := range c.Tools {
		if !allowed[t] || used[t] {
			return nil, ErrConfig
		}
		used[t] = true
	}
	if c.Context.MaxInputTokens < 1 || c.Context.MaxInputTokens > 2000000 || c.Context.ReservedOutputTokens < 1 || c.Context.ReservedOutputTokens > 1000000 || (c.Context.Compression != "summary" && c.Context.Compression != "reject") || c.Limits.MaxSteps < 1 || c.Limits.MaxSteps > 1000 || c.Limits.TimeoutSeconds < 1 || c.Limits.TimeoutSeconds > 3600 {
		return nil, ErrConfig
	}
	return &c, nil
}
func Equal(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
