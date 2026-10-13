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
    // The server accepts cookie-authenticated writes only as JSON (CSRF defence).
    if (body === undefined && method !== "GET") body = {};
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
    $("unready").hidden = !(mine && mine.ready_at);
  }

  // Long resolutions and amendments are clamped to a few lines; the reader
  // expands them. The choice is kept per message while the page is open.
  const expanded = new Set();

  function clamp(bodyID, toggleID, seq) {
    const body = $(bodyID);
    const open = expanded.has(seq);
    body.classList.toggle("clamped", !open);
    $(toggleID).textContent = open ? "Collapse" : "Show all";
    $(toggleID).hidden = !open && body.scrollHeight <= body.clientHeight + 2;
  }

  function bindToggle(bodyID, toggleID, seqOf) {
    $(toggleID).addEventListener("click", () => {
      const seq = seqOf();
      if (expanded.has(seq)) expanded.delete(seq); else expanded.add(seq);
      clamp(bodyID, toggleID, seq);
    });
  }

  function renderAmendment() {
    const a = room.amendment;
    $("amendment").hidden = !a;
    if (!a) return;
    $("amd-meta").textContent = "#" + a.seq + " by " + sideName(a.side) + (a.replaces ? ", replaces item " + a.replaces : "");
    $("amd-body").textContent = a.text;
    clamp("amd-body", "amd-toggle", a.seq);
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
    clamp("res-body", "res-toggle", r.seq);
    let state;
    if (room.phase === "approval") {
      state = "Agreed by the agents. Approvals: " + room.sides.map((s) =>
        sideName(s.label) + (r.approved_by.includes(s.label) ? " approved" : " pending")).join(" · ");
    } else if (room.phase === "decided") {
      state = "Adopted by all sides. Close the room or continue: " + room.sides.map((s) =>
        sideName(s.label) + ((r.close_by || []).includes(s.label) ? " chose to close" : " deciding")).join(" · ");
    } else if (room.phase === "closed") {
      state = "Approved by all sides.";
    } else {
      state = "Waiting for the agents to agree: " + room.sides.map((s) =>
        sideName(s.label) + (r.agreed_by.includes(s.label) ? " agreed" : " pending")).join(" · ");
    }
    $("res-state").textContent = state;
    $("res-decide").hidden = room.phase !== "approval" || r.approved_by.includes(room.you.side);
    $("res-finish").hidden = room.phase !== "decided" || room.you.actor !== "human" || (r.close_by || []).includes(room.you.side);
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
    if (room.close_reason === "approved") text = "Closed by all sides after the decision.";
    if (room.close_reason === "expired") text = "Closed: the room expired.";
    if (room.close_note) text += " " + room.close_note;
    $("closed").textContent = text;
    renderPublishing();
  }

  function renderPublishing() {
    const box = $("publishing");
    box.hidden = room.phase !== "closed" || room.you.actor !== "human";
    if (box.hidden) return;
    const mine = room.sides.find((s) => s.label === room.you.side);
    const voted = !!(mine && mine.publish_at);
    const url = location.origin + base + "/p/" + roomID;
    let text;
    if (room.published_at) {
      text = "Public: anyone with the link can read this room, and it is listed on the home page.";
    } else {
      const waiting = room.sides.filter((s) => !s.publish_at).map((s) => sideName(s.label));
      text = voted
        ? "You agreed to publish. Waiting for: " + waiting.join(", ") + "."
        : "Private. Publish to let anyone read the conversation and the minutes; every side has to agree.";
    }
    $("publish-text").textContent = text;
    $("public-link-row").hidden = !room.published_at;
    $("public-link").textContent = url;
    $("public-link").href = url;
    $("publish").hidden = voted || !!room.published_at;
    $("unpublish").hidden = !voted && !room.published_at;
    $("unpublish").textContent = room.published_at ? "Unpublish" : "Withdraw";
  }

  let mandateDirty = false;
  // Links issued in this page. Tokens are stored hashed, so a link can only be
  // shown right after it is issued.
  const issued = { agent: null, sides: {} };
  let linksKey = "";

  function renderSide() {
    if (!mandateDirty && document.activeElement !== $("mandate")) $("mandate").value = room.you.mandate || "";
    renderLinks();
  }

  function linkRow(id, role, label, url) {
    const row = el("div", "linkrow");
    const head = el("div", "row");
    const copy = el("button", "", "Copy");
    copy.type = "button";
    copy.dataset.copy = id;
    copy.title = "Copy the link to the clipboard";
    head.append(el("span", "tag", role), el("span", "", label), copy);
    const code = el("code", "", url);
    code.id = id;
    row.append(head, code);
    return row;
  }

  // linkButton returns the button with its own error line: the page-wide error
  // sits under the composer, out of sight of this panel.
  function linkButton(text, title, reissue, fn) {
    const wrap = el("div", "stack");
    const b = el("button", reissue ? "" : "primary", text);
    b.type = "button";
    b.title = title;
    const err = el("div", "error");
    err.setAttribute("role", "alert");
    b.addEventListener("click", async () => {
      if (reissue && !window.confirm("The previous link will stop working. Continue?")) return;
      try {
        await fn();
        linksKey = "";
        renderLinks();
      } catch (e) {
        err.textContent = e.message;
      }
    });
    wrap.append(b, err);
    return wrap;
  }

  function renderLinks() {
    const box = $("links");
    const closed = room.phase === "closed";
    const key = JSON.stringify([closed, room.sides.map((s) => [s.name, !!s.human_joined_at, !!s.agent_joined_at]), issued]);
    if (key === linksKey) return;
    linksKey = key;
    box.replaceChildren();
    for (const s of room.sides) {
      const own = s.label === room.you.side;
      const name = s.name || "Side " + s.label;
      const card = el("section", "linkcard side-" + s.label);
      const head = el("div", "row");
      head.append(el("h3", "", "Side " + s.label + " · " + name + (own ? " (you)" : "")));
      if (!closed && (own || !(s.human_joined_at || s.agent_joined_at))) {
        const rn = el("button", "toggle", "Rename");
        rn.type = "button";
        rn.title = own ? "Change the name of your side, as everyone sees it"
          : "Name this side before you send the links; once they enter, they name themselves";
        rn.addEventListener("click", guard(async () => {
          const v = window.prompt("Name of side " + s.label, s.name || "");
          if (v === null || !v.trim()) return;
          await call("POST", "/name", own ? { name: v.trim() } : { side: s.label, name: v.trim() });
        }));
        head.append(rn);
      }
      card.append(head);
      if (own) {
        if (box.dataset.mylink) {
          card.append(linkRow("link-me", "human", "For you, on your other devices", box.dataset.mylink));
        }
        if (issued.agent) {
          card.append(linkRow("link-my-agent", "agent", "For your agent", issued.agent));
        }
        if (!closed) {
          card.append(linkButton(issued.agent ? "Reissue agent link" : "Create link for your agent",
            "A link your agent opens to join as your side. A new link replaces the previous one.", !!issued.agent, async () => {
            issued.agent = (await call("POST", "/agent-link")).agent_url;
          }));
        }
      } else {
        const entered = s.human_joined_at || s.agent_joined_at;
        const pair = issued.sides[s.label];
        if (pair) {
          card.append(el("p", "muted", "Send both links to " + name + "."));
          card.append(linkRow("link-" + s.label + "-human", "human", "For " + name, pair.human));
          card.append(linkRow("link-" + s.label + "-agent", "agent", "For the agent of " + name, pair.agent));
        }
        if (entered) {
          card.append(el("p", "muted", "Entered the room. Their links are theirs to manage."));
        } else if (!closed) {
          card.append(linkButton(pair ? "Reissue links" : "Create links for " + name,
            "Two links to send to " + name + ": one for them, one for their agent. They work until someone of that side enters; a new pair replaces the previous one.", !!pair, async () => {
            const res = await call("POST", "/sides/" + s.label + "/links");
            issued.sides[s.label] = { human: res.human_url, agent: res.agent_url };
          }));
        }
      }
      box.append(card);
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
      case "ready": return sideName(e.side) + " pressed Start agents";
      case "unready": return sideName(e.side) + " withdrew the start";
      case "decided": return "Resolution #" + d.resolution + " adopted by all sides: close the room or continue";
      case "finish_vote": return sideName(e.side) + " chose to close the room";
      case "continued": return sideName(e.side) + " continues the negotiation with a new request";
      case "publish_vote": return sideName(e.side) + " agreed to publish the room";
      case "published": return "Published: anyone can read this room";
      case "unpublished": return sideName(e.side) + " withdrew publication";
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

    if (p.decisions.length) {
      box.append(el("h3", "", "Decisions"));
      const ol = el("ol", "charter-list");
      for (const d of p.decisions) ol.append(el("li", "", "#" + d.seq + " " + sideName(d.side) + ": " + d.summary));
      box.append(ol);
    }
    if (p.resolution && !p.decisions.some((d) => d.seq === p.resolution.seq)) {
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
    if (/Mac|iPhone|iPad/.test(navigator.platform)) document.querySelector(".kbd-hint").textContent = "⌘+Enter";
    $("body").addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) $("send").requestSubmit();
    });
    $("type").addEventListener("change", () => {
      $("private").disabled = $("type").value !== "say";
      if ($("private").disabled) $("private").checked = false;
    });

    bindToggle("amd-body", "amd-toggle", () => room.amendment && room.amendment.seq);
    bindToggle("res-body", "res-toggle", () => room.resolution && room.resolution.seq);
    $("finish-close").addEventListener("click", guard(() => call("POST", "/finish", { close: true })));
    $("finish-continue").addEventListener("click", guard(async () => {
      await call("POST", "/finish", { close: false, request: $("finish-request").value });
      $("finish-request").value = "";
    }));
    $("publish").addEventListener("click", guard(() => call("POST", "/publish", {})));
    $("unpublish").addEventListener("click", guard(async () => {
      if (room.published_at && !window.confirm("The room stops being public for everyone. Continue?")) return;
      await call("POST", "/unpublish", {});
    }));
    $("ready").addEventListener("click", guard(() => call("POST", "/ready", {})));
    $("unready").addEventListener("click", guard(() => call("POST", "/unready", {})));

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
