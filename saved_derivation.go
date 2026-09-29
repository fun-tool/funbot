package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type DerivedWallet struct {
	Address string `json:"address"`
	Label   string `json:"label"`
	Path    string `json:"path"`
}

type MnemonicSource struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	NextIndex int             `json:"nextIndex"`
	Wallets   []DerivedWallet `json:"wallets"`
}

type DeriveSavedRequest struct {
	Source   string `json:"source"`
	Start    int    `json:"start"`
	Count    int    `json:"count"`
	Label    string `json:"label"`
	Password string `json:"password"`
}

func validateDerivation(start, count int) error {
	if count < 1 || count > 500 {
		return fmt.Errorf("数量须为 1 到 500")
	}
	if start < 0 {
		return fmt.Errorf("起始序号不能是负数")
	}
	if uint64(start)+uint64(count) > 1<<31 {
		return fmt.Errorf("派生序号须在 0–2147483647 内")
	}
	return nil
}

func wipeSeats(seats []*Seat) {
	for _, seat := range seats {
		seat.clear()
	}
}

func resolveVault(sec *vaultSecret) ([]*Seat, []MnemonicSource, error) {
	var seats []*Seat
	var sources []MnemonicSource
	groups := map[string]int{}
	for _, e := range sec.Seats {
		key, addr, err := e.resolve()
		if err != nil {
			wipeSeats(seats)
			return nil, nil, fmt.Errorf("第 %d 个号解析失败", e.Index)
		}
		seats = append(seats, &Seat{Index: e.Index, Kind: e.Kind, Source: e.source(), Label: e.Label, Address: addr, key: key})
		if e.Mnemonic == "" {
			continue
		}
		mnemonic := strings.Join(strings.Fields(e.Mnemonic), " ")
		i, ok := groups[mnemonic]
		if !ok {
			i = len(sources)
			groups[mnemonic] = i
			sources = append(sources, MnemonicSource{ID: addr.Hex(), Label: e.Label})
		}
		path := e.Path
		if path == "" {
			path = defaultPath
		}
		sources[i].Wallets = append(sources[i].Wallets, DerivedWallet{Address: addr.Hex(), Label: e.Label, Path: path})
		if suffix, ok := strings.CutPrefix(path, "m/44'/60'/0'/0/"); ok {
			if n, err := strconv.Atoi(suffix); err == nil && n >= 0 && n < 1<<31 {
				sources[i].NextIndex = max(sources[i].NextIndex, n+1)
			}
		}
	}
	return seats, sources, nil
}

func (k *Keystore) MnemonicSources() []MnemonicSource {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.locked {
		return nil
	}
	out := append([]MnemonicSource{}, k.sources...)
	for i := range out {
		out[i].Wallets = append([]DerivedWallet{}, out[i].Wallets...)
	}
	return out
}

func (k *Keystore) DeriveSaved(req DeriveSavedRequest) (int, error) {
	if err := validateDerivation(req.Start, req.Count); err != nil {
		return 0, err
	}
	if !common.IsHexAddress(req.Source) {
		return 0, fmt.Errorf("请选择已导入的助记词组")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	release, err := lockVault(k.path)
	if err != nil {
		return 0, err
	}
	defer release()
	if k.locked {
		return 0, fmt.Errorf("请先解锁密钥文件")
	}
	if err := k.reloadLocked(); err != nil {
		return 0, err
	}
	if k.vault == nil {
		return 0, fmt.Errorf("请先在终端导入账号")
	}
	plain, err := decrypt(k.vault, req.Password)
	if err != nil {
		return 0, err
	}
	defer wipe(plain)
	var sec vaultSecret
	if err := json.Unmarshal(plain, &sec); err != nil {
		return 0, fmt.Errorf("密钥文件内容损坏")
	}
	var mnemonic string
	have := map[common.Address]bool{}
	for _, e := range sec.Seats {
		key, a, err := e.resolve()
		wipeKey(key)
		if err != nil {
			return 0, fmt.Errorf("已有账号解析失败")
		}
		have[a] = true
		if strings.EqualFold(a.Hex(), req.Source) {
			mnemonic = e.Mnemonic
		}
	}
	if mnemonic == "" {
		return 0, fmt.Errorf("此账号没有已保存的助记词，私钥账号不能派生")
	}
	prefix := strings.TrimSpace(req.Label)
	if prefix == "" {
		prefix = "派生钱包"
	}
	added := 0
	for i := req.Start; i < req.Start+req.Count; i++ {
		path := fmt.Sprintf("m/44'/60'/0'/0/%d", i)
		key, a, err := derive(mnemonic, path)
		wipeKey(key)
		if err != nil {
			return 0, fmt.Errorf("派生序号 %d 失败", i)
		}
		if have[a] {
			continue
		}
		sec.Seats = append(sec.Seats, secretSeat{Index: len(sec.Seats), Kind: "member", Label: fmt.Sprintf("%s %d", prefix, i), Mnemonic: mnemonic, Path: path})
		have[a] = true
		added++
	}
	if added == 0 {
		return 0, nil
	}
	seats, sources, err := resolveVault(&sec)
	if err != nil {
		return 0, err
	}
	if err := k.persist(&sec, req.Password); err != nil {
		wipeSeats(seats)
		return 0, err
	}
	wipeSeats(k.seats)
	k.seats, k.sources = seats, sources
	k.lastTouch = time.Now()
	return added, nil
}
