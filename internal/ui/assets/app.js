(function () {
  const app = document.getElementById("app");
  let snapshot = null;
  let decisions = null;
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
    if (!records.length) {
      kids.push(h("p", null, ["no decision"]));
    } else {
      records.forEach(function (record) {
        kids.push(h("p", { class: "decision" }, [
          h("span", { class: "status" }, [record.status || "unknown"]),
          document.createTextNode(record.statement || ""),
        ]));
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
      h("section", { class: "section cards" }, [
        costCard("Computed cost", view.computed_cost_usd, summary.computed_cost_usd, true),
        costCard("Agent-reported cost", view.agent_reported_cost_usd, null, false),
      ]),
      tokenSection(view),
      toolSection(view),
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
    if (summary.spans_hidden) {
      kids.push(h("p", { class: "detail" }, [summary.spans_hidden + " internal spans hidden, same as sdf summary without --all-spans"]));
    }
    return h("section", { class: "section" }, kids);
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
    let body;
    if (current.name === "detail") body = detail(current.id);
    else if (current.name === "sessions") body = sessions();
    else body = overview();
    shell(current.name, body);
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
    ])
      .then(function (bodies) {
        snapshot = bodies[0];
        if (!snapshot.sessions) snapshot.sessions = [];
        decisions = bodies[1] || { decisions: [], edges: [] };
        if (!decisions.decisions) decisions.decisions = [];
        if (!decisions.edges) decisions.edges = [];
        budget = bodies[2] && Object.prototype.hasOwnProperty.call(bodies[2], "budget") ? bodies[2].budget : null;
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
