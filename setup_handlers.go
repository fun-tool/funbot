package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func (s *Server) saveConnection() error {
	cfg := s.run.Config()
	if cfg == nil {
		return nil
	}
	return s.settings.Update(func(v *Settings) {
		if v.Master != "" && !strings.EqualFold(v.Master, cfg.Master.Hex()) {
			v.Selected = nil
			v.All = true
		}
		v.Master, v.Contract = cfg.Master.Hex(), cfg.Impl.Hex()
	})
}

func (s *Server) restoreConnection(ctx context.Context) {
	v := s.settings.Get()
	address := v.Contract
	if s.startContract != "" {
		address = s.startContract
	}
	if address == "" {
		return
	}
	a, err := parseAddr(address)
	if err == nil {
		err = s.run.Attach(ctx, a)
	}
	if err == nil {
		err = s.saveConnection()
	}
	if err != nil {
		s.st.Logf("warn", "自动载入合约失败，可在设置中重新载入：%v", err)
		return
	}
	s.startContract = ""
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		EntryMode   *string   `json:"entryMode"`
		MasterPlays *bool     `json:"masterPlays"`
		Rounds      *int      `json:"rounds"`
		All         *bool     `json:"all"`
		Selected    *[]string `json:"selected"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	if !s.st.TryBusy("保存设置") {
		s.fail(w, 400, fmt.Errorf("运行中不能修改设置"))
		return
	}
	defer s.st.Idle()
	err := s.settings.Update(func(v *Settings) {
		if b.EntryMode != nil {
			v.EntryMode = *b.EntryMode
		}
		if b.MasterPlays != nil {
			v.MasterPlays = *b.MasterPlays
		}
		if b.Rounds != nil {
			v.Rounds = *b.Rounds
		}
		if b.All != nil {
			v.All = *b.All
		}
		if b.Selected != nil {
			v.Selected = *b.Selected
		}
	})
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleDeriveSaved(w http.ResponseWriter, r *http.Request) {
	var req DeriveSavedRequest
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.fail(w, 400, fmt.Errorf("派生请求字段无效，只接受来源地址、序号、数量、备注和密码"))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.fail(w, 400, fmt.Errorf("派生请求只能包含一个对象"))
		return
	}
	err := s.busy("派生钱包", func(ctx context.Context) error {
		n, err := s.ks.DeriveSaved(req)
		if err != nil {
			return err
		}
		s.st.Logf("ok", "派生完成：新增 %d 个钱包，已有 %d 个已跳过。新增钱包需完成委托、加入名册和授权", n, req.Count-n)
		return nil
	})
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleVaultReload(w http.ResponseWriter, r *http.Request) {
	err := s.busy("重新读取账号", func(ctx context.Context) error {
		s.ks.Lock()
		if err := s.ks.Load(); err != nil {
			return err
		}
		s.st.Logf("ok", "已重新读取账号，请解锁以载入最新钱包")
		return nil
	})
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) participants(ctx context.Context, requested []common.Address) ([]common.Address, error) {
	if len(requested) > 0 {
		if err := s.ks.requireLocalAccounts(requested); err != nil {
			return nil, err
		}
	}
	sq, master, err := s.run.ready()
	if err != nil {
		return nil, err
	}
	roster, err := sq.Members(ctx)
	if err != nil {
		return nil, err
	}
	roster = s.ks.localRoster(roster)
	want := map[common.Address]bool{}
	for _, a := range requested {
		want[a] = true
	}
	plays := s.settings.Get().MasterPlays
	var who []common.Address
	for _, a := range roster {
		if !plays && a == master.Address {
			continue
		}
		if len(requested) == 0 || want[a] {
			who = append(who, a)
			delete(want, a)
		}
	}
	if !plays {
		delete(want, master.Address)
	}
	if len(want) != 0 {
		return nil, fmt.Errorf("选择中有账号不在链上名册，请刷新后重新选择")
	}
	if len(who) == 0 {
		return nil, fmt.Errorf("没有参赛账号，请先加入名册或开启主号参赛")
	}
	return who, nil
}
