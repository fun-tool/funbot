package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type Settings struct {
	EntryMode   string   `json:"entryMode"`
	ChainID     int64    `json:"chainId"`
	Arena       string   `json:"arena"`
	Master      string   `json:"master"`
	Contract    string   `json:"contract"`
	MasterPlays bool     `json:"masterPlays"`
	Rounds      int      `json:"rounds"`
	All         bool     `json:"all"`
	Selected    []string `json:"selected"`
}

type SettingsStore struct {
	mu    sync.Mutex
	path  string
	value Settings
}

func LoadSettings(path string, chainID int64) (*SettingsStore, error) {
	s := &SettingsStore{path: path, value: Settings{ChainID: chainID, Arena: Mainnet.Arena.Hex(), MasterPlays: true, Rounds: 12, All: true}}
	b, err := readPrivateFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var v Settings
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("设置文件损坏：%w", err)
	}

	if v.ChainID != chainID || v.Arena != Mainnet.Arena.Hex() {
		return s, nil
	}
	if err := validateSettings(v); err != nil {
		return nil, err
	}
	s.value = v
	return s, nil
}

func validateSettings(v Settings) error {
	if v.EntryMode != "" && v.EntryMode != "flexible" && v.EntryMode != "together" {
		return fmt.Errorf("入场方式无效")
	}
	if v.Rounds < 1 || v.Rounds > MaxMatchesPerDay {
		return fmt.Errorf("局数要在 1 到 12 之间")
	}
	for _, a := range append(append([]string{}, v.Selected...), v.Contract, v.Master) {
		if a != "" && (!common.IsHexAddress(a) || common.HexToAddress(a) == (common.Address{})) {
			return fmt.Errorf("设置里有无效地址")
		}
	}
	return nil
}

func (s *SettingsStore) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	v.Selected = append([]string{}, v.Selected...)
	return v
}

func (s *SettingsStore) Update(fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	v.Selected = append([]string{}, v.Selected...)
	fn(&v)
	if err := validateSettings(v); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicPrivateWrite(s.path, b); err != nil {
		return err
	}
	s.value = v
	return nil
}
