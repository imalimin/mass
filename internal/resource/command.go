package resource

import (
	"context"
	"errors"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type CommandOutput struct {
	ExitCode  *int   `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Duration  int64  `json:"duration_ms"`
	Detail    string `json:"error_detail,omitempty"`
	Started   bool   `json:"-"`
	Truncated bool   `json:"-"`
}

type CommandOptions struct {
	Env       []string
	SkillDirs map[string]string
}

type CommandService struct {
	Workspaces         Workspaces
	Content            *ContentStore
	Skills             *SkillCache
	Execute            func(context.Context, string, string, []string, int) (CommandOutput, error)
	ExecuteWithEnv     func(context.Context, string, string, []string, []string, int) (CommandOutput, error)
	ExecuteWithOptions func(context.Context, string, string, []string, CommandOptions, int) (CommandOutput, error)
	Env                func(run.Snapshot) ([]string, func(), error)
}

func (c *CommandService) Run(ctx context.Context, s run.Snapshot, name string, args []string) (run.ToolResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(s.Definition.ToolPolicy.TimeoutMS)*time.Millisecond)
	defer cancel()
	var extraEnv []string
	if c.Env != nil {
		var cleanup func()
		var envErr error
		extraEnv, cleanup, envErr = c.Env(s)
		if envErr != nil {
			result := dataResult(CommandOutput{Detail: envErr.Error()}, "none")
			result.Status, result.Error = "failed", "command_failed"
			return result, context.Cause(ctx)
		}
		if cleanup != nil {
			defer cleanup()
		}
	}
	var skillDirs map[string]string
	if c.Skills != nil && len(s.Definition.Skills) > 0 {
		var err error
		skillDirs, err = c.Skills.Mounts(commandCtx, c.Content, s)
		if err != nil {
			result := dataResult(CommandOutput{Detail: err.Error()}, "none")
			result.Status, result.Error = "failed", "command_failed"
			return result, context.Cause(ctx)
		}
	}
	var out CommandOutput
	started := time.Now()
	maxLog := min(s.Definition.ToolPolicy.MaxLogBytes, 64<<10)
	err := c.Workspaces.With(commandCtx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		var err error
		switch {
		case c.ExecuteWithOptions != nil:
			out, err = c.ExecuteWithOptions(commandCtx, dir.Root().Name(), name, args, CommandOptions{Env: extraEnv, SkillDirs: skillDirs}, maxLog)
		case c.ExecuteWithEnv != nil:
			out, err = c.ExecuteWithEnv(commandCtx, dir.Root().Name(), name, args, extraEnv, maxLog)
		default:
			out, err = c.Execute(commandCtx, dir.Root().Name(), name, args, maxLog)
		}
		return err
	})
	out.Duration = time.Since(started).Milliseconds()
	effect := "none"
	if out.Started {
		// The process can have written files or sent requests before exiting.
		effect = "committed"
	}
	code := ""
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "command_timeout"
	case errors.Is(err, context.Canceled):
		code = "command_cancelled"
	case out.ExitCode != nil && *out.ExitCode != 0:
		code = "command_exit"
	case err != nil:
		code = "command_failed"
	}
	if err != nil {
		out.Detail = err.Error()
	}
	result := dataResult(out, effect)
	result.Complete, result.Truncated = !out.Truncated, out.Truncated
	if code != "" {
		result.Status, result.Error = "failed", code
	}
	// A command deadline is a tool result. Cancellation of the whole Run
	// still follows the supervisor's existing lifecycle.
	return result, context.Cause(ctx)
}
