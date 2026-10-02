package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ki/internal/types"
)

const WorkerArg = "__code-mode-worker"

// Limits bound transport, retained values and work. They are not a hard heap
// limit: goja shares the worker's Go heap, so process isolation remains required.
type Limits struct {
	MaxSourceBytes      int           `json:"maxSourceBytes"`
	MaxFrameBytes       int           `json:"maxFrameBytes"`
	MaxResultBytes      int           `json:"maxResultBytes"`
	MaxOutputBytes      int           `json:"maxOutputBytes"`
	MaxStoreBytes       int           `json:"maxStoreBytes"`
	MaxStoreKeys        int           `json:"maxStoreKeys"`
	MaxCells            int           `json:"maxCells"`
	MaxConcurrentCells  int           `json:"maxConcurrentCells"`
	MaxToolCalls        int           `json:"maxToolCalls"`
	MaxPendingToolCalls int           `json:"maxPendingToolCalls"`
	MaxTimers           int           `json:"maxTimers"`
	MaxStackDepth       int           `json:"maxStackDepth"`
	MaxExecutionTime    time.Duration `json:"maxExecutionTime"`
	DrainTimeout        time.Duration `json:"drainTimeout"`
	DefaultYieldTime    time.Duration `json:"defaultYieldTime"`
	MaxYieldTime        time.Duration `json:"maxYieldTime"`
	DefaultOutputBytes  int           `json:"defaultOutputBytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxSourceBytes: 256 << 10, MaxFrameBytes: 16 << 20,
		MaxResultBytes: 8 << 20, MaxOutputBytes: 8 << 20,
		MaxStoreBytes: 4 << 20, MaxStoreKeys: 1024,
		MaxCells: 64, MaxConcurrentCells: 8, MaxToolCalls: 512,
		MaxPendingToolCalls: 32, MaxTimers: 128, MaxStackDepth: 1024,
		MaxExecutionTime: 5 * time.Minute, DrainTimeout: 2 * time.Second,
		DefaultYieldTime: 10 * time.Second, MaxYieldTime: 60 * time.Second,
		DefaultOutputBytes: 40000,
	}
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	dst := []*int{&l.MaxSourceBytes, &l.MaxFrameBytes, &l.MaxResultBytes, &l.MaxOutputBytes, &l.MaxStoreBytes, &l.MaxStoreKeys, &l.MaxCells, &l.MaxConcurrentCells, &l.MaxToolCalls, &l.MaxPendingToolCalls, &l.MaxTimers, &l.MaxStackDepth, &l.DefaultOutputBytes}
	src := []int{d.MaxSourceBytes, d.MaxFrameBytes, d.MaxResultBytes, d.MaxOutputBytes, d.MaxStoreBytes, d.MaxStoreKeys, d.MaxCells, d.MaxConcurrentCells, d.MaxToolCalls, d.MaxPendingToolCalls, d.MaxTimers, d.MaxStackDepth, d.DefaultOutputBytes}
	for i := range dst {
		if *dst[i] <= 0 {
			*dst[i] = src[i]
		}
	}
	if l.MaxExecutionTime <= 0 {
		l.MaxExecutionTime = d.MaxExecutionTime
	}
	if l.DrainTimeout <= 0 {
		l.DrainTimeout = d.DrainTimeout
	}
	if l.DefaultYieldTime <= 0 {
		l.DefaultYieldTime = d.DefaultYieldTime
	}
	if l.MaxYieldTime <= 0 {
		l.MaxYieldTime = d.MaxYieldTime
	}
	if l.DefaultYieldTime > l.MaxYieldTime {
		l.DefaultYieldTime = l.MaxYieldTime
	}
	return l
}

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Freeform    bool   `json:"freeform,omitempty"`
}

type Invocation struct {
	ParentCallID string         `json:"parentCallId"`
	CellID       string         `json:"cellId"`
	ToolCallID   string         `json:"toolCallId"`
	Name         string         `json:"name"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Input        string         `json:"input,omitempty"`
	Freeform     bool           `json:"freeform,omitempty"`
}

type ToolResult struct {
	Content   []types.Content `json:"content"`
	Details   any             `json:"details,omitempty"`
	IsError   bool            `json:"isError"`
	Terminate bool            `json:"terminate,omitempty"`
}

type Notification struct {
	ParentCallID string `json:"parentCallId"`
	CellID       string `json:"cellId"`
	Text         string `json:"text"`
}

type Callbacks struct {
	Invoke func(context.Context, Invocation) (ToolResult, error)
	Notify func(context.Context, Notification) error
}

type ExecuteRequest struct {
	ParentCallID string           `json:"parentCallId"`
	Source       string           `json:"source"`
	Tools        []ToolDefinition `json:"tools"`
	YieldTime    time.Duration    `json:"yieldTime"`
	// YieldTimeSet distinguishes an explicit zero (immediate observation) from
	// the default yield interval. Positive YieldTime also implies it is set.
	YieldTimeSet      bool `json:"yieldTimeSet,omitempty"`
	MaxOutputBytes    int  `json:"maxOutputBytes,omitempty"`
	MaxOutputBytesSet bool `json:"maxOutputBytesSet,omitempty"`
}

type WaitRequest struct {
	CellID            string        `json:"cellId"`
	YieldTime         time.Duration `json:"yieldTime"`
	YieldTimeSet      bool          `json:"yieldTimeSet,omitempty"`
	MaxOutputBytes    int           `json:"maxOutputBytes,omitempty"`
	MaxOutputBytesSet bool          `json:"maxOutputBytesSet,omitempty"`
	Terminate         bool          `json:"terminate,omitempty"`
}

type Response struct {
	CellID    string          `json:"cellId"`
	Status    string          `json:"status"`
	Content   []types.Content `json:"content"`
	Error     string          `json:"error,omitempty"`
	Terminate bool            `json:"terminate,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

func (r Response) Running() bool { return r.Status == "running" }

type sessionBackend interface {
	execute(context.Context, ExecuteRequest, Callbacks) (Response, error)
	wait(context.Context, WaitRequest) (Response, error)
	terminateAll(context.Context) error
	close() error
}

// Session owns one logical session's cells and JSON store. A yielded cell keeps
// the callbacks advertised by its original Execute, never callbacks from Wait.
type Session struct{ backend sessionBackend }

func (s *Session) Execute(ctx context.Context, req ExecuteRequest, cb Callbacks) (Response, error) {
	if s == nil || s.backend == nil {
		return Response{}, errors.New("code mode session is unavailable")
	}
	return s.backend.execute(ctx, req, cb)
}
func (s *Session) Wait(ctx context.Context, req WaitRequest) (Response, error) {
	if s == nil || s.backend == nil {
		return Response{}, errors.New("code mode session is unavailable")
	}
	return s.backend.wait(ctx, req)
}
func (s *Session) TerminateAll(ctx context.Context) error {
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.terminateAll(ctx)
}
func (s *Session) Close() error {
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.close()
}

func validateRequest(req ExecuteRequest, l Limits) error {
	if req.ParentCallID == "" || len(req.ParentCallID) > 512 {
		return errors.New("invalid parent call ID")
	}
	if strings.TrimSpace(req.Source) == "" || len(req.Source) > l.MaxSourceBytes {
		return errors.New("code source is empty or exceeds the source limit")
	}
	if len(req.Tools) > l.MaxToolCalls {
		return errors.New("tool catalog exceeds limit")
	}
	names := make(map[string]bool)
	for _, t := range req.Tools {
		if t.Name == "" || len(t.Name) > 256 || len(t.Description) > 64<<10 || names[t.Name] {
			return fmt.Errorf("invalid or duplicate tool definition %q", t.Name)
		}
		names[t.Name] = true
	}
	if req.YieldTime < 0 || req.MaxOutputBytes < 0 {
		return errors.New("negative code mode observation limit")
	}
	if b, err := json.Marshal(req); err != nil || len(b) > l.MaxFrameBytes/2 {
		return errors.New("code mode request exceeds transport limit")
	}
	return nil
}

func yieldTime(value time.Duration, set bool, l Limits) time.Duration {
	if !set && value == 0 {
		value = l.DefaultYieldTime
	}
	if value > l.MaxYieldTime {
		value = l.MaxYieldTime
	}
	return value
}
