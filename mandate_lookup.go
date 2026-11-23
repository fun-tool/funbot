package main

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

type mandatePageRequest struct {
	method string
	who    common.Address
	offset uint64
	limit  uint64
	probe  bool
}

func mandatePages(wallets []common.Address, rewards bool, prev *MandateSnapshot) []mandatePageRequest {
	pages := make([]mandatePageRequest, 0, len(wallets)+1)
	for _, a := range wallets {
		if prev != nil {
			if old := prev.Accounts[a]; old != nil && old.probeCompleted {
				offset, limit := uint64(0), uint64(1)
				if old.Completed > 0 {
					offset, limit = old.Completed-1, 2
				}
				pages = append(pages, mandatePageRequest{method: "jiaziPage", who: a, offset: offset, limit: limit, probe: true})
			}
		}
		pages = append(pages, mandatePageRequest{method: "mandatePage", who: a})
	}
	if rewards {
		pages = append(pages, mandatePageRequest{method: "mandateRewardsPage"})
	}
	return pages
}

func (s *Squad) mandateBatch(block any, pages []mandatePageRequest) ([]rpc.BatchElem, error) {
	reqs := make([]rpc.BatchElem, len(pages))
	for i, p := range pages {
		offset := new(big.Int).SetUint64(p.offset)
		to, contractABI, args := s.impl, s.abi, []any{p.who, offset}
		switch p.method {
		case "mandateRewardsPage":
			args = []any{offset}
		case "jiaziPage":
			to, contractABI, args = Mainnet.Arena, extraReadABI, append(args, new(big.Int).SetUint64(p.limit))
		}
		data, err := contractABI.Pack(p.method, args...)
		if err != nil {
			return nil, err
		}
		reqs[i] = rpc.BatchElem{Method: "eth_call", Args: []any{map[string]any{"to": to, "data": hexutil.Bytes(data)}, block}, Result: new(hexutil.Bytes)}
	}
	return reqs, nil
}

func (s *Squad) readMandates(ctx context.Context, tag string, who []common.Address, rewards bool) (*MandateSnapshot, error) {
	snap, _, err := s.finishMandates(ctx, tag, mandatePages(who, rewards, nil), nil, false)
	return snap, err
}

func (s *Squad) finishMandates(ctx context.Context, block any, pages []mandatePageRequest, reqs []rpc.BatchElem, display bool) (*MandateSnapshot, map[common.Address]uint64, error) {
	snap := &MandateSnapshot{Accounts: make(map[common.Address]*MandateAccount)}
	totals := make(map[common.Address]uint64)
	amount := new(big.Int)
	for len(pages) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, totals, err
		}
		if reqs == nil {
			var err error
			reqs, err = s.mandateBatch(block, pages)
			if err != nil {
				return nil, totals, err
			}
			s.chain.readBatch(ctx, reqs)
		}
		var next []mandatePageRequest
		var firstErr error
		for i, p := range pages {
			err := reqs[i].Error
			if err == nil {
				var follow []mandatePageRequest
				follow, err = s.applyMandatePage(p, *reqs[i].Result.(*hexutil.Bytes), snap, totals, amount, display)
				next = append(next, follow...)
			}
			if err != nil {
				if p.probe {
					continue
				}
				err = fmt.Errorf("%s 第 %d 项：%w", p.method, p.offset, err)
				if display && p.method == "mandateRewardsPage" {
					snap.RewardError = err.Error()
					snap.Rewards = MandateRewards{}
					amount.SetUint64(0)
				} else if firstErr == nil {
					firstErr = err
				}
			}
		}
		if firstErr != nil {
			return nil, totals, firstErr
		}
		pages, reqs = next, nil
	}
	snap.Rewards.Count = len(snap.Rewards.ids)
	snap.Rewards.Amount = formatUnits(amount, 18)
	return snap, totals, nil
}

func (s *Squad) applyMandatePage(p mandatePageRequest, raw []byte, snap *MandateSnapshot, totals map[common.Address]uint64, amount *big.Int, display bool) ([]mandatePageRequest, error) {
	contractABI := s.abi
	if p.method == "jiaziPage" {
		contractABI = extraReadABI
	}
	v, err := contractABI.Unpack(p.method, raw)
	if err != nil {
		return nil, err
	}
	if p.method == "jiaziPage" {
		count := v[1].(*big.Int)
		rows := *abi.ConvertType(v[0], new([]JiaziRow)).(*[]JiaziRow)
		total := count.Uint64()
		if !count.IsUint64() || (!p.probe && total != totals[p.who]) || p.offset > total || uint64(len(rows)) != min(p.limit, total-p.offset) {
			return nil, fmt.Errorf("甲子俸分页不完整")
		}
		completed := uint64(0)
		for i, row := range rows {
			if !row.Index.IsUint64() || row.Index.Uint64() != p.offset+uint64(i) || !row.ValidDays.IsUint64() || row.ValidDays.Uint64() > 60 {
				return nil, fmt.Errorf("甲子俸记录无效")
			}
			if row.ValidDays.Uint64() == 60 {
				if completed != uint64(i) {
					return nil, fmt.Errorf("甲子俸完成顺序无效")
				}
				completed++
			}
		}
		if p.probe {
			if (p.offset == 0 || completed > 0) && (completed < uint64(len(rows)) || p.offset+completed == total) {
				snap.Accounts[p.who] = &MandateAccount{Completed: p.offset + completed, Remaining: 61, probeCompleted: true}
			}
		} else {
			snap.Accounts[p.who].Completed = p.offset + completed
		}
		return nil, nil
	}
	var next []mandatePageRequest
	var width, total uint64
	if p.method == "mandateRewardsPage" {
		r := &snap.Rewards
		r.Period, r.Deadline = v[0].(*big.Int).Uint64(), v[1].(*big.Int).Uint64()
		count := v[2].(*big.Int)
		ids := v[3].([]*big.Int)
		total, width = count.Uint64(), 32
		if !count.IsUint64() || p.offset > total || uint64(len(ids)) != min(width, total-p.offset) {
			return nil, fmt.Errorf("主号持仓分页不完整")
		}
		for _, id := range ids {
			if id.Sign() > 0 {
				r.ids = append(r.ids, id)
			}
		}
		amount.Add(amount, v[4].(*big.Int))
	} else {
		mask, count, left, awaiting, held := v[0].(*big.Int), v[1].(*big.Int), v[2].(*big.Int), v[3].(*big.Int), v[4].(*big.Int)
		total, width = count.Uint64(), 64
		if !count.IsUint64() || p.offset > total || (p.offset > 0 && total != totals[p.who]) || uint64(mask.BitLen()) > min(width, total-p.offset) {
			return nil, fmt.Errorf("盟主令资格分页不完整")
		}
		totals[p.who] = total
		if !held.IsUint64() || !left.IsUint64() || left.Uint64() > 61 || !awaiting.IsUint64() {
			return nil, fmt.Errorf("盟主令记录无效")
		}
		a := snap.Accounts[p.who]
		if a == nil {
			a = &MandateAccount{Remaining: 61}
			snap.Accounts[p.who] = a
		}
		a.Held = held.Uint64()
		a.Remaining = min(a.Remaining, left.Uint64())
		a.AwaitingClaim += awaiting.Uint64()
		known := awaiting.Uint64()
		for bit := 0; bit < mask.BitLen(); bit++ {
			if mask.Bit(bit) != 0 {
				a.indices = append(a.indices, new(big.Int).SetUint64(p.offset+uint64(bit)))
				known++
			}
		}
		a.Available = uint64(len(a.indices))
		if display {
			entries := min(width, total-p.offset)
			lower := max(known, uint64(mask.BitLen()))
			if left.Uint64() == 61 {
				a.Completed = max(a.Completed, p.offset+entries)
				if p.offset+entries == total {
					a.probeCompleted = false
				}
			} else {
				if lower >= entries {
					return nil, fmt.Errorf("甲子俸完成数量无效")
				}
				upper := entries - 1
				verified := a.probeCompleted && a.Completed >= p.offset+lower && a.Completed <= p.offset+upper
				a.probeCompleted = lower < upper
				if !verified {
					a.Completed = p.offset + lower
					if lower < upper {
						next = append(next, mandatePageRequest{method: "jiaziPage", who: p.who, offset: a.Completed, limit: upper - lower})
					}
				}
				// 有效天数按序不增，后续页均未满 60 天。
				return next, nil
			}
		}
	}
	if total-p.offset > width {
		p.offset += width
		next = append(next, p)
	}
	return next, nil
}
