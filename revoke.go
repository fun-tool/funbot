package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
)

func (c *Chain) PlanRevocation(ctx context.Context, master *Seat, seats []*Seat, impl common.Address, progress func(int, int)) (*DelegatePlan, error) {
	if master == nil || impl == (common.Address{}) {
		return nil, fmt.Errorf("请先连接合约并解锁主号")
	}
	codes, nonces, err := c.delegationState(ctx, seats, true, progress)
	if err != nil {
		return nil, err
	}
	plan := &DelegatePlan{Master: master.Address, Revoke: true, GasPrice: c.GasPrice().String()}
	want := delegationCode(impl)
	n := 0
	for i, s := range seats {
		if len(*codes[i]) != 0 && !equalHex(*codes[i], want) {
			return nil, fmt.Errorf("%s 的委托目标不是当前合约，已停止；请先核对该钱包的委托", short(s.Address))
		}
		already := len(*codes[i]) == 0
		plan.Accounts = append(plan.Accounts, DelegateEntry{Address: s.Address, Label: s.Label, Nonce: uint64(*nonces[i]), Already: already})
		if !already {
			n++
		}
	}
	plan.GasLimit = 60_000 + uint64(n)*40_000
	return plan, nil
}

func (r *Runner) RevokeDelegation(ctx context.Context, who []common.Address) error {
	if len(who) == 0 {
		return fmt.Errorf("请选择要取消委托的账号")
	}
	if err := r.verifyAttached(ctx); err != nil {
		return err
	}
	_, master, err := r.ready()
	if err != nil {
		return err
	}
	var seats []*Seat
	seen := map[common.Address]bool{}
	for _, a := range who {
		if seen[a] {
			continue
		}
		seen[a] = true
		seat := r.ks.Find(a)
		if seat == nil {
			return fmt.Errorf("%s 不在已解锁的密钥文件中", short(a))
		}
		seats = append(seats, seat)
	}
	r.st.Logf("warn", "检查取消委托：仅处理选中的 %d 个账号", len(seats))
	plan, err := r.chain.PlanRevocation(ctx, master, seats, r.Config().Impl, func(done, total int) { r.st.Logf("", "取消委托检查 %d/%d", done, total) })
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
		r.st.Logf("ok", "选中的 %d 个账号均无委托，无需发送交易", len(seats))
		return nil
	}
	hash, err := r.chain.Delegate(ctx, master, seats, plan)
	if err != nil {
		return err
	}
	r.st.Logf("", "取消委托交易已提交，等待确认：%s", hash.Hex())
	rec, err := r.chain.WaitReceipt(ctx, hash)
	if err != nil {
		return err
	}
	verified, err := r.chain.VerifyDelegation(ctx, seats, common.Address{}, func(done, total int) { r.st.Logf("", "取消委托确认 %d/%d", done, total) })
	if err != nil {
		return fmt.Errorf("交易 %s 已上链，但取消结果未核实：%w", hash.Hex(), err)
	}
	var failed []string
	for _, seat := range seats {
		if !verified[seat.Address] {
			failed = append(failed, short(seat.Address))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("取消委托交易 %s 已上链，但这些账号仍有代码：%v；请核查后再操作", hash.Hex(), failed)
	}
	if rec.Status == 0 {
		r.st.Logf("warn", "交易执行回滚，但链上回读已确认委托清除；授权变更以回读结果为准")
	}
	r.st.Logf("ok", "已取消 %d 个账号的委托，另 %d 个原本无委托（gas %d）；资金未归集，代币授权未撤销", todo, len(seats)-todo, rec.GasUsed)
	if seen[master.Address] {
		r.st.Logf("warn", "主号委托已取消，整队批量操作暂停；需要使用时重新委托主号即可")
	}
	return nil
}

func (s *Server) handleRevokeDelegation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := s.body(r, &req); err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(req.Addresses) == 0 || len(req.Addresses) > 500 {
		s.fail(w, 400, fmt.Errorf("请选择 1–500 个要取消委托的账号"))
		return
	}
	who, err := parseAddrs(req.Addresses)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("取消委托", func(ctx context.Context) error { return s.run.RevokeDelegation(ctx, who) }); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}
