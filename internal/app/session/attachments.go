package session

import (
	"context"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"io"
	"mime"
	"strings"
	"unicode/utf8"
)

func (s *Service) UploadAttachment(ctx context.Context, actor, space, session, name, media string, source io.Reader) (run.Attachment, error) {
	if len(name) < 1 || len(name) > 200 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\r\n\x00") {
		return run.Attachment{}, identity.ErrInvalid
	}
	if _, _, err := mime.ParseMediaType(media); err != nil || len(media) > 128 {
		return run.Attachment{}, identity.ErrInvalid
	}
	if _, err := s.Repo.RuntimeSession(ctx, actor, space, session); err != nil {
		return run.Attachment{}, err
	}
	limited := io.LimitReader(source, 8<<20+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > 8<<20 {
		return run.Attachment{}, identity.ErrInvalid
	}
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "attachment", media, strings.NewReader(string(raw)))
	if err != nil {
		return run.Attachment{}, err
	}
	a := run.Attachment{ID: uuid.NewString(), SessionID: session, Name: name, MediaType: media, Size: o.Size, Ref: Ref(o, "attachment")}
	return a, s.Repo.RegisterAttachment(ctx, actor, space, a)
}
