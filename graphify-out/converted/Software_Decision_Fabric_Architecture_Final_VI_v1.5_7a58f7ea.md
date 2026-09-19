<!-- converted from Software_Decision_Fabric_Architecture_Final_VI_v1.5.docx -->

Software Decision Fabric (SDF)
Architecture, Intent/Decision Graph, Business Economics, Protocols, FinOps, Safety, Operations & Handoff
Bản tổng hợp cuối cùng của các quyết định kiến trúc và nguyên tắc triển khai
FINAL 1.5  •  19/09/2026


# 1. Executive summary
SDF không phải một coding agent mới mà là control/decision/learning system phía trên nhiều agent và runtime. Research thesis trung tâm là Intent/Decision Graph: duy trì traceability hai chiều từ business objective, economic assumption và policy tới architecture decision, requirement, task, code/release, rồi quay lại runtime evidence để xác minh hoặc bác bỏ giả định ban đầu. Business Context và Business Economics đi trước architecture; FinOps là economic control plane xuyên build, release và runtime. Agent interoperability được tách thành ba protocol boundary: ACP cho boundary client-to-coding-agent, A2A cho boundary agent-system-to-agent-system, MCP cho boundary agent-to-tools/resources; native adapter luôn tồn tại như fallback nếu protocol adapter không đạt SLO. Human interface thuộc SDF Console và các external adapters, không nằm trên critical execution path. Software do SDF tạo ra phải mang theo source + tests + SBOM/provenance + Project Passport + Policy Pack + Intent/Decision snapshot + economics/ + finops/ để human hoặc một SDF khác tiếp quản cả technical intent lẫn business intent.
Business Context / Customer / Market
              ↓
      Business Economics
   Cost / Value / Pricing
              ↓
       Intent/Decision Graph
              ↓
             SDF
 Control + Policy + Learning
       ┌──────┼──────┐
      A2A    ACP     MCP
       │      │       │
 Agent/SDF  Coding   Tools
 Systems    Agents   APIs
              ↓
 Software + Maintainability + Economic Package

# 2. SDF được build để làm gì?
- Một entry point để nhận objective/task rồi điều phối nhiều coding agent/harness.
- Chia vai: planning, implementation, review, testing, research, operations có thể dùng agent/model khác nhau.
- Chuẩn hóa workflow: plan → retrieve context → route → guard → execute → validate → evaluate → repair/replan.
- Giữ state và shared context qua nhiều agent, session và machine.
- Áp policy/tool gate chung thay vì để mỗi agent có shell unrestricted.
- Đo hiệu quả theo task type, agent/model, cost, latency, retries và quality.
- Hỗ trợ fallback, retry, replan, switch agent và parallel execution.
- Xử lý incident với priority cao hơn normal task.
- Dùng lịch sử evaluation để cải thiện routing, prompts, workflow và tool selection.
- Tạo sản phẩm có thể bàn giao và maintain bởi một SDF khác.
- Đưa business context và economic constraints vào planning/architecture thay vì tính economics sau khi build xong.
- Đo và tối ưu cost-to-build, cost-to-serve, unit economics, customer value, margin và price corridor của từng offering.
- Duy trì Intent/Decision Graph để giải thích “tại sao hệ thống trở thành như hiện tại” và trace ngược từ runtime evidence về business assumption/architecture decision.
- Ra quyết định theo hypothesis → evidence → action → measurement → evaluation, thay vì biến suy luận của LLM thành fact.

# 3. Kiến trúc tổng thể: các plane

BUSINESS CONTEXT / ECONOMICS
Customer | Use case | Value | Market | Contract | Price Corridor
                         ↓
                 INTENT / DECISION GRAPH
Objective → Assumption → Constraint → ADR → Requirement → Task
      ↑                                              ↓
Runtime Evidence ← Metric / Eval ← Release ← Code / Infrastructure
                         ↓
HUMAN / WORK SYSTEMS
Jira | Linear | GitHub Issues | Notion | SDF Console | Backstage
                         ↓
                    SDF CONTROL
Task / Planner / State / Router / Scheduler / Release / Environment
        ┌────────────────┼────────────────┐
        ↓                ↓                ↓
 Memory + Context     Policy Plane     Quality + Learning
        └────────────────┼────────────────┘
                         ↓
                 PROTOCOL / INTEROP
             A2A      ACP      MCP
              │        │        │
        Agent systems  │      Tools/APIs
                       ↓
             Coding Agents + Native Fallback
                         ↓
                Sandbox / Worker Runtime
                         ↓
       CI → signed artifact → GitOps/CD → Runtime
                         ↓
          Observability → Alert Router → Incident
                         ↓
                FinOps / Actual Economics
                         ↓
        Evidence updates Decision Graph → ADAPT

OUTPUT: software + SBOM/provenance + Project Passport + Policy Pack + Decision Graph + economics/ + finops/
## 3.1 Research thesis trung tâm: Business Intent → Engineering Decisions → Runtime Evidence → Learning
Vấn đề khó nhất không phải gọi nhiều agent. SDF phải giữ được mối liên hệ nhân-quả/traceability đủ tốt giữa điều business muốn đạt, các giả định và constraint, quyết định kiến trúc/kỹ thuật, implementation/release và evidence thật từ production. Nếu liên kết này mất đi, SDF có thể maintain code nhưng không biết hệ thống còn phục vụ đúng business model hay không.
Business Goal
    ↓
Business Context / Economic Assumptions
    ↓
Constraints / Policies
    ↓
Architecture Decisions (ADRs)
    ↓
Requirements → Tasks → Code / IaC
    ↓
Release → Production
    ↓
Cost / SLO / Usage / Revenue / Incidents
    ↓
Did the original hypothesis hold?
    ↓
Maintain / Adapt / Supersede decision
## 3.2 Intent/Decision Graph là primitive khác Memory
Memory trả lời “chúng ta đã biết gì”; Intent/Decision Graph trả lời “tại sao hệ thống trở thành như hiện tại, quyết định nào dựa trên giả định/evidence nào, và runtime signal nào đang xác minh hoặc mâu thuẫn với quyết định đó”. Graph cần trace hai chiều: từ business objective xuống code/release và từ production symptom/cost ngược về ADR/assumption tạo ra trạng thái hiện tại.
BUSINESS-12  Target ARPU = $99
      │ constrains
      ▼
ECON-19      Max COGS/customer = $19.80
      │ constrains
      ▼
ADR-31       Multi-tenant managed PostgreSQL
      │ implemented_by
      ▼
GH-442       Tenant isolation
      │ shipped_as
      ▼
REL-98
      │ measured_by
      ▼
METRIC       COGS/customer = $14.20
Graph edges nên dùng vocabulary rõ như motivates, constrains, implements, depends_on, supersedes, validates, contradicts, measures và caused_by. Node/edge phải có source, owner, timestamp, confidence/evidence refs; hypothesis không được lưu như fact.
## 3.3 Evidence-based autonomy
Autonomy đáng tin không phải LLM “nghĩ X rồi làm X”. Mọi thay đổi có rủi ro hoặc ảnh hưởng business/economics nên đi qua vòng hypothesis → evidence → decision → action → measurement → evaluation. Ví dụ rightsizing DB chỉ được promote từ staging sang production khi evidence cho thấy cost giảm nhưng latency/error/SLO/capacity vẫn đạt.
Hypothesis → Evidence → Decision → Action → Measurement → Evaluation → Promote / Rollback / Replan
## 3.4 Knowledge distillation & continuity
Agent tạo ra rất nhiều transient events nhưng chỉ một phần nhỏ xứng đáng trở thành durable project knowledge. Knowledge Distiller phải phân loại và promote những kết luận bền vững thành ADR, invariant, runbook, policy, known issue, economic assumption hoặc architecture constraint; phần còn lại giữ ở task/experience storage hoặc discard theo retention policy. Đây là điều kiện để một SDF khác tiếp quản dự án mà không cần raw conversational history.
## 3.5 Research priorities
- Intent/Decision Graph: business → architecture → code/release → runtime evidence, với traceability hai chiều.
- Evidence-based autonomy: phân biệt hypothesis/fact, provenance/evidence và promotion/rollback dựa trên measurement.
- Knowledge distillation & portability: quyết định knowledge nào phải trở thành durable artifact để handoff.
- Evaluation/Evolution: chứng minh strategy mới thực sự tốt hơn bằng benchmark và production evidence.
- Economic-aware planning: business value, cost envelope và pricing/service model constrain architecture từ đầu.
- Multi-agent orchestration: planner/router/scheduler/context sharing trên nhiều harness.
- Runtime/tooling: sandbox, agent runtime, CI/CD, gateways và UI; quan trọng nhưng ít tạo differentiation nhất.
# 4. Business Context & Business Economics
Business Economics không phải bước định giá ở cuối vòng đời. Nó là input cho SDF trước architecture: software nào đáng xây, cho ai, giá trị kinh tế nào cần tạo ra, mức reliability/compliance nào cần thiết và complexity/cost envelope nào business chịu được.
Product × Customer × Use case × Market × Contract → Offering Economics
## 4.1 Business Context là first-class object
Mỗi initiative/product nên có BusinessContext machine-readable: customer segment, industry, scale, transaction/traffic profile, economic buyer, budget owner, pain severity, current process, business criticality, regulatory requirements, SLA, deployment model, procurement constraints và known alternatives. Pricing Engine không nhận “Product A”; nó nhận (Product A, BusinessContext B).
BusinessContext {
  customer_segment / industry
  scale / usage / transaction volume
  economic_buyer / budget_owner / procurement_limit
  pain / current_process / business_criticality
  SLA / compliance / data_residency
  alternatives[] / switching_cost
}
## 4.2 Cost, Value và Market là ba model khác nhau
Cost trả lời “chúng ta bỏ ra bao nhiêu”; Value trả lời “business nhận được bao nhiêu”; Market đặt các constraints như competitor, alternative, budget, procurement và willingness-to-pay. Price nằm trong khoảng giữa cost floor và customer value ceiling, nhưng không thể suy ra bằng build_cost × markup.
Cost floor  ≤  feasible price  ≤  customer value ceiling
Market + buyer + contract → vùng giá thực tế có thể chấp nhận
## 4.3 Product ≠ Offering
Repository/codebase chỉ là Product. Thứ được bán là Offering = Product + SLA + Support + Security + Compliance + Deployment Model + Contract Terms. Cùng một codebase có thể có SaaS, private deployment, managed service, OEM/API hoặc enterprise offering với COGS, risk và price khác nhau.
## 4.4 Value Model: value phải có evidence
Customer value nên được decomposition thành cost savings, revenue gain, productivity gain, risk avoidance và strategic value. Mỗi component phải mang estimate + confidence + evidence + assumptions; tránh để LLM tạo một con số “giá trị” không kiểm chứng. Khi có overlap giữa các component, Business Economics Evaluator phải điều chỉnh để không double-count.
Customer Value
├─ Cost Savings
├─ Revenue Gain
├─ Productivity Gain
├─ Risk Avoidance
└─ Strategic Value

Each component = estimate + confidence + evidence + assumptions
Với security/backup/compliance/observability, risk avoidance có thể model bằng Expected Loss = Probability × Impact; giảm expected loss là business value dù product không trực tiếp tạo revenue.
## 4.5 Alternatives, budget và willingness-to-pay
Khách hàng thường so solution với do-nothing, manual process, build internally, hire thêm người, incumbent hoặc competitor. Alternative Model là pricing anchor quan trọng. Economic value, budget và willingness-to-pay là ba thứ khác nhau: một solution có thể tạo $500k value nhưng buyer hiện tại chỉ kiểm soát $40k budget. Khi đó vấn đề có thể là buyer/contract alignment chứ không phải Value Model sai.
## 4.6 Price Corridor, Value Capture, ROI và Payback
SDF không nên xuất một “AI recommended price” giả chính xác. Nên xuất Price Corridor với economic floor, competitive anchors, value-based range, buyer budget ceiling và confidence/uncertainty. Human/Product/Sales quyết định quote cuối.
PRICE CORRIDOR
Economic floor          → cost + required margin/risk reserve
Competitive anchors     → alternatives / incumbent / internal build
Value-based range       → share of customer economic value
Buyer budget ceiling    → procurement reality
Recommended corridor    → overlap + confidence
Các metric quan trọng: Value Capture Ratio = Price / Customer Economic Value; Customer ROI = (Value − Price) / Price; Payback = Price / periodic value. Chúng giúp kiểm tra pricing từ cả seller và buyer perspective.
## 4.7 Pricing metric và commercial model
Pricing metric nên correlate với customer value nhưng vẫn kiểm soát được cost: per seat, per transaction, per API request, cloud spend under management, percentage of realized savings, subscription + usage, platform fee hoặc annual contract. Với FinOps, realized savings thường aligned hơn identified savings; với agent platform, token phản ánh cost tốt hơn value nên có thể cần hybrid pricing.
## 4.8 Business Economics Evaluator
Ngoài code/security/SLO evaluator, SDF cần evaluator cho economic viability: gross margin, cost-to-serve, support burden, customer ROI, payback, price/value ratio, operating complexity, revenue potential và uncertainty. Software technically excellent vẫn có thể là business failure nếu support/ops cost vượt economic envelope.
Business Economics Evaluation
├─ gross margin / COGS
├─ customer ROI / payback
├─ operating complexity
├─ support & compliance burden
├─ price/value ratio
├─ market alternatives
└─ confidence / unknowns
## 4.9 Economics phải influence architecture
Business Context phải đi vào Planner trước architecture. SMB product có ARPU $99/tháng không nên tự động nhận Kafka + service mesh + multi-region DB nếu economics không chịu nổi. Enterprise offering $200k/năm với strict SLA có thể justify dedicated deployment, regional DR và premium observability. Economic constraints là architecture constraints.
Business Context → Economic Model → Architecture Constraints → Plan → Build → Operate → Actual Economics → Adapt
## 4.10 Product Economics artifacts & portability
Generated product nên mang theo economics/ gồm creation-cost, COGS, maintenance/support model, value assumptions, alternatives, pricing model, margin model và uncertainty. Project Passport phải giải thích không chỉ “software chạy thế nào” mà còn “vì sao architecture này hợp business nào và assumptions nào làm offering khả thi”.
economics/
├── creation-cost.yaml
├── cogs.yaml
├── maintenance.yaml
├── value-model.yaml
├── alternatives.yaml
├── pricing.yaml
├── margin-model.yaml
└── assumptions.yaml
# 5. FinOps / Cost Governance Plane
FinOps là economic control plane cho cả SDF và software mà SDF tạo ra. Nó không chỉ hiển thị cloud bill; nó thu cost signals, normalize, attribute, budget, forecast, detect anomaly, tính unit economics, tạo optimization opportunities và đưa cost policy trở lại Planner/Router/Release/Evaluator.
Billing / Usage / Telemetry
        ↓
Cost Ingestion → Normalize → Attribution
        ↓
Budget / Forecast / Anomaly
        ↓
Unit Economics → Optimization → FinOps Policy
        ↓
Recommend / PR / Approve / Block / Alert
        ↓
SDF / Release / Operations
## 5.1 Cost ingestion & normalization
Cost sources gồm cloud billing, Kubernetes allocation, LLM/model usage, agent sandboxes, CI minutes, storage, observability, SaaS và shared platform services. Luôn giữ raw provider data để có thể reprocess. Canonical cost schema nên theo FOCUS hoặc mapping tương đương, với provider/service/resource/usage/billed cost/effective cost/currency/time/owner/product/environment.
raw provider exports → FOCUS-like canonical records → FinOps warehouse
MVP có thể dùng PostgreSQL; khi volume lớn, raw data ở object storage và analytical queries ở ClickHouse/warehouse tương đương. Cost data là structured analytics, không cần vector DB.
## 5.2 Cost Attribution
Tổng bill ít hữu ích nếu không biết cost thuộc product/team/service/environment/task nào. Attribution hierarchy nên thống nhất từ organization → business unit → product → service → environment → workload/task; riêng SDF có thể drill-down tới attempt → agent → model. Labels/tags phải là part of generated architecture và policy.
Organization → Business Unit → Product → Service → Environment → Workload / Task → Attempt / Agent / Model
Kubernetes có thể dùng OpenCost-style allocation theo CPU/RAM/GPU/PV/namespace/controller/labels; cloud/SaaS cost cần mapping tags/accounts/subscriptions/projects sang cùng business dimensions.
## 5.3 Shared cost allocation
Shared Kafka, observability, platform cluster hoặc network services phải có allocation rules rõ ràng: even split, usage proportional, request/message proportional, CPU proportional, revenue proportional hoặc custom business rule. Rule phải versioned/policy-driven thay vì hardcode SQL để showback/chargeback có thể audit.
## 5.4 Budget Manager & Router
Budget tồn tại theo hierarchy organization/product/environment/AI/task. Router dùng remaining budget + task criticality + historical cost per successful outcome để chọn agent/model và số lượng parallel attempts. “Model rẻ/token” có thể đắt hơn nếu retry nhiều; metric cần tối ưu là cost per accepted outcome.
routing fitness = correctness + reliability − cost − latency − retries
(cost được tính theo successful outcome, không chỉ per-call)
## 5.5 Forecasting & anomaly detection
Forecast trả lời “với tốc độ hiện tại tháng này sẽ kết thúc ở đâu”; anomaly trả lời “tại sao hôm nay khác thường”. MVP nên bắt đầu bằng rolling averages/EWMA/trend/seasonality trước khi dùng model phức tạp. Cost spike phải correlate với release/traffic/utilization: tăng cost do traffic/revenue tăng có thể bình thường; tăng cost khi traffic phẳng và utilization thấp là signal waste mạnh hơn.
## 5.6 Unit Economics
Không dừng ở monthly cloud spend. Mỗi offering nên định nghĩa unit metric: cost/customer, cost/transaction, cost/request, cost/inference, cost/active user, cost/merged PR hoặc cost/successful agent task. Unit economics nối FinOps với Business Economics và cho phép so sánh trước/sau optimization bằng value thay vì tổng bill.
## 5.7 Optimization Engine
Optimization opportunities nên là structured entities với type, resource, current cost, estimated saving, confidence, risk và evidence. Các loại chính: rightsizing, idle cleanup, scheduling, commitments, storage tiering, network/egress, caching/batching, LLM routing/context reduction, CI caching và agent retry reduction.
OptimizationOpportunity {
  resource / type
  current_cost / estimated_cost / monthly_saving
  confidence / risk
  evidence{}
  recommended_action
}
## 5.8 Shift-left FinOps
Cost governance phải chạy trước deploy. IaC plan → cost estimate → FinOps policy → PR/release gate. Infracost-style estimation phù hợp pre-deploy; OpenCost-style allocation phù hợp runtime; FOCUS-style normalized billing phù hợp actual economics. SDF nằm trên ba lớp này để so estimate vs actual và học calibration.
Infracost-like estimate → BEFORE deployment
OpenCost-like allocation  → DURING runtime
FOCUS-normalized billing  → ACTUAL billed/effective economics
## 5.9 FinOps Policy-as-Code
FinOps policy nên cover budget, cost delta, required ownership/tags, resource requests/limits, idle TTL, max per-task AI spend, approval thresholds và exception TTL. Cost increase không nên bị block máy móc: policy có thể require approval nếu delta lớn nhưng business value/revenue tăng tương ứng.
Code / IaC → cost estimate → FinOps Policy → warn | require_review | require_approval | deny
## 5.10 Alerts & remediation
FinOps alert phải actionable: actual vs expected, variance, likely cause, related release/task, forecast impact và next actions. Alert Router gửi technical anomalies tới SDF Console/incident room và service owner; engineering impact sang Linear/GitHub khi cần, delivery/budget risk sang Jira/management projection; external collaboration adapters chỉ mirror khi cần. Automatic remediation nên theo levels: recommend → create issue → create PR → auto-apply reversible → high-risk human approval.
## 5.11 FinOps + Evaluator + Evolver
Sau optimization, Evaluator phải kiểm savings cùng latency/error/SLO/capacity; giảm cost nhưng phá reliability là FAIL. Experience Store lưu agent/model/task cost, retries và accepted outcome để Evolver học routing, context strategy và architecture patterns có total cost of ownership tốt hơn.
## 5.12 FinOps artifacts trong generated product
Generated product nên ship finops/ gồm cost model, allocation rules, budget, policy, unit economics và dashboard definitions. Project Passport giữ cost baseline, primary cost drivers, target unit metric, known optimization opportunities và assumptions để SDF mới tiếp quản economics chứ không chỉ runtime.
finops/
├── cost-model.yaml
├── allocation.yaml
├── budget.yaml
├── policy.yaml
├── unit-economics.yaml
└── dashboards/
# 6. Control Plane
## 6.1 Planner
Planner chuyển objective thành work items/tasks, xác định dependencies, acceptance criteria và chiến lược thực hiện. Planner không nên trực tiếp sở hữu lifecycle của task.
## 6.2 Task Manager
Task Manager là nơi quản lý đơn vị công việc. Nó khác Scheduler, Router và runtime executor.
OBJECTIVE
    ↓
WORK ITEM
    ↓
TASK
 ├─ CHILD TASK
 └─ DEPENDENCY
    ↓
DISPATCH / ATTEMPT
    ↓
AGENT RUN
    ↓
TOOL CALL

## 6.3 Task lifecycle
CREATED → PLANNED → READY → QUEUED → RUNNING → VERIFYING
                                   │
                                   ├→ WAITING_DEPENDENCY
                                   ├→ WAITING_APPROVAL
                                   └→ RETRYING

Terminal states: COMPLETED / FAILED / BLOCKED / CANCELLED
Không nên tạo quá nhiều status. Retry/attempt nên là entity riêng để một logical task có thể được thực hiện nhiều lần bởi nhiều agent khác nhau.
## 6.4 State Manager
State là “hệ thống đang ở đâu ngay bây giờ”. State Manager nên thuộc SDF/control plane, không thuộc Agent Gateway.
- Workflow state: node hiện tại, transition tiếp theo, approvals, retry count.
- Execution state: dispatch/session/process/worktree/status của agent.
- Shared task state: output, artifacts, test result, current blockers.
- Incident state: severity, timeline, containment/recovery status.

## 6.5 Router + Capability Registry
Router không nên hardcode “Terraform → Claude”. Nó nên kết hợp task requirements + capability registry + historical evaluation + budget + current availability.
task requirements
      +
capabilities
      +
historical performance
      +
budget / latency
      ↓
choose agent + model + execution backend

# 7. Memory, Context và Knowledge
## 7.1 Memory Manager
Memory là những gì hệ thống nên nhớ qua nhiều task/session. Memory Manager thực hiện write, retrieve, rank/filter và lifecycle/retention của memory.


## 7.2 Context Builder
Không inject toàn bộ memory vào prompt. Context Builder lấy task + current state + relevant memory + repo context + policy constraints và tạo context package theo budget cho từng agent.
Task + State + Relevant Memory + Repo Facts + Policy
                         ↓
                   Context Builder
                         ↓
              Agent-specific context package
## 7.3 OpenViking
Trong trao đổi, OpenViking được đặt vào vai trò Memory/Context provider: lưu memory/resources/skills, retrieval và progressive context loading. Nó không nên quản lý runtime state, process/session, scheduler hay incident state.
- Phù hợp cho project knowledge, past incidents, trajectories, skills và long-term context.
- Không dùng để lưu worker PID, retry counter, current dispatch hay worktree status.
- Critical maintainability knowledge vẫn nên được promote vào file text/YAML/ADR trong repo để không phụ thuộc hoàn toàn vào một memory database.
- License của memory provider phải được audit nếu mục tiêu là proprietary/commercial product.
# 8. Validation, Guardrails và Safety
## 8.1 Validator
Validator trả lời: “Input/output có hợp lệ theo contract/invariant không?”. Càng deterministic càng tốt.
- Input schema, task type, required fields/context.
- Allowed/forbidden paths.
- Agent output schema và artifact existence.
- Diff khai báo so với diff thực.
- Invariant/project constraints.
## 8.2 Guardrail + Policy Engine
Guardrail trả lời: “Hành động này có được phép xảy ra không?”. Nó phải nằm trước hành động nguy hiểm, không phải chỉ kiểm tra sau khi agent đã chạy xong.
Agent → Tool Request → Policy/Guardrail → ALLOW / DENY / REQUIRE_APPROVAL → Tool
- Static policy: chặn rm -rf /, force push main, terraform destroy, destructive production commands.
- Contextual policy: cùng một command có thể ALLOW ở local nhưng REQUIRE_APPROVAL ở production.
- Runtime isolation: filesystem/network/credential boundary để agent không thể vượt policy chỉ bằng prompt.
- Mọi tool call nên đi qua Tool Proxy/Gateway để log và enforce policy.

## 8.3 Secrets, Identity và Budget
- Không đưa raw API keys/long-lived cloud credentials vào prompt.
- Cấp credential theo task/agent, scope nhỏ, TTL ngắn; revoke khi task kết thúc.
- Budget Manager giới hạn token, tiền, wall-clock, số agent và số retries.
- Budget là input của Router/Evolver; không phải chỉ là dashboard.
# 9. Evaluator, Experience Store và Evolution
## 9.1 Evaluator
Agent sinh output không nên là entity duy nhất quyết định output đó đúng. Evaluator chạy theo nhiều tầng, từ deterministic/rẻ đến semantic/đắt.
1.  Mechanical checks: build, lint, typecheck, unit/integration tests, exit codes.
2.  Change validation: scope, unexpected files, secrets/debug code, dependency/license policy.
3.  Acceptance validation: requirement/acceptance criteria nào đã pass/fail.
4.  Independent LLM review: correctness, architecture, edge cases.
5.  Runtime verification: API/browser test, Terraform plan, Kubernetes dry-run, synthetic transaction.
6.  Regression/benchmark: so với baseline về correctness, latency, cost, retries.
Agent result
   ↓
Mechanical checks
   ↓
Acceptance checks
   ↓
Independent review
   ↓
Runtime verification
   ↓
PASS → DONE
FAIL → REPAIR
BAD PLAN → REPLAN
Evaluator nên trả structured evidence, không chỉ pass/fail: criteria, evidence, failure category, feedback và confidence.
## 9.2 Experience Store
Experience Store là dữ liệu định lượng/structured cho từng run. Nó khác Memory Store vì được thiết kế để thống kê và tối ưu.
task_type
agent / model
prompt/workflow version
context strategy
result
cost / tokens
latency
retries
evaluation evidence
failure reason
## 9.3 Evolver
Evaluate tạo evidence; Evolve thay đổi behavior dựa trên evidence. Evolver không nên tự ý thay mọi thứ trong production.


# 10. Incident Management
## 10.1 Signal → Alert → Incident
Observability tạo signals; Alert Router quyết định signal nào đáng attention, deduplicate/group/correlate, gán severity/audience và route tới đúng surface. Alert không đồng nghĩa incident: agent retry hoặc CI failure có thể chỉ là warning; production SLO burn hoặc customer-impacting failure có thể escalate thành incident.
Signals → Normalize → Dedup/Group → Correlate → Severity/Audience
   ├─ SDF Console / incident room
   ├─ Pager / on-call
   ├─ GitHub / Linear / Jira projections
   ├─ SDF dashboard / audit
   └─ Incident Manager (khi escalation criteria đạt)
Pager/on-call có nhiệm vụ đánh thức đúng người cho SEV cao; SDF Console/incident room là nơi human + agents cộng tác xử lý; external collaboration adapters chỉ là projection tùy chọn; Backstage/catalog hiển thị service health; Notion nhận postmortem/knowledge sau incident chứ không phải raw alert inbox.
## 10.2 Incident workflow
Incident handling là workflow riêng và có priority cao hơn normal task. Incident Manager phải có quyền interrupt/pause/cancel workflow, freeze deploy, request approval và ưu tiên resources.
DETECTED → TRIAGED → CONTAINED → MITIGATING → RECOVERING → RESOLVED → POSTMORTEM
1.  Detect: Prometheus/Grafana/Sentry/CloudWatch/Kubernetes events/CI/tool errors/evaluator signals.
2.  Triage: gom recent deploy, git diff, logs, metrics, traces, events, past incidents; hypotheses phải gắn evidence.
3.  Contain: giới hạn blast radius trước; low-risk reversible actions có thể auto, irreversible actions cần approval.
4.  Diagnose: có thể fan-out log/trace/git agents rồi merge evidence.
5.  Mitigate/repair: rollback, fix-forward, config revert theo policy/risk.
6.  Validate recovery: command exit 0 không đồng nghĩa incident resolved; phải kiểm SLO/business transaction.
7.  Postmortem: root cause, contributing factors, detection gaps, guardrail gaps, regression test, policy/memory update.

# 11. Execution & Protocol Plane: ACP, A2A, MCP và native fallback
Final architecture không dùng một “agent gateway” proprietary làm contract duy nhất. SDF tách rõ semantic protocol khỏi runtime: ACP điều khiển coding agent, A2A delegate giữa các agent systems/SDF, MCP truy cập tools/resources; sandbox/worker manager vẫn chịu trách nhiệm process, isolation, resource limit và recovery.
## 11.1 Ba protocol boundary
MCP = Agent-to-Tool / Resource
ACP = Client/SDF-to-Coding Agent
A2A = Agent-System-to-Agent-System / SDF-to-SDF

Các protocol không thay Task Manager, Scheduler, State Manager,
Sandbox Manager, Event Bus hoặc Policy Plane.
## 11.2 ACP: primary interface có điều kiện
ACP v1 là stable protocol contract cho client-to-coding-agent. SDF nên pin stable v1, dùng initialize/capability negotiation, stream session/update trực tiếp vào SDF event stream và không giả định mọi ACP agent có cùng capability. Các v2/unstable features chỉ bật sau negotiation và không được trở thành dependency bắt buộc của core.
Task → Router → Agent Interface
                 ├─ ACP v1  (preferred when healthy)
                 └─ Native  (fallback)

ACP event → SDF Event Stream → WebSocket/SSE → SDF Console
           ↘ Evaluation / Audit / Metrics
Độ ổn định phải được đo bằng evidence, không dựa vào việc protocol “đẹp” về kiến trúc. Capability Registry nên giữ latency/reliability theo từng interface: spawn, initialize, session create, time-to-first-agent-event, time-to-first-text, final result, cancellation và failure rate.
## 11.3 Native adapter fallback và interface SLO
Nếu ACP adapter của Codex/Claude/Pi/Hermes chậm, treo hoặc mất event, Router được phép chuyển sang native adapter. Native path không phải failure của architecture; nó là safety valve giúp SDF không khóa critical path vào maturity của một protocol implementation.
agent: codex
interfaces:
  acp:
    enabled: true
    p95_ttft_ms: measured
    reliability: measured
  native:
    enabled: true
    p95_ttft_ms: measured
    reliability: measured

Router chooses by capability + SLO + policy + cost.
## 11.4 A2A: delegation giữa agent systems và SDF
A2A phù hợp cho capability-level delegation: Security Agent, FinOps Agent, Release Agent, Incident Agent hoặc một SDF ở tổ chức khác. A2A Task chỉ là một remote delegation/attempt; SDF Task vẫn là logical work unit giữ dependency, budget, acceptance criteria, attempts, cost, policy và Intent/Decision linkage.
SDF Task
   ↓ Capability Router
A2A Gateway
   ├─ Security Agent
   ├─ FinOps Agent
   ├─ Incident Agent
   └─ Another SDF

A2A boundary → identity + authN/authZ + OPA + data classification + audit
## 11.5 MCP: tool/resource boundary
MCP nằm dưới agent/harness và chỉ nên chuẩn hóa tool/resource access. Tool Proxy/Policy vẫn phải quyết định principal/action/resource/context trước tool call; MCP không được trở thành đường bypass guardrail.
## 11.6 Runtime backends
Protocol interface không thay sandbox/runtime. SDF vẫn cần Worker/Sandbox Manager để provision workspace, process, CPU/RAM/disk/network limits, checkpoint/recovery và cleanup.
- E2B Runtime / Firecracker: hướng sandbox-first cho untrusted/multi-tenant execution; self-host cần Linux/KVM.
- Docker + gVisor: MVP đơn giản hơn nếu threat model thấp hơn.
- Orca: optional engineering cockpit cho worktree/diff/browser/human review; không nằm trong core backend.
- Herdr: optional persistent terminal/session fleet; không làm protocol contract.
- AI SDK Harness/custom adapter: có thể tồn tại như implementation helper, không phải architectural dependency bắt buộc.
## 11.7 Final execution path
SDF Control
    ↓
Capability + Interface Router
    ├──────── A2A → remote/specialist agent system
    │
    └─ coding task
          ↓
       ACP v1 ───fallback──→ Native Adapter
          ↓                     ↓
       Coding Agent / Harness
          ↓
      Sandbox / Worker
          ↓
       Tools via MCP / native tool proxy
          ↓
   Events → SDF Event Stream → Eval / Audit / Console
# 12. Task systems: Jira, Linear, GitHub Issues
Trong mô hình tổ chức đang thiết kế ở đây, ba hệ thống phục vụ ba audience/độ phân giải khác nhau: Jira cho delivery/governance và stakeholders quan tâm tiến độ/năng suất; Linear cho engineering leads/technical decision-makers; GitHub Issues cho internal technical execution. Không cần chọn một và loại hai cái còn lại.


Jira Initiative
   ↓
Linear Engineering Work
   ├─ GitHub Issue #101
   ├─ GitHub Issue #102
   └─ GitHub Issue #103
          ↓
       SDF Task
          ↓
       Attempts / Agents
SDF nên normalize external systems qua Task Provider Adapter và giữ execution-specific state trong Postgres. External ticket systems là human-facing projections, không phải source of truth duy nhất cho machine execution.
# 13. Human Interface & Knowledge: Notion và SDF Console
## 13.1 Notion = curated institutional knowledge
Notion phù hợp làm human-readable knowledge/documentation plane: architecture, product requirements, ADRs, runbooks, postmortems, glossary, onboarding và Project Passport view. Không dùng Notion để lưu PID, retry counter, raw tool traces hoặc execution checkpoint.
OpenViking = machine retrieval / semantic memory
Notion      = curated institutional knowledge
Git/.hoh    = portable canonical project knowledge
Critical knowledge nên được promote từ agent/private memory thành project knowledge. Repo vẫn là portable canonical artifact; Notion và OpenViking là projection tối ưu cho human và machine.
## 13.2 SDF Console = primary human interface
Human collaboration không nên nằm trong critical path của agent execution. SDF Console đọc event stream trực tiếp từ SDF và hiển thị task progress, streaming agent output, approvals, policy decisions, release status, alerts, incidents, cost/economics và Decision Graph.
ACP / Native / A2A events
          ↓
    SDF Event Stream
          ↓
     WebSocket / SSE
          ↓
       SDF Console
          ├─ task / agent streams
          ├─ approvals
          ├─ incident room
          ├─ release / policy / cost views
          └─ Decision Graph navigation
## 13.3 External collaboration adapters are optional
Chat/collaboration products chỉ là adapters/projections: có thể nối Slack/Teams/email/ticketing hoặc một collaboration tool khác khi cần. Buzz bị loại khỏi final core architecture vì external-agent integration path hiện không đạt mức latency/reliability mong muốn trong trải nghiệm thực tế; SDF không được phụ thuộc vào một collaboration frontend để agent trả kết quả.
Chỉ project các event có ý nghĩa cho human như TASK_STARTED, AGENT_BLOCKED, EVALUATION_FAILED, APPROVAL_REQUIRED, INCIDENT_DETECTED, RELEASE_PROMOTION và TASK_COMPLETED; raw token/heartbeat/tool-noise ở observability/event store.
# 14. Software Catalog & Developer Portal
Software Catalog trả lời “chúng ta đang sở hữu/vận hành phần mềm nào, ai chịu trách nhiệm, repo ở đâu, phụ thuộc gì, chạy ở đâu, SLO/runbook/dashboard nào liên quan?”. Backstage là ứng viên OSS tự nhiên; Port/Cortex/OpsLevel là các hướng commercial.
Notion   → Chúng ta biết gì?
GitHub   → Source code ở đâu?
Backstage→ Chúng ta đang vận hành cái gì?
SDF      → Tiếp theo cần làm gì và ai/agent nào làm?
## 14.1 Catalog + dependency/ownership graph
Catalog nên index service/component, owner, repository, APIs, dependencies, environments, deployed version, SLOs, dashboards, incidents, runbooks và Project Passport. Dependency graph giúp Planner và Incident Manager ước lượng blast radius trước khi thay đổi.
Task/Change → affected component → dependencies → owners → environments → policy → evaluation/release gates
## 14.2 Registration lifecycle
Khi SDF tạo service mới, workflow DONE chưa hoàn chỉnh nếu service chưa được register vào catalog và không có owner/repo/runtime/runbook/passport links. Catalog là index; Project Passport vẫn là maintainability package portable.
# 15. CI/CD, Release & Environment Management
SDF không nên trở thành một CI/CD engine imperative. SDF là Release Controller đứng phía trên các deterministic delivery systems: nó tạo/chỉnh pipeline theo template, quan sát evidence, tạo release intent, yêu cầu promotion/approval, đánh giá deployment và kích hoạt rollback/incident khi cần.
## 15.1 CI: reproducible evidence, immutable artifact
PR → lint/test → integration/security/license checks → build → SBOM → sign/attest → immutable OCI artifact
CI nên build một lần và tạo artifact bất biến gắn commit SHA/digest. Không rebuild riêng cho dev/staging/prod. Pipeline definitions nên dựa trên approved templates; agent được phép sửa pipeline nhưng thay đổi phải qua review/evaluation/policy.
## 15.2 Release Manager
Release Manager là entity cấp SDF, tách khỏi GitHub Actions/Argo. Nó liên kết task, commit, artifact digest, CI evidence, security/SBOM/signature, environment promotions, approvals và rollback state.
CREATED → BUILDING → VERIFIED → ARTIFACT_READY
       → DEV → STAGING → WAITING_PROD_APPROVAL
       → PROGRESSIVE_DEPLOY → PRODUCTION
       ↘ FAILED / ROLLED_BACK / QUARANTINED
## 15.3 CD: GitOps + progressive delivery
SDF không trực tiếp kubectl apply production. SDF tạo release/promotion intent → Policy Gate → desired-state Git change → Argo CD reconcile → Argo Rollouts canary/blue-green → metrics/evaluator verify → promote hoặc rollback.
SDF → Release Intent → Policy → Git desired state → Argo CD → Argo Rollouts → Runtime
                                           ↑                    ↓
                                      immutable artifact   Prometheus/SLO/eval
## 15.4 Environment Manager
Environment Manager giữ inventory dev/staging/prod/preview/customer environments: cluster/namespace/region/version/config/secrets references/health/change windows/deployment policy. Environment state không nên được suy ra ngẫu nhiên từ agent chat.
# 16. Policy-as-Code Plane
Policy-as-Code phải tồn tại ở hai tầng: (1) policy kiểm soát chính SDF/agents/tools và (2) Policy Pack được tạo kèm software để sản phẩm tự mang governance contract của nó. Hai tầng có thể dùng chung principles nhưng không cần cùng một engine.
Organization Policy
      ├── SDF Policy → agents/tools/release/access/budget
      └── Product Baseline → generated software
                              ├── app authorization
                              ├── infra/Kubernetes
                              ├── CI/CD/supply chain
                              └── data/operations
## 16.1 SDF meta-policy
Meta-policy trả lời: agent nào được sửa repo nào, có được đọc production logs không, task được spend bao nhiêu, dependency/license nào được thêm, release nào được promote, credential nào được cấp và action nào yêu cầu approval. Decision contract nên chuẩn hóa ALLOW / DENY / REQUIRE_APPROVAL + reason/evidence.
## 16.2 Product Policy Pack
Mỗi generated product nên có policy/ trong repo, gồm policy manifest, source policies, tests, exceptions và evidence schema. Policy Pack là first-class artifact ngang source, tests, SBOM, docs và Project Passport.
product/
├── src/
├── tests/
├── policy/
│   ├── manifest.yaml
│   ├── authorization/
│   ├── infrastructure/
│   ├── kubernetes/
│   ├── cicd/
│   ├── supply-chain/
│   ├── data/
│   ├── exceptions/
│   └── tests/
└── .hoh/project-passport/
## 16.3 Engines theo domain
## 16.4 Policy hierarchy và exceptions
GLOBAL → ORGANIZATION → PRODUCT → ENVIRONMENT → TASK EXCEPTION
Exception không được silently override baseline. Nó phải có scope, reason, approver, task/incident reference và expiry/TTL. High-risk or irreversible exceptions phải human-approved.
## 16.5 Policy ≠ Guardrail ≠ Control ≠ Evidence ≠ Audit
## 16.6 Policy testing & promotion
Policy thay đổi phải được version/test/review/sign/publish như code. Evolver không được tự sửa production/security policy rồi enforce ngay; policy optimization phải qua eval + human/governance gate.
policy change → unit tests → simulation/eval → review → signed bundle → staged rollout → decision-log monitoring
# 17. Recommended OSS/Framework stack

# 18. MVP và Production roadmap
## 18.1 MVP tối thiểu
1.  Task Manager + State Manager trên PostgreSQL.
2.  Workflow graph/controller.
3.  Agent Interface Router: ACP v1 + native fallback cho ít nhất 2 coding agents; capability negotiation + interface SLO metrics.
4.  Workspace/Sandbox + Worker Manager cơ bản; execution events đi thẳng vào SDF Event Stream.
5.  Policy/Guardrail + Tool Proxy.
6.  Evaluator deterministic (tests/build/lint/acceptance).
7.  Observability/tracing.
8.  Memory provider + Context Builder.
9.  Experience Store.
10.  Human approval cho destructive/high-risk actions.
11.  Product Policy Pack + policy tests + decision logging baseline.
12.  Release Manager + immutable artifact promotion + basic Environment Manager.
13.  Project Passport generation + catalog registration gate.
14.  BusinessContext + Offering + basic Cost/Value/Economics model before architecture planning.
15.  FinOps baseline: cost attribution + per-task/model spend + basic budget/alerting.
16.  Economic constraints gate cho architecture/release và Project Passport economics/finops artifacts.
## 18.2 Chỉ thêm khi có nhu cầu

# 19. Resource & Deployment Sizing
Resource requirement của SDF nên được tính theo active agent concurrency và mức độ self-host, không chỉ theo số user. SDF control core tương đối nhẹ; sandbox/build workload, observability stack và local inference mới là các cost/resource driver lớn.
## 19.1 Nguyên tắc capacity planning
- API-first: model inference nằm ngoài host nên không cần GPU; ưu tiên dùng managed LLM, CI và observability khi đang prove architecture.
- Execution-first scaling: scale worker/sandbox pool theo active concurrency; không vertical-scale Control Plane vô hạn.
- GPU thuộc Inference Plane, không phải requirement mặc định của SDF.
- E2B/Firecracker self-host cần Linux/KVM; macOS phù hợp dev/control nhưng nên dùng remote Linux worker cho microVM execution.
## 19.2 Deployment profiles
## 19.3 Concurrency model
RAM_total ≈ base_infra
          + N_active × (sandbox_RAM + agent_runtime_RAM + build_test_RAM)
          + safety_headroom

CPU_total ≈ base_CPU + active_compute_heavy_tasks

Agent đang chờ model API thường ít CPU; build/test/container/terraform
mới tạo CPU/disk/network pressure lớn.
Planning estimate ban đầu cho một coding sandbox thông thường: khoảng 1–2 vCPU và 2–4 GB RAM; workload nặng như Java/Node build, Docker build, integration test hoặc IaC validation có thể cần 2–4 vCPU và 4–8 GB RAM. Đây là capacity estimate cần benchmark lại bằng workload thực tế, không phải guarantee của runtime.
## 19.4 Storage & network
Storage thường tăng nhanh do worktrees, container layers, snapshots, traces/logs, CI artifacts, OpenViking/project memory, SBOM/provenance và raw FinOps data. Giữ metadata/state trong Postgres, artifacts/raw telemetry trong object storage, analytics lớn trong ClickHouse/warehouse. Dùng package/container/git/CI caches vì concurrent git clone, npm install và image pull có thể bottleneck trước CPU.
## 19.5 Local inference
SDF / Workers  ─────→  Inference Plane
                         ├─ model server(s)
                         └─ GPU pool

Không colocate large local models với databases/sandbox workers nếu có thể.
Nếu dùng OpenAI/Anthropic/Gemini hoặc provider API, GPU = 0 là cấu hình hợp lý. Nếu self-host coding/VLM models, VRAM/throughput trở thành một capacity domain riêng và phải sizing theo model, quantization, context length và concurrent inference.
## 19.6 Cấu hình khởi đầu được khuyến nghị
Phase 1 — prove SDF
8–12 CPU / 16–32 GB / 250–500 GB SSD
LLM API + managed sandbox/observability/CI

Phase 2 — own execution
Remote Linux worker: ~16 CPU / 64 GB / 1 TB NVMe / KVM
5–15 concurrent coding agents tùy workload benchmark

Phase 3 — production
Control pool + Data pool + N×Execution workers + Object Storage
Scale by active concurrency and measured SLO, not by guess.
# 20. Commercial licensing & generated software
Một software được SDF sinh ra không tự động kế thừa license của SDF hoặc tool chỉ vì tool đó được dùng để tạo code. Rủi ro license nằm ở code/dependency thực sự đi vào output, cách OSS component được link/modified/distributed và điều khoản của từng dependency.
BUILD SYSTEM / SDF
  OSS runtimes / memory / agents
          │
          │ generate
          ▼
──────── OUTPUT BOUNDARY ────────
          ▼
Generated Software
  source + tests + dependencies + images + IaC
  + SBOM/provenance + Project Passport + Policy Pack
- Build-time dependency ≠ product dependency.
- Không để SDF dependency âm thầm trở thành dependency của generated product.
- Nếu agent copy substantial OSS code hoặc generated app import/link copyleft library, đó là license issue của output.
- Thêm License & Provenance Gate trước khi task/product được đánh dấu DONE.
- Ưu tiên MIT/Apache/BSD cho core commercial/proprietary stack; review kỹ LGPL/MPL/AGPL/proprietary terms.
- License status của OSS thay đổi theo version; cần re-verify ở thời điểm release và legal review cho AGPL/deep integration.
# 21. Handoff, maintainability và Project Passport
Khi phần mềm được bàn giao cho người khác có SDF nhưng không có lịch sử task/memory/context ban đầu, mục tiêu không phải tái tạo identical conversational history. Mục tiêu là tạo equivalent operational understanding.

## 21.1 Project Handoff Capsule / Project Passport
project/
├── source code
└── .hoh/
    ├── project.yaml
    ├── architecture.md
    ├── decisions/          # ADRs
    ├── intent-graph.json   # portable decision/intent snapshot
    ├── assumptions.yaml    # business/economic/technical hypotheses
    ├── evidence/            # durable evidence refs/summaries
    ├── runbooks/
    ├── invariants.yaml
    ├── capabilities.yaml
    ├── open-work.yaml
    ├── known-issues.yaml
    ├── provenance/
    ├── baseline-tests/
    ├── release-manifest.yaml
    ├── catalog.yaml
    └── memory.snapshot
policy/
├── manifest.yaml
├── tests/
└── exceptions/

## 21.2 Memory portability levels

## 21.3 Knowledge Distiller
Knowledge Distiller chạy liên tục sau task/evaluation, không chờ tới ngày handoff. Nó quyết định knowledge nào là noise, memory nào cần giữ, và khi nào promote thành ADR/runbook/invariant/known issue/economic assumption. Khi một durable decision được promote, Intent/Decision Graph cũng phải cập nhật edge tới source task, ADR, evidence và release tương ứng để “why” không bị mất trong handoff.
TASK → EXECUTE → EVALUATE → LEARN
                         ↓
             durable knowledge?
                 │ yes
                 ▼
         PROMOTE TO PROJECT MEMORY
      ADR / RUNBOOK / INVARIANT / ISSUE
## 21.4 Onboarding cho SDF mới
IMPORT REPO + PASSPORT
        ↓
SCAN REPO / DEPENDENCIES
        ↓
READ ARCHITECTURE + ADR + INVARIANTS
        ↓
REBUILD CODE/DEPENDENCY GRAPH
        ↓
IMPORT PROJECT MEMORY (optional)
        ↓
RUN BASELINE TESTS
        ↓
VERIFY DEPLOY/OPERATIONS
        ↓
READY TO MAINTAIN
# 22. Reference data model
## 22.1 Task
Task {
  id
  objective_id
  parent_task_id
  title
  description
  task_type
  status
  priority
  acceptance_criteria
  required_capabilities
  budget
  external_refs[]
  created_at / updated_at
}
## 22.2 Attempt / Dispatch
Attempt {
  id
  task_id
  agent
  model
  execution_provider
  dispatch_or_session_id
  workspace_id
  started_at / finished_at
  status
  cost / tokens / latency
  evaluation_id
  failure_reason
}
## 22.3 Evaluation
Evaluation {
  task_id
  attempt_id
  status: pass | fail | replan
  criteria[]
  evidence[]
  regressions[]
  security_findings[]
  feedback
  score_vector
}
## 22.4 Incident
Incident {
  id
  severity
  service
  signal
  state
  timeline[]
  hypotheses[]
  evidence[]
  containment_actions[]
  recovery_actions[]
  validation
  postmortem_ref
}
## 22.5 Release
Release {
  id
  application
  source_task
  commit_sha
  artifact_digest
  ci_evidence[]
  policy_decisions[]
  environments{}
  approvals[]
  status
  rollback_target
}
## 22.6 PolicyDecision
PolicyDecision {
  id
  policy_id / version
  principal
  action
  resource
  context
  decision: allow | deny | require_approval
  reason
  evidence[]
  exception_ref
  timestamp
}
## 22.7 BusinessContext
BusinessContext {
  id / product_id / customer_segment
  industry / scale / usage_metrics{}
  economic_buyer / budget_owner / procurement_limit
  pain_points[] / current_process
  business_criticality / SLA / compliance[]
  alternatives[] / switching_cost
  assumptions[] / confidence
}
## 22.8 OfferingEconomics
OfferingEconomics {
  offering_id / product_id / business_context_id
  creation_cost / monthly_cogs / maintenance_cost
  value_components[] / total_value_range
  economic_floor / competitive_range / value_based_range
  recommended_price_corridor
  gross_margin / customer_roi / payback
  pricing_metric / contract_model
  confidence / uncertainty[]
}
## 22.9 FinOpsRecord / CostAllocation
CostRecord {
  provider / service / resource_id
  usage_quantity / usage_unit
  billed_cost / effective_cost / currency
  product / service / environment / owner
  task_id / attempt_id / agent / model
  timestamp
}

CostAllocation {
  shared_resource
  allocation_strategy / metric
  targets[] / percentages{}
  policy_version
}
## 22.10 FinOpsSignal
FinOpsSignal {
  id / type: budget | forecast | anomaly | optimization
  scope / resource / owner
  expected / actual / variance
  forecast_impact
  related_release / related_task
  evidence{} / confidence / risk
  recommended_actions[]
}
## 22.11 IntentDecisionNode / IntentDecisionEdge
IntentDecisionNode {
  id
  type: objective | assumption | constraint | policy | adr | requirement | task | change | release | metric | evidence
  title / statement / status
  owner / source_ref
  confidence
  created_at / superseded_at
}

IntentDecisionEdge {
  from_id / to_id
  relation: motivates | constrains | implements | depends_on | supersedes | validates | contradicts | measures | caused_by
  evidence_refs[]
  confidence
  created_at
}
# 23. End-to-end reference flows
## 23.1 Normal coding task
Jira / Linear / GitHub
        ↓
Task Provider Adapter
        ↓
Task Manager + Postgres
        ↓
Planner
        ↓
Context Builder ← Project Memory
        ↓
Router ← Capabilities + History + Budget
        ↓
Policy / Guardrail
        ↓
Agent Interface Router
        ↓
Sandbox + Agent
        ↓
Output Validator
        ↓
Evaluator
   ┌────┴────┐
 PASS       FAIL
  ↓          ↓
DONE       REPAIR / REPLAN
  ↓
Experience Store
  ↓
Knowledge Distiller / Evolver
## 23.2 Incident flow
Telemetry / Alerts / Agent Failures
                ↓
         Incident Detector
                ↓
          Incident Manager
                ↓
     DETECT → TRIAGE → CONTAIN
                ↓
             DIAGNOSE
                ↓
         MITIGATE / RECOVER
                ↓
             VALIDATE
                ↓
            POSTMORTEM
                ↓
 Memory + Regression + Policy + Evolver
## 23.3 Handoff flow
SDF A builds product
        ↓
Distill durable knowledge continuously
        ↓
Generate Project Passport + Policy Pack + SBOM/provenance + baselines
        ↓
Export release/catalog metadata + current open work
        ↓
Deliver source + artifacts + portability package
        ↓
SDF B imports and reconstructs operational understanding
        ↓
Run policy tests + baseline verification
        ↓
Maintenance continues
## 23.4 Build → Release → Production
PR
 ↓
CI + tests/security/license/policy
 ↓
Immutable artifact + SBOM + signature
 ↓
Release Manager
 ↓
Policy / approval
 ↓
Git desired state
 ↓
Argo CD / Rollouts
 ↓
Metrics + Evaluator
 ├─ PASS → promote
 └─ FAIL → rollback + incident
## 23.5 Durable knowledge & collaboration projection
Task/Incident/Release outcome
       ↓
Knowledge Distiller
  ├─ Git/.hoh + policy/   (portable canonical)
  ├─ Notion               (human curated projection)
  ├─ OpenViking           (machine retrieval projection)
  ├─ Backstage            (software catalog projection)
  └─ SDF Console            (human incident/activity projection)
## 23.6 Business → Build → Economics feedback
Business Context
   ↓
Business Economics (value / cost / market / price corridor)
   ↓
Economic constraints
   ↓
Planner / Architecture
   ↓
Build / Release / Operate
   ↓
Actual cost + usage + SLO + product metrics
   ↓
FinOps + Business Economics Evaluator
   ↓
Adapt architecture / routing / pricing recommendation / optimization
## 23.7 Intent traceability & evidence feedback
Business Objective / Hypothesis
        ↓
Economic / Policy / Product Constraint
        ↓
ADR / Architecture Decision
        ↓
Requirement → Task → Change / PR
        ↓
Release → Runtime
        ↓
Metrics / Cost / SLO / Incidents / Product Usage
        ↓
Evaluator attaches Evidence
        ↓
Decision Graph
   ├─ validates assumption → keep
   ├─ contradicts assumption → replan
   └─ evidence insufficient → experiment / observe
# 24. Design principles đã chốt
- SDF là control/learning system; không phải một agent monolith.
- Memory giữ “what we know”; Intent/Decision Graph giữ “why the system is this way” và phải trace hai chiều từ business intent tới runtime evidence.
- Hypothesis, assumption và model-generated inference không được biến thành fact nếu thiếu evidence/provenance/confidence.
- Mọi durable architecture/business decision quan trọng phải có path tới implementation/release và evidence xác minh; khi bị thay thế phải dùng supersedes thay vì xóa lịch sử.
- Task Manager, Scheduler, Router, State Manager và Agent Interface Router phải là khái niệm tách biệt.
- Agent sinh output ≠ agent quyết định output đúng.
- Guardrails phải chặn trước hành động nguy hiểm; Evaluator xác minh sau execution.
- Memory không phải state; context không phải memory dump.
- Agent interface và runtime phải pluggable: ACP v1 là portable primary interface khi đạt SLO; native adapter là fallback; sandbox/runtime có thể thay mà không đổi core SDF.
- Incident handling là priority workflow riêng.
- Evolution chỉ xảy ra dựa trên evidence; security/policy changes phải có kiểm soát.
- Human task systems là projections của work graph; execution state vẫn thuộc SDF.
- Commercial architecture cần license boundary và provenance gate từ đầu.
- Generated software phải đi kèm maintainability package/Project Passport.
- Critical project knowledge phải portable, human-readable và machine-readable; không bị khóa trong vector DB hoặc agent memory.
- Policy là first-class domain: policy khai báo rule; guardrail enforce; control là mechanism; evidence chứng minh; audit giải thích lịch sử.
- Generated software phải mang theo Policy Pack và policy tests; governance không được chỉ sống trong SDF đã tạo ra nó.
- SDF là Release Controller, không phải CI/CD engine; production promotion dựa trên immutable artifact + deterministic delivery systems.
- Git/.hoh + policy/ là portability boundary; Notion/OpenViking/Backstage/SDF Console và external collaboration adapters là các projection có thể thay thế.
- Software Catalog là index của operational ownership; Project Passport là gói maintainability portable.
- Business Context và Business Economics phải đi trước architecture; pricing/FinOps không phải add-on ở cuối vòng đời.
- Product khác Offering: SLA/support/security/compliance/deployment/contract thay đổi COGS, risk và price dù codebase giống nhau.
- Cost cho biết price floor; customer value cho biết value ceiling; market/buyer/contract quyết định price corridor thực tế.
- FinOps phải tối ưu cost per accepted outcome và unit economics, không chỉ provider bill hoặc cost per token.
- Cost attribution, tags/labels và economic baselines là first-class generated artifacts để handoff sang SDF khác.
- Alert ≠ incident: Alert Router xử lý signal/severity/audience; Incident Manager chỉ nhận các tình huống cần coordinated response.
- ACP/A2A/MCP là ba boundary khác nhau: coding-session, agent-system delegation và tool/resource integration; không dùng một protocol để thay toàn bộ orchestration stack.
- Protocol reliability phải đo bằng SLO: spawn/init/session/TTFT/final/cancel/failure; Router được phép chọn native path khi adapter không đạt SLO.
- Human collaboration không nằm trên execution critical path; agent events phải đi trực tiếp vào SDF Event Stream rồi mới project ra Console/chat/ticketing.
- Capacity planning dựa trên active agent concurrency, sandbox/build workload và telemetry volume; GPU chỉ thuộc local inference strategy.
# 25. Kiến trúc đề xuất cuối cùng
BUSINESS CONTEXT
               Customer / Use case / Market / Contract
                              ↓
                    BUSINESS ECONOMICS
             Cost / Value / Alternatives / Pricing
                              ↓
                    Economic Constraints
                              ↓
                  INTENT / DECISION GRAPH
 Objective -- Assumption -- Constraint -- ADR -- Requirement -- Task
      ↑                                                   ↓
 Evidence / Metrics ← Evaluation ← Release ← Change / Code / IaC
                              ↓
Jira / Linear / GitHub Issues      Notion / SDF Console / Backstage
             \                         /
              └────── HUMAN PLANES ───┘
                              ↓
                    ┌──────────────────────┐
                    │      SDF CONTROL     │
                    │ Task / Planner       │
                    │ State / Router       │
                    │ Scheduler            │
                    │ Release / Env Mgr    │
                    └──────────┬───────────┘
                               │
        ┌──────────────────────┼──────────────────────┐
        ↓                      ↓                      ↓
 Memory / Context         Policy Plane         Quality / Learning
 OpenViking              OPA/Cedar/...         Eval / Experience
 Context Builder         Policy Registry       Evolver
        └──────────────────────┼──────────────────────┘
                               ↓
                    PROTOCOL / INTEROP LAYER
          ┌────────────────────┼────────────────────┐
          ↓                    ↓                    ↓
         A2A                  ACP                  MCP
 Agent/SDF delegation   Coding agent session   Tools/resources
          │                    │                    │
          │             Native fallback            │
          │                    ↓                    │
          └──────────→ Coding Agents / Harnesses ←─┘
                               ↓
                     Sandbox / Worker Manager
                               ↓
                       Source / PR / Code
                               ↓
             CI → signed immutable artifact → Release
                               ↓
                    GitOps / Environments
                               ↓
                  Observability / Alert Router
                               ↓
                    Incident / Runtime Metrics
                               ↓
                 FINOPS / ACTUAL ECONOMICS
        Cost attribution / forecast / unit economics / optimization
                               ↓
         Runtime evidence updates Decision Graph → ADAPT

HUMAN INTERFACE
ACP/A2A/native events → SDF Event Stream → WebSocket/SSE → SDF Console
External chat/ticket/email adapters are optional projections.

PORTABLE OUTPUT
source + tests + SBOM/provenance + Project Passport + Policy Pack
+ Intent/Decision Graph snapshot + business context + economics/ + finops/ + release/catalog metadata

# 26. Những điểm cần xác minh trước khi production
- Re-verify exact license/version của mọi OSS dependency tại thời điểm release; không dựa vào một snapshot thảo luận.
- Threat model: local single-user, enterprise internal, multi-tenant SaaS và untrusted code cần sandbox khác nhau.
- Benchmark runtime thực tế trên workload của bạn: RAM/CPU/disk/session count; không chỉ dựa vào architecture hoặc anecdotal reports.
- Define acceptance criteria/eval suite trước khi bật Evolver.
- Define project-memory promotion policy và retention/redaction cho sensitive context.
- Define task hierarchy và mapping Jira/Linear/GitHub theo organization thực tế.
- Test crash/recovery, duplicate dispatch, idempotency và partial failure của SDF itself.
- Legal review một lần cho AGPL/copyleft components và output provenance nếu bán proprietary B2B product.
- Xác định policy hierarchy, exception TTL/approval model, policy unit tests và staged rollout trước khi autonomous agents có production access.
- Kiểm thử release idempotency, immutable artifact promotion, canary abort/rollback và khả năng phục hồi khi environment drift.
- Xác định knowledge nào là canonical trong Git và knowledge nào chỉ được project sang Notion/OpenViking/Backstage/SDF Console/external adapters; mọi projection phải có thể tái tạo.
- Đưa catalog registration cùng owner/runbook/passport links vào tiêu chí DONE của service.
- Define canonical BusinessContext/Offering schema và ai chịu trách nhiệm xác nhận assumptions/value evidence trước khi Planner dùng chúng.
- Define price/economics confidence model; pricing output phải là corridor + uncertainty, không phải một con số AI giả chính xác.
- Validate cost attribution quality và tỷ lệ unallocated/shared cost trước khi dùng FinOps data để tự động route/optimize.
- Benchmark estimated vs actual infrastructure cost và calibrate Infracost/FOCUS/OpenCost mappings theo cloud/provider thực tế.
- Define FinOps remediation levels và approval boundary; không cho autonomous cost optimization xóa/downsize production resource không reversible.
- Test Alert Router dedup/correlation/escalation để tránh notification storm và bảo đảm SEV cao thật sự tới on-call.
- Benchmark cùng workload qua native vs ACP cho từng critical agent; đặt SLO cho initialize/session/TTFT/final/cancel và tự động fallback khi protocol path xuống cấp.
- Pin ACP stable v1 và A2A released 1.x contract; unstable/preview protocol features phải capability-gated và không được thành dependency bắt buộc.
- Threat-model A2A như network/organization boundary: identity, mTLS/OAuth as applicable, OPA authorization, data classification, budget và audit trước delegation.
- Load-test SDF Event Stream/Console tách biệt khỏi agent execution để UI/chat outage không làm mất hoặc block result.
- Benchmark active sandbox density trên worker thực tế; capacity plan phải dựa trên p95 RAM/CPU/disk/network của build/test workload, không chỉ idle agent count.
# 27. Glossary

— End of document —
| Mục tiêu: Thiết kế một SDF thương mại có thể điều phối nhiều coding agent/harness, chạy an toàn, đo được chất lượng, học từ kinh nghiệm, và bàn giao sản phẩm cho SDF/human khác mà không phụ thuộc vào lịch sử private của hệ thống ban đầu. |
| --- |
| Nguyên tắc cốt lõi: State Manager nhớ “đang làm gì”; Memory Manager nhớ “đã biết gì”; Context Builder quyết định “agent cần thấy gì”; Evaluator xác minh “kết quả có đạt không”; Evolver thay đổi behavior dựa trên evidence; Guardrail quyết định “hành động có được phép xảy ra không”. |
| --- |
| Không nên build SDF: Nếu nhu cầu chỉ là gọi Codex rồi gọi Claude tuần tự. Shell script hoặc CI pipeline đơn giản đã đủ. SDF đáng build khi cần state machine/graph, shared memory/context, policy, observability, evaluation, recovery và learning. |
| --- |
| Plane | Trách nhiệm chính | Thành phần |
| --- | --- | --- |
| Business Context & Economics Plane | Mô hình customer/use case/value/market/offering và đặt economic constraints trước khi thiết kế software. | Business Context, Value Model, Alternative Model, Offering Model, Pricing/ROI, Business Economics Evaluator |
| FinOps / Cost Governance Plane | Thu thập, phân bổ, forecast, cảnh báo và tối ưu chi phí của SDF lẫn generated product. | FOCUS cost model, Attribution, Budget, Forecast, Anomaly, Unit Economics, Optimization, FinOps Policy |
| Control Plane | Quyết định work được phân rã, sắp lịch và route như thế nào. | Planner, Task Manager, Router, Scheduler, State Manager, Capability Registry |
| Memory Plane | Lưu knowledge dài hạn và xây context phù hợp cho từng agent. | Memory Manager/Provider, Context Builder, Project Knowledge |
| Execution Plane | Chạy coding agents trong workspace/sandbox có isolation và lifecycle rõ ràng. | ACP Client, Native Adapter, Workspace Manager, Sandbox/Worker Runtime, Agents |
| Safety Plane | Ngăn hành động không hợp lệ hoặc nguy hiểm. | Validator, Policy Engine, Guardrail, Secrets, Budget, Human Approval |
| Quality & Learning Plane | Đo correctness và dùng evidence để cải thiện hệ thống. | Evaluator, Experience Store, Benchmark Suite, Evolver |
| Operations Plane | Vận hành SDF như một distributed system. | Observability, Alert Router, Audit, Checkpoint/Recovery, Incident Manager |
| Portability & Continuity Plane | Đảm bảo sản phẩm có thể được SDF/human khác tiếp quản. | Project Passport, ADR, Runbook, SBOM, Provenance, Memory Export, Baseline Tests |
| Human Interface & Knowledge Plane | Cho human quan sát, approve, xử lý incident và đọc knowledge được curate mà không nằm trên critical execution path. | SDF Console, Notion, External Collaboration Adapters, Knowledge Distiller |
| Software Catalog Plane | Index software/service: owner, repo, APIs, dependencies, runtime, environments, SLOs, runbooks. | Backstage hoặc catalog tương đương, dependency/ownership graph |
| Release & Environment Plane | Biến verified artifact thành release có kiểm soát qua environments, promotion, canary và rollback. | CI, Artifact Registry, Release Manager, Environment Manager, Argo CD/Rollouts |
| Policy & Governance Plane | Định nghĩa policy hierarchy, phân phối policy, decision logs và Policy Pack cho generated products. | OPA/Rego, Conftest, Kyverno, Cedar, Policy Registry, Evidence/Audit |
| Protocol & Interop Plane | Chuẩn hóa giao tiếp giữa SDF, coding agents, agent systems và tools; protocol failure không được làm mất khả năng thực thi. | ACP v1, A2A 1.0, MCP, capability negotiation, native fallback |
| Khái niệm | Câu hỏi nó trả lời |
| --- | --- |
| Task Manager | Work là gì? Lifecycle hiện tại là gì? Parent/child/dependency/acceptance criteria ra sao? |
| Scheduler | Khi nào chạy? Resource/priority/concurrency thế nào? |
| Router | Agent/model/execution backend nào phù hợp? |
| State Manager | Trạng thái hệ thống hiện tại là gì? |
| Agent Interface Router | ACP/native/A2A interface và execution backend nào phù hợp? |
| LangGraph/Workflow Engine | Flow xử lý task đi qua những node/transition nào? |
| Storage: PostgreSQL là source of truth tốt cho current/system state. Git/filesystem giữ code state và artifacts lớn. Không nhét toàn bộ source/diff lớn vào database. |
| --- |
| Registry field | Ví dụ |
| --- | --- |
| Capabilities | implementation, review, long_context, browser, terraform, kubernetes |
| Tools | shell, apply_patch, browser, MCP tools |
| Constraints | max context, auth mode, sandbox support |
| Economics | cost class, rate limits, expected latency |
| Runtime | local, remote, sandbox type, available machines |
| Observed performance | success rate, retries, eval scores by task type |
| Loại memory | Mục đích |
| --- | --- |
| Short-term | Context của task/session hiện tại. |
| Episodic | Các sự kiện/task/incident đã xảy ra trước đây. |
| Semantic | Kiến thức ổn định về project: architecture, conventions, domain. |
| Procedural | Cách hệ thống nên làm việc: apply_patch, test-before-merge, approval rules. |
| Distinction: State Manager nhớ “đang làm gì”. Memory Manager nhớ “đã biết gì”. Context Builder quyết định “agent cần thấy gì”. |
| --- |
| Ba câu hỏi khác nhau: Validator: “đúng format/contract không?” • Guardrail: “được phép không?” • Evaluator: “kết quả có đạt mục tiêu không?” |
| --- |
| Loại evolution | Ví dụ |
| --- | --- |
| Memory evolution | Deduplicate, promote, decay/forget low-value memory. |
| Prompt evolution | Cải thiện instructions/templates dựa trên eval dataset. |
| Routing evolution | Học agent/model nào phù hợp task nào. |
| Workflow evolution | Thêm planning/review/security step cho task type hay fail. |
| Tool evolution | Exact edit thường fail → chuyển multi-line edits sang apply_patch. |
| Evaluator evolution | Incident lọt qua → thêm regression test/check mới. |
| Fitness: Không tối ưu chỉ cho pass rate. Fitness nên đa mục tiêu: correctness + reliability + coverage − cost − latency − retries − unnecessary changes. |
| --- |
| Nguyên tắc: Agent task workflow tối ưu completion; Incident Manager tối ưu safety và recovery. Khi xung đột, incident workflow thắng. |
| --- |
| Protocol | Vai trò trong SDF | Không nên dùng để thay |
| --- | --- | --- |
| ACP v1 | Session/prompt/update/permission/cancel với coding agents; primary portable interface khi adapter đạt SLO. | Scheduler, sandbox lifecycle, task source-of-truth |
| A2A 1.0 | Discovery/delegation giữa specialist agents, remote agent systems hoặc SDF khác. | Internal event bus, SDF Task Manager, low-level shell/tool calls |
| MCP | Chuẩn tool/resource integration cho agent. | Agent orchestration hoặc coding-session lifecycle |
| Native adapter | Fallback/escape hatch khi protocol adapter chậm, thiếu capability hoặc không ổn định. | Không phải protocol cross-vendor; cần maintain theo agent |
| System | Audience/Plane | Thông tin nên hiển thị |
| --- | --- | --- |
| Jira | Business / Delivery / Governance Plane | Progress, ownership, risks, dependency, delivery, productivity/compliance view |
| Linear | Engineering Decision Plane | Engineering intent, technical coordination, architecture decisions, milestones, technical risks |
| GitHub Issues | Technical Execution Plane | Implementation-level tasks, code scope, tests, PR/commit relationship |
| SDF Task Manager | Machine Execution Plane | Attempts, agent/model, dispatch, retries, eval evidence, cost, checkpoint |
| Notion | Knowledge / Documentation Plane | Architecture, product requirements, ADRs, runbooks, postmortems, onboarding, curated Project Passport views. |
| SDF Console | Human Interface / Operations Plane | Realtime task/agent streams, approvals, policy/release/incident views, cost/economics and Decision Graph; fed directly from SDF event stream. |
| Backstage | Software Catalog / Developer Portal Plane | Service inventory, ownership, repo/APIs, dependencies, environments, SLO/runbook links; reconstruction entry point. |
| Quan trọng: Không sync 1:1 Jira = Linear = GitHub Issue. Chúng là các projection khác nhau của cùng một work graph. |
| --- |
| Responsibility | Đề xuất v1 |
| --- | --- |
| CI trigger | GitHub Actions |
| Portable pipeline logic | Dagger hoặc reusable workflow/templates |
| Artifacts | OCI registry |
| SBOM / vulnerability | Syft + Grype/Trivy |
| Signing / attestations | Cosign / Sigstore |
| Release state | SDF Release Manager + PostgreSQL |
| Desired state / CD | Git + Argo CD |
| Progressive delivery | Argo Rollouts |
| Metrics gates | Prometheus + evaluator |
| Environment inventory | SDF Environment Manager + Catalog links |
| Cost estimation / release economics | Infracost + SDF FinOps Policy |
| Cost / SLO gate | Prometheus + evaluator + FinOps evaluator |
| Domain | Engine/Stack | Vai trò |
| --- | --- | --- |
| SDF/general policy | OPA / Rego | Structured decisions cho agent/tool/release/budget/access. |
| Pre-runtime config | Conftest + OPA | Check Terraform plan, Kubernetes/YAML/JSON/CI config trong CI. |
| Kubernetes admission | Kyverno hoặc OPA/Gatekeeper | Runtime admission/mutation/verification ở cluster boundary. |
| Application authorization | Cedar hoặc OPA | Fine-grained principal/action/resource/context authorization. |
| Policy distribution | Policy Registry + signed bundles | Versioning, rollout, rollback và provenance của policy. |
| Khái niệm | Ý nghĩa |
| --- | --- |
| Policy | Rule/intent: ví dụ “production chỉ chạy signed image”. |
| Guardrail | Enforcement decision/block trước hành động nguy hiểm. |
| Control | Mechanism thực thi: admission controller, CI gate, auth middleware. |
| Evidence | Chứng cứ compliance: signature, SBOM, test result, approval. |
| Audit | Lịch sử ai/agent làm gì, policy nào quyết định và kết quả ra sao. |
| Mảnh ghép | Đề xuất | Ghi chú |
| --- | --- | --- |
| Workflow graph / SDF controller | LangGraph hoặc custom state machine | LangGraph phù hợp graph/checkpoint/HITL; giữ domain logic của SDF riêng. |
| Durable workflow (khi scale) | Temporal | Chỉ thêm khi có long-running workflow, crash/restart, distributed workers. |
| State / Task / Experience | PostgreSQL | Source of truth cho machine state và analytics. |
| Memory / Context | OpenViking hoặc pluggable MemoryProvider | Giữ abstraction để tránh lock-in/license constraints. |
| Coding-agent interface | ACP v1 + native adapters | ACP preferred when capability/SLO pass; native fallback mandatory for critical agents. |
| Sandbox | E2B Runtime / Docker+gVisor / Firecracker | Chọn theo threat model và multi-tenancy. |
| Human coding cockpit | Orca (optional) | Không bắt buộc trong core backend. |
| Terminal fleet runtime | Herdr (optional) | Phù hợp headless/remote sessions; audit license. |
| Policy engine | OPA | ALLOW/DENY/APPROVAL decisions. |
| MCP / Tool gateway | MCP + Tool Proxy; ContextForge/agentgateway optional | MCP standardizes tools/resources; Policy/Tool Proxy remains enforcement boundary. |
| Model gateway/budget | LiteLLM Proxy | Provider abstraction, fallback, budget/routing. |
| Validation | Pydantic / JSON Schema | Task/tool/output contracts. |
| Evaluator | Inspect AI + native tests | Benchmark/coding-agent eval. |
| Security eval | Promptfoo | Injection/tool abuse/security regression. |
| LLM observability | Langfuse | Prompt/context/tool/model/eval traces. |
| System observability | OpenTelemetry + Prometheus + Tempo/Loki + Grafana | Metrics/traces/logs. |
| Secrets | OpenBao hoặc equivalent | Dynamic/short-lived credentials. |
| Incident signals | Prometheus + Alertmanager | Detection/grouping/routing; SDF tự quản incident state machine. |
| Evolution | DSPy/GEPA cho prompt/program optimization | Chỉ dùng sau khi có eval dataset đáng tin; không tự sửa security policy. |
| License/SBOM | Syft + ScanCode + ORT + OPA policy | Gate generated product trước DONE. |
| Institutional knowledge | Notion (optional) | Human-curated docs/ADRs/runbooks/passport projection; repo remains portable canonical source. |
| Human interface | SDF Console + optional external adapters | Direct WebSocket/SSE event stream; no collaboration product on execution critical path. |
| Software catalog | Backstage | Service/component inventory, ownership, dependencies, environments, SLO/runbook links. |
| CI trigger | GitHub Actions | Use reusable/approved templates; keep CI deterministic. |
| Portable CI logic | Dagger or reusable workflows | Reduce CI-provider coupling and improve local reproducibility. |
| Artifact registry | OCI registry (GHCR/Harbor/etc.) | Immutable artifact digest promoted across environments. |
| Supply-chain signing | Cosign / Sigstore | Sign/attest artifacts and provenance. |
| CD / desired state | Argo CD | GitOps reconciliation; avoid agent direct-deploy to production. |
| Progressive delivery | Argo Rollouts | Canary/blue-green + metric analysis + rollback. |
| Release / Environment state | Custom SDF services + PostgreSQL | Release lifecycle and environment inventory belong to SDF domain model. |
| Product policy pack | OPA/Rego + Conftest + Kyverno + Cedar as needed | Choose engine by domain; ship policy + tests with generated product. |
| Business context / economics | Custom SDF domain model + PostgreSQL | BusinessContext, Offering, Value/Alternative/Pricing models are core SDF domain data. |
| Cost normalization | FOCUS-compatible schema | Normalize cloud/SaaS/AI costs into comparable billed/effective cost dimensions. |
| Kubernetes cost allocation | OpenCost | Runtime CPU/RAM/GPU/PV allocation and label-based cost attribution. |
| IaC cost estimation | Infracost | Shift-left cost estimates and PR/release cost deltas. |
| Cloud cost governance/remediation | Cloud Custodian (optional) | Policy-driven tagging, cleanup and cloud resource governance; only when needed. |
| FinOps analytics store | PostgreSQL → ClickHouse/warehouse at scale | Keep raw exports in object storage; use analytical DB when volume/query patterns require it. |
| Cost dashboards | Grafana | Unit economics, budget, forecast, attribution and anomaly visualization. |
| Alert routing | Alertmanager + SDF Alert Router | Dedup/group/severity/audience routing; incident creation remains SDF domain logic. |
| Intent / Decision Graph | PostgreSQL edge model first | Nodes/edges + evidence refs in core DB; only add a dedicated graph engine when traversal scale/analytics justify it. |
| Agent-system interoperability | A2A 1.0 adapter/gateway | Use for specialist agents, remote agent systems and SDF--SDF delegation; keep SDF Task as source-of-truth logical work. |
| Protocol telemetry | OpenTelemetry + SDF interface metrics | Measure spawn/init/session/TTFT/final/cancel/failure per ACP/native/A2A interface. |
| Nhu cầu thực tế xuất hiện | Thêm |
| --- | --- |
| Workflow kéo dài hàng giờ/ngày, cần durable resume | Temporal |
| Distributed worker fleet/event-driven scale | NATS/queue hoặc Temporal task queues |
| Multi-tenant/untrusted code mạnh | Firecracker/E2B microVM isolation |
| Đủ eval history để tối ưu tự động | DSPy/GEPA + Evolver |
| Human needs deep coding cockpit | Orca |
| Nhiều persistent terminal agents trên nhiều host | Herdr |
| Enterprise issue/governance integrations | Jira/Linear adapters, audit/compliance extensions |
| Cross-agent / cross-organization delegation | A2A 1.0 gateway + identity/policy boundary |
| ACP adapter không đạt latency/reliability SLO | Native adapter path + evidence-based interface routing |
| Profile | CPU | RAM | Storage | Ghi chú |
| --- | --- | --- | --- | --- |
| Dev / API-first | 8–12 vCPU | 16–32 GB | 250–500 GB SSD | LLM API, E2B Cloud, Langfuse Cloud, CI hosted; không GPU. |
| Single-node core | 12–16 cores | 32 GB | ~500 GB NVMe | SDF + Postgres + memory + policy + light observability; models/sandbox có thể external. |
| Full self-host lab | 16–24 cores | 64 GB | 0.5–1 TB NVMe | SDF + sandbox + observability; Linux/KVM nếu dùng E2B/Firecracker. |
| Prod Control pool | 2+ replicas × 2–4 vCPU | 4–8 GB / replica | small/ephemeral | API, Planner, Router, Policy, Release/Env managers; stateless where possible. |
| Prod Data/Intelligence | 8–16 vCPU | 32–64 GB | 0.5–2 TB+ | Postgres, memory/index, analytics/metrics; prefer managed/object storage as scale grows. |
| Prod Execution worker | 16–32 CPU / node | 64–128 GB / node | 0.5–1 TB NVMe | Scale horizontally; sandbox density determined by workload and isolation. |
| Rule: Knowledge cần thiết để maintain sản phẩm không được phép chỉ tồn tại trong private memory của SDF đã tạo ra nó. |
| --- |
| Artifact | Nội dung cần giữ |
| --- | --- |
| project.yaml | Identity, services, runtime versions, deploy model, owners/interfaces. |
| architecture.md | Boundaries, data flow, critical paths, failure modes, deployment topology. |
| ADRs | Why: quyết định kiến trúc và trade-off, không chỉ code hiện tại. |
| invariants.yaml | Những điều không được phá: idempotency, backward compatibility, approval, security constraints. |
| runbooks | Diagnose/recover common incidents, deploy/rollback, backup/restore. |
| open-work.yaml | Current open/blocked work và technical debt; không cần dump toàn bộ task history. |
| known-issues.yaml | Symptom → likely causes → diagnostic steps → mitigations. |
| provenance + SBOM | Code/dependency/source/license/build provenance. |
| baseline tests | Reference behavior/performance/security baseline để SDF mới verify environment. |
| memory.snapshot | Project-scoped distilled memory; không phụ thuộc duy nhất vào nó. |
| policy/ + Policy Manifest | Effective baselines, policy source/tests, approved exceptions, engine/version requirements và decision/evidence schema. |
| release-manifest.yaml | Last known release/artifact digests, promotion history, rollback target và environment compatibility. |
| catalog.yaml | Component identity, owner, repository, APIs, dependencies, SLO/runbook/dashboard links để catalog khác import lại. |
| business-context.yaml | Customer, use case, scale, buyer, budget, criticality, SLA, compliance and alternatives that shape architecture. |
| economics/ | Creation cost, COGS, maintenance/support, value model, alternatives, pricing corridor, margin and uncertainty. |
| finops/ | Cost model, allocation rules, budget, unit economics, FinOps policy, cost baseline and dashboard definitions. |
| economic-baseline.yaml | Actual/forecast cost, primary cost drivers, target unit metric, ROI/payback assumptions and last validated economics. |
| Level | Bàn giao? |
| --- | --- |
| Private agent memory | Không; agent-specific/noisy. |
| Task memory | Thường discard hoặc summarize nếu còn giá trị. |
| Project memory | Bắt buộc export/promote nếu cần để maintain. |
| Organization memory | Tùy access/policy; không tự động bàn giao. |
| Khuyến nghị thực dụng: Core commercial backend nên ưu tiên API-first, sandbox-first và permissive-license components. Orca nên là optional coding cockpit; Herdr là optional terminal fleet runtime; memory provider cần abstraction để thay thế khi license/maturity không phù hợp. Giá trị riêng của sản phẩm nằm ở SDF control logic, evaluation policy, experience model, incident workflows, routing/evolution và portability. |
| --- |
| Term | Nghĩa trong tài liệu này |
| --- | --- |
| SDF | Software Decision Fabric (SDF): control/decision/learning fabric kết nối business intent, engineering decisions, agents, runtime evidence và learning xuyên vòng đời software. |
| Harness | Runtime/tooling loop bao quanh model: context, tools, edit mechanism, process/session. |
| Agent | Một executor cụ thể (Codex, Claude Code, Pi...) được harness hóa để hoàn thành task. |
| Task | Logical work unit có lifecycle và acceptance criteria. |
| Attempt/Dispatch | Một lần thực thi task bởi agent/runtime cụ thể. |
| State | Trạng thái hiện tại của system/workflow. |
| Memory | Knowledge cần nhớ qua nhiều task/session. |
| Context | Subset thông tin được chọn để đưa vào một agent invocation. |
| Validator | Kiểm tra schema/contract/invariant. |
| Guardrail | Policy enforcement trước hành động. |
| Evaluator | Xác minh chất lượng/correctness sau execution. |
| Experience | Structured historical outcome phục vụ analytics/evolution. |
| Evolver | Cơ chế thay đổi prompts/routing/workflow/tool strategy dựa trên evidence. |
| Project Passport | Gói knowledge/metadata/baseline portable để SDF/human khác maintain sản phẩm. |
| Policy Pack | Bộ policy + tests + exceptions + manifest được version và ship cùng generated software. |
| Policy Plane | Cross-cutting layer định nghĩa, version, phân phối và audit policy cho SDF lẫn generated products. |
| Release Manager | SDF domain service quản lifecycle release, artifact evidence, promotion, approval và rollback. |
| Environment Manager | Inventory/trạng thái/policy của dev, staging, prod, preview hoặc customer environments. |
| Software Catalog | Index component/service, ownership, repo, APIs, dependencies, environments, SLOs và operational links. |
| Knowledge Distiller | Cơ chế promote durable knowledge từ task/memory thành portable project knowledge. |
| Business Context | Machine-readable context về customer/use case/scale/buyer/budget/criticality/SLA/compliance/alternatives dùng để constrain planning. |
| Business Economics | Model kết hợp cost, customer value, alternatives, market, ROI/payback và pricing để đánh giá viability của offering. |
| Offering | Product + SLA + support + security/compliance + deployment model + contract terms; đơn vị phù hợp hơn repository để pricing. |
| FinOps Plane | Economic control plane cho cost ingestion/normalization/attribution/budget/forecast/anomaly/unit economics/optimization. |
| Price Corridor | Khoảng giá khả thi tạo từ economic floor, competitive anchors, value-based range và buyer/budget constraints. |
| Unit Economics | Chi phí/giá trị trên một đơn vị business có ý nghĩa: customer, transaction, request, inference, successful task... |
| Alert Router | Layer nhận signals, deduplicate/group/correlate, gán severity/audience và route notification hoặc escalate thành incident. |
| Intent/Decision Graph | Portable graph linking objectives, assumptions, constraints, ADRs, tasks, changes and releases to runtime evidence. |
| ACP | Agent Client Protocol: contract client/SDF -- coding agent cho initialize, session, prompt, streaming updates, permission/cancel; v1 là baseline stable trong tài liệu này. |
| A2A | Agent2Agent Protocol: contract giữa independent agent systems/SDFs cho discovery, delegation, task/message/artifact exchange. |
| MCP | Model Context Protocol: tool/resource integration boundary; không thay agent orchestration hoặc coding-session lifecycle. |
| Native Adapter | Agent-specific integration path dùng làm fallback khi protocol adapter thiếu capability hoặc không đạt latency/reliability SLO. |
| SDF Event Stream | Canonical realtime stream của agent/task/policy/release/incident events; UI/collaboration systems consume từ đây thay vì nằm trên execution critical path. |
| SDF Console | Primary human interface cho task/agent streams, approvals, policy/release/incident/cost views và Decision Graph. |