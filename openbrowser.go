package main

import (
	"log"
	"os/exec"
	"runtime"
	"time"
)

func openBrowser(url string) {

	time.Sleep(400 * time.Millisecond)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":

		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Run(); err != nil {
		log.Printf("  打不开浏览器：%v。请在终端用 -no-open 启动以获取登录链接", err)
	}
}
