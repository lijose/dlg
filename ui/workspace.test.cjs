const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { parseHTML } = require("linkedom");
const root = path.resolve(__dirname, "..");
const html = fs.readFileSync(path.join(root, "static/index.html"), "utf8");
const { document } = parseHTML(html);
for (const dialog of document.querySelectorAll("dialog")) {
  dialog.showModal = function () {
    this.open = true;
  };
  dialog.close = function () {
    this.open = false;
  };
}
const makeStorage = () => {
  const values = new Map();
  return { getItem(key) { return values.get(key) ?? null; }, setItem(key, value) { values.set(key, String(value)); }, clear() { values.clear(); } };
};
const storage = makeStorage(), session = makeStorage();
const ctx = vm.createContext({
  document,
  console,
  URL,
  Map,
  Set,
  JSON,
  Date,
  Number,
  String,
  Object,
  Error,
  Promise,
  Array,
  localStorage: storage,
  sessionStorage: session,
  navigator: {},
  location: { origin: "http://localhost", hash: "", pathname: "/" },
  history: { pushState() {} },
  setTimeout() {
    return 1;
  },
  clearTimeout() {},
  window: { addEventListener() {} },
  fetch() {
    throw Error("Unexpected network request in rendering test");
  },
});
const run = (code) => vm.runInContext(code, ctx);
for (const script of html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)) {
  if (script[1].includes("startApp().catch")) continue;
  run(script[1]);
}
run(fs.readFileSync(path.join(root, "static/vendor/markdown-it.min.js"), "utf8"));
run(fs.readFileSync(path.join(root, "static/workspace.js"), "utf8"));
run(
  `setup={project:'example-project',configured:false,installed:false,primary_source:'not configured',project_keys:{},global_settings:{},agents:[{name:'claude',installed:true,primary:'claude -p',models:[]},{name:'codex',installed:true,primary:'codex exec',models:[]},{name:'cursor',installed:false,primary:'cursor-agent -p',models:[]}]};renderMain();`,
);
assert.match(
  document.getElementById("main").textContent,
  /Connect your first agent/,
);
run(`openAgent('primary','claude')`);
assert.match(
  document.getElementById("agentForm").textContent,
  /dlg config --global use claude/,
);
assert.match(document.getElementById("agentForm").textContent, /Check setup/);
run(
  `openAgent('reviewer','claude');agentDraft.name="reviewer-1";renderAgentCommand()`,
);
assert.match(
  document.getElementById("agentInstructions").textContent,
  /set-runner 'reviewer-1' 'claude -p'/,
);
run(`agentDraft.method='manual';renderAgentForm();`);
assert.match(
  document.getElementById("agentInstructions").textContent,
  /No command execution/,
);
const analysis = {
  summary: "Choose a binary installer.",
  solution: { title: "Guided setup", description: "A short guided flow." },
  questions: [{ question: "Which platforms?", assumption: "Linux" }],
  points: [
    { key: "P1", topic: "Distribution", primary: "Binary", status: "open" },
  ],
  changes: ["Made reviewers optional."],
};
const contribution = (id, kind, author, body, meta = {}) => ({
  id,
  kind,
  author,
  body: typeof body === "string" ? body : JSON.stringify(body),
  meta: JSON.stringify(meta),
  created_at: "2026-01-01T00:00:00Z",
});
const detail = {
  problem: {
    id: 1,
    title: "Improve installation",
    context: "Help first-time users.",
  },
  status: "open",
  ancestors: [],
  children: [],
  alternatives: [],
  disagreements: [],
  events: [],
  handoffs: [
    {
      to: "reviewer-1",
      state: "replied",
      mode: "full",
      text: "Review this proposal",
    },
  ],
  contributions: [
    contribution(1, "analysis", "claude", analysis),
    contribution(
      2,
      "review",
      "reviewer-1",
      "SUMMARY: Useful feedback\n[P1] AGREE: use binaries",
    ),
    contribution(
      3,
      "context",
      "user",
      "Q: Which platforms? A: Linux and macOS",
      { analysis_id: 1, question_index: 0, answer: "Linux and macOS" },
    ),
  ],
};
ctx.fixture = detail;
run(
  `state.settings={primary_name:'claude',primary_command:'claude -p',secondary_names:['reviewer-1'],secondary_commands:{},models:[]};state.problems=[];setup.configured=true;setup.primary_source='global';current=fixture;renderMain()`,
);
assert.match(
  document.getElementById("main").textContent,
  /Your answers are saved/,
);
assert.match(document.getElementById("main").textContent, /Linux and macOS/);
assert.match(
  document.getElementById("main").textContent,
  /Reply saved · awaiting analysis/,
);
assert.equal(document.querySelectorAll('[role="tab"]').length, 4);
run("viewReply(2)");
assert.match(
  document.getElementById("replyContent").textContent,
  /Useful feedback/,
);
run(`setTab('reviewers')`);
assert.match(document.getElementById("main").textContent, /View reply/);
run(`setTab('decision')`);
assert.match(document.getElementById("main").textContent, /Distribution/);
run(`setTab('history')`);
assert.match(
  document.getElementById("main").textContent,
  /Reply from reviewer-1/,
);
run(`openSettings()`);
assert.match(
  document.getElementById("settingsContent").textContent,
  /Global default/,
);
// Agent-supplied markup must remain text, including in the saved response panel.
run(
  `current.contributions[1].body='<img src=x onerror="alert(1)">';viewReply(2);`,
);
assert.equal(document.querySelector("#replyContent img"), null);
// Debug output stays collapsed while a job is running.
run(
  `job={id:'demo',kind:'review',state:'running',agent:'reviewer-1',elapsed_ms:100,model:'model-a',output:'private diagnostic output',problem_ids:[1]};setTab('overview')`,
);
const details = document.querySelector("#jobpanel details");
assert.ok(details);
assert.equal(details.hasAttribute("open"), false);
// Codex setup offers the project command and expands it after the Git-check error.
run(`setup.plain_folder=true;setup.standard_codex_analysis=true;openAgent('primary','codex')`);
assert.match(document.getElementById("agentForm").textContent, /dlg config codex-folder plain/);
run(`job={id:'git-check',kind:'analyse',state:'failed',error:'Not inside a trusted directory and --skip-git-repo-check was not specified.',problem_ids:[1]};renderMain()`);
const folderRecovery = document.querySelector("#main details.setup-notice[open]");
assert.ok(folderRecovery);
assert.match(folderRecovery.textContent, /dlg config codex-folder plain/);
run(`setup.codex_plain_folder_enabled=true;renderMain()`);
assert.match(document.getElementById("main").textContent, /Plain-folder analysis enabled/);
// The reviewer hint supplies its own runner command and preserves custom runners.
run(`state.settings.secondary_commands['reviewer-1']='codex exec';
  job={...job,kind:'review',agent:'reviewer-1',output:''};renderMain()`);
assert.equal(
  document.querySelector("#jobpanel .command-block pre").textContent,
  "dlg config set-runner 'reviewer-1' 'codex exec --sandbox read-only --skip-git-repo-check'",
);
assert.match(document.querySelector("#jobpanel .command-block button").textContent, /Copy command/);
run(`state.settings.secondary_commands['reviewer-1']='codex exec -m custom';renderMain()`);
assert.equal(document.querySelector("#jobpanel .command-block"), null);
run(`state.settings.secondary_commands['reviewer-1']='codex exec';job.error='Timed out';renderMain()`);
assert.equal(document.querySelector("#jobpanel .command-block"), null);
assert.match(document.querySelector("#jobpanel .error").textContent, /Timed out/);
// The grouped picker displays the Codex catalog label and sends a preset key.
assert.equal(run(`presetChoices([
  {key:'manual',model:'test-model',label:'Manual'},
  {key:'codex:test-model:medium',model:'test-model',label:'Test Model',effort:'medium'},
], 'codex:test-model:medium').models[0].label`), "Test Model");
assert.equal(run(`presetChoices([
  {key:'codex:test-model:medium',model:'test-model',label:'Test Model',effort:'medium'},
], '').pick('model', 'm:test-model')`), "codex:test-model:medium");
// Every generated inline event handler must remain syntactically valid.
for (const el of document.querySelectorAll("*"))
  for (const attr of el.attributes) {
    if (attr.name.startsWith("on")) new Function(attr.value);
  }
console.log(
  "UI checks passed: onboarding, setup commands, same-tool reviewer, manual reviewer, saved answers, reply viewing, four destinations, escaping, collapsed diagnostics.",
);

// Drafts survive refresh rendering, switching tabs, and unsuccessful saves.
run(`job=null;current.alternatives=[{id:5,title:'Option',description:'Details',author:'user',evidence:[]}];current.status='selected';setTab('decision');openKeys.add('pick');renderMain()`);
function typeField(id, value) {
  const field = document.getElementById(id);
  field.value = value;
  ctx.editedField = field;
  run(`(function(){ ${field.getAttribute('oninput')} }).call(editedField)`);
}
typeField('sel_rationale', 'Keep my rationale');
typeField('appr_note', 'Approval context');
run(`setTab('overview');setTab('decision')`);
assert.equal(document.getElementById('sel_rationale').value, 'Keep my rationale');
assert.equal(document.getElementById('appr_note').value, 'Approval context');
run(`setTab('history')`);
typeField('a_desc_5', 'Unsaved alternative');
run('renderMain()');
assert.equal(document.getElementById('a_desc_5').value, 'Unsaved alternative');
// The reset link must reflect any primary/model override, not just the command.
run(`setup.primary_source='global';setup.global_configured=true;setup.project_keys={default_analyse_model:true};openSettings()`);
assert.match(document.getElementById('settingsContent').textContent, /Use global primary and models/);
run(`setup.project_keys={};renderAgentSettings()`);
assert.doesNotMatch(document.getElementById('settingsContent').textContent, /Use global primary and models/);
// All agent types retain usage display; diagnostics start collapsed and forms stay accessible.
for (const agent of ['codex', 'claude', 'cursor']) {
  run(`current.cost={total:{runs:1,priced:${agent === 'claude' ? 1 : 0},cost_usd:0.25,input_tokens:100,output_tokens:20,cache_read_tokens:60,reasoning_output_tokens:5,turns:1,tool_calls:2,duration_ms:2000},by_agent:[],by_model:[]};setTab('history')`);
  const main = document.getElementById('main');
  assert.match(main.textContent, /100 input \/ 20 output tokens/);
  assert.match(main.textContent, /60 cached input/);
  assert.match(main.textContent, /5 reasoning output/);
  assert.match(main.textContent, agent === 'claude' ? /\$0\.25/ : /no cost reported/);
  const diagnostics = [...main.querySelectorAll('details')].find(d => d.querySelector('summary')?.textContent === 'Diagnostics');
  assert.ok(diagnostics);
  assert.equal(diagnostics.hasAttribute('open'), false);
  assert.match(diagnostics.textContent, /Audit trail/);
  assert.equal(diagnostics.querySelector('#a_desc_5'), null);
  assert.ok(document.getElementById('a_desc_5'));
}

// After auto-rounds, guidance is visible on Reviews, not only Overview.
run(`job=null;setTab('reviewers')`);
assert.ok(document.getElementById('recommended-action'));
run(`setTab('decision');openKeys.add('pt_P1');current.contributions[0].body=JSON.stringify({...parse(current.contributions[0]),points:[{key:'P1',topic:'Distribution',primary:'Binary',status:'decided',positions:{reviewer:'Agrees'}}]});renderMain()`);
assert.match(document.getElementById('main').textContent, /no point ruling is recorded/);
assert.match(document.getElementById('main').textContent, /Use a saved answer/);
run(`drafts.link_answer_P1='3';renderMain()`);
assert.match(document.getElementById('main').textContent, /Linux and macOS/);
assert.match(document.getElementById('main').textContent, /Use answer for P1/);
assert.match(run(`draftDisplayValue('link_answer_P1')`), /Linux and macOS/);
run(`drafts.answer_points_invalid='{}'`);
assert.equal(run(`answerPointKeys('invalid').length`),0);
run(`delete drafts.answer_points_invalid`);
run(`openKeys.add('answer_q_1_0');setTab('overview');toggleAnswerPoint('q_1_0','P1',true);renderMain()`);
assert.deepEqual(JSON.parse(run(`JSON.stringify(answerPointKeys('q_1_0'))`)), ['P1']);
assert.ok(document.querySelector('[onchange*=toggleAnswerPoint][checked]'));
run(`toggleAnswerPoint('q_1_0','P1',false);delete drafts.answer_points_q_1_0;delete drafts.link_answer_P1;current.contributions[0].body=JSON.stringify({...parse(current.contributions[0]),points:[{key:'P1',topic:'Distribution',primary:'Binary',status:'open'}]});setTab('history')`);
// Persistent drafts are isolated by ledger and problem and survive a new browser session.
run(`state.ledger='/test/ledger';restoreProjectDrafts();persistDrafts();updateDraft('p_note','Restart-safe note')`);
const storedKey = run(`draftStorageKey()`);
assert.equal(JSON.parse(storage.getItem(storedKey)).p_note, 'Restart-safe note');
session.clear();
run(`for(const k of Object.keys(drafts)) delete drafts[k];readDrafts();renderMain()`);
assert.equal(run(`drafts.p_note`), 'Restart-safe note');
assert.match(document.getElementById('draft-status').textContent, /kept in this browser/);
run(`switchProblemDrafts(99);current.problem.id=99`);
assert.equal(run(`drafts.p_note`), undefined);
run(`updateDraft('p_note','Second problem');switchProblemDrafts(1);current.problem.id=1`);
assert.equal(run(`drafts.p_note`), 'Restart-safe note');
run(`state.ledger='/different/ledger';restoreProjectDrafts()`);
assert.equal(run(`drafts.p_note`), undefined);
run(`state.ledger='/test/ledger';restoreProjectDrafts()`);
assert.equal(run(`drafts.p_note`), 'Restart-safe note');
// When durable storage is full, do not claim durability or restore an older local copy.
const storeItem = storage.setItem;
storage.setItem = () => { throw Error('quota exceeded'); };
run(`updateDraft('p_note','Newer fallback');for(const k of Object.keys(drafts)) delete drafts[k];readDrafts();renderMain()`);
assert.equal(run(`drafts.p_note`), 'Newer fallback');
assert.match(document.getElementById('draft-status').textContent, /storage unavailable/);
storage.setItem = storeItem;
run(`persistDrafts()`);
// Invalid storage cannot inject objects or prevent opening a problem.
storage.setItem(run(`draftStorageKey(77)`), '{broken');
run(`readDrafts(77)`);
assert.equal(run(`drafts.p_note`), 'Newer fallback');
// Recommended actions follow actual workflow state and never auto-approve or implement.
run(`const savedFixture = JSON.stringify(current); current.status='open';current.contributions=current.contributions.filter(c=>c.kind==='analysis');setTab('overview')`);
assert.match(document.getElementById('recommended-action').textContent, /Answer 1 question/);
assert.equal(document.querySelectorAll('.next button.primary').length, 1);
run(`current.contributions[0].body=JSON.stringify({...parse(current.contributions[0]),questions:[]});renderMain()`);
assert.match(document.getElementById('recommended-action').textContent, /Review open points/);
run(`current.handoffs=[{to:'reviewer',state:'unsent'}];renderMain()`);
assert.match(document.getElementById('recommended-action').textContent, /Request review/);
run(`current.handoffs[0].state='sent';renderMain()`);
assert.match(document.getElementById('recommended-action').textContent, /Check reviewer replies/);
for (const status of ['selected','approved','sent','deferred']) {
  run(`current.status='${status}';renderMain()`);
  assert.doesNotMatch(document.getElementById('recommended-action').getAttribute('onclick'), /approve\(|implement\(/);
}
run(`current.status='open';current.handoffs=[];current.contributions[0].body=JSON.stringify({...parse(current.contributions[0]),points:[]});current.alternatives=[];renderMain()`);
assert.match(document.getElementById('recommended-action').textContent, /Prepare final proposal/);
run(`job={id:'busy',kind:'analyse',state:'running',problem_ids:[1],output:''};renderMain()`);
assert.equal(document.getElementById('recommended-action'), null);
run(`job=null;current=JSON.parse(savedFixture)`);
// Structured comparison supports additions/removals, unchanged fields, arbitrary rounds and escaping.
ctx.nextAnalysis = contribution(4, 'analysis', 'claude', {...analysis, summary:'<img src=x onerror=alert(1)>', points:[{key:'P2', topic:'New point', primary:'New choice', status:'agreed'}]});
run(`current.contributions.push(nextAnalysis);setTab('history');openKeys.add('proposal-comparison');renderMain()`);
const compare = document.getElementById('compare-before').closest('details');
assert.match(compare.textContent, /Point P1.*Removed/s);
assert.match(compare.textContent, /Point P2.*Added/s);
assert.equal(compare.querySelector('img'), null);
assert.doesNotMatch(compare.textContent, /A short guided flow/); // unchanged solution is omitted
run(`comparisonChoices.set(1,[4,4]);renderMain()`);
assert.match(document.getElementById('compare-before').closest('details').textContent, /No proposal differences/);
// Tabs use roving focus and a named panel. Arrow keys and Home/End activate destinations.
let prevented = 0;
for (const [key, expected] of [['Home','overview'],['ArrowLeft','history'],['ArrowRight','overview'],['End','history']]) {
  ctx.tabEvent = {key, target:document.querySelector('[role="tab"][aria-selected="true"]'),preventDefault(){prevented++;}};
  run(`workspaceTabKey(tabEvent)`);
  assert.equal(run('tab'), expected);
  assert.equal(document.querySelectorAll('[role="tab"][tabindex="0"]').length, 1);
  assert.equal(document.getElementById('workspace-panel').getAttribute('aria-labelledby'), 'tab-'+expected);
}
assert.equal(prevented, 4);
// Focus identity and selection survive main renders (real-browser behavior checked separately).
run(`setTab('decision');openKeys.add('pick');renderMain()`);
const oldInput = document.getElementById('sel_rationale');
Object.defineProperty(document, 'activeElement', {configurable:true,value:oldInput});
oldInput.selectionStart=2;oldInput.selectionEnd=5;
assert.equal(run(`captureWorkspaceFocus($('main')).id`), 'sel_rationale');
assert.equal(run(`captureWorkspaceFocus($('main')).start`), 2);
run(`renderMain()`);
assert.notEqual(document.getElementById('sel_rationale'), oldInput);
assert.equal(document.getElementById('sel_rationale').getAttribute('aria-label'), 'Selection rationale (required)');
for (const dialog of document.querySelectorAll('dialog')) assert.ok(dialog.getAttribute('aria-label') || dialog.getAttribute('aria-labelledby'));
for (const el of document.querySelectorAll('*')) for (const attr of el.attributes) if (attr.name.startsWith('on')) new Function(attr.value);
run(`current=JSON.parse(savedFixture);drafts.sel_rationale='Keep my rationale';persistDrafts()`);
console.log('UI improvements passed: durable scoped drafts, storage failure, workflow guidance, structured comparison, keyboard tabs and accessible labels.');
// Final proposal Markdown is rendered locally; untrusted HTML and active URLs stay inert.
const proposalSource = 'Implement a local app in `todo_app.py`.\n\n### Approach\nKeep **tasks** in memory with *simple* controls.\n\n### Key steps\n1. Validate input.\n2. Keep duplicate titles distinct.\n\n```python\nprint("<script>not executable</script>")\n```\n\n> Temporary tasks only.\n\n| Field | Required |\n| --- | --- |\n| Title | Yes |\n\n[Documentation](https://example.com/docs)\n\n### Agreement status\nAll points are agreed.';
ctx.proposalSource = proposalSource;
run(`const beforeMarkdownFixture=JSON.stringify(current);current.alternatives.push({id:999,title:'Final <proposal>',description:proposalSource,author:'consensus',evidence:[]});setTab('decision')`);
assert.ok(document.querySelector('#proposal-read-999'));
assert.equal(document.querySelector('.proposal-excerpt code').textContent, 'todo_app.py');
assert.equal(document.querySelector('.proposal-card h3').textContent, 'Final <proposal>');
assert.equal(document.querySelector('.proposal-card .badge').textContent, '0/1 points settled');
assert.ok(document.querySelector('button[onclick^="approve("]')); // Existing approval action is intact.
run('viewFinalProposal(999)');
assert.equal(document.getElementById('proposalDialog').open, true);
const rendered = document.querySelector('.proposal-document');
assert.equal(rendered.querySelector('h3').textContent, 'Approach');
assert.equal(rendered.querySelectorAll('ol > li').length, 2);
assert.equal(rendered.querySelector('strong').textContent, 'tasks');
assert.equal(rendered.querySelector('pre code').textContent.trim(), 'print("<script>not executable</script>")');
assert.ok(rendered.querySelector('table'));
assert.ok(rendered.querySelector('blockquote'));
assert.equal(rendered.querySelector('a').getAttribute('rel'), 'noopener noreferrer');
assert.equal(document.getElementById('proposalDialogTitle').textContent, 'Final <proposal>');
const malicious = '<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n<svg onload=alert(1)>\n\n[bad](javascript:alert%281%29) [encoded](jav&#x61;script:alert%281%29) [data](data:text/html,test) [file](file:///etc/passwd) [relative](/api/implement)\n\n![remote](https://example.com/tracker.png)\n\n```html\n<iframe srcdoc="bad"></iframe>\n```';
ctx.malicious = malicious;
run(`current.alternatives.find(a=>a.id===999).description=malicious;$('proposalDialog').close();viewFinalProposal(999)`);
assert.equal(document.querySelectorAll('#proposalContent script, #proposalContent img, #proposalContent svg, #proposalContent iframe, #proposalContent style').length, 0);
for(const anchor of document.querySelectorAll('#proposalContent a')) assert.match(anchor.getAttribute('href'), /^(https?:|mailto:|#)/);
assert.match(document.querySelector('.proposal-document').textContent, /<script>alert\(1\)<\/script>/);
// Copy uses the exact Markdown, and the dialog keeps its displayed snapshot across refreshes.
run(`current.alternatives.find(a=>a.id===999).description=proposalSource;$('proposalDialog').close();viewFinalProposal(999);const savedCopyText=copyText;let copiedProposal='';copyText=text=>{copiedProposal=text};copyFinalProposal(999)`);
assert.equal(run('copiedProposal'), proposalSource);
run(`current.alternatives.find(a=>a.id===999).description='new revision';renderMain()`);
assert.equal(run('proposalSnapshot.description'), proposalSource);
run(`copyText(proposalSnapshot.description)`);
assert.equal(run('copiedProposal'), proposalSource);
run(`copyText=savedCopyText;$('proposalDialog').close();returnProposalFocus();current=JSON.parse(beforeMarkdownFixture);renderMain()`);
assert.equal(run('proposalSnapshot'), null);
const vendorPackage = require('markdown-it/package.json');
const vendorExpected = `/*! markdown-it ${vendorPackage.version}; licenses: markdown-it.LICENSE.txt */\n` + fs.readFileSync(require.resolve('markdown-it/browser'),'utf8').replace(/^\/\/# sourceMappingURL=.*$/gm,'');
assert.equal(fs.readFileSync(path.join(root,'static/vendor/markdown-it.min.js'),'utf8'),vendorExpected);
assert.match(fs.readFileSync(path.join(root,'static/vendor/markdown-it.LICENSE.txt'),'utf8'), /Permission is hereby granted/);
console.log('Markdown preview passed: formatting, safe HTML/links, ledger status, original copy, stable dialog snapshot, and bundled asset.');


// Approval drift comes from server data, not generated prose, and never selects or approves.
run(`const beforeDriftFixture=JSON.stringify(current);current.status='approved';current.selection={id:7,alternative_id:5,rationale:'Keep original'};current.approval={id:8,selection_id:7,created_at:'2026-11-08T00:00:00Z'};current.alternatives.push({id:10,title:'New <proposal>',description:'### Revised scope\\nStore tasks permanently.',author:'consensus',evidence:[]});current.approval_change={approved_alternative_id:5,latest_alternative_id:10,title_changed:true,description_changed:true};setTab('decision')`);
assert.match(document.getElementById('main').textContent,/Proposal changed since approval/);
assert.ok(document.getElementById('approval-change-review'));
run(`setTab('overview')`);
assert.equal(document.getElementById('recommended-action').textContent,'Review changes since approval');
run(`viewApprovalChanges()`);
assert.match(document.getElementById('proposalContent').textContent,/Approved selection/);
assert.match(document.getElementById('proposalContent').textContent,/Newer final proposal/);
assert.equal(document.querySelectorAll('#proposalContent proposal').length,0);
assert.equal(run(`current.selection.alternative_id`),5);
assert.equal(run(`current.status`),'approved');
run(`$('proposalDialog').close();returnProposalFocus();current=JSON.parse(beforeDriftFixture);renderMain()`);
console.log('Approval change UI passed: guidance, comparison, escaping and unchanged selection.');
(async () => {
  run(`api = async () => { throw Error('offline'); }`);
  await assert.rejects(run(`writeFields('POST','/fixture',{},'sel_rationale')`), /offline/);
  assert.equal(run(`drafts.sel_rationale`), 'Keep my rationale');
  run(`api = async () => ({id:1})`);
  await run(`writeFields('POST','/fixture',{},'sel_rationale')`);
  assert.equal(run(`drafts.sel_rationale`), undefined);
  assert.equal(JSON.parse(storage.getItem(run('draftStorageKey()'))).sel_rationale, undefined);
  run(`const savedAsk=ask,savedAct=act;let confirmationText='',implementationPosted=false;ask=async text=>{confirmationText=text;return false};act=()=>{implementationPosted=true};api=async(method,path)=>({problem:{title:'Changed problem'},alternatives:[{id:5,title:'Approved original'}],approval_change:{approved_alternative_id:5}})`);
  await run(`implement(null,[1,2])`);
  assert.match(run('confirmationText'),/Changes since approval/);
  assert.match(run('confirmationText'),/Approved original/);
  assert.equal(run('implementationPosted'),false);
  run(`api=async()=>({problem:{title:'Unchanged'},alternatives:[]})`);
  await run(`implement(null,[1])`);
  assert.doesNotMatch(run('confirmationText'),/Changes since approval/);
  run(`ask=savedAsk;act=savedAct`);
  console.log('UI regressions passed: drafts, scoped reset, all-agent usage, and collapsed diagnostics.');
})().catch(err => { console.error(err); process.exitCode = 1; });
assert.match(run(`usageSummary({runs:1,token_runs:0,priced:0,duration_ms:20})`), /Tokens not reported/);
assert.match(run(`usageSummary({runs:1,token_runs:1,input_tokens:0,output_tokens:0,priced:0,duration_ms:20})`), /0 input \/ 0 output tokens/);
