# MASS 技术方案

> 文档约束
> - 本文回答：每个模块怎样实现、依赖什么技术与第三方库、关键代码和数据契约是什么。
> - 包含：技术选型、模块算法、存储与接口设计、核心代码、运行配置、兼容性和实现限制。
> - 不包含：重新定义产品目标，或维护开发进度、测试执行报告和交付步骤。
> - 依据：[系统设计指引](design-guide.md)和[系统架构设计](architecture.md)；编码、测试与交付按[工程开发文档](engineering.md)执行。以后各模块细节继续补充在本文，不拆专题文档。
> - 状态：技术设计建议，尚未实现；更新：2026-10-17。代码为设计片段，不代表仓库已有可运行实现；片段中未定义的工程类型需在对应阶段补齐。

## T00 技术基线与依赖

### T00.1 运行形态

遵循用户已指定的技术约束：后端 Go，前端 Node.js 技术生态；Agent 使用 AGENTS.md 与指定版本 Skills；MVP 无沙箱，内置工具直接在本服务执行。

建议采用 React + TypeScript + Vite，单个 Go 服务承载 API、调度器和执行器，PostgreSQL 保存业务数据和运行事件，本地持久化目录保存文件及大载荷。生产环境由 Go 提供构建后的静态页面，Node.js 只用于开发、构建与测试。

MVP 不依赖 Redis、独立消息队列、向量数据库、对象存储服务或通用 Agent 编排框架。首版模型适配通过标准库 HTTP 实现，避免隐藏重试与无法捕获实际载荷的封装。

### T00.2 依赖清单

具体补丁版本在工程初始化时锁定到 go.mod/go.sum、工具版本配置和 package-lock.json；不以 latest 作为可复现构建输入。依赖兼容性在安装时复核。

| 模块 | 技术与依赖 | 作用及选择 |
| --- | --- | --- |
| M13 服务基础 | Go net/http、ServeMux、context、log/slog、embed | HTTP、取消传播、结构化日志和静态资源 |
| 存储基础 | PostgreSQL；github.com/jackc/pgx/v5、pgxpool | 事务、连接池、显式空间过滤 |
| SQL 与迁移 | sqlc、github.com/pressly/goose/v3 | 生成查询代码、执行版本化 SQL 迁移 |
| M01 身份 | crypto/rand、crypto/sha256、crypto/subtle；golang.org/x/crypto/argon2 | 随机登录令牌、令牌摘要、恒定时间比较、Argon2id 口令摘要 |
| M02 定义 | go.yaml.in/yaml/v4；encoding/json；crypto/sha256 | 配置解析、内部标准表示、内容完整性 |
| M02/M09 校验 | github.com/santhosh-tekuri/jsonschema/v6 | 运行时参数和配置 Schema 校验 |
| M03 凭证 | crypto/aes、crypto/cipher | AES-GCM 加密，密钥不存入业务数据库 |
| M04—M06 运行 | pgx 事务、context、sync、time | 幂等、并发准入、认领、执行和取消 |
| M07 上下文 | 自有组装/压缩模块；模型对应计数器适配 | 可控预算与可追踪摘要，不伪造统一精确 Token 计数 |
| M08 模型 | net/http、encoding/json、io | 请求体捕获、流式协议解码、显式重试 |
| M10 Trace | PostgreSQL、JSONB、BlobStore、net/http | 事件索引、原始载荷、SSE 续传 |
| M11 文件 | os.Root、io、crypto/sha256 | 目录边界、有限读取、内容校验 |
| M12 页面 | react、react-dom、typescript、vite、@vitejs/plugin-react | 前端 SPA |
| M12 路由/查询 | react-router；@tanstack/react-query | 页面路由、服务端状态缓存 |
| M12 编辑/预览 | monaco-editor；react-markdown、remark-gfm | 定义编辑、差异查看、受限 Markdown 预览 |
| API 类型 | OpenAPI 3.1；openapi-typescript | 机器可读接口与生成 TypeScript 类型 |
| 测试工具 | Go testing/httptest；vitest；@playwright/test | 单元、协议、集成与端到端测试 |

UI 基础组件先采用本地封装与普通样式，不额外引入一套未经选择的组件库。浏览器本地状态优先组件状态和 reducer，不额外引入全局状态框架。

[YAML 官方仓库](https://github.com/yaml/go-yaml)推荐新项目采用 v4；[jsonschema/v6](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6)提供运行时校验；它们的具体 API 以锁定版本为准。配置解析限制文件大小、层级和别名展开，拒绝未知配置字段与非预期多文档输入；Schema 引用限定为平台注册的本地资源，禁止任意网络加载。

[pgx](https://github.com/jackc/pgx)负责数据库连接；[sqlc](https://docs.sqlc.dev/en/latest/howto/generate.html)生成查询代码但不执行迁移；迁移使用 [goose](https://pressly.github.io/goose/)。迁移文件使用固定宽度序号或时间戳，确保读取顺序一致。

### T00.3 数据基础

多租户数据统一带 space_id；资源访问同时校验身份、成员关系、操作权限和对象归属。Repository 不提供默认跨空间查询；后台任务使用明确的系统身份和空间范围。

| 数据对象 | 关键内容和约束 |
| --- | --- |
| users / auth_sessions | 用户、口令摘要；会话令牌摘要、到期及撤销时间 |
| spaces / space_members | 空间；用户成员关系和角色唯一 |
| agents / agent_drafts / agent_versions | 元数据、草稿 revision、不可变内容与版本号；默认版本引用 |
| skills / skill_drafts / skill_versions | 独立草稿、版本及资源清单 |
| agent_version_skills | Agent 版本到准确 SkillVersion ID、显示版本号及加载顺序 |
| model_configs / credentials | 模型能力和参数；密文、key_id、nonce、凭证版本 |
| sessions / messages | 固定定义引用、历史顺序、消息/协议块与完成状态 |
| context_summaries | 来源消息范围、摘要内容、生成调用、策略版本 |
| runs / run_snapshots | 状态、执行归属、预算、取消请求、有效配置、版本/工具快照 |
| idempotency_keys | 空间、主体、操作、键、请求摘要、对应结果 |
| trace_events | Run 内序号、span 关系、事件类型、时间和载荷引用 |
| blobs / artifacts | 内容位置、大小、摘要、归属、引用和保留状态 |
| audit_logs | 管理操作主体、对象、动作、结果及时间 |

发布内容和快照不可修改；默认指针、归档状态属于独立可变元数据。迁移账号和应用账号分离，应用账号不具有修改结构的权限。

关键约束示意，完整迁移需在对应开发阶段补齐外键和列定义：

```sql
CREATE UNIQUE INDEX runs_one_active_per_session
  ON runs(space_id, session_id)
  WHERE status IN ('queued', 'running');

CREATE UNIQUE INDEX trace_events_run_sequence
  ON trace_events(space_id, run_id, sequence);

CREATE UNIQUE INDEX request_idempotency
  ON idempotency_keys(space_id, actor_id, operation, key);

-- 所有被联合外键引用的父表需先声明 UNIQUE(space_id, id)。
ALTER TABLE agent_version_skills
  ADD CONSTRAINT dependency_skill_same_space
  FOREIGN KEY (space_id, skill_version_id)
  REFERENCES skill_versions(space_id, id) ON DELETE RESTRICT;
```

同时建立 AgentVersion 侧的同空间外键以及会话、Run、Blob 引用的同空间约束。运行状态更新带旧状态或 revision 条件。MVP 不把尚未启用的数据库行级安全当作隔离保证。

### T00.4 服务契约约定

API 前缀 `/api/v1`。除认证接口外，业务路径以 `/spaces/{space_id}` 开头。路径中的 space_id 不是授权证明。

OpenAPI 定义请求、响应、分页、错误、幂等行为；`api/events/` 保存事件 Schema；`db/migrations/` 是表结构的可执行来源。它们是工程文件，不新增为第五份说明文档。

错误统一包含 `code`、`message`、`request_id` 和非敏感 details；采用明确的 HTTP 状态。列表使用游标分页；大型载荷单独授权获取。事件序号在 JSON/前端传输时用十进制字符串，避免整数精度丢失。

## T01 M01 身份、成员与空间权限

依赖：net/http、Argon2id、crypto/rand、pgx。首版采用账号密码和服务端登录会话，初始管理员通过本地初始化命令创建，新增成员由授权管理员管理；公开注册、密码邮件找回暂不实现。

登录令牌使用高熵随机值，数据库仅保存摘要，Cookie 设置 HttpOnly、Secure、SameSite=Lax。写请求校验 CSRF Token 和来源，登录失败限速。口令保存算法版本、盐和参数，参数在目标主机校准；比较使用恒定时间处理。

角色默认 owner/editor/viewer；权限动作包括 agent.read/write/publish、skill.read/write/publish、run.create/cancel/read、trace.read、credential.manage。viewer 不默认允许创建 Run；编辑者可使用获授权模型但不能读取凭证明文。避免删除最后一个 owner。

HTTP 中间件构造 Principal；应用服务再针对具体对象鉴权。身份会话到期、成员移除时终止后续订阅与操作。SSE 定期重新验证会话/成员状态；运行每次模型和工具调用前检查当前使用权限。

接口范围：`POST /auth/login`、`POST /auth/logout`、`GET /auth/me`；空间创建、成员列表、添加/修改/移除成员。未授权对象返回一致的拒绝或不存在行为，不通过错误内容泄露跨空间信息。

## T02 M02 定义、Skill 依赖与发布

依赖：YAML v4、JSON Schema、pgx/sqlc、BlobStore。内容结构如下：

```text
agent: AGENTS.md + agent.yaml + assets/
skill: SKILL.md + assets/
```

配置示例中的限额只是演示值，实际值在平台策略内校验：

```yaml
schema_version: 1
name: research-assistant
model: default-model
skills:
  - id: information-extraction
    version: 1.2.0
  - id: report-writing
    version: 2.0.1
tools: [read_file, write_file, read_skill_resource]
context:
  max_input_tokens: 24000
  reserved_output_tokens: 4000
  compression: summary
limits:
  max_steps: 30
  timeout_seconds: 300
```

版本号使用对象内唯一的字符串标识，MVP 不解析版本范围。Skill 引用拒绝 latest，发布后同时保存准确内部版本 ID、显示版本和内容摘要；不自动执行 Skill 中的脚本。

草稿更新采用 `WHERE revision = expected_revision` 条件，未更新任何行返回冲突。发布先写入不可变资源，再在数据库事务中校验并锁定草稿、权限和引用，写版本、依赖、审计和必要的首次默认版本指针。

核心流程代码为应用用例骨架；`WithinTx` 必须在回调错误或 panic 时回滚，成功才提交。其依赖方法需在实现阶段提供，不能忽略错误：

```go
func (s *CatalogService) PublishAgent(ctx context.Context, in PublishInput) (Version, error) {
    // Prepare 保存不可变资源，不持有数据库事务；失败直接结束。
    prepared, err := s.Prepare(ctx, in)
    if err != nil { return Version{}, err }
    var result Version
    err = s.DB.WithinTx(ctx, func(tx CatalogTx) error {
        if err := tx.AuthorizePublish(ctx, in.ActorID, in.SpaceID); err != nil { return err }
        draft, err := tx.LockDraft(ctx, in.SpaceID, in.AgentID)
        if err != nil { return err }
        if draft.Revision != in.ExpectedRevision || draft.Digest != prepared.DraftDigest {
            return ErrDraftConflict
        }
        deps, err := tx.ResolveExactSkills(ctx, in.SpaceID, prepared.SkillRefs)
        if err != nil { return err }
        // InsertVersionAndAudit 同时保存依赖、资源引用和审计。
        result, err = tx.InsertVersionAndAudit(ctx, in, prepared, deps)
        return err
    })
    return result, err
}
```

重复版本由唯一约束拒绝；并发草稿变化不会发布错内容。提交失败留下未引用 Blob，延迟清理；不得先声明发布成功再补文件。

调试使用同一解析与校验链创建不可变 snapshot。默认版本切换不改版本内容；归档只阻止新的选择，不破坏原有引用。删除未引用草稿资源和发布版本保留使用不同策略。

接口组：Agent/Skill 创建、草稿 GET/PUT（携带 revision）、资源上传、发布、版本列表/详情/差异、依赖查询、归档；Agent 另有默认版本切换与调试快照创建。

## T03 M03 模型配置与凭证

依赖：pgx、AES-GCM、crypto/rand。模型配置包含 provider、model_id、能力与容量、非秘密参数、批准端点和 credential_ref。一般空间用户从批准端点选择，不直接指定任意网络地址。

凭证密文关联 space_id、credential_id 和 key_id 作为附加认证数据；每次加密生成唯一 nonce。主密钥通过服务配置的秘密文件或环境注入，不提交仓库，不与密文一起作为数据库字段保存。明确密钥轮换和旧备份解密的要求。

Run 固定非秘密有效配置和凭证引用；每次实际调用重新验证权限及凭证有效性，记录非秘密凭证版本标识。凭证轮换不要求历史快照保存旧秘密。

参数能力在发布/调试和实际调用前校验。Provider 不支持工具、流式或输入类型时返回明确错误，不静默降级。费用记录同时保存价格来源及版本；无价格时只展示 usage。

接口组：模型配置 CRUD、可用模型能力查询、凭证创建/替换/禁用；凭证响应永不回显秘密。

## T04 M04 会话、消息与 Run

依赖：pgx/sqlc、crypto/sha256、context。Session 固定发布版本或调试快照；Run 快照记录准确 Skill 版本、有效模型/上下文配置、工具 Schema/实现版本、平台指令版本和服务构建标识。

提交事务锁定 Session，检查无活跃 Run，保存用户消息、快照、queued Run、幂等记录及初始 Trace 事件。相同主体/操作/幂等键且请求摘要相同返回原结果；内容不同返回 409。默认版本只在创建新 Session 时解析。

幂等处理必须在返回“已有活跃 Run”之前识别同一次请求的重试；两个并发相同请求通过唯一约束裁决，并读取赢家结果，不另建 Run。

终态固定为 succeeded/failed/cancelled；queued/running 属于活跃状态。`cancel_requested_at` 表达请求取消，不直接证明工具已停止。queued 可原子转 cancelled；running 由执行器确认结束后转终态。

最终回复、Run 终态、usage 汇总和 run.finished 同事务提交。部分助手输出保留在 Trace 中，失败时不作为完整回复提交。下一轮历史保留完成的协议块；失败的副作用以已完成/失败/未知说明进入后续上下文，不拼入缺少配对的不完整工具链。

接口组：`POST /sessions`、`GET /sessions/{id}/messages`、`POST /sessions/{id}/runs`、`GET /runs/{id}`、`POST /runs/{id}/cancel`。提交返回 202 和 Run ID；重复提交可返回原 Run 及其当前状态。

## T05 M05 调度、认领与停止

依赖：PostgreSQL、context、sync、time。runs 表是任务来源，内存通知用于唤醒，轮询用于补漏。并发名额按平台和空间控制，先获名额再认领；MVP 不支持多个调度服务同时执行。

队列认领在短事务内执行，限定已获准空间，并在同一事务写入 run.started：

```sql
WITH candidate AS (
    SELECT id FROM runs
    WHERE space_id = $1 AND status = 'queued'
      AND cancel_requested_at IS NULL
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE runs AS r
SET status = 'running', owner_id = $2,
    started_at = now(), heartbeat_at = now()
FROM candidate AS c
WHERE r.id = c.id AND r.space_id = $1
RETURNING r.*;
```

SKIP LOCKED 适用于队列竞争，不提供外部副作用的恰好一次保证。[PostgreSQL SELECT 文档](https://www.postgresql.org/docs/current/sql-select.html)

调度器启动时持有专用数据库连接上的单实例 advisory lock；未取得锁则拒绝成为执行实例。持有连接失效时停止认领和后续动作，运维不能在旧进程是否退出未知时强行接管。该锁是单实例防误启动措施，不宣称提供分布式故障切换。

维护运行 owner、heartbeat；确认旧进程退出后，将遗留 running 标为 failed/interrupted，保留未完成调用；queued 可继续调度。不可仅因心跳超时重放有副作用任务。

Run context 派生自服务执行生命周期，不使用提交 HTTP 请求的 context。取消信号持久化并通知在执行 Run；即使内存通知丢失，检查点也会读取取消状态。

退出时停止接单和认领，尝试有界完成或取消在执行任务，刷写事件。工具未停止前不释放会话或并发名额；goroutine 不能被安全地强杀，无法及时退出时由进程级停机处理并标记中断。

## T06 M06 执行循环

依赖：ContextBuilder、ModelGateway、ToolDispatcher、TraceRecorder、RunStore。执行器只操作这些端口，不直接引用供应商协议或 SQL。

核心循环骨架如下；Gateway、Tools 和终结器负责各自动作前后的持久化，所有错误必须传播。`Build` 内的摘要也走同一 Gateway 并计入预算：

```go
func (e *Executor) Execute(ctx context.Context, run RunSnapshot) error {
    for step := 0; step < run.MaxSteps; step++ {
        if err := e.Control.Check(ctx, run); err != nil { return err }
        input, err := e.Context.Build(ctx, run)
        if err != nil { return err }
        reply, err := e.Model.Call(ctx, run, input)
        if err != nil { return err }
        if len(reply.ToolCalls) == 0 {
            if !reply.Complete { return ErrIncompleteModelOutput }
            return e.Runs.CompleteWithReply(ctx, run, reply)
        }
        for _, call := range reply.ToolCalls {
            if err := e.Control.Check(ctx, run); err != nil { return err }
            if err := e.Tools.DispatchAndRecord(ctx, run, call); err != nil { return err }
        }
    }
    return ErrStepLimit
}
```

外层 Supervisor 将错误区分为 cancelled、deadline、模型失败、持久化失败等，使用独立且有超时的收尾 context 保存失败与终态；不能继续使用已经取消的调用 context 写关键收尾记录，也不能无限等待数据库恢复。

工具业务错误可以形成结构化工具结果继续交互；访问越权、预算耗尽或记录失败按策略终止。每轮所有工具 call_id 都需得到相应结果，模型输出非法参数先保存原文再校验。文本结束原因若为长度截断，不标记为正常完整回复。

模型调用、摘要、工具调用分别有上限，并受 Run 总时长和预算约束。计费 usage 不可用时保留未知状态，不捏造精确值。工具的写入结果未知时不自动重试。

## T07 M07 上下文组装、预算与摘要

依赖：固定内容解析器、消息仓储、BlobStore、ModelGateway、模型计数适配器；无统一第三方 Agent 框架。

MVP 按指定顺序加载所有绑定 Skill 的 SKILL.md，assets 按需读取。上下文顺序为平台规则、Agent 指令、Skill 指令、历史摘要、最近完整对话单元、当前输入及本轮交互。平台指令版本在快照中固定，权限校验独立于这些文本。

ContextManifest 保存来源 ID/版本/摘要值、消息范围、顺序、工具定义、计数方法、预算以及压缩/截断记录，指向实际模型请求。供应商特有的上下文封装也需计入预算。

容量约束：

```text
有效输入预算 = min(Agent 输入上限, 平台输入上限,
                  模型上下文窗口 - 输出预留 - 安全余量)
```

1. 先检查关键指令、工具定义和当前请求，单独已超预算则明确失败。
2. 保留最近完整对话单元，不能拆开模型工具调用与结果。
3. 对过早历史进行有界分块摘要；摘要不能递归调用压缩器。
4. 对大工具结果保留完整原文，向模型提供节选/摘要及可读取原文的资源引用。
5. 重新估算并准备实际请求；仍超限或供应商拒绝时执行有界补偿，不无限循环。

计数器精确时记录算法版本；无法精确计算时使用保守估计并标记 estimated，结合供应商 usage 校准。多模态输入按供应商能力处理；未支持的附件类型在提交时拒绝，不装作已读取。

摘要请求禁用工具，标记 purpose=summary，使用同一 Run 内独立 span、相同用量与次数预算。保存消息范围和源内容摘要；摘要失败且原上下文无法容纳时结束 Run。失效、被编辑或不属于本会话的摘要不可复用。

检查接口返回 Manifest、处理原因与实际 input 对照；输入缩略图或节选不能冒充完整 input。原始消息与 Trace 不因上下文压缩而被改写。

## T08 M08 模型适配与实际载荷记录

依赖：net/http、encoding/json、io。首版先实现 FakeProvider 测试协议，再实现一个明确供应商；真实供应商、模型及允许端点在该模块开始前补齐本节，其他模块无需等待凭证。

统一模型端口示意（省略业务 DTO 定义）：

```go
type PreparedRequest struct {
    Provider string
    Model    string
    Body     []byte // 最终实际发送字节；此后禁止重新序列化
    Purpose  string // task 或 summary
}

type ModelFrame struct {
    Raw        []byte // 供应商原始响应帧
    Normalized []byte // 统一事件表示，可为空
}

type ModelProvider interface {
    Prepare(context.Context, ModelInput) (PreparedRequest, error)
    Stream(context.Context, PreparedRequest, CredentialRef,
        func(ModelFrame) error) (ModelResult, error)
}
```

网关先调用 Prepare，保存确切 Body、允许记录的非秘密请求元信息及 model.started，然后 Stream 使用同一 Body 发送。认证头在传输时注入，不进入 Trace。body 若包含意外秘密，按策略拒绝发送或明确脱敏记录；不能悄悄改变记录与真实载荷的关系。

Stream 每次回调的帧先持久化再对外展示；回调错误需要中止请求。响应主体无论成功、非成功状态、解码失败都记录已接收内容及完成性，设置最大接收量与流式空闲超时。不能先整体读入内存再落盘。

供应商 opaque state、工具协议 ID、结束原因与 usage 原样保存或作为协议扩展保留；不把供应商 A 的状态转交给 B。模型网关不做隐藏重试，每次尝试拥有独立 span 和原始载荷；已经出现部分输出后失败，不自动拼接下一次输出。

采用 HTTP 客户端的连接、响应头和流空闲控制，不将很短的总超时套在流式请求上；Run 总超时由 context 控制。批准端点禁止跟随到未批准地址的重定向，防止凭证被带往其他地址。

## T09 M09 内置工具与文件边界

依赖：自有 Registry/Dispatcher、JSON Schema、os.Root、context。Registry 启动时注册工具及 Schema，禁止重复名称；预编译受信 Schema，工具参数校验失败返回结构化错误。

工具定义包含 name、implementation_version、input_schema、output_contract、required_permission、side_effect、timeout、max_output_bytes。Run 冻结实际定义及工具实现版本；不兼容升级使用新标识或明确迁移，历史 Agent 版本不等于冻结工具二进制。

首批工具：read_file、write_file、list_files、read_skill_resource。工具只接收当前工作区和准确 Skill 版本的只读资源能力，不接收任意宿主路径。拒绝执行 Skill 上传脚本、任意 Shell、自定义 HTTP 和 MCP。

目录访问优先使用 os.Root，不能仅用字符串前缀或“先解析符号链接再普通打开”。os.Root 防止路径/符号链接越界，但不提供进程隔离，也不防御特权挂载。[Go os.Root 文档](https://pkg.go.dev/os#Root)

下面为可独立编译的受限读取核心示例。调用方负责打开当前 Run 的 root、授权和记录；资源目录只允许服务创建普通文件，禁止特殊文件及不受控解包：

```go
package workspace

import (
    "fmt"
    "io"
    "os"
)

func ReadLimited(root *os.Root, name string, limit int64) ([]byte, error) {
    if limit <= 0 || limit > 64<<20 {
        return nil, fmt.Errorf("invalid read limit")
    }
    f, err := root.Open(name)
    if err != nil { return nil, err }
    defer f.Close()
    info, err := f.Stat()
    if err != nil { return nil, err }
    if !info.Mode().IsRegular() { return nil, fmt.Errorf("not a regular file") }
    data, err := io.ReadAll(io.LimitReader(f, limit+1))
    if err != nil { return nil, err }
    if int64(len(data)) > limit { return nil, fmt.Errorf("file exceeds read limit") }
    return data, nil
}
```

写入检查单文件和空间限额，在工作区内部临时写、完成后替换目标，防止半写文件被当作成功结果；不允许覆盖已发布资源。文件输出上限、工具结果保留上限、进入模型的节选上限是不同限制，分别配置。

Dispatcher 先检查运行状态、Agent 允许列表和空间权限，再持久化 tool.started，执行并保存完整已接收结果后写 tool.completed/tool.failed。输出超限明确标记；不把未接收到的部分声明为完整。权限不是靠 Markdown 或模型自觉维持。

## T10 M10 完整 Trace、用量、审计与 SSE

依赖：pgx、JSONB、BlobStore、net/http。Trace 是不采样的业务记录，服务日志与未来运维链路采样不得替代它。

### T10.1 数据契约

事件包括 schema_version、space_id、session_id、run_id、trace_id、span_id、parent_span_id、sequence、event_type、occurred_at、payload/payload_ref。数据库 sequence 使用 bigint，HTTP/SSE JSON 使用字符串。

| 事件 | 关键载荷 |
| --- | --- |
| run.queued / run.started | 输入及快照引用、执行归属 |
| context.built / context.compacted | Manifest、来源范围、处理原因、摘要调用 |
| model.started | 确切请求体、模型/参数、attempt、purpose |
| model.delta | 原始帧、帧序号、归一化输出增量 |
| model.completed / model.failed | 原始响应、聚合结果、结束原因、usage、错误及完成性 |
| tool.started | call_id、工具与实现版本、参数、授权结果 |
| tool.completed / tool.failed | 完整接收结果、错误、副作用状态 |
| run.cancel_requested / run.finished | 取消来源、最终状态、结果引用及汇总用量 |

原始载荷以字节 Blob 保存，JSONB 只保存查询结构，不能以重序列化后的内容冒充供应商原文。响应中未返回的内部推理不属于 Trace。非流式响应也必须记录实际体和解析失败。

大载荷按流分块写入受控临时对象，每段完成后提交引用；Run 结束时形成完整清单。最终完整性标识只有在全部已接收帧保存后才置为 complete。可按有界时间/字节批量写入，但崩溃造成的未落盘尾部需标为 incomplete，不能承诺无故障窗口。

### T10.2 顺序与事务

所有写入方包括取消请求都通过统一事务分配序号。下面语句在同一事务中执行；分配或插入失败整体回滚：

```sql
-- $1 = space_id, $2 = run_id；返回的 last_sequence 用于事件插入。
UPDATE runs SET last_sequence = last_sequence + 1
WHERE space_id = $1 AND id = $2
RETURNING last_sequence;

-- 后续 INSERT 的 sequence 使用上面返回值，禁止单独提交序号更新。
-- 若本次同时改变运行状态，状态 UPDATE 与事件 INSERT 也在此事务中。
```

不使用无锁 `max(sequence)+1`。大 Blob 必须先持久化，再提交引用；关键 Trace 写入失败时停止后续模型/工具操作，现有动作结果未知则保留未知。业务操作与其状态/事件事务写入，网络请求不包在数据库事务内。

认证秘密不存入 Trace；可识别意外秘密需要脱敏时记录位置与规则，界面显示 redacted。载荷访问单独鉴权，普通运维日志不复制完整用户内容。保留清理记录过期状态，不返回看似完整的空记录。

usage 逐调用保存后汇总，摘要和失败尝试也计入；供应商没有返回的消耗标记未知。可配置价格版本推算费用，但不可把本地估算作为实际账单。

### T10.3 SSE 与游标

`GET /spaces/{space_id}/runs/{run_id}/events` 返回 `text/event-stream`，支持 Last-Event-ID；首次连接可用 after 查询参数。服务端每次查询已提交的 sequence > cursor，不在历史读取后直接切换为不可靠内存事件流。

SSE 的 id 为序号、event 统一为 run_event，具体事件类型放在 JSON 的 event_type 中。浏览器回放只显示数据，不再次调用工具。过期游标、非法游标和跨空间 Run 都明确拒绝。

下面为可独立编译的帧发送核心。鉴权、数据库游标、心跳与循环位于调用方；deadline 需要按每次写入更新，代理不得缓冲 SSE：

```go
package stream

import (
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type WireEvent struct {
    RunID     string          `json:"run_id"`
    Sequence  string          `json:"sequence"`
    EventType string          `json:"event_type"`
    Payload   json.RawMessage `json:"payload"`
}

func WriteEvent(w http.ResponseWriter, event WireEvent) error {
    data, err := json.Marshal(event)
    if err != nil { return err }
    rc := http.NewResponseController(w)
    if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
        return err
    }
    if _, err := fmt.Fprintf(w, "id: %s\nevent: run_event\ndata: %s\n\n", event.Sequence, data); err != nil {
        return err
    }
    return rc.Flush()
}
```

sequence 必须来自已验证的十进制序号，不能使用任意用户文本。服务端先写 Content-Type/Cache-Control，再循环发送。慢客户端超时断开后从游标重连，不阻塞执行器。结束事件之后关闭连接，客户端收到终态后也停止重连；定期重新检查登录和成员权限。

管理审计另外记录成员、发布、默认版本及凭证变更；不包含凭证原值。

## T11 M11 Blob、工作区与产物

依赖：os.Root、crypto/sha256、pgx。BlobStore 接口覆盖 PutImmutable、Open、Stat、ReleaseReference；任何读取必须获得受权的空间与对象引用，不能任意传宿主路径。

存储布局为 `data/blobs/{space_id}/{blob_id}`、`data/workspaces/{space_id}/{run_id}`；ID 由后端生成。内容摘要用于完整性而不是跨空间存在性查询。MVP 不进行跨空间去重。

先写临时文件、关闭前同步内容、完成原子替换，再写数据库引用；要承诺掉电恢复时还需同步目录项，并在目标文件系统验证语义。数据库失败留下的孤儿内容按时间宽限期及实际引用查询回收，避免与正在提交的版本竞争。

上传限制总大小、类型和数量，不执行上传内容。首版不自动解包任意归档，以免引入特殊文件、链接和解压容量问题。下载通过鉴权 Handler 使用安全文件名和 Content-Disposition，活动文件不直接作为静态站点公开。

运行结束先归档需要保留的文件，生成 Artifact 记录，再清理工作区。已引用版本资源不因 Run 清理被删除；Trace 保留策略与 Artifact 保留策略可分别配置。清理任务需报告缺失、失败和过期，不能静默破坏记录。

备份由数据库、已引用 Blob 和密钥恢复材料共同组成。数据库恢复时要核验 Blob 摘要和引用完整性，不接受“能启动”作为完整恢复证明。

## T12 M12 前端管理与调试

依赖：React、TypeScript、Vite、React Router、TanStack Query、Monaco Editor、react-markdown、openapi-typescript。选用库的用途见官方说明：[Vite](https://vite.dev/guide/)、[React Router](https://reactrouter.com/start/declarative/installation)、[TanStack Query](https://tanstack.com/query/latest/docs/framework/react/overview)、[Monaco](https://github.com/microsoft/monaco-editor)。

### T12.1 页面与数据流

按 features/agents、skills、sessions、debug、runs、settings 组织。公共组件为定义编辑器、版本差异、依赖选择、Trace 时间线、原始载荷查看、上下文检查和产物列表。

查询键统一包含 space_id，详情继续包含对象及版本 ID；变更后只失效相关查询。切换空间关闭旧 SSE、撤销旧请求、清理私有缓存和未保存表单。草稿保存携带 revision，409 时提示差异，不自动覆盖。

Monaco 延迟加载；react-markdown 禁用任意原始 HTML，不加载允许执行用户代码的插件，链接与资源 URL 使用允许列表策略。[react-markdown 官方说明](https://github.com/remarkjs/react-markdown)

API 类型由 OpenAPI 生成，禁止手改生成文件；静态类型不能代替后端校验。[openapi-typescript](https://openapi-ts.dev/introduction)

### T12.2 调试与事件消费

调试开始先创建快照和绑定会话，再提交 Run。页面展示快照的准确依赖版本；编辑器修改后提示需要新调试会话，不把旧 Trace 展示为新草稿结果。

订阅使用同源 EventSource；自动重连沿用 Last-Event-ID，页面重建时显式传递已保存游标。事件处理按 Run+sequence 去重，sequence 用 BigInt 比较，持久化仍用字符串。

核心 reducer 逻辑示例（省略 WireEvent 的其余字段和页面状态定义）：

```ts
type RunEvent = { run_id: string; sequence: string; event_type: string };
type RunView = { runId: string; cursor: string; events: RunEvent[] };

export function appendEvent(state: RunView, event: RunEvent): RunView {
  if (event.run_id !== state.runId) return state;
  if (!/^[1-9][0-9]*$/.test(event.sequence)) throw new Error("invalid sequence");
  if (BigInt(event.sequence) <= BigInt(state.cursor)) return state;
  if (BigInt(event.sequence) !== BigInt(state.cursor) + 1n) {
    throw new Error("event gap: reload from committed cursor");
  }
  return { ...state, cursor: event.sequence, events: [...state.events, event] };
}
```

调用方捕获 gap 后关闭当前连接，从最后成功应用游标读取完整事件再续订；若服务分页，只能以已获取的连续区间更新游标。初始化 cursor 为 0，或来自明确覆盖的历史快照。大型历史采用分页与窗口，不无限增长示例中的 events 数组。

事件协议符合 SSE 的 id/event/data 格式，参考 [MDN SSE](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events)。大型输入输出按需获取，缩略显示必须可查看或下载完整内容；无法取得时显示明确状态。

## T13 M13 服务组装、配置与运维基础

依赖：net/http、ServeMux、slog、embed、pgxpool。构造器显式注入实现，服务入口统一管理模块生命周期，避免全局数据库和跨空间缓存单例。

中间件顺序覆盖 request_id、请求大小限制、错误恢复、认证、空间授权和日志。SSE 使用专门长连接配置，不套用普通短请求总超时；Web 静态资源采用构建产物，API 未命中不落入 SPA 页面回退。

`/health/live` 表示进程存活，`/health/ready` 检查必要数据库/schema/存储与配置；不把某个外部模型临时不可用直接等同于整个管理服务死亡。启动检查关键配置，不启动时静默修改数据库结构。

配置至少包括数据库连接、数据目录、主密钥来源、会话设置、平台/空间并发、模型/工具限额、SSE 心跳与写入超时、Trace/Artifact 保留策略、服务构建标识。开发默认值与生产必填项分开校验；示例配置只含占位值。

统计排队与活动 Run、模型失败率、工具失败率、Trace 写入失败、磁盘空间和数据库连接。运维日志带 request_id/space_id/run_id，不复制敏感交互内容。停机与异常恢复按 T05，备份按 T11。

## T14 接口清单与错误语义

下表的业务接口均在 `/api/v1/spaces/{space_id}` 下。字段约定在各模块与后续机器可读契约内保持一致；发布和调用操作必须记录审计或 Trace。

| 接口组 | 主要操作 | 重要语义 |
| --- | --- | --- |
| /agents、/skills | 创建、列表、详情、复制、归档 | 空间范围；不物理破坏历史引用 |
| /agents/{id}/draft、/skills/{id}/draft | GET/PUT、资源管理 | expected revision，409 冲突 |
| /agents/{id}/versions、/skills/{id}/versions | POST 发布、GET 列表/详情 | 发布内容不可变，依赖精确版本 |
| /agents/{id}/default-version | PUT | 指向合法发布版本，只影响新会话 |
| /agents/{id}/debug-snapshots | POST | 固定草稿 revision 与 Skill 依赖 |
| /sessions | POST/GET | 固定定义；版本和快照二选一 |
| /sessions/{id}/runs | POST | 幂等键；同会话最多一个活跃 Run |
| /runs/{id}、/runs/{id}/cancel | GET、POST | 取消请求不等于已停止 |
| /runs/{id}/events、/runs/{id}/trace | SSE、分页 GET | 空间鉴权、游标续传、完整性标识 |
| /runs/{id}/contexts、/payloads/{id} | GET | Manifest 和受控完整载荷 |
| /artifacts/{id}/download | GET | 权限校验、受控文件名、保留状态 |
| /models、/credentials、/members | 管理操作 | 分离管理权限；秘密不回显 |

错误码至少包括 unauthenticated、forbidden、not_found、draft_conflict、version_conflict、run_active、idempotency_conflict、context_overflow、unsupported_capability、budget_exceeded、trace_unavailable、cursor_expired、interrupted。错误码与展示文案分离，内部堆栈不返回给客户端。

OpenAPI 是接口的可执行契约来源；事件 schema_version 单独管理，不把供应商原始响应强制转成自有固定字段。接口变更同步生成类型与契约测试，不靠修改页面绕过不一致。

## T15 工程布局与待收敛参数

```text
mass/
├── cmd/mass/                  # 服务入口
├── internal/
│   ├── domain/                # 实体与规则
│   ├── app/                   # 身份、空间、定义、会话和运行用例
│   ├── runtime/               # 调度、执行、上下文、模型、工具、Trace
│   ├── adapter/               # PostgreSQL、文件、模型协议、内置工具、凭证
│   ├── transport/http/        # API、SSE、静态文件
│   └── bootstrap/             # 配置与组装
├── db/{migrations,queries}/
├── api/{openapi.yaml,events/}
├── web/src/{app,features,components,lib}/
├── tests/{integration,e2e}/
├── scripts/                   # 可重复的构建和验证入口
└── docs/
    ├── design-guide.md
    ├── architecture.md
    ├── technical-design.md
    └── engineering.md
```

真实模型供应商与模型、精确工具链版本、Token 计数器、默认限额、保留时长和目标主机容量在相关阶段开始前补齐本文。它们不能以未定为由阻塞身份、版本、FakeProvider 和持久化运行闭环的建设。

所有新增技术决策归入相应 T 节；核心代码落地后用真实工程路径关联本文，并注明设计片段是否被替换。实际代码与本文不一致时先判断是实现缺陷还是设计调整，不静默保留两套规则。
