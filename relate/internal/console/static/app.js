"use strict";

// Symaira Relate — local web console. Vanilla JS, no build step, no CDN,
// no telemetry: every request goes to this same-origin /api/v1/* surface,
// which is a thin wrapper over the same service layer the CLI and MCP
// server use (see docs/CONSOLE.md).

const view = document.getElementById("view");
const statusEl = document.getElementById("status");

function announce(text) {
  statusEl.textContent = text;
}

async function api(path, opts) {
  const res = await fetch(path, Object.assign({ headers: { "Content-Type": "application/json" } }, opts));
  let body = null;
  const text = await res.text();
  if (text) {
    try { body = JSON.parse(text); } catch (e) { body = null; }
  }
  if (!res.ok) {
    const message = (body && body.error) || `request failed (${res.status})`;
    throw new Error(message);
  }
  return body;
}

function el(tag, attrs, children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === false) continue; // omit unset optional/boolean attributes entirely
    if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const child of children || []) {
    if (child) node.appendChild(typeof child === "string" ? document.createTextNode(child) : child);
  }
  return node;
}

function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

function showLoading() {
  clear(view);
  view.appendChild(el("p", { class: "loading", text: "Loading…" }));
}

function showError(err) {
  clear(view);
  view.appendChild(el("p", { class: "error", role: "alert", text: String(err.message || err) }));
  announce("Error: " + (err.message || err));
}

// -- routing -----------------------------------------------------------

const routes = new Map([
  ["contacts", renderContacts],
  ["organizations", renderOrganizations],
  ["followups", renderFollowUps],
  ["import", renderImport],
]);

function setActiveTab(name) {
  document.querySelectorAll(".tab").forEach((btn) => {
    if (btn.dataset.view === name) btn.setAttribute("aria-current", "page");
    else btn.removeAttribute("aria-current");
  });
}

function navigate(name, param) {
  location.hash = param ? `${name}/${param}` : name;
}

function currentRoute() {
  const raw = location.hash.replace(/^#/, "") || "contacts";
  const [name, param] = raw.split("/");
  // Map.has is an own-entry check: inherited names like "constructor" or
  // "toString" can never reach the dispatch below.
  return { name: routes.has(name) ? name : "contacts", param };
}

function renderRoute() {
  const { name, param } = currentRoute();
  setActiveTab(name);
  view.focus();
  routes.get(name)(param).catch(showError);
}

window.addEventListener("hashchange", renderRoute);
document.querySelectorAll(".tab").forEach((btn) => {
  btn.addEventListener("click", () => navigate(btn.dataset.view));
});

// -- contacts ------------------------------------------------------------

async function renderContacts(personId) {
  if (personId) return renderContactDetail(personId);

  showLoading();
  const query = new URLSearchParams(location.search).get("q") || "";
  const data = await api("/api/v1/contacts?" + new URLSearchParams(query ? { q: query } : {}));

  clear(view);
  const toolbar = el("div", { class: "toolbar" }, [
    el("label", { for: "contact-search", class: "visually-hidden", text: "Search contacts by name" }),
    el("input", { id: "contact-search", type: "search", placeholder: "Search by name…", value: query }),
    el("button", { type: "button", onclick: () => renderContactAddForm() }, ["Add contact"]),
  ]);
  const searchInput = toolbar.querySelector("#contact-search");
  searchInput.addEventListener("change", () => {
    const q = searchInput.value.trim();
    history.replaceState(null, "", q ? `?q=${encodeURIComponent(q)}#contacts` : "#contacts");
    renderRoute();
  });
  view.appendChild(toolbar);

  const items = (data.Items || []);
  if (items.length === 0) {
    view.appendChild(el("p", { class: "empty", text: "No contacts found." }));
    return;
  }

  const table = el("table", {}, [
    el("thead", {}, [el("tr", {}, [el("th", { text: "Name" }), el("th", { text: "" })])]),
  ]);
  const tbody = el("tbody");
  for (const p of items) {
    tbody.appendChild(el("tr", {}, [
      el("td", {}, [el("button", { type: "button", onclick: () => navigate("contacts", p.ID) }, [p.DisplayName])]),
      el("td", { text: p.ID }),
    ]));
  }
  table.appendChild(tbody);
  view.appendChild(table);
  if (data.HasMore) view.appendChild(el("p", { class: "empty", text: "More results available — refine your search." }));
}

function renderContactAddForm() {
  clear(view);
  const form = el("form", { class: "card", onsubmit: onSubmitContactAdd }, [
    el("h2", { text: "Add contact" }),
    field("name", "Display name", "text", true),
    field("email", "Email", "email", false),
    field("phone", "Phone", "tel", false),
    field("notes", "Notes", "text", false),
    el("button", { type: "submit", class: "btn-primary" }, ["Create"]),
    el("button", { type: "button", onclick: () => navigate("contacts") }, ["Cancel"]),
  ]);
  view.appendChild(form);
  form.querySelector("input").focus();
}

function field(name, labelText, type, required) {
  const id = "field-" + name;
  return el("div", { class: "field" }, [
    el("label", { for: id, text: labelText + (required ? " *" : "") }),
    el("input", { id, name, type, required: required ? "required" : undefined }),
  ]);
}

async function onSubmitContactAdd(evt) {
  evt.preventDefault();
  const fd = new FormData(evt.target);
  try {
    const p = await api("/api/v1/contacts", {
      method: "POST",
      body: JSON.stringify({ display_name: fd.get("name"), email: fd.get("email") || "", phone: fd.get("phone") || "", notes: fd.get("notes") || "" }),
    });
    announce(`Created ${p.DisplayName}`);
    navigate("contacts", p.ID);
  } catch (err) { showError(err); }
}

async function renderContactDetail(id) {
  showLoading();
  const [person, timeline, memberships] = await Promise.all([
    api(`/api/v1/contacts/${id}`),
    api(`/api/v1/contacts/${id}/timeline`),
    api(`/api/v1/contacts/${id}/memberships`),
  ]);

  clear(view);
  view.appendChild(el("button", { type: "button", onclick: () => navigate("contacts") }, ["← Back to contacts"]));

  const card = el("div", { class: "card" }, [
    el("h2", { text: person.DisplayName }),
    el("p", { text: "ID: " + person.ID }),
  ]);
  if (person.ContactPoints && person.ContactPoints.length) {
    const list = el("ul");
    for (const cp of person.ContactPoints) list.appendChild(el("li", { text: `${cp.Kind}: ${cp.RawValue}` }));
    card.appendChild(list);
  }
  card.appendChild(el("button", { type: "button", class: "btn-danger", onclick: () => onErase(id) }, ["Erase contact"]));
  view.appendChild(card);

  view.appendChild(el("h3", { text: "Memberships" }));
  if (!memberships || memberships.length === 0) {
    view.appendChild(el("p", { class: "empty", text: "No organization memberships." }));
  } else {
    const list = el("ul");
    for (const m of memberships) list.appendChild(el("li", { text: `${m.Role || "member"} at ${m.OrganizationID}` }));
    view.appendChild(list);
  }

  view.appendChild(el("h3", { text: "Timeline" }));
  if (!timeline || timeline.length === 0) {
    view.appendChild(el("p", { class: "empty", text: "No interactions or follow-ups yet." }));
    return;
  }
  const list = el("ul");
  for (const entry of timeline) {
    const label = entry.Kind === "interaction"
      ? `${entry.Interaction.Kind}: ${entry.Interaction.Summary}`
      : `follow-up (${entry.FollowUp.Status}): ${entry.FollowUp.Notes}`;
    list.appendChild(el("li", { text: `${new Date(entry.At).toLocaleString()} — ${label}` }));
  }
  view.appendChild(list);
}

async function onErase(id) {
  if (!confirm("Erase this contact? This permanently deletes it and cannot be undone.")) return;
  try {
    await api(`/api/v1/contacts/${id}`, { method: "DELETE" });
    announce("Contact erased");
    navigate("contacts");
  } catch (err) { showError(err); }
}

// -- organizations ---------------------------------------------------------

async function renderOrganizations(orgId) {
  if (orgId) return renderOrganizationDetail(orgId);

  showLoading();
  const data = await api("/api/v1/organizations");
  clear(view);

  view.appendChild(el("div", { class: "toolbar" }, [
    el("button", { type: "button", onclick: renderOrganizationAddForm }, ["Add organization"]),
  ]));

  const items = data.Items || [];
  if (items.length === 0) {
    view.appendChild(el("p", { class: "empty", text: "No organizations found." }));
    return;
  }
  const list = el("ul");
  for (const o of items) {
    list.appendChild(el("li", {}, [el("button", { type: "button", onclick: () => navigate("organizations", o.ID) }, [o.Name])]));
  }
  view.appendChild(list);
}

function renderOrganizationAddForm() {
  clear(view);
  const form = el("form", { class: "card", onsubmit: onSubmitOrgAdd }, [
    el("h2", { text: "Add organization" }),
    field("name", "Name", "text", true),
    field("notes", "Notes", "text", false),
    el("button", { type: "submit", class: "btn-primary" }, ["Create"]),
    el("button", { type: "button", onclick: () => navigate("organizations") }, ["Cancel"]),
  ]);
  view.appendChild(form);
  form.querySelector("input").focus();
}

async function onSubmitOrgAdd(evt) {
  evt.preventDefault();
  const fd = new FormData(evt.target);
  try {
    const o = await api("/api/v1/organizations", { method: "POST", body: JSON.stringify({ name: fd.get("name"), notes: fd.get("notes") || "" }) });
    announce(`Created ${o.Name}`);
    navigate("organizations", o.ID);
  } catch (err) { showError(err); }
}

async function renderOrganizationDetail(id) {
  showLoading();
  const [org, timeline] = await Promise.all([api(`/api/v1/organizations/${id}`), api(`/api/v1/organizations/${id}/timeline`)]);
  clear(view);
  view.appendChild(el("button", { type: "button", onclick: () => navigate("organizations") }, ["← Back to organizations"]));
  view.appendChild(el("div", { class: "card" }, [
    el("h2", { text: org.Name }),
    el("p", { text: "ID: " + org.ID }),
    el("button", { type: "button", class: "btn-danger", onclick: async () => {
      if (!confirm("Erase this organization? This cannot be undone.")) return;
      await api(`/api/v1/organizations/${id}`, { method: "DELETE" });
      navigate("organizations");
    } }, ["Erase organization"]),
  ]));
  view.appendChild(el("h3", { text: "Timeline" }));
  if (!timeline || timeline.length === 0) {
    view.appendChild(el("p", { class: "empty", text: "No interactions or follow-ups yet." }));
  }
}

// -- follow-ups --------------------------------------------------------------

async function renderFollowUps() {
  clear(view);
  view.appendChild(el("p", { class: "empty", text: "Open a contact or organization to see and manage its follow-ups." }));
  view.appendChild(el("p", {}, ["Go to ", el("button", { type: "button", onclick: () => navigate("contacts") }, ["Contacts"]), " and select one."]));
}

// -- import ------------------------------------------------------------------

async function renderImport() {
  clear(view);
  const form = el("form", { class: "card", onsubmit: onSubmitImportPlan }, [
    el("h2", { text: "Import contacts" }),
    field("path", "File path (on this machine)", "text", true),
    el("div", { class: "field" }, [
      el("label", { for: "field-kind", text: "Format" }),
      el("select", { id: "field-kind", name: "kind" }, [
        el("option", { value: "vcard", text: "vCard" }),
        el("option", { value: "csv", text: "CSV" }),
      ]),
    ]),
    el("button", { type: "submit", class: "btn-primary" }, ["Preview (dry run)"]),
  ]);
  view.appendChild(form);

  const runsHeading = el("h3", { text: "Past imports" });
  view.appendChild(runsHeading);
  try {
    const runs = await api("/api/v1/import/runs");
    if (!runs || runs.length === 0) {
      view.appendChild(el("p", { class: "empty", text: "No imports yet." }));
    } else {
      const list = el("ul");
      for (const run of runs) list.appendChild(el("li", { text: JSON.stringify(run) }));
      view.appendChild(list);
    }
  } catch (err) {
    view.appendChild(el("p", { class: "error", text: "Could not load import history: " + err.message }));
  }
}

async function onSubmitImportPlan(evt) {
  evt.preventDefault();
  const fd = new FormData(evt.target);
  const req = { path: fd.get("path"), kind: fd.get("kind") };
  try {
    const plan = await api("/api/v1/import/plan", { method: "POST", body: JSON.stringify(req) });
    renderImportPlan(req, plan);
  } catch (err) { showError(err); }
}

function renderImportPlan(req, plan) {
  clear(view);
  view.appendChild(el("button", { type: "button", onclick: renderImport }, ["← Back"]));
  view.appendChild(el("h2", { text: "Import preview" }));
  view.appendChild(el("p", { text: `${(plan.Rows || []).length} rows parsed, ${(plan.Issues || []).length} validation issues, ${(plan.Duplicates || []).length} duplicate candidates.` }));

  if ((plan.Issues || []).length) {
    view.appendChild(el("h3", { text: "Validation issues (excluded)" }));
    const list = el("ul");
    for (const iss of plan.Issues) list.appendChild(el("li", { text: `Row ${iss.RowNumber} (${iss.Field}): ${iss.Message}` }));
    view.appendChild(list);
  }

  const form = el("form", { class: "card", onsubmit: (e) => onSubmitImportApply(e, req, plan) });
  form.appendChild(el("h3", { text: "Review duplicates and apply" }));
  const dupsByRow = {};
  for (const d of plan.Duplicates || []) (dupsByRow[d.RowNumber] = dupsByRow[d.RowNumber] || []).push(d);

  for (const [rowNumber, dups] of Object.entries(dupsByRow)) {
    const row = el("div", { class: "field" });
    row.appendChild(el("label", { for: "resolve-" + rowNumber, text: `Row ${rowNumber}: possible match with ${dups.map((d) => d.ExistingName).join(", ")}` }));
    const select = el("select", { id: "resolve-" + rowNumber, name: "resolve-" + rowNumber });
    select.appendChild(el("option", { value: "create", text: "Create as new contact" }));
    select.appendChild(el("option", { value: "skip", text: "Skip this row" }));
    for (const d of dups) select.appendChild(el("option", { value: "merge:" + d.ExistingPersonID, text: "Merge into " + d.ExistingName }));
    row.appendChild(select);
    form.appendChild(row);
  }
  form.appendChild(el("button", { type: "submit", class: "btn-primary" }, ["Apply import"]));
  view.appendChild(form);
}

async function onSubmitImportApply(evt, req, plan) {
  evt.preventDefault();
  const fd = new FormData(evt.target);
  const resolutions = [];
  for (const [key, value] of fd.entries()) {
    if (!key.startsWith("resolve-")) continue;
    const rowNumber = parseInt(key.slice("resolve-".length), 10);
    if (value === "create") resolutions.push({ RowNumber: rowNumber, Resolution: "create" });
    else if (value === "skip") resolutions.push({ RowNumber: rowNumber, Resolution: "skip" });
    else if (value.startsWith("merge:")) resolutions.push({ RowNumber: rowNumber, Resolution: "merge", MergePersonID: value.slice(6) });
  }
  try {
    const result = await api("/api/v1/import/apply", { method: "POST", body: JSON.stringify(Object.assign({}, req, { resolutions })) });
    clear(view);
    view.appendChild(el("h2", { text: "Import complete" }));
    view.appendChild(el("pre", { text: JSON.stringify(result.result, null, 2) }));
    view.appendChild(el("button", { type: "button", onclick: () => navigate("contacts") }, ["Go to contacts"]));
    announce("Import applied");
  } catch (err) { showError(err); }
}

renderRoute();
