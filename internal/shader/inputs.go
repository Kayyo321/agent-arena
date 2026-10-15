package shader

// Inputs are the numeric uniforms Arena supplies to a background shader. They
// are visual projections of audited state, never prompts, paths, identities,
// secrets or packet contents. Counts are whole numbers; ages are seconds since
// the most recent matching event (NeverAge when none has happened); envelopes
// are host-smoothed 0–1 levels that ease instead of jumping between frames.
type Inputs struct {
	// Viewport and clocks.
	UV, Cell, Resolution           Value
	Time, DeltaTime, Frame, Aspect float64
	WorkTime, Seed                 float64

	// Run lifecycle: exactly one of these flags is 1 (RunIdle without a run).
	RunActive, RunPaused, RunInterrupted, RunFailed, RunSucceeded, RunCancelled, RunIdle float64
	RunTime                                                                              float64

	// Agents by state.
	AgentCount, RunningAgents, QueuedAgents, WaitingAgents            float64
	SucceededAgents, FailedAgents, CancelledAgents, InterruptedAgents float64

	// Audited packets.
	PendingPackets, DeliveredPackets, FailedPackets, TotalPackets float64
	PendingQuestions, PendingTasks, PendingResults                float64

	// User decisions and questions.
	PendingDecisions, AwaitingUser, DecisionAge float64

	// Git isolation and integration.
	Changes, ChangesPending, ChangesIntegrated, ChangesConflicted float64
	Worktrees, WritingWorktrees                                   float64

	// Teams, providers and journal volume.
	TeamRounds, ProvidersReady, ProvidersBlocked, EventCount float64

	// Seconds since the latest event of each category.
	EventAge, MessageAge, ProgressAge, StartAge, SuccessAge, FailureAge float64
	ChangeAge, DecisionRequestAge, DecisionAnswerAge, ContextAge        float64
	TeamAge, InputAge                                                   float64

	// Composer and TUI state.
	PromptLength, Attachments, Busy, ErrorActive float64

	// Screen position (uv) of the agent most worth watching and its strength.
	Focus         Value
	FocusStrength float64

	// Host-smoothed 0–1 envelopes.
	Energy, Activity, Attention, Tension, Success, Progress, Typing float64
}

// NeverAge is the age reported for an event category that has not occurred.
const NeverAge = 10000

// Uniform describes one input a shader may reference by name.
type Uniform struct {
	Name    string
	Width   int
	varying bool
	get     func(*Inputs) Value
}

func scalar(f func(*Inputs) float64) func(*Inputs) Value {
	return func(in *Inputs) Value { return Scalar(f(in)) }
}
func vec2(f func(*Inputs) Value) func(*Inputs) Value {
	return func(in *Inputs) Value { v := f(in); v.N = 2; return v }
}

// uniforms is the complete, ordered input table. Only referenced entries cost
// compiled operations, so adding inputs never shrinks an existing program's budget.
var uniforms = []Uniform{
	{"uv", 2, true, vec2(func(in *Inputs) Value { return in.UV })},
	{"cell", 2, true, vec2(func(in *Inputs) Value { return in.Cell })},
	{"resolution", 2, false, vec2(func(in *Inputs) Value { return in.Resolution })},
	{"time", 1, false, scalar(func(in *Inputs) float64 { return in.Time })},
	{"deltaTime", 1, false, scalar(func(in *Inputs) float64 { return in.DeltaTime })},
	{"frame", 1, false, scalar(func(in *Inputs) float64 { return in.Frame })},
	{"aspect", 1, false, scalar(func(in *Inputs) float64 { return in.Aspect })},
	{"workTime", 1, false, scalar(func(in *Inputs) float64 { return in.WorkTime })},
	{"seed", 1, false, scalar(func(in *Inputs) float64 { return in.Seed })},

	{"runActive", 1, false, scalar(func(in *Inputs) float64 { return in.RunActive })},
	{"runPaused", 1, false, scalar(func(in *Inputs) float64 { return in.RunPaused })},
	{"runInterrupted", 1, false, scalar(func(in *Inputs) float64 { return in.RunInterrupted })},
	{"runFailed", 1, false, scalar(func(in *Inputs) float64 { return in.RunFailed })},
	{"runSucceeded", 1, false, scalar(func(in *Inputs) float64 { return in.RunSucceeded })},
	{"runCancelled", 1, false, scalar(func(in *Inputs) float64 { return in.RunCancelled })},
	{"runIdle", 1, false, scalar(func(in *Inputs) float64 { return in.RunIdle })},
	{"runTime", 1, false, scalar(func(in *Inputs) float64 { return in.RunTime })},

	{"agentCount", 1, false, scalar(func(in *Inputs) float64 { return in.AgentCount })},
	{"runningAgents", 1, false, scalar(func(in *Inputs) float64 { return in.RunningAgents })},
	{"queuedAgents", 1, false, scalar(func(in *Inputs) float64 { return in.QueuedAgents })},
	{"waitingAgents", 1, false, scalar(func(in *Inputs) float64 { return in.WaitingAgents })},
	{"succeededAgents", 1, false, scalar(func(in *Inputs) float64 { return in.SucceededAgents })},
	{"failedAgents", 1, false, scalar(func(in *Inputs) float64 { return in.FailedAgents })},
	{"cancelledAgents", 1, false, scalar(func(in *Inputs) float64 { return in.CancelledAgents })},
	{"interruptedAgents", 1, false, scalar(func(in *Inputs) float64 { return in.InterruptedAgents })},

	{"pendingPackets", 1, false, scalar(func(in *Inputs) float64 { return in.PendingPackets })},
	{"deliveredPackets", 1, false, scalar(func(in *Inputs) float64 { return in.DeliveredPackets })},
	{"failedPackets", 1, false, scalar(func(in *Inputs) float64 { return in.FailedPackets })},
	{"totalPackets", 1, false, scalar(func(in *Inputs) float64 { return in.TotalPackets })},
	{"pendingQuestions", 1, false, scalar(func(in *Inputs) float64 { return in.PendingQuestions })},
	{"pendingTasks", 1, false, scalar(func(in *Inputs) float64 { return in.PendingTasks })},
	{"pendingResults", 1, false, scalar(func(in *Inputs) float64 { return in.PendingResults })},

	{"pendingDecisions", 1, false, scalar(func(in *Inputs) float64 { return in.PendingDecisions })},
	{"awaitingUser", 1, false, scalar(func(in *Inputs) float64 { return in.AwaitingUser })},
	{"decisionAge", 1, false, scalar(func(in *Inputs) float64 { return in.DecisionAge })},

	{"changes", 1, false, scalar(func(in *Inputs) float64 { return in.Changes })},
	{"changesPending", 1, false, scalar(func(in *Inputs) float64 { return in.ChangesPending })},
	{"changesIntegrated", 1, false, scalar(func(in *Inputs) float64 { return in.ChangesIntegrated })},
	{"changesConflicted", 1, false, scalar(func(in *Inputs) float64 { return in.ChangesConflicted })},
	{"worktrees", 1, false, scalar(func(in *Inputs) float64 { return in.Worktrees })},
	{"writingWorktrees", 1, false, scalar(func(in *Inputs) float64 { return in.WritingWorktrees })},

	{"teamRounds", 1, false, scalar(func(in *Inputs) float64 { return in.TeamRounds })},
	{"providersReady", 1, false, scalar(func(in *Inputs) float64 { return in.ProvidersReady })},
	{"providersBlocked", 1, false, scalar(func(in *Inputs) float64 { return in.ProvidersBlocked })},
	{"eventCount", 1, false, scalar(func(in *Inputs) float64 { return in.EventCount })},

	{"eventAge", 1, false, scalar(func(in *Inputs) float64 { return in.EventAge })},
	{"messageAge", 1, false, scalar(func(in *Inputs) float64 { return in.MessageAge })},
	{"progressAge", 1, false, scalar(func(in *Inputs) float64 { return in.ProgressAge })},
	{"startAge", 1, false, scalar(func(in *Inputs) float64 { return in.StartAge })},
	{"successAge", 1, false, scalar(func(in *Inputs) float64 { return in.SuccessAge })},
	{"failureAge", 1, false, scalar(func(in *Inputs) float64 { return in.FailureAge })},
	{"changeAge", 1, false, scalar(func(in *Inputs) float64 { return in.ChangeAge })},
	{"decisionRequestAge", 1, false, scalar(func(in *Inputs) float64 { return in.DecisionRequestAge })},
	{"decisionAnswerAge", 1, false, scalar(func(in *Inputs) float64 { return in.DecisionAnswerAge })},
	{"contextAge", 1, false, scalar(func(in *Inputs) float64 { return in.ContextAge })},
	{"teamAge", 1, false, scalar(func(in *Inputs) float64 { return in.TeamAge })},
	{"inputAge", 1, false, scalar(func(in *Inputs) float64 { return in.InputAge })},

	{"promptLength", 1, false, scalar(func(in *Inputs) float64 { return in.PromptLength })},
	{"attachments", 1, false, scalar(func(in *Inputs) float64 { return in.Attachments })},
	{"busy", 1, false, scalar(func(in *Inputs) float64 { return in.Busy })},
	{"errorActive", 1, false, scalar(func(in *Inputs) float64 { return in.ErrorActive })},

	{"focus", 2, false, vec2(func(in *Inputs) Value { return in.Focus })},
	{"focusStrength", 1, false, scalar(func(in *Inputs) float64 { return in.FocusStrength })},

	{"energy", 1, false, scalar(func(in *Inputs) float64 { return in.Energy })},
	{"activity", 1, false, scalar(func(in *Inputs) float64 { return in.Activity })},
	{"attention", 1, false, scalar(func(in *Inputs) float64 { return in.Attention })},
	{"tension", 1, false, scalar(func(in *Inputs) float64 { return in.Tension })},
	{"success", 1, false, scalar(func(in *Inputs) float64 { return in.Success })},
	{"progress", 1, false, scalar(func(in *Inputs) float64 { return in.Progress })},
	{"typing", 1, false, scalar(func(in *Inputs) float64 { return in.Typing })},
}

var uniformIndex = func() map[string]int {
	index := map[string]int{}
	for i, u := range uniforms {
		index[u.Name] = i
	}
	return index
}()

// Uniforms lists every input name and width in declaration order.
func Uniforms() []Uniform { return append([]Uniform(nil), uniforms...) }

// IdleInputs describes a resting arena of the given size: no run, no events.
func IdleInputs(width, height int) Inputs {
	in := Inputs{Resolution: Vec2(float64(width), float64(height)), Aspect: float64(width) / float64(height*2), RunIdle: 1, Focus: Vec2(.5, .5)}
	for _, age := range []*float64{&in.EventAge, &in.MessageAge, &in.ProgressAge, &in.StartAge, &in.SuccessAge, &in.FailureAge, &in.ChangeAge, &in.DecisionRequestAge, &in.DecisionAnswerAge, &in.ContextAge, &in.TeamAge, &in.InputAge} {
		*age = NeverAge
	}
	return in
}
