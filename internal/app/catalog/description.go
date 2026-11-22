package catalog

import (
	"context"
	"strings"

	"github.com/imalimin/mass/internal/app/identity"
	"go.yaml.in/yaml/v4"
)

// ExtractSkillDescription parses the description field from leading YAML
// frontmatter in SKILL.md. Missing or invalid frontmatter returns "".
func ExtractSkillDescription(markdown string) string {
	text := strings.TrimPrefix(markdown, "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	firstLine, rest, ok := strings.Cut(text, "\n")
	if !ok || strings.TrimRight(firstLine, " \t") != "---" {
		return ""
	}
	var block strings.Builder
	closed := false
	for len(rest) > 0 {
		line, next, hasNewline := strings.Cut(rest, "\n")
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "---" || trimmed == "..." {
			closed = true
			break
		}
		block.WriteString(line)
		block.WriteByte('\n')
		if !hasNewline {
			break
		}
		rest = next
	}
	if !closed {
		return ""
	}
	var frontmatter struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(block.String()), &frontmatter); err != nil {
		return ""
	}
	return strings.TrimSpace(frontmatter.Description)
}

// SkillDescription extracts a Skill's description from its latest non-archived
// formal version's SKILL.md, falling back to the current draft when unpublished.
func (s *Service) SkillDescription(ctx context.Context, actor, space, id string) string {
	if finder, ok := s.Repo.(interface {
		LatestFormalVersion(context.Context, string, string, string, string) (Version, bool, error)
	}); ok {
		if latest, _, err := finder.LatestFormalVersion(ctx, actor, space, "skill", id); err == nil && latest.ID != "" {
			if full, err := s.Version(ctx, actor, space, "skill", id, latest.ID); err == nil {
				return ExtractSkillDescription(full.Bundle.Document.Markdown)
			}
		}
	} else {
		var latest Version
		after := ""
		for {
			versions, err := s.Repo.CatalogVersions(ctx, actor, space, "skill", id, false, identity.Query{After: after, Limit: 100})
			if err != nil || len(versions) == 0 {
				break
			}
			for _, v := range versions {
				if v.Archived || v.Debug {
					continue
				}
				if latest.ID == "" || v.CreatedAt.After(latest.CreatedAt) || (v.CreatedAt.Equal(latest.CreatedAt) && v.ID > latest.ID) {
					latest = v
				}
			}
			if len(versions) < 100 {
				break
			}
			after = versions[len(versions)-1].ID
		}
		if latest.ID != "" {
			if full, err := s.Version(ctx, actor, space, "skill", id, latest.ID); err == nil {
				return ExtractSkillDescription(full.Bundle.Document.Markdown)
			}
		}
	}
	if draft, err := s.Draft(ctx, actor, space, "skill", id); err == nil {
		return ExtractSkillDescription(draft.Document.Markdown)
	}
	return ""
}
