const body = document.body;
const $ = (id) => document.getElementById(id);


function call(method, ...args) {
  if (!window.mygo) {
    return Promise.resolve(undefined);
  }
  return window.mygo.call(method, ...args);
}

let expanded = false;
let snapshot = { qoderRunning: false, sessions: [], notices: [] };
let selectedID = null;
let noticeMode = false;
let lastNoticeKey = "";
let lastFocusBeforeNotice = null;
let polling = false;
let toastTimer = 0;
let switchDir = 1;
let lastRenderedID = null;
let switchTimer = 0;
let sparkSignature = null;
let manualUntil = 0;
let prevActive = new Set();
let wheelAccum = 0;
let wheelCooldown = false;
let wheelQuiet = 0;

const ACTIVE_STATES = new Set([
  "starting",
  "thinking",
  "tool",
  "running",
  "waiting-input",
  "permission",
  "queued",
  "reconnecting",
]);

function isActiveSession(session) {
  return !!session && ACTIVE_STATES.has(session.state);
}

function ruleOn(id, fallback = true) {
  const rules = snapshot.rules;
  if (rules && typeof rules[id] === "boolean") {
    return rules[id];
  }
  return fallback;
}

function setMetric(id, text) {
  const el = $(id);
  if (!el || el.textContent === text) {
    return;
  }
  el.textContent = text;
  el.classList.remove("flash");
  void el.offsetWidth;
  el.classList.add("flash");
}

function applyExpanded(next) {
  expanded = !!next;
  body.classList.toggle("expanded", expanded);
}

// Native code calls this when it changes the mode on its own, e.g. when an
// outside click collapses the notch.
window.__nonoExpanded = (next) => {
  applyExpanded(next);
  if (!next) {
    body.classList.remove("collapsing");
  }
};

// Native code adds the exit phase before shrinking the window around the card.
window.__nonoPhase = (phase) => {
  body.classList.toggle("collapsing", phase === "collapsing");
};

async function toggle() {
  if (expanded) {
    await collapse();
    return;
  }
  try {
    await call("Notch.Toggle");
  } catch (err) {
    console.error(err);
  }
  // The window and page expansion are driven by native code; fall back to the
  // poll if its sync is lost.
}

async function collapse() {
  if (!expanded) {
    return;
  }
  try {
    await call("Notch.Collapse");
  } catch (err) {
    console.error(err);
  }
  // Native code fades the card, shrinks the window, then applies the collapsed
  // layout. The poll also reconciles if that sync is lost.
}

function sessions() {
  return snapshot.sessions || [];
}

function notices() {
  return snapshot.notices || [];
}

function sessionByID(id) {
  return sessions().find((session) => session.id === id) || null;
}

const FALLBACK_ICON = "./assets/nono-32.png";

function iconFor(agentId) {
  return agentId ? "./assets/" + agentId + ".png" : FALLBACK_ICON;
}

function noticeKey(notice) {
  return notice ? notice.sessionId + "|" + notice.kind + "|" + notice.since : "";
}

function queueText(queue) {
  if (queue.length === 0) {
    return "";
  }
  const label = queue[0].label || "通知";
  return queue.length > 1 ? label + " +" + (queue.length - 1) : label;
}

function applySnapshot(data) {
  if (!data) {
    return;
  }
  snapshot = data;
  if (typeof data.expanded === "boolean") {
    applyExpanded(data.expanded);
  }
  const list = sessions();
  const queue = notices();
  const first = queue[0] || null;
  const key = noticeKey(first);

  if (first) {
    if (key !== lastNoticeKey) {
      if (!noticeMode) {
        lastFocusBeforeNotice = selectedID;
      }
      noticeMode = true;
      selectedID = first.sessionId;
      switchDir = 1;
      lastNoticeKey = key;
    }
  } else {
    if (noticeMode) {
      noticeMode = false;
      const restore = lastFocusBeforeNotice;
      selectedID =
        restore && sessionByID(restore)
          ? restore
          : data.focusedId || (list[0] && list[0].id) || null;
      lastFocusBeforeNotice = null;
    }
    lastNoticeKey = "";
  }

  if (!selectedID || !sessionByID(selectedID)) {
    selectedID =
      first && sessionByID(first.sessionId)
        ? first.sessionId
        : data.focusedId || (list[0] && list[0].id) || null;
  }

  // Follow the agent that is actually working. A manual pick from the arrows
  // is respected for a while, but a conversation that newly starts running
  // still takes over.
  const activeIDs = new Set(list.filter(isActiveSession).map((session) => session.id));
  let newlyActive = false;
  for (const id of activeIDs) {
    if (!prevActive.has(id)) {
      newlyActive = true;
      break;
    }
  }
  if (ruleOn("autoFollow", true) && !noticeMode && (Date.now() >= manualUntil || newlyActive)) {
    const selected = sessionByID(selectedID);
    if (!isActiveSession(selected)) {
      const running = list.find(isActiveSession);
      if (running && running.id !== selectedID) {
        const from = list.findIndex((session) => session.id === selectedID);
        const to = list.findIndex((session) => session.id === running.id);
        switchDir = from >= 0 && to < from ? -1 : 1;
        selectedID = running.id;
      }
    }
  }
  prevActive = activeIDs;

  render();
}

function cycle(dir) {
  const list = sessions();
  if (list.length < 2) {
    return;
  }
  let index = list.findIndex((session) => session.id === selectedID);
  if (index < 0) {
    index = 0;
  }
  selectedID = list[(index + dir + list.length) % list.length].id;
  switchDir = dir;
  noticeMode = false;
  manualUntil = Date.now() + 30000;
  render();
}

function dotClass(session) {
  if (!session) {
    return "idle";
  }
  switch (session.state) {
    case "permission":
      return "warn";
    case "waiting-input":
      return "waiting";
    case "reconnecting":
      return "waiting";
    case "failed":
    case "reconnect-failed":
      return "fail";
    case "starting":
    case "thinking":
    case "tool":
    case "running":
    case "queued":
      return "running";
    default:
      return "idle";
  }
}

function chipClass(session) {
  if (!session) {
    return "idle";
  }
  switch (session.state) {
    case "permission":
      return "warn";
    case "waiting-input":
      return "waiting";
    case "reconnecting":
      return "waiting";
    case "failed":
    case "reconnect-failed":
      return "fail";
    case "starting":
    case "thinking":
    case "tool":
    case "running":
    case "queued":
      return "running";
    default:
      return "idle";
  }
}

function formatMs(ms) {
  if (!ms || ms <= 0) {
    return "—";
  }
  if (ms < 1000) {
    return ms + "ms";
  }
  if (ms < 60000) {
    return (ms / 1000).toFixed(1) + "s";
  }
  const minutes = Math.floor(ms / 60000);
  const seconds = Math.round((ms % 60000) / 1000);
  return minutes + "m " + seconds + "s";
}

function formatTPS(value) {
  if (!value || value <= 0) {
    return "—";
  }
  return Math.round(value) + " t/s";
}

function formatTokens(value) {
  if (!value || value <= 0) {
    return "—";
  }
  if (value < 1000) {
    return String(value);
  }
  if (value < 1000000) {
    return (value / 1000).toFixed(1) + "k";
  }
  return (value / 1000000).toFixed(1) + "M";
}

function ttftOf(session) {
  if (session.live && session.live.ttftMs > 0) {
    return session.live.ttftMs;
  }
  return session.avgTtftMs;
}

function tpsOf(session) {
  if (session.live && session.live.tps > 0) {
    return session.live.tps;
  }
  if (session.recent && session.recent.length > 0 && session.recent[0].tps > 0) {
    return session.recent[0].tps;
  }
  return session.avgTps;
}

function postFirstOf(session) {
  if (session.live && session.live.postFirstMs > 0) {
    return session.live.postFirstMs;
  }
  if (session.recent && session.recent.length > 0) {
    return session.recent[0].postFirstMs;
  }
  return session.avgPostFirstMs;
}

function renderSpark(session) {
  const spark = $("spark");
  const recent = session && session.recent ? session.recent.slice(0, 5).reverse() : [];
  const values = recent.map((turn) => turn.tps || 0);
  const signature = values.join(",");
  if (signature === sparkSignature) {
    return;
  }
  sparkSignature = signature;
  spark.textContent = "";
  if (values.length === 0 || values.every((value) => value <= 0)) {
    const empty = document.createElement("span");
    empty.className = "spark-empty";
    empty.textContent = "—";
    spark.appendChild(empty);
    return;
  }
  const max = Math.max(...values, 1);
  values.forEach((value, index) => {
    const bar = document.createElement("i");
    bar.style.height = Math.max(3, Math.round((value / max) * 18)) + "px";
    bar.style.animationDelay = index * 45 + "ms";
    spark.appendChild(bar);
  });
}

function render() {
  const list = sessions();
  const session = sessionByID(selectedID);
  const queue = notices();
  const notice = queue.find((item) => item.sessionId === selectedID) || null;
  const index = session ? list.indexOf(session) : -1;
  const disabled = !!snapshot.disabled;
  const agents = snapshot.agents || [];
  const runningAgent = agents.find((agent) => agent.running) || null;
  const anyRunning =
    typeof snapshot.anyRunning === "boolean" ? snapshot.anyRunning : !!snapshot.qoderRunning;
  const activeAgent = session ? session.agent : runningAgent ? runningAgent.name : "";
  const activeIcon = disabled
    ? FALLBACK_ICON
    : session
      ? iconFor(session.agentId)
      : runningAgent
        ? iconFor(runningAgent.id)
        : FALLBACK_ICON;
  const idle = anyRunning ? "空闲" : "未运行";
  const showClock = ruleOn("showClock", true);
  body.classList.toggle("no-clock", !showClock);
  $("clock").hidden = !showClock;
  $("clock-detail").hidden = !showClock;

  $("notch").dataset.tone = disabled
    ? "off"
    : session
      ? dotClass(session)
      : anyRunning
        ? "idle"
        : "off";

  $("bar-logo").src = activeIcon;
  $("detail-logo").src = activeIcon;
  $("dot").className = "dot " + (disabled ? "idle" : dotClass(session));
  $("bar-project").textContent = disabled
    ? "Nono"
    : session
      ? session.project
      : runningAgent
        ? runningAgent.name
        : "Nono";
  $("bar-state").textContent = disabled ? "未启用" : session ? session.label : idle;
  $("bar-metric-1").textContent = disabled || !session ? "—" : "首字 " + formatMs(ttftOf(session));
  $("bar-metric-2").textContent = disabled || !session ? "—" : formatTPS(tpsOf(session));

  const badge = $("badge");
  if (!disabled && queue.length > 0) {
    badge.hidden = false;
    badge.textContent = queueText(queue);
  } else {
    badge.hidden = true;
  }

  $("title").textContent = disabled
    ? "未启用任何 agent"
    : session
      ? session.title
      : activeAgent
        ? activeAgent + " " + idle
        : idle;
  $("sub").textContent =
    disabled || !session ? "" : session.project + " · " + (session.model || session.agent);
  $("position").textContent = disabled || !session || list.length < 2 ? "" : index + 1 + " / " + list.length;

  const chip = $("chip");
  chip.className = "chip " + (disabled ? "idle" : chipClass(session));
  chip.textContent = disabled ? "未启用" : session ? session.label : idle;

  const hint = $("hint");
  hint.textContent = snapshot.error
    ? snapshot.error
    : disabled
      ? "在设置中启用要监控的 agent"
      : session
        ? session.detail || ""
        : "";
  hint.classList.toggle("error", !!snapshot.error);

  const queueLabel = $("queue");
  queueLabel.hidden = queue.length === 0;
  queueLabel.textContent = queueText(queue);

  setMetric("m-ttft", session ? formatMs(session.avgTtftMs) : "—");
  setMetric("m-post", session ? formatMs(postFirstOf(session)) : "—");
  setMetric("m-tps", session ? formatTPS(tpsOf(session)) : "—");
  setMetric(
    "m-tokens",
    session && session.recent && session.recent[0]
      ? formatTokens(session.recent[0].outputTokens)
      : "—"
  );
  renderSpark(session);

  $("actions").hidden = disabled || !anyRunning;
  $("open-qoder").hidden = !!notice;
  $("open-qoder").textContent = "打开 " + (activeAgent || "应用");
  $("handle").hidden = !notice;
  $("handle").textContent = notice && notice.kind === "completed" ? "查看" : "去处理";
  $("skip").hidden = !notice;
  $("prev").style.visibility = !disabled && list.length > 1 ? "visible" : "hidden";
  $("next").style.visibility = !disabled && list.length > 1 ? "visible" : "hidden";

  if (selectedID !== lastRenderedID) {
    if (lastRenderedID !== null) {
      const notch = $("notch");
      notch.classList.remove("switch-left", "switch-right");
      void notch.offsetWidth;
      notch.classList.add(switchDir < 0 ? "switch-right" : "switch-left");
      clearTimeout(switchTimer);
      switchTimer = setTimeout(() => {
        notch.classList.remove("switch-left", "switch-right");
      }, 360);
    }
    lastRenderedID = selectedID;
  }
}

async function refresh() {
  if (polling) {
    return;
  }
  polling = true;
  try {
    const data = await call("Notch.Snapshot");
    applySnapshot(data);
  } catch (err) {
    console.error(err);
  } finally {
    polling = false;
  }
}

function showToast(text) {
  const toast = $("toast");
  toast.textContent = text;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toast.hidden = true;
  }, 2200);
}

async function handleNotice() {
  const notice = notices().find((item) => item.sessionId === selectedID) || notices()[0];
  if (!notice) {
    return;
  }
  let ok = false;
  try {
    ok = await call("Notch.Handle", notice.sessionId);
  } catch (err) {
    console.error(err);
  }
  if (ok) {
    await collapse();
  } else {
    showToast("没有把 Qoder 切到前台");
  }
  setTimeout(refresh, 120);
}

async function skipNotice() {
  const notice = notices().find((item) => item.sessionId === selectedID) || notices()[0];
  if (!notice) {
    return;
  }
  try {
    await call("Notch.Skip", notice.sessionId);
  } catch (err) {
    console.error(err);
  }
  setTimeout(refresh, 120);
}

async function openAgent() {
  let ok = false;
  try {
    ok = await call("Notch.OpenAgent", selectedID || "");
  } catch (err) {
    console.error(err);
  }
  if (ok) {
    await collapse();
  } else {
    showToast("没有把应用切到前台");
  }
}

document.addEventListener("contextmenu", (event) => {
  event.preventDefault();
  call("Notch.ShowMenu", Math.round(event.clientX), Math.round(event.clientY));
});

$("center").addEventListener("click", (event) => {
  if (event.target.closest("button")) {
    return;
  }
  toggle();
});

$("detail").addEventListener("click", (event) => {
  if (event.target.closest("button, a, input, textarea, select, [data-interactive]")) {
    return;
  }
  collapse();
});

$("prev").addEventListener("click", (event) => {
  event.stopPropagation();
  cycle(-1);
});

$("next").addEventListener("click", (event) => {
  event.stopPropagation();
  cycle(1);
});

// One wheel gesture switches exactly one conversation. Momentum events keep
// the gesture locked until the wheel goes quiet for a moment.
document.addEventListener(
  "wheel",
  (event) => {
    if (!expanded) {
      return;
    }
    event.preventDefault();
    let delta = event.deltaY;
    if (event.deltaMode === 1) {
      delta *= 16;
    } else if (event.deltaMode === 2) {
      delta *= 100;
    }
    clearTimeout(wheelQuiet);
    wheelQuiet = setTimeout(() => {
      wheelAccum = 0;
      wheelCooldown = false;
    }, 220);
    if (wheelCooldown) {
      return;
    }
    wheelAccum += delta;
    if (Math.abs(wheelAccum) < 30) {
      return;
    }
    cycle(wheelAccum > 0 ? 1 : -1);
    wheelAccum = 0;
    wheelCooldown = true;
  },
  { passive: false }
);

$("open-qoder").addEventListener("click", (event) => {
  event.stopPropagation();
  openAgent();
});

$("handle").addEventListener("click", (event) => {
  event.stopPropagation();
  handleNotice();
});

$("skip").addEventListener("click", (event) => {
  event.stopPropagation();
  skipNotice();
});

let lastClock = "";

function updateClock() {
  const now = new Date();
  const text =
    String(now.getHours()).padStart(2, "0") + ":" + String(now.getMinutes()).padStart(2, "0");
  const changed = text !== lastClock;
  lastClock = text;
  for (const id of ["clock", "clock-detail"]) {
    const el = $(id);
    if (!el) {
      continue;
    }
    el.textContent = text;
    if (changed) {
      el.classList.remove("tick");
      void el.offsetWidth;
      el.classList.add("tick");
    }
  }
}

updateClock();
setInterval(updateClock, 1000);
refresh();
setInterval(refresh, 700);
