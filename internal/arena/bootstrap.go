package arena

import (
	"context"
	"fmt"

	assets "github.com/Kayyo321/agent-arena"
)

func (cli *CLI) bootstrap(ctx context.Context, p *Project, s Settings) error {
	if cli.isTerminal() {
		return cli.setupUI(ctx, p, s)
	}
	status := Probe(ctx, p.Root, s.DefaultModel)
	if !status.CanStart {
		return fmt.Errorf("setup conversation cannot start: %s (%s). Complete provider setup, then use arena bootstrap", status.Detail, status.Auth)
	}
	// API/local hosts use the same embedded skill and submit a builder request to
	// Arena's authoring command. Models never write generated configuration files.
	transcript := "Use the arena-orchestrator skill below to interview the user. Ask one concise question for missing choices. Do not invent providers, model IDs, effort, roles, edges, teams, router matches, or triggers. Once the user has supplied everything, return a builder request JSON with workflow and profiles following the embedded schemas. Set complete=true only then.\n" + string(assets.Skill())
	if sheet, err := assets.Files.ReadFile("docs/ARENA-SCORING.md"); err == nil {
		transcript += "\nArena grades the arena with this score sheet; design toward a high grade without adding agents only for points:\n" + string(sheet)
	}
	for _, name := range []string{"workflow", "agent"} {
		b, err := assets.Files.ReadFile("schemas/" + name + ".schema.json")
		if err != nil {
			return err
		}
		transcript += "\nSchema: " + string(b)
	}
	schema := objectSchema(map[string]any{"question": map[string]any{"type": "string"}, "complete": map[string]any{"type": "boolean"}, "builderRequestJson": map[string]any{"type": "string"}})
	for round := 0; round < 50; round++ {
		bounded, cancel := inferenceDeadline(ctx, s.Limits.MaxAgentRunMinutes)
		result, err := (Providers{}).Invoke(bounded, Invocation{Runtime: s.DefaultModel, Dir: p.Root, Prompt: transcript, Schema: schema})
		cancel()
		if err != nil {
			return err
		}
		if result["complete"] == true {
			file := p.Path(".state", "builder-requests", ID("builder-")+".json")
			if err := Atomic(file, []byte(str(result["builderRequestJson"])), 0600); err != nil {
				return err
			}
			var request struct {
				Workflow Workflow           `json:"workflow"`
				Profiles map[string]Profile `json:"profiles"`
			}
			if err := ReadJSON(file, &request); err != nil {
				return err
			}
			return cli.workflow(ctx, p, []string{"create", request.Workflow.Id}, Options{"input": []string{file}})
		}
		question := str(result["question"])
		answer, err := cli.ask(question, "", true)
		if err != nil {
			return err
		}
		transcript += "\nAssistant: " + question + "\nUser: " + answer
		if len(transcript) > 2<<20 {
			return fmt.Errorf("setup conversation exceeded its bounded context size")
		}
	}
	return fmt.Errorf("setup question limit reached; continue with arena workflow create")
}
