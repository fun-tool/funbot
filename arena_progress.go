package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var arenaProgressABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[
 {"type":"function","name":"currentRoundId","inputs":[],"outputs":[{"type":"uint256"}]},
 {"type":"function","name":"currentDayId","inputs":[],"outputs":[{"type":"uint256"}]},
 {"type":"function","name":"activityOf","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"},{"type":"uint256"},{"type":"uint256"},{"type":"uint256"}]},
 {"type":"function","name":"isSeated","inputs":[{"type":"uint256"},{"type":"address"}],"outputs":[{"type":"bool"}]}
]`))
	if err != nil {
		panic(err)
	}
	return a
}()

type PlayerProgress struct {
	Matches   uint64
	Seated    bool
	LastRound uint64
	Pending   bool
}
type ArenaProgress struct {
	Block, Round, Day uint64
	Players           map[common.Address]PlayerProgress
	PendingRounds     map[uint64]roundStatus
}

func (s *Squad) arenaProgress(ctx context.Context, arena common.Address, who []common.Address, settlement bool, confirmedBlock ...uint64) (*ArenaProgress, error) {
	var block uint64
	if len(confirmedBlock) > 0 {
		block = confirmedBlock[0]
	} else {
		var err error
		block, err = s.chain.BlockNumber(ctx)
		if err != nil {
			return nil, err
		}
	}
	tag := hexutil.EncodeUint64(block)
	round, _ := arenaProgressABI.Pack("currentRoundId")
	day, _ := arenaProgressABI.Pack("currentDayId")
	out, errs := s.chain.CallBatchAt(ctx, arena, [][]byte{round, day}, tag)
	p := &ArenaProgress{Block: block, Players: map[common.Address]PlayerProgress{}}
	for i, method := range []string{"currentRoundId", "currentDayId"} {
		if errs[i] != nil {
			return nil, errs[i]
		}
		v, err := arenaProgressABI.Unpack(method, out[i])
		if err != nil {
			return nil, err
		}
		if i == 0 {
			p.Round = v[0].(*big.Int).Uint64()
		} else {
			p.Day = v[0].(*big.Int).Uint64()
		}
	}
	var data [][]byte
	for _, a := range who {
		x, _ := arenaProgressABI.Pack("activityOf", a)
		y, _ := arenaProgressABI.Pack("isSeated", new(big.Int).SetUint64(p.Round), a)
		data = append(data, x, y)
	}
	out, errs = s.chain.CallBatchAt(ctx, arena, data, tag)
	for i, a := range who {
		if errs[2*i] != nil {
			return nil, errs[2*i]
		}
		if errs[2*i+1] != nil {
			return nil, errs[2*i+1]
		}
		x, err := arenaProgressABI.Unpack("activityOf", out[2*i])
		if err != nil {
			return nil, err
		}
		y, err := arenaProgressABI.Unpack("isSeated", out[2*i+1])
		if err != nil {
			return nil, err
		}
		v := PlayerProgress{Seated: y[0].(bool)}
		if x[1].(*big.Int).Uint64() == p.Day {
			v.Matches = x[2].(*big.Int).Uint64()
		}
		p.Players[a] = v
	}
	if settlement {
		if err := s.readSettlement(ctx, arena, who, tag, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

var arenaEnteredTopic = crypto.Keccak256Hash([]byte("ArenaEntered(uint256,uint256,address)"))

func enteredPlayers(rec *types.Receipt, arena common.Address, who []common.Address) (map[common.Address]uint64, error) {
	if rec == nil || rec.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("入场交易未成功")
	}
	want := map[common.Address]bool{}
	for _, a := range who {
		want[a] = true
	}
	got := map[common.Address]uint64{}
	for _, l := range rec.Logs {
		if l.Removed || l.Address != arena || len(l.Topics) != 4 || l.Topics[0] != arenaEnteredTopic {
			continue
		}
		a := common.BytesToAddress(l.Topics[3].Bytes())
		if want[a] {
			got[a] = l.Topics[1].Big().Uint64()
		}
	}
	return got, nil
}
