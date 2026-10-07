"use strict";
const $ = (id) => document.getElementById(id);
let running = false;
let showLog = false;
let logHidden = false; // hidden by hand: stays hidden until the next start
const openRuns = new Set();   // runs whose details are shown, by start time
let timer;
// gen changes on "Update now": a status asked for before the click is stale.
let gen = 0;

function renderStatus(s) {
  if (s.running && !running) logHidden = false; // a new run, from here or the schedule
  running = s.running;
  $("host").textContent = s.host;
  document.title = "tgbup · " + s.host;

  // Until the required settings are made, nothing is archived: say so first.
  const left = s.setup_left || [];
  $("setup-left").hidden = !left.length;
  $("setup-left").replaceChildren(
    el("h2", {}, "Setup is not finished — nothing is being archived yet"),
    el("ul", {}, left.map((p) => el("li", {}, p))),
    el("a", { href: "/settings" }, el("button", { type: "button" }, "Continue setup")));
  $("settings-tab").classList.toggle("todo", left.length > 0);
  const problems = $("problems");
  problems.replaceChildren(...(s.problems || []).map((p) => el("li", {}, p + " ", el("a", { href: "/settings" }, "Open Settings"))));
  problems.hidden = !(s.problems || []).length;

  const last = (s.runs || [])[0];
  const lr = $("last-run"), detail = $("last-run-detail"), errs = $("last-run-errors");
  errs.replaceChildren();
  if (!last) {
    lr.textContent = "No runs yet";
    lr.className = "big muted";
    detail.textContent = "";
  } else {
    lr.textContent = last.ok ? "OK · " + ago(last.finished) : "Errors · " + ago(last.finished);
    lr.className = "big " + (last.ok ? "ok" : "bad");
    detail.textContent = when(last.started) + " · " + plural(last.new_messages, "new message") + ", " + plural(last.new_media, "new file");
    errs.replaceChildren(...(last.errors || []).slice(0, 8).map((e) => el("li", {}, e)));
  }
  // An empty archive starts with the first download, not an update.
  const empty = !(s.archive.messages > 0);
  $("first-run").hidden = !empty || !!(s.setup_left || []).length;
  $("run-now").hidden = empty;
  $("first-takeout").disabled = $("first-plain").disabled = running;
  $("run-now").disabled = running;
  $("run-state").textContent = running ? " running…" : "";

  const a = s.archive;
  $("archive").textContent = plural(a.messages, "message");
  let extra = plural(a.chats, "chat") + " · " + plural(a.files, "file") + " · " + bytes(a.bytes);
  if (a.pending_files) extra += " · " + num(a.pending_files) + " files still to download";
  if (a.failed_files) extra += " · " + num(a.failed_files) + " files could not be downloaded";
  $("archive-detail").textContent = extra;
  $("open-archive").hidden = !a.site;

  $("backups").replaceChildren(...(s.backups || []).map((b, i) => {
    const old = isZero(b.last_success) || Date.now() - new Date(b.last_success) > 2 * 86400e3;
    return el("li", {}, el("span", { class: old ? "bad" : "ok" }, "● "), b.repo, el("br"),
      el("span", { class: "muted" }, (isZero(b.last_success) ? "no snapshot yet" : "up to date " + ago(b.last_success)) + " · "),
      el("a", { href: "/snapshots#place-" + i }, "snapshots"));
  }));
  if (!(s.backups || []).length) $("backups").replaceChildren(
    el("li", {}, "Off: the archive is kept as files in " + s.archive.dir),
    el("li", { class: "muted" }, "tgbup init can add encrypted backups to iCloud or Google Drive"));

  const sc = s.schedule;
  if (sc.note) $("schedule").textContent = sc.note;
  else if (sc.installed) $("schedule").replaceChildren(el("div", {}, "Daily at " + sc.times.join(", ")),
    el("div", { class: "muted" }, "next " + when(sc.next)));
  else $("schedule").replaceChildren(el("span", { class: "bad" }, "Not installed"));
  $("alerts").textContent = s.bot ? "Alerts go to your Telegram bot" : "No alerts bot";

  $("runs").replaceChildren(...(s.runs || []).flatMap((r) => {
    const took = Math.max(0, (new Date(r.finished) - new Date(r.started)) / 1000);
    const details = (r.errors || []).length + (r.log || []).length > 0;
    const shown = openRuns.has(r.started);
    const result = r.ok ? "OK" : plural((r.errors || []).length, "error");
    const row = el("tr", { class: details ? "clickable" : "" },
      el("td", {}, when(r.started)),
      el("td", {}, took < 90 ? Math.round(took) + " s" : Math.round(took / 60) + " min"),
      el("td", { class: r.ok ? "ok" : "bad" }, details ? el("button", { type: "button", class: "link",
        onclick: () => { shown ? openRuns.delete(r.started) : openRuns.add(r.started); renderStatus(s); } }, (shown ? "▾ " : "▸ ") + result) : result),
      el("td", { class: "num" }, num(r.new_messages)),
      el("td", { class: "num" }, num(r.new_media)),
      el("td", { class: "muted" }, r.snapshot || ""));
    if (!shown) return [row];
    return [row, el("tr", { class: "details" }, el("td", { colspan: 6 },
      (r.errors || []).length ? el("ul", { class: "errors" }, r.errors.map((e) => el("li", {}, e))) : null,
      (r.log || []).length ? el("pre", { class: "log" }, r.log.join("\n")) : null))];
  }));
}

async function refresh() {
  const asked = gen;
  try {
    const s = await api("GET", "/api/status");
    if (asked !== gen) return;
    renderStatus(s);
  } catch (e) {
    $("problems").replaceChildren(el("li", {}, "Could not read the status: " + e.message));
    $("problems").hidden = false;
  }
  if ((running || showLog) && !logHidden) await refreshLog();
  else $("log-box").hidden = true;
  $("log-show").hidden = !(running && logHidden);
  clearTimeout(timer);
  timer = setTimeout(refresh, running ? 3000 : 30000);
}

async function refreshLog() {
  $("log-box").hidden = false;
  $("log-note").textContent = running ? "· running" : "· finished";
  const log = $("log");
  const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
  try { log.textContent = await api("GET", "/api/run/log"); } catch (e) { log.textContent = e.message; }
  if (atBottom) log.scrollTop = log.scrollHeight;
}

$("log-hide").addEventListener("click", () => {
  logHidden = true;
  showLog = false;
  $("log-box").hidden = true;
  $("log-show").hidden = !running;
});
$("log-show").addEventListener("click", () => { logHidden = false; showLog = true; refresh(); });

$("open-archive").addEventListener("click", async () => {
  try { await api("POST", "/api/archive/open"); } catch (e) { toast("Could not open the archive: " + e.message); }
});

$("first-takeout").addEventListener("click", () => startRun("/api/run?takeout=1"));
$("first-plain").addEventListener("click", () => startRun("/api/run"));
$("run-now").addEventListener("click", () => startRun("/api/run"));

async function startRun(path) {
  $("run-now").disabled = $("first-takeout").disabled = $("first-plain").disabled = true;
  gen++;
  try {
    await api("POST", path);
  } catch (e) {
    if (e.status !== 409) {
      toast("Could not start: " + e.message);
      $("run-now").disabled = $("first-takeout").disabled = $("first-plain").disabled = false;
      return;
    }
    toast("A run is already in progress");
  }
  running = true;
  showLog = true;
  logHidden = false;
  $("run-state").textContent = " running…";
  clearTimeout(timer);
  timer = setTimeout(refresh, 1500);
}

refresh();
