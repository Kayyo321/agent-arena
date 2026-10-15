package arena

import (
	"context"
	"fmt"
)

func uniqueStrings(values []string) []string {
	out := []string{}
	for _, value := range values {
		if value != "" && !Has(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func (p *Project) ShareContext(ctx context.Context, r Run, actor, from, to, scope string, data map[string]any) error {
	if from == to || from == "arena" || to == "arena" {
		return nil
	}
	switch r.Settings.ApprovalPolicies.CrossProviderContextSharing {
	case "deny":
		return fmt.Errorf("cross-provider context sharing is denied")
	case "ask-user":
		answer, err := p.Decide(ctx, r, actor, "cross-provider-context-sharing", "Share this task-scoped "+scope+" with "+to+"?", []string{"yes", "no"}, data)
		if err != nil {
			return err
		}
		if answer != "yes" {
			return fmt.Errorf("cross-provider context sharing declined")
		}
	}
	return nil
}
