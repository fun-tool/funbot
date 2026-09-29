package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	gethkeystore "github.com/ethereum/go-ethereum/accounts/keystore"
)

const currentVaultVersion = 3
const vaultFormat = "fun-game-bot-keystore"

type keystorePayload struct {
	Format  string      `json:"format"`
	Version int         `json:"version"`
	Public  []VaultMeta `json:"public"`
	Data    []byte      `json:"data"`
}

func encryptKeystoreV3(plain []byte, password string, public []VaultMeta) (*Vault, error) {
	if len(password) < 8 {
		return nil, fmt.Errorf("密码至少 8 位")
	}

	payload, err := json.Marshal(keystorePayload{vaultFormat, currentVaultVersion, public, plain})
	if err != nil {
		return nil, err
	}
	defer wipe(payload)
	pw := []byte(password)
	defer wipe(pw)
	c, err := gethkeystore.EncryptDataV3(payload, pw, gethkeystore.StandardScryptN, gethkeystore.StandardScryptP)
	if err != nil {
		return nil, err
	}
	return &Vault{Format: vaultFormat, Version: currentVaultVersion, Crypto: &c, Public: public}, nil
}

func exactKDFInt(value any, want int) bool {
	switch n := value.(type) {
	case int:
		return n == want
	case float64:
		return n == float64(want)
	default:
		return false
	}
}

func validHex(s string, size int) bool {
	if len(s) != size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validateKeystoreV3(v *Vault) error {
	bad := fmt.Errorf("keystore 加密格式或参数损坏")
	if v.Format != vaultFormat || v.Crypto == nil || len(v.Salt) != 0 || len(v.Nonce) != 0 || len(v.Cipher) != 0 {
		return bad
	}
	c := v.Crypto
	if c.Cipher != "aes-128-ctr" || c.KDF != "scrypt" || !validHex(c.CipherParams.IV, 16) || !validHex(c.MAC, 32) {
		return bad
	}

	if len(c.KDFParams) != 5 || !exactKDFInt(c.KDFParams["n"], gethkeystore.StandardScryptN) ||
		!exactKDFInt(c.KDFParams["r"], 8) || !exactKDFInt(c.KDFParams["p"], gethkeystore.StandardScryptP) ||
		!exactKDFInt(c.KDFParams["dklen"], 32) {
		return bad
	}
	salt, ok := c.KDFParams["salt"].(string)
	if !ok || !validHex(salt, 32) || len(c.CipherText) < 2 || len(c.CipherText) > maxVaultBytes || len(c.CipherText)%2 != 0 {
		return bad
	}
	if _, err := hex.DecodeString(c.CipherText); err != nil {
		return bad
	}
	return nil
}

func decryptKeystoreV3(v *Vault, password string) ([]byte, error) {
	bad := fmt.Errorf("密码不对，或者文件被改动过")
	plain, err := gethkeystore.DecryptDataV3(*v.Crypto, password)
	if err != nil {
		return nil, bad
	}
	defer wipe(plain)
	var payload keystorePayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		wipe(payload.Data)
		return nil, bad
	}
	outer, _ := json.Marshal(v.Public)
	inner, _ := json.Marshal(payload.Public)
	if payload.Format != vaultFormat || payload.Version != currentVaultVersion || !bytes.Equal(outer, inner) || payload.Data == nil {
		wipe(payload.Data)
		return nil, bad
	}
	return payload.Data, nil
}

func (k *Keystore) backupLegacyVault() error {
	if k.vault == nil || k.vault.Version >= currentVaultVersion {
		return nil
	}
	body, err := readPrivateFile(k.path)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("%s.legacy-v%d.bak", k.path, k.vault.Version)
	existing, err := readPrivateFile(path)
	if err == nil {
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("旧版备份已存在且内容不同，未覆盖密钥文件：%s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := atomicPrivateWrite(path, body); err != nil {
		return fmt.Errorf("保存旧版加密备份失败，未迁移：%w", err)
	}
	return nil
}
