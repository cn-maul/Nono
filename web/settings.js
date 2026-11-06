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

function toggleInput(dataset, id, checked, disabled) {
  const toggle = element("label", "switch");
  const input = document.createElement("input");
  input.type = "checkbox";
  input.dataset[dataset] = id;
  input.checked = checked;
  input.disabled = !!disabled;
  toggle.append(input, element("span"));
  return toggle;
}

function renderAgents(agents) {
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

    row.append(icon, info, toggleInput("agent", agent.id, agent.installed && agent.enabled, !agent.installed));
    box.append(row);
  }
}

function renderRules(rules) {
  const box = $("rules");
  box.textContent = "";
  for (const rule of rules) {
    const row = element("div", "rule");
    const info = element("div", "rule-info");
    info.append(
      element("div", "rule-title", rule.title),
      element("div", "rule-desc", rule.description)
    );
    row.append(info, toggleInput("rule", rule.id, !!rule.enabled, false));
    box.append(row);
  }
}

const SECTIONS = {
  agents: {
    title: "智能体",
    description: "选择哪些 agent 显示在刘海屏中",
    box: "agents",
  },
  rules: {
    title: "规则",
    description: "控制刘海的状态切换与显示行为，保存后立即生效",
    box: "rules",
  },
};

function switchSection(name) {
  const section = SECTIONS[name];
  if (!section) {
    return;
  }
  for (const button of document.querySelectorAll(".nav")) {
    button.classList.toggle("active", button.dataset.section === name);
  }
  $("section-title").textContent = section.title;
  $("section-desc").textContent = section.description;
  $("agents").hidden = section.box !== "agents";
  $("rules").hidden = section.box !== "rules";
}

async function load() {
  try {
    const [agents, rules] = await Promise.all([
      call("Notch.SettingsData"),
      call("Notch.RulesData"),
    ]);
    renderAgents(agents || []);
    renderRules(rules || []);
  } catch (err) {
    console.error(err);
    showError("读取设置失败");
  }
}

function collect(selector) {
  const out = [];
  for (const input of document.querySelectorAll(selector)) {
    out.push({ id: input.dataset.agent || input.dataset.rule, enabled: input.checked });
  }
  return out;
}

async function save() {
  const agents = [];
  for (const input of document.querySelectorAll("input[data-agent]")) {
    if (!input.disabled) {
      agents.push({ id: input.dataset.agent, enabled: input.checked });
    }
  }
  const payload = { agents, rules: collect("input[data-rule]") };
  $("save").disabled = true;
  try {
    const ok = await call("Notch.SaveSettings", payload);
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

for (const button of document.querySelectorAll(".nav")) {
  button.addEventListener("click", () => switchSection(button.dataset.section));
}

$("save").addEventListener("click", save);
$("cancel").addEventListener("click", cancel);
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    cancel();
  }
});

load();
