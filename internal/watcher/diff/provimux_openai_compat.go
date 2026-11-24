package diff

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// provimuxOpenAICompatDetails are the reload log's details for the two fields Provimux adds to an
// openai-compatibility entry (key-model-lists.md 6.1), in the form the built-in keys' lines have.
func provimuxOpenAICompatDetails(oldEntry, newEntry config.OpenAICompatibility) []string {
	var details []string
	if oldEntry.FixedModels != newEntry.FixedModels {
		details = append(details, fmt.Sprintf("fixed-models %t -> %t", oldEntry.FixedModels, newEntry.FixedModels))
	}
	oldExcluded := SummarizeExcludedModels(oldEntry.ExcludedModels)
	newExcluded := SummarizeExcludedModels(newEntry.ExcludedModels)
	if oldExcluded.hash != newExcluded.hash {
		details = append(details, fmt.Sprintf("excluded-models updated (%d -> %d entries)", oldExcluded.count, newExcluded.count))
	}
	return details
}
