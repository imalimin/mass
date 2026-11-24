//go:build !darwin

package command

import (
	"context"
	"errors"

	"github.com/imalimin/mass/internal/resource"
)

func Execute(ctx context.Context, workspace, name string, args []string, maxOutput int) (resource.CommandOutput, error) {
	return ExecuteWithEnv(ctx, workspace, name, args, nil, maxOutput)
}

func ExecuteWithEnv(context.Context, string, string, []string, []string, int) (resource.CommandOutput, error) {
	return resource.CommandOutput{}, errors.New("run_command requires the macOS Seatbelt sandbox")
}

func ExecuteWithOptions(context.Context, string, string, []string, resource.CommandOptions, int) (resource.CommandOutput, error) {
	return resource.CommandOutput{}, errors.New("run_command requires the macOS Seatbelt sandbox")
}
