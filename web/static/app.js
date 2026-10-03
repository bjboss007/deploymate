// DeployMate dashboard behaviors. Kept tiny: htmx handles forms, a plain
// EventSource streams logs (the vendored htmx core has no SSE support),
// Chart.js draws the two metric charts.
document.body.addEventListener("htmx:afterSwap", (e) => {
  // Keep live log panels pinned to the bottom as events stream in.
  if (e.target && e.target.id === "logs") {
    e.target.scrollTop = e.target.scrollHeight;
  }
});

// --- copy-to-clipboard --------------------------------------------------
// Generic: any [data-copy="elementId"] button copies that element's text.
// Falls back to a prompt on non-secure origins (plain-http tunnels), where
// the clipboard API is unavailable.
document.body.addEventListener("click", (e) => {
  const btn = e.target.closest(".js-copy");
  if (!btn) return;
  const src = document.getElementById(btn.dataset.copy || "");
  if (!src) return;
  const text = (src.innerText || src.textContent).trim();
  const done = () => {
    const old = btn.textContent;
    btn.textContent = "Copied";
    btn.disabled = true;
    setTimeout(() => { btn.textContent = old; btn.disabled = false; }, 1400);
  };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done).catch(() => {});
  } else {
    window.prompt("Copy this value:", text); // not a secure context — let the owner copy manually
    done();
  }
});

// --- restore confirmation ------------------------------------------------
// Restore is destructive (the database is dropped and recreated), so the
// prompt asks the owner to TYPE the service name. The slug comes from the
// form's own action URL; the server re-checks the confirm field on POST —
// this prompt is UX, not security.
function confirmRestore(form) {
  const m = (form.action || "").match(/\/services\/([^/]+)\/backups\/restore/);
  const slug = m ? m[1] : "";
  if (!slug) return false;
  const answer = prompt('Type "' + slug + '" to confirm restoring this database from the backup. The database is dropped and recreated.');
  if (answer === null) return false;
  if (answer.trim() !== slug) {
    alert("Restore cancelled — the name did not match.");
    return false;
  }
  const input = document.createElement("input");
  input.type = "hidden";
  input.name = "confirm";
  input.value = slug;
  form.appendChild(input);
  return true;
}

// --- live logs via EventSource -------------------------------------------
// Batched + capped: a crash-looping container emits a firehose of boot
// logs — appending one DOM node per line, unbounded, is what made pages
// hang. Lines are buffered, flushed once per frame, and the panel keeps
// at most MAX_LOG_LINES nodes.
const MAX_LOG_LINES = 400;
const LOG_ERR = /\b(error|exception|fatal|failed|denied|refused|killed|panic)\b/i;

function attachLogStream(panel) {
  const src = panel.dataset.logSrc;
  if (!src) return;

  if (panel._logStream) panel._logStream.close(); // re-attach (replica filter)
  const es = new EventSource(src);
  panel._logStream = es;
  let gotFirst = false;
  let pending = [];
  let flushScheduled = false;

  const flush = () => {
    flushScheduled = false;
    if (pending.length === 0) return;
    if (!gotFirst) {
      panel.replaceChildren(); // clear the "connecting…" placeholder
      gotFirst = true;
    }
    const frag = document.createDocumentFragment();
    for (const text of pending) {
      const div = document.createElement("div");
      div.textContent = text; // textContent: build logs are data, not HTML
      if (LOG_ERR.test(text)) div.className = "log-err";
      frag.appendChild(div);
    }
    panel.appendChild(frag);
    while (panel.children.length > MAX_LOG_LINES) {
      panel.removeChild(panel.firstChild);
    }
    panel.scrollTop = panel.scrollHeight;
    pending = [];
  };

  const scheduleFlush = () => {
    if (flushScheduled) return;
    flushScheduled = true;
    requestAnimationFrame(flush);
  };

  const appendLine = (text) => {
    pending.push(text);
    scheduleFlush();
  };

  es.addEventListener("log", (e) => appendLine(e.data));
  es.addEventListener("deploy", (e) => appendLine("▸ " + e.data));
  es.onmessage = (e) => appendLine(e.data); // unnamed events, if any
}

// --- metrics charts -----------------------------------------------------
// Chart colours come from the theme tokens (so light and dark both work) and
// are re-applied when the theme is toggled.
const cssVar = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const CHART_COLORS = { cpu: "--accent", mem: "--green" };
const liveCharts = [];

function themeChart(chart, colorVar) {
  const color = cssVar(colorVar);
  const ds = chart.data.datasets[0];
  ds.borderColor = color;
  ds.backgroundColor = color + "22";
  chart.options.scales.y.ticks.color = cssVar("--faint");
  chart.options.scales.y.grid.color = cssVar("--border");
  chart.update("none");
}

function lineChart(id, label, color) {
  const ctx = document.getElementById(id);
  if (!ctx) return null;
  const chart = new Chart(ctx, {
    type: "line",
    data: { labels: [], datasets: [{ label, data: [], borderColor: color, backgroundColor: color + "22", fill: true, tension: 0.3, pointRadius: 0, borderWidth: 1.5 }] },
    options: {
      animation: false,
      plugins: { legend: { display: false } },
      scales: {
        x: { ticks: { display: false }, grid: { display: false } },
        y: { beginAtZero: true, ticks: { color: "#66738a", font: { family: "IBM Plex Mono", size: 10 } }, grid: { color: "#243040" } },
      },
    },
  });
  liveCharts.push({ chart, colorVar: color });
  themeChart(chart, color);
  return chart;
}

function fmtTime(ts) {
  const d = new Date(ts);
  return d.toTimeString().slice(0, 8);
}

// Charts are created once and refilled on filter changes (Chart.js refuses
// a second chart on the same canvas).
let metricsCharts = null;

async function loadMetrics(appSlug, replica) {
  if (!metricsCharts) {
    metricsCharts = {
      cpu: lineChart("cpu-chart", "CPU %", CHART_COLORS.cpu),
      mem: lineChart("mem-chart", "Memory MB", CHART_COLORS.mem),
    };
  }
  const { cpu, mem } = metricsCharts;
  if (!cpu && !mem) return; // not the app page

  const q = replica ? "?replica=" + encodeURIComponent(replica) : "";
  const resp = await fetch(`/apps/${appSlug}/metrics${q}`);
  if (!resp.ok) return;
  const data = await resp.json();

  if (cpu) {
    cpu.data.labels = data.cpu.map((p) => fmtTime(p.t));
    cpu.data.datasets[0].data = data.cpu.map((p) => p.v);
    cpu.update();
  }
  if (mem) {
    mem.data.labels = data.mem.map((p) => fmtTime(p.t));
    mem.data.datasets[0].data = data.mem.map((p) => p.v);
    mem.update();
  }
}

// --- fleet stats chart --------------------------------------------------
function loadFleetChart() {
  const chart = lineChart("fleet-deploys-chart", "Deploys", CHART_COLORS.cpu);
  const dataEl = document.getElementById("fleet-deploys-data");
  if (!chart || !dataEl) return; // not the stats page
  let days = [];
  try {
    days = JSON.parse(dataEl.textContent);
  } catch (_) {
    return;
  }
  chart.data.labels = days.map((d) => d.day);
  chart.data.datasets[0].data = days.map((d) => d.count);
  chart.update();
}

document.addEventListener("DOMContentLoaded", () => {
  document.querySelectorAll("[data-log-src]").forEach(attachLogStream);
  // Replica filter: narrow a merged replica log stream to one slot
  // (?replica=r2) by re-attaching the panel to the filtered source.
  document.querySelectorAll("[data-log-filter]").forEach((sel) => {
    const panel = document.getElementById(sel.dataset.logFilter);
    if (!panel) return;
    const base = panel.dataset.logSrc;
    sel.addEventListener("change", () => {
      panel.dataset.logSrc = sel.value ? base + "?replica=" + encodeURIComponent(sel.value) : base;
      panel.replaceChildren();
      attachLogStream(panel);
    });
  });
  loadFleetChart();

  const slug = document.body.dataset.appSlug;
  if (slug) {
    loadMetrics(slug);
    // Replica filter: total across replicas, or one replica's samples.
    document.querySelectorAll("[data-metrics-filter]").forEach((sel) => {
      sel.addEventListener("change", () => loadMetrics(slug, sel.value));
    });
  }
});


// --- environment variable rows ------------------------------------------
// "+ Add variable" appends a row (key_N / value_N / secret_N with the next
// index) to the form; Save posts every row at once. The first row can't be
// removed away entirely: removing the last row just clears it.
(() => {
  const form = document.querySelector("[data-env-rows]");
  if (!form) return;
  const list = form.querySelector("[data-env-rows-list]");
  let next = list.children.length;
  const addRow = () => {
    const i = next++;
    const row = document.createElement("div");
    row.className = "env-row";
    row.innerHTML =
      '<input type="text" name="key_' + i + '" placeholder="KEY" pattern="[A-Za-z_][A-Za-z0-9_]*" title="Letters, digits, underscores \u2014 starting with a letter" autocomplete="off" spellcheck="false"/>' +
      '<input type="text" name="value_' + i + '" placeholder="value" autocomplete="off" spellcheck="false"/>' +
      '<label class="checkbox"><input type="checkbox" name="secret_' + i + '"/> secret</label>' +
      '<button class="btn btn-ghost btn-sm" type="button" data-env-remove aria-label="Remove row">\u00d7</button>';
    list.appendChild(row);
    row.querySelector("input").focus();
  };
  form.addEventListener("click", (e) => {
    if (e.target.closest("[data-env-add]")) { addRow(); return; }
    const rm = e.target.closest("[data-env-remove]");
    if (!rm) return;
    const row = rm.closest(".env-row");
    if (list.children.length > 1) row.remove();
    else row.querySelectorAll("input[type=text]").forEach((i) => { i.value = ""; });
  });
})();


// --- theme toggle --------------------------------------------------------
// Dark is the default; "light" is remembered in localStorage and applied by
// an inline script in <head> before first paint.
document.addEventListener("click", (e) => {
  if (!e.target.closest("[data-theme-toggle]")) return;
  const root = document.documentElement;
  const light = root.getAttribute("data-theme") !== "light";
  if (light) root.setAttribute("data-theme", "light"); else root.removeAttribute("data-theme");
  try { localStorage.setItem("dm-theme", light ? "light" : "dark"); } catch (_) {}
  liveCharts.forEach((c) => themeChart(c.chart, c.colorVar));
});


// --- app page tabs -------------------------------------------------------
// Overview / Deployments / Logs / Variables / Settings. Server-rendered as
// stacked sections (works without JS); here they become tabs. The active tab
// follows the URL hash. Forms redirect back to the bare page with a flash, so
// the last-open tab is remembered per app and restored when a flash is
// showing — saving a variable lands you back on Variables, not Overview.
(() => {
  const nav = document.querySelector("[data-tabs]");
  if (!nav) return;
  const root = document.documentElement;
  const slug = document.body.dataset.appSlug || "";
  const key = "dm-tab:" + slug;
  const names = [...nav.querySelectorAll("[data-tab-btn]")].map((b) => b.dataset.tabBtn);
  const panels = Object.fromEntries(names.map((n) => [n, document.getElementById("tab-" + n)]));
  if (names.some((n) => !panels[n])) return; // markup mismatch: leave the stacked layout alone

  const settingsAnchors = [...document.querySelectorAll(".settings-group")].map((g) => g.id);
  const store = {
    get: () => { try { return sessionStorage.getItem(key); } catch (_) { return null; } },
    set: (v) => { try { sessionStorage.setItem(key, v); } catch (_) {} },
  };
  const show = (name, { scroll = false, anchor = "" } = {}) => {
    if (!panels[name]) name = "overview";
    root.classList.add("tabs-on");
    names.forEach((n) => {
      const on = n === name;
      panels[n].classList.toggle("is-active", on);
      const btn = nav.querySelector('[data-tab-btn="' + n + '"]');
      btn.classList.toggle("is-active", on);
      btn.setAttribute("aria-current", on ? "page" : "false");
    });
    store.set(name);
    // Charts sized while hidden need a nudge; the log stream follows the tail.
    window.dispatchEvent(new Event("resize"));
    if (name === "logs") { const l = document.querySelector("[data-log-src]"); if (l) l.scrollTop = l.scrollHeight; }
    if (anchor) document.getElementById(anchor)?.scrollIntoView({ block: "start" });
    else if (scroll) window.scrollTo({ top: 0 });
  };
  const fromHash = () => {
    const h = decodeURIComponent(location.hash.replace(/^#/, ""));
    if (names.includes(h)) return { name: h };
    if (settingsAnchors.includes(h)) return { name: "settings", anchor: h };
    return null;
  };

  const hasFlash = !!document.querySelector(".flash");
  const initial = fromHash() || (hasFlash && store.get() ? { name: store.get() } : { name: "overview" });
  show(initial.name, { anchor: initial.anchor });

  // Tab buttons and in-page links ([data-tab-link]) switch without a jump.
  document.addEventListener("click", (e) => {
    const link = e.target.closest("[data-tab-btn], [data-tab-link], .settings-nav a");
    if (!link) return;
    const href = link.getAttribute("href") || "";
    if (!href.startsWith("#")) return;
    const target = decodeURIComponent(href.slice(1));
    const dest = names.includes(target) ? { name: target } : settingsAnchors.includes(target) ? { name: "settings", anchor: target } : null;
    if (!dest) return;
    e.preventDefault();
    history.replaceState(null, "", href);
    show(dest.name, { scroll: !dest.anchor, anchor: dest.anchor });
  });
  window.addEventListener("hashchange", () => { const d = fromHash(); if (d) show(d.name, { anchor: d.anchor }); });
})();


// --- disclosure forms ("New app", "New project", …) -----------------------
// Opening one focuses its first field; Escape closes it.
document.addEventListener("toggle", (e) => {
  const d = e.target;
  if (d.classList && d.classList.contains("disclose") && d.open) d.querySelector("input, select")?.focus();
}, true);
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  document.querySelectorAll("details.disclose[open]").forEach((d) => { d.open = false; });
});


// --- "Jump to first error" on the deployment log --------------------------
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-log-jump]");
  if (!btn) return;
  const panel = document.getElementById(btn.dataset.logJump);
  if (!panel) return;
  panel.querySelectorAll(".log-hit").forEach((n) => n.classList.remove("log-hit"));
  const hit = [...panel.children].find((n) => n.classList && n.classList.contains("log-err"));
  if (!hit) { btn.textContent = "No errors in the log"; setTimeout(() => { btn.textContent = "Jump to first error"; }, 1800); return; }
  hit.classList.add("log-hit");
  panel.scrollTop = Math.max(0, hit.offsetTop - panel.offsetTop - 40);
});
