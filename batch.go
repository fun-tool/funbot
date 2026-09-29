package main

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type Plan struct {
	EntryMode string
	Rounds    int

	Who []common.Address
}

type Progress struct {
	Reason    string `json:"reason,omitempty"`
	Running   bool   `json:"running"`
	Round     int    `json:"round"`
	Rounds    int    `json:"rounds"`
	Step      string `json:"step"`
	Harvested string `json:"harvested"`
	Skipped   int    `json:"skipped"`
	Entered   int    `json:"entered"`
}

type Batch struct {
	mu     sync.Mutex
	run    *Runner
	st     *State
	cancel context.CancelFunc
	prog   Progress
}

func NewBatch(r *Runner, st *State) *Batch {
	return &Batch{run: r, st: st}
}

func (b *Batch) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prog.Running
}

func (b *Batch) set(fn func(p *Progress)) {
	b.mu.Lock()
	fn(&b.prog)
	p := b.prog
	b.mu.Unlock()
	b.st.Set(func(v *View) { v.Batch = p })
}

func (b *Batch) Stop() {
	b.mu.Lock()
	c := b.cancel
	b.mu.Unlock()
	if c != nil {
		b.st.Logf("warn", "收到停止，等当前这一步做完")
		c()
	}
}

func (b *Batch) Start(p Plan) error {
	if p.EntryMode != "" && p.EntryMode != "flexible" && p.EntryMode != "together" {
		return fmt.Errorf("入场方式无效")
	}
	if p.Rounds <= 0 || p.Rounds > MaxMatchesPerDay {
		return fmt.Errorf("局数要在 1 到 %d 之间", MaxMatchesPerDay)
	}
	if _, _, err := b.run.ready(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.prog.Running {
		b.mu.Unlock()
		return fmt.Errorf("已经在跑了")
	}
	if !b.st.TryBusy("批量运行") {
		b.mu.Unlock()
		return fmt.Errorf("有其他操作正在进行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.prog = Progress{Running: true, Rounds: p.Rounds, Step: "准备"}
	b.mu.Unlock()

	go func() {
		defer cancel()
		defer b.finish()
		b.loop(ctx, p)
		b.set(func(pr *Progress) {
			pr.Running = false
			pr.Step = "结束"
		})
		b.mu.Lock()
		b.cancel = nil
		b.mu.Unlock()
	}()
	return nil
}

const MaxMatchesPerDay = 12

func (b *Batch) loop(ctx context.Context, p Plan) {
	sq, master, err := b.run.ready()
	if err != nil {
		b.st.Logf("bad", "%v", err)
		return
	}
	arena := b.run.Config().Arena
	who, err := sq.Members(ctx)
	if err != nil {
		b.st.Logf("bad", "读取名册失败：%v", err)
		return
	}
	if len(p.Who) > 0 {
		roster := map[common.Address]bool{}
		for _, a := range who {
			roster[a] = true
		}
		who = nil
		for _, a := range p.Who {
			if !roster[a] {
				b.st.Logf("bad", "%s 不在名册，已停止", short(a))
				return
			}
			who = append(who, a)
		}
	}
	if b.run.settings != nil && !b.run.settings.Get().MasterPlays {
		filtered := make([]common.Address, 0, len(who))
		for _, a := range who {
			if a != master.Address {
				filtered = append(filtered, a)
			}
		}
		who = filtered
	}
	unique := make([]common.Address, 0, len(who))
	seen := map[common.Address]bool{}
	for _, a := range who {
		if !seen[a] {
			unique = append(unique, a)
			seen[a] = true
		}
	}
	who = unique
	if len(who) == 0 {
		b.st.Logf("bad", "没有参赛账号")
		return
	}

	together := p.EntryMode == "together"
	checks := make([]*Seat, 0, len(who))
	for _, a := range who {
		checks = append(checks, &Seat{Address: a})
	}
	verified, err := b.run.chain.VerifyDelegation(ctx, checks, sq.Impl(), nil)
	if err != nil {
		b.st.Logf("bad", "读取委托状态失败：%v", err)
		return
	}
	usable := make([]common.Address, 0, len(who))
	for _, a := range who {
		if !verified[a] {
			if together {
				b.st.Logf("bad", "%s 委托未确认，等齐模式未发送交易", short(a))
				return
			}
			b.st.Logf("warn", "%s 委托未确认，跳过", short(a))
			continue
		}
		usable = append(usable, a)
	}
	who = usable
	if len(who) == 0 {
		b.st.Logf("bad", "没有已确认委托的参赛账号")
		return
	}
	if err := sq.requireDelegated(ctx, master.Address); err != nil {
		b.st.Logf("bad", "%v", err)
		return
	}
	current, err := sq.arenaProgress(ctx, arena, who, true)
	if err != nil {
		b.st.Logf("bad", "读取场次失败：%v", err)
		return
	}
	day := current.Day
	goals := map[common.Address]uint64{}
	for _, a := range who {
		goals[a] = min(current.Players[a].Matches+uint64(p.Rounds), uint64(MaxMatchesPerDay))
	}
	mode := "能打就打"
	if together {
		mode = "等齐再打"
	}
	b.st.Logf("ok", "开跑：%d 个号，每号再打最多 %d 场，%s；跨组账号按实际 Gas 合并处理", len(who), p.Rounds, mode)
	txs := 0
	var waitSince time.Time
	lastWait := ""
	for ctx.Err() == nil {
		b.run.ks.Touch()
		if current.Day != day {
			b.st.Logf("warn", "已跨游戏日，请按新一天场次重新开跑")
			return
		}
		eligible, remaining, reason := readyPlayers(current, who, goals, p.Rounds == 1 && !together)
		completed := p.Rounds
		for _, a := range who {
			left := max(0, int(goals[a])-int(current.Players[a].Matches))
			completed = min(completed, p.Rounds-left)
		}
		b.set(func(pr *Progress) { pr.Round = completed })
		if remaining == 0 {
			b.st.Logf("ok", "本次完成：确认 %d 笔入场交易；收益保留，需收款时手动归集", txs)
			return
		}
		if len(eligible) == 0 || (together && len(eligible) < remaining) {
			if p.Rounds == 1 && !together {
				b.set(func(pr *Progress) {
					pr.Round = 1
					pr.Skipped = remaining
					pr.Reason = fmt.Sprintf("本次处理结束，%d 个账号仍在局内或等待条件，不等待补打", remaining)
				})
				b.st.Logf("ok", "本次结束：%d 笔交易，%d 个账号跳过，不等待补打", txs, remaining)
				return
			}
			if waitSince.IsZero() {
				waitSince = time.Now()
			}
			if time.Since(waitSince) >= 20*time.Minute {
				b.set(func(pr *Progress) { pr.Reason = "等待入场条件超过 20 分钟，本次结束：" + reason })
				b.st.Logf("warn", "%s", b.st.Snapshot().Batch.Reason)
				return
			}
			status := fmt.Sprintf("%s：%d/%d 已就绪；%s", mode, len(eligible), remaining, reason)
			if status != lastWait {
				b.set(func(pr *Progress) { pr.Step = status })
				b.st.Logf("", "%s", status)
				lastWait = status
			}
			next, err := b.awaitArenaChange(ctx, sq, arena, who, current, time.Until(waitSince.Add(20*time.Minute)))
			if err != nil {
				b.set(func(pr *Progress) { pr.Reason = fmt.Sprintf("等待结束：%v", err) })
				b.st.Logf("warn", "%v", err)
				return
			}
			current = next
			continue
		}
		waitSince = time.Time{}
		lastWait = ""
		b.set(func(pr *Progress) {
			pr.Step = fmt.Sprintf("整批模拟 %d 个账号及 Gas", len(eligible))
			pr.Reason = ""
		})
		preview, err := b.preparePlay(ctx, sq, eligible, current.Block, together)
		if err != nil {
			b.set(func(pr *Progress) { pr.Reason = err.Error() })
			b.st.Logf("bad", "未发送新的交易：%v", err)
			return
		}
		b.st.Logf("", "整批模拟（区块 %d）：%d/%d 个可入场，主号实际补币 %s fLGNS，Gas 上限 %d", preview.Block, len(preview.Who), len(eligible), fmtUnits(preview.Funded), preview.Gas)
		b.set(func(pr *Progress) { pr.Step = fmt.Sprintf("第 %d 笔 · %d 个号入场", txs+1, len(preview.Who)) })
		rec, err := sq.sendPlay(ctx, master, preview)
		if err != nil {
			b.st.Logf("bad", "入场未完成，已停止：%v", err)
			return
		}
		txs++
		got, err := enteredPlayers(rec, arena, preview.Who)
		if err != nil {
			b.st.Logf("bad", "%v", err)
			return
		}
		b.set(func(pr *Progress) { pr.Entered += len(got) })
		b.st.Logf("ok", "第 %d 笔：确认 %d/%d 个号入场（gas %d）", txs, len(got), len(preview.Who), rec.GasUsed)
		b.refreshBalances()
		if len(got) != len(preview.Who) {
			b.set(func(pr *Progress) {
				pr.Reason = "上链结果与模拟不同，已停止；请核对未入场账号后再操作"
			})
			b.st.Logf("bad", "%s", b.st.Snapshot().Batch.Reason)
			return
		}
		var confirmed []uint64
		if rec.BlockNumber != nil {
			confirmed = []uint64{rec.BlockNumber.Uint64()}
		}
		current, err = sq.arenaProgress(ctx, arena, who, true, confirmed...)
		if err != nil {
			b.st.Logf("bad", "交易已确认，但读取最新场次失败：%v", err)
			return
		}
	}
	b.st.Logf("warn", "已停止，已发送交易已等待回执")
}

func (b *Batch) HarvestNow(who []common.Address) error {
	if _, _, err := b.run.ready(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.prog.Running {
		b.mu.Unlock()
		return fmt.Errorf("正在打，等它跑完或者按停止")
	}
	if !b.st.TryBusy("批量运行") {
		b.mu.Unlock()
		return fmt.Errorf("有其他操作正在进行")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.prog = Progress{Running: true, Step: "收集"}
	b.mu.Unlock()

	go func() {
		defer cancel()
		defer b.finish()
		if err := b.harvest(ctx, who); err != nil {
			b.st.Logf("bad", "收集未完成：%v", err)
			b.set(func(pr *Progress) { pr.Running = false; pr.Step = "收集未完成" })
			return
		}
		b.set(func(pr *Progress) {
			pr.Running = false
			pr.Step = "收完"
		})
		b.mu.Lock()
		b.cancel = nil
		b.mu.Unlock()
	}()
	return nil
}

func (b *Batch) harvest(ctx context.Context, who []common.Address) error {
	sq, master, err := b.run.ready()
	if err != nil {
		b.st.Logf("bad", "%v", err)
		return err
	}
	total := new(big.Int)
	defer func() {
		b.set(func(pr *Progress) { pr.Harvested = fmtUnits(total) })
	}()
	for pass := 1; pass <= 5; pass++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.run.ks.Touch()
		var err error
		before, berr := sq.Overview(ctx)
		var rec *types.Receipt
		if len(who) == 0 {
			rec, err = sq.HarvestAll(ctx, master, 0, 0)
		} else {
			rec, err = sq.HarvestSome(ctx, master, who)
		}
		if err == nil {
			err = sq.HarvestJiaziPages(ctx, master, who)
		}
		after, aerr := sq.Overview(ctx)
		if err != nil {
			b.st.Logf("bad", "收集失败：%v", err)
			return err
		}

		if berr == nil && aerr == nil && after.MasterWei.Cmp(before.MasterWei) > 0 {
			total.Add(total, new(big.Int).Sub(after.MasterWei, before.MasterWei))
		}
		b.st.Logf("ok", "收集第 %d 趟：进主号 %s（gas %d）", pass, fmtUnits(total), rec.GasUsed)

		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		refreshErr := b.run.Refresh(rctx)
		cancel()
		if refreshErr != nil {
			return fmt.Errorf("交易已上链，但无法确认是否收干净：%w", refreshErr)
		}
		pick := map[common.Address]bool{}
		for _, a := range who {
			pick[a] = true
		}
		left := new(big.Int)
		for _, row := range b.st.Snapshot().Rows {
			if len(pick) > 0 && !pick[row.Who] {
				continue
			}
			left.Add(left, row.Ready)
		}
		if left.Sign() == 0 {
			b.st.Logf("ok", "收干净了，全部进主号")
			return nil
		}
		b.st.Logf("", "还剩 %s 可收，再来一趟", fmtUnits(left))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("收了 5 趟还有剩，请等结算后继续收集")
}

func (b *Batch) finish() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.run.Refresh(ctx); err != nil {
		b.st.Logf("warn", "余额刷新失败，页面保留上次快照：%v", err)
	}
	b.st.Idle()
}
