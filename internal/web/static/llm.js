const terminalRunStates = new Set(["succeeded", "failed", "cancelled", "interrupted"]);

export function createLLMWorkspace({ sidebarContent, sidebarSearch, readAPIError, openAssetPicker }) {
  const ui = {
    workspace: document.querySelector("#llm-workspace"),
    sidebarControls: document.querySelector("#llm-sidebar-controls"),
    folderFilter: document.querySelector("#llm-session-folder-filter"),
    newSession: document.querySelector("#llm-session-new"),
    sessionList: document.querySelector("#llm-session-list"),
    sessionListStatus: document.querySelector("#llm-session-list-status"),
    sessionForm: document.querySelector("#llm-session-form"),
    sessionTitle: document.querySelector("#llm-session-title"),
    sessionFolder: document.querySelector("#llm-session-folder"),
    sessionFork: document.querySelector("#llm-session-fork"),
    status: document.querySelector("#llm-workspace-status"),
    branchChooser: document.querySelector("#llm-branch-chooser"),
    currentPath: document.querySelector("#llm-current-path"),
    quickPathBar: document.querySelector("#llm-quick-path-bar"),
    executeSelected: document.querySelector("#llm-execute-selected"),
    runList: document.querySelector("#llm-run-list"),
    panelTemplate: document.querySelector("#llm-panel-template"),
    runTemplate: document.querySelector("#llm-run-template"),
    sessionDialog: document.querySelector("#llm-session-dialog"),
    sessionDialogForm: document.querySelector("#llm-session-dialog-form"),
    sessionDialogTitle: document.querySelector("#llm-session-dialog-title"),
    sessionDialogName: document.querySelector("#llm-session-dialog-name"),
    sessionDialogFolder: document.querySelector("#llm-session-dialog-folder"),
    sessionDialogMode: document.querySelector("#llm-session-dialog-mode"),
    sessionDialogPanel: document.querySelector("#llm-session-dialog-panel"),
    sessionDialogError: document.querySelector("#llm-session-dialog-error"),
    panelDialog: document.querySelector("#llm-panel-dialog"),
    panelDialogForm: document.querySelector("#llm-panel-dialog-form"),
    panelParent: document.querySelector("#llm-panel-parent"),
    newPanelTitle: document.querySelector("#llm-new-panel-title"),
    newPanelContent: document.querySelector("#llm-new-panel-content"),
    panelDialogError: document.querySelector("#llm-panel-dialog-error"),
    knowledgeDialog: document.querySelector("#llm-knowledge-picker"),
    knowledgeForm: document.querySelector("#llm-knowledge-picker-form"),
    knowledgePanel: document.querySelector("#llm-knowledge-panel"),
    knowledgeOptions: document.querySelector("#llm-knowledge-options"),
  };

  let active = false;
  let loadVersion = 0;
  let sessions = [];
  let workspace = null;
  let configuration = { quick_paths: [], prompt_templates: [] };
  let selectedSessionID = "";
  let knowledgeNotes = [];
  const selectedQuickPaths = new Set();
  const runs = new Map();
  const eventSources = new Map();

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
    return {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    };
  }

  function currentPanel() {
    if (!workspace) return null;
    return workspace.current_path.find((panel) => panel.id === workspace.session.current_panel_id) ?? null;
  }

  function panelByID(panelID) {
    return workspace?.panels.find((panel) => panel.id === panelID) ?? null;
  }

  function editablePanelByID(panelID) {
    return workspace?.current_path.find((panel) => panel.id === panelID) ?? null;
  }

  function readPanelDraft(panel) {
    const card = [...ui.currentPath.querySelectorAll(".llm-panel-card")]
      .find((candidate) => candidate.dataset.panelId === panel.id);
    if (!card) return null;
    return {
      title: card.querySelector('[data-panel-field="title"]').value,
      content: card.querySelector('[data-panel-field="content"]').value,
      included: card.querySelector('[data-panel-field="included"]').checked,
    };
  }

  function capturePanelDraft(panel) {
    const draft = readPanelDraft(panel);
    if (draft) Object.assign(panel, draft);
  }

  function renderFolderFilter() {
    const selected = ui.folderFilter.value;
    const folders = [...new Set(sessions.map((session) => session.folder).filter(Boolean))]
      .sort((left, right) => left.localeCompare(right));
    ui.folderFilter.replaceChildren(new Option("全部文件夹", ""));
    for (const folder of folders) ui.folderFilter.append(new Option(folder, folder));
    ui.folderFilter.value = folders.includes(selected) ? selected : "";
  }

  function filteredSessions() {
    const query = sidebarSearch.value.trim().toLocaleLowerCase();
    const folder = ui.folderFilter.value;
    return sessions.filter((session) => {
      if (folder && session.folder !== folder) return false;
      return !query || `${session.title}\n${session.folder || ""}`.toLocaleLowerCase().includes(query);
    });
  }

  function renderSessions() {
    const visible = filteredSessions();
    ui.sessionList.replaceChildren();
    ui.sessionListStatus.textContent = visible.length ? "" : "当前筛选下没有会话。";
    for (const session of visible) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "llm-session-item";
      if (session.id === selectedSessionID) button.setAttribute("aria-current", "true");
      const title = document.createElement("strong");
      title.textContent = session.title;
      const folder = document.createElement("small");
      folder.textContent = session.folder || "未分类";
      button.append(title, folder);
      button.addEventListener("click", () => selectSession(session.id));
      ui.sessionList.append(button);
    }
  }

  async function loadSessions(preferredID = selectedSessionID) {
    ui.sessionListStatus.textContent = "正在读取会话…";
    const body = await request("/api/v1/llm/sessions");
    sessions = body.sessions ?? [];
    if (preferredID && sessions.some((session) => session.id === preferredID)) selectedSessionID = preferredID;
    else selectedSessionID = sessions[0]?.id ?? "";
    renderFolderFilter();
    renderSessions();
    return selectedSessionID;
  }

  async function loadConfiguration() {
    configuration = await request("/api/v1/llm/config");
    const available = new Set((configuration.quick_paths ?? []).filter(providerEnabled).map((path) => path.id));
    for (const id of selectedQuickPaths) {
      if (!available.has(id)) selectedQuickPaths.delete(id);
    }
    if (!selectedQuickPaths.size) {
      const firstEnabled = (configuration.quick_paths ?? []).find((path) => {
        const provider = (configuration.providers ?? []).find((item) => item.id === path.provider_id);
        return provider?.enabled;
      });
      if (firstEnabled) selectedQuickPaths.add(firstEnabled.id);
    }
  }

  async function selectSession(sessionID) {
    selectedSessionID = sessionID;
    renderSessions();
    if (!sessionID) {
      workspace = null;
      renderWorkspace();
      return;
    }
    setStatus("正在读取工作区…");
    try {
      workspace = await request(`/api/v1/llm/sessions/${encodeURIComponent(sessionID)}`);
      renderWorkspace();
      setStatus("所有修改都需要显式保存。运行会先保存不可变请求快照。");
    } catch (error) {
      setStatus(`读取失败：${error.message}`, true);
    }
  }

  async function refreshWorkspace() {
    if (!selectedSessionID) return;
    workspace = await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}`);
    await loadSessions(selectedSessionID);
    renderWorkspace();
  }

  function renderWorkspace() {
    const hasSession = Boolean(workspace);
    ui.sessionForm.hidden = !hasSession;
    ui.sessionTitle.disabled = !hasSession;
    ui.sessionFolder.disabled = !hasSession;
    ui.sessionFork.disabled = !hasSession;
    ui.executeSelected.disabled = !hasSession;
    if (!hasSession) {
      ui.sessionTitle.value = "";
      ui.sessionFolder.value = "";
      ui.branchChooser.replaceChildren();
      ui.currentPath.replaceChildren();
      ui.quickPathBar.replaceChildren();
      setStatus("选择或新建会话。");
      return;
    }
    ui.sessionTitle.value = workspace.session.title;
    ui.sessionFolder.value = workspace.session.folder || "";
    renderBranches();
    renderPanels();
    renderQuickPaths();
  }

  function renderBranches() {
    ui.branchChooser.replaceChildren();
    for (const branch of workspace.branches ?? []) {
      const group = document.createElement("div");
      group.className = "llm-branch-group";
      const parent = panelByID(branch.parent_id);
      const label = document.createElement("span");
      label.textContent = `${parent?.title || "Panel"} 的下一步`;
      group.append(label);
      for (const option of branch.options ?? []) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = option.id === branch.selected_child_id ? "branch-button selected" : "branch-button";
        button.textContent = option.title || "未命名 Panel";
        button.addEventListener("click", () => chooseBranch(option.id));
        group.append(button);
      }
      ui.branchChooser.append(group);
    }
  }

  async function chooseBranch(panelID) {
    if (!workspace || panelID === workspace.session.current_panel_id) return;
    setStatus("正在切换分支…");
    try {
      workspace = await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}`, jsonOptions("PUT", {
        title: workspace.session.title,
        folder: workspace.session.folder || "",
        current_panel_id: panelID,
      }));
      renderWorkspace();
      renderSessions();
      setStatus("已切换分支；未保存的表单内容不会被写入。");
    } catch (error) {
      setStatus(`切换失败：${error.message}`, true);
    }
  }

  function makeReferencePill(kind, id, panel) {
    const pill = document.createElement("span");
    pill.className = "llm-reference-pill";
    const name = document.createElement("span");
    const note = kind === "knowledge" ? knowledgeNotes.find((item) => item.id === id) : null;
    name.textContent = note?.title || id;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "移除";
    remove.setAttribute("aria-label", `移除 ${name.textContent}`);
    remove.addEventListener("click", () => {
      capturePanelDraft(panel);
      const field = kind === "knowledge" ? "knowledge_ids" : "asset_ids";
      panel[field] = (panel[field] ?? []).filter((candidate) => candidate !== id);
      renderPanels();
      setStatus("引用已从 Panel 草稿移除；点击“保存 Panel”后写入。");
    });
    pill.append(name, remove);
    return pill;
  }

  function renderReferenceSummary(container, panel) {
    container.replaceChildren();
    const knowledge = document.createElement("div");
    knowledge.className = "llm-reference-row";
    const knowledgeLabel = document.createElement("strong");
    knowledgeLabel.textContent = "知识";
    knowledge.append(knowledgeLabel);
    for (const id of panel.knowledge_ids ?? []) knowledge.append(makeReferencePill("knowledge", id, panel));
    if (!(panel.knowledge_ids ?? []).length) knowledge.append(document.createTextNode("无"));
    const assets = document.createElement("div");
    assets.className = "llm-reference-row";
    const assetLabel = document.createElement("strong");
    assetLabel.textContent = "Assets";
    assets.append(assetLabel);
    for (const id of panel.asset_ids ?? []) assets.append(makeReferencePill("asset", id, panel));
    if (!(panel.asset_ids ?? []).length) assets.append(document.createTextNode("无"));
    container.append(knowledge, assets);
  }

  function renderRevisions(container, panel) {
    container.replaceChildren();
    if (!(panel.revisions ?? []).length) {
      container.textContent = "尚无修订记录。";
      return;
    }
    [...panel.revisions].reverse().forEach((revision) => {
      const row = document.createElement("div");
      row.className = "llm-revision-row";
      const summary = document.createElement("span");
      summary.textContent = `${revision.title || "未命名"} · ${new Date(revision.created_at).toLocaleString()}`;
      const restore = document.createElement("button");
      restore.type = "button";
      restore.className = "text-button";
      restore.textContent = "恢复";
      restore.addEventListener("click", () => restoreRevision(panel.id, revision.id));
      row.append(summary, restore);
      container.append(row);
    });
  }

  function renderPanels() {
    ui.currentPath.replaceChildren();
    for (const [index, panel] of (workspace.current_path ?? []).entries()) {
      const card = ui.panelTemplate.content.firstElementChild.cloneNode(true);
      card.dataset.panelId = panel.id;
      card.dataset.pathIndex = String(index);
      const title = card.querySelector('[data-panel-field="title"]');
      const content = card.querySelector('[data-panel-field="content"]');
      const included = card.querySelector('[data-panel-field="included"]');
      const body = card.querySelector(".llm-panel-body");
      const collapse = card.querySelector('[data-panel-action="collapse"]');
      const exa = card.querySelector('[data-panel-action="exa"]');
      title.value = panel.title || "";
      content.value = panel.content || "";
      included.checked = Boolean(panel.included);
      body.hidden = Boolean(panel.collapsed);
      collapse.textContent = panel.collapsed ? "展开" : "折叠";
      exa.hidden = !panel.exa_candidate;
      card.querySelector('[data-panel-action="delete"]').disabled = !panel.parent_id;
      const templateSelect = card.querySelector('[data-panel-field="template"]');
      for (const template of configuration.prompt_templates ?? []) {
        templateSelect.append(new Option(template.name, template.id));
      }
      templateSelect.addEventListener("change", () => insertTemplate(templateSelect, content));
      card.querySelector('[data-panel-action="save"]').addEventListener("click", () => savePanel(card, panel));
      collapse.addEventListener("click", () => setPanelCollapsed(panel, !panel.collapsed));
      card.querySelector('[data-panel-action="new-child"]').addEventListener("click", () => openPanelDialog(panel.id));
      card.querySelector('[data-panel-action="fork"]').addEventListener("click", () => openSessionDialog("fork", panel.id));
      card.querySelector('[data-panel-action="delete"]').addEventListener("click", () => deletePanel(panel));
      card.querySelector('[data-panel-action="knowledge"]').addEventListener("click", () => openKnowledgePicker(panel));
      card.querySelector('[data-panel-action="assets"]').addEventListener("click", () => chooseAssets(panel));
      exa.addEventListener("click", () => executeExa(panel));
      renderReferenceSummary(card.querySelector(".llm-panel-reference-summary"), panel);
      renderRevisions(card.querySelector(".llm-revision-list"), panel);
      card.querySelector(".llm-panel-technical").textContent = JSON.stringify({
        id: panel.id,
        parent_id: panel.parent_id || null,
        order: panel.order,
        result: panel.result || null,
        created_at: panel.created_at,
        updated_at: panel.updated_at,
      }, null, 2);
      ui.currentPath.append(card);
    }
  }

  function insertTemplate(select, textarea) {
    const template = (configuration.prompt_templates ?? []).find((item) => item.id === select.value);
    if (!template) return;
    const separator = textarea.value && !textarea.value.endsWith("\n") ? "\n\n" : "";
    textarea.value += separator + template.content;
    textarea.focus();
    select.value = "";
    setStatus("模板文本已插入草稿；不会自动保存或执行。");
  }

  function panelInput(panel, overrides = {}) {
    return {
      title: overrides.title ?? panel.title ?? "",
      content: overrides.content ?? panel.content ?? "",
      included: overrides.included ?? Boolean(panel.included),
      collapsed: overrides.collapsed ?? Boolean(panel.collapsed),
      knowledge_ids: overrides.knowledge_ids ?? panel.knowledge_ids ?? [],
      asset_ids: overrides.asset_ids ?? panel.asset_ids ?? [],
    };
  }

  async function savePanel(card, panel) {
    setStatus("正在保存 Panel…");
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels/${encodeURIComponent(panel.id)}`, jsonOptions("PUT", panelInput(panel, {
        title: card.querySelector('[data-panel-field="title"]').value,
        content: card.querySelector('[data-panel-field="content"]').value,
        included: card.querySelector('[data-panel-field="included"]').checked,
      })));
      await refreshWorkspace();
      setStatus("Panel 已保存；内容变化已进入修订记录。");
    } catch (error) {
      setStatus(`保存失败：${error.message}`, true);
    }
  }

  async function setPanelCollapsed(panel, collapsed) {
    const draft = readPanelDraft(panel);
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels/${encodeURIComponent(panel.id)}`, jsonOptions("PUT", panelInput(panel, { collapsed })));
      if (draft) Object.assign(panel, draft);
      panel.collapsed = collapsed;
      renderPanels();
    } catch (error) {
      setStatus(`折叠状态保存失败：${error.message}`, true);
    }
  }

  function openPanelDialog(parentID) {
    ui.panelDialogForm.reset();
    ui.panelParent.value = parentID;
    ui.panelDialogError.textContent = "";
    ui.panelDialog.showModal();
    ui.newPanelTitle.focus();
  }

  async function createPanel(event) {
    event.preventDefault();
    ui.panelDialogError.textContent = "";
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels`, jsonOptions("POST", {
        parent_id: ui.panelParent.value,
        title: ui.newPanelTitle.value,
        content: ui.newPanelContent.value,
        included: true,
        collapsed: false,
        knowledge_ids: [],
        asset_ids: [],
      }));
      ui.panelDialog.close();
      await refreshWorkspace();
      setStatus("子 Panel 已创建并成为当前分支终点。");
    } catch (error) {
      ui.panelDialogError.textContent = error.message;
    }
  }

  async function deletePanel(panel) {
    if (!panel.parent_id || !window.confirm(`删除“${panel.title || "未命名 Panel"}”及其全部后代？`)) return;
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels/${encodeURIComponent(panel.id)}`, { method: "DELETE" });
      await refreshWorkspace();
      setStatus("Panel 子树已删除。");
    } catch (error) {
      setStatus(`删除失败：${error.message}`, true);
    }
  }

  async function restoreRevision(panelID, revisionID) {
    if (!window.confirm("恢复这条修订？当前内容会先成为一条新的修订。")) return;
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels/${encodeURIComponent(panelID)}/restore/${encodeURIComponent(revisionID)}`, jsonOptions("POST"));
      await refreshWorkspace();
      setStatus("修订已恢复。");
    } catch (error) {
      setStatus(`恢复失败：${error.message}`, true);
    }
  }

  async function openKnowledgePicker(panel) {
    capturePanelDraft(panel);
    ui.knowledgePanel.value = panel.id;
    ui.knowledgeOptions.textContent = "正在读取知识条目…";
    ui.knowledgeDialog.showModal();
    try {
      const body = await request("/api/v1/knowledge");
      knowledgeNotes = body.notes ?? [];
      ui.knowledgeOptions.replaceChildren();
      if (!knowledgeNotes.length) ui.knowledgeOptions.textContent = "知识库为空。";
      for (const note of knowledgeNotes) {
        const label = document.createElement("label");
        label.className = "llm-knowledge-option";
        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.value = note.id;
        checkbox.checked = (panel.knowledge_ids ?? []).includes(note.id);
        const text = document.createElement("span");
        const title = document.createElement("strong");
        title.textContent = note.title;
        const folder = document.createElement("small");
        folder.textContent = note.folder || "未分类";
        text.append(title, folder);
        label.append(checkbox, text);
        ui.knowledgeOptions.append(label);
      }
    } catch (error) {
      ui.knowledgeOptions.textContent = `读取失败：${error.message}`;
    }
  }

  function applyKnowledgeSelection(event) {
    event.preventDefault();
    const panel = editablePanelByID(ui.knowledgePanel.value);
    if (!panel) return;
    panel.knowledge_ids = [...ui.knowledgeOptions.querySelectorAll('input[type="checkbox"]:checked')].map((input) => input.value);
    ui.knowledgeDialog.close();
    renderPanels();
    setStatus("知识引用已应用到草稿；点击“保存 Panel”后写入。");
  }

  async function chooseAssets(panel) {
    capturePanelDraft(panel);
    try {
      const existing = await Promise.all((panel.asset_ids ?? []).map(async (id) => {
        try {
          return await request(`/api/v1/assets/${encodeURIComponent(id)}`);
        } catch {
          return { id, state: "unknown" };
        }
      }));
      const selected = await openAssetPicker({ multiple: true, selected: panel.asset_ids ?? [] });
      if (selected === null) return;
      const retained = existing.filter((asset) => asset.state !== "active").map((asset) => asset.id);
      panel.asset_ids = [...new Set([...retained, ...selected.map((asset) => asset.id)])];
      renderPanels();
      setStatus("Asset 引用已应用到草稿；归档引用会保留，除非用“移除”明确删除。");
    } catch (error) {
      setStatus(`选择 Asset 失败：${error.message}`, true);
    }
  }

  async function executeExa(panel) {
    if (!panel.exa_candidate || !window.confirm("将这个 Panel 的严格 JSON 请求发送到已配置的 Exa？")) return;
    setStatus("正在执行 Exa 搜索…");
    try {
      await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/panels/${encodeURIComponent(panel.id)}/exa`, jsonOptions("POST"));
      await refreshWorkspace();
      setStatus("Exa 结果已作为子 Panel 写入。");
    } catch (error) {
      setStatus(`Exa 执行失败：${error.message}`, true);
    }
  }

  function providerEnabled(quickPath) {
    return Boolean((configuration.providers ?? []).find((provider) => provider.id === quickPath.provider_id)?.enabled);
  }

  function renderQuickPaths() {
    ui.quickPathBar.replaceChildren();
    const quickPaths = configuration.quick_paths ?? [];
    if (!quickPaths.length) {
      ui.quickPathBar.textContent = "尚未配置 Quick Path。";
      ui.executeSelected.disabled = true;
      return;
    }
    for (const quickPath of quickPaths) {
      const group = document.createElement("span");
      group.className = "llm-quick-path";
      const label = document.createElement("label");
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = selectedQuickPaths.has(quickPath.id);
      checkbox.disabled = !providerEnabled(quickPath);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) selectedQuickPaths.add(quickPath.id);
        else selectedQuickPaths.delete(quickPath.id);
        ui.executeSelected.disabled = !workspace || !selectedQuickPaths.size;
      });
      label.append(checkbox, document.createTextNode(quickPath.name));
      const send = document.createElement("button");
      send.type = "button";
      send.className = "text-button";
      send.textContent = "发送";
      send.disabled = !providerEnabled(quickPath);
      send.addEventListener("click", () => executeQuickPaths([quickPath.id]));
      group.append(label, send);
      ui.quickPathBar.append(group);
    }
    ui.executeSelected.disabled = !workspace || !selectedQuickPaths.size;
  }

  async function executeQuickPaths(quickPathIDs) {
    const panel = currentPanel();
    if (!panel || !quickPathIDs.length) return;
    setStatus("正在创建不可变请求快照…");
    try {
      const body = await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/execute`, jsonOptions("POST", {
        panel_id: panel.id,
        quick_path_ids: quickPathIDs,
      }));
      for (const run of body.runs ?? []) {
        runs.set(run.id, run);
        subscribeRun(run.id);
      }
      renderRuns();
      setStatus(`已启动 ${(body.runs ?? []).length} 个 Run。`);
    } catch (error) {
      setStatus(`执行失败：${error.message}`, true);
    }
  }

  function subscribeRun(runID) {
    eventSources.get(runID)?.close();
    const source = new EventSource(`/api/v1/llm/runs/${encodeURIComponent(runID)}/events`);
    eventSources.set(runID, source);
    for (const type of ["snapshot", "chunk", "state"]) {
      source.addEventListener(type, (event) => handleRunEvent(runID, event));
    }
    source.onerror = () => {
      const run = runs.get(runID);
      if (run && terminalRunStates.has(run.state)) return;
      setStatus("Run 事件连接已中断；刷新页面可读取最终结果。", true);
    };
  }

  async function handleRunEvent(runID, event) {
    let payload;
    try {
      payload = JSON.parse(event.data);
    } catch {
      return;
    }
    const existing = runs.get(runID);
    if (payload.run?.id) runs.set(runID, payload.run);
    else if (payload.chunk && existing) existing.output = (existing.output || "") + payload.chunk;
    renderRuns();
    const current = runs.get(runID);
    if (current && terminalRunStates.has(current.state)) {
      eventSources.get(runID)?.close();
      eventSources.delete(runID);
      if (active && current.session_id === selectedSessionID) {
        try {
          await refreshWorkspace();
          setStatus(current.state === "succeeded" ? "Run 已完成，结果已加入当前节点的分支。" : `Run 已结束：${current.state}`,
            current.state !== "succeeded" && current.state !== "cancelled");
        } catch (error) {
          setStatus(`Run 已结束，但刷新失败：${error.message}`, true);
        }
      }
    }
  }

  function renderRuns() {
    ui.runList.replaceChildren();
    const ordered = [...runs.values()].sort((left, right) => String(right.created_at).localeCompare(String(left.created_at)));
    for (const run of ordered) {
      const card = ui.runTemplate.content.firstElementChild.cloneNode(true);
      const quickPath = (configuration.quick_paths ?? []).find((item) => item.id === run.quick_path_id);
      card.querySelector(".llm-run-name").textContent = quickPath?.name || run.quick_path_id;
      card.querySelector(".llm-run-state").textContent = run.state;
      card.querySelector(".llm-run-state").dataset.state = run.state;
      card.querySelector(".llm-run-output").textContent = run.output || "等待输出…";
      card.querySelector(".llm-run-technical").textContent = JSON.stringify({
        id: run.id,
        status_code: run.status_code || null,
        result_panel_id: run.result_panel_id || null,
        error: run.error || null,
        snapshot: run.snapshot || null,
      }, null, 2);
      const cancel = card.querySelector('[data-run-action="cancel"]');
      cancel.disabled = terminalRunStates.has(run.state);
      cancel.addEventListener("click", () => cancelRun(run.id));
      ui.runList.append(card);
    }
  }

  async function cancelRun(runID) {
    try {
      const run = await request(`/api/v1/llm/runs/${encodeURIComponent(runID)}/cancel`, jsonOptions("POST"));
      runs.set(run.id, run);
      renderRuns();
    } catch (error) {
      setStatus(`取消失败：${error.message}`, true);
    }
  }

  function openSessionDialog(mode, panelID = "") {
    ui.sessionDialogForm.reset();
    ui.sessionDialogMode.value = mode;
    ui.sessionDialogError.textContent = "";
    if (mode === "fork") {
      const panel = editablePanelByID(panelID) ?? currentPanel();
      if (!panel) return;
      ui.sessionDialogTitle.textContent = "从当前节点派生会话";
      ui.sessionDialogName.value = `${workspace.session.title} - 分支`;
      ui.sessionDialogFolder.value = workspace.session.folder || "";
      ui.sessionDialogPanel.value = panel.id;
    } else {
      ui.sessionDialogTitle.textContent = "新建会话";
      ui.sessionDialogPanel.value = "";
    }
    ui.sessionDialog.showModal();
    ui.sessionDialogName.focus();
  }

  async function saveSessionDialog(event) {
    event.preventDefault();
    ui.sessionDialogError.textContent = "";
    try {
      const mode = ui.sessionDialogMode.value;
      const path = mode === "fork"
        ? `/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}/fork`
        : "/api/v1/llm/sessions";
      const body = {
        title: ui.sessionDialogName.value,
        folder: ui.sessionDialogFolder.value,
      };
      if (mode === "fork") body.panel_id = ui.sessionDialogPanel.value;
      workspace = await request(path, jsonOptions("POST", body));
      selectedSessionID = workspace.session.id;
      ui.sessionDialog.close();
      await loadSessions(selectedSessionID);
      renderWorkspace();
      setStatus(mode === "fork" ? "派生会话已创建，只复制了根到当前节点的路径。" : "会话已创建。");
    } catch (error) {
      ui.sessionDialogError.textContent = error.message;
    }
  }

  async function saveSession(event) {
    event.preventDefault();
    if (!workspace) return;
    setStatus("正在保存会话信息…");
    try {
      workspace = await request(`/api/v1/llm/sessions/${encodeURIComponent(selectedSessionID)}`, jsonOptions("PUT", {
        title: ui.sessionTitle.value,
        folder: ui.sessionFolder.value,
        current_panel_id: workspace.session.current_panel_id,
      }));
      await loadSessions(selectedSessionID);
      renderWorkspace();
      setStatus("标题与文件夹已保存。");
    } catch (error) {
      setStatus(`保存失败：${error.message}`, true);
    }
  }

  function closeEventSources() {
    for (const source of eventSources.values()) source.close();
    eventSources.clear();
  }

  async function enter() {
    active = true;
    const version = ++loadVersion;
    sidebarContent.replaceChildren(ui.sidebarControls);
    sidebarSearch.disabled = false;
    sidebarSearch.placeholder = "搜索会话标题或文件夹";
    try {
      await Promise.all([loadConfiguration(), loadSessions()]);
      if (!active || version !== loadVersion) return;
      for (const run of runs.values()) {
        if (!terminalRunStates.has(run.state)) subscribeRun(run.id);
      }
      if (selectedSessionID) await selectSession(selectedSessionID);
      else renderWorkspace();
    } catch (error) {
      if (active && version === loadVersion) setStatus(`初始化失败：${error.message}`, true);
    }
  }

  function leave() {
    active = false;
    loadVersion += 1;
    closeEventSources();
    sidebarSearch.disabled = true;
    sidebarSearch.placeholder = "搜索将在对应模块启用";
  }

  ui.newSession.addEventListener("click", () => openSessionDialog("new"));
  ui.sessionFork.addEventListener("click", () => openSessionDialog("fork"));
  ui.sessionForm.addEventListener("submit", saveSession);
  ui.sessionDialogForm.addEventListener("submit", saveSessionDialog);
  ui.panelDialogForm.addEventListener("submit", createPanel);
  ui.knowledgeForm.addEventListener("submit", applyKnowledgeSelection);
  ui.folderFilter.addEventListener("change", renderSessions);
  sidebarSearch.addEventListener("input", renderSessions);
  ui.executeSelected.addEventListener("click", () => executeQuickPaths([...selectedQuickPaths]));
  document.querySelectorAll("[data-llm-dialog-close]").forEach((button) => {
    button.addEventListener("click", () => document.querySelector(`#${button.dataset.llmDialogClose}`).close());
  });

  return { enter, leave };
}
