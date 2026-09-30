package main

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const mainnetChainID int64 = 6714

var Mainnet = &Network{
	Label:    "主网",
	Arena:    common.HexToAddress("0xff060bAB41e16eBF0eFE4FAd454404Fd5f75aAaA"),
	Token:    common.HexToAddress("0xffFfBe730E2CdE54D8928D36F547e41DEb66Aaaa"),
	Lineage:  common.HexToAddress("0xB84B4ADf44DDa95fceA681A91Ea78dAD341DB395"),
	GasToken: common.HexToAddress("0x83fd06F0846d9D90B3016bF670Efe2E0B11cDe14"),
}

type Network struct {
	Label    string         `json:"label"`
	Arena    common.Address `json:"arena"`
	Token    common.Address `json:"token"`
	Lineage  common.Address `json:"lineage"`
	GasToken common.Address `json:"gasToken"`
}

type Line struct {
	At   string `json:"at"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type View struct {
	ChampionshipsError  string              `json:"championshipsError,omitempty"`
	MasterChampionships *string             `json:"masterChampionships"`
	SystemName          string              `json:"systemName"`
	DataDir             string              `json:"dataDir"`
	BalancesUpdated     string              `json:"balancesUpdated,omitempty"`
	BalanceError        string              `json:"balanceError,omitempty"`
	VaultPath           string              `json:"vaultPath"`
	Pending             *PendingTransaction `json:"pending,omitempty"`
	PendingError        string              `json:"pendingError,omitempty"`
	MnemonicSources     []MnemonicSource    `json:"mnemonicSources"`
	ImportCommands      map[string]string   `json:"importCommands"`
	Settings            Settings            `json:"settings"`
	Locked              bool                `json:"locked"`
	LockIn              int                 `json:"lockIn"`
	HasVault            bool                `json:"hasVault"`

	MasterReady     bool            `json:"masterReady"`
	Delegations     map[string]bool `json:"delegations"`
	DelegationError string          `json:"delegationError,omitempty"`
	Busy            string          `json:"busy"`
	Contract        string          `json:"contract"`
	Net             string          `json:"net"`
	Block           uint64          `json:"block"`
	Seats           []VaultMeta     `json:"seats"`
	Master          string          `json:"master"`
	Config          *Config         `json:"config"`
	Overview        *Overview       `json:"overview"`
	Rows            []*Pulse        `json:"rows"`
	Batch           Progress        `json:"batch"`
	Log             []Line          `json:"log"`
	GasPrice        string          `json:"gasPrice"`
}

type State struct {
	closing bool
	mu      sync.RWMutex
	v       View
	logs    []Line
}

func NewState() *State {
	return &State{v: View{Locked: true, Net: Mainnet.Label}}
}

func (s *State) Snapshot() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.v
	out.Log = make([]Line, len(s.logs))
	copy(out.Log, s.logs)
	out.Seats = append([]VaultMeta(nil), s.v.Seats...)
	out.Rows = append([]*Pulse(nil), s.v.Rows...)
	out.Delegations = maps.Clone(s.v.Delegations)
	return out
}

func (s *State) Set(fn func(v *View)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.v)
}

func (s *State) Logf(kind, format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := Line{At: time.Now().Format("15:04:05"), Kind: kind, Text: fmt.Sprintf(format, args...)}

	if len(s.logs) > 0 && s.logs[0].Text == line.Text {
		s.logs[0].At = line.At
		return
	}
	s.logs = append([]Line{line}, s.logs...)
	if len(s.logs) > 200 {
		s.logs = s.logs[:200]
	}
}

func (s *State) TryBusy(what string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.v.Busy != "" {
		return false
	}
	s.v.Busy = what
	return true
}

func (s *State) Idle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v.Busy = ""
}

func (s *State) Busy() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v.Busy
}

func (s *State) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closing = true
}
