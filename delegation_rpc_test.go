package main

import (
	"context"
	"fmt"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

type witnessFixture struct {
	height int64
	fork   int64
	chain  hexutil.Uint64
	code   []byte
	target common.Address
	slow   bool
}

func (f *witnessFixture) ChainId() hexutil.Uint64 { return f.chain }
func (f *witnessFixture) GetBlockByNumber(ctx context.Context, _ string, _ bool) (map[string]any, error) {
	if f.slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return map[string]any{"number": hexutil.Uint64(f.height), "hash": common.BigToHash(big.NewInt(f.fork + f.height))}, nil
}
func (f *witnessFixture) GetCode(addr common.Address, block rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	if addr != f.target || block.BlockHash == nil || !block.RequireCanonical {
		return nil, fmt.Errorf("unexpected block or address")
	}
	height := block.BlockHash.Big().Int64() - f.fork
	if height < 1 || height > f.height {
		return nil, fmt.Errorf("not a canonical block")
	}
	return f.code, nil
}

func TestDelegationRPCQuorum(t *testing.T) {
	master, impl := common.HexToAddress("0x10"), common.HexToAddress("0x20")
	cfg := &Config{Impl: impl, Master: master, Shifu: master, Arena: Mainnet.Arena, Token: Mainnet.Token, Lineage: Mainnet.Lineage, GasToken: Mainnet.GasToken}
	for _, tc := range []struct {
		name  string
		nodes [3]string
		want  bool
	}{
		{"all agree", [3]string{}, true},
		{"same height", [3]string{"same", "same", "offline"}, true},
		{"one offline", [3]string{"", "offline", ""}, true},
		{"one stalled", [3]string{"slow", "", ""}, true},
		{"one wrong code", [3]string{"code", "", ""}, true},
		{"one wrong chain", [3]string{"", "", "chain"}, true},
		{"lowest node forked", [3]string{"", "fork", ""}, true},
		{"only one reachable", [3]string{"offline", "", "offline"}, false},
		{"insufficient valid code", [3]string{"code", "code", ""}, false},
		{"insufficient correct chain", [3]string{"chain", "", "chain"}, false},
		{"no common block", [3]string{"", "fork", "offline"}, false},
		{"same height different hashes", [3]string{"same", "same-fork", "offline"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var endpoints [3]string
			for i, height := range []int64{103, 101, 102} {
				f := &witnessFixture{height: height, chain: 123, code: trustedRuntime(cfg), target: impl}
				switch tc.nodes[i] {
				case "chain":
					f.chain++
				case "code":
					f.code[len(f.code)-1] ^= 1
				case "fork":
					f.fork = 1000
				case "same":
					f.height = 103
				case "same-fork":
					f.height, f.fork = 103, 1000
				case "slow":
					f.slow = true
				}
				api := rpc.NewServer()
				t.Cleanup(api.Stop)
				if err := api.RegisterName("eth", f); err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(api)
				t.Cleanup(server.Close)
				endpoints[i] = server.URL
				if tc.nodes[i] == "offline" {
					server.Close()
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := verifyDelegationTarget(ctx, endpoints, 123, cfg, impl, master)
			if (err == nil) != tc.want {
				t.Fatalf("want success=%t, got %v", tc.want, err)
			}
		})
	}
}
