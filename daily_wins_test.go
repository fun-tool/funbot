package main

import (
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"math/big"
	"sync/atomic"
	"testing"
)

type dailyChainFixture struct {
	headers []*types.Header
	history map[common.Hash]uint64
	calls   atomic.Int32
}

func (f *dailyChainFixture) GetBlockByNumber(n hexutil.Uint64, _ bool) (*types.Header, error) {
	if int(n) >= len(f.headers) {
		return nil, errors.New("unknown block")
	}
	return f.headers[n], nil
}
func (f *dailyChainFixture) Call(call struct {
	To   common.Address
	Data hexutil.Bytes
}, b rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	f.calls.Add(1)
	if call.To != Mainnet.Arena || b.BlockHash == nil || !b.RequireCanonical {
		return nil, errors.New("wrong block")
	}
	n, ok := f.history[*b.BlockHash]
	if !ok {
		return nil, errors.New("historical state unavailable")
	}
	return extraReadABI.Methods["jiaziCount"].Outputs.Pack(new(big.Int).SetUint64(n))
}
func TestDailyWinsMidnightRestartAndReorg(t *testing.T) {
	a, b := common.HexToAddress("0xaa"), common.HexToAddress("0xbb")
	const midnight = 100*86400 - 8*3600
	f := &dailyChainFixture{history: map[common.Hash]uint64{}}
	for i, ts := range []uint64{midnight - 3, midnight - 1, midnight, midnight + 60, midnight + 86400} {
		h := &types.Header{Number: big.NewInt(int64(i)), Time: ts, Difficulty: big.NewInt(1)}
		if i > 0 {
			h.ParentHash = f.headers[i-1].Hash()
		}
		f.headers = append(f.headers, h)
	}
	f.history[f.headers[1].Hash()] = 30
	server := rpc.NewServer()
	defer server.Stop()
	if err := server.RegisterName("eth", f); err != nil {
		t.Fatal(err)
	}
	cl := ethclient.NewClient(rpc.DialInProc(server))
	defer cl.Close()
	s := Squad{chain: &Chain{c: cl, rpcs: []string{"test"}}}
	run := func(i int, current, want uint64, canonical *types.Header) {
		t.Helper()
		r, e := s.dailyWins(t.Context(), f.headers[i], Mainnet.Arena, []common.Address{a, b}, map[common.Address]uint64{a: current, b: 0}, canonical)
		if e != nil || len(r) != 2 || r[a] != want || r[b] != 0 {
			t.Fatalf("block %d: %v %v", i, r, e)
		}
	}
	run(2, 31, 1, nil)
	run(3, 33, 3, f.headers[2])
	if f.calls.Load() != 1 {
		t.Fatalf("same day made %d historical calls", f.calls.Load())
	}
	first := types.CopyHeader(f.headers[2])
	first.Time = midnight - 1
	f.headers[2] = first
	f.headers[3].ParentHash = first.Hash()
	f.history[first.Hash()] = 32
	run(3, 33, 1, first)
	f.headers[4].ParentHash = f.headers[3].Hash()
	counts, err := s.dailyWins(t.Context(), f.headers[4], Mainnet.Arena, []common.Address{a, b}, map[common.Address]uint64{a: 34, b: 0}, nil)
	if err == nil || len(counts) != 1 {
		t.Fatalf("missing history: %v %v", counts, err)
	}
	f.history[f.headers[3].Hash()] = 33
	run(4, 34, 1, f.headers[4])
}
