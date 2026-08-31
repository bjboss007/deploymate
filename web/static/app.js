// DeployMate dashboard behaviors. Kept tiny: htmx handles forms, a plain
// EventSource streams logs (the vendored htmx core has no SSE support),
// Chart.js draws the two metric charts.
document.body.addEventListener("htmx:afterSwap", (e) => {
  // Keep live log panels pinned to the bottom as events stream in.
  if (e.target && e.target.id === "logs") {
    e.target.scrollTop = e.target.scrollHeight;
  }
});

// --- live logs via EventSource -------------------------------------------
// Batched + capped: a crash-looping container emits a firehose of boot
// logs — appending one DOM node per line, unbounded, is what made pages
// hang. Lines are buffered, flushed once per frame, and the panel keeps
// at most MAX_LOG_LINES nodes.
const MAX_LOG_LINES = 400;

function attachLogStream(panel) {
  const src = panel.dataset.logSrc;
  if (!src) return;

  const es = new EventSource(src);
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
const CHART_COLORS = { cpu: "#ffb224", mem: "#3ecf8e" };

function lineChart(id, label, color) {
  const ctx = document.getElementById(id);
  if (!ctx) return null;
  return new Chart(ctx, {
    type: "line",
    data: { labels: [], datasets: [{ label, data: [], borderColor: color, backgroundColor: color + "22", fill: true, tension: 0.3, pointRadius: 0, borderWidth: 1.5 }] },
    options: {
      animation: false,
      plugins: { legend: { display: false } },
      scales: {
        x: { ticks: { display: false }, grid: { display: false } },
        y: { beginAtZero: true, ticks: { color: "#565e70", font: { family: "IBM Plex Mono", size: 10 } }, grid: { color: "#1f2430" } },
      },
    },
  });
}

function fmtTime(ts) {
  const d = new Date(ts);
  return d.toTimeString().slice(0, 8);
}

async function loadMetrics(appSlug) {
  const cpu = lineChart("cpu-chart", "CPU %", CHART_COLORS.cpu);
  const mem = lineChart("mem-chart", "Memory MB", CHART_COLORS.mem);
  if (!cpu && !mem) return; // not the app page

  const resp = await fetch(`/apps/${appSlug}/metrics`);
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
  loadFleetChart();

  const slug = document.body.dataset.appSlug;
  if (slug) loadMetrics(slug);
});
