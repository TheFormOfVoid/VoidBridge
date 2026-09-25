"use strict";

// ---------- helpers ----------

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// Everything that may come from another device or the network is escaped.
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

let api = null; // window.go.main.App once Wails is ready
let state = null;
let page = "devices";

function toast(msg, bad = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "show" + (bad ? " bad" : "");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.className = ""), bad ? 5000 : 2200);
}

// call runs an API method, shows errors as a toast, and returns [result, ok].
async function call(method, ...args) {
  try {
    return [await api[method](...args), true];
  } catch (e) {
    toast(String(e?.message ?? e), true);
    return [null, false];
  }
}

// busy disables a button while an async action runs.
async function busy(btn, label, fn) {
  const old = btn.textContent;
  btn.disabled = true;
  if (label) btn.textContent = label;
  try {
    return await fn();
  } finally {
    btn.disabled = false;
    btn.textContent = old;
  }
}

function ago(ms) {
  if (!ms) return "";
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 10) return "just now";
  if (s < 60) return `${Math.floor(s)} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return new Date(ms).toLocaleString();
}

function size(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

function openURL(url) {
  if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url);
  else window.open(url, "_blank");
}

function confirmDialog(title, text, okLabel = "OK", danger = false) {
  return new Promise((resolve) => {
    const d = $("#dialog");
    $("#dialog-form").innerHTML = `
      <h3>${esc(title)}</h3><p class="muted">${esc(text)}</p>
      <div class="buttons"><button value="no" class="btn">Cancel</button>
      <button value="yes" class="btn ${danger ? "danger" : "accent"}">${esc(okLabel)}</button></div>`;
    d.onclose = () => resolve(d.returnValue === "yes");
    d.showModal();
  });
}

const icons = {
  windows: '<svg viewBox="0 0 24 24"><path d="M3 5h18v11H3z M8 20h8 M12 16v4"/></svg>',
  android: '<svg viewBox="0 0 24 24"><path d="M8 2h8a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z M11 19h2"/></svg>',
  server: '<svg viewBox="0 0 24 24"><path d="M4 4h16v6H4z M4 14h16v6H4z M7 7h.01 M7 17h.01"/></svg>',
  code: '<svg viewBox="0 0 24 24"><path d="M7 11V7a5 5 0 0 1 10 0v4 M5 11h14v10H5z M12 15v2"/></svg>',
  orb: '<svg viewBox="0 0 32 32"><circle cx="16" cy="16" r="12" fill="none" stroke="currentColor" stroke-width="3"/><circle cx="16" cy="16" r="5.5" fill="currentColor"/></svg>',
};

// ---------- navigation ----------

function show(p) {
  page = p;
  $$("#nav button").forEach((b) => b.classList.toggle("active", b.dataset.page === p));
  $$(".page").forEach((s) => (s.hidden = s.id !== "page-" + p));
  $("#main").scrollTop = 0;
  if (p === "history") loadHistory();
  if (p === "phone") loadAdb();
  if (p === "server") loadServer();
  render();
}

// ---------- rendering ----------

function viaTags(via) {
  return (via || []).map((v) => `<span class="tag ok">${esc(v)}</span>`).join("");
}

function render() {
  if (!state) return;
  const s = state;
  const setUp = s.mode !== "";
  $("#nav-server").hidden = s.mode !== "account";
  $("#sync-toggle").checked = !s.paused;
  $("#foot-status").textContent = !setUp ? "Not set up" : s.paused ? "Paused" : s.devices.length ? `${s.devices.length} device${s.devices.length > 1 ? "s" : ""} connected` : "Waiting for devices";

  // Hero
  const hero = $("#hero");
  let title, sub;
  if (!setUp) {
    title = "Let's connect your devices";
    sub = "Choose how your devices should find each other. It takes a minute.";
  } else if (s.paused) {
    title = "Sync is paused";
    sub = "Nothing is sent or received until you turn sync back on.";
  } else if (s.devices.length) {
    title = s.devices.length === 1 ? `Syncing with ${s.devices[0].name}` : `Syncing with ${s.devices.length} devices`;
    sub = "Copy on any device and paste on all the others." + (s.lastSync ? ` Last sync ${ago(s.lastSync)}.` : "");
  } else {
    title = "Waiting for your other devices";
    sub = s.mode === "account" ? (s.serverUp ? "Connected to your server. Sign in on another device to start syncing." : "Trying to reach your server…") : "Open VoidBridge on another device with the same sync code.";
  }
  hero.className = "hero" + (setUp && !s.paused && s.devices.length ? " live" : "") + (s.paused ? " paused" : "");
  hero.innerHTML = `<div class="orb">${icons.orb}</div><div><h1>${esc(title)}</h1><p class="muted">${esc(sub)}</p></div>`;

  // Callouts
  const callouts = [];
  if (!setUp) callouts.push(`<div class="callout"><div><b>Not set up yet.</b> Use your VoidBridge server, or a sync code with no server.</div><button class="btn accent" data-go="connect">Set up</button></div>`);
  if (s.signedOut) callouts.push(`<div class="callout bad"><div><b>Signed out by the server.</b> This device was removed or the account was disabled.</div><button class="btn" data-go="connect">Sign in again</button></div>`);
  else if (s.mode === "account" && !s.serverUp && s.serverErr) callouts.push(`<div class="callout warn"><div><b>Server unreachable:</b> ${esc(s.serverErr)}. ${s.direct ? "Devices on this network still sync directly." : ""}</div></div>`);
  if (s.peerErr) callouts.push(`<div class="callout warn"><div>${esc(s.peerErr)}</div></div>`);
  $("#setup-callout").innerHTML = callouts.join("");

  // This device
  const mode = s.mode === "account" ? `<span class="tag ${s.serverUp ? "ok" : "warn"}">${esc(s.username)} @ server</span>` : s.mode === "code" ? `<span class="tag">Sync code</span>` : `<span class="tag warn">Not set up</span>`;
  $("#this-device").innerHTML = `<div class="card"><div class="icon">${icons.windows}</div><div class="body"><div class="name">${esc(s.deviceName)}</div><div class="meta">${mode}${s.direct ? '<span class="tag">Wi-Fi</span>' : ""}${s.direct && s.tailscale && s.tailscaleInstalled ? '<span class="tag">Tailscale</span>' : ""}</div></div></div>`;

  // Other devices
  $("#device-count").textContent = s.devices.length || "";
  $("#device-list").innerHTML = s.devices.length
    ? s.devices.map((d) => `<div class="card"><div class="icon">${icons[d.kind] || icons.windows}</div><div class="body"><div class="name">${esc(d.name || d.id)}</div><div class="meta">${viaTags(d.via)}</div></div></div>`).join("")
    : `<div class="empty">${setUp ? "No other devices connected right now." : "Set up VoidBridge to see your devices here."}</div>`;

  renderConnect();
  renderSettings();
}

// ---------- connections page ----------

let setupTab = "signin";
let serverChecked = null;

function renderConnect() {
  const s = state;
  const card = $("#group-card");
  if (s.mode === "code") {
    card.innerHTML = `
      <div class="panel"><div class="setting column">
        <div><b>Sync code</b><p class="muted">Enter this code on your other devices to add them. Anyone with it can join, so keep it private.</p></div>
        <div class="code-display">${esc(s.code)}</div>
        <div class="inline-form"><button class="btn" id="copy-code">Copy code</button><button class="btn subtle danger" id="leave">Leave this group</button></div>
      </div></div>`;
    $("#copy-code").onclick = () => navigator.clipboard.writeText(s.code).then(() => toast("Code copied"));
    $("#leave").onclick = leave;
  } else if (s.mode === "account") {
    card.innerHTML = `
      <div class="panel"><div class="row">
        <div class="icon">${icons.server}</div>
        <div class="grow"><b>${esc(s.username)}</b> on <span class="mono">${esc(s.server)}</span>
          <div class="meta">${s.serverUp ? '<span class="tag ok">Connected</span>' : `<span class="tag warn">${esc(s.serverErr || "Connecting…")}</span>`}${s.admin ? '<span class="tag">Admin</span>' : ""}</div></div>
        <button class="btn subtle danger" id="leave">Sign out</button>
      </div></div>`;
    $("#leave").onclick = leave;
  } else if (!card.dataset.setup) {
    card.dataset.setup = "1";
    card.innerHTML = setupHTML();
    wireSetup();
  }
  if (s.mode !== "") delete card.dataset.setup;

  $("#set-direct").checked = s.direct;
  $("#set-tailscale").checked = s.tailscale;
  $("#set-tailscale").disabled = !s.direct;
  $("#ts-hint").textContent = s.tailscaleInstalled
    ? "Also look for your devices over Tailscale, so sync works away from home without a server."
    : "Tailscale isn't installed on this PC. Install it from tailscale.com to sync away from home without a server.";
  $("#manual-list").innerHTML = s.manual.length ? s.manual.map((a, i) => `<span class="chip mono">${esc(a)}<button data-rm="${i}" title="Remove">×</button></span>`).join("") : '<span class="muted small">None</span>';
  $("#my-addrs").textContent = s.localIPs.length ? `This PC's addresses: ${s.localIPs.join(", ")} (port ${s.port})` : "";
}

function setupHTML() {
  return `
  <div class="choices">
    <div class="choice">
      <div class="icon">${icons.server}</div>
      <div><h3>Use a VoidBridge server</h3><p class="muted">Sign in with an account on your own server (e.g. a Raspberry Pi). Syncs anywhere, and everyone gets their own private clipboard.</p></div>
      <div class="tabs"><button data-tab="signin" class="${setupTab === "signin" ? "on" : ""}">Sign in</button><button data-tab="register" class="${setupTab === "register" ? "on" : ""}">Create account</button></div>
      <form class="stack" id="account-form">
        <label>Server address<input id="acc-server" placeholder="e.g. raspberrypi or 100.64.1.2" autocomplete="off"></label>
        <p class="muted small" id="server-status"></p>
        <label>Username<input id="acc-user" autocomplete="username"></label>
        <label>Password<input id="acc-pass" type="password" autocomplete="current-password"></label>
        <label id="invite-row" ${setupTab === "register" ? "" : "hidden"}>Invite code<input id="acc-invite" placeholder="From the server admin"></label>
        <p class="muted small" ${setupTab === "register" ? "" : "hidden"} id="pass-hint">Your password also encrypts your clipboard, so the server can't read it. If you forget it, you'll need a new account.</p>
        <button class="btn accent" id="acc-submit">${setupTab === "register" ? "Create account" : "Sign in"}</button>
      </form>
    </div>
    <div class="choice">
      <div class="icon">${icons.code}</div>
      <div><h3>No server: use a sync code</h3><p class="muted">Devices share a secret code and connect directly on your Wi-Fi, or anywhere over Tailscale.</p></div>
      <button class="btn accent" id="create-code">Create a new sync code</button>
      <div class="muted small" style="text-align:center">or join devices that already have one</div>
      <form class="inline-form" id="join-form">
        <input id="join-code" class="mono" placeholder="XXXX-XXXX-XXXX-XXXX" autocomplete="off">
        <button class="btn">Join</button>
      </form>
    </div>
  </div>`;
}

function wireSetup() {
  $$(".tabs button").forEach((b) => (b.onclick = () => {
    setupTab = b.dataset.tab;
    const card = $("#group-card");
    const keep = { server: $("#acc-server").value, user: $("#acc-user").value };
    card.innerHTML = setupHTML();
    wireSetup();
    $("#acc-server").value = keep.server;
    $("#acc-user").value = keep.user;
    checkServer();
  }));
  $("#acc-server").addEventListener("change", checkServer);
  $("#account-form").onsubmit = async (e) => {
    e.preventDefault();
    const [srv, user, pass] = [$("#acc-server").value, $("#acc-user").value, $("#acc-pass").value];
    const btn = $("#acc-submit");
    const [, ok] = await busy(btn, setupTab === "register" ? "Creating…" : "Signing in…", () =>
      setupTab === "register" ? call("Register", srv, user, pass, $("#acc-invite").value) : call("SignIn", srv, user, pass));
    if (ok) {
      toast(setupTab === "register" ? "Account created" : "Signed in");
      show("devices");
    }
  };
  $("#create-code").onclick = async (e) => {
    const [code, ok] = await busy(e.target, "Creating…", () => call("CreateGroup"));
    if (ok) toast(`Created sync code ${code}`);
  };
  $("#join-form").onsubmit = async (e) => {
    e.preventDefault();
    const [, ok] = await busy($("#join-form button"), "Joining…", () => call("JoinGroup", $("#join-code").value));
    if (ok) toast("Joined");
  };
}

async function checkServer() {
  const addr = $("#acc-server")?.value.trim();
  const out = $("#server-status");
  if (!addr || !out) return;
  out.textContent = "Checking…";
  try {
    const info = await api.ServerInfo(addr);
    serverChecked = info;
    out.textContent = `✓ ${info.name} (VoidBridge server ${info.version})` + (!info.has_users ? ". No accounts yet: the account you create will be the admin." : info.signup === "open" ? ". Anyone can create an account." : ". New accounts need an invite code.");
    if (!info.has_users && setupTab === "signin") $$(".tabs button")[1].click();
  } catch (e) {
    out.textContent = "✗ " + (e?.message ?? e);
  }
}

async function leave() {
  const acct = state.mode === "account";
  if (!(await confirmDialog(acct ? "Sign out?" : "Leave this group?", acct ? "This PC will stop syncing until you sign in again." : "This PC will stop syncing. You can join again with the code.", acct ? "Sign out" : "Leave", true))) return;
  await call("Leave");
}

// ---------- settings ----------

function renderSettings() {
  const s = state;
  $("#set-autostart").checked = s.autostart;
  $("#set-sensitive").checked = s.skipSensitive;
  $("#set-history").checked = s.history;
  if (document.activeElement !== $("#set-name")) $("#set-name").value = s.deviceName;
  $("#about").textContent = `VoidBridge ${s.version} · device id ${s.deviceId}`;
}

async function saveSettings(patch = {}) {
  const s = state;
  const next = {
    deviceName: s.deviceName, paused: s.paused, skipSensitive: $("#set-sensitive").checked, history: $("#set-history").checked,
    autostart: $("#set-autostart").checked, direct: $("#set-direct").checked, tailscale: $("#set-tailscale").checked, ...patch,
  };
  const [, ok] = await call("SetSettings", next);
  if (!ok) refresh();
}

// ---------- history ----------

let history = [];

async function loadHistory() {
  const [h] = await call("History");
  history = h || [];
  renderHistory();
}

function renderHistory() {
  const q = $("#history-search").value.trim().toLowerCase();
  const items = history.filter((h) => !q || (h.text || "").toLowerCase().includes(q) || (h.from || "").toLowerCase().includes(q));
  const list = $("#history-list");
  if (!state?.history) {
    list.innerHTML = `<div class="empty">History is turned off in Settings.</div>`;
    return;
  }
  if (!items.length) {
    list.innerHTML = `<div class="empty">${q ? "Nothing matches." : "Things you copy on any device will show up here."}</div>`;
    return;
  }
  list.innerHTML = items.map((h) => `
    <div class="clip" data-id="${esc(h.id)}" title="Click to copy">
      ${h.type === "image" ? (h.thumb ? `<img src="${esc(h.thumb)}" alt="">` : `<div class="text muted">Image</div>`) : `<div class="text">${esc(h.text)}</div>`}
      <div class="foot"><span class="from">${h.local ? "This PC" : esc(h.from || "Another device")}</span>
      <span>${h.type === "image" ? `${h.width}×${h.height} · ${size(h.size)} · ` : ""}${esc(ago(h.time))}</span></div>
    </div>`).join("");
}

// ---------- phone setup ----------

let adbBusy = false;

async function loadAdb() {
  const panel = $("#adb-panel");
  if (adbBusy) return;
  const [st] = await call("AdbStatus");
  if (!st) return;
  if (!st.installed) {
    panel.innerHTML = `<p class="muted">VoidBridge needs Google's small adb tool (about 7 MB) to talk to your phone.</p>
      <p style="margin-top:10px"><button class="btn accent" id="adb-install">Download adb</button></p>`;
    $("#adb-install").onclick = async (e) => {
      adbBusy = true;
      const [, ok] = await busy(e.target, "Downloading…", () => call("AdbInstall"));
      adbBusy = false;
      if (ok) toast("adb installed");
      loadAdb();
    };
    return;
  }
  const rows = st.devices.map((d) => {
    const note = d.state === "unauthorized" ? '<span class="tag warn">Tap “Allow” on the phone</span>' : d.state !== "device" ? `<span class="tag warn">${esc(d.state)}</span>` : `<span class="tag ok">${d.wireless ? "Wireless" : "USB"}</span>`;
    return `<div class="phone-row"><div><b>${esc(d.model)}</b> ${note}</div>
      <button class="btn accent" data-setup="${esc(d.serial)}" ${d.state !== "device" ? "disabled" : ""}>Set up this phone</button></div>`;
  }).join("");
  panel.innerHTML = `
    <div class="panel">
      ${rows || `<div class="phone-row muted">No phone found yet. Plug it in with USB debugging on, or pair it wirelessly above.</div>`}
      <div class="phone-row"><span class="muted small">${st.error ? esc(st.error) : "The list refreshes every few seconds."}</span><button class="btn subtle small" id="adb-refresh">Refresh</button></div>
    </div>`;
  $("#adb-refresh").onclick = loadAdb;
  $$("[data-setup]", panel).forEach((b) => (b.onclick = () => setupPhone(b)));
}

async function setupPhone(btn) {
  adbBusy = true;
  const [steps, ok] = await busy(btn, "Setting up…", () => call("AdbSetup", btn.dataset.setup));
  adbBusy = false;
  if (!ok) return;
  const allOK = steps.every((s) => s.ok);
  $("#adb-results").innerHTML = `<div class="panel"><ul class="results">${steps.map((s) => `<li>${s.ok ? "✅" : "⚠️"} <span>${esc(s.name)}${s.ok ? "" : `: <span class="error">${esc(s.error)}</span>`}</span></li>`).join("")}</ul>
    <div class="phone-row"><span>${allOK ? "<b>Done!</b> On Android 13 and later, tap <b>Allow</b> if the phone asks about device logs. Then copying on the phone syncs automatically." : "Some steps didn't work. Check the phone and try again."}</span></div></div>`;
}

// ---------- server ----------

async function loadServer() {
  if (state?.mode !== "account") return;
  $("#server-sub").textContent = `Signed in as ${state.username} on ${state.server}`;
  const [me] = await call("ServerMe");
  if (!me) return;
  $("#my-devices").innerHTML = (me.devices || []).map((d) => `
    <div class="row"><span class="dot ${d.online ? "on" : ""}"></span><div class="icon">${icons[d.kind] || icons.windows}</div>
      <div class="grow"><b>${esc(d.name)}</b>${d.this ? ' <span class="tag">This PC</span>' : ""}<div class="muted small">${d.online ? "Online now" : "Last seen " + esc(ago(Date.parse(d.last_seen)))}</div></div>
      ${d.this ? "" : `<button class="btn small danger" data-revoke="${esc(d.id)}" data-name="${esc(d.name)}">Remove</button>`}</div>`).join("") || `<div class="row muted">No devices.</div>`;
  $$("[data-revoke]").forEach((b) => (b.onclick = async () => {
    if (!(await confirmDialog(`Remove ${b.dataset.name}?`, "It will be signed out and stop syncing until it signs in again.", "Remove", true))) return;
    if ((await call("RevokeDevice", b.dataset.revoke))[1]) loadServer();
  }));

  $("#admin-area").hidden = !me.admin;
  if (me.admin) loadAdmin();
}

async function loadAdmin() {
  const [[invites], [users]] = await Promise.all([call("AdminInvites"), call("AdminUsers")]);
  $("#invite-list").innerHTML = (invites || []).map((i) => `
    <div class="row"><div class="grow"><span class="mono" style="font-size:16px;user-select:text">${esc(i.code)}</span>
      <div class="muted small">${esc(i.note ? i.note + " · " : "")}${i.uses_left} use${i.uses_left === 1 ? "" : "s"} left · ${i.expires && !i.expires.startsWith("0001") ? "expires " + new Date(i.expires).toLocaleDateString() : "never expires"}</div></div>
      <div class="actions"><button class="btn small" data-copy="${esc(i.code)}">Copy</button><button class="btn small danger" data-revinv="${esc(i.code)}">Revoke</button></div></div>`).join("") || `<div class="row muted small">No open invites. Create one above and send it to whoever you want to let in.</div>`;
  $$("[data-copy]").forEach((b) => (b.onclick = () => navigator.clipboard.writeText(b.dataset.copy).then(() => toast("Invite code copied"))));
  $$("[data-revinv]").forEach((b) => (b.onclick = async () => (await call("AdminRevokeInvite", b.dataset.revinv))[1] && loadAdmin()));

  $("#user-list").innerHTML = (users || []).map((u) => `
    <div class="row"><span class="dot ${u.online ? "on" : ""}"></span>
      <div class="grow"><b>${esc(u.name)}</b> ${u.admin ? '<span class="tag">Admin</span>' : ""} ${u.disabled ? '<span class="tag bad">Disabled</span>' : ""}
        <div class="muted small">${u.devices} device${u.devices === 1 ? "" : "s"} · joined ${new Date(u.created).toLocaleDateString()}</div></div>
      ${u.name === state.username ? '<span class="muted small">You</span>' : `<div class="actions">
        <button class="btn small" data-admin="${esc(u.name)}" data-val="${!u.admin}">${u.admin ? "Remove admin" : "Make admin"}</button>
        <button class="btn small" data-disable="${esc(u.name)}" data-val="${!u.disabled}">${u.disabled ? "Enable" : "Disable"}</button>
        <button class="btn small danger" data-del="${esc(u.name)}">Delete</button></div>`}
    </div>`).join("");
  $$("[data-admin]").forEach((b) => (b.onclick = async () => (await call("AdminSetAdmin", b.dataset.admin, b.dataset.val === "true"))[1] && loadAdmin()));
  $$("[data-disable]").forEach((b) => (b.onclick = async () => (await call("AdminSetDisabled", b.dataset.disable, b.dataset.val === "true"))[1] && loadAdmin()));
  $$("[data-del]").forEach((b) => (b.onclick = async () => {
    if (!(await confirmDialog(`Delete ${b.dataset.del}?`, "The account and all its devices are removed. This can't be undone.", "Delete", true))) return;
    if ((await call("AdminDeleteUser", b.dataset.del))[1]) loadAdmin();
  }));
}

// ---------- wiring ----------

async function refresh() {
  const [s] = await call("State");
  if (s) {
    state = s;
    render();
  }
}

function wire() {
  $$("#nav button").forEach((b) => (b.onclick = () => show(b.dataset.page)));
  document.addEventListener("click", (e) => {
    const go = e.target.closest("[data-go]");
    if (go) show(go.dataset.go);
    const link = e.target.closest("[data-open]");
    if (link) {
      e.preventDefault();
      openURL(link.dataset.open);
    }
    const rm = e.target.closest("[data-rm]");
    if (rm) call("SetManualPeers", state.manual.filter((_, i) => i !== +rm.dataset.rm));
    const clip = e.target.closest(".clip");
    if (clip) call("CopyFromHistory", clip.dataset.id).then(([, ok]) => ok && toast("Copied to all your devices"));
  });

  $("#sync-toggle").onchange = (e) => call("SetPaused", !e.target.checked);
  ["#set-autostart", "#set-sensitive", "#set-history", "#set-direct", "#set-tailscale"].forEach((id) => ($(id).onchange = () => saveSettings()));
  $("#name-form").onsubmit = (e) => {
    e.preventDefault();
    saveSettings({ deviceName: $("#set-name").value }).then(() => toast("Saved"));
  };
  $("#manual-form").onsubmit = (e) => {
    e.preventDefault();
    const v = $("#manual-input").value.trim();
    if (!v) return;
    call("SetManualPeers", [...state.manual, v]);
    $("#manual-input").value = "";
  };
  $("#history-search").oninput = renderHistory;
  $("#history-clear").onclick = async () => {
    await call("ClearHistory");
    loadHistory();
  };
  $("#open-log").onclick = () => call("OpenLog");
  $("#quit").onclick = async () => (await confirmDialog("Quit VoidBridge?", "Your clipboard stops syncing until you start it again.", "Quit", true)) && call("Quit");
  $("#server-refresh").onclick = loadServer;
  $("#pair-form").onsubmit = async (e) => {
    e.preventDefault();
    const [, ok] = await busy($("#pair-form button"), "Pairing…", () => call("AdbPair", $("#pair-addr").value, $("#pair-code").value));
    if (ok) toast("Paired. Now connect below.");
  };
  $("#connect-form").onsubmit = async (e) => {
    e.preventDefault();
    const [, ok] = await busy($("#connect-form button"), "Connecting…", () => call("AdbConnect", $("#connect-addr").value));
    if (ok) {
      toast("Connected");
      loadAdb();
    }
  };

  window.runtime?.EventsOn?.("state", (s) => {
    const histChanged = state && s.historyLen !== state.historyLen;
    state = s;
    render();
    if (page === "history" && histChanged) loadHistory();
  });
  setInterval(() => {
    if (page === "phone") loadAdb();
    if (page === "devices" && state?.lastSync) render(); // keep "x min ago" fresh
  }, 4000);
}

// Wails injects window.go asynchronously; wait for it.
(function boot() {
  if (window.go?.main?.App) {
    api = window.go.main.App;
    if (window.runtime) document.documentElement.classList.add("wails");
    wire();
    refresh();
  } else {
    setTimeout(boot, 30);
  }
})();
