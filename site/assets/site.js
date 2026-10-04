// DeployMate site — small and dependency-free. Every feature degrades to the
// plain page: no script, no problem (the board just shows no strips).
(function () {
  "use strict";
  var reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };

  // ---- theme ------------------------------------------------------------
  var tbtn = $("[data-theme-toggle]");
  if (tbtn) tbtn.addEventListener("click", function () {
    var next = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
    document.documentElement.setAttribute("data-theme", next);
    try { localStorage.setItem("dm-site-theme", next); } catch (e) {}
  });

  // ---- copy buttons -------------------------------------------------------
  $$(".copy").forEach(function (b) {
    b.addEventListener("click", function () {
      var el = document.getElementById(b.getAttribute("data-copy"));
      if (!el || !navigator.clipboard) return;
      navigator.clipboard.writeText(el.textContent.trim()).then(function () {
        var t = b.textContent; b.textContent = "Copied"; b.classList.add("done");
        setTimeout(function () { b.textContent = t; b.classList.remove("done"); }, 1600);
      });
    });
  });

  // ---- the board: a fleet in miniature ------------------------------------
  // 24 hourly cells, newest on the right. u up · d down · s stopped; an
  // uppercase letter marks an hour in which something was deployed. The shape
  // matches the demo fleet in the tour (internal/demo).
  var apps = [
    { name: "web",      tile: "Re", c: "var(--sky)",    st: "up",   sub: "Running · d94be57 · 2 h ago",
      cells: "UuuuuuuuuUuuuuUuuuuuuUuu" },
    { name: "api",      tile: "Sp", c: "var(--teal)",   st: "up",   sub: "Running · prebuilt · run #228",
      cells: "uUuuuuuuuuuuUuuuDuuuuuuu" },
    { name: "invoicer", tile: "Nx", c: "var(--violet)", st: "down", attn: true, sub: "Running, but failing its health check.",
      cells: "uuuuuUuuuuuuuuuuuuuuuuud" }
  ];
  var rows = $("#rows");
  function pct(cells) {
    var up = 0, known = 0;
    cells.toLowerCase().split("").forEach(function (ch) { if (ch !== "n") { known++; if (ch === "u") up++; } });
    return known ? (up / known * 100).toFixed(1) : "—";
  }
  if (rows) {
    apps.forEach(function (a) {
      var cells = a.cells.slice(0, 24);
      var row = document.createElement("div");
      row.className = "row" + (a.attn ? " attn" : "");
      var strip = cells.split("").map(function (ch, i) {
        var s = ch.toLowerCase() === "u" ? "up" : ch.toLowerCase() === "d" ? "down" : "stopped";
        var cls = "hb " + s + (ch === ch.toUpperCase() ? " deploy" : "") + (i === 23 ? " live" : "");
        return '<span class="' + cls + '" style="--i:' + i + '"></span>';
      }).join("");
      row.innerHTML =
        '<span class="lamp ' + a.st + '" aria-hidden="true"></span>' +
        '<span class="name"><span class="tile" style="--c:' + a.c + '">' + a.tile + '</span><span>' + a.name +
        '<span class="sub' + (a.attn ? " bad" : "") + '">' + a.sub + '</span></span></span>' +
        '<span class="strip" role="img" aria-label="Last 24 hours, ' + pct(cells) + ' percent up">' + strip + '</span>' +
        '<span class="pct">' + pct(cells) + '</span>';
      rows.appendChild(row);
    });
    var strips = $$(".strip", rows);
    if (reduce) strips.forEach(function (s) { s.classList.add("in"); });
    else requestAnimationFrame(function () { setTimeout(function () { strips.forEach(function (s) { s.classList.add("in"); }); }, 250); });
  }

  // ---- the deploy log: the worker's real phrasing ---------------------------
  var log = [
    ["sys", "cloning https://github.com/acme-demo/storefront-web (main)"],
    ["hl",  "building commit d94be57c3a10 — Lazy-load the product gallery"],
    ["sys", "railpack build with runtime Node.js 22"],
    ["",    "#5 [build] npm run build"],
    ["",    "✓ 482 modules transformed."],
    ["",    "dist/assets/index-9d2f1c.js   214.07 kB │ gzip: 68.30 kB"],
    ["ok",  "▸ deployed ✓"]
  ];
  var linesEl = $("#term-lines"), timers = [];
  function play() {
    if (!linesEl) return;
    timers.forEach(clearTimeout); timers = [];
    linesEl.innerHTML = "";
    log.forEach(function (l, i) {
      var add = function () {
        var d = document.createElement("div");
        d.className = "l " + l[0]; d.textContent = l[1];
        linesEl.appendChild(d);
      };
      if (reduce) add(); else timers.push(setTimeout(add, 500 + i * 620));
    });
  }
  play();
  var rp = $("#replay"); if (rp) rp.addEventListener("click", play);

  // ---- tour ----------------------------------------------------------------
  var frame = $("#tour-frame"), urlEl = $("#tour-url"), capEl = $("#tour-cap");
  var tabs = $$(".tour-tabs button");
  tabs.forEach(function (b, idx) {
    b.addEventListener("click", function () { select(b); b.focus(); });
    b.addEventListener("keydown", function (e) {
      var n = e.key === "ArrowRight" ? idx + 1 : e.key === "ArrowLeft" ? idx - 1 : null;
      if (n === null) return;
      e.preventDefault(); var t = tabs[(n + tabs.length) % tabs.length]; select(t); t.focus();
    });
  });
  function select(b) {
    tabs.forEach(function (t) { t.setAttribute("aria-selected", t === b ? "true" : "false"); t.tabIndex = t === b ? 0 : -1; });
    frame.src = b.getAttribute("data-src");
    urlEl.textContent = b.getAttribute("data-url");
    capEl.innerHTML = b.getAttribute("data-cap");
  }
  tabs.forEach(function (t, i) { t.tabIndex = i === 0 ? 0 : -1; });
  // keep the snapshot's own links inside the tour
  if (frame) frame.addEventListener("load", function () {
    try {
      var path = frame.contentWindow.location.pathname.split("/").pop();
      tabs.forEach(function (t) {
        if (t.getAttribute("data-src").split("/").pop() === path) {
          tabs.forEach(function (x) { x.setAttribute("aria-selected", x === t ? "true" : "false"); x.tabIndex = x === t ? 0 : -1; });
          urlEl.textContent = t.getAttribute("data-url"); capEl.innerHTML = t.getAttribute("data-cap");
        }
      });
    } catch (e) {}
  });
})();
