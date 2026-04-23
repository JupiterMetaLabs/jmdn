package helper

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
)

func TestConvertBigToUint256(t *testing.T) {
	t.Run("Valid Conversion", func(t *testing.T) {
		b := big.NewInt(123456789)
		u256, overflow := ConvertBigToUint256(b)
		assert.False(t, overflow)
		assert.NotNil(t, u256)
		fmt.Println("u256:", u256)

		expected := uint256.NewInt(123456789)
		assert.True(t, u256.Eq(expected), "Expected uint256 value to match input")
	})

	t.Run("Overflow Conversion", func(t *testing.T) {
		// Simulate overflow: big.Int > 256 bits
		bigVal := new(big.Int).Exp(big.NewInt(2), big.NewInt(300), nil) // 2^300
		u256, overflow := ConvertBigToUint256(bigVal)
		assert.True(t, overflow)
		assert.Nil(t, u256)
		fmt.Println("u256:", u256)
	})
}

func TestBigIntToUint64Safe(t *testing.T) {
	t.Run("Valid uint64 Conversion", func(t *testing.T) {
		b := big.NewInt(123456789)
		val, err := BigIntToUint64Safe(b)
		assert.NoError(t, err)
		assert.Equal(t, uint64(123456789), val)
		fmt.Println("val:", val)
	})

	t.Run("Negative Value", func(t *testing.T) {
		b := big.NewInt(-1)
		_, err := BigIntToUint64Safe(b)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "negative")
		fmt.Println("val:", b)
		fmt.Println("err:", err)
	})

	t.Run("Overflow Value", func(t *testing.T) {
		// 2^65 > MaxUint64
		bigVal := new(big.Int).Exp(big.NewInt(2), big.NewInt(65), nil)
		b, err := BigIntToUint64Safe(bigVal)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "too large")
		fmt.Println("val:", b)
		fmt.Println("err:", err)
	})
}
