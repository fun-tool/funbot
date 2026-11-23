package main

import (
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMandateSnapshotUsesBatchedPagesAtOneBlock(t *testing.T) {
	master := common.HexToAddress("0x100")
	member := common.HexToAddress("0x200")
	sq, err := NewSquad(&Chain{}, common.HexToAddress("0x300"), master)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []struct {
			ID     int
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
			t.Error(err)
			return
		}
		sizes = append(sizes, len(reqs))
		replies := make([]map[string]any, 0, len(reqs))
		for _, req := range reqs {
			var call struct {
				To   common.Address
				Data hexutil.Bytes
			}
			if req.Method != "eth_call" || string(req.Params[1]) != `"0xabc"` {
				t.Error("unexpected method or snapshot block")
				return
			}
			if err := json.Unmarshal(req.Params[0], &call); err != nil {
				t.Error(err)
				return
			}
			if call.To != sq.impl {
				t.Error("reader must use trusted implementation")
				return
			}
			method, err := sq.abi.MethodById(call.Data)
			if err != nil {
				t.Error(err)
				return
			}
			args, err := method.Inputs.Unpack(call.Data[4:])
			if err != nil {
				t.Error(err)
				return
			}
			var out []byte
			switch method.Name {
			case "mandatePage":
				mask, total, left, awaiting, held := int64(0), int64(0), int64(61), int64(0), int64(0)
				if args[0].(common.Address) == master {
					mask, total, left, held = 1, 65, 8, 33
					if args[1].(*big.Int).Uint64() == 64 {
						left, awaiting = 3, 1
					}
				}
				out, err = method.Outputs.Pack(big.NewInt(mask), big.NewInt(total), big.NewInt(left), big.NewInt(awaiting), big.NewInt(held))
			case "mandateRewardsPage":
				length := 32
				if args[0].(*big.Int).Sign() != 0 {
					length = 1
				}
				ids := make([]*big.Int, length)
				for i := range ids {
					ids[i] = new(big.Int)
				}
				ids[0] = new(big.Int).Add(args[0].(*big.Int), big.NewInt(1))
				out, err = method.Outputs.Pack(big.NewInt(2), big.NewInt(1234), big.NewInt(33), ids, new(big.Int).Add(oneToken, big.NewInt(1)))
			default:
				t.Error(method.Name)
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			replies = append(replies, map[string]any{"id": req.ID, "jsonrpc": "2.0", "result": hexutil.Encode(out)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(replies)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	cl, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sq.chain = &Chain{rpcs: []string{server.URL}, c: cl}
	snapshot, err := sq.readMandates(t.Context(), "0xabc", []common.Address{master, member}, true)
	if err != nil {
		t.Fatal(err)
	}
	a := snapshot.Accounts[master]
	if a.Available != 2 || a.Held != 33 || a.Remaining != 3 || a.AwaitingClaim != 1 || fmt.Sprint(a.indices) != "[0 64]" {
		t.Fatalf("incorrect master data: %+v", a)
	}
	if snapshot.Accounts[member].Held != 0 || snapshot.Accounts[member].Available != 0 {
		t.Fatal("foreign holdings included")
	}
	rewards := snapshot.Rewards
	if fmt.Sprint(rewards.ids) != "[1 33]" || rewards.Count != 2 || rewards.Amount != "2.000000000000000002" {
		t.Fatalf("incorrect claim: %+v", rewards)
	}
	if fmt.Sprint(sizes) != "[3 2]" {
		t.Fatalf("unexpected HTTP batch sizes: %v", sizes)
	}
}

func TestTrustedSquadCode(t *testing.T) {
	impl := common.HexToAddress("0x100")
	master := common.HexToAddress("0x200")
	cfg := &Config{Impl: impl, Master: master, Shifu: master, Arena: Mainnet.Arena, Token: Mainnet.Token, Lineage: Mainnet.Lineage, GasToken: Mainnet.GasToken}
	code := trustedRuntime(cfg)
	if err := verifySquadCode(code, cfg, impl, master); err != nil {
		t.Fatal(err)
	}
	code[len(code)-1] ^= 1
	if verifySquadCode(code, cfg, impl, master) == nil {
		t.Fatal("modified runtime accepted")
	}
}

func TestMandateConfirmationGroups(t *testing.T) {
	preview := &MandateSnapshot{Accounts: make(map[common.Address]*MandateAccount)}
	current := &MandateSnapshot{Accounts: make(map[common.Address]*MandateAccount)}
	var addresses []common.Address
	for i := int64(1); i <= 49; i++ {
		addr := common.BigToAddress(big.NewInt(i))
		addresses = append(addresses, addr)
		preview.Accounts[addr] = &MandateAccount{Held: 1, indices: []*big.Int{big.NewInt(i)}}
		current.Accounts[addr] = &MandateAccount{Held: 1, indices: []*big.Int{big.NewInt(i)}}
		preview.Rewards.ids = append(preview.Rewards.ids, big.NewInt(i))
		current.Rewards.ids = append(current.Rewards.ids, big.NewInt(i))
	}
	if err := preview.checkGroup(current, addresses[:48], common.Address{}, false); err != nil {
		t.Fatal(err)
	}
	if err := preview.checkGroup(current, addresses, common.Address{}, false); err == nil {
		t.Fatal("accepted 49 addresses")
	}
	current.Accounts[addresses[0]].Held++
	if err := preview.checkGroup(current, addresses[:1], common.Address{}, false); err == nil {
		t.Fatal("accepted changed holdings")
	}
	preview.Rewards.Count, current.Rewards.Count = 49, 49
	preview.Rewards.Period, current.Rewards.Period = 2, 2
	if err := preview.checkGroup(current, nil, common.Address{}, true); err != nil {
		t.Fatal(err)
	}
	current.Rewards.Period++
	if err := preview.checkGroup(current, nil, common.Address{}, true); err == nil {
		t.Fatal("accepted different reward period")
	}
	current.Rewards.Period--
	current.Rewards.ids[47] = big.NewInt(99)
	if err := preview.checkGroup(current, nil, common.Address{}, true); err == nil {
		t.Fatal("accepted different NFT at group boundary")
	}
}
