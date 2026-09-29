package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

var extraReadABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[
 {"type":"function","name":"jiaziCount","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]},
 {"type":"function","name":"masterOf","inputs":[{"type":"address"}],"outputs":[{"type":"address"}]},
 {"type":"function","name":"allowance","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"uint256"}]},
 {"type":"function","name":"jiaziPage","inputs":[{"type":"address"},{"type":"uint256"},{"type":"uint256"}],"outputs":[{"type":"tuple[]","components":[{"name":"index","type":"uint256"},{"name":"roundId","type":"uint256"},{"name":"validDays","type":"uint256"},{"name":"released","type":"uint256"},{"name":"claimedDays","type":"uint256"},{"name":"claimableAmount","type":"uint256"}]},{"type":"uint256"}]}
 ]`))
	if err != nil {
		panic(err)
	}
	return a
}()

type JiaziRow struct {
	Index           *big.Int
	RoundID         *big.Int `abi:"roundId"`
	ValidDays       *big.Int
	Released        *big.Int
	ClaimedDays     *big.Int
	ClaimableAmount *big.Int
}

func (s *Squad) readExtra(ctx context.Context, to common.Address, method string, args ...any) ([]any, error) {
	data, err := extraReadABI.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	out, err := s.chain.Call(ctx, to, data)
	if err != nil {
		return nil, err
	}
	return extraReadABI.Unpack(method, out)
}
func (s *Squad) HarvestJiaziPages(ctx context.Context, master *Seat, who []common.Address) error {
	if len(who) == 0 {
		var err error
		who, err = s.Members(ctx)
		if err != nil {
			return err
		}
	}
	for from := uint64(0); ; from += 64 {
		if err := ctx.Err(); err != nil {
			return err
		}
		more := false
		var payable []common.Address
		for _, a := range who {
			out, err := s.readExtra(ctx, Mainnet.Arena, "jiaziPage", a, new(big.Int).SetUint64(from), big.NewInt(64))
			if err != nil {
				return fmt.Errorf("读取 %s 甲子俸第 %d 页失败：%w", short(a), from/64+1, err)
			}
			total := out[1].(*big.Int)
			if !total.IsUint64() {
				return fmt.Errorf("甲子俸数量异常")
			}
			if total.Uint64() > from+64 {
				more = true
			}
			rows := *abi.ConvertType(out[0], new([]JiaziRow)).(*[]JiaziRow)
			for _, row := range rows {
				if row.ClaimableAmount.Sign() > 0 {
					payable = append(payable, a)
					break
				}
			}
		}
		if len(payable) > 0 {
			if _, err := s.send(ctx, master, 0, "harvestJiaziPage", payable, new(big.Int).SetUint64(from)); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
	}
}
func (s *Squad) VerifyPrepared(ctx context.Context, who ...common.Address) error {
	for _, a := range who {
		if err := s.requireDelegated(ctx, a); err != nil {
			return err
		}
		bound, err := s.readExtra(ctx, Mainnet.Lineage, "masterOf", a)
		if err != nil {
			return err
		}
		allowance, err := s.readExtra(ctx, Mainnet.Token, "allowance", a, Mainnet.Arena)
		if err != nil {
			return err
		}
		if bound[0].(common.Address) == (common.Address{}) || allowance[0].(*big.Int).Cmp(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))) < 0 {
			return fmt.Errorf("%s 准备未完成，请检查拜师和授权状态后重试", short(a))
		}
	}
	return nil
}

func validateShifu(ctx context.Context, c *Chain, shifu common.Address) error {
	sq := &Squad{chain: c}
	out, err := sq.readExtra(ctx, Mainnet.Lineage, "masterOf", shifu)
	if err != nil {
		return fmt.Errorf("核验师父资格失败：%w", err)
	}
	if out[0].(common.Address) == (common.Address{}) {
		return fmt.Errorf("师父 %s 尚未加入师承，无法接受拜师。请填写已加入师承的师父；主号未拜师时不能留空使用主号", short(shifu))
	}
	return nil
}
