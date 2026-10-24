package javascript

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/imalimin/mass/internal/domain/run"
	"sync/atomic"
	"testing"
	"time"
)

type hostFunc func(string, json.RawMessage) (json.RawMessage, error)

func (f hostFunc) Invoke(name string, args json.RawMessage) (json.RawMessage, error) {
	return f(name, args)
}
func testPolicy() run.ToolPolicy {
	return run.ToolPolicy{Version: "mass-tools-v1", MaxCodeBytes: 65536, TimeoutMS: 1000, MaxInputBytes: 2 << 20, MaxOutputBytes: 2 << 20, MaxLogBytes: 128}
}
func TestVMContractAndBoundedResults(t *testing.T) {
	calls := atomic.Int32{}
	host := hostFunc(func(name string, args json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		var value any
		if json.Unmarshal(args, &value) != nil {
			t.Fatal("invalid host arguments")
		}
		if name == "fs.readFile" {
			return json.RawMessage(`{"content":"x"}`), nil
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	runner := &Runner{MaxConcurrent: 1}
	out, err := runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`{"count":3}`), Code: `let value='';for(let i=0;i<input.count;i++)value+=fs.readFile('a.txt').content;fs.writeFile('b.txt',value,{expected_sha256:'missing'});log({value});return {value};`}, host, testPolicy())
	if err != nil || out.Error != "" || string(out.Value) != `{"value":"xxx"}` || calls.Load() != 4 || len(out.Logs) != 1 {
		t.Fatal(string(out.Value), out.Error, out.Detail, err, calls.Load())
	}
	for _, code := range []string{`return NaN`, `return Infinity`, `return ()=>1`, `const a={};a.a=a;return a`, `return (async()=>1)()`, `return Promise.resolve(1)`, `return process.env`, `return require('fs')`, `log('x'.repeat(1000));return 1`, `return {a:undefined}`, `Number.isFinite=()=>true;return Infinity`} {
		out, err = runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: code}, host, testPolicy())
		if err == nil && out.Error == "" {
			t.Fatal("accepted unsupported result", code, string(out.Value))
		}
	}
	out, err = runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: "eval('1\\n//# sourceMappingURL=http://127.0.0.1/forbidden');new Function('return 2;\\n//# sourceMappingURL=http://127.0.0.1/forbidden')();return 3;"}, host, testPolicy())
	if err != nil || out.Error != "" || calls.Load() != 4 {
		t.Fatal("external source map activity", err, out.Error, out.Detail, calls.Load())
	}
	out, err = runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `Array.prototype.push=()=>{};Set.prototype.has=()=>false;Set.prototype.add=()=>{};Set.prototype.delete=()=>{};Object.prototype.toJSON=()=>({fake:true});return {values:[1,2]};`}, host, testPolicy())
	if err != nil || out.Error != "" || string(out.Value) != `{"values":[1,2]}` {
		t.Fatal("mutated prototypes changed serialization", out, err)
	}
	runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `globalThis.leaked=42;return true;`}, nil, testPolicy())
	out, err = runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `return typeof leaked;`}, nil, testPolicy())
	if err != nil || string(out.Value) != `"undefined"` {
		t.Fatal("VM state leaked across calls", out, err)
	}
}
func TestVMInterruptWaitsForExitAndReleasesSlot(t *testing.T) {
	runner := &Runner{MaxConcurrent: 1}
	policy := testPolicy()
	policy.TimeoutMS = 30
	start := time.Now()
	out, err := runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `while(true){}`}, hostFunc(func(string, json.RawMessage) (json.RawMessage, error) { panic("unexpected") }), policy)
	if err == nil || out.Error != "code_cancelled" || time.Since(start) > time.Second {
		t.Fatal(out.Error, err, time.Since(start))
	}
	out, err = runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `return 42;`}, nil, testPolicy())
	if err != nil || string(out.Value) != "42" {
		t.Fatal("slot not released", out, err)
	}
}

func TestCancellationKeepsSlotUntilHostActuallyExits(t *testing.T) {
	runner := &Runner{MaxConcurrent: 1}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := runner.Execute(ctx, run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `fs.readFile('a');return 1;`}, hostFunc(func(string, json.RawMessage) (json.RawMessage, error) {
			close(entered)
			<-release
			return json.RawMessage(`{}`), nil
		}), testPolicy())
		done <- err
	}()
	<-entered
	cancel()
	short := testPolicy()
	short.TimeoutMS = 30
	_, err := runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `return 2;`}, nil, short)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("cancelled host released its slot early", err)
	}
	select {
	case <-done:
		close(release)
		t.Fatal("reported exit while host was still running")
	default:
	}
	close(release)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("host exit did not propagate cancellation", err)
	}
	out, err := runner.Execute(context.Background(), run.CodeInput{Language: "javascript", Input: json.RawMessage(`null`), Code: `return 3;`}, nil, testPolicy())
	if err != nil || string(out.Value) != "3" {
		t.Fatal("slot not released after actual exit", out, err)
	}
}
