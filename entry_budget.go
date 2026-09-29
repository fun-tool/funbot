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

func (b *Batch) refreshBalances() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.run.Refresh(ctx); err != nil {
		b.st.Logf("warn", "余额刷新失败，页面保留上次快照：%v", err)
	}
}

func explainCallError(err error) error {
	var dataError rpc.DataError
	if !errors.As(err, &dataError) {
		return err
	}
	raw, ok := dataError.ErrorData().(string)
	if !ok {
		return err
	}
	data, decodeErr := hexutil.Decode(raw)
	if decodeErr != nil || len(data) != 100 || !bytes.Equal(data[:4], []byte{0xe4, 0x50, 0xd3, 0x8c}) {
		return err
	}
	sender := common.BytesToAddress(data[4:36])
	balance := new(big.Int).SetBytes(data[36:68])
	needed := new(big.Int).SetBytes(data[68:100])
	return fmt.Errorf("代币余额不足：%s 在模拟执行到此处时剩余 %s，需支付 %s（仅预估，尚未转账）", short(sender), fmtUnits(balance), fmtUnits(needed))
}
