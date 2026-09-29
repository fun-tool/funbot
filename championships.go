package main

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var historyABI = func() abi.ABI {
	addressType, _ := abi.NewType("address", "", nil)
	uintType, _ := abi.NewType("uint256", "", nil)
	method := abi.NewMethod("playerHistory", "playerHistory", abi.Function, "view", true, false,
		abi.Arguments{{Type: addressType}, {Type: uintType}, {Type: uintType}}, settlementABI.Methods["playerHistoryLatest"].Outputs)
	return abi.ABI{Methods: map[string]abi.Method{"playerHistory": method}}
}()

type championHistory struct {
	Settled, Wins uint64
}

func (s *Squad) ChampionshipCounts(ctx context.Context, arena common.Address, who []common.Address) (map[common.Address]uint64, error) {
	s.champMu.Lock()
	defer s.champMu.Unlock()
	var head, previous *types.Header
	err := s.chain.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		var err error
		head, err = cl.HeaderByNumber(ctx, nil)
		if err != nil {
			return err
		}
		previous = nil
		if s.champHead != nil && head.Number.Cmp(s.champHead.Number) > 0 {
			previous, err = cl.HeaderByNumber(ctx, s.champHead.Number)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	valid := s.champHead != nil && (head.Hash() == s.champHead.Hash() || previous != nil && previous.Hash() == s.champHead.Hash())
	cache := make(map[common.Address]championHistory, len(s.champions))
	if valid {
		for a, v := range s.champions {
			cache[a] = v
		}
	}
	active := make([]common.Address, 0, len(who))
	for _, a := range who {
		if _, ok := cache[a]; !ok || !valid || head.Hash() != s.champHead.Hash() {
			active = append(active, a)
		}
	}
	tag := hexutil.EncodeBig(head.Number)
	for len(active) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := make([][]byte, len(active))
		for i, a := range active {
			data[i], _ = historyABI.Pack("playerHistory", a, new(big.Int).SetUint64(cache[a].Settled), big.NewInt(64))
		}
		out, errs := s.chain.CallBatchAt(ctx, arena, data, tag)
		var next []common.Address
		for i, a := range active {
			if errs[i] != nil {
				return nil, fmt.Errorf("读取 %s 盟主战绩失败：%w", short(a), errs[i])
			}
			values, err := historyABI.Unpack("playerHistory", out[i])
			if err != nil {
				return nil, err
			}
			rows := *abi.ConvertType(values[0], new([]battleStatus)).(*[]battleStatus)
			total := values[1].(*big.Int)
			v := cache[a]
			if !total.IsUint64() || total.Uint64() < v.Settled || uint64(len(rows)) != min(uint64(64), total.Uint64()-v.Settled) {
				return nil, fmt.Errorf("%s 盟主战绩分页不完整", short(a))
			}
			pending := false
			for _, row := range rows {
				if !row.Settled {
					pending = true
					break
				}
				if row.Won {
					v.Wins++
				}
				v.Settled++
			}
			cache[a] = v
			if !pending && v.Settled < total.Uint64() {
				next = append(next, a)
			}
		}
		active = next
	}
	s.champHead, s.champions = head, cache
	counts := make(map[common.Address]uint64, len(who))
	for _, a := range who {
		counts[a] = cache[a].Wins
	}
	return counts, nil
}
