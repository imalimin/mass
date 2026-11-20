package evaluation

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidateDataset(in DatasetInput) error {
	if !utf8.ValidString(in.Name) || strings.TrimSpace(in.Name) != in.Name || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 100 || strings.ContainsAny(in.Name, "\x00\r\n") || in.Cases == nil || len(in.Cases) > 100 {
		return identity.ErrInvalid
	}
	for _, c := range in.Cases {
		if len(c.Turns) < 1 || len(c.Turns) > 10 {
			return identity.ErrInvalid
		}
		for _, turn := range c.Turns {
			if len(turn.Content) != 1 {
				return identity.ErrInvalid
			}
			p := turn.Content[0]
			if p.Type != "text" {
				if p.Type == "image_url" || p.Type == "video_url" {
					return identity.ErrCapability
				}
				return identity.ErrInvalid
			}
			if p.ImageURL != nil || p.VideoURL != nil || !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) == "" || len(p.Text) > 256<<10 {
				return identity.ErrInvalid
			}
		}
	}
	return nil
}
func validateExperiment(in ExperimentInput) error {
	const maxStoredConcurrency = 1<<16 - 1 // evaluation_experiments.concurrency is SMALLINT UNSIGNED.
	if !storage.ValidShortID(in.DatasetID) || !storage.ValidShortID(in.AgentID) || !storage.ValidID(in.AgentVersionID) || !keyPattern.MatchString(in.Key) || in.Concurrency < 1 || in.Concurrency > maxStoredConcurrency {
		return identity.ErrInvalid
	}
	return nil
}
