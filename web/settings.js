const $ = (id) => document.getElementById(id);

function call(method, ...args) {
  if (!window.mygo) {
    return Promise.resolve(undefined);
  }
  return window.mygo.call(method, ...args);
}

function showError(text) {
  $("error").textContent = text || "";
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
}

function render(agents) {
  const box = $("agents");
  box.textContent = "";
  for (const agent of agents) {
    const row = element("div", "agent" + (agent.installed ? "" : " missing"));

    const icon = document.createElement("img");
    icon.src = agent.icon;
    icon.alt = agent.name;

    const info = element("div", "agent-info");
    const nameLine = element("div", "name-line");
    nameLine.appendChild(element("span", "name", agent.name));
    const version = agent.installed
      ? agent.version
        ? "v" + agent.version
        : "版本未知"
      : "未安装";
    nameLine.appendChild(element("span", "version", version));
    if (!agent.supported) {
      nameLine.appendChild(element("span", "note", "暂未接入"));
    }
    const path = element("div", "path", agent.installed ? agent.path : "未安装");
    if (agent.installed && agent.path) {
      path.title = agent.path;
    }
    info.append(nameLine, path);

    const toggle = element("label", "switch");
    const input = document.createElement("input");
    input.type = "checkbox";
    input.dataset.agent = agent.id;
    input.checked = agent.installed && agent.enabled;
    input.disabled = !agent.installed;
    toggle.append(input, element("span"));

    row.append(icon, info, toggle);
    box.append(row);
  }
}

async function load() {
  try {
    const agents = await call("Notch.SettingsData");
    render(agents || []);
  } catch (err) {
    console.error(err);
    showError("读取设置失败");
  }
}

async function save() {
  const toggles = [];
  for (const input of document.querySelectorAll("input[data-agent]")) {
    if (input.disabled) {
      continue;
    }
    toggles.push({ id: input.dataset.agent, enabled: input.checked });
  }
  $("save").disabled = true;
  try {
    const ok = await call("Notch.SaveSettings", toggles);
    if (!ok) {
      showError("保存失败");
      $("save").disabled = false;
    }
  } catch (err) {
    console.error(err);
    showError("保存失败");
    $("save").disabled = false;
  }
}

async function cancel() {
  try {
    await call("Notch.CancelSettings");
  } catch (err) {
    console.error(err);
  }
}

$("save").addEventListener("click", save);
$("cancel").addEventListener("click", cancel);
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    cancel();
  }
});

load();
