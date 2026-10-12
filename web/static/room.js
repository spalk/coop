"use strict";

(function () {
  const base = document.body.dataset.base;
  const roomID = document.body.dataset.room;
  const api = base + "/api/rooms/" + roomID;
  const $ = (id) => document.getElementById(id);

  let room = null;
  let lastSeq = 0;
  const events = new Map();

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
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
    const d = new Date(ts);
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
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
    if (!res.ok) {
      if (data && data.room) update(data.room);
      throw new Error((data && data.message) || res.statusText);
    }
    if (data && data.room) update(data.room);
    return data;
  }

  function showError(err) {
    $("error").textContent = err ? err.message : "";
  }

  // Status rendering

  function update(r) {
    room = r;
    document.title = r.topic + " · coop";
    $("topic").textContent = r.topic;
    $("you").textContent = "You: " + sideName(r.you.side);

    const st = $("status");
    st.replaceChildren();
    const state = el("span", "state-" + r.state);
    state.append(el("b", "", r.state));
    st.append(state);
    if (r.state === "open" && r.floor) {
      st.append(el("span", "", "Floor: " + sideName(r.floor) + (r.floor === r.you.side ? " (your agent)" : "")));
    }
    st.append(el("span", "", "Agent turns: " + r.turns_used + " of " + r.turn_budget));

    const sides = $("sides");
    sides.replaceChildren();
    for (const s of r.sides) {
      const span = el("span");
      span.append(document.createTextNode(sideName(s.label) + ": "));
      for (const [k, at] of [["agent", s.agent_joined_at], ["human", s.human_joined_at]]) {
        const d = el("span", "dot" + (at ? " on" : ""));
        d.title = k + (at ? " joined" : " not joined");
        span.append(d, document.createTextNode(k + " "));
      }
      sides.append(span);
    }

    renderPause();
    renderResolution();
    renderClosed();
  }

  function renderPause() {
    const box = $("pause");
    if (room.state !== "paused") {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    const p = room.pause || {};
    let text;
    if (!p.side && p.reason === "budget") text = "Paused: the agents have used their turn budget.";
    else text = "Paused by " + (p.side ? sideName(p.side) + ", " + p.actor : "the server") + (p.reason ? ": " + p.reason : ".");
    $("pause-text").textContent = text;

    const canResume = !p.side || p.side === room.you.side;
    const form = $("resume");
    form.hidden = !canResume;
    const sel = $("resume-floor");
    const prev = sel.value;
    sel.replaceChildren(el("option", "", "as before"));
    sel.firstChild.value = "";
    for (const s of room.sides) {
      const o = el("option", "", sideName(s.label));
      o.value = s.label;
      sel.append(o);
    }
    sel.value = prev;
    if (room.turns_used >= room.turn_budget && Number($("resume-turns").value) === 0) {
      $("resume-turns").value = 10;
    }
  }

  function renderResolution() {
    const box = $("resolution");
    const seq = room.pending_resolution;
    const ev = seq && events.get(seq);
    if (!ev) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    $("res-meta").textContent = "#" + seq + " by " + who(ev) + ", " + time(ev.ts);
    $("res-body").textContent = ev.body;
    const parts = room.sides.map((s) => sideName(s.label) + ": " + (s.approved_at ? "approved " + time(s.approved_at) : "not yet"));
    $("res-approvals").textContent = parts.join(" · ");
    const mine = room.sides.find((s) => s.label === room.you.side);
    $("approve").hidden = room.state === "closed" || !!(mine && mine.approved_at);
  }

  function renderClosed() {
    const box = $("closed");
    if (room.state !== "closed") {
      box.hidden = true;
      $("send").hidden = false;
      return;
    }
    box.hidden = false;
    box.textContent = room.close_reason === "approved"
      ? "Closed: the resolution was approved by all sides."
      : "Closed: the room has expired.";
    $("send").hidden = true;
    $("raise").hidden = true;
  }

  // Feed rendering

  const quiet = new Set(["turn"]);

  function describe(e) {
    const d = e.data || {};
    switch (e.type) {
      case "created": return "Room created";
      case "joined": return who(e) + " joined";
      case "paused":
        if (!e.side && d.reason === "budget") return "Paused: turn budget used up";
        return "Paused by " + who(e) + (d.reason ? ": " + d.reason : "");
      case "resumed": return "Resumed by " + who(e) + ", floor to " + sideName(d.floor) + ", budget " + d.turn_budget;
      case "approved": return who(e) + " approved resolution #" + d.resolution;
      case "closed": return "Room closed: " + d.reason;
      case "token_reissued": return "Invitation of " + sideName(e.side) + " was reissued";
      case "renamed": return "Side " + e.side + " is now " + d.name;
      default: return e.type;
    }
  }

  function render(e) {
    const isMsg = ["say", "ask", "propose", "agree", "resolution"].includes(e.type);
    if (!isMsg) {
      if (quiet.has(e.type)) return null;
      return el("div", "event", time(e.ts) + " · " + describe(e));
    }
    let cls = "msg";
    if (room && e.side === room.you.side) cls += " own";
    if (e.actor === "human") cls += " human";
    if (e.type === "resolution") cls += " resolution";
    const n = el("article", cls);
    const meta = el("div", "meta");
    meta.append(el("span", "who", who(e)), el("span", "kind", e.type));
    if (e.to) meta.append(el("span", "", "to side " + e.to));
    meta.append(el("span", "", time(e.ts)), el("span", "", "#" + e.seq));
    n.append(meta, el("div", "text", e.body));
    return n;
  }

  function nearBottom() {
    return window.innerHeight + window.scrollY >= document.body.scrollHeight - 120;
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
    if (stick) window.scrollTo(0, document.body.scrollHeight);
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
    $("send").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const body = $("body").value;
      if (!body.trim()) return;
      const btn = ev.submitter;
      if (btn) btn.disabled = true;
      try {
        await call("POST", "/messages", { type: $("type").value, body });
        $("body").value = "";
        $("type").value = "say";
        showError(null);
      } catch (err) {
        showError(err);
      } finally {
        if (btn) btn.disabled = false;
      }
    });
    $("body").addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) $("send").requestSubmit();
    });

    $("raise-open").addEventListener("click", () => {
      $("raise").hidden = false;
      $("raise-reason").focus();
    });
    $("raise-cancel").addEventListener("click", () => { $("raise").hidden = true; });
    $("raise").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      try {
        await call("POST", "/raise", { reason: $("raise-reason").value });
        $("raise-reason").value = "";
        $("raise").hidden = true;
        showError(null);
      } catch (err) {
        showError(err);
      }
    });

    $("resume").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      try {
        await call("POST", "/resume", {
          floor: $("resume-floor").value,
          add_turns: Number($("resume-turns").value) || 0,
        });
        $("resume-turns").value = 0;
        showError(null);
      } catch (err) {
        showError(err);
      }
    });

    $("approve").addEventListener("click", async () => {
      try {
        await call("POST", "/approve", { resolution: room.pending_resolution });
        showError(null);
      } catch (err) {
        showError(err);
      }
    });

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
