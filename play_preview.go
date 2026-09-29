package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type playPreview struct {
	Who        []common.Address
	Data       []byte
	Gas, Block uint64
	Funded     *big.Int
}

type playUnavailable struct {
	Reason   string
	Capacity bool
}

func (e *playUnavailable) Error() string { return e.Reason }

func (s *Squad) previewPlay(ctx context.Context, who []common.Address, minBlock uint64) (*playPreview, error) {
	if len(who) == 0 {
		return nil, fmt.Errorf("没有可模拟的账号")
	}
	data, err := s.abi.Pack("playSome", who)
	if err != nil {
		return nil, err
	}
	var preview *playPreview
	err = s.chain.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		head, err := cl.HeaderByNumber(ctx, nil)
		if err != nil {
			return err
		}
		if head.Number.Uint64() < minBlock {
			return fmt.Errorf("模拟节点区块落后：%d < %d", head.Number.Uint64(), minBlock)
		}
		call := ethereum.CallMsg{From: s.acct, To: &s.acct, Data: data, GasPrice: s.chain.GasPrice()}
		var output []byte
		var estimate uint64
		var callErr, gasErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			msg := call
			msg.Gas = head.GasLimit
			output, callErr = cl.CallContract(ctx, msg, head.Number)
		}()
		go func() { defer wg.Done(); estimate, gasErr = cl.EstimateGas(ctx, call) }()
		wg.Wait()
		if callErr != nil {
			return callErr
		}
		if gasErr != nil {
			return gasErr
		}
		values, err := s.abi.Unpack("playSome", output)
		if err != nil {
			return fmt.Errorf("无法解码入场模拟结果：%w", err)
		}
		entered := values[2].(*big.Int)
		if entered.Cmp(big.NewInt(int64(len(who)))) != 0 {
			preview = nil
			return &playUnavailable{Reason: fmt.Sprintf("整批模拟仅 %s/%d 个账号能入场（占位、委托、授权、余额或当天上限需核对）", entered, len(who))}
		}
		if estimate == 0 {
			return fmt.Errorf("节点返回无效的 Gas 预估")
		}
		if estimate > head.GasLimit || estimate/5 > head.GasLimit-estimate {
			return &playUnavailable{Reason: "整批 Gas 加安全余量后超过区块上限，请减少选择的账号", Capacity: true}
		}
		gas := estimate + estimate/5
		for {
			msg := call
			msg.Gas = gas
			checked, err := cl.CallContract(ctx, msg, head.Number)
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "out of gas") {
				return err
			}
			if err == nil {
				valuesAtGas, err := s.abi.Unpack("playSome", checked)
				if err != nil {
					return err
				}
				if valuesAtGas[2].(*big.Int).Cmp(big.NewInt(int64(len(who)))) == 0 {
					values = valuesAtGas
					break
				}
			}
			if gas >= head.GasLimit {
				return &playUnavailable{Reason: "区块 Gas 上限内无法让全部账号入场", Capacity: true}
			}
			if gas > head.GasLimit/2 {
				gas = head.GasLimit
			} else {
				gas *= 2
			}
		}
		preview = &playPreview{Who: append([]common.Address(nil), who...), Data: data, Gas: gas, Block: head.Number.Uint64(), Funded: new(big.Int).Set(values[0].(*big.Int))}
		return nil
	})
	if err != nil {
		var unavailable *playUnavailable
		if errors.As(err, &unavailable) {
			return nil, unavailable
		}
		var rpcError rpc.DataError
		if errors.As(err, &rpcError) || strings.Contains(strings.ToLower(err.Error()), "execution reverted") {
			return nil, &playUnavailable{Reason: fmt.Sprintf("入场模拟失败：%v", explainCallError(err))}
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "exceeds block gas limit") || strings.Contains(lower, "gas required exceeds allowance") {
			return nil, &playUnavailable{Reason: fmt.Sprintf("交易 Gas 或节点执行额度不足：%v", err), Capacity: true}
		}
		return nil, err
	}
	return preview, nil
}

func (b *Batch) preparePlay(ctx context.Context, sq *Squad, who []common.Address, block uint64, together bool) (*playPreview, error) {
	preview, err := sq.previewPlay(ctx, who, block)
	if err == nil {
		return preview, nil
	}
	var unavailable *playUnavailable
	if !errors.As(err, &unavailable) || together {
		return nil, err
	}
	b.st.Logf("warn", "%v；检查可执行子集，期间不发送交易", err)
	var accepted []common.Address
	var best *playPreview
	blocked := map[string]string{}
	full := false
	var selectChunk func([]common.Address) error
	selectChunk = func(chunk []common.Address) error {
		if len(chunk) == 0 || full {
			return nil
		}
		candidate := append(append([]common.Address(nil), accepted...), chunk...)
		trial, err := sq.previewPlay(ctx, candidate, block)
		if err == nil {
			accepted = candidate
			best = trial
			return nil
		}
		var failure *playUnavailable
		if !errors.As(err, &failure) {
			return err
		}
		if len(chunk) == 1 {
			if failure.Capacity && len(accepted) > 0 {
				full = true
				return nil
			}
			blocked[strings.ToLower(chunk[0].Hex())] = failure.Reason
			b.st.Logf("warn", "%s 暂缓：%v", short(chunk[0]), err)
			return nil
		}
		mid := len(chunk) / 2
		if err := selectChunk(chunk[:mid]); err != nil {
			return err
		}
		return selectChunk(chunk[mid:])
	}
	mid := len(who) / 2
	if err := selectChunk(who[:mid]); err != nil {
		return nil, err
	}
	if err := selectChunk(who[mid:]); err != nil {
		return nil, err
	}
	b.set(func(p *Progress) { p.Reason = "" })
	if best == nil {
		return nil, &playUnavailable{Reason: fmt.Sprintf("当前没有可执行账号，%d 个账号模拟未通过，请查看逐号原因", len(blocked))}
	}
	return best, nil
}

func (s *Squad) sendPlay(ctx context.Context, master *Seat, p *playPreview) (*types.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.chain.SendCall(ctx, master, s.acct, p.Data, p.Gas)
}
