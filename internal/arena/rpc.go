package arena

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

type RPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params map[string]any  `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type RPC struct {
	input    io.WriteCloser
	messages chan RPCMessage
	errors   chan error
	process  *Process
	counter  int
}

func StartRPC(ctx context.Context, dir string, overrides ...string) (*RPC, error) {
	cmd := exec.Command("codex", append(overrides, "app-server")...)
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	input, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	process, e := StartProcess(ctx, cmd)
	if e != nil {
		return nil, e
	}
	rpc := &RPC{input: input, messages: make(chan RPCMessage, 32), errors: make(chan error, 1), process: process}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			var m RPCMessage
			if e := json.Unmarshal(scanner.Bytes(), &m); e != nil {
				rpc.errors <- fmt.Errorf("invalid Codex protocol response")
				process.Stop()
				return
			}
			select {
			case rpc.messages <- m:
			case <-ctx.Done():
				return
			}
		}
		e := scanner.Err()
		if e == nil {
			e = io.EOF
		}
		select {
		case rpc.errors <- e:
		default:
		}
	}()
	if _, e := rpc.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "agent_arena", "title": "Agent Arena", "version": Version}}, nil); e != nil {
		rpc.Close()
		return nil, e
	}
	if e := rpc.Send(map[string]any{"method": "initialized"}); e != nil {
		rpc.Close()
		return nil, e
	}
	return rpc, nil
}
func (r *RPC) Send(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = r.input.Write(append(b, '\n'))
	return e
}
func (r *RPC) Call(ctx context.Context, method string, params any, handle func(RPCMessage) error) (json.RawMessage, error) {
	r.counter++
	id := r.counter
	if e := r.Send(map[string]any{"id": id, "method": method, "params": params}); e != nil {
		return nil, e
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-r.errors:
			return nil, e
		case m := <-r.messages:
			if string(m.ID) == fmt.Sprint(id) && m.Method == "" {
				if len(m.Error) > 0 {
					return nil, fmt.Errorf("Codex %s: %s", method, Redact(string(m.Error)))
				}
				return m.Result, nil
			}
			if handle != nil {
				if e := handle(m); e != nil {
					return nil, e
				}
			} else if len(m.ID) > 0 {
				if e := r.Send(map[string]any{"id": json.RawMessage(m.ID), "error": map[string]any{"code": -32601, "message": "Arena discovery client does not handle server requests"}}); e != nil {
					return nil, e
				}
			}
		}
	}
}
func (r *RPC) Close() { _ = r.input.Close(); r.process.Stop(); _ = r.process.Wait() }
