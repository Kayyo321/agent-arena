package arena

import (
	"encoding/json"
	"fmt"

	assets "github.com/Kayyo321/agent-arena"
)

type Compatibility struct {
	SchemaVersion int               `json:"schemaVersion"`
	ReviewedAt    string            `json:"reviewedAt"`
	Platforms     []string          `json:"platforms"`
	Adapters      []AdapterManifest `json:"adapters"`
}
type AdapterManifest struct {
	ID               string   `json:"id"`
	Interface        string   `json:"interface"`
	ObservedVersions []string `json:"observedVersions"`
	RequiredMethods  []string `json:"requiredMethods,omitempty"`
	RequiredFlags    []string `json:"requiredFlags,omitempty"`
	Auth             string   `json:"auth"`
	Verification     string   `json:"verification"`
	Source           string   `json:"source"`
	Backends         []string `json:"backends,omitempty"`
}

func Manifest() Compatibility {
	b, err := assets.Files.ReadFile("providers/compatibility.json")
	if err != nil {
		panic(err)
	}
	var m Compatibility
	if err := json.Unmarshal(b, &m); err != nil {
		panic(err)
	}
	return m
}
func VersionDrift(provider, version string) string {
	for _, a := range Manifest().Adapters {
		if a.ID == provider {
			if len(a.ObservedVersions) == 0 {
				return "backend version unverified"
			}
			if Has(a.ObservedVersions, version) {
				return "matches a version inspected during implementation"
			}
			return fmt.Sprintf("version drift: %s was not among inspected versions; capability checks still apply", version)
		}
	}
	return "unknown adapter"
}
