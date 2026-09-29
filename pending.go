package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type PendingTransaction struct {
	Raw      hexutil.Bytes  `json:"raw,omitempty"`
	Resolved bool           `json:"resolved"`
	Outcome  string         `json:"outcome,omitempty"`
	ChainID  int64          `json:"chainId"`
	Hash     common.Hash    `json:"hash"`
	From     common.Address `json:"from"`
	Nonce    uint64         `json:"nonce"`
	Created  time.Time      `json:"created"`
}

func (c *Chain) recordPending(ctx context.Context, tx *types.Transaction) (func(), error) {
	if c.journalPath == "" {
		return func() {}, nil
	}
	release, err := lockVault(c.journalPath)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	if err := c.checkPending(ctx); err != nil {
		return fail(err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return fail(err)
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		return fail(err)
	}
	p := PendingTransaction{Raw: raw, ChainID: c.chainID, Hash: tx.Hash(), From: from, Nonce: tx.Nonce(), Created: time.Now()}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := atomicPrivateWrite(c.journalPath, data); err != nil {
		return fail(err)
	}
	return release, nil
}
func (c *Chain) pending() (*PendingTransaction, error) {
	if c.journalPath == "" {
		return nil, nil
	}
	b, err := readPrivateFile(c.journalPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PendingTransaction
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("待确认交易记录损坏，禁止发送新交易：%w", err)
	}
	if p.ChainID != c.chainID || p.Hash == (common.Hash{}) {
		return nil, fmt.Errorf("待确认交易记录链或哈希无效")
	}
	return &p, nil
}
func (c *Chain) checkPending(ctx context.Context) error {
	p, err := c.pending()
	if err != nil {
		return err
	}
	if p == nil || p.Resolved {
		return nil
	}
	var rec *types.Receipt
	err = c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		var err error
		rec, err = cl.TransactionReceipt(ctx, p.Hash)
		if err == ethereum.NotFound {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("无法核对上一笔交易 %s：%w", p.Hash, err)
	}
	if rec == nil {
		return fmt.Errorf("上一笔交易尚未确认：%s（nonce %d）。已阻止重复发送；请先在浏览器核查，确认上链后重试", p.Hash, p.Nonce)
	}
	return nil
}

func (c *Chain) resolvePending(h common.Hash, outcome string) error {
	if c.journalPath == "" {
		return nil
	}
	release, err := lockVault(c.journalPath)
	if err != nil {
		return err
	}
	defer release()
	p, err := c.pending()
	if err != nil {
		return err
	}
	if p == nil || p.Hash != h {
		return nil
	}
	p.Resolved = true
	p.Outcome = outcome
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicPrivateWrite(c.journalPath, data)
}

func (c *Chain) RecoverPending(ctx context.Context) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	release, err := lockVault(c.journalPath)
	if err != nil {
		return err
	}
	defer release()
	p, err := c.pending()
	if err != nil {
		return err
	}
	if p == nil || p.Resolved {
		return nil
	}
	var receipt *types.Receipt
	var nonce uint64
	err = c.retry(ctx, func(ctx context.Context, cl *ethclient.Client) error {
		var err error
		receipt, err = cl.TransactionReceipt(ctx, p.Hash)
		if err != nil && err != ethereum.NotFound {
			return err
		}
		nonce, err = cl.NonceAt(ctx, p.From, nil)
		return err
	})
	if err != nil {
		return err
	}
	if receipt != nil || nonce > p.Nonce {
		p.Resolved = true
		p.Outcome = "confirmed"
		if receipt == nil {
			p.Outcome = "nonce-consumed"
		} else if receipt.Status == 0 {
			p.Outcome = "reverted"
		}
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicPrivateWrite(c.journalPath, data); err != nil {
			return err
		}
		if receipt == nil {
			return fmt.Errorf("原交易 nonce 已被其他交易消费。已解除等待，请核对链上状态后重新操作")
		}
		if receipt.Status == 0 {
			return fmt.Errorf("原交易已回滚，等待已解除；请检查原因后重试")
		}
		return nil
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(p.Raw); err != nil {
		return fmt.Errorf("原交易数据损坏：%w", err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), &tx)
	if err != nil || tx.Hash() != p.Hash || from != p.From || tx.Nonce() != p.Nonce || tx.ChainId().Int64() != c.chainID {
		return fmt.Errorf("原交易数据校验失败")
	}
	if err := c.broadcast(ctx, &tx); err != nil {
		return err
	}
	return fmt.Errorf("已重发同一笔交易 %s；等待上链后再次检查", p.Hash)
}
