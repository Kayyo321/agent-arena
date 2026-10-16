package arena

import (
	"strings"
	"testing"
)

func TestRedactCoversShareCodeTokenPatterns(t *testing.T) {
	// Fixture values shaped like real tokens; none is a real credential.
	for _, secret := range []string{
		"AKIA" + strings.Repeat("Q", 16),
		"aws_secret_access_key = " + strings.Repeat("s", 40),
		"xoxb-" + strings.Repeat("1", 12) + "-abcdefabcdef",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlc2ln",
		"github_pat_" + strings.Repeat("A", 30),
		"ghp_" + strings.Repeat("a", 30),
		"sk_live_" + strings.Repeat("9", 20),
		"ya29." + strings.Repeat("x", 30),
		"sk-" + strings.Repeat("k", 20),
		"Authorization: Bearer abc.def",
	} {
		if got := Redact("log line " + secret + " end"); strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
			t.Errorf("Redact kept %q: %q", secret, got)
		}
	}
	for _, plain := range []string{"/home/dev/project", "AKIA is not a key", "eyJ alone", "run sk-short"} {
		if got := Redact(plain); got != plain {
			t.Errorf("Redact changed ordinary text %q to %q", plain, got)
		}
	}
}
