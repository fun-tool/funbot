package main

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

var mandateAddress = common.HexToAddress("0xfffe850fd170625a8f816a731b9b278cc161aaaa")
var nftTransfer = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
var rewardsAddress = common.HexToAddress("0xff1e7170c041c0e37bf9b9ce0feed600f3deaaaa")
var claimABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"claim","inputs":[{"type":"uint256"},{"type":"uint256[]"}],"outputs":[{"type":"uint256"}]}]`))
	if err != nil {
		panic(err)
	}
	return a
}()

type MandateAccount struct {
	Completed      uint64 `json:"completed"` // Stipends with all 60 valid days, including previously minted ones.
	Available      uint64 `json:"available"`
	Held           uint64 `json:"held"`
	AwaitingClaim  uint64 `json:"awaitingClaim"`
	Remaining      uint64 `json:"remaining"` // 61 = no unfinished stipend
	probeCompleted bool
	indices        []*big.Int
}
type MandateRewards struct {
	Period   uint64 `json:"period"`
	Deadline uint64 `json:"deadline"`
	Count    int    `json:"count"`
	Amount   string `json:"amount"`
	ids      []*big.Int
}

type MandateSnapshot struct {
	RewardError string                             `json:"rewardError,omitempty"`
	Revision    common.Hash                        `json:"revision"`
	Accounts    map[common.Address]*MandateAccount `json:"accounts"`
	Rewards     MandateRewards                     `json:"rewards"`
}

func (preview *MandateSnapshot) checkGroup(current *MandateSnapshot, who []common.Address, master common.Address, claim bool) error {
	equalID := func(a, b *big.Int) bool { return a.Cmp(b) == 0 }
	if claim {
		old, next := preview.Rewards, current.Rewards
		if old.Period != next.Period || old.Amount != next.Amount || old.Count != next.Count ||
			!slices.EqualFunc(old.ids, next.ids, equalID) {
			return fmt.Errorf("福泽期次、金额或可领盟主令已变化，请刷新后重新确认")
		}
		return nil
	}
	if len(who) == 0 || len(who) > gatherGroupSize {
		return fmt.Errorf("每组须包含 1 至 %d 个地址", gatherGroupSize)
	}
	for _, addr := range who {
		old, next := preview.Accounts[addr], current.Accounts[addr]
		if old == nil || next == nil || (addr != master && old.Held != next.Held) || !slices.EqualFunc(old.indices, next.indices, equalID) {
			return fmt.Errorf("%s 的持仓或铸造资格已变化，请刷新后重新确认", short(addr))
		}
	}
	return nil
}

func (r *Runner) mandateAction(ctx context.Context, who []common.Address, claim bool, preview *MandateSnapshot) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	if err = r.verifyAttached(ctx); err != nil {
		return err
	}
	if !claim {
		if err = r.ks.requireLocalAccounts(who); err != nil {
			return err
		}
	}
	head, err := r.chain.BlockNumber(ctx)
	if err != nil {
		return err
	}
	snap, err := sq.readMandates(ctx, hexutil.EncodeUint64(head), who, claim)
	if err != nil {
		return err
	}
	if err := preview.checkGroup(snap, who, master.Address, claim); err != nil {
		return err
	}
	if claim {
		rewards := snap.Rewards
		if rewards.Count == 0 {
			return fmt.Errorf("主号本期暂无可领福泽")
		}
		for ids := range slices.Chunk(rewards.ids, 32) {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.ks.Touch()
			data, err := claimABI.Pack("claim", new(big.Int).SetUint64(rewards.Period), ids)
			if err != nil {
				return err
			}
			receipt, err := r.chain.SendCall(ctx, master, rewardsAddress, data, 0)
			if err != nil {
				return err
			}
			if receipt.Status != 1 {
				return fmt.Errorf("领取福泽已回滚：%s", receipt.TxHash)
			}
			r.st.Logf("ok", "主号已领取第 %d 期 %d 枚盟主令的福泽 · %s", rewards.Period, len(ids), receipt.TxHash)
		}
		return nil
	}
	if err = sq.requireDelegated(ctx, master.Address); err != nil {
		return err
	}
	return r.gatherMandateGroup(ctx, sq, master, snap, who)
}

func (b *Batch) startMandates(who []common.Address, claim bool, revision common.Hash) error {
	name := "铸造并归集盟主令"
	if claim {
		name = "主号领取福泽"
	}
	if _, _, err := b.run.ready(); err != nil {
		return err
	}
	if !claim {
		if err := b.run.ks.requireLocalAccounts(who); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prog.Running || !b.st.TryBusy(name) {
		return fmt.Errorf("请等待当前操作结束")
	}
	preview := b.st.Snapshot().Mandates
	if preview == nil || revision == (common.Hash{}) || preview.Revision != revision {
		b.st.Idle()
		return fmt.Errorf("页面上的盟主令数据已更新，请刷新后重新确认")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.prog = Progress{Running: true, Step: name}
	b.st.Set(func(v *View) { v.Batch = b.prog })
	go func() {
		defer cancel()
		defer b.finish()
		var err error
		if claim {
			err = b.run.mandateAction(ctx, nil, true, preview)
		} else {
			for start := 0; start < len(who); start += gatherGroupSize {
				if err = ctx.Err(); err != nil {
					break
				}
				end := min(start+gatherGroupSize, len(who))
				b.st.Logf("", "归集第 %d/%d 批，本批 %d 个地址", start/gatherGroupSize+1, (len(who)+gatherGroupSize-1)/gatherGroupSize, end-start)
				err = b.run.mandateAction(ctx, who[start:end], false, preview)
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			b.st.Logf("bad", "%s未完成：%v。已确认交易可在日志查看。", name, err)
		} else {
			b.st.Logf("ok", "%s完成", name)
		}
		b.mu.Lock()
		b.cancel = nil
		b.mu.Unlock()
		b.set(func(p *Progress) { p.Running = false })
	}()
	return nil
}
