const terminalAttemptStates = new Set(["succeeded", "failed", "cancelled", "interrupted"]);
const activeAttemptStates = new Set(["queued", "submitting", "polling", "running"]);
const defaultTiming = { mode: "frames", video_frames: 49, fps: 12 };

export function createVideoWorkspace({ openAssetPicker, readAPIError }) {
  const ui = {
    workspace: document.querySelector("#video-workspace"),
    sidebarControls: document.querySelector("#video-sidebar-controls"),
    sidebarSearch: document.querySelector("#sidebar-search"),
    folderFilter: document.querySelector("#video-batch-folder-filter"),
    newBatch: document.querySelector("#video-batch-new"),
    batchList: document.querySelector("#video-batch-list"),
    batchListStatus: document.querySelector("#video-batch-list-status"),
    batchForm: document.querySelector("#video-batch-form"),
    title: document.querySelector("#video-batch-title"),
    folder: document.querySelector("#video-batch-folder"),
    executionKind: document.querySelector("#video-execution-kind"),
    preset: document.querySelector("#video-batch-preset"),
    concurrency: document.querySelector("#video-batch-concurrency"),
    saveBatch: document.querySelector("#video-batch-save"),
    deleteBatch: document.querySelector("#video-batch-delete"),
    runBatch: document.querySelector("#video-batch-run"),
    capabilities: document.querySelector("#video-batch-capabilities"),
    status: document.querySelector("#video-workspace-status"),
    timingFrames: document.querySelector("#video-timing-frames"),
    timingDuration: document.querySelector("#video-timing-duration"),
    frameCount: document.querySelector("#video-frame-count"),
    duration: document.querySelector("#video-duration-seconds"),
    fps: document.querySelector("#video-fps"),
    requestedFrames: document.querySelector("#video-requested-frames"),
    commonParams: document.querySelector("#video-common-params"),
    commonParamsError: document.querySelector("#video-common-params-error"),
    modelLimits: document.querySelector("#video-model-limits"),
    itemList: document.querySelector("#video-item-list"),
    itemAdd: document.querySelector("#video-item-add"),
    bulkOpen: document.querySelector("#video-bulk-open"),
    resultList: document.querySelector("#video-result-list"),
    resultTemplate: document.querySelector("#video-result-template"),
    tailPreset: document.querySelector("#video-tail-preset"),
    createDialog: document.querySelector("#video-batch-editor"),
    createForm: document.querySelector("#video-batch-create-form"),
    createTitle: document.querySelector("#video-new-batch-title"),
    createFolder: document.querySelector("#video-new-batch-folder"),
    createKind: document.querySelector("#video-new-execution-kind"),
    createPreset: document.querySelector("#video-new-batch-preset"),
    createConcurrency: document.querySelector("#video-new-batch-concurrency"),
    createError: document.querySelector("#video-new-batch-error"),
    bulkDialog: document.querySelector("#video-bulk-editor"),
    bulkForm: document.querySelector("#video-bulk-form"),
    bulkPrompts: document.querySelector("#video-bulk-prompts"),
    bulkError: document.querySelector("#video-bulk-error"),
    itemDialog: document.querySelector("#video-item-editor"),
    itemForm: document.querySelector("#video-item-form"),
    itemID: document.querySelector("#video-item-id"),
    itemPrompt: document.querySelector("#video-item-prompt"),
    itemNegative: document.querySelector("#video-item-negative"),
    itemEnabled: document.querySelector("#video-item-enabled"),
    itemOverride: document.querySelector("#video-item-override"),
    itemError: document.querySelector("#video-item-error"),
    cliAssets: document.querySelector("#video-item-cli-assets"),
    cliAssetsPick: document.querySelector("#video-cli-assets-pick"),
    cliAssetList: document.querySelector("#video-cli-asset-list"),
    attemptHistory: document.querySelector("#video-attempt-history"),
    attemptTechnical: document.querySelector("#video-attempt-technical"),
    cliDetails: document.querySelector("#video-cli-details"),
    cliLog: document.querySelector("#video-cli-log"),
    cliLogClear: document.querySelector("#video-cli-log-clear"),
    cliLogSave: document.querySelector("#video-cli-log-save"),
    cliCleanup: document.querySelector("#video-cli-workspace-cleanup"),
    cliPath: document.querySelector("#video-cli-workspace-path"),
    itemUp: document.querySelector("#video-item-up"),
    itemDown: document.querySelector("#video-item-down"),
    itemCopy: document.querySelector("#video-item-copy"),
    itemDelete: document.querySelector("#video-item-delete"),
    itemRun: document.querySelector("#video-item-run"),
    itemRetry: document.querySelector("#video-item-retry"),
    itemCancel: document.querySelector("#video-item-cancel"),
  };

  let active = false;
  let loadVersion = 0;
  let batchListVersion = 0;
  let detailVersion = 0;
  let selectionVersion = 0;
  let configuration = { http_providers: [], cli_presets: [], tail_frame_presets: [] };
  let batches = [];
  let selectedBatchID = "";
  let detail = null;
  let batchDraftDirty = false;
  let batchEditVersion = 0;
  let eventSource = null;
  let detailRefreshRunning = false;
  let detailRefreshPending = false;
  let editingItemID = "";
  let imageAssetDraft = emptyImageAssets();
  let cliAssetDraft = [];
  const assetCache = new Map();
  const tailExtractionsBySource = new Map();
  const tailEventSources = new Map();
  const timers = new Set();

  let logEventSource = null;
  let logAttemptID = "";
  let logBytes = new Uint8Array();
  let logStartOffset = 0;
  let logEndOffset = 0;
  let clearOffset = 0;
  let logAwaitingSnapshot = false;
  let logReconnectTimer = null;
  let logReconnectDelay = 250;
  const logClearOffsets = new Map();
  const logDecoder = new TextDecoder();

  function setStatus(message, error = false) {
    ui.status.textContent = message;
    ui.status.dataset.state = error ? "error" : "";
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

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function parseObject(value, label) {
    let parsed;
    try { parsed = JSON.parse(value); } catch (error) { throw new Error(`${label} 不是合法 JSON：${error.message}`); }
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error(`${label} 必须是 JSON Object`);
    return parsed;
  }

  function textButton(label, className, handler) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = className;
    button.textContent = label;
    button.addEventListener("click", handler);
    return button;
  }

  function emptyImageAssets() {
    return { init_image_id: "", end_image_id: "", control_frame_ids: [] };
  }

  function markBatchDraftDirty() {
    batchDraftDirty = true;
    batchEditVersion += 1;
  }

  function presetsFor(kind) {
    return kind === "local_cli" ? (configuration.cli_presets ?? []) : (configuration.http_providers ?? []);
  }

  function fillPresetSelect(select, kind, selected = "") {
    select.replaceChildren();
    const presets = presetsFor(kind);
    for (const preset of presets) {
      const option = new Option(`${preset.name}${preset.enabled ? "" : "（已禁用）"}`, preset.id);
      option.disabled = !preset.enabled && preset.id !== selected;
      select.append(option);
    }
    const enabled = presets.find((preset) => preset.enabled)?.id || "";
    select.value = presets.some((preset) => preset.id === selected) ? selected : enabled;
  }

  function renderTailPresets() {
    const selected = ui.tailPreset.value;
    ui.tailPreset.replaceChildren(new Option("选择预设", ""));
    for (const preset of configuration.tail_frame_presets ?? []) {
      if (!preset.enabled) continue;
      ui.tailPreset.append(new Option(preset.name, preset.id));
    }
    ui.tailPreset.value = [...ui.tailPreset.options].some((option) => option.value === selected) ? selected : "";
  }

  function renderFolderFilter() {
    const selected = ui.folderFilter.value;
    const folders = [...new Set(batches.map((batch) => batch.folder).filter(Boolean))].sort((a, b) => a.localeCompare(b));
    ui.folderFilter.replaceChildren(new Option("全部文件夹", ""));
    for (const folder of folders) ui.folderFilter.append(new Option(folder, folder));
    ui.folderFilter.value = folders.includes(selected) ? selected : "";
  }

  function filteredBatches() {
    const query = ui.sidebarSearch.value.trim().toLocaleLowerCase();
    const folder = ui.folderFilter.value;
    return batches.filter((batch) => (!folder || batch.folder === folder) &&
      (!query || `${batch.title}\n${batch.folder || ""}`.toLocaleLowerCase().includes(query)));
  }

  function renderBatchList() {
    const visible = filteredBatches();
    ui.batchList.replaceChildren();
    ui.batchListStatus.textContent = visible.length ? "" : "当前筛选下没有视频批次。";
    for (const batch of visible) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "video-batch-list-item";
      if (batch.id === selectedBatchID) button.setAttribute("aria-current", "true");
      const title = document.createElement("strong");
      title.textContent = batch.title;
      const facts = document.createElement("small");
      facts.textContent = `${batch.folder || "未分类"} · ${batch.execution_kind === "local_cli" ? "CLI" : "HTTP"} · ${batch.items?.length ?? 0} 项`;
      button.append(title, facts);
      button.addEventListener("click", () => selectBatch(batch.id));
      ui.batchList.append(button);
    }
  }

  async function loadBatches(preferredID = selectedBatchID) {
    const version = ++batchListVersion;
    const body = await request("/api/v1/videos/batches");
    if (version !== batchListVersion) return "";
    batches = body.batches ?? [];
    renderFolderFilter();
    renderBatchList();
    return preferredID && batches.some((batch) => batch.id === preferredID) ? preferredID : (batches[0]?.id ?? "");
  }

  async function loadBatchDetail(batchID) {
    const loaded = await request(`/api/v1/videos/batches/${encodeURIComponent(batchID)}`);
    if (loaded.batch?.id !== batchID) throw new Error("批次详情响应与请求的批次不一致");
    return loaded;
  }

  function commitBatchSelection(batchID, loaded) {
    selectedBatchID = batchID;
    detail = loaded;
    detailRefreshPending = false;
    batchDraftDirty = false;
    detailVersion += 1;
    for (const asset of detail.assets ?? []) assetCache.set(asset.id, asset);
    const summary = batches.find((batch) => batch.id === batchID);
    if (summary) Object.assign(summary, detail.batch);
    renderBatchList();
    renderWorkspace();
  }

  async function selectBatch(batchID, { discardDraft = false } = {}) {
    if (batchID === selectedBatchID && detail?.batch?.id === batchID && !discardDraft) return;
    if (batchID !== selectedBatchID && batchDraftDirty && !discardDraft &&
      !window.confirm("当前批次有未保存修改。切换将放弃这些修改，是否继续？")) return;
    if (!batchID) {
      selectionVersion += 1;
      closeBatchEvents();
      closeAttemptLog();
      closeTailEvents();
      selectedBatchID = "";
      detail = null;
      batchDraftDirty = false;
      renderBatchList();
      renderWorkspace();
      return;
    }
    const editVersion = batchEditVersion;
    const version = ++selectionVersion;
    setStatus("正在读取视频批次…");
    try {
      const loaded = await loadBatchDetail(batchID);
      if (!active || version !== selectionVersion) return;
      if (batchEditVersion !== editVersion) {
        setStatus("当前批次草稿在读取目标批次期间发生修改；已保留草稿。请再次点击目标批次并确认切换。", true);
        return;
      }
      closeBatchEvents();
      closeAttemptLog();
      closeTailEvents();
      commitBatchSelection(batchID, loaded);
      if (active) {
        connectBatchEvents();
        connectRelevantTailEvents();
      }
      setStatus(detail.preset_available ? "所有修改都需要显式保存。" : "当前预设不可用，请在配置中启用或切换。", !detail.preset_available);
    } catch (error) {
      if (active && version === selectionVersion) setStatus(`读取失败；仍保留当前批次与未保存草稿：${error.message}`, true);
    }
  }

  async function refreshDetail({ preserveBatchDraft = true } = {}) {
    if (!selectedBatchID) return false;
    const batchID = selectedBatchID;
    const version = ++detailVersion;
    const loaded = await loadBatchDetail(batchID);
    if (batchID !== selectedBatchID || version !== detailVersion || loaded.batch?.id !== batchID) return false;
    detail = loaded;
    for (const asset of detail.assets ?? []) assetCache.set(asset.id, asset);
    const summary = batches.find((batch) => batch.id === batchID);
    if (summary) Object.assign(summary, detail.batch);
    renderBatchList();
    renderWorkspace({ preserveBatchDraft });
    if (ui.itemDialog.open) {
      const item = findItem();
      renderAttemptHistory(item);
      updateItemActions(item);
      connectAttemptLog(latestAttempt(item));
    }
    return true;
  }

  function renderWorkspace({ preserveBatchDraft = false } = {}) {
    const batch = detail?.batch;
    const enabled = Boolean(batch);
    for (const control of [ui.title, ui.folder, ui.executionKind, ui.preset, ui.concurrency, ui.saveBatch, ui.deleteBatch,
      ui.runBatch, ui.capabilities, ui.timingFrames, ui.timingDuration, ui.frameCount, ui.duration, ui.fps, ui.commonParams,
      ui.itemAdd, ui.bulkOpen]) control.disabled = !enabled;
    if (!batch) {
      ui.batchForm.hidden = true;
      ui.itemList.replaceChildren();
      ui.resultList.replaceChildren();
      setStatus("选择或新建视频批次。");
      return;
    }
    ui.batchForm.hidden = false;
    if (!preserveBatchDraft || !batchDraftDirty) {
      ui.title.value = batch.title;
      ui.folder.value = batch.folder || "";
      ui.executionKind.value = batch.execution_kind;
      fillPresetSelect(ui.preset, batch.execution_kind, batch.preset_id);
      ui.concurrency.value = String(batch.concurrency);
      ui.timingFrames.checked = batch.timing?.mode !== "duration";
      ui.timingDuration.checked = batch.timing?.mode === "duration";
      ui.frameCount.value = String(batch.timing?.video_frames || defaultTiming.video_frames);
      ui.duration.value = String(batch.timing?.duration_seconds || 4);
      ui.fps.value = String(batch.timing?.fps || defaultTiming.fps);
      ui.commonParams.value = JSON.stringify(batch.common_params ?? {}, null, 2);
      ui.commonParamsError.textContent = "";
      batchDraftDirty = false;
    }
    syncTimingControls();
    ui.capabilities.hidden = ui.executionKind.value !== "http";
    renderItems();
    renderResults();
  }

  function currentTiming() {
    const fps = Number(ui.fps.value);
    if (ui.timingDuration.checked) return { mode: "duration", duration_seconds: Number(ui.duration.value), fps };
    return { mode: "frames", video_frames: Number(ui.frameCount.value), fps };
  }

  function syncTimingControls() {
    const durationMode = ui.timingDuration.checked;
    for (const field of document.querySelectorAll("[data-video-timing-field]")) {
      field.hidden = field.dataset.videoTimingField !== (durationMode ? "duration" : "frames");
    }
    const fps = Math.max(0, Number(ui.fps.value) || 0);
    const requested = durationMode ? Math.ceil(Math.max(0, Number(ui.duration.value) || 0) * fps) : Math.max(0, Number(ui.frameCount.value) || 0);
    ui.requestedFrames.textContent = `请求 ${requested} 帧；实际帧数由结果决定。`;
  }

  function batchPayload() {
    const commonParams = parseObject(ui.commonParams.value, "公共参数");
    ui.commonParams.value = JSON.stringify(commonParams, null, 2);
    return {
      title: ui.title.value.trim(), folder: ui.folder.value.trim(), execution_kind: ui.executionKind.value,
      preset_id: ui.preset.value, concurrency: Number(ui.concurrency.value), common_params: commonParams, timing: currentTiming(),
    };
  }

  async function saveBatch(event) {
    event.preventDefault();
    if (!detail) return;
    const batchID = selectedBatchID;
    const editVersion = batchEditVersion;
    try {
      const updated = await request(`/api/v1/videos/batches/${encodeURIComponent(batchID)}`, jsonOptions("PUT", batchPayload()));
      if (selectedBatchID !== batchID) return;
      detail.batch = updated;
      await loadBatches(batchID);
      const changedWhileSaving = batchEditVersion !== editVersion;
      batchDraftDirty = changedWhileSaving;
      renderWorkspace({ preserveBatchDraft: changedWhileSaving });
      closeBatchEvents();
      connectBatchEvents();
      setStatus("视频批次已保存。");
    } catch (error) {
      ui.commonParamsError.textContent = error.message;
      setStatus(`保存失败：${error.message}`, true);
    }
  }

  async function deleteBatch() {
    if (!detail || !window.confirm(`删除视频批次“${detail.batch.title}”？`)) return;
    const batchID = selectedBatchID;
    try {
      await request(`/api/v1/videos/batches/${encodeURIComponent(batchID)}`, { method: "DELETE" });
      if (selectedBatchID !== batchID) return;
      detail = null;
      selectedBatchID = "";
      closeBatchEvents();
      closeAttemptLog();
      closeTailEvents();
      const nextBatchID = await loadBatches("");
      if (nextBatchID) await selectBatch(nextBatchID, { discardDraft: true });
      else renderWorkspace();
    } catch (error) { setStatus(`删除失败：${error.message}`, true); }
  }

  async function loadCapabilities() {
    if (ui.executionKind.value !== "http" || !ui.preset.value) return;
    setStatus("正在读取视频 capabilities…");
    try {
      const capabilities = await request(`/api/v1/videos/providers/${encodeURIComponent(ui.preset.value)}/capabilities`);
      ui.modelLimits.textContent = JSON.stringify({
        video_generation_supported: capabilities.video_generation_supported,
        model: capabilities.model,
        features: capabilities.features_by_mode?.vid_gen ?? capabilities.features,
        output_formats: capabilities.output_formats_by_mode?.vid_gen ?? capabilities.output_formats,
        limits: capabilities.limits_by_mode?.vid_gen ?? capabilities.limits,
      }, null, 2);
      setStatus(capabilities.video_generation_supported ? "当前模型报告支持视频生成。" : "当前模型未报告 vid_gen 支持。", !capabilities.video_generation_supported);
    } catch (error) { setStatus(`Capabilities 读取失败：${error.message}`, true); }
  }

  function openCreateDialog() {
    ui.createTitle.value = "";
    ui.createFolder.value = ui.folderFilter.value || "";
    ui.createKind.value = presetsFor("http").some((preset) => preset.enabled) ? "http" : "local_cli";
    fillPresetSelect(ui.createPreset, ui.createKind.value);
    ui.createConcurrency.value = "1";
    ui.createError.textContent = "";
    ui.createDialog.showModal();
    ui.createTitle.focus();
  }

  async function createBatch(event) {
    event.preventDefault();
    try {
      const created = await request("/api/v1/videos/batches", jsonOptions("POST", {
        title: ui.createTitle.value.trim(), folder: ui.createFolder.value.trim(), execution_kind: ui.createKind.value,
        preset_id: ui.createPreset.value, concurrency: Number(ui.createConcurrency.value), common_params: {}, timing: clone(defaultTiming),
      }));
      ui.createDialog.close();
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
      const body = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", {
        items: prompts.map((prompt) => ({ prompt, negative_prompt: "", enabled: true, params_override: {}, control_frame_ids: [], selected_assets: [] })),
      }));
      ui.bulkDialog.close();
      for (const item of body.items ?? []) detail.batch.items.push(item);
      renderItems();
      setStatus(`已添加 ${prompts.length} 个请求项。`);
      scheduleReconcile();
    } catch (error) { ui.bulkError.textContent = error.message; }
  }

  function findItem(itemID = editingItemID) {
    return detail?.batch.items.find((item) => item.id === itemID) ?? null;
  }

  function latestAttempt(item) {
    return item?.attempts?.[item.attempts.length - 1] ?? null;
  }

  function stateText(state) {
    return ({ queued: "排队中", submitting: "提交中", polling: "远端生成中", running: "本地运行中", succeeded: "已完成",
      failed: "失败", cancelled: "已取消", interrupted: "已中断" })[state] || "未运行";
  }

  function itemInputCount(item) {
    return (item.init_image_id ? 1 : 0) + (item.end_image_id ? 1 : 0) + (item.control_frame_ids?.length ?? 0) + (item.selected_assets?.length ?? 0);
  }

  function renderItems() {
    ui.itemList.replaceChildren();
    const items = detail?.batch.items ?? [];
    if (!items.length) {
      const empty = document.createElement("p");
      empty.className = "inline-message";
      empty.textContent = "还没有视频请求项，可单独或批量添加。";
      ui.itemList.append(empty);
      return;
    }
    for (const item of items) {
      const latest = latestAttempt(item);
      const running = Boolean(latest && activeAttemptStates.has(latest.state));
      const card = document.createElement("article");
      card.className = "video-item-card";
      card.dataset.enabled = String(item.enabled);
      const order = document.createElement("span");
      order.className = "video-item-order";
      order.textContent = String(item.order + 1);
      const summary = document.createElement("button");
      summary.type = "button";
      summary.className = "video-item-summary";
      const prompt = document.createElement("strong");
      prompt.textContent = item.prompt || "（空 Prompt）";
      const meta = document.createElement("small");
      meta.textContent = `${item.enabled ? "启用" : "停用"} · ${stateText(latest?.state)} · ${itemInputCount(item)} 个输入引用`;
      summary.append(prompt, meta);
      summary.addEventListener("click", () => openItemEditor(item.id));
      const actions = document.createElement("div");
      actions.className = "button-row video-item-inline-actions";
      const run = textButton(latest && terminalAttemptStates.has(latest.state) ? "重试" : "运行", "secondary-button",
        () => latest && terminalAttemptStates.has(latest.state) ? retryAttempt(latest.id) : executeItem(item.id));
      run.disabled = running || !item.enabled;
      const toggle = textButton(item.enabled ? "停用" : "启用", "text-button", () => updateItemEnabled(item.id, !item.enabled));
      toggle.disabled = running;
      const edit = textButton("编辑", "text-button", () => openItemEditor(item.id));
      const up = textButton("上移", "text-button", () => moveItem(item.id, -1));
      up.disabled = item.order === 0 || running;
      const down = textButton("下移", "text-button", () => moveItem(item.id, 1));
      down.disabled = item.order === items.length - 1 || running;
      const copy = textButton("复制", "text-button", () => copyItem(item.id));
      const remove = textButton("删除", "text-button danger-text", () => deleteItem(item.id));
      remove.disabled = running;
      actions.append(run, toggle, edit, up, down, copy, remove);
      if (running) actions.append(textButton("取消", "text-button", () => cancelAttempt(latest.id)));
      card.append(order, summary, actions);
      ui.itemList.append(card);
    }
  }

  function openItemEditor(itemID = "", initialAssetID = "") {
    const item = findItem(itemID);
    editingItemID = item?.id || "";
    ui.itemID.value = editingItemID;
    ui.itemPrompt.value = item?.prompt || "";
    ui.itemNegative.value = item?.negative_prompt || "";
    ui.itemEnabled.checked = item?.enabled ?? true;
    ui.itemOverride.value = JSON.stringify(item?.params_override ?? {}, null, 2);
    imageAssetDraft = {
      init_image_id: initialAssetID || item?.init_image_id || "",
      end_image_id: item?.end_image_id || "",
      control_frame_ids: clone(item?.control_frame_ids ?? []),
    };
    cliAssetDraft = clone(item?.selected_assets ?? []).sort((left, right) => left.order - right.order);
    reindexCLIAssets();
    ui.itemError.textContent = "";
    ui.cliPath.textContent = "";
    renderItemAssets();
    renderAttemptHistory(item);
    updateItemActions(item);
    ui.cliAssets.hidden = detail?.batch.execution_kind !== "local_cli";
    ui.cliDetails.hidden = detail?.batch.execution_kind !== "local_cli";
    ui.itemDialog.showModal();
    connectAttemptLog(latestAttempt(item));
  }

  function updateItemActions(item) {
    const latest = latestAttempt(item);
    const running = Boolean(latest && activeAttemptStates.has(latest.state));
    ui.itemUp.disabled = !item || item.order === 0 || running;
    ui.itemDown.disabled = !item || item.order === detail?.batch.items.length - 1 || running;
    ui.itemCopy.disabled = !item;
    ui.itemDelete.disabled = !item || running;
    ui.itemRun.disabled = !item || running || !item.enabled;
    ui.itemRetry.disabled = !item || running || !latest || !terminalAttemptStates.has(latest.state);
    ui.itemCancel.disabled = !running;
    ui.cliCleanup.disabled = !latest || !terminalAttemptStates.has(latest.state) || latest.execution_kind !== "local_cli";
    ui.cliLogSave.disabled = !latest || latest.execution_kind !== "local_cli";
  }

  function renderAttemptHistory(item) {
    ui.cliPath.textContent = "";
    ui.attemptHistory.replaceChildren();
    const attempts = item?.attempts ?? [];
    if (!attempts.length) ui.attemptHistory.textContent = "尚未运行。";
    for (const attempt of [...attempts].reverse()) {
      const row = document.createElement("div");
      row.className = "video-attempt-row";
      const state = document.createElement("strong");
      state.textContent = stateText(attempt.state);
      const time = document.createElement("small");
      time.textContent = new Date(attempt.created_at).toLocaleString();
      const error = document.createElement("span");
      error.textContent = attempt.error?.message || attempt.remote_status || "";
      row.append(state, time, error);
      ui.attemptHistory.append(row);
    }
    const latest = latestAttempt(item);
    ui.attemptTechnical.textContent = latest ? JSON.stringify({
      id: latest.id, state: latest.state, snapshot: latest.snapshot, remote_job_id: latest.remote_job_id,
      remote_status: latest.remote_status, queue_position: latest.queue_position, pid: latest.pid,
      actual_frame_count: latest.actual_frame_count, error: latest.error,
    }, null, 2) : "尚未运行。";
    if (latest?.workspace_relative_path) ui.cliPath.textContent = `工作区相对路径：${latest.workspace_relative_path}`;
  }

  function assetLabel(id) {
    const asset = assetCache.get(id);
    if (!asset) return id;
    return `${asset.display_name}${asset.state === "active" ? "" : "（已归档但仍引用）"}`;
  }

  function imageIDsFor(group) {
    if (group === "init") return imageAssetDraft.init_image_id ? [imageAssetDraft.init_image_id] : [];
    if (group === "end") return imageAssetDraft.end_image_id ? [imageAssetDraft.end_image_id] : [];
    return imageAssetDraft.control_frame_ids ?? [];
  }

  function renderItemAssets() {
    for (const group of ["init", "end", "control"]) {
      const container = document.querySelector(`[data-video-asset-group="${group}"] .video-asset-reference`);
      container.replaceChildren();
      const ids = imageIDsFor(group);
      if (!ids.length) container.textContent = "未选择";
      for (const id of ids) {
        const pill = document.createElement("span");
        pill.className = "video-asset-reference-pill";
        const label = document.createElement("span");
        label.textContent = assetLabel(id);
        const remove = textButton("移除", "text-button", () => {
          if (group === "control") imageAssetDraft.control_frame_ids = imageAssetDraft.control_frame_ids.filter((candidate) => candidate !== id);
          else imageAssetDraft[`${group}_image_id`] = "";
          renderItemAssets();
        });
        pill.append(label, remove);
        container.append(pill);
      }
    }
    renderCLIAssets();
  }

  async function chooseImageAssets(group) {
    const multiple = group === "control";
    const existingIDs = imageIDsFor(group);
    const selected = await openAssetPicker({ multiple, selected: existingIDs, mediaPrefix: "image/" });
    if (!selected) return;
    for (const asset of selected) assetCache.set(asset.id, asset);
    const archivedIDs = existingIDs.filter((id) => assetCache.get(id)?.state !== "active");
    if (multiple) imageAssetDraft.control_frame_ids = [...new Set([...archivedIDs, ...selected.map((asset) => asset.id)])];
    else imageAssetDraft[`${group}_image_id`] = selected[0]?.id || archivedIDs[0] || "";
    renderItemAssets();
  }

  function reindexCLIAssets() {
    cliAssetDraft.forEach((selected, index) => { selected.order = index; });
  }

  function renderCLIAssets() {
    ui.cliAssetList.replaceChildren();
    if (!cliAssetDraft.length) ui.cliAssetList.textContent = "未选择 CLI 素材。";
    cliAssetDraft.forEach((selected, index) => {
      const row = document.createElement("div");
      row.className = "video-cli-asset-row";
      const label = document.createElement("span");
      label.textContent = assetLabel(selected.asset_id);
      const role = document.createElement("input");
      role.required = true;
      role.maxLength = 64;
      role.placeholder = "必填角色，例如 reference_video";
      role.value = selected.role || "";
      role.setAttribute("aria-label", `${label.textContent} 的角色`);
      role.addEventListener("input", () => { selected.role = role.value.trim(); });
      const actions = document.createElement("div");
      actions.className = "button-row";
      const up = textButton("上移", "text-button", () => moveCLIAsset(index, -1));
      up.disabled = index === 0;
      const down = textButton("下移", "text-button", () => moveCLIAsset(index, 1));
      down.disabled = index === cliAssetDraft.length - 1;
      actions.append(up, down, textButton("移除", "text-button danger-text", () => {
        cliAssetDraft.splice(index, 1);
        reindexCLIAssets();
        renderCLIAssets();
      }));
      row.append(label, role, actions);
      ui.cliAssetList.append(row);
    });
  }

  function moveCLIAsset(index, direction) {
    const target = index + direction;
    if (target < 0 || target >= cliAssetDraft.length) return;
    [cliAssetDraft[index], cliAssetDraft[target]] = [cliAssetDraft[target], cliAssetDraft[index]];
    reindexCLIAssets();
    renderCLIAssets();
  }

  function reconcileSelectedCLIAssets(previous, pickerAssets) {
    const pickedIDs = new Set(pickerAssets.map((asset) => asset.id));
    const previousIDs = new Set(previous.map((item) => item.asset_id));
    const surviving = previous.filter((item) => assetCache.get(item.asset_id)?.state !== "active" || pickedIDs.has(item.asset_id));
    const appended = pickerAssets.filter((asset) => !previousIDs.has(asset.id)).map((asset) => ({
      asset_id: asset.id, role: "", order: 0,
    }));
    return [...surviving, ...appended];
  }

  async function chooseCLIAssets() {
    const activeIDs = cliAssetDraft.filter((selected) => assetCache.get(selected.asset_id)?.state === "active").map((selected) => selected.asset_id);
    const selected = await openAssetPicker({ multiple: true, selected: activeIDs });
    if (!selected) return;
    for (const asset of selected) assetCache.set(asset.id, asset);
    cliAssetDraft = reconcileSelectedCLIAssets(cliAssetDraft, selected);
    reindexCLIAssets();
    renderCLIAssets();
  }

  function storedItemPayload(item, changes = {}) {
    return {
      prompt: item.prompt, negative_prompt: item.negative_prompt, enabled: item.enabled,
      params_override: item.params_override ?? {}, timing_override: item.timing_override ?? null,
      init_image_id: item.init_image_id || "", end_image_id: item.end_image_id || "",
      control_frame_ids: clone(item.control_frame_ids ?? []), selected_assets: clone(item.selected_assets ?? []), ...changes,
    };
  }

  function itemPayload() {
    const params = parseObject(ui.itemOverride.value, "Params Override");
    const selectedAssets = clone(cliAssetDraft);
    for (const selected of selectedAssets) {
      if (!/^[A-Za-z0-9._-]{1,64}$/.test(selected.role || "")) throw new Error("每个 CLI 素材都需要 1–64 位安全角色名称。");
    }
    return {
      prompt: ui.itemPrompt.value, negative_prompt: ui.itemNegative.value, enabled: ui.itemEnabled.checked,
      params_override: params, init_image_id: imageAssetDraft.init_image_id, end_image_id: imageAssetDraft.end_image_id,
      control_frame_ids: clone(imageAssetDraft.control_frame_ids), selected_assets: selectedAssets,
    };
  }

  async function saveItem(event) {
    event.preventDefault();
    try {
      const payload = itemPayload();
      if (editingItemID) {
        await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(editingItemID)}`, jsonOptions("PUT", payload));
      } else {
        const body = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", { items: [payload] }));
        editingItemID = body.items?.[0]?.id || "";
      }
      ui.itemDialog.close();
      closeAttemptLog();
      await refreshDetail();
      setStatus("视频请求项已保存。");
    } catch (error) { ui.itemError.textContent = error.message; }
  }

  async function updateItemEnabled(itemID, enabled) {
    const item = findItem(itemID);
    if (!item) return;
    try {
      const updated = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(itemID)}`, jsonOptions("PUT", storedItemPayload(item, { enabled })));
      Object.assign(item, updated);
      renderItems();
      setStatus(enabled ? "请求项已启用。" : "请求项已停用。");
    } catch (error) { setStatus(`更新请求项失败：${error.message}`, true); }
  }

  async function moveItem(itemID, direction) {
    try {
      detail.batch = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(itemID)}/move`, jsonOptions("POST", { direction }));
      renderItems();
      if (ui.itemDialog.open) updateItemActions(findItem());
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`移动失败：${error.message}`, true);
    }
  }

  async function copyItem(itemID = editingItemID) {
    const source = findItem(itemID);
    if (!source) return;
    try {
      const body = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items`, jsonOptions("POST", { items: [storedItemPayload(source)] }));
      const copyID = body.items[0].id;
      const moves = Math.max(0, detail.batch.items.length - source.order - 1);
      for (let index = 0; index < moves; index += 1) {
        detail.batch = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(copyID)}/move`, jsonOptions("POST", { direction: -1 }));
      }
      await refreshDetail();
      if (ui.itemDialog.open) {
        ui.itemDialog.close();
        closeAttemptLog();
        openItemEditor(copyID);
      }
      setStatus("已复制到原请求项下方。");
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`复制失败：${error.message}`, true);
    }
  }

  async function deleteItem(itemID = editingItemID) {
    const item = findItem(itemID);
    if (!item || !window.confirm("删除这个视频请求项及其 Attempt 历史？结果 Asset 不会被删除。")) return;
    try {
      await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(item.id)}`, { method: "DELETE" });
      if (ui.itemDialog.open && editingItemID === item.id) {
        ui.itemDialog.close();
        closeAttemptLog();
      }
      await refreshDetail();
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`删除失败：${error.message}`, true);
    }
  }

  function attachAttempt(attempt) {
    if (!attempt || !detail) return false;
    const item = detail.batch.items.find((candidate) => candidate.id === attempt.item_id);
    if (!item) return false;
    item.attempts ??= [];
    const index = item.attempts.findIndex((candidate) => candidate.id === attempt.id);
    if (index >= 0) item.attempts[index] = attempt;
    else item.attempts.push(attempt);
    return true;
  }

  function renderAfterAttempts() {
    renderItems();
    renderResults();
    if (ui.itemDialog.open) {
      const item = findItem();
      renderAttemptHistory(item);
      updateItemActions(item);
      connectAttemptLog(latestAttempt(item));
    }
  }

  async function executeBatch() {
    if (!selectedBatchID) return;
    try {
      const body = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/execute`, jsonOptions("POST"));
      for (const attempt of body.attempts ?? []) attachAttempt(attempt);
      renderAfterAttempts();
      setStatus("待执行视频项已立即加入队列。");
      scheduleReconcile();
    } catch (error) { setStatus(`启动失败：${error.message}`, true); }
  }

  async function executeItem(itemID = editingItemID) {
    if (!itemID) return;
    try {
      const body = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(itemID)}/execute`, jsonOptions("POST"));
      for (const attempt of body.attempts ?? []) attachAttempt(attempt);
      renderAfterAttempts();
      setStatus("视频请求项已立即加入队列。");
      scheduleReconcile();
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`启动失败：${error.message}`, true);
    }
  }

  async function retryAttempt(attemptID) {
    if (!attemptID) return;
    try {
      const attempt = await request(`/api/v1/videos/attempts/${encodeURIComponent(attemptID)}/retry`, jsonOptions("POST"));
      attachAttempt(attempt);
      renderAfterAttempts();
      setStatus("重试已立即加入队列。");
      scheduleReconcile();
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = error.message;
      else setStatus(`重试失败：${error.message}`, true);
    }
  }

  async function cancelAttempt(attemptID) {
    if (!attemptID) return;
    try {
      const attempt = await request(`/api/v1/videos/attempts/${encodeURIComponent(attemptID)}/cancel`, jsonOptions("POST"));
      attachAttempt(attempt);
      renderAfterAttempts();
      if (attempt.error?.code === "remote_cannot_cancel") {
        const message = "远端拒绝取消（409）：生成仍在运行，工作台会继续对账状态。";
        if (ui.itemDialog.open) ui.itemError.textContent = message;
        setStatus(message, true);
      } else {
        setStatus("取消请求已处理。");
      }
      scheduleReconcile();
    } catch (error) {
      if (ui.itemDialog.open) ui.itemError.textContent = `取消失败；远端任务可能仍在运行：${error.message}`;
      setStatus(`取消失败；远端任务可能仍在运行：${error.message}`, true);
    }
  }

  function connectBatchEvents() {
    closeBatchEvents();
    if (!active || !selectedBatchID) return;
    const batchID = selectedBatchID;
    const source = new EventSource(`/api/v1/videos/batches/${encodeURIComponent(batchID)}/events`);
    eventSource = source;
    source.addEventListener("snapshot", (event) => {
      if (eventSource !== source || selectedBatchID !== batchID) return;
      let payload;
      try { payload = JSON.parse(event.data); } catch (_) { return; }
      for (const attempt of payload.attempts ?? []) attachAttempt(attempt);
      renderAfterAttempts();
      scheduleDetailRefresh();
    });
    source.addEventListener("state", (event) => {
      if (eventSource !== source || selectedBatchID !== batchID) return;
      let payload;
      try { payload = JSON.parse(event.data); } catch (_) { return; }
      const known = attachAttempt(payload.attempt);
      renderAfterAttempts();
      if (!known || terminalAttemptStates.has(payload.attempt?.state)) scheduleDetailRefresh();
    });
    source.addEventListener("persistence_error", () => {
      setStatus("任务状态已变化，但一次持久化失败；正在通过 GET 对账。", true);
      scheduleDetailRefresh();
    });
    source.onerror = () => {
      if (active && eventSource === source && selectedBatchID === batchID) setStatus("视频任务事件流暂时断开，浏览器会自动重连并对账。", true);
    };
  }

  function closeBatchEvents() {
    if (eventSource) eventSource.close();
    eventSource = null;
  }

  async function scheduleDetailRefresh() {
    detailRefreshPending = true;
    if (detailRefreshRunning) return;
    detailRefreshRunning = true;
    try {
      while (detailRefreshPending && active) {
        detailRefreshPending = false;
        try { await refreshDetail(); }
        catch (error) { setStatus(`视频状态对账失败：${error.message}`, true); }
      }
    } finally {
      detailRefreshRunning = false;
      if (detailRefreshPending && active) scheduleDetailRefresh();
    }
  }

  function scheduleReconcile() {
    const timer = window.setTimeout(() => {
      timers.delete(timer);
      if (active) scheduleDetailRefresh();
    }, 250);
    timers.add(timer);
  }

  function clearTimers() {
    for (const timer of timers) window.clearTimeout(timer);
    timers.clear();
  }

  function base64Bytes(value) {
    const binary = atob(value || "");
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
    return bytes;
  }

  function appendBytes(left, right) {
    const joined = new Uint8Array(left.length + right.length);
    joined.set(left, 0);
    joined.set(right, left.length);
    return joined;
  }

  function renderLog() {
    const visibleOffset = Math.max(clearOffset, logStartOffset);
    const start = Math.min(logBytes.length, Math.max(0, visibleOffset - logStartOffset));
    ui.cliLog.textContent = logDecoder.decode(logBytes.slice(start));
  }

  function validLogSnapshot(startOffset, endOffset, byteLength) {
    return Number.isSafeInteger(startOffset) && Number.isSafeInteger(endOffset) && startOffset >= 0 &&
      endOffset >= startOffset && endOffset - startOffset === byteLength;
  }

  function scheduleLogReconnect(attemptID) {
    if (logReconnectTimer !== null) return;
    const delay = logReconnectDelay;
    logReconnectDelay = Math.min(logReconnectDelay * 2, 5000);
    logReconnectTimer = window.setTimeout(() => {
      const timer = logReconnectTimer;
      logReconnectTimer = null;
      timers.delete(timer);
      if (!active || !ui.itemDialog.open || logAttemptID !== attemptID) return;
      const latest = latestAttempt(findItem());
      if (latest?.id === attemptID) connectAttemptLog(latest);
    }, delay);
    timers.add(logReconnectTimer);
  }

  function invalidateLogStream(message) {
    logAwaitingSnapshot = true;
    if (logEventSource) logEventSource.close();
    logEventSource = null;
    ui.cliPath.textContent = message;
    if (logAttemptID) scheduleLogReconnect(logAttemptID);
  }

  function receiveLogSnapshot(payload) {
    const bytes = base64Bytes(payload.data_base64);
    const startOffset = Number(payload.start_offset);
    const endOffset = Number(payload.end_offset);
    if (!validLogSnapshot(startOffset, endOffset, bytes.length)) {
      invalidateLogStream("日志快照 offset 或字节长度无效；已丢弃并重新订阅权威快照。");
      return;
    }
    logStartOffset = startOffset;
    logEndOffset = endOffset;
    logBytes = bytes;
    logAwaitingSnapshot = false;
    logReconnectDelay = 250;
    renderLog();
  }

  function receiveLogChunk(payload) {
    if (logAwaitingSnapshot) return;
    const bytes = base64Bytes(payload.data_base64);
    const startOffset = Number(payload.start_offset);
    if (!Number.isSafeInteger(startOffset) || startOffset < 0) {
      invalidateLogStream("CLI 日志 chunk offset 无效；已丢弃并重新订阅权威快照。");
      return;
    }
    const chunkEnd = startOffset + bytes.length;
    if (!Number.isSafeInteger(chunkEnd)) {
      invalidateLogStream("CLI 日志 chunk 长度超出安全 offset 范围；已丢弃并重新订阅权威快照。");
      return;
    }
    if (chunkEnd <= logEndOffset) return;
    if (startOffset > logEndOffset) {
      invalidateLogStream("CLI 日志出现字节缺口；已丢弃该 chunk 并重新订阅权威快照。");
      return;
    } else {
      const overlap = Math.max(0, logEndOffset - startOffset);
      logBytes = appendBytes(logBytes, bytes.slice(overlap));
      logEndOffset = chunkEnd;
    }
    renderLog();
  }

  function connectAttemptLog(attempt) {
    if (!active || !ui.itemDialog.open || !attempt || attempt.execution_kind !== "local_cli" ||
      (!terminalAttemptStates.has(attempt.state) && attempt.state !== "running")) {
      if (!attempt || attempt?.id !== logAttemptID) closeAttemptLog();
      return;
    }
    if (logEventSource && logAttemptID === attempt.id) return;
    if (logAttemptID !== attempt.id) {
      closeAttemptLog();
      logAttemptID = attempt.id;
      clearOffset = logClearOffsets.get(attempt.id) ?? 0;
      logReconnectDelay = 250;
    }
    logAwaitingSnapshot = true;
    const source = new EventSource(`/api/v1/videos/attempts/${encodeURIComponent(attempt.id)}/logs`);
    logEventSource = source;
    source.addEventListener("snapshot", (event) => {
      if (logEventSource !== source || logAttemptID !== attempt.id) return;
      try { receiveLogSnapshot(JSON.parse(event.data)); }
      catch (error) { invalidateLogStream(`日志快照解析失败，已重新订阅：${error.message}`); }
    });
    source.addEventListener("chunk", (event) => {
      if (logEventSource !== source || logAttemptID !== attempt.id) return;
      try { receiveLogChunk(JSON.parse(event.data)); }
      catch (error) { invalidateLogStream(`日志字节解析失败，已重新订阅：${error.message}`); }
    });
    source.onerror = () => {
      if (active && logEventSource === source) {
        logAwaitingSnapshot = true;
        ui.cliPath.textContent = "CLI 日志流暂时断开，浏览器会自动重连并等待权威快照。";
      }
    };
  }

  function closeAttemptLog() {
    if (logEventSource) logEventSource.close();
    if (logReconnectTimer !== null) {
      window.clearTimeout(logReconnectTimer);
      timers.delete(logReconnectTimer);
      logReconnectTimer = null;
    }
    logEventSource = null;
    logAttemptID = "";
    logBytes = new Uint8Array();
    logStartOffset = 0;
    logEndOffset = 0;
    clearOffset = 0;
    logAwaitingSnapshot = false;
    logReconnectDelay = 250;
    ui.cliLog.textContent = "";
  }

  function clearAttemptLogLocally() {
    if (!logAttemptID) return;
    clearOffset = logEndOffset;
    logClearOffsets.set(logAttemptID, clearOffset);
    renderLog();
    ui.cliPath.textContent = "仅清除了当前浏览器显示；服务端原始日志保持不变。";
  }

  async function saveAttemptLog() {
    const attempt = latestAttempt(findItem());
    if (!attempt || attempt.execution_kind !== "local_cli") return;
    try {
      const saved = await request(`/api/v1/videos/attempts/${encodeURIComponent(attempt.id)}/logs/save`, jsonOptions("POST"));
      ui.cliPath.textContent = `日志已手动保存：${saved.workspace_path}`;
    } catch (error) { ui.itemError.textContent = `保存日志失败：${error.message}`; }
  }

  async function cleanupAttemptWorkspace() {
    const attempt = latestAttempt(findItem());
    if (!attempt || !terminalAttemptStates.has(attempt.state) || attempt.execution_kind !== "local_cli") return;
    if (!window.confirm("清理这个终态 CLI Attempt 的工作区？该操作不能撤销。")) return;
    try {
      closeAttemptLog();
      await request(`/api/v1/videos/attempts/${encodeURIComponent(attempt.id)}/workspace`, { method: "DELETE" });
      ui.cliPath.textContent = "CLI 工作区已清理。";
    } catch (error) { ui.itemError.textContent = `清理工作区失败：${error.message}`; }
  }

  function results() {
    const found = [];
    for (const item of detail?.batch.items ?? []) {
      for (const attempt of item.attempts ?? []) {
        if (attempt.output_asset_id) found.push({ item, attempt, assetID: attempt.output_asset_id });
      }
    }
    return found;
  }

  function renderResults() {
    ui.resultList.replaceChildren();
    const available = results();
    if (!available.length) {
      const empty = document.createElement("p");
      empty.className = "inline-message";
      empty.textContent = "成功结果会在这里通过受控 Asset 内容 API 播放。";
      ui.resultList.append(empty);
      return;
    }
    for (const result of available) {
      const asset = assetCache.get(result.assetID);
      const card = ui.resultTemplate.content.firstElementChild.cloneNode(true);
      card.dataset.state = asset?.state || "";
      const video = card.querySelector("[data-video-result]");
      video.src = `/api/v1/assets/${encodeURIComponent(result.assetID)}/content`;
      card.querySelector("[data-video-result-name]").textContent = asset?.display_name || result.assetID;
      card.querySelector("[data-video-result-state]").textContent = asset?.state === "active" ? "精选" : "归档";
      card.querySelector("[data-video-result-prompt]").textContent = result.item.prompt || "（空 Prompt）";
      const timing = result.attempt.snapshot?.timing ?? {};
      const fps = Number(timing.fps) || 0;
      const requested = Number(timing.requested_frames) || 0;
      const actual = Number(result.attempt.actual_frame_count) || 0;
      const duration = fps > 0 ? (actual || requested) / fps : 0;
      card.querySelector("[data-video-result-facts]").textContent =
        `请求 ${requested || "未报告"} 帧 · 实际 ${actual || "未报告"} 帧 · ${fps || "未报告"} FPS · ${duration ? duration.toFixed(3) : "未报告"} 秒`;
      const actions = card.querySelector("[data-video-result-actions]");
      actions.append(textButton(asset?.state === "active" ? "移入归档" : "设为精选", "text-button",
        () => setAssetState(result.assetID, asset?.state === "active" ? "archive" : "active")));
      const history = tailExtractionsBySource.get(result.assetID) ?? [];
      renderTailActions(card, result, history);
      ui.resultList.append(card);
    }
  }

  async function setAssetState(assetID, state) {
    try {
      const asset = await request(`/api/v1/assets/${encodeURIComponent(assetID)}/state`, jsonOptions("POST", { state }));
      assetCache.set(asset.id, asset);
      renderResults();
      setStatus(state === "active" ? "Asset 已设为精选。" : "Asset 已移入归档；已有引用继续显示。");
    } catch (error) { setStatus(`更新 Asset 失败：${error.message}`, true); }
  }

  function upsertTailExtraction(extraction) {
    if (!extraction?.id || !extraction.source_asset_id) return [];
    const history = [...(tailExtractionsBySource.get(extraction.source_asset_id) ?? [])];
    const index = history.findIndex((candidate) => candidate.id === extraction.id);
    if (index >= 0) history[index] = extraction;
    else history.push(extraction);
    history.sort((left, right) => {
      const time = new Date(left.created_at).getTime() - new Date(right.created_at).getTime();
      return time || left.id.localeCompare(right.id);
    });
    tailExtractionsBySource.set(extraction.source_asset_id, history);
    return history;
  }

  function latestTailExtraction(sourceAssetID) {
    const history = tailExtractionsBySource.get(sourceAssetID) ?? [];
    return history[history.length - 1] ?? null;
  }

  function renderTailHistory(container, history) {
    container.replaceChildren();
    if (!history.length) {
      container.textContent = "尚无提取历史。";
      return;
    }
    for (const extraction of [...history].reverse()) {
      const row = document.createElement("div");
      row.className = "video-tail-history-row";
      const summary = document.createElement("span");
      const error = extraction.error?.message ? ` · ${extraction.error.message}` : "";
      summary.textContent = `${new Date(extraction.created_at).toLocaleString()} · ${stateText(extraction.state)}${error}`;
      const save = textButton("保存本次日志", "text-button", () => saveTailLog(extraction.id));
      row.append(summary, save);
      container.append(row);
    }
  }

  function renderTailActions(card, result, history) {
    const status = card.querySelector("[data-video-tail-status]");
    const actions = card.querySelector("[data-video-tail-actions]");
    const extraction = history[history.length - 1] ?? null;
    status.textContent = extraction ? `${stateText(extraction.state)}${extraction.error?.message ? `：${extraction.error.message}` : ""}` : "尚未提取尾帧。";
    if (!extraction || terminalAttemptStates.has(extraction.state)) {
      const extract = textButton(extraction ? "重新提取尾帧" : "提取尾帧", "secondary-button", () => startTailExtraction(result.assetID));
      extract.disabled = !ui.tailPreset.value;
      actions.append(extract);
    } else {
      actions.append(textButton("取消尾帧提取", "text-button", () => cancelTailExtraction(extraction.id)));
    }
    if (extraction?.state === "succeeded" && extraction.output_asset_id) {
      const tailAsset = assetCache.get(extraction.output_asset_id);
      actions.append(
        textButton(tailAsset?.state === "active" ? "尾帧已精选" : "将尾帧设为精选", "text-button",
          () => setAssetState(extraction.output_asset_id, "active")),
        textButton("作为当前项首帧", "text-button", () => useTailAsCurrentItem(extraction.output_asset_id)),
        textButton("作为新项首帧", "text-button", () => useTailAsNewItem(extraction.output_asset_id)),
        textButton("保存提取日志", "text-button", () => saveTailLog(extraction.id)),
      );
    }
    renderTailHistory(card.querySelector("[data-video-tail-history]"), history);
  }

  async function startTailExtraction(sourceAssetID) {
    if (!ui.tailPreset.value) { setStatus("请先选择启用的尾帧预设。", true); return; }
    try {
      const extraction = await request("/api/v1/videos/tail-extractions", jsonOptions("POST", {
        source_asset_id: sourceAssetID, preset_id: ui.tailPreset.value,
      }));
      upsertTailExtraction(extraction);
      renderResults();
      connectTailEvents(extraction);
      setStatus("尾帧提取已加入队列。");
    } catch (error) { setStatus(`尾帧提取失败：${error.message}`, true); }
  }

  async function cancelTailExtraction(extractionID) {
    try {
      const extraction = await request(`/api/v1/videos/tail-extractions/${encodeURIComponent(extractionID)}/cancel`, jsonOptions("POST"));
      upsertTailExtraction(extraction);
      renderResults();
    } catch (error) { setStatus(`取消尾帧提取失败：${error.message}`, true); }
  }

  function connectRelevantTailEvents() {
    const sourceAssets = new Set(results().map((result) => result.assetID));
    for (const sourceAssetID of sourceAssets) {
      const extraction = latestTailExtraction(sourceAssetID);
      if (extraction && !terminalAttemptStates.has(extraction.state)) connectTailEvents(extraction);
    }
  }

  function connectTailEvents(extraction) {
    if (!active || latestTailExtraction(extraction.source_asset_id)?.id !== extraction.id ||
      terminalAttemptStates.has(extraction.state) || tailEventSources.has(extraction.id)) return;
    for (const previous of tailExtractionsBySource.get(extraction.source_asset_id) ?? []) {
      if (previous.id === extraction.id) continue;
      tailEventSources.get(previous.id)?.close();
      tailEventSources.delete(previous.id);
    }
    const source = new EventSource(`/api/v1/videos/tail-extractions/${encodeURIComponent(extraction.id)}/events`);
    tailEventSources.set(extraction.id, source);
    const receive = async (event) => {
      if (tailEventSources.get(extraction.id) !== source) return;
      let current;
      try { current = JSON.parse(event.data); } catch (_) { return; }
      upsertTailExtraction(current);
      if (latestTailExtraction(current.source_asset_id)?.id !== current.id) {
        source.close();
        tailEventSources.delete(current.id);
        return;
      }
      if (current.state === "succeeded" && current.output_asset_id) {
        try {
          const asset = await request(`/api/v1/assets/${encodeURIComponent(current.output_asset_id)}`);
          assetCache.set(asset.id, asset);
        } catch (error) { setStatus(`尾帧 Asset 读取失败：${error.message}`, true); }
      }
      renderResults();
      if (terminalAttemptStates.has(current.state)) {
        source.close();
        tailEventSources.delete(current.id);
      }
    };
    source.addEventListener("snapshot", receive);
    source.addEventListener("state", receive);
    source.onerror = () => {
      if (active && tailEventSources.get(extraction.id) === source) setStatus("尾帧状态流暂时断开，浏览器会自动重连。", true);
    };
  }

  function closeTailEvents() {
    for (const source of tailEventSources.values()) source.close();
    tailEventSources.clear();
  }

  async function useTailAsCurrentItem(assetID) {
    const item = findItem();
    if (!item) { setStatus("请先打开一个现有请求项，再把尾帧写为当前项首帧。", true); return; }
    try {
      await ensureAssetActive(assetID);
      const updated = await request(`/api/v1/videos/batches/${encodeURIComponent(selectedBatchID)}/items/${encodeURIComponent(item.id)}`,
        jsonOptions("PUT", storedItemPayload(item, { init_image_id: assetID })));
      Object.assign(item, updated);
      await refreshDetail();
      setStatus("尾帧已写入当前请求项的首帧。");
    } catch (error) {
      const message = `无法把尾帧设为当前项首帧：${error.message}`;
      if (ui.itemDialog.open) ui.itemError.textContent = message;
      setStatus(message, true);
    }
  }

  async function ensureAssetActive(assetID) {
    let asset = assetCache.get(assetID);
    try {
      if (!asset) asset = await request(`/api/v1/assets/${encodeURIComponent(assetID)}`);
      if (asset.state !== "active") {
        asset = await request(`/api/v1/assets/${encodeURIComponent(assetID)}/state`, jsonOptions("POST", { state: "active" }));
      }
      assetCache.set(asset.id, asset);
      return asset;
    } catch (error) {
      throw new Error(`无法先将归档尾帧设为精选：${error.message}`);
    }
  }

  async function useTailAsNewItem(assetID) {
    try {
      await ensureAssetActive(assetID);
      openItemEditor("", assetID);
      setStatus("尾帧已激活并填入新请求项首帧，请编辑后保存。");
    } catch (error) {
      setStatus(`无法用尾帧创建请求项：${error.message}`, true);
    }
  }

  async function saveTailLog(extractionID) {
    try {
      const saved = await request(`/api/v1/videos/tail-extractions/${encodeURIComponent(extractionID)}/logs/save`, jsonOptions("POST"));
      setStatus(`尾帧提取日志已手动保存：${saved.workspace_path}`);
    } catch (error) { setStatus(`保存尾帧日志失败：${error.message}`, true); }
  }

  async function enter() {
    if (active) return;
    active = true;
    const version = ++loadVersion;
    ui.sidebarControls.hidden = false;
    document.querySelector("#sidebar-content").replaceChildren(ui.sidebarControls);
    ui.sidebarSearch.disabled = false;
    ui.sidebarSearch.placeholder = "搜索视频批次标题或文件夹";
    ui.workspace.hidden = false;
    ui.batchListStatus.textContent = "正在读取视频批次…";
    batchDraftDirty = false;
    try {
      const [videoConfig, batchBody, tailBody] = await Promise.all([
        request("/api/v1/videos/config"), request("/api/v1/videos/batches"), request("/api/v1/videos/tail-extractions"),
      ]);
      if (!active || version !== loadVersion) return;
      configuration = videoConfig;
      configuration.http_providers ??= [];
      configuration.cli_presets ??= [];
      configuration.tail_frame_presets ??= [];
      batches = batchBody.batches ?? [];
      tailExtractionsBySource.clear();
      for (const extraction of tailBody.extractions ?? []) upsertTailExtraction(extraction);
      const nextBatchID = batches.some((batch) => batch.id === selectedBatchID) ? selectedBatchID : (batches[0]?.id ?? "");
      renderFolderFilter();
      renderBatchList();
      renderTailPresets();
      if (nextBatchID) await selectBatch(nextBatchID, { discardDraft: true });
      else await selectBatch("", { discardDraft: true });
    } catch (error) {
      if (active && version === loadVersion) setStatus(`读取视频工作区失败：${error.message}`, true);
    }
  }

  function leave() {
    active = false;
    loadVersion += 1;
    batchListVersion += 1;
    detailVersion += 1;
    selectionVersion += 1;
    detailRefreshPending = false;
    batchDraftDirty = false;
    closeBatchEvents();
    closeAttemptLog();
    closeTailEvents();
    clearTimers();
    for (const dialog of [ui.createDialog, ui.bulkDialog, ui.itemDialog]) if (dialog.open) dialog.close();
    ui.workspace.hidden = true;
  }

  ui.batchForm.addEventListener("submit", saveBatch);
  ui.deleteBatch.addEventListener("click", deleteBatch);
  ui.runBatch.addEventListener("click", executeBatch);
  ui.capabilities.addEventListener("click", loadCapabilities);
  ui.newBatch.addEventListener("click", openCreateDialog);
  ui.createForm.addEventListener("submit", createBatch);
  ui.createKind.addEventListener("change", () => fillPresetSelect(ui.createPreset, ui.createKind.value));
  ui.bulkOpen.addEventListener("click", openBulkDialog);
  ui.bulkForm.addEventListener("submit", createBulkItems);
  ui.itemAdd.addEventListener("click", () => openItemEditor());
  ui.itemForm.addEventListener("submit", saveItem);
  ui.itemUp.addEventListener("click", () => moveItem(editingItemID, -1));
  ui.itemDown.addEventListener("click", () => moveItem(editingItemID, 1));
  ui.itemCopy.addEventListener("click", () => copyItem());
  ui.itemDelete.addEventListener("click", () => deleteItem());
  ui.itemRun.addEventListener("click", () => executeItem());
  ui.itemRetry.addEventListener("click", () => retryAttempt(latestAttempt(findItem())?.id));
  ui.itemCancel.addEventListener("click", () => cancelAttempt(latestAttempt(findItem())?.id));
  ui.cliAssetsPick.addEventListener("click", chooseCLIAssets);
  ui.cliLogClear.addEventListener("click", clearAttemptLogLocally);
  ui.cliLogSave.addEventListener("click", saveAttemptLog);
  ui.cliCleanup.addEventListener("click", cleanupAttemptWorkspace);
  ui.folderFilter.addEventListener("change", renderBatchList);
  ui.sidebarSearch.addEventListener("input", () => { if (active) renderBatchList(); });
  ui.executionKind.addEventListener("change", () => {
    fillPresetSelect(ui.preset, ui.executionKind.value);
    ui.capabilities.hidden = ui.executionKind.value !== "http";
    markBatchDraftDirty();
  });
  for (const control of [ui.title, ui.folder, ui.preset, ui.concurrency, ui.commonParams]) {
    control.addEventListener("input", markBatchDraftDirty);
    control.addEventListener("change", markBatchDraftDirty);
  }
  for (const control of [ui.timingFrames, ui.timingDuration, ui.frameCount, ui.duration, ui.fps]) {
    control.addEventListener("input", () => { syncTimingControls(); markBatchDraftDirty(); });
    control.addEventListener("change", () => { syncTimingControls(); markBatchDraftDirty(); });
  }
  ui.tailPreset.addEventListener("change", renderResults);
  for (const button of document.querySelectorAll("[data-video-workspace-close]")) {
    button.addEventListener("click", () => {
      const dialog = document.querySelector(`#${button.dataset.videoWorkspaceClose}`);
      if (dialog === ui.itemDialog) closeAttemptLog();
      dialog.close();
    });
  }
  for (const button of document.querySelectorAll("[data-video-asset-pick]")) {
    button.addEventListener("click", () => chooseImageAssets(button.dataset.videoAssetPick));
  }
  ui.itemDialog.addEventListener("close", closeAttemptLog);

  return { enter, leave };
}
