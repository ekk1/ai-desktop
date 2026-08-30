const terminalAttemptStates = new Set(["succeeded", "failed", "cancelled", "interrupted"]);
const activeAttemptStates = new Set(["queued", "submitting", "polling"]);
const defaultBaseParams = { width: 1024, height: 1024, seed: -1, batch_count: 1, output_format: "png" };

export function createImageWorkspace({ sidebarContent, sidebarSearch, readAPIError, openAssetPicker }) {
  const ui = {
    workspace: document.querySelector("#image-workspace"),
    sidebarControls: document.querySelector("#image-sidebar-controls"),
    folderFilter: document.querySelector("#image-batch-folder-filter"),
    newBatch: document.querySelector("#image-batch-new"),
    batchList: document.querySelector("#image-batch-list"),
    batchListStatus: document.querySelector("#image-batch-list-status"),
    batchForm: document.querySelector("#image-batch-form"),
    title: document.querySelector("#image-batch-title"),
    folder: document.querySelector("#image-batch-folder"),
    provider: document.querySelector("#image-batch-provider"),
    concurrency: document.querySelector("#image-batch-concurrency"),
    saveBatch: document.querySelector("#image-batch-save"),
    deleteBatch: document.querySelector("#image-batch-delete"),
    capabilities: document.querySelector("#image-batch-capabilities"),
    status: document.querySelector("#image-workspace-status"),
    baseJSON: document.querySelector("#image-base-params-json"),
    baseApply: document.querySelector("#image-base-params-apply"),
    baseError: document.querySelector("#image-base-params-error"),
    width: document.querySelector("#image-param-width"),
    height: document.querySelector("#image-param-height"),
    seed: document.querySelector("#image-param-seed"),
    batchCount: document.querySelector("#image-param-batch-count"),
    steps: document.querySelector("#image-param-steps"),
    cfg: document.querySelector("#image-param-cfg"),
    method: document.querySelector("#image-param-method"),
    scheduler: document.querySelector("#image-param-scheduler"),
    format: document.querySelector("#image-param-format"),
    methodOptions: document.querySelector("#image-method-options"),
    schedulerOptions: document.querySelector("#image-scheduler-options"),
    formatOptions: document.querySelector("#image-format-options"),
    itemList: document.querySelector("#image-item-list"),
    itemAdd: document.querySelector("#image-item-add"),
    bulkOpen: document.querySelector("#image-bulk-open"),
    runBatch: document.querySelector("#image-batch-run"),
    resultGrid: document.querySelector("#image-result-grid"),
    createDialog: document.querySelector("#image-batch-editor"),
    createForm: document.querySelector("#image-batch-create-form"),
    createTitle: document.querySelector("#image-new-batch-title"),
    createFolder: document.querySelector("#image-new-batch-folder"),
    createProvider: document.querySelector("#image-new-batch-provider"),
    createConcurrency: document.querySelector("#image-new-batch-concurrency"),
    createError: document.querySelector("#image-new-batch-error"),
    bulkDialog: document.querySelector("#image-bulk-editor"),
    bulkForm: document.querySelector("#image-bulk-form"),
    bulkPrompts: document.querySelector("#image-bulk-prompts"),
    bulkError: document.querySelector("#image-bulk-error"),
    itemDialog: document.querySelector("#image-item-editor"),
    itemForm: document.querySelector("#image-item-form"),
    itemID: document.querySelector("#image-item-id"),
    itemPrompt: document.querySelector("#image-item-prompt"),
    itemNegative: document.querySelector("#image-item-negative"),
    itemOverride: document.querySelector("#image-item-override"),
    itemError: document.querySelector("#image-item-error"),
    itemHistory: document.querySelector("#image-attempt-history"),
    itemTechnical: document.querySelector("#image-attempt-technical"),
    itemUp: document.querySelector("#image-item-up"),
    itemDown: document.querySelector("#image-item-down"),
    itemCopy: document.querySelector("#image-item-copy"),
    itemDelete: document.querySelector("#image-item-delete"),
    itemRun: document.querySelector("#image-item-run"),
    itemRetry: document.querySelector("#image-item-retry"),
    itemCancel: document.querySelector("#image-item-cancel"),
  };

  const commonFields = [
    [ui.width, ["width"], true], [ui.height, ["height"], true], [ui.seed, ["seed"], true],
    [ui.batchCount, ["batch_count"], true], [ui.steps, ["sample_params", "sample_steps"], true],
    [ui.cfg, ["sample_params", "guidance", "txt_cfg"], true],
    [ui.method, ["sample_params", "sample_method"], false],
    [ui.scheduler, ["sample_params", "scheduler"], false], [ui.format, ["output_format"], false],
  ];
  const assetGroups = {
    init: { field: "init_image_id", multiple: false }, refs: { field: "ref_image_ids", multiple: true },
    mask: { field: "mask_image_id", multiple: false }, control: { field: "control_image_id", multiple: false },
    ip_adapter: { field: "ip_adapter_image_id", multiple: false },
  };

  let active = false;
  let loadVersion = 0;
  let batchListVersion = 0;
  let detailVersion = 0;
  let batches = [];
  let configuration = { providers: [] };
  let selectedBatchID = "";
  let detail = null;
  let baseParams = JSON.parse(JSON.stringify(defaultBaseParams));
  let batchDraftDirty = false;
  let batchEditVersion = 0;
  let detailRefreshRunning = false;
  let detailRefreshPending = false;
  let eventSource = null;
  let editingItemID = "";
  let itemAssetDraft = emptyInputAssets();
  const assetCache = new Map();

  function setStatus(message, error = false) {
    ui.status.textContent = message;
    ui.status.dataset.state = error ? "error" : "";
  }

  function markBatchDraftDirty() {
    batchDraftDirty = true;
    batchEditVersion += 1;
  }

  async function request(path, options = {}) {
    const response = await fetch(path, options);
    if (!response.ok) throw new Error(await readAPIError(response));
    if (response.status === 204) return null;
    return response.json();
  }

  function jsonOptions(method, body = {}) {
    return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  }

  function parseObject(value, label) {
    let parsed;
    try { parsed = JSON.parse(value); } catch (error) { throw new Error(`${label} 不是合法 JSON：${error.message}`); }
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error(`${label} 必须是 JSON Object`);
    return parsed;
  }

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function emptyInputAssets() {
    return { init_image_id: "", ref_image_ids: [], mask_image_id: "", control_image_id: "", ip_adapter_image_id: "" };
  }

  function getPath(object, path) {
    return path.reduce((current, key) => current && typeof current === "object" ? current[key] : undefined, object);
  }

  function setPath(object, path, value) {
    let current = object;
    for (const key of path.slice(0, -1)) {
      if (!current[key] || Array.isArray(current[key]) || typeof current[key] !== "object") current[key] = {};
      current = current[key];
    }
    const finalKey = path[path.length - 1];
    if (value === "") delete current[finalKey];
    else current[finalKey] = value;
  }

  function applyBaseJSON() {
    try {
      baseParams = parseObject(ui.baseJSON.value, "Base Params");
      syncCommonControls();
      ui.baseJSON.value = JSON.stringify(baseParams, null, 2);
      ui.baseError.textContent = "";
      markBatchDraftDirty();
      return true;
    } catch (error) {
      ui.baseError.textContent = error.message;
      return false;
    }
  }

  function syncCommonControls() {
    for (const [input, path] of commonFields) {
      const value = getPath(baseParams, path);
      input.value = value === undefined || value === null ? "" : String(value);
    }
  }

  function commonInputChanged(input, path, numeric) {
    const raw = input.value.trim();
    if (numeric && raw && !Number.isFinite(Number(raw))) return;
    try {
      baseParams = parseObject(ui.baseJSON.value, "Base Params");
    } catch (error) {
      ui.baseError.textContent = `${error.message}；修正完整 JSON 后才能修改常用参数。`;
      syncCommonControls();
      return;
    }
    setPath(baseParams, path, raw === "" ? "" : numeric ? Number(raw) : raw);
    ui.baseJSON.value = JSON.stringify(baseParams, null, 2);
    ui.baseError.textContent = "";
    markBatchDraftDirty();
  }

  function enabledProviders() {
    return (configuration.providers ?? []).filter((provider) => provider.enabled);
  }

  function fillProviderSelect(select, selected) {
    select.replaceChildren();
    for (const provider of configuration.providers ?? []) {
      const option = new Option(`${provider.name}${provider.enabled ? "" : "（已禁用）"}`, provider.id);
      option.disabled = !provider.enabled && provider.id !== selected;
      select.append(option);
    }
    select.value = selected || enabledProviders()[0]?.id || "";
  }

  function renderFolderFilter() {
    const selected = ui.folderFilter.value;
    const folders = [...new Set(batches.map((batch) => batch.folder).filter(Boolean))].sort((a, b) => a.localeCompare(b));
    ui.folderFilter.replaceChildren(new Option("全部文件夹", ""));
    for (const folder of folders) ui.folderFilter.append(new Option(folder, folder));
    ui.folderFilter.value = folders.includes(selected) ? selected : "";
  }

  function filteredBatches() {
    const query = sidebarSearch.value.trim().toLocaleLowerCase();
    const folder = ui.folderFilter.value;
    return batches.filter((batch) => (!folder || batch.folder === folder) &&
      (!query || `${batch.title}\n${batch.folder || ""}`.toLocaleLowerCase().includes(query)));
  }

  function renderBatchList() {
    const visible = filteredBatches();
    ui.batchList.replaceChildren();
    ui.batchListStatus.textContent = visible.length ? "" : "当前筛选下没有批次。";
    for (const batch of visible) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "image-batch-list-item";
      if (batch.id === selectedBatchID) button.setAttribute("aria-current", "true");
      const title = document.createElement("strong");
      title.textContent = batch.title;
      const facts = document.createElement("small");
      facts.textContent = `${batch.folder || "未分类"} · ${batch.items?.length ?? 0} 项`;
      button.append(title, facts);
      button.addEventListener("click", () => selectBatch(batch.id));
      ui.batchList.append(button);
    }
  }

  async function loadBatches(preferredID = selectedBatchID) {
    const version = ++batchListVersion;
    const selectionAtStart = selectedBatchID;
    const body = await request("/api/v1/images/batches");
    if (version !== batchListVersion) return false;
    batches = body.batches ?? [];
    const preferred = selectedBatchID !== selectionAtStart ? selectedBatchID : preferredID;
    if (preferred && batches.some((batch) => batch.id === preferred)) selectedBatchID = preferred;
    else selectedBatchID = batches[0]?.id ?? "";
    renderFolderFilter();
    renderBatchList();
    return true;
  }

  async function selectBatch(batchID) {
    selectedBatchID = batchID;
    batchDraftDirty = false;
    detailRefreshPending = false;
    renderBatchList();
    closeEvents();
    if (!batchID) {
      detail = null;
      renderWorkspace();
      return;
    }
    setStatus("正在读取批次…");
    try {
      const loaded = await refreshDetail({ preserveBatchDraft: false });
      if (!loaded) return;
      if (active) connectEvents();
      setStatus(detail.provider_available ? "所有修改都需要显式保存。" : "当前 Provider 不可用，请在配置中启用或切换。", !detail.provider_available);
    } catch (error) {
      setStatus(`读取失败：${error.message}`, true);
    }
  }

  async function refreshDetail({ preserveBatchDraft = true } = {}) {
    if (!selectedBatchID) return false;
    const batchID = selectedBatchID;
    const version = ++detailVersion;
    const loaded = await request(`/api/v1/images/batches/${encodeURIComponent(batchID)}`);
    if (batchID !== selectedBatchID || version !== detailVersion || loaded.batch?.id !== batchID) return false;
    detail = loaded;
    for (const asset of detail.assets ?? []) assetCache.set(asset.id, asset);
    const summary = batches.find((batch) => batch.id === selectedBatchID);
    if (summary) Object.assign(summary, detail.batch);
    renderBatchList();
    renderWorkspace({ preserveBatchDraft });
    return true;
  }

  function renderWorkspace({ preserveBatchDraft = false } = {}) {
    const batch = detail?.batch;
    const enabled = Boolean(batch);
    for (const control of [ui.title, ui.folder, ui.provider, ui.concurrency, ui.saveBatch, ui.deleteBatch, ui.capabilities, ui.itemAdd, ui.bulkOpen, ui.runBatch]) {
      control.disabled = !enabled;
    }
    if (!batch) {
      ui.batchForm.hidden = true;
      ui.itemList.replaceChildren();
      ui.resultGrid.replaceChildren();
      setStatus("选择或新建批次。");
      return;
    }
    ui.batchForm.hidden = false;
    if (!preserveBatchDraft || !batchDraftDirty) {
      ui.title.value = batch.title;
      ui.folder.value = batch.folder || "";
      fillProviderSelect(ui.provider, batch.provider_id);
      ui.concurrency.value = String(batch.concurrency);
      baseParams = clone(batch.base_params ?? defaultBaseParams);
      ui.baseJSON.value = JSON.stringify(baseParams, null, 2);
      syncCommonControls();
      batchDraftDirty = false;
    }
    renderItems();
    renderResults();
  }

  function latestAttempt(item) {
    return item?.attempts?.[item.attempts.length - 1] ?? null;
  }

  function stateText(state) {
    return ({ queued: "排队中", submitting: "提交中", polling: "生成中", succeeded: "已完成", failed: "失败", cancelled: "已取消", interrupted: "已中断" })[state] || "未运行";
  }

  function renderItems() {
    ui.itemList.replaceChildren();
    const items = detail?.batch.items ?? [];
    if (!items.length) {
      const empty = document.createElement("p");
      empty.className = "inline-message";
      empty.textContent = "还没有请求项，可单独或批量添加。";
      ui.itemList.append(empty);
      return;
    }
    for (const item of items) {
      const latest = latestAttempt(item);
      const card = document.createElement("article");
      card.className = "image-item-card";
      const order = document.createElement("span");
      order.className = "image-item-order";
      order.textContent = String(item.order + 1);
      const summary = document.createElement("button");
      summary.type = "button";
      summary.className = "image-item-summary";
      const prompt = document.createElement("strong");
      prompt.textContent = item.prompt || "（空 Prompt）";
      const meta = document.createElement("small");
      meta.textContent = `${stateText(latest?.state)} · ${(item.input_assets?.ref_image_ids ?? []).length + (item.input_assets?.init_image_id ? 1 : 0)} 个输入引用`;
      summary.append(prompt, meta);
      summary.addEventListener("click", () => openItemEditor(item.id));
      const actions = document.createElement("div");
      actions.className = "button-row image-item-inline-actions";
      const run = textButton(latest && terminalAttemptStates.has(latest.state) ? "重试" : "运行", "secondary-button", () => executeItem(item.id));
      run.disabled = Boolean(latest && activeAttemptStates.has(latest.state));
      const edit = textButton("编辑", "text-button", () => openItemEditor(item.id));
      actions.append(run, edit);
      if (latest && activeAttemptStates.has(latest.state)) actions.append(textButton("取消", "text-button", () => cancelAttempt(latest.id)));
      card.append(order, summary, actions);
      ui.itemList.append(card);
    }
  }

  function textButton(label, className, handler) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = className;
    button.textContent = label;
    button.addEventListener("click", handler);
    return button;
  }

  function renderResults() {
    ui.resultGrid.replaceChildren();
    const results = [];
    for (const item of detail?.batch.items ?? []) {
      for (const attempt of item.attempts ?? []) {
        for (const assetID of attempt.result_asset_ids ?? []) results.push({ item, attempt, assetID });
      }
    }
    if (!results.length) {
      const empty = document.createElement("p");
      empty.className = "inline-message";
      empty.textContent = "任务结果会在这里进入共享 Asset 流程。";
      ui.resultGrid.append(empty);
      return;
    }
    for (const result of results) {
      const asset = assetCache.get(result.assetID);
      const card = document.createElement("article");
      card.className = "image-result-card";
      card.dataset.state = asset?.state || "";
      const media = document.createElement("div");
      media.className = "image-result-media";
      const image = document.createElement("img");
      image.loading = "lazy";
      image.alt = asset?.display_name || result.item.prompt || "生成结果";
      image.src = `/api/v1/assets/${encodeURIComponent(result.assetID)}/content`;
      media.append(image);
      const body = document.createElement("div");
      body.className = "image-result-body";
      const name = document.createElement("strong");
      name.textContent = asset?.display_name || result.assetID;
      const prompt = document.createElement("small");
      prompt.textContent = result.item.prompt || "（空 Prompt）";
      const state = document.createElement("span");
      state.className = "state-pill";
      state.textContent = asset?.state === "active" ? "精选" : "归档";
      const action = textButton(asset?.state === "active" ? "移入归档" : "设为精选", "text-button", () => setAssetState(result.assetID, asset?.state === "active" ? "archive" : "active"));
      body.append(name, prompt, state, action);
      card.append(media, body);
      ui.resultGrid.append(card);
    }
  }

  async function setAssetState(assetID, state) {
    try {
      const asset = await request(`/api/v1/assets/${encodeURIComponent(assetID)}/state`, jsonOptions("POST", { state }));
      assetCache.set(asset.id, asset);
      renderResults();
      setStatus(state === "active" ? "结果已加入精选库。" : "结果已移入归档库。");
    } catch (error) { setStatus(`更新 Asset 失败：${error.message}`, true); }
  }

  async function saveBatch(event) {
    event.preventDefault();
    if (!detail || !applyBaseJSON()) return;
    const batchID = selectedBatchID;
    const draftVersion = batchEditVersion;
    try {
      const updated = await request(`/api/v1/images/batches/${encodeURIComponent(batchID)}`, jsonOptions("PUT", {
        title: ui.title.value.trim(), folder: ui.folder.value.trim(), provider_id: ui.provider.value,
        concurrency: Number(ui.concurrency.value), base_params: baseParams,
      }));
      if (selectedBatchID !== batchID) {
        await loadBatches(selectedBatchID);
        return;
      }
      detailVersion += 1;
      detail.batch = updated;
      await loadBatches(updated.id);
      if (selectedBatchID !== batchID) return;
      const draftChangedWhileSaving = batchEditVersion !== draftVersion;
      batchDraftDirty = draftChangedWhileSaving;
      renderWorkspace({ preserveBatchDraft: draftChangedWhileSaving });
      closeEvents();
      connectEvents();
      setStatus("批次已保存。");
    } catch (error) { setStatus(`保存失败：${error.message}`, true); }
  }

  async function deleteBatch() {
    if (!detail || !window.confirm(`删除批次“${detail.batch.title}”？`)) return;
    const batchID = selectedBatchID;
    try {
      await request(`/api/v1/images/batches/${encodeURIComponent(batchID)}`, { method: "DELETE" });
      if (selectedBatchID !== batchID) {
        await loadBatches(selectedBatchID);
        return;
      }
      detail = null;
      selectedBatchID = "";
      closeEvents();
      await loadBatches();
      if (selectedBatchID) await selectBatch(selectedBatchID); else renderWorkspace();
    } catch (error) { setStatus(`删除失败：${error.message}`, true); }
  }

  async function loadCapabilities() {
    const providerID = ui.provider.value;
    if (!providerID) return;
    setStatus("正在读取 capabilities…");
    try {
      const capabilities = await request(`/api/v1/images/providers/${encodeURIComponent(providerID)}/capabilities`);
      fillDatalist(ui.methodOptions, capabilities.samplers ?? []);
      fillDatalist(ui.schedulerOptions, capabilities.schedulers ?? []);
      fillDatalist(ui.formatOptions, capabilities.output_formats ?? ["png", "jpg", "webp"]);
      const model = capabilities.model?.name || capabilities.model?.path || "当前模型";
      setStatus(`${model}：${(capabilities.samplers ?? []).length} 个采样器，${(capabilities.schedulers ?? []).length} 个 Scheduler。`);
    } catch (error) { setStatus(`Capabilities 读取失败：${error.message}`, true); }
  }

  function fillDatalist(list, values) {
    list.replaceChildren();
    for (const value of values) {
      const option = document.createElement("option");
      option.value = String(value);
      list.append(option);
    }
  }

  function openCreateDialog() {
    fillProviderSelect(ui.createProvider, enabledProviders()[0]?.id || "");
    ui.createTitle.value = "";
    ui.createFolder.value = ui.folderFilter.value || "";
    ui.createConcurrency.value = "1";
    ui.createError.textContent = "";
    ui.createDialog.showModal();
    ui.createTitle.focus();
  }

  async function createBatch(event) {
    event.preventDefault();
    try {
      const created = await request("/api/v1/images/batches", jsonOptions("POST", {
        title: ui.createTitle.value.trim(), folder: ui.createFolder.value.trim(), provider_id: ui.createProvider.value,
        concurrency: Number(ui.createConcurrency.value), base_params: clone(defaultBaseParams),
      }));
      ui.createDialog.close();
      selectedBatchID = created.id;
      await loadBatches(created.id);
      await selectBatch(created.id);
    } catch (error) { ui.createError.textContent = error.message; }
  }

  function openBulkDialog() {
    if (!detail) return;
    ui.bulkPrompts.value = "";
    ui.bulkError.textContent = "";
    ui.bulkDialog.showModal();
    ui.bulkPrompts.focus();
  }

  async function createBulkItems(event) {
    event.preventDefault();
    const prompts = ui.bulkPrompts.value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    if (!prompts.length) { ui.bulkError.textContent = "至少输入一个非空 Prompt。"; return; }
    try {
      await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", {
        items: prompts.map((prompt) => ({ prompt, negative_prompt: "", params_override: {}, input_assets: emptyInputAssets() })),
      }));
      ui.bulkDialog.close();
      await refreshDetail();
      setStatus(`已添加 ${prompts.length} 个请求项。`);
    } catch (error) { ui.bulkError.textContent = error.message; }
  }

  function findItem(itemID = editingItemID) {
    return detail?.batch.items.find((item) => item.id === itemID) ?? null;
  }

  function openItemEditor(itemID = "") {
    const item = findItem(itemID);
    editingItemID = item?.id || "";
    ui.itemID.value = editingItemID;
    ui.itemPrompt.value = item?.prompt || "";
    ui.itemNegative.value = item?.negative_prompt || "";
    ui.itemOverride.value = JSON.stringify(item?.params_override ?? {}, null, 2);
    itemAssetDraft = clone(item?.input_assets ?? emptyInputAssets());
    ui.itemError.textContent = "";
    renderItemAssets();
    renderAttemptHistory(item);
    updateItemActions(item);
    ui.itemDialog.showModal();
  }

  function updateItemActions(item) {
    const latest = item ? latestAttempt(item) : null;
    const running = Boolean(latest && activeAttemptStates.has(latest.state));
    ui.itemUp.disabled = !item || item.order === 0 || running;
    ui.itemDown.disabled = !item || item.order === detail.batch.items.length - 1 || running;
    ui.itemCopy.disabled = !item;
    ui.itemDelete.disabled = !item || running;
    ui.itemRun.disabled = !item || running;
    ui.itemRetry.disabled = !item || running || !latest;
    ui.itemCancel.disabled = !running;
  }

  function renderAttemptHistory(item) {
    ui.itemHistory.replaceChildren();
    const attempts = item?.attempts ?? [];
    if (!attempts.length) ui.itemHistory.textContent = "尚未运行。";
    for (const attempt of [...attempts].reverse()) {
      const row = document.createElement("div");
      row.className = "image-attempt-row";
      const state = document.createElement("strong");
      state.textContent = stateText(attempt.state);
      const time = document.createElement("small");
      time.textContent = new Date(attempt.created_at).toLocaleString();
      const error = document.createElement("span");
      error.textContent = attempt.error?.message || "";
      row.append(state, time, error);
      ui.itemHistory.append(row);
    }
    ui.itemTechnical.textContent = item ? JSON.stringify(item.attempts ?? [], null, 2) : "新项目尚无技术信息。";
  }

  function assetIDsFor(group) {
    const definition = assetGroups[group];
    const value = itemAssetDraft[definition.field];
    return definition.multiple ? (value ?? []) : value ? [value] : [];
  }

  function renderItemAssets() {
    for (const [group, definition] of Object.entries(assetGroups)) {
      const container = document.querySelector(`[data-asset-group="${group}"] .image-asset-reference`);
      container.replaceChildren();
      const ids = assetIDsFor(group);
      if (!ids.length) container.textContent = "未选择";
      for (const id of ids) {
        const pill = document.createElement("span");
        pill.className = "image-asset-reference-pill";
        const label = document.createElement("span");
        const asset = assetCache.get(id);
        label.textContent = asset ? `${asset.display_name}${asset.state === "active" ? "" : "（归档）"}` : id;
        const remove = textButton("移除", "text-button", () => {
          if (definition.multiple) itemAssetDraft[definition.field] = (itemAssetDraft[definition.field] ?? []).filter((candidate) => candidate !== id);
          else itemAssetDraft[definition.field] = "";
          renderItemAssets();
        });
        pill.append(label, remove);
        container.append(pill);
      }
    }
  }

  async function chooseAssets(group) {
    const definition = assetGroups[group];
    const existingIDs = assetIDsFor(group);
    const selected = await openAssetPicker({ multiple: definition.multiple, selected: existingIDs, mediaPrefix: "image/" });
    if (!selected) return;
    for (const asset of selected) assetCache.set(asset.id, asset);
    if (definition.multiple) {
      const archivedIDs = existingIDs.filter((id) => assetCache.get(id)?.state !== "active");
      itemAssetDraft[definition.field] = [...new Set([...archivedIDs, ...selected.map((asset) => asset.id)])];
    } else {
      itemAssetDraft[definition.field] = selected[0]?.id || existingIDs.find((id) => assetCache.get(id)?.state !== "active") || "";
    }
    renderItemAssets();
  }

  function itemPayload() {
    return {
      prompt: ui.itemPrompt.value, negative_prompt: ui.itemNegative.value,
      params_override: parseObject(ui.itemOverride.value, "Params Override"), input_assets: clone(itemAssetDraft),
    };
  }

  async function saveItem(event) {
    event.preventDefault();
    try {
      const payload = itemPayload();
      if (editingItemID) {
        await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(editingItemID)}`, jsonOptions("PUT", payload));
      } else {
        const body = await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", { items: [payload] }));
        editingItemID = body.items?.[0]?.id || "";
      }
      ui.itemDialog.close();
      await refreshDetail();
      setStatus("请求项已保存。");
    } catch (error) { ui.itemError.textContent = error.message; }
  }

  async function moveEditingItem(direction) {
    if (!editingItemID) return;
    try {
      const batch = await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(editingItemID)}/move`, jsonOptions("POST", { direction }));
      detail.batch = batch;
      renderItems();
      updateItemActions(findItem());
    } catch (error) { ui.itemError.textContent = error.message; }
  }

  async function copyEditingItem() {
    const source = findItem();
    if (!source) return;
    try {
      const createdBody = await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", { items: [{
        prompt: source.prompt, negative_prompt: source.negative_prompt, params_override: source.params_override, input_assets: source.input_assets,
      }] }));
      const copyID = createdBody.items[0].id;
      const moves = Math.max(0, detail.batch.items.length - source.order - 1);
      for (let index = 0; index < moves; index += 1) {
        detail.batch = await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(copyID)}/move`, jsonOptions("POST", { direction: -1 }));
      }
      ui.itemDialog.close();
      await refreshDetail();
      openItemEditor(copyID);
      setStatus("已复制到原请求项下方。");
    } catch (error) { ui.itemError.textContent = error.message; }
  }

  async function deleteEditingItem() {
    const item = findItem();
    if (!item || !window.confirm("删除这个请求项及其 Attempt 历史？生成结果 Asset 不会被删除。")) return;
    try {
      await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(item.id)}`, { method: "DELETE" });
      ui.itemDialog.close();
      await refreshDetail();
    } catch (error) { ui.itemError.textContent = error.message; }
  }

  async function executeBatch() {
    try {
      await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/execute`, jsonOptions("POST"));
      await refreshDetail();
      setStatus("待执行项已加入队列。");
    } catch (error) { setStatus(`启动失败：${error.message}`, true); }
  }

  async function executeItem(itemID = editingItemID) {
    if (!itemID) return;
    try {
      await request(`/api/v1/images/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(itemID)}/execute`, jsonOptions("POST"));
      await refreshDetail();
      if (ui.itemDialog.open && itemID === editingItemID) {
        renderAttemptHistory(findItem());
        updateItemActions(findItem());
      }
      setStatus("请求项已加入队列。");
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`启动失败：${error.message}`, true);
    }
  }

  async function cancelAttempt(attemptID) {
    if (!attemptID) return;
    try {
      await request(`/api/v1/images/attempts/${encodeURIComponent(attemptID)}/cancel`, jsonOptions("POST"));
      await refreshDetail();
      if (ui.itemDialog.open) { renderAttemptHistory(findItem()); updateItemActions(findItem()); }
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`取消失败：${error.message}`, true);
    }
  }

  function connectEvents() {
    closeEvents();
    if (!active || !selectedBatchID) return;
    const batchID = selectedBatchID;
    const source = new EventSource(`/api/v1/images/batches/${encodeURIComponent(batchID)}/events`);
    eventSource = source;
    const receive = (event) => {
      if (eventSource !== source || selectedBatchID !== batchID) return;
      let payload;
      try { payload = JSON.parse(event.data); } catch (_) { return; }
      const known = mergeAttempt(payload.attempt);
      if (!known || terminalAttemptStates.has(payload.attempt?.state)) scheduleDetailRefresh();
    };
    source.addEventListener("snapshot", receive);
    source.addEventListener("state", receive);
    source.onerror = () => {
      if (active && eventSource === source && selectedBatchID === batchID) setStatus("任务事件流暂时断开，浏览器会自动重连。", true);
    };
  }

  function mergeAttempt(attempt) {
    if (!attempt || !detail) return false;
    for (const item of detail.batch.items) {
      const index = (item.attempts ?? []).findIndex((candidate) => candidate.id === attempt.id);
      if (index >= 0) {
        item.attempts[index] = attempt;
        renderItems();
        renderResults();
        if (ui.itemDialog.open && editingItemID === item.id) { renderAttemptHistory(item); updateItemActions(item); }
        return true;
      }
    }
    return false;
  }

  async function scheduleDetailRefresh() {
    detailRefreshPending = true;
    if (detailRefreshRunning) return;
    detailRefreshRunning = true;
    try {
      while (detailRefreshPending && active) {
        detailRefreshPending = false;
        try {
          await refreshDetail();
        } catch (error) {
          setStatus(`刷新结果失败：${error.message}`, true);
        }
      }
    } finally {
      detailRefreshRunning = false;
      if (detailRefreshPending && active) scheduleDetailRefresh();
    }
  }

  function closeEvents() {
    if (eventSource) eventSource.close();
    eventSource = null;
  }

  async function enter() {
    active = true;
    const version = ++loadVersion;
    sidebarContent.replaceChildren(ui.sidebarControls);
    sidebarSearch.disabled = false;
    sidebarSearch.placeholder = "搜索批次标题或文件夹";
    ui.workspace.hidden = false;
    ui.batchListStatus.textContent = "正在读取批次…";
    try {
      const [imageConfig] = await Promise.all([request("/api/v1/images/config"), loadBatches()]);
      if (!active || version !== loadVersion) return;
      configuration = imageConfig;
      if (selectedBatchID) await selectBatch(selectedBatchID); else renderWorkspace();
    } catch (error) {
      if (active && version === loadVersion) setStatus(`读取生图工作区失败：${error.message}`, true);
    }
  }

  function leave() {
    active = false;
    loadVersion += 1;
    detailVersion += 1;
    batchListVersion += 1;
    detailRefreshPending = false;
    closeEvents();
    ui.workspace.hidden = true;
  }

  ui.batchForm.addEventListener("submit", saveBatch);
  ui.deleteBatch.addEventListener("click", deleteBatch);
  ui.capabilities.addEventListener("click", loadCapabilities);
  ui.newBatch.addEventListener("click", openCreateDialog);
  ui.createForm.addEventListener("submit", createBatch);
  ui.bulkOpen.addEventListener("click", openBulkDialog);
  ui.bulkForm.addEventListener("submit", createBulkItems);
  ui.itemAdd.addEventListener("click", () => openItemEditor());
  ui.itemForm.addEventListener("submit", saveItem);
  ui.itemUp.addEventListener("click", () => moveEditingItem(-1));
  ui.itemDown.addEventListener("click", () => moveEditingItem(1));
  ui.itemCopy.addEventListener("click", copyEditingItem);
  ui.itemDelete.addEventListener("click", deleteEditingItem);
  ui.itemRun.addEventListener("click", () => executeItem());
  ui.itemRetry.addEventListener("click", () => executeItem());
  ui.itemCancel.addEventListener("click", () => cancelAttempt(latestAttempt(findItem())?.id));
  ui.runBatch.addEventListener("click", executeBatch);
  ui.baseApply.addEventListener("click", applyBaseJSON);
  ui.folderFilter.addEventListener("change", renderBatchList);
  sidebarSearch.addEventListener("input", () => { if (active) renderBatchList(); });
  for (const control of [ui.title, ui.folder, ui.provider, ui.concurrency]) {
    control.addEventListener("input", markBatchDraftDirty);
    control.addEventListener("change", markBatchDraftDirty);
  }
  ui.baseJSON.addEventListener("input", markBatchDraftDirty);
  for (const [input, path, numeric] of commonFields) input.addEventListener("input", () => commonInputChanged(input, path, numeric));
  for (const button of document.querySelectorAll("[data-image-workspace-close]")) {
    button.addEventListener("click", () => document.querySelector(`#${button.dataset.imageWorkspaceClose}`).close());
  }
  for (const button of document.querySelectorAll("[data-image-asset-pick]")) {
    button.addEventListener("click", () => chooseAssets(button.dataset.imageAssetPick));
  }

  return { enter, leave };
}
