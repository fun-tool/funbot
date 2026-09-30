package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

type Squad struct {
	chain     *Chain
	impl      common.Address
	acct      common.Address
	abi       abi.ABI
	champMu   sync.Mutex
	champHead *types.Header
	champions map[common.Address]championHistory
}

type Config struct {
	Arena    common.Address `json:"arena"`
	Token    common.Address `json:"token"`
	Lineage  common.Address `json:"lineage"`
	GasToken common.Address `json:"gasToken"`
	Shifu    common.Address `json:"shifu"`
	Master   common.Address `json:"master"`
	Impl     common.Address `json:"impl"`
}

type Pulse struct {
	Championships *string        `json:"championships"`
	Who           common.Address `json:"who"`
	Label         string         `json:"label"`
	Wallet        *big.Int       `json:"-"`
	Settled       *big.Int       `json:"-"`
	Unsettled     *big.Int       `json:"-"`
	Lineage       *big.Int       `json:"-"`
	Jiazi         *big.Int       `json:"-"`
	Total         *big.Int       `json:"-"`
	Ready         *big.Int       `json:"-"`
	Locked        *big.Int       `json:"-"`

	CanHarvest   bool   `json:"canHarvest"`
	BlocksToWait uint64 `json:"blocksToWait"`
	Seated       bool   `json:"seated"`
	Delegated    bool   `json:"delegated"`
	Truncated    bool   `json:"jiaziTruncated"`
	MatchesToday uint64 `json:"matchesToday"`
	LastRound    uint64 `json:"lastRound"`
	CurrentRound uint64 `json:"currentRound"`
	OpenSeats    uint64 `json:"openSeats"`
	Seats        uint64 `json:"seats"`
	Backlog      uint64 `json:"backlog"`
	SeatNo       uint64 `json:"seatNo"`

	WalletStr    string `json:"wallet"`
	SettledStr   string `json:"settled"`
	UnsettledStr string `json:"unsettled"`
	LineageStr   string `json:"lineage"`
	JiaziStr     string `json:"jiazi"`
	TotalStr     string `json:"total"`
	ReadyStr     string `json:"ready"`
	LockedStr    string `json:"locked"`
	ArenaStr     string `json:"arena"`

	GasBalanceStr string `json:"gasBalance"`
}

func NewSquad(c *Chain, impl, master common.Address) (*Squad, error) {
	parsed, err := abi.JSON(strings.NewReader(squadABI))
	if err != nil {
		return nil, err
	}
	return &Squad{chain: c, impl: impl, acct: master, abi: parsed}, nil
}

func (s *Squad) Impl() common.Address { return s.impl }

func (s *Squad) call(ctx context.Context, method string, args ...any) ([]any, error) {
	return s.callAt(ctx, s.acct, method, args...)
}

func (s *Squad) callAt(ctx context.Context, to common.Address, method string, args ...any) ([]any, error) {
	in, err := s.abi.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s 编码失败: %w", method, err)
	}
	out, err := s.chain.Call(ctx, to, in)
	if err != nil {
		return nil, fmt.Errorf("%s 调用失败: %w", method, err)
	}
	vals, err := s.abi.Unpack(method, out)
	if err != nil {
		return nil, fmt.Errorf("%s 解码失败: %w", method, err)
	}
	return vals, nil
}

func (s *Squad) Config(ctx context.Context) (*Config, error) {
	v, err := s.callAt(ctx, s.impl, "config")
	if err != nil {
		return nil, err
	}
	if len(v) < 7 {
		return nil, fmt.Errorf("config 返回了 %d 个值，期望 7 —— 合约版本对不上", len(v))
	}
	get := func(i int) common.Address { a, _ := v[i].(common.Address); return a }
	return &Config{
		Arena: get(0), Token: get(1), Lineage: get(2), GasToken: get(3),
		Shifu: get(4), Master: get(5), Impl: get(6),
	}, nil
}

type Overview struct {
	Total         uint64 `json:"total"`
	Upgraded      uint64 `json:"upgraded"`
	RoundID       uint64 `json:"roundId"`
	DayID         uint64 `json:"dayId"`
	MasterBalance string `json:"masterBalance"`
	MasterGas     string `json:"masterGas"`

	MasterWei *big.Int `json:"-"`
}

func (s *Squad) Overview(ctx context.Context) (*Overview, error) {
	v, err := s.call(ctx, "overview")
	if err != nil {
		return nil, err
	}
	u := func(i int) uint64 {
		if b, ok := v[i].(*big.Int); ok {
			return b.Uint64()
		}
		return 0
	}
	b := func(i int) *big.Int {
		if x, ok := v[i].(*big.Int); ok {
			return x
		}
		return big.NewInt(0)
	}
	return &Overview{
		Total: u(0), Upgraded: u(1), RoundID: u(2), DayID: u(3),
		MasterBalance: fmtUnits(b(4)), MasterGas: fmtUnits(b(5)),
		MasterWei: b(4),
	}, nil
}

func (s *Squad) Members(ctx context.Context) ([]common.Address, error) {
	v, err := s.call(ctx, "allMembers")
	if err != nil {
		return nil, err
	}
	out, _ := v[0].([]common.Address)
	return out, nil
}

func (s *Squad) PulseBatch(ctx context.Context, who []common.Address) ([]*Pulse, []error) {
	datas := make([][]byte, len(who))
	errs := make([]error, len(who))
	for i, a := range who {
		in, err := s.abi.Pack("pulse", a)
		if err != nil {
			errs[i] = err
			continue
		}
		datas[i] = in
	}
	raws, callErrs := s.chain.CallBatch(ctx, s.acct, datas)
	out := make([]*Pulse, len(who))
	for i := range who {
		if errs[i] != nil {
			continue
		}
		if callErrs[i] != nil {
			errs[i] = callErrs[i]
			continue
		}
		p, err := s.decodePulse(who[i], raws[i])
		if err != nil {
			errs[i] = err
			continue
		}
		out[i] = p
	}
	return out, errs
}

func (s *Squad) Pulse(ctx context.Context, who common.Address) (*Pulse, error) {
	in, err := s.abi.Pack("pulse", who)
	if err != nil {
		return nil, err
	}
	raw, err := s.chain.Call(ctx, s.acct, in)
	if err != nil {
		return nil, err
	}
	return s.decodePulse(who, raw)
}

func (s *Squad) decodePulse(who common.Address, out []byte) (*Pulse, error) {
	v, err := s.abi.Unpack("pulse", out)
	if err != nil {
		return nil, fmt.Errorf("pulse 解码失败: %w", err)
	}
	if len(v) == 0 {
		return nil, fmt.Errorf("pulse 没有返回值")
	}
	raw, ok := v[0].(struct {
		Wallet         *big.Int `json:"wallet"`
		Settled        *big.Int `json:"settled"`
		Unsettled      *big.Int `json:"unsettled"`
		Lineage        *big.Int `json:"lineage"`
		Jiazi          *big.Int `json:"jiazi"`
		Total          *big.Int `json:"total"`
		Ready          *big.Int `json:"ready"`
		Locked         *big.Int `json:"locked"`
		CanHarvest     bool     `json:"canHarvest"`
		BlocksToWait   *big.Int `json:"blocksToWait"`
		Seated         bool     `json:"seated"`
		Delegated      bool     `json:"delegated"`
		JiaziTruncated bool     `json:"jiaziTruncated"`
		MatchesToday   *big.Int `json:"matchesToday"`
		LastRound      *big.Int `json:"lastRound"`
		CurrentRound   *big.Int `json:"currentRound"`
		OpenSeats      *big.Int `json:"openSeats"`
		Seats          *big.Int `json:"seats"`
		Backlog        *big.Int `json:"backlog"`
		SeatNo         *big.Int `json:"seatNo"`
		GasBalance     *big.Int `json:"gasBalance"`
	})
	if !ok {
		return nil, fmt.Errorf("pulse 的返回结构对不上 —— 合约和这个程序的版本不一致")
	}
	arena := new(big.Int).Add(raw.Settled, raw.Unsettled)
	return &Pulse{
		Who:    who,
		Wallet: raw.Wallet, Settled: raw.Settled, Unsettled: raw.Unsettled,
		Lineage: raw.Lineage, Jiazi: raw.Jiazi, Total: raw.Total,
		Ready: raw.Ready, Locked: raw.Locked,
		CanHarvest: raw.CanHarvest, BlocksToWait: raw.BlocksToWait.Uint64(),
		Seated: raw.Seated, Delegated: raw.Delegated, Truncated: raw.JiaziTruncated,
		MatchesToday: raw.MatchesToday.Uint64(), LastRound: raw.LastRound.Uint64(),
		CurrentRound: raw.CurrentRound.Uint64(), OpenSeats: raw.OpenSeats.Uint64(),
		Seats: raw.Seats.Uint64(), Backlog: raw.Backlog.Uint64(), SeatNo: raw.SeatNo.Uint64(),
		WalletStr: fmtUnits(raw.Wallet), SettledStr: fmtUnits(raw.Settled),
		UnsettledStr: fmtUnits(raw.Unsettled), LineageStr: fmtUnits(raw.Lineage),
		JiaziStr: fmtUnits(raw.Jiazi), TotalStr: fmtUnits(raw.Total),
		ReadyStr: fmtUnits(raw.Ready), LockedStr: fmtUnits(raw.Locked),
		ArenaStr:      fmtUnits(arena),
		GasBalanceStr: fmtUnits(raw.GasBalance),
	}, nil
}

const gasDeploy = 6_000_000

func (s *Squad) send(ctx context.Context, master *Seat, gas uint64, method string, args ...any) (*types.Receipt, error) {

	if err := s.requireDelegated(ctx, master.Address); err != nil {
		return nil, err
	}
	in, err := s.abi.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s 编码失败: %w", method, err)
	}

	var floor uint64
	if gas == 0 {
		n := 0
		explicitList := false
		if len(args) > 0 {
			if who, ok := args[0].([]common.Address); ok {
				n = len(who)
				explicitList = true
			}
		}
		switch method {
		case "playOne", "harvestOne", "removeMember":
			n = 1
		default:
			if !explicitList || (method == "sweepGasSome" && n == 0) {
				members, err := s.Members(ctx)
				if err != nil {
					return nil, err
				}
				n = len(members)
			}
		}
		if method == "prepareAll" && len(args) > 1 {
			if count, ok := args[1].(*big.Int); ok && count.Sign() > 0 {
				n = int(count.Int64())
			}
		}
		perMember := uint64(500_000)
		if strings.HasPrefix(method, "harvest") {
			perMember = 2_000_000
		}
		floor = 300_000 + uint64(n)*perMember
		if method == "addMembers" || method == "fundSome" || method == "removeMember" || method == "playOne" {
			floor = 0
		}
	}
	rec, err := s.chain.SendCall(ctx, master, s.acct, in, gas, floor)
	if err != nil {
		return nil, err
	}
	if rec.Status == 0 {
		return rec, fmt.Errorf("%s 被链上回滚了（交易 %s）", method, rec.TxHash.Hex())
	}
	return rec, nil
}

func (s *Squad) AddMembers(ctx context.Context, master *Seat, who []common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "addMembers", who)
}

func (s *Squad) RemoveMember(ctx context.Context, master *Seat, who common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "removeMember", who)
}

func (s *Squad) HarvestSome(ctx context.Context, master *Seat, who []common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "harvestSome", who)
}

func (s *Squad) PlayOne(ctx context.Context, master *Seat, who common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "playOne", who)
}

func (s *Squad) SweepGas(ctx context.Context, master *Seat, who []common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "sweepGasSome", who)
}

func (s *Squad) HarvestOne(ctx context.Context, master *Seat, who common.Address) (*types.Receipt, error) {
	return s.send(ctx, master, 0, "harvestOne", who)
}

func (s *Squad) requireDelegated(ctx context.Context, who common.Address) error {
	code, err := s.chain.CodeAt(ctx, who)
	if err != nil {
		return err
	}
	want := delegationCode(s.impl)
	if equalHex(code, want) {
		return nil
	}
	if len(code) == 0 {
		return fmt.Errorf("主号 %s 还没委托到合约 —— 现在发出去的交易会「成功」但什么都不做。先做委托那一步", short(who))
	}
	return fmt.Errorf("主号 %s 委托到了别处（%x），不是这份合约", short(who), code)
}

func DeploySquad(ctx context.Context, c *Chain, master *Seat, net *Network, shifu common.Address) (common.Address, error) {
	parsed, err := abi.JSON(strings.NewReader(squadABI))
	if err != nil {
		return common.Address{}, err
	}
	args, err := parsed.Pack("", net.Arena, net.Token, net.Lineage, net.GasToken, master.Address, shifu)
	if err != nil {
		return common.Address{}, fmt.Errorf("构造参数编码失败: %w", err)
	}
	code, err := hexutil.Decode(squadBytecode)
	if err != nil {
		return common.Address{}, err
	}

	nonce, err := c.PendingNonce(ctx, master.Address)
	if err != nil {
		return common.Address{}, err
	}
	tx := types.NewTx(&types.LegacyTx{
		Nonce: nonce, To: nil, Value: big.NewInt(0),
		Gas: gasDeploy, GasPrice: c.GasPrice(), Data: append(code, args...),
	})
	signed, err := master.signTx(tx, types.NewEIP155Signer(big.NewInt(c.chainID)))
	if err != nil {
		return common.Address{}, err
	}
	if err := c.Send(ctx, signed); err != nil {
		return common.Address{}, err
	}
	rec, err := c.WaitReceipt(ctx, signed.Hash())
	if err != nil {
		return common.Address{}, err
	}
	if rec.Status == 0 {
		return common.Address{}, fmt.Errorf("部署交易被回滚了：%s", signed.Hash().Hex())
	}
	if rec.ContractAddress == (common.Address{}) {
		return common.Address{}, fmt.Errorf("回执里没有合约地址")
	}
	return rec.ContractAddress, nil
}

var oneToken = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func fmtUnits(v *big.Int) string {
	if v == nil {
		return "0"
	}
	q, m := new(big.Int).QuoRem(v, oneToken, new(big.Int))
	if m.Sign() == 0 {
		return q.String()
	}
	frac := m.String()
	for len(frac) < 18 {
		frac = "0" + frac
	}
	frac = strings.TrimRight(frac[:4], "0")
	if frac == "" {
		return q.String()
	}
	return q.String() + "." + frac
}
