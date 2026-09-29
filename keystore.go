package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gethkeystore "github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	bip32 "github.com/tyler-smith/go-bip32"
	bip39 "github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/argon2"
)

type Seat struct {
	Index   int            `json:"index"`
	Kind    string         `json:"kind"`
	Source  string         `json:"source"`
	Label   string         `json:"label"`
	Address common.Address `json:"address"`
	keyMu   sync.RWMutex
	key     *ecdsa.PrivateKey
}

func (s *Seat) signTx(tx *types.Transaction, signer types.Signer) (*types.Transaction, error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if s.key == nil {
		return nil, fmt.Errorf("密钥已上锁")
	}
	return types.SignTx(tx, signer, s.key)
}
func (s *Seat) signAuthorization(a types.SetCodeAuthorization) (types.SetCodeAuthorization, error) {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	if s.key == nil {
		return types.SetCodeAuthorization{}, fmt.Errorf("密钥已上锁")
	}
	return types.SignSetCode(s.key, a)
}
func (s *Seat) clear() {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	wipeKey(s.key)
	s.key = nil
}

type Vault struct {
	Format  string                   `json:"format,omitempty"`
	Crypto  *gethkeystore.CryptoJSON `json:"crypto,omitempty"`
	Version int                      `json:"version"`
	Salt    []byte                   `json:"salt,omitempty"`
	Nonce   []byte                   `json:"nonce,omitempty"`
	Cipher  []byte                   `json:"cipher,omitempty"`
	Public  []VaultMeta              `json:"public"`
}

type VaultMeta struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Label   string `json:"label"`
	Address string `json:"address"`
}

type vaultSecret struct {
	Seats []secretSeat `json:"seats"`
}

type secretSeat struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Mnemonic string `json:"mnemonic,omitempty"`
	PrivKey  string `json:"privkey,omitempty"`
	Path     string `json:"path,omitempty"`
}

func (s secretSeat) source() string {
	if s.PrivKey != "" {
		return "privkey"
	}
	return "mnemonic"
}

func (s secretSeat) resolve() (*ecdsa.PrivateKey, common.Address, error) {
	if s.PrivKey != "" {
		return fromPrivKey(s.PrivKey)
	}
	return derive(s.Mnemonic, s.Path)
}

type Keystore struct {
	mu      sync.RWMutex
	path    string
	vault   *Vault
	seats   []*Seat
	sources []MnemonicSource
	locked  bool

	idle      time.Duration
	lastTouch time.Time
}

const defaultPath = "m/44'/60'/0'/0/0"

func NewKeystore(path string, idle time.Duration) *Keystore {
	return &Keystore{path: path, locked: true, idle: idle}
}

func (k *Keystore) Touch() {
	k.mu.Lock()
	k.lastTouch = time.Now()
	k.mu.Unlock()
}

func (k *Keystore) IdleFor() time.Duration {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.locked || k.lastTouch.IsZero() {
		return 0
	}
	return time.Since(k.lastTouch)
}

func (k *Keystore) IdleLimit() time.Duration { return k.idle }

func (k *Keystore) AutoLockLoop(ctx context.Context, onLock func(time.Duration), acquire ...func() func()) {
	if k.idle <= 0 {
		return
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var release func()
			if len(acquire) > 0 {
				release = acquire[0]()
				if release == nil {
					k.Touch()
					continue
				}
			}
			if d := k.IdleFor(); d > k.idle {
				k.Lock()
				if onLock != nil {
					onLock(d)
				}
			}
			if release != nil {
				release()
			}

		}
	}
}

func (k *Keystore) Path() string { return k.path }

func (k *Keystore) Exists() bool {
	_, err := os.Stat(k.path)
	return err == nil
}

func (k *Keystore) Locked() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.locked
}

func (k *Keystore) Meta() []VaultMeta {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.vault == nil {
		return nil
	}
	out := make([]VaultMeta, len(k.vault.Public))
	copy(out, k.vault.Public)
	return out
}

func (k *Keystore) Seats() []*Seat {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]*Seat, len(k.seats))
	copy(out, k.seats)
	return out
}

func (k *Keystore) Master() *Seat {
	for _, s := range k.Seats() {
		if s.Kind == "master" {
			return s
		}
	}
	return nil
}

func (k *Keystore) Members() []*Seat {
	var out []*Seat
	for _, s := range k.Seats() {
		if s.Kind == "member" {
			out = append(out, s)
		}
	}
	return out
}

func (k *Keystore) Find(addr common.Address) *Seat {
	for _, s := range k.Seats() {
		if s.Address == addr {
			return s
		}
	}
	return nil
}

func (k *Keystore) Load() error {
	if err := privateParent(k.path); err != nil {
		return err
	}
	body, err := readPrivateFile(k.path)
	if os.IsNotExist(err) {
		k.mu.Lock()
		k.vault = nil
		k.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	var v Vault
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("%s 不是合法的密钥文件: %w", k.path, err)
	}
	if err := validateVault(&v); err != nil {
		return err
	}
	k.mu.Lock()
	k.vault = &v
	k.mu.Unlock()
	return nil
}

func (k *Keystore) Unlock(password string) error {
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

	defer wipe(plain)

	var sec vaultSecret
	if err := json.Unmarshal(plain, &sec); err != nil {
		return fmt.Errorf("解密出来的内容不对（密码可能是对的，文件坏了）: %w", err)
	}
	seats, sources, err := resolveVault(&sec)
	if err != nil {
		return err
	}
	if k.vault.Version < currentVaultVersion {
		if err := k.persist(&sec, password); err != nil {
			wipeSeats(seats)
			return err
		}
	}
	wipeSeats(k.seats)
	k.seats, k.sources = seats, sources
	k.locked = false
	k.lastTouch = time.Now()
	return nil
}

func (k *Keystore) Lock() {
	k.mu.Lock()
	defer k.mu.Unlock()

	for _, s := range k.seats {
		s.clear()
	}
	k.seats = nil
	k.sources = nil
	k.locked = true
}

func wipeKey(key *ecdsa.PrivateKey) {
	if key == nil {
		return
	}
	if key.D != nil {
		bits := key.D.Bits()
		for i := range bits {
			bits[i] = 0
		}
		key.D.SetInt64(0)
	}
	key.X, key.Y = nil, nil
}

func (k *Keystore) reloadLocked() error {
	body, err := readPrivateFile(k.path)
	if err != nil {
		if os.IsNotExist(err) {
			k.vault = nil
			return nil
		}
		return err
	}
	var v Vault
	if err := json.Unmarshal(body, &v); err != nil {
		return fmt.Errorf("密钥文件读不懂: %w", err)
	}
	if err := validateVault(&v); err != nil {
		return err
	}
	k.vault = &v
	return nil
}

func (k *Keystore) AddSeat(password, kind, label, secret, path string) error {
	if kind != "master" && kind != "member" {
		return fmt.Errorf("kind 只能是 master 或 member")
	}
	entry, err := parseSecret(secret, path)
	if err != nil {
		return err
	}
	key, addr, err := entry.resolve()
	wipeKey(key)
	if err != nil {
		return err
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

	var sec vaultSecret
	if k.vault != nil {
		plain, err := decrypt(k.vault, password)
		if err != nil {
			return err
		}
		err = json.Unmarshal(plain, &sec)
		wipe(plain)
		if err != nil {
			return err
		}
	}
	for _, s := range sec.Seats {
		key, a, err := s.resolve()
		wipeKey(key)
		if err == nil && a == addr {
			return fmt.Errorf("这个号已经导入过了：%s", addr.Hex())
		}
		if kind == "master" && s.Kind == "master" {
			return fmt.Errorf("已经有主号了（%s）—— 先删掉再换", s.Label)
		}
	}
	entry.Index = len(sec.Seats)
	entry.Kind = kind
	entry.Label = label
	sec.Seats = append(sec.Seats, *entry)
	return k.persistAndRefresh(&sec, password)
}

func (k *Keystore) AddDerived(
	password, mnemonic string, start, count int, prefix string, firstIsMaster bool,
) (int, error) {
	mnemonic = strings.Join(strings.Fields(mnemonic), " ")
	if !bip39.IsMnemonicValid(mnemonic) {
		return 0, fmt.Errorf("助记词校验不过 —— 有单词拼错了或顺序不对")
	}
	if err := validateDerivation(start, count); err != nil {
		return 0, err
	}
	if prefix == "" {
		prefix = "队员"
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	release, err := lockVault(k.path)
	if err != nil {
		return 0, err
	}
	defer release()

	if err := k.reloadLocked(); err != nil {
		return 0, err
	}

	var sec vaultSecret
	if k.vault != nil {
		plain, err := decrypt(k.vault, password)
		if err != nil {
			return 0, err
		}
		err = json.Unmarshal(plain, &sec)
		wipe(plain)
		if err != nil {
			return 0, err
		}
	}

	have := map[common.Address]bool{}
	for _, e := range sec.Seats {
		key, a, err := e.resolve()
		wipeKey(key)
		if err == nil {
			have[a] = true
		}
	}

	if firstIsMaster {
		for _, e := range sec.Seats {
			if e.Kind == "master" {
				return 0, fmt.Errorf("已经有主号了（%s）—— 要换主号先把它删掉", e.Label)
			}
		}
	}

	added := 0
	for i := start; i < start+count; i++ {
		path := fmt.Sprintf("m/44'/60'/0'/0/%d", i)
		key, addr, err := derive(mnemonic, path)
		wipeKey(key)
		if err != nil {
			return added, fmt.Errorf("派生 %s 失败: %w", path, err)
		}
		if have[addr] {
			continue
		}
		have[addr] = true
		kind, label := "member", fmt.Sprintf("%s %d", prefix, i)
		if firstIsMaster && i == start {
			kind, label = "master", fmt.Sprintf("主号 %d", i)
		}
		sec.Seats = append(sec.Seats, secretSeat{
			Index: len(sec.Seats),
			Kind:  kind,
			Label: label,

			Mnemonic: mnemonic,
			Path:     path,
		})
		added++
	}
	if added == 0 {
		return 0, fmt.Errorf("这 %d 个号都已经导入过了", count)
	}
	if err := k.persist(&sec, password); err != nil {
		return 0, err
	}

	if !k.locked {
		wipeSeats(k.seats)
		k.locked = true
		k.seats = nil
		k.sources = nil
	}
	return added, nil
}

func parseSecret(in, path string) (*secretSeat, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return nil, fmt.Errorf("内容是空的")
	}
	hex := strings.TrimPrefix(strings.TrimPrefix(in, "0x"), "0X")
	if len(hex) == 64 && isHex(hex) {
		key, _, err := fromPrivKey(hex)
		wipeKey(key)
		if err != nil {
			return nil, err
		}
		return &secretSeat{PrivKey: hex}, nil
	}

	words := strings.Join(strings.Fields(in), " ")
	n := len(strings.Fields(words))
	if !bip39.IsMnemonicValid(words) {
		if n == 12 || n == 15 || n == 18 || n == 21 || n == 24 {

			return nil, fmt.Errorf("%d 个单词，但校验位不对 —— 有单词拼错了或顺序不对", n)
		}
		return nil, fmt.Errorf("既不是私钥（64 位十六进制），也不是合法助记词（现在是 %d 个词）", n)
	}
	if path == "" {
		path = defaultPath
	}
	return &secretSeat{Mnemonic: words, Path: path}, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func fromPrivKey(h string) (*ecdsa.PrivateKey, common.Address, error) {
	h = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(h), "0x"), "0X")
	key, err := crypto.HexToECDSA(h)
	if err != nil {

		return nil, common.Address{}, fmt.Errorf("私钥不合法: %w", err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func (k *Keystore) RemoveSeat(password string, addr common.Address) error {
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
	uerr := json.Unmarshal(plain, &sec)
	wipe(plain)
	if uerr != nil {
		return uerr
	}
	kept := sec.Seats[:0]
	found := false
	for _, s := range sec.Seats {
		key, a, err := s.resolve()
		wipeKey(key)
		if err == nil && a == addr {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return fmt.Errorf("没找到 %s", addr.Hex())
	}
	for i := range kept {
		kept[i].Index = i
	}
	sec.Seats = kept
	return k.persistAndRefresh(&sec, password)
}

func (k *Keystore) persistAndRefresh(sec *vaultSecret, password string) error {
	var seats []*Seat
	var sources []MnemonicSource
	if !k.locked {
		var err error
		seats, sources, err = resolveVault(sec)
		if err != nil {
			return err
		}
	}
	if err := k.persist(sec, password); err != nil {
		wipeSeats(seats)
		return err
	}
	if !k.locked {
		wipeSeats(k.seats)
		k.seats, k.sources = seats, sources
	}
	return nil
}

func (k *Keystore) persist(sec *vaultSecret, password string) error {
	plain, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	defer wipe(plain)
	v := &Vault{}
	v.Public = make([]VaultMeta, 0, len(sec.Seats))
	for _, s := range sec.Seats {
		key, a, err := s.resolve()
		wipeKey(key)
		if err != nil {
			return err
		}
		v.Public = append(v.Public, VaultMeta{
			Index: s.Index, Kind: s.Kind, Source: s.source(), Label: s.Label, Address: a.Hex(),
		})
	}
	v, err = encryptKeystoreV3(plain, password, v.Public)
	if err != nil {
		return err
	}
	if err := validateVault(v); err != nil {
		return err
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(body) > maxVaultBytes {
		return fmt.Errorf("密钥文件过大，未保存")
	}
	if err := k.backupLegacyVault(); err != nil {
		return err
	}
	if err := atomicPrivateWrite(k.path, body); err != nil {
		return err
	}
	k.vault = v
	return nil
}

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func deriveKey(password string, salt []byte) []byte {
	pw := []byte(password)
	defer wipe(pw)
	return argon2.IDKey(pw, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

func (v *Vault) aad() []byte {
	if v.Version == 1 {
		return nil
	}
	b, _ := json.Marshal(struct {
		Version int         `json:"version"`
		Public  []VaultMeta `json:"public"`
	}{v.Version, v.Public})
	return b
}
func validateVault(v *Vault) error {
	if v == nil || (v.Version != 1 && v.Version != 2 && v.Version != currentVaultVersion) {
		return fmt.Errorf("不支持的密钥文件版本")
	}
	if v.Version == currentVaultVersion {
		if err := validateKeystoreV3(v); err != nil {
			return err
		}
	} else if v.Format != "" || v.Crypto != nil || len(v.Salt) != 16 || len(v.Nonce) != 12 || len(v.Cipher) < 16 || len(v.Cipher) > maxVaultBytes {
		return fmt.Errorf("密钥文件加密参数损坏")
	}
	seen := map[common.Address]bool{}
	for i, m := range v.Public {
		if m.Index != i || (m.Kind != "master" && m.Kind != "member") || (m.Source != "mnemonic" && m.Source != "privkey") || !common.IsHexAddress(m.Address) || len(m.Label) > 1024 {
			return fmt.Errorf("密钥文件公开信息损坏")
		}
		a := common.HexToAddress(m.Address)
		if seen[a] {
			return fmt.Errorf("密钥文件地址重复")
		}
		seen[a] = true
	}
	return nil
}
func decrypt(v *Vault, password string) ([]byte, error) {
	if err := validateVault(v); err != nil {
		return nil, err
	}
	if v.Version == currentVaultVersion {
		return decryptKeystoreV3(v, password)
	}
	key := deriveKey(password, v.Salt)
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, v.Nonce, v.Cipher, v.aad())
	if err != nil {

		return nil, fmt.Errorf("密码不对，或者文件被改动过")
	}
	return plain, nil
}

func derive(mnemonic, path string) (*ecdsa.PrivateKey, common.Address, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, common.Address{}, fmt.Errorf("助记词校验不过")
	}
	if path == "" {
		path = defaultPath
	}
	seed := bip39.NewSeed(mnemonic, "")
	defer wipe(seed)
	cur, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, common.Address{}, err
	}
	defer func() {
		if cur != nil {
			wipe(cur.Key)
			wipe(cur.ChainCode)
		}
	}()
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "m" {
		return nil, common.Address{}, fmt.Errorf("派生路径须以 m/ 开始")
	}
	for _, part := range parts[1:] {
		hardened := strings.HasSuffix(part, "'") || strings.HasSuffix(part, "h")
		num := part
		if hardened {
			num = part[:len(part)-1]
		}
		n, err := strconv.ParseUint(num, 10, 31)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("路径 %q 里的 %q 无效", path, part)
		}
		idx := uint32(n)
		if hardened {
			idx += bip32.FirstHardenedChild
		}
		prev := cur
		cur, err = cur.NewChildKey(idx)
		wipe(prev.Key)
		wipe(prev.ChainCode)
		if err != nil {
			return nil, common.Address{}, err
		}
	}
	key, err := crypto.ToECDSA(cur.Key)
	if err != nil {
		return nil, common.Address{}, err
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}
