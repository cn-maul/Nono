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

function applyExpanded(next) {
  expanded = !!next;
  body.classList.toggle("expanded", expanded);
}

async function toggle() {
  let next;
  try {
    next = await call("Notch.Toggle");
  } catch (err) {
    console.error(err);
  }
  if (typeof next !== "boolean") {
    next = !expanded;
  }
  applyExpanded(next);
}

async function collapse() {
  if (!expanded) {
    return;
  }
  let next;
  try {
    next = await call("Notch.Collapse");
  } catch (err) {
    console.error(err);
  }
  if (typeof next !== "boolean") {
    next = false;
  }
  applyExpanded(next);
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

function noticeKey(notice) {
  return notice ? notice.sessionId + "|" + notice.kind + "|" + notice.since : "";
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
  spark.textContent = "";
  const recent = session && session.recent ? session.recent.slice(0, 5).reverse() : [];
  const values = recent.map((turn) => turn.tps || 0);
  if (values.length === 0 || values.every((value) => value <= 0)) {
    const empty = document.createElement("span");
    empty.className = "spark-empty";
    empty.textContent = "—";
    spark.appendChild(empty);
    return;
  }
  const max = Math.max(...values, 1);
  for (const value of values) {
    const bar = document.createElement("i");
    bar.style.height = Math.max(3, Math.round((value / max) * 18)) + "px";
    spark.appendChild(bar);
  }
}

function render() {
  const list = sessions();
  const session = sessionByID(selectedID);
  const queue = notices();
  const notice = queue.find((item) => item.sessionId === selectedID) || null;
  const index = session ? list.indexOf(session) : -1;
  const disabled = !!snapshot.disabled;

  $("dot").className = "dot " + (disabled ? "idle" : dotClass(session));
  $("bar-project").textContent = disabled ? "Qoder" : session ? session.project : "Qoder";
  $("bar-state").textContent = disabled
    ? "未启用"
    : session
      ? session.label
      : snapshot.qoderRunning
        ? "空闲"
        : "未运行";
  $("bar-metric-1").textContent = disabled || !session ? "—" : "首字 " + formatMs(ttftOf(session));
  $("bar-metric-2").textContent = disabled || !session ? "—" : formatTPS(tpsOf(session));

  const badge = $("badge");
  if (!disabled && queue.length > 0) {
    badge.hidden = false;
    badge.textContent = "待处理 " + queue.length;
  } else {
    badge.hidden = true;
  }

  $("title").textContent = disabled
    ? "Qoder 未启用"
    : session
      ? session.title
      : snapshot.qoderRunning
        ? "Qoder 空闲"
        : "Qoder 未运行";
  $("sub").textContent =
    disabled || !session ? "" : session.project + " · " + (session.model || session.agent);
  $("position").textContent = disabled || !session || list.length < 2 ? "" : index + 1 + " / " + list.length;

  const chip = $("chip");
  chip.className = "chip " + (disabled ? "idle" : chipClass(session));
  chip.textContent = disabled
    ? "未启用"
    : session
      ? session.label
      : snapshot.qoderRunning
        ? "空闲"
        : "未运行";

  const hint = $("hint");
  hint.textContent = snapshot.error
    ? snapshot.error
    : disabled
      ? "在设置中启用该 agent"
      : session
        ? session.detail || ""
        : "";
  hint.classList.toggle("error", !!snapshot.error);

  const queueLabel = $("queue");
  queueLabel.hidden = queue.length === 0;
  queueLabel.textContent = queue.length > 0 ? "待处理 " + queue.length : "";

  $("m-ttft").textContent = session ? formatMs(session.avgTtftMs) : "—";
  $("m-post").textContent = session ? formatMs(postFirstOf(session)) : "—";
  $("m-tps").textContent = session ? formatTPS(tpsOf(session)) : "—";
  renderSpark(session);

  $("actions").hidden = disabled || !snapshot.qoderRunning;
  $("open-qoder").hidden = !!notice;
  $("handle").hidden = !notice;
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
      }, 180);
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

async function openQoder() {
  let ok = false;
  try {
    ok = await call("Notch.OpenQoder");
  } catch (err) {
    console.error(err);
  }
  if (ok) {
    await collapse();
  } else {
    showToast("没有把 Qoder 切到前台");
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

$("open-qoder").addEventListener("click", (event) => {
  event.stopPropagation();
  openQoder();
});

$("handle").addEventListener("click", (event) => {
  event.stopPropagation();
  handleNotice();
});

$("skip").addEventListener("click", (event) => {
  event.stopPropagation();
  skipNotice();
});

refresh();
setInterval(refresh, 700);
