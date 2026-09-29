package billingexpr_test

import (
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/assert"
)

func TestUsedUsageKeysExtractsLiteralCallsAndSkipsDynamicArguments(t *testing.T) {
	expression := `tier("base", u(" seconds ") * 0.4 + (u("clips") > 0 ? u("clips") * 0.1 : 0)) + (u(header("usage-key")) == nil ? 0 : 0)`

	keys := billingexpr.UsedUsageKeys(expression)

	assert.Equal(t, map[string]bool{"seconds": true, "clips": true}, keys)
}

func TestUsedUsageKeysReturnsNilForEmptyOrInvalidExpressions(t *testing.T) {
	assert.Nil(t, billingexpr.UsedUsageKeys(""))
	assert.Nil(t, billingexpr.UsedUsageKeys(`tier("broken",`))
}
