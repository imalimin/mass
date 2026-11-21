package resource

import (
	"context"
	"errors"
	"os"
	"path"

	"github.com/imalimin/mass/internal/domain/storage"
)

func (w *RunWorkspaces) InitializeIteration(ctx context.Context, actor, space, session string) error {
	if w.Available(space, session) {
		return nil
	}
	return w.Workspaces.With(ctx, space, session, func(dir WorkspaceDir) error {
		if w.Available(space, session) {
			return nil
		}
		seeded, err := w.seedIteration(ctx, actor, space, session, dir)
		if seeded && err == nil {
			w.Mark(space, session)
		}
		return err
	})
}

func (w *RunWorkspaces) seedIteration(ctx context.Context, actor, space, session string, dir WorkspaceDir) (bool, error) {
	if w.Iteration == nil {
		return false, nil
	}
	var total int64
	started := false
	seeded, err := w.Iteration(ctx, actor, space, session, func(name string, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !filePath(name) {
			return storage.ErrIntegrity
		}
		size := int64(len(data))
		if size > w.Limits.MaxArchiveFileBytes || size > w.Limits.MaxTotalBytes-total {
			return storage.ErrWorkspace
		}
		if !started {
			if err := dir.Clear(ctx, nil); err != nil {
				return err
			}
			started = true
		}
		root := dir.Root()
		if data == nil {
			return root.MkdirAll(name, 0700)
		}
		if parent := path.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0700); err != nil {
				return err
			}
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		total += size
		_, err = file.Write(data)
		return errors.Join(err, file.Close())
	})
	if err != nil && started {
		return false, errors.Join(err, dir.Clear(context.WithoutCancel(ctx), nil))
	}
	return seeded, err
}
