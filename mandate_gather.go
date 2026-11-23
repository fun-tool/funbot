package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type mandateStep struct {
	address common.Address
	indices []*big.Int
	held    uint64
	sweep   uint64
}

func nextMandateRound(snap *MandateSnapshot, who []common.Address, master common.Address) []mandateStep {
	var steps []mandateStep
	for _, addr := range who {
		a := snap.Accounts[addr]
		indices := a.indices[:min(24, len(a.indices))]
		var sweep uint64
		if addr != master {
			sweep = min(24, min(a.Held, 24)+uint64(len(indices)))
		}
		if len(indices) > 0 || sweep > 0 {
			steps = append(steps, mandateStep{addr, indices, a.Held, sweep})
		}
	}
	return steps
}

func (r *Runner) gatherMandateGroup(ctx context.Context, sq *Squad, master *Seat, snap *MandateSnapshot, who []common.Address) error {
	for {
		steps := nextMandateRound(snap, who, master.Address)
		if len(steps) == 0 {
			return nil
		}
		if err := r.sendMandateSteps(ctx, sq, master, steps); err != nil {
			return err
		}
		for _, step := range steps {
			a := snap.Accounts[step.address]
			a.indices = a.indices[len(step.indices):]
			if step.address != master.Address {
				a.Held -= step.sweep - uint64(len(step.indices))
			}
		}
	}
}

func (r *Runner) sendMandateSteps(ctx context.Context, sq *Squad, master *Seat, steps []mandateStep) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.ks.Touch()
	wallets := make([]common.Address, len(steps))
	indices := make([][]*big.Int, len(steps))
	held := make([]*big.Int, len(steps))
	for i, step := range steps {
		wallets[i], indices[i], held[i] = step.address, step.indices, new(big.Int).SetUint64(step.held)
	}
	data, err := sq.abi.Pack("gatherMandatesBatch", wallets, indices, held)
	if err != nil {
		return err
	}
	gas, err := r.chain.estimateCallGas(ctx, master.Address, master.Address, data)
	if err != nil {
		// Splitting is only allowed here, before any transaction was signed or broadcast.
		reason := strings.ToLower(err.Error())
		capacity := errors.Is(err, errGasCapacity) || strings.Contains(reason, "gas required exceeds allowance") || strings.Contains(reason, "exceeds block gas limit") || strings.Contains(reason, "out of gas")
		if len(steps) == 1 || !capacity {
			return err
		}
		mid := len(steps) / 2
		r.st.Logf("", "%d 个地址的 Gas 预估超限，拆为 %d 和 %d 个地址", len(steps), mid, len(steps)-mid)
		if err := r.sendMandateSteps(ctx, sq, master, steps[:mid]); err != nil {
			return err
		}
		return r.sendMandateSteps(ctx, sq, master, steps[mid:])
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rec, err := r.chain.SendCall(ctx, master, master.Address, data, gas)
	if err != nil {
		return err
	}
	if err := verifyMandateTransfers(rec, steps, master.Address); err != nil {
		return err
	}
	var minted, swept uint64
	for _, step := range steps {
		minted += uint64(len(step.indices))
		swept += step.sweep
	}
	r.st.Logf("ok", "%d 个地址铸造 %d 枚，归集 %d 枚 · %s", len(steps), minted, swept, rec.TxHash)
	return nil
}

func verifyMandateTransfers(rec *types.Receipt, steps []mandateStep, master common.Address) error {
	if rec.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("归集交易已回滚：%s", rec.TxHash)
	}
	minted, swept := make(map[common.Address]int), make(map[common.Address]uint64)
	for _, event := range rec.Logs {
		if event == nil || event.Removed || event.Address != mandateAddress || len(event.Topics) != 4 || event.Topics[0] != nftTransfer {
			continue
		}
		from := common.BytesToAddress(event.Topics[1].Bytes())
		to := common.BytesToAddress(event.Topics[2].Bytes())
		if from == (common.Address{}) {
			minted[to]++
		} else if to == master && from != master {
			swept[from]++
		}
	}
	for _, step := range steps {
		if minted[step.address] != len(step.indices) || swept[step.address] != step.sweep {
			return fmt.Errorf("交易 %s 中 %s 的铸造或归集数量与确认计划不符，请核对", rec.TxHash, short(step.address))
		}
	}
	return nil
}
