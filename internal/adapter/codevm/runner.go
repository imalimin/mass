package codevm

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/imalimin/mass/internal/adapter/javascript"
	"github.com/imalimin/mass/internal/domain/run"
)

type Runner struct {
	slots chan struct{}
	js    javascript.Runner
	dir   string
}

func New(maxConcurrent int, dir string) *Runner {
	return &Runner{slots: make(chan struct{}, max(1, maxConcurrent)), js: javascript.Runner{MaxConcurrent: max(1, maxConcurrent)}, dir: dir}
}

func (r *Runner) Execute(ctx context.Context, input run.CodeInput, host run.CodeHost, policy run.ToolPolicy) (run.CodeOutput, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return run.CodeOutput{}, ctx.Err()
	}
	if len(input.Code) > policy.MaxCodeBytes {
		return run.CodeOutput{Error: "invalid_code_input", Detail: "Source exceeds the configured code size limit"}, nil
	}
	switch input.Language {
	case "javascript":
		out, err := r.js.Execute(ctx, input, host, policy)
		out.Engine = "goja-e6862644b38f"
		return out, err
	case "python", "go":
		result := execution{
			host: host, policy: policy,
			out: run.CodeOutput{Logs: []json.RawMessage{}, LogsComplete: true},
		}
		var err error
		if input.Language == "python" {
			err = result.python(ctx, input)
		} else {
			err = result.goCode(ctx, input, r.dir)
		}
		return result.out, err
	default:
		return run.CodeOutput{}, run.ErrProtocol
	}
}

type execution struct {
	host     run.CodeHost
	policy   run.ToolPolicy
	out      run.CodeOutput
	logBytes int
}

func (e *execution) invoke(operation string, args []byte) (json.RawMessage, error) {
	if int64(len(args)) > e.policy.MaxInputBytes || !json.Valid(args) {
		return nil, run.ToolFailure("invalid_host_input", "none")
	}
	return e.host.Invoke(operation, args)
}

func (e *execution) log(raw []byte) error {
	if len(raw) > e.policy.MaxLogBytes-e.logBytes {
		e.out.LogsComplete = false
		return run.ToolFailure("log_limit", "none")
	}
	e.out.Logs = append(e.out.Logs, append(json.RawMessage(nil), raw...))
	e.logBytes += len(raw)
	return nil
}

func (e *execution) value(raw []byte) error {
	if int64(len(raw)) > e.policy.MaxOutputBytes || !json.Valid(raw) {
		return run.ToolFailure("result_limit", "none")
	}
	e.out.Value = append(json.RawMessage(nil), raw...)
	return nil
}

func (e *execution) captureOutput(output *limitedBuffer) {
	if output.buf.Len() > 0 {
		raw, _ := json.Marshal(output.buf.String())
		if e.log(raw) != nil {
			e.out.LogsComplete = false
		}
	}
	e.out.LogsComplete = e.out.LogsComplete && !output.truncated
}

func (e *execution) fail(ctx context.Context, code, detail string) error {
	if err := ctx.Err(); err != nil {
		status, message := "code_cancelled", err.Error()
		if err == context.DeadlineExceeded {
			status, message = "code_timeout", "Code execution exceeded the tool timeout"
		}
		if detail != "" && detail != err.Error() {
			message += "\n" + detail
		}
		code, detail = status, message
	}
	e.out.Error = code
	if len(detail) > 2048 {
		detail = detail[:1024] + "\n…\n" + detail[len(detail)-1024:]
	}
	e.out.Detail = detail
	return ctx.Err()
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	size := min(len(p), b.limit-b.buf.Len())
	b.truncated = b.truncated || size < len(p)
	b.buf.Write(p[:size])
	return len(p), nil
}
