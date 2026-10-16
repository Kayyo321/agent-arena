package arena

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Share codes leave this machine, so export fails closed: every string in a
// bundle (values and map keys) is checked against the patterns below, runtime
// options must be documented ones, and nothing is ever redacted silently. The
// receiver gets exactly what the author saw, or no code at all. Imports apply
// the same checks so a leaked secret is never saved into another project.

type sharePattern struct {
	label string
	re    *regexp.Regexp
}

var shareSensitive = []sharePattern{
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`)},
	{"an AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"an AWS secret key", regexp.MustCompile(`(?i)aws_?secret_?access_?key`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"a JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"a GitHub token", regexp.MustCompile(`\b(?:github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,})`)},
	{"an API key", regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|[rs]k_live_[0-9A-Za-z]{16,}|AIza[0-9A-Za-z_-]{20,}|ya29\.[0-9A-Za-z_-]{20,})`)},
	{"a credential assignment", regexp.MustCompile(`(?i)\b[a-z_]*(?:secret|token|passwd|password|pwd|private_?key|api_?key|credential)[a-z_]*["']?\s*[:=]\s*["']?[A-Za-z0-9_\-./+=]{8,}`)},
	{"a URL with a user name or password", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s@]+@`)},
	{"a home directory path (it names a user)", regexp.MustCompile(`(?:^|[^A-Za-z0-9_.~-])/(?:home|Users)/[^/\s"'*]+|(?i)\b[A-Z]:\\Users\\[^\\\s]+`)},
	{"an email address", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)},
	{"an internal host name", regexp.MustCompile(`(?i)\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:internal|corp|lan|intranet|localdomain|home\.arpa)\b`)},
}

var (
	shareIPv4    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	shareTokenly = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

// shareIssue describes the first field that may not travel, without its value.
type shareIssue struct{ path, label string }

func (i shareIssue) Error() string { return i.path + " contains " + i.label }

// highEntropy flags long mixed-case alphanumeric runs typical of keys. Long
// identifiers in prose stay below the threshold; random keys sit near 4.6+.
func highEntropy(s string) bool {
	var upper, lower, digit bool
	counts := map[rune]float64{}
	for _, r := range s {
		upper = upper || unicode.IsUpper(r)
		lower = lower || unicode.IsLower(r)
		digit = digit || unicode.IsDigit(r)
		counts[r]++
	}
	if !upper || !lower || !digit {
		return false
	}
	entropy := 0.0
	for _, n := range counts {
		p := n / float64(len(s))
		entropy -= p * math.Log2(p)
	}
	return entropy >= 4.5
}

func controlCharacter(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return true
		}
	}
	return false
}

// scanShareString reports what in s must not be shared, or "".
func scanShareString(s string, local []string) string {
	if controlCharacter(s) {
		return "terminal control characters"
	}
	for _, l := range local {
		if l != "" && strings.Contains(s, l) {
			return "a path on this machine"
		}
	}
	if Redact(s) != s {
		return "a credential"
	}
	for _, p := range shareSensitive {
		if p.re.MatchString(s) {
			return p.label
		}
	}
	for _, ip := range shareIPv4.FindAllString(s, -1) {
		if !strings.HasPrefix(ip, "127.") && ip != "0.0.0.0" {
			return "an IP address"
		}
	}
	for _, run := range shareTokenly.FindAllString(s, -1) {
		if highEntropy(run) {
			return "a long random-looking token"
		}
	}
	return ""
}

// shareLocalStrings are this machine's paths that must never appear in a code.
func shareLocalStrings(p *Project) []string {
	local := []string{}
	if p != nil && len(p.Root) > 1 {
		local = append(local, p.Root)
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		local = append(local, home)
	}
	return local
}

// checkShareable walks every string and key in the bundle and checks runtime
// options against the documented ones for each provider.
func checkShareable(b ShareBundle, local []string) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	var tree any
	if err := DecodeJSON(data, &tree); err != nil {
		return err
	}
	if issue := walkShare(tree, "", local); issue != nil {
		return *issue
	}
	if b.Workflow != nil {
		for i, a := range b.Workflow.Agents {
			if err := checkShareOptions(a.Runtime, fmt.Sprintf("workflow.agents[%d] (%s).runtime.options", i, a.Id)); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkShare(v any, path string, local []string) *shareIssue {
	join := func(key string) string {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	switch x := v.(type) {
	case string:
		if label := scanShareString(x, local); label != "" {
			return &shareIssue{path, label}
		}
	case []any:
		for i, item := range x {
			if issue := walkShare(item, fmt.Sprintf("%s[%d]", path, i), local); issue != nil {
				return issue
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if label := scanShareString(k, local); label != "" {
				return &shareIssue{join("<key>"), label}
			}
			if issue := walkShare(x[k], join(k), local); issue != nil {
				return issue
			}
		}
	}
	return nil
}

var (
	codexConfigKeys      = []string{"model_reasoning_effort", "service_tier"}
	localGenerationKeys  = []string{"temperature", "top_p", "max_tokens", "seed", "reasoning_effort"}
	geminiGenerationKeys = []string{"temperature", "topP", "topK", "candidateCount", "maxOutputTokens", "stopSequences", "presencePenalty", "frequencyPenalty", "seed", "responseMimeType", "thinkingConfig"}
	ollamaOptionKey      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

// checkShareOptions allows only the runtime options each adapter documents,
// with plain values. Anything else stays on this machine.
func checkShareOptions(r Runtime, path string) error {
	refuse := func(key, why string) error {
		return shareIssue{path + "." + key, why}
	}
	allowed := map[string][]string{
		"codex":       {"config"},
		"claude-code": {},
		"gemini":      {"authMethod", "generation"},
		"local":       {"backend", "endpoint", "generation"},
	}[r.Provider]
	for key, value := range r.Options {
		if !Has(allowed, key) {
			return refuse(key, "an option "+r.Provider+" does not document; share codes carry documented options only")
		}
		switch key {
		case "authMethod", "backend":
			if _, ok := value.(string); !ok {
				return refuse(key, "a non-text value")
			}
		case "endpoint":
			text, _ := value.(string)
			u, err := url.Parse(text)
			if err != nil || u.User != nil {
				return refuse(key, "a URL with a user name or password")
			}
			if _, err := LocalEndpoint(r); err != nil {
				return refuse(key, "an endpoint that is not on this machine's loopback address")
			}
		case "config":
			config, ok := value.(map[string]any)
			if !ok {
				return refuse(key, "a non-object value")
			}
			for k, v := range config {
				if _, ok := v.(string); !Has(codexConfigKeys, k) || !ok {
					return refuse(key+"."+k, "a Codex config entry that is not documented for Arena")
				}
			}
		case "generation":
			generation, ok := value.(map[string]any)
			if !ok {
				return refuse(key, "a non-object value")
			}
			for k, v := range generation {
				known := Has(localGenerationKeys, k)
				switch {
				case r.Provider == "gemini":
					known = Has(geminiGenerationKeys, k)
				case str(r.Options["backend"]) == "ollama":
					known = ollamaOptionKey.MatchString(k)
				}
				if !known || !plainShareValue(v, k == "thinkingConfig") {
					return refuse(key+"."+k, "a generation option that is not documented or not a plain number, flag or short text")
				}
			}
		}
	}
	return nil
}

// plainShareValue accepts numbers, booleans, short text and short lists of
// them; nested is allowed one level for Gemini's thinkingConfig.
func plainShareValue(v any, nested bool) bool {
	switch x := v.(type) {
	case json.Number, float64, int, int64, bool:
		return true
	case string:
		return len(x) <= 64
	case []any:
		if len(x) > 16 {
			return false
		}
		for _, item := range x {
			if !plainShareValue(item, false) {
				return false
			}
		}
		return true
	case map[string]any:
		if !nested {
			return false
		}
		for k, item := range x {
			if (k != "includeThoughts" && k != "thinkingBudget") || !plainShareValue(item, false) {
				return false
			}
		}
		return true
	}
	return false
}

// terminalSafe strips escape sequences and control characters before text
// from a share code reaches the terminal.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
