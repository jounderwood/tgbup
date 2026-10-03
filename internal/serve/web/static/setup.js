"use strict";
// The setup page: one card per step, the same steps as tgbup init.
const $ = (id) => document.getElementById(id);
let state = null;
const open = new Set(); // done steps the person chose to change
const draft = {};       // per-step values typed but not saved yet

async function load() {
  try {
    state = await api("GET", "/api/setup");
  } catch (e) {
    $("steps").replaceChildren(el("p", { class: "bad" }, "Could not read the setup state: " + e.message));
    return;
  }
  render();
}

function render() {
  $("steps").replaceChildren(...state.steps.map(card));
}

function card(step) {
  const showForm = !step.done || open.has(step.id);
  const head = el("header", {},
    el("span", { class: "mark " + (step.done ? "ok" : "todo") }, step.done ? "✓" : "○"),
    el("h3", {}, step.title),
    step.optional ? el("span", { class: "opt" }, "optional") : null,
    el("span", { class: "state" }, step.unavailable || step.status || ""),
    step.done && !step.unavailable && forms[step.id] && changeable(step)
      ? el("button", { type: "button", class: "link", onclick: () => { open.has(step.id) ? open.delete(step.id) : open.add(step.id); render(); } },
          open.has(step.id) ? "close" : "change")
      : null);
  const box = el("section", { class: "step", id: "step-" + step.id }, head);
  if (showForm && !step.unavailable && forms[step.id]) box.append(forms[step.id](step));
  return box;
}

function changeable(step) {
  if (step.id === "archive") return !state.archive.has_messages;
  return step.id !== "download";
}

// action runs one setup action from a form; errors show inside the form.
async function action(form, name, body, okText) {
  const err = form.querySelector(".err") || form.appendChild(el("div", { class: "err" }));
  err.textContent = "";
  const buttons = form.querySelectorAll("button");
  buttons.forEach((b) => { b.disabled = true; });
  const busy = el("div", { class: "hint" }, "Working…");
  form.append(busy);
  try {
    const res = await api("POST", "/api/setup/" + name, body || {});
    if (okText) toast(okText);
    return res;
  } catch (e) {
    err.textContent = e.message;
    return null;
  } finally {
    busy.remove();
    buttons.forEach((b) => { b.disabled = false; });
  }
}

function field(label, attrs) {
  const input = el("input", Object.assign({ type: "text", autocomplete: "off" }, attrs));
  return { input, node: el("label", {}, label, input) };
}

function folderRow(input) {
  return el("div", { class: "row" }, input,
    el("button", { type: "button", class: "secondary", onclick: async (e) => {
      const res = await action(e.target.closest(".form"), "choose-folder");
      if (res && res.path) input.value = res.path;
    } }, "Choose…"));
}

const forms = {
  keys() {
    const id = field("api_id", { inputmode: "numeric", placeholder: "12345678" });
    const hash = field("api_hash", { placeholder: "32 characters" });
    const f = el("div", { class: "form" },
      el("p", { class: "hint" }, "Telegram gives every program its own keys: open ",
        el("a", { href: "https://my.telegram.org", target: "_blank", rel: "noopener" }, "my.telegram.org"),
        ", sign in, open API development tools and create an app (any title, platform Desktop)."),
      id.node, hash.node,
      el("div", {}, el("button", { type: "button", onclick: async () => {
        if (await action(f, "keys", { api_id: id.input.value, api_hash: hash.input.value }, "Keys saved")) { open.delete("keys"); load(); }
      } }, "Save")));
    return f;
  },

  login() {
    const f = el("div", { class: "form" });
    if (state.login.password_needed) {
      const pw = field("Two-step verification password", { type: "password", autocomplete: "current-password" });
      f.append(pw.node, el("div", {}, el("button", { type: "button", onclick: async () => {
        if (await action(f, "login/password", { password: pw.input.value }, "Logged in")) load();
      } }, "Sign in")));
    } else if (state.login.code_sent) {
      const code = field("Login code from Telegram", { inputmode: "numeric", autocomplete: "one-time-code" });
      f.append(el("p", { class: "hint" }, "Telegram sent a code to your Telegram app (or by SMS)."), code.node,
        el("div", { class: "row" },
          el("button", { type: "button", onclick: async () => {
            const res = await action(f, "login/signin", { code: code.input.value });
            if (res && res.done) toast("Logged in");
            if (res) load();
          } }, "Sign in"),
          el("button", { type: "button", class: "link", onclick: () => { state.login.code_sent = false; render(); } }, "use another number")));
    } else {
      const phone = field("Phone number", { type: "tel", placeholder: "+79990001122", autocomplete: "tel" });
      f.append(phone.node, el("div", {}, el("button", { type: "button", onclick: async () => {
        if (await action(f, "login/code", { phone: phone.input.value })) load();
      } }, "Send code")));
    }
    return f;
  },

  archive() {
    const a = state.archive;
    const mode = draft.archiveMode || "new";
    const f = el("div", { class: "form" });
    const pick = (m) => { draft.archiveMode = m; render(); };
    f.append(el("div", { class: "row" },
      el("label", { class: "check" }, el("input", { type: "radio", name: "amode", checked: mode === "new", onchange: () => pick("new") }), "New archive"),
      el("label", { class: "check" }, el("input", { type: "radio", name: "amode", checked: mode === "restore", onchange: () => pick("restore") }), "Restore from a backup")));
    const dir = el("input", { type: "text", value: a.dir || a.default, "aria-label": "Archive folder" });
    if (mode === "new") {
      f.append(el("label", {}, "Archive folder"), folderRow(dir),
        el("div", {}, el("button", { type: "button", onclick: async () => {
          if (await action(f, "archive", { dir: dir.value }, "Archive folder saved")) { open.delete("archive"); load(); }
        } }, "Save")));
      return f;
    }
    return restoreForm(f, dir);
  },

  chats() {
    return el("div", { class: "form" }, el("p", { class: "hint" }, "Tick the chats and forum topics to archive."),
      el("div", {}, el("a", { href: "/chats" }, el("button", { type: "button" }, "Choose chats"))));
  },

  backup() {
    const b = state.backup;
    const d = draft.backup || (draft.backup = {
      enabled: true, local: b.local || b.default_dir, extras: new Set(b.extras || []), custom: "",
      password: "", saved: false, own: false, replace: !b.has_password,
    });
    const f = el("div", { class: "form" });
    f.append(el("p", { class: "hint" }, "Backups keep earlier versions of the archive (encrypted restic snapshots after every run). Every place gets each snapshot."),
      el("label", { class: "check" }, el("input", { type: "checkbox", checked: d.enabled, onchange: (e) => { d.enabled = e.target.checked; render(); } }), "Back up the archive"));
    if (!d.enabled) {
      f.append(el("div", {}, el("button", { type: "button", onclick: async () => {
        if (await action(f, "backup", { enabled: false }, "Backups are off")) { open.delete("backup"); load(); }
      } }, "Save")));
      return f;
    }
    const local = el("input", { type: "text", value: d.local, "aria-label": "Backup folder on this Mac", oninput: (e) => { d.local = e.target.value; } });
    f.append(el("label", {}, "Folder on this Mac (empty: none)"), folderRow(local));
    f.append(el("div", { class: "choices" }, el("span", { class: "hint" }, "Also back up to:"),
      ...b.choices.filter((c) => c.repo).map((c) => el("label", { class: "check" },
        el("input", { type: "checkbox", checked: d.extras.has(c.repo), onchange: (e) => { e.target.checked ? d.extras.add(c.repo) : d.extras.delete(c.repo); } }), c.label)),
      el("input", { type: "text", placeholder: "another folder or restic repository (sftp:, s3:, rclone:…)", value: d.custom, oninput: (e) => { d.custom = e.target.value; } })));

    const pw = el("div", { class: "form" });
    if (b.has_password && !d.replace) {
      pw.append(el("p", { class: "hint" }, "Password: set. ",
        el("button", { type: "button", class: "link", onclick: () => { d.replace = true; render(); } }, "Replace it"),
        " (only when the repositories were made with another one)."));
    } else if (d.own) {
      const p1 = el("input", { type: "password", autocomplete: "new-password", "aria-label": "Password" });
      const p2 = el("input", { type: "password", autocomplete: "new-password", "aria-label": "Password again" });
      p1.addEventListener("input", () => { d.password = p1.value === p2.value ? p1.value : ""; d.saved = true; });
      p2.addEventListener("input", () => { d.password = p1.value === p2.value ? p1.value : ""; d.saved = true; });
      pw.append(el("label", {}, "Your password (at least 12 characters; for existing repositories, the one they were made with)", p1),
        el("label", {}, "Once more", p2),
        el("button", { type: "button", class: "link", onclick: () => { d.own = false; d.password = ""; render(); } }, "generate one instead"));
    } else if (d.password) {
      pw.append(el("p", { class: "hint" }, "Backups are encrypted with this password. Without it they cannot be read back:"),
        el("div", { class: "row" }, el("div", { class: "secret" }, d.password),
          el("button", { type: "button", class: "secondary", onclick: () => navigator.clipboard.writeText(d.password).then(() => toast("Copied"), () => toast("Select it and copy")) }, "Copy")),
        el("label", { class: "check" }, el("input", { type: "checkbox", checked: d.saved, onchange: (e) => { d.saved = e.target.checked; render(); } }),
          "I saved it in my password manager (e.g. 1Password, item “tgbup backups”)"));
    } else {
      pw.append(el("div", { class: "row" },
        el("button", { type: "button", onclick: async () => {
          const res = await action(f, "password/new");
          if (res) { d.password = res.password; d.saved = false; render(); }
        } }, "Generate a password"),
        el("button", { type: "button", class: "secondary", onclick: () => { d.own = true; render(); } }, "Use my own (or the existing one)")));
    }
    f.append(pw);
    f.append(el("div", {}, el("button", { type: "button", onclick: async () => {
      const extras = [...d.extras];
      if (d.custom.trim()) extras.push(d.custom.trim());
      const needsPw = !b.has_password || d.replace;
      if (needsPw && !d.password) { f.querySelector(".err") || f.append(el("div", { class: "err" })); f.querySelector(".err").textContent = d.own ? "Type the same password twice" : "Generate a password or use your own"; return; }
      const res = await action(f, "backup", { enabled: true, local: d.local, extras, password: needsPw ? d.password : "", password_saved: d.saved },
        "Backups are set up");
      if (res) {
        delete draft.backup;
        open.delete("backup");
        if (res.log) toast(res.log.split("\n").pop());
        load();
      }
    } }, "Save and create the backups")),
      el("p", { class: "hint" }, "Google Drive through rclone opens a Google sign-in page in your browser; come back here after allowing access."));
    return f;
  },

  bot() {
    const d = draft.bot || (draft.bot = { token: "", name: "", chat: null, waiting: false });
    const f = el("div", { class: "form" });
    if (!d.name) {
      const token = field("Bot token from @BotFather", { placeholder: "123456789:AAE…", value: d.token });
      f.append(el("p", { class: "hint" }, "Your own Telegram bot reports errors right away and sends a weekly summary. In Telegram open @BotFather → /newbot, then paste the token here."),
        token.node, el("div", {}, el("button", { type: "button", onclick: async () => {
          d.token = token.input.value.trim();
          const res = await action(f, "bot/check", { token: d.token });
          if (res) { d.name = res.name; render(); waitForChat(); }
        } }, "Check the token")));
      return f;
    }
    if (!d.chat) {
      f.append(el("p", {}, "Now send any message to your bot: ",
        el("a", { href: "https://t.me/" + d.name, target: "_blank", rel: "noopener" }, "t.me/" + d.name)),
        el("p", { class: "hint" }, d.waiting ? "Waiting for your message…" : "Not waiting any more."),
        el("div", { class: "row" },
          d.waiting ? null : el("button", { type: "button", onclick: () => waitForChat() }, "Wait again"),
          el("button", { type: "button", class: "link", onclick: () => { delete draft.bot; render(); } }, "use another bot")));
      return f;
    }
    f.append(el("p", {}, "Got a message from ", el("b", {}, d.chat.name), ". Send alerts to this chat?"),
      el("div", { class: "row" },
        el("button", { type: "button", onclick: async () => {
          if (await action(f, "bot/save", { token: d.token, chat_id: d.chat.chat_id }, "Sent a test message; alerts are on")) { delete draft.bot; open.delete("bot"); load(); }
        } }, "Yes, send a test message"),
        el("button", { type: "button", class: "link", onclick: () => { d.chat = null; render(); waitForChat(); } }, "no, wait for another")));
    return f;
  },

  download() {
    const f = el("div", { class: "form" });
    if (state.running) {
      f.append(el("p", {}, "Running. ", el("a", { href: "/" }, "See the log on Overview"), "; this page updates when it finishes."));
      setTimeout(load, 5000);
      return f;
    }
    f.append(el("p", { class: "hint" }, "The first download takes minutes to hours. Telegram asks to allow a data export in your Telegram app (a message from “Telegram”): allow it, then start again here."),
      el("div", { class: "row" },
        el("button", { type: "button", onclick: async () => { if (await action(f, "download", { takeout: true }, "Download started")) load(); } }, "Start the download"),
        el("button", { type: "button", class: "secondary", onclick: async () => { if (await action(f, "download", { takeout: false }, "Download started")) load(); } }, "Without the export permission")));
    return f;
  },

  schedule(step) {
    const s = state.schedule;
    const f = el("div", { class: "form" });
    if (!step.done) {
      f.append(el("p", { class: "hint" }, "tgbup run downloads what is new, backs up and alerts at " + s.times.join(", ") + ". A run missed while the Mac sleeps starts on wake-up."),
        el("div", {}, el("button", { type: "button", onclick: async () => { if (await action(f, "schedule/install", {}, "Schedule installed")) load(); } }, "Install")));
      return f;
    }
    f.append(el("p", { class: "hint" }, s.last_run ? "Last run " + when(s.last_run.started) + ": " + (s.last_run.ok ? "worked" : "had errors (see Overview)") : "No run yet."),
      el("p", { class: "hint" }, "A check run works exactly like a scheduled one; macOS may ask to let tgbup access iCloud or other folders — allow it."),
      el("div", {}, el("button", { type: "button", onclick: async () => { if (await action(f, "schedule/check", {}, "Check run started: see Overview")) load(); } }, "Run a check now")));
    return f;
  },
};

// waitForChat long-polls the bot for a private message, up to 3 minutes.
async function waitForChat() {
  const d = draft.bot;
  if (!d || d.waiting) return;
  d.waiting = true;
  render();
  const until = Date.now() + 3 * 60 * 1000;
  while (draft.bot === d && !d.chat && Date.now() < until) {
    try {
      const res = await api("POST", "/api/setup/bot/wait", { token: d.token });
      if (res.chat_id) d.chat = res;
    } catch (e) {
      toast(e.message);
      break;
    }
  }
  d.waiting = false;
  if (draft.bot === d) render();
}

function restoreForm(f, dir) {
  const d = draft.restore || (draft.restore = { repo: "", custom: "", password: "", snapshots: null, snapshot: "" });
  const places = state.backup.places.filter((p) => p.repo);
  const select = el("select", { "aria-label": "Backup place", onchange: (e) => { d.repo = e.target.value; d.snapshots = null; render(); } },
    el("option", { value: "" }, "Choose where the backup is…"),
    ...places.map((p) => el("option", { value: p.repo, selected: d.repo === p.repo }, p.label)),
    el("option", { value: "other", selected: d.repo === "other" }, "another folder or repository"));
  f.append(el("label", {}, "Backup place", select));
  if (d.repo === "other") f.append(el("input", { type: "text", placeholder: "folder or restic repository", value: d.custom, oninput: (e) => { d.custom = e.target.value; } }));
  const pw = el("input", { type: "password", autocomplete: "current-password", value: d.password, oninput: (e) => { d.password = e.target.value; } });
  f.append(el("label", {}, "Backup password (from your password manager)", pw));
  const repo = () => (d.repo === "other" ? d.custom.trim() : d.repo);
  f.append(el("div", {}, el("button", { type: "button", onclick: async () => {
    const res = await action(f, "restore/snapshots", { repo: repo(), password: d.password });
    if (res) { d.snapshots = res.snapshots || []; d.snapshot = d.snapshots.length ? d.snapshots[0].id : ""; render(); }
  } }, "Find the backups")));
  if (d.snapshots) {
    if (!d.snapshots.length) {
      f.append(el("p", { class: "err" }, "No tgbup backups there."));
      return f;
    }
    f.append(el("label", {}, "Snapshot", el("select", { onchange: (e) => { d.snapshot = e.target.value; } },
      ...d.snapshots.slice(0, 20).map((s, i) => el("option", { value: s.id, selected: s.id === d.snapshot },
        when(s.time) + " · " + s.short_id + " · from " + s.hostname + (i === 0 ? " (newest)" : ""))))),
      el("label", {}, "Restore into"), folderRow(dir),
      el("p", { class: "hint" }, "Nothing is deleted: a database already there is kept aside, existing media stay."),
      el("div", {}, el("button", { type: "button", onclick: async () => {
        const res = await action(f, "restore", { repo: repo(), password: d.password, snapshot: d.snapshot, dir: dir.value });
        if (res) {
          toast("Restored: " + res.MediaAdded + " media files");
          delete draft.restore;
          draft.archiveMode = "new";
          open.delete("archive");
          load();
        }
      } }, "Restore")));
  }
  return f;
}

load();
