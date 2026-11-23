package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestDashboardRefreshRequestCounts(t *testing.T) {
	for _, n := range []int{24, 481} {
		t.Run(fmt.Sprint(n), func(t *testing.T) { checkRefreshCounts(t, n) })
	}
}

func checkRefreshCounts(t *testing.T, n int) {
	st := NewState()
	var seats []VaultMeta
	var roster []common.Address
	for i := 1; i <= n; i++ {
		a := common.BigToAddress(big.NewInt(int64(i)))
		seats = append(seats, VaultMeta{Address: a.Hex()})
		roster = append(roster, a)
		st.v.Rows = append(st.v.Rows, &Pulse{Who: a})
	}
	master := roster[0]
	sq, err := NewSquad(nil, common.BigToAddress(big.NewInt(25)), master)
	if err != nil {
		t.Fatal(err)
	}
	head := &types.Header{Number: big.NewInt(500), Time: 1700000000, Difficulty: big.NewInt(1), Extra: []byte{}}
	var batches, headers, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		type request struct {
			ID     int
			Method string
			Params []json.RawMessage
		}
		reply := func(id int, result any) map[string]any {
			return map[string]any{"id": id, "jsonrpc": "2.0", "result": result}
		}
		w.Header().Set("Content-Type", "application/json")
		if !bytes.HasPrefix(body, []byte("[")) {
			var req request
			if err := json.Unmarshal(body, &req); err != nil || req.Method != "eth_getBlockByNumber" {
				t.Errorf("unexpected head request: %s", body)
				return
			}
			headers.Add(1)
			_ = json.NewEncoder(w).Encode(reply(req.ID, head))
			return
		}
		batches.Add(1)
		var reqs []request
		if err := json.Unmarshal(body, &reqs); err != nil {
			t.Error(err)
			return
		}
		var replies []map[string]any
		for _, req := range reqs {
			var fixed rpc.BlockNumberOrHash
			hashPinned := len(req.Params) == 2 && json.Unmarshal(req.Params[1], &fixed) == nil && fixed.BlockHash != nil && *fixed.BlockHash == head.Hash() && fixed.RequireCanonical
			if len(req.Params) != 2 || (!hashPinned && string(req.Params[1]) != fmt.Sprintf("\"0x%x\"", head.Number)) {
				t.Error("expected fixed snapshot block")
				return
			}
			var raw []byte
			switch req.Method {
			case "eth_getCode":
				raw = delegationCode(sq.impl)
			case "eth_call":
				var call struct{ Data hexutil.Bytes }
				if err := json.Unmarshal(req.Params[0], &call); err != nil {
					t.Error(err)
					return
				}
				m, err := sq.abi.MethodById(call.Data)
				if err != nil {
					t.Error(err)
					return
				}
				switch m.Name {
				case "mandatePage":
					z := big.NewInt(0)
					raw, err = m.Outputs.Pack(z, z, big.NewInt(61), z, z)
				case "mandateRewardsPage":
					z := big.NewInt(0)
					raw, err = m.Outputs.Pack(big.NewInt(1), z, z, []*big.Int{}, z)
				case "overview":
					raw = make([]byte, 32*len(m.Outputs))
				case "allMembers":
					raw, err = m.Outputs.Pack(roster)
				case "pulse":
					raw = make([]byte, 32*len(m.Outputs[0].Type.TupleElems))
					for i, name := range m.Outputs[0].Type.TupleRawNames {
						if name == "canHarvest" || name == "delegated" {
							raw[32*i+31] = 1
						}
					}
				default:
					t.Errorf("unexpected detail lookup %s", m.Name)
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
			default:
				t.Error(req.Method)
				return
			}
			replies = append(replies, reply(req.ID, hexutil.Encode(raw)))
			calls.Add(1)
		}
		_ = json.NewEncoder(w).Encode(replies)
	}))
	defer server.Close()
	client, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sq.chain = NewChain([]string{server.URL}, mainnetChainID)
	sq.chain.c = client
	ks := &Keystore{vault: &Vault{Public: seats}, seats: []*Seat{{Kind: "master", Address: master}}}
	r := NewRunner(sq.chain, ks, st)
	r.squad = sq
	r.cfg = &Config{Master: master}
	r.lastHead = types.CopyHeader(head)
	r.lastHead.Number = big.NewInt(499)
	r.lastSeats = seats
	r.lastDetails = time.Now()
	check := func(mode refreshMode, wantRequests, wantItems int) {
		t.Helper()
		headers.Store(0)
		batches.Store(0)
		calls.Store(0)
		if err := r.refresh(t.Context(), mode); err != nil {
			t.Fatal(err)
		}
		http := int(headers.Load() + batches.Load())
		items := int(headers.Load() + calls.Load())
		if http != wantRequests || items != wantItems {
			t.Fatalf("got %d HTTP, %d items; want %d, %d", http, items, wantRequests, wantItems)
		}
	}
	batchesFor := func(n int) int { return (n + 79) / 80 }
	check(refreshIdle, 1+batchesFor(n+3), n+4)
	check(refreshIdle, 1, 1)
	v := st.Snapshot()
	if len(v.Rows) != n || !v.Rows[0].CanHarvest || !v.MasterReady {
		t.Fatal("missing roster data")
	}
	// All addresses are registered. Empty stipend and NFT histories need only their first pages.
	check(refreshFull, 1+batchesFor(2*n+4), 2*n+5)
	if v := st.Snapshot(); v.TodayWins == nil || *v.TodayWins != 0 || v.Mandates == nil || v.MandateError != "" || v.TodayError != "" {
		t.Fatalf("incomplete details: %s %s", v.MandateError, v.TodayError)
	}
	check(refreshFull, 1, 1)
	r.lastHead = nil
	st.Set(func(v *View) { v.Rows = nil })
	check(refreshFull, 1+batchesFor(2*n+3)+batchesFor(n), 3*n+4)

}

func TestIdleReadYieldsToForeground(t *testing.T) {
	st := NewState()
	ctx, finish := st.beginIdleRead(t.Context())
	if ctx == nil || !st.TryBusy("批量运行") {
		t.Fatal("poll blocked foreground work")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("background read still active")
	}
	finish()
	if _, done := st.beginIdleRead(t.Context()); done != nil {
		t.Fatal("poll started during operation")
	}
	st.Idle()
	_, done := st.beginIdleRead(t.Context())
	if done == nil {
		t.Fatal("poll failed to resume")
	}
	done()
}
