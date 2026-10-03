"use strict";
const $ = (id) => document.getElementById(id);
let running = false;
let showLog = false;
let timer;
// gen changes on "Update now": a status asked for before the click is stale.
let gen = 0;

function renderStatus(s) {
  running = s.running;
  $("host").textContent = s.host;
  document.title = "tgbup · " + s.host;

  const problems = $("problems");
  problems.replaceChildren(...(s.problems || []).map((p) => el("li", {}, p + " ", el("a", { href: "/setup" }, "Open Setup"))));
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
  $("run-now").disabled = running;
  $("run-state").textContent = running ? " running…" : "";

  const a = s.archive;
  $("archive").textContent = plural(a.messages, "message");
  let extra = plural(a.chats, "chat") + " · " + plural(a.files, "file") + " · " + bytes(a.bytes);
  if (a.pending_files) extra += " · " + num(a.pending_files) + " files still to download";
  if (a.failed_files) extra += " · " + num(a.failed_files) + " files could not be downloaded";
  $("archive-detail").textContent = extra;
  $("archive-link").hidden = !a.site;

  $("backups").replaceChildren(...(s.backups || []).map((b) => {
    const old = isZero(b.last_success) || Date.now() - new Date(b.last_success) > 2 * 86400e3;
    return el("li", {}, el("span", { class: old ? "bad" : "ok" }, old ? "● " : "● "), b.repo, el("br"),
      el("span", { class: "muted" }, "last snapshot " + ago(b.last_success)));
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

  $("runs").replaceChildren(...(s.runs || []).map((r) => {
    const took = Math.max(0, (new Date(r.finished) - new Date(r.started)) / 1000);
    return el("tr", {},
      el("td", {}, when(r.started)),
      el("td", {}, took < 90 ? Math.round(took) + " s" : Math.round(took / 60) + " min"),
      el("td", { class: r.ok ? "ok" : "bad", title: (r.errors || []).join("\n") }, r.ok ? "OK" : plural((r.errors || []).length, "error")),
      el("td", { class: "num" }, num(r.new_messages)),
      el("td", { class: "num" }, num(r.new_media)));
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
  if (running || showLog) await refreshLog();
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

$("run-now").addEventListener("click", async () => {
  $("run-now").disabled = true;
  gen++;
  try {
    await api("POST", "/api/run");
  } catch (e) {
    if (e.status !== 409) { toast("Could not start: " + e.message); $("run-now").disabled = false; return; }
    toast("A run is already in progress");
  }
  running = true;
  showLog = true;
  $("run-state").textContent = " running…";
  clearTimeout(timer);
  timer = setTimeout(refresh, 1500);
});

refresh();
