//go:build !darwin

package command

import (
	"context"
	"errors"

	"github.com/imalimin/mass/internal/resource"
)

func Execute(context.Context, string, string, []string, int) (resource.CommandOutput, error) {
	return resource.CommandOutput{}, errors.New("run_command requires the macOS Seatbelt sandbox")
}
