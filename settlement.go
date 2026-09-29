package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

var settlementABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[
 {"type":"function","name":"playerHistoryLatest","inputs":[{"type":"address"},{"type":"uint256"}],"outputs":[{"name":"rows","type":"tuple[]","components":[{"name":"roundId","type":"uint256"},{"name":"timestamp","type":"uint256"},{"name":"seat","type":"uint256"},{"name":"settled","type":"bool"},{"name":"won","type":"bool"},{"name":"jiaziAdvanced","type":"bool"},{"name":"champion","type":"address"},{"name":"payout","type":"uint256"}]},{"type":"uint256"}]},
 {"type":"function","name":"roundInfo","inputs":[{"type":"uint256"}],"outputs":[{"type":"tuple","components":[{"name":"state","type":"uint8"},{"name":"seatCount","type":"uint256"},{"name":"sealedBlock","type":"uint256"},{"name":"champion","type":"address"},{"name":"championSeat","type":"uint256"},{"name":"rosterHash","type":"bytes32"},{"name":"lockedEntropy","type":"bytes32"}]}]}
 ]`))
	if err != nil {
		panic(err)
	}
	return a
}()

type battleStatus struct {
	RoundId, Timestamp, Seat    *big.Int
	Settled, Won, JiaziAdvanced bool
	Champion                    common.Address
	Payout                      *big.Int
}

type roundStatus struct {
	State                     uint8
	SeatCount, SealedBlock    *big.Int
	Champion                  common.Address
	ChampionSeat              *big.Int
	RosterHash, LockedEntropy [32]byte
}

func (s *Squad) readSettlement(ctx context.Context, arena common.Address, who []common.Address, tag string, p *ArenaProgress) error {
	data := make([][]byte, len(who))
	for i, a := range who {
		data[i], _ = settlementABI.Pack("playerHistoryLatest", a, big.NewInt(1))
	}
	out, errs := s.chain.CallBatchAt(ctx, arena, data, tag)
	pending := map[uint64]bool{}
	for i, a := range who {
		if errs[i] != nil {
			return fmt.Errorf("读取 %s 裁决记录失败：%w", short(a), errs[i])
		}
		values, err := settlementABI.Unpack("playerHistoryLatest", out[i])
		if err != nil {
			return err
		}
		rows := *abi.ConvertType(values[0], new([]battleStatus)).(*[]battleStatus)
		total := values[1].(*big.Int)
		if len(rows) > 1 || (len(rows) == 0) != (total.Sign() == 0) {
			return fmt.Errorf("%s 的最近场次记录不完整", short(a))
		}
		player := p.Players[a]
		if len(rows) == 1 {
			row := rows[0]
			if !row.RoundId.IsUint64() || row.RoundId.Sign() == 0 || row.RoundId.Uint64() > p.Round {
				return fmt.Errorf("%s 的局号无效", short(a))
			}
			player.LastRound = row.RoundId.Uint64()
			player.Pending = !row.Settled
			if player.Pending {
				pending[player.LastRound] = true
			}
		}
		if player.Seated && (!player.Pending || player.LastRound != p.Round) {
			return fmt.Errorf("%s 的入座与裁决状态不一致", short(a))
		}
		p.Players[a] = player
	}
	rounds := make([]uint64, 0, len(pending))
	for rid := range pending {
		rounds = append(rounds, rid)
	}

	p.PendingRounds = map[uint64]roundStatus{}
	if len(rounds) == 0 {
		return nil
	}
	data = make([][]byte, len(rounds))
	for i, rid := range rounds {
		data[i], _ = settlementABI.Pack("roundInfo", new(big.Int).SetUint64(rid))
	}
	out, errs = s.chain.CallBatchAt(ctx, arena, data, tag)
	for i, rid := range rounds {
		if errs[i] != nil {
			return errs[i]
		}
		values, err := settlementABI.Unpack("roundInfo", out[i])
		if err != nil {
			return err
		}
		state := *abi.ConvertType(values[0], new(roundStatus)).(*roundStatus)
		if (state.State != 1 && state.State != 2) || !state.SeatCount.IsUint64() || state.SeatCount.Sign() == 0 || state.SeatCount.Uint64() > 24 {
			return fmt.Errorf("第 %d 局裁决状态不一致", rid)
		}
		p.PendingRounds[rid] = state
	}
	return nil
}

func readyPlayers(p *ArenaProgress, who []common.Address, goals map[common.Address]uint64, single bool) (eligible []common.Address, remaining int, reason string) {
	var seated []common.Address
	var reasons []string
	for _, a := range who {
		v := p.Players[a]
		if v.Matches >= goals[a] {
			continue
		}
		remaining++
		if v.Seated {
			seated = append(seated, a)
			r := p.PendingRounds[v.LastRound]
			reasons = append(reasons, fmt.Sprintf("%s 第 %d 局等待凑满 24 席（%s/24）", short(a), v.LastRound, r.SeatCount))
			continue
		}
		if v.Pending && !single {
			r := p.PendingRounds[v.LastRound]
			mature := r.State == 2 && (r.LockedEntropy != ([32]byte{}) || (r.SealedBlock.IsUint64() && p.Block >= r.SealedBlock.Uint64() && p.Block-r.SealedBlock.Uint64() >= 2))
			if !mature {
				reasons = append(reasons, fmt.Sprintf("%s 第 %d 局等待封盘区块 %s + 2", short(a), v.LastRound, r.SealedBlock))
				continue
			}
		}
		eligible = append(eligible, a)
	}
	canAdvance := false
	if r, ok := p.PendingRounds[p.Round]; ok && r.State == 1 && r.SeatCount != nil && r.SeatCount.IsUint64() {
		seats := r.SeatCount.Uint64()
		canAdvance = seats > 0 && seats < 24 && uint64(len(eligible)) >= 24-seats
	}
	if len(eligible) > 0 && (single || canAdvance) {
		eligible = append(eligible, seated...)
	}
	if len(reasons) > 0 {
		reason = reasons[0]
		if len(reasons) > 1 {
			reason += fmt.Sprintf("；另 %d 个账号等待", len(reasons)-1)
		}
	}
	return
}

func (b *Batch) awaitArenaChange(ctx context.Context, sq *Squad, arena common.Address, who []common.Address, current *ArenaProgress, timeout time.Duration) (*ArenaProgress, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("等待入场条件结束：%w", waitCtx.Err())
		case <-ticker.C:
		}
		if b.run.ks != nil {
			b.run.ks.Touch()
		}
		block, err := sq.chain.BlockNumber(waitCtx)
		if err != nil {
			return nil, err
		}
		if block < current.Block {
			return nil, fmt.Errorf("节点区块落后，停止等待")
		}
		if block == current.Block {
			continue
		}
		return sq.arenaProgress(waitCtx, arena, who, true, block)
	}
}
