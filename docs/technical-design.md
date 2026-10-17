# MASS 技术方案

> 文档约束
> - 本文回答：每个模块怎样实现、依赖什么技术与第三方库、关键代码和数据契约是什么。
> - 包含：技术选型、模块算法、存储与接口设计、核心代码、运行配置、兼容性和实现限制。
> - 不包含：重新定义产品目标，或维护开发进度、测试执行报告和交付步骤。
> - 依据：[系统设计指引](design-guide.md)和[系统架构设计](architecture.md)；编码、测试与交付按[工程开发文档](engineering.md)执行。以后各模块细节继续补充在本文，不拆专题文档。
> - 状态：技术设计建议，尚未实现；更新：2026-10-17。代码为设计片段，不代表仓库已有可运行实现；片段中未定义的工程类型需在对应阶段补齐。

## T00 技术基线与依赖

### T00.1 单服务运行形态

本节选型按用户要求固定：后端 Go，前端独立工程位于 `./frontend`，前后端由同一个 Go 进程提供服务；数据库 MariaDB；工程资源持久化采用字节跳动火山引擎 TOS；LLM 接入统一使用 CloudWeGo Eino；首版采用最简账号密码登录；运行事件采用 AG-UI 协议，通过 SSE 传输。Agent 仍使用 AGENTS.md 与指定版本 Skills，MVP 不接入沙箱。

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
| M07 上下文 | SessionJournal、ContextPlanner/Projector + Eino schema/model | 固定日志边界、确定性投影、摘要与前缀稳定 |
| M08 模型 | github.com/cloudwego/eino；对应 eino-ext 模型组件 | 统一模型接口、工具定义和流式结果 |
| M08 Trace 捕获 | Eino callbacks + 自定义 http.RoundTripper | 语义事件和实际 HTTP 载荷分别记录 |
| M10 日志与 Trace | MariaDB JSON、TOS、net/http | 会话原始事件、内容引用、AG-UI 事件投影与 SSE 续传 |
| M10/M12 交互协议 | AG-UI；Go SDK github.com/ag-ui-protocol/ag-ui/sdks/community/go；前端 @ag-ui/core | 标准事件类型/校验、服务端序列化、前端协议消费 |
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
| sessions | 空间归属、会话标识、last_sequence；固定定义来自 session.created |
| runtime_events / event_objects | Session 内追加日志、事件格式版本、因果关系、有序内容对象关联 |
| agui_streams / agui_events | 每个 Run 固定协议/映射版本、日志处理检查点、独立 ui_sequence、有序 AG-UI JSON；可重建的传输视图 |
| messages / runs / run_snapshots | 从日志同步维护的消息、运行状态及快照视图，保存来源事件位置 |
| context_summaries / context_projections | 摘要来源、投影输入边界、策略/渲染版本、epoch、Manifest 与投影对象引用 |
| session_active_runs | 每个 Session 唯一的活跃 Run 占用记录 |
| idempotency_keys | 空间、主体、操作、幂等键、请求摘要和结果引用 |
| content_objects / workspace_files / artifacts | TOS key、可选 version_id、SHA-256、大小；逻辑路径及当前对象版本 |
| model_cache_bindings（启用显式缓存时） | 空间、供应商作用域、前缀/配置指纹、缓存引用和到期时间；不作为历史来源 |
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

CREATE UNIQUE INDEX runtime_events_session_sequence
  ON runtime_events(space_id, session_id, sequence);
CREATE UNIQUE INDEX runtime_events_event_id
  ON runtime_events(space_id, event_id);
CREATE INDEX runtime_events_run_sequence
  ON runtime_events(space_id, run_id, sequence);
CREATE UNIQUE INDEX request_idempotency
  ON idempotency_keys(space_id, actor_id, operation, idempotency_key);
```

版本及依赖、会话、对象元数据采用包含 space_id 的联合外键，避免跨空间关联；被引用版本使用删除限制。状态更新带旧状态条件，草稿使用 revision。版本内容不可修改，默认指针和归档状态单独可变。

DDL 与业务事务分开执行，不能假设所有迁移语句都可事务回滚。死锁只重试尚无外部副作用的数据库事务；未知提交结果按幂等键核实，不重新执行整个 Run。

### T00.4 服务契约约定

所有页面与 API 同源、同端口。API 前缀 `/api/v1`；业务路径以 `/spaces/{space_id}` 开头，传入空间 ID 仍须做成员归属检查。API 未命中不能返回 SPA 页面。

OpenAPI、事件 Schema 和数据库迁移分别存放在 `api/openapi.yaml`、`api/events/` 和 `db/migrations/`，它们是工程文件，不新增建设文档。

错误返回 code、message、request_id 和非敏感 details；列表使用游标，大载荷由 Go 服务鉴权后从 TOS 代理读取。日志 sequence 与 AG-UI 传输 ui_sequence 分别在数据库用 bigint，游标经 HTTP/SSE 传为十进制字符串，禁止混用。SSE data 直接是 AG-UI 事件对象，协议规则见 T10.3—T10.6。

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

依赖：YAML v4、JSON Schema、database/sql、TOS ContentStore。内容结构如下：

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

依赖：database/sql、SessionJournal、ContentStore、crypto/sha256、context。会话与执行链路以 T10 的 runtime_events 为权威事实来源；messages、runs、run_snapshots 是与事件同事务更新的查询/调度视图，记录 applied_sequence 和 reducer_version，不允许脱离日志单独改变业务事实。Session 的游标、执行锁和心跳属于协调数据，不要求逐次转成模型输入或审计事件。

创建 Session 时追加 session.created，固定 Agent/Skill 准确版本、指令原文对象、有序 Skill 清单、平台指令内容及版本；不能只保存可变配置 ID。Agent/Skill 发布库负责编辑与版本管理，日志用不可变引用复用内容，投影通过日志和它引用的对象读取定义。

用户提交先把原始文本和附件保存为 content_objects；同一数据库事务检查权限、锁定 Session、追加 user.message.accepted 和 run.created、更新消息/queued Run 视图、插入 session_active_runs 占用及幂等记录。run.created 固定模型非秘密配置、工具 Schema/实现版本、预算、构建标识和 user_event_id。未提交成功的输入不进入后续投影。默认 Agent 版本只在创建新 Session 时解析。

相同主体/操作/幂等键且请求摘要相同返回原结果；内容不同返回 409。幂等核实先于活跃 Run 冲突判断；并发提交由唯一约束裁决，不重复追加用户事件。占用主键冲突时整个事务回滚。

终态为 succeeded/failed/cancelled；queued/running 属于活跃状态。run.cancel.requested 表示收到取消，不代表停止；queued 可在同事务追加 run.finished 并释放占用，running 等实际停止后终结。

run.finished、最终回复的来源响应引用、usage 汇总、状态视图更新及占用删除同事务提交。最终回复引用 model.response.completed，不另造一份同内容原始消息。部分输出保留在日志，但不作为成功回复。失败运行的历史参与规则见 T07；取消或失败不能抹去已确认副作用。

MVP 不原地修改已接受的消息。补充或纠错追加新用户消息；历史分支和消息编辑重算不属于首版范围。视图重建只读取事件和内容，不调用模型或工具；离线重建验证通过后才切换视图，不直接驱动生产调度。

接口组：POST /sessions、GET /sessions/{id}/messages、POST /sessions/{id}/runs、GET /runs/{id}、POST /runs/{id}/cancel。提交返回 202 和 Run ID；消息接口查询视图，同时提供来源事件标识。投影不以页面消息视图替代原始日志。

## T05 M05 单进程调度、认领与停止

依赖：MariaDB/InnoDB、database/sql、context、sync、time。调度器与 API、前端资源服务在同一个 Go 进程中。runs 表是任务来源，内存通知用于唤醒，轮询补漏；平台与空间并发由进程内准入器限制。

MVP 由一个 dispatcher 串行认领，再交给受限 goroutine 执行。不要求队列 SKIP LOCKED 能力。先在事务外读取候选 Run/Session ID，再按“Session → Run → 其他视图/引用”的固定锁顺序，在短事务中复核并认领；没有候选或复核失败则释放并发名额。事件追加、提交和取消也遵循相同锁顺序。

```sql
START TRANSACTION;
SELECT last_sequence FROM sessions
WHERE space_id = ? AND id = ? FOR UPDATE;
SELECT status, cancel_requested_at FROM runs
WHERE space_id = ? AND id = ? AND session_id = ? FOR UPDATE;
-- 复核仍为 queued 且未取消，否则回滚。
UPDATE runs
SET status = 'running', owner_id = ?, started_at = UTC_TIMESTAMP(6),
    heartbeat_at = UTC_TIMESTAMP(6)
WHERE space_id = ? AND id = ? AND status = 'queued';
-- 检查 RowsAffected == 1，通过 Journal 在同事务追加 run.started。
COMMIT;
```

增加 `(space_id, status, created_at, id)` 索引，先获得并发名额再认领。事务不跨模型或 TOS 请求持有行锁。`FOR UPDATE` 必须运行在有效事务内。[MariaDB 行锁文档](https://mariadb.com/docs/server/reference/sql-statements/data-manipulation/selecting-data/for-update)

服务启动用独立 `*sql.Conn` 持有 `GET_LOCK('mass:<database>:runtime', 0)`，返回 1 才成为执行实例，退出在同连接 RELEASE_LOCK 并关闭。该连接不能借出到连接池；锁随连接而非事务生命周期存在。持锁连接异常时停止接单和后续动作，不能宣称它提供自动故障切换或副作用恰好一次。[MariaDB GET_LOCK](https://mariadb.com/docs/server/reference/sql-functions/secondary-functions/miscellaneous-functions/get_lock)

Run context 派生自服务执行生命周期，不使用提交请求的 context。取消先写 MariaDB，再通知执行器；queued 取消与占用释放同事务，running 直到实际停止才释放占用。断开浏览器不会自动取消。

确认旧进程退出后，重启将遗留 running 标记为 interrupted 失败，保存未完成调用并释放占用；queued 继续调度。资源从 MariaDB/TOS 恢复，不读取宿主工作目录，也不自动重做未知副作用。

停机先停止接单和认领，有界等待或取消已有执行，刷写 Trace；无法停止的工具由进程退出终止，重启时标记中断，而不是提前报告成功取消。

## T06 M06 执行循环

依赖：ContextPlanner/ContextProjector、基于 Eino 的 ModelGateway、ToolDispatcher、SessionJournal、RunStore。执行器负责 MASS 的版本、预算与生命周期；模型调用统一走 Eino 组件，不直接实现供应商 SDK。MVP 保留这一个显式执行循环，不再套一层 Eino Agent 的完整循环，避免重复调度工具和预算。后续如切换 Eino ADK，需要迁移相同的权限、上下文与 Trace 约束。

核心循环骨架如下；Gateway、Tools 和终结器负责各自动作前后的持久化，所有错误必须传播。ContextPlanner.Prepare 负责必要的摘要生成、日志追加和投影准备，摘要也走同一 Gateway 并计入预算；ContextProjector.Project 本身不调用 LLM：

```go
func (e *Executor) Execute(ctx context.Context, run RunSnapshot) error {
    for step := 0; step < run.MaxSteps; step++ {
        if err := e.Control.Check(ctx, run); err != nil { return err }
        input, err := e.Context.Prepare(ctx, run)
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

## T07 M07 确定性上下文投影、预算与缓存

### T07.1 唯一来源与职责

输入来源为 SessionJournal 截至确定序号的已提交事件及其不可变 content_objects。ContextProjector 不再分别向草稿、消息视图、运行内存和工具模块索要当前内容。当前用户输入也必须先成为日志事件。模型对应计数器和已固定的策略/配置属于显式输入；实时授权在调用前单独检查，不能被历史日志替代。

ContextPlanner 负责预算规划、触发摘要和选择 context_epoch；ContextProjector 负责从固定事实确定性地产生模型消息、工具定义与 ContextManifest。投影是只读转换，不能隐式调用模型、读取当前时间、执行工具或产生新摘要。日志索引和有界缓存可加速读取，但都需要匹配来源边界和版本。

### T07.2 输入契约与投影规则

```text
ProjectContext(
  space_id, session_id, run_id, through_sequence,
  purpose, context_epoch, base_projection_ref,
  projection_policy_version, renderer_version, parser_version,
  effective_config_ref, summary_refs
) -> messages_ref, tools_ref, manifest_ref, content_digest
```

所有引用必须属于已提交的同空间记录；summary_refs 的创建事件不晚于 through_sequence。模型能力、工具集合、计数器版本及输出预留来自 effective_config_ref。相同输入、相同对象字节和相同版本应得到相同的模型可见消息/工具内容；投影 ID、生成时间等审计字段不参与该等价关系。

固定顺序为平台指令、Agent 指令、按定义顺序排列的 Skill 指令、本阶段固定摘要及保留历史、按时间追加的对话。供应商协议需要的工具字段、角色和内容块顺序由版本化适配器处理，不强行统一成一段文本。MVP 加载所有绑定 Skill 的 SKILL.md，资源通过工具读取后作为结果事件进入日志。

| 来源事件 | 投影规则 |
| --- | --- |
| session.created / run.created | 读取固定定义及有效配置，禁止重新解析 latest 或当前草稿 |
| user.message.accepted | 按序进入对话，文本/附件均引用已保存内容 |
| model.response.completed，purpose=task | 采用完整结构化响应及必要供应商协议字段，工具调用按原顺序保留 |
| tool.execution.completed / failed | 依据 tool_call_id 配对；可恢复业务错误形成工具结果 |
| context.summary.created | 仅在被本次投影选中时替代指定完整历史单元，原文不删除 |
| model.response.chunk.recorded / attempt.failed | 原文用于审计；流式片段和失败尝试不拼成成功助手消息 |
| purpose=summary 的模型响应 | 仅通过摘要记录使用，不作为用户任务回复 |
| context.projected / 请求原文 / run.finished | 用于追溯、状态和最终回复选择，不重复加入已有内容 |

同一用户消息、助手响应和工具结果各出现一次。构建完整对话单元再预算：一个助手响应中的所有工具调用及对应结果作为一组，不拆对、不重排 call_id。助手最终回复视图不再作为另一条消息加载。

失败 Run 中已完整完成的交互单元可保留；未闭合工具组不能原样发送给要求配对的模型。投影按版本化规则生成带来源事件引用的“中断/副作用已确认或未知”说明，不伪造工具成功。非法参数、未知工具、取消导致未执行的调用必须有明确拒绝/未执行结果或被完整转为中断说明；缺失结果不能默认为无副作用。该转换记录在 Manifest 中。

### T07.3 投影记录与可重建性

context_projections 的核心字段如下；大清单/消息体进入 TOS，记录由 context.projected 事件引用：

| 字段 | 用途 |
| --- | --- |
| projection_id、space_id、session_id、run_id、purpose | 身份、隔离与任务/摘要用途 |
| through_sequence、parent_projection_id | 固定原始日志边界及上一投影 |
| projection_policy_version、renderer_version、parser_version | 历史选择、协议转换和解析规则版本 |
| context_epoch、base_projection_ref、summary_refs | 本阶段固定基础与明确使用的摘要 |
| effective_config_ref、model_config_fingerprint、tools_fingerprint | 模型/端点/能力/工具及影响缓存兼容性的配置 |
| ordered_block_refs、selected_event_ids | 模型可见块顺序、对象摘要和原始事件来源 |
| manifest_ref | 来源范围、采用/省略/摘要/节选原因、失败历史转换 |
| projected_messages_ref、tools_schema_ref、content_digest | 已保存的逻辑输入和确定性比较依据 |
| token_estimate、counter_version、prefix_change_reason | 容量估计、计数方法和前缀变化解释 |

context.projected 自身序号晚于 through_sequence；实际 model.request.prepared 引用 projection_id 和确切请求体。为了审计，保留最终逻辑输入与实际请求，不能只依赖未来重新执行旧版本代码。内容删除、过期或缺失时明确标记不能完整重建。

来源事件序号和 Run ID 存在 Manifest 中，默认不写进模型可见内容。摘要引用、模型配置、内容摘要等字段用于平台校验，不等于供应商缓存键，也不证明缓存命中。context_projections 是已执行投影的历史记录；页面查询索引可重建，但历史摘要和实际请求原文必须保留。

### T07.4 容量与摘要阶段

```text
有效输入预算 = min(Agent 输入上限, 平台输入上限,
                  模型上下文窗口 - 输出预留 - 安全余量)
```

关键指令、工具定义和当前请求先计入预算，必要输入已超限则拒绝。大工具结果先保存完整原文，确定节选/摘要对象后固定引用；不在每次投影中重新生成不同节选。未支持的附件明确拒绝。计数无法精确时标为 estimated，供应商 usage 用于校准，不捏造精确计数。

context_epoch 表示一段基础内容保持稳定的历史压缩阶段。正常多轮只追加新的完整交互；容量临近阈值时，Planner 对足够多的旧完整单元生成摘要，保留近期完整单元，并为后续多轮预留空间，避免逐轮滑动删除和重复摘要。阈值、目标余量及选取规则属于版本化策略，不承诺固定命中率。

```text
epoch 1：固定指令/工具 + 原始历史持续追加
epoch 2：固定指令/工具 + 固定摘要 S1 + 保留历史及后续追加
epoch 3：固定指令/工具 + 固定摘要 S2 + 保留历史及后续追加
```

摘要调用 purpose=summary，禁用工具、禁止递归压缩，并计入当前 Run 用量和预算。流程为：选定来源事件 → 保存摘要请求投影 → 统一网关调用 → 追加完整响应和 context.summary.created → 固定新的日志边界 → 生成任务投影。摘要新增 source_event_ids、有序源内容摘要、coverage 范围、生成调用及策略版本；范围内审计事件不算摘要内容，不能只凭起止序号判断覆盖。若使用上一摘要，保留递归来源引用。

摘要在生成时具有不确定性，但后续投影复用已保存摘要，不重新生成。摘要失败且原上下文无法容纳时终止 Run，旧 epoch 保持有效。只有摘要保存并且新投影验证通过才切换阶段。任务摘要与任务对话使用不同投影父链，避免相互污染缓存比较。

### T07.5 前缀稳定性与缓存边界

平台保证确定性内容、可解释的变化和同阶段尽量只追加；不保证每次供应商 Prompt/KV 缓存命中。本服务的投影结果缓存仅减少读取/组装成本，不代表模型缓存。投影缓存键至少包含空间、会话、日志边界、策略/渲染/解析版本、配置和摘要引用；命中也必须重新验证当前授权。

固定指令、Skill/工具顺序、Schema、空白格式、历史协议字段和已有内容块必须稳定；不要使用无序 map 迭代产生工具顺序。当前时间、Run ID、剩余预算等默认留在审计数据中；必要动态信息按语义放在后部，不能降低指令优先级。后端适配器最终请求也要检查，不能只比较投影前的内部 JSON。

语义布局为“固定指令/工具 → epoch 固定基础 → 追加交互”，具体线序由供应商决定。模型生成的回答能否作为下一次可缓存前缀由其协议决定；保持内容稳定不代表整个旧 HTTP 请求体都是新请求的字节前缀。

压缩、必要配置变化、模型切换或协议版本变化会开启新 epoch，并记录 prefix_change_reason；Agent/Skill 版本升级仍须新建会话。权限或凭证撤销优先于缓存，首版直接停止受影响调用，不为了复用继续使用已撤销能力。

隐式缓存受模型门槛、有效期、淘汰和路由影响；方舟明确不保证命中概率及最长前缀命中。显式缓存具有模型/API 和配置限制，必须针对最终 Eino 组件验证后才能启用。[方舟上下文缓存](https://docs.volcengine.com/docs/ark/context-cache?lang=zh)

MVP 先实现固定前缀、阶段压缩与真实命中观测；不把显式缓存支持作为所有模型的默认能力。启用时由 M08 管理 model_cache_bindings，记录空间、供应商账号作用域、端点/模型、协议/配置/前缀指纹、缓存 ID 和有效期。缓存不存在或失效时根据原始日志和固定投影重建请求，记录新的调用尝试，不重复用户输入或工具副作用。

供应商托管历史或 previous_response_id 必须能在本地日志中解析为完整逻辑输入；同时保存实际发送的增量请求及远端引用，不能只保存缓存 ID。敏感缓存引用按凭证类似方式受控，不跨空间复用私有历史。

### T07.6 观测与检查

每次模型 attempt 保存 input_tokens、cache_read_tokens、可用时的 cache_write_tokens、首 token 延迟、模型/组件版本、epoch、投影指纹及前缀变化原因。供应商字段由适配器归一化并保留原始 usage；缺失值为 unknown，不当作零。摘要调用单独统计并计入总成本。

```text
请求命中率 = cache_read_tokens > 0 的请求数 / 有有效缓存统计的请求数
缓存输入占比 = Σcache_read_tokens / Σinput_tokens
```

第二项仅对两项统计齐全且口径一致的请求求和；额外报告统计覆盖率，按供应商/模型/用途分组，不混合不同字段口径。冷启动、正常追加、压缩切换和过期分组对比，零分母显示无数据。平台侧公共前缀长度只作为诊断估计，不能替代供应商 usage。

检查界面提供原始事件 → 投影 Manifest → 实际请求的导航；展示选取/省略原因、epoch、前缀变化以及真实缓存数据。稳定性测试可以有确定通过标准，真实云缓存收益只能基于测量报告，不能伪造“每次必命中”的验收要求。

## T08 M08 基于 Eino 的 LLM 接入与完整 Trace

### T08.1 框架与模块边界

统一采用 `github.com/cloudwego/eino` 的 model/schema 接口及 `eino-ext` 模型组件。MASS 的 M08 包装 Eino，M06 负责执行与工具循环，M07 管理上下文，M09 保留工具权限和 TOS 工作区访问边界。Eino 是进程内框架，不额外启动 LLM 服务，也不代替实际模型 endpoint 与凭证。

首版使用 `model.ToolCallingChatModel`、`schema.Message`、`schema.ToolInfo`，通过 `Generate`/`Stream` 接入实际模型。工具用 `WithTools` 绑定到当前调用实例，避免并发共享模型上原地修改工具集合。模型能力和协议信息通过组件适配，不丢弃继续对话所需字段。[Eino 模型接口](https://github.com/cloudwego/eino/blob/main/components/model/interface.go)

Eino ADK/Graph 可以后续承接更复杂编排，首版不同时启用两个完整 Agent 循环。所有自有组件仍采用 Eino 的模型接口进行真调用和 FakeChatModel 测试，不能以另写一套 HTTP 模型服务替代 Eino。

### T08.2 核心调用代码

以下片段演示 Eino 流的单一消费路径。业务 DTO 转换、授权、预算、完整性判定及 SQL/TOS 事务在网关外层。`record` 确认对应原文区块已持久化，并维护可重建的语义增量视图，保存失败必须返回错误；它不把每个增量另写成一条完整助手消息。聚合与校验完成后由网关统一追加 model.response.completed。引用的框架 API 以初始化时锁定版本为准：

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

Eino callbacks 用于模型输入输出、错误、耗时等语义层记录，但不能当作供应商实际 HTTP 原文。实际 input/output 捕获通过注入组件 HTTPClient 的自定义 `http.RoundTripper` 完成，两者通过 Run/span/attempt 关联，写入同一 SessionJournal；回调不是第二套权威历史。[Eino callbacks 概述](https://www.cloudwego.io/docs/eino/overview/)

请求在 Eino/供应商 SDK 完成序列化后由 Transport 捕获：限制容量、保存最终请求体至 TOS、提交引用及 model.request.prepared，之后才发送相同字节；认证头和签名查询参数不记录。拒绝不受支持的请求流，不默默跳过捕获。

响应以包装的 Body 接收并按有界块写入 TOS，再把已保存的字节交给 Eino 解码；保留原始顺序、错误状态码和解码失败的原文。慢存储可产生有界背压，持久化失败立即取消模型请求。原文区块形成 model.response.chunk.recorded；解析后的语义增量可作为同一记录的派生展示，完整结构化响应在 model.response.completed 关联相应区块清单和 parser_version；流被提前 Close 或中断时标记 incomplete。

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

`write_file` 先把新内容写入唯一且不可变的 TOS 对象，再用 revision 条件事务更新 workspace_files 指针并写 tool.execution.completed；原内容不会被原地覆盖。返回成功前对象内容和元数据均已确认。`read_file` 按该版本读取，`list_files` 查询逻辑目录索引，`read_skill_resource` 只能读取快照引用的资源。

超时后对象可能已上传但数据库未引用，应记为待核实或孤儿并延迟回收，不盲目重做工具。一次工具调用的映射更新与 call_id/幂等记录关联，重试不能重复产生新逻辑效果。

Dispatcher 执行前校验登录主体的空间归属、Agent 工具列表、预算和取消状态，持久化 tool.execution.started；执行后保存完整接收结果和结构化错误。内置工具不开放 Shell、用户脚本、自定义 HTTP 或 MCP，执行仍发生在当前 Go 进程。

## T10 M10 统一会话日志、Trace、用量与审计

依赖：database/sql、MariaDB JSON、TOS ContentStore、net/http。SessionJournal 是会话和执行链路的唯一事实入口，Trace 是它的查询视图，不再另写一套 trace_events。Agent/Skill 草稿、身份及空间管理仍使用普通业务表与 audit_logs，MVP 不把整个系统改成事件溯源架构。

### T10.1 统一存储与事件契约

| 结构 | 职责与约束 |
| --- | --- |
| sessions | 同空间会话标识、last_sequence 和协调状态；序号贯穿全部 Run |
| runtime_events | event_id、space/session/run、sequence、event_type、schema_version、operation_id、causation_event_id、correlation_id、actor、occurred_at、recorded_at、data、event_digest；追加后不改写 |
| content_objects | 不可变原文/结构化内容；object_id、空间、media_type、size、SHA-256、TOS key/version_id、存储状态 |
| event_objects | event_id、object_id、role、ordinal；区分指令、请求体、响应块、工具结果、附件等用途及顺序 |
| context_projections / context_summaries | 由对应事件提交的不可变派生产物及来源清单；字段见 T07 |
| runs / messages / run_snapshots | 事务内同步维护的状态和展示视图，保留源事件与 applied_sequence；不能成为另一套独立历史 |

事件 data 只存结构化语义与非秘密索引；用户正文、工具结果、实际模型载荷统一通过事件对象引用访问。TOS 原始字节与解析后结构化对象分开保存并关联，不能用重序列化结果冒充原文。事件格式按 event_type + schema_version 演进，未知版本拒绝投影并明确诊断，不静默跳过。

事件示例（HTTP 中 sequence 为十进制字符串）：

```json
{
  "event_id": "evt_tool_done",
  "schema_version": 1,
  "space_id": "space_1",
  "session_id": "session_1",
  "sequence": "42",
  "run_id": "run_1",
  "event_type": "tool.execution.completed",
  "operation_id": "execution_1",
  "causation_event_id": "evt_tool_started",
  "correlation_id": "model_call_1",
  "actor": {"kind": "tool", "id": "read_file"},
  "occurred_at": "2026-10-17T08:00:00Z",
  "recorded_at": "2026-10-17T08:00:00Z",
  "data": {"tool_call_id": "call_1", "side_effect": "none"},
  "content_refs": [
    {"role": "tool_result", "ordinal": 0, "object_id": "obj_1", "sha256": "<digest>"}
  ]
}
```

数据库 event_objects 是 content_refs 的关系存储，API 组装时返回数组。所有引用使用同空间联合外键；causation 指向本会话更早的事件，correlation 仅负责关联，不替代顺序或权限校验。模型原生 tool_call_id 只在相应模型调用范围内唯一，平台 operation_id 单独生成。

| 事件类型 | 确认的事实及投影语义 |
| --- | --- |
| session.created | 定义及不可变内容清单固定，提供基础指令 |
| user.message.accepted | 用户内容已接受并持久化，提供对话输入 |
| run.created / run.started | 运行配置固定并排队 / 已被认领；不重复用户消息 |
| context.projected | 固定边界的逻辑输入和 Manifest 已保存，提供审计关联 |
| model.request.prepared | 最终请求原文已保存、准备发送；不能证明到达供应商 |
| model.response.chunk.recorded | 已保存原始响应字节区块，带 attempt 和块序号；不当作完整消息 |
| model.response.completed | 协议完整、可解释的响应及结构化结果已保存；task 用于上下文，summary 用于摘要 |
| model.attempt.failed | 非成功响应、截断、流/解析/保存失败等；保留已存区块和完整性状态 |
| model.usage.reported | 供应商后续补充的用量及缓存统计，关联原 attempt 和统计版本；汇总按最新已确认统计替换，不能重复累加 |
| tool.execution.started | 调用意图和参数已保存，即将执行；不能证明已产生副作用 |
| tool.execution.completed | 工具结果已确认保存，提供配对结果 |
| tool.execution.failed | 错误、拒绝、取消或未执行等已确认，保留 side_effect 和错误结果 |
| context.summary.created | 摘要、准确来源事件及生成调用已保存，供明确选择后投影 |
| run.cancel.requested / run.finished | 请求取消 / 实际终结；最终回复引用已有响应 |
| operation.reconciled | 中断恢复时对遗留调用追加核实结论；未知保持 unknown，不追写假成功 |
| content.unavailable | 内容按保留策略被删除、过期或发现缺失，记录受影响引用；不修改历史事件 |

事件类型、actor 和作用域由服务端可信执行路径赋值，外部文本不能直接成为可信事件。事件是否参与上下文由版本化投影器决定，不接受客户端提供 include_in_prompt 开关。purpose=task/summary、attempt_id、projection_id 等由模型事件 data 携带；Run/span 父子关系可用于 Trace 展示。

每个模型 attempt 只允许一个终结事实 completed 或 failed；同一 operation 的重复完成按幂等规则处理，不能选择两份响应同时进入上下文。协议完整性同时检查结束原因、工具参数、配对所需标识及必要状态。终结后追加的核实或用量补充是独立事实，不原地改写原事件。

工具 side_effect 明确为 none、committed 或 unknown；failed 不等于没有副作用。写文件的对象映射更新与 tool.execution.completed 在同事务提交。执行完成但记录前崩溃时，恢复追加 operation.reconciled；缺证据保持 unknown，不能依据只有 started 推断未执行，也不自动重放。

原始 HTTP 字节区块可以跨协议帧，解析器负责帧组装。大内容按有界区块写 TOS，成功后提交事件引用；尚未完成的 multipart 不能作为已可读区块。完成标识需全部已收到内容保存且协议完整；崩溃前未持久化尾部明确 incomplete。认证秘密不保存，必要脱敏记录规则和位置，不能声称脱敏后的内容为逐字原文。

### T10.2 追加、事务与恢复

唯一写入口为 Journal.Append，调用者提供同一逻辑写入重试时稳定的 event_id 或 append_key；服务端校验事件 schema、权限、操作状态、同空间引用和 ready 对象。使用 event_digest 比较类型、因果字段、data 和有序内容摘要，排除提交序号/记录时间；同 ID 同内容返回已提交结果，不同内容返回冲突。

```text
Append(session_id, expected_sequence?, append_key, proposed_events)
  -> assigned_event_ids_and_sequences
```

1. 授权并保存内容至 TOS，事务外完成上传；记录上传意图和 ready 状态。
2. 事务内锁定 Session，先核实幂等结果，再按需校验 expected_sequence。
3. 由服务端 reducer 校验状态转换，为批量事件分配连续序号；业务视图不能由外部任意指定更新值。
4. 插入 runtime_events/event_objects，同事务更新必要视图、context_projections/summary 索引、对象引用和幂等结果。
5. 提交后才能发布通知、确认 SSE 可读或开始后续动作。通知丢失由数据库轮询补齐。

```sql
START TRANSACTION;
SELECT last_sequence FROM sessions
WHERE space_id = ? AND id = ? FOR UPDATE;
-- 先核实 append_key/event_id；必要时检查 expected_sequence。
-- 服务端 reducer 为本次 N 条事件分配 last_sequence+1 ... +N。
-- INSERT runtime_events / event_objects，并更新业务视图及引用。
UPDATE sessions SET last_sequence = ? WHERE space_id = ? AND id = ?;
COMMIT;
```

同一事务失败整体回滚，不使用无锁 max(sequence)+1。所有业务更新遵循 Session → Run → 其他视图/引用的锁顺序。期望序号用于依赖特定状态的命令；外部动作返回的结果必须按稳定 operation/event ID 追加，不能因期间插入取消事件就丢弃结果或重做动作。冲突仅重试无外部副作用的追加事务。

投影读取 through_sequence 后追加自己的 context.projected；其间出现的审计事件不改变该历史输入，当前权限/取消仍在真正调用前重新检查。已启动操作的结果须保存，终态等待它们收尾；进程异常后的核实采用明确的新事件。

关键事件或引用保存失败时停止后续动作；数据库不可用时不能保证失败事件立即落库，重启后依据遗留操作追加中断核实。事件只追加与内容摘要校验不等于防管理员篡改，也不保证模型/工具结果恰好一次。

视图重建固定 reducer_version，读取已有事实，不触发模型/工具。重建结果与线上视图对比，覆盖失败、取消、重复追加和未知副作用；完整重建依赖未过期的内容对象。历史原文按保留规则删除后追加 content.unavailable，保留审计元数据并明确不可重建范围。

usage 按 attempt 记录并汇总，摘要和失败尝试均计入；缺失用量为 unknown，价格估算保存价格版本。缓存统计口径见 T07.6。运维日志不复制原始用户内容，空间授权同样用于日志和载荷读取。

### T10.3 AG-UI 协议边界与依赖

确定使用 AG-UI 作为运行交互事件协议，SSE 作为 HTTP 传输方式。MASS 内部 runtime_events 保持 T10.1 的事实语义，由 AguiProjector 投影成标准 AG-UI 事件；ContextProjector 仍读取原始日志，不读取前端事件。

Go 使用协议官方仓库中的 community Go SDK 的 core/events 类型及 JSON 编码能力，前端使用 @ag-ui/core 类型与事件 Schema。D00 锁定兼容的 SDK/Schema 版本，校验本方案使用的事件子集；SSE framing 由 MASS 包装以支持持久化游标。[AG-UI Go SDK](https://docs.ag-ui.com/sdk/go/overview)、[TypeScript 事件契约](https://docs.ag-ui.com/sdk/js/core/events)。

继续使用 MASS 的 POST /sessions/{id}/runs 接受请求并返回 Run ID，再通过 GET /runs/{id}/events 订阅同一 Run。前端提供这一提交/订阅方式的适配器。官方 HttpAgent 默认使用 POST RunAgentInput 并直接读取事件流，不能直接把它指向这里的 GET 地址；本版本提供 AG-UI 事件兼容性，MASS 的提交、鉴权及续传方式是应用约定。[AG-UI 传输与客户端架构](https://docs.ag-ui.com/concepts/architecture)

不接受前端提供的历史、tools 或 state 覆盖后端原始日志与授权配置；工具仍全部由后端执行。断线重订阅不会创建新 Run；取消使用原有明确的取消接口。

### T10.4 内部事实到 AG-UI 的映射

threadId 映射 Session ID，runId 映射 Run ID。messageId 由 Run/attempt/响应消息位置确定；toolCallId 由平台模型调用标识和供应商调用 ID 确定，防止不同尝试复用供应商 ID 造成冲突。run.created 保存 protocol_profile、mapper/parser_version，同事务初始化 agui_streams；这些是 UI 映射配置，不进入 LLM 指令。重连不能重新生成标识或改变映射版本。

| 原始事实或条件 | AG-UI 输出 | MASS 语义 |
| --- | --- | --- |
| run.created | RUN_STARTED，然后 CUSTOM mass.run.status.v1 | 接受请求后开始交互生命周期，status=queued；不宣称执行器已开始 |
| run.started | CUSTOM mass.run.status.v1 | status=running，不重复 RUN_STARTED |
| task 模型回复的可展示文本增量 | TEXT_MESSAGE_START / CONTENT / END | 同一 messageId，非空 delta；按协议边界闭合 |
| task 模型的完整工具调用描述 | TOOL_CALL_START / ARGS / END | 同一 toolCallId，参数是 JSON 字符串；MVP 在响应完成校验后输出完整参数，后续可流式优化 |
| tool.execution.started | CUSTOM mass.tool.status.v1 | 工具执行中；与参数结束分开 |
| tool.execution.completed | TOOL_CALL_RESULT | messageId、toolCallId、role=tool、content 为字符串 |
| 可恢复工具业务错误 | TOOL_CALL_RESULT + CUSTOM mass.tool.status.v1 | content 中明确错误结构，不伪装成功；最终是否终止由 Run 状态决定 |
| 未知副作用、未执行或协议不完整 | CUSTOM mass.tool.status.v1 / mass.message.status.v1 | 明确 unknown/incomplete/not_executed，不能补造成功结果 |
| 投影、摘要、模型 attempt 状态 | CUSTOM mass.context.v1 / mass.trace.v1 | 提供来源 ID、状态及载荷引用，不把摘要任务输出当作用户回复 |
| 用量与缓存统计 | CUSTOM mass.usage.v1 | 保存未知标识和统计口径；锁定版本支持时可同时填终态标准 usage |
| run.cancel.requested | CUSTOM mass.run.status.v1 | cancelRequested=true；继续等待真正终态 |
| run.finished succeeded | RUN_FINISHED | 唯一成功终态，携带 threadId/runId |
| run.finished failed | RUN_ERROR | message 和业务 code；可恢复模型尝试失败不能提前发 Run 终态 |
| run.finished cancelled | CUSTOM mass.run.status.v1，然后 RUN_ERROR | MASS 约定 status=cancelled、code=RUN_CANCELLED，不伪装成功 |

取消通过错误终态表达是 MASS 对当前选定事件子集的映射约定，客户端按 RUN_CANCELLED 展示“已取消”。每个完整 Run 流以一个 RUN_STARTED 开始，以 RUN_FINISHED 或 RUN_ERROR 之一结束；排队即取消也满足这一边界。后续版本若引入其他终态表达，必须显式升级协议配置，不能混用。

TOOL_CALL_END 仅结束工具调用参数，不表示工具执行成功；结果由 TOOL_CALL_RESULT 或自定义状态给出。[AG-UI 工具事件语义](https://docs.ag-ui.com/concepts/events)

标准事件字段采用 AG-UI camelCase 和 type 判别字段；仅凭改名将原始 event_type/payload 包进 data 不算完成协议接入。自定义信息统一采用 CUSTOM 的 name/value，命名为 mass.<topic>.v1，value 包含 schemaVersion、threadId、runId、sourceEventIds 及经授权的业务摘要。原始模型请求、敏感协议块和大结果通过受控 payload API 按需读取，不直接塞入 rawEvent。

文本增量只能来自已持久化原始响应的解码结果。投影器按固定 parser_version 维护跨区块解码状态，不能把网络区块当作文本 delta；完成响应不再重复发送已发文本。非流式回复可产生一次完整文本 CONTENT。消息 END 只关闭展示边界，流中断时先标记 incomplete，再闭合已打开的文本消息；重试使用新 attempt/messageId，不拼接成同一回答。

TOOL_CALL_RESULT 的大内容只传明确标注的节选及受控引用，并由 CUSTOM 标记 truncated/payloadRef；完整结果仍在原始日志。工具参数超过 UI 传输上限时只发送 CUSTOM 说明及载荷引用，不伪造 ARGS。没有发出标准 TOOL_CALL_START 的工具，也不单独发无对应关系的 TOOL_CALL_RESULT。

MVP 使用显式 START/CONTENT/END 事件；不使用自动展开的 CHUNK 简写、STATE_DELTA、前端工具执行或中断恢复能力。状态补充通过 CUSTOM，完整调试内容通过查询 API 获取，后续扩展必须补契约测试。

### T10.5 AG-UI 传输视图与断线续传

一条 runtime_event 可以生成零个、一个或多个 AG-UI 事件，因此 SSE id 使用独立 ui_sequence，不能直接使用 Session sequence。原始日志用于审计，agui_events 是可重建的传输视图：

| 结构 | 必要字段与约束 |
| --- | --- |
| agui_streams | space_id、session_id、run_id、protocol_profile、mapper/parser_version、source_sequence、投影器状态、last_ui_sequence、终态标识 |
| agui_events | space_id、run_id、ui_sequence、source_event_id、source_ordinal、event_json 或不可变对象引用 |
| 唯一约束 | (space_id, run_id, ui_sequence)；(space_id, run_id, source_event_id, source_ordinal) |

投影器在同一 Go 进程运行，读取已提交原始日志及对象；解析可在事务外准备，再锁定 agui_streams、复核检查点，在同一事务追加该批输出并推进检查点/解析状态。投影器不能反向改变原始日志或执行工具；崩溃重试不会重复分配输出。输出事件先落库，再发送 SSE，不能针对不同连接重新切分文本。受限大小的投影状态保存到数据库或不可变内容对象，不能只放进程内存。

protocol_profile、分块与标识规则在 Run 创建时锁定；同一流后续重建必须保留相同游标和输出，不原地换映射器。原始 source_sequence 允许因其他 Run/会话事件跳跃；本 Run 的 ui_sequence 连续递增，分页与 SSE 使用同一顺序。

GET /spaces/{space_id}/runs/{run_id}/events 使用 Cookie 空间鉴权，返回 text/event-stream；Last-Event-ID 或 after 是该 Run 的 ui_sequence，两者同时出现且不同返回错误。按已提交 ui_sequence > cursor 升序补齐，不以连接打开时间作为起点。首连从 0 读取完整生命周期；续传是旧流的后缀，不能再添加一次 RUN_STARTED。

游标、已应用界面状态和协议版本必须配套。页面刷新时若只有游标而没有对应状态，应从 0 回放；大历史可调用 GET /runs/{id}/ui-snapshot 获取同一水位的 reducerState、cursor、protocolProfile，再从该 cursor 续传。快照需要包含尚未结束的消息/工具状态。SSE 历史过期返回 cursor_expired；仅当存在覆盖该位置的完整快照时允许快照恢复，否则明确无法完整回看。不得跳过缺失数据并返回成功。

断线、代理超时、鉴权失败和 UI 投影服务错误是传输问题，不能凭空追加 RUN_ERROR 将仍在运行的任务改成失败；客户端查询 Run 状态并按已应用游标重连。慢客户端有界超时断开，不阻塞执行器；投影延迟显示为同步中。终态提交并投影后，发送终态事件关闭流，客户端停止订阅；已应用终态后无需再连接。终态后的用量补充或核实仍可追加原始日志并通过查询展示，但不能继续向已终结 AG-UI 流追加事件或改写旧帧。定期复核成员与登录状态。

### T10.6 SSE 帧与编码示例

每个 data 直接包含一个 AG-UI JSON 对象；SSE id 是 MASS 的续传扩展。不再使用 event: run_event；默认 message 事件可供 EventSource.onmessage 消费。心跳使用 SSE 注释，不分配业务游标。

```text
id: 1
data: {"type":"RUN_STARTED","threadId":"session_1","runId":"run_1"}

id: 2
data: {"type":"CUSTOM","name":"mass.run.status.v1","value":{"schemaVersion":1,"threadId":"session_1","runId":"run_1","status":"queued","sourceEventIds":["evt_run_created"]}}

```

下面是独立可编译的传输核心；body 来自已按锁定 AG-UI Schema 验证并持久化的输出，不能直接接受任意用户 JSON。Compact 保证 data 为单行，协议类型/必填字段校验在上游完成：

```go
package stream

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "strconv"
    "time"
)

func WriteAGUIEvent(w http.ResponseWriter, cursor string, body json.RawMessage) error {
    n, err := strconv.ParseUint(cursor, 10, 64)
    if err != nil || n == 0 || strconv.FormatUint(n, 10) != cursor {
        return fmt.Errorf("invalid AG-UI cursor")
    }
    var data bytes.Buffer
    if err := json.Compact(&data, body); err != nil { return err }
    rc := http.NewResponseController(w)
    if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
        return err
    }
    if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", cursor, data.Bytes()); err != nil {
        return err
    }
    return rc.Flush()
}
```

调用方在写入前完成鉴权，设置 Content-Type: text/event-stream、Cache-Control: no-cache，并配置代理不缓冲。AG-UI 事件 JSON、SSE id 和 HTTP 状态分别承担协议内容、续传与连接错误职责。[AG-UI SSE 编码示例](https://github.com/ag-ui-protocol/ag-ui/blob/main/docs/sdk/python/encoder/overview.mdx)

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
| 运行、会话、runtime_events、event_objects、空间和权限 | MariaDB；不依赖本地文件 |
| 用户原文、摘要、投影消息/Manifest、结构化模型响应 | TOS 不可变对象 + 事件/投影引用 |

所有事件及其投影引用都参与对象保留判断；同一不可变对象可被多个事件引用，避免重复复制定义或结果。普通上传使用流/有界缓冲直接发送 TOS，大文件使用受限并发 multipart；不使用 SDK 的本地文件上传/下载帮助方法。配置和编译产物属于部署输入，可以存在本地，但不能存用户运行内容。

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

运行结束时将应保留的逻辑文件关联为 Artifact；清理工作区只移除不再需要的逻辑引用，不删除仍被版本、日志、投影或产物引用的对象。TOS 生命周期策略必须与应用引用策略协调，不能按前缀盲删活跃内容。

TOS 不可用或对象无法确认保存时，拒绝新的相关写操作，停止后续 Agent 动作；不得回退到本地持久化。可以保留有限内存中的未完成块，但重启后不声称其已保存。

恢复需要 MariaDB 备份、仍保留的 TOS 对象及解密材料；数据库备份不包含对象内容。恢复验收校验对象清单/摘要和缺失状态。删除 MASS 主机运行目录后，登录状态、已发布资源和历史产物仍应能从数据库与 TOS 读取；中断执行本身按 T05 失败处理，不自动续跑。

## T12 M12 前端管理与调试

依赖：React、TypeScript、Vite、React Router、TanStack Query、Monaco Editor、react-markdown、openapi-typescript、@ag-ui/core。选用库的用途见官方说明：[Vite](https://vite.dev/guide/)、[React Router](https://reactrouter.com/start/declarative/installation)、[TanStack Query](https://tanstack.com/query/latest/docs/framework/react/overview)、[Monaco](https://github.com/microsoft/monaco-editor)。

### T12.1 页面与数据流

前端源码只放在 `frontend/src/`，按 features/agents、skills、sessions、debug、runs、settings 组织；package.json、lockfile、Vite 配置和前端测试均位于 `frontend/`。公共组件为定义编辑器、版本差异、依赖选择、Trace 时间线、原始载荷查看、上下文检查和产物列表。

查询键统一包含 space_id，详情继续包含对象及版本 ID；变更后只失效相关查询。切换空间关闭旧 SSE、撤销旧请求、清理私有缓存和未保存表单。草稿保存携带 revision，409 时提示差异，不自动覆盖。

Monaco 延迟加载；react-markdown 禁用任意原始 HTML，不加载允许执行用户代码的插件，链接与资源 URL 使用允许列表策略。[react-markdown 官方说明](https://github.com/remarkjs/react-markdown)

API 类型由 OpenAPI 生成，禁止手改生成文件；静态类型不能代替后端校验。[openapi-typescript](https://openapi-ts.dev/introduction)

### T12.2 AG-UI 调试与事件消费

调试先创建固定快照和会话，再提交 Run。前端 AguiRunClient 使用 MASS 提交 API 和 EventSource GET 订阅，协议事件交给 @ag-ui/core 的对应事件 Schema 校验后进入 reducer；不同 SDK 版本的导出方法以锁定版本确认，不用 TypeScript 断言代替运行时校验。

EventSource.onmessage 的 data 是 AG-UI 对象，lastEventId 是 ui_sequence；runId 由订阅上下文固定，不能假定每一种 AG-UI 事件都有 runId 字段。生命周期事件的 threadId/runId 必须与订阅一致，消息/工具事件按稳定 ID 关联。CUSTOM 校验 name/value 版本，未知可选 CUSTOM 可忽略并推进游标，未知必需标准事件或非法数据须停止消费并报协议错误。

传输包装只存在于前端适配器内部，不能替代线上 AG-UI JSON。以下为消费顺序伪代码，reduceAgui 由实际协议 reducer 提供：

```text
onFrame(message):
  event = validateAgui(message.data)
  cursor = validateDecimal(message.lastEventId)
  if cursor <= state.cursor: return
  if cursor != state.cursor + 1: stopAndRecover(state.cursor)
  next = reduceAgui(state, event)
  commit(next, cursor)  // 状态应用成功后才前移游标
```

这里检查的是连续的 Run 传输序号，不是允许跳号的 Session 原始序号。客户端统一控制重连：onerror/解码或应用失败时关闭旧 EventSource，用最后成功应用的 after 重新建立连接；不让浏览器已接收但未成功应用的 Last-Event-ID 跳过事件。刷新时从 0 或成对保存的快照/游标恢复，不仅凭一个旧游标重连。

TEXT_MESSAGE_CONTENT 按 messageId 追加，工具参数按 toolCallId 聚合，TOOL_CALL_END 只关闭参数，结果到达后才展示执行结果。失败 attempt 的已显示文本标为 incomplete，重试新建消息；RUN_CANCELLED 显示已取消，其他 RUN_ERROR 显示失败，终态关闭订阅。事件回放只更新界面，不发起工具调用。

调试页同时展示原始日志、投影 Manifest、实际模型请求、epoch/前缀变化、真实缓存统计和载荷引用；摘要调用放在 Trace 区而非聊天助手回复中。普通 AG-UI 消息流不能冒充完整模型原文。空间切换立即关闭订阅并清空私有视图，大历史通过水位一致的 ui-snapshot 恢复并采用窗口展示。

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
| /runs/{id}/events、/runs/{id}/ui-snapshot | AG-UI over SSE、快照 GET | 同源鉴权，独立 ui_sequence、协议版本与水位一致性 |
| /runs/{id}/trace、/sessions/{id}/journal | 分页 GET | 内部原始日志及内容引用，Session sequence 与 UI 游标分离 |
| /runs/{id}/contexts、/payloads/{id} | GET | 投影/Manifest、epoch、缓存统计和受控完整载荷 |
| /artifacts/{id}/download | GET | 权限校验、受控文件名、保留状态 |
| /models、/credentials、/members | 基础 owner 管理操作；成员可先用同二进制命令配置 | 不引入权限矩阵；秘密不回显 |

错误码至少包括 unauthenticated、forbidden、not_found、draft_conflict、version_conflict、run_active、idempotency_conflict、context_overflow、unsupported_capability、unsupported_event_schema、projection_unavailable、event_conflict、budget_exceeded、trace_unavailable、cursor_expired、interrupted。错误码与展示文案分离，内部堆栈不返回给客户端。

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
│   ├── transport/agui/             # 协议事件映射与持久化传输视图
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

待收敛项仅包括 MariaDB/Go/Node 与 Eino/TOS SDK 的精确版本、实际 TOS 配置与权限、Eino 模型供应商和模型、计数器、默认限额及保留时长；已指定的单服务、MariaDB、TOS、Eino、frontend、简单登录及 AG-UI over SSE 不再作为待选方案。AG-UI Go/TS SDK 精确版本与兼容事件子集在 D00 固定并验证。

模型缓存能力、默认压缩阈值/目标余量和投影策略版本在对应实现阶段收敛；未知缓存命中不能用假数据补齐。

真实云资源缺失时用内存 TOS fake 和 Eino FakeChatModel 完成可验证编码，但不能以 fake 测试替代真实 TOS/模型接入验收。框架与 SDK 的实际方法、字段和重试行为以锁定版本核验，并将证据记录到工程开发文档。
