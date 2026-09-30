package main

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func parseFundingAmount(input string) (*big.Int, error) {
	s := strings.TrimSpace(input)
	bad := fmt.Errorf("转入数量须为正数，最多 18 位小数")
	if len(s) == 0 || len(s) > 80 {
		return nil, bad
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, bad
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
		if len(frac) == 0 || len(frac) > 18 {
			return nil, bad
		}
	}
	for _, c := range parts[0] + frac {
		if c < '0' || c > '9' {
			return nil, bad
		}
	}
	n, ok := new(big.Int).SetString(parts[0]+frac+strings.Repeat("0", 18-len(frac)), 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, bad
	}
	return n, nil
}

func (s *Server) handleFund(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Who        []string `json:"who"`
		AmountEach string   `json:"amountEach"`
	}
	if err := s.body(r, &req); err != nil {
		s.fail(w, 400, err)
		return
	}
	amount, err := parseFundingAmount(req.AmountEach)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(req.Who) == 0 || len(req.Who) > 500 {
		s.fail(w, 400, fmt.Errorf("请选择 1–500 个钱包"))
		return
	}
	var who []common.Address
	seen := map[common.Address]bool{}
	for _, value := range req.Who {
		a, err := parseAddr(value)
		if err != nil || a == (common.Address{}) {
			s.fail(w, 400, fmt.Errorf("充值地址无效"))
			return
		}
		if !seen[a] {
			who = append(who, a)
			seen[a] = true
		}
	}
	if err := s.busy("批量转入 fLGNS", func(ctx context.Context) error {
		return s.run.Fund(ctx, who, amount)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (r *Runner) Fund(ctx context.Context, who []common.Address, amount *big.Int) error {
	if err := r.ks.requireLocalAccounts(who); err != nil {
		return err
	}
	sq, master, err := r.ready()
	if err != nil {
		return err
	}
	var members []common.Address
	seen := map[common.Address]bool{}
	for _, a := range who {
		if a != master.Address && !seen[a] {
			members = append(members, a)
			seen[a] = true
		}
	}
	if len(members) == 0 || len(members) > 500 || amount == nil || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return fmt.Errorf("请选择队员并填写有效转入数量；主号不向自己转账")
	}
	total := new(big.Int).Mul(amount, big.NewInt(int64(len(members))))
	if total.BitLen() > 256 {
		return fmt.Errorf("转入总数量过大")
	}
	r.st.Logf("", "向 %d 个队员各转入 %s fLGNS，合计 %s", len(members), fmtUnits(amount), fmtUnits(total))
	rec, err := sq.send(ctx, master, 0, "fundSome", members, amount)
	if err != nil {
		return err
	}
	r.st.Logf("ok", "批量转入完成：%d 个队员，共 %s fLGNS（交易 %s）", len(members), fmtUnits(total), rec.TxHash.Hex())
	return nil
}
