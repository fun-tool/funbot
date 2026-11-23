package main

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type dailyWinsCache struct {
	first  *types.Header
	counts map[common.Address]uint64
}

func beijingDay(timestamp uint64) uint64 { return (timestamp + 8*3600) / 86400 }

func (c *Chain) header(ctx context.Context, number *big.Int) (*types.Header, error) {
	var h *types.Header
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		var err error
		h, err = cl.HeaderByNumber(ctx, number)
		return err
	})
	return h, err
}

func (c *Chain) firstBlockSince(ctx context.Context, head *types.Header, start uint64) (*types.Header, error) {
	lo, err := c.header(ctx, new(big.Int))
	if err != nil || lo.Time >= start {
		return lo, err
	}
	hi := head
	for attempt := 0; hi.Number.Uint64()-lo.Number.Uint64() > 1; attempt++ {
		span := hi.Number.Uint64() - lo.Number.Uint64()
		step := span / 2
		if attempt < 5 {
			// Estimate by timestamps, then bisect if block intervals were irregular.
			step = uint64(float64(span) * (float64(start-lo.Time) / float64(hi.Time-lo.Time)))
			step = min(span-1, max(uint64(1), step))
		}
		probe, err := c.header(ctx, new(big.Int).SetUint64(lo.Number.Uint64()+step))
		if err != nil {
			return nil, err
		}
		if probe.Time < start {
			lo = probe
		} else {
			hi = probe
		}
	}
	if hi.ParentHash != lo.Hash() {
		return nil, fmt.Errorf("零点区块已重组，请刷新重试")
	}
	return hi, nil
}

func (s *Squad) dailyWins(ctx context.Context, head *types.Header, arena common.Address, wallets []common.Address, current map[common.Address]uint64, canonical *types.Header) (map[common.Address]uint64, error) {
	result := make(map[common.Address]uint64, len(wallets))
	var missing []common.Address
	day := beijingDay(head.Time)
	cache := &s.daily
	valid := canonical != nil && cache.first != nil && canonical.Hash() == cache.first.Hash() && beijingDay(canonical.Time) == day && canonical.Number.Cmp(head.Number) <= 0
	if !valid {
		*cache = dailyWinsCache{}
	}
	var firstErr error
	for _, a := range wallets {
		n, ok := current[a]
		if !ok {
			firstErr = fmt.Errorf("部分账号的累计盟主次数尚未读到")
			continue
		}
		if base, ok := cache.counts[a]; ok {
			if n < base {
				firstErr = fmt.Errorf("当前盟主次数低于零点基准，请刷新重试")
				continue
			}
			result[a] = n - base
		} else if n == 0 {
			result[a] = 0
		} else {
			missing = append(missing, a)
		}
	}
	if len(missing) == 0 {
		return result, firstErr
	}
	if cache.first == nil {
		var start uint64
		if day > 0 {
			start = day*86400 - 8*3600
		}
		first, err := s.chain.firstBlockSince(ctx, head, start)
		if err != nil {
			return result, err
		}
		if beijingDay(first.Time) != day {
			return result, fmt.Errorf("零点区块已变化，请刷新重试")
		}
		cache.first, cache.counts = first, make(map[common.Address]uint64)
	}
	reqs := make([]rpc.BatchElem, len(missing))
	if cache.first.Number.Sign() != 0 {
		for i, a := range missing {
			data, err := extraReadABI.Pack("jiaziCount", a)
			if err != nil {
				return result, err
			}
			reqs[i] = rpc.BatchElem{Method: "eth_call", Args: []any{map[string]any{"to": arena, "data": hexutil.Bytes(data)}, rpc.BlockNumberOrHashWithHash(cache.first.ParentHash, true)}, Result: new(hexutil.Bytes)}
		}
		s.chain.readBatch(ctx, reqs)
	}
	for i, a := range missing {
		var base uint64
		if cache.first.Number.Sign() != 0 {
			if err := reqs[i].Error; err != nil {
				firstErr = fmt.Errorf("读取零点前的累计盟主次数失败：%w", err)
				continue
			}
			raw := *reqs[i].Result.(*hexutil.Bytes)
			n := new(big.Int).SetBytes(raw)
			if len(raw) != 32 || !n.IsUint64() {
				firstErr = fmt.Errorf("零点累计盟主次数无效")
				continue
			}
			base = n.Uint64()
		}
		if current[a] < base {
			firstErr = fmt.Errorf("当前盟主次数低于零点基准，请刷新重试")
			continue
		}
		cache.counts[a], result[a] = base, current[a]-base
	}
	return result, firstErr
}
