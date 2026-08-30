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
  window.location.hash = name;
  setSidebar(false);
}

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
