package arena

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func toolLoop(ctx context.Context, in Invocation, infer inference) (map[string]any, error) {
	if in.Schema != nil {
		in.Prompt += "\nFinal result JSON Schema: " + string(JSON(in.Schema))
	}
	messages := []chatMessage{{Role: "user", Content: in.Prompt + attachmentManifest(in.Attachments) + "\nUse read_attachment with path from the attachment manifest for user-supplied files.\nUse Arena's bounded JSON tool protocol. Return exactly one JSON object each turn: {\"action\":\"complete\",\"result\":<the requested structured result>} or {\"action\":\"read_attachment|read_file|list_files|write_file|run_command|ask_user|send_packet\",\"arguments\":{...}}. read_file/write_file use path (relative to assigned worktree); write_file also content. list_files uses path. run_command uses executable and args array, never shell expansion. ask_user uses question. send_packet uses to, kind, payload. You have only the capabilities in the task. Never read credentials or edit .git/.arena control files. A tool error is not success."}}
	for turn := 0; turn < 40; turn++ {
		text, e := infer(ctx, messages)
		if e != nil {
			return nil, e
		}
		if in.Output != nil {
			if e := in.Output(text); e != nil {
				return nil, e
			}
		}
		reply, e := parseOutput(text)
		if e != nil {
			return nil, e
		}
		action := str(reply["action"])
		if action == "complete" {
			result, ok := reply["result"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("complete action requires an object result")
			}
			return result, nil
		}
		args, _ := reply["arguments"].(map[string]any)
		activity := map[string]any{"type": "arenaTool", "tool": action, "status": "started"}
		if !Has([]string{"read_attachment", "read_file", "list_files", "write_file", "run_command", "ask_user", "send_packet"}, action) {
			activity["tool"] = "unsupported"
		}
		if in.Diagnostic != nil {
			if err := in.Diagnostic("provider.activity", activity); err != nil {
				return nil, err
			}
		}
		result, e := performTool(ctx, in, action, args)
		activity = map[string]any{"type": "arenaTool", "tool": activity["tool"], "status": "completed"}
		if e != nil {
			activity["status"] = "failed"
		}
		if in.Diagnostic != nil {
			if err := in.Diagnostic("provider.activity", activity); err != nil {
				return nil, err
			}
		}
		feedback := map[string]any{"action": action, "result": result}
		if e != nil {
			feedback = map[string]any{"action": action, "error": Redact(e.Error())}
		}
		messages = append(messages, chatMessage{Role: "assistant", Content: text}, chatMessage{Role: "user", Content: string(SafeJSON(feedback))})
		if len(JSON(messages)) > 2<<20 {
			return nil, fmt.Errorf("inference context budget exceeded")
		}
	}
	return nil, fmt.Errorf("bounded tool-turn limit reached")
}
func protectedPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".git" || part == ".arena" || part == ".arena-root" || part == ".env" || part == ".ssh" || part == ".aws" || part == ".codex" || part == ".claude" || part == ".gemini" {
			return true
		}
	}
	return false
}
func toolPath(dir, rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	if protectedPath(rel) {
		return "", fmt.Errorf("Arena control/credential paths are excluded from file tools")
	}
	return SafePath(dir, rel)
}
func performTool(ctx context.Context, in Invocation, action string, args map[string]any) (any, error) {
	switch action {
	case "read_attachment":
		return attachmentContent(in.Attachments, str(args["path"]))
	case "read_file":
		path, e := toolPath(in.Dir, str(args["path"]))
		if e != nil {
			return nil, e
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if len(b) > 512<<10 {
			return nil, fmt.Errorf("file exceeds handoff size limit")
		}
		return Redact(string(b)), nil
	case "list_files":
		path, e := toolPath(in.Dir, str(args["path"]))
		if e != nil {
			return nil, e
		}
		files := []string{}
		e = filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, e := filepath.Rel(in.Dir, path)
			if e != nil {
				return e
			}
			if protectedPath(rel) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if len(files) >= 1000 {
				return filepath.SkipAll
			}
			if !d.IsDir() {
				files = append(files, rel)
			}
			return nil
		})
		return files, e
	case "write_file":
		if !in.Write {
			return nil, fmt.Errorf("agent is read-only")
		}
		path, e := toolPath(in.Dir, str(args["path"]))
		if e != nil {
			return nil, e
		}
		content := str(args["content"])
		if len(content) > 2<<20 {
			return nil, fmt.Errorf("write exceeds 2 MiB tool limit")
		}
		mode := os.FileMode(0644)
		if info, e := os.Stat(path); e == nil {
			mode = info.Mode().Perm()
		}
		return "written", Atomic(path, []byte(content), mode)
	case "run_command":
		if !in.Commands {
			return nil, fmt.Errorf("profile does not allow run_commands")
		}
		executable := str(args["executable"])
		if executable == "" {
			return nil, fmt.Errorf("explicit executable is required")
		}
		argv := []string{}
		for _, x := range anySlice(args["args"]) {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("command arguments must be strings")
			}
			argv = append(argv, s)
		}
		if in.Approve == nil {
			return nil, fmt.Errorf("no command authorization handler")
		}
		yes, e := in.Approve(ctx, "shell-command-execution", args)
		if e != nil {
			return nil, e
		}
		if !yes {
			return nil, fmt.Errorf("command refused by policy or user")
		}
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd, cleanup, e := SandboxedCommand(in.Dir, in.Write, executable, argv)
		if e != nil {
			return nil, e
		}
		defer cleanup()
		var output cappedBuffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		process, e := StartProcess(bounded, cmd)
		if e != nil {
			return nil, e
		}
		e = process.Wait()
		return map[string]any{"output": Redact(output.String()), "exitCode": cmd.ProcessState.ExitCode()}, e
	case "ask_user":
		if in.Question == nil {
			return nil, fmt.Errorf("question handling unavailable")
		}
		return in.Question(ctx, str(args["question"]))
	case "send_packet":
		if in.Packet == nil {
			return nil, fmt.Errorf("packet handling unavailable")
		}
		return in.Packet(ctx, args)
	}
	return nil, fmt.Errorf("unknown Arena tool %s", action)
}
func anySlice(v any) []any { a, _ := v.([]any); return a }

type cappedBuffer struct{ data []byte }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := (1 << 20) - len(b.data)
	if left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *cappedBuffer) String() string { return string(b.data) }
func commandEnv() []string {
	env := []string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TERM"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}
func sandboxAvailable() bool {
	cmd, cleanup, err := SandboxedCommand(os.TempDir(), false, "true", nil)
	if err != nil {
		return false
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	process, err := StartProcess(ctx, cmd)
	return err == nil && process.Wait() == nil
}
