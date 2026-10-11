"use strict";
// The Snapshots tab: every place's snapshots, each with "Restore a copy" (then
// "Open") and "Copy command". Places load side by side: a cloud one is slow.
const $ = (id) => document.getElementById(id);

async function start() {
  let s;
  try { s = await api("GET", "/api/status"); } catch (e) {
    $("places").replaceChildren(el("p", { class: "bad" }, "Could not read the status: " + e.message));
    return;
  }
  $("host").textContent = s.host;
  const backups = s.backups || [];
  if (!backups.length) {
    $("places").replaceChildren(el("p", { class: "muted" }, "No backups are set up: add a place in Settings → Backups."));
    return;
  }
  $("places").replaceChildren(...backups.map((b, i) => el("section", { class: "box place", id: "place-" + i },
    el("div", { class: "place-name" }, b.repo, b.path && b.path !== b.repo ? el("span", { class: "muted path" }, " " + b.path) : null),
    el("div", { id: "list-" + i, class: "muted" }, "reading the snapshots…"))));
  await Promise.all(backups.map((_, i) => load(i)));
  // After the lists: they push the place asked for down as they fill.
  document.getElementById(location.hash.slice(1))?.scrollIntoView();
}

async function load(i) {
  const box = $("list-" + i);
  try {
    const snaps = await api("GET", "/api/backups/" + i + "/snapshots");
    box.className = "";
    box.replaceChildren(snaps.length ? table(i, snaps) : el("p", { class: "muted" }, "no snapshots yet"));
  } catch (e) {
    box.className = "bad";
    box.textContent = "Could not read the snapshots: " + e.message;
  }
}

function table(i, snaps) {
  return el("ul", { class: "snaps" }, snaps.map((sn) => el("li", {},
    el("div", {}, el("b", {}, when(sn.time)), " · ", el("code", {}, sn.id)),
    el("div", { class: "muted" }, (sn.changes || "no record of what changed (made before tgbup kept one)") + " · on " + sn.host),
    (sn.same || []).length ? el("div", { class: "muted" }, "the same version as ",
      sn.same.flatMap((s, i) => [i ? ", " : "", el("code", {}, s.id), " in " + s.place])) : null,
    el("div", { class: "actions" }, actions(i, sn)))));
}

function actions(i, sn) {
  const out = [];
  if (sn.restored) {
    out.push(el("button", { type: "button", onclick: () => openCopy(i, sn) }, "Open"));
  } else {
    out.push(el("button", { type: "button", onclick: (e) => restore(i, sn, e.target) }, "Restore a copy"));
  }
  out.push(el("button", { type: "button", class: "secondary", title: sn.command, onclick: () => copy(sn.command) }, "Copy command"));
  if (sn.restored) out.push(el("div", { class: "muted path" }, sn.copy));
  return out;
}

async function restore(i, sn, button) {
  button.disabled = true;
  button.textContent = "Restoring…";
  toast("Restoring " + sn.id + " into " + sn.copy + ": all its files are copied, this can take a while");
  try {
    const done = await api("POST", "/api/backups/" + i + "/snapshots/" + encodeURIComponent(sn.id) + "/restore");
    Object.assign(sn, done);
    toast("Restored " + sn.id + " into " + sn.copy);
  } catch (e) {
    toast("Could not restore " + sn.id + ": " + e.message);
  }
  button.closest(".actions").replaceChildren(...actions(i, sn));
}

async function openCopy(i, sn) {
  try { await api("POST", "/api/backups/" + i + "/snapshots/" + encodeURIComponent(sn.id) + "/open"); }
  catch (e) { toast("Could not open it: " + e.message); }
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("Copied: " + text);
  } catch {
    window.prompt("The command to restore it", text);
  }
}

start();
