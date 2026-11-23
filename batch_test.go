package main

import (
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestRemainingHarvest(t *testing.T) {
	a, b, c, outside := common.Address{1}, common.Address{2}, common.Address{3}, common.Address{4}
	selected := []common.Address{c, b, a}
	rows := []*Pulse{
		{Who: outside, Ready: big.NewInt(99)},
		{Who: a, Ready: big.NewInt(1)},
		{Who: b, Ready: big.NewInt(0)},
		{Who: c, Ready: big.NewInt(3)},
	}
	check := func(want []common.Address, sum int64) {
		t.Helper()
		got, total, err := remainingHarvest(selected, rows)
		if err != nil || !slices.Equal(got, want) || total.Cmp(big.NewInt(sum)) != 0 {
			t.Fatalf("remainingHarvest = %v, %v, %v; want %v, %d", got, total, err, want, sum)
		}
	}
	check([]common.Address{c, a}, 4)
	rows[1].Ready, rows[2].Ready, rows[3].Ready = big.NewInt(0), big.NewInt(2), big.NewInt(0)
	check([]common.Address{b}, 2)
	rows[2].Ready = big.NewInt(0)
	check(nil, 0)
	if _, _, err := remainingHarvest(selected, rows[:3]); err == nil {
		t.Fatal("missing selected account reported as collected")
	}
}
