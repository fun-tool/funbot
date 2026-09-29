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
	"github.com/holiman/uint256"
)

type DelegatePlan struct {
	Revoke   bool            `json:"revoke,omitempty"`
	Master   common.Address  `json:"master"`
	Target   common.Address  `json:"target"`
	Accounts []DelegateEntry `json:"accounts"`
	GasLimit uint64          `json:"gasLimit"`
	GasPrice string          `json:"gasPrice"`
}

type DelegateEntry struct {
	Address common.Address `json:"address"`
	Label   string         `json:"label"`
	Nonce   uint64         `json:"nonce"`
	Already bool           `json:"already"`
}

func (c *Chain) PlanDelegation(ctx context.Context, master *Seat, targets []*Seat, impl common.Address, progress func(int, int)) (*DelegatePlan, error) {
	if master == nil {
		return nil, fmt.Errorf("没有主号")
	}
	want := delegationCode(impl)
	plan := &DelegatePlan{Master: master.Address, Target: impl}

	codes, nonces, err := c.delegationState(ctx, targets, true, progress)
	if err != nil {
		return nil, err
	}
	for i, s := range targets {
		plan.Accounts = append(plan.Accounts, DelegateEntry{
			Address: s.Address, Label: s.Label, Nonce: uint64(*nonces[i]), Already: equalHex(*codes[i], want),
		})
	}

	n := 0
	for _, a := range plan.Accounts {
		if !a.Already {
			n++
		}
	}

	plan.GasLimit = 60_000 + uint64(n)*40_000
	plan.GasPrice = c.GasPrice().String()
	return plan, nil
}

func (c *Chain) Delegate(ctx context.Context, master *Seat, seats []*Seat, plan *DelegatePlan) (common.Hash, error) {
	if plan == nil || master == nil || plan.Master != master.Address || plan.Revoke != (plan.Target == (common.Address{})) {
		return common.Hash{}, fmt.Errorf("委托计划无效")
	}

	byAddr := map[common.Address]*Seat{}
	for _, s := range seats {
		byAddr[s.Address] = s
	}

	txNonce, err := c.PendingNonce(ctx, master.Address)
	if err != nil {
		return common.Hash{}, err
	}

	var list []types.SetCodeAuthorization
	for _, a := range plan.Accounts {
		if a.Already {
			continue
		}
		s := byAddr[a.Address]
		if s == nil {
			return common.Hash{}, fmt.Errorf("%s 没有解锁的私钥", short(a.Address))
		}

		authNonce := a.Nonce
		if a.Address == master.Address {
			authNonce = txNonce + 1
		}

		auth, err := s.signAuthorization(types.SetCodeAuthorization{
			ChainID: *uint256.NewInt(uint64(c.chainID)),
			Address: plan.Target,
			Nonce:   authNonce,
		})
		if err != nil {
			return common.Hash{}, fmt.Errorf("给 %s 签授权失败: %w", short(a.Address), err)
		}
		list = append(list, auth)
	}
	if len(list) == 0 {
		return common.Hash{}, fmt.Errorf("没有需要变更委托的账号")
	}

	fee, _ := uint256.FromBig(c.GasPrice())
	tx := types.NewTx(&types.SetCodeTx{
		ChainID:   uint256.NewInt(uint64(c.chainID)),
		Nonce:     txNonce,
		GasTipCap: fee,
		GasFeeCap: fee,
		Gas:       plan.GasLimit,

		To:       common.Address{},
		AuthList: list,
	})
	signed, err := master.signTx(tx, types.NewPragueSigner(big.NewInt(c.chainID)))
	if err != nil {
		return common.Hash{}, err
	}
	if err := c.Send(ctx, signed); err != nil {
		return common.Hash{}, err
	}
	return signed.Hash(), nil
}

func (c *Chain) VerifyDelegation(ctx context.Context, seats []*Seat, impl common.Address, progress func(int, int)) (map[common.Address]bool, error) {
	codes, _, err := c.delegationState(ctx, seats, false, progress)
	if err != nil {
		return nil, err
	}
	want := delegationCode(impl)
	out := make(map[common.Address]bool, len(seats))
	for i, s := range seats {
		out[s.Address] = equalHex(*codes[i], want)
	}
	return out, nil
}

func (c *Chain) delegationState(ctx context.Context, seats []*Seat, withNonces bool, progress func(int, int)) ([]*hexutil.Bytes, []*hexutil.Uint64, error) {
	codes := make([]*hexutil.Bytes, len(seats))
	nonces := make([]*hexutil.Uint64, len(seats))
	for start := 0; start < len(seats); start += 40 {
		end := min(start+40, len(seats))
		err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
			reqs := make([]rpc.BatchElem, 0, (end-start)*2)
			for i := start; i < end; i++ {
				codes[i], nonces[i] = nil, nil
				reqs = append(reqs, rpc.BatchElem{Method: "eth_getCode", Args: []any{seats[i].Address, "latest"}, Result: &codes[i]})
				if withNonces {
					reqs = append(reqs, rpc.BatchElem{Method: "eth_getTransactionCount", Args: []any{seats[i].Address, "pending"}, Result: &nonces[i]})
				}
			}
			if err := cl.Client().BatchCallContext(ctx, reqs); err != nil {
				return err
			}
			for _, req := range reqs {
				if req.Error != nil {
					return fmt.Errorf("读取 %s 的 %s 失败: %w", req.Args[0], req.Method, req.Error)
				}
			}
			for i := start; i < end; i++ {
				if codes[i] == nil || (withNonces && nonces[i] == nil) {
					return fmt.Errorf("节点未返回 %s 的完整委托状态", short(seats[i].Address))
				}
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		if progress != nil {
			progress(end, len(seats))
		}
	}
	return codes, nonces, nil
}

func delegationCode(impl common.Address) []byte {
	if impl == (common.Address{}) {
		return nil
	}
	out := make([]byte, 0, 23)
	out = append(out, 0xef, 0x01, 0x00)
	return append(out, impl.Bytes()...)
}

func equalHex(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func short(a common.Address) string {
	h := a.Hex()
	return h[:8] + "…" + h[len(h)-4:]
}
