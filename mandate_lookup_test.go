package main

import (
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type stipendReadFixture struct {
	squad       *Squad
	wallet      common.Address
	total       int64
	completed   atomic.Int64
	detailRows  atomic.Int64
	calls       atomic.Int32
	failDetails atomic.Bool
}

func (f *stipendReadFixture) Call(call struct {
	To   common.Address
	Data hexutil.Bytes
}, block string) (hexutil.Bytes, error) {
	f.calls.Add(1)
	if block != "0x101" {
		return nil, fmt.Errorf("unexpected block")
	}
	contractABI := f.squad.abi
	if call.To == Mainnet.Arena {
		contractABI = extraReadABI
	} else if call.To != f.squad.impl {
		return nil, fmt.Errorf("wrong destination")
	}
	m, err := contractABI.MethodById(call.Data)
	if err != nil {
		return nil, err
	}
	args, err := m.Inputs.Unpack(call.Data[4:])
	if err != nil {
		return nil, err
	}
	wallet := args[0].(common.Address)
	if wallet == (common.Address{}) {
		return nil, fmt.Errorf("account unavailable")
	}
	if m.Name == "mandatePage" {
		n, mask, left, await := int64(0), int64(0), int64(61), int64(0)
		if wallet == f.wallet {
			n = f.total
			from := args[1].(*big.Int).Int64()
			if from == 0 && f.completed.Load() >= 2 {
				mask, await = 2, 1
			}
			if min(n, from+64) > f.completed.Load() {
				left = 1
			}
		}
		return m.Outputs.Pack(big.NewInt(mask), big.NewInt(n), big.NewInt(left), big.NewInt(await), big.NewInt(5))
	}
	if m.Name != "jiaziPage" || f.failDetails.Load() {
		return nil, fmt.Errorf("details unavailable")
	}
	var rows []JiaziRow
	from, limit := args[1].(*big.Int).Int64(), args[2].(*big.Int).Int64()
	f.detailRows.Add(limit)
	for i := from; i < min(from+limit, f.total); i++ {
		valid := int64(59)
		if i < f.completed.Load() {
			valid = 60
		}
		rows = append(rows, JiaziRow{Index: big.NewInt(i), RoundID: big.NewInt(0), ValidDays: big.NewInt(valid), Released: big.NewInt(0), ClaimedDays: big.NewInt(0), ClaimableAmount: big.NewInt(0)})
	}
	return m.Outputs.Pack(rows, big.NewInt(f.total))
}

func TestMandateDisplayCounts(t *testing.T) {
	wallet, empty := common.HexToAddress("0xabcd"), common.HexToAddress("0xeeee")
	sq, err := NewSquad(nil, common.HexToAddress("0xac"), wallet)
	if err != nil {
		t.Fatal(err)
	}
	f := &stipendReadFixture{squad: sq, wallet: wallet, total: 33}
	f.completed.Store(3)
	server := rpc.NewServer()
	defer server.Stop()
	if err := server.RegisterName("eth", f); err != nil {
		t.Fatal(err)
	}
	cl := ethclient.NewClient(rpc.DialInProc(server))
	defer cl.Close()
	sq.chain = &Chain{c: cl, rpcs: []string{"fixture"}}
	read := func(who ...common.Address) (*MandateSnapshot, map[common.Address]uint64, error) {
		return sq.finishMandates(t.Context(), "0x101", mandatePages(who, false, nil), nil, true)
	}
	snap, totals, err := read(wallet, empty)
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Accounts[wallet]
	if n, ok := totals[empty]; !ok || n != 0 || totals[wallet] != 33 || a.Completed != 3 || a.Available != 1 || a.AwaitingClaim != 1 || a.Remaining != 1 || a.Held != 5 || a.indices[0].Uint64() != 1 {
		t.Fatalf("counts: %v %+v", totals, a)
	}
	if f.calls.Load() != 3 {
		t.Fatalf("made %d RPC calls, want 3", f.calls.Load())
	}
	if f.detailRows.Swap(0) != 30 {
		t.Fatal("expected only the uncertain portion of the first page")
	}
	for _, tc := range []struct {
		total, completed, calls, rows int64
	}{
		{33, 3, 2, 2},
		{250, 67, 4, 65},
		{250, 67, 3, 2},
		{250, 0, 3, 65},
		{250, 0, 2, 1},
		{250, 250, 5, 1},
		{250, 250, 4, 0},
	} {
		f.total = tc.total
		f.completed.Store(tc.completed)
		f.calls.Store(0)
		pages := mandatePages([]common.Address{wallet}, false, snap)
		snap, _, err = sq.finishMandates(t.Context(), "0x101", pages, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		got, calls, rows := snap.Accounts[wallet].Completed, int64(f.calls.Load()), f.detailRows.Swap(0)
		if int64(got) != tc.completed || calls != tc.calls || rows != tc.rows {
			t.Fatalf("total %d, completed %d: got %d, calls %d, detail rows %d", tc.total, tc.completed, got, calls, rows)
		}
	}
	f.total = 33
	f.completed.Store(3)

	failed := common.Address{}
	snap, totals, err = read(failed, wallet, empty)
	if _, ok := totals[failed]; err == nil || snap != nil || ok || len(totals) != 2 || totals[wallet] != 33 {
		t.Fatalf("account failure: %v %v", totals, err)
	}
	f.failDetails.Store(true)
	snap, totals, err = read(wallet)
	if err == nil || snap != nil || totals[wallet] != 33 {
		t.Fatalf("detail failure: %v %v", totals, err)
	}
}
