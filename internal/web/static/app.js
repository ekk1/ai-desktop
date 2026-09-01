import { createLLMConfig } from "/assets/llm-config.js";
import { createLLMWorkspace } from "/assets/llm.js";
import { createImageConfig } from "/assets/image-config.js";
import { createVideoConfig } from "/assets/video-config.js";
import { createImageWorkspace } from "/assets/images.js";

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
const llmWorkspace = document.querySelector("#llm-workspace");
const imageWorkspace = document.querySelector("#image-workspace");
const backendWorkspace = document.querySelector("#backend-workspace");
const galleryWorkspace = document.querySelector("#gallery-workspace");
const knowledgeWorkspace = document.querySelector("#knowledge-workspace");
const settingsWorkspace = document.querySelector("#settings-workspace");
const llmConfigWorkspace = createLLMConfig({ readAPIError });
const imageConfigWorkspace = createImageConfig({ readAPIError });
const videoConfigWorkspace = createVideoConfig({ readAPIError });
const llmWorkspaceController = createLLMWorkspace({ sidebarContent, sidebarSearch: document.querySelector("#sidebar-search"), readAPIError, openAssetPicker });
const imageWorkspaceController = createImageWorkspace({ sidebarContent, sidebarSearch: document.querySelector("#sidebar-search"), readAPIError, openAssetPicker });

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
  execution: document.querySelector("#backend-execution-summary"),
  workerInstance: document.querySelector("#backend-worker-instance"),
  workerRun: document.querySelector("#backend-worker-run"),
  workerConnection: document.querySelector("#backend-worker-connection"),
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
let backendLogBytes = new Uint8Array();
let backendLogEvents = null;
let backendLogProfileID = "";
let backendLogNextOffset = 0;
let backendLogClearOffset = 0;
const backendLogDecoder = new TextDecoder();

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
  const showGallery = name === "gallery";
  const showKnowledge = name === "knowledge";
  const showSettings = name === "settings";
  const showLLM = name === "llm";
  const showImages = name === "images";
  emptyState.hidden = showLLM || showImages || showBackends || showGallery || showKnowledge || showSettings;
  llmWorkspace.hidden = !showLLM;
  imageWorkspace.hidden = !showImages;
  backendWorkspace.hidden = !showBackends;
  galleryWorkspace.hidden = !showGallery;
  knowledgeWorkspace.hidden = !showKnowledge;
  settingsWorkspace.hidden = !showSettings;
  if (showBackends) {
    refreshBackends();
  } else {
    closeBackendLogEvents();
  }
  if (showGallery) refreshGallery();
  if (showKnowledge) refreshKnowledge();
  if (showSettings) {
    llmConfigWorkspace.enter();
    imageConfigWorkspace.enter();
    videoConfigWorkspace.enter();
  } else {
    llmConfigWorkspace.leave();
    imageConfigWorkspace.leave();
    videoConfigWorkspace.leave();
  }
  if (showLLM) llmWorkspaceController.enter();
  else llmWorkspaceController.leave();
  if (showImages) imageWorkspaceController.enter();
  else imageWorkspaceController.leave();
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
    interrupted: "已中断",
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
    const location = profile.execution?.kind === "worker" ? "远端" : "本机";
    state.textContent = `${location} · ${stateLabel(run)}`;
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
    backendUI.execution.textContent = "—";
    backendUI.workerInstance.textContent = "—";
    backendUI.workerRun.textContent = "—";
    backendUI.workerConnection.textContent = "—";
    for (const control of [backendUI.start, backendUI.stop, backendUI.edit, backendUI.copy, backendUI.delete, backendUI.logSave, backendUI.logClear]) {
      control.disabled = true;
    }
    backendLogBytes = new Uint8Array();
    backendLogProfileID = "";
    backendLogNextOffset = 0;
    backendLogClearOffset = 0;
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
  const remote = profile.execution?.kind === "worker";
  backendUI.execution.textContent = remote ? `远端 · ${profile.execution.worker_base_url}` : "本机";
  backendUI.workerInstance.textContent = run?.worker_instance_id || "—";
  backendUI.workerRun.textContent = run?.worker_run_id || "—";
  backendUI.workerConnection.textContent = remote
    ? ({ connected: "已连接", unknown: "连接未知" }[run?.connection_state] ?? "未连接")
    : "不适用";
  const active = isActiveRun(run);
  backendUI.start.disabled = active;
  backendUI.stop.disabled = !active;
  backendUI.edit.disabled = false;
  backendUI.copy.disabled = false;
  backendUI.delete.disabled = active;
  backendUI.logSave.disabled = !run;
  backendUI.logClear.disabled = !run;
  backendUI.actionMessage.textContent = run?.connection_error
    ? `Worker 控制连接：${run.connection_error}`
    : (run?.error || "");
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
  if (backendLogProfileID !== profileID) {
    backendLogProfileID = profileID;
    backendLogBytes = new Uint8Array();
    backendLogNextOffset = 0;
    backendLogClearOffset = 0;
    renderBackendLog();
  }
  if (!hasRun) return;
  backendLogEvents = new EventSource(`/api/v1/backends/${profileID}/logs/events`);
  backendLogEvents.addEventListener("snapshot", (event) => {
    const log = decodeBackendLogEvent(event.data);
    const start = Math.max(log.startOffset, backendLogClearOffset);
    backendLogBytes = start < log.endOffset ? log.bytes.slice(start - log.startOffset) : new Uint8Array();
    backendLogNextOffset = log.endOffset;
    renderBackendLog();
  });
  backendLogEvents.addEventListener("chunk", (event) => {
    const log = decodeBackendLogEvent(event.data);
    const start = Math.max(log.startOffset, backendLogNextOffset, backendLogClearOffset);
    if (start < log.endOffset) appendBackendLogBytes(log.bytes.slice(start - log.startOffset));
    backendLogNextOffset = Math.max(backendLogNextOffset, log.endOffset);
    renderBackendLog();
  });
  backendLogEvents.onerror = () => {
    if (backendLogEvents) backendLogEvents.close();
  };
}

function appendBackendLogBytes(bytes) {
  const combined = new Uint8Array(backendLogBytes.length + bytes.length);
  combined.set(backendLogBytes);
  combined.set(bytes, backendLogBytes.length);
  backendLogBytes = combined;
}

function decodeBackendLogEvent(encoded) {
  const event = JSON.parse(encoded);
  const binary = atob(event.data_base64);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  if (event.start_offset < 0 || event.end_offset < event.start_offset || bytes.length !== event.end_offset - event.start_offset) {
    throw new Error("invalid backend log event offsets");
  }
  return { startOffset: event.start_offset, endOffset: event.end_offset, bytes };
}

function closeBackendLogEvents() {
  if (backendLogEvents) {
    backendLogEvents.close();
    backendLogEvents = null;
  }
}

function renderBackendLog() {
  const backendLogText = backendLogDecoder.decode(backendLogBytes);
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
  document.querySelector("#backend-execution-kind").value = profile?.execution?.kind ?? "local";
  document.querySelector("#backend-worker-url").value = profile?.execution?.worker_base_url ?? "";
  document.querySelector("#backend-env").value = JSON.stringify(profile?.env ?? {}, null, 2);
  document.querySelector("#backend-variables").value = JSON.stringify(profile?.variables ?? {}, null, 2);
  document.querySelector("#backend-readiness-kind").value = profile?.readiness?.kind ?? "none";
  document.querySelector("#backend-readiness-value").value = readinessValue(profile?.readiness);
  document.querySelector("#backend-readiness-timeout").value = String(profile?.readiness?.timeout_seconds ?? 60);
  document.querySelector("#backend-stop-grace").value = String(profile?.stop_grace_seconds ?? 10);
  document.querySelector("#backend-log-capacity").value = String(profile?.log_buffer_bytes ?? 1048576);
  document.querySelector("#backend-form-error").textContent = "";
  document.querySelector("#backend-worker-test-result").textContent = "";
  updateBackendExecutionFields();
  backendEditor.showModal();
}

function updateBackendExecutionFields() {
  const remote = document.querySelector("#backend-execution-kind").value === "worker";
  document.querySelector("#backend-worker-url-field").hidden = !remote;
  document.querySelector("#backend-worker-test").hidden = !remote;
  if (!remote) document.querySelector("#backend-worker-test-result").textContent = "";
}

async function testBackendWorkerConnection() {
  const result = document.querySelector("#backend-worker-test-result");
  const workerBaseURL = document.querySelector("#backend-worker-url").value.trim();
  result.textContent = "正在连接…";
  try {
    const response = await fetch("/api/v1/backends/worker/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ worker_base_url: workerBaseURL }),
    });
    if (!response.ok) throw new Error(await readAPIError(response));
    const health = await response.json();
    result.textContent = `连接成功 · ${health.version} · ${health.instance_id}`;
  } catch (error) {
    result.textContent = `连接失败：${error.message}`;
  }
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
      execution: document.querySelector("#backend-execution-kind").value === "worker"
        ? { kind: "worker", worker_base_url: document.querySelector("#backend-worker-url").value.trim() }
        : { kind: "local" },
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

const galleryUI = {
  fileInput: document.querySelector("#gallery-file-input"),
  filter: document.querySelector("#gallery-filter"),
  search: document.querySelector("#gallery-search"),
  grid: document.querySelector("#gallery-grid"),
  message: document.querySelector("#gallery-message"),
  selectAll: document.querySelector("#gallery-select-all"),
  selectionCount: document.querySelector("#gallery-selection-count"),
  activate: document.querySelector("#gallery-activate"),
  archive: document.querySelector("#gallery-archive"),
  export: document.querySelector("#gallery-export"),
};

const galleryPreview = document.querySelector("#gallery-preview");
const assetPicker = document.querySelector("#asset-picker");
let galleryAssets = [];
let selectedGalleryAssets = new Set();
let previewAssetID = "";
let pickerAssets = [];
let selectedPickerAssets = new Set();
let pickerAllowsMultiple = true;
let pickerMediaPrefix = "";
let pickerResolve = null;
let gallerySearchTimer = null;

function assetContentURL(id) {
  return `/api/v1/assets/${encodeURIComponent(id)}/content`;
}

function formatBytes(size) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function createAssetMedia(item) {
  const container = document.createElement("div");
  container.className = "asset-media";
  const url = assetContentURL(item.id);
  if (item.media_type.startsWith("image/")) {
    const image = document.createElement("img");
    image.src = url;
    image.alt = item.display_name;
    image.loading = "lazy";
    container.append(image);
  } else if (item.media_type.startsWith("video/")) {
    const video = document.createElement("video");
    video.src = url;
    video.controls = true;
    video.preload = "metadata";
    container.append(video);
  } else {
    const attachment = document.createElement("div");
    attachment.className = "attachment-preview";
    const type = document.createElement("strong");
    type.textContent = item.media_type || "文件";
    const size = document.createElement("span");
    size.textContent = formatBytes(item.size);
    attachment.append(type, size);
    container.append(attachment);
  }
  return container;
}

function createAssetCard(item, selected, onSelect, onOpen = null) {
  const card = document.createElement("article");
  card.className = "asset-card";
  card.dataset.state = item.state;
  const selection = document.createElement("label");
  selection.className = "asset-card-selection";
  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.checked = selected;
  checkbox.setAttribute("aria-label", `选择 ${item.display_name}`);
  checkbox.addEventListener("change", () => onSelect(item, checkbox.checked));
  const state = document.createElement("span");
  state.textContent = item.state === "active" ? "精选" : "归档";
  selection.append(checkbox, state);
  card.append(selection, createAssetMedia(item));
  const body = document.createElement("div");
  body.className = "asset-card-body";
  const name = document.createElement("strong");
  name.title = item.display_name;
  name.textContent = item.display_name;
  const meta = document.createElement("small");
  meta.textContent = `${formatBytes(item.size)} · ${item.media_type}`;
  body.append(name, meta);
  if (onOpen) {
    const open = document.createElement("button");
    open.type = "button";
    open.className = "text-button asset-open";
    open.textContent = "查看";
    open.addEventListener("click", () => onOpen(item));
    body.append(open);
  }
  card.append(body);
  return card;
}

async function refreshGallery() {
  galleryUI.message.textContent = "正在读取资产…";
  const parameters = new URLSearchParams();
  if (galleryUI.filter.value) parameters.set("state", galleryUI.filter.value);
  if (galleryUI.search.value.trim()) parameters.set("q", galleryUI.search.value.trim());
  try {
    const suffix = parameters.size ? `?${parameters}` : "";
    const response = await fetch(`/api/v1/assets${suffix}`);
    if (!response.ok) throw new Error(await readAPIError(response));
    galleryAssets = (await response.json()).assets ?? [];
    const visibleIDs = new Set(galleryAssets.map((item) => item.id));
    selectedGalleryAssets = new Set([...selectedGalleryAssets].filter((id) => visibleIDs.has(id)));
    renderGallery();
  } catch (error) {
    galleryUI.message.textContent = `读取失败：${error.message}`;
  }
}

function renderGallery() {
  galleryUI.grid.replaceChildren();
  galleryUI.message.textContent = galleryAssets.length ? "" : "当前筛选下没有资产。";
  for (const item of galleryAssets) {
    galleryUI.grid.append(createAssetCard(item, selectedGalleryAssets.has(item.id), (assetItem, checked) => {
      if (checked) selectedGalleryAssets.add(assetItem.id);
      else selectedGalleryAssets.delete(assetItem.id);
      updateGallerySelection();
    }, openGalleryPreview));
  }
  updateGallerySelection();
}

function updateGallerySelection() {
  const count = selectedGalleryAssets.size;
  galleryUI.selectionCount.textContent = `已选 ${count} 项`;
  galleryUI.selectAll.checked = galleryAssets.length > 0 && count === galleryAssets.length;
  galleryUI.selectAll.indeterminate = count > 0 && count < galleryAssets.length;
  for (const button of [galleryUI.activate, galleryUI.archive, galleryUI.export]) button.disabled = count === 0;
}

async function setSelectedAssetState(state) {
  galleryUI.message.textContent = "正在更新…";
  const response = await fetch("/api/v1/assets/state", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ asset_ids: [...selectedGalleryAssets], state }),
  });
  if (!response.ok) {
    galleryUI.message.textContent = await readAPIError(response);
    return;
  }
  selectedGalleryAssets.clear();
  await refreshGallery();
}

async function exportSelectedAssets() {
  galleryUI.message.textContent = "正在生成 ZIP…";
  const response = await fetch("/api/v1/assets/export", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ asset_ids: [...selectedGalleryAssets] }),
  });
  if (!response.ok) {
    galleryUI.message.textContent = await readAPIError(response);
    return;
  }
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = "assets.zip";
  link.click();
  URL.revokeObjectURL(url);
  galleryUI.message.textContent = `已导出 ${selectedGalleryAssets.size} 项。`;
}

async function importGalleryFiles(files) {
  if (!files.length) return;
  for (let index = 0; index < files.length; index += 1) {
    const file = files[index];
    galleryUI.message.textContent = `正在导入 ${index + 1}/${files.length}：${file.name}`;
    const body = new FormData();
    body.append("file", file);
    body.append("media_type", file.type || "application/octet-stream");
    body.append("source", "upload");
    const response = await fetch("/api/v1/assets", { method: "POST", body });
    if (!response.ok) {
      galleryUI.message.textContent = `${file.name} 导入失败：${await readAPIError(response)}`;
      return;
    }
  }
  galleryUI.fileInput.value = "";
  await refreshGallery();
}

function openGalleryPreview(item) {
  previewAssetID = item.id;
  document.querySelector("#gallery-preview-title").textContent = item.display_name;
  document.querySelector("#gallery-preview-name").value = item.display_name;
  document.querySelector("#gallery-preview-state").value = item.state === "active" ? "精选" : "归档";
  document.querySelector("#gallery-preview-notes").value = item.notes || "";
  document.querySelector("#gallery-preview-error").textContent = "";
  document.querySelector("#gallery-preview-download").href = assetContentURL(item.id);
  document.querySelector("#gallery-preview-media").replaceChildren(createAssetMedia(item));
  const facts = document.querySelector("#gallery-preview-facts");
  facts.replaceChildren();
  for (const [label, value] of [["类型", item.media_type], ["大小", formatBytes(item.size)], ["尺寸", item.width && item.height ? `${item.width} × ${item.height}` : "—"], ["来源", item.source || "—"]]) {
    const group = document.createElement("div");
    const term = document.createElement("dt");
    term.textContent = label;
    const description = document.createElement("dd");
    description.textContent = value;
    group.append(term, description);
    facts.append(group);
  }
  const references = document.querySelector("#gallery-preview-references");
  references.replaceChildren();
  if (!item.references?.length) {
    references.textContent = "没有模块引用。";
  } else {
    for (const reference of item.references) {
      const entry = document.createElement("code");
      entry.textContent = `${reference.module}:${reference.record_id}`;
      references.append(entry);
    }
  }
  galleryPreview.showModal();
}

async function saveGalleryPreview(event) {
  event.preventDefault();
  const response = await fetch(`/api/v1/assets/${encodeURIComponent(previewAssetID)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ display_name: document.querySelector("#gallery-preview-name").value.trim(), notes: document.querySelector("#gallery-preview-notes").value }),
  });
  if (!response.ok) {
    document.querySelector("#gallery-preview-error").textContent = await readAPIError(response);
    return;
  }
  galleryPreview.close();
  await refreshGallery();
}

async function deletePreviewAsset() {
  const item = galleryAssets.find((candidate) => candidate.id === previewAssetID);
  if (!item || !window.confirm(`删除资产“${item.display_name}”？`)) return;
  const response = await fetch(`/api/v1/assets/${encodeURIComponent(previewAssetID)}`, { method: "DELETE" });
  if (!response.ok) {
    const references = item.references?.map((entry) => `${entry.module}:${entry.record_id}`).join("、");
    document.querySelector("#gallery-preview-error").textContent = `${await readAPIError(response)}${references ? `；当前引用：${references}` : ""}`;
    return;
  }
  galleryPreview.close();
  selectedGalleryAssets.delete(previewAssetID);
  await refreshGallery();
}

async function openAssetPicker(options = {}) {
  if (pickerResolve) closeAssetPicker(null);
  pickerAllowsMultiple = options.multiple !== false;
  pickerMediaPrefix = options.mediaPrefix ?? "";
  selectedPickerAssets = new Set(options.selected ?? []);
  document.querySelector("#asset-picker-search").value = "";
  document.querySelector("#asset-picker-message").textContent = "正在读取精选资产…";
  assetPicker.showModal();
  const result = new Promise((resolve) => { pickerResolve = resolve; });
  try {
    const response = await fetch("/api/v1/assets?state=active");
    if (!response.ok) throw new Error(await readAPIError(response));
    pickerAssets = ((await response.json()).assets ?? []).filter((item) => !pickerMediaPrefix || item.media_type?.startsWith(pickerMediaPrefix));
    renderAssetPicker();
  } catch (error) {
    document.querySelector("#asset-picker-message").textContent = `读取失败：${error.message}`;
  }
  return result;
}

function renderAssetPicker() {
  const grid = document.querySelector("#asset-picker-grid");
  const query = document.querySelector("#asset-picker-search").value.trim().toLocaleLowerCase();
  const visible = pickerAssets.filter((item) => !query || `${item.display_name}\n${item.notes || ""}`.toLocaleLowerCase().includes(query));
  grid.replaceChildren();
  document.querySelector("#asset-picker-message").textContent = visible.length ? `已选 ${selectedPickerAssets.size} 项` : "没有可选的精选资产。";
  for (const item of visible) {
    grid.append(createAssetCard(item, selectedPickerAssets.has(item.id), (assetItem, checked) => {
      if (!pickerAllowsMultiple) selectedPickerAssets.clear();
      if (checked) selectedPickerAssets.add(assetItem.id);
      else selectedPickerAssets.delete(assetItem.id);
      renderAssetPicker();
    }));
  }
}

function closeAssetPicker(result) {
  if (assetPicker.open) assetPicker.close();
  const resolve = pickerResolve;
  pickerResolve = null;
  if (resolve) resolve(result);
}

window.openAssetPicker = openAssetPicker;

document.querySelector("#gallery-refresh").addEventListener("click", refreshGallery);
galleryUI.filter.addEventListener("change", refreshGallery);
galleryUI.search.addEventListener("input", () => {
  window.clearTimeout(gallerySearchTimer);
  gallerySearchTimer = window.setTimeout(refreshGallery, 180);
});
galleryUI.fileInput.addEventListener("change", () => importGalleryFiles([...galleryUI.fileInput.files]));
galleryUI.selectAll.addEventListener("change", () => {
  selectedGalleryAssets = galleryUI.selectAll.checked ? new Set(galleryAssets.map((item) => item.id)) : new Set();
  renderGallery();
});
galleryUI.activate.addEventListener("click", () => setSelectedAssetState("active"));
galleryUI.archive.addEventListener("click", () => setSelectedAssetState("archive"));
galleryUI.export.addEventListener("click", exportSelectedAssets);
document.querySelector("#gallery-preview-form").addEventListener("submit", saveGalleryPreview);
document.querySelector("#gallery-preview-close").addEventListener("click", () => galleryPreview.close());
document.querySelector("#gallery-preview-delete").addEventListener("click", deletePreviewAsset);
document.querySelector("#asset-picker-search").addEventListener("input", renderAssetPicker);
document.querySelector("#asset-picker-close").addEventListener("click", () => closeAssetPicker(null));
document.querySelector("#asset-picker-cancel").addEventListener("click", () => closeAssetPicker(null));
document.querySelector("#asset-picker-confirm").addEventListener("click", () => closeAssetPicker(pickerAssets.filter((item) => selectedPickerAssets.has(item.id))));
assetPicker.addEventListener("cancel", (event) => {
  event.preventDefault();
  closeAssetPicker(null);
});

const knowledgeUI = {
  search: document.querySelector("#knowledge-search"),
  folderFilter: document.querySelector("#knowledge-folder-filter"),
  list: document.querySelector("#knowledge-list"),
  listMessage: document.querySelector("#knowledge-list-message"),
  form: document.querySelector("#knowledge-form"),
  title: document.querySelector("#knowledge-title"),
  folder: document.querySelector("#knowledge-folder"),
  tags: document.querySelector("#knowledge-tags"),
  content: document.querySelector("#knowledge-content"),
  assets: document.querySelector("#knowledge-assets"),
  error: document.querySelector("#knowledge-form-error"),
  message: document.querySelector("#knowledge-save-message"),
  delete: document.querySelector("#knowledge-delete"),
};

let knowledgeNotes = [];
let editingKnowledgeID = "";
let knowledgeIsDraft = false;
let knowledgeLinkedAssets = new Map();
let knowledgeSearchTimer = null;

async function refreshKnowledge() {
  knowledgeUI.listMessage.textContent = "正在读取条目…";
  const parameters = new URLSearchParams();
  if (knowledgeUI.search.value.trim()) parameters.set("q", knowledgeUI.search.value.trim());
  try {
    const response = await fetch("/api/v1/knowledge?" + parameters);
    if (!response.ok) throw new Error(await readAPIError(response));
    knowledgeNotes = (await response.json()).notes ?? [];
    renderKnowledgeFolders();
    renderKnowledgeList();
    if (!knowledgeIsDraft && !knowledgeNotes.some((note) => note.id === editingKnowledgeID)) {
      if (knowledgeNotes.length) await selectKnowledgeNote(knowledgeNotes[0]);
      else startKnowledgeDraft();
    }
  } catch (error) {
    knowledgeUI.listMessage.textContent = `读取失败：${error.message}`;
  }
}

function renderKnowledgeFolders() {
  const selected = knowledgeUI.folderFilter.value;
  const folders = [...new Set(knowledgeNotes.map((note) => note.folder).filter(Boolean))].sort((left, right) => left.localeCompare(right));
  knowledgeUI.folderFilter.replaceChildren(new Option("全部文件夹", ""));
  for (const folder of folders) knowledgeUI.folderFilter.append(new Option(folder, folder));
  knowledgeUI.folderFilter.value = folders.includes(selected) ? selected : "";
}

function visibleKnowledgeNotes() {
  const folder = knowledgeUI.folderFilter.value;
  return folder ? knowledgeNotes.filter((note) => note.folder === folder) : knowledgeNotes;
}

function renderKnowledgeList() {
  const notes = visibleKnowledgeNotes();
  knowledgeUI.list.replaceChildren();
  knowledgeUI.listMessage.textContent = notes.length ? "" : "当前筛选下没有条目。";
  for (const note of notes) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "knowledge-list-item";
    button.setAttribute("aria-current", String(note.id === editingKnowledgeID));
    const title = document.createElement("strong");
    title.textContent = note.title;
    const details = document.createElement("small");
    details.textContent = note.folder || "未分类";
    button.append(title, details);
    button.addEventListener("click", () => selectKnowledgeNote(note));
    knowledgeUI.list.append(button);
  }
}

async function selectKnowledgeNote(note) {
  editingKnowledgeID = note.id;
  knowledgeIsDraft = false;
  document.querySelector("#knowledge-editor-heading").textContent = note.title;
  knowledgeUI.title.value = note.title;
  knowledgeUI.folder.value = note.folder || "";
  knowledgeUI.tags.value = (note.tags ?? []).join(", ");
  knowledgeUI.content.value = note.content || "";
  knowledgeUI.error.textContent = "";
  knowledgeUI.message.textContent = "";
  knowledgeUI.delete.disabled = false;
  knowledgeLinkedAssets = new Map();
  await Promise.all((note.asset_ids ?? []).map(async (id) => {
    const response = await fetch(`/api/v1/assets/${encodeURIComponent(id)}`);
    if (response.ok) knowledgeLinkedAssets.set(id, await response.json());
    else knowledgeLinkedAssets.set(id, { id, display_name: id, state: "unknown" });
  }));
  renderKnowledgeAssets();
  renderKnowledgeList();
}

function startKnowledgeDraft() {
  editingKnowledgeID = "";
  knowledgeIsDraft = true;
  document.querySelector("#knowledge-editor-heading").textContent = "新建备忘录";
  knowledgeUI.form.reset();
  knowledgeUI.folderFilter.value = knowledgeUI.folderFilter.value || "";
  knowledgeUI.error.textContent = "";
  knowledgeUI.message.textContent = "尚未保存。";
  knowledgeUI.delete.disabled = true;
  knowledgeLinkedAssets = new Map();
  renderKnowledgeAssets();
  renderKnowledgeList();
  knowledgeUI.title.focus();
}

function renderKnowledgeAssets() {
  knowledgeUI.assets.replaceChildren();
  if (!knowledgeLinkedAssets.size) {
    const empty = document.createElement("p");
    empty.className = "inline-message";
    empty.textContent = "没有关联 Asset。";
    knowledgeUI.assets.append(empty);
    return;
  }
  for (const item of knowledgeLinkedAssets.values()) {
    const pill = document.createElement("span");
    pill.className = "knowledge-asset-pill";
    const name = document.createElement("span");
    name.textContent = item.display_name;
    name.title = item.id;
    const state = document.createElement("small");
    state.textContent = item.state === "active" ? "精选" : item.state === "archive" ? "归档" : "不可用";
    const remove = document.createElement("button");
    remove.type = "button";
    remove.setAttribute("aria-label", `移除 ${item.display_name}`);
    remove.textContent = "×";
    remove.addEventListener("click", () => {
      knowledgeLinkedAssets.delete(item.id);
      renderKnowledgeAssets();
    });
    pill.append(name, state, remove);
    knowledgeUI.assets.append(pill);
  }
}

async function chooseKnowledgeAssets() {
  const selected = await window.openAssetPicker({ multiple: true, selected: [...knowledgeLinkedAssets.keys()] });
  if (selected === null) return;
  const retained = [...knowledgeLinkedAssets.values()].filter((item) => item.state !== "active");
  knowledgeLinkedAssets = new Map(retained.map((item) => [item.id, item]));
  for (const item of selected) knowledgeLinkedAssets.set(item.id, item);
  renderKnowledgeAssets();
}

function knowledgeInput() {
  return {
    title: knowledgeUI.title.value.trim(),
    folder: knowledgeUI.folder.value.trim(),
    content: knowledgeUI.content.value,
    tags: knowledgeUI.tags.value.split(/[,，\n]/).map((tag) => tag.trim()).filter(Boolean),
    asset_ids: [...knowledgeLinkedAssets.keys()],
  };
}

async function saveKnowledgeNote(event) {
  event.preventDefault();
  knowledgeUI.error.textContent = "";
  knowledgeUI.message.textContent = "正在保存…";
  const path = editingKnowledgeID ? `/api/v1/knowledge/${encodeURIComponent(editingKnowledgeID)}` : "/api/v1/knowledge";
  const response = await fetch(path, {
    method: editingKnowledgeID ? "PUT" : "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(knowledgeInput()),
  });
  if (!response.ok) {
    knowledgeUI.message.textContent = "";
    knowledgeUI.error.textContent = await readAPIError(response);
    return;
  }
  const saved = await response.json();
  editingKnowledgeID = saved.id;
  knowledgeIsDraft = false;
  knowledgeUI.message.textContent = "已保存。";
  await refreshKnowledge();
  const refreshed = knowledgeNotes.find((note) => note.id === saved.id);
  if (refreshed) await selectKnowledgeNote(refreshed);
  knowledgeUI.message.textContent = "已保存。";
}

async function deleteKnowledgeNote() {
  const note = knowledgeNotes.find((candidate) => candidate.id === editingKnowledgeID);
  if (!note || !window.confirm(`删除知识条目“${note.title}”？`)) return;
  const response = await fetch(`/api/v1/knowledge/${encodeURIComponent(note.id)}`, { method: "DELETE" });
  if (!response.ok) {
    knowledgeUI.error.textContent = await readAPIError(response);
    return;
  }
  editingKnowledgeID = "";
  knowledgeIsDraft = false;
  await refreshKnowledge();
}

document.querySelector("#knowledge-new").addEventListener("click", startKnowledgeDraft);
knowledgeUI.search.addEventListener("input", () => {
  window.clearTimeout(knowledgeSearchTimer);
  knowledgeSearchTimer = window.setTimeout(refreshKnowledge, 180);
});
knowledgeUI.folderFilter.addEventListener("change", renderKnowledgeList);
knowledgeUI.form.addEventListener("submit", saveKnowledgeNote);
knowledgeUI.delete.addEventListener("click", deleteKnowledgeNote);
document.querySelector("#knowledge-choose-assets").addEventListener("click", chooseKnowledgeAssets);

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
backendUI.logClear.addEventListener("click", () => {
  backendLogClearOffset = backendLogNextOffset;
  backendLogBytes = new Uint8Array();
  renderBackendLog();
});
backendUI.logSave.addEventListener("click", async () => {
  try {
    const result = await backendAction(`/api/v1/backends/${selectedBackendID}/logs/save`);
    backendUI.actionMessage.textContent = `已保存到 ${result.path}`;
  } catch (error) {
    backendUI.actionMessage.textContent = error.message;
  }
});
document.querySelector("#backend-execution-kind").addEventListener("change", updateBackendExecutionFields);
document.querySelector("#backend-worker-test").addEventListener("click", testBackendWorkerConnection);
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
