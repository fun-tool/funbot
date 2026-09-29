package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type Server struct {
	ks            *Keystore
	st            *State
	run           *Runner
	batch         *Batch
	chain         *Chain
	token         string
	settings      *SettingsStore
	startContract string
}

func NewServer(ks *Keystore, st *State, run *Runner, b *Batch, c *Chain) (*Server, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	settings, err := LoadSettings(ks.Path()+".settings.json", c.chainID)
	if err != nil {
		return nil, err
	}
	c.journalPath = ks.Path() + ".pending.json"
	run.settings = settings
	return &Server{settings: settings, ks: ks, st: st, run: run, batch: b, chain: c, token: hex.EncodeToString(buf)}, nil
}

func (s *Server) Token() string { return s.token }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/transaction/recover", s.guard(s.handleRecoverTransaction, true))
	mux.HandleFunc("/api/settings", s.guard(s.handleSettings, true))
	mux.HandleFunc("/api/derive", s.guard(s.handleDeriveSaved, true))
	mux.HandleFunc("/api/vault/reload", s.guard(s.handleVaultReload, true))
	mux.HandleFunc("/api/", http.NotFound)
	mux.HandleFunc("/api/state", s.guard(s.handleState, false))
	mux.HandleFunc("/api/unlock", s.guard(s.handleUnlock, true))
	mux.HandleFunc("/api/lock", s.guard(s.handleLock, true))
	mux.HandleFunc("/api/seat/rename", s.guard(s.handleSeatRename, true))
	mux.HandleFunc("/api/mentors", s.guard(s.handleMentors, false))
	mux.HandleFunc("/api/seat/remove", s.guard(s.handleSeatRemove, true))
	mux.HandleFunc("/api/deploy", s.guard(s.handleDeploy, true))
	mux.HandleFunc("/api/attach", s.guard(s.handleAttach, true))
	mux.HandleFunc("/api/delegate", s.guard(s.handleDelegate, true))
	mux.HandleFunc("/api/delegate/revoke", s.guard(s.handleRevokeDelegation, true))
	mux.HandleFunc("/api/roster/add", s.guard(s.handleRosterAdd, true))
	mux.HandleFunc("/api/roster/remove", s.guard(s.handleRosterRemove, true))
	mux.HandleFunc("/api/prepare", s.guard(s.handlePrepare, true))
	mux.HandleFunc("/api/play", s.guard(s.handlePlay, true))
	mux.HandleFunc("/api/harvest", s.guard(s.handleHarvest, true))
	mux.HandleFunc("/api/fund", s.guard(s.handleFund, true))
	mux.HandleFunc("/api/sweepgas", s.guard(s.handleSweepGas, true))
	mux.HandleFunc("/api/batch/start", s.guard(s.handleBatchStart, true))
	mux.HandleFunc("/api/batch/stop", s.guard(s.handleBatchStop, true))
	mux.HandleFunc("/api/batch/harvest", s.guard(s.handleBatchHarvest, true))
	mux.Handle("/", uiHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) guard(h http.HandlerFunc, write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method := http.MethodGet
		if write {
			method = http.MethodPost
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			s.fail(w, 405, fmt.Errorf("只接受 %s", method))
			return
		}

		if !localHost(r.Host) {
			s.fail(w, 403, fmt.Errorf("只接受本机访问，收到的 Host 是 %q", r.Host))
			return
		}

		if o := r.Header.Get("Origin"); o != "" && !s.sameOrigin(o, r.Host) {
			s.fail(w, 403, fmt.Errorf("跨站请求被拒绝"))
			return
		}
		tok := r.Header.Get("X-Token")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			s.fail(w, 401, fmt.Errorf("访问令牌不对 —— 用启动时打印的那个地址打开页面"))
			return
		}
		h(w, r)
	}
}

func localHost(host string) bool {
	h := host
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		h = host[:i]
	}
	h = strings.Trim(h, "[]")
	switch h {
	case "127.0.0.1", "localhost", "::1", "":
		return true
	}
	return false
}

func (s *Server) sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == "http" && u.Host == host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	s.writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) ok(w http.ResponseWriter) {
	s.writeJSON(w, 200, s.snapshot())
}

func (s *Server) snapshot() View {
	v := s.st.Snapshot()
	v.VaultPath = s.ks.Path()
	v.DataDir = filepath.Dir(v.VaultPath)
	v.SystemName = filepath.Base(v.DataDir)
	if v.SystemName == ".data" {
		v.SystemName = filepath.Base(filepath.Dir(v.DataDir))
	}
	var err error
	v.Pending, err = s.chain.pending()
	if err != nil {
		v.PendingError = err.Error()
	}
	if v.Pending != nil {
		v.Pending.Raw = nil
		if v.Pending.Resolved {
			v.Pending = nil
		}
	}
	v.Settings = s.settings.Get()
	v.Locked = s.ks.Locked()
	v.HasVault = s.ks.Exists()
	v.Seats = s.ks.Meta()
	v.MnemonicSources = s.ks.MnemonicSources()
	v.ImportCommands = terminalImportCommands(s.ks.Path())
	if lim := s.ks.IdleLimit(); lim > 0 && !s.ks.Locked() {
		if left := lim - s.ks.IdleFor(); left > 0 {
			v.LockIn = int(left.Seconds())
		}
	}
	if m := s.ks.Master(); m != nil {
		v.Master = m.Address.Hex()
	} else {
		for _, meta := range v.Seats {
			if meta.Kind == "master" {
				v.Master = meta.Address
			}
		}
	}
	return v
}

func (s *Server) body(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("请求内容不是合法 JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("请求只能包含一个 JSON 对象")
	}

	return nil
}

func (s *Server) busy(what string, fn func(ctx context.Context) error) error {

	if !s.st.TryBusy(what) {
		return fmt.Errorf("正在%s，等它做完", s.st.Busy())
	}
	defer s.st.Idle()

	s.ks.Touch()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := fn(ctx); err != nil {
		s.st.Logf("bad", "%s失败：%v", what, err)
		return err
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rcancel()
	_ = s.run.Refresh(rctx)
	return nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, 200, s.snapshot())
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Password string `json:"password"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("解锁", func(ctx context.Context) error {
		if err := s.ks.Unlock(b.Password); err != nil {
			return err
		}
		s.restoreConnection(ctx)
		return nil
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ks.Touch()
	if d := s.ks.IdleLimit(); d > 0 {
		s.st.Logf("ok", "已解锁，%d 个号 —— 空闲 %s 后自动上锁", len(s.ks.Seats()), d)
	} else {
		s.st.Logf("ok", "已解锁，%d 个号", len(s.ks.Seats()))
	}
	s.ok(w)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	if !s.st.TryBusy("上锁") {
		s.fail(w, 400, fmt.Errorf("请先停止运行并等待当前交易完成"))
		return
	}
	defer s.st.Idle()
	s.ks.Lock()
	s.st.Logf("", "已上锁")
	s.ok(w)
}

func (s *Server) handleSeatRemove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	pw := b.Password
	if pw == "" {
		s.fail(w, 400, fmt.Errorf("要输密码 —— 删号需要重新加密整个文件"))
		return
	}
	addr, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("删除账号", func(ctx context.Context) error { return s.ks.RemoveSeat(pw, addr) }); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.st.Logf("", "移除 %s", short(addr))
	s.ok(w)
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Shifu string `json:"shifu"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	shifu := common.Address{}
	if strings.TrimSpace(b.Shifu) != "" {
		var err error
		if shifu, err = parseAddr(b.Shifu); err != nil {
			s.fail(w, 400, err)
			return
		}
	} else if m := s.ks.Master(); m != nil {
		shifu = m.Address
	}
	err := s.busy("部署", func(ctx context.Context) error {
		_, err := s.run.Deploy(ctx, shifu)
		if err != nil {
			return err
		}
		return s.saveConnection()
	})
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address string `json:"address"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	addr, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("载入合约", func(ctx context.Context) error {
		if err := s.run.Attach(ctx, addr); err != nil {
			return err
		}
		return s.saveConnection()
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleDelegate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Addresses []string `json:"addresses"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(b.Addresses)
	if err != nil {
		s.fail(w, 400, err)
		return
	}

	if len(who) == 0 {
		for _, m := range s.ks.Members() {
			who = append(who, m.Address)
		}
	}
	if err := s.busy("委托", func(ctx context.Context) error {
		return s.run.Delegate(ctx, who)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleRosterAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Addresses []string `json:"addresses"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(b.Addresses)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(who) == 0 {
		if s.settings.Get().MasterPlays {
			if m := s.ks.Master(); m != nil {
				who = append(who, m.Address)
			}
		}
		for _, m := range s.ks.Members() {
			who = append(who, m.Address)
		}
	}
	if err := s.busy("加入名册", func(ctx context.Context) error {
		return s.run.AddMembers(ctx, who)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleRosterRemove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address string `json:"address"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	addr, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("移出名册", func(ctx context.Context) error {
		return s.run.RemoveMember(ctx, addr)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handlePrepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := s.body(r, &req); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(req.Addresses)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(who) == 0 {
		s.fail(w, 400, fmt.Errorf("请选择要拜师授权的账号"))
		return
	}
	if err := s.busy("拜师授权", func(ctx context.Context) error {
		return s.run.Prepare(ctx, who)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address string `json:"address"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("打一局", func(ctx context.Context) error {
		return s.run.PlayOne(ctx, who)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleHarvest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Address string `json:"address"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	addr, err := parseAddr(b.Address)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.busy("收", func(ctx context.Context) error {
		return s.run.HarvestOne(ctx, addr)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleBatchStart(w http.ResponseWriter, r *http.Request) {
	var b struct {
		EntryMode string   `json:"entryMode"`
		Rounds    int      `json:"rounds"`
		Who       []string `json:"who"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(b.Who)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(who) == 0 {
		s.fail(w, 400, fmt.Errorf("请明确选择至少一个账号"))
		return
	}
	if s.batch.Running() || s.st.Busy() != "" {
		s.fail(w, 400, fmt.Errorf("已有操作在运行"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	who, err = s.participants(ctx, who)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err := s.settings.Update(func(v *Settings) { v.Rounds = b.Rounds; v.EntryMode = b.EntryMode }); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ks.Touch()

	if err := s.batch.Start(Plan{Rounds: b.Rounds, Who: who, EntryMode: b.EntryMode}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleBatchHarvest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Who []string `json:"who"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(b.Who)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(who) == 0 {
		s.fail(w, 400, fmt.Errorf("请明确选择至少一个账号"))
		return
	}
	s.ks.Touch()

	if err := s.batch.HarvestNow(who); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func (s *Server) handleBatchStop(w http.ResponseWriter, r *http.Request) {
	s.batch.Stop()
	s.ok(w)
}

func (s *Server) handleSweepGas(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Who []string `json:"who"`
	}
	if err := s.body(r, &b); err != nil {
		s.fail(w, 400, err)
		return
	}
	who, err := parseAddrs(b.Who)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if len(who) == 0 {
		s.fail(w, 400, fmt.Errorf("请明确选择至少一个账号"))
		return
	}
	if err := s.busy("收 gas 币", func(ctx context.Context) error {
		return s.run.SweepGas(ctx, who)
	}); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.ok(w)
}

func parseAddr(s string) (common.Address, error) {
	s = strings.TrimSpace(s)
	if !common.IsHexAddress(s) {
		return common.Address{}, fmt.Errorf("%q 不是合法地址", s)
	}
	a := common.HexToAddress(s)
	if a == (common.Address{}) {
		return common.Address{}, fmt.Errorf("地址不能是零地址")
	}
	return a, nil
}

func parseAddrs(in []string) ([]common.Address, error) {
	seen := map[common.Address]bool{}
	var out []common.Address
	for _, value := range in {
		a, err := parseAddr(value)
		if err != nil {
			return nil, err
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out, nil
}

func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q 格式不对（要像 127.0.0.1:8910）", addr)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {

		return nil, fmt.Errorf("为了安全只允许绑 127.0.0.1，你写的是 %q", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	log.Printf("  监听 %s", ln.Addr())
	return ln, nil
}

func ListenAuto(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q 格式不对（要像 127.0.0.1:8910）", addr)
	}
	base, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("端口 %q 不是数字", portStr)
	}
	var last error
	for i := 0; i < 20; i++ {
		ln, err := Listen(net.JoinHostPort(host, strconv.Itoa(base+i)))
		if err == nil {
			if i > 0 {
				log.Printf("  （%d 被别的程序占着，换到 %d）", base, base+i)
			}
			return ln, nil
		}
		last = err
		if !isAddrInUse(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%d 往后 20 个端口都被占着：%w", base, last)
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || containsFold(err.Error(), "address already in use")
}

func (s *Server) handleRecoverTransaction(w http.ResponseWriter, r *http.Request) {
	if err := s.busy("核查原交易", func(ctx context.Context) error { return s.chain.RecoverPending(ctx) }); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.st.Logf("ok", "原交易已确认，请核对状态后继续")
	s.ok(w)
}
