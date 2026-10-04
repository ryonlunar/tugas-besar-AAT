const $ = (selector) => document.querySelector(selector);
const h = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const state = {
  page: "overview",
  bootstrap: null,
  spec: {},
  containers: [],
  statusError: "",
  run: null,
  active: "",
  tab: "execution",
  follow: true,
  inspections: {},
  refs: {},
  baseline: null,
};
const names = {
  overview: "Overview",
  p1: "Mapping",
  p2: "Resilience",
  p3: "Authentication",
  p4: "Independence",
  p5: "Events",
  inspect: "Inspect",
  evidence: "Evidence",
};
const goals = {
  p1: "See how two different source formats become HazardEvent, and how a live schema change survives ingestion.",
  p2: "Show that slow or unavailable PVMBG does not prevent BMKG reads. Measure the sustained workload separately.",
  p3: "Show separate trust domains, server-enforced permissions, natural token expiry and refresh.",
  p4: "Rebuild one component while the others continue, and inspect dynamic attributes without a new migration.",
  p5: "Watch independent consumers receive the same stream, recover after downtime, and add another subscriber.",
};
let toastTimer,
  logPending = false,
  runPending = false,
  statusPending = false;
const inspectionSequence = {};
function reveal(selector) {
  const el = $(selector);
  if (el)
    el.scrollIntoView({
      block: "start",
      behavior: matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "instant"
        : "smooth",
    });
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body
        ? {
            "Content-Type": "application/json",
            "X-Console-Token": state.bootstrap.token,
          }
        : {}),
      ...options.headers,
    },
  });
  const text = await response.text();
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    value = { error: text };
  }
  if (!response.ok) throw new Error(value.error || `HTTP ${response.status}`);
  return value;
}
function toast(message) {
  $("#toast").textContent = message;
  $("#toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => ($("#toast").hidden = true), 6500);
}
const badge = (status, label = status) =>
  `<span class="badge ${["passed", "failed", "cancelled", "healthy", "unhealthy", "starting", "running", "exited", "good"].includes(status) ? status : "neutral"}">${h(label)}</span>`;
const actionButton = (label, id, parameters = {}, primary = false) =>
  `<button class="button ${primary ? "primary" : ""}" data-action="${h(id)}" data-parameters="${h(JSON.stringify(parameters))}">${h(label)}</button>`;
const inspectButton = (label, kind, params = {}, extra = "") =>
  `<button class="button" data-inspect="${h(kind)}" data-query="${h(JSON.stringify(params))}" ${extra}>${h(label)}</button>`;
function step(number, title, description, controls, expected) {
  return `<div class="step"><span class="step-number">${number}</span><div><h3>${h(title)}</h3><p>${h(description)}</p><div class="actions">${controls}</div><p class="expected">Look for: ${h(expected)}</p></div></div>`;
}
function trial(id, label) {
  const a = state.bootstrap.actions.find((a) => a.id === id);
  return `<section class="card trial"><div><h2>${h(label || a.title)}</h2><p>${h(a.impact)}</p></div>${actionButton("Run trial", id, {}, true)}</section>`;
}
function specification() {
  const s = state.spec[state.page];
  return s
    ? `<details class="spec"><summary>Original specification · pages ${s.page}–${s.page + 1}</summary><div class="spec-body"><div class="spec-links"><a href="/api/spec/m1#page=${s.page}" target="_blank" rel="noopener">Open M1 PDF ↗</a><a href="/api/spec/central" target="_blank" rel="noopener">Central document ↗</a></div><pre class="spec-text">${h(s.text)}</pre></div></details>`
    : "";
}
function heading(kicker, title, description) {
  return `<div class="page-heading"><span class="kicker">${h(kicker)}</span><h1>${h(title)}</h1><p>${h(description)}</p></div>`;
}
const resultPanel = () =>
  `<section class="card" id="inspection-panel"><div class="card-heading"><h2>Observed result</h2><span class="subtle" id="inspection-time">Fetch a sample to begin</span></div><div id="inspection-result" class="result-empty">Results appear here. The console keeps the execution output.</div></section>`;
const runPanel = () =>
  `<section class="card" id="run-panel" hidden><div class="result-heading"><h2 id="run-title">Latest run</h2><span id="run-badge"></span></div><div id="run-summary" class="run-summary"></div><div id="run-error"></div><ul id="run-checks" class="check-list"></ul><div id="load-result"></div><div id="run-downloads" class="actions"></div></section>`;

function overview() {
  return (
    heading(
      "WORKSPACE",
      "System overview",
      "Prepare the stack, check its services, then choose a problem to demonstrate.",
    ) +
    `<div class="stats"><div class="stat"><div class="stat-label">Healthy services</div><div class="stat-value" id="healthy-count">—</div><div class="stat-note">Nine default services; third consumer optional</div></div><div class="stat"><div class="stat-label">Selected project</div><div class="stat-value mono">${h(state.bootstrap.project)}</div><div class="stat-note">All actions target this Compose project</div></div><div class="stat"><div class="stat-label">Run state</div><div class="stat-value" id="overview-run-state">Idle</div><div class="stat-note">One action at a time</div></div></div>` +
    `<section class="card"><div class="card-heading"><h2>Prepare & start</h2><span class="subtle">Existing keys and data are retained</span></div><p class="note">Prepare keys once, then build and start the stack. On later launches, Start stack reuses the current images.</p><div class="actions">${actionButton("Prepare JWT keys", "keys-setup")}${actionButton("Build and start", "stack-build", {}, true)}${actionButton("Start stack", "stack-start")}</div></section>` +
    `<section class="card"><div class="card-heading"><h2>Services</h2><span id="status-time" class="subtle"></span></div><div id="service-table"></div></section>` +
    `<section class="card"><div class="card-heading"><h2>How the system connects</h2><span class="subtle">HTTP + NATS</span></div><div class="diagram"><div class="diagram-row"><span class="node">BMKG</span><span class="node">PVMBG</span><span class="arrow">→</span><span class="node core">Aggregator</span><span class="arrow">→</span><span class="node">PostgreSQL</span></div><div class="diagram-row"><span class="node">Client API</span><span class="arrow">→</span><span class="node core">Aggregator reads</span></div><div class="diagram-row"><span class="node core">Aggregator outbox</span><span class="arrow">→</span><span class="node">NATS JetStream</span></div><div class="diagram-row"><span class="node">Auth tokens → Client API</span><span class="node">NATS → Dashboard / Notifier</span></div></div><p class="note">The Aggregator owns canonical storage. Client reads use its API; each consumer keeps its own durable position and private store.</p></section>` +
    runPanel() +
    `<p class="note mono">${h(state.bootstrap.repo)}</p>`
  );
}

function problemPage(page) {
  const sourceSelect = `<label class="field">Source<select id="mapping-source"><option value="PVMBG">PVMBG</option><option value="BMKG">BMKG</option></select></label>`;
  let content = "";
  if (page === "p1")
    content =
      `<section class="card"><h2>Walk through the change</h2>${step(1, "Read a mapped sample", "The source and canonical record use the same source reference. The selection stays pinned while you change the schema.", `<div class="field-row">${sourceSelect}<button class="button" id="mapping-read">Read sample</button><button class="button" id="mapping-new">Choose latest sample</button></div>`, "Source fields map to the canonical fields; remaining attributes are preserved.")}${step(2, "Change the live schema", "Keep the same report selected. Wait for the next polling cycle, then read the sample again.", actionButton("Schema v1", "source-schema", { version: "1" }) + actionButton("Schema v2", "source-schema", { version: "2" }), "confidence_level appears in attributes after v2, with the same hazard identity.")}${step(3, "Read stored history", "Use the pinned report to inspect revisions and the migration ledger.", `<button class="button" id="pinned-history">Read revisions 1 & 2</button>`, "Both versions remain readable and the migration ledger does not change.")}</section>` +
      trial("smoke-trial", "Verify mapping, outage & access");
  if (page === "p2")
    content =
      `<section class="card"><h2>Failure & recovery</h2>${step(1, "Read both sources", "Read the cached API response before changing PVMBG availability.", inspectButton("Read BMKG", "api", { source: "BMKG" }) + inspectButton("Read PVMBG", "api", { source: "PVMBG" }), "Data and source freshness metadata are available.")}${step(2, "Make PVMBG unavailable", "Enable outage, allow a polling attempt to fail, then read both sources again.", actionButton("Enable outage", "source-outage", { enabled: "true" }) + inspectButton("Read BMKG", "api", { source: "BMKG" }) + inspectButton("Read PVMBG", "api", { source: "PVMBG" }), "BMKG still responds; PVMBG is stale or explicitly unavailable, without a 500.")}${step(3, "Recover without a restart", "Disable outage and allow the next poll to complete.", actionButton("Disable outage", "source-outage", { enabled: "false" }) + inspectButton("Read PVMBG", "api", { source: "PVMBG" }), "Freshness returns to normal without recreating the services.")}</section><section class="card"><h2>Slow source</h2><p class="note">The load trial sets a fixed 3-second PVMBG delay and captures TCP connections alongside the 90-second workload. Setup and TCP capture add time to the run.</p><div class="actions">${actionButton("Set 3-second delay", "source-delay", { preset: "slow" })}${actionButton("Restore configured delay", "source-delay", { preset: "default" })}</div></section>` +
      trial("load-trial", "Measure sustained concurrency");
  if (page === "p3")
    content =
      `<section class="card"><h2>Check the trust boundaries</h2>${step(1, "Compare client access", "Make real API requests as Media, Field Team and BNPB. Also test full-view and raw-field requests as Media.", actionButton("Check client permissions", "access-check", {}, true), "Media receives summary fields and raw requests are rejected by the server.")}${step(2, "Inspect an API response", "Choose the identity and source, then inspect the returned fields.", `<div class="field-row"><label class="field">Identity<select id="api-identity"><option value="media">Media</option><option value="field-team">Field Team</option><option value="bnpb-internal">BNPB internal</option></select></label><label class="field">Source<select id="api-source"><option>BMKG</option><option>PVMBG</option></select></label><button class="button" id="identity-read">Read API</button></div>`, "Different identities receive the permitted projections of the same data.")}</section>` +
      trial("token-trial", "Natural expiry & refresh");
  if (page === "p4")
    content =
      `<section class="card"><h2>Deploy one component</h2>${step(1, "Capture container identities", "Check IDs, start times and restart counts before rebuilding.", `<button class="button" id="show-containers">Read container state</button>`, "Every component has a separate container.")}${step(2, "Rebuild only the dashboard consumer", "The action captures before/after container states and verifies that only dashboard changed.", actionButton("Rebuild dashboard", "dashboard-rebuild", {}, true), "Other container IDs and start times are unchanged.")}${step(3, "Inspect dynamic storage", "The audit command runs inside the Aggregator, using a read-only transaction.", inspectButton("Inspect store & migrations", "store") + inspectButton("HazardEvent schema", "schema", { name: "hazard-event" }), "attributes is JSONB; stored versions and applied migrations are visible.")}</section>` +
      trial("events-trial", "Verify rebuild with live API reads");
  if (page === "p5")
    content =
      `<section class="card"><h2>Consumer downtime & catch-up</h2>${step(1, "Capture a checkpoint", "Read stream sequence, consumer ACK positions and inbox counts.", `<button class="button" id="capture-checkpoint">Capture checkpoint</button>`, "Two independent consumers subscribe to the same stream.")}${step(2, "Stop dashboard and observe progress", "Wait for new source events, then read the positions again.", actionButton("Stop dashboard", "dashboard-stop") + inspectButton("Read event positions", "events"), "The stream and notifier advance while dashboard is stopped.")}${step(3, "Start dashboard and inspect catch-up", "The durable consumer resumes its existing position.", actionButton("Start dashboard", "dashboard-start") + inspectButton("Read event positions", "events"), "Dashboard catches up to events published during its downtime.")}${step(4, "Add another subscriber", "The third consumer uses its own durable and storage. Use the full trial below to prove a fresh durable on repeated demos.", actionButton("Start third consumer", "third-start") + actionButton("Stop third consumer", "third-stop") + inspectButton("Read event positions", "events"), "The new subscriber receives retained events without changing the producer.")}</section>` +
      trial("events-trial", "Verify delivery, rebuild & a fresh durable");
  return (
    heading(
      `PROBLEM ${page.slice(1)} · MILESTONE 1`,
      state.spec[page]?.title || names[page],
      goals[page],
    ) +
    specification() +
    content +
    (page === "p1" || page === "p2"
      ? `<div class="actions">${actionButton("Restore source defaults", "restore-defaults")}</div>`
      : "") +
    resultPanel() +
    runPanel()
  );
}

function inspectPage() {
  return (
    heading(
      "WORKSPACE",
      "Inspect the system",
      "Read current records, owner-provided storage metadata, wire schemas and the selected configuration.",
    ) +
    `<section class="card"><h2>Data & delivery</h2><div class="actions">${inspectButton("BMKG API", "api", { source: "BMKG" })}${inspectButton("PVMBG API", "api", { source: "PVMBG" })}${inspectButton("Store & outbox", "store")}${inspectButton("NATS & consumers", "events")}</div><div class="field-row"><label class="field">Wire schema<select id="schema-name"><option>hazard-event</option><option>hazard-envelope</option><option>hazard-summary</option></select></label><button class="button" id="read-schema">Read schema</button></div></section>` +
    `<section class="card"><h2>Record history</h2><div class="field-row"><label class="field">Source<select id="history-source"><option>PVMBG</option><option>BMKG</option></select></label><label class="field grow">Source reference<input id="history-ref" placeholder="report_id or event_id" maxlength="64" value="${h(state.refs.PVMBG || "")}"></label><label class="field">Revisions<select id="history-revisions"><option value="1,2">1 & 2</option><option value="1">1</option></select></label><button class="button" id="read-history">Read history</button></div><p class="note">P1 pins a mapped sample and fills its reference here. Reads run through the Aggregator's audit command.</p></section>` +
    resultPanel() +
    `<details class="spec"><summary>Configured environment · secrets masked</summary><div class="spec-body"><p class="note">These are the startup configuration values. Live source changes made during a demo appear in execution results.</p><div class="config-list">${Object.entries(
      state.bootstrap.profile,
    )
      .sort(([a], [b]) => a.localeCompare(b))
      .map(
        ([key, value]) =>
          `<div class="config-entry"><span>${h(key)}</span>${h(value || "(empty)")}</div>`,
      )
      .join("")}</div></div></details>`
  );
}

function evidencePage() {
  return (
    heading(
      "WORKSPACE",
      "Evidence",
      "Every action saves its revision, result and execution log. Historical trials are shown separately.",
    ) +
    `<section class="card"><div class="card-heading"><h2>Console runs</h2><button class="button small" id="refresh-evidence">Refresh</button></div><div id="evidence-list">Loading runs…</div><p class="note mono">${h(state.bootstrap.evidence_dir)}</p></section><section class="card"><h2>Historical repository evidence</h2><p class="note">These files belong to earlier revisions. Opening one does not run a new experiment.</p><div class="field-row"><label class="field grow">Recorded result<select id="historical-file"><option value="">Loading files…</option></select></label><button class="button" id="read-historical">Open result</button></div></section>` +
    resultPanel() +
    runPanel()
  );
}

function renderPage() {
  document.title = `${names[state.page]} · AAT demo console`;
  document.querySelectorAll("[data-page]").forEach((button) => {
    const active = button.dataset.page === state.page;
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  $("#main").innerHTML =
    state.page === "overview"
      ? overview()
      : state.page === "inspect"
        ? inspectPage()
        : state.page === "evidence"
          ? evidencePage()
          : problemPage(state.page);
  renderServices();
  renderInspection();
  renderRun();
  updateButtons();
  if (state.page === "evidence") loadEvidence();
}
function updateButtons() {
  document
    .querySelectorAll("[data-action]")
    .forEach((button) => (button.disabled = !!state.active));
  $("#cancel-run").hidden = !state.active;
  $("#overview-run-state")?.replaceChildren(
    document.createTextNode(state.active ? "Running" : "Idle"),
  );
}

async function refreshStatus() {
  if (statusPending) return;
  statusPending = true;
  try {
    state.containers = await api("/api/status");
    state.statusError = "";
  } catch (error) {
    state.statusError = error.message;
  } finally {
    statusPending = false;
    renderServices();
  }
}
function renderServices() {
  const good = state.containers.filter(
    (c) => c.state === "running" && c.health === "healthy",
  ).length;
  $("#health").className =
    `badge ${state.statusError ? "failed" : good >= 9 ? "good" : "neutral"}`;
  $("#health").textContent = state.statusError
    ? "Docker unavailable"
    : state.containers.length
      ? `${good}/${state.containers.length} healthy`
      : "Stack not started";
  if ($("#healthy-count"))
    $("#healthy-count").textContent = `${good}/${state.containers.length || 9}`;
  const target = $("#service-table");
  if (!target) return;
  if (state.statusError) {
    target.innerHTML = `<div class="run-error">${h(state.statusError)}</div>`;
    return;
  }
  if (!state.containers.length) {
    target.innerHTML =
      '<div class="empty">No containers found in this project. Prepare keys, then build and start.</div>';
    return;
  }
  target.innerHTML = `<div class="table-wrap"><table><thead><tr><th>Service</th><th>State</th><th>Container</th><th>Restarts</th></tr></thead><tbody>${[
    ...state.containers,
  ]
    .sort((a, b) => a.service.localeCompare(b.service))
    .map(
      (c) =>
        `<tr><td class="service-name">${h(c.service)}</td><td>${badge(c.state === "running" ? c.health || "running" : c.state)}</td><td class="mono" title="${h(c.started)}">${h(c.id.slice(0, 12))}</td><td class="mono">${c.restarts}</td></tr>`,
    )
    .join(
      "",
    )}</tbody></table></div><div class="table-caption">Container IDs are live. Hover an ID for its start time.</div>`;
  $("#status-time").textContent =
    `Checked ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
}

async function inspect(kind, params = {}, checkpoint = false) {
  const page = state.page;
  const sequence = (inspectionSequence[page] =
    (inspectionSequence[page] || 0) + 1);
  const node = $("#inspection-result");
  if (node) {
    node.className = "result-empty";
    node.textContent = "Reading current state…";
  }
  try {
    const result = await api(
      `/api/inspect/${kind}?${new URLSearchParams(params)}`,
    );
    if (sequence !== inspectionSequence[page]) return;
    const value = JSON.parse(result.json);
    if (kind === "mapping")
      state.refs[params.source || "PVMBG"] = value.source_ref_id;
    if (checkpoint) state.baseline = value;
    state.inspections[page] = {
      kind,
      text: result.json,
      value,
      time: new Date().toLocaleTimeString(),
      checkpoint,
    };
    if (state.page === page) {
      renderInspection();
      reveal("#inspection-panel");
    }
  } catch (error) {
    if (sequence !== inspectionSequence[page]) return;
    state.inspections[page] = {
      error: error.message,
      time: new Date().toLocaleTimeString(),
    };
    if (state.page === page) renderInspection();
    toast(error.message);
  }
}
function renderInspection() {
  const node = $("#inspection-result");
  if (!node) return;
  const item = state.inspections[state.page];
  if (!item) return;
  $("#inspection-time").textContent = `Observed ${item.time}`;
  node.className = "";
  if (item.error) {
    node.innerHTML = `<div class="run-error">${h(item.error)}</div>`;
    return;
  }
  if (item.kind === "mapping") {
    node.innerHTML = `<p class="run-summary">Pinned source reference: <span class="mono">${h(item.value.source_ref_id)}</span></p><div class="json-pair"><div><p class="json-label">SOURCE RECORD</p><pre class="json">${h(item.value.source)}</pre></div><div><p class="json-label">CANONICAL HAZARDEVENT</p><pre class="json">${h(item.value.canonical)}</pre></div></div>`;
  } else if (item.kind === "events") {
    const stream = item.value.stream;
    const sequence = stream?.state?.last_seq ?? "—";
    const baseline = state.baseline?.stream?.state?.last_seq;
    node.innerHTML = `<div class="stats"><div class="stat"><div class="stat-label">Last stream sequence</div><div class="stat-value">${h(sequence)}</div><div class="stat-note">${baseline !== undefined ? `Checkpoint: ${h(baseline)} · Δ ${h(sequence - baseline)}` : "Capture a checkpoint before downtime"}</div></div><div class="stat"><div class="stat-label">Retained messages</div><div class="stat-value">${h(stream?.state?.messages ?? "—")}</div></div><div class="stat"><div class="stat-label">Durables</div><div class="stat-value">${h(stream?.consumer_detail?.length ?? 0)}</div></div></div><div class="table-wrap"><table><thead><tr><th>Consumer</th><th>ACK stream position</th><th>Pending</th></tr></thead><tbody>${(stream.consumer_detail || []).map((c) => `<tr><td class="mono">${h(c.name)}</td><td class="mono">${h(c.ack_floor?.stream_seq ?? "—")}</td><td class="mono">${h(c.num_pending ?? "—")}</td></tr>`).join("")}</tbody></table></div><details class="spec"><summary>Full stream & consumer inspection</summary><div class="spec-body"><pre class="json">${h(item.text)}</pre></div></details>`;
  } else {
    node.innerHTML = `<pre class="json">${h(item.text)}</pre>`;
  }
}

async function startAction(id, parameters) {
  if (state.active) {
    toast("Finish or cancel the running action first.");
    return;
  }
  try {
    state.run = await api("/api/runs", {
      method: "POST",
      body: JSON.stringify({ action: id, parameters }),
    });
    state.active = state.run.id;
    state.tab = "execution";
    setConsoleTab();
    renderRun();
    updateButtons();
    toast("Started: " + state.run.title);
  } catch (error) {
    toast(error.message);
  }
}
function duration(run) {
  const seconds = Math.max(
    0,
    Math.round(
      (run.elapsed_ms ??
        (run.finished ? new Date(run.finished) - new Date(run.started) : 0)) /
        1000,
    ),
  );
  return seconds >= 60
    ? `${Math.floor(seconds / 60)}m ${seconds % 60}s`
    : `${seconds}s`;
}
async function pollRun() {
  if (
    runPending ||
    !state.run ||
    (!state.active && state.run.status !== "running")
  )
    return;
  runPending = true;
  try {
    if (state.active && state.active !== state.run.id) {
      const active = await api("/api/runs/" + state.active);
      if (active.status !== "running") {
        state.active = "";
        refreshStatus();
        if (state.page === "evidence") loadEvidence();
      }
    }
    if (state.run.status === "running") {
      const id = state.run.id;
      const run = await api("/api/runs/" + id);
      if (state.run.id !== id) return;
      state.run = run;
      if (state.active === id && run.status !== "running") {
        state.active = "";
        refreshStatus();
        if (state.page === "evidence") loadEvidence();
      }
      renderRun();
    }
    updateButtons();
  } catch (error) {
    toast(error.message);
  } finally {
    runPending = false;
  }
}
function observedChecks(run) {
  const checks = [];
  for (const line of run.lines || []) {
    const start = line.indexOf("{");
    if (start < 0) continue;
    try {
      const record = JSON.parse(line.slice(start));
      if (record.check && !checks.includes(record.check))
        checks.push(record.check);
    } catch {}
  }
  if (run.result?.results) {
    return run.result.results.map((r) => r.check);
  }
  if (Array.isArray(run.result))
    return run.result.map((r) =>
      r.identity
        ? `${r.identity}: HTTP ${r.status}, ${r.field_count} fields (${r.view})`
        : `${r.check} ${r.query || ""}: HTTP ${r.status}`,
    );
  return checks;
}
function renderRun() {
  const run = state.run;
  if (state.tab === "execution") {
    $("#run-info").hidden = false;
    $("#log-controls").hidden = true;
    $("#console-state").textContent = run ? run.status : "Idle";
    $("#run-info").innerHTML = run
      ? `<strong>${h(run.title)}</strong>${h(run.id)} · ${h(duration(run))} <button id="view-run-result">View result</button>`
      : "<span>No action selected</span>";
    if (run) setOutput((run.lines || []).join("\n"));
  }
  const panel = $("#run-panel");
  if (!panel || !run) return;
  panel.hidden = false;
  $("#run-title").textContent = run.title;
  $("#run-badge").innerHTML = badge(run.status);
  $("#run-summary").textContent =
    `${run.project} · ${run.revision?.slice(0, 8) || "unknown revision"}${run.dirty ? " + local changes" : ""} · ${duration(run)}`;
  $("#run-error").innerHTML = run.error
    ? `<div class="run-error">${h(run.error)}</div>`
    : "";
  $("#run-checks").innerHTML = observedChecks(run)
    .map((check) => `<li><span class="check-mark">✓</span>${h(check)}</li>`)
    .join("");
  $("#load-result").innerHTML = "";
  if (run.action === "load-trial" && run.result?.load) {
    const v = run.result.load;
    const samples = run.result.tcp?.samples || [];
    $("#load-result").innerHTML =
      `<div class="stats"><div class="stat"><div class="stat-label">BMKG successful p95</div><div class="stat-value">${h(v.sources?.BMKG?.success_duration_ms?.p95?.toFixed(1) ?? "—")} <small>ms</small></div><div class="stat-note">Required &lt; 300 ms</div></div><div class="stat"><div class="stat-label">Unexpected errors</div><div class="stat-value">${h(v.hazard_unexpected_error_percent?.toFixed(2))}%</div><div class="stat-note">Required &lt; 1%</div></div><div class="stat"><div class="stat-label">Successful throughput</div><div class="stat-value">${h(v.hazard_successes_per_second?.toFixed(0))}</div><div class="stat-note">Requests / second</div></div></div>${tcpChart(samples)}<p class="note">TCP criterion: ≥50 established connections sustained for ≥60 seconds. Measured span: ${h(run.result.sustained_tcp_seconds?.toFixed(1) ?? "—")} seconds. Full percentiles and controlled 429 counts are in the result JSON.</p>`;
  }
  $("#run-downloads").innerHTML =
    run.status === "running"
      ? ""
      : `${run.result ? `<a class="button small" href="/api/runs/${run.id}/result.json">Result JSON ↓</a>` : ""}<a class="button small" href="/api/runs/${run.id}/output.log">Execution log ↓</a><a class="button small" href="/api/runs/${run.id}/run.json">Run metadata ↓</a>`;
}
function tcpChart(samples) {
  if (!samples.length) return "";
  const max = Math.max(
    60,
    ...samples.map((s) => s.established_connections_to_api),
  );
  const end = samples.at(-1).elapsed_seconds || 1;
  const points = samples
    .map(
      (s) =>
        `${((s.elapsed_seconds / end) * 550 + 30).toFixed(1)},${(105 - (s.established_connections_to_api / max) * 90).toFixed(1)}`,
    )
    .join(" ");
  const threshold = 105 - (50 / max) * 90;
  return `<svg class="chart" viewBox="0 0 600 120" role="img" aria-label="Measured established TCP connections over time"><line x1="30" y1="${threshold}" x2="580" y2="${threshold}"/><text x="0" y="${threshold + 3}">50</text><polyline points="${points}"/><text x="30" y="119">0s</text><text x="535" y="119">${h(Math.round(end))}s</text></svg>`;
}
function setOutput(text) {
  const el = $("#console-output");
  if (el.textContent === text) return;
  const position = el.scrollTop;
  el.textContent = text || "Waiting for output…";
  el.scrollTop = state.follow ? el.scrollHeight : position;
}
function setConsoleTab() {
  const execution = state.tab === "execution";
  $("#execution-tab").setAttribute("aria-selected", execution);
  $("#logs-tab").setAttribute("aria-selected", !execution);
  $("#run-info").hidden = !execution;
  $("#log-controls").hidden = execution;
  if (execution) renderRun();
  else {
    setOutput("Loading service logs…");
    refreshLogs();
  }
}
async function refreshLogs() {
  if (logPending || state.tab !== "logs") return;
  logPending = true;
  try {
    const service = $("#log-service").value;
    const data = await api("/api/logs/" + encodeURIComponent(service));
    if (state.tab === "logs" && $("#log-service").value === service)
      setOutput(data.text);
  } catch (error) {
    if (state.tab === "logs") setOutput(error.message);
  } finally {
    logPending = false;
  }
}

async function loadEvidence() {
  try {
    const data = await api("/api/runs");
    const el = $("#evidence-list");
    if (el)
      el.innerHTML = data.runs.length
        ? data.runs
            .map(
              (run) =>
                `<div class="evidence-item"><div><div class="evidence-title">${h(run.title)} ${badge(run.status)}</div><div class="evidence-meta">${h(run.id)} · ${h(run.revision?.slice(0, 8))}</div></div><button class="button small" data-open-run="${h(run.id)}">Open</button></div>`,
            )
            .join("")
        : '<div class="empty">No console runs yet. Run a demo step to create evidence.</div>';
    const files = await api("/api/inspect/historical");
    const select = $("#historical-file");
    if (select)
      select.innerHTML =
        JSON.parse(files.json)
          .map((file) => `<option value="${h(file)}">${h(file)}</option>`)
          .join("") || '<option value="">No recorded files</option>';
  } catch (error) {
    toast(error.message);
  }
}

document.addEventListener("click", async (event) => {
  const button = event.target.closest("button");
  if (!button) return;
  if (button.dataset.page) {
    location.hash = button.dataset.page;
    return;
  }
  if (button.dataset.action) {
    startAction(
      button.dataset.action,
      JSON.parse(button.dataset.parameters || "{}"),
    );
    return;
  }
  if (button.dataset.inspect) {
    inspect(button.dataset.inspect, JSON.parse(button.dataset.query || "{}"));
    return;
  }
  if (button.dataset.openRun) {
    try {
      state.run = await api("/api/runs/" + button.dataset.openRun);
      state.tab = "execution";
      setConsoleTab();
      renderRun();
      reveal("#run-panel");
    } catch (error) {
      toast(error.message);
    }
    return;
  }
  switch (button.id) {
    case "view-run-result":
      if (!$("#run-panel")) location.hash = "evidence";
      else reveal("#run-panel");
      break;
    case "refresh-status":
      refreshStatus();
      break;
    case "execution-tab":
      state.tab = "execution";
      setConsoleTab();
      break;
    case "logs-tab":
      state.tab = "logs";
      setConsoleTab();
      break;
    case "follow-output":
      state.follow = !state.follow;
      button.setAttribute("aria-pressed", state.follow);
      if (state.follow)
        $("#console-output").scrollTop = $("#console-output").scrollHeight;
      break;
    case "copy-output":
      try {
        await navigator.clipboard.writeText($("#console-output").textContent);
        toast("Output copied.");
      } catch {
        toast("Select the output and copy it manually.");
      }
      break;
    case "cancel-run":
      try {
        await api("/api/runs/" + state.active + "/cancel", {
          method: "POST",
          body: "{}",
        });
        toast("Cancelling. Waiting for cleanup to finish.");
      } catch (error) {
        toast(error.message);
      }
      break;
    case "mapping-read":
    case "mapping-new": {
      const source = $("#mapping-source").value;
      if (button.id === "mapping-new") delete state.refs[source];
      inspect("mapping", {
        source,
        ...(state.refs[source] ? { ref: state.refs[source] } : {}),
      });
      break;
    }
    case "pinned-history": {
      const source = $("#mapping-source").value;
      const ref = state.refs[source];
      if (!ref) {
        toast("Read a mapped sample first.");
        break;
      }
      inspect("history", { source, ref, revisions: "1,2" });
      break;
    }
    case "identity-read":
      inspect("api", {
        identity: $("#api-identity").value,
        source: $("#api-source").value,
      });
      break;
    case "read-history":
      inspect("history", {
        source: $("#history-source").value,
        ref: $("#history-ref").value.trim(),
        revisions: $("#history-revisions").value,
      });
      break;
    case "read-schema":
      inspect("schema", { name: $("#schema-name").value });
      break;
    case "show-containers":
      await refreshStatus();
      state.inspections[state.page] = {
        kind: "containers",
        text: JSON.stringify(state.containers, null, 2),
        time: new Date().toLocaleTimeString(),
      };
      renderInspection();
      break;
    case "capture-checkpoint":
      inspect("events", {}, true);
      break;
    case "refresh-evidence":
      loadEvidence();
      break;
    case "read-historical": {
      const file = $("#historical-file").value;
      if (!file) return;
      try {
        const result = await api(
          "/api/historical?" + new URLSearchParams({ file }),
        );
        state.inspections[state.page] = {
          kind: "historical",
          text: result.json,
          time: new Date().toLocaleTimeString(),
        };
        renderInspection();
      } catch (error) {
        toast(error.message);
      }
      break;
    }
  }
});
$("#log-service").addEventListener("change", refreshLogs);
window.addEventListener("hashchange", () => {
  const page = location.hash.slice(1) || "overview";
  state.page = names[page] ? page : "overview";
  renderPage();
});
async function boot() {
  try {
    [state.bootstrap, state.spec] = await Promise.all([
      api("/api/bootstrap"),
      api("/specification.json"),
    ]);
    $("#project").textContent = state.bootstrap.project;
    $("#project-label").textContent = state.bootstrap.project;
    $("#revision").textContent = state.bootstrap.revision;
    state.page = names[location.hash.slice(1)]
      ? location.hash.slice(1)
      : "overview";
    renderPage();
    refreshStatus();
    const runs = await api("/api/runs");
    state.active = runs.active;
    if (state.active) {
      state.run = await api("/api/runs/" + state.active);
      renderRun();
      updateButtons();
    }
    setInterval(() => {
      if (!document.hidden) {
        pollRun();
        refreshLogs();
      }
    }, 1000);
    setInterval(() => {
      if (!document.hidden) refreshStatus();
    }, 10000);
  } catch (error) {
    $("#main").innerHTML = `<div class="run-error">${h(error.message)}</div>`;
  }
}
boot();
