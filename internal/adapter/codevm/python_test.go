package codevm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type pythonHostFunc func(string, json.RawMessage) (json.RawMessage, error)

func (f pythonHostFunc) Invoke(op string, args json.RawMessage) (json.RawMessage, error) {
	return f(op, args)
}

func TestPythonExecutionErrors(t *testing.T) {
	runner := New(1, t.TempDir())
	policy := run.ToolPolicy{MaxCodeBytes: 65536, MaxInputBytes: 4096, MaxOutputBytes: 4096, MaxLogBytes: 128}
	input := run.CodeInput{Language: "python", Input: json.RawMessage("null")}
	for _, test := range []struct{ source, detail string }{
		{"def broken(:\n    pass", "SyntaxError"},
		{"raise ValueError('fix the input')", "fix the input"},
	} {
		input.Code = test.source
		out, err := runner.Execute(context.Background(), input, nil, policy)
		if err != nil || out.Error != "code_exception" || !strings.Contains(out.Detail, test.detail) || !strings.Contains(out.Detail, "mass-code.py") {
			t.Fatalf("missing diagnostic: %+v, err=%v", out, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	input.Code = "while True:\n    pass"
	out, err := runner.Execute(ctx, input, nil, policy)
	if !errors.Is(err, context.DeadlineExceeded) || out.Error != "code_timeout" {
		t.Fatalf("Python was not interrupted: %+v, err=%v", out, err)
	}
	hostCtx, stopHost := context.WithCancel(context.Background())
	defer stopHost()
	input.Code = "x = 1\nhost('fs.mkdir', {'arguments': ['parent/child']})"
	out, err = runner.Execute(hostCtx, input, pythonHostFunc(func(string, json.RawMessage) (json.RawMessage, error) {
		stopHost()
		return nil, hostCtx.Err()
	}), policy)
	if !errors.Is(err, context.Canceled) || !strings.Contains(out.Detail, `mass-code.py:2: host call "fs.mkdir" interrupted`) {
		t.Fatalf("missing interrupted host call detail: %+v, err=%v", out, err)
	}
	input.Code = "print('ready')\nreturn 42"
	out, err = runner.Execute(context.Background(), input, nil, policy)
	if err != nil || out.Error != "" || string(out.Value) != "42" || len(out.Logs) != 1 || !strings.Contains(string(out.Logs[0]), "ready") {
		t.Fatalf("unexpected result after a failed execution: %+v, err=%v", out, err)
	}
}
