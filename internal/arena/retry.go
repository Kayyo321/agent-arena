package arena

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

func (e *Engine) invokeAttempts(ctx context.Context, a Agent, in Invocation, task Packet) (map[string]any, error) {
	limit := e.Run.Settings.Limits.MaxRetriesPerAgent
	delivered := in.Delivered
	for attempt := 0; ; attempt++ {
		bounded, cancel := inferenceDeadline(ctx, e.Run.Settings.Limits.MaxAgentRunMinutes)
		slot, err := e.Project.ProviderSlot(bounded, e.Run.Settings.Limits.MaxConcurrentAgents)
		if err != nil {
			cancel()
			return nil, err
		}
		var once sync.Once
		acked := false
		var ackErr error
		in.Delivered = func() error {
			once.Do(func() {
				acked = true
				if delivered != nil {
					ackErr = delivered()
				}
			})
			return ackErr
		}
		result, err := e.Invoker.Invoke(bounded, in)
		if err == nil {
			err = in.Delivered()
		}
		if err != nil && !acked {
			if _, eventErr := e.event(a.Id, "message.failed", map[string]any{"messageId": task["messageId"], "error": Redact(err.Error())}); eventErr != nil {
				err = eventErr
			}
		}
		slotErr := slot.Close()
		cancel()
		if slotErr != nil {
			return nil, slotErr
		}
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil || attempt >= limit {
			return nil, err
		}
		answer, decisionErr := e.Project.Decide(ctx, e.Run, a.Id, "retry", "Provider attempt failed. Retry can repeat tool effects; inspect its workspace before deciding.", []string{"retry", "stop"}, map[string]any{"attemptId": task["attemptId"], "error": Redact(err.Error()), "workspace": in.Dir})
		if decisionErr != nil {
			return nil, decisionErr
		}
		if answer != "retry" {
			return nil, err
		}
		checkpoint, checkpointErr := e.Project.Checkpoint(in.Dir, "before explicit retry for "+a.Id)
		if checkpointErr != nil {
			return nil, checkpointErr
		}
		previous := string(SafeJSON(task))
		oldAttempt := task["attemptId"]
		oldMessage := task["messageId"]
		task["attemptId"] = ID("attempt-")
		task["messageId"] = ID("message-")
		task["replyTo"] = oldMessage
		task["createdAt"] = Now()
		if _, err := e.Project.Send(e.Run, task); err != nil {
			return nil, err
		}
		in.Prompt = strings.ReplaceAll(in.Prompt, previous, string(SafeJSON(task)))
		if _, err := e.event(a.Id, "attempt.retry", map[string]any{"previousAttemptId": oldAttempt, "attemptId": task["attemptId"], "checkpointId": checkpoint.ID, "retry": attempt + 1}); err != nil {
			return nil, err
		}
		in.Prompt += fmt.Sprintf("\nThis is explicitly approved retry %d; inspect existing workspace effects before acting.", attempt+1)
	}
}
