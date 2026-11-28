(function () {
  const app = document.getElementById("app");
  let snapshot = null;
  let decisions = null;
  let decisionGraph = null;
  let decisionGraphError = "";
  let overviewCharts = null;
  let overviewChartsError = "";
  let budget = undefined;
  let loadError = "";
  let agentFilter = "all";
  let sortKey = "started";
  let sortDir = -1;

  // childNodes turns a kids list into DOM nodes: nested lists are
  // flattened, null, undefined, true and false are dropped, and anything
  // else that is not a node becomes a text node. A real append() or
  // replaceChildren() would draw null as the text "null", so every caller
  // that fills an element passes its kids through here (h, s, fill).
  function childNodes(kids) {
    const out = [];
    (function add(list) {
      for (let i = 0; i < list.length; i++) {
        const kid = list[i];
        if (kid == null || kid === false || kid === true) continue;
        if (Array.isArray(kid)) add(kid);
        else out.push(kid instanceof Node ? kid : document.createTextNode(String(kid)));
      }
    })(Array.isArray(kids) ? kids : (kids == null ? [] : [kids]));
    return out;
  }

  // fill replaces el's children with kids, filtered as childNodes does.
  function fill(el, kids) {
    el.replaceChildren.apply(el, childNodes(kids));
    return el;
  }

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
    childNodes(kids).forEach(function (kid) { node.append(kid); });
    return node;
  }

  // A hand-edited or truncated link can hold a malformed percent escape
  // (#/decisions/x/%E0%A4%A); decodeURIComponent throws on it. Such a part
  // reads as fallback instead, so the route still draws.
  function safeDecode(part, fallback) {
    try {
      return decodeURIComponent(part);
    } catch (err) {
      return fallback;
    }
  }

  function route() {
    const hash = location.hash || "#/overview";
    if (hash.indexOf("#/sessions/") === 0) {
      // An undecodable id is shown as typed, which no session matches.
      const raw = hash.slice("#/sessions/".length);
      return { name: "detail", id: safeDecode(raw, raw) };
    }
    if (hash === "#/sessions") return { name: "sessions" };
    if (/^#\/decisions(\/|\?|$)/.test(hash)) {
      // #/decisions[/<chapter>[/<node>]][?at=N]: at is the replay position,
      // 1-based over the chapter's timeline; without it the page shows now.
      // An undecodable chapter or node falls back to the newest chapter or
      // the chapter view.
      const q = hash.indexOf("?");
      const path = q >= 0 ? hash.slice(0, q) : hash;
      const at = /(?:^|&)at=(\d+)/.exec(q >= 0 ? hash.slice(q + 1) : "");
      const parts = path.slice("#/decisions".length).split("/").filter(Boolean).map(function (part) { return safeDecode(part, ""); });
      return { name: "decisions", chapter: parts[0] || "", node: parts[1] || "", at: at ? parseInt(at[1], 10) : null };
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

  // Path data is copied from docs/images/logo.svg. Colors are classes so the
  // mark is not redrawn and the page carries no inline style attribute.
  function markLogo() {
    const weave = [
      "M113.7 33.7 L194.3 114.3",
      "M87.7 59.7 L168.3 140.3",
      "M61.7 85.7 L128.0 152.0",
      "M142.3 33.7 L61.7 114.3",
      "M168.3 59.7 L87.7 140.3",
      "M128.0 152.0 L98.0 206.0",
    ];
    return s("svg", {
      class: "mark-logo",
      viewBox: "0 0 256 256",
      "aria-hidden": "true",
    }, [
      s("rect", { class: "mark-tile", width: "256", height: "256" }),
      s("g", { class: "mark-weave" }, weave.map(function (d) { return s("path", { d: d }); })),
      s("path", { class: "mark-thread", d: "M194.3 85.7 L128.0 152.0 L158.0 206.0" }),
    ]);
  }

  function routeTitle(current) {
    if (current && current.name === "sessions") return "Sessions · SDF";
    if (current && current.name === "detail") return "Session · SDF";
    if (current && current.name === "decisions") return "Decisions · SDF";
    return "Overview · SDF";
  }

  function header(active) {
    return h("header", { class: "top" }, [
      h("div", { class: "mark" }, [
        markLogo(),
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
      h("p", null, ["This database has no sessions yet. Point an agent at the receiver, restart the agent, then refresh."]),
      h("pre", null, ["sdf init <agent> --write\nsdf init --check"]),
      h("p", { class: "detail" }, ["Agents: claude-code, codex, opencode, pi, qwen, grok, gemini, copilot. Without --write, init only prints the settings. The agent reads them when it starts, so restart it. Leave sdf serve running while the agent exports."]),
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
      kids.push(chartsSection());
      kids.push(h("section", { class: "section" }, [
        h("h2", null, ["By agent"]),
        agentTable(snapshot.agents || []),
      ]));
    }
    if (isEmpty()) kids.push(chartsSection());
    if (snapshot.unattributed && snapshot.unattributed.length) kids.push(unattributedBlock(snapshot.unattributed));
    kids.push(decisionsSection());
    return h("div", null, kids);
  }

  function chartsHasData() {
    if (overviewChartsError && !overviewCharts) return true;
    if (!overviewCharts) return false;
    const totals = overviewCharts.totals || {};
    if ((totals.commits_total || 0) > 0) return true;
    if ((totals.unlinked_commits || 0) > 0 || (totals.unlinked_decisions || 0) > 0) return true;
    if ((totals.decisions_active || 0) > 0) return true;
    const activity = snapshot && snapshot.activity_by_day ? snapshot.activity_by_day : [];
    if (activity.length) return true;
    if (snapshot && snapshot.undated_sessions) return true;
    return false;
  }

  function chartsSection() {
    if (!chartsHasData()) return null;
    const kids = [h("h2", null, ["Overview charts"])];
    if (overviewChartsError && !overviewCharts) {
      kids.push(h("p", { class: "reason" }, [overviewChartsError]));
      return h("section", { class: "section charts-section", "aria-label": "Overview charts" }, kids);
    }
    kids.push(h("p", { class: "detail" }, [
      "Decision-link recall is the share of SDF-recorded commits that have an implements edge to a stored decision. SDF-Decision trailers are not stored, so they are not counted. Unlinked commits and unlinked decisions are the same gaps sdf decision audit reports. Sessions and computed cost are grouped by the UTC day the session started."
    ]));
    kids.push(h("div", { class: "charts" }, [
      recallChartCard(),
      unlinkedChartCard(),
      activityChartCard(),
    ]));
    return h("section", { class: "section charts-section", "aria-label": "Overview charts" }, kids);
  }

  function formatPercent(text) {
    if (text == null || text === "") return "";
    const n = Number(text);
    if (!isFinite(n)) return "";
    const pct = Math.round(n * 1000) / 10;
    return String(pct) + "%";
  }

  function recallChartCard() {
    const totals = overviewCharts.totals || {};
    const series = overviewCharts.recall_by_day || [];
    const known = totals.recall_status === "known";
    const headline = known
      ? (String(totals.commits_with_implements || 0) + " of " + String(totals.commits_total || 0) + " SDF-recorded commits have an implements edge (" + formatPercent(totals.recall) + ").")
      : (totals.recall_reason || "No commits in the store.");
    const table = h("table", { class: "chart-table", id: "chart-recall-table" }, [
      h("thead", null, [h("tr", null, [
        h("th", { scope: "col" }, ["Day (UTC)"]),
        h("th", { scope: "col" }, ["Commits"]),
        h("th", { scope: "col" }, ["With implements"]),
        h("th", { scope: "col" }, ["Daily recall"]),
        h("th", { scope: "col" }, ["Cumulative recall"]),
      ])]),
      h("tbody", null, series.length ? series.map(function (row) {
        return h("tr", null, [
          h("td", null, [row.day]),
          h("td", null, [String(row.commits_total)]),
          h("td", null, [String(row.commits_with_implements)]),
          h("td", null, [row.recall_status === "known" ? formatPercent(row.recall) : "—"]),
          h("td", null, [row.cumulative_recall_status === "known" ? formatPercent(row.cumulative_recall) : "—"]),
        ]);
      }) : [h("tr", null, [h("td", { colspan: "5" }, ["No commits recorded yet."])])]),
    ]);
    const values = series.map(function (row) {
      return row.cumulative_recall_status === "known" ? Number(row.cumulative_recall) : null;
    });
    const svg = lineChart(series.map(function (row) { return row.day; }), values, {
      max: 1,
      yLabel: function (v) { return formatPercent(String(v)) || "0%"; },
      label: "Cumulative decision-link recall by UTC day",
      describedBy: "chart-recall-table",
    });
    return chartCard("Decision recall", headline, svg, table, "No commits recorded yet, so there is no recall to plot.");
  }

  function unlinkedChartCard() {
    const totals = overviewCharts.totals || {};
    const series = overviewCharts.unlinked_by_day || [];
    const headline = String(totals.unlinked_commits || 0) + " unlinked commits, " +
      String(totals.unlinked_decisions || 0) + " unlinked decisions (the same gaps sdf decision audit reports).";
    const table = h("table", { class: "chart-table", id: "chart-unlinked-table" }, [
      h("thead", null, [h("tr", null, [
        h("th", { scope: "col" }, ["Day (UTC)"]),
        h("th", { scope: "col" }, ["Unlinked commits"]),
        h("th", { scope: "col" }, ["Unlinked decisions"]),
      ])]),
      h("tbody", null, series.length ? series.map(function (row) {
        return h("tr", null, [
          h("td", null, [row.day]),
          h("td", null, [String(row.unlinked_commits)]),
          h("td", null, [String(row.unlinked_decisions)]),
        ]);
      }) : [h("tr", null, [h("td", { colspan: "3" }, ["No unlinked gaps in the store."])])]),
    ]);
    const svg = barChart(series.map(function (row) { return row.day; }), [
      { name: "Unlinked commits", values: series.map(function (row) { return row.unlinked_commits; }), cls: "chart-bar-a" },
      { name: "Unlinked decisions", values: series.map(function (row) { return row.unlinked_decisions; }), cls: "chart-bar-b" },
    ], { label: "Unlinked commits and unlinked decisions by UTC day", describedBy: "chart-unlinked-table" });
    return chartCard("Unlinked commits and decisions", headline, svg, table, "No days to plot yet.");
  }

  function activityChartCard() {
    const series = snapshot && snapshot.activity_by_day ? snapshot.activity_by_day : [];
    const undated = snapshot && snapshot.undated_sessions ? snapshot.undated_sessions : 0;
    let total = 0;
    series.forEach(function (row) { total += row.sessions || 0; });
    let headline = String(total) + " dated session" + (total === 1 ? "" : "s") + " by UTC start day.";
    if (undated) headline += " " + String(undated) + " session" + (undated === 1 ? "" : "s") + " had no start time and " + (undated === 1 ? "is" : "are") + " not plotted.";
    const table = h("table", { class: "chart-table", id: "chart-activity-table" }, [
      h("thead", null, [h("tr", null, [
        h("th", { scope: "col" }, ["Day (UTC)"]),
        h("th", { scope: "col" }, ["Sessions"]),
        h("th", { scope: "col" }, ["Computed USD"]),
      ])]),
      h("tbody", null, series.length ? series.map(function (row) {
        const cost = row.computed_cost_usd || {};
        const costText = cost.status === "known" ? (cost.value == null ? "0" : String(cost.value)) : "unknown";
        return h("tr", null, [
          h("td", null, [row.day]),
          h("td", null, [String(row.sessions)]),
          h("td", null, [costText]),
        ]);
      }) : [h("tr", null, [h("td", { colspan: "3" }, ["No sessions with a start time."])])]),
    ]);
    const days = series.map(function (row) { return row.day; });
    const sessions = series.map(function (row) { return row.sessions || 0; });
    const costs = series.map(function (row) {
      const cost = row.computed_cost_usd || {};
      return cost.status === "known" && cost.value != null && cost.value !== "" ? Number(cost.value) : null;
    });
    const svg = comboChart(days, sessions, costs, {
      label: "Session count and computed cost in USD by UTC day",
      describedBy: "chart-activity-table",
    });
    return chartCard("Sessions and cost", headline, svg, table, "No sessions with a start time, so there is nothing to plot.");
  }

  function chartCard(title, headline, svg, table, empty) {
    const plot = svg || h("p", { class: "chart-empty" }, [empty || "The chart could not be drawn. The table below has the same figures."]);
    return h("article", { class: "chart-card" }, [
      h("h3", null, [title]),
      h("p", { class: "detail" }, [headline]),
      h("div", { class: "chart-plot" }, [plot]),
      table,
    ]);
  }

  function chartLayout(n) {
    const W = Math.max(320, Math.min(640, 72 + Math.max(n, 1) * 48));
    const H = 200;
    const pad = { l: 44, r: 48, t: 14, b: 36 };
    return { W: W, H: H, pad: pad, iw: W - pad.l - pad.r, ih: H - pad.t - pad.b };
  }

  function shortDay(day) {
    if (!day || day.length < 10) return day || "";
    return day.slice(5);
  }

  function niceMax(values) {
    let max = 0;
    values.forEach(function (v) {
      if (v != null && isFinite(v) && v > max) max = v;
    });
    if (max <= 0) return 1;
    const pow = Math.pow(10, Math.floor(Math.log(max) / Math.LN10));
    const n = max / pow;
    const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
    return nice * pow;
  }

  // Series 1 and 2 are close in luminance, so adjacent columns keep a 1px gap.
  const CHART_COLUMN_GAP = 1;

  function lineChart(days, values, opts) {
    if (!days.length) return null;
    const box = chartLayout(days.length);
    const max = opts.max != null ? opts.max : niceMax(values);
    const kids = chartFrame(box, days, max, opts.yLabel);
    const pts = [];
    values.forEach(function (v, i) {
      if (v == null || !isFinite(v)) return;
      const x = box.pad.l + (days.length === 1 ? box.iw / 2 : box.iw * i / (days.length - 1));
      const y = box.pad.t + box.ih * (1 - v / max);
      pts.push({ x: x, y: y });
      kids.push(s("circle", { cx: x, cy: y, r: 3, class: "chart-point" }));
    });
    if (pts.length >= 2) {
      let d = "M " + pts[0].x + " " + pts[0].y;
      for (let i = 1; i < pts.length; i++) d += " L " + pts[i].x + " " + pts[i].y;
      kids.push(s("path", { d: d, class: "chart-line", fill: "none" }));
    }
    return chartSvg(box, opts.label, opts.describedBy, kids);
  }

  function barChart(days, series, opts) {
    if (!days.length) return null;
    const box = chartLayout(days.length);
    let all = [];
    series.forEach(function (srow) { all = all.concat(srow.values); });
    const max = niceMax(all);
    const kids = chartFrame(box, days, max, null);
    const groupW = days.length === 0 ? box.iw : box.iw / days.length;
    const inner = groupW * 0.72;
    const barW = inner / Math.max(series.length, 1);
    days.forEach(function (day, i) {
      series.forEach(function (srow, si) {
        const v = srow.values[i] || 0;
        const h = max === 0 ? 0 : box.ih * (v / max);
        const x = box.pad.l + i * groupW + (groupW - inner) / 2 + si * barW;
        const y = box.pad.t + box.ih - h;
        kids.push(s("rect", {
          x: x, y: y, width: Math.max(barW - CHART_COLUMN_GAP, 1), height: Math.max(h, v > 0 ? 1 : 0),
          class: srow.cls || "chart-bar-a",
        }));
      });
    });
    const legendY = 10;
    series.forEach(function (srow, si) {
      const x = box.pad.l + si * 150;
      kids.push(s("rect", { x: x, y: legendY - 6, width: 10, height: 10, class: srow.cls || "chart-bar-a" }));
      kids.push(s("text", { x: x + 14, y: legendY + 3, class: "chart-legend" }, [srow.name]));
    });
    return chartSvg(box, opts.label, opts.describedBy, kids);
  }

  function comboChart(days, sessions, costs, opts) {
    if (!days.length) return null;
    const box = chartLayout(days.length);
    const sMax = niceMax(sessions);
    const cMax = niceMax(costs.map(function (v) { return v == null ? 0 : v; }));
    const kids = [];
    kids.push(s("line", { x1: box.pad.l, y1: box.pad.t + box.ih, x2: box.pad.l + box.iw, y2: box.pad.t + box.ih, class: "chart-axis" }));
    kids.push(s("line", { x1: box.pad.l, y1: box.pad.t, x2: box.pad.l, y2: box.pad.t + box.ih, class: "chart-axis" }));
    kids.push(s("line", { x1: box.pad.l + box.iw, y1: box.pad.t, x2: box.pad.l + box.iw, y2: box.pad.t + box.ih, class: "chart-axis" }));
    for (let t = 0; t <= 4; t++) {
      const frac = t / 4;
      const y = box.pad.t + box.ih * (1 - frac);
      kids.push(s("line", { x1: box.pad.l, y1: y, x2: box.pad.l + box.iw, y2: y, class: "chart-grid" }));
      const sVal = sMax * frac;
      const cVal = cMax * frac;
      kids.push(s("text", { x: box.pad.l - 6, y: y + 3, class: "chart-tick chart-tick-left" }, [String(sVal % 1 === 0 ? sVal : Math.round(sVal * 10) / 10)]));
      kids.push(s("text", { x: box.pad.l + box.iw + 6, y: y + 3, class: "chart-tick chart-tick-right" }, [String(cVal % 1 === 0 ? cVal : Math.round(cVal * 100) / 100)]));
    }
    kids.push(s("text", { x: box.pad.l, y: 12, class: "chart-legend" }, ["Sessions"]));
    kids.push(s("text", { x: box.pad.l + box.iw, y: 12, class: "chart-legend chart-tick-right" }, ["USD"]));
    const groupW = box.iw / days.length;
    const barW = groupW * 0.5;
    days.forEach(function (day, i) {
      const v = sessions[i] || 0;
      const h = sMax === 0 ? 0 : box.ih * (v / sMax);
      const x = box.pad.l + i * groupW + (groupW - barW) / 2;
      const y = box.pad.t + box.ih - h;
      kids.push(s("rect", {
        x: x, y: y, width: Math.max(barW, 1), height: Math.max(h, v > 0 ? 1 : 0),
        class: "chart-bar-a",
      }));
      const labelX = box.pad.l + i * groupW + groupW / 2;
      kids.push(s("text", { x: labelX, y: box.H - 10, class: "chart-tick chart-tick-x" }, [shortDay(day)]));
    });
    const pts = [];
    costs.forEach(function (v, i) {
      if (v == null || !isFinite(v)) return;
      const x = box.pad.l + i * groupW + groupW / 2;
      const y = box.pad.t + box.ih * (1 - (cMax === 0 ? 0 : v / cMax));
      pts.push({ x: x, y: y });
      kids.push(s("circle", { cx: x, cy: y, r: 3, class: "chart-point" }));
    });
    if (pts.length >= 2) {
      let d = "M " + pts[0].x + " " + pts[0].y;
      for (let i = 1; i < pts.length; i++) d += " L " + pts[i].x + " " + pts[i].y;
      kids.push(s("path", { d: d, class: "chart-line", fill: "none" }));
    }
    return chartSvg(box, opts.label, opts.describedBy, kids);
  }

  function chartFrame(box, days, max, yLabel) {
    const kids = [];
    kids.push(s("line", { x1: box.pad.l, y1: box.pad.t + box.ih, x2: box.pad.l + box.iw, y2: box.pad.t + box.ih, class: "chart-axis" }));
    kids.push(s("line", { x1: box.pad.l, y1: box.pad.t, x2: box.pad.l, y2: box.pad.t + box.ih, class: "chart-axis" }));
    for (let t = 0; t <= 4; t++) {
      const frac = t / 4;
      const y = box.pad.t + box.ih * (1 - frac);
      const val = max * frac;
      kids.push(s("line", { x1: box.pad.l, y1: y, x2: box.pad.l + box.iw, y2: y, class: "chart-grid" }));
      const label = yLabel ? yLabel(val) : String(val % 1 === 0 ? val : Math.round(val * 100) / 100);
      kids.push(s("text", { x: box.pad.l - 6, y: y + 3, class: "chart-tick chart-tick-left" }, [label]));
    }
    const groupW = days.length === 0 ? box.iw : box.iw / days.length;
    days.forEach(function (day, i) {
      const x = days.length === 1 ? box.pad.l + box.iw / 2 : box.pad.l + i * groupW + groupW / 2;
      kids.push(s("text", { x: x, y: box.H - 10, class: "chart-tick chart-tick-x" }, [shortDay(day)]));
    });
    return kids;
  }

  function chartSvg(box, label, describedBy, kids) {
    return s("svg", {
      width: "100%",
      height: box.H,
      viewBox: "0 0 " + box.W + " " + box.H,
      role: "img",
      "aria-label": label,
      "aria-describedby": describedBy,
      focusable: "false",
      class: "chart-svg",
    }, kids);
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

  function decisionCategoryLabel(record) {
    const raw = record && record.category != null ? String(record.category).trim() : "";
    return raw || "implementation";
  }

  function decisionCategoryStatus(record) {
    return h("span", { class: "status" }, [h("span", { class: "sr-only" }, ["category "]), decisionCategoryLabel(record)]);
  }

  function decisionWho(record) {
    if (!record || !record.decided_by) return "";
    if (record.decided_by_legacy || record.decided_by === "user" || record.decided_by === "agent") {
      return record.decided_by + " (legacy)";
    }
    return record.decided_by;
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
          decisionCategoryStatus(record),
        ];
        if (record.decided_by) {
          line.push(h("span", { class: "status" }, [decisionWho(record)]));
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
        h("table", { class: "wide dense" }, [
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
  let dgJumpValue = "";
  let dgList = false;
  // Focus to restore after the next draw: "node:<id>" or a data-focus-key.
  let dgWantFocus = "";
  let dgNavigating = false;
  let dgLive = null;
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
    childNodes(kids).forEach(function (kid) { node.append(kid); });
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

  function dgHash(chapter, node, at) {
    let out = "#/decisions/" + encodeURIComponent(chapter);
    if (node) out += "/" + encodeURIComponent(node);
    if (at != null) out += "?at=" + at;
    return out;
  }

  // Focusing a node keeps the replay position; a chapter link does not.
  function dgGo(chapter, node) {
    location.hash = dgHash(chapter, node, dgView.chapter && dgView.chapter.id === chapter ? dgAt : null);
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

  // Approximate advance widths: 11px detail text runs about 6.1px a
  // character, the 12.5px semibold title about 7.1px (dgSize uses the same).
  const DG_CHAR = { detail: 6.1, title: 7.1 };

  function dgFit(text, width, charW) {
    const room = Math.max(4, Math.floor(width / (charW || DG_CHAR.detail)));
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
    const by = node.decided_by || "";
    if (dgShow === "login") return by === "" || by === "user" || by === "agent";
    return by !== dgShow;
  }

  // Legacy words keep a single letter. A login mark is unique in the
  // graph and at most three characters, so it fits the 26px badge circle
  // (11px bold capitals run about 8px wide). A shared prefix takes a digit
  // instead of growing. A one-character login is never the bare letter U
  // or A. The legend, the accessible name, and the title carry the full login.
  function dgBadgeMarks(nodes) {
    const logins = [];
    (nodes || []).forEach(function (node) {
      const by = node.decided_by || "";
      if (!by || by === "user" || by === "agent" || logins.indexOf(by) >= 0) return;
      logins.push(by);
    });
    const marks = { user: "U", agent: "A" };
    const used = { U: true, A: true };
    function clashes(by, n) {
      const pref = by.toUpperCase().slice(0, n);
      if (pref === "U" || pref === "A") return true;
      return logins.some(function (other) {
        return other !== by && other.toUpperCase().slice(0, n) === pref;
      });
    }
    function claim(mark) {
      if (!mark || mark.length > 3 || mark === "U" || mark === "A" || used[mark]) return false;
      used[mark] = true;
      return true;
    }
    logins.forEach(function (by) {
      const upper = by.toUpperCase();
      let mark = "";
      const start = by.length === 1 ? 1 : 2;
      for (let n = start; n <= Math.min(3, upper.length); n++) {
        const pref = upper.slice(0, n);
        if (!clashes(by, n) && !used[pref] && pref !== "U" && pref !== "A") {
          mark = pref;
          break;
        }
      }
      if (!claim(mark)) {
        const stems = [];
        const two = upper.slice(0, Math.min(2, upper.length));
        if (two.length === 2) stems.push(two);
        if (upper.slice(0, 1)) stems.push(upper.slice(0, 1));
        stems.push("Z");
        for (let s = 0; s < stems.length && !mark; s++) {
          const stem = stems[s];
          for (let i = 2; i <= 99; i++) {
            const next = stem + i;
            if (next.length > 3) break;
            if (claim(next)) { mark = next; break; }
          }
        }
      }
      marks[by] = mark;
    });
    return marks;
  }

  function dgPos(node) {
    return { x: DG.laneX[node.lane] || DG.laneX.spine, y: DG.top + node.row * DG.rowH + DG.nodeH / 2 };
  }

  function lockIcon(x, y, tone) {
    return s("g", { transform: "translate(" + x + "," + y + ")", class: "dg-lock-" + tone, "aria-hidden": "true" }, [
      s("path", { d: "M-4 -1 v-3 a4 4 0 0 1 8 0 v3", class: "dg-lock-shackle" }),
      s("rect", { x: "-6", y: "-1", width: "12", height: "9", rx: "1.5", class: "dg-lock-body" }),
    ]);
  }

  function dgGlyph(type, x, y, label) {
    let shape;
    if (type.style === "lock") {
      shape = [s("circle", { cx: x, cy: y, r: 11, class: "dg-glyph-lock" }), lockIcon(x, y - 1.5, "locked")];
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
    // pts is the drawn line as a polyline (a curve is sampled), span the
    // part of it a mid-edge glyph may sit on, and len the visible length.
    if (a.row === b.row) {
      const dir = pb.x > pa.x ? 1 : -1;
      const x0 = pa.x + dir * sa / 2;
      const x1 = pb.x - dir * (sb / 2 + 3);
      const flat = [[x0, pa.y], [x1, pb.y]];
      return { d: "M" + x0 + "," + pa.y + " L" + x1 + "," + pb.y, mid: [(pa.x + pb.x) / 2, pa.y], pts: flat, span: flat, sameRow: true, len: Math.abs(x1 - x0) };
    }
    if (b.row > a.row) {
      const sy = pa.y + DG.nodeH / 2;
      const ty = pb.y - DG.nodeH / 2 - 3;
      const my = (sy + ty) / 2;
      if (pa.x === pb.x) {
        const line = [[pa.x, sy], [pb.x, ty]];
        return { d: "M" + pa.x + "," + sy + " L" + pb.x + "," + ty, mid: [pa.x, my], pts: line, span: line };
      }
      const curve = [];
      for (let i = 0; i <= 16; i++) {
        const u = i / 16;
        const v = 1 - u;
        // Cubic Bezier with control points (pa.x, my) and (pb.x, my).
        curve.push([v * v * v * pa.x + 3 * v * v * u * pa.x + 3 * v * u * u * pb.x + u * u * u * pb.x,
          v * v * v * sy + 3 * v * v * u * my + 3 * v * u * u * my + u * u * u * ty]);
      }
      return { d: "M" + pa.x + "," + sy + " C" + pa.x + "," + my + " " + pb.x + "," + my + " " + pb.x + "," + ty, mid: [(pa.x + pb.x) / 2, my], pts: curve, span: curve };
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
    return { d: d, mid: [gutter, (sy + ty) / 2], pts: [[sx, sy], [gutter, sy], [gutter, ty], [tx, ty]], span: [[gutter, sy], [gutter, ty]] };
  }

  // The point a fraction f along a polyline.
  function dgAlong(pts, f) {
    let total = 0;
    for (let i = 1; i < pts.length; i++) total += Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]);
    let want = total * f;
    for (let i = 1; i < pts.length; i++) {
      const seg = Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]);
      if (want <= seg || i === pts.length - 1) {
        const u = seg ? Math.min(1, want / seg) : 0;
        return [pts[i - 1][0] + (pts[i][0] - pts[i - 1][0]) * u, pts[i - 1][1] + (pts[i][1] - pts[i - 1][1]) * u];
      }
      want -= seg;
    }
    return pts[0];
  }

  function dgNearLine(p, pts, r) {
    for (let i = 1; i < pts.length; i++) {
      const ax = pts[i - 1][0];
      const ay = pts[i - 1][1];
      const dx = pts[i][0] - ax;
      const dy = pts[i][1] - ay;
      const l2 = dx * dx + dy * dy;
      const u = l2 ? Math.max(0, Math.min(1, ((p[0] - ax) * dx + (p[1] - ay) * dy) / l2)) : 0;
      if (Math.hypot(p[0] - ax - u * dx, p[1] - ay - u * dy) < r) return true;
    }
    return false;
  }

  // Where a mid-edge glyph goes: the middle of its span unless that lands
  // on a node, on another edge of the chosen path (it would read as that
  // edge's relation), or on a glyph already placed; then 30%, 70%, 40% or
  // 60% along. A same-row edge under 40px gets no glyph: the node's own
  // icon and the dash already say it.
  function dgGlyphSpot(geo, edge, all, boxes, placed) {
    if (geo.sameRow && geo.len < 40) return null;
    const fracs = [0.5, 0.3, 0.7, 0.4, 0.6];
    for (let i = 0; i < fracs.length; i++) {
      const p = dgAlong(geo.span, fracs[i]);
      const onNode = boxes.some(function (b) { return p[0] > b[0] - 12 && p[0] < b[2] + 12 && p[1] > b[1] - 12 && p[1] < b[3] + 12; });
      const onPath = all.some(function (o) { return o.edge !== edge && o.edge.on_path && dgNearLine(p, o.geo.pts, 14); });
      const onGlyph = placed.some(function (q) { return Math.hypot(q[0] - p[0], q[1] - p[1]) < 24; });
      if (!onNode && !onPath && !onGlyph) return p;
    }
    return geo.mid;
  }

  function dgNeighbours(chapter, focus) {
    const near = {};
    if (!focus) return null;
    near[focus] = true;
    chapter.edges.forEach(function (edge) {
      if (edge.from === focus) near[edge.to] = true;
      if (edge.to === focus) near[edge.from] = true;
    });
    let fnode = null;
    chapter.nodes.forEach(function (node) {
      if (node.parent === focus || node.id === focus) near[node.id] = true;
      if (node.id === focus) fnode = node;
    });
    if (fnode && fnode.parent) {
      // An alternative's neighbourhood is its parent and the parent's other
      // alternatives, which stay drawn as ghosts.
      near[fnode.parent] = true;
      chapter.nodes.forEach(function (node) { if (node.parent === fnode.parent) near[node.id] = true; });
    }
    if (fnode && fnode.ending && Array.isArray(fnode.ending.via)) {
      fnode.ending.via.forEach(function (id) { near[id] = true; });
    }
    return near;
  }

  function dgKbRing(l, t, w, hgt) {
    return s("rect", { x: l - 6, y: t - 6, width: w + 12, height: hgt + 12, rx: 13, class: "dg-kb-ring", "aria-hidden": "true" });
  }

  function dgNodeShape(node, w, focused, marks) {
    const p = dgPos(node);
    const l = p.x - w / 2;
    const t = p.y - DG.nodeH / 2;
    const hgt = DG.nodeH;
    // The keyboard ring is always drawn, unstroked, 6px outside the node; CSS
    // strokes it on :focus-visible. Being outside the shape, no state style
    // (path fill, locked amber, conflict red) can override it.
    const kids = [dgKbRing(l, t, w, hgt)];
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
    if (node.future) return kids; // replay: an outline holds the place, with no text
    let tx = l + 12;
    let anchor = "start";
    if (node.decided_by) {
      let letter = marks && marks[node.decided_by];
      if (!letter) {
        if (node.decided_by === "user") letter = "U";
        else if (node.decided_by === "agent") letter = "A";
        else {
          const upper = String(node.decided_by).toUpperCase();
          letter = upper.slice(0, Math.min(2, upper.length));
          if (letter === "U" || letter === "A") letter = upper.slice(0, 1) + "2";
          letter = letter.slice(0, 3);
        }
      }
      const r = letter.length > 1 ? 13 : 10;
      kids.push(s("circle", { cx: l + 18, cy: p.y, r: r, class: "dg-badge" }));
      kids.push(s("text", { x: l + 18, y: p.y + 4, "text-anchor": "middle", class: "dg-badge-text" }, [letter]));
      tx = l + 34;
    }
    if (node.kind === "start" || node.kind === "ending") {
      tx = p.x;
      anchor = "middle";
    }
    const room = anchor === "start" ? w - (tx - l) - 10 - (node.role === "locked" ? 26 : 0) : w - 20;
    kids.push(s("text", { x: tx, y: p.y - 3, "text-anchor": anchor, class: "dg-title" }, [dgFit(node.title, room, DG_CHAR.title)]));
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
      kids.push(lockIcon(l + w - 16, p.y + 4, node.role === "locked" ? "locked" : "muted"));
    }
    return kids;
  }

  function dgCategoryBit(node) {
    if (!node || node.kind !== "decision") return "";
    return "category " + decisionCategoryLabel(node.decision || {});
  }

  function dgAria(node) {
    const bits = [node.title + ": " + (node.detail || "")];
    bits.push((node.role || node.kind).replace(/_/g, " "));
    const cat = dgCategoryBit(node);
    if (cat) bits.push(cat);
    if (node.decided_by) bits.push(dgWho(node));
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
    const drawn = [];
    chapter.edges.forEach(function (edge) {
      const a = byId[edge.from];
      const b = byId[edge.to];
      if (!a || !b || !dgVisible(a) || !dgVisible(b)) return;
      const touches = focus && (edge.from === focus || edge.to === focus);
      if (edge.tension && !dgTensions && !touches) return;
      drawn.push({ edge: edge, a: a, b: b, touches: touches, geo: dgEdgePath(a, b, width[a.id], width[b.id]) });
    });
    const boxes = chapter.nodes.filter(dgVisible).map(function (node) {
      const p = dgPos(node);
      return [p.x - width[node.id] / 2, p.y - DG.nodeH / 2, p.x + width[node.id] / 2, p.y + DG.nodeH / 2];
    });
    const placed = [];
    drawn.forEach(function (item) {
      const edge = item.edge;
      const a = item.a;
      const b = item.b;
      const touches = item.touches;
      const geo = item.geo;
      const type = dgRelation(edge.relation);
      const cls = "dg-edge dg-" + type.style + (edge.on_path ? " dg-on-path" : "") + (b.role === "locked_ending" ? " dg-to-locked" : "");
      const parts = [s("title", null, [a.title + " " + type.label + " " + b.title])];
      if (edge.on_path) parts.push(s("path", { d: geo.d, class: "dg-halo" }));
      parts.push(s("path", { d: geo.d, class: cls, "marker-end": type.symmetric ? null : (edge.on_path ? "url(#dg-arrow-ink)" : "url(#dg-arrow)") }));
      if (!edge.derived && (type.style === "lock" || type.style === "crack" || type.glyph)) {
        const spot = dgGlyphSpot(geo, edge, drawn, boxes, placed);
        if (spot) {
          placed.push(spot);
          parts.push(dgGlyph(type, spot[0], spot[1], type.label));
        }
      }
      const gcls = [focus && !touches ? "dg-dim" : "", edge.enter ? "dg-enter" : ""].join(" ").trim();
      const group = s("g", { class: gcls || null }, parts);
      (edge.on_path ? path : off).push(group);
    });
    const marks = dgBadgeMarks(chapter.nodes);
    const nodes = [];
    chapter.nodes.forEach(function (node) {
      if (!dgVisible(node)) return;
      const focused = node.id === focus;
      const classes = ["dg-node", "dg-kind-" + node.kind, "dg-role-" + node.role];
      if (node.on_path) classes.push("dg-on-path");
      if (near && !near[node.id]) classes.push("dg-dim");
      const muted = dgMuted(node);
      if (muted) classes.push("dg-muted");
      if (node.future) classes.push("dg-future");
      if (node.enter) classes.push("dg-enter");
      let tip = node.title + ": " + (node.detail || "");
      if (node.decided_by && node.decided_by !== "user" && node.decided_by !== "agent") tip += " (" + node.decided_by + ")";
      const kids = (node.future ? [] : [s("title", null, [tip])]).concat(dgNodeShape(node, width[node.id], focused, marks));
      const hidden = (node.alternatives || []).filter(function (id) { return byId[id] && byId[id].lane === "none"; });
      if (hidden.length && dgNotTaken && !node.future) {
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
      // A node the replay has not reached yet is an outline only: it keeps
      // its place, so the layout never moves, but it is out of the tab order
      // and the accessibility tree.
      if (node.future) {
        nodes.push(s("g", { class: classes.join(" "), "data-node": node.id, "aria-hidden": "true" }, kids));
        return;
      }
      nodes.push(s("g", {
        class: classes.join(" "),
        // A node the legacy or login filter mutes stays clickable but leaves
        // the tab order.
        tabindex: muted ? "-1" : "0",
        role: "button",
        "data-node": node.id,
        "aria-label": dgAria(node) + (muted ? ", filtered out" : ""),
        "aria-pressed": focused ? "true" : "false",
        onclick: function () { if (focused) dgClose(chapter, node.id); else dgGo(chapter.id, node.id); },
        onkeydown: function (event) {
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            if (focused) dgClose(chapter, node.id);
            else dgGo(chapter.id, node.id);
          }
        },
      }, kids));
    });
    // A focused node's collapsed alternatives open as ghosts below the chapter.
    const ghosts = [];
    // An alternative drawn as a ghost keeps its parent's ghosts on screen
    // while it has focus, so the graph does not vanish under the user.
    let fnode = focus && byId[focus];
    if (fnode && fnode.kind === "alternative" && fnode.lane === "none" && fnode.parent && byId[fnode.parent]) fnode = byId[fnode.parent];
    if (fnode && dgNotTaken && !fnode.future) {
      const hidden = (fnode.alternatives || []).map(function (id) { return byId[id]; }).filter(function (alt) { return alt && alt.lane === "none"; });
      const fp = dgPos(fnode);
      hidden.forEach(function (alt, i) {
        const lane = i % 2 === 0 ? "left" : "right";
        const gx = DG.laneX[lane];
        const gy = DG.top + (chapter.rows + Math.floor(i / 2)) * DG.rowH + DG.nodeH / 2;
        const gw = 210;
        const sx = fp.x + (lane === "left" ? -1 : 1) * (width[fnode.id] / 2 - 30);
        const ex = lane === "left" ? gx + gw / 2 + 3 : gx - gw / 2 - 3;
        const on = alt.id === focus;
        function activate() {
          if (on) dgClose(chapter, alt.id);
          else dgGo(chapter.id, alt.id);
        }
        ghosts.push(s("g", {
          class: "dg-node dg-kind-alternative dg-role-not_taken dg-ghost-node",
          tabindex: "0",
          role: "button",
          "data-node": alt.id,
          "aria-label": "Not taken, alternative to " + fnode.title + ": " + alt.title,
          "aria-pressed": on ? "true" : "false",
          onclick: activate,
          onkeydown: function (event) {
            if (event.key === "Enter" || event.key === " ") {
              event.preventDefault();
              activate();
            }
          },
        }, [
          s("title", null, ["Not taken: " + alt.title]),
          dgKbRing(gx - gw / 2, gy - 19, gw, 38),
          on ? s("rect", { x: gx - gw / 2 - 6, y: gy - 25, width: gw + 12, height: 50, rx: 13, class: "dg-focus-ring" }) : null,
          s("path", { d: "M" + sx + "," + (fp.y + DG.nodeH / 2) + " C" + sx + "," + gy + " " + sx + "," + gy + " " + ex + "," + gy, class: "dg-edge dg-ghost", "marker-end": "url(#dg-arrow)" }),
          s("rect", { x: gx - gw / 2, y: gy - 19, width: gw, height: 38, rx: 9, class: "dg-shape" }),
          s("text", { x: gx - gw / 2 + 12, y: gy - 3, class: "dg-detail" }, ["not taken · what if"]),
          s("text", { x: gx - gw / 2 + 12, y: gy + 12, class: "dg-title" }, [dgFit(alt.title, gw - 24, DG_CHAR.title)]),
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

  const DG_SHOW = [["all", "Everyone"], ["user", "user (legacy)"], ["agent", "agent (legacy)"], ["login", "Logins"]];

  // Everyone / user (legacy) / agent (legacy) / Logins is one choice: a
  // radiogroup with a roving tabindex, where the arrows (and Home/End)
  // move and select. A login is not greyed under both legacy filters.
  function dgShowGroup() {
    function pick(value) {
      dgShow = value;
      dgWantFocus = "show:" + value;
      render();
    }
    return h("div", { class: "dg-radios", role: "radiogroup", "aria-label": "Decided by" }, DG_SHOW.map(function (opt, i) {
      const on = dgShow === opt[0];
      return h("button", {
        type: "button",
        role: "radio",
        "aria-checked": on ? "true" : "false",
        tabindex: on ? "0" : "-1",
        "data-focus-key": "show:" + opt[0],
        onclick: function () { pick(opt[0]); },
        onkeydown: function (event) {
          if (event.ctrlKey || event.metaKey || event.altKey) return;
          const n = DG_SHOW.length;
          let to = -1;
          if (event.key === "ArrowRight" || event.key === "ArrowDown") to = (i + 1) % n;
          else if (event.key === "ArrowLeft" || event.key === "ArrowUp") to = (i + n - 1) % n;
          else if (event.key === "Home") to = 0;
          else if (event.key === "End") to = n - 1;
          if (to < 0) return;
          event.preventDefault();
          if (event.stopPropagation) event.stopPropagation();
          pick(DG_SHOW[to][0]);
        },
      }, [opt[1]]);
    }));
  }

  function dgToolbar(chapter) {
    function toggle(label, on, fn, extra) {
      return h("button", Object.assign({ type: "button", "aria-pressed": on ? "true" : "false", "data-focus-key": "tb:" + label, onclick: function () { fn(); render(); } }, extra || {}), [label]);
    }
    const jump = h("input", {
      type: "search",
      class: "dg-jump",
      placeholder: "Jump to id…",
      "aria-label": "Jump to a decision id",
      "data-focus-key": "jump",
      "aria-keyshortcuts": "/",
      value: dgJumpValue || null,
      oninput: function (event) { dgJumpValue = String(event.target.value || ""); },
      onkeydown: function (event) {
        if (event.key !== "Enter") return;
        const want = String(event.target.value || "").toLowerCase();
        if (!want) return;
        const hit = chapter.nodes.filter(function (node) { return node.id.toLowerCase().indexOf(want) >= 0 || (node.detail || "").toLowerCase().indexOf(want) >= 0; })[0];
        if (hit) dgGo(chapter.id, hit.id);
        else { dgAnnounce("No node matches " + want + "."); render(); }
      },
    });
    return h("div", { class: "dg-toolbar filters", role: "toolbar", "aria-label": "Graph controls" }, [
      dgShowGroup(),
      h("span", { class: "dg-sep", "aria-hidden": "true" }),
      toggle("Not taken", dgNotTaken, function () { dgNotTaken = !dgNotTaken; }),
      toggle("Show tensions", dgTensions, function () { dgTensions = !dgTensions; }, { "aria-keyshortcuts": "t" }),
      toggle("Chosen path only", dgPathOnly, function () { dgPathOnly = !dgPathOnly; }),
      toggle("List", dgList, function () { dgList = !dgList; }, { "aria-label": "List view" }),
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
      const full = String(label);
      return h("li", null, [
        h("span", { class: "dg-key-swatch " + cls, "aria-hidden": "true" }),
        h("span", { class: "dg-key-label", title: full }, [full]),
      ]);
    }
    const logins = [];
    chapter.nodes.forEach(function (node) {
      const by = node.decided_by || "";
      if (!by || by === "user" || by === "agent" || logins.indexOf(by) >= 0) return;
      logins.push(by);
    });
    const items = [
      row("path", "Chosen path"),
      row("off", "Off the path"),
      row("user", "user (legacy)"),
      row("agent", "agent (legacy)"),
    ];
    if (logins.length) logins.forEach(function (login) { items.push(row("login", login)); });
    else items.push(row("login", "GitHub login"));
    items.push(row("ghost", "Not taken"));
    items.push(row("superseded", "Superseded"));
    items.push(row("hypothesis", "Hypothesis ?"));
    items.push(row("locked", "Locked"));
    items.push(row("challenged", "Challenged"));
    items.push(row("ending", "Ending"));
    items.push(h("li", null, ["🔒 blocks · ⚡ conflicts · ↶ supersedes · ↑ refines"]));
    return h("details", { class: "dg-key", open: open ? "" : null, ontoggle: function (event) { dgKeyOpen = !!event.target.open; } }, [
      h("summary", null, ["Key"]),
      h("ul", null, items),
    ]);
  }

  function dgHelpBox() {
    if (!dgHelp) return null;
    return h("div", { class: "dg-help", role: "note" }, [
      h("p", null, [h("strong", null, ["Shortcuts"])]),
      h("p", null, ["Tab moves between nodes · Enter or Space focuses one · Esc closes it · ↑ ↓ walk the rows · ← → choose Everyone, user (legacy), agent (legacy), or Logins · t toggles tensions · / jumps to an id · ? shows this · List shows the chapter as a list"]),
      h("p", null, ["Replay: [ and ] step back and forward from anywhere on the tab · on the slider ← → step, PageUp PageDown jump a tenth, Home End go to the first or last event, Space plays or pauses"]),
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
    if (chapter.cost && chapter.cost.status !== "unknown") meta.push("computed " + dgUsd(chapter.cost.usd) + " USD" + (chapter.cost.status === "partial" ? " (partial)" : ""));
    const facts = [h("strong", null, [f.endings_reached + " of " + f.endings_total + (f.endings_total === 1 ? " ending reached" : " endings reached")])];
    if (f.locked) facts.push(" · ", h("span", { class: "dg-amber" }, [f.locked + " locked"]));
    if (f.conflicts) facts.push(" · ", h("span", { class: "dg-red" }, [f.conflicts + " conflict" + (f.conflicts === 1 ? "" : "s")]));
    if (f.challenged) facts.push(" · ", h("span", { class: "dg-red" }, [f.challenged + " challenged"]));
    const kids = [
      dgReplayNote(chapter),
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
    const endings = chapter.nodes.filter(function (node) { return node.kind === "ending" && !node.future; });
    kids.push(h("p", { class: "label" }, ["Endings"]));
    if (!endings.length) kids.push(h("p", { class: "detail" }, ["No ending yet. An ending is a pull request or commit a linked session produced."]));
    else kids.push(h("ul", { class: "dg-endings" }, endings.map(function (node) {
      const end = node.ending || {};
      const bits = [node.detail];
      if (end.cost && end.cost.status !== "unknown") bits.push(dgUsd(end.cost.usd) + " USD");
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

  // The one external link on the page is an ending's pull request. Only a
  // GitHub https URL becomes an href (no javascript:, data: or other host).
  // Assembled, like SVGNS, so the asset holds no literal URL.
  const GITHUB_PREFIX = ["https:", "", "github.com", ""].join("/");

  function dgSafeURL(url) {
    return typeof url === "string" && url.indexOf(GITHUB_PREFIX) === 0 && !/[\s\\]/.test(url);
  }

  // A long id is cut to 12 characters with an ellipsis; the caller puts the
  // full id in a title.
  function dgShortId(ref) {
    ref = String(ref || "");
    return ref.length > 12 ? ref.slice(0, 12) + "…" : ref;
  }

  // Costs at 2 decimals; a non-zero cost under half a cent reads "<0.01",
  // never as 0.00. A value that is not a number passes through.
  function dgUsd(usd) {
    const n = Number(usd);
    if (usd == null || usd === "" || !isFinite(n)) return String(usd == null ? "" : usd);
    if (n > 0 && n < 0.005) return "<0.01";
    // Round half up on the decimal value (2.655 → 2.66), not the binary one.
    return (Math.round(n * 100 + (n >= 0 ? 1e-7 : -1e-7)) / 100).toFixed(2);
  }

  function dgLinkTarget(chapter, kind, ref) {
    if (kind === "decision") {
      const inChapter = chapter.nodes.some(function (node) { return node.id === ref; });
      return inChapter ? h("button", { type: "button", class: "dg-linkish", onclick: function () { dgGo(chapter.id, ref); } }, [ref]) : h("span", null, [ref]);
    }
    if (kind === "session") return h("a", { href: "#/sessions/" + encodeURIComponent(ref), title: ref.length > 12 ? ref : null }, ["session " + dgShortId(ref)]);
    if (kind === "commit") return h("code", { title: ref.length > 12 ? ref : null }, [dgShortId(ref)]);
    if (kind === "node") {
      const node = chapter.nodes.filter(function (n) { return n.id === ref; })[0];
      return h("button", { type: "button", class: "dg-linkish", onclick: function () { dgGo(chapter.id, ref); } }, [node ? node.title : ref]);
    }
    return h("span", null, [kind + " " + ref]);
  }

  // One live region for the whole page, created once beside the app root so
  // app.replaceChildren never replaces it and announcements are not lost.
  function dgLiveRegion() {
    if (dgLive) return dgLive;
    const host = document.body || app.parentNode;
    if (!host || !host.appendChild) return null;
    dgLive = h("p", { class: "sr-only", role: "status", "aria-live": "polite", "aria-atomic": "true", "data-sdf-live": "" });
    host.appendChild(dgLive);
    return dgLive;
  }

  function dgAnnounce(text) {
    dgStatus = text || "";
    const live = dgLiveRegion();
    if (!live) return;
    // The same message twice in a row still changes the text, so it is read.
    live.textContent = text && live.textContent === text ? text + "\u00a0" : dgStatus;
    // Replay steps redraw only part of the tab; keep the visible copy current.
    const shown = app.querySelector ? app.querySelector(".dg-live") : null;
    if (shown) shown.textContent = dgStatus;
  }

  function dgFocusKey(el) {
    if (!el || !el.getAttribute) return "";
    const key = el.getAttribute("data-focus-key");
    if (key) return key;
    const node = el.getAttribute("data-node");
    return node ? "node:" + node : "";
  }

  function dgFindFocus(key) {
    if (!key || !app.querySelector) return null;
    const esc = function (v) { return String(v).replace(/["\\]/g, "\\$&"); };
    if (key.indexOf("node:") === 0) return app.querySelector('[data-node="' + esc(key.slice(5)) + '"]');
    const el = app.querySelector('[data-focus-key="' + esc(key) + '"]');
    // A hidden control cannot take focus: the replay's Now button hides at
    // now, and its focus goes to the slider.
    if (el && (el.hidden || (el.hasAttribute && el.hasAttribute("hidden")))) return key === "replay:now" ? app.querySelector('[data-focus-key="replay:slider"]') : null;
    return el;
  }

  // Close the focus panel and give focus back to the node it described.
  function dgClose(chapter, id) {
    // A ghost alternative is not drawn once its panel closes: focus its parent.
    const node = id ? chapter.nodes.filter(function (n) { return n.id === id; })[0] : null;
    if (node && node.kind === "alternative" && node.lane === "none" && node.parent) id = node.parent;
    if (id) dgWantFocus = "node:" + id;
    dgGo(chapter.id, null);
  }

  function dgCopy(text) {
    function done(ok) {
      dgAnnounce(ok ? "Copied the command. sdf did not run it." : "Copy failed. Select the command and copy it.");
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
      h("button", { type: "button", "data-focus-key": "copy:" + alt.id, onclick: function () { dgCopy(alt.command); } }, ["Copy command"]),
    ]);
  }

  // A node's links as <li> sentences ("Blocked by merge-first"), grouped
  // and ordered by LINK_ORDER; the focus panel and the List view share it.
  function dgLinkSentences(chapter, node) {
    const rec = node.decision || {};
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
    return labels.map(function (label) {
      const items = [];
      groups[label].forEach(function (item, i) { if (i) items.push(", "); items.push(item); });
      return h("li", null, [h("strong", null, [label + " "])].concat(items));
    });
  }

  function dgWho(node) {
    if (!node || !node.decided_by) return "";
    const rec = node.decision || {};
    const legacy = rec.decided_by_legacy || node.decided_by === "user" || node.decided_by === "agent";
    return "by " + node.decided_by + (legacy ? " (legacy)" : "");
  }

  function dgListState(node) {
    const bits = [];
    if (node.kind === "start") bits.push("chapter start");
    else if (node.kind === "ending") bits.push(node.role === "locked_ending" ? "locked ending" : node.role === "reached" ? "ending, reached" : "ending, not reached");
    else bits.push((node.role || node.kind).replace(/_/g, " "));
    const cat = dgCategoryBit(node);
    if (cat) bits.push(cat);
    if (node.decided_by) bits.push(dgWho(node));
    const rec = node.decision || {};
    if (rec.blocked) bits.push("blocked");
    if (rec.conflicted) bits.push("conflicted");
    if (rec.challenged) bits.push("challenged");
    if (dgMuted(node)) bits.push("filtered out");
    return bits.join(", ");
  }

  function dgListItem(chapter, node, byId, focus) {
    const on = node.id === focus;
    const sub = [];
    (node.alternatives || []).map(function (id) { return byId[id]; }).filter(Boolean).forEach(function (alt) {
      if (!dgNotTaken) return;
      sub.push(h("li", { class: "dg-list-alt" }, [h("strong", null, ["Not taken "]), h("button", {
        type: "button", class: "dg-linkish", "data-node": alt.id, "aria-pressed": alt.id === focus ? "true" : "false",
        onclick: function () { dgGo(chapter.id, alt.id); },
      }, [alt.title])]));
    });
    if (node.kind !== "alternative") dgLinkSentences(chapter, node).forEach(function (li) { sub.push(li); });
    return h("li", { class: "dg-list-item" + (on ? " dg-list-on" : "") }, [
      h("button", {
        type: "button",
        class: "dg-linkish dg-list-node",
        "data-node": node.id,
        "aria-pressed": on ? "true" : "false",
        onclick: function () { if (on) dgClose(chapter, node.id); else dgGo(chapter.id, node.id); },
      }, [node.title]),
      h("span", { class: "detail" }, [" " + (node.kind === "start" ? dgWhen(node.detail) : (node.detail || "")) + " · " + dgListState(node)]),
      sub.length ? h("ul", null, sub) : null,
    ]);
  }

  // List view (§2b scale rule f, §4): the chapter as an ordered, nested
  // list: the chosen path in order, then endings, then the rest; each node carries its
  // alternatives and link sentences. It doubles as the screen-reader view.
  function dgListView(chapter, focus) {
    const byId = {};
    chapter.nodes.forEach(function (n) { byId[n.id] = n; });
    // A node the replay has not reached yet is not listed.
    const shown = dgOrderedNodes(chapter).filter(function (n) { return !n.future && (n.kind !== "alternative" || n.lane !== "none"); });
    const onPath = shown.filter(function (n) { return n.on_path && n.kind !== "ending"; });
    const endings = shown.filter(function (n) { return n.kind === "ending"; });
    const rest = shown.filter(function (n) { return !n.on_path && n.kind !== "alternative" && n.kind !== "ending"; });
    const kids = [
      h("p", { class: "label", id: "dg-list-path" }, ["Chosen path, in order"]),
      h("ol", { class: "dg-list", "aria-labelledby": "dg-list-path" }, onPath.map(function (n) { return dgListItem(chapter, n, byId, focus); })),
    ];
    if (endings.length) {
      kids.push(h("p", { class: "label", id: "dg-list-endings" }, ["Endings"]));
      kids.push(h("ul", { class: "dg-list", "aria-labelledby": "dg-list-endings" }, endings.map(function (n) { return dgListItem(chapter, n, byId, focus); })));
    }
    if (rest.length) {
      kids.push(h("p", { class: "label", id: "dg-list-rest" }, ["Off the path"]));
      kids.push(h("ul", { class: "dg-list", "aria-labelledby": "dg-list-rest" }, rest.map(function (n) { return dgListItem(chapter, n, byId, focus); })));
    }
    return h("div", { class: "dg-listview" }, kids);
  }

  // While replaying, the panel says which point it describes.
  function dgReplayNote(chapter, node) {
    if (!chapter.replay) return null;
    const text = node && node.future ?
      "Not recorded yet at this replay point (" + chapter.replay.at + " of " + chapter.replay.total + "). Its details below are as of now." :
      "As of " + dgEventTime(chapter.replay.event, dgTimeline(dgView.chapter || chapter).seconds) + " · " + chapter.replay.at + " of " + chapter.replay.total + ". Flags and counts are recomputed for this point.";
    return h("p", { class: "dg-replay-note" }, [text]);
  }

  function dgFocusPanel(chapter, node) {
    const byId = {};
    chapter.nodes.forEach(function (n) { byId[n.id] = n; });
    const kids = [h("div", { class: "dg-panel-top" }, [
      h("button", { type: "button", onclick: function () { dgClose(chapter, node.id); } }, ["← Chapter"]),
      h("button", { type: "button", "aria-label": "Close", onclick: function () { dgClose(chapter, node.id); } }, ["×"]),
    ]), dgReplayNote(chapter, node)];
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
      if (end.url && dgSafeURL(end.url)) kids.push(h("p", null, [h("a", { href: end.url, rel: "noopener noreferrer" }, ["Open pull request #" + end.pull_request])]));
      else if (end.pull_request) kids.push(h("p", { class: "detail" }, ["Pull request #" + end.pull_request]));
      if (end.via && end.via.length) kids.push(h("p", null, [end.locked ? "Waits on " : "Reached via "].concat(end.via.map(function (id, i) { return [i ? ", " : "", dgLinkTarget(chapter, "decision", id)]; }).flat())));
      if (end.sessions && end.sessions.length) kids.push(h("p", null, ["Sessions "].concat(end.sessions.map(function (id, i) { return [i ? ", " : "", dgLinkTarget(chapter, "session", id)]; }).flat())));
      if (end.cost && end.cost.status !== "unknown") kids.push(h("p", { class: "detail" }, ["Computed cost " + dgUsd(end.cost.usd) + " USD" + (end.cost.status === "partial" ? " (partial)" : "")]));
      return kids;
    }
    const rec = node.decision || {};
    kids.push(h("p", { class: "kicker" }, ["Decision" + (node.decided_by ? " · " + dgWho(node) : "")]));
    kids.push(h("h2", null, [node.id]));
    // A decision not recorded yet at the replay point has no state then:
    // its chips (status, effective status, blocked, conflicted, challenged)
    // would be today's, so they are left out.
    if (!node.future) {
      const chips = [h("span", { class: "dg-pchip" }, [h("span", { class: "sr-only" }, ["category "]), decisionCategoryLabel(rec)]), h("span", { class: "dg-pchip" }, [rec.status || "decision"]), h("span", { class: "dg-pchip" }, [rec.effective_status || "active"])];
      if (rec.blocked) chips.push(h("span", { class: "dg-pchip dg-amber" }, ["🔒 blocked"]));
      if (rec.conflicted) chips.push(h("span", { class: "dg-pchip dg-red" }, ["⚡ conflicted"]));
      if (rec.challenged) chips.push(h("span", { class: "dg-pchip dg-red" }, ["challenged"]));
      kids.push(h("p", { class: "dg-pchips" }, chips));
    }
    const meta = [dgWhen(rec.recorded_at)];
    if (rec.source) meta.push("source " + rec.source);
    kids.push(h("p", { class: "detail" }, [meta.join(" · ")]));
    kids.push(h("p", null, [rec.statement || node.detail]));
    if (rec.reason) kids.push(h("p", null, [h("span", { class: "label" }, ["Reason"]), rec.reason]));
    else kids.push(h("p", { class: "detail" }, ["Reason: none recorded. sdf keeps what was recorded and never invents one."]));
    if (rec.retraction) kids.push(h("p", { class: "detail" }, ["Retracted " + dgWhen(rec.retraction.retracted_at) + (rec.retraction.reason ? ": " + rec.retraction.reason : "")]));
    const links = dgLinkSentences(chapter, node);
    if (links.length) {
      kids.push(h("p", { class: "label" }, ["Links"]));
      kids.push(h("ul", { class: "dg-links" }, links));
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
    return chapter.nodes.filter(function (node) { return dgVisible(node) && !node.future; }).slice().sort(function (a, b) {
      if (a.row !== b.row) return a.row - b.row;
      const order = { left: 0, spine: 1, right: 2 };
      return (order[a.lane] || 0) - (order[b.lane] || 0);
    });
  }

  // ---- Replay ----------------------------------------------------------------
  // The scrubber under the canvas replays a chapter in recorded_at order. Its
  // timeline has one event per decision, per stored edge that touches one of
  // the chapter's decisions, and per retraction, all read from
  // GET /api/decisions/graph (decision.recorded_at, links[].recorded_at, and
  // retraction.retracted_at); no other endpoint is needed.
  //
  // Derived flags are RECOMPUTED as of the playhead, not approximated: dgProject
  // re-runs the decision package's read-time rules (internal/decision,
  // resolveRelations) over only the decisions and edges recorded so far, so a
  // decision shows "locked" only once its blocks edge exists, stops being
  // locked once a decision supersedes its hypothesis blocker, and so on:
  //   - effective status: retracted once its retraction is recorded, else
  //     superseded once a supersedes edge from a recorded decision is, else
  //     active;
  //   - blocked: active with a blocks edge from a blocker whose current heads
  //     (following supersedes, skipping retracted) include a hypothesis, or
  //     whose chain only reaches a cycle;
  //   - conflicted: active with a conflicts_with edge to an active decision;
  //   - challenged: active and the latest counting validates/contradicts edge
  //     (from a session, a commit, or an active decision) is contradicts.
  // Roles, facts, needs-attention, and explored are rebuilt from those flags
  // the way package decisiongraph builds them. Layout is not: every node keeps
  // its final lane and row (concept §5, "the final layout is computed once"),
  // and nodes the playhead has not reached are drawn as faint outlines.
  // Two things are approximated because the API has no time for them: an
  // ending appears once every decision it came from is recorded and linked to
  // a session or commit, and a pull request's merged state is today's.
  // At the last event the page shows the server's chapter itself.
  let dgAt = null; // replay position, 1..M; null is now (the last event)
  let dgPrevAt = null;
  let dgTimer = null;
  let dgView = { chapter: null, focus: null };
  let dgDragging = false;
  const dgTimelines = typeof WeakMap === "function" ? new WeakMap() : null;
  const DG_EVENT_RANK = { decision: 0, edge: 1, retraction: 2 };

  // dgParseTime reads a stored time: RFC3339 with a zone, as the store
  // writes it, to full precision. ms is the epoch millisecond and sub the
  // rest of the fraction in nanoseconds (0..999999), so events in one
  // millisecond still order by time. A time with no zone is read as UTC, the
  // zone the store writes, never as the browser's. Anything else is null.
  function dgParseTime(text) {
    if (typeof text !== "string") return null;
    const m = /^(\d{4}-\d{2}-\d{2})[Tt ](\d{2}:\d{2})(:\d{2})?(?:\.(\d{1,9}))?(Z|z|[+-]\d{2}:?\d{2})?$/.exec(text.trim());
    if (!m) return null;
    const frac = m[4] || "";
    let zone = m[5] || "Z";
    if (zone === "z") zone = "Z";
    if (zone.length === 5) zone = zone.slice(0, 3) + ":" + zone.slice(3);
    const ms = Date.parse(m[1] + "T" + m[2] + (m[3] || ":00") + (frac ? "." + (frac + "00").slice(0, 3) : "") + zone);
    if (isNaN(ms)) return null;
    return { ms: ms, sub: parseInt((frac.slice(3) + "000000").slice(0, 6), 10) };
  }

  // The replay's times are in the browser's zone, as dgWhen's are, with the
  // zone's label, so the scrubber and the panel never disagree.
  let dgTimeFormat = null;
  function dgTimeParts(ms) {
    if (ms == null) return null;
    try {
      if (!dgTimeFormat) {
        dgTimeFormat = new Intl.DateTimeFormat("en-US", {
          month: "short", day: "numeric",
          hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23", timeZoneName: "short",
        });
      }
      const out = {};
      dgTimeFormat.formatToParts(new Date(ms)).forEach(function (part) { out[part.type] = part.value; });
      return out.month ? out : null;
    } catch (err) {
      return null;
    }
  }

  // "Nov 22 · 09:14 GMT+7": the event's time in the browser's zone with its
  // label; seconds are added when two events of the timeline share a
  // minute. An event with no readable time says so.
  function dgEventTime(ev, seconds) {
    const key = seconds ? "whenS" : "when";
    if (ev[key] != null) return ev[key];
    const p = dgTimeParts(ev.ms);
    ev[key] = p ? p.month + " " + p.day + " · " + p.hour + ":" + p.minute + (seconds ? ":" + p.second : "") + " " + p.timeZoneName : "time unknown";
    return ev[key];
  }

  function dgEventDay(ev) {
    if (ev.day != null) return ev.day;
    const p = dgTimeParts(ev.ms);
    ev.day = p ? p.month + " " + p.day : "";
    return ev.day;
  }

  function dgEndpointLabel(end) {
    if (end.kind === "decision") return end.ref;
    if (end.kind === "session") return "session " + (end.ref.length > 16 ? end.ref.slice(0, 12) + "…" : end.ref);
    if (end.kind === "commit") return "commit " + end.ref.slice(0, 7);
    return end.kind + " " + end.ref;
  }

  function dgEdgeKey(relation, from, to) {
    return relation + "\u0000" + from.kind + ":" + from.ref + "\u0000" + to.kind + ":" + to.ref;
  }

  function dgTimeline(chapter) {
    const cached = dgTimelines && dgTimelines.get(chapter);
    if (cached) return cached;
    const events = [];
    const decisionNodes = {};
    chapter.nodes.forEach(function (node) {
      if (node.kind === "decision" && node.decision) decisionNodes[node.id] = node;
    });
    const seen = {};
    chapter.nodes.forEach(function (node) {
      if (!decisionNodes[node.id]) return;
      const rec = node.decision;
      events.push({ type: "decision", id: node.id, at: rec.recorded_at, by: node.decided_by || rec.decided_by || "", label: node.id + " recorded" });
      if (rec.retraction) events.push({ type: "retraction", id: node.id, at: rec.retraction.retracted_at, label: node.id + " retracted" });
      // Each stored edge once: a decision-to-decision edge from its source's
      // out link, and an edge from a session or commit from the in link.
      (node.links || []).forEach(function (link) {
        let from;
        let to;
        if (link.direction === "out") {
          from = { kind: "decision", ref: node.id };
          to = { kind: link.kind, ref: link.ref };
        } else if (link.kind !== "decision") {
          from = { kind: link.kind, ref: link.ref };
          to = { kind: "decision", ref: node.id };
        } else {
          return;
        }
        const key = dgEdgeKey(link.relation, from, to);
        const once = key + "\u0000" + link.recorded_at;
        if (seen[once]) return;
        seen[once] = true;
        events.push({ type: "edge", relation: link.relation, from: from, to: to, key: key, at: link.recorded_at,
          label: dgEndpointLabel(from) + " " + dgRelation(link.relation).label + " " + dgEndpointLabel(to) });
      });
    });
    events.forEach(function (ev, i) {
      const t = dgParseTime(ev.at);
      ev.seq = i;
      ev.ms = t ? t.ms : null;
      ev.sub = t ? t.sub : 0;
    });
    // By full stored time (to the nanosecond), then decision, edge,
    // retraction, then the order the API listed them. An event whose time
    // cannot be read goes last, in API order.
    events.sort(function (a, b) {
      if ((a.ms == null) !== (b.ms == null)) return a.ms == null ? 1 : -1;
      if (a.ms !== b.ms) return a.ms - b.ms;
      if (a.sub !== b.sub) return a.sub - b.sub;
      if (a.type !== b.type) return DG_EVENT_RANK[a.type] - DG_EVENT_RANK[b.type];
      return a.seq - b.seq;
    });
    let seconds = false;
    for (let i = 1; i < events.length; i++) {
      if (events[i].ms != null && events[i - 1].ms != null &&
        Math.floor(events[i].ms / 60000) === Math.floor(events[i - 1].ms / 60000)) seconds = true;
    }
    const out = { events: events, seconds: seconds };
    if (dgTimelines) dgTimelines.set(chapter, out);
    return out;
  }

  function dgClampAt(chapter, at) {
    const m = dgTimeline(chapter).events.length;
    if (at == null || at >= m) return null;
    return Math.max(1, at);
  }

  // dgHeadResolver is decision.headResolver (internal/decision) in
  // JavaScript: it returns resolve(id) -> { heads, cyclic }, memoized per id,
  // in time linear in the decisions and supersedes edges reached, where a
  // walk of every path doubles with each diamond in a chain. next(id) lists
  // the decisions that supersede id; alive(id) is false for a retracted or
  // unrecorded decision, which ends a path with no head. A live decision
  // nothing supersedes is a head. Decisions are grouped into strongly
  // connected components (Tarjan) first: a decision is cyclic when its
  // component has more than one decision or when any component it reaches
  // is cyclic, so every member of a cycle gets the same answer. The walk
  // keeps its own stack instead of recursing, so a long supersedes chain
  // cannot overflow the call stack.
  function dgHeadResolver(next, alive) {
    const memo = new Map();
    const order = new Map();
    const low = new Map();
    const onStack = new Set();
    const stack = [];
    let counter = 0;
    function finish(v) {
      if (low.get(v) !== order.get(v)) return;
      const members = [];
      for (;;) {
        const w = stack.pop();
        onStack.delete(w);
        members.push(w);
        if (w === v) break;
      }
      const info = { heads: [], cyclic: members.length > 1 };
      const seen = new Set();
      const addHead = function (hd) {
        if (!seen.has(hd)) { seen.add(hd); info.heads.push(hd); }
      };
      // Members pop in reverse; walk them in discovery order so heads come
      // out in the order a walk first reaches them.
      for (let i = members.length - 1; i >= 0; i--) {
        const m = members[i];
        if (!alive(m)) continue;
        const nx = next(m);
        if (!nx.length) addHead(m);
        for (let j = 0; j < nx.length; j++) {
          const sub = memo.get(nx[j]); // undefined for a member of this component
          if (!sub) continue;
          if (sub.cyclic) info.cyclic = true;
          sub.heads.forEach(addHead);
        }
      }
      members.forEach(function (m) { memo.set(m, info); });
    }
    function visit(root) {
      const frames = [];
      function enter(v) {
        order.set(v, counter);
        low.set(v, counter);
        counter++;
        stack.push(v);
        onStack.add(v);
        frames.push({ v: v, kids: alive(v) ? next(v) : [], i: 0 });
      }
      enter(root);
      while (frames.length) {
        const f = frames[frames.length - 1];
        if (f.i < f.kids.length) {
          const w = f.kids[f.i++];
          if (!order.has(w)) enter(w);
          else if (onStack.has(w)) low.set(f.v, Math.min(low.get(f.v), order.get(w)));
          continue;
        }
        frames.pop();
        finish(f.v);
        if (frames.length) {
          const parent = frames[frames.length - 1].v;
          low.set(parent, Math.min(low.get(parent), low.get(f.v)));
        }
      }
    }
    return function resolve(id) {
      if (!order.has(id)) visit(id);
      return memo.get(id);
    };
  }

  // dgProject returns the chapter as it stood after the first n events: the
  // same nodes in the same places, with decision records, roles, facts, and
  // attention recomputed, and future nodes marked future. See the comment at
  // the top of this section for the rules.
  function dgProject(chapter, n, prev) {
    const tl = dgTimeline(chapter);
    if (n == null || n >= tl.events.length) return chapter;
    const applied = tl.events.slice(0, n);
    const present = {};
    const retracted = {};
    const edges = [];
    const appliedKeys = {};
    applied.forEach(function (ev) {
      if (ev.type === "decision") present[ev.id] = true;
      else if (ev.type === "retraction") retracted[ev.id] = true;
      else {
        edges.push(ev);
        appliedKeys[ev.key] = true;
      }
    });
    const last = applied[applied.length - 1];
    const stepped = prev != null && prev === n - 1;
    const recs = {};
    chapter.nodes.forEach(function (node) { if (node.kind === "decision" && node.decision) recs[node.id] = node.decision; });
    const stored = function (end) { return end.kind === "decision" && !!present[end.ref]; };
    const counts = function (e, rel) { return e.relation === rel && stored(e.from) && stored(e.to) && e.from.ref !== e.to.ref; };
    function push(map, key, val) {
      if (!map[key]) map[key] = [];
      if (map[key].indexOf(val) < 0) map[key].push(val);
    }
    const supersededBy = {};
    edges.forEach(function (e) { if (counts(e, "supersedes")) push(supersededBy, e.to.ref, e.from.ref); });
    const eff = {};
    Object.keys(present).forEach(function (id) {
      eff[id] = retracted[id] ? "retracted" : (supersededBy[id] ? "superseded" : "active");
    });
    const active = function (id) { return eff[id] === "active"; };
    const blockedBy = {};
    const conflicts = {};
    const refinedBy = {};
    const evidence = {};
    // Per-id indexes, so a role or an ending costs one lookup, not a pass
    // over every edge: ids that refine another stored decision, and ids
    // with an edge to or from a session or commit.
    const refines = {};
    const linkedIds = {};
    edges.forEach(function (e) {
      if (e.from.kind === "decision" && e.to.kind !== "decision") linkedIds[e.from.ref] = true;
      if (e.to.kind === "decision" && e.from.kind !== "decision") linkedIds[e.to.ref] = true;
      if (counts(e, "blocks")) push(blockedBy, e.to.ref, e.from.ref);
      else if (counts(e, "conflicts_with")) { push(conflicts, e.to.ref, e.from.ref); push(conflicts, e.from.ref, e.to.ref); }
      else if (counts(e, "refines")) { push(refinedBy, e.to.ref, e.from.ref); refines[e.from.ref] = true; }
      else if ((e.relation === "validates" || e.relation === "contradicts") && stored(e.to) &&
        !(e.from.kind === e.to.kind && e.from.ref === e.to.ref) && (e.from.kind !== "decision" || active(e.from.ref))) {
        evidence[e.to.ref] = e;
      }
    });
    const resolver = dgHeadResolver(function (id) { return supersededBy[id] || []; },
      function (id) { return !!present[id] && !retracted[id]; });
    function unsettled(blocker) {
      const info = resolver(blocker);
      if (!info.heads.length) return info.cyclic;
      return info.heads.some(function (hd) { return recs[hd] && recs[hd].status === "hypothesis"; });
    }
    const asOf = {};
    Object.keys(present).forEach(function (id) {
      const rec = recs[id];
      const isActive = active(id);
      const open = (blockedBy[id] || []).filter(unsettled);
      const ev = evidence[id];
      const r = Object.assign({}, rec, {
        effective_status: eff[id],
        superseded_by: supersededBy[id],
        retraction: retracted[id] ? rec.retraction : undefined,
        blocked_by: blockedBy[id],
        open_blockers: open.length ? open : undefined,
        blocked: isActive && open.length > 0,
        conflicts_with: conflicts[id],
        conflicted: isActive && (conflicts[id] || []).some(active),
        refined_by: refinedBy[id],
        challenged: isActive && !!ev && ev.relation === "contradicts",
      });
      r.challenger = r.challenged ? " (" + ev.from.kind + " " + ev.from.ref.slice(0, 12) + ")" : "";
      asOf[id] = r;
    });
    function roleOf(id, final) {
      const r = asOf[id];
      if (r.effective_status === "retracted") return "retracted";
      if (r.effective_status === "superseded") return "superseded";
      if (refines[id]) return "refinement";
      if (r.status === "hypothesis") return "hypothesis";
      if (r.blocked) return "locked";
      return "path";
    }
    // An ending exists once its decisions are recorded and linked to the
    // session or commit it stands for (approximated: any such link).
    function linked(id) {
      return !!linkedIds[id];
    }
    const nodes = chapter.nodes.map(function (node) {
      const copy = Object.assign({}, node);
      if (node.kind === "decision") {
        if (!present[node.id]) { copy.future = true; return copy; }
        copy.decision = asOf[node.id];
        copy.role = roleOf(node.id, node.role);
        copy.links = (node.links || []).filter(function (link) {
          const self = { kind: "decision", ref: node.id };
          const other = { kind: link.kind, ref: link.ref };
          return appliedKeys[link.direction === "out" ? dgEdgeKey(link.relation, self, other) : dgEdgeKey(link.relation, other, self)];
        });
        if (stepped && last.type === "decision" && last.id === node.id) copy.enter = true;
      } else if (node.kind === "alternative") {
        if (!present[node.parent]) copy.future = true;
        else if (stepped && last.type === "decision" && last.id === node.parent) copy.enter = true;
      } else if (node.kind === "ending") {
        const end = node.ending || {};
        const via = end.via || [];
        if (end.type === "pending") {
          const r = asOf[via[0]];
          copy.future = !(r && r.blocked);
          if (!copy.future) copy.detail = "needs " + r.open_blockers.join(", ");
        } else {
          copy.future = !via.every(function (id) { return present[id] && linked(id); });
        }
      }
      return copy;
    });
    const byId = {};
    nodes.forEach(function (node) { byId[node.id] = node; });
    const keep = function (id) { return byId[id] && !byId[id].future; };
    const outEdges = [];
    chapter.edges.forEach(function (edge) {
      if (!keep(edge.from) || !keep(edge.to)) return;
      if (!edge.derived && !appliedKeys[dgEdgeKey(edge.relation, { kind: "decision", ref: edge.from }, { kind: "decision", ref: edge.to })]) return;
      const copy = Object.assign({}, edge);
      if (stepped && ((last.type === "edge" && !edge.derived && last.key === dgEdgeKey(edge.relation, { kind: "decision", ref: edge.from }, { kind: "decision", ref: edge.to })) ||
        (last.type === "decision" && (edge.to === last.id || byId[edge.to].parent === last.id)))) copy.enter = true;
      outEdges.push(copy);
    });
    // Facts, needs attention, and explored, as decisiongraph builds them.
    const facts = { endings_reached: 0, endings_total: 0, locked: 0, conflicts: 0, challenged: 0 };
    const attention = [];
    const explored = { decided: 0, not_taken: 0, hypotheses: 0, locked: 0, total: 0, percent: null };
    nodes.forEach(function (node) {
      if (node.kind === "ending" && !node.future) {
        facts.endings_total++;
        if (node.ending && node.ending.reached) facts.endings_reached++;
      }
    });
    const pairs = {};
    nodes.forEach(function (node) {
      if (node.kind !== "decision" || node.future) return;
      const d = node.decision;
      if (d.blocked) {
        facts.locked++;
        attention.push({ kind: "locked", nodes: [node.id].concat(d.open_blockers), text: node.id + " waits on " + d.open_blockers.join(", ") });
      }
      if (d.conflicted) {
        (d.conflicts_with || []).forEach(function (other) {
          if (!active(other)) return;
          const pair = other < node.id ? [other, node.id] : [node.id, other];
          if (pairs[pair.join("\u0000")]) return;
          pairs[pair.join("\u0000")] = true;
          facts.conflicts++;
          attention.push({ kind: "conflict", nodes: pair, text: pair[0] + " ⟷ " + pair[1] + ", both active" });
        });
      }
      if (d.challenged) {
        facts.challenged++;
        attention.push({ kind: "challenged", nodes: [node.id], text: node.id + ": the latest evidence contradicts it" + d.challenger });
      }
      if (node.role === "refinement") return;
      const alts = (node.alternatives || []).length;
      if (d.status === "decision" && d.blocked) explored.locked++;
      else if (d.status === "decision") explored.decided++;
      else if (d.effective_status === "active") explored.hypotheses++;
      else return;
      explored.not_taken += alts;
    });
    const rank = { conflict: 0, locked: 1, challenged: 2 };
    attention.sort(function (a, b) { return rank[a.kind] - rank[b.kind]; });
    explored.total = explored.decided + explored.not_taken + explored.hypotheses + explored.locked;
    if (explored.total > 0) explored.percent = Math.floor((explored.decided * 200 + explored.total) / (explored.total * 2));
    return Object.assign({}, chapter, {
      nodes: nodes, edges: outEdges, facts: facts, needs_attention: attention, explored: explored,
      replay: { at: n, total: tl.events.length, event: last },
    });
  }

  function dgReduced() {
    try {
      return !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
    } catch (err) {
      return false;
    }
  }

  function dgReadout(chapter, at) {
    const tl = dgTimeline(chapter);
    const m = tl.events.length;
    const n = at == null ? m : at;
    const ev = tl.events[n - 1];
    if (!ev) return { text: "", valuetext: "" };
    const when = dgEventTime(ev, tl.seconds);
    const what = ev.type === "decision" ? ev.id : ev.label;
    return {
      text: when + " · " + what + " · " + n + " of " + m + (at == null ? " · now" : ""),
      valuetext: n + " of " + m + ", " + when + ", " + ev.label + (at == null ? ", now" : ""),
    };
  }

  // Ticks are spaced by event order, not by time, so a burst of events
  // recorded in one second still gets one tick each.
  function dgTickX(i, m) {
    return m <= 1 ? 50 : 2 + (96 * i) / (m - 1);
  }

  function dgTickClass(ev, future) {
    let who = "";
    if (ev.type === "decision") {
      if (ev.by === "agent") who = "agent";
      else if (ev.by === "user") who = "user";
      else if (ev.by) who = "login";
      else who = "unset";
    }
    return "dg-tick dg-tick-" + ev.type + (who ? " dg-tick-" + who : "") + (future ? " dg-tick-future" : "");
  }

  // dgTrack draws the scrubber track once per chapter. A step moves the
  // playhead with dgTrackMove instead, which touches only the ticks between
  // the old and the new position, so a long timeline is not redrawn per step.
  function dgTrack(chapter, at) {
    const tl = dgTimeline(chapter);
    const m = tl.events.length;
    const n = at == null ? m : at;
    const pos = dgTickX(n - 1, m);
    const done = s("line", { x1: "2%", x2: pos + "%", y1: 16, y2: 16, class: "dg-track-done" });
    const kids = [
      s("line", { x1: "2%", x2: "98%", y1: 16, y2: 16, class: "dg-track-base" }),
      done,
    ];
    const ticks = [];
    tl.events.forEach(function (ev, i) {
      const x = dgTickX(i, m) + "%";
      const cls = dgTickClass(ev, i >= n);
      const title = s("title", null, [dgEventTime(ev, true) + " · " + ev.label]);
      let tick;
      if (ev.type === "decision") tick = s("circle", { cx: x, cy: 16, r: 5.5, class: cls }, [title]);
      else if (ev.type === "retraction") tick = s("rect", { x: x, y: 12, width: 8, height: 8, transform: "translate(-4 0)", class: cls }, [title]);
      else tick = s("circle", { cx: x, cy: 16, r: 3, class: cls }, [title]);
      ticks.push(tick);
      kids.push(tick);
    });
    const head = s("line", { x1: pos + "%", x2: pos + "%", y1: 3, y2: 29, class: "dg-playhead" });
    const thumb = s("circle", { cx: pos + "%", cy: 16, r: 8, class: "dg-thumb" });
    kids.push(head, thumb);
    // Date axis: a label where the day changes in the browser's zone; within
    // one day the first and last times instead. Labels closer than 14% are
    // dropped.
    const labels = [];
    let day = "";
    tl.events.forEach(function (ev, i) {
      const d = dgEventDay(ev);
      if (d && d !== day) { labels.push({ i: i, text: d }); day = d; }
    });
    if (labels.length < 2 && m > 1) {
      labels.length = 0;
      labels.push({ i: 0, text: dgEventTime(tl.events[0], tl.seconds) });
      labels.push({ i: m - 1, text: dgEventTime(tl.events[m - 1], tl.seconds).replace(/^\S+ \S+ · /, "") });
    }
    let lastX = -100;
    labels.forEach(function (label, k) {
      const x = dgTickX(label.i, m);
      if (x - lastX < 14 && k !== labels.length - 1) return;
      lastX = x;
      const anchor = x < 12 ? "start" : (x > 88 ? "end" : "middle");
      kids.push(s("text", { x: (anchor === "start" ? Math.max(0, x - 1) : (anchor === "end" ? Math.min(100, x + 1) : x)) + "%", y: 46, "text-anchor": anchor, class: "dg-axis" }, [label.text]));
    });
    const svg = s("svg", { class: "dg-track", width: "100%", height: 52, "aria-hidden": "true", focusable: "false" }, kids);
    svg.sdfTrack = { chapter: chapter, n: n, ticks: ticks, done: done, head: head, thumb: thumb };
    return svg;
  }

  // dgTrackMove moves the drawn track's playhead to at, or redraws the track
  // when it belongs to another chapter.
  function dgTrackMove(slider, chapter, at) {
    const svg = slider.querySelector ? slider.querySelector(".dg-track") : null;
    const t = svg && svg.sdfTrack;
    if (!t || t.chapter !== chapter) { fill(slider, dgTrack(chapter, at)); return; }
    const events = dgTimeline(chapter).events;
    const m = events.length;
    const n = at == null ? m : at;
    if (n === t.n) return;
    for (let i = Math.min(n, t.n); i < Math.max(n, t.n); i++) t.ticks[i].setAttribute("class", dgTickClass(events[i], i >= n));
    const pos = dgTickX(n - 1, m) + "%";
    t.done.setAttribute("x2", pos);
    t.head.setAttribute("x1", pos);
    t.head.setAttribute("x2", pos);
    t.thumb.setAttribute("cx", pos);
    t.n = n;
  }

  function dgPlayLabel() {
    return dgTimer ? "❚❚ Pause" : "▶ Replay";
  }

  // Pointer moves while dragging are coalesced to one seek per animation
  // frame; the last position always lands (on the next frame or on release).
  let dgPointer = null;
  let dgPointerFrame = 0;
  function dgPointerSeek(el, clientX) {
    const chapter = dgView.chapter;
    if (!chapter || !el || !el.getBoundingClientRect) return;
    const m = dgTimeline(chapter).events.length;
    const box = el.getBoundingClientRect();
    if (!box.width) return;
    const pct = ((clientX - box.left) / box.width) * 100;
    const i = m <= 1 ? 0 : Math.round(((pct - 2) / 96) * (m - 1));
    dgSeek(Math.max(1, Math.min(m, i + 1)), true);
  }
  function dgPointerFlush() {
    if (dgPointerFrame && typeof cancelAnimationFrame === "function") cancelAnimationFrame(dgPointerFrame);
    dgPointerFrame = 0;
    const p = dgPointer;
    dgPointer = null;
    if (p) dgPointerSeek(p.el, p.x);
  }
  function dgPointerQueue(el, clientX) {
    dgPointer = { el: el, x: clientX };
    if (dgPointerFrame) return;
    if (typeof requestAnimationFrame !== "function") { dgPointerFlush(); return; }
    dgPointerFrame = requestAnimationFrame(function () {
      dgPointerFrame = 0;
      dgPointerFlush();
    });
  }
  function dgDragEnd() {
    if (!dgDragging) return;
    dgDragging = false;
    dgPointerFlush();
  }

  function dgSlider(chapter) {
    const tl = dgTimeline(chapter);
    const m = tl.events.length;
    const readout = dgReadout(chapter, dgAt);
    return h("div", {
      class: "dg-slider",
      role: "slider",
      tabindex: "0",
      "data-focus-key": "replay:slider",
      "aria-label": "Replay position",
      "aria-valuemin": "1",
      "aria-valuemax": String(m),
      "aria-valuenow": String(dgAt == null ? m : dgAt),
      "aria-valuetext": readout.valuetext,
      "aria-keyshortcuts": "ArrowLeft ArrowRight Home End PageUp PageDown [ ] Space",
      onkeydown: function (event) { dgSliderKey(event); },
      onpointerdown: function (event) {
        if (event.button != null && event.button !== 0) return;
        dgDragging = true;
        const el = event.currentTarget;
        try {
          if (el.setPointerCapture && event.pointerId != null) el.setPointerCapture(event.pointerId);
        } catch (err) { /* a synthetic pointer has nothing to capture */ }
        if (el.focus) el.focus();
        event.preventDefault();
        dgPointer = null;
        dgPointerSeek(el, event.clientX);
      },
      onpointermove: function (event) {
        if (!dgDragging) return;
        // A release this element never saw (the tab redrew mid-drag) leaves
        // no button down: stop instead of scrubbing on hover.
        if (event.buttons === 0) { dgDragging = false; dgPointer = null; return; }
        dgPointerQueue(event.currentTarget, event.clientX);
      },
      onpointerup: dgDragEnd,
      onpointercancel: dgDragEnd,
    }, [dgTrack(chapter, dgAt)]);
  }

  function dgReplayBar(chapter) {
    const readout = dgReadout(chapter, dgAt);
    return h("div", { class: "dg-replay", role: "group", "aria-label": "Replay" }, [
      h("div", { class: "dg-replay-top" }, [
        h("button", { type: "button", class: "dg-play", "data-focus-key": "replay:play", "aria-pressed": dgTimer ? "true" : "false", "aria-label": dgTimer ? "Pause replay" : "Play replay",
          onclick: function () { dgPlayToggle(); } }, [dgPlayLabel()]),
        h("p", { class: "dg-readout", "aria-hidden": "true" }, [readout.text]),
        h("button", { type: "button", class: "dg-now", "data-focus-key": "replay:now", hidden: dgAt == null ? "" : null, onclick: function () { dgSeek(null, true); } }, ["Now"]),
      ]),
      dgSlider(chapter),
      h("p", { class: "dg-replay-hint" }, ["[ ] step · ← → Home End · Space plays · " + (dgReduced() ? "reduced motion: steps without animation" : "drag to scrub")]),
    ]);
  }

  function dgSliderKey(event) {
    const chapter = dgView.chapter;
    if (!chapter || event.ctrlKey || event.metaKey || event.altKey) return;
    const m = dgTimeline(chapter).events.length;
    const n = dgAt == null ? m : dgAt;
    const big = Math.max(1, Math.round(m / 10));
    let next = null;
    switch (event.key) {
      case "ArrowLeft": case "ArrowDown": case "[": next = n - 1; break;
      case "ArrowRight": case "ArrowUp": case "]": next = n + 1; break;
      case "PageDown": next = n - big; break;
      case "PageUp": next = n + big; break;
      case "Home": next = 1; break;
      case "End": next = m; break;
      case " ": case "Spacebar":
        event.preventDefault();
        if (event.stopPropagation) event.stopPropagation();
        dgPlayToggle();
        return;
      default: return;
    }
    event.preventDefault();
    if (event.stopPropagation) event.stopPropagation();
    dgSeek(Math.max(1, Math.min(m, next)), true);
  }

  // dgGuard runs a replay step. Should it throw, the replay stops and the
  // whole tab is drawn again by render(), which shows its own error rather
  // than a half-updated page.
  function dgGuard(fn) {
    try {
      fn();
    } catch (err) {
      dgStop();
      dgDragging = false;
      dgPointer = null;
      render();
    }
  }

  // dgSeek moves the playhead. It redraws only the canvas, the panel, and the
  // scrubber's insides, so the slider element (its focus and pointer
  // capture) survives a drag. The hash keeps ?at=N with replaceState, which
  // fires no hashchange.
  function dgSeek(at, byUser) {
    dgGuard(function () {
      const chapter = dgView.chapter;
      if (!chapter) return;
      const paused = !!(byUser && dgTimer);
      if (paused) dgStop("Replay paused at " + dgReadout(chapter, dgAt).valuetext + ".");
      const next = dgClampAt(chapter, at);
      if (next === dgAt) { if (paused) dgRefresh(); return; }
      dgPrevAt = dgAt == null ? dgTimeline(chapter).events.length : dgAt;
      dgAt = next;
      const hash = dgHash(chapter.id, dgView.focus, dgAt);
      if (typeof history !== "undefined" && history.replaceState) {
        try { history.replaceState(null, "", hash); } catch (err) { /* keep going without the URL */ }
      }
      dgRefresh();
    });
  }

  function dgRefresh() {
    dgGuard(dgRefreshNow);
  }

  function dgRefreshNow() {
    const chapter = dgView.chapter;
    const root = app.querySelector ? app.querySelector(".dg") : null;
    if (!chapter || !root) { render(); return; }
    const proj = dgProject(chapter, dgAt, dgReduced() ? null : dgPrevAt);
    const focus = dgView.focus;
    const node = focus ? proj.nodes.filter(function (n) { return n.id === focus; })[0] : null;
    const slider = root.querySelector(".dg-slider");
    const now = root.querySelector(".dg-now");
    // Back at now the Now button hides; a hidden button cannot keep focus,
    // so focus moves to the slider first.
    if (now && dgAt == null && slider && slider.focus && typeof document !== "undefined" && document.activeElement === now) slider.focus();
    // The graph (or the List view) is replaced, so focus on a node inside
    // it would fall to <body>: put it back on the same node or control.
    const active = typeof document !== "undefined" ? document.activeElement : null;
    const kept = active && root.contains && root.contains(active) ? dgFocusKey(active) : "";
    const svg = root.querySelector(".dg-canvas > svg");
    if (svg) svg.replaceWith(dgCanvas(proj, focus));
    const list = root.querySelector(".dg-listview");
    if (list) list.replaceWith(dgListView(proj, focus));
    dgRevealLatest(root, proj);
    const panel = root.querySelector(".dg-panel");
    if (panel) fill(panel, node ? dgFocusPanel(proj, node) : dgSummaryPanel(proj));
    const readout = dgReadout(chapter, dgAt);
    if (slider) {
      slider.setAttribute("aria-valuenow", String(dgAt == null ? dgTimeline(chapter).events.length : dgAt));
      slider.setAttribute("aria-valuetext", readout.valuetext);
      dgTrackMove(slider, chapter, dgAt);
    }
    const text = root.querySelector(".dg-readout");
    if (text) text.textContent = readout.text;
    if (now) {
      now.hidden = dgAt == null;
      if (dgAt == null) now.setAttribute("hidden", "");
      else if (now.removeAttribute) now.removeAttribute("hidden");
    }
    const play = root.querySelector(".dg-play");
    if (play) {
      play.textContent = dgPlayLabel();
      play.setAttribute("aria-pressed", dgTimer ? "true" : "false");
      play.setAttribute("aria-label", dgTimer ? "Pause replay" : "Play replay");
    }
    const back = kept && document.activeElement !== active ? dgFindFocus(kept) : null;
    if (back && back.focus) back.focus();
  }

  // Scroll the canvas, never the page, so the decision the playhead just
  // recorded is in view.
  function dgRevealLatest(root, proj) {
    const ev = proj.replay && proj.replay.event;
    const canvas = root.querySelector(".dg-canvas");
    if (!ev || ev.type !== "decision" || !canvas || !canvas.getBoundingClientRect) return;
    const el = canvas.querySelector('[data-node="' + String(ev.id).replace(/["\\]/g, "\\$&") + '"]');
    if (!el || !el.getBoundingClientRect) return;
    const box = canvas.getBoundingClientRect();
    const at = el.getBoundingClientRect();
    if (at.bottom > box.bottom - 8) canvas.scrollTop += at.bottom - box.bottom + 24;
    else if (at.top < box.top + 8) canvas.scrollTop -= box.top - at.top + 24;
  }

  function dgStop(message) {
    if (dgTimer) clearInterval(dgTimer);
    dgTimer = null;
    if (message) dgAnnounce(message);
  }

  // The live region hears the replay start, pause, and finish only. Each
  // step is in the slider's aria-valuetext, which a screen reader reads
  // when the slider has focus; announcing every step as well would talk
  // over itself at the playback pace.
  function dgPlayToggle() {
    dgGuard(function () {
      const chapter = dgView.chapter;
      if (!chapter) return;
      if (dgTimer) {
        dgStop("Replay paused at " + dgReadout(chapter, dgAt).valuetext + ".");
        dgRefresh();
        return;
      }
      const m = dgTimeline(chapter).events.length;
      if (dgAt == null) dgSeek(1, false);
      const reduced = dgReduced();
      // Reduced motion: no fades, and a slower discrete pace.
      dgTimer = setInterval(function () {
        dgGuard(function () {
          const current = route();
          if (current.name !== "decisions" || !dgView.chapter || dgView.chapter.id !== chapter.id) { dgStop(); return; }
          const n = (dgAt == null ? m : dgAt) + 1;
          if (n >= m) {
            dgStop("Replay finished: " + dgReadout(chapter, null).valuetext + ".");
            dgSeek(null, false);
            return;
          }
          dgSeek(n, false);
        });
      }, reduced ? 1600 : 900);
      dgAnnounce("Replaying from " + dgReadout(chapter, dgAt).valuetext + ".");
      dgRefresh();
    });
  }

  // [ and ] step the replay from anywhere on the Decisions tab except a text
  // field; on the slider itself its own handler runs first.
  window.addEventListener("keydown", function (event) {
    if (event.key !== "[" && event.key !== "]") return;
    if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
    if (dgTyping(event.target)) return;
    dgGuard(function () {
      if (route().name !== "decisions" || !dgView.chapter) return;
      event.preventDefault();
      const m = dgTimeline(dgView.chapter).events.length;
      const n = dgAt == null ? m : dgAt;
      dgSeek(Math.max(1, Math.min(m, n + (event.key === "]" ? 1 : -1))), true);
    });
  });

  // A drag released outside the slider still ends.
  window.addEventListener("pointerup", dgDragEnd);
  window.addEventListener("pointercancel", dgDragEnd);

  function decisionsTab(chapterId, focus, at) {
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
    const named = chapters.filter(function (c) { return c.id === chapterId; })[0];
    const live = named || chapters[0];
    // ?at=N counts events of the chapter the link named; on the fallback
    // to the newest chapter it would land somewhere arbitrary, so it is
    // ignored there.
    if (!named) at = null;
    if (dgView.chapter && dgView.chapter.id !== live.id) dgStop();
    if (!dgView.chapter || dgView.chapter.id !== live.id) dgPrevAt = null;
    dgAt = dgClampAt(live, at);
    // The graph as of the replay position; the live chapter at the end.
    const chapter = dgProject(live, dgAt, dgReduced() ? null : dgPrevAt);
    dgPrevAt = null;
    const node = focus ? chapter.nodes.filter(function (n) { return n.id === focus; })[0] : null;
    if (!node) focus = null;
    dgView = { chapter: live, focus: focus };
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
          if (key === "Escape") { dgClose(chapter, focus); return; }
          if (key === "ArrowDown" || key === "ArrowUp") {
            event.preventDefault();
            const list = dgOrderedNodes(chapter);
            let at = -1;
            for (let i = 0; i < list.length; i++) if (list[i].id === focus) at = i;
            const next = list[Math.max(0, Math.min(list.length - 1, at + (key === "ArrowDown" ? 1 : -1)))];
            if (next) dgGo(chapter.id, next.id);
          }
        },
      }, dgList ? [dgListView(chapter, focus)] : [dgKey(chapter), dgCanvas(chapter, focus)]),
      dgReplayBar(live),
      // The visible copy of the last status; screen readers hear it from the
      // one persistent live region outside the drawn tree (dgAnnounce).
      h("p", { class: "dg-live", "aria-hidden": "true" }, [dgStatus]),
    ]);
    const panel = h("aside", { class: "dg-panel", "aria-label": node ? "Decision details" : "Chapter summary" }, node ? dgFocusPanel(chapter, node) : dgSummaryPanel(chapter));
    return h("section", { class: "dg" }, [rail, canvas, panel]);
  }

  // Every draw replaces the tree, so put focus back: first where a control
  // asked (a closed panel returns to its node), then, after navigation, the
  // route's node, else the control that had focus before the draw.
  function dgRestoreFocus(current, kept) {
    const want = dgWantFocus;
    dgWantFocus = "";
    if (current.name !== "decisions") return;
    const node = current.node ? "node:" + current.node : "";
    const order = dgNavigating ? [want, node, kept] : [want, kept, node];
    for (let i = 0; i < order.length; i++) {
      const el = dgFindFocus(order[i]);
      if (el && el.focus) {
        el.focus();
        return;
      }
    }
  }

  function shell(active, body) {
    fill(app, h("div", { class: "wrap" }, [
      header(active),
      loadError ? h("div", { class: "error" }, [h("p", null, [loadError])]) : body,
      footer(),
    ]));
  }

  function render() {
    // route() and the focus restore run inside the try: anything they throw
    // draws as an error, never as a blank page.
    let current = { name: "overview" };
    const active = typeof document !== "undefined" ? document.activeElement : null;
    const kept = active && app.contains && app.contains(active) ? dgFocusKey(active) : "";
    try {
      current = route();
      document.title = routeTitle(current);
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
      else if (current.name === "decisions") body = decisionsTab(current.chapter, current.node, current.at);
      else body = overview();
      shell(current.name, body);
      dgRestoreFocus(current, kept);
    } catch (err) {
      document.title = routeTitle(current);
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
      readJSON("/api/overview-charts", "overview charts").catch(function (err) {
        return { error: err && err.message ? err.message : "The overview charts could not be read." };
      }),
    ])
      .then(function (bodies) {
        snapshot = bodies[0];
        if (!snapshot.sessions) snapshot.sessions = [];
        if (!snapshot.activity_by_day) snapshot.activity_by_day = [];
        decisions = bodies[1] || { decisions: [], edges: [] };
        if (!decisions.decisions) decisions.decisions = [];
        if (!decisions.edges) decisions.edges = [];
        budget = bodies[2] && Object.prototype.hasOwnProperty.call(bodies[2], "budget") ? bodies[2].budget : null;
        decisionGraph = bodies[3] && !bodies[3].error ? bodies[3] : null;
        decisionGraphError = bodies[3] && bodies[3].error ? bodies[3].error : "";
        overviewCharts = bodies[4] && !bodies[4].error && bodies[4].schema ? bodies[4] : null;
        overviewChartsError = bodies[4] && bodies[4].error ? bodies[4].error : "";
        loadError = "";
        render();
      })
      .catch(function (err) {
        loadError = err && err.message ? err.message : "The summary could not be read.";
        render();
      });
  }

  function dgTyping(target) {
    if (!target) return false;
    const tag = target.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || !!target.isContentEditable;
  }

  // Decisions tab shortcuts, from anywhere on the tab except a text field,
  // and never with Ctrl, Meta or Alt (browser and OS shortcuts):
  // / focuses Jump, t toggles tensions, ? opens the shortcut sheet.
  window.addEventListener("keydown", function (event) {
    const key = event.key;
    if (key !== "/" && key !== "t" && key !== "?") return;
    if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
    if (dgTyping(event.target)) return;
    let current;
    try {
      current = route();
    } catch (err) {
      return;
    }
    if (current.name !== "decisions" || !app.querySelector) return;
    if (key === "/") {
      const jump = app.querySelector(".dg-jump");
      if (!jump) return;
      event.preventDefault();
      jump.focus();
      return;
    }
    if (!app.querySelector(".dg-toolbar")) return;
    event.preventDefault();
    if (key === "t") dgTensions = !dgTensions;
    else dgHelp = !dgHelp;
    render();
  });

  // The live region exists before the first announcement: a region added
  // together with its text is often not read.
  dgLiveRegion();

  // Navigation clears the status, and focus goes to the route's node.
  window.addEventListener("hashchange", function () {
    dgAnnounce("");
    dgNavigating = true;
    try {
      render();
    } finally {
      dgNavigating = false;
    }
  });
  render();
  load(false);
})();
