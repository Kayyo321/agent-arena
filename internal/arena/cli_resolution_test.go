package arena

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func cliResolutionSetup(t *testing.T) (*Project, Run, Change, Change) {
	t.Helper()
	p, r := resolutionRun(t)
	alpha := propose(t, p, r, "alpha", map[string]string{"a.txt": "a\n"})
	beta := propose(t, p, r, "beta", map[string]string{"b.txt": "b\n"})
	succeedRun(t, p, r)
	return p, r, alpha, beta
}

func runCLI(p *Project, args ...string) error {
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	return cli.Run(context.Background(), append(args, "--project", p.Root))
}

func TestIntegrateAllRejectsExtraChangeIDs(t *testing.T) {
	p, r, alpha, beta := cliResolutionSetup(t)
	head := resolutionGit(t, p.Root, "rev-parse", "HEAD")
	if err := runCLI(p, "integrate", r.ID, "--all", "--skip", alpha.ID, beta.ID, "--yes"); err == nil {
		t.Fatal("expected error for extra change IDs with --all")
	}
	for _, id := range []string{alpha.ID, beta.ID} {
		if s := changeState(t, p, r, id); s != "proposed" {
			t.Fatalf("change %s state = %s, want proposed", id, s)
		}
	}
	if got := resolutionGit(t, p.Root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %s -> %s", head, got)
	}
}

func TestSingleChangeIntegrateRejectsSkipAndDryRun(t *testing.T) {
	p, r, alpha, beta := cliResolutionSetup(t)
	if err := runCLI(p, "integrate", r.ID, beta.ID, "--skip", alpha.ID, "--yes"); err == nil {
		t.Fatal("expected error for --skip on single change")
	}
	if err := runCLI(p, "integrate", r.ID, beta.ID, "--dry-run"); err == nil {
		t.Fatal("expected error for --dry-run on single change")
	}
	if s := changeState(t, p, r, beta.ID); s != "proposed" {
		t.Fatalf("beta state = %s, want proposed", s)
	}
}

func TestIntegrateAllWithRepeatedSkipAppliesTheRest(t *testing.T) {
	p, r := resolutionRun(t)
	alpha := propose(t, p, r, "alpha", map[string]string{"a.txt": "a\n"})
	beta := propose(t, p, r, "beta", map[string]string{"b.txt": "b\n"})
	gamma := propose(t, p, r, "gamma", map[string]string{"c.txt": "c\n"})
	succeedRun(t, p, r)
	if err := runCLI(p, "integrate", r.ID, "--all", "--skip", alpha.ID, "--skip", gamma.ID, "--yes"); err != nil {
		t.Fatal(err)
	}
	if s := changeState(t, p, r, alpha.ID); s != "proposed" {
		t.Fatalf("alpha state = %s, want proposed", s)
	}
	if s := changeState(t, p, r, beta.ID); s != "integrated" {
		t.Fatalf("beta state = %s, want integrated", s)
	}
	if s := changeState(t, p, r, gamma.ID); s != "proposed" {
		t.Fatalf("gamma state = %s, want proposed", s)
	}
}
