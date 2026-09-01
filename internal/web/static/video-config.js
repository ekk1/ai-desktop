function element(selector) {
  return document.querySelector(selector);
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function objectFromJSON(value, label, stringValues = false) {
  const parsed = JSON.parse(value || "{}");
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error(`${label} 必须是 JSON Object`);
  }
  if (stringValues && Object.values(parsed).some((item) => typeof item !== "string")) {
    throw new Error(`${label} 的值必须都是字符串`);
  }
  return parsed;
}

function validateHeaders(headers) {
  for (const [name, value] of Object.entries(headers)) {
    if (!/^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/.test(name) || /[\r\n]/.test(value)) {
      throw new Error("Headers 包含无效名称或换行符");
    }
  }
  return headers;
}

function validateEnv(env) {
  for (const name of Object.keys(env)) {
    if (!/^[_A-Za-z][_A-Za-z0-9]*$/.test(name)) throw new Error(`Env 名称无效：${name}`);
  }
  return env;
}

function boundedNumber(selector, minimum, maximum, label) {
  const value = Number(element(selector).value);
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${label}必须是 ${minimum}–${maximum} 的整数`);
  }
  return value;
}

function configRow(title, detail, enabled, onEdit, onCopy, onDelete, onCapabilities) {
  const row = document.createElement("div");
  row.className = "config-row";
  const copy = document.createElement("div");
  const name = document.createElement("strong");
  name.textContent = title;
  const description = document.createElement("small");
  description.textContent = detail;
  copy.append(name, description);
  const actions = document.createElement("div");
  actions.className = "config-row-actions";
  const badge = document.createElement("span");
  badge.className = "state-pill";
  badge.textContent = enabled ? "启用" : "停用";
  actions.append(badge);
  if (onCapabilities) actions.append(actionButton("测试 capabilities", onCapabilities));
  actions.append(actionButton("编辑", onEdit), actionButton("复制", onCopy), actionButton("删除", onDelete, "danger-text"));
  row.append(copy, actions);
  return row;
}

function actionButton(label, handler, className = "") {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `text-button ${className}`.trim();
  button.textContent = label;
  button.addEventListener("click", handler);
  return button;
}

function emptyRow(text) {
  const empty = document.createElement("p");
  empty.className = "inline-message";
  empty.textContent = text;
  return empty;
}

function copiedID(value) {
  return `${value || "preset"}-copy`;
}

export function createVideoConfig({ readAPIError }) {
  const workspace = element("#settings-workspace");
  const status = element("#video-config-save-status");
  const refreshButton = element("#video-config-refresh");
  const saveButton = element("#video-config-save");
  const httpList = element("#video-http-provider-list");
  const cliList = element("#video-cli-preset-list");
  const tailList = element("#video-tail-frame-preset-list");
  const httpDialog = element("#video-http-provider-editor");
  const cliDialog = element("#video-cli-preset-editor");
  const tailDialog = element("#video-tail-frame-preset-editor");
  let configuration = null;
  let loaded = false;
  let busy = false;
  let editing = { kind: "", index: -1 };

  function setBusy(value) {
    busy = value;
    refreshButton.disabled = busy;
    saveButton.disabled = busy || !configuration;
    document.querySelectorAll("[data-video-new]").forEach((button) => { button.disabled = busy || !configuration; });
    if (configuration) render();
  }

  async function refresh() {
    if (busy) return;
    setBusy(true);
    status.textContent = "正在读取视频配置…";
    try {
      const response = await fetch("/api/v1/videos/config", { cache: "no-store" });
      if (!response.ok) throw new Error(await readAPIError(response));
      configuration = await response.json();
      configuration.http_providers ??= [];
      configuration.cli_presets ??= [];
      configuration.tail_frame_presets ??= [];
      loaded = true;
      render();
      status.textContent = "视频配置已读取；草稿需点击保存后生效。";
    } catch (error) {
      status.textContent = `读取失败：${error.message}`;
    } finally {
      setBusy(false);
    }
  }

  function render() {
    renderList(httpList, configuration.http_providers, "http", "还没有 HTTP Provider。", (preset) => `${preset.id} · ${preset.base_url}`);
    renderList(cliList, configuration.cli_presets, "cli", "还没有 CLI Preset。", (preset) => `${preset.id} · ${preset.command_template}`);
    renderList(tailList, configuration.tail_frame_presets, "tail", "还没有 Tail Frame Preset。", (preset) => `${preset.id} · ${preset.output_extension}`);
  }

  function renderList(list, presets, kind, empty, detail) {
    list.replaceChildren();
    presets.forEach((preset, index) => {
      list.append(configRow(preset.name, detail(preset), preset.enabled,
        () => openEditor(kind, index),
        () => duplicate(kind, index),
        () => remove(kind, index),
        kind === "http" && preset.enabled ? () => testCapabilities(preset) : null));
    });
    if (!presets.length) list.append(emptyRow(empty));
  }

  function presetsFor(kind) {
    return kind === "http" ? configuration.http_providers : kind === "cli" ? configuration.cli_presets : configuration.tail_frame_presets;
  }

  function remove(kind, index) {
    if (busy) return;
    presetsFor(kind).splice(index, 1);
    render();
    status.textContent = "预设草稿已删除；保存后生效。";
  }

  function duplicate(kind, index) {
    if (busy) return;
    const preset = clone(presetsFor(kind)[index]);
    preset.id = copiedID(preset.id);
    preset.name = `${preset.name} 副本`;
    presetsFor(kind).splice(index + 1, 0, preset);
    render();
    status.textContent = "预设已复制到草稿；请编辑唯一 ID 后保存。";
  }

  function openEditor(kind, index = -1) {
    if (!configuration || busy) return;
    editing = { kind, index };
    const preset = index >= 0 ? presetsFor(kind)[index] : defaultsFor(kind);
    if (kind === "http") fillHTTP(preset, index >= 0);
    if (kind === "cli") fillCLI(preset, index >= 0);
    if (kind === "tail") fillTail(preset, index >= 0);
  }

  function defaultsFor(kind) {
    if (kind === "http") return { id: "video-http-new", name: "Video HTTP Provider", base_url: "http://127.0.0.1:1234", headers: {}, connect_timeout_seconds: 10, job_timeout_seconds: 3600, poll_interval_milliseconds: 750, max_request_bytes: 402653184, max_error_bytes: 65536, max_video_bytes: 1073741824, max_input_image_bytes: 268435456, max_concurrent_jobs: 1, enabled: true, default_params: {} };
    if (kind === "cli") return { id: "video-cli-new", name: "Local Video CLI", enabled: true, execution_kind: "local_cli", prepare_command_template: "", command_template: "generate-video --output {{OUTPUT_PATH}}", work_dir: "", env: {}, timeout_seconds: 3600, stop_grace_seconds: 10, log_buffer_bytes: 1048576, output_relative_path: "outputs/result.webm", output_media_type: "video/webm", output_extension: ".webm", max_output_bytes: 1073741824, default_params: {} };
    return { id: "tail-frame-new", name: "Tail Frame", enabled: true, command_template: "extract-tail --output {{OUTPUT_IMAGE}}", timeout_seconds: 300, stop_grace_seconds: 10, max_image_bytes: 268435456, output_extension: ".png" };
  }

  function json(value) {
    return JSON.stringify(value ?? {}, null, 2);
  }

  function fillHTTP(preset, existing) {
    element("#video-http-provider-editor-title").textContent = existing ? `编辑 ${preset.name}` : "新增 HTTP Provider";
    setValues({
      "#video-http-provider-id": preset.id, "#video-http-provider-name": preset.name, "#video-http-provider-base-url": preset.base_url,
      "#video-http-provider-concurrency": preset.max_concurrent_jobs, "#video-http-provider-headers": json(preset.headers), "#video-http-provider-default-params": json(preset.default_params),
      "#video-http-provider-connect-timeout": preset.connect_timeout_seconds, "#video-http-provider-job-timeout": preset.job_timeout_seconds, "#video-http-provider-poll-interval": preset.poll_interval_milliseconds,
      "#video-http-provider-max-request": preset.max_request_bytes, "#video-http-provider-max-error": preset.max_error_bytes, "#video-http-provider-max-video": preset.max_video_bytes, "#video-http-provider-max-input": preset.max_input_image_bytes,
    });
    element("#video-http-provider-enabled").checked = Boolean(preset.enabled);
    element("#video-http-provider-capability-status").textContent = "保存后可测试 capabilities。";
    element("#video-http-provider-error").textContent = "";
    httpDialog.showModal();
  }

  function fillCLI(preset, existing) {
    element("#video-cli-preset-editor-title").textContent = existing ? `编辑 ${preset.name}` : "新增 CLI Preset";
    setValues({
      "#video-cli-preset-id": preset.id, "#video-cli-preset-name": preset.name, "#video-cli-preset-command": preset.command_template, "#video-cli-preset-work-dir": preset.work_dir,
      "#video-cli-preset-output-path": preset.output_relative_path, "#video-cli-preset-output-media-type": preset.output_media_type, "#video-cli-preset-output-extension": preset.output_extension,
      "#video-cli-preset-prepare-command": preset.prepare_command_template, "#video-cli-preset-env": json(preset.env), "#video-cli-preset-default-params": json(preset.default_params),
      "#video-cli-preset-timeout": preset.timeout_seconds, "#video-cli-preset-stop-grace": preset.stop_grace_seconds, "#video-cli-preset-log-buffer": preset.log_buffer_bytes, "#video-cli-preset-max-output": preset.max_output_bytes,
    });
    element("#video-cli-preset-enabled").checked = Boolean(preset.enabled);
    element("#video-cli-preset-error").textContent = "";
    cliDialog.showModal();
  }

  function fillTail(preset, existing) {
    element("#video-tail-frame-preset-editor-title").textContent = existing ? `编辑 ${preset.name}` : "新增 Tail Frame Preset";
    setValues({
      "#video-tail-frame-preset-id": preset.id, "#video-tail-frame-preset-name": preset.name, "#video-tail-frame-preset-command": preset.command_template,
      "#video-tail-frame-preset-extension": preset.output_extension, "#video-tail-frame-preset-timeout": preset.timeout_seconds,
      "#video-tail-frame-preset-stop-grace": preset.stop_grace_seconds, "#video-tail-frame-preset-max-image": preset.max_image_bytes,
    });
    element("#video-tail-frame-preset-enabled").checked = Boolean(preset.enabled);
    element("#video-tail-frame-preset-error").textContent = "";
    tailDialog.showModal();
  }

  function setValues(values) {
    Object.entries(values).forEach(([selector, value]) => { element(selector).value = String(value ?? ""); });
  }

  function required(value, label) {
    const trimmed = value.trim();
    if (!trimmed) throw new Error(`${label}为必填项`);
    return trimmed;
  }

  function validID(value) {
    if (!/^[A-Za-z0-9._-]{1,120}$/.test(value)) throw new Error("ID 仅可包含字母、数字、点、下划线和连字符");
    return value;
  }

  function readHTTP() {
    const baseURL = required(element("#video-http-provider-base-url").value, "Base URL").replace(/\/+$/, "");
    if (!/^https?:\/\/[^/?#]+(?:\/[^?#]*)?$/.test(baseURL)) throw new Error("Base URL 必须是无查询参数的绝对 HTTP(S) URL");
    return {
      id: validID(required(element("#video-http-provider-id").value, "ID")), name: required(element("#video-http-provider-name").value, "名称"), base_url: baseURL,
      headers: validateHeaders(objectFromJSON(element("#video-http-provider-headers").value, "Headers", true)), default_params: objectFromJSON(element("#video-http-provider-default-params").value, "Default Params"),
      connect_timeout_seconds: boundedNumber("#video-http-provider-connect-timeout", 1, 86400, "连接超时"), job_timeout_seconds: boundedNumber("#video-http-provider-job-timeout", 1, 86400, "任务超时"),
      poll_interval_milliseconds: boundedNumber("#video-http-provider-poll-interval", 100, 10000, "轮询间隔"), max_request_bytes: boundedNumber("#video-http-provider-max-request", 1, 2147483648, "请求上限"),
      max_error_bytes: boundedNumber("#video-http-provider-max-error", 1, 65536, "错误响应上限"), max_video_bytes: boundedNumber("#video-http-provider-max-video", 1, 4294967296, "视频上限"),
      max_input_image_bytes: boundedNumber("#video-http-provider-max-input", 1, 1073741824, "输入图上限"), max_concurrent_jobs: boundedNumber("#video-http-provider-concurrency", 1, 16, "最大并发任务"), enabled: element("#video-http-provider-enabled").checked,
    };
  }

  function readCLI() {
    const outputPath = required(element("#video-cli-preset-output-path").value, "精确输出路径");
    if (!outputPath.startsWith("outputs/") || outputPath.includes("..") || outputPath.startsWith("/")) throw new Error("精确输出路径必须保持在 outputs/ 目录下");
    const workDir = element("#video-cli-preset-work-dir").value.trim();
    if (workDir && !workDir.startsWith("/")) throw new Error("工作目录必须是绝对路径或留空");
    const extension = required(element("#video-cli-preset-output-extension").value, "输出扩展名");
    if (!/^\.[^/\\.]+$/.test(extension)) throw new Error("输出扩展名必须以点开头且不含路径");
    return {
      id: validID(required(element("#video-cli-preset-id").value, "ID")), name: required(element("#video-cli-preset-name").value, "名称"), enabled: element("#video-cli-preset-enabled").checked, execution_kind: "local_cli",
      prepare_command_template: element("#video-cli-preset-prepare-command").value, command_template: required(element("#video-cli-preset-command").value, "命令模板"), work_dir: workDir,
      env: validateEnv(objectFromJSON(element("#video-cli-preset-env").value, "Env", true)), default_params: objectFromJSON(element("#video-cli-preset-default-params").value, "Default Params"),
      timeout_seconds: boundedNumber("#video-cli-preset-timeout", 1, 86400, "超时"), stop_grace_seconds: boundedNumber("#video-cli-preset-stop-grace", 0, 3600, "停止宽限"),
      log_buffer_bytes: boundedNumber("#video-cli-preset-log-buffer", 1, 16777216, "日志缓存"), output_relative_path: outputPath, output_media_type: required(element("#video-cli-preset-output-media-type").value, "输出 MIME"), output_extension: extension,
      max_output_bytes: boundedNumber("#video-cli-preset-max-output", 1, 4294967296, "输出上限"),
    };
  }

  function readTail() {
    return {
      id: validID(required(element("#video-tail-frame-preset-id").value, "ID")), name: required(element("#video-tail-frame-preset-name").value, "名称"), enabled: element("#video-tail-frame-preset-enabled").checked,
      command_template: required(element("#video-tail-frame-preset-command").value, "命令模板"), output_extension: element("#video-tail-frame-preset-extension").value,
      timeout_seconds: boundedNumber("#video-tail-frame-preset-timeout", 1, 86400, "超时"), stop_grace_seconds: boundedNumber("#video-tail-frame-preset-stop-grace", 0, 3600, "停止宽限"), max_image_bytes: boundedNumber("#video-tail-frame-preset-max-image", 1, 1073741824, "图片上限"),
    };
  }

  function saveDraft(event, kind, reader, dialog) {
    event.preventDefault();
    try {
      const preset = reader();
      const presets = presetsFor(kind);
      if (editing.index >= 0) presets[editing.index] = preset;
      else presets.push(preset);
      dialog.close();
      render();
      status.textContent = "预设草稿已修改；请保存视频配置。";
    } catch (error) {
      element(`#video-${kind === "http" ? "http-provider" : kind === "cli" ? "cli-preset" : "tail-frame-preset"}-error`).textContent = error.message;
    }
  }

  async function save() {
    if (!configuration || busy) return;
    setBusy(true);
    status.textContent = "正在保存视频配置…";
    try {
      const response = await fetch("/api/v1/videos/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(clone(configuration)) });
      if (!response.ok) throw new Error(await readAPIError(response));
      configuration = await response.json();
      render();
      status.textContent = "视频配置已保存并立即生效。";
    } catch (error) {
      status.textContent = `保存失败：${error.message}`;
    } finally {
      setBusy(false);
    }
  }

  async function testCapabilities(provider) {
    status.textContent = `正在读取 ${provider.name} capabilities…`;
    try {
      const response = await fetch(`/api/v1/videos/providers/${encodeURIComponent(provider.id)}/capabilities`, { cache: "no-store" });
      if (!response.ok) throw new Error(await readAPIError(response));
      const capabilities = await response.json();
      const formats = capabilities.output_formats_by_mode?.vid_gen ?? capabilities.output_formats ?? [];
      const features = capabilities.features_by_mode?.vid_gen ?? capabilities.features ?? {};
      const featureText = Object.entries(features).filter(([, enabled]) => enabled).map(([name]) => name).join("、") || "无功能提示";
      status.textContent = `${provider.name} · vid_gen ${capabilities.video_generation_supported ? "支持" : "不支持"} · 输出：${formats.join("、") || "未报告"} · 功能：${featureText}`;
    } catch (error) {
      status.textContent = `${provider.name} 测试失败：${error.message}`;
    }
  }

  refreshButton.addEventListener("click", refresh);
  saveButton.addEventListener("click", save);
  element("#video-http-provider-new").dataset.videoNew = "true";
  element("#video-cli-preset-new").dataset.videoNew = "true";
  element("#video-tail-frame-preset-new").dataset.videoNew = "true";
  element("#video-http-provider-new").addEventListener("click", () => openEditor("http"));
  element("#video-cli-preset-new").addEventListener("click", () => openEditor("cli"));
  element("#video-tail-frame-preset-new").addEventListener("click", () => openEditor("tail"));
  element("#video-http-provider-form").addEventListener("submit", (event) => saveDraft(event, "http", readHTTP, httpDialog));
  element("#video-cli-preset-form").addEventListener("submit", (event) => saveDraft(event, "cli", readCLI, cliDialog));
  element("#video-tail-frame-preset-form").addEventListener("submit", (event) => saveDraft(event, "tail", readTail, tailDialog));
  document.querySelectorAll("[data-video-dialog-close]").forEach((button) => button.addEventListener("click", closeEditors));

  function closeEditors() {
    [httpDialog, cliDialog, tailDialog].forEach((dialog) => { if (dialog.open) dialog.close(); });
  }

  return {
    async enter() {
      workspace.hidden = false;
      if (!loaded) await refresh();
    },
    leave() {
      workspace.hidden = true;
      closeEditors();
    },
  };
}
