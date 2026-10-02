package codemode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"ki/internal/types"
)

type completion struct {
	id        uint64
	timer     bool
	notify    bool
	raw       json.RawMessage
	err       error
	terminate bool
	fatal     bool
}

type promiseResolver struct {
	resolve func(any) error
	reject  func(any) error
}

type scheduledTimer struct {
	timer *time.Timer
	fn    goja.Callable
}

type cellVM struct {
	cell                *cell
	vm                  *goja.Runtime
	stringify           goja.Callable
	parse               goja.Callable
	values              map[string]json.RawMessage
	writes              map[string]json.RawMessage
	pending             map[uint64]promiseResolver
	timers              map[uint64]scheduledTimer
	nextCall            uint64
	nextTimer           uint64
	notifications       int
	notifyError         error
	terminate           bool
	toolCtx             context.Context
	cancelTools         context.CancelFunc
	notifyCtx           context.Context
	cancelNotifications context.CancelFunc
	toolTasks           sync.WaitGroup
	notifyTasks         sync.WaitGroup
}

type successfulExit struct{}

var errCellStopped = errors.New("code cell stopped successfully")

func (c *cell) run(snapshot map[string]json.RawMessage) {
	vm := goja.New()
	vm.SetMaxCallStackSize(c.session.limits.MaxStackDepth)
	v := &cellVM{cell: c, vm: vm, values: snapshot, writes: make(map[string]json.RawMessage), pending: make(map[uint64]promiseResolver), timers: make(map[uint64]scheduledTimer)}
	v.toolCtx, v.cancelTools = context.WithCancel(c.ctx)
	v.notifyCtx, v.cancelNotifications = context.WithCancel(c.ctx)
	interruptDone := make(chan struct{})
	go func() {
		select {
		case <-c.ctx.Done():
			vm.Interrupt(c.ctx.Err())
		case <-interruptDone:
		}
	}()
	status := "completed"
	var runErr error
	defer func() {
		if p := recover(); p != nil {
			runErr = fmt.Errorf("code runtime failed: %v", p)
			status = "failed"
		}
		// The watcher must be stopped before final cancellation, otherwise a
		// normal completion races a spurious interrupt into notification drain.
		close(interruptDone)
		for _, t := range v.timers {
			t.timer.Stop()
		}
		if c.ctx.Err() == nil {
			if err := v.drainNotifications(); err != nil {
				runErr = err
				status = "failed"
			}
		}
		v.cancelNotifications()
		v.cancelTools()
		if err := drainTasks(&v.toolTasks, c.session.limits.DrainTimeout); err != nil {
			runErr = err
			status = "failed"
		}
		if err := drainTasks(&v.notifyTasks, c.session.limits.DrainTimeout); err != nil {
			runErr = err
			status = "failed"
		}
		// Unawaited tool promises are discarded, but a dispatcher failure or
		// terminate decision cannot disappear merely because JS did not await it.
		for {
			select {
			case e := <-c.events:
				v.terminate = v.terminate || e.terminate
				if e.fatal {
					runErr = e.err
					status = "failed"
				}
			default:
				goto callbacksCollected
			}
		}
	callbacksCollected:
		if c.ctx.Err() != nil {
			c.mu.Lock()
			explicit := c.explicitStop
			c.mu.Unlock()
			if explicit || errors.Is(c.ctx.Err(), context.Canceled) {
				status = "terminated"
				runErr = nil
			} else {
				status = "failed"
				runErr = c.ctx.Err()
			}
		} else if err := c.session.commit(v.writes); err != nil {
			status = "failed"
			runErr = err
		}
		c.finish(status, boundedError(runErr, 8192), v.terminate)
	}()
	if err := v.install(); err != nil {
		runErr = err
		status = "failed"
		return
	}
	result, err := vm.RunScript("exec.js", "(async function(){\n"+c.req.Source+"\n})()")
	if err != nil {
		var interrupted *goja.InterruptedError
		if errors.As(err, &interrupted) {
			if _, ok := interrupted.Value().(successfulExit); ok {
				return
			}
		}
		runErr = err
		status = "failed"
		return
	}
	promise, ok := result.Export().(*goja.Promise)
	if !ok {
		runErr = errors.New("code runtime did not produce an async promise")
		status = "failed"
		return
	}
	for promise.State() == goja.PromiseStatePending {
		select {
		case <-c.ctx.Done():
			return
		case e := <-c.events:
			if err := v.apply(e); err != nil {
				if errors.Is(err, errCellStopped) {
					return
				}
				var interrupted *goja.InterruptedError
				if errors.As(err, &interrupted) {
					if _, ok := interrupted.Value().(successfulExit); ok {
						return
					}
				}
				runErr = err
				status = "failed"
				return
			}
		}
	}
	if promise.State() == goja.PromiseStateRejected {
		runErr = errors.New(promise.Result().String())
		status = "failed"
	}
}

func drainTasks(tasks *sync.WaitGroup, timeout time.Duration) error {
	done := make(chan struct{})
	go func() { tasks.Wait(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		// Report a budget violation, never completion while callback code can
		// still mutate the harness. The parent watchdog can kill a stuck worker.
		<-done
		return errors.New("code mode callback cancellation cleanup exceeded deadline")
	}
}

func (v *cellVM) drainNotifications() error {
	timer := time.NewTimer(v.cell.session.limits.DrainTimeout)
	defer timer.Stop()
	for v.notifications > 0 {
		select {
		case e := <-v.cell.events:
			// Completed tool promises and timers are intentionally not resumed
			// after the top-level promise's terminal frontier.
			if e.notify {
				v.notifications--
				if e.err != nil {
					v.notifyError = e.err
				}
			} else {
				v.terminate = v.terminate || e.terminate
				if e.fatal {
					v.notifyError = e.err
				}
			}
		case <-v.cell.ctx.Done():
			return v.cell.ctx.Err()
		case <-timer.C:
			return errors.New("code mode notification drain timed out")
		}
	}
	return v.notifyError
}

func (v *cellVM) emit(e completion) {
	select {
	case v.cell.events <- e:
	case <-v.cell.ctx.Done():
	}
}

func (v *cellVM) jsonValue(raw json.RawMessage) (goja.Value, error) {
	return v.parse(goja.Undefined(), v.vm.ToValue(string(raw)))
}

func (v *cellVM) serialize(value goja.Value, limit int) (json.RawMessage, error) {
	out, err := v.stringify(goja.Undefined(), value)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(out) {
		return nil, errors.New("value is not JSON serializable")
	}
	raw := json.RawMessage(out.String())
	if len(raw) > limit {
		return nil, errors.New("serialized value exceeds code mode limit")
	}
	return raw, nil
}

func (v *cellVM) text(value goja.Value) (string, error) {
	if goja.IsUndefined(value) {
		return "undefined", nil
	}
	if value.ExportType() != nil && value.ExportType().Kind().String() == "string" {
		return value.String(), nil
	}
	raw, err := v.serialize(value, v.cell.session.limits.MaxOutputBytes)
	return string(raw), err
}

func (v *cellVM) fail(err error) { panic(v.vm.NewTypeError("%s", boundedError(err, 8192))) }

func (v *cellVM) install() error {
	vm := v.vm
	jsonObj := vm.Get("JSON").ToObject(vm)
	var ok bool
	v.stringify, ok = goja.AssertFunction(jsonObj.Get("stringify"))
	if !ok {
		return errors.New("JSON.stringify unavailable")
	}
	v.parse, ok = goja.AssertFunction(jsonObj.Get("parse"))
	if !ok {
		return errors.New("JSON.parse unavailable")
	}
	for _, name := range []string{"console", "Atomics", "SharedArrayBuffer", "WebAssembly"} {
		_ = vm.GlobalObject().Delete(name)
	}
	tools := vm.NewObject()
	metadata := make([]map[string]string, 0, len(v.cell.req.Tools))
	for _, definition := range v.cell.req.Tools {
		def := definition
		if err := tools.Set(def.Name, func(call goja.FunctionCall) goja.Value { return v.invoke(def, call.Argument(0)) }); err != nil {
			return err
		}
		metadata = append(metadata, map[string]string{"name": def.Name, "description": def.Description})
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	allTools, err := v.jsonValue(raw)
	if err != nil {
		return err
	}
	if err = vm.Set("tools", tools); err != nil {
		return err
	}
	if err = vm.Set("ALL_TOOLS", allTools); err != nil {
		return err
	}
	globals := map[string]any{
		"text": func(call goja.FunctionCall) goja.Value {
			text, err := v.text(call.Argument(0))
			if err != nil {
				v.fail(err)
			}
			if err = v.cell.appendOutput(types.Content{Type: "text", Text: text}); err != nil {
				v.fail(err)
			}
			return goja.Undefined()
		},
		"image": func(call goja.FunctionCall) goja.Value {
			item, err := v.image(call.Argument(0))
			if err != nil {
				v.fail(err)
			}
			if err = v.cell.appendOutput(item); err != nil {
				v.fail(err)
			}
			return goja.Undefined()
		},
		"store": func(call goja.FunctionCall) goja.Value {
			key := call.Argument(0).String()
			if goja.IsUndefined(call.Argument(0)) || len(key) > 1024 {
				v.fail(errors.New("invalid store key"))
			}
			raw, err := v.serialize(call.Argument(1), v.cell.session.limits.MaxStoreBytes)
			if err != nil {
				v.fail(err)
			}
			total := 0
			for k, val := range v.values {
				if k != key {
					total += len(k) + len(val)
				}
			}
			_, exists := v.values[key]
			if total+len(key)+len(raw) > v.cell.session.limits.MaxStoreBytes || (!exists && len(v.values) >= v.cell.session.limits.MaxStoreKeys) {
				v.fail(errors.New("store quota exceeded"))
			}
			v.values[key], v.writes[key] = raw, raw
			return goja.Undefined()
		},
		"load": func(call goja.FunctionCall) goja.Value {
			raw, ok := v.values[call.Argument(0).String()]
			if !ok {
				return goja.Undefined()
			}
			value, err := v.jsonValue(raw)
			if err != nil {
				v.fail(err)
			}
			return value
		},
		"exit": func(goja.FunctionCall) goja.Value { vm.Interrupt(successfulExit{}); return goja.Undefined() },
		"notify": func(call goja.FunctionCall) goja.Value {
			text, err := v.text(call.Argument(0))
			if err != nil {
				v.fail(err)
			}
			if strings.TrimSpace(text) == "" {
				v.fail(errors.New("notify expects non-empty text"))
			}
			if err = v.cell.appendOutput(types.Content{Type: "text", Text: text}); err != nil {
				v.fail(err)
			}
			if v.cell.cb.Notify != nil {
				if v.notifications >= v.cell.session.limits.MaxPendingToolCalls {
					v.fail(errors.New("pending notification limit exceeded"))
				}
				v.notifications++
				v.notifyTasks.Add(1)
				n := Notification{ParentCallID: v.cell.req.ParentCallID, CellID: v.cell.id, Text: text}
				go func() {
					defer v.notifyTasks.Done()
					err := safeNotify(v.notifyCtx, v.cell.cb.Notify, n)
					fatal := err != nil && v.notifyCtx.Err() == nil
					if fatal {
						v.vm.Interrupt(err)
					}
					v.emit(completion{notify: true, err: err, fatal: fatal})
				}()
			}
			return goja.Undefined()
		},
		"yield_control": func(goja.FunctionCall) goja.Value { v.cell.yield(); return goja.Undefined() },
		"setTimeout": func(call goja.FunctionCall) goja.Value {
			fn, ok := goja.AssertFunction(call.Argument(0))
			if !ok {
				v.fail(errors.New("setTimeout requires a callback"))
			}
			delay := float64(0)
			if !goja.IsUndefined(call.Argument(1)) {
				delay = call.Argument(1).ToFloat()
			}
			if math.IsNaN(delay) || math.IsInf(delay, 0) || delay < 0 || delay > float64(v.cell.session.limits.MaxExecutionTime/time.Millisecond) {
				v.fail(errors.New("invalid timeout delay"))
			}
			if len(v.timers) >= v.cell.session.limits.MaxTimers {
				v.fail(errors.New("timer limit exceeded"))
			}
			v.nextTimer++
			id := v.nextTimer
			timer := time.AfterFunc(time.Duration(delay*float64(time.Millisecond)), func() { v.emit(completion{id: id, timer: true}) })
			v.timers[id] = scheduledTimer{timer: timer, fn: fn}
			return vm.ToValue(id)
		},
		"clearTimeout": func(call goja.FunctionCall) goja.Value {
			id := uint64(call.Argument(0).ToInteger())
			if t, ok := v.timers[id]; ok {
				t.timer.Stop()
				delete(v.timers, id)
			}
			return goja.Undefined()
		},
	}
	for name, fn := range globals {
		if err := vm.Set(name, fn); err != nil {
			return err
		}
	}
	// Ki's IR represents generated images as ordinary image content.
	if err := vm.Set("generatedImage", vm.Get("image")); err != nil {
		return err
	}
	return nil
}

func (v *cellVM) invoke(def ToolDefinition, arg goja.Value) goja.Value {
	if v.nextCall >= uint64(v.cell.session.limits.MaxToolCalls) || len(v.pending) >= v.cell.session.limits.MaxPendingToolCalls {
		v.fail(errors.New("tool call limit exceeded"))
	}
	inv := Invocation{ParentCallID: v.cell.req.ParentCallID, CellID: v.cell.id, Name: def.Name, Freeform: def.Freeform}
	if def.Freeform {
		if goja.IsUndefined(arg) || arg.ExportType() == nil || arg.ExportType().Kind().String() != "string" {
			v.fail(errors.New("freeform tool expects a string"))
		}
		inv.Input = arg.String()
		if len(inv.Input) > v.cell.session.limits.MaxSourceBytes {
			v.fail(errors.New("freeform input exceeds limit"))
		}
	} else {
		if goja.IsUndefined(arg) {
			inv.Arguments = map[string]any{}
		} else {
			raw, err := v.serialize(arg, v.cell.session.limits.MaxResultBytes)
			if err != nil {
				v.fail(err)
			}
			if len(raw) == 0 || raw[0] != '{' {
				v.fail(errors.New("function tool expects an object"))
			}
			if err := json.Unmarshal(raw, &inv.Arguments); err != nil {
				v.fail(err)
			}
		}
	}
	v.nextCall++
	id := v.nextCall
	inv.ToolCallID = fmt.Sprintf("%s/tool-%d", v.cell.id, id)
	promise, resolve, reject := v.vm.NewPromise()
	v.pending[id] = promiseResolver{resolve: resolve, reject: reject}
	v.toolTasks.Add(1)
	go func() {
		defer v.toolTasks.Done()
		result, err := safeInvoke(v.toolCtx, v.cell.cb.Invoke, inv)
		var raw json.RawMessage
		if err == nil {
			raw, err = json.Marshal(result)
			if len(raw) > v.cell.session.limits.MaxResultBytes {
				err = errors.New("nested tool result exceeds intermediate result limit")
				raw = nil
			}
		}
		// Fatal dispatcher errors must also preempt JS that ignored the promise
		// and entered a CPU-bound loop; only Interrupt is used cross-thread.
		fatal := err != nil && v.toolCtx.Err() == nil
		if v.toolCtx.Err() == nil {
			if err != nil {
				v.vm.Interrupt(err)
			} else if result.Terminate {
				v.vm.Interrupt(successfulExit{})
			}
		}
		v.emit(completion{id: id, raw: raw, err: err, terminate: result.Terminate, fatal: fatal})
	}()
	return v.vm.ToValue(promise)
}

func (v *cellVM) apply(e completion) error {
	if e.notify {
		v.notifications--
		if e.err != nil {
			v.notifyError = e.err
			return e.err
		}
		return nil
	}
	if e.timer {
		t, ok := v.timers[e.id]
		if !ok {
			return nil
		}
		delete(v.timers, e.id)
		_, err := t.fn(goja.Undefined())
		return err
	}
	p, ok := v.pending[e.id]
	if !ok {
		return nil
	}
	delete(v.pending, e.id)
	v.terminate = v.terminate || e.terminate
	if e.err != nil {
		// Dispatcher Go errors represent harness/policy failures, unlike an
		// ordinary resolved IsError result. Never let JS catch and bypass them.
		v.vm.Interrupt(e.err)
		return e.err
	}
	if e.terminate {
		// Terminate is an execution-control decision, not ordinary JSON data.
		// Stop before resolving the promise so JS cannot issue later effects.
		v.vm.Interrupt(successfulExit{})
		return errCellStopped
	}
	value, err := v.jsonValue(e.raw)
	if err != nil {
		return err
	}
	return p.resolve(value)
}

func safeInvoke(ctx context.Context, fn func(context.Context, Invocation) (ToolResult, error), inv Invocation) (res ToolResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool callback panic: %v", p)
		}
	}()
	if fn == nil {
		return res, errors.New("nested tool dispatcher unavailable")
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return fn(ctx, inv)
}
func safeNotify(ctx context.Context, fn func(context.Context, Notification) error, n Notification) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("notification callback panic: %v", p)
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fn(ctx, n)
}

func (v *cellVM) image(value goja.Value) (types.Content, error) {
	var data, mime string
	if value.ExportType() != nil && value.ExportType().Kind().String() == "string" {
		data = value.String()
	} else {
		raw, err := v.serialize(value, v.cell.session.limits.MaxResultBytes)
		if err != nil {
			return types.Content{}, err
		}
		var object struct {
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
			ImageURL string `json:"image_url"`
			URL      string `json:"imageUrl"`
		}
		if err = json.Unmarshal(raw, &object); err != nil {
			return types.Content{}, errors.New("image expects an image block or data URL")
		}
		data, mime = object.Data, object.MIMEType
		if data == "" {
			data = object.ImageURL
		}
		if data == "" {
			data = object.URL
		}
	}
	if strings.HasPrefix(data, "data:") {
		prefix, payload, ok := strings.Cut(data, ",")
		if !ok || !strings.HasSuffix(prefix, ";base64") {
			return types.Content{}, errors.New("image requires a base64 data URL")
		}
		mime = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
		data = payload
	}
	if !strings.HasPrefix(mime, "image/") || data == "" || len(data) > v.cell.session.limits.MaxResultBytes {
		return types.Content{}, errors.New("image requires inline image data; URLs and filesystem paths are not supported")
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return types.Content{}, errors.New("image data is not valid base64")
	}
	return types.Content{Type: "image", Data: data, MIMEType: mime}, nil
}
