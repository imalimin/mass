// Package javascript provides a cancellable embedded VM, without an OS sandbox.
package javascript

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
	"github.com/imalimin/mass/internal/domain/run"
	"sync"
	"time"
)

type Runner struct {
	MaxConcurrent int
	once          sync.Once
	slots         chan struct{}
}

func (r *Runner) Execute(ctx context.Context, in run.CodeInput, host run.CodeHost, policy run.ToolPolicy) (out run.CodeOutput, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(policy.TimeoutMS)*time.Millisecond)
	defer cancel()
	r.once.Do(func() { r.slots = make(chan struct{}, max(1, r.MaxConcurrent)) })
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	if in.Language != "javascript" || len(in.Code) > min(65536, policy.MaxCodeBytes) || len(in.Input) > int(policy.MaxInputBytes) || !json.Valid(in.Input) {
		return out, run.ToolFailure("invalid_code_input", "none")
	}
	vm := goja.New()
	vm.SetParserOptions(parser.WithDisableSourceMaps, parser.WithSourceMapLoader(func(string) ([]byte, error) { return nil, errors.New("source_map_disabled") }))
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()
	defer func() { close(done); <-exited }()
	out.Logs = []json.RawMessage{}
	out.LogsComplete = true
	// Serialization runs inside the VM so getters, cycles and deep structures are
	// bounded by the same interruptible call, and no arbitrary Go Export is used.
	safeValue, e := vm.RunString(`(function(){const stringify=JSON.stringify,ownKeys=Object.keys,isArray=Array.isArray,isFinite=Number.isFinite,define=Object.defineProperty,SetType=Set,getProto=Object.getPrototypeOf,objectProto=Object.prototype,create=Object.create,setProto=Object.setPrototypeOf,has=Function.prototype.call.bind(Set.prototype.has),add=Function.prototype.call.bind(Set.prototype.add),remove=Function.prototype.call.bind(Set.prototype.delete);return function(value){let count=0,seen=new SetType();function visit(v,depth){if(++count>10000||depth>32)throw Error("result_limit");if(v===null)return v;let t=typeof v;if(t==='number'){if(!isFinite(v))throw Error('unsupported_number');return v}if(t==='string'||t==='boolean')return v;if(t!=='object'||typeof v.then==='function'||(!isArray(v)&&getProto(v)!==objectProto&&getProto(v)!==null))throw Error('unsupported_result');if(has(seen,v))throw Error('cyclic_result');add(seen,v);let out;if(isArray(v)){if(v.length>10000)throw Error('result_limit');out=[];setProto(out,null);for(let i=0;i<v.length;i++)define(out,String(i),{value:visit(v[i],depth+1),enumerable:true})}else{out=create(null);let keys=ownKeys(v);if(keys.length>10000)throw Error('result_limit');for(let k of keys)define(out,k,{value:visit(v[k],depth+1),enumerable:true})}remove(seen,v);return out}return stringify(visit(value,0))}})()`)
	if e != nil {
		return out, e
	}
	serialize, _ := goja.AssertFunction(safeValue)
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	encode := func(value goja.Value, limit int) (json.RawMessage, error) {
		encoded, e := serialize(goja.Undefined(), value)
		if e != nil {
			return nil, e
		}
		text := encoded.String()
		if len(text) > limit || !json.Valid([]byte(text)) {
			return nil, errors.New("result_limit")
		}
		return json.RawMessage(text), nil
	}
	invoke := func(name string, args goja.Value) goja.Value {
		if err := ctx.Err(); err != nil {
			panic(vm.NewGoError(err))
		}
		raw, e := encode(args, int(policy.MaxInputBytes))
		if e != nil {
			panic(vm.NewTypeError("invalid_host_input: %s", e.Error()))
		}
		response, e := host.Invoke(name, raw)
		if e != nil {
			panic(vm.NewGoError(e))
		}
		if len(response) > int(policy.MaxOutputBytes) {
			panic(vm.NewTypeError("host_output_limit"))
		}
		value, e := parse(goja.Undefined(), vm.ToValue(string(response)))
		if e != nil {
			panic(vm.NewTypeError("invalid_host_output"))
		}
		return value
	}
	fs := vm.NewObject()
	for _, method := range []string{"list", "readFile", "mkdir", "writeFile", "editFile", "rename", "remove"} {
		method := method
		fs.Set(method, func(call goja.FunctionCall) goja.Value {
			args := vm.NewObject()
			values := make([]any, len(call.Arguments))
			for i, v := range call.Arguments {
				values[i] = v
			}
			args.Set("arguments", vm.NewArray(values...))
			return invoke("fs."+method, args)
		})
	}
	vm.Set("fs", fs)
	net := vm.NewObject()
	net.Set("request", func(call goja.FunctionCall) goja.Value { return invoke("net.request", call.Argument(0)) })
	vm.Set("net", net)
	csv := vm.NewObject()
	for _, method := range []string{"parse", "stringify"} {
		method := method
		csv.Set(method, func(call goja.FunctionCall) goja.Value { return invoke("csv."+method, call.Argument(0)) })
	}
	vm.Set("CSV", csv)
	logBytes := 0
	vm.Set("log", func(call goja.FunctionCall) goja.Value {
		if ctx.Err() != nil {
			panic(vm.NewGoError(ctx.Err()))
		}
		for _, value := range call.Arguments {
			raw, e := encode(value, policy.MaxLogBytes)
			if e != nil {
				out.LogsComplete = false
				panic(vm.NewTypeError("log_limit"))
			}
			if len(raw) > policy.MaxLogBytes-logBytes {
				out.LogsComplete = false
				panic(vm.NewTypeError("log_limit"))
			}
			out.Logs = append(out.Logs, raw)
			logBytes += len(raw)
		}
		return goja.Undefined()
	})
	// No async host APIs, environment, module loader or process handles exist.
	vm.Set("Promise", goja.Undefined())
	function, e := vm.RunScript("mass-code-v1.js", "(function(input){'use strict';\n"+in.Code+"\n})")
	if e != nil {
		out.Error = "code_exception"
		out.Detail = e.Error()
		if len(out.Detail) > 4096 {
			out.Detail = out.Detail[:4096]
		}
		return out, nil
	}
	callable, ok := goja.AssertFunction(function)
	if !ok {
		return out, run.ErrProtocol
	}
	input, e := parse(goja.Undefined(), vm.ToValue(string(in.Input)))
	if e != nil {
		return out, run.ErrProtocol
	}
	value, e := callable(goja.Undefined(), input)
	if e == nil {
		out.Value, e = encode(value, int(policy.MaxOutputBytes))
	}
	if e != nil {
		if ctx.Err() != nil {
			out.Error = "code_cancelled"
			return out, ctx.Err()
		}
		out.Error = "code_exception"
		out.Detail = e.Error()
		if len(out.Detail) > 4096 {
			out.Detail = out.Detail[:4096]
		}
		return out, nil
	}
	return out, nil
}
