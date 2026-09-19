# Graph Report - SDFA  (2026-09-19)

## Corpus Check
- Corpus is ~12,766 words - fits in a single context window. You may not need a graph.

## Summary
- 95 nodes · 107 edges · 16 communities (12 shown, 4 thin omitted)
- Extraction: 93% EXTRACTED · 7% INFERRED · 0% AMBIGUOUS · INFERRED: 7 edges (avg confidence: 0.74)
- Token cost: 0 input · 0 output

## Community Hubs (Navigation)
- Agent Configuration
- Delivery and GitOps
- Task Control and Planning
- Decision Graph Core
- Knowledge and Project Passport
- FinOps and Unit Economics
- Domain Documentation
- Routing and Adapters
- Evaluation and Learning
- Policy and Tool Safety
- Observability and Incidents
- Context and Memory
- Software Catalog
- Product and Offering
- Budget Management
- Evidence-based Autonomy

## God Nodes (most connected - your core abstractions)
1. `Software Decision Fabric Architecture Final 1.5` - 14 edges
2. `Issue Tracker: Local Markdown` - 8 edges
3. `Intent/Decision Graph` - 8 edges
4. `Triage Labels` - 7 edges
5. `Control Plane` - 7 edges
6. `Project Passport` - 7 edges
7. `Task Manager` - 6 edges
8. `Domain Docs` - 5 edges
9. `Business Economics` - 4 edges
10. `Planner` - 4 edges

## Surprising Connections (you probably didn't know these)
- `Architecture Decision Records` --semantically_similar_to--> `Intent/Decision Graph`  [INFERRED] [semantically similar]
  docs/agents/domain.md → graphify-out/converted/Software_Decision_Fabric_Architecture_Final_VI_v1.5_7a58f7ea.md
- `Implementation ticket` --semantically_similar_to--> `Task Manager`  [INFERRED] [semantically similar]
  docs/agents/issue-tracker.md → graphify-out/converted/Software_Decision_Fabric_Architecture_Final_VI_v1.5_7a58f7ea.md
- `Wayfinding map` --semantically_similar_to--> `Control Plane`  [INFERRED] [semantically similar]
  docs/agents/issue-tracker.md → graphify-out/converted/Software_Decision_Fabric_Architecture_Final_VI_v1.5_7a58f7ea.md
- `ADR conflict surfacing` --conceptually_related_to--> `Intent/Decision Graph`  [INFERRED]
  docs/agents/domain.md → graphify-out/converted/Software_Decision_Fabric_Architecture_Final_VI_v1.5_7a58f7ea.md
- `Issue status` --semantically_similar_to--> `Task lifecycle`  [INFERRED] [semantically similar]
  docs/agents/issue-tracker.md → graphify-out/converted/Software_Decision_Fabric_Architecture_Final_VI_v1.5_7a58f7ea.md

## Hyperedges (group relationships)
- **Business intent to runtime evidence traceability** — graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_business_context, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_business_economics, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_intent_decision_graph, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_runtime_evidence [EXTRACTED 1.00]
- **Normal coding task flow** — graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_task_manager, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_planner, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_context_builder, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_router, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_guardrail, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_acp_v1, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_sandbox_worker_manager, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_validator, graphify_out_converted_software_decision_fabric_architecture_final_vi_v1_5_7a58f7ea_evaluator [EXTRACTED 1.00]
- **Local issue tracker artifact structure** — docs_agents_issue_tracker_feature_directory, docs_agents_issue_tracker_spec, docs_agents_issue_tracker_ticket, docs_agents_issue_tracker_status, docs_agents_issue_tracker_comments, docs_agents_issue_tracker_map [EXTRACTED 1.00]

## Communities (16 total, 4 thin omitted)

### Community 0 - "Agent Configuration"
Cohesion: 0.15
Nodes (14): Domain docs, Issue tracker, Canonical triage labels, Issue Tracker: Local Markdown, Feature directory, Feature specification, Issue status, Triage Labels (+6 more)

### Community 1 - "Delivery and GitOps"
Cohesion: 0.15
Nodes (13): Argo CD, Argo Rollouts, Artifact Registry, Continuous Integration, Environment Manager, Generated Software, GitOps, License & Provenance Gate (+5 more)

### Community 2 - "Task Control and Planning"
Cohesion: 0.20
Nodes (12): Wayfinding map, Implementation ticket, Control Plane, Economic-aware planning, GitHub Issues, Jira, Linear, Planner (+4 more)

### Community 3 - "Decision Graph Core"
Cohesion: 0.25
Nodes (11): Software Decision Fabric Architecture Final 1.5, A2A, Business Context, Business Economics, IntentDecisionEdge, Intent/Decision Graph, IntentDecisionNode, Runtime evidence (+3 more)

### Community 4 - "Knowledge and Project Passport"
Cohesion: 0.29
Nodes (7): Issue comments, Economics artifacts, FinOps artifacts, Knowledge Distiller, Knowledge portability, Notion, Project Passport

### Community 5 - "FinOps and Unit Economics"
Cohesion: 0.33
Nodes (6): Business Economics Evaluator, Cost Attribution, FinOps / Cost Governance Plane, Offering Economics, Price Corridor, Unit Economics

### Community 6 - "Domain Documentation"
Cohesion: 0.40
Nodes (5): Domain Docs, ADR conflict surfacing, Architecture Decision Records, CONTEXT.md, Glossary vocabulary

### Community 7 - "Routing and Adapters"
Cohesion: 0.50
Nodes (5): ACP v1, Capability Registry, Native Adapter, Router, Sandbox / Worker Manager

### Community 8 - "Evaluation and Learning"
Cohesion: 0.40
Nodes (5): Evaluator, Evolver, Experience Store, Hypothesis-evidence-decision-action loop, Validator

### Community 9 - "Policy and Tool Safety"
Cohesion: 0.50
Nodes (4): Guardrail, MCP, Policy Engine, Tool Proxy/Gateway

### Community 10 - "Observability and Incidents"
Cohesion: 0.67
Nodes (3): Alert Router, Incident Manager, Observability

### Community 11 - "Context and Memory"
Cohesion: 0.67
Nodes (3): Context Builder, Memory Manager, OpenViking

## Knowledge Gaps
- **38 isolated node(s):** `CONTEXT.md`, `Glossary vocabulary`, `Feature directory`, `Feature specification`, `needs-triage` (+33 more)
  These have ≤1 connection - possible missing edges or undocumented components. (Counts symbols only; 42 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **4 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Software Decision Fabric Architecture Final 1.5` connect `Decision Graph Core` to `Delivery and GitOps`, `Knowledge and Project Passport`, `FinOps and Unit Economics`, `Routing and Adapters`, `Policy and Tool Safety`?**
  _High betweenness centrality (0.420) - this node is a cross-community bridge._
- **Why does `Issue Tracker: Local Markdown` connect `Agent Configuration` to `Task Control and Planning`, `Knowledge and Project Passport`?**
  _High betweenness centrality (0.160) - this node is a cross-community bridge._
- **Why does `SBOM / Provenance` connect `Delivery and GitOps` to `Decision Graph Core`?**
  _High betweenness centrality (0.131) - this node is a cross-community bridge._
- **Are the 2 inferred relationships involving `Intent/Decision Graph` (e.g. with `ADR conflict surfacing` and `Architecture Decision Records`) actually correct?**
  _`Intent/Decision Graph` has 2 INFERRED edges - model-reasoned connections that need verification._
- **What connects `CONTEXT.md`, `Glossary vocabulary`, `Feature directory` to the rest of the system?**
  _38 weakly-connected nodes found - possible documentation gaps or missing edges._