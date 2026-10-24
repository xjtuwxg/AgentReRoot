// reroot UI. Plain JavaScript, no build step, no external dependencies:
// the page is embedded in the reroot binary and works offline.
"use strict";

const TOKEN = new URLSearchParams(location.search).get("t") || "";
const ACTS = ["active", "warm", "dormant", "stale", "unknown"];
const ACT_LABEL = { active: "Active < 30d", warm: "Warm < 90d", dormant: "Dormant < 1y", stale: "Stale > 1y", unknown: "Unknown" };

const S = {
  report: null,
  byRel: new Map(),
  plan: { version: 1, root: "", operations: [] },
  planFile: "",
  home: "",
  errors: [],
  dry: null,        // last dry-run response
  dryKey: "",       // plan JSON the dry run was made for
  sort: { key: "last", asc: false },
  filter: { q: "", acts: new Set(), agents: false, plan: false },
  selected: new Set(),
};

// ---------- helpers ----------

const $ = (sel, el = document) => el.querySelector(sel);
const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-Reroot-Token": TOKEN, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return data;
}

function toast(msg, ms = 2600) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; }, ms);
}

function ago(iso) {
  if (!iso) return "–";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  const units = [[31536000, "year"], [2592000, "month"], [604800, "week"], [86400, "day"], [3600, "hour"], [60, "minute"]];
  for (const [n, u] of units) {
    const v = Math.floor(s / n);
    if (v >= 1) return `${v} ${u}${v > 1 ? "s" : ""} ago`;
  }
  return "just now";
}

function bytes(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
}

const actColor = a => `var(--act-${a || "unknown"})`;
const actText = a => `var(--act-${a || "unknown"}-text)`;
const sessions = p => (p.agents || []).reduce((n, a) => n + a.sessions, 0);
const memories = p => (p.agents || []).reduce((n, a) => n + (a.memory_files || 0), 0);
const within = (rel, base) => rel === base || rel.startsWith(base + "/");
const parentOf = rel => rel.includes("/") ? rel.slice(0, rel.lastIndexOf("/")) : "";
// short shows paths inside the workspace relative to it, and home as "~".
function short(p) {
  if (!p) return "";
  const root = S.report && S.report.root;
  if (root && (p === root || p.startsWith(root + "/"))) return p === root ? "./" : p.slice(root.length + 1);
  if (S.home && p.startsWith(S.home + "/")) return "~" + p.slice(S.home.length);
  return p;
}
const baseOf = rel => rel.slice(rel.lastIndexOf("/") + 1);

// ---------- plan model ----------

function opPaths(op) {
  if (op.op === "move") return [op.from || ""];
  if (op.op === "merge") return op.sources || [];
  if (op.op === "archive") return [op.path || ""];
  return [];
}

function opTarget(op) {
  if (op.op === "move") return op.to || "";
  if (op.op === "merge") return op.into || "";
  return op.to || "_archive/" + (op.path || "");
}

// planOps returns the 1-based indexes of operations that touch rel.
function planOps(rel) {
  const out = [];
  S.plan.operations.forEach((op, i) => {
    if (opPaths(op).some(p => p && (within(rel, p) || within(p, rel)))) out.push(i + 1);
  });
  return out;
}

function addOp(op) {
  S.plan.operations.push(op);
  planChanged();
  toast(`Added ${op.op} to the plan (${S.plan.operations.length} operation${S.plan.operations.length > 1 ? "s" : ""})`);
}

let saveTimer = null;
function planChanged() {
  renderPlanBadge();
  renderPlan();
  if (S.report) { renderTreemap(); renderTable(); }
  clearTimeout(saveTimer);
  saveTimer = setTimeout(savePlan, 350);
}

async function savePlan() {
  try {
    const r = await api("PUT", "/api/plan", S.plan);
    S.errors = r.errors || [];
    renderPlan();
  } catch (e) {
    toast("Could not save plan: " + e.message, 5000);
  }
}

function renderPlanBadge() {
  const b = $("#plan-count");
  const n = S.plan.operations.length;
  b.textContent = n;
  b.toggleAttribute("data-zero", n === 0);
}

// ---------- loading ----------

async function loadAll(refresh = false) {
  $("#treemap").innerHTML = `<div class="loading">Scanning…</div>`;
  const [rep, pl] = await Promise.all([
    api("GET", "/api/scan" + (refresh ? "?refresh=1" : "")),
    api("GET", "/api/plan"),
  ]);
  S.report = rep;
  S.byRel = new Map(rep.projects.map(p => [p.rel, p]));
  S.plan = pl.plan;
  S.plan.operations = S.plan.operations || [];
  S.planFile = pl.file;
  S.home = pl.home || "";
  S.errors = pl.errors || [];
  $("#root-path").textContent = rep.root;
  $("#root-path").title = rep.root;
  $("#plan-file").textContent = short(S.planFile);
  fillDirs();
  renderAll();
}

function renderAll() {
  renderPlanBadge();
  renderTiles();
  renderLegend();
  renderTreemap();
  renderGroupBars();
  renderDups();
  renderActivityFilter();
  renderTable();
  renderPlan();
}

function fillDirs() {
  const dirs = new Set(S.report.groups.map(g => g.path));
  for (const p of S.report.projects) {
    let d = parentOf(p.rel);
    while (d) { dirs.add(d); d = parentOf(d); }
  }
  $("#dirs").innerHTML = [...dirs].sort().map(d => `<option value="${esc(d)}/">`).join("");
}

// ---------- overview ----------

function renderTiles() {
  const ps = S.report.projects;
  const count = a => ps.filter(p => p.activity === a).length;
  const tiles = [
    ["Projects", ps.length, null],
    ["Active", count("active"), "active"],
    ["Warm", count("warm"), "warm"],
    ["Dormant", count("dormant"), "dormant"],
    ["Stale", count("stale"), "stale"],
    ["With agent memory", ps.filter(p => sessions(p) + memories(p) > 0).length, null],
    ["Possible overlaps", (S.report.duplicates || []).length, null],
  ];
  $("#tiles").innerHTML = tiles.map(([k, v, a]) => `
    <div class="tile">
      <div class="k">${a ? `<span class="dot" style="background:${actColor(a)}"></span>` : ""}${esc(k)}</div>
      <div class="v">${v}</div>
    </div>`).join("");
}

function renderLegend() {
  $("#legend").innerHTML = ACTS.map(a => `<span><i class="dot" style="background:${actColor(a)}"></i>${ACT_LABEL[a]}</span>`).join("")
    + `<span><i class="dot" style="outline:2px dashed var(--op-move);outline-offset:-2px"></i>in plan</span>`;
}

function buildTree(sizeBy) {
  const root = { name: "", rel: "", children: new Map(), project: null };
  for (const p of S.report.projects) {
    let node = root;
    const parts = p.rel.split("/");
    parts.forEach((part, i) => {
      if (!node.children.has(part)) {
        node.children.set(part, { name: part, rel: parts.slice(0, i + 1).join("/"), children: new Map(), project: null });
      }
      node = node.children.get(part);
    });
    node.project = p;
  }
  const weight = p => {
    if (sizeBy === "equal") return 1;
    if (sizeBy === "bytes") return Math.sqrt(Math.max(p.size_bytes, 1024));
    return Math.sqrt(Math.max(p.files, 1));
  };
  const finish = n => {
    n.kids = [...n.children.values()].map(finish);
    n.value = n.project ? weight(n.project) : n.kids.reduce((s, k) => s + k.value, 0);
    n.kids.sort((a, b) => b.value - a.value);
    return n;
  };
  return finish(root);
}

// squarify lays out items (sorted by value, descending) into a rectangle.
function squarify(items, x, y, w, h) {
  const out = [];
  const total = items.reduce((s, it) => s + it.value, 0);
  if (!total || w <= 0 || h <= 0) return out;
  const scale = (w * h) / total;
  const rect = { x, y, w, h };
  let row = [];
  const worst = (r, len) => {
    const s = r.reduce((a, it) => a + it.area, 0);
    let mx = 0, mn = Infinity;
    for (const it of r) { mx = Math.max(mx, it.area); mn = Math.min(mn, it.area); }
    return Math.max((len * len * mx) / (s * s), (s * s) / (len * len * mn));
  };
  const flush = () => {
    const s = row.reduce((a, it) => a + it.area, 0);
    if (rect.w >= rect.h) {
      const cw = s / rect.h;
      let yy = rect.y;
      for (const it of row) { const hh = it.area / cw; out.push({ item: it.item, x: rect.x, y: yy, w: cw, h: hh }); yy += hh; }
      rect.x += cw; rect.w -= cw;
    } else {
      const rh = s / rect.w;
      let xx = rect.x;
      for (const it of row) { const ww = it.area / rh; out.push({ item: it.item, x: xx, y: rect.y, w: ww, h: rh }); xx += ww; }
      rect.y += rh; rect.h -= rh;
    }
    row = [];
  };
  for (const item of items) {
    const it = { item, area: item.value * scale };
    const len = Math.min(rect.w, rect.h);
    if (row.length === 0 || worst([...row, it], len) <= worst(row, len)) row.push(it);
    else { flush(); row.push(it); }
  }
  if (row.length) flush();
  return out;
}

function dominantActivity(node) {
  const counts = {};
  const walk = n => { if (n.project) counts[n.project.activity] = (counts[n.project.activity] || 0) + 1; n.kids.forEach(walk); };
  walk(node);
  return ACTS.find(a => counts[a]) || "unknown";
}

function renderTreemap() {
  const el = $("#treemap");
  const tree = buildTree($("#map-size").value);
  const W = el.clientWidth, H = el.clientHeight;
  const html = [];
  const draw = (nodes, x, y, w, h, depth) => {
    for (const r of squarify(nodes, x, y, w, h)) {
      const n = r.item;
      const gap = 2;
      const rx = r.x + gap / 2, ry = r.y + gap / 2, rw = Math.max(r.w - gap, 0), rh = Math.max(r.h - gap, 0);
      if (n.project) {
        const p = n.project;
        const inPlan = planOps(p.rel).length > 0;
        const label = rw > 38 && rh > 14 ? esc(n.name) : "";
        html.push(`<div class="tm-leaf${inPlan ? " in-plan" : ""}" data-rel="${esc(p.rel)}"
          style="left:${rx}px;top:${ry}px;width:${rw}px;height:${rh}px;background:${actColor(p.activity)};color:${actText(p.activity)}">${label}</div>`);
      } else if (rw > 30 && rh > 34 && depth < 6) {
        html.push(`<div class="tm-group" style="left:${rx}px;top:${ry}px;width:${rw}px;height:${rh}px">
          <div class="tm-label" title="${esc(n.rel)}">${esc(n.name)}</div></div>`);
        draw(n.kids, rx + 2, ry + 19, rw - 4, rh - 21, depth + 1);
      } else {
        // Too small to show its children: one block for the whole folder.
        const a = dominantActivity(n);
        html.push(`<div class="tm-leaf" data-group="${esc(n.rel)}"
          style="left:${rx}px;top:${ry}px;width:${rw}px;height:${rh}px;background:${actColor(a)};color:${actText(a)}">${rw > 38 && rh > 14 ? esc(n.name) + "/" : ""}</div>`);
      }
    }
  };
  draw(tree.kids, 0, 0, W, H, 0);
  el.innerHTML = html.join("");
}

function tooltipFor(rel) {
  const p = S.byRel.get(rel);
  if (!p) return "";
  const ag = (p.agents || []).map(a => `${a.agent} ${a.sessions} session${a.sessions === 1 ? "" : "s"}`).join(", ");
  return `<b>${esc(p.rel)}</b><br>${esc(p.activity)} · last activity ${esc(ago(p.last_activity))}<br>`
    + `${esc((p.languages || []).join(", ") || "no code detected")} · ${p.files} files · ${bytes(p.size_bytes)}`
    + (ag ? `<br>${esc(ag)}` : "")
    + (p.readme ? `<br><span style="opacity:.8">${esc(p.readme.slice(0, 140))}</span>` : "");
}

function renderGroupBars() {
  // Folders holding over a quarter of all projects are split one level
  // deeper, otherwise a single "git/" bar would swallow everything.
  const ps = S.report.projects;
  const topCount = new Map();
  for (const p of ps) {
    const t = p.rel.split("/")[0];
    topCount.set(t, (topCount.get(t) || 0) + 1);
  }
  const bucket = rel => {
    const parts = rel.split("/");
    if (parts.length === 1) return "(top level)";
    if (parts.length > 2 && topCount.get(parts[0]) > ps.length / 4) return parts[0] + "/" + parts[1];
    return parts[0];
  };
  const groups = new Map();
  for (const p of ps) {
    const top = bucket(p.rel);
    if (!groups.has(top)) groups.set(top, Object.fromEntries(ACTS.map(a => [a, 0])));
    groups.get(top)[p.activity]++;
  }
  const rows = [...groups.entries()].map(([name, c]) => ({ name, c, n: ACTS.reduce((s, a) => s + c[a], 0) }))
    .sort((a, b) => b.n - a.n);
  const max = Math.max(...rows.map(r => r.n), 1);
  $("#group-bars").innerHTML = rows.map(r => `
    <div class="gbar">
      <div class="name" data-filter="${esc(r.name === "(top level)" ? "" : r.name + "/")}" title="Show in Projects">${esc(r.name)}</div>
      <div class="bar" style="width:${(r.n / max) * 100}%">${ACTS.filter(a => r.c[a]).map(a =>
        `<span style="flex:${r.c[a]};background:${actColor(a)}" title="${r.c[a]} ${a}"></span>`).join("")}</div>
      <div class="n">${r.n}</div>
    </div>`).join("");
}

function renderDups() {
  const d = S.report.duplicates || [];
  $("#dup-count").textContent = d.length ? `${d.length} hint${d.length > 1 ? "s" : ""}` : "";
  if (!d.length) { $("#dups").innerHTML = `<div class="empty">No overlaps detected.</div>`; return; }
  $("#dups").innerHTML = d.map((x, i) => `
    <div class="dup">
      <div class="why">${esc(x.reason)}</div>
      <div class="items">${x.projects.map(r => {
        const p = S.byRel.get(r);
        return `<span class="pchip" data-rel="${esc(r)}"><i class="dot" style="background:${actColor(p && p.activity)}"></i>${esc(r)}</span>`;
      }).join("")}</div>
      <button class="btn small" data-dup-merge="${i}">Merge…</button>
    </div>`).join("");
}

// ---------- projects table ----------

function renderActivityFilter() {
  $("#activity-filter").innerHTML = ACTS.map(a => `
    <button class="chip${S.filter.acts.has(a) ? " on" : ""}" data-act="${a}"><i class="dot" style="background:${actColor(a)}"></i>${a}</button>`).join("");
}

function filteredProjects() {
  const q = S.filter.q.trim().toLowerCase();
  const ps = S.report.projects.filter(p => {
    if (S.filter.acts.size && !S.filter.acts.has(p.activity)) return false;
    if (S.filter.agents && sessions(p) + memories(p) === 0) return false;
    if (S.filter.plan && planOps(p.rel).length === 0) return false;
    if (q) {
      const hay = [p.rel, (p.languages || []).join(" "), p.readme || "", (p.markers || []).join(" ")].join(" ").toLowerCase();
      if (!q.split(/\s+/).every(t => hay.includes(t))) return false;
    }
    return true;
  });
  const k = S.sort.key;
  const val = p => ({
    rel: p.rel, activity: ACTS.indexOf(p.activity), last: p.last_activity || "",
    languages: (p.languages || [])[0] || "", size: p.size_bytes,
    commits: p.git ? p.git.commits_90d : -1, agents: sessions(p),
  })[k];
  ps.sort((a, b) => {
    const va = val(a), vb = val(b);
    const c = va < vb ? -1 : va > vb ? 1 : 0;
    return S.sort.asc ? c : -c;
  });
  return ps;
}

function renderTable() {
  const ps = filteredProjects();
  $$("#projects th[data-sort]").forEach(th => {
    th.classList.toggle("sorted", th.dataset.sort === S.sort.key);
    th.classList.toggle("asc", th.dataset.sort === S.sort.key && S.sort.asc);
  });
  $("#projects tbody").innerHTML = ps.map(p => {
    const inPlan = planOps(p.rel);
    const ag = (p.agents || []).map(a => `${a.agent} ${a.sessions}`).join(" · ");
    return `<tr data-rel="${esc(p.rel)}">
      <td class="sel"><input type="checkbox" data-sel="${esc(p.rel)}" ${S.selected.has(p.rel) ? "checked" : ""} aria-label="Select"></td>
      <td class="path" title="${esc(p.path)}">${esc(p.rel)}
        ${inPlan.length ? `<span class="tag plan">plan #${inPlan.join(",")}</span>` : ""}
        ${p.git && p.git.dirty ? `<span class="tag dirty">uncommitted</span>` : ""}
        ${(p.warnings || []).length ? `<span class="tag warn" title="${esc(p.warnings.join("\n"))}">warning</span>` : ""}</td>
      <td><span class="act"><i class="dot" style="background:${actColor(p.activity)}"></i>${esc(p.activity)}</span></td>
      <td>${esc(ago(p.last_activity))}</td>
      <td>${esc((p.languages || []).join(", "))}</td>
      <td class="num">${bytes(p.size_bytes)}</td>
      <td class="num">${p.git ? p.git.commits_90d : "–"}</td>
      <td class="num" title="${esc(ag)}">${sessions(p) || ""}</td>
      <td><div class="row-actions">
        <button class="btn small" data-move="${esc(p.rel)}">Move…</button>
        <button class="btn small" data-archive="${esc(p.rel)}">Archive</button>
      </div></td>
    </tr>`;
  }).join("");
  const n = S.selected.size;
  $("#sel-info").textContent = n ? `${n} selected` : `${ps.length} of ${S.report.projects.length} projects`;
  $("#merge-selected").disabled = n < 2;
  $("#archive-selected").disabled = n < 1;
}

// ---------- drawer ----------

function openDrawer(rel) {
  const p = S.byRel.get(rel);
  if (!p) return;
  const g = p.git;
  const kv = [
    ["Activity", `<span class="act"><i class="dot" style="background:${actColor(p.activity)}"></i>${esc(p.activity)}</span> · ${esc(ago(p.last_activity))}`],
    ["Path", `<span class="mono">${esc(p.path)}</span>`],
    ["Detected by", esc((p.markers || []).join(", "))],
    ["Languages", esc((p.languages || []).join(", ") || "–")],
    ["Files", `${p.files}${p.truncated ? "+" : ""} · ${bytes(p.size_bytes)}`],
  ];
  if (g) {
    kv.push(["Git origin", esc(g.origin || "–")]);
    kv.push(["Last commit", esc(g.last_commit ? ago(g.last_commit) : "–")]);
    kv.push(["Commits (90d)", String(g.commits_90d)]);
    kv.push(["Working tree", g.dirty ? `<span class="tag dirty">uncommitted changes</span>` : "clean"]);
    if (g.worktrees) kv.push(["Worktrees", String(g.worktrees)]);
  }
  const agents = (p.agents || []).map(a => `<div class="kv"><div class="k">${esc(a.agent)}</div><div class="v">${a.sessions} session${a.sessions === 1 ? "" : "s"}${a.memory_files ? `, ${a.memory_files} memory file${a.memory_files === 1 ? "" : "s"}` : ""} · last ${esc(ago(a.last_active))}</div></div>`).join("");
  const inPlan = planOps(rel);
  $("#drawer-body").innerHTML = `
    <h3>${esc(p.rel)}</h3>
    ${inPlan.length ? `<div class="okbox">In plan: operation ${inPlan.map(i => "#" + i).join(", ")}</div>` : ""}
    <div class="actions">
      <button class="btn primary" data-move="${esc(rel)}">Move…</button>
      <button class="btn" data-merge-with="${esc(rel)}">Merge with…</button>
      <button class="btn" data-archive="${esc(rel)}">Archive</button>
      <button class="btn ghost" data-copy="${esc(p.path)}">Copy path</button>
    </div>
    ${p.readme ? `<div class="readme">${esc(p.readme)}</div>` : ""}
    <div class="kv">${kv.map(([k, v]) => `<div class="k">${esc(k)}</div><div class="v">${v}</div>`).join("")}</div>
    <h4>Agent memory</h4>
    ${agents || `<div class="muted">No Claude Code or Codex state for this project.</div>`}
    ${(p.warnings || []).length ? `<h4>Warnings</h4>${p.warnings.map(w => `<div class="warnbox">${esc(w)}</div>`).join("")}` : ""}`;
  $("#drawer").classList.add("open");
  $("#drawer").setAttribute("aria-hidden", "false");
}

function closeDrawer() {
  $("#drawer").classList.remove("open");
  $("#drawer").setAttribute("aria-hidden", "true");
}

// ---------- modals ----------

function modal(title, bodyHTML, okLabel, onOk) {
  $("#modal-title").textContent = title;
  $("#modal-body").innerHTML = bodyHTML;
  $("#modal-ok").textContent = okLabel;
  $("#modal").hidden = false;
  const first = $("#modal-body input, #modal-body textarea");
  if (first) setTimeout(() => first.focus(), 0);
  modal.onOk = onOk;
}

function closeModal() { $("#modal").hidden = true; modal.onOk = null; }

function moveDialog(rel) {
  modal(`Move ${rel}`, `
    <div class="field"><label for="m-to">New location (relative to the workspace root)</label>
      <input type="text" id="m-to" list="dirs" value="${esc(rel)}"></div>
    <div class="field"><label for="m-reason">Reason (optional, kept in the plan)</label>
      <input type="text" id="m-reason" placeholder="e.g. grew from a demo into a security tool"></div>`,
  "Add to plan", () => {
    const raw = $("#m-to").value.trim();
    const to = raw.endsWith("/") ? raw + baseOf(rel) : raw;
    if (!to || to === rel) { toast("Choose a different location"); return false; }
    addOp({ op: "move", from: rel, to, reason: $("#m-reason").value.trim() || undefined });
  });
  // Typing a folder from the list (ending in "/") keeps the project name.
  $("#m-to").addEventListener("change", e => {
    if (e.target.value.endsWith("/")) e.target.value += baseOf(rel);
  });
}

function mergeDialog(sources) {
  const guess = sources.length ? parentOf(sources[0]) + "/" + baseOf(sources[0]).replace(/[-_.]?(v\d+|backup|bak|old|copy|\d+)$/i, "") + "-merged" : "";
  modal("Merge projects", `
    <div class="field"><label for="m-src">Sources, one per line. Each becomes &lt;into&gt;/&lt;name&gt;.</label>
      <textarea id="m-src" rows="${Math.max(3, sources.length + 1)}">${esc(sources.join("\n"))}</textarea></div>
    <div class="field"><label for="m-into">Into</label>
      <input type="text" id="m-into" list="dirs" value="${esc(guess.replace(/^\//, ""))}"></div>
    <div class="field"><label for="m-reason">Reason (optional)</label>
      <input type="text" id="m-reason" placeholder="e.g. two attempts at the same problem"></div>
    <p class="hint">Agent memory of every source is also copied into the memory of the merge target. Code is not merged.</p>`,
  "Add to plan", () => {
    const srcs = $("#m-src").value.split("\n").map(s => s.trim()).filter(Boolean);
    const into = $("#m-into").value.trim().replace(/\/+$/, "");
    if (srcs.length < 1 || !into) { toast("Need sources and a target"); return false; }
    addOp({ op: "merge", sources: srcs, into, reason: $("#m-reason").value.trim() || undefined });
  });
}

function archiveRel(rel) {
  addOp({ op: "archive", path: rel });
}

// ---------- plan tab ----------

function opImpact(op) {
  if (!S.report) return "";
  const paths = opPaths(op).filter(Boolean);
  const ps = S.report.projects.filter(p => paths.some(x => within(p.rel, x)));
  if (!ps.length) return "";
  const per = {};
  for (const p of ps) for (const a of p.agents || []) per[a.agent] = (per[a.agent] || 0) + a.sessions;
  const parts = [`${ps.length} project${ps.length > 1 ? "s" : ""}`];
  for (const [a, n] of Object.entries(per)) parts.push(`${n} ${a} session${n > 1 ? "s" : ""}`);
  const mem = ps.reduce((n, p) => n + memories(p), 0);
  if (mem) parts.push(`${mem} memory file${mem > 1 ? "s" : ""}`);
  const dirty = ps.filter(p => p.git && p.git.dirty).length;
  if (dirty) parts.push(`${dirty} with uncommitted changes`);
  return parts.join(" · ");
}

function renderPlan() {
  const ops = S.plan.operations;
  $("#ops-empty").hidden = ops.length > 0;
  const errsFor = i => S.errors.filter(e => e.index === i + 1);
  const field = (i, key, label, value, ph) => `
    <div class="op-row"><label for="op${i}-${key}">${label}</label>
      <input type="text" id="op${i}-${key}" data-op="${i}" data-key="${key}" list="dirs" value="${esc(value || "")}" placeholder="${esc(ph || "")}"></div>`;
  $("#ops").innerHTML = ops.map((op, i) => {
    const errs = errsFor(i);
    let body = "";
    if (op.op === "move") body = field(i, "from", "from", op.from) + field(i, "to", "to", op.to);
    else if (op.op === "merge") body = field(i, "sources", "sources", (op.sources || []).join(", "), "a/x, b/y") + field(i, "into", "into", op.into);
    else body = field(i, "path", "path", op.path) + field(i, "to", "to", op.to, "_archive/" + (op.path || "…"));
    body += field(i, "reason", "reason", op.reason, "why (optional)");
    const impact = opImpact(op);
    return `<div class="op ${esc(op.op)}${errs.length ? " has-error" : ""}">
      <div class="op-head">
        <span class="op-num">#${i + 1}</span><span class="op-kind">${esc(op.op)}</span>
        <span class="spacer"></span>
        <button class="iconbtn" data-up="${i}" title="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
        <button class="iconbtn" data-down="${i}" title="Move down" ${i === ops.length - 1 ? "disabled" : ""}>↓</button>
        <button class="iconbtn" data-del="${i}" title="Remove">✕</button>
      </div>
      ${body}
      ${impact ? `<div class="op-impact">Affects ${esc(impact)}</div>` : ""}
      ${errs.map(e => `<div class="op-err">${esc(e.msg)}</div>`).join("")}
    </div>`;
  }).join("");
  const general = S.errors.filter(e => !e.index);
  if (general.length) $("#ops").insertAdjacentHTML("afterbegin", general.map(e => `<div class="errbox">${esc(e.msg)}</div>`).join(""));
  const fresh = S.dry && S.dryKey === JSON.stringify(S.plan);
  const ok = fresh && !S.dry.run_error && (S.dry.errors || []).length === 0 && ops.length > 0;
  $("#apply").disabled = !ok;
  $("#dryrun").disabled = ops.length === 0;
  if (S.dry && !fresh) {
    const pv = $("#preview");
    if (!pv.querySelector(".stale-note")) pv.insertAdjacentHTML("afterbegin", `<div class="warnbox stale-note">The plan changed since this dry run. Run it again before applying.</div>`);
  }
}

function classify(a) {
  const p = a.path || "";
  if (a.kind === "git") return "git";
  if (/\/\.claude(\/|\.json$)/.test(p)) return "claude";
  if (/\/\.codex\//.test(p)) return "codex";
  if (a.kind === "warn" || a.kind === "note") return "note";
  return "files";
}

function renderPreview() {
  const d = S.dry;
  const pv = $("#preview");
  if (!d) return;
  if ((d.errors || []).length) {
    pv.innerHTML = `<div class="errbox">Fix the plan errors first (${d.errors.length}).</div>`;
    return;
  }
  const r = d.result || { actions: [] };
  const byOp = new Map();
  for (const a of r.actions || []) {
    if (!byOp.has(a.op)) byOp.set(a.op, []);
    byOp.get(a.op).push(a);
  }
  const head = [];
  for (const w of r.warnings || []) head.push(`<div class="warnbox">${esc(w)}</div>`);
  if (d.run_error) head.push(`<div class="errbox">${esc(d.run_error)}</div>`);
  const total = (r.actions || []).length;
  head.push(`<div class="okbox">Dry run: ${total} change${total === 1 ? "" : "s"} across ${byOp.size} operation${byOp.size === 1 ? "" : "s"}. Nothing has been changed yet.</div>`);
  const sections = S.plan.operations.map((op, i) => {
    const acts = byOp.get(i + 1) || [];
    const c = { files: 0, claude: 0, codex: 0, git: 0, note: 0 };
    acts.forEach(a => c[classify(a)]++);
    const label = `${opPaths(op).join(", ")} → ${opTarget(op)}`;
    const counts = [
      c.files && `<span class="cnt">${c.files} folder</span>`,
      c.claude && `<span class="cnt claude">${c.claude} Claude</span>`,
      c.codex && `<span class="cnt codex">${c.codex} Codex</span>`,
      c.git && `<span class="cnt">${c.git} git</span>`,
      c.note && `<span class="cnt">${c.note} note</span>`,
    ].filter(Boolean).join("");
    return `<details class="pv-op"${i === 0 ? " open" : ""}>
      <summary><span class="op-num">#${i + 1}</span><span class="op-kind" style="color:var(--op-${esc(op.op)})">${esc(op.op)}</span>
        <span class="mono">${esc(label)}</span><span class="counts">${counts}</span></summary>
      <div class="pv-list">${acts.map(a => `
        <div class="pv-act"><div class="k">${esc(a.kind)}${a.count ? " ×" + a.count : ""}</div>
          <div><div class="p" title="${esc(a.path || "")}">${esc(short(a.path))}${a.target ? " → " + esc(short(a.target)) : ""}</div>
          ${a.desc && !a.target ? `<div class="d">${esc(a.desc)}</div>` : ""}</div></div>`).join("")}</div>
    </details>`;
  });
  pv.innerHTML = head.join("") + sections.join("");
}

async function dryRun() {
  $("#dryrun").disabled = true;
  $("#preview").innerHTML = `<div class="loading">Running dry run…</div>`;
  try {
    const key = JSON.stringify(S.plan);
    S.dry = await api("POST", "/api/dryrun", S.plan);
    S.dryKey = key;
    S.errors = S.dry.errors || [];
    renderPreview();
  } catch (e) {
    $("#preview").innerHTML = `<div class="errbox">${esc(e.message)}</div>`;
  }
  renderPlan();
}

function applyDialog() {
  const r = S.dry.result;
  const warn = (r.warnings || []).map(w => `<div class="warnbox">${esc(w)}</div>`).join("");
  modal("Apply this plan?", `
    ${warn}
    <p>This executes ${r.actions.length} changes exactly as shown in the dry run:</p>
    <ul>${S.plan.operations.map(op => `<li><b>${esc(op.op)}</b> <span class="mono">${esc(opPaths(op).join(", "))}</span> → <span class="mono">${esc(opTarget(op))}</span></li>`).join("")}</ul>
    <p class="hint">Every change is journaled and can be rolled back from the History tab.</p>
    <label class="check"><input type="checkbox" id="m-confirm"> I reviewed the dry run</label>`,
  "Apply", async () => {
    if (!$("#m-confirm").checked) { toast("Tick the confirmation box first"); return false; }
    closeModal();
    $("#preview").innerHTML = `<div class="loading">Applying…</div>`;
    try {
      const res = await api("POST", "/api/apply?confirm=yes", S.plan);
      if ((res.errors || []).length) { S.errors = res.errors; S.dry = null; renderPlan(); toast("Plan is no longer valid"); return; }
      const id = res.result && res.result.journal_id;
      if (res.run_error) {
        $("#preview").innerHTML = `<div class="errbox">${esc(res.run_error)}</div>`;
        toast("Apply failed; partial changes can be undone from History", 6000);
      } else {
        $("#preview").innerHTML = `<div class="okbox">Applied ${res.result.actions.length} changes. Journal <code>${esc(id)}</code>. Undo from the History tab.</div>`;
        toast("Plan applied");
        S.plan.operations = [];
        S.dry = null;
        await savePlan();
      }
      await loadAll(true);
      renderHistory();
    } catch (e) {
      $("#preview").innerHTML = `<div class="errbox">${esc(e.message)}</div>`;
    }
  });
}

// ---------- history ----------

async function renderHistory() {
  const el = $("#history");
  try {
    const list = await api("GET", "/api/history");
    if (!list.length) { el.innerHTML = `<div class="empty">Nothing applied yet.</div>`; return; }
    el.innerHTML = list.map(m => `
      <div class="journal">
        <div>
          <div><code>${esc(m.id)}</code> <span class="status ${esc(m.status)}">${esc(m.status)}</span></div>
          <div class="meta">${esc(new Date(m.created).toLocaleString())} · ${esc(m.root || "")}</div>
          ${(m.summary || []).length ? `<ul>${m.summary.map(s => `<li>${esc(s)}</li>`).join("")}</ul>` : ""}
        </div>
        <div>${m.status === "applied" || m.status === "failed" ? `<button class="btn danger" data-undo="${esc(m.id)}">Undo…</button>` : ""}</div>
      </div>`).join("");
  } catch (e) {
    el.innerHTML = `<div class="errbox">${esc(e.message)}</div>`;
  }
}

function undoDialog(id) {
  modal("Undo this apply?", `<p>Folders move back and every rewritten agent-state file is restored from the journal <code>${esc(id)}</code>.</p>
    <p class="hint">Files that changed after the apply are skipped and reported.</p>`, "Undo", async () => {
    closeModal();
    try {
      const r = await api("POST", "/api/undo", { id });
      toast(r.run_error ? r.run_error : "Undone", r.run_error ? 6000 : 2600);
      await loadAll(true);
      renderHistory();
    } catch (e) {
      toast(e.message, 6000);
    }
  });
}

async function runVerify() {
  const el = $("#verify-out");
  el.innerHTML = `<div class="loading">Checking…</div>`;
  try {
    const list = await api("GET", "/api/verify");
    if (!list.length) { el.innerHTML = `<div class="okbox">No problems found.</div>`; return; }
    el.innerHTML = `<div class="table-wrap"><table><thead><tr><th>Agent</th><th>Kind</th><th>Path</th><th>Detail</th></tr></thead><tbody>${
      list.map(f => `<tr><td>${esc(f.agent)}</td><td>${esc(f.kind)}</td><td class="path" title="${esc(f.path)}">${esc(f.path)}</td><td class="muted">${esc(f.detail)}</td></tr>`).join("")
    }</tbody></table></div>`;
  } catch (e) {
    el.innerHTML = `<div class="errbox">${esc(e.message)}</div>`;
  }
}

// ---------- events ----------

function showTab(name) {
  $$(".tabs button").forEach(b => b.classList.toggle("active", b.dataset.tab === name));
  $$(".tab").forEach(t => t.classList.toggle("active", t.id === "tab-" + name));
  if (name === "history") renderHistory();
  if (name === "overview" && S.report) renderTreemap();
}

document.addEventListener("click", e => {
  const t = e.target.closest("button, [data-rel], [data-group], [data-filter], th[data-sort], tr[data-rel]");
  if (!t) return;
  const d = t.dataset;
  if (d.tab) return showTab(d.tab);
  if (d.move) { e.stopPropagation(); return moveDialog(d.move); }
  if (d.archive) { e.stopPropagation(); return archiveRel(d.archive); }
  if (d.mergeWith) return mergeDialog([d.mergeWith]);
  if (d.copy) { navigator.clipboard?.writeText(d.copy).then(() => toast("Path copied"), () => toast(d.copy)); return; }
  if (d.dupMerge) return mergeDialog(S.report.duplicates[+d.dupMerge].projects);
  if (d.add) {
    const blank = { move: { op: "move", from: "", to: "" }, merge: { op: "merge", sources: [], into: "" }, archive: { op: "archive", path: "" } }[d.add];
    S.plan.operations.push(blank);
    return planChanged();
  }
  if (d.del) { S.plan.operations.splice(+d.del, 1); return planChanged(); }
  if (d.up || d.down) {
    const i = +(d.up ?? d.down), j = d.up ? i - 1 : i + 1;
    const ops = S.plan.operations;
    [ops[i], ops[j]] = [ops[j], ops[i]];
    return planChanged();
  }
  if (d.undo) return undoDialog(d.undo);
  if (d.act) {
    S.filter.acts.has(d.act) ? S.filter.acts.delete(d.act) : S.filter.acts.add(d.act);
    renderActivityFilter();
    return renderTable();
  }
  if (d.sort) {
    S.sort = { key: d.sort, asc: S.sort.key === d.sort ? !S.sort.asc : d.sort === "rel" };
    return renderTable();
  }
  if (d.filter !== undefined) {
    S.filter.q = d.filter;
    $("#q").value = d.filter;
    showTab("projects");
    return renderTable();
  }
  if (d.group) {
    S.filter.q = d.group + "/";
    $("#q").value = S.filter.q;
    showTab("projects");
    return renderTable();
  }
  if (d.rel && !(e.target instanceof HTMLInputElement)) return openDrawer(d.rel);
});

document.addEventListener("change", e => {
  const t = e.target;
  if (t.dataset.sel) {
    t.checked ? S.selected.add(t.dataset.sel) : S.selected.delete(t.dataset.sel);
    return renderTable();
  }
  if (t.id === "sel-all") {
    filteredProjects().forEach(p => t.checked ? S.selected.add(p.rel) : S.selected.delete(p.rel));
    return renderTable();
  }
});

document.addEventListener("input", e => {
  const t = e.target;
  if (t.dataset.op !== undefined) {
    const op = S.plan.operations[+t.dataset.op];
    const v = t.value;
    if (t.dataset.key === "sources") op.sources = v.split(",").map(s => s.trim()).filter(Boolean);
    else if (v) op[t.dataset.key] = v.trim();
    else delete op[t.dataset.key];
    // Re-render lazily so the input keeps focus while typing.
    renderPlanBadge();
    clearTimeout(saveTimer);
    saveTimer = setTimeout(async () => { await savePlan(); }, 450);
    S.dry && renderPlan();
    return;
  }
  if (t.id === "q") { S.filter.q = t.value; renderTable(); }
});

// Plan inputs re-render (impact, errors) when they lose focus.
document.addEventListener("focusout", e => {
  if (e.target.dataset && e.target.dataset.op !== undefined) setTimeout(() => {
    if (!document.activeElement || document.activeElement.dataset.op === undefined) { renderPlan(); renderTable(); renderTreemap(); }
  }, 0);
});

$("#only-agents").addEventListener("change", e => { S.filter.agents = e.target.checked; renderTable(); });
$("#only-plan").addEventListener("change", e => { S.filter.plan = e.target.checked; renderTable(); });
$("#merge-selected").addEventListener("click", () => mergeDialog([...S.selected]));
$("#archive-selected").addEventListener("click", () => {
  [...S.selected].forEach(r => S.plan.operations.push({ op: "archive", path: r }));
  S.selected.clear();
  planChanged();
  toast("Added archive operations to the plan");
});
$("#map-size").addEventListener("change", renderTreemap);
$("#rescan").addEventListener("click", () => loadAll(true).then(() => toast("Rescanned")).catch(err => toast(err.message, 6000)));
$("#dryrun").addEventListener("click", dryRun);
$("#apply").addEventListener("click", applyDialog);
$("#verify").addEventListener("click", runVerify);
$("#drawer-close").addEventListener("click", closeDrawer);
$("#modal-cancel").addEventListener("click", closeModal);
$("#modal-ok").addEventListener("click", async () => {
  const f = modal.onOk;
  if (!f) return closeModal();
  const keep = await f();
  if (keep !== false && !$("#modal").hidden && modal.onOk === f) closeModal();
});
$("#modal").addEventListener("click", e => { if (e.target.id === "modal") closeModal(); });
document.addEventListener("keydown", e => {
  if (e.key === "Escape") { if (!$("#modal").hidden) closeModal(); else closeDrawer(); }
  if (e.key === "Enter" && !$("#modal").hidden && e.target.tagName === "INPUT" && e.target.type === "text") $("#modal-ok").click();
});

const tip = $("#tooltip");
document.addEventListener("mousemove", e => {
  const leaf = e.target.closest(".tm-leaf");
  if (!leaf) { tip.hidden = true; return; }
  tip.innerHTML = leaf.dataset.rel ? tooltipFor(leaf.dataset.rel) : `<b>${esc(leaf.dataset.group)}/</b><br>folder too small to show its projects; click to list them`;
  tip.hidden = false;
  const x = Math.min(e.clientX + 14, innerWidth - tip.offsetWidth - 8);
  const y = Math.min(e.clientY + 14, innerHeight - tip.offsetHeight - 8);
  tip.style.left = x + "px";
  tip.style.top = y + "px";
});

let resizeTimer;
addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => S.report && renderTreemap(), 120); });

if (!TOKEN) {
  document.querySelector("main").innerHTML = `<div class="card errbox">Open this page with the URL printed by <code>reroot ui</code> (it carries an access token).</div>`;
} else {
  loadAll().catch(e => {
    document.querySelector("main").innerHTML = `<div class="card errbox">${esc(e.message)}</div>`;
  });
}
