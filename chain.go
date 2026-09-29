package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type Chain struct {
	mu          sync.Mutex
	dialMu      sync.Mutex
	sendMu      sync.Mutex
	journalPath string
	rpcs        []string
	cur         int
	c           *ethclient.Client
	chainID     int64
	gasPrice    *big.Int
}

const (
	defaultGasPrice = 12_000_000_000
	txWait          = 3 * time.Minute
)

func NewChain(rpcs []string, chainID int64) *Chain {
	return &Chain{rpcs: rpcs, chainID: chainID, gasPrice: big.NewInt(defaultGasPrice)}
}

func (c *Chain) SetGasPrice(wei *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gasPrice = new(big.Int).Set(wei)
}

func (c *Chain) GasPrice() *big.Int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return new(big.Int).Set(c.gasPrice)
}

func (c *Chain) Dial(ctx context.Context) error {
	c.dialMu.Lock()
	defer c.dialMu.Unlock()
	if c.client() != nil {
		return nil
	}
	c.mu.Lock()
	start := c.cur
	c.mu.Unlock()
	var last error
	for offset := range c.rpcs {
		i := (start + offset) % len(c.rpcs)
		attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		cl, err := ethclient.DialContext(attemptCtx, c.rpcs[i])
		if err != nil {
			cancel()
			last = err
			continue
		}
		id, err := cl.ChainID(attemptCtx)
		cancel()
		if err != nil {
			cl.Close()
			last = err
			continue
		}
		if id.Cmp(big.NewInt(c.chainID)) != 0 {
			cl.Close()
			last = fmt.Errorf("%s 的 chainId 是 %s，应该是 %d", c.rpcs[i], id, c.chainID)
			continue
		}
		c.mu.Lock()
		c.c, c.cur = cl, i
		c.mu.Unlock()
		return nil
	}
	return fmt.Errorf("三个节点都连不上或链不对：%w", last)
}

func (c *Chain) client() *ethclient.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.c
}

func (c *Chain) retry(ctx context.Context, fn func(context.Context, *ethclient.Client) error) error {
	var last error
	for attempt := 0; attempt < len(c.rpcs)*2; attempt++ {
		cl := c.client()
		if cl == nil {
			if err := c.Dial(ctx); err != nil {
				return err
			}
			cl = c.client()
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := fn(attemptCtx, cl)
		cancel()
		if err == nil {
			return nil
		} else {
			last = err

			var unavailable *playUnavailable
			if errors.As(err, &unavailable) || isRevert(err) {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		if c.c == cl {
			c.c.Close()
			c.c = nil
			c.cur = (c.cur + 1) % len(c.rpcs)
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return last
}

func (c *Chain) BlockNumber(ctx context.Context) (uint64, error) {
	var n uint64
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		v, err := cl.BlockNumber(ctx)
		n = v
		return err
	})
	return n, err
}

func (c *Chain) CodeAt(ctx context.Context, a common.Address) ([]byte, error) {
	var out []byte
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		v, err := cl.CodeAt(ctx, a, nil)
		out = v
		return err
	})
	return out, err
}

func (c *Chain) PendingNonce(ctx context.Context, a common.Address) (uint64, error) {
	var n uint64
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		v, err := cl.PendingNonceAt(ctx, a)
		n = v
		return err
	})
	return n, err
}

func (c *Chain) CallBatch(ctx context.Context, to common.Address, datas [][]byte) ([][]byte, []error) {
	return c.CallBatchAt(ctx, to, datas, "latest")
}

func (c *Chain) CallBatchAt(ctx context.Context, to common.Address, datas [][]byte, block string) ([][]byte, []error) {
	out := make([][]byte, len(datas))
	errs := make([]error, len(datas))
	if len(datas) > 80 {
		for start := 0; start < len(datas); start += 80 {
			end := min(start+80, len(datas))
			values, failures := c.CallBatchAt(ctx, to, datas[start:end], block)
			copy(out[start:end], values)
			copy(errs[start:end], failures)
		}
		return out, errs
	}
	if len(datas) == 0 {
		return out, errs
	}
	reqs := make([]rpc.BatchElem, len(datas))
	results := make([]hexutil.Bytes, len(datas))
	for i, d := range datas {
		reqs[i] = rpc.BatchElem{
			Method: "eth_call",
			Args: []any{map[string]any{
				"to":   to,
				"data": hexutil.Bytes(d),
			}, block},
			Result: &results[i],
		}
	}
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		return cl.Client().BatchCallContext(ctx, reqs)
	})
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return out, errs
	}
	for i := range reqs {
		if reqs[i].Error != nil {
			errs[i] = reqs[i].Error
			continue
		}
		out[i] = results[i]
	}
	return out, errs
}

func (c *Chain) Call(ctx context.Context, to common.Address, data []byte) ([]byte, error) {
	var out []byte
	err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		v, err := cl.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
		out = v
		return err
	})
	return out, err
}

func (c *Chain) Send(ctx context.Context, tx *types.Transaction) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	release, err := c.recordPending(ctx, tx)
	if err != nil {
		return err
	}
	defer release()
	return c.broadcast(ctx, tx)
}

func (c *Chain) broadcast(ctx context.Context, tx *types.Transaction) error {
	return c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		err := cl.SendTransaction(ctx, tx)
		if err != nil && alreadyInFlight(err) {

			return nil
		}
		return err
	})
}

func (c *Chain) SendCall(ctx context.Context, from *Seat, to common.Address, data []byte, gas uint64, floors ...uint64) (*types.Receipt, error) {
	if gas == 0 {

		var estimated uint64
		var head *types.Header
		err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
			var err error
			head, err = cl.HeaderByNumber(ctx, nil)
			if err != nil {
				return err
			}
			estimated, err = cl.EstimateGas(ctx, ethereum.CallMsg{From: from.Address, To: &to, Data: data, GasPrice: c.GasPrice()})
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("整批交易预估失败，未广播：%w", explainCallError(err))
		}
		gas = estimated + estimated/5

		if len(floors) > 0 && gas < floors[0] {
			gas = floors[0]
		}
		if gas > head.GasLimit {
			return nil, fmt.Errorf("整批所需 gas 超过区块上限，请减少选择的账号")
		}
	}
	nonce, err := c.PendingNonce(ctx, from.Address)
	if err != nil {
		return nil, err
	}
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &to,
		Value:    big.NewInt(0),
		Gas:      gas,
		GasPrice: c.GasPrice(),
		Data:     data,
	})
	signed, err := from.signTx(tx, types.NewEIP155Signer(big.NewInt(c.chainID)))
	if err != nil {
		return nil, err
	}
	if err := c.Send(ctx, signed); err != nil {
		return nil, err
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), txWait)
	defer cancel()
	return c.WaitReceipt(wctx, signed.Hash())
}

func (c *Chain) WaitReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	deadline := time.Now().Add(txWait)
	for {
		var rec *types.Receipt
		err := c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
			r, err := cl.TransactionReceipt(ctx, h)
			if err == ethereum.NotFound {
				return nil
			}
			if err != nil {
				return err
			}
			rec = r
			return nil
		})
		if err == nil && rec != nil {
			outcome := "confirmed"
			if rec.Status == 0 {
				outcome = "reverted"
			}
			if err := c.resolvePending(h, outcome); err != nil {
				return rec, fmt.Errorf("交易已上链，但保存回执状态失败：%w", err)
			}
			return rec, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等回执超时（%s），交易可能还在队列里：%s", txWait, h.Hex())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func isRevert(err error) bool {
	m := err.Error()
	for _, k := range []string{
		"execution reverted", "revert", "gas required exceeds",
		"insufficient funds", "nonce too low", "already known",
		"invalid opcode", "out of gas",
	} {
		if containsFold(m, k) {
			return true
		}
	}
	return false
}

func alreadyInFlight(err error) bool {
	m := err.Error()
	for _, k := range []string{"already known", "already imported", "known transaction"} {
		if containsFold(m, k) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
