"use strict";
// The chat picker, a step of the Setup page. Its markup is in setup.html;
// it reads the chats from Telegram only once the step is opened.
window.tgbupChats = (() => {
// Found inside the picker: while its step is closed it is not in the document.
const picker = document.getElementById("chatpicker");
const $ = (id) => picker.querySelector("#" + id);
let started = false;
let onSaved = () => {};
const PAGE = 50;
let data = { groups: [], chats: [] };
const byId = new Map();
const selected = new Set();
const excluded = new Map(); // chat id -> Set of topic ids left out
const counts = new Map();   // chat id -> {messages, topics, error}
const topics = new Map();   // chat id -> [{id, title, messages}]
const open = new Set();     // forums whose topics are shown
let limit = PAGE;
let loadingCounts = false;

function filtered() {
  const g = data.groups[$("group").value] || { ids: [] };
  const q = $("search").value.trim().toLowerCase().replace(/^@/, "");
  return g.ids.map((id) => byId.get(id)).filter((c) => c &&
    (!q || c.title.toLowerCase().includes(q) || (c.username || "").toLowerCase().includes(q)));
}

function topicLabel(c) {
  const n = counts.get(c.id);
  const ex = (excluded.get(c.id) || new Set()).size;
  let text = n && !n.error ? plural(n.topics, "topic") : "topics";
  if (ex) text += " · " + ex + " left out";
  return el("button", { type: "button", class: "link", onclick: () => toggleTopics(c) }, (open.has(c.id) ? "▾ " : "▸ ") + text);
}

function row(c) {
  const n = counts.get(c.id);
  const msgs = !n ? "…" : n.error ? "?" : num(n.messages);
  const cells = [
    el("td", {}, el("input", { type: "checkbox", checked: selected.has(c.id), "aria-label": c.title,
      onchange: (e) => { e.target.checked ? selected.add(c.id) : selected.delete(c.id); render(); } })),
    el("td", { class: "title" }, c.title, c.username ? el("span", { class: "user" }, " @" + c.username) : null,
      c.type === "forum" && open.has(c.id) ? topicList(c) : null),
    el("td", {}, c.type),
    el("td", { class: "num", title: n && n.error ? n.error : "" }, msgs),
    el("td", {}, c.type === "forum" ? topicLabel(c) : ""),
  ];
  return el("tr", { class: selected.has(c.id) ? "sel" : "" }, cells);
}

function topicList(c) {
  const ts = topics.get(c.id);
  if (!ts) return el("div", { class: "muted" }, "loading topics…");
  if (!ts.length) return el("div", { class: "muted" }, "no topics");
  const ex = excluded.get(c.id) || new Set();
  return el("ul", { class: "topics" }, ts.map((t) => el("li", {}, el("label", {},
    el("input", { type: "checkbox", checked: !ex.has(t.id), onchange: (e) => {
      const set = excluded.get(c.id) || new Set();
      e.target.checked ? set.delete(t.id) : set.add(t.id);
      excluded.set(c.id, set);
      render();
    } }),
    el("span", {}, t.title + " #" + t.id), el("span", { class: "n" }, num(t.messages))))));
}

async function toggleTopics(c) {
  if (open.has(c.id)) { open.delete(c.id); render(); return; }
  open.add(c.id);
  render();
  if (!topics.has(c.id)) {
    try { topics.set(c.id, await api("GET", "/api/chats/" + c.id + "/topics")); }
    catch (e) { open.delete(c.id); toast("Could not load topics: " + e.message); }
    render();
  }
}

function render() {
  const list = filtered();
  const shown = list.slice(0, limit);
  $("rows").replaceChildren(...shown.map(row));
  $("more").hidden = list.length <= limit;
  $("more").textContent = "Show more (" + num(list.length - limit) + " left)";
  $("selected").textContent = num(selected.size) + " selected";
  loadCounts(shown);
}

// loadCounts asks for message counts of the rows on screen, a batch at a time:
// Telegram limits how fast they can be read.
async function loadCounts(shown) {
  if (loadingCounts) return;
  const missing = shown.filter((c) => !counts.has(c.id)).map((c) => c.id).slice(0, 20);
  if (!missing.length) return;
  loadingCounts = true;
  try {
    const got = await api("POST", "/api/chats/counts", { ids: missing });
    for (const [id, n] of Object.entries(got)) counts.set(Number(id), n);
  } catch (e) {
    for (const id of missing) counts.set(id, { error: e.message });
  }
  loadingCounts = false;
  render();
}

function selection() {
  const ex = {};
  for (const id of selected) {
    const set = excluded.get(id);
    if (set && set.size && byId.get(id).type === "forum") ex[id] = [...set].sort((a, b) => a - b);
  }
  return { chats: [...selected], exclude_topics: ex };
}

async function load() {
  try {
    data = await api("GET", "/api/chats");
  } catch (e) {
    $("loading").textContent = "Could not load the chats: " + e.message + " (close and open this step to try again)";
    started = false;
    return;
  }
  for (const c of data.chats) {
    byId.set(c.id, c);
    if (c.selected) selected.add(c.id);
    if ((c.excluded_topics || []).length) excluded.set(c.id, new Set(c.excluded_topics));
  }
  const warn = $("warnings");
  warn.replaceChildren(...(data.warnings || []).map((w) => el("li", {}, w)));
  warn.hidden = !(data.warnings || []).length;
  $("group").replaceChildren(...data.groups.map((g, i) => el("option", { value: i }, g.title + " (" + g.ids.length + ")")));
  $("loading").hidden = true;
  document.querySelector("table.chatlist").hidden = false;
  render();
}

$("group").addEventListener("change", () => { limit = PAGE; render(); });
$("search").addEventListener("input", () => { limit = PAGE; render(); });
$("more").addEventListener("click", () => { limit += PAGE; render(); });
$("all").addEventListener("click", () => { filtered().forEach((c) => selected.add(c.id)); render(); });
$("none").addEventListener("click", () => { filtered().forEach((c) => selected.delete(c.id)); render(); });

$("review").addEventListener("click", async () => {
  if (!selected.size) { toast("Choose at least one chat"); return; }
  $("review").disabled = true;
  try {
    const p = await api("POST", "/api/chats/preview", selection());
    $("report").textContent = p.report;
    $("config").textContent = p.config;
    $("confirm").showModal();
  } catch (e) {
    toast("Could not check the list: " + e.message);
  }
  $("review").disabled = false;
});
$("cancel").addEventListener("click", () => $("confirm").close());
$("save").addEventListener("click", async () => {
  $("save").disabled = true;
  try {
    await api("POST", "/api/chats", selection());
    $("confirm").close();
    toast("Saved " + plural(selected.size, "chat") + ". The next run archives them.");
    onSaved();
  } catch (e) {
    toast("Not saved: " + e.message);
  }
  $("save").disabled = false;
});

return {
  // start shows the picker in place of node's content and loads the chats once.
  start(saved) {
    onSaved = saved;
    if (!started) { started = true; load(); }
    picker.hidden = false;
    return picker;
  },
};
})();
