package main

import (
	"context"
	"flag"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var noOpen bool

func main() {
	log.SetFlags(log.Ltime)

	const defaultAddr = "127.0.0.1:8910"
	addr := flag.String("addr", defaultAddr, "监听地址（只允许本机）")
	keyfile := flag.String("keys", defaultKeyfile(), "加密密钥文件")
	rpc := flag.String("rpc", "", "指定一个 RPC（留空用内置的三个，会自动切换）")
	contract := flag.String("contract", "", "启动就连上这个合约地址")
	idle := flag.Duration("idle-lock", 30*time.Minute, "空闲多久自动上锁，0 表示不自动")
	gwei := flag.Int64("gas-price", 12, "gas 价格（gwei）。12 是这条链的底价，拥堵时才需要调高")
	flag.BoolVar(&noOpen, "no-open", false, "不要自动打开浏览器")
	addKind := flag.String("add", "", "在终端里导号：master / member / derive（一条助记词派生一批）")
	flag.Parse()
	if err := initializeVaultLocation(*keyfile); err != nil {
		log.Fatal(err)
	}

	absKeys, err := filepath.Abs(*keyfile)
	if err != nil {
		log.Fatal(err)
	}
	*keyfile = absKeys
	if *addKind != "" {
		if err := runAdd(*addKind, *keyfile); err != nil {
			log.Fatalf("  %v", err)
		}
		return
	}

	releaseSystem, err := lockSystem(*keyfile)
	if err != nil {
		log.Fatal(err)
	}
	defer releaseSystem()

	rpcs := []string{
		"https://rpc.anubispace.org",
		"https://rpc1.anubispace.org",
		"https://rpc2.anubispace.org",
	}
	if *rpc != "" {
		rpcs = append([]string{*rpc}, rpcs...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chain := NewChain(rpcs, mainnetChainID)
	if *gwei < 12 {

		log.Fatalf("  gas 价格不能低于 12 gwei（这条链的底价），你给的是 %d", *gwei)
	}
	chain.SetGasPrice(new(big.Int).Mul(big.NewInt(*gwei), big.NewInt(1e9)))

	ks := NewKeystore(*keyfile, *idle)
	if err := ks.Load(); err != nil {
		log.Fatalf("  读密钥文件失败：%v", err)
	}
	st := NewState()

	st.Set(func(v *View) { v.GasPrice = chain.GasPrice().String() })
	run := NewRunner(chain, ks, st)
	batch := NewBatch(run, st)

	srv, err := NewServer(ks, st, run, batch, chain)
	if err != nil {
		log.Fatalf("  起服务失败：%v", err)
	}

	handler := srv.Handler()

	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			explicit = true
		}
	})
	listen := ListenAuto
	if explicit {
		listen = Listen
	}
	ln, err := listen(*addr)
	if err != nil {
		log.Fatalf("  %v", err)
	}

	log.Printf("  密钥文件  %s%s", *keyfile, existsNote(ks.Exists()))
	log.Printf("  环境      %s · chainId %d", Mainnet.Label, mainnetChainID)
	url := "http://" + ln.Addr().String() + "/?t=" + srv.Token()
	log.Printf("  用这个地址打开页面：")
	log.Printf("    %s", url)
	if !noOpen {
		go openBrowser(url)
	}

	srv.startContract = *contract

	go run.pollEvery(ctx, time.Second)
	go ks.AutoLockLoop(ctx, func(d time.Duration) {
		st.Logf("warn", "空闲 %s，已自动上锁 —— 私钥已从内存清除", d.Round(time.Minute))
	}, func() func() {
		if !st.TryBusy("自动锁定") {
			return nil
		}
		return st.Idle
	})

	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("  服务退出：%v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("  正在退出，清掉内存里的私钥")
	st.Close()
	batch.Stop()
	cancel()
	ks.Lock()
	sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer scancel()
	_ = httpSrv.Shutdown(sctx)
}

func existsNote(ok bool) string {
	if ok {
		return ""
	}
	return "（还不存在，导入第一个号时创建）"
}
