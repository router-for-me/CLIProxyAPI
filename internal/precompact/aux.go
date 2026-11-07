package precompact

import "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

// AuxPromptShare is the fraction of the auxiliary model's window that one
// transcript chunk may use; the rest is headroom for the instruction, the
// previous summary and the summary output.
const AuxPromptShare = 0.7

// AuxBudget returns how many transcript tokens fit in one aux call for model,
// using the same registry lookup as ModelWindow (default 200k).
func AuxBudget(reg *registry.ModelRegistry, model string) int {
	budget := int(float64(ModelWindow(reg, model)) * AuxPromptShare)
	if budget < 1 {
		budget = 1
	}
	return budget
}
