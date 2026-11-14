package codevm

import (
	"context"
	"errors"
	"fmt"

	python "github.com/goccy/go-python"
	"github.com/imalimin/mass/internal/domain/run"
)

const pythonScript = `import ast
import json
from sys import _getframe
def host(operation, arguments):
    return json.loads(_host(operation, json.dumps(arguments, allow_nan=False), _getframe(1).f_lineno))
def log(value):
    _log(json.dumps(value, allow_nan=False))
body = ast.parse(_source(), filename="mass-code.py")
wrapper = ast.parse("def _run(input):\n    pass")
wrapper.body[0].body = body.body or wrapper.body[0].body
if body.body:
    wrapper.body[0].lineno = body.body[0].lineno
    wrapper.body[0].end_lineno = body.body[-1].end_lineno
exec(compile(wrapper, "mass-code.py", "exec"))
def _execute():
    _result(json.dumps(_run(json.loads(_input())), allow_nan=False))
_execute
`

func (e *execution) python(ctx context.Context, input run.CodeInput) error {
	e.out.Engine = "cpython-wasm"
	stdout := &limitedBuffer{limit: e.policy.MaxLogBytes}
	vm, err := python.New(python.Config{MaxMemoryBytes: 64 << 20, Stdout: stdout, Stderr: stdout})
	if err != nil {
		return e.fail(ctx, "code_unavailable", "Could not initialize the Python interpreter")
	}
	defer vm.Close()

	var interruptedHost string
	if err := vm.Bind("_host", func(args []python.Value, kwargs map[string]python.Value) (python.Value, error) {
		if len(args) != 3 || len(kwargs) != 0 {
			return nil, errors.New("host expects an operation name and a JSON value")
		}
		operation, opOK := args[0].(python.StrValue)
		payload, payloadOK := args[1].(python.StrValue)
		line, lineOK := args[2].(python.IntValue)
		if !opOK || !payloadOK || !lineOK {
			return nil, errors.New("invalid host arguments")
		}
		raw, err := e.invoke(operation.String(), []byte(payload.String()))
		if err != nil && ctx.Err() != nil {
			lineNumber, _ := line.Int64()
			interruptedHost = fmt.Sprintf("mass-code.py:%d: host call %.64q interrupted", lineNumber, operation.String())
		}
		return python.ValueOf(string(raw)), err
	}); err != nil {
		return e.fail(ctx, "code_unavailable", "Could not initialize the Python host interface")
	}
	for name, receive := range map[string]func([]byte) error{"_log": e.log, "_result": e.value} {
		if err := vm.Bind(name, func(args []python.Value, kwargs map[string]python.Value) (python.Value, error) {
			if len(args) != 1 || len(kwargs) != 0 {
				return nil, errors.New("expected a single JSON value")
			}
			value, ok := args[0].(python.StrValue)
			if !ok {
				return nil, errors.New("expected JSON text")
			}
			return nil, receive([]byte(value.String()))
		}); err != nil {
			return e.fail(ctx, "code_unavailable", "Could not initialize the Python output interface")
		}
	}
	for name, value := range map[string]string{"_source": input.Code, "_input": string(input.Input)} {
		if err := vm.Bind(name, func([]python.Value, map[string]python.Value) (python.Value, error) {
			return python.ValueOf(value), nil
		}); err != nil {
			return e.fail(ctx, "code_unavailable", "Could not initialize the Python input")
		}
	}

	result, err := vm.Eval(ctx, pythonScript)
	if err != nil {
		return e.fail(ctx, "code_exception", err.Error())
	}
	if result.Error != nil {
		return e.fail(ctx, "code_exception", pythonDetail(result.Error))
	}
	execute, err := python.As[python.FunctionValue](result.Value)
	if err != nil {
		return e.fail(ctx, "code_unavailable", "Could not initialize the Python function")
	}
	_, err = execute.Call(ctx)
	if err == nil {
		err = vm.FlushStdio(ctx)
	} else if ctx.Err() == nil {
		_ = vm.FlushStdio(ctx)
	}
	e.captureOutput(stdout)
	if err != nil {
		detail := pythonDetail(err)
		if interruptedHost != "" {
			detail = interruptedHost
		}
		return e.fail(ctx, "code_exception", detail)
	}
	if e.out.Value == nil {
		return e.fail(ctx, "code_exception", "Python did not return a JSON result")
	}
	return nil
}

func pythonDetail(err error) string {
	var exception *python.PythonError
	if errors.As(err, &exception) && exception.Traceback != "" {
		return exception.Traceback
	}
	return err.Error()
}
