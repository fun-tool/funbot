package main

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type Runner struct {
	settings *SettingsStore
	mu       sync.Mutex
	chain    *Chain
	ks       *Keystore
	st       *State

	squad *Squad
	cfg   *Config
}

func NewRunner(c *Chain, ks *Keystore, st *State) *Runner {
	return &Runner{chain: c, ks: ks, st: st}
}

func (r *Runner) Attach(ctx context.Context, addr common.Address) error {
	code, err := r.chain.CodeAt(ctx, addr)
	if err != nil {
		return err
	}
	if len(code) == 0 {
		return fmt.Errorf("%s 上没有合约", addr.Hex())
	}
	master := r.ks.Master()
	if master == nil {
		return fmt.Errorf("密钥文件里没有主号 —— 先导入主号再连合约")
	}

	sq, err := NewSquad(r.chain, addr, master.Address)
	if err != nil {
		return err
	}
	cfg, err := sq.Config(ctx)
	if err != nil {
		return fmt.Errorf("读合约配置失败 —— 这个地址可能不是本程序的合约: %w", err)
	}

	if err := verifySquadCode(code, cfg, addr, master.Address); err != nil {
		return err
	}

	r.mu.Lock()
	r.squad, r.cfg = sq, cfg
	r.mu.Unlock()
	label := Mainnet.Label
	r.st.Set(func(v *View) {
		v.Contract = addr.Hex()
		v.Config = cfg
		v.Net = label
	})
	r.st.Logf("ok", "已连上【%s】合约 %s", label, short(addr))
	return nil
}

func (r *Runner) Squad() *Squad {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.squad
}

func (r *Runner) Config() *Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *Runner) Deploy(ctx context.Context, shifu common.Address) (common.Address, error) {
	master := r.ks.Master()
	if master == nil {
		return common.Address{}, fmt.Errorf("还没有导入主号")
	}
	net := Mainnet
	if shifu == (common.Address{}) {
		return common.Address{}, fmt.Errorf("师父地址不能为空")
	}
	if err := validateShifu(ctx, r.chain, shifu); err != nil {
		return common.Address{}, err
	}
	r.st.Logf("warn", "在【%s】部署，师父 %s —— 拜师永久不可改", net.Label, shifu.Hex())
	addr, err := DeploySquad(ctx, r.chain, master, net, shifu)
	if err != nil {
		return common.Address{}, err
	}
	r.st.Logf("ok", "合约部署在 %s", addr.Hex())
	if err := r.Attach(ctx, addr); err != nil {
		return addr, err
	}
	return addr, nil
}

func (r *Runner) Refresh(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			r.st.Set(func(v *View) { v.BalanceError = err.Error() })
		}
	}()
	sq := r.Squad()
	if sq == nil {
		return nil
	}
	if n, err := r.chain.BlockNumber(ctx); err == nil {
		r.st.Set(func(v *View) { v.Block = n })
	}

	master := r.ks.Master()
	if master == nil {
		return nil
	}
	code, err := r.chain.CodeAt(ctx, master.Address)
	if err != nil {
		return err
	}
	cfg := r.Config()
	if cfg == nil || !equalHex(code, delegationCode(cfg.Impl)) {
		r.st.Set(func(v *View) {
			v.MasterReady = false
			v.Overview = nil
			v.Rows = nil
		})
		return nil
	}

	ov, err := sq.Overview(ctx)
	if err != nil {
		return err
	}
	onChain, err := sq.Members(ctx)
	if err != nil {
		return err
	}

	labels := map[common.Address]string{}
	for _, s := range r.ks.Seats() {
		labels[s.Address] = s.Label
	}

	ps, errs := sq.PulseBatch(ctx, onChain)
	rows := make([]*Pulse, 0, len(onChain))
	for i, a := range onChain {
		if errs[i] != nil {

			return fmt.Errorf("读取 %s 状态失败，本次结果不完整：%w", short(a), errs[i])
		}
		ps[i].Label = labels[a]
		rows = append(rows, ps[i])
	}
	countWho := append([]common.Address{}, onChain...)
	if !slices.Contains(countWho, master.Address) {
		countWho = append(countWho, master.Address)
	}
	countCtx, countCancel := context.WithTimeout(ctx, 8*time.Second)
	counts, countErr := sq.ChampionshipCounts(countCtx, cfg.Arena, countWho)
	countCancel()
	var masterChampionships *string
	if countErr == nil {
		for _, row := range rows {
			n := strconv.FormatUint(counts[row.Who], 10)
			row.Championships = &n
		}
		n := strconv.FormatUint(counts[master.Address], 10)
		masterChampionships = &n
	}
	r.st.Set(func(v *View) {
		v.MasterChampionships = masterChampionships
		v.ChampionshipsError = ""
		if countErr != nil {
			v.ChampionshipsError = countErr.Error()
		}
		v.MasterReady = true
		v.Overview = ov
		v.Rows = rows
		v.BalancesUpdated = time.Now().Format("15:04:05")
		v.BalanceError = ""
		v.GasPrice = r.chain.GasPrice().String()
	})
	return nil
}

func (r *Runner) PlayOne(ctx context.Context, who common.Address) error {
	if r.settings != nil && !r.settings.Get().MasterPlays {
		if master := r.ks.Master(); master != nil && master.Address == who {
			return fmt.Errorf("设置已关闭主号参赛")
		}
	}
	sq, master, err := r.ready()
	if err != nil {
		return err
	}

	rec, err := sq.PlayOne(ctx, master, who)
	if err != nil {
		return err
	}
	r.st.Logf("ok", "%s 打了一局（gas %d）", short(who), rec.GasUsed)
	return nil
}

func (r *Runner) HarvestOne(ctx context.Context, who common.Address) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	p, err := sq.Pulse(ctx, who)
	if err != nil {
		return err
	}
	if !p.Delegated {
		return fmt.Errorf("%s 还没升级", short(who))
	}

	if !p.CanHarvest && !p.Truncated {
		if p.Locked.Sign() > 0 {
			return fmt.Errorf("%s 现在没有能收的；还有 %s 压在未结算的局里，等 %d 个区块",
				short(who), p.LockedStr, p.BlocksToWait)
		}
		return fmt.Errorf("%s 身上没有可收的", short(who))
	}

	r.st.Logf("", "%s 收 %s：钱包 %s · 擂台 %s · 师承 %s · 甲子 %s",
		label(p), p.ReadyStr, p.WalletStr, p.SettledStr, p.LineageStr, p.JiaziStr)
	rec, err := sq.HarvestOne(ctx, master, who)
	if err != nil {
		return err
	}

	if err := sq.HarvestJiaziPages(ctx, master, []common.Address{who}); err != nil {
		return err
	}

	after, err := sq.Pulse(ctx, who)
	switch {
	case err != nil:
		r.st.Logf("ok", "%s 已收（gas %d，交易 %s）", label(p), rec.GasUsed, short2(rec.TxHash))
	case after.Ready.Sign() > 0:

		r.st.Logf("warn", "%s 收完又出来 %s（收集顺带推了结算）—— 再点一次", label(p), after.ReadyStr)
	case after.Locked.Sign() > 0:
		r.st.Logf("ok", "%s 已收干净；还有 %s 压在局里，结算后可收（gas %d）", label(p), after.LockedStr, rec.GasUsed)
	default:
		r.st.Logf("ok", "%s 已收干净，全部进主号（gas %d）", label(p), rec.GasUsed)
	}
	return nil
}

func (r *Runner) Delegate(ctx context.Context, who []common.Address) error {
	if err := r.verifyAttached(ctx); err != nil {
		return err
	}
	_, master, err := r.ready()
	if err != nil {
		return err
	}
	cfg := r.Config()

	seats := []*Seat{master}
	seen := map[common.Address]bool{master.Address: true}
	for _, a := range who {
		s := r.ks.Find(a)
		if s == nil {
			return fmt.Errorf("%s 不在密钥文件里，没有私钥就签不出授权", short(a))
		}
		if !seen[a] {
			seats = append(seats, s)
			seen[a] = true
		}
	}

	r.st.Logf("", "开始检查 %d 个账号的委托状态", len(seats))
	plan, err := r.chain.PlanDelegation(ctx, master, seats, cfg.Impl, func(done, total int) {
		r.st.Logf("", "委托检查 %d/%d", done, total)
	})
	if err != nil {
		return err
	}
	todo := 0
	for _, a := range plan.Accounts {
		if !a.Already {
			todo++
		}
	}
	if todo == 0 {
		return fmt.Errorf("这些号都已经委托好了")
	}

	r.st.Logf("", "委托 %d 个未完成账号到 %s（主号未委托时会一并处理）",
		todo, short(cfg.Impl))

	hash, err := r.chain.Delegate(ctx, master, seats, plan)
	if err != nil {
		return err
	}
	r.st.Logf("", "委托交易已提交，等待确认：%s", hash.Hex())
	rec, err := r.chain.WaitReceipt(ctx, hash)
	if err != nil {
		return err
	}

	got, err := r.chain.VerifyDelegation(ctx, seats, cfg.Impl, func(done, total int) {
		r.st.Logf("", "委托确认 %d/%d", done, total)
	})
	if err != nil {
		return err
	}
	if rec.Status == 0 {
		r.st.Logf("warn", "委托交易回执是回滚（gas %d），但授权在执行前就应用了 —— 以回读为准", rec.GasUsed)
	}
	ok, bad := 0, []string{}
	for _, s := range seats {
		if got[s.Address] {
			ok++
		} else {
			bad = append(bad, short(s.Address))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("交易上链了但有 %d 个号没生效：%v —— 授权被丢弃，多半是 nonce 对不上", len(bad), bad)
	}
	r.st.Logf("ok", "%d 个号委托生效（gas %d）", ok, rec.GasUsed)
	return nil
}

func (r *Runner) AddMembers(ctx context.Context, who []common.Address) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	if len(who) == 0 {
		return fmt.Errorf("没有要加的号")
	}
	rec, err := sq.AddMembers(ctx, master, who)
	if err != nil {
		return err
	}
	r.st.Logf("ok", "%d 个号加入名册（gas %d）", len(who), rec.GasUsed)
	return nil
}

func (r *Runner) RemoveMember(ctx context.Context, who common.Address) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	if _, err := sq.RemoveMember(ctx, master, who); err != nil {
		return err
	}
	r.st.Logf("ok", "%s 已移出名册", short(who))
	return nil
}

func (r *Runner) Prepare(ctx context.Context, who []common.Address) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	if len(who) == 0 {
		return fmt.Errorf("请选择要拜师授权的账号")
	}
	if err := validateShifu(ctx, r.chain, r.Config().Shifu); err != nil {
		return err
	}
	members, err := sq.Members(ctx)
	if err != nil {
		return err
	}
	selected := map[common.Address]bool{}
	for _, a := range who {
		selected[a] = true
	}
	for _, a := range members {
		if selected[a] {
			delete(selected, a)
		}
	}
	if len(selected) > 0 {
		return fmt.Errorf("所选账号尚未全部加入名册")
	}
	for _, a := range who {
		selected[a] = true
	}
	for i := 0; i < len(members); {
		if !selected[members[i]] {
			i++
			continue
		}
		start := i
		for i < len(members) && selected[members[i]] {
			i++
		}
		rec, err := sq.send(ctx, master, 0, "prepareAll", big.NewInt(int64(start)), big.NewInt(int64(i-start)))
		if err != nil {
			return err
		}
		r.st.Logf("ok", "选中账号拜师授权 %d 个（gas %d）", i-start, rec.GasUsed)
	}
	return sq.VerifyPrepared(ctx, who...)
}

func (r *Runner) SweepGas(ctx context.Context, who []common.Address) error {
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	before, _ := sq.Overview(ctx)
	rec, err := sq.SweepGas(ctx, master, who)
	if err != nil {
		return err
	}
	after, aerr := sq.Overview(ctx)
	if before != nil && aerr == nil {
		r.st.Logf("ok", "gas 币收回主号：%s → %s（gas %d）",
			before.MasterGas, after.MasterGas, rec.GasUsed)
	} else {
		r.st.Logf("ok", "gas 币已收回主号（gas %d）", rec.GasUsed)
	}
	return nil
}

func (r *Runner) ready() (*Squad, *Seat, error) {
	if r.ks.Locked() {
		return nil, nil, fmt.Errorf("先解锁密钥文件")
	}
	sq := r.Squad()
	if sq == nil {
		return nil, nil, fmt.Errorf("还没连上合约")
	}
	master := r.ks.Master()
	if master == nil {
		return nil, nil, fmt.Errorf("密钥文件里没有主号")
	}
	if cfg := r.Config(); cfg == nil || cfg.Master != master.Address {
		return nil, nil, fmt.Errorf("当前主号与合约不一致，请重新载入合约")
	}
	return sq, master, nil
}

func label(p *Pulse) string {
	if p.Label != "" {
		return p.Label
	}
	return short(p.Who)
}

func short2(h common.Hash) string {
	s := h.Hex()
	return s[:10] + "…"
}

func (r *Runner) pollEvery(ctx context.Context, d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.ks.Locked() || r.Squad() == nil || r.st.Busy() != "" {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := r.Refresh(rctx); err != nil && ctx.Err() == nil {
				r.st.Logf("bad", "刷新失败: %v", err)
			}
			cancel()
		}
	}
}
