# A2A và ACP có thực sự cần thiết cho SDF không?

Ngày nghiên cứu: 2026-09-19  
Phạm vi: `Software_Decision_Fabric_Architecture_Final_VI_v1.5.docx`, đối chiếu với đặc tả chính thức của A2A và Agent Client Protocol (ACP).

## Kết luận ngắn

- **A2A không cần cho core/MVP của SDF.** Nên giữ nó là một adapter boundary tùy chọn, chỉ triển khai khi SDF thực sự phải ủy quyền công việc cho agent system/SDF độc lập, khác trust domain hoặc khác tổ chức.
- **ACP có giá trị sớm hơn nhưng vẫn không nên là dependency bắt buộc của core.** Nó phù hợp làm interface chuẩn cho nhiều coding agent/harness; SDF vẫn cần native adapter, sandbox, policy gateway, task/state manager và evaluator.
- **Khuyến nghị:** xây một `Agent Interface` nội bộ ổn định trước; triển khai một ACP v1 adapter có capability negotiation + đo SLO; hoãn A2A đến khi có use case remote delegation thứ hai (ví dụ Security/FinOps/Release Agent hoặc một SDF khác). Không đưa `Task`, `Session`, budget, acceptance criteria hay policy enforcement của SDF vào làm phụ thuộc trực tiếp của protocol.

Đây là khuyến nghị kiến trúc, không phải yêu cầu bắt buộc của các đặc tả. Các phần dưới đây tách **FACT** (nguồn chính thức nói gì) khỏi **INFERENCE** (áp dụng vào SDF).

## ACP đang được nói tới là gì?

**FACT:** ACP trong đánh giá này là **Agent Client Protocol** tại [agentclientprotocol.com](https://agentclientprotocol.com/get-started/introduction), do hệ sinh thái Zed/JetBrains và cộng đồng ACP duy trì. Đặc tả mô tả nó là giao thức giữa code editor/IDE và coding agent; hỗ trợ agent local qua subprocess/JSON-RPC stdio và agent remote qua HTTP/WebSocket, nhưng tài liệu chính thức ghi rõ hỗ trợ remote đầy đủ vẫn đang được phát triển.

**INFERENCE:** Đây là cách hiểu phù hợp với SDF v1.5: tài liệu gọi ACP là boundary “Client/SDF-to-Coding Agent”, nêu `initialize`, session/update và các coding agent như Codex/Claude/Pi/Hermes. Nếu nhóm đang muốn nói tới “Agent Communication Protocol” khác, phải đổi tên trong ADR; không nên dùng chữ ACP không định danh.

### Quy ước định danh bắt buộc trong SDF

Trong mọi ADR, interface name, config, metric và diagram của SDF, ghi đầy đủ:

> **ACP = Agent Client Protocol (ACP v1)** — protocol giữa SDF/client/editor và coding agent.

Không dùng `ACP` đứng một mình trong tên module mới. Dùng các tên như `AgentClientProtocolAdapter`, `acp_v1`, `acp_session_id` và `acp_capability_negotiation`. Nếu đề cập protocol cũ của BeeAI, ghi rõ **Agent Communication Protocol (BeeAI ACP, legacy/merged)**; không được gọi ngắn là ACP trong cùng một tài liệu.

## Ghi chú chuẩn cho hai protocol stack

### Stack A — A2A: agent-system ↔ agent-system

**Boundary:** SDF/agent system này gọi một agent system hoặc SDF độc lập, thường qua mạng và có trust boundary riêng.

**Chuẩn hóa:** Agent Card/discovery, identity và capability/skill, message, remote Task, Artifact, multi-turn interaction, streaming, async push, task status và authentication/authorization binding.

**Ví dụ trong SDF:** ủy quyền cho Security Agent, FinOps Agent, Release Agent, Incident Agent hoặc một SDF của tổ chức/vendor khác.

**SDF vẫn sở hữu:** logical `Task`, dependency, attempt, budget, acceptance criteria, policy decision, audit, retry/replan và Intent/Decision linkage. A2A Task chỉ là remote delegation/attempt, không phải source of truth.

**Không phải:** coding-session protocol, editor UX, file/terminal permission model, tool/resource protocol hoặc orchestration/state manager nội bộ.

**Security note:** coi A2A như network/organization boundary; bắt buộc identity, authN/authZ, data classification, budget/quota, audit và policy enforcement trước khi delegate.

**Activation gate:** chỉ bật khi có remote specialist/SDF thật, khác trust domain/vendor/tổ chức, hoặc có nhu cầu discovery và task/artifact portability qua mạng.

### Stack B — Agent Client Protocol: client/editor ↔ coding agent

**Boundary:** SDF/client/editor điều khiển một coding agent hoặc harness.

**Chuẩn hóa:** JSON-RPC hai chiều, `initialize`, version/capability negotiation, authentication tùy chọn, `session/new`/resume, prompt turn, streamed `session/update`, cancellation, permission requests, diff/message/tool-call content và file/terminal capabilities.

**Ví dụ trong SDF:** Router chọn Codex/Claude/Pi/Hermes adapter; event từ coding session đi vào SDF Event Stream rồi được project sang Console, evaluator và audit.

**SDF vẫn sở hữu:** logical task/state, scheduler, retry/replan, budget, guardrail/policy, sandbox/worker lifecycle, evaluator, runtime evidence và acceptance criteria.

**Không phải:** peer-agent discovery/delegation (A2A), tool/resource authorization (MCP), sandbox isolation, policy engine hoặc business-domain graph.

**Security note:** ACP permission/tool-call metadata không thay authorization của SDF. Tool Proxy/Policy Plane vẫn phải quyết định principal, action, resource, context và approval boundary.

**Reliability note:** capability negotiation là bắt buộc; đo spawn/init/session/TTFT/final/cancel/failure. ACP adapter không đạt SLO phải tự động nhường cho native adapter.

**Activation gate:** ưu tiên khi có từ hai coding agent/harness hoặc cần editor/client portability. Nếu chỉ có một harness nội bộ, native adapter trước là đủ.

### Quan hệ giữa hai stack

```text
SDF Control Plane
  ├─ A2A Gateway ──> remote specialist agent / another SDF
  └─ Agent Router
       ├─ ACP v1 Adapter ──> coding agent / harness
       └─ Native Adapter ──> coding agent / harness (fallback)
```

A2A và Agent Client Protocol có thể cùng xuất hiện trong một execution path, nhưng không thay thế nhau: A2A là **delegation giữa agent systems**, còn ACP là **session điều khiển coding agent**. Cả hai đều phải nằm dưới SDF Task/Policy/Event boundary.

## Trách nhiệm của từng giao thức

| Boundary | Giao thức chính thức | Nó chuẩn hóa | Khớp với phần nào của SDF? | Mức cần thiết |
|---|---|---|---|---|
| Agent system ↔ agent system | [A2A v1.0.0](https://a2a-protocol.org/latest/specification/) | Discovery qua Agent Card, capability/modalities, message/task/artifact, streaming, async push, multi-turn và HTTP/JSON-RPC/gRPC bindings | Delegation tới Security/FinOps/Release/Incident Agent hoặc SDF khác | **Có điều kiện**; không cần cho single-SDF core |
| Client/editor ↔ coding agent | [ACP v1](https://agentclientprotocol.com/protocol/v1/overview) | Initialize/capability negotiation, authentication, session lifecycle, prompt turn, streamed updates, cancellation, permission requests, diff/tool-call UX, file/terminal capabilities | Router gọi coding agent/harness và đưa progress về SDF Console/Event Stream | **Có ích sớm**, nhưng không bắt buộc nếu chỉ có một native harness |
| Agent ↔ tool/resource | [MCP](https://a2a-protocol.org/latest/specification/#appendix-b-relationship-to-mcp-model-context-protocol) | Tool, API, data source và resource access | Tool Proxy/Gateway và tool/resource boundary | Không thay thế A2A hoặc ACP |

### FACT: A2A

Đặc tả A2A v1.0.0 định nghĩa peer agents là các hệ thống độc lập, có thể “opaque”: chúng khám phá capability, thương lượng modality, quản lý task cộng tác và trao đổi kết quả mà không cần truy cập internal state, memory hoặc tools của nhau. Core model có `Task`, trạng thái task, `Message`, `Artifact`; operations gồm gửi message, streaming, lấy/list/cancel/subscribe task và push notification. A2A Server phải cung cấp Agent Card mô tả identity, capability, skill, endpoint và yêu cầu auth. [Nguồn: A2A Specification, §§1, 2, 3, 4, 8](https://a2a-protocol.org/latest/specification/)

A2A cũng nói rõ nó bổ sung cho MCP: A2A là peer-agent collaboration/delegation, còn MCP là cách agent dùng tool/API/data/resource. [Nguồn: A2A Specification, Appendix B](https://a2a-protocol.org/latest/specification/#appendix-b-relationship-to-mcp-model-context-protocol)

### FACT: ACP

ACP v1 dùng JSON-RPC hai chiều. Flow chuẩn là client initialize để thương lượng version/capability, tạo hoặc load session, gửi `session/prompt`, nhận `session/update`, và có thể cancel. Client có thể cung cấp file-system/terminal/permission capabilities; agent báo plan, message chunks, tool calls và kết quả để client hiển thị. [Nguồn: ACP v1 Overview](https://agentclientprotocol.com/protocol/v1/overview), [Initialization](https://agentclientprotocol.com/protocol/v1/initialization), [Prompt Turn](https://agentclientprotocol.com/protocol/v1/prompt-turn), [Tool Calls](https://agentclientprotocol.com/protocol/v1/tool-calls)

ACP được thiết kế quanh coding-agent UX và mô hình client tin cậy agent. Tài liệu kiến trúc nói editor thường khởi chạy agent subprocess, agent có thể dùng file/MCP server; trang giới thiệu ghi hỗ trợ remote đầy đủ vẫn là work in progress. [Nguồn: ACP Introduction](https://agentclientprotocol.com/get-started/introduction), [ACP Architecture](https://agentclientprotocol.com/get-started/architecture)

## Có bị trùng không?

**FACT:** Có trùng ở cơ chế thấp hơn: cả hai đều có trao đổi hai chiều, progress/streaming và hủy công việc. Nhưng semantic boundary khác nhau:

- A2A nói về **một agent system hợp tác với một agent system khác**, thường qua mạng và trust boundary.
- ACP nói về **một client/editor điều khiển một coding agent**, với session, prompt, diff, tool-call display, file/terminal và user permission.
- A2A `Task` không phải ACP `Session`; A2A `Agent Card` không phải ACP capability response. Có thể map chúng trong adapter, nhưng không nên coi chúng là cùng một state model.

**INFERENCE:** Dùng A2A thay ACP cho coding agent sẽ thiếu các primitive UX/control mà SDF cần (session/prompt/update, diff, permission, file/terminal). Dùng ACP thay A2A cho remote specialist agents sẽ kéo theo giả định editor/coding-agent và không cung cấp mô hình peer discovery/task delegation của A2A. Vì vậy hai giao thức bổ sung nhau, nhưng không cần bật cả hai trong mọi deployment.

## Phân tích tính cần thiết cho SDF

### A2A: chỉ cần khi có remote delegation thật

**FACT:** A2A giải quyết đúng bài toán mà SDF v1.5 nêu ở §11.4: delegate capability-level task tới Security/FinOps/Release/Incident Agent hoặc một SDF khác, trong khi hệ thống gọi không cần biết internal state/tools của bên kia. Nó cũng có Agent Card, task lifecycle, streaming/async push và auth/scoping phù hợp cho boundary này. [A2A Specification](https://a2a-protocol.org/latest/specification/)

**INFERENCE:** Nếu SDF chỉ điều phối các worker/coding agent do cùng một control plane sở hữu, A2A tạo thêm chi phí mà chưa tạo interoperability value:

- thêm Agent Card/discovery và version/binding compatibility;
- phải map A2A task vào SDF logical task/attempt, dependency, budget, acceptance criteria và audit;
- phải vận hành identity/authN/authZ, tenant/data classification, streaming/reconnect và webhook/push;
- dễ tạo hai nguồn sự thật nếu A2A Task bị dùng thay cho SDF Task.

**Decision gate cho A2A:** chỉ triển khai khi ít nhất một điều sau là thật và có kiểm thử: (1) agent thuộc trust domain khác; (2) agent do team/vendor khác vận hành; (3) cần discover/route động giữa nhiều SDF/agent systems; hoặc (4) long-running remote delegation cần task/artifact portability. Trước gate đó, internal HTTP/gRPC/queue + native adapter là đủ, miễn là giữ `A2A Gateway` như seam có thể thay thế.

### ACP: đáng dùng cho coding-agent interoperability, nhưng không phải control plane

**FACT:** ACP giảm N×M integration giữa editor và coding agent bằng một interface chung; v1 đã mô tả initialize/capability negotiation, session/prompt/update/cancel, permission và coding-specific content. Điều này khớp với SDF vì SDF muốn route nhiều coding agent/harness và đưa event về Console/Eval/Audit. [ACP Introduction](https://agentclientprotocol.com/get-started/introduction), [ACP v1 Overview](https://agentclientprotocol.com/protocol/v1/overview)

**FACT:** ACP không thay policy hoặc sandbox của SDF. ACP tool-call `name` chỉ là metadata thông tin, không quảng bá capability và không cấp authorization; tài liệu cũng mô tả mô hình “trusted” nơi editor kiểm soát quyền nhưng agent được truy cập file/MCP. [ACP Tool Calls](https://agentclientprotocol.com/protocol/v1/tool-calls), [ACP Architecture](https://agentclientprotocol.com/get-started/architecture)

**INFERENCE:** ACP có thể là interface ưu tiên cho coding agent, nhưng không nên là dependency bắt buộc của SDF core vì:

- SDF vẫn phải sở hữu logical task, state, scheduler, retry/replan, budget, policy/guardrail, sandbox/worker, evaluator và runtime evidence;
- ACP remote support đang được hoàn thiện, trong khi SDF server-side execution cần boundary remote đáng tin cậy;
- capability negotiation và event mapping là bắt buộc; không được giả định agent nào cũng hỗ trợ file, terminal, MCP hoặc plan;
- ACP adapter hỏng/chậm không được làm mất execution path — cần native fallback và SLO thực đo.

**Decision gate cho ACP:** nếu SDF tích hợp từ hai coding agent/harness trở lên hoặc muốn client/editor portability, implement ACP v1 adapter ngay sau internal interface. Nếu chỉ có một harness nội bộ, native adapter trước sẽ rẻ và ít rủi ro hơn; thêm ACP khi có integration thứ hai hoặc nhu cầu editor interoperability được chứng minh.

## Khuyến nghị triển khai theo giai đoạn

1. **MVP:** `Agent Interface` nội bộ + một native adapter; SDF Task/Attempt/Event/Policy là source of truth. MCP chỉ dùng ở tool/resource boundary. Chưa cần A2A; ACP là optional adapter.
2. **Interoperability slice:** thêm ACP v1 adapter, pin version, initialize/capability negotiation, normalize `session/update` vào SDF Event Stream, đo p95 time-to-first-event, completion, cancellation và failure rate. Giữ native fallback.
3. **Distributed slice:** chỉ thêm A2A Gateway khi có remote specialist/SDF use case thật. Giữ SDF Task là logical work unit; coi A2A Task là remote delegation/attempt. Enforce authN/authZ, data classification, budget, acceptance criteria và audit ở SDF boundary.
4. **Không làm:** không dùng A2A hoặc ACP để thay State Manager, Scheduler, Policy Plane, Sandbox Manager, Event Bus, Evaluator hoặc Intent/Decision Graph. Không để protocol object trở thành business-domain source of truth.

## Decision checklist

| Câu hỏi | Nếu “không” | Nếu “có” |
|---|---|---|
| Có từ hai coding agent/harness cần một contract chung không? | Native adapter đủ cho hiện tại | Ưu tiên ACP v1 adapter |
| Có agent/SDF độc lập ở trust domain/vendor/tổ chức khác không? | Chưa cần A2A | Đánh giá A2A Gateway |
| Cần discovery capability động, task dài và async reconnect qua mạng không? | Internal RPC/queue đơn giản hơn | A2A có lợi rõ ràng |
| Protocol SLO đã tốt hơn native path chưa? | Giữ fallback, chưa khóa critical path | Có thể route preferred theo capability/SLO |
| Protocol có làm mất policy/budget/audit của SDF không? | Dừng thiết kế và sửa boundary | Cho phép adapter triển khai |

## Nguồn chính thức và ngày truy cập

- [A2A Protocol Specification v1.0.0](https://a2a-protocol.org/latest/specification/) — phiên bản mới nhất, mục tiêu, task/artifact, discovery, security, binding và quan hệ với MCP. Truy cập 2026-09-19.
- [A2A Appendix B: Relationship to MCP](https://a2a-protocol.org/latest/specification/#appendix-b-relationship-to-mcp-model-context-protocol) — ranh giới peer-agent và tool/resource. Truy cập 2026-09-19.
- [Agent Client Protocol — Introduction](https://agentclientprotocol.com/get-started/introduction) — phạm vi editor/IDE ↔ coding agent, local/remote và trạng thái remote support. Truy cập 2026-09-19.
- [ACP Architecture](https://agentclientprotocol.com/get-started/architecture) — JSON-RPC, subprocess setup, UX/trusted model và quan hệ với MCP. Truy cập 2026-09-19.
- [ACP v1 Overview](https://agentclientprotocol.com/protocol/v1/overview) — message flow, session/prompt/update/cancel và các capability của client/agent. Truy cập 2026-09-19.
- [ACP v1 Initialization](https://agentclientprotocol.com/protocol/v1/initialization), [Prompt Turn](https://agentclientprotocol.com/protocol/v1/prompt-turn), [Tool Calls](https://agentclientprotocol.com/protocol/v1/tool-calls) — negotiation, streaming, cancellation, permission và tool-call semantics. Truy cập 2026-09-19.
- [SDF Architecture v1.5](../../Software_Decision_Fabric_Architecture_Final_VI_v1.5.docx), §11 “Execution & Protocol Plane” — boundary A2A/ACP/MCP và native fallback được đánh giá trong báo cáo này. Tài liệu local, truy cập 2026-09-19.
