(function () {
  const app = document.getElementById("app");
  let snapshot = null;
  let decisions = null;
  let decisionGraph = null;
  let decisionGraphError = "";
  let budget = undefined;
  let loadError = "";
  let agentFilter = "all";
  let sortKey = "started";
  let sortDir = -1;

  function h(tag, props, kids) {
    const node = document.createElement(tag);
    if (props) {
      Object.keys(props).forEach(function (key) {
        const value = props[key];
        if (value == null || value === false) return;
        if (key === "class") node.className = value;
        else if (key.slice(0, 2) === "on" && typeof value === "function") node.addEventListener(key.slice(2), value);
        else node.setAttribute(key, value);
      });
    }
    (kids || []).forEach(function (kid) {
      if (kid == null || kid === false) return;
      node.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    });
    return node;
  }

  function route() {
    const hash = location.hash || "#/overview";
    if (hash.indexOf("#/sessions/") === 0) {
      return { name: "detail", id: decodeURIComponent(hash.slice("#/sessions/".length)) };
    }
    if (hash === "#/sessions") return { name: "sessions" };
    if (hash === "#/decisions" || hash.indexOf("#/decisions/") === 0) {
      const parts = hash.slice("#/decisions".length).split("/").filter(Boolean).map(decodeURIComponent);
      return { name: "decisions", chapter: parts[0] || "", node: parts[1] || "" };
    }
    return { name: "overview" };
  }

  function knownAmount(amount) {
    return !!(amount && amount.status === "known" && amount.value != null && amount.value !== "");
  }

  function figure(amount, opts) {
    opts = opts || {};
    if (!knownAmount(amount)) {
      return h("div", { class: "figure" }, [
        h("p", { class: "figure-value unknown" }, ["unknown"]),
        h("p", { class: "reason" }, [(amount && amount.reason) || "unknown"]),
      ]);
    }
    const zero = opts.money && amount.value === "0";
    const kids = [
      h("p", { class: zero ? "figure-value known-zero" : "figure-value" }, [
        amount.value + (opts.suffix || ""),
      ]),
    ];
    if (amount.detail) kids.push(h("p", { class: "detail" }, [amount.detail]));
    if (zero) kids.push(h("p", { class: "detail" }, ["Known zero: the price table says this is free. A missing price stays unknown."]));
    return h("div", { class: "figure" }, kids);
  }

  function inlineAmount(amount, opts) {
    opts = opts || {};
    if (!knownAmount(amount)) {
      return h("div", { class: "amount" }, [
        h("div", { class: "unknown" }, ["unknown"]),
        amount && amount.reason ? h("div", { class: "reason" }, [amount.reason]) : null,
      ]);
    }
    const text = amount.value + (opts.suffix || "");
    return h("div", { class: "amount" }, [
      h("div", { class: opts.money && amount.value === "0" ? "known-zero" : "" }, [text]),
      amount.detail && opts.detail ? h("div", { class: "reason" }, [amount.detail]) : null,
    ]);
  }

  function stat(label, body) {
    return h("div", { class: "stat" }, [h("span", { class: "label" }, [label]), body]);
  }

  function heroCost(cost) {
    if (!cost || cost.status !== "known" || cost.usd == null || cost.usd === "") {
      return {
        value: h("p", { class: "hero-value unknown" }, ["unknown"]),
        note: (cost && cost.reason) || "unknown",
      };
    }
    let note = (cost.accepted_outcomes || 0) + " merged";
    if (cost.accepted_cost_usd) note += " · accepted cost " + cost.accepted_cost_usd + " USD";
    if (cost.note) note += " · " + cost.note;
    const zero = cost.usd === "0";
    return {
      value: h("p", { class: zero ? "hero-value known-zero" : "hero-value" }, [cost.usd]),
      note: note + (zero ? " · known zero, not a missing price" : " USD"),
    };
  }

  const LINK_METHODS = ["exact", "hook", "trailer", "heuristic"];

  function linkName(method) {
    for (let i = 0; i < LINK_METHODS.length; i++) {
      if (LINK_METHODS[i] === method) return method;
    }
    return "none";
  }

  function linkPhrase(totals) {
    if (!totals) return "unknown";
    if (!totals.linked) return "none";
    const parts = [];
    if (totals.linked_exact) parts.push(totals.linked_exact + " exact");
    if (totals.linked_hook) parts.push(totals.linked_hook + " hook");
    if (totals.linked_trailer) parts.push(totals.linked_trailer + " trailer");
    if (totals.linked_heuristic) parts.push(totals.linked_heuristic + " heuristic");
    return parts.join(", ") || String(totals.linked);
  }

  function header(active) {
    return h("header", { class: "top" }, [
      h("div", { class: "mark" }, [
        h("div", { class: "mark-badge" }, ["SDF"]),
        h("div", null, [
          h("h1", null, ["SDF"]),
          h("p", null, ["Cost per accepted outcome"]),
        ]),
      ]),
      h("p", { class: "quiet" }, ["Local read-only · localhost"]),
      h("nav", { class: "nav" }, [
        h("a", { href: "#/overview", "aria-current": active === "overview" ? "page" : null }, ["Overview"]),
        h("a", { href: "#/sessions", "aria-current": active === "sessions" || active === "detail" ? "page" : null }, [
          "Sessions" + (snapshot && snapshot.sessions ? " (" + snapshot.sessions.length + ")" : ""),
        ]),
        dgChapters().length || active === "decisions" ? h("a", { href: "#/decisions", "aria-current": active === "decisions" ? "page" : null }, ["Decisions"]) : null,
        h("button", { type: "button", class: "refresh", onclick: function () { load(true); } }, ["Refresh"]),
      ]),
    ]);
  }

  function footer() {
    if (!snapshot) return null;
    const bits = [
      "Figures are the sdf summary report for this database.",
      "This page only requests this server.",
    ];
    if (snapshot.price_table) bits.push("Price table: " + snapshot.price_table + ".");
    if (snapshot.price_source) bits.push(snapshot.price_source + ".");
    if (snapshot.price_date) bits.push("Retrieved " + snapshot.price_date + ".");
    if (snapshot.github) bits.push("Merge status is queried from GitHub on refresh.");
    else bits.push("Merge status was not requested. Restart with sdf serve --github to ask.");
    return h("footer", null, [h("p", null, [bits.join(" ")])]);
  }

  function emptyState() {
    return h("section", { class: "empty" }, [
      h("h2", null, ["No telemetry sessions in the store."]),
      h("p", null, ["This database has no sessions yet. Point an agent at the receiver, then refresh."]),
      h("pre", null, ["sdf init claude-code\nsdf init codex\nsdf init opencode\nsdf init pi"]),
      h("p", { class: "detail" }, ["init prints the exporter settings. --write saves them. Leave sdf serve running while the agent exports."]),
    ]);
  }

  function isEmpty() {
    return snapshot && snapshot.sessions.length === 0 && (!snapshot.unattributed || snapshot.unattributed.length === 0);
  }

  function overview() {
    const kids = [
      budgetSection(),
    ];
    if (isEmpty()) {
      kids.push(emptyState());
    } else {
      const cost = heroCost(snapshot.cost_per_accepted_outcome);
      const totals = snapshot.totals || {};
      kids.push(h("section", { class: "hero" }, [
        h("div", null, [
          h("p", { class: "kicker" }, ["Cost per accepted outcome"]),
          cost.value,
          h("p", { class: "hero-note" }, [cost.note]),
          h("p", { class: "detail" }, ["Computed cost of sessions whose pull request merged, divided by how many. The same rule as sdf summary."]),
        ]),
        h("div", { class: "stats" }, [
          stat("Sessions", h("span", { class: "value" }, [String(totals.sessions || 0)])),
          stat("Linked", h("span", { class: "value" }, [linkPhrase(totals)])),
          stat("Merged sessions", h("span", { class: "value" }, [String(totals.merged || 0)])),
          stat("Outcome unknown", h("span", { class: "value" }, [String(totals.accepted_unknown || 0)])),
          stat("Computed USD", inlineAmount(totals.computed_cost_usd, { money: true })),
          stat("Agent-reported USD", inlineAmount(totals.agent_reported_cost_usd, { money: true })),
          stat("Input tokens", inlineAmount(totals.input_tokens)),
          stat("Output tokens", inlineAmount(totals.output_tokens)),
        ]),
      ]));
      kids.push(h("section", { class: "section" }, [
        h("h2", null, ["By agent"]),
        agentTable(snapshot.agents || []),
      ]));
    }
    if (snapshot.unattributed && snapshot.unattributed.length) kids.push(unattributedBlock(snapshot.unattributed));
    kids.push(decisionsSection());
    return h("div", null, kids);
  }

  function budgetSection() {
    if (budget == null) return null;
    const status = budget.status || "unknown";
    const note = [];
    if (status === "unknown") {
      note.push(budget.reason || "unknown");
    } else {
      note.push("total " + (budget.total_usd || "") + " USD");
      note.push("ceiling " + (budget.ceiling_usd || "") + " USD");
    }
    if (budget.window) note.push("window " + budget.window);
    if (budget.day) note.push(budget.day);
    return h("section", { class: "section budget" }, [
      h("h2", null, ["Budget"]),
      h("p", { class: status === "unknown" ? "figure-value unknown" : "figure-value budget-status" }, [status]),
      h("p", { class: "detail" }, [note.join(" · ")]),
    ]);
  }

  function decisionsSection() {
    const records = decisions && decisions.decisions ? decisions.decisions : [];
    const edges = decisions && decisions.edges ? decisions.edges : [];
    if (!records.length && !edges.length) return null;
    const kids = [h("h2", null, ["Decisions"])];
    const activeIDs = {};
    records.forEach(function (record) {
      if (record.effective_status === "active") activeIDs[record.id] = true;
    });
    if (!records.length) {
      kids.push(h("p", null, ["no decision"]));
    } else {
      records.forEach(function (record) {
        const by = Array.isArray(record.superseded_by) ? record.superseded_by : [];
        const retraction = record.effective_status === "retracted" && record.retraction ? record.retraction : null;
        const superseded = by.length > 0 && (retraction || record.effective_status === "superseded");
        const line = [
          h("span", { class: "status" }, [record.status || "unknown"]),
        ];
        if (record.decided_by) {
          line.push(h("span", { class: "status" }, [record.decided_by]));
        }
        if (retraction) {
          line.push(h("span", { class: "retracted" }, ["retracted"]));
        }
        if (superseded) {
          line.push(h("span", { class: "superseded-by" }, ["superseded by " + by.join(", ")]));
        }
        const open = Array.isArray(record.open_blockers) ? record.open_blockers : [];
        const conflicts = Array.isArray(record.conflicts_with) ? record.conflicts_with : [];
        // Every blocker, then the open ones, as sdf decision list prints it.
        const blockers = Array.isArray(record.blocked_by) ? record.blocked_by : [];
        const blockerText = blockers.length
          ? "blocked by " + blockers.join(", ") + (open.length ? " (open: " + open.join(", ") + ")" : " (settled)")
          : "";
        if (record.blocked && blockers.length) {
          line.push(h("span", { class: "relation-flag blocked" }, [blockerText]));
        }
        const activeConflicts = conflicts.filter(function (id) { return activeIDs[id]; });
        if (record.conflicted && activeConflicts.length) {
          line.push(h("span", { class: "relation-flag conflicted" }, ["conflicts with " + activeConflicts.join(", ")]));
        }
        if (record.challenged) {
          line.push(h("span", { class: "relation-flag challenged" }, ["challenged"]));
        }
        line.push(document.createTextNode(record.statement || ""));
        let cls = "decision";
        if (retraction) cls = "decision retracted-record";
        else if (superseded) cls = "decision superseded";
        kids.push(h("p", { class: cls }, line));
        const extra = [];
        if (record.alternatives) extra.push("alternatives " + record.alternatives);
        if (record.reason) extra.push("reason " + record.reason);
        if (blockers.length && !record.blocked) extra.push(blockerText);
        if (conflicts.length && !record.conflicted) extra.push("conflicts with " + conflicts.join(", ") + " (resolved)");
        const refined = Array.isArray(record.refined_by) ? record.refined_by : [];
        if (refined.length) extra.push("refined by " + refined.join(", "));
        if (retraction) {
          const parts = ["retraction " + (retraction.retracted_at || "")];
          if (retraction.retracted_by) parts.push("by " + retraction.retracted_by);
          if (retraction.reason) parts.push(retraction.reason);
          extra.push(parts.join(" "));
        }
        if (extra.length) {
          kids.push(h("p", { class: "detail" }, [extra.join(" · ")]));
        }
      });
    }
    edges.forEach(function (edge) {
      kids.push(h("p", { class: "detail" }, [
        endpointText(edge.from) + " " + (edge.relation || "") + " " + endpointText(edge.to),
      ]));
    });
    return h("section", { class: "section" }, kids);
  }

  function endpointText(endpoint) {
    if (!endpoint) return "";
    const kind = endpoint.kind || "";
    const ref = endpoint.ref || "";
    if (kind === "commit" && ref.length > 12) return kind + " " + ref.slice(0, 12);
    return (kind + " " + ref).trim();
  }

  function agentTable(agents) {
    if (!agents.length) return h("p", { class: "detail" }, ["No sessions."]);
    return h("div", { class: "table-scroll" }, [
      h("table", null, [
        h("thead", null, [h("tr", null, [
          h("th", { scope: "col" }, ["Agent"]),
          h("th", { scope: "col" }, ["Sessions"]),
          h("th", { scope: "col" }, ["Linked"]),
          h("th", { scope: "col" }, ["Merged"]),
          h("th", { scope: "col" }, ["Computed USD"]),
          h("th", { scope: "col" }, ["Agent-reported USD"]),
          h("th", { scope: "col" }, ["Cost per accepted"]),
        ])]),
        h("tbody", null, agents.map(function (agent) {
          const per = agent.cost_per_accepted_outcome || {};
          const perAmount = per.status === "known" && per.usd
            ? { status: "known", value: per.usd, detail: per.note || "" }
            : { status: "unknown", reason: per.reason || "unknown" };
          return h("tr", null, [
            h("td", null, [h("a", {
              href: "#/sessions",
              onclick: function () {
                agentFilter = agent.tool || "unknown";
                if ((location.hash || "#/overview") === "#/sessions") render();
              },
            }, [agent.tool || "unknown"])]),
            h("td", null, [String(agent.sessions)]),
            h("td", null, [linkPhrase(agent)]),
            h("td", null, [String(agent.merged)]),
            h("td", null, [inlineAmount(agent.computed_cost_usd, { money: true })]),
            h("td", null, [inlineAmount(agent.agent_reported_cost_usd, { money: true })]),
            h("td", null, [inlineAmount(perAmount, { money: true })]),
          ]);
        })),
      ]),
    ]);
  }

  function unattributedBlock(groups) {
    return h("section", { class: "section" }, [
      h("h2", null, ["Unattributed"]),
      h("div", { class: "table-scroll" }, [
        h("table", null, [
          h("thead", null, [h("tr", null, [
            h("th", { scope: "col" }, ["Agent"]),
            h("th", { scope: "col" }, ["Traces"]),
            h("th", { scope: "col" }, ["Logs"]),
            h("th", { scope: "col" }, ["Metrics"]),
            h("th", { scope: "col" }, ["Reason"]),
          ])]),
          h("tbody", null, groups.map(function (group) {
            return h("tr", null, [
              h("td", null, [group.tool || "unknown"]),
              h("td", null, [String(group.traces)]),
              h("td", null, [String(group.logs)]),
              h("td", null, [String(group.metrics)]),
              h("td", null, [group.reason || "unknown"]),
            ]);
          })),
        ]),
      ]),
    ]);
  }

  function sessions() {
    if (isEmpty()) {
      return h("div", null, [budgetSection(), emptyState(), decisionsSection()]);
    }
    const tools = [];
    snapshot.sessions.forEach(function (view) {
      const tool = (view.summary && view.summary.tool) || "unknown";
      if (tools.indexOf(tool) === -1) tools.push(tool);
    });
    tools.sort();
    const filters = h("div", { class: "filters", role: "group", "aria-label": "Filter by agent" }, [
      filterButton("all", "All"),
    ].concat(tools.map(function (tool) { return filterButton(tool, tool); })));
    const rows = sorted(filtered());
    return h("div", null, [
      budgetSection(),
      h("p", { class: "kicker" }, ["Sessions"]),
      filters,
      h("div", { class: "table-scroll" }, [
        h("table", { class: "wide" }, [
          h("thead", null, [h("tr", null, columns().map(function (col) {
            const active = sortKey === col.key;
            const aria = active ? (sortDir < 0 ? "descending" : "ascending") : "none";
            const arrow = active ? (sortDir < 0 ? " ↓" : " ↑") : "";
            return h("th", { scope: "col" }, [
              h("button", {
                type: "button",
                "aria-sort": aria,
                onclick: function () { toggleSort(col.key); },
              }, [col.label + arrow]),
            ]);
          }))]),
          h("tbody", null, rows.map(sessionRow)),
        ]),
      ]),
      decisionsSection(),
    ]);
  }

  function columns() {
    return [
      { key: "session", label: "Session" },
      { key: "agent", label: "Agent" },
      { key: "started", label: "Started" },
      { key: "input", label: "Input" },
      { key: "output", label: "Output" },
      { key: "computed", label: "Computed USD" },
      { key: "agent_cost", label: "Agent-reported" },
      { key: "outcome", label: "Outcome" },
      { key: "duration", label: "Duration" },
      { key: "link", label: "Link" },
      { key: "model", label: "Model" },
    ];
  }

  function filterButton(id, label) {
    return h("button", {
      type: "button",
      "aria-pressed": agentFilter === id ? "true" : "false",
      onclick: function () { agentFilter = id; render(); },
    }, [label]);
  }

  function filtered() {
    return snapshot.sessions.filter(function (view) {
      if (agentFilter === "all") return true;
      return ((view.summary && view.summary.tool) || "unknown") === agentFilter;
    });
  }

  function toggleSort(key) {
    if (sortKey === key) sortDir = -sortDir;
    else {
      sortKey = key;
      sortDir = key === "started" ? -1 : 1;
    }
    render();
  }

  function sortValue(view) {
    const summary = view.summary || {};
    switch (sortKey) {
      case "session":
        return summary.session_id || "";
      case "started":
        return view.started_unix_nano ? BigInt(view.started_unix_nano) : null;
      case "agent":
        return summary.tool || "";
      case "duration":
        return knownAmount(view.duration) ? Number(view.duration.value) : null;
      case "model":
        return (summary.models && summary.models[0]) || null;
      case "input":
        return knownAmount(view.input_tokens) ? Number(view.input_tokens.value) : null;
      case "output":
        return knownAmount(view.output_tokens) ? Number(view.output_tokens.value) : null;
      case "computed":
        return knownAmount(view.computed_cost_usd) ? Number(view.computed_cost_usd.value) : null;
      case "agent_cost":
        return knownAmount(view.agent_reported_cost_usd) ? Number(view.agent_reported_cost_usd.value) : null;
      case "outcome":
        return outcomeRank(view);
      case "link":
        return view.link_method || "none";
      default:
        return "";
    }
  }

  function outcomeRank(view) {
    const outcome = view.summary && view.summary.outcome;
    if (!outcome || outcome.status !== "linked") return null;
    if (outcome.accepted == null) return 1;
    return outcome.accepted ? 3 : 2;
  }

  function sorted(rows) {
    return rows.slice().sort(function (a, b) {
      const av = sortValue(a);
      const bv = sortValue(b);
      if (av == null && bv == null) return 0;
      if (av == null) return 1;
      if (bv == null) return -1;
      let cmp = 0;
      if (typeof av === "bigint" || typeof bv === "bigint") {
        const left = typeof av === "bigint" ? av : BigInt(av);
        const right = typeof bv === "bigint" ? bv : BigInt(bv);
        cmp = left < right ? -1 : left > right ? 1 : 0;
      } else if (typeof av === "number" && typeof bv === "number") {
        cmp = av < bv ? -1 : av > bv ? 1 : 0;
      } else {
        cmp = String(av) < String(bv) ? -1 : String(av) > String(bv) ? 1 : 0;
      }
      return cmp * sortDir;
    });
  }

  function sessionRow(view) {
    const summary = view.summary || {};
    const id = summary.session_id || "";
    return h("tr", null, [
      h("td", null, [h("a", { class: "id", href: "#/sessions/" + encodeURIComponent(id) }, [id || "unknown"])]),
      h("td", null, [summary.tool || "unknown"]),
      h("td", null, [inlineAmount(view.started)]),
      h("td", null, [inlineAmount(view.input_tokens)]),
      h("td", null, [inlineAmount(view.output_tokens)]),
      h("td", null, [inlineAmount(view.computed_cost_usd, { money: true })]),
      h("td", null, [inlineAmount(view.agent_reported_cost_usd, { money: true })]),
      h("td", null, [outcomeCell(view)]),
      h("td", null, [inlineAmount(view.duration, { suffix: knownAmount(view.duration) ? " ms" : "", detail: true })]),
      h("td", null, [linkCell(view)]),
      h("td", null, [modelsCell(summary)]),
    ]);
  }

  function modelsCell(summary) {
    const models = summary.models || [];
    if (!models.length) {
      return inlineAmount({ status: "unknown", reason: "no model id on the session" });
    }
    return document.createTextNode(models.join(", "));
  }

  function outcomeCell(view) {
    const outcome = view.summary && view.summary.outcome;
    if (!outcome || outcome.status !== "linked") {
      return h("div", { class: "amount" }, [
        h("div", null, ["unlinked"]),
        h("div", { class: "reason" }, [view.link_reason || "unlinked"]),
      ]);
    }
    const bits = [];
    (outcome.commits || []).forEach(function (commit) {
      bits.push(commit.subject ? commit.sha.slice(0, 12) + " " + commit.subject : commit.sha);
    });
    if (outcome.pull_request) {
      bits.push("PR #" + outcome.pull_request.number + " " + pullState(outcome.pull_request));
    }
    const head = bits.join(" · ");
    if (outcome.accepted == null) {
      return h("div", { class: "amount" }, [
        head ? h("div", null, [head]) : null,
        h("div", { class: "unknown" }, ["unknown"]),
        h("div", { class: "reason" }, [outcome.accepted_reason || "merge status was not established"]),
      ]);
    }
    if (outcome.accepted === false) {
      return h("div", { class: "amount" }, [
        h("div", null, [head || "not merged"]),
        outcome.accepted_reason ? h("div", { class: "reason" }, [outcome.accepted_reason]) : null,
      ]);
    }
    return h("div", null, [head || "merged"]);
  }

  function pullState(pr) {
    if (pr.merged) return "merged";
    if (pr.state === "closed") return "closed without merging";
    return "not merged";
  }

  function linkCell(view) {
    const method = linkName(view.link_method);
    return h("div", { class: "amount" }, [
      h("div", null, [method]),
      view.link_reason ? h("div", { class: "reason" }, [view.link_reason]) : null,
    ]);
  }

  const SPAN_SIBLING_LIMIT = 40;
  const SPAN_OPEN_DEPTH = 2;
  const SPAN_ERROR_BANNER_LIMIT = 8;
  const SPAN_DEPTH_LIMIT = 32;
  const spanErrorFlag = new WeakMap();

  function annotateSpanTree(roots) {
    const errors = [];
    const ordered = [];
    const st = [];
    const list = roots || [];
    for (let i = list.length - 1; i >= 0; i--) st.push(list[i]);
    while (st.length) {
      const node = st.pop();
      if (!node) continue;
      ordered.push(node);
      if (node.status_code === "ERROR") {
        errors.push({ name: node.name || "(unnamed)", message: node.status_message || "" });
      }
      const kids = node.children || [];
      for (let i = kids.length - 1; i >= 0; i--) st.push(kids[i]);
    }
    for (let i = ordered.length - 1; i >= 0; i--) {
      const node = ordered[i];
      let has = node.status_code === "ERROR";
      const kids = node.children || [];
      for (let j = 0; j < kids.length && !has; j++) {
        if (kids[j] && spanErrorFlag.get(kids[j])) has = true;
      }
      spanErrorFlag.set(node, has);
    }
    return errors;
  }

  function formatSpanNum(value) {
    const n = Number(value);
    if (!isFinite(n)) return String(value);
    const rounded = Math.round(n);
    if (Math.abs(n - rounded) < 1e-9) return String(rounded);
    return String(n);
  }

  function spanErrorBanner(errors) {
    if (!errors || !errors.length) return null;
    const heading = errors.length === 1
      ? "This session has a span with status=ERROR."
      : "This session has " + errors.length + " spans with status=ERROR.";
    const kids = [h("p", null, [heading])];
    const shown = errors.slice(0, SPAN_ERROR_BANNER_LIMIT);
    shown.forEach(function (err) {
      const text = err.message ? err.name + ": " + err.message : err.name + ": status=ERROR";
      kids.push(h("p", { class: "span-error-message" }, [text]));
    });
    if (errors.length > shown.length) {
      kids.push(h("p", { class: "detail" }, [
        "And " + (errors.length - shown.length) + " more in the span tree below.",
      ]));
    }
    return h("div", { class: "callout span-errors", role: "status" }, kids);
  }

  function spanLine(node) {
    const name = (node && node.name) || "(unnamed)";
    const kids = [h("span", { class: "span-name" }, [name])];
    const tool = node && node.tool_name;
    if (tool && tool !== name && name.slice(-(String(tool).length + 1)) !== " " + tool) {
      kids.push(document.createTextNode(" " + tool));
    }
    if (node && node.call_id) {
      kids.push(document.createTextNode(" call_id=" + node.call_id));
    }
    if (node && node.duration_ms != null && node.duration_ms !== "") {
      kids.push(h("span", { class: "span-duration" }, [" " + formatSpanNum(node.duration_ms) + "ms"]));
    }
    if (node && node.status_code === "ERROR") {
      let status = "status=ERROR";
      if (node.status_message) status += " (" + node.status_message + ")";
      if (node.error_type && String(node.status_message || "").indexOf(node.error_type) < 0) status += " error_type=" + node.error_type;
      kids.push(document.createTextNode(" "));
      kids.push(h("span", { class: "span-status" }, [status]));
    }
    if (node && node.success != null && node.success !== "") {
      let success = "success=" + node.success;
      if (node.success_source) success += " (" + node.success_source + ")";
      kids.push(document.createTextNode(" " + success));
    }
    if (node && node.wait_ms != null && node.wait_ms !== "") {
      let wait = "wait=" + formatSpanNum(node.wait_ms) + "ms";
      if (node.wait_source) wait += " (" + node.wait_source + ")";
      kids.push(document.createTextNode(" " + wait));
    }
    if (node && node.execution_ms != null && node.execution_ms !== "") {
      let exec = "execution=" + formatSpanNum(node.execution_ms) + "ms";
      if (node.execution_source) exec += " (" + node.execution_source + ")";
      kids.push(document.createTextNode(" " + exec));
    }
    return h("span", { class: "span-line" }, kids);
  }

  function buildSpanList(nodes, depth) {
    const ul = h("ul", { class: "span-tree" }, []);
    const jobs = [{ nodes: nodes || [], depth: depth, parent: ul }];
    while (jobs.length) {
      const job = jobs.pop();
      appendSpanChildren(job.nodes, job.depth, job.parent, jobs);
    }
    return ul;
  }

  function appendSpanChildren(nodes, depth, ul, jobs) {
    const list = nodes || [];
    const visible = list.slice(0, SPAN_SIBLING_LIMIT);
    const rest = list.slice(SPAN_SIBLING_LIMIT);
    for (let i = 0; i < visible.length; i++) {
      ul.append(makeSpanItem(visible[i], depth, jobs));
    }
    if (rest.length) {
      ul.append(lazyMoreItem(rest, depth));
    }
  }

  function lazyMoreItem(rest, depth) {
    const details = h("details", null, [
      h("summary", null, ["Show " + rest.length + " more spans"]),
    ]);
    function onToggle() {
      if (!details.open) return;
      details.removeEventListener("toggle", onToggle);
      details.append(buildSpanList(rest, depth));
    }
    details.addEventListener("toggle", onToggle);
    return h("li", { class: "span-more" }, [details]);
  }

  function makeSpanItem(node, depth, jobs) {
    const kids = (node && node.children) || [];
    const className = node && node.status_code === "ERROR" ? "span-node span-error" : "span-node";
    const line = spanLine(node);
    if (!kids.length) {
      return h("li", { class: className + " span-leaf" }, [line]);
    }
    if (depth >= SPAN_DEPTH_LIMIT) {
      return h("li", { class: className + " span-leaf" }, [
        line,
        h("p", { class: "detail span-truncated" }, [
          "Deeper spans not shown (depth limit " + SPAN_DEPTH_LIMIT + ").",
        ]),
      ]);
    }
    const props = {};
    if (depth < SPAN_OPEN_DEPTH || spanErrorFlag.get(node)) props.open = true;
    const childList = h("ul", { class: "span-tree" }, []);
    const details = h("details", props, [
      h("summary", null, [line]),
      childList,
    ]);
    jobs.push({ nodes: kids, depth: depth + 1, parent: childList });
    return h("li", { class: className }, [details]);
  }

  function spanSection(summary) {
    const spans = (summary && summary.spans) || [];
    const kids = [h("h2", null, ["Spans"])];
    if (!spans.length) {
      kids.push(h("p", { class: "detail" }, ["none in the default view"]));
    } else {
      kids.push(buildSpanList(spans, 0));
    }
    if (summary && summary.spans_hidden) {
      const noun = summary.spans_hidden === 1 ? "span" : "spans";
      kids.push(h("p", { class: "detail" }, [
        summary.spans_hidden + " internal " + noun + " hidden, same as sdf summary without --all-spans",
      ]));
    }
    return h("section", { class: "section" }, kids);
  }

  function detail(id) {
    if (isEmpty()) {
      return h("div", null, [budgetSection(), emptyState(), decisionsSection()]);
    }
    const view = (snapshot.sessions || []).filter(function (item) {
      return item.summary && item.summary.session_id === id;
    })[0];
    if (!view) {
      return h("div", null, [
        budgetSection(),
        h("section", { class: "empty" }, [
          h("p", { class: "crumb" }, [h("a", { href: "#/sessions" }, ["All sessions"])]),
          h("h2", null, ["No session with that id."]),
          h("p", null, [id || ""]),
        ]),
        decisionsSection(),
      ]);
    }
    const summary = view.summary;
    const spanErrors = annotateSpanTree(summary.spans || []);
    return h("div", null, [
      budgetSection(),
      h("p", { class: "crumb" }, [h("a", { href: "#/sessions" }, ["All sessions"])]),
      h("p", { class: "kicker" }, [summary.tool || "unknown"]),
      h("h2", { class: "id" }, [summary.session_id || ""]),
      h("div", { class: "meta" }, [
        h("div", null, [h("span", { class: "label" }, ["Started"]), inlineAmount(view.started)]),
        h("div", null, [h("span", { class: "label" }, ["Duration"]), inlineAmount(view.duration, { suffix: knownAmount(view.duration) ? " ms" : "", detail: true })]),
        h("div", null, [h("span", { class: "label" }, ["Models"]), modelsCell(summary)]),
        h("div", null, [h("span", { class: "label" }, ["Records"]), document.createTextNode(
          "traces " + summary.traces + " · logs " + summary.logs + " · metrics " + summary.metrics
        )]),
      ]),
      spanErrorBanner(spanErrors),
      h("section", { class: "section cards" }, [
        costCard("Computed cost", view.computed_cost_usd, summary.computed_cost_usd, true),
        costCard("Agent-reported cost", view.agent_reported_cost_usd, null, false),
      ]),
      tokenSection(view),
      toolSection(view),
      spanSection(summary),
      outcomeSection(view),
      missingSection(summary),
      decisionsSection(),
    ]);
  }

  function costCard(title, amount, raw, computed) {
    const kids = [h("h3", null, [title]), figure(amount, { money: true })];
    if (computed && raw && raw.status === "known" && raw.components && raw.components.length) {
      kids.push(h("table", { class: "components" }, [
        h("thead", null, [h("tr", null, ["Bucket", "Tokens", "USD / token", "USD"].map(function (label) {
          return h("th", { scope: "col" }, [label]);
        }))]),
        h("tbody", null, raw.components.map(function (part) {
          return h("tr", null, [
            h("td", null, [part.name || ""]),
            h("td", null, [part.tokens || ""]),
            h("td", null, [part.usd_per_token || ""]),
            h("td", null, [part.usd || ""]),
          ]);
        })),
      ]));
      if (raw.note) kids.push(h("p", { class: "detail" }, [raw.note]));
    }
    if (computed && raw && raw.status !== "known" && raw.model) {
      kids.push(h("p", { class: "detail" }, ["Model " + raw.model]));
    }
    return h("article", { class: "card" }, kids);
  }

  function tokenSection(view) {
    const summary = view.summary || {};
    const tokens = summary.tokens || {};
    const keys = Object.keys(tokens);
    const breakdown = view.model_breakdown || {};
    const note = breakdown.status === "known"
      ? (breakdown.note || "")
      : (breakdown.reason || "unknown");
    const bannerClass = breakdown.status === "known" ? "callout ok" : "callout";
    const rows = keys.map(function (key) {
      return h("tr", null, [
        h("td", { class: "mono" }, [key]),
        h("td", null, [String(tokens[key])]),
      ]);
    });
    return h("section", { class: "section" }, [
      h("h2", null, ["Tokens"]),
      h("p", { class: bannerClass }, [
        breakdown.status === "known" ? "Per model: " + ((breakdown.models || []).join(", ") || "one model") + ". " + note : "Per model: unknown. " + note,
      ]),
      keys.length
        ? h("table", null, [
          h("thead", null, [h("tr", null, [h("th", { scope: "col" }, ["Field"]), h("th", { scope: "col" }, ["Tokens"])])]),
          h("tbody", null, rows),
        ])
        : h("p", { class: "unknown" }, ["unknown"]),
      summary.tokens_source ? h("p", { class: "detail" }, [summary.tokens_source + (summary.tokens_method ? " · " + summary.tokens_method : "")]) : null,
    ]);
  }

  function toolSection(view) {
    const summary = view.summary || {};
    const kids = [h("h2", null, ["Tool calls"])];
    if (view.tool_calls_status !== "reported") {
      kids.push(h("p", { class: "unknown" }, ["unknown"]));
      kids.push(h("p", { class: "reason" }, ["none reported"]));
      return h("section", { class: "section" }, kids);
    }
    kids.push(h("table", null, [
      h("thead", null, [h("tr", null, [h("th", { scope: "col" }, ["Tool"]), h("th", { scope: "col" }, ["Count"])])]),
      h("tbody", null, (view.tool_counts || []).map(function (count) {
        return h("tr", null, [
          h("td", null, [count.name || "(unnamed)"]),
          h("td", null, [count.status === "known" ? count.count : inlineAmount(count)]),
        ]);
      })),
    ]));
    (summary.tool_calls || []).forEach(function (call) {
      const name = call.name || "(unnamed)";
      const bits = [name];
      if (call.call_id) bits.push(call.call_id);
      bits.push(call.success == null || call.success === "" ? "success unknown" : "success " + call.success);
      if (call.duration_ms != null) bits.push(call.duration_ms + " ms");
      if (call.source) bits.push(call.source);
      kids.push(h("p", { class: "detail" }, [bits.join(" · ")]));
    });
    return h("section", { class: "section" }, kids);
  }

  function outcomeSection(view) {
    const outcome = view.summary && view.summary.outcome;
    const kids = [h("h2", null, ["Outcome"])];
    if (!outcome) {
      kids.push(inlineAmount({ status: "unknown", reason: view.link_reason || "outcome was not attached" }));
      return h("section", { class: "section" }, kids);
    }
    const method = linkName(view.link_method);
    kids.push(h("div", { class: "dl" }, [
      row("Link", document.createTextNode(method)),
      row("Why", document.createTextNode(view.link_reason || outcome.reason || "not stated")),
      row("Repository", outcome.repo ? document.createTextNode(outcome.repo) : inlineAmount({ status: "unknown", reason: "no repository path on the link" })),
      row("Accepted", acceptedNode(outcome)),
      row("CI", outcome.ci ? document.createTextNode(outcome.ci) : inlineAmount({ status: "unknown", reason: "CI was not queried" })),
    ]));
    (outcome.commits || []).forEach(function (commit) {
      const text = commit.sha + (commit.subject ? " · " + commit.subject : "");
      kids.push(h("p", { class: "mono" }, [text]));
    });
    if (outcome.pull_request) {
      const pr = outcome.pull_request;
      const label = "PR #" + pr.number + " " + pullState(pr) + (pr.url ? " " + pr.url : "");
      kids.push(h("p", null, [label]));
    }
    return h("section", { class: "section" }, kids);
  }

  function acceptedNode(outcome) {
    if (outcome.accepted == null) {
      return inlineAmount({ status: "unknown", reason: outcome.accepted_reason || "merge status was not established" });
    }
    if (outcome.accepted) {
      return document.createTextNode("merged");
    }
    return document.createTextNode(outcome.accepted_reason || "not merged");
  }

  function row(label, value) {
    return h("div", null, [h("dt", null, [label]), h("dd", null, [value])]);
  }

  function missingSection(summary) {
    const missing = summary.missing || [];
    const kids = [h("h2", null, ["Missing"])];
    if (!missing.length) {
      kids.push(h("p", null, ["none"]));
      return h("section", { class: "section" }, kids);
    }
    missing.forEach(function (item) {
      kids.push(h("p", null, [(item.signal || "signal") + ": " + (item.reason || "unknown")]));
    });
    return h("section", { class: "section" }, kids);
  }

  // ---- Decisions tab -------------------------------------------------------
  // Draws GET /api/decisions/graph. The server places every node in a lane
  // and a row; this code only turns lanes and rows into coordinates, so the
  // page never re-derives a chapter, an ending, or a count.
  // The SVG namespace is an identifier the DOM compares, never a URL the page
  // requests. It is assembled so the asset holds no literal URL: the page
  // tests reject any, and the CSP keeps every request on this server anyway.
  const SVGNS = ["http:", "", "www.w3.org", "2000", "svg"].join("/");
  const DG = {
    laneX: { left: 130, spine: 390, right: 650, none: 390 },
    laneCap: { left: 210, spine: 236, right: 220, none: 200 },
    width: 790,
    rowH: 66,
    top: 34,
    nodeH: 44,
  };
  let dgShow = "all";
  let dgTensions = false;
  let dgNotTaken = true;
  let dgPathOnly = false;
  let dgHelp = false;
  let dgStatus = "";
  let dgKeyOpen = null;

  function s(tag, props, kids) {
    const node = document.createElementNS(SVGNS, tag);
    if (props) {
      Object.keys(props).forEach(function (key) {
        const value = props[key];
        if (value == null || value === false) return;
        if (key.slice(0, 2) === "on" && typeof value === "function") node.addEventListener(key.slice(2), value);
        else node.setAttribute(key, value);
      });
    }
    (kids || []).forEach(function (kid) {
      if (kid == null || kid === false) return;
      node.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    });
    return node;
  }

  function dgChapters() {
    return decisionGraph && Array.isArray(decisionGraph.chapters) ? decisionGraph.chapters : [];
  }

  function dgRelation(name) {
    const types = decisionGraph && Array.isArray(decisionGraph.relation_types) ? decisionGraph.relation_types : [];
    for (let i = 0; i < types.length; i++) if (types[i].name === name) return types[i];
    return { name: name, style: "generic", label: name };
  }

  function dgHash(chapter, node) {
    let out = "#/decisions/" + encodeURIComponent(chapter);
    if (node) out += "/" + encodeURIComponent(node);
    return out;
  }

  function dgGo(chapter, node) {
    location.hash = dgHash(chapter, node);
  }

  function dgWhen(text) {
    if (!text) return "";
    const date = new Date(text);
    if (isNaN(date.getTime())) return text;
    try {
      return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZoneName: "short" });
    } catch (err) {
      return text;
    }
  }

  function dgSize(node) {
    const title = node.title || "";
    const detail = node.detail || "";
    const pad = node.decided_by ? 46 : 26;
    let w = Math.max(title.length * 7.1, detail.length * 6.1) + pad;
    if (node.kind === "start") w = Math.max(w, 150);
    if (node.role === "locked" || node.role === "locked_ending") w += 30;
    return Math.round(Math.min(Math.max(w, 140), DG.laneCap[node.lane] || 220));
  }

  function dgFit(text, width) {
    const room = Math.max(4, Math.floor(width / 6.1));
    if (!text || text.length <= room) return text || "";
    return text.slice(0, room - 1).replace(/\s+$/, "") + "…";
  }

  function dgVisible(node) {
    if (node.lane === "none") return false;
    if (node.kind === "alternative" && !dgNotTaken) return false;
    if (dgPathOnly && !(node.on_path || node.kind === "start" || node.kind === "ending")) return false;
    return true;
  }

  function dgMuted(node) {
    if (dgShow === "all" || node.kind !== "decision") return false;
    return node.decided_by !== dgShow;
  }

  function dgPos(node) {
    return { x: DG.laneX[node.lane] || DG.laneX.spine, y: DG.top + node.row * DG.rowH + DG.nodeH / 2 };
  }

  function lockIcon(x, y, color) {
    return s("g", { transform: "translate(" + x + "," + y + ")", "aria-hidden": "true" }, [
      s("path", { d: "M-4 -1 v-3 a4 4 0 0 1 8 0 v3", fill: "none", stroke: color, "stroke-width": "1.7" }),
      s("rect", { x: "-6", y: "-1", width: "12", height: "9", rx: "1.5", fill: color }),
    ]);
  }

  function dgGlyph(type, x, y, label) {
    let shape;
    if (type.style === "lock") {
      shape = [s("circle", { cx: x, cy: y, r: 11, class: "dg-glyph-lock" }), lockIcon(x, y - 1.5, "#8a4b08")];
    } else if (type.style === "crack") {
      shape = [
        s("rect", { x: x - 9, y: y - 9, width: 18, height: 18, transform: "rotate(45 " + x + " " + y + ")", class: "dg-glyph-crack" }),
        s("path", { d: "M" + (x - 2) + " " + (y - 7) + " L" + (x + 2.5) + " " + (y - 1) + " L" + (x - 2.5) + " " + (y + 1) + " L" + (x + 2) + " " + (y + 7), class: "dg-crack-line" }),
      ];
    } else if (type.glyph) {
      shape = [
        s("circle", { cx: x, cy: y, r: 10, class: "dg-glyph-ring dg-" + type.style }),
        s("text", { x: x, y: y + 5, "text-anchor": "middle", class: "dg-glyph-text dg-" + type.style }, [type.glyph]),
      ];
    } else {
      return null;
    }
    // The relation name is the glyph's tooltip and a sentence in the focus
    // panel; a text label beside the glyph collided with neighbouring nodes.
    return s("g", { class: "dg-glyph" }, [s("title", null, [label])].concat(shape));
  }

  function dgEdgePath(a, b, sa, sb) {
    const pa = dgPos(a);
    const pb = dgPos(b);
    if (a.row === b.row) {
      const dir = pb.x > pa.x ? 1 : -1;
      return { d: "M" + (pa.x + dir * sa / 2) + "," + pa.y + " L" + (pb.x - dir * (sb / 2 + 3)) + "," + pb.y, mid: [(pa.x + pb.x) / 2, pa.y] };
    }
    if (b.row > a.row) {
      const sy = pa.y + DG.nodeH / 2;
      const ty = pb.y - DG.nodeH / 2 - 3;
      const my = (sy + ty) / 2;
      if (pa.x === pb.x) return { d: "M" + pa.x + "," + sy + " L" + pb.x + "," + ty, mid: [pa.x, my] };
      return { d: "M" + pa.x + "," + sy + " C" + pa.x + "," + my + " " + pb.x + "," + my + " " + pb.x + "," + ty, mid: [(pa.x + pb.x) / 2, my] };
    }
    // Upward: run along the gutter between the two lanes, or beside the lane.
    let gutter;
    let sx;
    let tx;
    if (pa.x === pb.x) {
      gutter = pa.x - Math.max(sa, sb) / 2 - 16;
      sx = pa.x - sa / 2;
      tx = pb.x - sb / 2 - 3;
    } else {
      const aLeft = pa.x < pb.x;
      const leftEdge = aLeft ? pa.x + sa / 2 : pb.x + sb / 2;
      const rightEdge = aLeft ? pb.x - sb / 2 : pa.x - sa / 2;
      gutter = (leftEdge + rightEdge) / 2;
      sx = aLeft ? pa.x + sa / 2 : pa.x - sa / 2;
      tx = aLeft ? pb.x - sb / 2 - 3 : pb.x + sb / 2 + 3;
    }
    const sy = pa.y - 10;
    const ty = pb.y + 8;
    const d = "M" + sx + "," + sy + " L" + gutter + "," + sy + " L" + gutter + "," + ty + " L" + tx + "," + ty;
    return { d: d, mid: [gutter, (sy + ty) / 2] };
  }

  function dgNeighbours(chapter, focus) {
    const near = {};
    if (!focus) return null;
    near[focus] = true;
    chapter.edges.forEach(function (edge) {
      if (edge.from === focus) near[edge.to] = true;
      if (edge.to === focus) near[edge.from] = true;
    });
    chapter.nodes.forEach(function (node) {
      if (node.parent === focus || node.id === focus) near[node.id] = true;
    });
    return near;
  }

  function dgNodeShape(node, w, focused) {
    const p = dgPos(node);
    const l = p.x - w / 2;
    const t = p.y - DG.nodeH / 2;
    const hgt = DG.nodeH;
    const kids = [];
    if (focused) kids.push(s("rect", { x: l - 6, y: t - 6, width: w + 12, height: hgt + 12, rx: 13, class: "dg-focus-ring" }));
    if (node.decided_by === "agent") {
      const c = 10;
      const pts = [[l + c, t], [l + w - c, t], [l + w, t + c], [l + w, t + hgt - c], [l + w - c, t + hgt], [l + c, t + hgt], [l, t + hgt - c], [l, t + c]]
        .map(function (q) { return q[0] + "," + q[1]; }).join(" ");
      kids.push(s("polygon", { points: pts, class: "dg-shape" }));
    } else if (node.kind === "ending") {
      kids.push(s("rect", { x: l, y: t, width: w, height: hgt, rx: 3, class: "dg-shape" }));
      if (node.role === "reached") kids.push(s("rect", { x: l + 4, y: t + 4, width: w - 8, height: hgt - 8, rx: 2, class: "dg-inner" }));
    } else {
      kids.push(s("rect", { x: l, y: t, width: w, height: hgt, rx: node.kind === "start" ? hgt / 2 : 9, class: "dg-shape" }));
    }
    let tx = l + 12;
    let anchor = "start";
    if (node.decided_by) {
      kids.push(s("circle", { cx: l + 18, cy: p.y, r: 10, class: "dg-badge" }));
      kids.push(s("text", { x: l + 18, y: p.y + 4, "text-anchor": "middle", class: "dg-badge-text" }, [node.decided_by === "user" ? "U" : "A"]));
      tx = l + 34;
    }
    if (node.kind === "start" || node.kind === "ending") {
      tx = p.x;
      anchor = "middle";
    }
    const room = anchor === "start" ? w - (tx - l) - 10 - (node.role === "locked" ? 26 : 0) : w - 20;
    kids.push(s("text", { x: tx, y: p.y - 3, "text-anchor": anchor, class: "dg-title" }, [dgFit(node.title, room * 1.15)]));
    const detail = node.kind === "start" ? dgWhen(node.detail) : node.detail;
    kids.push(s("text", { x: tx, y: p.y + 13, "text-anchor": anchor, class: "dg-detail" }, [dgFit(detail, room)]));
    const chips = [];
    const rec = node.decision || {};
    if (node.role === "superseded") chips.push(["superseded", "soft"]);
    if (node.role === "retracted") chips.push(["retracted", "soft"]);
    if (node.role === "hypothesis") chips.push(["? hypothesis", "ink"]);
    if (node.role === "refinement") chips.push(["refines", "violet"]);
    if (node.kind === "alternative") chips.push(["not taken", "soft"]);
    if (rec.blocked) chips.push(["locked", "amber"]);
    if (rec.conflicted) chips.push(["conflict", "red"]);
    if (rec.challenged) chips.push(["challenged", "red"]);
    let cx = l + w - 6;
    chips.slice().reverse().forEach(function (chip) {
      const cw = 12 + chip[0].length * 6.1;
      cx -= cw;
      kids.push(s("rect", { x: cx, y: t - 9, width: cw, height: 17, rx: 8.5, class: "dg-chip dg-chip-" + chip[1] }));
      kids.push(s("text", { x: cx + cw / 2, y: t + 3.5, "text-anchor": "middle", class: "dg-chip-text dg-chip-" + chip[1] }, [chip[0]]));
      cx -= 4;
    });
    if (node.role === "locked" || node.role === "locked_ending") {
      kids.push(s("circle", { cx: l + w - 16, cy: p.y + 5, r: 10, class: "dg-lock-ring" }));
      kids.push(lockIcon(l + w - 16, p.y + 4, node.role === "locked" ? "#8a4b08" : "#5e584e"));
    }
    return kids;
  }

  function dgAria(node) {
    const bits = [node.title + ": " + (node.detail || "")];
    bits.push((node.role || node.kind).replace(/_/g, " "));
    if (node.decided_by) bits.push("by " + node.decided_by);
    const rec = node.decision || {};
    if (rec.blocked) bits.push("blocked");
    if (rec.conflicted) bits.push("conflicted");
    if (rec.challenged) bits.push("challenged");
    if (node.on_path) bits.push("on the chosen path");
    return bits.join(", ");
  }

  function dgCanvas(chapter, focus) {
    const byId = {};
    chapter.nodes.forEach(function (node) { byId[node.id] = node; });
    const near = dgNeighbours(chapter, focus);
    const width = {};
    chapter.nodes.forEach(function (node) { width[node.id] = dgSize(node); });
    const off = [];
    const path = [];
    chapter.edges.forEach(function (edge) {
      const a = byId[edge.from];
      const b = byId[edge.to];
      if (!a || !b || !dgVisible(a) || !dgVisible(b)) return;
      const type = dgRelation(edge.relation);
      const touches = focus && (edge.from === focus || edge.to === focus);
      if (edge.tension && !dgTensions && !touches) return;
      const geo = dgEdgePath(a, b, width[a.id], width[b.id]);
      const cls = "dg-edge dg-" + type.style + (edge.on_path ? " dg-on-path" : "") + (b.role === "locked_ending" ? " dg-to-locked" : "");
      const parts = [s("title", null, [a.title + " " + type.label + " " + b.title])];
      if (edge.on_path) parts.push(s("path", { d: geo.d, class: "dg-halo" }));
      parts.push(s("path", { d: geo.d, class: cls, "marker-end": type.symmetric ? null : (edge.on_path ? "url(#dg-arrow-ink)" : "url(#dg-arrow)") }));
      if (!edge.derived) parts.push(dgGlyph(type, geo.mid[0], geo.mid[1], type.label));
      const group = s("g", { class: focus && !touches ? "dg-dim" : null }, parts);
      (edge.on_path ? path : off).push(group);
    });
    const nodes = [];
    chapter.nodes.forEach(function (node) {
      if (!dgVisible(node)) return;
      const focused = node.id === focus;
      const classes = ["dg-node", "dg-kind-" + node.kind, "dg-role-" + node.role];
      if (node.on_path) classes.push("dg-on-path");
      if (near && !near[node.id]) classes.push("dg-dim");
      if (dgMuted(node)) classes.push("dg-muted");
      const kids = [s("title", null, [node.title + ": " + (node.detail || "")])].concat(dgNodeShape(node, width[node.id], focused));
      const hidden = (node.alternatives || []).filter(function (id) { return byId[id] && byId[id].lane === "none"; });
      if (hidden.length && dgNotTaken) {
        const p = dgPos(node);
        // Bottom-left, straddling the border: the spine edge leaves from the
        // centre and badges sit top-right, so this corner is free.
        const px = p.x - width[node.id] / 2 + 10;
        const py = p.y + DG.nodeH / 2 - 8;
        kids.push(s("g", { class: "dg-pill" }, [
          s("rect", { x: px, y: py, width: 86, height: 17, rx: 8.5 }),
          s("text", { x: px + 43, y: py + 12.5, "text-anchor": "middle" }, ["+" + hidden.length + " not taken"]),
        ]));
      }
      nodes.push(s("g", {
        class: classes.join(" "),
        tabindex: "0",
        role: "button",
        "data-node": node.id,
        "aria-label": dgAria(node),
        "aria-pressed": focused ? "true" : "false",
        onclick: function () { dgGo(chapter.id, focused ? null : node.id); },
        onkeydown: function (event) {
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            dgGo(chapter.id, focused ? null : node.id);
          }
        },
      }, kids));
    });
    // A focused node's collapsed alternatives open as ghosts below the chapter.
    const ghosts = [];
    const fnode = focus && byId[focus];
    if (fnode && dgNotTaken) {
      const hidden = (fnode.alternatives || []).map(function (id) { return byId[id]; }).filter(function (alt) { return alt && alt.lane === "none"; });
      const fp = dgPos(fnode);
      hidden.forEach(function (alt, i) {
        const lane = i % 2 === 0 ? "left" : "right";
        const gx = DG.laneX[lane];
        const gy = DG.top + (chapter.rows + Math.floor(i / 2)) * DG.rowH + DG.nodeH / 2;
        const gw = 210;
        const sx = fp.x + (lane === "left" ? -1 : 1) * (width[fnode.id] / 2 - 30);
        const ex = lane === "left" ? gx + gw / 2 + 3 : gx - gw / 2 - 3;
        ghosts.push(s("g", { class: "dg-node dg-kind-alternative dg-role-not_taken", tabindex: "0", role: "button", "aria-label": "Not taken: " + alt.title,
          onclick: function () { dgGo(chapter.id, alt.id); } }, [
          s("title", null, ["Not taken: " + alt.title]),
          s("path", { d: "M" + sx + "," + (fp.y + DG.nodeH / 2) + " C" + sx + "," + gy + " " + sx + "," + gy + " " + ex + "," + gy, class: "dg-edge dg-ghost", "marker-end": "url(#dg-arrow)" }),
          s("rect", { x: gx - gw / 2, y: gy - 19, width: gw, height: 38, rx: 9, class: "dg-shape" }),
          s("text", { x: gx - gw / 2 + 12, y: gy - 3, class: "dg-detail" }, ["not taken · what if"]),
          s("text", { x: gx - gw / 2 + 12, y: gy + 12, class: "dg-title" }, [dgFit(alt.title, gw - 20)]),
        ]));
      });
    }
    const extraRows = ghosts.length ? Math.ceil(ghosts.length / 2) : 0;
    const height = DG.top + (chapter.rows + extraRows) * DG.rowH + 10;
    const defs = s("defs", null, [
      s("marker", { id: "dg-arrow", viewBox: "0 0 10 10", refX: "8", refY: "5", markerWidth: "7", markerHeight: "7", orient: "auto" }, [s("path", { d: "M0 0L10 5L0 10z", class: "dg-arrow" })]),
      s("marker", { id: "dg-arrow-ink", viewBox: "0 0 10 10", refX: "8", refY: "5", markerWidth: "5", markerHeight: "5", orient: "auto" }, [s("path", { d: "M0 0L10 5L0 10z", class: "dg-arrow-ink" })]),
      s("pattern", { id: "dg-hatch", width: "8", height: "8", patternUnits: "userSpaceOnUse", patternTransform: "rotate(45)" }, [
        s("rect", { width: "8", height: "8", class: "dg-hatch-bg" }),
        s("line", { x1: "0", y1: "0", x2: "0", y2: "8", class: "dg-hatch-line" }),
      ]),
    ]);
    return s("svg", {
      class: "dg-svg" + (focus ? " dg-focusing" : ""),
      width: DG.width,
      height: height,
      viewBox: "0 0 " + DG.width + " " + height,
      role: "group",
      "aria-label": "Decision graph: " + chapter.title,
    }, [defs, s("g", null, off), s("g", null, path), s("g", null, ghosts), s("g", null, nodes)]);
  }

  function dgToolbar(chapter) {
    function toggle(label, on, fn, extra) {
      return h("button", Object.assign({ type: "button", "aria-pressed": on ? "true" : "false", onclick: function () { fn(); render(); } }, extra || {}), [label]);
    }
    const jump = h("input", {
      type: "search",
      class: "dg-jump",
      placeholder: "Jump to id…",
      "aria-label": "Jump to a decision id",
      onkeydown: function (event) {
        if (event.key !== "Enter") return;
        const want = String(event.target.value || "").toLowerCase();
        if (!want) return;
        const hit = chapter.nodes.filter(function (node) { return node.id.toLowerCase().indexOf(want) >= 0 || (node.detail || "").toLowerCase().indexOf(want) >= 0; })[0];
        if (hit) dgGo(chapter.id, hit.id);
        else { dgStatus = "No node matches " + want + "."; render(); }
      },
    });
    return h("div", { class: "dg-toolbar filters", role: "toolbar", "aria-label": "Graph controls" }, [
      toggle("Everyone", dgShow === "all", function () { dgShow = "all"; }),
      toggle("You", dgShow === "user", function () { dgShow = "user"; }),
      toggle("Agents", dgShow === "agent", function () { dgShow = "agent"; }),
      h("span", { class: "dg-sep", "aria-hidden": "true" }),
      toggle("Not taken", dgNotTaken, function () { dgNotTaken = !dgNotTaken; }),
      toggle("Show tensions", dgTensions, function () { dgTensions = !dgTensions; }, { "aria-keyshortcuts": "t" }),
      toggle("Chosen path only", dgPathOnly, function () { dgPathOnly = !dgPathOnly; }),
      jump,
      toggle("?", dgHelp, function () { dgHelp = !dgHelp; }, { "aria-label": "Keyboard shortcuts", "aria-keyshortcuts": "?" }),
    ]);
  }

  function dgKey(chapter) {
    // Open by default only when the corner it covers is empty.
    const open = dgKeyOpen != null ? dgKeyOpen : !chapter.nodes.some(function (node) {
      return node.lane === "left" && node.row <= 3;
    });
    function row(cls, label) {
      return h("li", null, [h("span", { class: "dg-key-swatch " + cls, "aria-hidden": "true" }), label]);
    }
    return h("details", { class: "dg-key", open: open ? "" : null, ontoggle: function (event) { dgKeyOpen = !!event.target.open; } }, [
      h("summary", null, ["Key"]),
      h("ul", null, [
        row("path", "Chosen path"),
        row("off", "Off the path"),
        row("user", "You (U)"),
        row("agent", "Agent (A)"),
        row("ghost", "Not taken"),
        row("superseded", "Superseded"),
        row("hypothesis", "Hypothesis ?"),
        row("locked", "Locked"),
        row("challenged", "Challenged"),
        row("ending", "Ending"),
        h("li", null, ["🔒 blocks · ⚡ conflicts · ↶ supersedes · ↑ refines"]),
      ]),
    ]);
  }

  function dgHelpBox() {
    if (!dgHelp) return null;
    return h("div", { class: "dg-help", role: "note" }, [
      h("p", null, [h("strong", null, ["Shortcuts"])]),
      h("p", null, ["Tab moves between nodes · Enter or Space focuses one · Esc closes it · ↑ ↓ walk the rows · t toggles tensions · / jumps to an id · ? shows this"]),
    ]);
  }

  function dgSummaryPanel(chapter) {
    const f = chapter.facts || {};
    const e = chapter.explored || {};
    const meta = [];
    if (chapter.start) meta.push(dgWhen(chapter.start) + (chapter.end && chapter.end !== chapter.start ? " – " + dgWhen(chapter.end) : ""));
    if (chapter.branches && chapter.branches.length) meta.push("branches " + chapter.branches.join(", "));
    if (chapter.pull_requests && chapter.pull_requests.length) meta.push("PRs " + chapter.pull_requests.map(function (n) { return "#" + n; }).join(" "));
    if (chapter.sessions && chapter.sessions.length) meta.push(chapter.sessions.length + " session" + (chapter.sessions.length === 1 ? "" : "s"));
    if (chapter.cost && chapter.cost.status !== "unknown") meta.push("computed " + chapter.cost.usd + " USD" + (chapter.cost.status === "partial" ? " (partial)" : ""));
    const facts = [h("strong", null, [f.endings_reached + " of " + f.endings_total + " endings reached"])];
    if (f.locked) facts.push(" · ", h("span", { class: "dg-amber" }, [f.locked + " locked"]));
    if (f.conflicts) facts.push(" · ", h("span", { class: "dg-red" }, [f.conflicts + " conflict" + (f.conflicts === 1 ? "" : "s")]));
    if (f.challenged) facts.push(" · ", h("span", { class: "dg-red" }, [f.challenged + " challenged"]));
    const kids = [
      h("p", { class: "kicker" }, ["Chapter · connected decisions"]),
      h("h2", null, [chapter.title]),
      h("p", { class: "detail" }, [meta.join(" · ")]),
      h("p", { class: "dg-facts" }, facts),
    ];
    if (chapter.needs_attention && chapter.needs_attention.length) {
      kids.push(h("p", { class: "label" }, ["Needs attention"]));
      chapter.needs_attention.forEach(function (item) {
        const icon = item.kind === "locked" ? "🔒 " : "⚡ ";
        kids.push(h("button", {
          type: "button",
          class: "dg-attention dg-attention-" + item.kind,
          onclick: function () { dgGo(chapter.id, item.nodes[0]); },
        }, [icon, h("strong", null, [item.kind[0].toUpperCase() + item.kind.slice(1) + ": "]), item.text, h("span", { class: "dg-focus-link" }, [" Focus"])]));
      });
    }
    const endings = chapter.nodes.filter(function (node) { return node.kind === "ending"; });
    kids.push(h("p", { class: "label" }, ["Endings"]));
    if (!endings.length) kids.push(h("p", { class: "detail" }, ["No ending yet. An ending is a pull request or commit a linked session produced."]));
    else kids.push(h("ul", { class: "dg-endings" }, endings.map(function (node) {
      const end = node.ending || {};
      const bits = [node.detail];
      if (end.cost && end.cost.status !== "unknown") bits.push(end.cost.usd + " USD");
      return h("li", { class: "dg-ending-" + node.role }, [
        h("button", { type: "button", class: "dg-linkish", onclick: function () { dgGo(chapter.id, node.id); } }, [
          h("span", { class: "dg-ending-mark", "aria-hidden": "true" }, [end.locked ? "🔒" : ""]),
          node.title,
        ]),
        h("span", { class: "detail" }, [bits.filter(Boolean).join(" · ")]),
      ]);
    })));
    kids.push(h("p", { class: "label" }, ["Explored"]));
    const pct = e.percent == null ? "–" : e.percent + "%";
    kids.push(h("div", { class: "dg-explored" }, [
      h("p", null, [e.decided + " of " + e.total + " choice points decided", h("span", null, [pct])]),
      h("progress", { max: e.total || 1, value: e.decided || 0, "aria-label": "Choice points decided" }),
      h("p", { class: "detail" }, ["Decided ÷ (decided + not taken + hypotheses + locked) = " + e.decided + " ÷ (" + e.decided + " + " + e.not_taken + " + " + e.hypotheses + " + " + e.locked + "). Refinements and commits do not count. A low figure only means many recorded alternatives."]),
    ]));
    return kids;
  }

  const LINK_SENTENCE = {
    "in blocks": "Blocked by", "out blocks": "Blocks",
    "in conflicts_with": "Conflicts with", "out conflicts_with": "Conflicts with",
    "in refines": "Refined by", "out refines": "Refines",
    "in implements": "Implemented by", "in validates": "Validated by", "out validates": "Validates",
    "in contradicts": "Challenged by", "out contradicts": "Contradicts",
    "in supersedes": "Superseded by", "out supersedes": "Supersedes",
    "in constrains": "Constrained by", "out constrains": "Constrains",
  };
  const LINK_ORDER = ["Blocked by", "Conflicts with", "Challenged by", "Refined by", "Refines", "Implemented by", "Validated by", "Supersedes", "Superseded by", "Constrains", "Constrained by", "Blocks", "Validates", "Contradicts", "Leads to"];

  function dgLinkTarget(chapter, kind, ref) {
    if (kind === "decision") {
      const inChapter = chapter.nodes.some(function (node) { return node.id === ref; });
      return inChapter ? h("button", { type: "button", class: "dg-linkish", onclick: function () { dgGo(chapter.id, ref); } }, [ref]) : h("span", null, [ref]);
    }
    if (kind === "session") return h("a", { href: "#/sessions/" + encodeURIComponent(ref) }, ["session " + ref.slice(0, 12)]);
    if (kind === "commit") return h("code", null, [ref.slice(0, 12)]);
    if (kind === "node") {
      const node = chapter.nodes.filter(function (n) { return n.id === ref; })[0];
      return h("button", { type: "button", class: "dg-linkish", onclick: function () { dgGo(chapter.id, ref); } }, [node ? node.title : ref]);
    }
    return h("span", null, [kind + " " + ref]);
  }

  function dgCopy(text) {
    function done(ok) {
      dgStatus = ok ? "Copied the command. sdf did not run it." : "Copy failed. Select the command and copy it.";
      render();
    }
    const nav = typeof navigator !== "undefined" ? navigator : null;
    if (nav && nav.clipboard && nav.clipboard.writeText) {
      nav.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
      return;
    }
    done(false);
  }

  function dgWhatIf(alt) {
    return h("div", { class: "dg-whatif" }, [
      h("p", null, [h("strong", null, [alt.title])]),
      h("code", { class: "dg-command" }, [alt.command]),
      h("button", { type: "button", onclick: function () { dgCopy(alt.command); } }, ["Copy command"]),
    ]);
  }

  function dgFocusPanel(chapter, node) {
    const byId = {};
    chapter.nodes.forEach(function (n) { byId[n.id] = n; });
    const kids = [h("div", { class: "dg-panel-top" }, [
      h("button", { type: "button", onclick: function () { dgGo(chapter.id, null); } }, ["← Chapter"]),
      h("button", { type: "button", "aria-label": "Close", onclick: function () { dgGo(chapter.id, null); } }, ["×"]),
    ])];
    if (node.kind === "alternative") {
      kids.push(h("p", { class: "kicker" }, ["Not taken · what if"]));
      kids.push(h("h2", null, [node.title]));
      kids.push(h("p", { class: "detail" }, ["An alternative recorded on ", dgLinkTarget(chapter, "decision", node.parent), ". The page is read-only: copy the command to record it as a hypothesis."]));
      kids.push(dgWhatIf(node));
      return kids;
    }
    if (node.kind === "ending" || node.kind === "start") {
      const end = node.ending || {};
      kids.push(h("p", { class: "kicker" }, [node.kind === "start" ? "Chapter start" : "Ending · " + (end.type || "").replace("_", " ")]));
      kids.push(h("h2", null, [node.title]));
      kids.push(h("p", { class: "detail" }, [node.kind === "start" ? dgWhen(node.detail) : node.detail]));
      if (end.url) kids.push(h("p", null, [h("a", { href: end.url, rel: "noreferrer" }, ["Open pull request #" + end.pull_request])]));
      if (end.via && end.via.length) kids.push(h("p", null, [end.locked ? "Waits on " : "Reached via "].concat(end.via.map(function (id, i) { return [i ? ", " : "", dgLinkTarget(chapter, "decision", id)]; }).flat())));
      if (end.sessions && end.sessions.length) kids.push(h("p", null, ["Sessions "].concat(end.sessions.map(function (id, i) { return [i ? ", " : "", dgLinkTarget(chapter, "session", id)]; }).flat())));
      if (end.cost && end.cost.status !== "unknown") kids.push(h("p", { class: "detail" }, ["Computed cost " + end.cost.usd + " USD" + (end.cost.status === "partial" ? " (partial)" : "")]));
      return kids;
    }
    const rec = node.decision || {};
    kids.push(h("p", { class: "kicker" }, ["Decision" + (node.decided_by ? " · by " + (node.decided_by === "user" ? "you" : "the agent") : "")]));
    kids.push(h("h2", null, [node.id]));
    const chips = [h("span", { class: "dg-pchip" }, [rec.status || "decision"]), h("span", { class: "dg-pchip" }, [rec.effective_status || "active"])];
    if (rec.blocked) chips.push(h("span", { class: "dg-pchip dg-amber" }, ["🔒 blocked"]));
    if (rec.conflicted) chips.push(h("span", { class: "dg-pchip dg-red" }, ["⚡ conflicted"]));
    if (rec.challenged) chips.push(h("span", { class: "dg-pchip dg-red" }, ["challenged"]));
    kids.push(h("p", { class: "dg-pchips" }, chips));
    const meta = [dgWhen(rec.recorded_at)];
    if (rec.source) meta.push("source " + rec.source);
    kids.push(h("p", { class: "detail" }, [meta.join(" · ")]));
    kids.push(h("p", null, [rec.statement || node.detail]));
    if (rec.reason) kids.push(h("p", null, [h("span", { class: "label" }, ["Reason"]), rec.reason]));
    else kids.push(h("p", { class: "detail" }, ["Reason: none recorded. sdf keeps what was recorded and never invents one."]));
    if (rec.retraction) kids.push(h("p", { class: "detail" }, ["Retracted " + dgWhen(rec.retraction.retracted_at) + (rec.retraction.reason ? ": " + rec.retraction.reason : "")]));
    const groups = {};
    (node.links || []).forEach(function (link) {
      const label = LINK_SENTENCE[link.direction + " " + link.relation] || (link.direction === "in" ? link.relation + " ←" : link.relation + " →");
      (groups[label] = groups[label] || []).push(dgLinkTarget(chapter, link.kind, link.ref));
    });
    chapter.edges.forEach(function (edge) {
      if (edge.from === node.id && (edge.relation === "next" || edge.relation === "outcome")) {
        (groups["Leads to"] = groups["Leads to"] || []).push(dgLinkTarget(chapter, "node", edge.to));
      }
    });
    if (rec.blocked && rec.open_blockers && rec.open_blockers.length) {
      groups["Blocked by"] = (rec.open_blockers || []).map(function (id) { return dgLinkTarget(chapter, "decision", id); });
    }
    const labels = Object.keys(groups).sort(function (a, b) {
      const ia = LINK_ORDER.indexOf(a);
      const ib = LINK_ORDER.indexOf(b);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
    });
    if (labels.length) {
      kids.push(h("p", { class: "label" }, ["Links"]));
      kids.push(h("ul", { class: "dg-links" }, labels.map(function (label) {
        const items = [];
        groups[label].forEach(function (item, i) { if (i) items.push(", "); items.push(item); });
        return h("li", null, [h("strong", null, [label + " "])].concat(items));
      })));
    }
    const alts = (node.alternatives || []).map(function (id) { return byId[id]; }).filter(Boolean);
    if (alts.length) {
      kids.push(h("p", { class: "label" }, ["Not taken · what if"]));
      alts.forEach(function (alt) { kids.push(dgWhatIf(alt)); });
      kids.push(h("p", { class: "detail" }, ["Copy command records the alternative as a hypothesis. The page stays read-only."]));
    }
    return kids;
  }

  function dgOrderedNodes(chapter) {
    return chapter.nodes.filter(dgVisible).slice().sort(function (a, b) {
      if (a.row !== b.row) return a.row - b.row;
      const order = { left: 0, spine: 1, right: 2 };
      return (order[a.lane] || 0) - (order[b.lane] || 0);
    });
  }

  function decisionsTab(chapterId, focus) {
    const chapters = dgChapters();
    if (decisionGraphError) {
      return h("div", { class: "error" }, [h("p", null, [decisionGraphError])]);
    }
    if (!chapters.length) {
      return h("section", { class: "empty" }, [
        h("h2", null, ["No decision records in the store."]),
        h("p", null, ["An agent with the sdf-decisions skill records them as you work. You can add one by hand:"]),
        h("pre", null, ["sdf decision add --id dont-push --status decision --decided-by user --statement 'No push or PR without my OK'"]),
      ]);
    }
    let chapter = chapters.filter(function (c) { return c.id === chapterId; })[0] || chapters[0];
    const node = focus ? chapter.nodes.filter(function (n) { return n.id === focus; })[0] : null;
    if (!node) focus = null;
    const rail = h("nav", { class: "dg-rail", "aria-label": "Chapters" }, [
      h("p", { class: "label" }, ["Chapters"]),
      h("ul", null, chapters.map(function (c) {
        const e = c.explored || {};
        const bits = [dgWhen(c.end), (e.decided || 0) + "/" + (e.total || 0) + " decided"];
        if (c.facts && c.facts.locked) bits.push(c.facts.locked + " locked");
        return h("li", null, [h("a", { href: dgHash(c.id), "aria-current": c.id === chapter.id ? "true" : null }, [
          h("strong", null, [c.title]),
          h("span", null, [bits.join(" · ")]),
        ])]);
      })),
    ]);
    const canvas = h("div", { class: "dg-canvas-wrap" }, [
      dgToolbar(chapter),
      dgHelpBox(),
      h("div", {
        class: "dg-canvas",
        tabindex: "-1",
        onkeydown: function (event) {
          const key = event.key;
          if (event.target && event.target.tagName === "INPUT") return;
          if (key === "Escape") { dgGo(chapter.id, null); return; }
          if (key === "t") { dgTensions = !dgTensions; render(); return; }
          if (key === "?") { dgHelp = !dgHelp; render(); return; }
          if (key === "ArrowDown" || key === "ArrowUp") {
            event.preventDefault();
            const list = dgOrderedNodes(chapter);
            let at = -1;
            for (let i = 0; i < list.length; i++) if (list[i].id === focus) at = i;
            const next = list[Math.max(0, Math.min(list.length - 1, at + (key === "ArrowDown" ? 1 : -1)))];
            if (next) dgGo(chapter.id, next.id);
          }
        },
      }, [dgKey(chapter), dgCanvas(chapter, focus)]),
      h("p", { class: "dg-live", role: "status", "aria-live": "polite" }, [dgStatus]),
    ]);
    const panel = h("aside", { class: "dg-panel", "aria-label": node ? "Decision details" : "Chapter summary" }, node ? dgFocusPanel(chapter, node) : dgSummaryPanel(chapter));
    return h("section", { class: "dg" }, [rail, canvas, panel]);
  }

  function dgRestoreFocus() {
    const current = route();
    if (current.name !== "decisions" || !current.node || !app.querySelector) return;
    const el = app.querySelector('[data-node="' + String(current.node).replace(/["\\]/g, "\\$&") + '"]');
    if (el && el.focus) el.focus();
  }

  function shell(active, body) {
    app.replaceChildren(h("div", { class: "wrap" }, [
      header(active),
      loadError ? h("div", { class: "error" }, [h("p", null, [loadError])]) : body,
      footer(),
    ]));
  }

  function render() {
    const current = route();
    if (!snapshot && !loadError) {
      shell(current.name, h("p", { class: "detail" }, ["Reading the summary…"]));
      return;
    }
    if (loadError && !snapshot) {
      shell(current.name, null);
      return;
    }
    try {
      let body;
      if (current.name === "detail") body = detail(current.id);
      else if (current.name === "sessions") body = sessions();
      else if (current.name === "decisions") body = decisionsTab(current.chapter, current.node);
      else body = overview();
      shell(current.name, body);
      dgRestoreFocus();
    } catch (err) {
      const message = err && err.message ? err.message : "The page could not be drawn.";
      shell(current.name, h("div", { class: "error" }, [h("p", null, [message])]));
    }
  }

  function readJSON(url, label) {
    return fetch(url, { headers: { Accept: "application/json" } }).then(function (response) {
      if (!response.ok) throw new Error(label + " request failed (" + response.status + ")");
      return response.json();
    });
  }

  function load(manual) {
    if (manual) loadError = "";
    Promise.all([
      readJSON("/api/snapshot", "summary"),
      readJSON("/api/decisions", "decisions"),
      readJSON("/api/summary", "summary"),
      readJSON("/api/decisions/graph", "decision graph").catch(function (err) {
        return { error: err && err.message ? err.message : "The decision graph could not be read." };
      }),
    ])
      .then(function (bodies) {
        snapshot = bodies[0];
        if (!snapshot.sessions) snapshot.sessions = [];
        decisions = bodies[1] || { decisions: [], edges: [] };
        if (!decisions.decisions) decisions.decisions = [];
        if (!decisions.edges) decisions.edges = [];
        budget = bodies[2] && Object.prototype.hasOwnProperty.call(bodies[2], "budget") ? bodies[2].budget : null;
        decisionGraph = bodies[3] && !bodies[3].error ? bodies[3] : null;
        decisionGraphError = bodies[3] && bodies[3].error ? bodies[3].error : "";
        loadError = "";
        render();
      })
      .catch(function (err) {
        loadError = err && err.message ? err.message : "The summary could not be read.";
        render();
      });
  }

  window.addEventListener("hashchange", render);
  render();
  load(false);
})();
