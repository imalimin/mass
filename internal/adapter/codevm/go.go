package codevm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func (e *execution) goCode(ctx context.Context, input run.CodeInput, root string) error {
	e.out.Engine = "go-wasi"
	if err := checkGoBody(input.Code); err != nil {
		return e.fail(ctx, "code_exception", err.Error())
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin, err = exec.LookPath("go")
		if err != nil {
			return e.fail(ctx, "code_unavailable", "run_code_go requires a local Go toolchain")
		}
	}
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return e.fail(ctx, "code_unavailable", "Could not create the Go build cache")
	}
	dir, err := os.MkdirTemp(root, "compile-*")
	if err != nil {
		return e.fail(ctx, "code_unavailable", "Could not create the Go build directory")
	}
	defer os.RemoveAll(dir)
	source := goPrelude + "\n//line mass-code.go:1\n" + input.Code + "\n}\n"
	if err = os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600); err != nil {
		return e.fail(ctx, "code_unavailable", "Could not write the Go source")
	}
	cmd := exec.CommandContext(ctx, goBin, "build", "-p=2", "-trimpath", "-buildvcs=false", "-o", "main.wasm", "main.go")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return os.ErrProcessDone
	}
	cmd.Dir = dir
	cmd.Env = []string{
		"GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GO111MODULE=off",
		"GOENV=off", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off",
		"GOCACHE=" + cache,
		"TMPDIR=" + dir,
	}
	buildOutput := &limitedBuffer{limit: 2048}
	cmd.Stdout, cmd.Stderr = buildOutput, buildOutput
	if err = cmd.Run(); err != nil {
		detail := strings.TrimSpace(buildOutput.buf.String())
		if detail == "" {
			return e.fail(ctx, "code_unavailable", "Could not execute the Go compiler")
		}
		return e.fail(ctx, "code_exception", detail)
	}
	guest, err := os.ReadFile(filepath.Join(dir, "main.wasm"))
	if err != nil {
		return e.fail(ctx, "code_unavailable", "Could not read the compiled Go module")
	}
	return e.runWasm(ctx, guest, input.Input)
}

// The compiler receives a function body wrapped in trusted source. Check the
// boundary before building so user text cannot add imports or compiler inputs.
func checkGoBody(source string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "mass-code.go", "package main\nfunc run(){\n//line mass-code.go:1\n"+source+"\n}", parser.ParseComments)
	if err != nil {
		return err
	}
	if len(file.Decls) != 1 {
		return errors.New("provide a Go function body without package declarations or imports")
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "//go:") {
				return errors.New("compiler directives are unavailable in run_code_go")
			}
		}
	}
	return nil
}

func (e *execution) runWasm(ctx context.Context, guest, input []byte) error {
	vm := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).WithMemoryLimitPages(1024))
	defer vm.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, vm); err != nil {
		return e.fail(ctx, "code_unavailable", "Could not initialize the Go runtime")
	}

	var response []byte
	bridge := vm.NewHostModuleBuilder("mass")
	bridge.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, opPtr, opLen, argPtr, argLen uint32) uint32 {
		op, okOp := m.Memory().Read(opPtr, opLen)
		args, okArgs := m.Memory().Read(argPtr, argLen)
		var err error
		if okOp && okArgs && len(op) <= 64 {
			response, err = e.invoke(string(op), args)
		} else {
			err = errors.New("invalid host arguments")
		}
		if err != nil {
			response = []byte(err.Error())
			return uint32(len(response)) | 1<<31
		}
		return uint32(len(response))
	}).Export("host_call")
	bridge.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr uint32) {
		m.Memory().Write(ptr, response)
		response = nil
	}).Export("host_read")
	output := func(receive func([]byte) error) func(context.Context, api.Module, uint32, uint32) uint32 {
		return func(_ context.Context, m api.Module, ptr, length uint32) uint32 {
			raw, ok := m.Memory().Read(ptr, length)
			if !ok || receive(raw) != nil {
				return 0
			}
			return 1
		}
	}
	bridge.NewFunctionBuilder().WithFunc(output(e.log)).Export("log")
	bridge.NewFunctionBuilder().WithFunc(output(e.value)).Export("result")
	if _, err := bridge.Instantiate(ctx); err != nil {
		return e.fail(ctx, "code_unavailable", "Could not initialize the Go host interface")
	}

	stdout, stderr := &limitedBuffer{limit: e.policy.MaxLogBytes}, &limitedBuffer{limit: 2048}
	config := wazero.NewModuleConfig().WithStdin(bytes.NewReader(input)).WithStdout(stdout).WithStderr(stderr)
	_, err := vm.InstantiateWithConfig(ctx, guest, config)
	e.captureOutput(stdout)
	if err != nil {
		return e.fail(ctx, "code_exception", strings.TrimSpace(fmt.Sprintf("%s\n%v", stderr.buf.String(), err)))
	}
	if e.out.Value == nil {
		return e.fail(ctx, "code_exception", "Go did not return a JSON result")
	}
	return nil
}

const goPrelude = `package main
import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)
var _, _, _, _ = fmt.Sprint, math.Abs, strconv.Itoa, strings.TrimSpace

//go:wasmimport mass host_call
func hostCall(operation string, payload string) uint32

//go:wasmimport mass host_read
func hostRead(ptr *byte)

//go:wasmimport mass log
func emitLog(raw string) uint32

//go:wasmimport mass result
func emitResult(raw string) uint32

func host(operation string, arguments any) any {
	raw, err := json.Marshal(arguments)
	if err != nil { panic(err) }
	size := hostCall(operation, string(raw))
	buf := make([]byte, int(size &^ (1<<31)))
	hostRead(unsafe.SliceData(buf))
	runtime.KeepAlive(buf)
	if size >> 31 != 0 { panic(string(buf)) }
	var result any
	if err := json.Unmarshal(buf, &result); err != nil { panic(err) }
	return result
}

func log(value any) {
	raw, err := json.Marshal(value)
	if err != nil { panic(err) }
	if emitLog(string(raw)) == 0 { panic("log_limit") }
}

func main() {
	var input any
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil { panic(err) }
	raw, err := json.Marshal(run(input))
	if err != nil { panic(err) }
	if emitResult(string(raw)) == 0 { panic("result_limit") }
}

func run(input any) any {
`
