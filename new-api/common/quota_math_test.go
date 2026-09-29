package common

import (
	"fmt"
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2000 quota per call * n=18446744073686646784 overflows int64; the constant
// below reproduces that oversized product for the saturation checks.
const overflowingProduct = 2000 * 1.8446744073686647e19

// TestQuotaFromFloat guards the billing invariant that oversized quota
// products (e.g. price multiplied by a huge user-supplied count) saturate
// instead of wrapping into a negative charge (credit). QuotaFromFloat
// truncates toward zero.
func TestQuotaFromFloat(t *testing.T) {
	assert.Equal(t, 42, QuotaFromFloat(42.4))
	assert.Equal(t, 42, QuotaFromFloat(42.9))
	assert.Equal(t, -42, QuotaFromFloat(-42.9))
	assert.Equal(t, MaxQuota, QuotaFromFloat(float64(math.MaxInt32)+42))
	assert.Equal(t, MaxQuota, QuotaFromFloat(overflowingProduct))
	assert.Equal(t, MinQuota, QuotaFromFloat(-overflowingProduct))
	assert.Equal(t, MaxQuota, QuotaFromFloat(math.Inf(1)))
	assert.Equal(t, MinQuota, QuotaFromFloat(math.Inf(-1)))
	assert.Equal(t, 0, QuotaFromFloat(math.NaN()))
}

// TestQuotaRound checks half-away-from-zero rounding with the same
// saturation policy.
func TestQuotaRound(t *testing.T) {
	assert.Equal(t, 42, QuotaRound(41.5))
	assert.Equal(t, 43, QuotaRound(42.5))
	assert.Equal(t, -43, QuotaRound(-42.5))
	assert.Equal(t, MaxQuota, QuotaRound(float64(math.MaxInt32)+0.5))
	assert.Equal(t, MaxQuota, QuotaRound(overflowingProduct))
	assert.Equal(t, MinQuota, QuotaRound(-overflowingProduct))
	assert.Equal(t, 0, QuotaRound(math.NaN()))
}

// TestQuotaFromDecimal checks the decimal entry point rounds and saturates
// consistently with the float variants.
func TestQuotaFromDecimal(t *testing.T) {
	assert.Equal(t, 43, QuotaFromDecimal(decimal.NewFromFloat(42.5)))
	assert.Equal(t, 42, QuotaFromDecimal(decimal.NewFromFloat(41.7)))
	assert.Equal(t, MaxQuota, QuotaFromDecimal(decimal.NewFromInt(2000).Mul(decimal.NewFromFloat(1.8446744073686647e19))))
	assert.Equal(t, MinQuota, QuotaFromDecimal(decimal.NewFromInt(-2000).Mul(decimal.NewFromFloat(1.8446744073686647e19))))
}

// TestQuotaFromFloatChecked verifies the clamp descriptor is nil in range and
// carries the correct kind/clamped value on saturation, so billing callers can
// audit the event.
func TestQuotaFromFloatChecked(t *testing.T) {
	quota, clamp := QuotaFromFloatChecked(42.9)
	assert.Equal(t, 42, quota)
	assert.Nil(t, clamp)

	quota, clamp = QuotaFromFloatChecked(overflowingProduct)
	assert.Equal(t, MaxQuota, quota)
	if assert.NotNil(t, clamp) {
		assert.Equal(t, "QuotaFromFloat", clamp.Op)
		assert.Equal(t, QuotaClampOverflow, clamp.Kind)
		assert.Equal(t, MaxQuota, clamp.Clamped)
	}

	quota, clamp = QuotaFromFloatChecked(-overflowingProduct)
	assert.Equal(t, MinQuota, quota)
	if assert.NotNil(t, clamp) {
		assert.Equal(t, QuotaClampUnderflow, clamp.Kind)
		assert.Equal(t, MinQuota, clamp.Clamped)
	}

	quota, clamp = QuotaFromFloatChecked(math.NaN())
	assert.Equal(t, 0, quota)
	if assert.NotNil(t, clamp) {
		assert.Equal(t, QuotaClampNaN, clamp.Kind)
		assert.Equal(t, 0, clamp.Clamped)
	}
}

func TestQuotaFromFloatStrictReturnsTypedClampError(t *testing.T) {
	quota, err := QuotaFromFloatStrict(42.9)
	require.NoError(t, err)
	assert.Equal(t, 42, quota)

	quota, err = QuotaFromFloatStrict(overflowingProduct)
	assert.Zero(t, quota)
	var clamp *QuotaClamp
	require.ErrorAs(t, err, &clamp)
	assert.Equal(t, QuotaClampOverflow, clamp.Kind)
	assert.Equal(t, MaxQuota, clamp.Clamped)
	assert.ErrorContains(t, err, "QuotaFromFloat")
	assert.ErrorContains(t, err, "overflow")
	assert.ErrorContains(t, err, "original=")
	assert.ErrorContains(t, err, fmt.Sprintf("clamped=%d", MaxQuota))
}

// TestQuotaRoundChecked verifies the rounding entry point reports clamps the
// same way.
func TestQuotaRoundChecked(t *testing.T) {
	quota, clamp := QuotaRoundChecked(42.5)
	assert.Equal(t, 43, quota)
	assert.Nil(t, clamp)

	quota, clamp = QuotaRoundChecked(overflowingProduct)
	assert.Equal(t, MaxQuota, quota)
	if assert.NotNil(t, clamp) {
		assert.Equal(t, "QuotaRound", clamp.Op)
		assert.Equal(t, QuotaClampOverflow, clamp.Kind)
	}
}

// TestQuotaFromDecimalChecked verifies the decimal entry point reports clamps.
func TestQuotaFromDecimalChecked(t *testing.T) {
	quota, clamp := QuotaFromDecimalChecked(decimal.NewFromFloat(41.7))
	assert.Equal(t, 42, quota)
	assert.Nil(t, clamp)

	quota, clamp = QuotaFromDecimalChecked(decimal.NewFromInt(2000).Mul(decimal.NewFromFloat(1.8446744073686647e19)))
	assert.Equal(t, MaxQuota, quota)
	if assert.NotNil(t, clamp) {
		assert.Equal(t, "QuotaFromDecimal", clamp.Op)
		assert.Equal(t, QuotaClampOverflow, clamp.Kind)
	}
}

func TestWalletQuotaFromDecimalStrict(t *testing.T) {
	quota, err := WalletQuotaFromDecimalStrict(decimal.NewFromInt(4_294_500_000))
	require.NoError(t, err)
	assert.Equal(t, 4_294_500_000, quota)

	quota, err = WalletQuotaFromDecimalStrict(decimal.NewFromInt(MaxWalletQuota))
	require.NoError(t, err)
	assert.Equal(t, MaxWalletQuota, quota)

	quota, err = WalletQuotaFromDecimalStrict(decimal.NewFromInt(MaxWalletQuota + 1))
	assert.Zero(t, quota)
	var clamp *QuotaClamp
	require.ErrorAs(t, err, &clamp)
	assert.Equal(t, "WalletQuotaFromDecimal", clamp.Op)
	assert.Equal(t, QuotaClampOverflow, clamp.Kind)
}
