pragma solidity 0.8.28;

interface IArena {
    function enterArena() external;
    function claimRewards() external;
    function claimLineage() external;
    function claimJiazi(uint256 from, uint256 count) external;

    struct JiaziRow {
        uint256 index;
        uint256 roundId;
        uint256 validDays;
        uint256 released;
        uint256 claimedDays;
        uint256 claimableAmount;
    }
    function jiaziPage(address owner, uint256 from, uint256 count)
        external
        view
        returns (JiaziRow[] memory rows, uint256 total);
    function jiaziCount(address owner) external view returns (uint256);
    function claimable(address player) external view returns (uint256);
    function ENTRY_FEE() external view returns (uint256);
    function isSeated(uint256 roundId, address player) external view returns (bool);
    function currentRoundId() external view returns (uint256);
    function currentDayId() external view returns (uint256);
    function lineagePending(address claimant) external view returns (uint256);
    function activityOf(address owner)
        external
        view
        returns (uint256 fullDays, uint256 dayId, uint256 matchesToday, uint256 firstIncomplete);

    struct PlayerSummary {
        uint256 walletBalance;
        uint256 allowance;
        uint256 prize;
        uint256 inMatchDeposit;
        uint256 arenaTotal;
        uint256 battles;
        uint256 championships;
        uint256 jiaziTotalGranted;
        uint256 jiaziReleased;
        uint256 jiaziClaimable;
        bool jiaziTruncated;
        uint256 entriesToday;
        uint256 entriesRemainingToday;
        uint256 roundsFromPrize;
        bool nextEntryFromPrize;
        bool seatedInCurrentRound;
    }

    function playerSummary(address player) external view returns (PlayerSummary memory);

    enum RoundState {
        NONE,
        OPEN,
        SEALED,
        SETTLED
    }

    struct RoundView {
        RoundState state;
        uint256 seatCount;
        uint256 sealedBlock;
        address champion;
        uint256 championSeat;
        bytes32 rosterHash;
        bytes32 lockedEntropy;
    }

    function roundInfo(uint256 rid) external view returns (RoundView memory);
    function seatsOf(uint256 rid) external view returns (address[] memory);
    function pendingHead() external view returns (uint256);
    function SEATS() external view returns (uint256);
}

interface IERC20 {
    function balanceOf(address) external view returns (uint256);
    function transfer(address to, uint256 value) external returns (bool);
    function approve(address spender, uint256 value) external returns (bool);
    function allowance(address owner, address spender) external view returns (uint256);
}

interface ILineage {
    function bind(address master) external;
    function masterOf(address disciple) external view returns (address);
}

contract Squad {
    IArena public immutable ARENA;
    IERC20 public immutable TOKEN;
    ILineage public immutable LINEAGE;

    IERC20 public immutable GAS_TOKEN;

    address public immutable MASTER;

    address public immutable SHIFU;

    receive() external payable {}

    address public immutable IMPL;

    bytes32 public immutable DELEGATION_CODEHASH;

    address[] public members;
    mapping(address => bool) public isMember;

    mapping(address => uint256) public lastRoundOf;

    uint256 public constant JIAZI_PAGE = 64;

    uint256 public constant MAX_MATCHES_PER_DAY = 12;

    error NotMaster();
    error NotSelf();
    error ZeroAddress();
    error NoMembers();
    error TransferFailed();
    error InvalidFunding();
    error NotDelegated(address who);

    error CouldNotEnter(address who);

    event MembersAdded(uint256 count);
    event MemberRemoved(address who);
    event RoundPlayed(uint256 funded, uint256 entered);
    event Harvested(uint256 members, uint256 total);
    event WalletsFunded(uint256 amountEach, uint256 total);

    event GasSwept(uint256 total);

    modifier onlyMaster() {
        if (msg.sender != MASTER) revert NotMaster();
        _;
    }

    modifier onlySelf() {
        if (msg.sender != address(this) || address(this) != MASTER) revert NotSelf();
        _;
    }

    constructor(address arena, address token, address lineage, address gasToken, address master, address shifu) {
        if (arena == address(0) || token == address(0) || lineage == address(0)) revert ZeroAddress();
        if (gasToken == address(0) || master == address(0) || shifu == address(0)) revert ZeroAddress();
        ARENA = IArena(arena);
        TOKEN = IERC20(token);
        LINEAGE = ILineage(lineage);
        GAS_TOKEN = IERC20(gasToken);
        MASTER = master;
        SHIFU = shifu;
        IMPL = address(this);
        DELEGATION_CODEHASH = keccak256(abi.encodePacked(bytes3(0xef0100), address(this)));
    }

    function isDelegated(address who) public view returns (bool) {
        return who.codehash == DELEGATION_CODEHASH;
    }

    function memberCount() external view returns (uint256) {
        return members.length;
    }

    function allMembers() external view returns (address[] memory) {
        return members;
    }

    function addMembers(address[] calldata who) external onlySelf {
        uint256 added;
        for (uint256 i = 0; i < who.length; ++i) {
            address a = who[i];
            if (a == address(0) || isMember[a]) continue;
            isMember[a] = true;
            members.push(a);
            ++added;
        }
        emit MembersAdded(added);
    }

    function removeMember(address who) external onlySelf {
        uint256 n = members.length;
        for (uint256 i = 0; i < n; ++i) {
            if (members[i] != who) continue;
            members[i] = members[n - 1];
            members.pop();
            isMember[who] = false;
            emit MemberRemoved(who);
            return;
        }
    }

    function _range(uint256 from, uint256 count) private view returns (uint256 lo, uint256 hi) {
        uint256 n = members.length;
        if (n == 0) revert NoMembers();
        lo = from < n ? from : n;
        hi = count == 0 ? n : lo + count;
        if (hi > n) hi = n;
    }

    function enter() external onlyMaster {
        ARENA.enterArena();
    }

    function _sweepToMaster(uint256 before) private returns (uint256 gained) {
        if (address(this) == MASTER) {
            uint256 now_ = TOKEN.balanceOf(MASTER);
            return now_ > before ? now_ - before : 0;
        }
        gained = TOKEN.balanceOf(address(this));
        if (gained != 0 && !TOKEN.transfer(MASTER, gained)) revert TransferFailed();
    }

    function _held() private view returns (uint256) {
        return address(this) == MASTER ? TOKEN.balanceOf(MASTER) : 0;
    }

    function sweepGas() external onlyMaster returns (uint256 sent) {
        if (address(this) == MASTER) return 0;
        sent = GAS_TOKEN.balanceOf(address(this));
        if (sent == 0) return 0;
        if (!GAS_TOKEN.transfer(MASTER, sent)) revert TransferFailed();
    }

    function claimLineageOnly() external onlyMaster {
        ARENA.claimLineage();
    }

    function claimJiaziPage(uint256 from) external onlyMaster returns (uint256 sent) {
        uint256 beforeMaster = _held();
        (IArena.JiaziRow[] memory rows,) = ARENA.jiaziPage(address(this), from, JIAZI_PAGE);
        uint256 amount;
        for (uint256 i; i < rows.length; ++i) {
            amount += rows[i].claimableAmount;
        }
        if (amount != 0) ARENA.claimJiazi(from, JIAZI_PAGE);
        return _sweepToMaster(beforeMaster);
    }

    function harvestJiaziPage(address[] calldata who, uint256 from) external onlySelf returns (uint256 total) {
        for (uint256 i; i < who.length; ++i) {
            if (!isDelegated(who[i])) revert NotDelegated(who[i]);
            total += Squad(payable(who[i])).claimJiaziPage(from);
        }
        emit Harvested(who.length, total);
    }

    function claimJiaziOnly() external onlyMaster {
        ARENA.claimJiazi(0, JIAZI_PAGE);
    }

    function collect() external onlyMaster returns (uint256 sent) {
        uint256 before = _held();
        try ARENA.claimRewards() {} catch {}
        sent = _sweepToMaster(before);
    }

    function harvest() external onlyMaster returns (uint256 sent) {
        uint256 before = _held();

        try ARENA.claimLineage() {} catch {}

        try ARENA.claimJiazi(0, JIAZI_PAGE) {} catch {}

        try ARENA.claimRewards() {} catch {}

        sent = _sweepToMaster(before);
    }

    function sweep() external onlyMaster returns (uint256 sent) {
        sent = _sweepToMaster(_held());
    }

    function approveArena(uint256 value) external onlyMaster {
        if (!TOKEN.approve(address(ARENA), value)) revert TransferFailed();
    }

    function bindShifu() external onlyMaster {
        LINEAGE.bind(SHIFU);
    }

    function playOne(address who) external onlySelf returns (uint256 funded, uint256 reaped, bool entered) {
        if (!isDelegated(who)) revert NotDelegated(who);
        (funded, reaped, entered) = _seatOne(who);
        if (!entered) revert CouldNotEnter(who);
        emit RoundPlayed(funded, 1);
    }

    function playRound(uint256 from, uint256 count)
        external
        onlySelf
        returns (uint256 funded, uint256 reaped, uint256 entered)
    {
        (uint256 lo, uint256 hi) = _range(from, count);
        for (uint256 i = lo; i < hi; ++i) {
            (uint256 f, uint256 r, bool ok) = _seatOne(members[i]);
            funded += f;
            reaped += r;
            if (ok) {
                unchecked {
                    ++entered;
                }
            }
        }
        emit RoundPlayed(funded, entered);
    }

    function playSome(address[] calldata who)
        external
        onlySelf
        returns (uint256 funded, uint256 reaped, uint256 entered)
    {
        for (uint256 i = 0; i < who.length; ++i) {
            if (!isMember[who[i]]) continue;
            (uint256 f, uint256 r, bool ok) = _seatOne(who[i]);
            funded += f;
            reaped += r;
            if (ok) {
                unchecked {
                    ++entered;
                }
            }
        }
        emit RoundPlayed(funded, entered);
    }

    function fundSome(address[] calldata who, uint256 amountEach) external onlySelf returns (uint256 total) {
        if (amountEach == 0 || who.length == 0) revert InvalidFunding();
        for (uint256 i; i < who.length; ++i) {
            address k = who[i];
            if (k == MASTER) continue;
            if (!isMember[k] || !isDelegated(k)) revert NotDelegated(k);
            for (uint256 j; j < i; ++j) {
                if (who[j] == k) revert InvalidFunding();
            }
            if (!TOKEN.transfer(k, amountEach)) revert TransferFailed();
            total += amountEach;
        }
        if (total == 0) revert InvalidFunding();
        emit WalletsFunded(amountEach, total);
    }

    function harvestSome(address[] calldata who) external onlySelf returns (uint256 total) {
        for (uint256 i = 0; i < who.length; ++i) {
            address k = who[i];
            if (!isMember[k] || !isDelegated(k)) continue;
            try Squad(payable(k)).claimLineageOnly() {} catch {}
            try Squad(payable(k)).claimJiaziOnly() {} catch {}
        }

        uint256 n;
        for (uint256 pass = 0; pass < 2; ++pass) {
            for (uint256 i = 0; i < who.length; ++i) {
                address k = who[i];
                if (!isMember[k] || !isDelegated(k)) continue;
                try Squad(payable(k)).collect() returns (uint256 sent) {
                    if (sent != 0) {
                        total += sent;
                        if (pass == 0) {
                            unchecked {
                                ++n;
                            }
                        }
                    }
                } catch {}
            }
        }
        emit Harvested(n, total);
    }

    function _seatOne(address k) private returns (uint256 funded, uint256 reaped, bool entered) {
        if (!isDelegated(k)) return (0, 0, false);
        uint256 rid = ARENA.currentRoundId();
        if (ARENA.isSeated(rid, k)) return (0, 0, false);
        (, uint256 dayId, uint256 matches,) = ARENA.activityOf(k);
        if (dayId == ARENA.currentDayId() && matches >= MAX_MATCHES_PER_DAY) return (0, 0, false);

        uint256 fee = ARENA.ENTRY_FEE();
        if (k != MASTER && ARENA.claimable(k) < fee) {
            uint256 available = TOKEN.balanceOf(k);
            if (available < fee) {
                funded = fee - available;
                if (!TOKEN.transfer(k, funded)) revert TransferFailed();
            }
        }
        reaped = 0;
        try Squad(payable(k)).enter() {
            lastRoundOf[k] = rid;
            entered = true;
        } catch {}
    }

    function harvestAll(uint256 from, uint256 count) external onlySelf returns (uint256 total) {
        (uint256 lo, uint256 hi) = _range(from, count);

        for (uint256 i = lo; i < hi; ++i) {
            address k = members[i];
            if (!isDelegated(k)) continue;
            try Squad(payable(k)).claimLineageOnly() {} catch {}
            try Squad(payable(k)).claimJiaziOnly() {} catch {}
        }

        uint256 n;
        for (uint256 pass = 0; pass < 2; ++pass) {
            for (uint256 i = lo; i < hi; ++i) {
                address k = members[i];
                if (!isDelegated(k)) continue;
                try Squad(payable(k)).collect() returns (uint256 sent) {
                    if (sent != 0) {
                        total += sent;
                        if (pass == 0) {
                            unchecked {
                                ++n;
                            }
                        }
                    }
                } catch {}
            }
        }
        emit Harvested(n, total);
    }

    function sweepGasSome(address[] calldata who) external onlySelf returns (uint256 total) {
        uint256 n = who.length;
        bool all = n == 0;
        if (all) n = members.length;
        for (uint256 i = 0; i < n; ++i) {
            address k = all ? members[i] : who[i];
            if (!isMember[k] || !isDelegated(k)) continue;
            if (GAS_TOKEN.balanceOf(k) == 0) continue;
            try Squad(payable(k)).sweepGas() returns (uint256 sent) {
                total += sent;
            } catch {}
        }
        emit GasSwept(total);
    }

    function harvestOne(address who) external onlySelf returns (uint256 sent) {
        if (!isDelegated(who)) revert NotDelegated(who);
        sent = Squad(payable(who)).harvest();
        emit Harvested(1, sent);
    }

    function prepareAll(uint256 from, uint256 count) external onlySelf returns (uint256 bound, uint256 approved) {
        (uint256 lo, uint256 hi) = _range(from, count);
        for (uint256 i = lo; i < hi; ++i) {
            address k = members[i];
            if (!isDelegated(k)) continue;
            if (LINEAGE.masterOf(k) == address(0)) {
                try Squad(payable(k)).bindShifu() {
                    unchecked {
                        ++bound;
                    }
                } catch {}
            }
            if (TOKEN.allowance(k, address(ARENA)) < type(uint128).max) {
                try Squad(payable(k)).approveArena(type(uint256).max) {
                    unchecked {
                        ++approved;
                    }
                } catch {}
            }
        }
    }

    struct Pulse {


        uint256 wallet;
        uint256 settled;
        uint256 unsettled;
        uint256 lineage;
        uint256 jiazi;
        uint256 total;

        uint256 ready;
        uint256 locked;
        bool canHarvest;
        uint256 blocksToWait;

        bool seated;
        bool delegated;
        bool jiaziTruncated;
        uint256 matchesToday;
        uint256 lastRound;
        uint256 currentRound;
        uint256 openSeats;
        uint256 seats;
        uint256 backlog;
        uint256 seatNo;

        uint256 gasBalance;
    }

    function pulse(address who) external view returns (Pulse memory p) {
        p.currentRound = ARENA.currentRoundId();
        p.seats = ARENA.SEATS();
        p.delegated = isDelegated(who);
        p.lastRound = lastRoundOf[who];

        uint256 head = ARENA.pendingHead();
        p.backlog = p.currentRound > head ? p.currentRound - head : 0;
        p.openSeats = ARENA.roundInfo(p.currentRound).seatCount;

        IArena.PlayerSummary memory ps = ARENA.playerSummary(who);
        p.wallet = ps.walletBalance;
        p.settled = ps.prize;
        p.unsettled = ps.inMatchDeposit;
        p.jiazi = ps.jiaziClaimable;
        p.jiaziTruncated = ps.jiaziTruncated;
        p.seated = ps.seatedInCurrentRound;
        p.lineage = ARENA.lineagePending(who);

        (, uint256 dayId, uint256 matches,) = ARENA.activityOf(who);
        p.matchesToday = dayId == ARENA.currentDayId() ? matches : 0;

        p.locked = p.unsettled;

        uint256 collectible = who == MASTER ? 0 : p.wallet;

        p.ready = collectible + p.settled + p.lineage + p.jiazi;
        p.total = p.wallet + p.settled + p.lineage + p.jiazi + p.locked;
        p.canHarvest = p.ready > 0;
        p.gasBalance = GAS_TOKEN.balanceOf(who);

        if (p.seated) {
            address[] memory roster = ARENA.seatsOf(p.currentRound);
            for (uint256 i = 0; i < roster.length; ++i) {
                if (roster[i] == who) {
                    p.seatNo = i + 1;
                    break;
                }
            }
            return p;
        }

        if (p.locked > 0 && p.lastRound != 0) {
            IArena.RoundView memory r = ARENA.roundInfo(p.lastRound);
            if (r.state == IArena.RoundState.SEALED && block.number <= r.sealedBlock + 1) {
                p.blocksToWait = r.sealedBlock + 2 - block.number;
            }
        }
    }

    function overview()
        external
        view
        returns (
            uint256 total,
            uint256 upgraded,
            uint256 roundId,
            uint256 dayId,
            uint256 masterBalance,
            uint256 masterGas
        )
    {
        total = members.length;
        for (uint256 i = 0; i < total; ++i) {
            if (isDelegated(members[i])) ++upgraded;
        }
        roundId = ARENA.currentRoundId();
        dayId = ARENA.currentDayId();
        masterBalance = TOKEN.balanceOf(MASTER);
        masterGas = GAS_TOKEN.balanceOf(MASTER);
    }

    function config()
        external
        view
        returns (
            address arena,
            address token,
            address lineage,
            address gasToken,
            address shifu,
            address master,
            address delegationTarget
        )
    {
        return (address(ARENA), address(TOKEN), address(LINEAGE), address(GAS_TOKEN), SHIFU, MASTER, IMPL);
    }
}
