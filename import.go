package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"
)

var stdinReader = bufio.NewReader(os.Stdin)

func askPassword(isNew bool) (string, error) {
	if isNew {
		fmt.Print("  给密钥文件设一个密码（至少 8 位）: ")
	} else {
		fmt.Print("  密钥文件密码: ")
	}
	pw, err := readHidden()
	if err != nil {
		return "", err
	}
	if isNew {
		fmt.Print("  再输一次: ")
		again, err := readHidden()
		if err != nil {
			return "", err
		}
		if pw != again {
			return "", fmt.Errorf("两次输入不一致")
		}
	}
	return pw, nil
}

func runAdd(kind, keyfile string) error {
	if kind == "derive" {
		return runDerive(keyfile)
	}
	if kind != "master" && kind != "member" {
		return fmt.Errorf("-add 只能是 master、member 或 derive")
	}
	ks := NewKeystore(keyfile, 0)
	if err := ks.Load(); err != nil {
		return err
	}
	pw, err := askPassword(!ks.Exists())
	if err != nil {
		return err
	}

	added := 0
	for {
		fmt.Printf("\n  助记词或私钥（直接回车结束）: ")
		secret, err := readHidden()
		if err != nil {
			return err
		}
		if strings.TrimSpace(secret) == "" {
			break
		}

		fmt.Println("  " + secretInputSummary(secret))

		entry, err := parseSecret(secret, "")
		if err != nil {
			fmt.Printf("  ✗ %v\n", err)
			continue
		}
		key, addr, err := entry.resolve()
		wipeKey(key)
		if err != nil {
			fmt.Printf("  ✗ %v\n", err)
			continue
		}
		fmt.Printf("  → 地址 %s\n", addr.Hex())
		fmt.Print("  对吗？(y/n): ")
		yn, _ := stdinReader.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(yn)), "y") {
			fmt.Println("  跳过")
			continue
		}

		fmt.Print("  备注: ")
		label, _ := stdinReader.ReadString('\n')
		label = strings.TrimSpace(label)
		if label == "" {
			label = addr.Hex()[:10]
		}

		if err := ks.AddSeat(pw, kind, label, secret, ""); err != nil {
			fmt.Printf("  ✗ %v\n", err)
			continue
		}
		fmt.Printf("  ✓ 已加入：%s %s\n", kind, label)
		added++
		if kind == "master" {
			break
		}
	}
	fmt.Printf("\n  共加入 %d 个 → %s\n", added, keyfile)
	return nil
}

func readHidden() (string, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return "", fmt.Errorf("需要交互式终端，无法保证隐藏输入，已停止；请在终端直接运行命令")
	}
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		wipe(b)
		return "", err
	}
	defer wipe(b)
	return strings.TrimSpace(string(b)), nil
}

func secretInputSummary(secret string) string {
	in := strings.TrimSpace(secret)
	if in == "" {
		return "未输入内容"
	}
	fields := strings.Fields(in)
	hex := strings.TrimPrefix(strings.TrimPrefix(in, "0x"), "0X")
	if len(fields) == 1 && (hex != in || isHex(hex) || utf8.RuneCountInString(in) >= 32) {
		return fmt.Sprintf("已输入私钥：%d 位（不含 0x，内容不显示）", utf8.RuneCountInString(hex))
	}
	return fmt.Sprintf("已输入助记词：%d 个单词（内容不显示）", len(fields))
}

func terminalImportCommands(keyfile string) map[string]string {
	exe, err := os.Executable()
	if err != nil {
		exe = "funbot"
	}
	if abs, err := filepath.Abs(keyfile); err == nil {
		keyfile = abs
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	prefix := ""
	if runtime.GOOS == "windows" {
		prefix = "& "
		quote = func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	}
	name := "funbot"
	out := map[string]string{
		"macStart":     "./" + name,
		"windowsStart": ".\\" + name,
		"macVault":     "程序所在文件夹/.data/keys.json（go run 使用项目文件夹）",
		"windowsVault": "程序所在文件夹\\.data\\keys.json（go run 使用项目文件夹）",
	}
	for _, kind := range []string{"master", "member"} {
		out["mac"+strings.ToUpper(kind[:1])+kind[1:]] = "./" + name + " -add " + kind
		out["windows"+strings.ToUpper(kind[:1])+kind[1:]] = ".\\" + name + " -add " + kind
		out[kind] = prefix + quote(exe) + " -keys " + quote(keyfile) + " -add " + kind
	}
	return out
}

func runDerive(keyfile string) error {
	ks := NewKeystore(keyfile, 0)
	if err := ks.Load(); err != nil {
		return err
	}
	pw, err := askPassword(!ks.Exists())
	if err != nil {
		return err
	}

	fmt.Print("\n  助记词（不回显）: ")
	mnemonic, err := readHidden()
	if err != nil {
		return err
	}
	if strings.TrimSpace(mnemonic) == "" {
		return fmt.Errorf("助记词不能为空")
	}

	fmt.Println("  " + secretInputSummary(mnemonic))

	start := askInt("  从第几号开始", 0)
	count := askInt("  派生几个", 24)
	if err := validateDerivation(start, count); err != nil {
		return err
	}

	firstIsMaster := false
	if !hasMaster(ks) {
		fmt.Printf("  第 %d 号同时当主号吗？（它出钱发指令，也一起上擂台）(y/n) [y]: ", start)
		yn, _ := stdinReader.ReadString('\n')
		firstIsMaster = !strings.HasPrefix(strings.ToLower(strings.TrimSpace(yn)), "n")
	}

	fmt.Printf("\n  这一批的地址：\n")
	for i := start; i < start+count && i < start+3; i++ {
		key, addr, derr := derive(strings.Join(strings.Fields(mnemonic), " "),
			fmt.Sprintf("m/44'/60'/0'/0/%d", i))
		wipeKey(key)
		if derr != nil {
			return derr
		}
		fmt.Printf("    %d  %s\n", i, addr.Hex())
	}
	if count > 3 {
		fmt.Printf("    …… 一共 %d 个\n", count)
	}
	fmt.Print("\n  对吗？(y/n): ")
	yn, _ := stdinReader.ReadString('\n')
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(yn)), "y") {
		fmt.Println("  取消了")
		return nil
	}

	n, err := ks.AddDerived(pw, mnemonic, start, count, "队员", firstIsMaster)
	if err != nil {
		return err
	}
	fmt.Printf("\n  导入 %d 个 → %s\n", n, keyfile)
	return nil
}

func hasMaster(ks *Keystore) bool {
	for _, m := range ks.Meta() {
		if m.Kind == "master" {
			return true
		}
	}
	return false
}

func askInt(prompt string, def int) int {
	fmt.Printf("%s [%d]: ", prompt, def)
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		fmt.Printf("  「%s」不是数字，用 %d\n", line, def)
		return def
	}
	return n
}
