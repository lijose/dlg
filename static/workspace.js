// The workspace keeps configuration and durable progress visible; commands stay terminal-only.
let setup = { agents: [], project_keys: {}, global_settings: {} };
let settingsScope = "project";
let globalPrimaryChoices = [],
  globalReviewerChoices = {};
let agentDraft = {
  role: "primary",
  tool: "claude",
  name: "reviewer-1",
  model: "",
  scope: "global",
  method: "cli",
};
let updateState = null;

async function loadSetup() {
  setup = await api("GET", "/api/setup");
  $("projectLabel").textContent = setup.project;
}
const sourceLabel = (source) =>
  source === "global"
    ? "Global default"
    : source === "project"
      ? "Project override"
      : "Not configured";
const primaryModelLabel = () =>
  models.find((m) => m.key === modelChoice("analyse"))?.model ||
  models.find((m) => m.key === modelChoice("analyse"))?.label ||
  "Tool default";
function primarySummary() {
  return `<span class="model-picker-summary">Primary <b>${esc(state.settings.primary_name)} · ${esc(primaryModelLabel())}</b> · ${sourceLabel(setup.primary_source)}</span>`;
}
function renderWelcome() {
  const configured = setup.configured;
  return `<section class="welcome"><span class="badge approved">✓ Project initialized</span>
    <h2>${configured ? "Your decision workspace is ready." : "Your workspace is ready.<br>Connect your first agent."}</h2>
    <p class="meta">${configured ? "Describe a decision. Your primary agent investigates and proposes; you stay in control of the final choice." : "Choose a primary agent to investigate problems and propose solutions. Add independent reviewers whenever you need them."}</p>
    ${
      configured
        ? `<div class="profile-line">${primarySummary()}<p class="status-line">${setup.installed ? "CLI detected · login not checked" : "CLI not detected · check installation and PATH"}</p><button class="link" onclick="openSettings()">Manage agents →</button></div>${setup.standard_codex_analysis ? codexFolderOption() : ""}`
        : `
      <div class="setup-section"><h3>Choose a primary agent</h3><p class="meta">Installed tools are detected automatically. Configuration is approved in your terminal.</p>
      <div class="tool-options">${setup.agents.map((a) => `<button class="tool-option" onclick="openAgent('primary',${jsArg(a.name)})">${esc(a.name)}<span class="meta">${a.installed ? "CLI detected" : "CLI not detected"}</span></button>`).join("")}</div>
      <button class="link" onclick="openAgent('primary','other')">Use another CLI →</button></div>`
    }
    <div class="setup-section"><h3>${configured ? "Create your first problem" : "You can start writing now"}</h3>
      <label for="welcome-title">What do you want to decide?</label><input type="text" ${keep("welcome-title")} value="${draft("welcome-title")}" placeholder="For example: Choose a database for this project">
      <label for="welcome-context">Context <span class="meta">· optional</span></label><textarea ${keep("welcome-context")} placeholder="Constraints, alternatives, and what matters to you">${draft("welcome-context")}</textarea>
      <div class="row"><button class="primary" onclick="createWelcomeProblem(this)">Save problem →</button><span class="meta">Saved locally in ${esc(setup.project)}.</span></div></div>
  </section>`;
}
function createWelcomeProblem(btn) {
  const title = (drafts["welcome-title"] || "").trim();
  if (!title) {
    fieldError(btn, "Give your problem a title.");
    return;
  }
  act(btn, async () => {
    const p = await api("POST", "/api/problems", {
      title,
      context: drafts["welcome-context"] || "",
    });
    delete drafts["welcome-title"];
    delete drafts["welcome-context"];
    await openProblem(p.id);
  });
}
function reviewLabel(r, a) {
  if (!r) return "Waiting for reply";
  return r.id > (a?.id || 0)
    ? "Reply saved · awaiting analysis"
    : "Included in latest proposal";
}
function reviewerSummary(reviews, a, handoffs) {
  return `<aside class="workspace-side"><h3>Reviews</h3>${
    handoffs.length
      ? handoffs
          .map((h) => {
            const r = reviews.filter((r) => r.author === h.to).at(-1);
            return `<div class="review-summary"><b>${r ? "✓" : "○"} ${esc(h.to)}</b><p class="meta">${reviewLabel(r, a)}</p>${state.settings.reviewer_labels?.[h.to] ? `<p class="meta">${esc(state.settings.reviewer_labels[h.to])}</p>` : ""}${r ? `<div class="meta">${time(r.created_at)}</div><button class="link" onclick="viewReply(${r.id})">Read reply →</button>` : ""}${h.state === "changed" ? '<p class="meta">A new review request is ready.</p>' : ""}<div class="row"><button class="link" onclick="setTab('reviewers')">${r ? "Manage review" : "Send review request"} →</button></div></div>`;
          })
          .join("")
      : '<p class="empty-reviews">A primary agent is enough to get started. Add a reviewer for another perspective.</p>'
  }<button class="link" onclick="openAgent('reviewer')">+ Add reviewer</button></aside>`;
}
function viewReply(id) {
  const r = current.contributions.find((c) => c.id === id);
  if (!r) return;
  const a = current.contributions.filter((c) => c.kind === "analysis").at(-1),
    m = metaOf(r);
  $("replyContent").innerHTML =
    `<div class="dialog-title"><h2>${esc(r.author)}’s reply</h2><button onclick="$('replyDialog').close()" aria-label="Close reply">Close</button></div><span class="badge approved">✓ Saved</span><p class="status-line">${reviewLabel(r, a)} · ${time(r.created_at)}</p>${m.label ? `<p class="meta">${esc(m.label)} · user-selected chat</p>` : m.tool || m.model ? `<p class="meta">${esc(m.tool || "")} · ${esc(m.model || "Tool default")}</p>` : ""}<pre>${esc(r.body)}</pre>${det("reply-tech-" + id, "Technical details", runInfo(m) + (usageText(m) ? `<p class="meta">${usageText(m)}</p>` : ""), "technical")}`;
  $("replyDialog").showModal();
}
function questionAnswer(analysis, index) {
  const notes = current.contributions.filter((c) => c.kind === "context");
  return (
    notes
      .filter((c) => {
        const m = metaOf(c);
        return m.analysis_id === analysis.id && m.question_index === index;
      })
      .at(-1) ||
    notes
      .filter(
        (c) =>
          c.id > analysis.id &&
          c.body.startsWith(`Q: ${analysis.questions[index].question} A: `),
      )
      .at(-1)
  );
}
function renderQuestions(analysis) {
  const qs = analysis?.questions || [];
  if (!qs.length) return "";
  const unanswered = qs.filter((_, i) => !questionAnswer(analysis, i)).length;
  return `<div class="step"><h3>${unanswered ? `${unanswered} question${unanswered === 1 ? "" : "s"} need your input` : "Your answers are saved"}</h3>${qs
    .map((q, i) => {
      const saved = questionAnswer(analysis, i),
        key = `q_${analysis.id}_${i}`,
        edit = openKeys.has("answer_" + key);
      return `<div class="question-item"><div>${esc(q.question)}</div><div class="meta">Assumption: ${esc(q.assumption)}</div>${saved && !edit ? `<div class="answer-text">${esc(metaOf(saved).answer ?? saved.body.split(" A: ").slice(1).join(" A: "))}</div><span class="answer-saved">✓ Answer saved · ready for next analysis</span> <button class="link" onclick="openKeys.add(${jsArg("answer_" + key)});renderMain()">Edit</button>` : `<input type="text" ${keep(key)} value="${drafts[key] !== undefined ? draft(key) : esc(saved ? (metaOf(saved).answer ?? saved.body.split(" A: ").slice(1).join(" A: ")) : "")}" placeholder="Your answer">${answerPointChoices(analysis,key,saved)}<div class="row"><button onclick="saveQuestion(this,${i},false)">Save answer</button><button class="link" onclick="saveQuestion(this,${i},true)">Use assumption</button></div>`}</div>`;
    })
    .join("")}</div>`;
}
function saveQuestion(btn, index, useAssumption) {
  const a = current.contributions
      .filter((c) => c.kind === "analysis")
      .map(parse)
      .filter(Boolean)
      .at(-1),
    key = `q_${a.id}_${index}`;
  act(btn, async () => {
    await api("POST", `/api/problems/${current.problem.id}/answers`, {
      analysis_id: a.id,
      index,
      answer: drafts[key] ?? $(key)?.value ?? "",
      use_assumption: useAssumption,
      point_keys: answerPointKeys(key),
    });
    delete drafts[key];
    delete drafts["answer_points_" + key];
    openKeys.delete("answer_" + key);
    toast("Answer saved.");
  });
}
function savedAnswers() {
  const notes = current.contributions.filter(
    (c) => c.kind === "context" && metaOf(c).analysis_id,
  );
  const latest = new Map();
  notes.forEach((n) => {
    const m = metaOf(n);
    latest.set(`${m.analysis_id}:${m.question_index}`, n);
  });
  if (!latest.size) return "";
  return det(
    "saved-answers",
    `Your answers <span class="meta">· ${latest.size} saved</span>`,
    [...latest.values()]
      .map(
        (n) =>
          `<div class="question-item"><div class="prose">${esc(n.body)}</div><span class="answer-saved">✓ Saved · ${n.id < (current.contributions.filter((c) => c.kind === "analysis").at(-1)?.id || 0) ? "Included in latest proposal" : "Ready for next analysis"} · ${time(n.created_at)}</span></div>`,
      )
      .join(""),
  );
}
function openSettings(scope = "project") {
  settingsScope = scope;
  renderAgentSettings();
  $("settings").showModal();
  loadSettingsChoices();
}
function renderAgentSettingsContent() {
  const cfg =
    settingsScope === "global" ? setup.global_settings : state.settings;
  const inherited =
    settingsScope === "project" && !["primary_name", "primary_command", "implement_command", "models", "default_analyse_model", "default_implement_model", "model_list_command"].some(key => setup.project_keys[key]);
  const opts = (list, value) =>
    `<option value="">Use tool default</option>` +
    list
      .map(
        (m) =>
          `<option value="${esc(m.key)}" ${m.key === value ? "selected" : ""}>${esc(m.key.startsWith("codex:") ? m.label : m.model || m.label)}${m.context ? " · " + esc(m.context) : ""}${m.effort ? " · " + esc(m.effort) : ""}</option>`,
      )
      .join("");
  const primaryChoices =
    settingsScope === "project"
      ? models
      : globalPrimaryChoices.length
        ? globalPrimaryChoices
        : setup.agents.find((a) => a.name === clientTool(cfg.primary_command))
            ?.models || [];
  $("settingsContent").innerHTML =
    `<div class="dialog-title"><h2>Agents</h2><button onclick="$('settings').close()">Close</button></div>
    <div class="settings-scope"><select aria-label="Settings scope" onchange="settingsScope=this.value;renderAgentSettings();loadSettingsChoices()"><option value="project" ${settingsScope === "project" ? "selected" : ""}>This project</option><option value="global" ${settingsScope === "global" ? "selected" : ""}>Global defaults</option></select><span class="meta">${settingsScope === "global" ? "Inherited by projects without overrides." : "Project choices take precedence over global defaults."}</span></div>
    <div class="profile-line"><div class="row"><b>Primary · ${esc(cfg.primary_name)}</b><span style="flex:1"></span><button class="link" onclick="openAgent('primary',${jsArg(clientTool(cfg.primary_command))})">Change agent</button></div>
      <span class="agent-scope">${settingsScope === "global" ? (setup.global_configured ? "Global default" : "Not configured globally") : sourceLabel(setup.primary_source)} · ${esc(clientTool(cfg.primary_command) || "No tool")}</span>
      <div class="model-settings"><label for="settings-model">Model for analysis <span class="meta">· ${settingsScope === "global" ? "Global default" : setup.project_keys.default_analyse_model ? "Project override" : "Inherited / tool default"}</span></label><select id="settings-model" onchange="saveScopedModel('analyse',this.value)">${opts(primaryChoices, cfg.default_analyse_model)}</select>
      <p class="meta">${primaryChoices.some(m => m.key.startsWith("codex:")) ? "Choices from Codex and configured presets." : clientTool(cfg.primary_command) === "cursor" ? "Choices from the installed CLI where available." : "Built-in / terminal-configured presets · account access not checked."}</p>
      <details><summary class="meta">Implementation model</summary><select aria-label="Implementation model" onchange="saveScopedModel('implement',this.value)">${opts(primaryChoices, cfg.default_implement_model)}</select></details></div>
      ${settingsScope === "project" && setup.global_configured && !inherited ? `<button class="link" onclick="inheritSettings('primary')">Use global primary and models →</button>` : ""}
      ${settingsScope === "project" && setup.standard_codex_analysis ? codexFolderOption() : ""}</div>
    <h3>Reviewers <span class="meta">· optional</span></h3>
    ${
      (cfg.secondary_names || [])
        .map((name) => {
          const command = cfg.secondary_commands?.[name],
            r = (
              settingsScope === "global"
                ? globalReviewerChoices
                : reviewerModels
            )[name];
          return `<div class="profile-line"><div class="row"><b>${esc(name)}</b><span style="flex:1"></span><button class="link" onclick="removeScopedReviewer(${jsArg(name)})">Remove</button></div><span class="agent-scope">${command ? `${esc(clientTool(command))} · ${runnable(name) ? "Approved in this project" : "Awaiting project terminal approval"}` : `Copy & paste · ${esc(cfg.reviewer_labels?.[name] || "no CLI required")}`}</span>${command && r?.models?.length ? `<label>Reviewer model<select onchange="saveScopedReviewerModel(${jsArg(name)},this.value)">${opts(r.models, cfg.review_models?.[name])}</select></label>` : ""}${command && !runnable(name) ? commandBlock("dlg config approve-runners", "Approve inherited runners in this project") : ""}</div>`;
        })
        .join("") ||
      '<p class="meta">No reviewers configured. Start with one primary agent.</p>'
    }
    <div class="row"><button onclick="openAgent('reviewer')">+ Add reviewer</button>${settingsScope === "project" ? `<button class="link" onclick="inheritSettings('reviewers')">Use global reviewers</button>` : ""}</div>
    <p class="meta">The same tool and model can be used in several roles. Every run starts a separate process; custom commands must not resume an existing session.</p>
    <details class="technical"><summary>Advanced configuration</summary><p class="meta">Agent commands stay in your terminal. They can read project files and send content to the selected vendor. Implementation can write files.</p><pre>${esc(JSON.stringify({ primary: cfg.primary_command, implement: cfg.implement_command, reviewers: cfg.secondary_commands }, null, 2))}</pre><p class="meta">Custom model presets: <code>dlg config add-model</code>. Global configuration: <code>${esc(setup.global_path)}</code>.</p></details>`;
}
const clientTool = (command) => {
  const s = String(command || "").replaceAll("\\", "/");
  return s.includes("cursor-agent") || /^agent\s/.test(s)
    ? "cursor"
    : s.includes("claude")
      ? "claude"
      : s.includes("codex")
        ? "codex"
        : "other";
};
function commandBlock(command, label) {
  return `<div class="command-block"><div class="row"><span class="meta">${esc(label)}</span><span style="flex:1"></span><button class="link" onclick="copyText(${jsArg(command)},'Copied. Review and run in your project terminal.')">Copy command</button></div><pre>${esc(command)}</pre></div>`;
}
const quoteShellArg = (s) => "'" + s.replaceAll("'", "'\"'\"'") + "'";

function reviewerGitRecovery(job) {
  if (job.kind !== "review" || job.state !== "failed" || !job.agent ||
      !job.error?.includes("Not inside a trusted directory") ||
      !job.error.includes("--skip-git-repo-check")) return "";
  const runner = state.settings.secondary_commands?.[job.agent];
  // Replacing a custom runner could discard its options.
  if (!/^\s*codex\s+exec\s*$/.test(runner || "")) return "";

  const command = `dlg config set-runner ${quoteShellArg(job.agent)} 'codex exec --sandbox read-only --skip-git-repo-check'`;
  return `<div class="setup-notice"><p><b>${esc(job.agent)} needs plain-folder access.</b> Run the command in your project terminal and approve it. Then retry the reviewer.</p>
    ${commandBlock(command, "Enable read-only review in this project")}</div>`;
}
function codexFolderOption(expanded = false) {
  if (!setup.plain_folder && !setup.codex_plain_folder_enabled && !expanded) return "";
  const enabled = setup.codex_plain_folder_enabled;
  return `<details class="setup-notice" ${expanded && !enabled ? "open" : ""}><summary>${enabled ? "Plain-folder analysis enabled" : "Use this folder without Git"}</summary>
    <p class="meta">${enabled ? "Codex can analyse this folder using a read-only sandbox. To require Git again, run the command below." : `${setup.plain_folder ? "No Git repository was detected. " : ""}Approve read-only Codex analysis for this folder in your project terminal.${setup.plain_folder ? " If you want version control, run git init here instead." : ""}`}</p>
    ${commandBlock(`dlg config codex-folder ${enabled ? "git" : "plain"}`, "Run in your project terminal and approve when prompted")}
    <button onclick="checkCodexFolderSetup(this)">Check folder setup</button></details>`;
}
function checkCodexFolderSetup(btn) {
  act(btn, async () => {
    await loadSetup();
    if ($("settings").open) renderAgentSettings();
    if ($("agentDialog").open) renderAgentCommand();
  });
}
async function savePreferences(values) {
  if (
    settingsScope === "global" &&
    !(await ask(
      "Save global defaults? Inheriting projects use them on their next run. Active runs and project overrides keep their configuration.",
      "Save globally",
    ))
  ) {
    renderAgentSettings();
    return;
  }
  await api("PUT", "/api/agent-preferences", { scope: settingsScope, values });
  await refresh();
  await loadModels(false);
  await loadReviewerModels(false);
  renderAgentSettings();
  renderMain();
}
function saveScopedModel(stage, key) {
  act(null, () => savePreferences({ ["default_" + stage + "_model"]: key }));
}
function saveScopedReviewerModel(name, key) {
  const cfg =
    settingsScope === "global" ? setup.global_settings : state.settings;
  act(null, () =>
    savePreferences({ review_models: { ...cfg.review_models, [name]: key } }),
  );
}
function removeScopedReviewer(name) {
  const cfg =
    settingsScope === "global" ? setup.global_settings : state.settings;
  act(null, () =>
    savePreferences({
      secondary_names: (cfg.secondary_names || []).filter((n) => n !== name),
    }),
  );
}
async function inheritSettings(group) {
  if (
    !(await ask(
      `Use global ${group}? This removes this project's overrides for that group.`,
      "Use global defaults",
    ))
  )
    return;
  act(null, async () => {
    await api("POST", "/api/settings/inherit", { group });
    await refresh();
    await loadModels(false);
    await loadReviewerModels(false);
    renderAgentSettings();
  });
}
function openAgent(role = "reviewer", tool) {
  agentDraft = {
    role,
    tool: tool || setup.agents.find((a) => a.installed)?.name || "claude",
    name: `reviewer-${(state.settings.secondary_names || []).length + 1}`,
    model: "",
    scope: $("settings").open
      ? settingsScope
      : setup.global_configured
        ? "project"
        : "global",
    method: "cli",
  };
  renderAgentForm();
  if (!$("agentDialog").open) $("agentDialog").showModal();
}
function renderAgentFormContent() {
  const a = agentDraft,
    tool = setup.agents.find((t) => t.name === a.tool),
    available = tool?.models || [];
  $("agentForm").innerHTML =
    `<div class="dialog-title"><h2>${a.role === "primary" ? "Choose primary agent" : "Add a reviewer"}</h2><button onclick="$('agentDialog').close()">Close</button></div>
    <div class="form-grid"><label>Tool<select onchange="agentDraft.tool=this.value;agentDraft.model='';renderAgentForm()">${[...setup.agents.map((t) => ({ name: t.name, label: t.name + (t.installed ? " · detected" : " · not detected") })), { name: "other", label: "Other CLI / Grok chat" }].map((t) => `<option value="${t.name}" ${t.name === a.tool ? "selected" : ""}>${esc(t.label)}</option>`).join("")}</select></label>
    <label>Save for<select onchange="agentDraft.scope=this.value;renderAgentForm()"><option value="global" ${a.scope === "global" ? "selected" : ""}>Global defaults</option><option value="project" ${a.scope === "project" ? "selected" : ""}>This project</option></select></label>
    ${a.role === "reviewer" ? `<label>Reviewer identity<input type="text" value="${esc(a.name)}" oninput="agentDraft.name=this.value;renderAgentCommand()"></label><label>Connection<select onchange="agentDraft.method=this.value;agentDraft.model='';renderAgentForm()"><option value="cli" ${a.method === "cli" ? "selected" : ""}>CLI runner</option><option value="manual" ${a.method === "manual" ? "selected" : ""}>Copy & paste</option></select></label>` : ""}
    ${a.method === "manual" && a.role === "reviewer" ? `<label>Model selected in your chat<input type="text" value="${esc(a.model)}" oninput="agentDraft.model=this.value" placeholder="Optional model name"></label>` : `<label>Model<select onchange="agentDraft.model=this.value"><option value="">Use tool default</option>${available.map((m) => `<option value="${esc(m.key)}" ${a.model === m.key ? "selected" : ""}>${esc(m.model)} · ${esc(m.context)} · ${esc(m.effort)}</option>`).join("")}</select></label>`}</div>
    <p class="meta">${available.length ? "Built-in presets · account availability not checked." : a.tool === "cursor" ? "Configure the runner, then pick a model from its CLI list in Agents." : "Custom CLI models require terminal configuration. Grok chat can be added as a copy & paste reviewer."}</p>
    <div id="agentInstructions"></div><p id="agentFeedback" class="status-line" aria-live="polite"></p>`;
  renderAgentCommand();
}
function agentSetupCommand() {
  const a = agentDraft,
    prefix = "dlg config " + (a.scope === "global" ? "--global " : "");
  if (a.role === "primary")
    return a.tool === "other"
      ? prefix +
          "set-primary <name> '<read-only-command>' '<implementation-command>'"
      : prefix + "use " + a.tool;
  const tool = setup.agents.find((t) => t.name === a.tool);
  const command = tool
    ? prefix + "set-runner " + quoteShellArg(a.name) + " " + quoteShellArg(tool.primary)
    : prefix +
      "set-runner " +
      quoteShellArg(a.name) +
      " '<command that reads the request from stdin>'";
  return command + (a.scope === "global" ? "\ndlg config approve-runners" : "");
}
function renderAgentCommand() {
  const a = agentDraft;
  $("agentInstructions").innerHTML =
    a.role === "reviewer" && a.method === "manual"
      ? `<p class="meta">Copy requests to your chat app and paste replies into dlg. No command execution is configured.</p><button class="primary" onclick="finishManualReviewer(this)">Add reviewer</button>`
      : `${commandBlock(agentSetupCommand(), "Run in another terminal · leave dlg serve running")}<p class="meta">Read the settings dlg prints and type <code>y</code> at its approval prompt. ${a.scope === "global" ? "These defaults are reused across projects. Reviewer execution still requires project approval." : "These settings apply to this project."}</p><div class="row"><button class="primary" onclick="checkAgentSetup(this)">Check setup</button><span class="meta">CLI login remains with the agent tool.</span></div>${a.tool === "other" ? '<p class="warning-note">Replace placeholders using your CLI’s documented commands. dlg has no built-in Grok runner.</p>' : ""}`;
  if (a.role === "primary" && a.tool === "codex") {
    $("agentInstructions").insertAdjacentHTML("beforeend", codexFolderOption());
  }
}
async function finishManualReviewer(btn) {
  act(btn, async () => {
    const a = agentDraft,
      cfg = a.scope === "global" ? setup.global_settings : state.settings;
    if (!a.name.trim()) throw Error("Give this reviewer a distinct name.");
    if (a.name === cfg.primary_name)
      throw Error(
        "Use a distinct reviewer identity, such as reviewer-1. The tool and model can be the same.",
      );
    if ((cfg.secondary_names || []).includes(a.name))
      throw Error("That reviewer identity already exists.");
    await api("PUT", "/api/agent-preferences", {
      scope: a.scope,
      values: {
        secondary_names: [...(cfg.secondary_names || []), a.name],
        reviewer_labels: {
          ...cfg.reviewer_labels,
          [a.name]: a.tool + (a.model ? " · " + a.model : ""),
        },
      },
    });
    $("agentDialog").close();
    await refresh();
    if ($("settings").open) renderAgentSettings();
    toast("Reviewer added.");
  });
}
async function checkAgentSetup(btn) {
  act(btn, async () => {
    await refresh();
    const a = agentDraft,
      cfg = a.scope === "global" ? setup.global_settings : state.settings;
    if (a.role === "reviewer" && a.name === cfg.primary_name)
      throw Error("Use a distinct reviewer identity, such as reviewer-1.");
    const command =
      a.role === "primary"
        ? cfg.primary_command
        : cfg.secondary_commands?.[a.name];
    if (
      !command ||
      clientTool(command) !== a.tool ||
      (a.role === "primary" &&
        (a.scope === "global" ? !setup.global_configured : !setup.configured))
    )
      throw Error(
        "Configuration not found yet. Run the displayed command in your terminal, approve it, then check again.",
      );
    const values = {};
    if (a.role === "reviewer")
      values.secondary_names = [
        ...new Set([...(cfg.secondary_names || []), a.name]),
      ];
    if (a.model) {
      if (a.role === "primary") values.default_analyse_model = a.model;
      else values.review_models = { ...cfg.review_models, [a.name]: a.model };
    }
    if (Object.keys(values).length)
      await api("PUT", "/api/agent-preferences", { scope: a.scope, values });
    await loadModels(false);
    await loadReviewerModels(false);
    $("agentDialog").close();
    await refresh();
    if ($("settings").open) renderAgentSettings();
    toast("Agent configuration found. Login has not been checked.");
  });
}
async function openUpdates(force = false) {
  if (!$("updatesDialog").open) $("updatesDialog").showModal();
  $("updatesContent").innerHTML =
    '<p class="meta">Checking release information…</p>';
  try {
    updateState = await api(
      "GET",
      force ? "/api/updates?refresh=1" : "/api/about",
    );
    renderUpdates();
  } catch (e) {
    $("updatesContent").textContent = e.message;
  }
}
function renderUpdates() {
  const u = updateState;
  if (!u) return;
  $("updatesContent").innerHTML =
    `<div class="dialog-title"><h2>About & updates</h2><button onclick="$('updatesDialog').close()">Close</button></div><p>Installed: <b>${esc(u.current)}</b>${u.development ? " · Development build" : ""}</p><p class="meta about-meta">Executable: ${esc(u.executable)}</p>${u.latest ? `<p>Latest stable: <b>${esc(u.latest)}</b></p>` : ""}<p class="status-line">${u.error ? esc(u.error) : u.available ? "A newer stable release is available." : u.development ? "Development builds are not compared with release versions." : !u.latest ? "Check for the latest stable release." : "No newer compatible stable release found."}</p>${u.release_url ? `<a href="${esc(u.release_url)}" target="_blank" rel="noopener noreferrer">Release notes ↗</a>` : ""}<div class="row"><button onclick="openUpdates(true)">Check for updates</button><span class="meta">${u.checked_at && !u.checked_at.startsWith("0001") ? "Last checked " + time(u.checked_at) : ""}</span></div>${u.managed ? '<p class="meta">Use the package manager that installed dlg to update it.</p>' : u.commands ? commandBlock(u.commands, "Download, verify checksum, and install from your terminal") : ""}<p class="meta">Checks use the official lijose/dlg GitHub release, checked only when you click Check for updates. No project content is sent. dlg does not download or install updates from the browser.</p>${u.commands ? '<p class="warning-note">Checksums detect corruption; this release process does not provide signed publisher verification. Stop active runs before copying, then restart dlg serve.</p>' : ""}`;
}
// Drafts are local to this browser origin, ledger and problem; they are not ledger notes.
let restoredLedger = "", draftDurable = true;
function draftStorageKey(id = current?.problem.id || "welcome") {
  return "dlg-drafts:" + state.ledger + ":" + id;
}
function readDrafts(id) {
  const key = draftStorageKey(id);
  let raw;
  try {
    if (sessionStorage.getItem(key + ":fallback") === "1") { raw = sessionStorage.getItem(key); draftDurable = false; }
  } catch {}
  if (raw == null) try { raw = localStorage.getItem(key); } catch { draftDurable = false; }
  if (raw == null) { try { raw = sessionStorage.getItem(key); } catch {} }
  try {
    const saved = JSON.parse(raw || "{}");
    if (saved && typeof saved === "object" && !Array.isArray(saved))
      for (const [k,v] of Object.entries(saved))
        if (k !== "__proto__" && (typeof v === "string" || typeof v === "number")) drafts[k] = v;
  } catch {} // A damaged browser draft must not prevent opening the ledger.
}
function restoreProjectDrafts() {
  if (!state.ledger || restoredLedger === state.ledger) return;
  if (restoredLedger) for (const key of Object.keys(drafts)) delete drafts[key];
  restoredLedger = state.ledger;
  readDrafts();
}
function persistDrafts() {
  if (!state.ledger) return;
  const key = draftStorageKey(), body = JSON.stringify(drafts);
  try { localStorage.setItem(key, body); draftDurable = true; }
  catch { draftDurable = false; }
  // Keep the fallback current, including cleared drafts, so old session drafts cannot return.
  try { sessionStorage.setItem(key, body); sessionStorage.setItem(key + ":fallback", draftDurable ? "0" : "1"); } catch {}
  updateDraftStatus();
}
function switchProblemDrafts(id) {
  if (current?.problem.id === id) return;
  persistDrafts();
  for (const key of Object.keys(drafts)) delete drafts[key];
  readDrafts(id);
}
function updateDraft(id, value) {
  drafts[id] = value;
  persistDrafts();
}
function textDraftKeys() {
  return Object.keys(drafts).filter(k => !/^(model_|rmodel_|cycle_rounds$)/.test(k));
}
function updateDraftStatus() {
  const status = $("draft-status");
  if (status) {
    status.hidden = !textDraftKeys().length;
    status.textContent = draftDurable
      ? "Unsaved draft · kept in this browser. Save each field to add it to the ledger."
      : "Unsaved draft · browser storage unavailable. Keep this page open and save your changes.";
  }
  const recovery = $("browser-draft-list");
  if (recovery) recovery.innerHTML = renderDraftRecovery();
  document.querySelectorAll("[data-draft]").forEach(field => {
    const dirty = Object.hasOwn(drafts, field.id);
    field.classList.toggle("has-draft", dirty);
    field.setAttribute("aria-describedby", dirty ? "draft-status" : "");
  });
}
document.addEventListener("input", persistDrafts);
document.addEventListener("change", persistDrafts);
window.addEventListener("beforeunload", persistDrafts);

async function loadSettingsChoices() {
  if (settingsScope !== "global") return;
  try {
    if (setup.global_configured) {
      const r = await api("GET", "/api/agent-models?scope=global");
      globalPrimaryChoices = r.models || [];
    }
    await Promise.all(
      (setup.global_settings.secondary_names || []).map(async (name) => {
        if (!setup.global_settings.secondary_commands?.[name]) return;
        globalReviewerChoices[name] = await api(
          "GET",
          "/api/agent-models?scope=global&reviewer=" + encodeURIComponent(name),
        );
      }),
    );
    if ($("settings").open && settingsScope === "global") renderAgentSettings();
  } catch (e) {
    showError(e.message);
  }
}

// Keep the recommendation navigational when a decision or approval needs user review.
function recommendedAction({a, unanswered, newReplies, notesSince, allSettled, finalAlt, pending, waiting, unsettled}) {
  let label, action, hint;
  if (job?.state === "running") return '<p class="status-line" role="status">A run is in progress. You can continue reading and saving input.</p>';
  if (current.approval_change) {
    label = "Review changes since approval"; action = "viewApprovalChanges()";
    hint = "A newer final proposal differs from your approved solution. Implementation still uses the approved selection.";
  } else if (["approved", "sent"].includes(current.status)) {
    label = current.status === "sent" ? "Review implementation status" : "Review approved solution";
    action = "setTab('decision')";
    hint = current.status === "sent" ? "The approved solution has been sent. Check the run before sending again." : "Open Decisions to send the approved solution for implementation.";
  } else if (current.status === "deferred") {
    label = "Revisit decision"; action = "openDecisionChoice()"; hint = "This problem is deferred. Revisit it when you are ready.";
  } else if (!a) {
    label = setup.configured ? "Analyse problem" : "Connect primary agent";
    action = setup.configured ? "analyse(this)" : "openAgent('primary')";
    hint = "Start with a proposal from your primary agent.";
  } else if (unanswered) {
    label = `Answer ${unanswered} question${unanswered === 1 ? "" : "s"}`;
    const i = a.questions.findIndex((_, i) => !questionAnswer(a, i));
    action = `focusWorkspaceField(${JSON.stringify(`q_${a.id}_${i}`)})`;
    hint = "Save an answer or explicitly use the assumption for each question.";
  } else if (newReplies.length || notesSince.length) {
    label = "Update proposal"; action = "analyse(this)";
    hint = "Include saved answers, notes and reviewer replies in the next analysis.";
  } else if (current.status === "selected") {
    label = "Review selection for approval"; action = "setTab('decision')";
    hint = "Review your selected solution and approve it in Decisions.";
  } else if (allSettled && !finalAlt) {
    label = "Prepare final proposal"; action = `finalise(this, ${unsettled})`; hint = "The discussion points are settled. Prepare a proposal for your selection.";
  } else if (pending && !allSettled) {
    label = "Request review"; action = "setTab('reviewers')"; hint = `A review request is ready for ${pending.to}.`;
  } else if (waiting.length && !allSettled) {
    label = "Check reviewer replies"; action = "setTab('reviewers')"; hint = "Waiting for replies. Save them in Reviews when available.";
  } else if (unsettled) {
    label = "Review open points"; action = "setTab('decision')"; hint = `${unsettled} point${unsettled === 1 ? " needs" : "s need"} your decision.`;
  } else if (finalAlt || current.alternatives.length) {
    label = "Choose a solution"; action = "openDecisionChoice()"; hint = "Compare the alternatives and save your selection with a rationale.";
  } else {
    label = "Prepare final proposal"; action = "finalise(this, 0)"; hint = "Prepare a final proposal before selecting a solution.";
  }
  return `<div class="recommended"><p>${esc(hint)}</p><button id="recommended-action" class="primary" onclick="${esc(action)}">${esc(label)}</button></div>`;
}
function focusWorkspaceField(id) {
  if (!$(id)) setTab("overview");
  $(id)?.focus();
  $(id)?.scrollIntoView?.({block: "center", behavior: "smooth"});
}
function openDecisionChoice() {
  openKeys.add("pick"); setTab("decision"); focusWorkspaceField("sel_rationale");
}

// Compare structured proposal fields, never raw agent output or generated change claims.
const comparisonChoices = new Map();
function proposalFields(a) {
  const fields = new Map();
  const add = (key, label, value) => fields.set(key, {label, value: value || ""});
  add("summary", "Summary", a.summary);
  add("title", "Solution title", a.solution?.title);
  add("description", "Solution", a.solution?.description);
  add("evidence", "Evidence", (a.solution?.evidence || []).map(e => `${e.quote}\nSource: ${e.source}`).join("\n\n"));
  for (const p of a.points || []) add("point:" + p.key, `Point ${p.key}`, [p.topic, `Primary: ${p.primary}`, `Status: ${p.status}`, ...Object.entries(p.positions || {}).sort(([x],[y]) => x.localeCompare(y)).map(([agent, position]) => `${agent}: ${position}`)].join("\n"));
  add("questions", "Questions and assumptions", (a.questions || []).map(q => `${q.question}\nAssumption: ${q.assumption}`).join("\n\n"));
  add("open", "Open points", (a.open_points || []).join("\n"));
  add("alternatives", "Other options", (a.alternatives || []).map(o => `${o.title}\n${o.description}${(o.evidence || []).map(e => `\n${e.quote}\nSource: ${e.source}`).join("")}`).join("\n\n"));
  add("subproblems", "Suggested sub-problems", (a.subproblems || []).map(p => `${p.title}\n${p.context || ""}`).join("\n\n"));
  return fields;
}
function renderComparison(analyses) {
  if (analyses.length < 2) return '<p class="meta">Proposal comparison is available after a second analysis.</p>';
  let [beforeId, afterId] = comparisonChoices.get(current.problem.id) || [analyses.at(-2).id, analyses.at(-1).id];
  const before = analyses.find(a => a.id === beforeId) || analyses.at(-2);
  const after = analyses.find(a => a.id === afterId) || analyses.at(-1);
  const left = proposalFields(before), right = proposalFields(after);
  const keys = [...new Set([...left.keys(), ...right.keys()])];
  const changed = keys.filter(k => (left.get(k)?.value || "") !== (right.get(k)?.value || ""));
  const round = a => `Round ${analyses.indexOf(a) + 1}`;
  const options = selected => analyses.map(a => `<option value="${a.id}" ${a.id === selected ? "selected" : ""}>${round(a)} · ${time(a.at)}</option>`).join("");
  const row = k => {
    const l = left.get(k), r = right.get(k);
    const kind = !l?.value ? "Added" : !r?.value ? "Removed" : "Changed";
    return `<article class="comparison-field"><h3>${esc(r?.label || l.label)} <span class="badge">${kind}</span></h3><div class="comparison-columns"><div><div class="meta">${round(before)}</div><div class="prose">${esc(l?.value || "—")}</div></div><div><div class="meta">${round(after)}</div><div class="prose">${esc(r?.value || "—")}</div></div></div></article>`;
  };
  return det("proposal-comparison", "Compare proposals", `<p class="meta">Compare saved analyses. Only changed fields are shown; point statuses are as reported in each analysis.</p><div class="comparison-controls"><label for="compare-before">From<select id="compare-before" onchange="chooseComparison()">${options(before.id)}</select></label><label for="compare-after">To<select id="compare-after" onchange="chooseComparison()">${options(after.id)}</select></label></div><p role="status" class="status-line">${changed.length ? `${changed.length} changed field${changed.length === 1 ? "" : "s"}` : "No proposal differences."}</p>${changed.map(row).join("")}`);
}
function chooseComparison() {
  comparisonChoices.set(current.problem.id, [Number($("compare-before").value), Number($("compare-after").value)]);
  openKeys.add("proposal-comparison"); renderMain();
}

function workspaceTabKey(event) {
  if (event.altKey || event.ctrlKey || event.metaKey || !event.target.matches('[role="tab"]')) return;
  const tabs = ["overview", "reviewers", "decision", "history"], i = tabs.indexOf(tab);
  const target = {ArrowRight: tabs[(i + 1) % tabs.length], ArrowLeft: tabs[(i + tabs.length - 1) % tabs.length], Home: tabs[0], End: tabs.at(-1)}[event.key];
  if (!target) return;
  event.preventDefault(); setTab(target); $("tab-" + target)?.focus();
}
function captureWorkspaceFocus(root) {
  const el = document.activeElement;
  if (!el || !root?.contains(el)) return null;
  return {id: el.id, tag: el.tagName, handler: el.getAttribute("onclick"), change: el.getAttribute("onchange"), text: el.textContent,
    start: el.selectionStart, end: el.selectionEnd, direction: el.selectionDirection,
    scroll: root.scrollTop, problem: root.id === "main" ? root.dataset.problem : String(current?.problem.id || "")};
}
function restoreWorkspaceFocus(root, saved) {
  if (!saved || saved.problem !== String(current?.problem.id || "")) return;
  const match = saved.id ? $(saved.id) : [...root.querySelectorAll("button, summary, input, textarea, select")].find(el => el.tagName === saved.tag && (saved.change ? el.getAttribute("onchange") === saved.change : el.getAttribute("onclick") === saved.handler && el.textContent === saved.text));
  const target = match || $("tab-" + tab);
  if (!target || !root.contains(target)) return;
  target.focus({preventScroll: true});
  if (match && saved.start != null && typeof target.setSelectionRange === "function") {
    try { target.setSelectionRange(saved.start, saved.end, saved.direction); } catch {}
  }
  root.scrollTop = saved.scroll;
}
function labelWorkspaceFields(root) {
  const names = {p_title: "Problem title", p_context: "Problem context", sel_rationale: "Selection rationale (required)", appr_note: "Approval note (optional)"};
  root.querySelectorAll("input, textarea, select").forEach(field => {
    if (field.type === "hidden" || field.getAttribute("aria-label") || field.getAttribute("aria-labelledby") || field.closest("label")) return;
    if (field.id && [...root.querySelectorAll("label")].some(label => label.htmlFor === field.id || label.getAttribute("for") === field.id)) return;
    const question = field.closest(".question-item")?.querySelector("div")?.textContent;
    const label = names[field.id] || question || field.getAttribute("placeholder") || field.getAttribute("title") || field.name || (field.tagName === "SELECT" ? "Model choice" : "Value");
    field.setAttribute("aria-label", label);
  });
}
function renderMain() {
  const root = $("main"), saved = captureWorkspaceFocus(root);
  renderMainContent();
  root.dataset.problem = String(current?.problem.id || "");
  const host = root.querySelector(".problem, .welcome") || root;
  host.insertAdjacentHTML("beforeend", '<p id="draft-status" class="status-line" role="status" aria-live="polite" hidden></p>');
  host.insertAdjacentHTML("beforeend", '<div id="browser-draft-list"></div>');
  labelWorkspaceFields(root); updateDraftStatus(); restoreWorkspaceFocus(root, saved);
}

function renderAgentSettings() {
  const root = $("settingsContent"), saved = captureWorkspaceFocus(root);
  renderAgentSettingsContent(); labelWorkspaceFields(root); restoreWorkspaceFocus(root, saved);
}
function renderAgentForm() {
  const root = $("agentForm"), saved = captureWorkspaceFocus(root);
  renderAgentFormContent(); labelWorkspaceFields(root); restoreWorkspaceFocus(root, saved);
}
function draftLabel(key) {
  const names = {p_title: "Problem title", p_context: "Problem context", p_note: "Context note", sel_rationale: "Selection rationale", appr_note: "Approval note", sub_title: "Sub-problem title", new_reviewer: "Reviewer name", "welcome-title": "New problem title", "welcome-context": "New problem context", selected_alternative: "Alternative selection"};
  if (names[key]) return names[key];
  const question = key.match(/^q_(\d+)_(\d+)$/);
  if (question) {
    const analysis = current?.contributions.find(c => c.id === Number(question[1]) && c.kind === "analysis");
    return "Answer: " + (analysis && parse(analysis)?.questions?.[Number(question[2])]?.question || "Earlier question");
  }
  if (key.startsWith("answer_points_")) return "Points chosen for an answer";
  if (key.startsWith("link_answer_")) return "Saved answer chosen for " + key.slice(12);
  if (key.startsWith("rev_")) return "Reply from " + key.slice(4);
  if (key.startsWith("rule_")) return "Decision on " + key.slice(5);
  if (key.startsWith("a_")) return "Alternative edit";
  return "Form draft";
}
function renderDraftRecovery() {
  const keys = textDraftKeys();
  if (!keys.length) return "";
  return det("browser-drafts", `Browser drafts <span class="meta">(${keys.length})</span>`, '<p class="meta">These drafts are not submitted. Copy an earlier answer into a current field if it still applies. Clearing a draft leaves saved ledger records unchanged.</p>' + keys.map(key => `<div class="question-item"><b>${esc(draftLabel(key))}</b><div class="prose">${esc(draftDisplayValue(key))}</div><div class="row"><button onclick="copyText(draftDisplayValue(${jsArg(key)}), 'Draft copied')">Copy</button><button onclick="clearBrowserDraft(${jsArg(key)})">Clear draft…</button></div></div>`).join(""));
}
async function clearBrowserDraft(key) {
  if (!await ask("Clear this browser draft? Saved ledger records will stay unchanged.", "Clear draft")) return;
  delete drafts[key]; persistDrafts(); renderMain();
}

function latestSavedAnswers() {
  const byQuestion = new Map();
  for (const note of current.contributions.filter(c => c.kind === "context" && c.author === "user")) {
    const m = metaOf(note);
    if (m.analysis_id && typeof m.answer === "string" && m.answer.trim()) byQuestion.set(`${m.analysis_id}:${m.question_index}`, note);
  }
  return [...byQuestion.values()];
}
function linkedAnswerPoints(answerId) {
  return current.contributions.filter(c => c.kind === "context" && metaOf(c).linked_answer_id === answerId).map(c => metaOf(c).point_key);
}
function answerPointKeys(key) {
  try {
    const keys = JSON.parse(drafts["answer_points_" + key] || "[]");
    return Array.isArray(keys) ? keys.filter(k => typeof k === "string") : [];
  } catch { return []; }
}
function toggleAnswerPoint(key, point, checked) {
  const selected = new Set(answerPointKeys(key));
  checked ? selected.add(point) : selected.delete(point);
  updateDraft("answer_points_" + key, JSON.stringify([...selected]));
}
function answerPointChoices(analysis,key,saved) {
  if (!analysis.points?.length) return "";
  // Editing an answer offers its prior links, but never records a decision without a visible user choice.
  const linked = saved ? linkedAnswerPoints(saved.id) : [];
  const chosen = answerPointKeys(key);
  return det("answer-points-" + key, "Also record a point decision (optional)", `<p class="meta">Check only the points this answer resolves. Saving records your answer as a ruling on each checked point.${linked.length ? " Earlier rulings remain unless you explicitly replace them." : ""}</p>${analysis.points.map(p => `<label><input type="checkbox" ${chosen.includes(p.key) ? "checked" : ""} onchange="toggleAnswerPoint(${jsArg(key)},${jsArg(p.key)},this.checked)"> ${esc(p.key)} · ${esc(p.topic)}</label>`).join("")}`);
}
function savedAnswerRuling(point, analysis) {
  const answers = latestSavedAnswers();
  if (!answers.length) return "";
  const key = "link_answer_" + point.key;
  const selected = answers.find(n => n.id === Number(drafts[key]));
  return det("saved-answer-" + point.key, "Use a saved answer", `<label for="${esc(key)}">Answer to use for ${esc(point.key)}</label><select id="${esc(key)}" onchange="updateDraft(${jsArg(key)},this.value);renderMain()"><option value="">Choose a saved answer…</option>${answers.map(n => `<option value="${n.id}" ${selected?.id === n.id ? "selected" : ""}>${esc(n.body.length > 130 ? n.body.slice(0,130) + "…" : n.body)}</option>`).join("")}</select>${selected ? `<p class="prose">${esc(selected.body)}</p>` : ""}<p class="meta">This records the selected answer as your decision on ${esc(point.key)} · ${esc(point.topic)}.</p><button ${selected ? "" : "disabled"} onclick="linkSavedAnswer(this,${analysis.id},${jsArg(point.key)})">Use answer for ${esc(point.key)}</button>`);
}
function linkSavedAnswer(btn, analysisId, pointKey) {
  const key = "link_answer_" + pointKey, id = Number(drafts[key]);
  if (!id) return;
  act(btn, () => writeFields("POST", `/api/problems/${current.problem.id}/answer-rulings`, {analysis_id:analysisId,answer_id:id,point_keys:[pointKey]},key));
}

function draftDisplayValue(key) {
  if (key.startsWith("link_answer_")) return current?.contributions.find(c => c.id === Number(drafts[key]))?.body || "Previously selected answer is unavailable";
  if (key === "selected_alternative") return current?.alternatives.find(a => a.id === Number(drafts[key]))?.title || "Previously selected alternative is unavailable";
  if (key.startsWith("answer_points_")) return answerPointKeys(key.slice(14)).join(", ") || "No points selected";
  return String(drafts[key]);
}

// Agent proposals are untrusted Markdown. Raw HTML stays text and images never load.
const proposalMarkdown = markdownit({html:false, linkify:false, typographer:false});
proposalMarkdown.disable('image');
const defaultProposalLinkCheck = proposalMarkdown.validateLink;
proposalMarkdown.validateLink = href => defaultProposalLinkCheck(href) && /^(?:https?:|mailto:|#)/i.test(href);
proposalMarkdown.renderer.rules.link_open = (tokens, index, options, env, renderer) => {
  tokens[index].attrSet('target', '_blank');
  tokens[index].attrSet('rel', 'noopener noreferrer');
  return renderer.renderToken(tokens, index, options);
};
function proposalAgreement() {
  const a = current?.contributions.filter(c => c.kind === 'analysis').map(parse).filter(Boolean).at(-1);
  const points = a?.points || [];
  const ruled = new Set((current?.contributions || []).filter(c => c.kind === 'context').map(c => c.body.match(/^Ruling on \[([^\]]+)\]/)?.[1]).filter(Boolean));
  const count = points.filter(p => p.status === 'agreed' || ruled.has(p.key)).length;
  return `<span class="badge ${points.length && count === points.length ? 'approved' : ''}">${points.length ? `${count}/${points.length} points settled` : 'No board points'}</span>`;
}
function proposalExcerpt(body) {
  const tokens = proposalMarkdown.parse(body || '', {});
  const firstParagraph = tokens.findIndex(t => t.type === 'paragraph_open');
  const inline = firstParagraph >= 0 ? tokens[firstParagraph + 1] : tokens.find(t => t.type === 'inline');
  return inline?.type === 'inline' ? proposalMarkdown.renderInline(inline.content) : 'Open the full proposal to read its details.';
}
function finalProposalCard(alt) {
  return `<div class="final proposal-card"><div class="eyebrow">Final proposal</div><div class="row"><h3>${esc(alt.title)}</h3>${proposalAgreement()}</div><div class="markdown-body proposal-excerpt">${proposalExcerpt(alt.description)}</div><div class="row"><button id="proposal-read-${alt.id}" onclick="viewFinalProposal(${alt.id})">Read full proposal</button><button onclick="copyFinalProposal(${alt.id})">Copy Markdown</button></div></div>`;
}
let proposalSnapshot = null;
function viewFinalProposal(id) {
  const alt = current?.alternatives.find(a => a.id === id);
  if (!alt) return;
  proposalSnapshot = {id:alt.id, opener:document.activeElement?.id, problemId:current.problem.id, title:alt.title, description:alt.description || ''};
  $('proposalContent').innerHTML = `<header class="proposal-header"><div><div class="eyebrow">Final proposal</div><h2 id="proposalDialogTitle">${esc(alt.title)}</h2></div><button autofocus aria-label="Close proposal" onclick="$('proposalDialog').close()">Close</button></header><div class="row proposal-meta">${proposalAgreement()}<span class="meta">Current ledger status${alt.created_at ? ' · Proposal saved ' + time(alt.created_at) : ''}</span><button onclick="copyText(proposalSnapshot.description, 'Copied proposal Markdown')">Copy Markdown</button></div><article class="markdown-body proposal-document">${proposalMarkdown.render(proposalSnapshot.description)}</article>`;
  $('proposalDialog').showModal();
  $('proposalDialog').scrollTop = 0;
}
function copyFinalProposal(id) {
  const alt = current?.alternatives.find(a => a.id === id);
  if (alt) return copyText(alt.description || '', 'Copied proposal Markdown');
}
function returnProposalFocus() {
  if (current?.problem.id === proposalSnapshot?.problemId) ($(proposalSnapshot.opener) || $('proposal-read-' + proposalSnapshot.id) || $('tab-' + tab))?.focus({preventScroll:true});
  proposalSnapshot = null;
}

function approvalChangeNotice(d) {
  const change = d.approval_change;
  if (!change) return "";
  return `<aside class="approval-change" aria-label="Changes since approval"><h3>Proposal changed since approval</h3><p>Approved: <b>${esc(altTitle(d,change.approved_alternative_id))}</b><br>Newer proposal: <b>${esc(altTitle(d,change.latest_alternative_id))}</b></p><p>Implementation still uses the approved selection. Select and approve the newer proposal if you want to use it.</p><button id="approval-change-review" onclick="viewApprovalChanges()">Review changes</button></aside>`;
}
function viewApprovalChanges() {
  const change = current?.approval_change;
  if (!change) return;
  const approved = current.alternatives.find(a => a.id === change.approved_alternative_id);
  const latest = current.alternatives.find(a => a.id === change.latest_alternative_id);
  if (!approved || !latest) return;
  proposalSnapshot = {id:approved.id, problemId:current.problem.id, opener:document.activeElement?.id};
  const changed = [change.title_changed ? "Title changed" : "", change.description_changed ? "Description changed" : ""].filter(Boolean).join(" · ");
  const column = (alt,label) => `<section><div class="eyebrow">${label}</div><h3>${esc(alt.title)}</h3><article class="markdown-body">${proposalMarkdown.render(alt.description || "")}</article></section>`;
  $('proposalContent').innerHTML = `<header class="proposal-header"><div><div class="eyebrow">Approved and newer proposals</div><h2 id="proposalDialogTitle">Changes since approval</h2></div><button autofocus aria-label="Close proposal" onclick="$('proposalDialog').close()">Close</button></header><p class="status-line">${changed}</p><p>Implementation still uses the approved selection. To change it, select the newer proposal and approve it in Decisions.</p><div class="approval-comparison">${column(approved,"Approved selection")}${column(latest,"Newer final proposal")}</div><button onclick="$('proposalDialog').close();openDecisionChoice()">Review selection in Decisions</button>`;
  $('proposalDialog').showModal(); $('proposalDialog').scrollTop = 0;
}
