const modules = {
  llm: {
    title: "LLM 工作区",
    description: "组装 Panel、模板、知识和资产，向选定模型发送一次完整请求。",
    sidebar: "这里将显示文件夹和会话。",
    action: "新建会话",
  },
  images: {
    title: "生图",
    description: "维护图像专用批次、提示词和参数，并从结果中挑选共享资产。",
    sidebar: "这里将显示图像批次和预设。",
    action: "新建批次",
  },
  video: {
    title: "视频",
    description: "通过 HTTP 或 CLI 执行视频任务，并管理首帧、参考素材和尾帧。",
    sidebar: "这里将显示视频批次和工作区。",
    action: "新建批次",
  },
  backends: {
    title: "后端管理",
    description: "按运行时配置启动和停止本地 AI Server，并观察原始实时日志。",
    sidebar: "这里将显示后端配置和运行状态。",
    action: "新建配置",
  },
  gallery: {
    title: "Gallery",
    description: "浏览全部资产，并在 active 与 archive 之间调整精选状态。",
    sidebar: "这里将显示资产筛选条件。",
    action: "导入资产",
  },
  knowledge: {
    title: "知识库",
    description: "维护可被完整请求快速引用的轻量备忘录。",
    sidebar: "这里将显示知识文件夹和条目。",
    action: "新建条目",
  },
  settings: {
    title: "配置",
    description: "查看工作台运行信息，并维护后续阶段加入的 Provider 与执行配置。",
    sidebar: "这里将显示配置分组。",
    action: "新增配置",
  },
};

const status = document.querySelector("#connection-status");
const pageTitle = document.querySelector("#page-title");
const pageDescription = document.querySelector("#page-description");
const sidebarTitle = document.querySelector("#sidebar-title");
const sidebarContent = document.querySelector("#sidebar-content");
const sidebarToggle = document.querySelector(".sidebar-toggle");
const sidebarClose = document.querySelector(".sidebar-close");
const sidebarScrim = document.querySelector(".sidebar-scrim");
const emptyState = document.querySelector(".empty-state");
const backendWorkspace = document.querySelector("#backend-workspace");

const backendUI = {
  list: document.querySelector("#backend-list"),
  listMessage: document.querySelector("#backend-list-message"),
  detailName: document.querySelector("#backend-detail-name"),
  detailDescription: document.querySelector("#backend-detail-description"),
  state: document.querySelector("#backend-state"),
  start: document.querySelector("#backend-start"),
  stop: document.querySelector("#backend-stop"),
  edit: document.querySelector("#backend-edit"),
  copy: document.querySelector("#backend-copy"),
  delete: document.querySelector("#backend-delete"),
  command: document.querySelector("#backend-command-preview"),
  pid: document.querySelector("#backend-pid"),
  uptime: document.querySelector("#backend-uptime"),
  workdir: document.querySelector("#backend-workdir"),
  log: document.querySelector("#backend-log"),
  logSearch: document.querySelector("#backend-log-search"),
  logFollow: document.querySelector("#backend-log-follow"),
  logSave: document.querySelector("#backend-log-save"),
  logClear: document.querySelector("#backend-log-clear"),
  actionMessage: document.querySelector("#backend-action-message"),
};

const backendEditor = document.querySelector("#backend-editor");
const backendForm = document.querySelector("#backend-form");
let backendProfiles = [];
let backendRuns = [];
let selectedBackendID = "";
let editingBackendID = "";
let backendLogText = "";
let backendLogEvents = null;

function setSidebar(open) {
  document.body.classList.toggle("sidebar-open", open);
  sidebarToggle.setAttribute("aria-expanded", String(open));
}

function selectModule(name) {
  const module = modules[name] ?? modules.llm;
  document.querySelectorAll("[data-module]").forEach((button) => {
    if (button.dataset.module === name) {
      button.setAttribute("aria-current", "page");
    } else {
      button.removeAttribute("aria-current");
    }
  });
  pageTitle.textContent = module.title;
  pageDescription.textContent = module.description;
  sidebarTitle.textContent = module.title;
  sidebarContent.replaceChildren();
  const description = document.createElement("p");
  description.textContent = module.sidebar;
  const action = document.createElement("button");
  action.type = "button";
  action.disabled = true;
  action.textContent = module.action;
  sidebarContent.append(description, action);
  const showBackends = name === "backends";
  emptyState.hidden = showBackends;
  backendWorkspace.hidden = !showBackends;
  if (showBackends) {
    refreshBackends();
  } else {
    closeBackendLogEvents();
  }
  window.location.hash = name;
  setSidebar(false);
}

function backendRun(profileID) {
  return backendRuns.find((run) => run.profile_id === profileID) ?? null;
}

function isActiveRun(run) {
  return run && ["starting", "running", "stopping"].includes(run.state);
}

function stateLabel(run) {
  if (!run) return "未运行";
  return {
    starting: "启动中",
    running: "运行中",
    stopping: "停止中",
    stopped: "已停止",
    failed: "异常退出",
  }[run.state] ?? run.state;
}

async function refreshBackends() {
  try {
    const response = await fetch("/api/v1/backends");
    if (!response.ok) throw new Error(await readAPIError(response));
    const data = await response.json();
    backendProfiles = data.profiles ?? [];
    backendRuns = data.runs ?? [];
    if (!backendProfiles.some((profile) => profile.id === selectedBackendID)) {
      selectedBackendID = backendProfiles[0]?.id ?? "";
    }
    renderBackendList();
    renderSelectedBackend();
  } catch (error) {
    backendUI.listMessage.hidden = false;
    backendUI.listMessage.textContent = `读取失败：${error.message}`;
  }
}

function renderBackendList() {
  backendUI.list.replaceChildren();
  backendUI.listMessage.hidden = backendProfiles.length > 0;
  backendUI.listMessage.textContent = backendProfiles.length ? "" : "还没有后端配置。";
  for (const profile of backendProfiles) {
    const run = backendRun(profile.id);
    const button = document.createElement("button");
    button.type = "button";
    button.className = "backend-list-item";
    button.setAttribute("aria-current", String(profile.id === selectedBackendID));
    const name = document.createElement("strong");
    name.textContent = profile.name;
    const state = document.createElement("small");
    state.textContent = stateLabel(run);
    button.append(name, state);
    button.addEventListener("click", () => {
      selectedBackendID = profile.id;
      renderBackendList();
      renderSelectedBackend();
    });
    backendUI.list.append(button);
  }
}

function renderSelectedBackend() {
  const profile = backendProfiles.find((item) => item.id === selectedBackendID);
  const run = profile ? backendRun(profile.id) : null;
  if (!profile) {
    backendUI.detailName.textContent = "选择一个配置";
    backendUI.detailDescription.textContent = "新建配置后即可启动本地 AI Server。";
    backendUI.state.textContent = "未运行";
    backendUI.state.dataset.state = "";
    backendUI.command.textContent = "尚未选择配置";
    backendUI.pid.textContent = "—";
    backendUI.uptime.textContent = "—";
    backendUI.workdir.textContent = "—";
    for (const control of [backendUI.start, backendUI.stop, backendUI.edit, backendUI.copy, backendUI.delete, backendUI.logSave, backendUI.logClear]) {
      control.disabled = true;
    }
    backendLogText = "";
    renderBackendLog();
    closeBackendLogEvents();
    return;
  }

  backendUI.detailName.textContent = profile.name;
  backendUI.detailDescription.textContent = profile.description || "没有说明";
  backendUI.state.textContent = stateLabel(run);
  backendUI.state.dataset.state = run?.state ?? "";
  backendUI.command.textContent = profile.command;
  backendUI.pid.textContent = run?.pid ? String(run.pid) : "—";
  backendUI.uptime.textContent = formatUptime(run);
  backendUI.workdir.textContent = profile.work_dir || "继承工作台目录";
  const active = isActiveRun(run);
  backendUI.start.disabled = active;
  backendUI.stop.disabled = !active;
  backendUI.edit.disabled = false;
  backendUI.copy.disabled = false;
  backendUI.delete.disabled = active;
  backendUI.logSave.disabled = !run;
  backendUI.logClear.disabled = !run;
  backendUI.actionMessage.textContent = run?.error || "";
  connectBackendLog(profile.id, Boolean(run));
}

function formatUptime(run) {
  if (!run?.started_at) return "—";
  const end = run.ended_at ? new Date(run.ended_at) : new Date();
  const seconds = Math.max(0, Math.floor((end - new Date(run.started_at)) / 1000));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  return hours ? `${hours}h ${minutes}m ${remainder}s` : `${minutes}m ${remainder}s`;
}

function connectBackendLog(profileID, hasRun) {
  closeBackendLogEvents();
  backendLogText = "";
  renderBackendLog();
  if (!hasRun) return;
  backendLogEvents = new EventSource(`/api/v1/backends/${profileID}/logs/events`);
  backendLogEvents.addEventListener("snapshot", (event) => {
    backendLogText = JSON.parse(event.data);
    renderBackendLog();
  });
  backendLogEvents.addEventListener("chunk", (event) => {
    backendLogText += JSON.parse(event.data);
    renderBackendLog();
  });
  backendLogEvents.onerror = () => {
    if (backendLogEvents) backendLogEvents.close();
  };
}

function closeBackendLogEvents() {
  if (backendLogEvents) {
    backendLogEvents.close();
    backendLogEvents = null;
  }
}

function renderBackendLog() {
  const query = backendUI.logSearch.value.toLocaleLowerCase();
  const visible = query
    ? backendLogText.split("\n").filter((line) => line.toLocaleLowerCase().includes(query)).join("\n")
    : backendLogText;
  backendUI.log.textContent = visible || "启动后在这里显示原始输出。";
  if (backendUI.logFollow.checked) {
    backendUI.log.scrollTop = backendUI.log.scrollHeight;
  }
}

async function backendAction(path, body = null) {
  const options = { method: "POST", headers: {} };
  if (body !== null) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  if (!response.ok) throw new Error(await readAPIError(response));
  if (response.status === 204) return null;
  return response.json();
}

async function readAPIError(response) {
  try {
    const body = await response.json();
    return body.error?.message || `HTTP ${response.status}`;
  } catch {
    return `HTTP ${response.status}`;
  }
}

async function runBackendAction(action) {
  if (!selectedBackendID) return;
  backendUI.actionMessage.textContent = "执行中…";
  try {
    await backendAction(`/api/v1/backends/${selectedBackendID}/${action}`, action === "start" ? { variables: {} } : null);
    await refreshBackends();
  } catch (error) {
    backendUI.actionMessage.textContent = error.message;
  }
}

function openBackendEditor(profile = null) {
  editingBackendID = profile?.id ?? "";
  document.querySelector("#backend-editor-title").textContent = profile ? "编辑配置" : "新建配置";
  document.querySelector("#backend-name").value = profile?.name ?? "";
  document.querySelector("#backend-description").value = profile?.description ?? "";
  document.querySelector("#backend-command").value = profile?.command ?? "";
  document.querySelector("#backend-work-dir").value = profile?.work_dir ?? "";
  document.querySelector("#backend-env").value = JSON.stringify(profile?.env ?? {}, null, 2);
  document.querySelector("#backend-variables").value = JSON.stringify(profile?.variables ?? {}, null, 2);
  document.querySelector("#backend-readiness-kind").value = profile?.readiness?.kind ?? "none";
  document.querySelector("#backend-readiness-value").value = readinessValue(profile?.readiness);
  document.querySelector("#backend-readiness-timeout").value = String(profile?.readiness?.timeout_seconds ?? 60);
  document.querySelector("#backend-stop-grace").value = String(profile?.stop_grace_seconds ?? 10);
  document.querySelector("#backend-log-capacity").value = String(profile?.log_buffer_bytes ?? 1048576);
  document.querySelector("#backend-form-error").textContent = "";
  backendEditor.showModal();
}

function readinessValue(readiness) {
  if (!readiness) return "";
  if (readiness.kind === "delay") return String(readiness.delay_seconds ?? "");
  if (readiness.kind === "http") return readiness.url ?? "";
  if (readiness.kind === "log_regex") return readiness.pattern ?? "";
  return "";
}

function readJSONMap(id) {
  const value = JSON.parse(document.querySelector(id).value || "{}");
  if (!value || Array.isArray(value) || typeof value !== "object") {
    throw new Error("环境变量和模板变量必须是 JSON Object");
  }
  for (const entry of Object.values(value)) {
    if (typeof entry !== "string") throw new Error("变量值必须全部是字符串");
  }
  return value;
}

function buildReadiness() {
  const kind = document.querySelector("#backend-readiness-kind").value;
  const value = document.querySelector("#backend-readiness-value").value.trim();
  const readiness = {
    kind,
    timeout_seconds: Number(document.querySelector("#backend-readiness-timeout").value),
  };
  if (kind === "delay") readiness.delay_seconds = Number(value);
  if (kind === "http") readiness.url = value;
  if (kind === "log_regex") readiness.pattern = value;
  return readiness;
}

async function saveBackend(event) {
  event.preventDefault();
  const errorArea = document.querySelector("#backend-form-error");
  try {
    const profile = {
      name: document.querySelector("#backend-name").value.trim(),
      description: document.querySelector("#backend-description").value.trim(),
      command: document.querySelector("#backend-command").value,
      work_dir: document.querySelector("#backend-work-dir").value.trim(),
      env: readJSONMap("#backend-env"),
      variables: readJSONMap("#backend-variables"),
      readiness: buildReadiness(),
      stop_grace_seconds: Number(document.querySelector("#backend-stop-grace").value),
      log_buffer_bytes: Number(document.querySelector("#backend-log-capacity").value),
    };
    const path = editingBackendID ? `/api/v1/backends/${editingBackendID}` : "/api/v1/backends";
    const response = await fetch(path, {
      method: editingBackendID ? "PUT" : "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(profile),
    });
    if (!response.ok) throw new Error(await readAPIError(response));
    const saved = await response.json();
    selectedBackendID = saved.id;
    backendEditor.close();
    await refreshBackends();
  } catch (error) {
    errorArea.textContent = error.message;
  }
}

document.querySelector("#backend-new").addEventListener("click", () => openBackendEditor());
document.querySelector("#backend-refresh").addEventListener("click", refreshBackends);
backendUI.start.addEventListener("click", () => runBackendAction("start"));
backendUI.stop.addEventListener("click", () => runBackendAction("stop"));
backendUI.edit.addEventListener("click", () => {
  const profile = backendProfiles.find((item) => item.id === selectedBackendID);
  if (profile) openBackendEditor(profile);
});
backendUI.copy.addEventListener("click", () => {
  const profile = backendProfiles.find((item) => item.id === selectedBackendID);
  if (!profile) return;
  openBackendEditor({ ...profile, id: "", name: `${profile.name} 副本` });
});
backendUI.delete.addEventListener("click", async () => {
  const profile = backendProfiles.find((item) => item.id === selectedBackendID);
  if (!profile || !window.confirm(`删除后端配置“${profile.name}”？`)) return;
  const response = await fetch(`/api/v1/backends/${profile.id}`, { method: "DELETE" });
  if (!response.ok) {
    backendUI.actionMessage.textContent = await readAPIError(response);
    return;
  }
  selectedBackendID = "";
  await refreshBackends();
});
backendUI.logSearch.addEventListener("input", renderBackendLog);
backendUI.logFollow.addEventListener("change", renderBackendLog);
backendUI.logClear.addEventListener("click", async () => {
  try {
    await backendAction(`/api/v1/backends/${selectedBackendID}/logs/clear`);
    backendLogText = "";
    renderBackendLog();
  } catch (error) {
    backendUI.actionMessage.textContent = error.message;
  }
});
backendUI.logSave.addEventListener("click", async () => {
  try {
    const result = await backendAction(`/api/v1/backends/${selectedBackendID}/logs/save`);
    backendUI.actionMessage.textContent = `已保存到 ${result.path}`;
  } catch (error) {
    backendUI.actionMessage.textContent = error.message;
  }
});
backendForm.addEventListener("submit", saveBackend);
document.querySelector("#backend-editor-close").addEventListener("click", () => backendEditor.close());
document.querySelector("#backend-editor-cancel").addEventListener("click", () => backendEditor.close());

document.querySelectorAll("[data-module]").forEach((button) => {
  button.addEventListener("click", () => selectModule(button.dataset.module));
});

sidebarToggle.addEventListener("click", () => {
  setSidebar(!document.body.classList.contains("sidebar-open"));
});
sidebarClose.addEventListener("click", () => setSidebar(false));
sidebarScrim.addEventListener("click", () => setSidebar(false));
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    setSidebar(false);
  }
});

async function loadRuntime() {
  try {
    const [healthResponse, settingsResponse] = await Promise.all([
      fetch("/api/v1/health"),
      fetch("/api/v1/settings"),
    ]);
    if (!healthResponse.ok || !settingsResponse.ok) {
      throw new Error("runtime API returned an error");
    }
    const health = await healthResponse.json();
    const settings = await settingsResponse.json();
    status.dataset.state = "connected";
    status.lastElementChild.textContent = "已连接";
    document.querySelector("#runtime-version").textContent = health.version;
    document.querySelector("#runtime-port").textContent = String(settings.listen_port);
    document.querySelector("#runtime-data-dir").textContent = settings.data_dir;
  } catch {
    status.dataset.state = "error";
    status.lastElementChild.textContent = "连接失败";
    document.querySelector("#runtime-version").textContent = "不可用";
    document.querySelector("#runtime-port").textContent = "不可用";
    document.querySelector("#runtime-data-dir").textContent = "不可用";
  }
}

const initialModule = window.location.hash.slice(1);
selectModule(Object.hasOwn(modules, initialModule) ? initialModule : "llm");
loadRuntime();
