# MASS 技术方案

> 文档约束
> - 本文回答：每个模块怎样实现、依赖什么技术与第三方库、关键代码和数据契约是什么。
> - 包含：技术选型、模块算法、存储与接口设计、核心代码、运行配置、兼容性和实现限制。
> - 不包含：重新定义产品目标，或维护开发进度、测试执行报告和交付步骤。
> - 依据：[系统设计指引](design-guide.md)和[系统架构设计](architecture.md)；编码、测试与交付按[工程开发文档](engineering.md)执行。以后各模块细节继续补充在本文，不拆专题文档。
> - 状态：技术设计建议，尚未实现；更新：2026-10-17。代码为设计片段，不代表仓库已有可运行实现；片段中未定义的工程类型需在对应阶段补齐。

## T00 技术基线与依赖

### T00.1 单服务运行形态

本节选型按用户要求固定：后端 Go，前端独立工程位于 `./frontend`，前后端由同一个 Go 进程提供服务；数据库 MariaDB；工程资源持久化采用字节跳动火山引擎 TOS；LLM 接入统一使用 CloudWeGo Eino；首版采用最简账号密码登录。Agent 仍使用 AGENTS.md 与指定版本 Skills，MVP 不接入沙箱。

交付为一个 `mass` 可执行文件、一个服务配置入口和一个监听地址。同一进程提供页面、API、SSE、文件下载、调度器及 Agent 执行器，不拆 Node.js 服务、BFF、Worker 或模型代理服务。MariaDB、TOS 和实际模型端点是外部基础依赖，“单服务”指只部署一个 MASS 应用服务，并不将这些外部依赖嵌入 Go 进程。

前端采用 React + TypeScript + Vite，源码和 package.json/package-lock.json 全部在 `frontend/`。构建生成 `frontend/dist`，构建脚本复制到后端嵌入目录，通过 Go embed 编入二进制。Node.js 仅用于构建、测试或文件构建监听；默认开发流程也由 Go 统一提供页面，不要求另开 Vite HTTP 服务。

运行期间不持久化业务文件到本地：版本资源、附件、运行工作区文件、产物及完整 Trace 原始载荷均进入 TOS，元数据及状态进入 MariaDB。Go 仅使用有界内存缓冲，不提供本地磁盘回退。本地源码、配置和构建产物不属于运行中的业务资源持久化。

MVP 不增加 Redis、独立消息队列、向量数据库或独立认证服务。Eino 以 Go 库方式嵌入 MASS，并不等同于具体模型供应商或模型托管服务。

### T00.2 依赖清单

精确版本在初始化时锁定至 go.mod/go.sum、工具配置和 `frontend/package-lock.json`，不以 latest 作为可复现构建输入。

| 模块 | 技术与依赖 | 用途 |
| --- | --- | --- |
| M13 服务基础 | Go net/http、ServeMux、context、log/slog、embed | 一个进程提供所有应用能力及前端资源 |
| 数据访问 | MariaDB/InnoDB；database/sql；github.com/go-sql-driver/mysql | 连接池、显式 SQL、事务和空间过滤 |
| 迁移 | github.com/pressly/goose/v3，mysql 方言 | 同一二进制 migrate 子命令执行迁移；MVP 不引入 SQL 代码生成器 |
| M01 身份 | golang.org/x/crypto/bcrypt；crypto/rand、crypto/sha256 | 密码校验、随机会话令牌和令牌摘要 |
| M02 定义 | go.yaml.in/yaml/v4；encoding/json、crypto/sha256 | 定义解析、标准表示、内容摘要 |
| M02/M09 校验 | github.com/santhosh-tekuri/jsonschema/v6 | 配置和工具参数运行时校验 |
| M03 凭证 | crypto/aes、crypto/cipher | 供应商凭证加密，主密钥从统一配置的秘密来源注入 |
| M04—M06 运行 | database/sql、context、sync、time | 幂等、事务认领、并发与取消 |
| M07 上下文 | 平台上下文策略 + Eino schema/model | 消息转换、摘要调用和模型对应预算 |
| M08 模型 | github.com/cloudwego/eino；对应 eino-ext 模型组件 | 统一模型接口、工具定义和流式结果 |
| M08 Trace 捕获 | Eino callbacks + 自定义 http.RoundTripper | 语义事件和实际 HTTP 载荷分别记录 |
| M10 Trace | MariaDB JSON、TOS、net/http | 事件索引、原文对象与 SSE 续传 |
| M11 对象存储 | github.com/volcengine/ve-tos-golang-sdk/v2/tos | TOS 上传、读取、元信息、分片与清理 |
| M12 前端 | React、TypeScript、Vite、React Router、TanStack Query | frontend 工程中的页面、路由与查询 |
| M12 编辑/预览 | monaco-editor、react-markdown、remark-gfm | 定义编辑、差异和受限预览 |
| API 类型 | OpenAPI 3.1、openapi-typescript | HTTP 契约和前端类型 |
| 测试 | Go testing/httptest、Vitest、Playwright | 单元、协议、MariaDB/TOS 集成与浏览器验证 |

数据库驱动使用 [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql)；Repository 手写带空间条件的 SQL，通过 `sql.Tx` 保持事务边界；[goose](https://pressly.github.io/goose/)作为库集成，不要求额外常驻进程。

[YAML](https://github.com/yaml/go-yaml)与 [JSON Schema](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6)负责配置解析和校验。限制配置大小及深度，拒绝未知字段和非预期多文档输入，Schema 引用只允许平台注册资源，不动态访问外部网络。

### T00.3 MariaDB 数据与约束

采用 InnoDB、utf8mb4、UTC 时间和明确的事务边界；ID 与幂等键用二进制或大小写敏感字段。SQL 占位符为 `?`，通过 database/sql 传参。JSON 字段存结构化查询数据，原始请求/响应字节存 TOS；MariaDB JSON 是带校验的文本类型，不依赖二进制 JSON 存储或其索引行为。[MariaDB JSON 文档](https://mariadb.com/docs/server/reference/data-types/string-data-types/json)

| 数据对象 | 关键内容和约束 |
| --- | --- |
| users / auth_sessions | username、password_hash；会话令牌摘要、到期时间 |
| spaces / space_members | 空间、成员归属、is_owner；不引入细粒度权限表 |
| agents / agent_drafts / agent_versions | 草稿 revision、内容对象引用、不可变版本和默认指针 |
| skills / skill_drafts / skill_versions | 方法内容、草稿、版本及 TOS 资源清单 |
| agent_version_skills | AgentVersion 到准确 SkillVersion、显示版本号和顺序 |
| model_configs / credentials | Eino 组件类型、实际模型配置、加密供应商凭证 |
| sessions / messages | 固定定义、历史顺序、消息及协议块引用 |
| context_summaries | 来源消息范围、内容引用、生成调用和策略版本 |
| runs / run_snapshots | 状态、预算、归属、取消、有效定义/模型/工具快照 |
| session_active_runs | 每个 Session 唯一的活跃 Run 占用记录 |
| idempotency_keys | 空间、主体、操作、幂等键、请求摘要和结果引用 |
| trace_events | Run 内序号、span、事件类型、时间、JSON 载荷或 TOS 引用 |
| blobs / workspace_files / artifacts | TOS key、可选 version_id、SHA-256、大小；逻辑路径及当前对象版本 |
| audit_logs | 发布、成员、凭证等管理操作及结果 |

MariaDB 不使用带 WHERE 的部分唯一索引。通过独立占用表保证同会话只有一个活跃 Run；提交时与 Run 创建同事务插入，确认运行结束后与终态事件同事务删除。下例是迁移片段，父表须具备相同类型和联合唯一约束：

```sql
CREATE TABLE session_active_runs (
    space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    PRIMARY KEY (space_id, session_id),
    UNIQUE KEY active_run (space_id, run_id),
    CONSTRAINT active_session_fk FOREIGN KEY (space_id, session_id)
      REFERENCES sessions(space_id, id),
    CONSTRAINT active_run_fk FOREIGN KEY (space_id, run_id)
      REFERENCES runs(space_id, id)
) ENGINE=InnoDB;

CREATE UNIQUE INDEX trace_events_run_sequence
  ON trace_events(space_id, run_id, sequence);
CREATE UNIQUE INDEX request_idempotency
  ON idempotency_keys(space_id, actor_id, operation, idempotency_key);
```

版本及依赖、会话、对象元数据采用包含 space_id 的联合外键，避免跨空间关联；被引用版本使用删除限制。状态更新带旧状态条件，草稿使用 revision。版本内容不可修改，默认指针和归档状态单独可变。

DDL 与业务事务分开执行，不能假设所有迁移语句都可事务回滚。死锁只重试尚无外部副作用的数据库事务；未知提交结果按幂等键核实，不重新执行整个 Run。

### T00.4 服务契约约定

所有页面与 API 同源、同端口。API 前缀 `/api/v1`；业务路径以 `/spaces/{space_id}` 开头，传入空间 ID 仍须做成员归属检查。API 未命中不能返回 SPA 页面。

OpenAPI、事件 Schema 和数据库迁移分别存放在 `api/openapi.yaml`、`api/events/` 和 `db/migrations/`，它们是工程文件，不新增建设文档。

错误返回 code、message、request_id 和非敏感 details；列表使用游标，大载荷由 Go 服务鉴权后从 TOS 代理读取。事件 sequence 在数据库用 bigint，传到前端用十进制字符串。

### T00.5 一个配置入口

采用一个 `mass.yaml`，环境变量仅覆盖同一份配置树或提供秘密值；`serve`、`migrate`、`user` 等子命令共用它。以下是设计样例，`*_env` 表示程序明确读取该环境变量，不依赖 YAML 自动插值：

```yaml
http:
  listen: ":8080"
  public_url: "https://mass.example.com"
database:
  dsn_env: "MASS_MARIADB_DSN"
tos:
  endpoint_env: "MASS_TOS_ENDPOINT"
  region_env: "MASS_TOS_REGION"
  bucket_env: "MASS_TOS_BUCKET"
  prefix: "mass/"
  access_key_env: "MASS_TOS_ACCESS_KEY"
  secret_key_env: "MASS_TOS_SECRET_KEY"
auth:
  session_ttl: "24h"
  cookie_secure: true
llm:
  framework: "eino"
  provider: "ark" # 可替换的首个组件示例，不代表已确定实际模型供应商
  model_env: "MASS_LLM_MODEL"
  api_key_env: "MASS_LLM_API_KEY"
secrets:
  master_key_env: "MASS_MASTER_KEY"
runtime:
  max_concurrent_runs: 4 # 示例值，部署时配置
```

前端 API 使用相对路径，不单独保存另一套后端地址。TOS endpoint/region/bucket 与实际模型仍需用户环境提供，不猜测真实值。配置与二进制可存在本地，工程资源与历史状态不依赖本地目录。

此配置管理服务部署和模型接入默认值；空间内的模型选择与凭证是 MariaDB 中的业务配置。初始化时可显式导入默认模型，之后以空间配置为准，重启不得覆盖用户修改；Run 保存最终解析结果及来源，避免文件与数据库互相覆盖。

## T01 M01 最简密码登录与空间归属

依赖：net/http、bcrypt、crypto/rand、crypto/sha256、database/sql。首版只实现“账号密码校验 → 建立会话 → 查询当前用户 → 退出”，不接入 OAuth/SSO、验证码、注册找回、JWT 刷新体系或通用 RBAC 框架。

用户记录存 username 和 bcrypt password_hash；使用同一个 `mass user create --config mass.yaml` 子命令初始化用户，密码通过交互输入或指定秘密来源提供，不作为明文命令参数或配置常量。支持多个用户，不能用全局共享密码代替用户身份。

最小认证流程：

```text
POST /api/v1/auth/login(username, password)
→ 查询用户并调用 bcrypt.CompareHashAndPassword
→ 生成随机会话令牌，将令牌摘要和到期时间保存到 MariaDB
→ 设置 HttpOnly Cookie
→ 后续请求根据 Cookie 找到 user_id，并检查资源的 space_id 归属
```

Cookie 使用 SameSite=Lax，HTTPS 下 Secure；本地回环地址开发可显式关闭 Secure。会话固定到期，不做自动续期或 refresh token。退出删除会话；登录失败统一错误，密码与会话令牌不写日志。

同源写请求校验 Origin 和预期 Content-Type；文件上传同样验证来源。保持基础防跨站请求措施，不建立独立 CSRF Token 子系统。SSE 复用同源 Cookie，定期检查登录是否到期，不把令牌放到 URL。

空间隔离仍保留：用 space_members 检查成员关系，用 is_owner 区分基础管理操作。MVP 不维护角色矩阵和逐动作授权配置；成员可操作本空间 Agent/Skill/Run，owner 管理成员和模型凭证。首版账号及成员配置可由同一二进制的管理子命令完成，前端先提供登录、退出、当前用户和空间切换。

接口最少为 `POST /auth/login`、`POST /auth/logout`、`GET /auth/me` 和当前用户空间列表；用户未登录或不属于目标空间时拒绝读取，包括 SSE、TOS 文件代理与历史载荷。复杂用户管理页面不作为首版认证阻塞项。

## T02 M02 定义、Skill 依赖与发布

依赖：YAML v4、JSON Schema、database/sql、TOS BlobStore。内容结构如下：

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

接口组：Agent/Skill 创建、草稿 GET/PUT（携带 revision）、TOS 资源上传、发布、版本列表/详情/差异、依赖查询、归档；Agent 另有默认版本切换与调试快照创建。

## T03 M03 模型配置与凭证

依赖：database/sql、AES-GCM、crypto/rand。模型配置包含 provider、model_id、能力与容量、非秘密参数、批准端点和 credential_ref。一般空间用户从批准端点选择，不直接指定任意网络地址。

凭证密文关联 space_id、credential_id 和 key_id 作为附加认证数据；每次加密生成唯一 nonce。主密钥通过服务配置的秘密文件或环境注入，不提交仓库，不与密文一起作为数据库字段保存。明确密钥轮换和旧备份解密的要求。

Run 固定非秘密有效配置和凭证引用；每次实际调用重新验证权限及凭证有效性，记录非秘密凭证版本标识。凭证轮换不要求历史快照保存旧秘密。

参数能力在发布/调试和实际调用前校验。Provider 不支持工具、流式或输入类型时返回明确错误，不静默降级。费用记录同时保存价格来源及版本；无价格时只展示 usage。

接口组：模型配置 CRUD、可用模型能力查询、凭证创建/替换/禁用；凭证响应永不回显秘密。

## T04 M04 会话、消息与 Run

依赖：database/sql、crypto/sha256、context。Session 固定发布版本或调试快照；Run 快照记录准确 Skill 版本、有效模型/上下文配置、工具 Schema/实现版本、平台指令版本和服务构建标识。

提交事务锁定 Session，保存用户消息、快照、queued Run、session_active_runs 占用、幂等记录及初始 Trace 事件；占用主键冲突时整个事务回滚。相同主体/操作/幂等键且请求摘要相同返回原结果；内容不同返回 409。默认版本只在创建新 Session 时解析。

幂等处理必须在返回“已有活跃 Run”之前识别同一次请求的重试；两个并发相同请求通过唯一约束裁决，并读取赢家结果，不另建 Run。

终态固定为 succeeded/failed/cancelled；queued/running 属于活跃状态。`cancel_requested_at` 表达请求取消，不直接证明工具已停止。queued 可原子转 cancelled；running 由执行器确认结束后转终态。

最终回复、Run 终态、usage 汇总、删除 session_active_runs 占用和 run.finished 同事务提交。部分助手输出保留在 Trace 中，失败时不作为完整回复提交。下一轮历史保留完成的协议块；失败的副作用以已完成/失败/未知说明进入后续上下文，不拼入缺少配对的不完整工具链。

接口组：`POST /sessions`、`GET /sessions/{id}/messages`、`POST /sessions/{id}/runs`、`GET /runs/{id}`、`POST /runs/{id}/cancel`。提交返回 202 和 Run ID；重复提交可返回原 Run 及其当前状态。

## T05 M05 单进程调度、认领与停止

依赖：MariaDB/InnoDB、database/sql、context、sync、time。调度器与 API、前端资源服务在同一个 Go 进程中。runs 表是任务来源，内存通知用于唤醒，轮询补漏；平台与空间并发由进程内准入器限制。

MVP 由一个 dispatcher 串行认领，再交给受限 goroutine 执行。不要求队列 SKIP LOCKED 能力；一个认领事务只完成下列数据库操作，不能跨模型或 TOS 请求持有行锁：

```sql
START TRANSACTION;
SELECT id FROM runs
WHERE space_id = ? AND status = 'queued'
  AND cancel_requested_at IS NULL
ORDER BY created_at, id
LIMIT 1 FOR UPDATE;

-- 应用取出 ID 后，用同一 sql.Tx 更新并检查 RowsAffected == 1。
UPDATE runs
SET status = 'running', owner_id = ?, started_at = UTC_TIMESTAMP(6),
    heartbeat_at = UTC_TIMESTAMP(6)
WHERE space_id = ? AND id = ? AND status = 'queued';
-- 同事务分配事件序号并插入 run.started。
COMMIT;
```

增加 `(space_id, status, created_at, id)` 索引，先获得并发名额再认领。`FOR UPDATE` 必须运行在有效事务内。[MariaDB 行锁文档](https://mariadb.com/docs/server/reference/sql-statements/data-manipulation/selecting-data/for-update)

服务启动用独立 `*sql.Conn` 持有 `GET_LOCK('mass:<database>:runtime', 0)`，返回 1 才成为执行实例，退出在同连接 RELEASE_LOCK 并关闭。该连接不能借出到连接池；锁随连接而非事务生命周期存在。持锁连接异常时停止接单和后续动作，不能宣称它提供自动故障切换或副作用恰好一次。[MariaDB GET_LOCK](https://mariadb.com/docs/server/reference/sql-functions/secondary-functions/miscellaneous-functions/get_lock)

Run context 派生自服务执行生命周期，不使用提交请求的 context。取消先写 MariaDB，再通知执行器；queued 取消与占用释放同事务，running 直到实际停止才释放占用。断开浏览器不会自动取消。

确认旧进程退出后，重启将遗留 running 标记为 interrupted 失败，保存未完成调用并释放占用；queued 继续调度。资源从 MariaDB/TOS 恢复，不读取宿主工作目录，也不自动重做未知副作用。

停机先停止接单和认领，有界等待或取消已有执行，刷写 Trace；无法停止的工具由进程退出终止，重启时标记中断，而不是提前报告成功取消。

## T06 M06 执行循环

依赖：ContextBuilder、基于 Eino 的 ModelGateway、ToolDispatcher、TraceRecorder、RunStore。执行器负责 MASS 的版本、预算与生命周期；模型调用统一走 Eino 组件，不直接实现供应商 SDK。MVP 保留这一个显式执行循环，不再套一层 Eino Agent 的完整循环，避免重复调度工具和预算。后续如切换 Eino ADK，需要迁移相同的权限、上下文与 Trace 约束。

核心循环骨架如下；Gateway、Tools 和终结器负责各自动作前后的持久化，所有错误必须传播。`Build` 内的摘要也走同一 Gateway 并计入预算：

```go
func (e *Executor) Execute(ctx context.Context, run RunSnapshot) error {
    for step := 0; step < run.MaxSteps; step++ {
        if err := e.Control.Check(ctx, run); err != nil { return err }
        input, err := e.Context.Build(ctx, run)
        if err != nil { return err }
        reply, err := e.Model.Call(ctx, run, input)
        if err != nil { return err }
        if !reply.Complete { return ErrIncompleteModelOutput }
        if len(reply.ToolCalls) == 0 {
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

依赖：固定内容解析器、MariaDB 消息仓储、TOS BlobStore、Eino schema/model 与模型计数适配器。业务上下文策略由 MASS 管理，模型输入转换为 Eino 消息；摘要也通过同一 Eino 网关调用。

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

## T08 M08 基于 Eino 的 LLM 接入与完整 Trace

### T08.1 框架与模块边界

统一采用 `github.com/cloudwego/eino` 的 model/schema 接口及 `eino-ext` 模型组件。MASS 的 M08 包装 Eino，M06 负责执行与工具循环，M07 管理上下文，M09 保留工具权限和 TOS 工作区访问边界。Eino 是进程内框架，不额外启动 LLM 服务，也不代替实际模型 endpoint 与凭证。

首版使用 `model.ToolCallingChatModel`、`schema.Message`、`schema.ToolInfo`，通过 `Generate`/`Stream` 接入实际模型。工具用 `WithTools` 绑定到当前调用实例，避免并发共享模型上原地修改工具集合。模型能力和协议信息通过组件适配，不丢弃继续对话所需字段。[Eino 模型接口](https://github.com/cloudwego/eino/blob/main/components/model/interface.go)

Eino ADK/Graph 可以后续承接更复杂编排，首版不同时启用两个完整 Agent 循环。所有自有组件仍采用 Eino 的模型接口进行真调用和 FakeChatModel 测试，不能以另写一套 HTTP 模型服务替代 Eino。

### T08.2 核心调用代码

以下片段演示 Eino 流的单一消费路径。业务 DTO 转换、授权、预算、完整性判定及 SQL/TOS 事务在网关外层，`record` 回调必须落库成功后才返回。引用的框架 API 以初始化时锁定版本为准：

```go
package modelgateway

import (
    "context"
    "errors"
    "io"

    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/schema"
)

func StreamWithTools(ctx context.Context, base model.ToolCallingChatModel,
    messages []*schema.Message, tools []*schema.ToolInfo,
    record func(context.Context, *schema.Message) error) ([]*schema.Message, error) {
    bound, err := base.WithTools(tools)
    if err != nil { return nil, err }
    reader, err := bound.Stream(ctx, messages)
    if err != nil { return nil, err }
    defer reader.Close()
    var chunks []*schema.Message
    for {
        chunk, err := reader.Recv()
        if errors.Is(err, io.EOF) { return chunks, nil }
        if err != nil { return nil, err }
        if err := record(ctx, chunk); err != nil { return nil, err }
        chunks = append(chunks, chunk)
    }
}
```

此片段假定外层已限制总输出容量，生产实现按界限聚合或外存；不能无限累积 chunks。`io.EOF` 只代表流结束，是否完整仍需检查 finish reason、工具 JSON、截断和供应商 usage。使用 Eino 对应版本的消息合并能力处理工具参数增量，不能逐片段当作完整工具调用执行。

### T08.3 两层 Trace

Eino callbacks 用于模型输入输出、错误、耗时等语义层记录，但不能当作供应商实际 HTTP 原文。实际 input/output 捕获通过注入组件 HTTPClient 的自定义 `http.RoundTripper` 完成，两者通过 Run/span/attempt 关联。[Eino callbacks 概述](https://www.cloudwego.io/docs/eino/overview/)

请求在 Eino/供应商 SDK 完成序列化后由 Transport 捕获：限制容量、保存最终请求体至 TOS、提交引用及 model.started，之后才发送相同字节；认证头和签名查询参数不记录。拒绝不受支持的请求流，不默默跳过捕获。

响应以包装的 Body 接收并按有界块写入 TOS，再把已保存的字节交给 Eino 解码；保留原始顺序、错误状态码和解码失败的原文。慢存储可产生有界背压，持久化失败立即取消模型请求。Eino 语义事件和原文区块分别保存，通过清单关联；流被提前 Close 或中断时标记 incomplete。

最终请求体在 SDK 传输层捕获，不能由平台重新序列化并当作实际请求。元信息里去掉 API Key，载荷如果必须脱敏则明确标记，不能冒充逐字原文。

### T08.4 首个供应商与重试

Eino 的选择不自动确定模型供应商。建议先验证 `github.com/cloudwego/eino-ext/components/model/ark`，实际 provider/model/endpoint 由部署配置确认；若使用其他 Eino 组件，必须同样支持自定义 HTTP 捕获和完整协议数据。

Ark 组件配置可注入 HTTPClient 并设置 RetryTimes；初版将 RetryTimes 显式设为 0，网关负责有界重试和每次尝试记录。以下是已批准端点和凭证解析后的构造片段：

```go
noRetry := 0
chatModel, err := ark.NewChatModel(ctx, &ark.ChatModelConfig{
    APIKey: apiKey,
    Model: modelID,
    BaseURL: approvedBaseURL,
    HTTPClient: tracedHTTPClient,
    RetryTimes: &noRetry,
})
```

`tracedHTTPClient` 同时负责批准端点/重定向检查和请求记录，必须传递调用 context；不因使用 SDK 而启用看不见的重试。非成功响应、流中断及部分输出后的失败分别记录，不拼接新尝试为同一个成功响应。[Ark 官方组件源码](https://github.com/cloudwego/eino-ext/blob/main/components/model/ark/chatmodel.go)

FakeChatModel 实现同一个 Eino 模型接口；本地假 HTTP 供应商验证真实 Eino 扩展组件的序列化和原文捕获。完整 Trace 验证通过前不将该组件标记为接入完成。

## T09 M09 内置工具与对象工作区

依赖：Registry/Dispatcher、JSON Schema、TOS WorkspaceStore、context。保留 read_file、write_file、list_files、read_skill_resource 名称，但“文件”是运行工作区中的逻辑文件，不访问宿主磁盘，不挂载 TOS 为本地文件系统。

工具定义包含名称、实现版本、参数 Schema、输出结构、基础空间/Agent 允许列表、超时、最大输入输出和副作用类型。实际版本与 Schema 固定到 Run 快照；不兼容升级用新标识或明确迁移。

WorkspaceStore 使用 `(space_id, run_id, logical_path)` 查找 MariaDB 中的对象映射，再访问 TOS。调用方只能提供逻辑路径，不能提供 bucket、完整对象 key、预签名 URL 或其他空间 ID。Skill 资源从固定 SkillVersion 的只读清单读取，禁止写文件工具修改。

逻辑路径校验核心示例：

```go
package workspace

import (
    "fmt"
    "path"
    "strings"
)

func ValidateLogicalPath(name string) (string, error) {
    if name == "" || len(name) > 1024 || path.IsAbs(name) ||
        strings.ContainsAny(name, "\\\x00") {
        return "", fmt.Errorf("invalid workspace path")
    }
    for _, part := range strings.Split(name, "/") {
        if part == "" || part == "." || part == ".." {
            return "", fmt.Errorf("invalid workspace path segment")
        }
    }
    return name, nil
}
```

参数通过 JSON 传入且只解码一次，校验后的路径不能再做 URL 解码。数据库路径字段使用一致的大小写敏感比较；逻辑路径不直接拼成可访问对象 key。MVP 不实现符号链接、硬链接或归档自动解包。

`write_file` 先把新内容写入唯一且不可变的 TOS 对象，再用 revision 条件事务更新 workspace_files 指针并写 tool.completed；原内容不会被原地覆盖。返回成功前对象内容和元数据均已确认。`read_file` 按该版本读取，`list_files` 查询逻辑目录索引，`read_skill_resource` 只能读取快照引用的资源。

超时后对象可能已上传但数据库未引用，应记为待核实或孤儿并延迟回收，不盲目重做工具。一次工具调用的映射更新与 call_id/幂等记录关联，重试不能重复产生新逻辑效果。

Dispatcher 执行前校验登录主体的空间归属、Agent 工具列表、预算和取消状态，持久化 tool.started；执行后保存完整接收结果和结构化错误。内置工具不开放 Shell、用户脚本、自定义 HTTP 或 MCP，执行仍发生在当前 Go 进程。

## T10 M10 完整 Trace、用量、审计与 SSE

依赖：database/sql、MariaDB JSON、TOS BlobStore、net/http。Trace 是不采样的业务记录，服务日志与未来运维链路采样不得替代它。

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

原始载荷以 TOS 字节对象保存，MariaDB JSON 只保存查询结构，不能以重序列化后的内容冒充供应商原文。响应中未返回的内部推理不属于 Trace。非流式响应也必须记录实际体和解析失败。

大载荷按有界内存区块写入 TOS 不可变对象，每段成功后提交引用；Run 结束时形成完整清单。尚未完成的 multipart 不算已持久化可读区块，不可提前向前端确认。最终完整性标识只有在全部已接收帧保存后才置为 complete。可按有界时间/字节批量写入，但崩溃造成的未持久化尾部需标为 incomplete，不能承诺无故障窗口。

### T10.2 顺序与事务

所有写入方包括取消请求都通过统一事务分配序号。下面语句在同一事务中执行；分配或插入失败整体回滚：

```sql
START TRANSACTION;
SELECT last_sequence FROM runs
WHERE space_id = ? AND id = ? FOR UPDATE;
-- 应用计算 next_sequence = last_sequence + 1。
UPDATE runs SET last_sequence = ? WHERE space_id = ? AND id = ?;
-- 同一 sql.Tx INSERT trace_events，sequence 使用 next_sequence。
-- 若改变运行状态，状态更新与事件写入也在这个事务中。
COMMIT;
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

## T11 M11 TOS 持久化、工作区与产物

### T11.1 统一对象存储

采用火山引擎 TOS 官方 Go SDK `github.com/volcengine/ve-tos-golang-sdk/v2/tos`，通过 endpoint、region、bucket 和凭证创建 ClientV2。MASS 中所有需要持久保留的工程资源都由它承接，不实现生产本地 BlobStore，不在失败时写宿主磁盘备用。[TOS 官方 SDK](https://github.com/volcengine/ve-tos-golang-sdk)

对象 key 由服务生成，例如 `mass/{space_id}/objects/{object_id}`；保存对象类型、SHA-256、字节数、ETag、可选 version_id 与状态。路径映射、引用关系和当前版本在 MariaDB 中。不得把 ETag 直接当作内容 SHA-256，也不依赖 TOS Bucket 版本控制替代 Agent/Skill 的业务版本。

| 资源 | 持久化方式 |
| --- | --- |
| AGENTS.md、SKILL.md、模板、版本 assets | 不可变 TOS 对象 + MariaDB 发布版本清单 |
| 用户附件、工具产物 | TOS 对象 + 授权元数据 |
| 运行工作区文件 | TOS 内容版本 + workspace_files 逻辑路径及 revision |
| 模型实际请求/响应、工具完整大结果 | 有序 TOS 区块 + Trace 清单/完整性状态 |
| 运行、会话、事件索引、空间和权限 | MariaDB；不依赖本地文件 |

普通上传使用流/有界缓冲直接发送 TOS，大文件使用受限并发 multipart；不使用 SDK 的本地文件上传/下载帮助方法。配置和编译产物属于部署输入，可以存在本地，但不能存用户运行内容。

### T11.2 对象写入示例

下面只展示已授权、已分配唯一 key 的小对象上传；大小上限、凭证初始化、SHA-256 计算、状态和元数据事务由调用方提供。使用 SDK 的禁止覆盖选项，避免版本内容被原地替换：

```go
package blob

import (
    "bytes"
    "context"

    "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

func PutNewObject(ctx context.Context, client *tos.ClientV2,
    bucket, key string, data []byte) error {
    _, err := client.PutObjectV2(ctx, &tos.PutObjectV2Input{
        PutObjectBasicInput: tos.PutObjectBasicInput{
            Bucket: bucket, Key: key,
            ContentLength: int64(len(data)),
            ForbidOverwrite: true,
        },
        Content: bytes.NewReader(data),
    })
    return err
}
```

GetObjectV2 成功后必须关闭返回结果的 Content（io.ReadCloser），使用有界流传输给工具或用户。具体 SDK 字段和 multipart/重试行为用锁定版本验证。[TOS Go API](https://pkg.go.dev/github.com/volcengine/ve-tos-golang-sdk/v2/tos)

### T11.3 跨存储一致性

1. MariaDB 记录上传意图、对象 ID 和用途，暂不可被业务引用。
2. 上传到新 TOS key；收到成功或通过受控 Head/Get 核实内容后，将对象标为 ready。
3. 发布、写文件或完成工具结果的事务只引用 ready 对象，维护业务引用和 revision。
4. DB 提交失败留下未引用对象；有宽限期地查询上传状态和引用，回收确认无引用对象，并清理超时 multipart。

TOS 与 MariaDB 没有共同事务；不能使用“本地 rename 后提交数据库”的假设。响应丢失时根据对象 ID、摘要和请求记录核实，不盲目覆盖或把对象缺失当作成功。引用删除与回收需锁定元数据、设置 deleting 状态并拒绝新引用，防止 GC 与发布竞争。

读取私有 Bucket 必须由 Go 服务先做用户/空间鉴权，再从 TOS 流式代理；浏览器不获得 AK/SK，不要求直连 TOS 或额外 CORS 服务。前端静态资源来自 Go 二进制，业务附件来自 TOS，二者不混淆。

### T11.4 保留、恢复与故障

运行结束时将应保留的逻辑文件关联为 Artifact；清理工作区只移除不再需要的逻辑引用，不删除仍被版本、Trace 或产物引用的对象。TOS 生命周期策略必须与应用引用策略协调，不能按前缀盲删活跃内容。

TOS 不可用或对象无法确认保存时，拒绝新的相关写操作，停止后续 Agent 动作；不得回退到本地持久化。可以保留有限内存中的未完成块，但重启后不声称其已保存。

恢复需要 MariaDB 备份、仍保留的 TOS 对象及解密材料；数据库备份不包含对象内容。恢复验收校验对象清单/摘要和缺失状态。删除 MASS 主机运行目录后，登录状态、已发布资源和历史产物仍应能从数据库与 TOS 读取；中断执行本身按 T05 失败处理，不自动续跑。

## T12 M12 前端管理与调试

依赖：React、TypeScript、Vite、React Router、TanStack Query、Monaco Editor、react-markdown、openapi-typescript。选用库的用途见官方说明：[Vite](https://vite.dev/guide/)、[React Router](https://reactrouter.com/start/declarative/installation)、[TanStack Query](https://tanstack.com/query/latest/docs/framework/react/overview)、[Monaco](https://github.com/microsoft/monaco-editor)。

### T12.1 页面与数据流

前端源码只放在 `frontend/src/`，按 features/agents、skills、sessions、debug、runs、settings 组织；package.json、lockfile、Vite 配置和前端测试均位于 `frontend/`。公共组件为定义编辑器、版本差异、依赖选择、Trace 时间线、原始载荷查看、上下文检查和产物列表。

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

## T13 M13 单服务组装、前端嵌入与配置

依赖：net/http、ServeMux、slog、embed、database/sql、Eino、TOS SDK。一个 `cmd/mass` 入口构造数据库、TOS、模型组件、调度与 API，不启动独立前端、Worker 或 LLM 代理进程。

前端构建链固定为：`npm --prefix frontend ci` → 类型检查/测试 → `npm --prefix frontend run build` → 复制 frontend/dist 到 internal/transport/http/ui/dist → Go 构建。后端嵌入目录是生成物，不手工维护另一份前端源码；所有 Go 编译/测试任务需先准备嵌入资源。

嵌入示例位于后端 `internal/transport/http/ui/assets.go`：

```go
package ui

import (
    "embed"
    "io/fs"
)

//go:embed all:dist
var built embed.FS

func Assets() (fs.FS, error) {
    return fs.Sub(built, "dist")
}
```

同一 ServeMux 下 `/api/v1/` 提供 API/SSE，`/health/` 提供健康检查，`/` 提供静态页面及前端路由回退。仅合法页面路径回退 index.html；不存在的 API、健康接口和静态文件返回实际错误。SPA 使用相对 API 地址，同源 Cookie，无反向代理拼接前后端的必需配置。

标准开发入口使用前端 build/watch 和 Go 重建/重启，浏览器始终只访问 Go 地址。构建监听不是额外应用服务；不把 Vite dev server、SSR 或 Node 常驻进程设为必需条件。

统一配置仅有 T00.5 的 `mass.yaml` 入口；同一二进制提供 `serve`、`migrate`、`user`/`member` 管理子命令。前端不维护 API base URL 配置，模型与 TOS 秘密不会编入页面。

`/health/live` 表示进程存活；`/health/ready` 检查 MariaDB schema、配置及必要 TOS 连通/授权状态，使用有界缓存避免每次探针都发云请求。外部模型暂时不可用以调用错误呈现，不使整个管理服务伪装进程死亡。

服务日志输出到 stdout，附 request_id/space_id/run_id，不复制用户原始内容。监控队列、并发、模型错误、Trace/TOS 写入失败、上传字节数和数据库连接；不配置业务数据目录或磁盘缓存。关闭时停止认领并完成有界 TOS/事件刷写。

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
| /models、/credentials、/members | 基础 owner 管理操作；成员可先用同二进制命令配置 | 不引入权限矩阵；秘密不回显 |

错误码至少包括 unauthenticated、forbidden、not_found、draft_conflict、version_conflict、run_active、idempotency_conflict、context_overflow、unsupported_capability、budget_exceeded、trace_unavailable、cursor_expired、interrupted。错误码与展示文案分离，内部堆栈不返回给客户端。

OpenAPI 是接口的可执行契约来源；事件 schema_version 单独管理，不把供应商原始响应强制转成自有固定字段。接口变更同步生成类型与契约测试，不靠修改页面绕过不一致。

## T15 工程布局与待收敛参数

```text
mass/
├── cmd/mass/                       # 唯一程序：serve/migrate/user/member
├── internal/
│   ├── domain/
│   ├── app/
│   ├── runtime/                    # 同进程调度、执行、上下文、工具和 Trace
│   ├── adapter/
│   │   ├── mariadb/                # database/sql Repository
│   │   ├── tos/                    # 对象与虚拟工作区
│   │   └── eino/                   # 模型组件、callbacks 和传输 Trace
│   ├── transport/http/ui/          # assets.go + 生成的 dist 嵌入目录
│   └── bootstrap/                  # 单配置树及依赖组装
├── frontend/
│   ├── package.json
│   ├── package-lock.json
│   ├── vite.config.ts
│   ├── src/{app,features,components,lib}/
│   └── dist/                       # 构建生成，不是运行期资源目录
├── db/{migrations,queries}/
├── api/{openapi.yaml,events/}
├── configs/mass.example.yaml       # 单服务配置模板，不含秘密
├── tests/{integration,e2e}/
├── scripts/                        # 构建、嵌入和验证
└── docs/
    ├── design-guide.md
    ├── architecture.md
    ├── technical-design.md
    └── engineering.md
```

不建立持久化的 `data/blobs`、宿主工作区或前端独立服务配置。源码和构建缓存可以使用开发磁盘，应用资源持久化只使用 MariaDB/TOS。

待收敛项仅包括 MariaDB/Go/Node 与 Eino/TOS SDK 的精确版本、实际 TOS 配置与权限、Eino 模型供应商和模型、计数器、默认限额及保留时长；已指定的单服务、MariaDB、TOS、Eino、frontend 和简单登录不再作为待选方案。

真实云资源缺失时用内存 TOS fake 和 Eino FakeChatModel 完成可验证编码，但不能以 fake 测试替代真实 TOS/模型接入验收。框架与 SDK 的实际方法、字段和重试行为以锁定版本核验，并将证据记录到工程开发文档。
