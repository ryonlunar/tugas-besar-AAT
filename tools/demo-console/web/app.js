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
  selected: "aggregator",
  active: "",
  stream: null,
  run: null,
  refs: {},
  pending: null,
  starting: false,
  preview: 0,
  switchCommand: null,
};
const titles = { overview: "Overview", inspect: "Inspect", runs: "Runs" };
const nodeLayout = [
  ["bmkg-mock", "BMKG", 16, 12],
  ["pvmbg-mock", "PVMBG", 56, 12],
  ["aggregator", "Aggregator", 43, 36],
  ["postgres", "PostgreSQL", 80, 36],
  ["nats", "NATS", 43, 62],
  ["client-api", "Client API", 80, 62],
  ["auth", "Auth", 88, 87],
  ["dashboard-consumer", "Dashboard", 14, 87],
  ["notifier-consumer", "Notifier", 38, 87],
  ["third-consumer", "Third", 62, 87],
];
const edges = [
  [
    "bmkg-mock",
    "aggregator",
    "M160 166 C160 228 430 200 430 266",
    "HTTP polling",
  ],
  [
    "pvmbg-mock",
    "aggregator",
    "M560 166 C560 220 470 220 470 266",
    "HTTP polling",
  ],
  ["aggregator", "postgres", "M545 324 L685 324", "SQL"],
  ["aggregator", "nats", "M430 380 L430 500", "Outbox publication"],
  [
    "client-api",
    "aggregator",
    "M750 504 C740 444 550 455 500 380",
    "Aggregator API reads",
  ],
  [
    "auth",
    "client-api",
    "M880 727 C880 680 800 680 800 616",
    "JWT tokens and verification keys",
  ],
  [
    "nats",
    "dashboard-consumer",
    "M385 616 C380 675 140 665 140 728",
    "Event subscription",
  ],
  [
    "nats",
    "notifier-consumer",
    "M430 616 C430 675 380 660 380 728",
    "Event subscription",
  ],
  [
    "nats",
    "third-consumer",
    "M475 616 C480 675 620 660 620 728",
    "Event subscription",
  ],
];
let toastTimer,
  statusPending = false;
const terminal = new Terminal({
  fontSize: 15,
  lineHeight: 1.2,
  fontFamily: "ui-monospace, SFMono-Regular, Consolas, monospace",
  convertEol: true,
  disableStdin: true,
  scrollback: 20000,
  cursorStyle: "bar",
  cursorInactiveStyle: "none",
  minimumContrastRatio: 4.5,
  theme: {
    background: "#11161d",
    foreground: "#d7dee7",
    selectionBackground: "#3b5269",
    cursor: "#93b7ab",
  },
});
const fit = new FitAddon.FitAddon();
terminal.loadAddon(fit);
terminal.open($("#terminal"));
new ResizeObserver(() => requestAnimationFrame(() => fit.fit())).observe(
  $("#terminal"),
);
terminal.attachCustomKeyEventHandler((event) => {
  if (event.type !== "keydown") return true;
  if (event.key === "Enter" && $("#command-input").value.trim()) {
    event.preventDefault();
    executePending();
    return false;
  }
  if (
    event.key === "Escape" ||
    (event.ctrlKey &&
      event.key.toLowerCase() === "c" &&
      !terminal.hasSelection())
  ) {
    if (state.active) {
      if (event.key !== "Escape") stopCommand();
    } else if (!state.starting) discardPending();
    event.preventDefault();
    return false;
  }
  return true;
});

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
  toastTimer = setTimeout(() => ($("#toast").hidden = true), 5000);
}
function button(label, action, parameters = {}, primary = false) {
  return `<button class="button ${primary ? "primary" : ""}" data-action="${h(action)}" data-parameters="${h(JSON.stringify(parameters))}">${h(label)}</button>`;
}
const read = (label, kind, parameters = {}) =>
  button(label, "inspect", { kind, ...parameters });
const history = () =>
  `<div class="field-row"><label>PVMBG source reference<input id="history-ref" value="${h(state.refs.PVMBG || "")}" placeholder="report_id" maxlength="64"></label><button class="button" id="pinned-history">Read versions</button></div>`;
function infoButton(type, value = "") {
  return `<button class="info-button" data-info="${h(type)}" data-value="${h(value)}" aria-label="More information">i</button>`;
}
function heading(title, eyebrow = "", info = "") {
  return `<div class="page-heading"><div><span class="eyebrow">${h(eyebrow)}</span>${info}</div><h1>${h(title)}</h1></div>`;
}
function excerpt(text) {
  if (text.length <= 170) return text;
  const short = text.slice(0, 170);
  return short.slice(0, short.lastIndexOf(" ")) + "…";
}
function showInfo(title, content) {
  $("#info-title").textContent = title;
  $("#info-content").innerHTML = content;
  $("#info-dialog").showModal();
}

function decorateCommandButtons() {
  document
    .querySelectorAll(
      "#main [data-action], #pinned-history, #login-api, #read-api, #read-history, #read-schema, #new-sample",
    )
    .forEach((target) => {
      if (target.parentElement.classList.contains("command-group")) return;
      const group = document.createElement("span");
      group.className = "command-group";
      target.before(group);
      const help = document.createElement("button");
      help.className = "command-help";
      help.type = "button";
      help.textContent = "i";
      help.title = `About ${target.textContent}`;
      help.setAttribute("aria-label", help.title);
      help.commandTarget = target;
      group.append(target, help);
    });
}
function explainCommand(target) {
  let action = target.dataset.action;
  let p = JSON.parse(target.dataset.parameters || "{}");
  const special = {
    "login-api": () => ({ identity: $("#api-identity").value }),
    "read-api": () => ({
      kind: "api",
      source: $("#api-source").value,
      identity: $("#api-identity").value,
    }),
    "read-history": () => ({ kind: "history" }),
    "pinned-history": () => ({
      kind: "history",
      source: "PVMBG",
      ref: state.refs.PVMBG,
    }),
    "read-schema": () => ({ kind: "schema", name: $("#schema-name").value }),
    "new-sample": () => ({ kind: "canonical", source: "PVMBG", fresh: true }),
  };
  if (special[target.id]) {
    action = target.id === "login-api" ? "client-login" : "inspect";
    p = special[target.id]();
  }
  const number = target.closest("[data-criterion]")?.dataset.criterion;
  const context = state.spec[state.page]
    ? `${state.page.toUpperCase()}${number ? ` · criterion ${number}` : ""}`
    : titles[state.page];
  showInfo(
    target.textContent,
    `<p class="subtle">${h(context)}</p><p>${h(commandPurpose(action, p))}</p>`,
  );
}
function commandPurpose(action, p) {
  if (action === "inspect") {
    const identity = {
      media: "Media",
      "field-team": "Field Team",
      "bnpb-internal": "BNPB internal",
    }[p.identity || "field-team"];
    const source = p.source || "BMKG";
    const reads = {
      api: `Reads ${source} hazards using the saved ${identity} session. Log in first; this read does not refresh an expired token. Use it to check the client's allowed fields${state.page === "p2" ? " and whether data remains available during the PVMBG outage" : ""}.`,
      load:
        p.measurement === "load"
          ? "Reads the latest recorded workload metrics: throughput, per-source p50/p95/p99, errors and controlled 429 responses. Record load + TCP first. A failed or cancelled run may leave partial files; use completed measurements for evidence."
          : "Reads the latest recorded TCP samples. Check for at least 50 established API connections continuously for at least 60 seconds. The full load trial automatically verifies this interval. Record load + TCP first.",
      canonical: p.fresh
        ? "Pins a current PVMBG source_ref_id for a new history comparison and prints the canonical API response."
        : `Reads stored ${source} HazardEvents through the Aggregator. Compare event_id/report_id in the source output with source_ref_id here. The first read pins a reference for Read versions; later reads keep that reference. After schema v2, check attributes.confidence_level.`,
      broker:
        "Reads JetStream stream and durable positions. Compare message counts and ACK/pending counts before stopping a consumer, during the stop, and after restart.",
      consumer: `Reads ${p.service}'s inbox and ACK position. Compare with the broker and the other consumer to see independent delivery and recovery.`,
      history:
        "Reads stored revisions 1 and 2 for the selected record. Use it to show that schema changes preserve older data. Read PVMBG HazardEvents under schema v1 first, or enter its source_ref_id explicitly.",
      store:
        "Shows stored hazards, JSONB column types, migration history and outbox counts. Use it to check the canonical store and show that adding a source field required no database migration.",
      events:
        "Shows the JetStream state and running consumers' inbox/ACK positions. Compare before stopping a consumer, while stopped and after recovery to see independent progress and catch-up.",
      containers:
        "Lists this project's containers and their status. Compare before and after stopping or rebuilding a single service for P4.",
      profile:
        "Shows the selected startup environment with credentials masked. Use it to check ports, token TTLs, polling and NATS settings.",
      "tracked-config":
        "Lists configuration paths tracked by Git. Use it for P3's credential review; source code must be checked separately.",
      config: `Shows ${p.service}'s environment and network attachments, with credentials masked. In P4, compare PostgreSQL and API networks to check database access boundaries.`,
      schema: `Prints the ${p.name || "hazard-event"} JSON Schema contract. Compare it with the mapped data to see the required fields and extension rules.`,
    };
    return reads[p.kind];
  }
  const purposes = {
    "source-read": `Sends the ${p.credential} credential to ${p.source}. Expect HTTP ${p.source === p.credential ? "200 with source records" : "401 or 403 with an authorization error"}. This command makes only this request. The visible since filter selects the last two minutes; edit or remove it for older records.`,
    "client-login": `Logs in ${p.identity} and saves its session privately for subsequent commands. Expect HTTP 200, scope and expires_in. Tokens are masked in the log. This replaces any previous saved session for this client.`,
    "client-read": `Uses the saved ${p.identity} token without login or refresh. ${p.identity === "media" && p.view !== "summary" ? "Expect HTTP 401 or 403 for the explicit raw-data request." : "Expect HTTP 200 and the permitted fields while the token is valid; HTTP 401 after expiry."}${state.page === "p2" ? " During outage, inspect meta.sources for freshness, wait for a polling cycle, and repeat after recovery." : ""}`,
    "token-wait":
      "Reads the saved JWT expiry timestamp and waits until the clock reaches it plus two seconds. No login or refresh occurs. Run Read expired token next; expect HTTP 401. Backward clock changes can extend the wait; host and service clocks must agree.",
    "token-refresh":
      "Uses the saved refresh token, without logging in. Expect HTTP 200. Saves the new session and keeps the prior access token for Read old token. Run promptly after the expiry check.",
    "token-old-read":
      "Uses the access token kept before refresh. After the natural-expiry step, expect HTTP 401. This proves the expired token remains rejected; refresh alone does not revoke an unexpired JWT.",
    "load-run":
      "Runs 60 workers for 90 seconds and a 115-second TCP collector concurrently. Saves their raw measurements; read each separately afterward. Does not change the source delay. Ctrl+C stops both processes. The full trial additionally configures/restores delay and asserts the sustained TCP threshold.",
    "latency-trial":
      "Runs 30 BMKG and 30 PVMBG workers concurrently for 90 seconds. First set the 3-second source delay and allow a polling cycle. Expect BMKG p95 below 300 ms. This command leaves the delay as set; use Restore defaults afterward.",
    "keys-setup":
      "Creates the RSA key pair when absent and loads separate private/public key volumes. Auth needs the private key to sign tokens; the API uses the public key to verify them.",
    "stack-start":
      "Starts the nine default services from the selected Compose project. Use it to bring up an existing build before the demo.",
    "stack-build":
      "Builds changed images and starts the default services. Use it after changing service code or for initial setup.",
    "service-start": `Starts ${p.service} independently. For consumers, this lets the existing durable subscription resume its backlog.`,
    "service-stop": `Stops only the ${p.service} container. Use the service map and logs to observe the effect on its dependencies and clients.`,
    "service-rebuild": `Builds and recreates ${p.service} with --no-deps. Use it to demonstrate an independent container update.`,
    "service-logs": `Follows ${p.service}'s actual container logs. Use it beside the experiment to see requests, polling, publication or consumption. Stop or Ctrl+C ends the log follower.`,
    "source-schema":
      p.version === "2"
        ? "Switches live PVMBG data to schema v2, which adds confidence_level. Read the same mapping and stored versions to demonstrate additive schema changes without restart."
        : "Switches live PVMBG data to schema v1. Read a mapping to capture the baseline before adding confidence_level.",
    "source-outage":
      p.enabled === "true"
        ? "Makes PVMBG return HTTP 503. For P2, read BMKG and PVMBG through the client API to check isolation and stale cached data."
        : "Makes PVMBG available again. Wait for the next poll, then read the API to check recovery and fresh data.",
    "source-delay":
      p.preset === "slow"
        ? "Sets PVMBG response delay to 3 seconds. For P2, compare BMKG latency while the PVMBG poll is slow."
        : "Restores the PVMBG delay configured in .env.",
    "restore-defaults":
      "Restores PVMBG's configured schema, delay and outage settings. Use it to return to the starting state after manual experiments.",
    "access-check":
      "Reads the API as all three clients, checks Media's summary fields and rejects Media raw-data requests. It also tries each source's credential at the other source to check P3's trust boundaries.",
    "token-trial":
      "Checks an existing JWT while Auth is stopped, waits for the default 60-second expiry, then tests refresh and replay rejection. Auth is restarted by the script.",
    "smoke-trial":
      "Runs the existing mapping, access, outage and schema-history checks for P1–P4. It changes PVMBG settings during the trial and restores their configured defaults.",
    "events-trial":
      "Checks independent consumer progress, backlog recovery and dashboard rebuild. It creates and removes a fresh durable to prove that a new consumer can read retained events. It finishes with Dashboard and the default third consumer running.",
    "load-trial":
      "Defaults: 30 BMKG and 30 PVMBG workers for 90 seconds, with PVMBG delayed by 3 seconds. It measures P2 latency and checks at least 50 TCP connections for 60 seconds. Cleanup stops the measurements and restores delay.",
    "dashboard-stop":
      state.page === "p4"
        ? "Stops Dashboard independently. Read BMKG afterward to show that the client API still serves requests while this container is stopped."
        : "Stops Dashboard while the producer and Notifier continue. Compare event positions, then restart Dashboard to demonstrate backlog recovery.",
    "dashboard-start":
      "Restarts Dashboard using its existing durable subscription. Read event positions afterward to show it catches up with retained messages.",
    "dashboard-rebuild":
      "Rebuilds and recreates Dashboard with --no-deps. Read Containers before and after to show other containers were not restarted.",
    "third-start":
      "Starts the demo third consumer with its own durable and storage. Use it for P5's subscriber extension demo; repeated starts reuse the same durable.",
    "third-stop":
      "Stops the demo third consumer while retaining its durable position and stored data. Use it when ending or repeating the third-consumer demo.",
  };
  return (
    purposes[action] ||
    state.bootstrap.actions.find((a) => a.id === action)?.impact
  );
}

function problemControls(page, n) {
  const source = (name, credential = name) =>
    button(`${credential} credential → ${name}`, "source-read", {
      source: name,
      credential,
    });
  const raw = (name) =>
    button(`${name} source`, "source-read", { source: name, credential: name });
  const canonical = (name) =>
    read(`${name} HazardEvents`, "canonical", { source: name });
  const schema = (version) =>
    button("Schema v" + version, "source-schema", { version: String(version) });
  const login = (identity, name) =>
    button(`Log in ${name}`, "client-login", { identity });
  const apiRead = (
    label,
    identity = "field-team",
    source = "BMKG",
    view = "full",
  ) => button(label, "client-read", { identity, source, view });
  const broker = () => read("Broker positions", "broker");
  const consumer = (service, name) =>
    read(`${name} inbox`, "consumer", { service });
  const positions = () =>
    broker() +
    consumer("dashboard-consumer", "Dashboard") +
    consumer("notifier-consumer", "Notifier");
  const controls = {
    p1: [
      schema(1) +
        raw("BMKG") +
        canonical("BMKG") +
        raw("PVMBG") +
        canonical("PVMBG"),
      schema(2),
      raw("PVMBG") + canonical("PVMBG") + read("Wire schema", "schema"),
      canonical("PVMBG"),
      "",
    ],
    p2: [
      button("3 s delay", "source-delay", { preset: "slow" }) +
        button("Compare latency · 90 s", "latency-trial"),
      button("Record load + TCP · 90 s", "load-run") +
        read("Read load metrics", "load", { measurement: "load" }) +
        read("Read TCP samples", "load", { measurement: "tcp" }),
      login("field-team", "Field Team") +
        button("Outage on", "source-outage", { enabled: "true" }) +
        apiRead("Read BMKG") +
        apiRead("Read PVMBG", "field-team", "PVMBG") +
        button("Outage off", "source-outage", { enabled: "false" }),
      "",
    ],
    p3: [
      source("BMKG") +
        source("PVMBG", "BMKG") +
        source("PVMBG") +
        source("BMKG", "PVMBG"),
      login("media", "Media") +
        apiRead("Media summary", "media", "BMKG", "summary") +
        apiRead("Media requests full", "media") +
        apiRead("Media requests attributes", "media", "BMKG", "attributes") +
        login("field-team", "Field Team") +
        apiRead("Field Team full") +
        login("bnpb-internal", "BNPB") +
        apiRead("BNPB full", "bnpb-internal"),
      login("field-team", "Field Team") +
        apiRead("Read before expiry") +
        button("Wait for expiry", "token-wait") +
        apiRead("Read expired token") +
        button("Refresh token", "token-refresh") +
        apiRead("Read new token") +
        button("Read old token", "token-old-read"),
      read("Tracked configuration", "tracked-config"),
      "",
    ],
    p4: [
      read("Containers", "containers") +
        login("field-team", "Field Team") +
        button("Stop dashboard", "dashboard-stop") +
        apiRead("Read BMKG") +
        button("Rebuild dashboard", "dashboard-rebuild") +
        button("Start dashboard", "dashboard-start"),
      schema(1) +
        canonical("PVMBG") +
        schema(2) +
        history() +
        read("Store & migrations", "store"),
      read("PostgreSQL config", "config", { service: "postgres" }) +
        read("API config", "config", { service: "client-api" }),
      "",
    ],
    p5: [
      broker(),
      consumer("dashboard-consumer", "Dashboard") +
        consumer("notifier-consumer", "Notifier"),
      positions() +
        button("Stop dashboard", "dashboard-stop") +
        button("Start dashboard", "dashboard-start"),
      button("Start third", "third-start") +
        consumer("third-consumer", "Third") +
        broker() +
        button("Stop third", "third-stop"),
      "",
    ],
  };
  return controls[page][n - 1];
}
function problemPage(page) {
  const spec = state.spec[page];
  return (
    heading(
      spec.title,
      `Problem ${page.slice(1)} · pp. ${spec.page}–${spec.page + 1}`,
      infoButton("problem"),
    ) +
    `<ol class="criteria">${spec.criteria
      .map((c) => {
        const controls = problemControls(page, c.number);
        return `<li class="criterion" data-criterion="${c.number}"><div class="criterion-heading"><span class="criterion-number">${c.number}.</span><p class="criterion-title">${h(excerpt(c.text))}</p>${infoButton("criterion", c.number)}</div>${controls ? `<div class="actions demo-steps">${controls}</div>` : '<span class="report-tag">Laporan</span>'}</li>`;
      })
      .join("")}</ol>` +
    `<div class="problem-footer">${["p1", "p2"].includes(page) ? button("Restore defaults", "restore-defaults") : ""}<details><summary>Full trials</summary><div class="actions">${page === "p1" ? button("Smoke trial", "smoke-trial") : page === "p2" ? button("Load + TCP trial", "load-trial") : page === "p3" ? button("Access trial", "access-check") + button("Token trial", "token-trial") : button("Event trial", "events-trial")}</div></details></div>`
  );
}
function overview() {
  return (
    heading("Services", state.bootstrap.project, infoButton("map")) +
    `<div class="actions">${button("JWT keys", "keys-setup")}${button("Build & start", "stack-build", {}, true)}${button("Start stack", "stack-start")}<button class="button" id="refresh-status">Refresh</button></div>` +
    `<div id="status-error" class="error" hidden></div><div class="service-map"><svg viewBox="0 0 1000 900" aria-hidden="true"><defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M0 0 L10 5 L0 10" fill="#98a8b4"/></marker></defs>${edges.map(([from, to, path, label]) => `<path class="connection" data-from="${from}" data-to="${to}" d="${path}" marker-end="url(#arrow)"><title>${h(label)}</title></path>`).join("")}</svg>${nodeLayout.map(([id, name, x, y]) => `<div class="service-node" data-node="${id}" style="left:${x}%;top:${y}%"><button class="node-name" data-select="${id}">${name}</button><div class="node-state"><span class="health-dot"></span><span data-health>loading</span></div><button class="service-toggle" role="switch" aria-checked="false" aria-label="Toggle ${name}" data-toggle="${id}"></button></div>`).join("")}</div><div id="service-panel" class="service-panel"></div>`
  );
}
function renderServicePanel() {
  const node = $("#service-panel");
  if (!node) return;
  const service = state.selected;
  const name = nodeLayout.find((n) => n[0] === service)?.[1] || service;
  node.innerHTML =
    `<div class="section-heading"><h2>${h(name)}</h2>${infoButton("service", service)}</div><div class="actions">${button("Logs", "service-logs", { service })}${button("Rebuild", "service-rebuild", { service })}${read("Environment", "config", { service })}</div>` +
    (service === "pvmbg-mock"
      ? `<div class="live-controls"><span class="subtle">Live source controls ${infoButton("source")}</span><div class="actions">${button("Schema v1", "source-schema", { version: "1" })}${button("Schema v2", "source-schema", { version: "2" })}${button("3 s delay", "source-delay", { preset: "slow" })}${button("Outage on", "source-outage", { enabled: "true" })}${button("Restore defaults", "restore-defaults")}</div></div>`
      : "");
  decorateCommandButtons();
  updateButtons();
}
function inspectPage() {
  return (
    heading("Inspect") +
    `<section class="panel"><div class="section-heading"><h2>Data</h2></div><div class="actions">${button("BMKG source", "source-read", { source: "BMKG", credential: "BMKG" })}${read("BMKG HazardEvents", "canonical", { source: "BMKG" })}${button("PVMBG source", "source-read", { source: "PVMBG", credential: "PVMBG" })}${read("PVMBG HazardEvents", "canonical", { source: "PVMBG" })}<button class="button" id="new-sample">New PVMBG sample</button>${read("Store & outbox", "store")}${read("Stream & consumers", "events")}</div><div class="field-row"><label>Identity<select id="api-identity"><option value="media">Media</option><option value="field-team">Field Team</option><option value="bnpb-internal">BNPB internal</option></select></label><label>Source<select id="api-source"><option>BMKG</option><option>PVMBG</option></select></label><button class="button" id="login-api">Log in</button><button class="button" id="read-api">Read API</button></div></section>` +
    `<section class="panel"><h2>Record history</h2><div class="field-row"><label>Source<select id="history-source"><option>PVMBG</option><option>BMKG</option></select></label><label class="grow">Source reference<input id="history-ref" value="${h(state.refs.PVMBG || "")}" placeholder="report_id / event_id" maxlength="64"></label><button class="button" id="read-history">Read v1 & v2</button></div></section>` +
    `<section class="panel"><h2>Configuration</h2><div class="actions">${read("Environment", "profile")}${read("Tracked config", "tracked-config")}${read("Containers", "containers")}</div><div class="field-row"><label>Wire schema<select id="schema-name"><option>hazard-event</option><option>hazard-envelope</option><option>hazard-summary</option></select></label><button class="button" id="read-schema">Read schema</button></div></section>`
  );
}
function runsPage() {
  return (
    heading("Runs") +
    `<div class="section-heading"><span class="subtle">Saved commands & output</span><button class="button" id="refresh-runs">Refresh</button></div><div id="run-list">Loading…</div>`
  );
}
function renderPage() {
  document.title = `${state.spec[state.page]?.title || titles[state.page]} · AAT`;
  document.querySelectorAll("[data-page]").forEach((a) => {
    if (a.dataset.page === state.page) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  });
  $("#main").innerHTML = state.spec[state.page]
    ? problemPage(state.page)
    : state.page === "overview"
      ? overview()
      : state.page === "inspect"
        ? inspectPage()
        : runsPage();
  if (state.page === "overview") {
    renderServices();
    renderServicePanel();
  }
  if (state.page === "runs") loadRuns();
  decorateCommandButtons();
  updateButtons();
}
function updateButtons() {
  document
    .querySelectorAll(
      "[data-action], [data-toggle], #pinned-history, #login-api, #read-api, #read-history, #read-schema, #new-sample, [data-open-run]",
    )
    .forEach((b) => (b.disabled = !!state.active || state.starting));
  $("#stop-command").hidden = !state.active;
  $("#command-input").disabled =
    !state.bootstrap || !!state.active || state.starting;
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
  const error = $("#status-error");
  if (error) {
    error.hidden = !state.statusError;
    error.textContent = state.statusError || "";
  }
  for (const [id] of nodeLayout) {
    const node = document.querySelector(`[data-node="${id}"]`);
    if (!node) continue;
    const c = state.containers.find((c) => c.service === id);
    const running = c?.state === "running";
    const health = c
      ? running
        ? c.health || "running"
        : "stopped"
      : state.statusError
        ? "unknown"
        : "not started";
    node.classList.toggle("healthy", health === "healthy");
    node.classList.toggle("unhealthy", health === "unhealthy");
    node.classList.toggle("selected", state.selected === id);
    node.querySelector("[data-health]").textContent = health;
    node
      .querySelector("[data-toggle]")
      .setAttribute("aria-checked", String(!!running));
  }
  document.querySelectorAll(".connection").forEach((edge) =>
    edge.classList.toggle(
      "on",
      [edge.dataset.from, edge.dataset.to].every((id) =>
        state.containers.some(
          (c) =>
            c.service === id && c.state === "running" && c.health === "healthy",
        ),
      ),
    ),
  );
}
function output(text) {
  terminal.write(text);
}
function resizeInput() {
  const input = $("#command-input");
  input.style.height = "0px";
  input.style.height = `${Math.min(input.scrollHeight, $("#terminal-panel").clientHeight * 0.4)}px`;
}
function clearTerminal() {
  terminal.reset();
}
function discardPending() {
  if (state.starting) return;
  state.preview++;
  state.pending = null;
  $("#command-input").value = "";
  resizeInput();
  $("#command-state").textContent = state.run?.status || "idle";
}
async function queueCommand(action, parameters = {}) {
  if (state.active || state.starting) {
    toast("Stop the current command first.");
    return;
  }
  parameters = { ...parameters, format: $("#json-format").value };
  const ticket = ++state.preview;
  try {
    const preview = await api("/api/commands/preview", {
      method: "POST",
      body: JSON.stringify({ action, parameters }),
    });
    if (ticket !== state.preview || state.active || state.starting) return;
    state.stream?.close();
    state.stream = null;
    state.pending = { action, parameters, ticket, script: preview.script };
    $("#command-input").value = preview.script;
    resizeInput();
    $("#command-state").textContent = "ready · Enter to run";
    $("#command-input").focus();
    const cursor = preview.script.includes("\n") ? 0 : preview.script.length;
    $("#command-input").setSelectionRange(cursor, cursor);
    $("#command-input").scrollTop = 0;
    toast("Command ready. Press Enter to run.");
  } catch (error) {
    if (ticket === state.preview) {
      await discardPending();
      toast(error.message);
    }
  }
}
async function executePending() {
  const script = $("#command-input").value;
  if (!script.trim() || state.active || state.starting) return;
  if (state.pending && state.pending.ticket !== state.preview) return;
  const pending = state.pending;
  state.starting = true;
  state.preview++;
  updateButtons();
  try {
    const run = await api("/api/runs", {
      method: "POST",
      body: JSON.stringify({
        action: pending?.action || "custom",
        parameters: pending?.parameters || {},
        script,
      }),
    });
    state.pending = null;
    $("#command-input").value = "";
    resizeInput();
    state.active = run.id;
    attachStream(run);
    terminal.focus();
  } catch (error) {
    if (pending) pending.ticket = state.preview;
    $("#command-state").textContent = "ready · Enter to retry";
    toast(error.message);
  } finally {
    state.starting = false;
    updateButtons();
  }
}
$("#command-input").addEventListener("input", () => {
  state.preview++;
  if (state.pending) state.pending.ticket = state.preview;
  $("#command-state").textContent = $("#command-input").value.trim()
    ? "ready · Enter to run"
    : state.run?.status || "idle";
  resizeInput();
});
$("#command-input").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    executePending();
  } else if (
    event.key === "Escape" ||
    (event.ctrlKey &&
      event.key.toLowerCase() === "c" &&
      event.target.selectionStart === event.target.selectionEnd)
  ) {
    event.preventDefault();
    discardPending();
  }
});
new ResizeObserver(resizeInput).observe($("#terminal-panel"));
function confirmService(service) {
  if (state.active || state.starting) return;
  const running = state.containers.some(
    (c) => c.service === service && c.state === "running",
  );
  state.switchCommand = {
    action: running ? "service-stop" : "service-start",
    parameters: { service },
  };
  $("#confirm-title").textContent = `${running ? "Stop" : "Start"} ${service}?`;
  $("#confirm-description").textContent =
    `Project: ${state.bootstrap.project}. The command will be placed in the terminal. Press Enter there to run it.`;
  $("#confirm-service").showModal();
}
function attachStream(run) {
  state.stream?.close();
  state.run = run;
  $("#command-state").textContent = run.status;
  $("#download-log").href = `/api/runs/${run.id}/output.log`;
  $("#download-log").hidden = false;
  const stream = new EventSource(`/api/runs/${run.id}/stream`);
  state.stream = stream;
  stream.addEventListener("output", (event) =>
    output(JSON.parse(event.data).text),
  );
  stream.addEventListener("complete", (event) => {
    const result = JSON.parse(event.data);
    stream.close();
    if (state.stream === stream) state.stream = null;
    state.run = result;
    if (
      result.action === "inspect" &&
      result.parameters?.kind === "mapping" &&
      result.result?.source_ref_id
    )
      state.refs[result.parameters.source || "PVMBG"] =
        result.result.source_ref_id;
    if (
      result.action === "inspect" &&
      result.parameters?.kind === "canonical"
    ) {
      const source = result.parameters.source || "PVMBG";
      const ref = result.result?.data?.[0]?.source_ref_id;
      if (ref && !state.refs[source]) state.refs[source] = ref;
      const input = $("#history-ref");
      if (source === "PVMBG" && input && !input.value)
        input.value = state.refs[source] || "";
    }
    if (state.active === result.id) state.active = "";
    $("#command-state").textContent = result.status;

    updateButtons();
    refreshStatus();
    if (state.page === "runs") loadRuns();
  });
  stream.onopen = () => {
    if (state.active === run.id) $("#command-state").textContent = "running";
  };
  stream.onerror = () => {
    if (state.stream === stream)
      $("#command-state").textContent = "reconnecting…";
  };
}
async function stopCommand() {
  if (!state.active) return;
  try {
    await api(`/api/runs/${state.active}/cancel`, {
      method: "POST",
      body: "{}",
    });
    $("#command-state").textContent = "stopping…";
  } catch (error) {
    toast(error.message);
  }
}
async function loadRuns() {
  try {
    const data = await api("/api/runs");
    const target = $("#run-list");
    if (!target) return;
    target.innerHTML = data.runs.length
      ? data.runs
          .map(
            (r) =>
              `<div class="run-row"><div><strong>${h(r.title)}</strong> <span class="status-${h(r.status)}">${h(r.status)}</span><div class="run-meta">${h(new Date(r.started).toLocaleString())} · ${h(r.revision?.slice(0, 8))}${r.dirty ? " + local changes" : ""}</div><div class="run-links"><a href="/api/runs/${r.id}/output.log">Log ↓</a>${r.has_result ? `<a href="/api/runs/${r.id}/result.json">JSON ↓</a>` : ""}<a href="/api/runs/${r.id}/run.json">Metadata ↓</a></div></div><button class="button" data-open-run="${r.id}">Open</button></div>`,
          )
          .join("")
      : '<p class="empty">No commands yet.</p>';
    updateButtons();
  } catch (error) {
    toast(error.message);
  }
}
function information(type, value) {
  if (type === "problem" || type === "criterion") {
    const spec = state.spec[state.page];
    const criteria =
      type === "criterion"
        ? spec.criteria.filter((c) => c.number === Number(value))
        : spec.criteria;
    const extra =
      type === "criterion" ? criterionNote(state.page, Number(value)) : "";
    showInfo(
      `${state.page.toUpperCase()} · ${spec.title}`,
      `<ol start="${criteria[0].number}">${criteria.map((c) => `<li value="${c.number}">${h(c.text)}</li>`).join("")}</ol>${extra ? `<p>${h(extra)}</p>` : ""}<p><a href="/api/spec/m1#page=${spec.page}" target="_blank" rel="noopener">M1 PDF · pp. ${spec.page}–${spec.page + 1} ↗</a></p>`,
    );
    return;
  }
  if (type === "map")
    showInfo(
      "Service map",
      "<p>Nodes show Docker container health. Arrows show configured polling, data and event paths; they are not connection probes.</p><p>The switch asks for confirmation, then places a command in the terminal. Enter runs it for only that service. Select its name for logs, rebuild and configuration.</p>",
    );
  if (type === "service") {
    const c = state.containers.find((c) => c.service === value);
    showInfo(
      value,
      c
        ? `<pre>${h(JSON.stringify(c, null, 2))}</pre>`
        : "<p>Container not created yet.</p>",
    );
  }
  if (type === "source")
    showInfo(
      "Live PVMBG controls",
      "<p>Schema, delay and outage changes take effect through the existing admin API, without a restart. Restore defaults applies the startup configuration.</p><p>Environment reads are masked. Edit .env locally to change startup configuration.</p>",
    );
}
function criterionNote(page, n) {
  const notes = {
    "p1:4":
      "Unknown fields are preserved in attributes. Schema v2 adds confidence_level; read the same mapped record after changing the schema.",
    "p1:5":
      "The implementation supports additive fields and preserves unknown JSON attributes. Renaming fields, changing required field types or removing required fields needs an adapter change.",
    "p2:1":
      "Set the delay, wait for a polling cycle, then compare simultaneous BMKG/PVMBG latency. Restore defaults afterward.",
    "p2:2":
      "Record load + TCP runs 60 workers for 90 seconds with a concurrent 115-second TCP collector. Read metrics and TCP samples separately. The full trial automatically checks the sustained TCP threshold.",
    "p3:1":
      "Each credential has its own accepted-source and rejected-source check. Compare the four HTTP statuses.",
    "p3:3":
      "Run the helpers left to right. Reads reuse the saved session; only Refresh token rotates it. The full trial additionally tests Auth restart and refresh replay.",
    "p3:4":
      "Tracked configuration shows tracked config paths and masked values. Review source code separately before claiming there are no hardcoded credentials.",
    "p4:1":
      "The full event trial also verifies continued API reads during an independent dashboard rebuild.",
    "p4:2":
      "Read PVMBG HazardEvents under schema v1 to pin a reference, or enter source_ref_id. Switch to schema v2, wait for a poll, then read versions 1 and 2 of that same record.",
    "p4:3":
      "PostgreSQL is on the store network with the Aggregator. Other services obtain canonical data through the Aggregator API.",
    "p5:3":
      "Stop dashboard, wait for new events, then read positions. Restart and read again to see catch-up. At-least-once delivery can redeliver events; consumer stores deduplicate them.",
    "p5:4":
      "The first Start third creates its durable; repeated starts reuse it. The full event trial creates a fresh temporary durable and also tests dashboard recovery/rebuild.",
  };
  return notes[`${page}:${n}`] || "";
}

document.addEventListener("click", async (event) => {
  const el = event.target.closest("button");
  if (!el) return;
  if (el.classList.contains("command-help")) {
    explainCommand(el.commandTarget);
    return;
  }
  if (el.dataset.action) {
    queueCommand(el.dataset.action, JSON.parse(el.dataset.parameters || "{}"));
    return;
  }
  if (el.dataset.toggle) {
    confirmService(el.dataset.toggle);
    return;
  }
  if (el.dataset.select) {
    state.selected = el.dataset.select;
    renderServices();
    renderServicePanel();
    return;
  }
  if (el.dataset.info) {
    information(el.dataset.info, el.dataset.value);
    return;
  }
  if (el.dataset.openRun) {
    if (state.active) {
      toast("Stop the current command first.");
      return;
    }
    try {
      const run = await api("/api/runs/" + el.dataset.openRun);
      await discardPending();
      clearTerminal();
      attachStream(run);
    } catch (error) {
      toast(error.message);
    }
    return;
  }
  switch (el.id) {
    case "cancel-service":
      $("#confirm-service").close();
      break;
    case "queue-service": {
      const command = state.switchCommand;
      $("#confirm-service").close();
      if (command) queueCommand(command.action, command.parameters);
      break;
    }
    case "close-info":
      $("#info-dialog").close();
      break;
    case "refresh-status":
      refreshStatus();
      break;
    case "refresh-runs":
      loadRuns();
      break;
    case "stop-command":
      stopCommand();
      break;
    case "clear-terminal":
      if (state.starting) break;
      await discardPending();
      clearTerminal();
      break;
    case "copy-terminal": {
      const buffer = terminal.buffer.active;
      const lines = [];
      for (let i = 0; i < buffer.length; i++)
        lines.push(buffer.getLine(i).translateToString(true));
      try {
        await navigator.clipboard.writeText(lines.join("\n").trimEnd());
        toast("Copied.");
      } catch {
        toast("Select terminal output and copy it.");
      }
      break;
    }
    case "expand-terminal": {
      const expanded = document.body.classList.toggle("terminal-only");
      el.setAttribute("aria-pressed", String(expanded));
      el.setAttribute(
        "aria-label",
        expanded ? "Restore split layout" : "Expand terminal",
      );
      break;
    }
    case "new-sample":
      delete state.refs.PVMBG;
      queueCommand("inspect", { kind: "canonical", source: "PVMBG" });
      break;
    case "login-api":
      queueCommand("client-login", { identity: $("#api-identity").value });
      break;
    case "read-api":
      queueCommand("inspect", {
        kind: "api",
        source: $("#api-source").value,
        identity: $("#api-identity").value,
      });
      break;
    case "read-history":
      queueCommand("inspect", {
        kind: "history",
        source: $("#history-source").value,
        ref: $("#history-ref").value.trim(),
        revisions: "1,2",
      });
      break;
    case "read-schema":
      queueCommand("inspect", {
        kind: "schema",
        name: $("#schema-name").value,
      });
      break;
    case "pinned-history": {
      const ref = $("#history-ref")?.value.trim() || state.refs.PVMBG;
      if (!ref) {
        toast("Read PVMBG HazardEvents first, or enter a source reference.");
        return;
      }
      queueCommand("inspect", {
        kind: "history",
        source: "PVMBG",
        ref,
        revisions: "1,2",
      });
      break;
    }
  }
});
$("#confirm-service").addEventListener("close", () => {
  state.switchCommand = null;
});
window.addEventListener("hashchange", () => {
  const page = location.hash.slice(1);
  state.page = state.spec[page] || titles[page] ? page : "overview";
  renderPage();
  $("#main").scrollTop = 0;
});
async function boot() {
  try {
    [state.bootstrap, state.spec] = await Promise.all([
      api("/api/bootstrap"),
      api("/specification.json"),
    ]);
    $("#project").textContent = state.bootstrap.project;
    output(
      `cwd: ${state.bootstrap.repo}\nenv: ${state.bootstrap.env_file}\nproject: ${state.bootstrap.project}\n`,
    );
    const page = location.hash.slice(1);
    state.page = state.spec[page] || titles[page] ? page : "overview";
    renderPage();
    refreshStatus();
    const runs = await api("/api/runs");
    if (runs.active) {
      const run = await api("/api/runs/" + runs.active);
      state.active = run.id;
      attachStream(run);
      updateButtons();
    }
    setInterval(() => {
      if (!document.hidden) refreshStatus();
    }, 10000);
  } catch (error) {
    $("#main").innerHTML = `<p class="error">${h(error.message)}</p>`;
  }
}
boot();
