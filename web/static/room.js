"use strict";

(function () {
  const base = document.body.dataset.base;
  const roomID = document.body.dataset.room;
  const api = base + "/api/rooms/" + roomID;
  const $ = (id) => document.getElementById(id);
  const MESSAGES = ["say", "ask", "propose", "agree", "reject", "defer", "resolution", "amendment"];

  let room = null;
  let lastSeq = 0;
  const events = new Map();

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  }

  function sideName(label) {
    const s = room && room.sides.find((x) => x.label === label);
    return s && s.name ? "Side " + label + " (" + s.name + ")" : "Side " + label;
  }

  function who(e) {
    if (!e.side) return "Server";
    return sideName(e.side) + (e.actor && e.actor !== "server" ? ", " + e.actor : "");
  }

  function time(ts) {
    return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  async function call(method, path, body) {
    const opts = { method, headers: {}, credentials: "same-origin" };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(api + path, opts);
    let data = null;
    try { data = await res.json(); } catch (_) { /* empty body */ }
    if (data && data.room) update(data.room);
    if (!res.ok) throw new Error((data && data.message) || res.statusText);
    return data;
  }

  function showError(err) {
    $("error").textContent = err ? err.message : "";
  }

  function guard(fn) {
    return async (ev) => {
      if (ev) ev.preventDefault();
      try {
        await fn(ev);
        showError(null);
      } catch (err) {
        showError(err);
      }
    };
  }

  // Header and notices

  function update(r) {
    room = r;
    const me = r.you.side;
    document.title = r.topic + " · coop";
    $("topic").textContent = r.topic;
    $("you").textContent = "You: " + sideName(me) + ", human";

    const st = $("status");
    st.replaceChildren();
    st.append(el("span", "phase phase-" + r.phase, r.phase));
    if (r.pause) st.append(el("span", "paused", "paused: " + r.pause.kind));
    if (r.floor) st.append(el("span", "", "Floor: " + sideName(r.floor)));
    for (const s of r.sides) {
      const span = el("span", "side-tag side-" + s.label);
      span.append(el("b", "", s.label), document.createTextNode(" " + s.turns_used + "/" + r.turn_budget));
      const parts = [];
      if (!s.claimed) parts.push("invited");
      else {
        parts.push(s.human_joined_at ? "human" : "no human");
        parts.push(s.agent_joined_at ? "agent" : "no agent");
      }
      span.title = sideName(s.label) + ": " + parts.join(", ");
      st.append(span);
    }
    st.append(el("span", "muted", "Charter v" + r.charter.version));

    renderBriefing();
    renderAmendment();
    renderResolution();
    renderPause();
    renderClosed();
    renderSide();
    scheduleProtocol();
  }

  function renderBriefing() {
    const box = $("briefing");
    box.hidden = room.phase !== "briefing";
    if (box.hidden) return;
    const waiting = room.sides.filter((s) => !s.ready_at).map((s) => sideName(s.label));
    $("briefing-text").textContent = waiting.length ? "Waiting for: " + waiting.join(", ") : "";
    const mine = room.sides.find((s) => s.label === room.you.side);
    $("ready").hidden = !!(mine && mine.ready_at);
  }

  function renderAmendment() {
    const a = room.amendment;
    $("amendment").hidden = !a;
    if (!a) return;
    $("amd-meta").textContent = "#" + a.seq + " by " + sideName(a.side) + (a.replaces ? ", replaces item " + a.replaces : "");
    $("amd-body").textContent = a.text;
    $("amd-votes").textContent = room.sides.map((s) => sideName(s.label) + ": " + (a.votes[s.label] || "not voted")).join(" · ");
    $("amd-vote").hidden = !!a.votes[room.you.side];
  }

  function renderResolution() {
    const r = room.resolution;
    const box = $("resolution");
    const ev = r && events.get(r.seq);
    box.hidden = !ev;
    if (!ev) return;
    $("res-meta").textContent = "#" + r.seq + " by " + sideName(r.side) + ", charter v" + r.charter_version;
    $("res-body").textContent = ev.body;
    let state;
    if (room.phase === "approval") {
      state = "Agreed by the agents. Approvals: " + room.sides.map((s) =>
        sideName(s.label) + (r.approved_by.includes(s.label) ? " approved" : " pending")).join(" · ");
    } else if (room.phase === "closed") {
      state = "Approved by all sides.";
    } else {
      state = "Waiting for the agents to agree: " + room.sides.map((s) =>
        sideName(s.label) + (r.agreed_by.includes(s.label) ? " agreed" : " pending")).join(" · ");
    }
    $("res-state").textContent = state;
    $("res-decide").hidden = room.phase !== "approval" || r.approved_by.includes(room.you.side);
  }

  function renderPause() {
    const box = $("pause");
    const p = room.pause;
    box.hidden = !p || p.kind === "amendment";
    if (box.hidden) return;
    $("pause-text").textContent = p.kind === "budget"
      ? "Paused: the agents have used their turn budget."
      : "Paused by " + sideName(p.side) + ", " + p.actor + (p.reason ? ": " + p.reason : ".");
    const canResume = p.kind === "budget" || p.side === room.you.side;
    $("resume").hidden = !canResume;
    const sel = $("resume-floor");
    const prev = sel.value;
    sel.replaceChildren();
    const keep = el("option", "", "as before");
    keep.value = "";
    sel.append(keep);
    for (const s of room.sides) {
      const o = el("option", "", sideName(s.label));
      o.value = s.label;
      sel.append(o);
    }
    sel.value = prev;
    if (p.kind === "budget" && Number($("resume-turns").value) === 0) $("resume-turns").value = 10;
  }

  function renderClosed() {
    const closed = room.phase === "closed";
    $("closed").hidden = !closed;
    $("composer").hidden = closed;
    if (!closed) return;
    let text = "Closed: " + room.close_reason;
    if (room.close_reason === "approved") text = "Closed: the resolution was approved by all sides.";
    if (room.close_reason === "expired") text = "Closed: the room expired.";
    if (room.close_note) text += " " + room.close_note;
    $("closed").textContent = text;
  }

  let mandateDirty = false;
  const invites = {}; // side label -> invitation URL issued in this page

  function renderSide() {
    if (!mandateDirty && document.activeElement !== $("mandate")) $("mandate").value = room.you.mandate || "";
    const box = $("invites");
    box.replaceChildren();
    for (const s of room.sides) {
      if (s.claimed || room.phase === "closed") continue;
      const row = el("div", "copybox");
      if (invites[s.label]) {
        const code = el("code", "", invites[s.label]);
        code.id = "invite-" + s.label;
        const copy = el("button", "", "Copy");
        copy.type = "button";
        copy.dataset.copy = code.id;
        row.append(el("span", "muted", "Invitation for " + sideName(s.label)), copy, code);
      }
      const btn = el("button", "", "New invitation for " + sideName(s.label));
      btn.type = "button";
      btn.addEventListener("click", guard(async () => {
        const res = await call("POST", "/sides/" + s.label + "/invitation");
        invites[s.label] = res.url;
        renderSide();
      }));
      row.append(btn);
      box.append(row);
    }
  }

  // Feed

  function describe(e) {
    const d = e.data || {};
    switch (e.type) {
      case "created": return "Room created";
      case "claimed": return sideName(e.side) + " accepted the invitation";
      case "joined": return who(e) + " joined";
      case "phase": return "Phase: " + d.to;
      case "ready": return sideName(e.side) + " is ready to negotiate";
      case "paused":
        if (d.kind === "budget") return "Paused: side " + d.side + " used its turn budget";
        return who(e) + " raised a hand" + (e.body ? ": " + e.body : "");
      case "resumed": return "Resumed by " + who(e) + ", floor to " + sideName(d.floor);
      case "vote": return sideName(e.side) + (d.accept ? " accepts" : " rejects") + " amendment #" + d.amendment + (e.body ? ": " + e.body : "");
      case "charter": return "Charter v" + d.version + ", item " + d.n + ": " + e.body;
      case "amendment_rejected": return "Amendment #" + d.amendment + " rejected";
      case "resolution_declined": return sideName(e.side) + " declined resolution #" + d.resolution;
      case "resolution_voided": return "Resolution #" + d.resolution + " voided: the charter changed";
      case "approved": return sideName(e.side) + " approved resolution #" + d.resolution;
      case "resolution_rejected": return sideName(e.side) + " rejected resolution #" + d.resolution + ": " + e.body;
      case "mandate": return "Mandate updated";
      case "closed": return "Room closed: " + d.reason + (e.body ? ". " + e.body : "");
      case "renamed": return "Side " + e.side + " is now " + d.name;
      case "link_issued": return null;
      case "turn": return null;
      default: return e.type;
    }
  }

  function render(e) {
    if (!MESSAGES.includes(e.type)) {
      const text = describe(e);
      if (!text) return null;
      return el("div", "event" + (e.type === "charter" ? " charter" : ""), time(e.ts) + " · " + text);
    }
    const idx = e.side ? e.side.charCodeAt(0) - 65 : 0;
    let cls = "msg " + e.actor + " side-" + e.side + (idx % 2 ? " right" : " left");
    if (e.private) cls += " private";
    if (e.type === "resolution" || e.type === "amendment") cls += " key";
    const n = el("article", cls);
    const meta = el("div", "meta");
    meta.append(el("span", "who", who(e)), el("span", "kind", e.type));
    if (e.re) meta.append(el("span", "", "re #" + e.re));
    if (e.private) meta.append(el("span", "", "private"));
    meta.append(el("span", "", time(e.ts) + " #" + e.seq));
    n.append(meta);
    if (e.summary) n.append(el("div", "summary", e.summary));
    n.append(el("div", "text", e.body));
    return n;
  }

  function nearBottom() {
    const f = $("feed");
    return f.scrollHeight - f.scrollTop - f.clientHeight < 120;
  }

  function addEvents(list) {
    const stick = nearBottom();
    const feed = $("feed");
    for (const e of list) {
      if (e.seq <= lastSeq) continue;
      lastSeq = e.seq;
      events.set(e.seq, e);
      const n = render(e);
      if (n) feed.append(n);
    }
    if (stick) feed.scrollTop = feed.scrollHeight;
  }

  // Protocol panel

  let protoTimer = null;
  function scheduleProtocol() {
    clearTimeout(protoTimer);
    protoTimer = setTimeout(() => loadProtocol().catch(() => {}), 300);
  }

  async function loadProtocol() {
    const res = await fetch(api + "/protocol", { credentials: "same-origin" });
    if (!res.ok) return;
    const p = await res.json();
    const box = $("protocol");
    box.replaceChildren();

    const head = el("div", "proto-head");
    head.append(el("div", "", "Phase: " + p.room.phase + (p.room.pause ? ", paused (" + p.room.pause.kind + ")" : "")));
    head.append(el("div", "muted", "Turns: " + p.room.sides.map((s) => s.label + " " + s.turns_used).join(", ") + " of " + p.room.turn_budget));
    if (p.room.close_reason) head.append(el("div", "", "Closed: " + p.room.close_reason + (p.room.close_note ? ". " + p.room.close_note : "")));
    box.append(head);

    box.append(el("h3", "", "Charter v" + p.room.charter.version));
    if (!p.room.charter.items.length) box.append(el("p", "muted", "No conditions yet."));
    else {
      const ol = el("ol", "charter-list");
      for (const it of p.room.charter.items) {
        const li = el("li", "", it.text);
        li.value = it.n;
        ol.append(li);
      }
      box.append(ol);
    }

    if (p.resolution) {
      box.append(el("h3", "", "Resolution #" + p.resolution.seq));
      box.append(el("p", "", sideName(p.resolution.side) + ": " + p.resolution.summary + " (" + p.resolution.status + ")"));
    }

    box.append(el("h3", "", "Threads"));
    if (!p.threads.length) box.append(el("p", "muted", "None yet."));
    for (const t of p.threads) {
      const div = el("div", "thread status-" + t.status);
      const top = el("div", "thread-head");
      top.append(el("span", "badge", t.status), el("b", "side-" + t.side, t.side), document.createTextNode(" " + t.type + " #" + t.seq + ": " + t.summary));
      div.append(top);
      for (const r of t.replies) {
        div.append(el("div", "reply", r.side + " " + r.type + " #" + r.seq + ": " + r.summary));
      }
      box.append(div);
    }

    if (p.open_questions.length) {
      box.append(el("h3", "", "Open questions"));
      const ul = el("ul");
      for (const seq of p.open_questions) {
        const t = p.threads.find((x) => x.seq === seq);
        if (t) ul.append(el("li", "", "#" + t.seq + " " + t.summary + " (" + t.status + ")"));
      }
      box.append(ul);
    }

    box.append(el("h3", "", "Timeline"));
    const tl = el("ol", "timeline");
    for (const l of p.timeline.slice(-40)) {
      tl.append(el("li", l.side ? "side-" + l.side : "server", time(l.ts) + " " + l.text));
    }
    box.append(tl);
  }

  // Live updates

  function connect() {
    const es = new EventSource(api + "/events?after=" + lastSeq);
    es.addEventListener("batch", (msg) => {
      const b = JSON.parse(msg.data);
      addEvents(b.events);
      update(b.room);
    });
  }

  // Actions

  function bind() {
    $("send").addEventListener("submit", guard(async (ev) => {
      const body = $("body").value;
      if (!body.trim()) return;
      const btn = ev.submitter;
      if (btn) btn.disabled = true;
      try {
        const msg = { type: $("type").value, body };
        if ($("private").checked && msg.type === "say") msg.private = true;
        await call("POST", "/messages", msg);
        $("body").value = "";
        $("type").value = "say";
      } finally {
        if (btn) btn.disabled = false;
      }
    }));
    $("body").addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) $("send").requestSubmit();
    });
    $("type").addEventListener("change", () => {
      $("private").disabled = $("type").value !== "say";
      if ($("private").disabled) $("private").checked = false;
    });

    $("ready").addEventListener("click", guard(() => call("POST", "/ready", {})));

    $("amd-vote").addEventListener("click", guard(async (ev) => {
      const btn = ev.target.closest("[data-accept]");
      if (!btn) return;
      await call("POST", "/amendments/" + room.amendment.seq + "/vote",
        { accept: btn.dataset.accept === "true", comment: $("amd-comment").value });
      $("amd-comment").value = "";
    }));

    $("approve").addEventListener("click", guard(() => call("POST", "/approve", { resolution: room.resolution.seq })));
    $("reject").addEventListener("click", guard(async () => {
      await call("POST", "/reject", { resolution: room.resolution.seq, comment: $("res-comment").value });
      $("res-comment").value = "";
    }));

    $("pause-open").addEventListener("click", guard(() => call("POST", "/raise", {})));
    $("resume").addEventListener("submit", guard(async () => {
      await call("POST", "/resume", { floor: $("resume-floor").value, add_turns: Number($("resume-turns").value) || 0 });
      $("resume-turns").value = 0;
    }));

    $("close-open").addEventListener("click", () => { $("close-row").hidden = false; $("close-reason").focus(); });
    $("close-cancel").addEventListener("click", () => { $("close-row").hidden = true; });
    $("close-confirm").addEventListener("click", guard(async () => {
      await call("POST", "/close", { reason: $("close-reason").value });
      $("close-row").hidden = true;
    }));

    $("mandate").addEventListener("input", () => { mandateDirty = true; $("mandate-state").textContent = "unsaved"; });
    $("mandate-save").addEventListener("click", guard(async () => {
      await call("PUT", "/mandate", { text: $("mandate").value });
      mandateDirty = false;
      $("mandate-state").textContent = "saved";
    }));

    $("agent-link").addEventListener("click", guard(async () => {
      const res = await call("POST", "/agent-link");
      $("handoff").textContent = res.handoff;
      $("handoff-box").hidden = false;
    }));

    $("export-md").href = api + "/minutes.md?download=1";
    $("export-json").href = api + "/minutes.json?download=1";
  }

  async function start() {
    const res = await fetch(api, { credentials: "same-origin" });
    if (res.status === 401 || res.status === 404) {
      $("noaccess").hidden = false;
      return;
    }
    const data = await res.json();
    $("app").hidden = false;
    update(data.room);
    bind();
    connect();
  }

  start().catch((err) => {
    $("noaccess").hidden = false;
    $("noaccess").querySelector("p").textContent = err.message;
  });
})();
