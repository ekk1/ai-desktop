function element(selector) {
  return document.querySelector(selector);
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function objectFromJSON(value, label) {
  const parsed = JSON.parse(value || "{}");
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error(`${label} 必须是 JSON Object`);
  }
  return parsed;
}

function configRow(title, detail, status, edit, remove) {
  const row = document.createElement("div");
  row.className = "config-row";
  const copy = document.createElement("div");
  const heading = document.createElement("strong");
  heading.textContent = title;
  const description = document.createElement("small");
  description.textContent = detail;
  copy.append(heading, description);
  const actions = document.createElement("div");
  actions.className = "config-row-actions";
  if (status) {
    const badge = document.createElement("span");
    badge.className = "state-pill";
    badge.textContent = status;
    actions.append(badge);
  }
  const editButton = document.createElement("button");
  editButton.type = "button";
  editButton.className = "text-button";
  editButton.textContent = "编辑";
  editButton.addEventListener("click", edit);
  const deleteButton = document.createElement("button");
  deleteButton.type = "button";
  deleteButton.className = "text-button danger-text";
  deleteButton.textContent = "删除";
  deleteButton.addEventListener("click", remove);
  actions.append(editButton, deleteButton);
  row.append(copy, actions);
  return row;
}

export function createLLMConfig({ readAPIError }) {
  const workspace = element("#settings-workspace");
  const status = element("#llm-config-save-status");
  const providerList = element("#llm-provider-list");
  const quickPathList = element("#llm-quick-path-list");
  const templateList = element("#llm-template-list");
  const providerDialog = element("#llm-provider-editor");
  const quickPathDialog = element("#llm-quick-path-editor");
  const templateDialog = element("#llm-template-editor");
  let configuration = null;
  let providerIndex = -1;
  let quickPathIndex = -1;
  let templateIndex = -1;

  async function load() {
    status.textContent = "正在读取配置…";
    try {
      const response = await fetch("/api/v1/llm/config", { cache: "no-store" });
      if (!response.ok) throw new Error(await readAPIError(response));
      configuration = await response.json();
      fillExa();
      render();
      status.textContent = "配置已读取；编辑内容需点击“保存全部”才会生效。";
    } catch (error) {
      status.textContent = error.message;
    }
  }

  function fillExa() {
    const exa = configuration?.exa ?? {};
    element("#llm-exa-url").value = exa.api_url ?? "https://api.exa.ai/search";
    element("#llm-exa-key").value = exa.api_key ?? "";
    element("#llm-exa-timeout").value = String(exa.timeout_seconds ?? 60);
    element("#llm-exa-max-response").value = String(exa.max_response_bytes ?? 16777216);
  }

  function readExa() {
    return {
      api_url: element("#llm-exa-url").value.trim(),
      api_key: element("#llm-exa-key").value,
      timeout_seconds: Number(element("#llm-exa-timeout").value),
      max_response_bytes: Number(element("#llm-exa-max-response").value),
    };
  }

  function render() {
    if (!configuration) return;
    providerList.replaceChildren();
    configuration.providers.forEach((provider, index) => {
      providerList.append(configRow(
        provider.name,
        `${provider.id} · ${provider.url}`,
        provider.enabled ? "启用" : "停用",
        () => openProvider(index),
        () => { configuration.providers.splice(index, 1); render(); },
      ));
    });
    if (!configuration.providers.length) providerList.append(emptyRow("没有 Provider。"));

    quickPathList.replaceChildren();
    configuration.quick_paths.forEach((quickPath, index) => {
      quickPathList.append(configRow(
        quickPath.name,
        `${quickPath.id} · ${quickPath.provider_id}${quickPath.model ? ` · ${quickPath.model}` : ""}`,
        "",
        () => openQuickPath(index),
        () => { configuration.quick_paths.splice(index, 1); render(); },
      ));
    });
    if (!configuration.quick_paths.length) quickPathList.append(emptyRow("没有 Quick Path。"));

    templateList.replaceChildren();
    configuration.prompt_templates.forEach((template, index) => {
      templateList.append(configRow(
        template.name,
        `${template.id} · ${template.content.length} 字符`,
        "",
        () => openTemplate(index),
        () => { configuration.prompt_templates.splice(index, 1); render(); },
      ));
    });
    if (!configuration.prompt_templates.length) templateList.append(emptyRow("没有 Prompt Template。"));
  }

  function emptyRow(message) {
    const paragraph = document.createElement("p");
    paragraph.className = "inline-message";
    paragraph.textContent = message;
    return paragraph;
  }

  function openProvider(index = -1) {
    providerIndex = index;
    const item = index >= 0 ? configuration.providers[index] : {
      id: "provider-new", name: "New Provider", url: "http://127.0.0.1:8080/completion", method: "POST",
      api_key: "", headers: { "Content-Type": "application/json" },
      body_template: "{\"prompt\":${CONTENT_JSON}}", response_mode: "json", response_content_path: "content",
      stream_content_path: "", stream_done_path: "", connect_timeout_seconds: 10, total_timeout_seconds: 600,
      max_response_bytes: 16777216, max_asset_bytes: 33554432, enabled: true,
    };
    element("#llm-provider-editor-title").textContent = index >= 0 ? `编辑 ${item.name}` : "新增 Provider";
    element("#llm-provider-id").value = item.id;
    element("#llm-provider-name").value = item.name;
    element("#llm-provider-url").value = item.url;
    element("#llm-provider-connect-timeout").value = String(item.connect_timeout_seconds);
    element("#llm-provider-total-timeout").value = String(item.total_timeout_seconds);
    element("#llm-provider-enabled").checked = Boolean(item.enabled);
    element("#llm-provider-api-key").value = item.api_key ?? "";
    element("#llm-provider-headers").value = JSON.stringify(item.headers ?? {}, null, 2);
    element("#llm-provider-body").value = item.body_template;
    element("#llm-provider-response-mode").value = item.response_mode;
    element("#llm-provider-content-path").value = item.response_content_path ?? "";
    element("#llm-provider-stream-path").value = item.stream_content_path ?? "";
    element("#llm-provider-done-path").value = item.stream_done_path ?? "";
    element("#llm-provider-max-response").value = String(item.max_response_bytes);
    element("#llm-provider-max-assets").value = String(item.max_asset_bytes);
    element("#llm-provider-error").textContent = "";
    providerDialog.showModal();
  }

  function saveProviderDraft(event) {
    event.preventDefault();
    try {
      const headers = objectFromJSON(element("#llm-provider-headers").value, "Headers");
      for (const value of Object.values(headers)) {
        if (typeof value !== "string") throw new Error("Header 值必须是字符串");
      }
      const item = {
        id: element("#llm-provider-id").value.trim(),
        name: element("#llm-provider-name").value.trim(),
        url: element("#llm-provider-url").value.trim(), method: "POST",
        api_key: element("#llm-provider-api-key").value, headers,
        body_template: element("#llm-provider-body").value,
        response_mode: element("#llm-provider-response-mode").value,
        response_content_path: element("#llm-provider-content-path").value.trim(),
        stream_content_path: element("#llm-provider-stream-path").value.trim(),
        stream_done_path: element("#llm-provider-done-path").value.trim(),
        connect_timeout_seconds: Number(element("#llm-provider-connect-timeout").value),
        total_timeout_seconds: Number(element("#llm-provider-total-timeout").value),
        max_response_bytes: Number(element("#llm-provider-max-response").value),
        max_asset_bytes: Number(element("#llm-provider-max-assets").value),
        enabled: element("#llm-provider-enabled").checked,
      };
      if (providerIndex >= 0) configuration.providers[providerIndex] = item;
      else configuration.providers.push(item);
      providerDialog.close();
      render();
      status.textContent = "Provider 草稿已修改；请保存全部。";
    } catch (error) {
      element("#llm-provider-error").textContent = error.message;
    }
  }

  function openQuickPath(index = -1) {
    quickPathIndex = index;
    const item = index >= 0 ? configuration.quick_paths[index] : {
      id: "path-new", name: "New Path", provider_id: configuration.providers[0]?.id ?? "", model: "", params: {},
    };
    const providerSelect = element("#llm-quick-path-provider");
    providerSelect.replaceChildren();
    for (const provider of configuration.providers) {
      const option = document.createElement("option");
      option.value = provider.id;
      option.textContent = provider.name;
      providerSelect.append(option);
    }
    element("#llm-quick-path-id").value = item.id;
    element("#llm-quick-path-name").value = item.name;
    providerSelect.value = item.provider_id;
    element("#llm-quick-path-model").value = item.model ?? "";
    element("#llm-quick-path-params").value = JSON.stringify(item.params ?? {}, null, 2);
    element("#llm-quick-path-error").textContent = "";
    quickPathDialog.showModal();
  }

  function saveQuickPathDraft(event) {
    event.preventDefault();
    try {
      const item = {
        id: element("#llm-quick-path-id").value.trim(),
        name: element("#llm-quick-path-name").value.trim(),
        provider_id: element("#llm-quick-path-provider").value,
        model: element("#llm-quick-path-model").value.trim(),
        params: objectFromJSON(element("#llm-quick-path-params").value, "Params"),
      };
      if (quickPathIndex >= 0) configuration.quick_paths[quickPathIndex] = item;
      else configuration.quick_paths.push(item);
      quickPathDialog.close();
      render();
      status.textContent = "Quick Path 草稿已修改；请保存全部。";
    } catch (error) {
      element("#llm-quick-path-error").textContent = error.message;
    }
  }

  function openTemplate(index = -1) {
    templateIndex = index;
    const item = index >= 0 ? configuration.prompt_templates[index] : { id: "template-new", name: "New Template", content: "" };
    element("#llm-template-id").value = item.id;
    element("#llm-template-name").value = item.name;
    element("#llm-template-content").value = item.content;
    element("#llm-template-error").textContent = "";
    templateDialog.showModal();
  }

  function saveTemplateDraft(event) {
    event.preventDefault();
    const item = {
      id: element("#llm-template-id").value.trim(),
      name: element("#llm-template-name").value.trim(),
      content: element("#llm-template-content").value,
    };
    if (templateIndex >= 0) configuration.prompt_templates[templateIndex] = item;
    else configuration.prompt_templates.push(item);
    templateDialog.close();
    render();
    status.textContent = "Prompt Template 草稿已修改；请保存全部。";
  }

  async function save() {
    if (!configuration) return;
    status.textContent = "正在保存…";
    const candidate = clone(configuration);
    candidate.exa = readExa();
    const response = await fetch("/api/v1/llm/config", {
      method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(candidate),
    });
    if (!response.ok) {
      status.textContent = await readAPIError(response);
      return;
    }
    configuration = await response.json();
    fillExa();
    render();
    status.textContent = "配置已保存并立即生效。";
  }

  async function addPreset() {
    status.textContent = "正在添加 llama.cpp 预设…";
    const response = await fetch("/api/v1/llm/providers/preset/llama-completion", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
    });
    if (!response.ok) {
      status.textContent = await readAPIError(response);
      return;
    }
    configuration = await response.json();
    fillExa();
    render();
    status.textContent = "llama.cpp 预设已添加。";
  }

  element("#llm-provider-new").addEventListener("click", () => openProvider());
  element("#llm-quick-path-new").addEventListener("click", () => openQuickPath());
  element("#llm-template-new").addEventListener("click", () => openTemplate());
  element("#llm-provider-form").addEventListener("submit", saveProviderDraft);
  element("#llm-quick-path-form").addEventListener("submit", saveQuickPathDraft);
  element("#llm-template-form").addEventListener("submit", saveTemplateDraft);
  element("#llm-config-save").addEventListener("click", save);
  element("#llm-config-refresh").addEventListener("click", load);
  element("#llm-preset-llama").addEventListener("click", addPreset);
  document.querySelectorAll("[data-close-dialog]").forEach((button) => {
    button.addEventListener("click", () => element(`#${button.dataset.closeDialog}`).close());
  });

  return {
    enter() {
      workspace.hidden = false;
      if (!configuration) load();
    },
    leave() {
      workspace.hidden = true;
    },
  };
}
