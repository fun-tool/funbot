const $ = (id) => document.getElementById(id);

const TOKEN = new URLSearchParams(location.search).get("t") || "";
if (TOKEN && window.history.replaceState) {
  window.history.replaceState(null, "", location.pathname);
}

function wipeSecrets() {
  for (const id of ["pw", "derivePassword", "deletePassword"]) {
    const el = $(id);
    if (el) el.value = "";
  }
}

async function api(path, body) {
  const r = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json", "X-Token": TOKEN },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
  return j;
}

const short = (a) => (a ? `${a.slice(0, 6)}…${a.slice(-4)}` : "—");
const esc = (t) =>
  String(t ?? "").replace(
    /[&<>"]/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c],
  );

let pointerTarget = null;
const renderedHTML = new WeakMap();
const deferredHTML = new Map();
let selectionFrame = 0;
let copyNoticeTimer = 0;

function preserveText(el) {
  if (pointerTarget && el.contains(pointerTarget)) return true;
  const selected = window.getSelection();
  if (!selected || selected.isCollapsed) return false;
  for (let i = 0; i < selected.rangeCount; i++) {
    if (selected.getRangeAt(i).intersectsNode(el)) return true;
  }
  return false;
}

function setHTML(id, html) {
  const el = $(id);
  if (renderedHTML.get(el) === html) { deferredHTML.delete(id); return true; }
  if (preserveText(el)) { deferredHTML.set(id, html); return false; }
  deferredHTML.delete(id);
  el.innerHTML = html;
  renderedHTML.set(el, html);
  return true;
}

function addressHTML(address, compact = false) {
  if (!/^0x[0-9a-f]{40}$/i.test(address || "")) return esc(address || "—");
  return `<span class="address-copy"><span class="ad address-text" title="${esc(address)}">${esc(compact ? short(address) : address)}</span><button type="button" class="copy-address" data-copy-address="${esc(address)}" aria-label="复制完整地址 ${esc(address)}" title="复制完整地址"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/></svg></button></span>`;
}

async function copyAddress(address) {
  try {
    if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
    await navigator.clipboard.writeText(address);
  } catch {
    const active = document.activeElement;
    const selected = window.getSelection();
    const ranges = selected ? Array.from({ length: selected.rangeCount }, (_, i) => selected.getRangeAt(i).cloneRange()) : [];
    const input = document.createElement("textarea");
    input.value = address;
    input.readOnly = true;
    input.className = "clipboard-buffer";
    document.body.append(input);
    let copied = false;
    try {
      input.select();
      copied = document.execCommand("copy");
    } finally {
      input.remove();
      active?.focus({ preventScroll: true });
      if (selected) {
        selected.removeAllRanges();
        ranges.forEach((range) => selected.addRange(range));
      }
    }
    if (!copied) throw new Error("copy failed");
  }
}

function copyNotice(message) {
  clearTimeout(copyNoticeTimer);
  const notice = $("copyNotice");
  notice.textContent = message;
  notice.hidden = false;
  copyNoticeTimer = setTimeout(() => { notice.hidden = true; }, 2500);
}

function resumeRendering() {
  if (selectionFrame) return;
  selectionFrame = requestAnimationFrame(() => {
    selectionFrame = 0;
    render();
    for (const [id, html] of deferredHTML) setHTML(id, html);
  });
}

function put(id, text, cls) {
  const el = $(id);
  if (!el) return;
  const t = String(text ?? "—");
  if (cls !== undefined) el.className = cls;
  if (el.textContent !== t && !preserveText(el)) el.textContent = t;
}

let V = null;
let acting = false;
let helpInitialized = false;
let sourceSignature = "";
let logPaused = false;
let logSignature = "";
function renderLog() {
  put(
    "consoleStatus",
    logPaused
      ? "显示已暂停 · 程序继续运行"
      : V?.busy ||
          (V?.batch?.running
            ? "批量任务执行中"
            : V?.locked
              ? "密钥已锁定 · 等待解锁"
              : "就绪 · 等待操作"),
  );
  if (logPaused) return;
  const lines = [...(V?.log || [])].reverse();
  const signature = JSON.stringify(lines);
  if (signature === logSignature) return;
  const log = $("log"),
    top = log.scrollTop;
  const html = lines.length
    ? lines
        .map(
          (l) =>
            `<div class="log-line"><time>${esc(l.at)}</time><span class="${["ok", "bad", "warn", "dim"].includes(l.kind) ? l.kind : ""}">${esc(l.text)}</span></div>`,
        )
        .join("")
    : '<div class="log-empty">等待执行记录。启动任务后，结果会显示在这里。</div>';
  if (!setHTML("log", html)) return;
  logSignature = signature;
  put("logCount", `${lines.length} 条记录 · 最近 200 条`);
  log.scrollTop = $("logFollow").checked ? log.scrollHeight : top;
}
const setupPicked = new Set();
let selectionAnchor = null;
let selectionDrag = null;
let suppressPickClick = false;
let mentors = {};
let mentorSignature = "";
let mentorsLoading = false;

function orderedSeats() {
  return [...(V?.seats || [])].sort(
    (a, b) =>
      (b.kind === "master") - (a.kind === "master") || a.index - b.index,
  );
}
function accountGroups() {
  let index = 0;
  const groups = new Map();
  const plays = $("cbMasterPlays").checked;
  for (const seat of orderedSeats()) {
    const group = seat.kind === "master" && !plays ? "master" : String(Math.floor(index++ / 24) + 1);
    groups.set(seat.address.toLowerCase(), group);
  }
  return groups;
}
function groupHeading(address, previous, scope, columns) {
  const groups = accountGroups(), group = groups.get(address.toLowerCase()) || "other";
  if (previous && groups.get(previous.toLowerCase()) === group) return "";
  const count = [...groups.values()].filter((g) => g === group).length;
  const name = group === "master" ? "主号 · 不参加比赛" : group === "other" ? "其他名册账号" : `第 ${group} 组 · ${count}/24 个账号`;
  const disabled = acting || V?.busy || V?.batch?.running || V?.locked ? " disabled" : "";
  return `<tr class="group-heading"><td colspan="${columns}"><strong>${esc(name)}</strong><span><button class="mini" data-group="${group}" data-scope="${scope}" data-select="yes"${disabled}>选本组</button> <button class="mini" data-group="${group}" data-scope="${scope}" data-select="no"${disabled}>取消本组</button></span></td></tr>`;
}
function selectAccountGroup(button) {
  if (acting || V?.busy || V?.batch?.running || V?.locked) return;
  const groups = accountGroups(), setup = button.dataset.scope === "setup";
  if (!setup && allSelected) { rosterRows().forEach((r) => picked.add(r.who.toLowerCase())); allSelected = false; }
  const chosen = setup ? setupPicked : picked;
  const available = setup ? orderedSeats().map((s) => s.address) : rosterRows().map((r) => r.who);
  for (const address of available) {
    const low = address.toLowerCase();
    if ((groups.get(low) || "other") !== button.dataset.group) continue;
    if (button.dataset.select === "yes") chosen.add(low); else chosen.delete(low);
  }
  render();
  if (!setup) savePreferences().catch(() => {});
}

function selectedSetupAddresses(includeMaster = true) {
  return orderedSeats()
    .filter((s) => setupPicked.has(s.address.toLowerCase()) && (includeMaster || s.kind !== "master"))
    .map((s) => s.address);
}
function rosterRows() {
  const seats = orderedSeats();
  const order = new Map(seats.map((s, i) => [s.address.toLowerCase(), i]));
  const labels = new Map(seats.map((s) => [s.address.toLowerCase(), s.label]));
  const master = (V?.master || "").toLowerCase();
  return [...(V?.rows || [])]
    .filter((r) => order.has(r.who.toLowerCase()))
    .sort((a, b) => {
      const x = a.who.toLowerCase(),
        y = b.who.toLowerCase();
      return (
        (y === master) - (x === master) ||
        (order.get(x) ?? Infinity) - (order.get(y) ?? Infinity) ||
        x.localeCompare(y)
      );
    })
    .map((r) => ({ ...r, label: labels.get(r.who.toLowerCase()) ?? r.label }));
}
function paintSelection() {
  document.querySelectorAll("#rows input.ck").forEach((ck) => {
    ck.checked = isPicked(ck.dataset.a);
    ck.closest("tr").classList.toggle("on", ck.checked);
  });
  updatePickInfo();
}
function selectInterval(start, end, checked, base) {
  const rows = rosterRows();
  const a = rows.findIndex((r) => r.who.toLowerCase() === start);
  const b = rows.findIndex((r) => r.who.toLowerCase() === end);
  if (a < 0 || b < 0) return;
  allSelected = false;
  picked.clear();
  base.forEach((a) => picked.add(a));
  rows.slice(Math.min(a, b), Math.max(a, b) + 1).forEach((r) => {
    if (checked) picked.add(r.who.toLowerCase());
    else picked.delete(r.who.toLowerCase());
  });
  paintSelection();
}
async function loadMentors(force = false) {
  if (!V || V.locked || tab !== "setup" || mentorsLoading) return;
  const signature = JSON.stringify(orderedSeats().map((s) => s.address));
  if (!force && signature === mentorSignature) return;
  mentorsLoading = true;
  mentorSignature = signature;
  try {
    mentors = await api("/api/mentors");
    mentorSignature = signature;
  } catch (e) {
    if (force) alert(e.message);
  } finally {
    mentorsLoading = false;
    render();
  }
}
function mentorText(address) {
  const a = mentors[address.toLowerCase()];
  if (!a) return mentorsLoading ? "读取中…" : "未读取 / 读取失败";
  if (/^0x0{40}$/i.test(a)) return "尚未拜师";
  const seat = (V?.seats || []).find(
    (s) => s.address.toLowerCase() === a.toLowerCase(),
  );
  return `${seat ? esc(seat.label) + "<br>" : ""}${addressHTML(a)}`;
}

const picked = new Set();
let allSelected = true;
let preferencesLoaded = false;
let savingPreferences = Promise.resolve();
function hydratePreferences() {
  if (preferencesLoaded || !V?.settings) return;
  preferencesLoaded = true;
  const p = V.settings;
  allSelected = p.all;
  (p.selected || []).forEach((a) => picked.add(a.toLowerCase()));
  $("cbMasterPlays").checked = p.masterPlays;
  $("entryMode").value = p.entryMode || "flexible";
  [...$("segRounds").children].forEach((b) =>
    b.classList.toggle("on", Number(b.dataset.v) === p.rounds),
  );
  if (p.contract && !$("inContract").value) $("inContract").value = p.contract;
}
function savePreferences() {
  const prefs = {
    masterPlays: $("cbMasterPlays").checked,
    entryMode: $("entryMode").value,
    rounds: Number($("segRounds").querySelector("button.on")?.dataset.v || 12),
    all: allSelected,
    selected: [...picked],
  };
  put("settingsStatus", "正在保存…");
  savingPreferences = savingPreferences
    .catch(() => {})
    .then(async () => {
      const state = await api("/api/settings", prefs);
      if (V) V.settings = state.settings;
      put("settingsStatus", "已保存，下次启动自动恢复");
    })
    .catch((e) => {
      put("settingsStatus", "保存失败：" + e.message);
      throw e;
    });
  return savingPreferences;
}
function isPicked(a) {
  return allSelected || picked.has(a.toLowerCase());
}
function actionSelection(play = false) {
  let rows = rosterRows().filter((r) => isPicked(r.who));
  if (play && !$("cbMasterPlays").checked)
    rows = rows.filter(
      (r) => r.who.toLowerCase() !== (V.master || "").toLowerCase(),
    );
  return rows.map((r) => r.who);
}

let tab = localStorage.getItem("squad.tab") || "play";

async function act(btn, path, body, confirmText) {
  if (acting) return;
  if (confirmText && !confirm(confirmText)) return;
  acting = true;
  const all = [...document.querySelectorAll("button")];
  const was = all.map((b) => b.disabled);
  all.forEach((b) => (b.disabled = true));
  const old = btn?.textContent;
  if (btn && old) btn.textContent = "…";
  try {
    V = await api(path, body ?? {});
    if (
      [
        "/api/prepare",
        "/api/vault/reload",
        "/api/unlock",
        "/api/lock",
      ].includes(path)
    ) {
      mentorSignature = "";
      mentors = {};
    }
    render();
  } catch (e) {
    alert(e.message);
  } finally {
    acting = false;
    all.forEach((b, i) => (b.disabled = was[i]));
    if (btn && old) btn.textContent = old;
    render();
  }
}

function showTab(which) {
  tab = which;
  localStorage.setItem("squad.tab", which);
  $("tab-play").hidden = which !== "play";
  $("tab-setup").hidden = which !== "setup";
  if (which === "setup") loadMentors();
  document
    .querySelectorAll(".tab")
    .forEach((b) => b.classList.toggle("on", b.dataset.tab === which));
}

function render() {
  if (!V || selectionDrag) return;
  const locked = V.locked;
  put("balanceStatus", V.balanceError
    ? `余额刷新失败，当前显示上次快照（${V.balancesUpdated || "尚无成功记录"}），请勿当作实时余额`
    : V.balancesUpdated ? `链上余额快照：${V.balancesUpdated}${V.batch?.running ? " · 每笔确认后更新" : ""}` : "等待读取链上余额", V.balanceError ? "bad" : "dim");
  hydratePreferences();

  put("net", V.net || "—", "chip " + (V.net ? "ok" : ""));
  put(
    "lockState",
    locked ? "锁着" : "已解锁",
    "chip " + (locked ? "no" : "ok"),
  );
  put("block", V.block ? V.block.toLocaleString("en-US") : "—", "chip");
  $("busy").hidden = !V.busy;
  if (V.busy) put("busy", V.busy, "chip warn");

  $("secUnlock").hidden = !(locked && V.hasVault);
  put("vaultPath", V.vaultPath);
  put("systemIdentity", `${V.systemName || "本地系统"} · 主号 ${short(V.master)}`);
  $("systemIdentity").title = V.dataDir || V.vaultPath || "";
  $("pendingBox").hidden = !(V.pending || V.pendingError);
  put("pendingText", V.pendingError || V.pending?.hash || "");
  for (const id of [
    "macStart",
    "windowsStart",
    "macMaster",
    "windowsMaster",
    "macMember",
    "windowsMember",
    "macVault",
    "windowsVault",
  ])
    put(id, V.importCommands?.[id]);
  put("cmdMaster", V.importCommands?.master || "funbot -add master");
  put("cmdMember", V.importCommands?.member || "funbot -add member");
  if (!helpInitialized) {
    $("importHelp").open = !V.hasVault;
    helpInitialized = true;
  }
  renderSources();
  $("setupBody").hidden = locked;
  $("secPrep").hidden = !V.contract;
  $("deployBox").hidden = false;

  renderLog();

  if (!locked && V.contract && !$("inContract").value)
    $("inContract").value = V.contract;
  const cfg = V.config;
  $("cfgBox").hidden = !cfg;
  if (cfg) {
    setHTML("cfgBox", [["擂台", cfg.arena], ["代币", cfg.token], ["师父", cfg.shifu], ["主号", cfg.master], ["委托目标", cfg.impl]]
      .map(([label, address]) => `<div class="config-address"><span>${label}</span>${addressHTML(address)}</div>`).join(""));
  }

  const rows = rosterRows();
  const seats = orderedSeats();
  const inRoster = new Set(rows.map((r) => r.who.toLowerCase()));
  const delegated = V.delegations || {};
  $("seatWrap").hidden = !seats.length;
  const yes = '<span class="pill ok">是</span>';
  const no = '<span class="pill no">否</span>';
  const available = new Set(seats.map((s) => s.address.toLowerCase()));
  for (const a of setupPicked) if (!available.has(a)) setupPicked.delete(a);
  for (const a of picked) if (!available.has(a)) picked.delete(a);
  put("delegateCount", `已选 ${setupPicked.size} / ${seats.length} 个`);
  const prepBusy = acting || !!V.busy || !!V.batch?.running || locked;
  $("btnDelegate").disabled = prepBusy || !setupPicked.size;
  $("btnRevokeDelegate").disabled = prepBusy || !setupPicked.size;
  $("btnDelegateAll").disabled = prepBusy || !seats.length;
  $("btnRoster").disabled = prepBusy || !selectedSetupAddresses($("cbMasterPlays").checked).length;
  $("btnRosterAll").disabled = prepBusy || !seats.length;
  document.querySelectorAll("#delegateSelection button, #delegateSelection input").forEach((el) => { el.disabled = prepBusy; });
  setHTML("seatRows", seats
    .map((s, i) => {
      const isM = s.kind === "master";
      const low = s.address.toLowerCase();
      return groupHeading(s.address, seats[i - 1]?.address, "setup", 6) + `<tr>
      <td><div class="chk account-name"><input type="checkbox" class="delegate-pick" data-a="${esc(s.address)}"${setupPicked.has(low) ? " checked" : ""}${prepBusy ? " disabled" : ""} aria-label="选择账号 ${i + 1}" /><span class="nm">${i + 1}. ${esc(s.label)}</span></div>${addressHTML(s.address)}</td>
      <td data-label="身份"><span class="pill ${isM ? "ok" : "dim"}">${isM ? "主号" : "队员"}</span></td>
      <td data-label="在名册">${inRoster.has(low) ? yes : no}</td>
      <td data-label="已委托" title="${esc(V.delegationError || '是否委托到当前合约，与加入名册无关')}">${delegated[low] === true ? yes : delegated[low] === false ? no : `<span class="pill dim">${V.delegationError ? '读取失败' : '待核对'}</span>`}</td>
      <td class="mentor" data-label="链上师父">${mentorText(s.address)}</td>
      <td class="act"><button class="mini seatrename" data-a="${esc(s.address)}"${acting || V.busy || V.batch?.running ? " disabled" : ""}>改名</button> <button class="mini seatrm" data-a="${esc(s.address)}">删</button></td>
    </tr>`;
    })
    .join(""));

  let blocked = "";
  if (locked) blocked = "密钥锁着 → 去「设置」解锁";
  else if (!V.contract) blocked = "还没连上合约 → 去「设置」载入或部署";
  else if (!V.masterReady) blocked = "主号还没委托 → 去「设置」点「① 委托」";
  else if (!rows.length)
    blocked = "名册是空的 → 去「设置」导入队员，再点「② 加入名册」";
  $("playBlocked").hidden = !blocked;
  $("playBlocked").textContent = blocked;
  $("playBody").hidden = !!blocked;

  const bt = V.batch || {};
  $("btnRun").hidden = !!bt.running;
  $("btnReapAll").hidden = !!bt.running;
  $("btnSweepGas").hidden = !!bt.running;
  $("btnFund").disabled = !!blocked || !!bt.running || !!V.busy || acting;
  $("fundAmount").disabled = !!bt.running || !!V.busy || acting;

  const gasSum = rows.reduce(
    (n, r) => n + Number(r.gasBalance || 0),
    0,
  );
  $("btnSweepGas").disabled = !!blocked || gasSum <= 0;
  $("btnSweepGas").className = gasSum > 0 ? "go pulse" : "";
  $("btnStop").hidden = !bt.running;

  const reapable = rows.reduce((n, r) => n + Number(r.ready || 0), 0);
  $("btnReapAll").className = reapable > 0 ? "go pulse" : "";
  $("btnReapAll").disabled =
    !!blocked ||
    !!bt.running ||
    !!V.busy ||
    (reapable <= 0 && !rows.some((r) => r.jiaziTruncated));
  $("runBar").hidden = !bt.running && !bt.harvested && !bt.rounds;
  if (bt.running || bt.harvested || bt.rounds) {
    const pct = bt.rounds ? Math.round((bt.round / bt.rounds) * 100) : 0;
    $("barFill").style.width = pct + "%";
    const bits = [];
    if (bt.running) {
      bits.push(
        bt.rounds ? `已完成 ${bt.round}/${bt.rounds} 场 · ${bt.step}` : bt.step,
      );
    } else
      bits.push(bt.reason || (bt.rounds === 1 && bt.round === 1 ? "本次处理完成" : "已结束"));
    if (Number(bt.entered)) bits.push(`已入场 ${bt.entered} 人次`);
    if (Number(bt.skipped)) bits.push(`已在局内跳过 ${bt.skipped} 个`);
    if (Number(bt.harvested)) bits.push(`收集 ${bt.harvested}`);
    put("barText", bits.join("　·　"));
  }

  if (!acting) {
    const running = !!bt.running || !!V.busy;
    for (const id of [
      "btnDeploy",
      "btnAttach",
      "btnLock",
      "btnPrepare",
      "btnReloadVault",
      "btnDeriveSaved",
      "cbMasterPlays",
      "entryMode",
    ])
      $(id).disabled = running;
    document
      .querySelectorAll(
        "#segRounds button, button[data-pick], input.ck, #ckAll, #btnPickRange, #pickFrom, #pickTo",
      )
      .forEach((el) => (el.disabled = running));
  }
  if (blocked) return;

  $("masterStandalone").hidden = $("cbMasterPlays").checked || rows.some((r) => r.who.toLowerCase() === (V.master || "").toLowerCase());
  setHTML("masterStandalone", `主号 · 不参加比赛　${addressHTML(V.master)}　盟主 ${esc(V.masterChampionships ?? "—")} 次　fLGNS ${esc(V.overview?.masterBalance || "—")}　Gas ${esc(V.overview?.masterGas || "—")}`);
  const ov = V.overview;
  if (ov) {
    put("mBal", ov.masterBalance, "v jade");
    put("mGas", ov.masterGas, "v");
    put("round", ov.roundId, "v");
  }
  updatePickInfo();

  const sum = rows.reduce((n, r) => n + Number(r.ready || 0), 0);
  put("sumTotal", sum.toFixed(2), "v " + (sum > 0 ? "jade" : "faint"));
  put("seats", `${rows[0].openSeats}/${rows[0].seats}`, "v");

  setHTML("rows", rows
    .map((r, index) => {
      const heading = groupHeading(r.who, rows[index - 1]?.who, "play", 11);
      const on = isPicked(r.who);
      const ck = `<td class="pk"><input type="checkbox" class="ck" aria-label="选择第 ${index + 1} 个账号" data-a="${r.who}"${on ? " checked" : ""}${bt.running || V.busy ? " disabled" : ""}></td>`;
      const who = `<div class="nm">${index + 1}. ${esc(r.label || short(r.who))}${r.who.toLowerCase() === (V.master || "").toLowerCase() ? " · 主号" : ""}</div>${addressHTML(r.who, true)}`;
      const championships = `<td data-label="盟主次数" class="n championships" title="${esc(r.championships == null ? V.championshipsError || '等待读取盟主次数' : '累计已裁决的盟主次数')}">${esc(r.championships ?? "—")}</td>`;
      if (!r.delegated) {
        return heading + `<tr>${ck}<td class="identity">${who}</td><td class="status" data-label="状态"><span class="pill no">未委托</span></td>
          <td data-label="场次" class="n">—</td>${championships}
          ${["钱包", "擂台", "师承", "甲子俸", "可收"].map((label) => `<td data-label="${label}" class="n">—</td>`).join("")}
          <td class="act"><button class="mini rm" data-a="${r.who}">移出</button></td></tr>`;
      }
      let state = '<span class="pill dim">空闲</span>';
      if (r.seated) state = `<span class="pill ok">第 ${r.seatNo} 席</span>`;
      else if (r.blocksToWait > 0)
        state = `<span class="pill wait">等 ${r.blocksToWait} 块</span>`;
      else if (r.matchesToday >= 12)
        state = '<span class="pill dim">打满</span>';

      const z = (v) => (Number(v) ? "n" : "n z");
      let reap = `<span>${r.ready}</span>`;
      if (Number(r.locked)) reap += `<small>+${r.locked} 压着</small>`;

      return heading + `<tr class="${on ? "on" : ""}">
        ${ck}
        <td class="identity">${who}</td>
        <td class="status" data-label="状态">${state}</td>
        <td data-label="场次" class="${r.matchesToday ? "n" : "n z"}">${r.matchesToday}/12</td>
        ${championships}
        <td data-label="钱包" class="${z(r.wallet)}">${r.wallet}</td>
        <td data-label="擂台" class="${z(r.settled)}">${r.settled}<small>已结算 · 可用于入场</small>${Number(r.unsettled) ? `<small>未结算 ${r.unsettled}</small>` : ""}</td>
        <td data-label="师承" class="${z(r.lineage)}">${r.lineage}</td>
        <td data-label="甲子俸" class="${z(r.jiazi)}">${r.jiazi}${r.jiaziTruncated ? "<small>含后续页，点收完整检查</small>" : ""}</td>
        <td data-label="可收" class="${Number(r.ready) ? "n big" : "n z"}">${reap}</td>
        <td class="act">
          <button class="mini play" data-a="${r.who}"
            ${bt.running || V.busy || r.seated || r.matchesToday >= 12 || (!$("cbMasterPlays").checked && r.who.toLowerCase() === (V.master || "").toLowerCase()) ? "disabled" : ""}>打</button>
          <button class="mini reap" data-a="${r.who}"
            ${(r.canHarvest || r.jiaziTruncated) && !bt.running && !V.busy ? "" : "disabled"}>收</button>
        </td></tr>`;
    })
    .join(""));
}

function updatePickInfo() {
  const rows = rosterRows();
  const on = rows.filter((r) => isPicked(r.who));

  put(
    "pickInfo",
    on.length === rows.length
      ? `全部 ${rows.length} 个`
      : `选了 ${on.length} / ${rows.length}`,
  );
  const ck = $("ckAll");
  if (ck) {
    ck.checked = rows.length > 0 && on.length === rows.length;
    ck.indeterminate = on.length > 0 && on.length < rows.length;
  }
}

function wire() {
  document.addEventListener("pointerdown", (ev) => { if (ev.button === 0) pointerTarget = ev.target; }, true);
  const releaseText = () => { pointerTarget = null; resumeRendering(); };
  document.addEventListener("pointerup", releaseText);
  document.addEventListener("pointercancel", releaseText);
  window.addEventListener("blur", releaseText);
  document.addEventListener("selectionchange", resumeRendering);
  document.addEventListener("click", async (ev) => {
    const button = ev.target.closest("button[data-copy-address]");
    if (!button) return;
    ev.preventDefault();
    ev.stopPropagation();
    try {
      await copyAddress(button.dataset.copyAddress);
      copyNotice("已复制完整地址");
    } catch {
      copyNotice("复制失败，请选中地址后手动复制");
    }
  }, true);

  const setGuide = (platform) => {
    for (const [key, suffix] of [
      ["mac", "Mac"],
      ["windows", "Windows"],
    ]) {
      $("guide" + suffix).hidden = key !== platform;
      $("guide" + suffix + "Button").classList.toggle("on", key === platform);
      $("guide" + suffix + "Button").setAttribute(
        "aria-pressed",
        String(key === platform),
      );
    }
  };
  $("guideMacButton").onclick = () => setGuide("mac");
  $("guideWindowsButton").onclick = () => setGuide("windows");
  setGuide(/Windows/i.test(navigator.userAgent) ? "windows" : "mac");
  $("btnLogPause").onclick = () => {
    logPaused = !logPaused;
    put("btnLogPause", logPaused ? "继续显示" : "暂停显示");
    renderLog();
  };
  $("logFollow").onchange = () => {
    if ($("logFollow").checked) $("log").scrollTop = $("log").scrollHeight;
  };
  $("log").addEventListener("scroll", () => {
    const el = $("log");
    if (el.scrollHeight - el.clientHeight - el.scrollTop > 40)
      $("logFollow").checked = false;
  });
  document
    .querySelectorAll(".tab")
    .forEach((b) => (b.onclick = () => showTab(b.dataset.tab)));

  $("btnUnlock").onclick = async (e) => {
    const pw = $("pw").value;
    $("pw").value = "";
    await act(e.target, "/api/unlock", { password: pw });
    if (V && !V.locked) showTab(V.contract && V.masterReady ? "play" : "setup");
  };
  $("btnLock").onclick = async (e) => {
    wipeSecrets();
    await act(e.target, "/api/lock", {});
  };

  $("btnAttach").onclick = (e) =>
    act(e.target, "/api/attach", { address: $("inContract").value });
  $("btnDeploy").onclick = (e) =>
    act(
      e.target,
      "/api/deploy",
      { shifu: $("inShifu").value },
      "部署一份新合约？\n\n用的是真钱。\n拜师永久不可改 —— 师父地址想清楚了吗？",
    );

  $("delegateAll").onclick = () => {
    orderedSeats().forEach((s) => setupPicked.add(s.address.toLowerCase()));
    render();
  };
  $("delegateNone").onclick = () => { setupPicked.clear(); render(); };
  $("delegateRange").onclick = () => {
    const seats = orderedSeats();
    const from = Number($("delegateFrom").value), to = Number($("delegateTo").value);
    if (!Number.isInteger(from) || !Number.isInteger(to) || from < 1 || to < from || to > seats.length)
      return alert(`请输入 1–${seats.length} 内的有效账号序号范围`);
    setupPicked.clear();
    seats.slice(from - 1, to).forEach((s) => setupPicked.add(s.address.toLowerCase()));
    render();
  };
  $("btnDelegate").onclick = (e) => {
    const addresses = selectedSetupAddresses();
    if (!addresses.length) return alert("请先在账号设置中选择要委托的账号");
    act(e.target, "/api/delegate", { addresses });
  };
  $("btnDelegateAll").onclick = (e) =>
    act(e.target, "/api/delegate", { addresses: [] }, `委托全部 ${orderedSeats().length} 个账号？已成功委托的会跳过。`);
  $("btnRevokeDelegate").onclick = (e) => {
    const addresses = selectedSetupAddresses();
    if (!addresses.length) return alert("请先选择要取消委托的账号");
    const includesMaster = addresses.some((a) => a.toLowerCase() === (V.master || "").toLowerCase());
    const message = `取消选中 ${addresses.length} 个账号的 EIP-7702 委托？\n\n主号支付本次 Gas。取消后这些账号不能再由机器人代操作；钱包与擂台资金保留，不会自动领取、归集或撤销代币授权。` +
      (includesMaster ? "\n\n本次包含主号：取消后整队批量操作暂停，重新委托主号后才能恢复。" : "\n\n仅取消选中的账号，不会自动取消主号委托。");
    act(e.target, "/api/delegate/revoke", { addresses }, message);
  };
  $("entryMode").onchange = () => savePreferences().catch(() => {});
  for (const id of ["rows", "seatRows"]) $(id).addEventListener("click", (event) => { const button = event.target.closest("button[data-group]"); if (button) selectAccountGroup(button); });
  $("cbMasterPlays").onchange = () => {
    savePreferences().catch(() => {});
    render();
  };
  const addToRoster = async (btn, all) => {
    const addresses = all ? [] : selectedSetupAddresses($("cbMasterPlays").checked);
    if (!all && !addresses.length)
      return alert("请先选择要加入名册的账号；主号仅在开启「主号参加比赛」时加入");
    try {
      await savePreferences();
      await act(btn, "/api/roster/add", { addresses }, all ? "把全部适用账号加入名册？主号是否加入取决于「主号参加比赛」设置。" : undefined);
    } catch (e) {
      alert(e.message);
    }
  };
  $("btnRoster").onclick = (e) => addToRoster(e.target, false);
  $("btnRosterAll").onclick = (e) => addToRoster(e.target, true);
  $("btnPrepare").onclick = (e) => { const addresses = selectedSetupAddresses($("cbMasterPlays").checked); if (!addresses.length) return alert("请选择要拜师授权的账号"); act(e.target, "/api/prepare", { addresses }); };

  for (const id of ["segRounds"]) {
    $(id).addEventListener("click", (ev) => {
      const b = ev.target.closest("button");
      if (!b) return;
      [...$(id).children].forEach((x) => x.classList.toggle("on", x === b));
      savePreferences().catch(() => {});
    });
  }
  const segVal = (id) => $(id).querySelector("button.on")?.dataset.v || "0";

  $("btnRun").onclick = async (e) => {
    try {
      await savePreferences();
    } catch (e) {
      return alert(e.message);
    }
    const n = segVal("segRounds");
    const who = actionSelection(true);
    const cnt = who.length;
    if (!cnt) return alert("一个号都没选");
    act(
      e.target,
      "/api/batch/start",
      { rounds: Number(n), who, entryMode: $("entryMode").value },
      `${cnt} 个号，每号再打最多 ${n} 场，${$("entryMode").value === "together" ? "等齐再打：全部满足入场条件且整批模拟通过才发送，不自动拆分" : "能打就打：先处理可执行账号，Gas 超限时分批；选 1 不等待补打"}。\n跨组选中可以合并为一笔交易；区块条件成熟后可在入场时顺带裁决。按实际执行顺序补币，不领取其他收益、不归集。`,
    );
  };

  $("btnFund").onclick = (e) => {
    const who = actionSelection().filter(
      (a) => a.toLowerCase() !== (V?.master || "").toLowerCase(),
    );
    if (!who.length) return alert("请至少勾选一个队员，主号不向自己转账");
    const amountEach = $("fundAmount").value.trim();
    if (
      !/^[0-9]+(?:\.[0-9]{1,18})?$/.test(amountEach) ||
      amountEach.length > 80
    )
      return alert("数量须为正数，最多 18 位小数");
    const [whole, fraction = ""] = amountEach.split(".");
    const wei = BigInt(whole) * 10n ** 18n + BigInt(fraction.padEnd(18, "0"));
    const total = wei * BigInt(who.length);
    if (wei <= 0n || total >= 2n ** 256n) return alert("转入数量无效或过大");
    const decimals = (total % 10n ** 18n)
      .toString()
      .padStart(18, "0")
      .replace(/0+$/, "");
    const totalText =
      (total / 10n ** 18n).toString() + (decimals ? "." + decimals : "");
    act(
      e.target,
      "/api/fund",
      { who, amountEach },
      `向 ${who.length} 个队员各转入 ${amountEach} fLGNS，合计 ${totalText} fLGNS？\n\n一笔交易完成，加到现有余额上，不会自动入场。重复点击会再次转入。`,
    );
  };

  $("btnReapAll").onclick = (e) => {
    const who = actionSelection();
    if (!who.length) return alert("请先选择账号");
    const rows = rosterRows().filter((r) => who.includes(r.who));
    updatePickInfo();

    const sum = rows.reduce((n, r) => n + Number(r.ready || 0), 0);
    act(
      e.target,
      "/api/batch/harvest",
      { who },
      `把 ${rows.length} 个号的钱收回主号？\n\n现在可收 ${sum.toFixed(2)}。\n会领取甲子俸、师徒收益、擂台回款，并把钱包全部可用 fLGNS 转回主号。\n只处理勾选地址；结算有积压或甲子俸跨页时可能发送多笔交易。`,
    );
  };

  $("pickInfo").parentElement.addEventListener("click", (ev) => {
    const b = ev.target.closest("button[data-pick]");
    if (!b) return;
    const rows = rosterRows();
    const key = (a) => a.toLowerCase();
    allSelected = b.dataset.pick === "all";
    switch (b.dataset.pick) {
      case "all":
        rows.forEach((r) => picked.add(key(r.who)));
        break;
      case "none":
        picked.clear();
        break;
      case "idle":
        picked.clear();
        rows
          .filter((r) => !r.seated && r.matchesToday < 12)
          .forEach((r) => picked.add(key(r.who)));
        break;
      case "ready":
        picked.clear();
        rows.filter((r) => r.canHarvest).forEach((r) => picked.add(key(r.who)));
        break;
      case "firstN": {
        const n = Math.max(1, Number($("pickN").value) || 1);
        picked.clear();
        rows.slice(0, n).forEach((r) => picked.add(key(r.who)));
        break;
      }
    }
    savePreferences().catch(() => {});
    render();
  });

  $("btnPickRange").onclick = () => {
    if (acting || V?.busy || V?.batch?.running) return;
    const rows = rosterRows(),
      from = Number($("pickFrom").value),
      to = Number($("pickTo").value);
    if (
      !Number.isInteger(from) ||
      !Number.isInteger(to) ||
      from < 1 ||
      to < from ||
      to > rows.length
    )
      return alert(`请输入 1–${rows.length} 内的起止序号`);
    selectInterval(
      rows[from - 1].who.toLowerCase(),
      rows[to - 1].who.toLowerCase(),
      true,
      new Set(),
    );
    selectionAnchor = rows[from - 1].who.toLowerCase();
    savePreferences().catch(() => {});
  };
  $("rows").addEventListener("pointerdown", (ev) => {
    const ck = ev.target.closest("input.ck");
    if (
      !ck ||
      ck.disabled ||
      ev.button !== 0 ||
      acting ||
      V?.busy ||
      V?.batch?.running
    )
      return;
    ev.preventDefault();
    const a = ck.dataset.a.toLowerCase();
    selectionDrag = {
      start: ev.shiftKey && selectionAnchor ? selectionAnchor : a,
      end: a,
      checked: !ck.checked,
      base: new Set(
        rosterRows()
          .filter((r) => isPicked(r.who))
          .map((r) => r.who.toLowerCase()),
      ),
      x: ev.clientX,
      y: ev.clientY,
    };
    selectInterval(
      selectionDrag.start,
      a,
      selectionDrag.checked,
      selectionDrag.base,
    );
    const advance = () => {
      const d = selectionDrag;
      if (!d) return;
      const delta = d.y > innerHeight - 55 ? 16 : d.y < 75 ? -16 : 0;
      if (delta) window.scrollBy(0, delta);
      const row = document
        .elementFromPoint(d.x, Math.max(0, Math.min(innerHeight - 1, d.y)))
        ?.closest("#rows tr");
      const target = row?.querySelector("input.ck");
      if (target) {
        d.end = target.dataset.a.toLowerCase();
        selectInterval(d.start, d.end, d.checked, d.base);
      }
      requestAnimationFrame(advance);
    };
    requestAnimationFrame(advance);
  });
  document.addEventListener("pointermove", (ev) => {
    if (selectionDrag) {
      selectionDrag.x = ev.clientX;
      selectionDrag.y = ev.clientY;
    }
  });
  const finishSelection = () => {
    if (!selectionDrag) return;
    selectionAnchor = selectionDrag.start;
    selectionDrag = null;
    suppressPickClick = true;
    setTimeout(() => {
      suppressPickClick = false;
      paintSelection();
    }, 0);
    savePreferences().catch(() => {});
  };
  document.addEventListener("pointerup", finishSelection);
  document.addEventListener("pointercancel", finishSelection);
  window.addEventListener("blur", finishSelection);

  $("ckAll").onclick = () => {
    allSelected = $("ckAll").checked;
    picked.clear();
    savePreferences().catch(() => {});
    render();
  };
  $("btnStop").onclick = (e) => act(e.target, "/api/batch/stop", {});

  $("btnSweepGas").onclick = (e) => {
    const who = actionSelection();
    if (!who.length) return alert("请先选择账号");
    const rows = rosterRows().filter((r) => who.includes(r.who));
    const sum = rows.reduce((n, r) => n + Number(r.gasBalance || 0), 0);
    if (sum <= 0) return alert("这些号手上没有 gas 币");
    act(
      e.target,
      "/api/sweepgas",
      { who },
      `把 ${rows.length} 个号手上的 gas 币收回主号？\n\n一共 ${sum.toFixed(6)} DAI。`,
    );
  };

  $("rows").addEventListener("click", (ev) => {
    const ck = ev.target.closest("input.ck");
    if (ck) {
      if (suppressPickClick) {
        ev.preventDefault();
        return;
      }
      if (acting || V?.busy || V?.batch?.running) {
        ev.preventDefault();
        return;
      }
      const a = ck.dataset.a.toLowerCase();
      const base = new Set(
        rosterRows()
          .filter((r) => isPicked(r.who))
          .map((r) => r.who.toLowerCase()),
      );
      selectInterval(
        ev.shiftKey && selectionAnchor ? selectionAnchor : a,
        a,
        ck.checked,
        base,
      );
      selectionAnchor = a;
      savePreferences().catch(() => {});
      return;
    }
    const b = ev.target.closest("button");
    if (!b) return;
    const a = b.dataset.a;
    if (b.classList.contains("play")) act(b, "/api/play", { address: a });
    else if (b.classList.contains("reap"))
      act(b, "/api/harvest", { address: a });
    else if (b.classList.contains("rm"))
      act(b, "/api/roster/remove", { address: a }, `移出名册？`);
  });

  $("btnMentors").onclick = () => loadMentors(true);
  $("seatRows").addEventListener("click", async (ev) => {
    if (acting || V?.busy || V?.batch?.running) return;
    const pick = ev.target.closest("input.delegate-pick");
    if (pick) {
      const address = pick.dataset.a.toLowerCase();
      if (pick.checked) setupPicked.add(address); else setupPicked.delete(address);
      render();
      return;
    }
    const rename = ev.target.closest("button.seatrename");
    if (rename) {
      const seat = (V?.seats || []).find(
        (s) => s.address.toLowerCase() === rename.dataset.a.toLowerCase(),
      );
      const label = prompt("新的账号名称（最多 80 个字符）", seat?.label || "");
      if (label === null || label.trim() === seat?.label) return;
      if (!label.trim() || [...label.trim()].length > 80)
        return alert("名称需为 1–80 个字符");
      const password = await askPassword(
        "输入密钥文件密码以加密保存新名称",
        "保存名称",
      );
      if (password)
        await act(rename, "/api/seat/rename", {
          address: rename.dataset.a,
          label: label.trim(),
          password,
        });
      return;
    }
    const b = ev.target.closest("button.seatrm");
    if (!b) return;
    if (
      !confirm(
        `删掉 ${short(b.dataset.a)}？\n删除后不再显示或参与本程序操作。链上名册、7702 委托和资产不会自动移除；取消委托请在删除前单独操作。`,
      )
    )
      return;
    const pw = await askPassword();
    if (!pw) return;
    act(b, "/api/seat/remove", { address: b.dataset.a, password: pw });
  });
}

async function tick() {
  if (selectionDrag) return;
  try {
    V = await api("/api/state");
    if (acting) { renderLog(); return; }
    render();
    if (tab === "setup" && !V.locked) loadMentors();
  } catch {
    put("lockState", "读不到", "chip no");
  }
}

window.addEventListener("load", async () => {
  wire();
  wireDerivation();
  showTab(tab);
  await tick();
  if (V?.locked || !V?.hasVault) showTab("setup");
  setInterval(() => {
    if (!document.hidden) tick();
  }, 1000);
});

function renderSources() {
  const sources = V?.mnemonicSources || [];
  $("noMnemonic").hidden = sources.length > 0;
  $("deriveBody").hidden = !sources.length;
  const signature = JSON.stringify(sources);
  if (signature === sourceSignature) return;
  sourceSignature = signature;
  const old = $("deriveSource").value;
  $("deriveSource").innerHTML = sources
    .map(
      (s) =>
        `<option value="${esc(s.id)}">${esc(s.label || "助记词")} · ${s.wallets.length} 个钱包 · ${esc(short(s.id))}</option>`,
    )
    .join("");
  if (sources.some((s) => s.id === old)) $("deriveSource").value = old;
  showSourceWallets();
}

function showSourceWallets() {
  const source = (V?.mnemonicSources || []).find(
    (s) => s.id === $("deriveSource").value,
  );
  if (!source) return;
  $("deriveStart").value = source.nextIndex;
  $("deriveLabel").value = source.label || "派生钱包";
  setHTML("derivedWallets", source.wallets
    .map(
      (w) =>
        `<tr><td>${esc(w.label)}</td><td>${addressHTML(w.address)}<div class="ad">${esc(w.path)}</div></td></tr>`,
    )
    .join(""));
}

function wireDerivation() {
  $("deriveSource").onchange = () => {
    $("derivePassword").value = "";
    showSourceWallets();
  };
  $("btnDeriveSaved").onclick = async (e) => {
    if (acting) return;
    const body = {
      source: $("deriveSource").value,
      start: Number($("deriveStart").value),
      count: Number($("deriveCount").value),
      label: $("deriveLabel").value,
      password: $("derivePassword").value,
    };
    if (!body.source) return alert("请先在终端导入助记词并解锁");
    if (!body.password) return alert("请输入密钥文件密码");
    $("derivePassword").value = "";
    await act(e.target, "/api/derive", body);
  };
  $("btnReloadVault").onclick = async (e) => {
    wipeSecrets();
    await act(e.target, "/api/vault/reload", {});
    if (V?.hasVault) $("pw").focus();
  };
  $("pw").onkeydown = (e) => {
    if (e.key === "Enter") $("btnUnlock").click();
  };
}
window.addEventListener("pagehide", wipeSecrets);

function askPassword(
  message = "输入密钥文件密码以确认删除",
  button = "确认删除",
) {
  put("passwordPrompt", message);
  put("passwordConfirm", button);
  const dialog = $("passwordDialog"),
    input = $("deletePassword");
  input.value = "";
  return new Promise((resolve) => {
    dialog.addEventListener(
      "close",
      () => {
        const value = dialog.returnValue === "confirm" ? input.value : "";
        input.value = "";
        resolve(value);
      },
      { once: true },
    );
    dialog.returnValue = "cancel";
    dialog.showModal();
    input.focus();
  });
}

$("btnRecoverTransaction").addEventListener("click", (ev) =>
  act(ev.target, "/api/transaction/recover", {}),
);
