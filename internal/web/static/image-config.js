function element(selector) {
  return document.querySelector(selector);
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function parseHeaders(value) {
  const headers = JSON.parse(value || "{}");
  if (!headers || Array.isArray(headers) || typeof headers !== "object") {
    throw new Error("Headers 必须是 JSON Object");
  }
  for (const [name, content] of Object.entries(headers)) {
    if (!name || typeof content !== "string") throw new Error("Headers 的名称和值都必须是字符串");
  }
  return headers;
}

function boundedNumber(selector, minimum, maximum, label) {
  const value = Number(element(selector).value);
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${label}必须是 ${minimum}–${maximum} 的整数`);
  }
  return value;
}

export function createImageConfig({ readAPIError }) {
  const workspace = element("#settings-workspace");
  const list = element("#image-provider-list");
  const status = element("#image-config-save-status");
  const dialog = element("#image-provider-editor");
  const form = element("#image-provider-form");
  const refreshButton = element("#image-config-refresh");
  const newButton = element("#image-provider-new");
  const saveButton = element("#image-config-save");
  let configuration = null;
  let editingIndex = -1;
  let busy = false;

  function setBusy(value) {
    busy = value;
    refreshButton.disabled = busy;
    newButton.disabled = busy || !configuration;
    saveButton.disabled = busy || !configuration;
    if (configuration) render();
  }

  async function load() {
    if (busy) return;
    setBusy(true);
    status.textContent = "正在读取 Image 配置…";
    try {
      const response = await fetch("/api/v1/images/config", { cache: "no-store" });
      if (!response.ok) throw new Error(await readAPIError(response));
      configuration = await response.json();
      render();
      status.textContent = "Image 配置已读取；草稿需点击保存后生效。";
    } catch (error) {
      status.textContent = `读取失败：${error.message}`;
    } finally {
      setBusy(false);
    }
  }

  function render() {
    list.replaceChildren();
    for (const [index, provider] of (configuration?.providers ?? []).entries()) {
      const row = document.createElement("div");
      row.className = "config-row image-provider-row";
      const copy = document.createElement("div");
      const name = document.createElement("strong");
      name.textContent = provider.name;
      const detail = document.createElement("small");
      detail.textContent = `${provider.id} · ${provider.base_url}`;
      copy.append(name, detail);
      const actions = document.createElement("div");
      actions.className = "config-row-actions";
      const badge = document.createElement("span");
      badge.className = "state-pill";
      badge.textContent = provider.enabled ? "启用" : "停用";
      const test = document.createElement("button");
      test.type = "button";
      test.className = "text-button";
      test.textContent = "测试 capabilities";
      test.disabled = busy || !provider.enabled;
      test.dataset.imageConfigAction = "true";
      test.addEventListener("click", () => testCapabilities(provider));
      const edit = document.createElement("button");
      edit.type = "button";
      edit.className = "text-button";
      edit.textContent = "编辑";
      edit.disabled = busy;
      edit.dataset.imageConfigAction = "true";
      edit.addEventListener("click", () => openEditor(index));
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "text-button danger-text";
      remove.textContent = "删除";
      remove.disabled = busy;
      remove.dataset.imageConfigAction = "true";
      remove.addEventListener("click", () => {
        configuration.providers.splice(index, 1);
        render();
        status.textContent = "Provider 草稿已删除；保存后生效。";
      });
      actions.append(badge, test, edit, remove);
      row.append(copy, actions);
      list.append(row);
    }
    if (!configuration?.providers?.length) {
      const empty = document.createElement("p");
      empty.className = "inline-message";
      empty.textContent = "还没有 Image Provider。";
      list.append(empty);
    }
  }

  function openEditor(index = -1) {
    editingIndex = index;
    const provider = index >= 0 ? configuration.providers[index] : {
      id: "sdcpp-new", name: "stable-diffusion.cpp", base_url: "http://127.0.0.1:1234", headers: {},
      connect_timeout_seconds: 10, job_timeout_seconds: 3600, poll_interval_milliseconds: 750,
      max_response_bytes: 268435456, max_image_bytes: 134217728, max_concurrent_jobs: 1, enabled: true,
    };
    element("#image-provider-editor-title").textContent = index >= 0 ? `编辑 ${provider.name}` : "新增 Image Provider";
    element("#image-provider-id").value = provider.id;
    element("#image-provider-name").value = provider.name;
    element("#image-provider-base-url").value = provider.base_url;
    element("#image-provider-concurrency").value = String(provider.max_concurrent_jobs);
    element("#image-provider-enabled").checked = Boolean(provider.enabled);
    element("#image-provider-headers").value = JSON.stringify(provider.headers ?? {}, null, 2);
    element("#image-provider-connect-timeout").value = String(provider.connect_timeout_seconds);
    element("#image-provider-job-timeout").value = String(provider.job_timeout_seconds);
    element("#image-provider-poll-interval").value = String(provider.poll_interval_milliseconds);
    element("#image-provider-max-response").value = String(provider.max_response_bytes);
    element("#image-provider-max-image").value = String(provider.max_image_bytes);
    element("#image-provider-capability-status").textContent = "保存配置后可在列表测试 capabilities。";
    element("#image-provider-error").textContent = "";
    dialog.showModal();
  }

  function saveDraft(event) {
    event.preventDefault();
    try {
      const baseURL = element("#image-provider-base-url").value.trim().replace(/\/+$/, "");
      const provider = {
        id: element("#image-provider-id").value.trim(),
        name: element("#image-provider-name").value.trim(),
        base_url: baseURL,
        headers: parseHeaders(element("#image-provider-headers").value),
        connect_timeout_seconds: boundedNumber("#image-provider-connect-timeout", 1, 300, "连接超时"),
        job_timeout_seconds: boundedNumber("#image-provider-job-timeout", 1, 86400, "任务超时"),
        poll_interval_milliseconds: boundedNumber("#image-provider-poll-interval", 100, 10000, "轮询间隔"),
        max_response_bytes: boundedNumber("#image-provider-max-response", 1, 1073741824, "响应上限"),
        max_image_bytes: boundedNumber("#image-provider-max-image", 1, 1073741824, "单图上限"),
        max_concurrent_jobs: boundedNumber("#image-provider-concurrency", 1, 16, "最大并发任务"),
        enabled: element("#image-provider-enabled").checked,
      };
      if (!provider.id || !provider.name || !provider.base_url) throw new Error("ID、名称和 Base URL 均为必填项");
      if (editingIndex >= 0) configuration.providers[editingIndex] = provider;
      else configuration.providers.push(provider);
      dialog.close();
      render();
      status.textContent = "Provider 草稿已修改；请保存 Image 配置。";
    } catch (error) {
      element("#image-provider-error").textContent = error.message;
    }
  }

  async function save() {
    if (!configuration || busy) return;
    setBusy(true);
    status.textContent = "正在保存 Image 配置…";
    try {
      const response = await fetch("/api/v1/images/config", {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(clone(configuration)),
      });
      if (!response.ok) throw new Error(await readAPIError(response));
      configuration = await response.json();
      render();
      status.textContent = "Image 配置已保存并立即生效。";
    } catch (error) {
      status.textContent = `保存失败：${error.message}`;
    } finally {
      setBusy(false);
    }
  }

  async function testCapabilities(provider) {
    status.textContent = `正在读取 ${provider.name} capabilities…`;
    try {
      const response = await fetch(`/api/v1/images/providers/${encodeURIComponent(provider.id)}/capabilities`, { cache: "no-store" });
      if (!response.ok) throw new Error(await readAPIError(response));
      const capabilities = await response.json();
      const model = capabilities.model?.name || capabilities.model?.stem || "未报告模型";
      status.textContent = `${provider.name} 可用 · ${model} · ${capabilities.current_mode || "未知模式"}`;
    } catch (error) {
      status.textContent = `${provider.name} 测试失败：${error.message}`;
    }
  }

  newButton.disabled = true;
  saveButton.disabled = true;
  newButton.addEventListener("click", () => {
    if (!configuration || busy) return;
    openEditor();
  });
  refreshButton.addEventListener("click", load);
  saveButton.addEventListener("click", save);
  form.addEventListener("submit", saveDraft);
  document.querySelectorAll("[data-image-dialog-close]").forEach((button) => {
    button.addEventListener("click", () => dialog.close());
  });

  return {
    enter() {
      workspace.hidden = false;
      if (!configuration) load();
    },
    leave() {
      workspace.hidden = true;
      if (dialog.open) dialog.close();
    },
  };
}
