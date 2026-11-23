package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

// A custom -rpc changes the normal failover order, never the signing witnesses.
var delegationRPCs = [3]string{
	"https://rpc.anubispace.org",
	"https://rpc1.anubispace.org",
	"https://rpc2.anubispace.org",
}

func verifyDelegationTarget(ctx context.Context, endpoints [3]string, chainID int64, cfg *Config, target, master common.Address) error {
	if err := verifySquadConfig(cfg, target, master); err != nil {
		return err
	}
	expected := trustedRuntime(cfg)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	type witness struct {
		number hexutil.Uint64
		hash   common.Hash
		client *rpc.Client
	}
	type result struct {
		w   *witness
		err error
	}
	readCode := func(cl *rpc.Client, hash common.Hash) error {
		var code hexutil.Bytes
		block := rpc.BlockNumberOrHashWithHash(hash, true)
		if err := cl.CallContext(ctx, &code, "eth_getCode", target, block); err != nil {
			return err
		}
		if !bytes.Equal(code, expected) {
			return fmt.Errorf("区块 %s 的合约代码与本地预期不一致", hash)
		}
		return nil
	}
	results := make(chan result, 6) // three heads and at most three pair checks
	pending := len(endpoints)
	for i, endpoint := range endpoints {
		cl, err := rpc.DialContext(ctx, endpoint)
		if err != nil {
			results <- result{err: fmt.Errorf("RPC %d：%w", i+1, err)}
			continue
		}
		defer cl.Close()
		go func() {
			var id hexutil.Big
			var head struct {
				Number *hexutil.Uint64
				Hash   common.Hash
			}
			batch := []rpc.BatchElem{
				{Method: "eth_chainId", Result: &id},
				{Method: "eth_getBlockByNumber", Args: []any{"latest", false}, Result: &head},
			}
			err := cl.BatchCallContext(ctx, batch)
			if err == nil {
				err = errors.Join(batch[0].Error, batch[1].Error)
			}
			if err == nil && (id.ToInt().Cmp(big.NewInt(chainID)) != 0 || head.Number == nil || head.Hash == (common.Hash{})) {
				err = fmt.Errorf("链 ID 或区块信息无效")
			}
			if err == nil {
				err = readCode(cl, head.Hash)
			}
			if err != nil {
				results <- result{err: fmt.Errorf("RPC %d：%w", i+1, err)}
				return
			}
			results <- result{w: &witness{*head.Number, head.Hash, cl}}
		}()
	}
	var verified []*witness
	var lastErr error
	for pending > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("不足两个 RPC 确认同一区块上的可信合约：%w", ctx.Err())
		case r := <-results:
			pending--
			if r.err != nil {
				lastErr = r.err
				continue
			}
			if r.w == nil {
				return nil
			} // successful cross-check at the other node's block
			for _, other := range verified {
				older, newer := other, r.w
				if older.number > newer.number {
					older, newer = newer, older
				}
				if older.number == newer.number {
					if older.hash == newer.hash {
						return nil
					}
					lastErr = fmt.Errorf("同高度区块哈希不同")
					continue
				}
				pending++
				go func() { results <- result{err: readCode(newer.client, older.hash)} }()
			}
			verified = append(verified, r.w)
		}
	}
	return fmt.Errorf("不足两个 RPC 确认同一区块上的可信合约：%w", lastErr)
}
