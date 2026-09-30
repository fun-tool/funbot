package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
)

func (k *Keystore) localRoster(roster []common.Address) []common.Address {
	members := make(map[common.Address]bool, len(roster))
	for _, a := range roster {
		members[a] = true
	}
	var local []common.Address
	for _, seat := range k.Meta() {
		a := common.HexToAddress(seat.Address)
		if members[a] {
			local = append(local, a)
			delete(members, a)
		}
	}
	return local
}

func (k *Keystore) requireLocalAccounts(who []common.Address) error {
	if len(who) == 0 {
		return fmt.Errorf("请明确选择至少一个本地账号")
	}
	local := make(map[common.Address]bool)
	for _, seat := range k.Meta() {
		local[common.HexToAddress(seat.Address)] = true
	}
	for _, a := range who {
		if !local[a] {
			return fmt.Errorf("%s 已删除或不在本地账号中，请刷新后重新选择", short(a))
		}
	}
	return nil
}

func (k *Keystore) RenameSeat(password string, addr common.Address, label string) error {
	label = strings.TrimSpace(label)
	if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > 80 {
		return fmt.Errorf("名称需为 1–80 个字符")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	release, err := lockVault(k.path)
	if err != nil {
		return err
	}
	defer release()
	if err := k.reloadLocked(); err != nil {
		return err
	}
	if k.vault == nil {
		return fmt.Errorf("还没有密钥文件")
	}
	plain, err := decrypt(k.vault, password)
	if err != nil {
		return err
	}
	var sec vaultSecret
	err = json.Unmarshal(plain, &sec)
	wipe(plain)
	if err != nil {
		return err
	}
	for i := range sec.Seats {
		key, a, err := sec.Seats[i].resolve()
		wipeKey(key)
		if err != nil {
			return err
		}
		if a == addr {
			sec.Seats[i].Label = label
			return k.persistAndRefresh(&sec, password)
		}
	}
	return fmt.Errorf("没找到这个账号")
}

func (s *Server) handleSeatRename(w http.ResponseWriter, r *http.Request) {
	var b struct{ Address, Label, Password string }
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	a, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if b.Password == "" {
		s.fail(w, 400, fmt.Errorf("请输入密钥文件密码以重新加密保存名称"))
		return
	}
	if err := s.busy("修改账号名称", func(ctx context.Context) error {
		return s.ks.RenameSeat(b.Password, a, b.Label)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleMentors(w http.ResponseWriter, r *http.Request) {
	if s.ks.Locked() {
		s.fail(w, 400, fmt.Errorf("请先解锁"))
		return
	}
	seats := s.ks.Meta()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result := map[string]string{}

	for start := 0; start < len(seats); start += 100 {
		end := start + 100
		if end > len(seats) {
			end = len(seats)
		}
		data := make([][]byte, end-start)
		for i, seat := range seats[start:end] {
			data[i], _ = extraReadABI.Pack("masterOf", common.HexToAddress(seat.Address))
		}
		out, errs := s.chain.CallBatch(ctx, Mainnet.Lineage, data)
		for i, seat := range seats[start:end] {
			if errs[i] != nil {
				continue
			}
			v, err := extraReadABI.Unpack("masterOf", out[i])
			if err != nil {
				continue
			}
			result[strings.ToLower(seat.Address)] = v[0].(common.Address).Hex()
		}
	}
	s.writeJSON(w, 200, result)
}
