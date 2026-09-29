package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

func trustedRuntime(cfg *Config) []byte {
	code := hexutil.MustDecode(squadRuntime)
	values := map[string][]byte{
		"ARENA": cfg.Arena.Bytes(), "TOKEN": cfg.Token.Bytes(), "LINEAGE": cfg.Lineage.Bytes(), "GAS_TOKEN": cfg.GasToken.Bytes(),
		"MASTER": cfg.Master.Bytes(), "SHIFU": cfg.Shifu.Bytes(), "IMPL": cfg.Impl.Bytes(),
		"DELEGATION_CODEHASH": crypto.Keccak256(delegationCode(cfg.Impl)),
	}
	for name, offsets := range squadImmutables {
		value, ok := values[name]
		if !ok {
			panic("unknown Squad immutable: " + name)
		}
		for _, offset := range offsets {
			copy(code[offset:offset+32], common.LeftPadBytes(value, 32))
		}
	}
	return code
}
func verifySquadCode(code []byte, cfg *Config, addr, master common.Address) error {
	if cfg.Impl != addr || cfg.Master != master || cfg.Arena != Mainnet.Arena || cfg.Token != Mainnet.Token || cfg.Lineage != Mainnet.Lineage || cfg.GasToken != Mainnet.GasToken || cfg.Shifu == (common.Address{}) {
		return fmt.Errorf("合约配置与当前主号、游戏或委托目标不一致，拒绝使用")
	}
	if !bytes.Equal(code, trustedRuntime(cfg)) {
		return fmt.Errorf("合约代码不属于本版本可信 Squad，禁止委托。旧版请先部署新版，再重新委托；账户和战绩保留")
	}
	return nil
}
func (r *Runner) verifyAttached(ctx context.Context) error {
	cfg := r.Config()
	master := r.ks.Master()
	if cfg == nil || master == nil {
		return fmt.Errorf("请先连接合约并解锁")
	}
	code, err := r.chain.CodeAt(ctx, cfg.Impl)
	if err != nil {
		return err
	}
	return verifySquadCode(code, cfg, cfg.Impl, master.Address)
}
