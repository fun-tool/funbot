package main

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

type dashboardData struct {
	overview    *Overview
	rows        []*Pulse
	delegations map[string]bool
	masterReady bool
}

func (s *Squad) dashboardAt(ctx context.Context, tag string, seats []VaultMeta, prev View, full bool, extraReads []rpc.BatchElem) (dashboardData, error) {
	data := dashboardData{delegations: maps.Clone(prev.Delegations)}
	if full || data.delegations == nil {
		data.delegations = make(map[string]bool)
	}
	var reqs []rpc.BatchElem
	addCall := func(method string, args ...any) (int, error) {
		input, err := s.abi.Pack(method, args...)
		if err != nil {
			return 0, err
		}
		reqs = append(reqs, rpc.BatchElem{Method: "eth_call", Args: []any{map[string]any{"to": s.acct, "data": hexutil.Bytes(input)}, tag}, Result: new(hexutil.Bytes)})
		return len(reqs) - 1, nil
	}
	overview, err := addCall("overview")
	if err != nil {
		return data, err
	}
	members, err := addCall("allMembers")
	if err != nil {
		return data, err
	}
	pulses := make(map[common.Address]int)
	for _, row := range prev.Rows {
		index, err := addCall("pulse", row.Who)
		if err != nil {
			return data, err
		}
		pulses[row.Who] = index
	}
	target := delegationCode(s.impl)
	queueCodes := func(addrs []common.Address) []rpc.BatchElem {
		checks := make([]rpc.BatchElem, len(addrs))
		for i, a := range addrs {
			checks[i] = rpc.BatchElem{Method: "eth_getCode", Args: []any{a, tag}, Result: new(hexutil.Bytes)}
		}
		return checks
	}
	applyCodes := func(addrs []common.Address, checks []rpc.BatchElem) error {
		for i, a := range addrs {
			if err := checks[i].Error; err != nil {
				data.delegations = nil
				return fmt.Errorf("读取 %s 委托失败：%w", short(a), err)
			}
			data.delegations[strings.ToLower(a.Hex())] = equalHex(*checks[i].Result.(*hexutil.Bytes), target)
		}
		return nil
	}
	check := []common.Address{s.acct}
	if full {
		for _, seat := range seats {
			a := common.HexToAddress(seat.Address)
			if _, covered := pulses[a]; a != s.acct && !covered {
				check = append(check, a)
			}
		}
	}
	codesAt := len(reqs)
	reqs = append(reqs, queueCodes(check)...)
	start := len(reqs)
	reqs = append(reqs, extraReads...)
	s.chain.readBatch(ctx, reqs)
	copy(extraReads, reqs[start:])
	if err := applyCodes(check, reqs[codesAt:start]); err != nil {
		return data, err
	}
	data.masterReady = data.delegations[strings.ToLower(s.acct.Hex())]
	fillMissing := func() error {
		if !full {
			return nil
		}
		var missing []common.Address
		for _, seat := range seats {
			a := common.HexToAddress(seat.Address)
			if _, ok := data.delegations[strings.ToLower(a.Hex())]; !ok {
				missing = append(missing, a)
			}
		}
		checks := queueCodes(missing)
		s.chain.readBatch(ctx, checks)
		return applyCodes(missing, checks)
	}
	if !data.masterReady {
		err := fillMissing()
		return data, err
	}
	unpack := func(method string, index int) ([]any, error) {
		req := reqs[index]
		if req.Error != nil {
			return nil, req.Error
		}
		return s.abi.Unpack(method, *req.Result.(*hexutil.Bytes))
	}
	values, err := unpack("overview", overview)
	if err != nil {
		return data, err
	}
	data.overview = decodeOverview(values)
	values, err = unpack("allMembers", members)
	if err != nil {
		return data, err
	}
	roster := make(map[common.Address]bool)
	for _, a := range values[0].([]common.Address) {
		roster[a] = true
	}

	var missing []common.Address
	for _, seat := range seats {
		a := common.HexToAddress(seat.Address)
		if _, ok := pulses[a]; roster[a] && !ok {
			missing = append(missing, a)
		}
	}
	extra, errs := s.PulseBatch(ctx, missing, tag)
	next := 0
	for _, seat := range seats {
		a := common.HexToAddress(seat.Address)
		if !roster[a] {
			continue
		}
		var row *Pulse
		if idx, ok := pulses[a]; ok {
			req := reqs[idx]
			err = req.Error
			if err == nil {
				row, err = s.decodePulse(a, *req.Result.(*hexutil.Bytes))
			}
		} else {
			row, err = extra[next], errs[next]
			next++
		}
		if err != nil {
			return data, fmt.Errorf("读取 %s 状态失败：%w", short(a), err)
		}
		row.Label = seat.Label
		data.rows = append(data.rows, row)
		data.delegations[strings.ToLower(a.Hex())] = row.Delegated
	}
	err = fillMissing()
	return data, err
}
