# MASS 技术方案

> 文档约束
> - 本文回答：每个模块怎样实现、依赖什么技术与第三方库、关键代码和数据契约是什么。
> - 包含：技术选型、模块算法、存储与接口设计、核心代码、运行配置、兼容性和实现限制。
> - 不包含：重新定义产品目标，或维护开发进度、测试执行报告和交付步骤。
> - 依据：[系统设计指引](design-guide.md)和[系统架构设计](architecture.md)；编码、测试与交付按[工程开发文档](engineering.md)执行。以后各模块细节继续补充在本文，不拆专题文档。
> - 状态：技术方案基线，D00 已收敛契约见 T00.6，D02 管理能力见 T01.1/T03.1，D01 持久化实现在 T10.2.1/T11.5，D03 定义管理见 T02.1，D04 运行实现在 T04.1/T05.1/T07.7/T08.5/T10.7/T11.6，D05 内置工具见 T09.6，D10 交付与离线恢复见 T13.1，真实 TOS 恢复已通过 E15.11 验收；更新：2026-10-24。实际实现与验证范围见工程开发文档；片段中未定义的工程类型需在对应阶段补齐。

## T00 技术基线与依赖

### T00.1 单服务运行形态

本节选型按用户要求固定：后端 Go，前端独立工程位于 `./frontend`，前后端由同一个 Go 进程提供服务；数据库 MariaDB；工程资源持久化采用字节跳动火山引擎 TOS；LLM 接入统一使用 CloudWeGo Eino；首版采用最简账号密码登录；运行事件采用 AG-UI 协议，通过 SSE 传输。Agent 仍使用 AGENTS.md 与指定版本 Skills，MVP 不接入沙箱。

交付为一个 `mass` 可执行文件、一个服务配置入口和一个监听地址。同一进程提供页面、API、SSE、文件下载、调度器及 Agent 执行器，不拆 Node.js 服务、BFF、Worker 或模型代理服务。MariaDB、TOS 和实际模型端点是外部基础依赖，“单服务”指只部署一个 MASS 应用服务，并不将这些外部依赖嵌入 Go 进程。

前端采用 React + TypeScript + Vite，源码和 package.json/package-lock.json 全部在 `frontend/`。构建生成 `frontend/dist`，构建脚本复制到后端嵌入目录，通过 Go embed 编入二进制。Node.js 仅用于构建、测试或文件构建监听；默认开发流程也由 Go 统一提供页面，不要求另开 Vite HTTP 服务。

版本资源、附件、产物及完整 Trace 原始载荷持久保存到 TOS，元数据及状态进入 MariaDB。每个 Session 在服务宿主机拥有真实临时工作目录，跨 Run 连续使用；每个已执行 Run 结束时打包上传 TOS，并将不可变快照 URI、摘要和来源入库。临时目录不是持久化权威来源，缺失或重启时从已提交快照恢复；无快照的新会话创建空目录。详见 T11。

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
| M09 路径匹配与搜索 | github.com/bmatcuk/doublestar/v4；regexp、strings | 匹配会话目录内的规范路径；受限文本搜索 |
| M09 受限计算 | github.com/dop251/goja；encoding/json、encoding/csv | 进程内 JavaScript、文件/网络宿主接口及数据转换，不安装 Node 运行环境 |
| M09/M11 工作目录 | os、path/filepath、archive/tar、compress/gzip、io；net/http | 会话目录、快照打包/恢复；代码网络请求 |
| M02/M09 校验 | github.com/santhosh-tekuri/jsonschema/v6 | 配置和工具参数运行时校验 |
| M03 凭证 | crypto/aes、crypto/cipher | 供应商凭证加密，主密钥从统一配置的秘密来源注入 |
| M04—M06 运行 | database/sql、context、sync、time | 幂等、事务认领、并发与取消 |
| M07 上下文 | SessionJournal、ContextPlanner/Projector + Eino schema/model | 固定日志边界、确定性投影、摘要与前缀稳定 |
| M08 模型 | github.com/cloudwego/eino；对应 eino-ext 模型组件 | 统一模型接口、工具定义和流式结果 |
| M08 Trace 捕获 | Eino callbacks + 自定义 http.RoundTripper | 语义事件和实际 HTTP 载荷分别记录 |
| M10 日志与 Trace | MariaDB JSON、TOS、net/http | 会话原始事件、内容引用、AG-UI 事件投影与 SSE 续传 |
| M10/M12 交互协议 | AG-UI；Go SDK github.com/ag-ui-protocol/ag-ui/sdks/community/go；前端 @ag-ui/core | 标准事件类型/校验、服务端序列化、前端协议消费 |
| M11 对象存储 | github.com/volcengine/ve-tos-golang-sdk/v2/tos | TOS 上传、读取、元信息与分片 |
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
| definitions / definition_drafts / definition_versions | Agent/Skill 共用结构，kind 区分；草稿 revision、不可变发布版本/调试快照、默认指针 |
| definition_dependencies | Agent 发布版本/调试快照到准确 SkillVersion、显示版本号、摘要和顺序 |
| definition_draft_objects / definition_version_objects | 分别保存草稿/历史版本的同空间 ready 内容引用 |
| model_configs / credentials | Eino 组件类型、实际模型配置、加密供应商凭证 |
| sessions | 空间归属、last_sequence、latest_workspace_snapshot_id、workspace_error；固定定义来自 session.created；本地目录位置由服务生成 |
| runtime_events / event_objects | Session 内追加日志、事件格式版本、因果关系、有序内容对象关联 |
| agui_streams / agui_events | 每个 Run 固定协议/映射版本、日志处理检查点、独立 ui_sequence、有序 AG-UI JSON；可重建的传输视图 |
| messages / runs / run_snapshots | 从日志同步维护的消息、运行状态及快照视图，保存来源事件位置 |
| context_summaries / context_projections | 摘要来源、投影输入边界、策略/渲染版本、epoch、Manifest 与投影对象引用 |
| session_active_runs | 每个 Session 唯一的活跃 Run 占用记录 |
| idempotency_keys | 空间、主体、操作、幂等键、请求摘要和结果引用 |
| content_objects / workspace_snapshots / artifacts | 不可变对象及摘要；快照的 space/session/run、base_snapshot_id、object_id、tos_uri、archive_sha256、manifest_ref、format_version；产物固定对象和来源 path/sha256 |
| attachments / run_plans | 附件上传状态及被接受输入的绑定；计划 revision、步骤与 source_event_id，均带空间范围；计划是日志派生视图 |
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
  migration_dsn_env: "MASS_MARIADB_MIGRATION_DSN"
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
  workspace_temp_root: "/tmp/mass-workspaces" # 服务专用临时根；不是持久存储
  snapshot_timeout: "60s" # 收尾上限；不是数据保留期限
tools:
  code:
    timeout: "5s" # 以下均为待压测的初始示例，不是沙箱硬限制
    max_concurrent: 1
    max_source_bytes: 65536
    max_input_bytes: 2097152
    max_output_bytes: 2097152
    max_log_bytes: 65536
    max_file_operations: 100
    max_network_requests: 10
    max_network_body_bytes: 2097152
```

前端 API 使用相对路径，不单独保存另一套后端地址。TOS endpoint/region/bucket 与实际模型仍需用户环境提供，不猜测真实值。配置、二进制与会话临时目录可以存在本地，持久历史和已确认快照不依赖原主机目录。

此配置管理服务部署和模型接入默认值；空间内的模型选择与凭证是 MariaDB 中的业务配置。初始化时可显式导入默认模型，之后以空间配置为准，重启不得覆盖用户修改；Run 保存最终解析结果及来源，避免文件与数据库互相覆盖。

### T00.6 D00 已收敛的工程契约（2026-10-18）

工具链固定 Go 1.26.9、Node.js 24.22.0（npm 11.19.1），MariaDB 目标版本为 11.8.9 LTS；D00 时尚未接入数据库；D02 补齐的连接与迁移契约见 T01.1。精确 Go/前端依赖分别以 go.mod/go.sum、frontend/package-lock.json 为准；TypeScript 使用 5.9.3 以满足 openapi-typescript 的 5.x peer 约束。服务编译前必须构建并同步前端资源。

D00 只开放 `serve`、`check-config`、`version` 命令及 `/api/v1/system`、`/health/live`、`/health/ready`。配置拒绝未知字段、多文档、别名、过深/过大输入及缺失秘密来源；master_key_env 使用 base64 编码的 32 字节密钥。`http.shutdown_timeout` 控制有界关闭。此时 live=200，ready=503/dependencies_not_initialized；D01 安装真实存储/schema 检查后再具备业务就绪条件，不以静态成功响应冒充接入。

业务 ID 采用服务生成的 UUID；请求标识为 16 随机字节的小写十六进制。幂等键为 1—128 字符的 ASCII 字母、数字、点、下划线、冒号或短横线。序号采用非负 signed bigint，线上为规范十进制字符串，最大 9223372036854775807；事件从 1 开始，初始游标为 0。分页上限 100，默认 20，游标是不透明且绑定查询范围的字符串。契约写入 api/openapi.yaml 与 api/events，不代表尚未开放的业务 API 已实现。

首个模型组件锁定 Eino Ark，关闭 SDK 重试并验证自定义 HTTPClient；依赖试验使用本地 httptest 端点，不要求真实模型凭证。goja 试验验证显式 fs/net、连续文件读取、取消及 source map 禁用；试验宿主只用于测试，不是 D05 的完整工具实现。AG-UI profile 固定 mass-agui-v1；锁定的 @ag-ui/core 1.0.3 将运行时 Schema 导出放在 `@ag-ui/core/schemas`，需显式安装 zod。Monaco 的 DOMPurify 传递依赖通过 package.json override 固定到 3.4.17。采用既定事件子集与 RUN_ERROR/RUN_CANCELLED 映射，不自动启用新版协议扩展。

## T01 M01 最简密码登录与空间归属

依赖：net/http、bcrypt、crypto/rand、crypto/sha256、database/sql。认证采用“账号密码校验 → 建立会话 → 查询当前用户 → 退出”，不接入 OAuth/SSO、验证码、注册找回、JWT 刷新体系或通用 RBAC 框架。

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

空间隔离用 space_members 检查成员关系，is_owner 区分管理操作；成员可操作本空间 Agent/Skill/Run，owner 管理成员和模型凭证。不建设角色矩阵或邀请系统。账号由 `mass user create` 初始化；空间与成员必须有以下最小页面及 API，不能仅靠管理命令：

| API（相对 /api/v1） | 行为 |
| --- | --- |
| POST /spaces、GET /spaces | 创建空间并将创建者设为 owner；列出本人所属空间 |
| GET /spaces/{space_id}/members | 成员查看本空间成员列表 |
| POST /spaces/{space_id}/members | owner 按已有账号 username 添加普通成员；重复添加幂等 |
| DELETE /spaces/{space_id}/members/{user_id} | owner 移除普通成员；本人可退出；MVP 禁止删除 owner，空间删除/所有权转移留待后续 |

页面提供创建、切换、成员列表、添加与移除，服务端重新校验每次操作。用户名只做精确匹配，不开放全站用户枚举。成员变更与 audit_logs 同事务。

**权限失效只报错并结束任务。** 最小身份 API 保留 POST /auth/login、POST /auth/logout 和 GET /auth/me。HTTP 入口、文件下载、SSE 输出批次、模型调用和每个工具/宿主 I/O 前重新检查当前主体；排队任务认领时也检查。移除成员提交成功后，进程内通知 Supervisor 取消该 actor 在该 space 的 queued/running Run，撤销活跃读取连接；Supervisor 的已有心跳定期复核作为通知遗漏兜底。不增加新任务状态、重新授权等待或自动续跑。未登录/登录失效返回 401 unauthenticated，成员失效返回 403 forbidden；Run 以 failed 和同名错误原因结束。已完成 Run 不因后来移除成员改写。

模型 HTTP、代码网络请求、文件循环和 VM 接收取消；等待实际退出后才释放会话占用。正在传输的请求或已经发生的副作用不能追溯撤销，但之后不启动新动作。服务使用独立有界收尾 context 保存已收到的结果、权限失败和已有文件快照；这是系统收尾权限，不允许被移除主体借此读取内容或继续执行。数据库无法验证权限时拒绝继续，以实际存储错误结束，不伪称 403。

SSE/下载已发送响应头后无法改 HTTP 状态：停止后续数据并关闭连接，前端通过普通 API 复核；得到 401/403 后清空私有视图、显示对应错误并停止重连。订阅者失去权限只终止其连接；只有 Run 发起者失去权限才终止该 Run，其他授权成员可查询失败事实。

### T01.1 D02 已实现的身份与空间契约

应用编排位于 internal/app/identity，MariaDB Repository 位于 internal/adapter/mariadb，HTTP 层只负责 Cookie、请求检查与 DTO。新增依赖 golang.org/x/crypto v0.58.0（bcrypt 默认 cost=10）；用户名为大小写敏感 ASCII 的 3—64 字符，密码创建要求 12—72 字节。CLI 只接受 --password-env 指定秘密环境来源，不接受明文密码参数。登录返回统一失败信息，未知用户也执行 bcrypt；每个直接连接 IP 每分钟最多 10 次尝试、全局最多 4 个并发密码校验，限速桶上限 4096。暂不信任 X-Forwarded-For；反向代理部署目前共享代理地址限额，后续需要显式可信代理配置才能扩展。

随机会话令牌为 32 字节，浏览器只收到十六进制 Cookie；MariaDB 保存 SHA-256 摘要和 UTC 固定到期时间。Cookie 名 mass_session，Path=/、HttpOnly、SameSite=Lax，Secure 由经过回环/HTTPS 检查的配置控制。重新登录替换当前 Cookie 对应的旧服务端会话；退出删除服务端记录。请求体上限 16 KiB，拒绝未知字段及额外 JSON 文档。所有管理写请求（包括 DELETE 和退出）要求 application/json 与精确匹配 http.public_url 的 Origin；不根据客户端 Host 推导可信来源。

迁移 00001_identity 建立 users、spaces、space_members、auth_sessions、audit_logs；00002_models 建立 credentials/model_configs。迁移嵌入同一二进制，只提供向前迁移，serve 检查版本而不自动执行 DDL。database.migration_dsn_env 仅 migrate 要求和使用，应用 DSN 只需 SELECT/INSERT/UPDATE/DELETE；DDL 不能假定回滚，部分失败须检查实际 schema 后修复。连接统一 utf8mb4/UTC、有界连接池和网络超时。这两份基础迁移是 D02 交付时的必要前置补充；后续 D01 通过新增 00003 补齐对象、快照与日志，见 T10.2.1。

所有空间内 Repository 操作按「空间行 → 成员关系 → 资源」顺序在事务内授权并加锁；成员变更、模型和凭证变更与审计共同提交。添加已有成员幂等且不重复审计，普通成员可退出自己，owner 永不可删除。模型/凭证联合外键携带 space_id，跨空间 ID 不可用于建立引用。列表默认 20、最多 100 条，以 UUID keyset 排序，HMAC 游标绑定 actor/space/list/filter；管理列表为实时视图，新增到已翻页位置之前的项需刷新才能看到，不宣称不可变历史快照。

WatchAccess 先登记取消订阅再查询权限，消除订阅和撤权的检查竞态；RemoveMember 事务提交后通知同空间/主体的订阅者。CheckAccess 为每次动作与心跳提供重新查询入口，数据库错误保持存储失败语义。D04 尚未实现，通知目前通过真实数据库集成测试验证；尚无 Supervisor、后台 Run 或长连接可供终止，不能把 context 取消等同于实际退出。前端私有查询键包含 space_id；切换、退出、401/403 时取消请求、清空私有缓存及表单，成员页面每 5 秒复核，收到权限失败后停止该页面查询。

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
model: 00000000-0000-4000-8000-000000000001 # 本空间可用模型配置 ID
skills:
  - id: 00000000-0000-4000-8000-000000000002
    version: 1.2.0
  - id: 00000000-0000-4000-8000-000000000003
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

重复版本由唯一约束拒绝；并发草稿变化不会发布错内容。提交失败留下不可见的未引用 Blob，MVP 不自动清理；不得先声明发布成功再补文件。

调试使用同一解析与校验链创建不可变 snapshot。默认版本切换不改版本内容；归档只阻止新的选择，不破坏原有引用。从草稿移除资源不删除已保存的历史对象；MVP 无自动过期或回收策略。

接口组：Agent/Skill 创建、草稿 GET/PUT（携带 revision）、TOS 资源上传、发布、版本列表/详情/差异、依赖查询、归档；Agent 另有默认版本切换与调试快照创建。

### T02.1 D03 已实现的定义契约

新增迁移 00004_definitions，不改动已发布的前三份迁移；D03 使用 schema 4；当前 serve 要求 schema 6。Agent 与 Skill 的元数据、草稿和版本复用 definitions / definition_drafts / definition_versions，以 kind 区分，外部 API 仍为 /agents 和 /skills。全部关联含 space_id；definition_dependencies 保存有序、准确的 Skill 内部版本 ID，数据库外键限制删除被引用版本。版本 label 在单一定义内区分大小写且唯一；latest（不区分大小写）及范围表达式不被接受。debug=true 的不可变快照无发布 label，不改变默认版本。

定义名称为 1—64 字节的小写字母/数字/连字符标识，首字符为字母。agent.yaml 的 model 和 skills[].id 分别使用本空间模型配置 ID 与 Skill ID，version 是准确显示版本；name 必须与定义名称一致。严格拒绝未知字段、重复键、多 YAML 文档、alias/anchor、合并键、非法类型、未知工具、重复 Skill/工具及越界限额。草稿允许空正文和空配置便于新建；非空配置保存时校验结构，发布和调试快照进一步要求非空正文、完整配置、可用模型/凭证和未归档的准确依赖。输入/输出预留总量不得超过模型上下文容量，输出预留不超过模型上限。平台上限为 1000 步、3600 秒；这里是配置校验，D04 已接入运行限额，D05 继续扩展工具。

Document 包含 Markdown 原文、配置原文和资源清单；正文上限 256 KiB、配置 64 KiB、资源最多 100 个、单资源 8 MiB。资源路径只允许 assets/ 下的规范相对路径，拒绝穿越、绝对路径、大小写冲突及文件/目录前缀冲突。JSON 上传接口使用 data_base64，HTTP 请求上限 12 MiB；资源上传自动推进草稿 revision。上传先经 ContentStore 写 TOS 并验证 ready，再在普通管理事务中提交草稿与引用；失败的未引用对象不能下载。草稿移除资源只移除当前引用，历史版本引用仍有效。

发布准备阶段在授权事务中读取草稿、解析依赖和当前模型非秘密有效配置；随后在事务外将完整 Bundle 保存为 TOS 不可变对象，内容含原文、资源摘要、有序依赖 ID/label/hash、模型和凭证引用，不含秘密。最终事务再次检查成员、归档状态、草稿 revision、模型/依赖和 Bundle 摘要，再原子写入版本、依赖、对象引用、审计及 Agent 首次默认指针。准备后模型或草稿变化返回 draft_conflict，不发布混合内容；唯一版本冲突返回 version_conflict。网络写入不占用 SQL 事务，SQL 回滚保留不可见孤立对象，无 GC。

调试快照走相同准备/发布链，读取版本时验证 TOS 完整摘要并在远端读取后复核成员。默认版本切换只改指针；当前默认版本必须先切换后才能归档。归档阻止新的发布/依赖选择，已发布依赖与历史内容可继续读取。DELETE 版本不会物理删除；被引用返回 version_referenced，其他返回 invalid_definition，生命周期通过归档管理。复制定义创建独立新草稿，重写 Agent 配置 name，资源复用不可变对象。资源下载复用 D01 payloads 鉴权读取。

前端使用延迟加载的 Monaco 编辑器和只读差异视图；支持模型/Skill 准确版本选择、工具选择、Markdown 预览、上传/移除资源、发布、默认版本、归档、复制、反向引用和调试快照。409 保留本地内容，读取服务器草稿展示差异，仅在显式点击后载入服务器内容。Markdown 不执行原始 HTML、不显示外部图片，只开放 http/https 链接。run_code 选择项说明文件与网络权限范围。调试快照固定定义；D04 会话创建可选择该快照执行任务。

## T03 M03 模型配置与凭证

依赖：database/sql、AES-GCM、crypto/rand。模型配置包含 provider、model_id、能力与容量、非秘密参数、批准端点和 credential_ref。一般空间用户从批准端点选择，不直接指定任意网络地址。

凭证密文关联 space_id、credential_id 和 key_id 作为附加认证数据；每次加密生成唯一 nonce。主密钥通过服务配置的秘密文件或环境注入，不提交仓库，不与密文一起作为数据库字段保存。明确密钥轮换和旧备份解密的要求。

Run 固定非秘密有效配置和凭证引用；每次实际调用重新验证权限及凭证有效性，记录非秘密凭证版本标识。凭证轮换不要求历史快照保存旧秘密。

参数能力在发布/调试和实际调用前校验。Provider 不支持工具、流式或输入类型时返回明确错误，不静默降级。费用记录同时保存价格来源及版本；无价格时只展示 usage。

接口组：模型配置 CRUD、可用模型能力查询、凭证创建/替换/禁用；凭证响应永不回显秘密。

### T03.1 D02 最小模型配置

批准端点默认 https://ark.cn-beijing.volces.com/api/v3，可通过 llm.base_url_env 指向部署批准的兼容 Base URL；旧配置缺省保留默认值。自定义地址要求 HTTPS，仅回环测试允许 HTTP，拒绝 userinfo/query/fragment。组件固定 D00 已验证的 Ark；model-capabilities 查询给出 text、streaming、tool_calling 支持范围。owner 填写已开通的模型 ID、context_window、max_output_tokens 和 temperature，后端检查模型 ID 格式、端点、容量和参数边界，不允许任意 URL。可用模型列表只返回模型和凭证均启用的配置。D04 已接入实际模型文本调用，多工具/缓存等能力继续在 D07 验证；D02 的配置保存不发起收费调用，也不声称测试用 ep-test 真实存在。

模型支持创建、列表、详情、修改、停用；DELETE 是保留引用的软停用。凭证支持创建、修改/替换/禁用；修改必须携带 revision，旧 revision 返回 409。响应和审计不包含秘密、密文或主密钥。AES-256-GCM 使用 Go 随机 nonce AEAD，AAD 为版本化的 space_id/credential_id/key_id 元组；key_id 为主密钥 SHA-256 的前 16 字节标识。主密钥由已有 base64 32 字节配置来源加载，缺失或不匹配不能解密。

当前只有一个有效主密钥；不得直接更换主密钥后丢弃旧值。主密钥迁移/密钥环未实现，备份恢复须保留与密文对应的旧主密钥；D07 再扩展完整轮换流程。供应商 API 密钥替换已支持，使用新随机 nonce 并增加 revision。服务端 ModelSecret 重新检查成员、模型/凭证启用状态后才能解密，HTTP 不暴露此方法；D04 每次调用重新解析 Run 固定的 credential_id，并检查当前模型及该凭证启用状态；凭证替换使用当前 revision，模型改绑不会悄悄改用其他凭证。通知与心跳配合停止检查，不缓存授权结果。llm.model_env/api_key_env 只用于显式真实冒烟，不自动向空间导入凭证。

## T04 M04 会话、消息与 Run

依赖：database/sql、SessionJournal、ContentStore、crypto/sha256、context。会话与执行链路以 T10 的 runtime_events 为权威事实来源；messages、runs、run_snapshots 是与事件同事务更新的查询/调度视图，记录 applied_sequence 和 reducer_version，不允许脱离日志单独改变业务事实。Session 的游标、执行锁和心跳属于协调数据，不要求逐次转成模型输入或审计事件。

创建 Session 时追加 session.created，固定 Agent/Skill 准确版本、指令原文对象、有序 Skill 清单、平台指令内容及版本；不能只保存可变配置 ID。Agent/Skill 发布库负责编辑与版本管理，日志用不可变引用复用内容，投影通过日志和它引用的对象读取定义。

用户提交先把原始文本和附件保存为 content_objects；同一数据库事务检查权限、锁定 Session、追加 user.message.accepted 和 run.created、更新消息/queued Run 视图、插入 session_active_runs 占用及幂等记录。run.created 同时记录 base_workspace_snapshot_id，并固定模型非秘密配置、工具 Schema/实现版本、预算、构建标识和 user_event_id。未提交成功的输入不进入后续投影。默认 Agent 版本只在创建新 Session 时解析。

相同主体/操作/幂等键且请求摘要相同返回原结果；内容不同返回 409。幂等核实先于活跃 Run 冲突判断；并发提交由唯一约束裁决，不重复追加用户事件。占用主键冲突时整个事务回滚。

终态为 succeeded/failed/cancelled；queued/running 属于活跃状态。run.cancel.requested 表示收到取消，不代表停止；queued 可在同事务追加 run.finished 并释放占用，running 等实际停止后终结。

对于实际开始执行的 Run，Supervisor 等待执行停止后保存完整工作目录快照（包括失败/取消后的已发生变更），再将 workspace.snapshot.created、快照 URI/索引、Session 最新快照指针、run.finished、最终回复引用、usage、状态及占用删除同事务提交。上传与 SQL 不构成共同事务；快照失败处理见 T11.4，不能返回成功。排队取消且未使用目录的 Run 不产生新快照。最终回复引用 model.response.completed，不另造一份同内容原始消息。部分输出保留在日志，但不作为成功回复。失败运行的历史参与规则见 T07；取消或失败不能抹去已确认副作用。

同一 Session 的下一 Run 复用当前目录；目录丢失或服务重启时恢复 latest_workspace_snapshot_id。新 Session 可传 source_workspace_snapshot_id，服务仅解析当前主体可访问的同空间快照，记录来源后恢复到独立目录；不接受任意 URI，不复制旧会话对话，也不改变新会话选择的 Agent 定义。恢复事实追加 workspace.restored，快照错误时拒绝开始执行，不把恢复失败当空目录。

MVP 不原地修改已接受的消息。补充或纠错追加新用户消息；历史分支和消息编辑重算不属于首版范围。视图重建只读取事件和内容，不调用模型或工具；离线重建验证通过后才切换视图，不直接驱动生产调度。

接口组：POST /sessions、GET /sessions/{id}/messages、POST /sessions/{id}/runs、GET /runs/{id}、POST /runs/{id}/cancel。提交返回 202 和 Run ID；消息接口查询视图，同时提供来源事件标识。投影不以页面消息视图替代原始日志。

### T04.1 D04 实现与当前入口

新增 00005_runtime，保持 00001—00004 不变。sessions 扩展固定定义引用和恢复状态；runs 保存不可变 Run snapshot、取消标记及收尾计划；runtime_requests、messages、model_attempts/model_calls、tool_executions、artifacts 与运行事件同事务维护。`internal/domain/run.CheckTransition` 统一 Claim/Cancel/Finish 规则。成功必须来自同 Run 的无工具调用 `model.response.completed`，并有已提交的工作目录快照。

`internal/app/session` 只接受并持久化请求，不执行任务。创建会话解析准确发布/调试版本，保存平台/Agent/Skill 指令原文；提交时固定当前模型非秘密参数及实际工具 Schema/版本。幂等键作用域包含主体、空间、操作；先查原结果再判断占用。单 Session 只能有一个活跃 Run，全局 queued 上限 100。附件接收待 D05。

HTTP 在 `/api/v1/spaces/{space_id}` 下提供 sessions 列表/创建/详情、会话 runs 提交/列表、messages、workspace-snapshots，以及 runs 详情/cancel/trace/contexts/artifacts/ui-snapshot/events。提交返回 202，读取/断开 SSE 均不触发执行；准确请求及返回结构以 api/openapi.yaml 为准。

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

确认旧进程退出后，重启将遗留 running 标记为 interrupted 失败，保存未完成调用并释放占用；queued 继续调度。会话文件从 MariaDB 指向的最近已提交 TOS 快照恢复到新临时目录，不信任遗留脏目录，也不自动重做未知副作用。中断 Run 尚未保存的文件修改可能丢失，恢复结果明确注明快照来源与中断原因；无已提交快照只可恢复创建时的空目录或指定起始快照。

停机先停止接单和认领，有界等待或取消已有执行，刷写 Trace；无法停止的工具由进程退出终止，重启时标记中断，而不是提前报告成功取消。

### T05.1 D04 调度和收尾实现

bootstrap.StartRuntime 组装串行 Scheduler、Supervisor、Executor、Planner、Gateway 与工具 Dispatcher。Scheduler 每 300ms 轮询并接受内存通知，先获取全局名额，再按空间最多 min(全局, 2) 准入，事务追加 run.started 后交接。数据库写入沿用空间授权锁 → Session → Run 顺序。GET_LOCK 独占连接保留至所有执行实际退出，普通请求取消不会关闭该连接。

Supervisor 以服务生命周期创建有界 Run context，每 500ms 复核主体、固定凭证和模型启用状态并更新心跳。取消、撤权、超时、panic 均等待实际执行退出，再调用 resource.Finisher。运行通知不承担权限授予，撤权后的普通查询/动作仍被 Repository 拒绝。

Finisher 的系统身份是一个绑定已认领 Run、主体、空间/Session 和 owner 的窄 Repository 能力，仅允许收尾/核实事实及该 Run 的归档/Manifest；调用方伪造 actor.kind=system 不能绕过鉴权。成功提交再次检查成员资格和取消标记；竞态发生时复用相同归档改为失败/取消收尾。收尾计划和稳定 UUID/时间写入数据库，以便核实未知提交；SQL 失败保留占用并停止接单，快照上传失败则提交明确失败和文件阻塞状态。

启动取得单实例锁后，将遗留 running 认领为 interrupted 收尾，未结束的模型 attempt 记失败、工具操作记 operation.reconciled/unknown；丢弃遗留脏目录，保留最近已提交快照指针，不重做动作。queued 继续正常调度。锁不提供分布式恰好一次或外部副作用 fencing。

## T06 Runtime 子模块与 M06 执行循环

### T06.1 包边界与职责

`internal/runtime` 是 M05—M09 执行能力的组织目录，不是一个包含所有实现的 Go package。按下表建立叶子包；M04 的提交/查询在应用层，M10 的统一日志和 M11 的资源服务是管理与运行共用的能力，放在 runtime 之外。模块编号继续沿用架构文档，不因代码拆包新增产品模块。

| 子包（相对于 internal/） | 归属 | 输入 → 输出与独占职责 | 不承担的职责 |
| --- | --- | --- | --- |
| runtime/scheduler | M05 | queued 候选、并发配额 → 已认领 Run 与准入名额；串行 dispatcher、唤醒/轮询、单实例锁监测、认领后交接 | 不创建用户消息、不执行 ReAct、不写运行终态 |
| runtime/supervisor | M05 | 已认领 Run 与名额 → 有界执行、终态和资源释放；Run context、取消信号、心跳、panic 收尾、启动恢复 | 不组装消息、不解析模型响应、不实现工具 |
| runtime/executor | M06 | RunSnapshot → Outcome 或 error；唯一 ReAct 循环、步骤推进、串行派发一组工具、选择最终响应 | 不更新 Run 状态、不持有 SQL/TOS/SDK、不做摘要算法 |
| runtime/control | M06 | Run 限额、操作类型、已记录用量 → 允许执行或停止原因；统一次数、时限与预算检查/预留/结算 | 不管理队列并发、不组装上下文、不独立保存计费事实 |
| runtime/context/planner | M07 | Run 与日志最新已提交边界 → 已保存的 PreparedContext；容量规划、摘要编排、epoch 选择、投影登记 | 不实现模型传输、不递归调用自己生成摘要 |
| runtime/context/projector | M07 | 固定 ProjectionSpec、事件和不可变内容 → 消息、工具定义、Manifest 与内容指纹；完整单元、稳定排序、版本化渲染 | 不调用 LLM/工具、不选择当前草稿、不写日志或生成摘要 |
| runtime/modelgateway | M08 | PreparedContext 与 purpose → 已记录的完整 ModelReply 或失败；attempt、重试、授权/预算、流聚合、完整性与 usage | 不回调 Planner、不执行工具、不决定 Run 终态 |
| runtime/tool/registry | M09 | 平台内置定义与快照版本 → 不可变工具描述和 Handler；名称/版本索引、Schema 编译与精确版本解析 | 不按模型文本动态注册、不做运行调度 |
| runtime/tool/dispatcher | M09 | 已完成响应中的 ToolCall → 已记录的工具结果；授权、参数校验、限额、超时、调用及结果提交 | 不把 TOOL_CALL_END 当作执行结果、不直接写 SQL |
| runtime/tool/builtin | M09 | 已校验参数与受限资源接口 → PreparedToolResult；T09 的 14 个内置工具具体实现 | 不越过 resource 直接访问任意宿主文件、不自行提交 tool 终结事件、不控制执行循环 |

包内再按实现职责拆文件：supervisor 分为 supervise/cancel/recover，planner 分为 prepare/epoch/summary，projector 分为 history/units/render/manifest，modelgateway 分为 attempt/stream/retry/usage，dispatcher 分为 authorize/validate/execute/commit；文件共享该包的内部类型，不再为每个函数增加子包。供应商帧解码与实际 HTTP 捕获仍属于 adapter/eino。

拆分仍是同一个 Go 进程内的方法调用，不引入 RPC、消息队列或插件系统。`scheduler` 的并发名额与 `control` 的 Run 调用/用量预算是两类资源，不能用同一个计数器代替。

共用能力的代码归属如下：

| 包 | 责任与接口边界 |
| --- | --- |
| domain | RunSnapshot、事件/内容引用、ProjectionSpec、ModelReply、ToolCall、Outcome 等跨模块值类型和纯状态规则；不依赖 runtime、数据库、HTTP 或供应商 SDK |
| app | 接受会话/Run/取消请求、权限校验与查询编排；M04 通过 Journal 提交事件，不直接调用 Executor |
| journal | SessionJournal 的可信追加与读取服务、事件校验及纯 reducer；状态、消息、用量和快照引用进入统一事务；本地文件动作不属于 SQL 事务，供 app/runtime 共用 |
| resource | 内容完整性、版本资源读取、会话真实目录管理、文件操作与快照准备；通过 adapter/workspace 执行文件系统动作，返回内容摘要与实际效果，不宣称 SQL 回滚可撤销文件 |
| adapter/mariadb、adapter/tos、adapter/workspace、adapter/eino | 分别实现事务/查询、对象访问、会话文件与快照、Eino 模型/原文捕获；供应商配置与框架类型限定在模型适配边界 |
| transport/agui、transport/http | Journal 到 AG-UI 的持久化派生视图，以及 API/SSE 传输；不成为执行器、模型网关或投影器的依赖 |
| bootstrap | 构造具体组件并注入依赖、注册内置 Handler、启动/关闭后台任务；不包含业务状态转换 |

`domain` 按 run/event/context/model/tool/content 的概念拆文件或小包，禁止变成存放所有业务逻辑的公共工具包。Trace 查询和 usage 汇总复用 journal 的事实/视图；不再额外建立可写的 runtime/trace 历史库。

### T06.2 依赖方向与接口所有权

以下箭头表示调用方向；调用方定义窄接口，bootstrap 注入实现，并不意味着必须直接 import 被调用实现包：

```text
app ────────────────────────────────────→ journal / resource
scheduler → supervisor → executor → context/planner → context/projector
                                  │                └→ modelgateway (purpose=summary)
                                  ├→ modelgateway (purpose=task)
                                  └→ tool/dispatcher ─→ tool/registry (解析 Handler)
                                                     └→ builtin Handler → resource / CodeRunner
executor / modelgateway / tool/dispatcher → control
planner / modelgateway / dispatcher / supervisor / scheduler → journal 的各自窄接口
transport/agui → journal 的只读接口 → 已提交事实
bootstrap → 上述具体实现及 adapter/*，完成组装
```

`ModelCaller` 等消费接口声明在使用它的包中，共享数据契约放 domain；不建立包含所有能力的 RuntimeService、全局 ServiceLocator 或可任意发事件的通用 EventBus。适配器依赖消费接口/领域值，不允许核心反向 import adapter、transport 或 bootstrap。跨边界不传 `*sql.Tx`、HTTP request、TOS client 或具体供应商配置。

| 消费方 | 最小接口与实现方 | 关键返回约束 |
| --- | --- | --- |
| scheduler | CandidateReader、ClaimWriter（MariaDB/Journal）；RunLauncher（Supervisor） | 认领提交后交接；启动结果明确是否接收了名额所有权 |
| supervisor | Runner（Executor）、WorkspaceSnapshots（resource）、FinishWriter（Journal）、取消/心跳存储接口 | 运行实际退出后才提交终态；重复 Finish 返回相同结果或状态冲突 |
| executor | ContextPreparer（Planner）、ModelCaller（Gateway）、ToolDispatcher、RunControl | 返回成功即对应动作已持久化；Executor 只保留引用，不缓存另一份历史 |
| planner | JournalReader、ContentReader、Projector、SummaryCaller（同一 Gateway）、ProjectionWriter（Journal） | 摘要先保存再选用；PreparedContext 仅引用已提交的 context.projected |
| projector | 固定边界 JournalReader、不可变 ContentReader、版本化计数器/渲染器 | 只读；返回投影值/有界内容流，不保存对象与事件 |
| modelgateway | ModelTransport（Eino adapter）、AttemptRecorder（Journal/ContentStore）、RunControl、授权/凭证接口 | 原文先保存再消费；完整 ModelReply 携带已提交响应 event_id |
| dispatcher | ToolResolver（Registry）、RunControl、授权接口、ToolOutcomeWriter（Journal） | 终结事件与业务视图同事务；本地/网络动作先发生，实际效果及结果入日志，下一轮从日志读取 |
| builtin | ContentReader、SessionWorkspace、SkillResourceReader（resource）；CodeRunner（JS adapter）、NetworkClient、受限 I/O 记录接口（Dispatcher 注入） | 只获得当前空间/Session/版本范围能力，报告实际效果与结果，不自行提交终结事件 |

Eino 的 `schema.Message`/`ToolInfo` 转换由 `adapter/eino` 负责；domain 的有序内容块和供应商扩展字段必须能无损保留后续对话所需信息，不能以“通用 DTO”为由删除协议字段。网关面向领域 ModelTransport；T08 的 Eino 流示例属于该适配边界，SDK 的真实传输仍统一捕获。

Planner 可调用 Gateway，Gateway 永远不依赖 Planner。摘要使用显式 `purpose=summary` 的固定来源输入和独立投影，关闭工具；摘要的输入预算不足则按版本化策略分段或失败，不递归触发另一轮 Planner.Prepare。上下文容量计算属于 M07；Run 的累计额度检查属于 control，摘要和重试均计入同一 Run。

### T06.3 ReAct 循环与终态所有权

MVP 只保留这一层显式 ReAct 循环，不再套 Eino Agent 的完整循环。后续引入 DeepAgent 或 Eino ADK 时，作为执行策略替换/扩展 Executor，继续复用 Planner、Gateway、Dispatcher 和 Supervisor；不能新增一套旁路日志、预算或工具权限。

以下为消费接口与循环骨架。RunSnapshot、PreparedContext、ModelReply、ToolCall、Outcome 为 domain 值类型在该包内的类型别名；Outcome.FinalResponseEventID 引用已经完成的模型响应。构造器必须注入全部依赖，不由 Executor 创建模型客户端或 Repository。

```go
// package executor；领域类型别名及错误声明在对应实现阶段补齐。
type ContextPreparer interface {
    Prepare(context.Context, RunSnapshot) (PreparedContext, error)
}
type ModelCaller interface {
    Call(context.Context, RunSnapshot, PreparedContext) (ModelReply, error)
}
type ToolDispatcher interface {
    DispatchAndRecord(context.Context, RunSnapshot, ToolCall) error
}
type RunControl interface {
    Check(context.Context, RunSnapshot) error
}
type Executor struct {
    Context ContextPreparer
    Model   ModelCaller
    Tools   ToolDispatcher
    Control RunControl
}

func (e *Executor) Execute(ctx context.Context, run RunSnapshot) (Outcome, error) {
    for step := 0; step < run.MaxSteps; step++ {
        if err := e.Control.Check(ctx, run); err != nil { return Outcome{}, err }
        input, err := e.Context.Prepare(ctx, run)
        if err != nil { return Outcome{}, err }
        reply, err := e.Model.Call(ctx, run, input)
        if err != nil { return Outcome{}, err }
        if !reply.Complete { return Outcome{}, ErrIncompleteModelOutput }
        if len(reply.ToolCalls) == 0 {
            return Outcome{FinalResponseEventID: reply.EventID}, nil
        }
        for _, call := range reply.ToolCalls {
            if err := e.Control.Check(ctx, run); err != nil { return Outcome{}, err }
            if err := e.Tools.DispatchAndRecord(ctx, run, call); err != nil {
                return Outcome{}, err
            }
        }
    }
    return Outcome{}, ErrStepLimit
}
```

只有 Supervisor 为已认领的 Run 提交成功/失败/取消终态。Executor 返回 Outcome 不代表数据库状态已经完成；Supervisor 校验最终响应引用，执行 T11 的快照收尾后，在 T04/T10 的事务中追加 run.finished、更新视图和释放会话占用。queued 取消仍由 M04 在事务内终结；启动中断恢复由 Supervisor 发起，均使用同一 Journal 状态规则。

Supervisor 区分 cancelled、deadline、模型失败、持久化失败及 panic，使用独立且有超时的收尾 context 保存终态；不能复用已取消的执行 context，也不能无限等待数据库恢复。Finish 事务最后复核 Run 主体的成员权限并与成员变更串行裁决；已撤销则 failed/forbidden，保留已完成响应与快照，不被普通 context cancelled 覆盖。权限仍有效时再裁决取消：锁内已存在取消请求则 cancelled，成功终态先提交则后来取消/撤权不改写旧终态。收尾期间撤权也遵循此规则。

scheduler 取得并发名额，认领失败自行释放；认领成功后 Supervisor 接收 Run 与名额并承担一次性释放责任。交接失败由 scheduler 调用 Supervisor 的启动失败收尾入口；认领成功后不重新入队重放。取消后必须等待执行栈和子调用退出才能释放执行名额；会话占用仅随终态事务释放。收尾持久化失败时可以释放已停止执行的内存名额，但 Run/会话仍保持待恢复状态，停止继续认领并暴露运行子系统异常，不能假报结束。

Run context 由 Supervisor 从服务生命周期派生；模型流、摘要和工具获得其子 context。scheduler/AG-UI 投影是服务级后台任务，Run 内 goroutine 由启动它的组件负责等待退出；不得在取消后遗留未跟踪的工具工作。无法停止的动作等待进程退出并在恢复时标记中断。

工具业务错误可以形成结构化工具结果继续交互；访问越权、预算耗尽或记录失败按策略终止。所有 call_id 必须有结果或按 T07 明确转换为中断说明。模型参数先保存再校验；长度截断不能成为正常完整回复；副作用未知的工具不自动重试。

### T06.4 动作记录、事务与预算边界

模块拆分不拆开日志与数据库视图的原子提交；文件系统、网络、TOS 与 SQL 之间没有共同事务。内容上传在事务外，运行事实和必要视图在 Journal 的一次短事务内提交；调用者不能通过回调向 Journal 注入任意 SQL。

| 动作 | 编排者 | 持久化边界 |
| --- | --- | --- |
| 投影/摘要 | Planner | Projector 返回值 → ContentStore 保存不可变内容 → Journal 追加 context.projected/context.summary.created；返回前提交引用与索引 |
| 一次模型 attempt | Gateway + Eino 传输适配 | 请求/响应原文由 AttemptRecorder 保存到 TOS/Journal；完整聚合后 Gateway 提交 completed，失败提交 failed；回调不重复提交终结 |
| 一次工具调用 | Dispatcher | started 先提交；Handler 执行文件/网络动作并保存 I/O 事实；Dispatcher 通过 ToolOutcomeWriter 提交工具结果及计划/产物视图 |
| 一次 Run | Supervisor | 执行停止 → 打包上传工作目录 → FinishWriter 提交快照引用、run.finished、usage 与占用释放；失败不能假报快照成功 |

`write_file` 先记录意图及输入，在会话目录锁内校验 expected_sha256 并写临时文件、同目录 rename 替换；执行结果经 Dispatcher 记录。PreparedToolResult 名称只表示待登记的结果，不表示文件动作尚未发生。文件操作确认完成时 side_effect=committed，表示实际动作已完成，不代表已上传目录快照；快照持久状态由独立 workspace_snapshot_id 表达。代码执行可包含多个文件及网络动作，失败可以已有部分效果；不得宣称多动作整体回滚。结果提交未知先查 operation_id，缺证据记 unknown，禁止自动重做。计划及产物登记仍由可信 reducer 与工具结果在同一 SQL 事务维护。

control 的可变状态按 space_id/run_id 隔离，同一 Run 的 Executor、Planner 间接触发的模型调用及 Dispatcher 共用同一控制实例；进程重启不恢复执行中的 Run。

control 对每次实际模型 attempt（包含摘要和重试）及工具执行做一次准入/预算预留，由 Gateway/Dispatcher 在动作前调用，Executor.Check 只检查取消、时限等停止条件，不再次扣费。保留输入估算、输出上限预留和已知 usage 的区别；缺失用量按既定保守策略限制后续调用，计费视图仍显示 unknown。用量结算从已提交事件按 attempt/operation 去重，后续 model.usage.reported 替换统计而非累加第二遍。T07 的 token 容量估算不直接改变计费记录。

Runtime 拆包本身不增加第三方依赖：调度/监督/控制使用 context、sync、time；Planner/Projector 使用已有日志、内容、计数与渲染接口；Schema 校验留在工具侧；MariaDB、TOS、Eino 仍由现有适配器接入。扩展工具另采用 T09 的路径匹配与嵌入式计算依赖；详细算法和协议分别以 T05、T07—T11 为准。

## T07 M07 确定性上下文投影、预算与缓存

### T07.1 唯一来源与职责

输入来源为 SessionJournal 截至确定序号的已提交事件及其不可变 content_objects。ContextProjector 不再分别向草稿、消息视图、运行内存和工具模块索要当前内容。当前用户输入也必须先成为日志事件。模型对应计数器和已固定的策略/配置属于显式输入；实时授权在调用前单独检查，不能被历史日志替代。

ContextPlanner 负责预算规划、触发摘要和选择 context_epoch，并将投影内容与事件保存后返回；ContextProjector 负责从固定事实确定性地产生模型消息、工具定义与 ContextManifest。投影是只读转换，不能隐式调用模型、读取当前时间、执行工具或产生新摘要。日志索引和有界缓存可加速读取，但都需要匹配来源边界和版本。

### T07.2 输入契约与投影规则

```text
ProjectContext(
  space_id, session_id, run_id, through_sequence,
  purpose, context_epoch, base_projection_ref,
  projection_policy_version, renderer_version, parser_version,
  effective_config_ref, summary_refs
) -> messages, tools, manifest, content_digest

ContextPlanner 保存上述内容并追加 context.projected
  -> projection_id, messages_ref, tools_ref, manifest_ref
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
| tool.execution.input_resolved / tool.io.recorded / workspace.snapshot.created / workspace.snapshot.failed / plan.updated / artifact.published | 审计/业务事实；对应工具结果已经进入对话，不重复加入，也不读取当前计划视图 |
| workspace.restored | 正常恢复只审计；若从中断/快照失败回退到旧快照，追加一次带事件来源的文件状态说明，标明哪些 Run 的未保存改动不保证存在，不能修改旧工具事实或旧投影 |
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

context.projected 自身序号晚于 through_sequence；实际 model.request.prepared 引用 projection_id 和确切请求体。为了审计，保留最终逻辑输入与实际请求，不能只依赖未来重新执行旧版本代码。内容缺失或损坏时明确标记不能完整重建；首版不设置历史过期策略。

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

### T07.7 D04 的最小投影实现范围

Projector 只读固定 through_sequence 的统一日志及引用对象，不读草稿/页面视图，不依赖 Gateway。保留原生 Eino 消息协议字段，按有序指令及准确工具定义重建完整交互；失败/取消回复不当成功历史复用，已确认效果和未知操作保留显式提示。缺失工具结果配对直接拒绝。

计数器暂为 `utf8-byte-upper-bound-v1`：对完整消息和工具的序列化字节加固定协议余量做保守容量检查，renderer=mass-context-v1、parser=1、epoch=1。指纹包含定义摘要、当前有效模型参数摘要、工具、版本和准确来源事件。不是供应商精确 tokenizer；因此可能提前拒绝容量，绝不静默截断或隐式摘要。Planner 保存完整投影再登记 context.projected，D06 扩展摘要与缓存策略。

control 每次模型 attempt 和工具调用只预留一次；已知 input/output 按实际值结算，未知仍保留上界，统计显示 null 而非 0。同一结算重复不累加、不同内容拒绝；当前调用/工具/Token 总额由 max_steps 及输入/输出限额推导，实际用量超预算明确停止。

### T07.8 D06 实现与边界

D06 使用 `stage-summary-v1` / `mass-context-v2` / parser 1，保留 `utf8-byte-upper-bound-v2` 保守计数。输入为固定水位日志、不可变定义、Run 有效模型配置、准确工具顺序及显式摘要引用。Manifest 保存半开消息区间的 ordered_block_refs、来源事件/对象摘要、选取及省略原因、有效配置事件、父投影、epoch 和内容指纹。显式 Build 不访问模型、草稿、消息视图或时钟；未知事件/语义版本、日志缺口与缺失/损坏对象明确失败。

完整助手响应与该响应的所有工具结果为一个不可拆单元，已闭合的失败 Run 交互仍保留。失败/取消后的未闭合组整体转换为中断说明，携带终结结果和 I/O 确认/未知效果的来源，缺失结果不当成功；正常工作目录恢复只审计，回退到未保存运行之前的快照才增加文件状态说明。原生消息协议字段继续保留。工具结果超过 4096 字节时固定节选 data 的 UTF-8 前缀，并保留 status、error、side_effect、complete 与原始内容引用；投影对象固定节选字节，原文保持完整。

`compression=summary` 在估计输入达到预算 85% 时压缩完整旧轮次；尽可能保留最近旧轮及当前请求，摘要输入自身也受容量约束，必要时在旧轮边界缩小范围。摘要使用同一 Gateway/Control，purpose=summary、无工具、不递归压缩，消耗 Run 的模型次数与 Token 配额。摘要及其来源区块、递归 previous_summary_ref、覆盖水位和生成响应持久化后，任务投影仍须验证容量，并满足不超过预算 75% 且小于原输入才激活新 epoch。无法腾出空间、关键输入过大或摘要失败会明确终止。摘要、流片段、重试失败和最终回复视图不会重复成为任务消息。

只有已提交的 task 投影是阶段检查点；单独保存的摘要或 summary 投影不激活阶段。后续调用使用同一已保存摘要；模型/工具/语义配置改变另开 epoch 并说明原因。任务和摘要有各自父链。新增 schema 7 的 context_projections 索引与日志同事务，可离线重建；迁移回填旧投影而不改写旧对象。旧 Session 未固定 compression 的定义保持 reject，新 Session 才采用新配置。

Projector 的不可变字节读取缓存每实例最多 128 项 / 8 MiB，单项最大 1 MiB；缓存只复用按空间、对象 ID 和 SHA 标识的原始字节，语义投影始终按水位、版本、配置及摘要引用重建，每次缓存读取重新检查当前授权。统一日志读取现有 20000 事件上限、递归摘要校验最多 64 层，超限明确失败，不自动清理历史。

检查 API `GET /api/v1/spaces/{space_id}/runs/{run_id}/contexts/{projection_id}` 返回固定投影、采用的源事件、摘要及实际请求引用；界面在 Trace 的 context.projected 下提供入口。来源事件及原始载荷继续走空间授权与下载校验。阶段前缀稳定只作平台诊断，D06 未据此声明供应商缓存命中；真实模型工具/摘要与缓存测量仍在 D07。


## T08 M08 基于 Eino 的 LLM 接入与完整 Trace

### T08.1 框架与模块边界

统一采用 `github.com/cloudwego/eino` 的 model/schema 接口及 `eino-ext` 模型组件。MASS 的 M08 包装 Eino，M06 负责执行与工具循环，M07 管理上下文，M09 保留工具权限和 会话工作目录访问边界。Eino 是进程内框架，不额外启动 LLM 服务，也不代替实际模型 endpoint 与凭证。

首版使用 `model.ToolCallingChatModel`、`schema.Message`、`schema.ToolInfo`，通过 `Generate`/`Stream` 接入实际模型。工具用 `WithTools` 绑定到当前调用实例，避免并发共享模型上原地修改工具集合。模型能力和协议信息通过组件适配，不丢弃继续对话所需字段。[Eino 模型接口](https://github.com/cloudwego/eino/blob/main/components/model/interface.go)

Eino ADK/Graph 可以后续承接更复杂编排，首版不同时启用两个完整 Agent 循环。所有自有组件仍采用 Eino 的模型接口进行真调用和 FakeChatModel 测试，不能以另写一套 HTTP 模型服务替代 Eino。

### T08.2 核心调用代码

以下片段位于 internal/adapter/eino，演示 Eino 流的单一消费路径。业务 DTO 转换、授权、预算、完整性判定及 SQL/TOS 事务在网关外层。`record` 确认对应原文区块已持久化，并维护可重建的语义增量视图，保存失败必须返回错误；它不把每个增量另写成一条完整助手消息。聚合与校验完成后由网关统一追加 model.response.completed。引用的框架 API 以初始化时锁定版本为准：

```go
package eino

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

Eino 的选择不自动确定模型供应商。首个组件固定为 `github.com/cloudwego/eino-ext/components/model/ark` v0.1.71，实际 model/endpoint 由部署配置确认；若使用其他 Eino 组件，必须同样支持自定义 HTTP 捕获和完整协议数据。

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

### T08.5 D04 Gateway 与真实传输

生产采用 adapter/eino.ModelTransport，测试可注入实现 Eino ToolCallingChatModel 的 FakeChatModel，并明确 source。实际 SDK 配置 RetryTimes=0、120s HTTP 上限、禁止重定向；请求最多 4 MiB、响应最多 8 MiB。HTTP 捕获在正文消费前将实际请求/响应字节写入 TOS 并登记 wire 事实，不记录授权头或密钥；无法保存原文或语义事件立即停止消费。

Gateway 统一接受显式 purpose=task/summary 的 Prepared 输入，summary 必须禁用工具；D04 仅编排 task。每个 attempt 先记录固定 projection、message、purpose、来源及 credential revision。完整聚合必须包含 SSE 结束标记和合法 finish_reason；工具参数必须为完整 JSON、单次至多 16 个调用，部分输出/截断不升级为 completed。最终完整回复对象保存 Eino Native 字段、原始 usage 和准确 response_event_id；工具只接受该完成事件中的调用 ID、名称及参数摘要。用量缺失与明确 0 分开处理，缓存字段不可用时为未知。本阶段没有自动模型重试、摘要调用或缓存命中推断。

### T08.6 D07 实现进展

每个新 Run 固定 `model_attempts`，由部署配置 `llm.max_attempts`（1—3，省略为 1）提供。SDK RetryTimes 仍显式为 0；平台仅在未输出文本、失败与用量结算已经持久化、状态为 HTTP 429/502/503/504 时，按 250ms/500ms 有界等待重试。每次尝试独立 attempt/message、retry_of、凭证 revision、原文和预算；重试复用同一固定投影。部分输出、协议错误、取消、存储失败不自动重试。Journal reducer 校验 retry_of 为同一 Run/投影/用途的失败尝试。

每个完成/失败响应附 observation，保存耗时、首个模型文本片段延迟（无文本为 null）、HTTP 状态（尚无响应为 0）与组件版本。响应捕获对空非成功正文也保存状态；错误 JSON 可使解析提前停止，只保留已实际接收的字节前缀，不为填满原文而无界继续消费。失败流从已保存原文恢复实际 usage，null usage 帧不覆盖此前统计。请求及失败响应正文仍以同一 TOS/Journal 路径保存，不保存认证头。

MeasureCache 单独统计 attempts、可用 cache_read 覆盖数、命中数及同口径 input/cache 配对数；零分母返回 null。缓存输入占比只使用配对完整的样本，未知不补 0。真实冒烟最多 8 次模型请求，报告按 task/summary 及 initial/append/history_compressed 分组保留实际 usage 与耗时；真实验证尚待授权执行，不能以单元统计测试或前缀比较代替云端命中证据。

D07 回归发现 D06 的 SummaryAttempts 只留在内存、跨事件批次时丢失。现在该内部标记随 agui_streams 检查点持久化，HTTP/AG-UI State 不暴露内部字段，防止摘要片段成为任务助手输出；兼容读取旧状态 JSON。

## T09 M09 内置工具、计算与产物交付

### T09.1 工具目录与版本

MVP 固定提供下列 14 个平台内置工具。每个 Agent 选择授权子集；名称、实现版本、参数/结果 Schema、限额策略与代码引擎/辅助函数版本固定到 Run 快照，不能在执行中解析 latest。Registry 在创建 Run 时校验版本可用，版本缺失明确拒绝；Skill 内容不能自动注册工具或放宽权限。

| 工具 | 主要参数 | 结果与语义 |
| --- | --- | --- |
| list_files | directory、cursor、limit | 当前会话真实目录条目、类型、大小及下一页 |
| read_file | path、可选 expected_sha256、range | 读取当前文件，返回摘要、范围、不可变内容引用和截断状态；可连续分段读取 |
| write_file | path、content、media_type、expected_sha256 | 创建或整体更新单文件；新建使用 missing，更新匹配摘要；返回新摘要 |
| edit_file | path、expected_sha256、edits[{old_text,new_text}] | UTF-8 精确替换，全部匹配成功才替换该文件 |
| glob_files | pattern、cursor、limit | 按逻辑路径模式查找文件，如 reports/**/*.csv；返回版本及范围完整性 |
| search_files | pattern、mode、path_glob、cursor、limit | mode=literal/regex，返回路径、sha256、行号和片段；不跨工作区搜索 |
| move_file | source、target、expected_sha256 | 单文件移动/重命名；目标存在即冲突，MVP 不提供覆盖目标选项 |
| delete_file | path、expected_sha256 | 删除当前单文件；不递归删除，不删除已有快照或 TOS 原文 |
| list_skill_resources | skill_binding_id、prefix、cursor、limit | 绑定版本的资源清单、类型、大小及可用说明 |
| read_skill_resource | skill_binding_id、path、range | 固定 SkillVersion 的只读内容、版本及来源 |
| read_attachment | attachment_id、range、format | 读取本会话已接受的附件；返回原文引用、解析方式/版本、范围和完整性 |
| run_code | language、code、input | JavaScript 计算、当前目录文件操作及网络请求；返回 JSON、日志/I/O 引用和已发生效果 |
| update_plan | expected_revision、steps[{id,title,status}] | 当前 Run 的完整计划及新 revision；status=pending/in_progress/completed/skipped |
| publish_artifact | path、expected_sha256、display_name、media_type | 固定文件版本为产物，返回 artifact_id 和本服务鉴权下载入口 |

`range` 为互斥的行范围或字节范围；文本行号从 1 开始，字节范围为 offset/length，服务器施加上限。二进制只能返回受限编码片段/内容引用，不能伪装成文本；非法编码明确失败。结构化 format=json/csv 在输入上限内先验证完整文档，再返回有界记录；不能把截断的字节片段冒充完整 JSON/CSV。read_attachment 首版解释 UTF-8 文本、JSON、CSV，其他格式返回 unsupported_media_type；PDF、Office、图片识别不属于首版承诺。SKILL.md 自动加载，其他资源通过工具按需读取；脚本作为资源可读，但不能因此自动执行。

### T09.2 统一授权、结果与资源范围

所有工具先校验 Run 主体的当前空间归属、Agent 工具允许列表、准确版本、参数、预算与取消状态，再执行。参数 JSON 只解码一次，禁止额外 URL 解码。工具定义还包含超时、输入/输出大小、扫描限额及允许的副作用类型；服务上限优先，调用者不能自行提高。

SessionWorkspace 按 `(space_id, session_id)` 绑定真实临时目录，文件参数只允许目录内相对路径；Skill 从固定绑定清单读取；附件必须由同 Session 的已提交 user.message.accepted 引用。上传成功但尚未被接受的附件不是模型可用资源。用户输入事件保存附件清单，使模型知道可用 attachment_id、名称、类型和大小。同会话 Run 连续共享文件；其他 Session 的目录不可直接访问，新会话只允许显式恢复授权快照的独立副本。

调用者不能传 bucket、TOS key、预签名 URL 或其他空间 ID。服务端解析的不可变引用进入 source_refs；每次读取复核授权、对象可用性与摘要，不以“知道 object_id”作为权限。代码资源也遵守同样规则。

工具结果统一包含 `status`、`data_ref`、有界 `preview`、`source_refs`、`truncated`、`complete`、`error`、`side_effect`；分页结果另含 `next_cursor` 和扫描范围。source_refs 记录对象摘要、文件 sha256、快照来源、SkillVersion 或附件绑定事件。error 包含稳定 code 及可恢复性，side_effect=none/committed/unknown；committed 表示已确认实际效果，目录快照是否持久保存另行表示。超限或缺失不得返回貌似完整的空结果；完整原始结果保存 TOS，界面和模型节选明确标记。

PreparedToolResult 是内部准备结果，包含内容引用、已发生效果及类型化登记意图；Handler 不能自行写业务视图、发终结事件或调用其他工具。Dispatcher 通过 Journal 提交 tool.execution.completed/failed 与必要业务事件、视图及幂等结果。失败或副作用未知时遵循 T06/T10，禁止自动重做未知写入。

逻辑路径校验保留如下核心规则，目录参数另允许规范化的根目录标识；不允许绝对路径、符号链接、硬链接或特殊文件。以下仅是逻辑校验片段，实现还必须校验所有路径分量及根目录归属，实际文件操作统一通过 SessionWorkspace；只做字符串前缀比较不够。快照恢复由服务执行受控解包（T11），不提供任意上传归档自动解包工具：

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

### T09.3 文件发现、读取与修改

SessionWorkspace 由服务创建专属目录（服务生成 ID、目录权限 0700），Agent 只看到相对路径。同 Session 一次仅一个 Run，所有文件工具与代码宿主函数共享目录锁；禁止 Agent 创建链接或访问目录外路径，不向 VM 暴露底层 Go 文件句柄。文件名称使用正斜杠规范，平台不承诺大小写不敏感宿主能够同时保存仅大小写不同的文件名，遇到冲突明确报错。

文件读取不要求预声明：read_file 和 fs.readFile 可在循环内连续读取。每次结果保存不可变内容引用、实际范围及 sha256 后才能进入模型结果；分段读取用 expected_sha256 绑定同一内容，文件变化返回 conflict，不能静默拼接不同版本。读取操作返回的完整原始结果持续保留，后续投影不重新读取当前目录。

write_file/edit_file 在目录锁内校验 expected_sha256；新建使用 missing，更新要求当前完整文件摘要匹配。edit_file 仅支持有界 UTF-8 文本，每个 old_text 必须非空、恰好出现一次且区间不重叠，全部匹配后逆序替换，保留未修改内容及换行。写入通过同目录临时文件及 rename 完成单文件替换；移动只允许未占用目标；删除只删除当前单文件。临时文件不纳入快照；不对多个文件或文件与日志提供共同事务。代码宿主函数执行同样的校验与目录锁规则。

glob_files 使用 [doublestar.Match](https://github.com/bmatcuk/doublestar#match) 匹配目录枚举得到的相对路径；search_files 使用 strings/regexp，限制表达式、文件数量、扫描字节、行长及结果数量。枚举和扫描只遍历当前 Session 的目录，不新增搜索服务。

分页查询首次在目录锁内取得有界清单，搜索片段/候选摘要保存成不可变工具输出；游标绑定该输出、查询摘要、空间/Session 与位置，后续页读取相同输出并再次鉴权。超限标记 complete=false 和覆盖范围，不声称扫描了整个目录。list/glob 的返回清单不等于未来文件内容固定；再次 read_file 时以摘要检查并保存当次实际读取结果。

### T09.4 run_code：会话文件与网络能力

采用纯 Go [goja](https://github.com/dop251/goja)，adapter/javascript 实现 CodeRunner，经 bootstrap 注入 SessionWorkspace、NetworkClient 和受限 I/O 记录接口。每次调用使用独立 VM 和单一执行 goroutine；取消/超时发 Interrupt，等待实际退出后释放名额。输入为 language=javascript、同步函数体 code 与 JSON input；不要求预先声明资源/输出清单，文件直接在当前 Session 目录读写。启用 run_code 即授权下面的文件和网络能力，Agent 编辑页须明确展示这一范围；不要求为了调用 fs 再逐项启用同名文件工具。

| 宿主 API | 能力和约束 |
| --- | --- |
| fs.list / fs.readFile / fs.mkdir | 当前工作目录下枚举、读取和建目录；读取支持范围与 expected_sha256，返回摘要与内容 |
| fs.writeFile / fs.editFile / fs.rename / fs.remove | 采用 T09.3 的摘要检查；单文件即时生效，不自动回滚；不支持链接或任意宿主路径 |
| net.request({method,url,headers,body}) | 同步 HTTP(S)，返回 status、受控 headers、body；允许读取及有副作用请求，非 2xx 明确返回状态 |
| log、CSV 解析/编码、JSON | 有界日志及数据转换；不允许把对象无界导出 |

NetworkClient 使用 net/http，绑定 Run/tool context 和调用超时，限制请求次数、请求/响应体、重定向次数；每次发送和重定向前重新授权，不自动重试，不将服务配置、模型凭证、Cookie 或环境变量注入请求。仅允许 http/https，拒绝 URL userinfo；跨源重定向不转发认证头，Trace 对认证头及秘密查询参数脱敏并注明，保存允许范围内实际请求/响应体和状态。网络访问是 run_code 的内置能力，不新增独立自定义 HTTP、MCP、浏览器或搜索工具。

不暴露 Go 对象、反射、环境变量、进程启动、Node require/process、动态包安装、其他工具入口或跨调用 VM 状态；不支持 Promise/后台异步任务。禁用 goja 默认外部 source map 加载路径，全部编译入口使用关闭 source map 的解析配置，避免绕过显式文件和网络接口。文件工具与网络宿主函数每次检查授权、取消和容量，使用相同服务范围；传入的用户文件和响应内容不是可提升权限的指令。

示例中的文件由前序步骤写入当前目录：

```javascript
const source = fs.readFile("records.json");
const rows = JSON.parse(source.content);
const total = rows.reduce((sum, row) => sum + Number(row.amount), 0);
fs.writeFile("summary.json", JSON.stringify({ total }), { expected_sha256: "missing" });
log({ count: rows.length });
return { total, path: "summary.json" };
```

执行顺序：持久化 tool.execution.started（原始代码/input）→ 保存 tool.execution.input_resolved（Session、工作目录逻辑标识、起始快照）→ 启动 VM → 每次宿主 I/O 先保存 prepared 意图，再操作、保存 completed/failed 的输入/结果和实际效果 → 保存工具终结结果。I/O 记录共享 operation_id，使用递增 io_sequence 和 phase，由 tool.io.recorded 表达；不另建日志库。读取文件/网络得到的数据保存不可变引用后才返回 VM，写入意图保存目标及内容引用。文件或请求已经发生而记录失败时停止代码，缺证据为 unknown，不自动重放。

工具返回有界 JSON 值、日志及 I/O 引用，拒绝循环引用、函数、Promise、超深结构和不可表达数字。代码长度、文件/网络调用次数、读取/写入/返回总量、日志与耗时都计限额；CSV 和 Go 宿主 I/O 持续检查 context。代码异常、取消或序列化失败不撤销此前文件修改/外部请求，结果明确 none/committed/unknown 及已完成操作，Run 收尾仍保存工作目录快照。需要交付下载文件时使用 publish_artifact。

此方案没有沙箱，不提供每次 VM 的硬内存隔离；Interrupt 不能替代 Go 宿主函数的取消检查，也不能保证终止所有引擎内部操作。服务等待实际退出，无法退出则随进程终止并按中断处理，不能提前宣称已取消。MVP 不支持 Shell、Python 或任意 Skill 脚本执行。[goja 中断限制](https://github.com/dop251/goja#interrupting)、[解析器配置](https://pkg.go.dev/github.com/dop251/goja/parser)

### T09.5 计划与产物

`update_plan` 操作当前 Run 的计划，初始 expected_revision=0。每次提交完整有界步骤清单，步骤 ID 唯一、最多一个 in_progress；既有 ID 保持语义，调整清单记录新 revision。标题和状态是任务数据，不能变成系统指令。Journal 同事务追加 plan.updated 与 tool.execution.completed、更新 run_plans；工具结果返回完整新计划，前端可以独立展示。计划不触发新 Run、不自动派发工具，也不以步骤全部 completed 代替 Run 成功；它是 Agent 报告的进度，不是独立验收结论。

`publish_artifact` 在会话目录锁内校验 expected_sha256，复制当前文件为不可变内容并上传 TOS；确认 ready 后，在同一事务追加 artifact.published 与 tool.execution.completed、建立 artifact 记录和保留引用。产物保存 object_id/hash、来源 Session/path/sha256、名称与媒体类型；不只保存可变路径或临时 URL。返回本服务 `/api/v1/spaces/{space_id}/artifacts/{id}/download`，下载仍检查当前成员权限；媒体类型验证并按附件下载，不把生成内容作为服务页面执行。源文件后续修改、移动、删除不改变已交付内容。重复 operation 返回同一产物，不重复登记；新的显式发布可以产生新的产物版本。

计划和产物专用事件服务于审计及界面视图，不重复加入 LLM 对话；模型读取对应 tool 结果一次。摘要保留任务所需的计划状态、产物 ID 和来源，仍只基于固定日志边界，不从 run_plans/artifacts 当前视图反向组装上下文。

### T09.6 D05 实现约定

D05 延续三个 v1 工具的兼容参数，新增 range、分页及其余十一种 v1 工具；Run 中已保存的 Schema/描述保持固定。结果 Schema 与实现/策略版本一起写入新 Run；旧 Run 缺少新增字段仍能查询和恢复。新增迁移 00006_tools，既有迁移不改写。

文件读取完整校验摘要并保存不可变来源，返回上限 64 KiB 的 UTF-8 或 base64 范围；JSON/CSV 附件先在 8 MiB 内完整验证，再通过 record_offset/record_limit 续读结构化记录；text 的 range 保持字节/行语义，二者不混用。列表/搜索首次有界扫描后保存 tool.query 对象，游标签名绑定主体、空间、Session、查询与位置，后续页读取同一对象；扫描上限显式给出 complete=false。查询至多扫描 min(storage.max_entries, 10000) 个条目和 8 MiB 文件，保存至多 1000 项；正则至多 512 字节，行至多 64 KiB、片段至多 1024 字节，每页 1—100 项，完整查询对象至多 4 MiB。文件工具写入上限延续 1 MiB，目录采用已有 storage 限额；编辑以原始文本唯一、不重叠区间为准。

skill_binding_id 使用固定绑定的 SkillVersion ID，资源从该不可变版本清单解析；Run 事件不重复内联完整资源清单，读取时依据 session.created 的准确版本引用和摘要取得，避免大清单突破事件容量。附件上传先保存本 Session 元数据，再由 user.message.accepted 的准确清单与对象引用接受；上传成功本身不授予模型读取权。附件被接受后，原始字节可从历史消息下载，工具仅支持 UTF-8 text/JSON/CSV，并记录解析版本。

run_code 使用同步函数体、单 VM/单 goroutine，与工具 context、全局代码并发名额及实际退出等待绑定。默认工具限额沿用 tools.code；每次 I/O 先保存脱敏意图，再执行，再保存结果，记录失败停止后续代码。关闭外部 source map，文件操作共享 FileService 校验；HTTP 不自动重试、最多 3 次重定向并逐次复核授权。代码异常保留已经确认的写入/请求；日志、返回值和完整性单独报告，不声称生产沙箱或硬内存隔离。

计划变更与工具终结同事务，产物继续保存独立字节。Manifest 可选 runtime_state 保存该 Run 的计划/产物及其准确来源水位；水位采用最后一条相关业务事实，不因随后取消或收尾审计而变化，从而保持未知提交/快照重试的稳定对象摘要。旧 Manifest 仍按格式 1 恢复，跨会话复制只复制文件。

代码源码、JSON input、宿主参数及完整文件读取共同计入 max_input_bytes；文件读取在打开后以实际 stat 大小预留容量，范围预览仍对完整来源计费。文件/网络返回共同计入宿主输出限额，VM 返回值与日志另有界限。源码最多 64 KiB、JSON 参数最多 2 MiB，完整参数保存在 model.response 对象；响应事件仅内联有界参数、保存参数摘要。平台生成独立调用 ID，保留 native_id 供 Eino 对话配对，供应商跨响应复用调用 ID 不会冲突。大参数通过 Trace 载荷查看，不发出伪造的空参数标准事件。

D05 新投影使用 utf8-byte-upper-bound-v2：工具计数只含发送给模型的名称、描述和输入 Schema；结果 Schema 留在固定定义和校验中，完整指纹仍包含工具契约。旧已保存投影及 v1 计数器记录保留。计划/产物业务事件不额外加入模型消息，只有对应工具结果进入对话。


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
| tool.execution.input_resolved | run_code 的会话工作目录逻辑标识和起始快照已确认保存，属于工具原始输入，不单独作为对话消息 |
| tool.io.recorded | 一次代码文件/网络 I/O 的 prepared/completed/failed 及原文引用、效果；tool 结果按需引用，不另当一条对话消息 |
| workspace.snapshot.created / workspace.restored | 目录快照及 URI 已入库 / 已从指定快照恢复；用于文件恢复与审计；异常回退的恢复说明按 T07 投影，不注入归档正文 |
| workspace.snapshot.failed | 目录快照未确认保存，记录失败原因及最后可恢复快照 |
| tool.execution.completed | 工具结果及允许的业务变更已确认保存，提供配对结果 |
| plan.updated / artifact.published | 计划新版本 / 文件版本已登记为产物；与工具完成同事务，审计/UI 使用，不再重复进入模型输入 |
| tool.execution.failed | 错误、拒绝、取消或未执行等已确认，保留 side_effect 和错误结果 |
| context.summary.created | 摘要、准确来源事件及生成调用已保存，供明确选择后投影 |
| run.cancel.requested / run.finished | 请求取消 / 实际终结；最终回复引用已有响应 |
| operation.reconciled | 中断恢复时对遗留调用追加核实结论；未知保持 unknown，不追写假成功 |
| content.unavailable | 发现对象缺失或损坏，记录受影响引用；不修改历史事件 |

事件类型、actor 和作用域由服务端可信执行路径赋值，外部文本不能直接成为可信事件。事件是否参与上下文由版本化投影器决定，不接受客户端提供 include_in_prompt 开关。purpose=task/summary、attempt_id、projection_id 等由模型事件 data 携带；Run/span 父子关系可用于 Trace 展示。

plan.updated/artifact.published 与对应工具终结组成一个 Append 批次，批次使用稳定 append_key 和事件 ID；业务事件以 tool.execution.started 为因果来源并共享 operation_id，operation 的唯一终结约束仅作用于工具 completed/failed，不误将业务事件计为第二次完成。run_plans、artifacts 和 workspace_snapshots 索引均由可信 reducer 维护；工作目录由 SessionWorkspace 操作，不以数据库视图替代真实文件。

每个模型 attempt 只允许一个终结事实 completed 或 failed；同一 operation 的重复完成按幂等规则处理，不能选择两份响应同时进入上下文。协议完整性同时检查结束原因、工具参数、配对所需标识及必要状态。终结后追加的核实或用量补充是独立事实，不原地改写原事件。

工具 side_effect 明确为 none、committed 或 unknown；failed 不等于没有副作用。本地文件/网络动作与 tool.execution.completed 不在共同事务中，已发生效果不会因日志失败回滚；目录快照单独记录持久状态。执行完成但记录前崩溃时，恢复追加 operation.reconciled；缺证据保持 unknown，不能依据只有 started 推断未执行，也不自动重放。

原始 HTTP 字节区块可以跨协议帧，解析器负责帧组装。大内容按有界区块写 TOS，成功后提交事件引用；尚未完成的 multipart 不能作为已可读区块。完成标识需全部已收到内容保存且协议完整；崩溃前未持久化尾部明确 incomplete。认证秘密不保存，必要脱敏记录规则和位置，不能声称脱敏后的内容为逐字原文。

### T10.2 追加、事务与恢复

唯一写入口为 Journal.Append，调用者提供同一逻辑写入重试时稳定的 event_id 或 append_key；服务端校验事件 schema、权限、操作状态、同空间引用和 ready 对象。权限失效后的结果、快照及终态使用 T01 限定的系统收尾身份，保留原 actor 关联，不再次要求已撤销的主体授权。使用 event_digest 比较类型、因果字段、data 和有序内容摘要，排除提交序号/记录时间；同 ID 同内容返回已提交结果，不同内容返回冲突。

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

视图重建固定 reducer_version，读取已有事实，不触发模型/工具。重建结果与线上视图对比，覆盖失败、取消、重复追加和未知副作用；完整重建依赖完好的内容对象。MVP 不自动过期或删除历史；发现对象缺失/损坏则追加 content.unavailable 并明确不可重建范围。

usage 按 attempt 记录并汇总，摘要和失败尝试均计入；缺失用量为 unknown，价格估算保存价格版本。缓存统计口径见 T07.6。运维日志不复制原始用户内容，空间授权同样用于日志和载荷读取。

### T10.2.1 D01 已落地的事务基础

新增迁移 00003_storage_journal，保留 D02 前两份迁移不变。空间授权锁继续使用 D02 的空间行；其后统一按 Session → Run → 对象/索引顺序操作。授权和查询都经现有 MariaDB Repository，成员撤销后不复用旧权限。所有外部对象 I/O 均在 SQL 事务之外完成。

D01 注册 schema_version=1 的最小事件子集：session.created、run.created、run.started、run.finished、workspace.snapshot.created/failed、workspace.restored、operation.reconciled。未知类型/版本在追加和读取时明确拒绝；其他 T10.1 事件随所属阶段增加。事件由内部可信 Go 调用产生，HTTP 不提供任意 Append 或视图写入接口。D01 入口只接受当前成员鉴权的 user actor；D04 新增绑定已认领 Run 的窄系统收尾能力（T05.1），普通 Journal Repository 仍拒绝 system actor。

一次最多 100 个事件、每个 data 最多 64 KiB、每个事件最多 100 个对象引用；data 规范化保留 JSON 数字精度，引用按 role/ordinal 排序，occurred_at 统一 UTC 微秒。摘要排除服务器序号与记录时间，包含事件 ID、类型、版本、因果/操作/关联、actor、发生时间、data 和有序引用。调用者须保留事件 ID、发生时间及 append_key 重试；幂等核实先于 expected_sequence，部分重复批次拒绝，不把新事件拼到已存在批次。对来自已执行动作的结果使用不带 expected_sequence 的稳定追加，避免期间别的事件造成结果丢失。

基础 reducer 同事务创建 Session、排队/开始/终结 Run 视图和唯一占用；成功终结必须已有该 Run 的已提交快照，运行中无快照的失败终结留下 workspace_error 并阻止新 Run。快照索引和终结可置于同一 Append 批次，释放占用失败时所有视图和事件共同回滚。D04 在同一事务基础上补齐 Claim/Finish、取消、用量、模型/工具来源校验、快照失败收尾及离线重建。

读取返回 events、through_sequence、next_after、has_more；after_sequence 和 through_sequence 是 Session 日志十进制字符串，不复用 AG-UI 游标。首次分页固定已提交水位，随后带回同一个 through_sequence。日志、快照元数据和载荷的 HTTP 读接口继续复用 D02 Cookie/成员检查；载荷只对已被提交事件引用的 ready 对象开放，下载用 attachment/octet-stream，开始前验证完整摘要、传输期间分块复核身份与成员。

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
| tool.io.recorded / workspace.snapshot.created / workspace.snapshot.failed / workspace.restored | CUSTOM mass.trace.v1 | 展示 I/O 和目录快照来源/状态，不能当作对话或运行终态；无 run_id 的恢复事实仅在 Session 查询展示 |
| tool.execution.completed | TOOL_CALL_RESULT | messageId、toolCallId、role=tool、content 为字符串 |
| 可恢复工具业务错误 | TOOL_CALL_RESULT + CUSTOM mass.tool.status.v1 | content 中明确错误结构，不伪装成功；最终是否终止由 Run 状态决定 |
| 未知副作用、未执行或协议不完整 | CUSTOM mass.tool.status.v1 / mass.message.status.v1 | 明确 unknown/incomplete/not_executed，不能补造成功结果 |
| 投影、摘要、模型 attempt 状态 | CUSTOM mass.context.v1 / mass.trace.v1 | 提供来源 ID、状态及载荷引用，不把摘要任务输出当作用户回复 |
| plan.updated / artifact.published | CUSTOM mass.plan.v1 / mass.artifact.v1 | 带版本与来源事件的计划状态/产物引用；不替代 TOOL_CALL_RESULT，加入 UI 快照 |
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

游标、已应用界面状态和协议版本必须配套。页面刷新时若只有游标而没有对应状态，应从 0 回放；大历史可调用 GET /runs/{id}/ui-snapshot 获取同一水位的 reducerState、cursor、protocolProfile，再从该 cursor 续传。快照需要包含尚未结束的消息/工具状态以及同水位的计划 revision 和产物清单。MVP 不过期或裁剪 SSE 历史；非法游标返回 invalid_cursor，存储缺失则明确返回历史不可用。不得跳过缺失数据并返回成功。

断线、代理超时和 UI 投影服务错误不改变 Run 状态；客户端查询 Run 并按已应用游标重连。鉴权失败按 T01 关闭连接并停止重连；若 Run 主体也已失去权限，由 Supervisor 以实际权限错误终结 Run，AG-UI 从该终态派生 RUN_ERROR，不由订阅连接伪造。慢客户端有界超时断开，不阻塞执行器；投影延迟显示为同步中。终态提交并投影后，发送终态事件关闭流，客户端停止订阅；已应用终态后无需再连接。终态后的用量补充或核实仍可追加原始日志并通过查询展示，但不能继续向已终结 AG-UI 流追加事件或改写旧帧。定期复核成员与登录状态。

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

### T10.7 D04 AG-UI 与离线视图重建

transport/agui.Projector 使用已锁定 SDK 的构造/验证方法，以 mapper/parser=1 映射 journal。agui_events 使用每 Run 连续 ui_sequence，源事件和 ordinal 唯一；与 agui_streams 的来源水位、UI 状态及游标同事务 CAS 提交。单条事实可生成多条 UI 帧，原始 Session 序号不作为 SSE ID。终态之后不生成新的该 Run 帧。

SSE 从持久化帧发送，支持 canonical 十进制 after/Last-Event-ID，拒绝未来游标及互相冲突的参数；每批/发送期间检查 Cookie 与成员。流不套用普通接口 15s 超时，使用 3s 心跳和每次写入 5s deadline。后台投影不依赖浏览器存活，撤权后仍可通过受限派生视图能力完成终态；这项能力不能追加动作事实。前端从 ui-snapshot 原子状态/游标起步，事件通过 SDK Schema 和纯 reducer 后才推进游标，协议错误或 401/403 停止重连。

`mass rebuild-views` 必须离线取得同一运行锁。先校验事件版本/摘要及全部被引用的 TOS 对象大小/摘要，再在单事务清空并重建消息、attempt、工具、产物、占用和 AG-UI 派生视图，以及 Run 可变状态/Session 快照指针。为保留不可变日志和跨会话快照外键，Run 身份行和不可变快照索引作为锚点保留；心跳等协调数据不当业务事实重放。过程不注入 Gateway/工具，任意损坏导致失败，不接受半份重建结果。

## T11 M11 TOS 持久化、工作区与产物

### T11.1 统一对象存储

采用火山引擎 TOS 官方 Go SDK `github.com/volcengine/ve-tos-golang-sdk/v2/tos`，通过 endpoint、region、bucket 和凭证创建 ClientV2。MASS 中所有需要持久保留的工程资源都由它承接。本地会话目录是执行工作区，TOS 快照才是文件恢复依据；不以本地目录替代失败的持久化操作。[TOS 官方 SDK](https://github.com/volcengine/ve-tos-golang-sdk)

对象 key 由服务生成，例如 `mass/{space_id}/objects/{object_id}`；保存对象类型、SHA-256、字节数、ETag、可选 version_id 与状态。快照索引、引用关系和最新快照指针在 MariaDB 中。不得把 ETag 直接当作内容 SHA-256，也不依赖 TOS Bucket 版本控制替代 Agent/Skill 的业务版本。

| 资源 | 持久化方式 |
| --- | --- |
| AGENTS.md、SKILL.md、模板、版本 assets | 不可变 TOS 对象 + MariaDB 发布版本清单 |
| 用户附件、工具产物 | TOS 对象 + 授权元数据 |
| 会话工作目录 | 本地临时目录连续使用；每个 Run 收尾生成 TOS 全量 tar.gz 快照，MariaDB 保存 URI/摘要/清单引用及来源 |
| 模型实际请求/响应、工具完整大结果 | 有序 TOS 区块 + Trace 清单/完整性状态 |
| 运行、会话、runtime_events、event_objects、空间和权限 | MariaDB；不依赖本地文件 |
| 用户原文、摘要、投影消息/Manifest、结构化模型响应 | TOS 不可变对象 + 事件/投影引用 |

同一不可变对象可被多个事件引用，避免重复复制定义或结果；MVP 不设置自动过期或 GC。普通载荷使用有界流上传，目录快照采用 archive/tar + compress/gzip 打包后上传，允许使用工作目录之外的临时归档文件，上传完成即清理该临时归档。Agent 不可访问临时根目录、其他会话目录或归档中间文件。

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

### T11.3 会话目录与快照提交

resource 管理 SessionWorkspace 与 SnapshotStore，adapter/workspace 用 os/path/filepath、archive/tar、compress/gzip 实现本地操作；adapter/tos 负责对象上传。临时根通过统一配置指定，Session 目录路径由服务生成，不接受模型/用户指定宿主根。工作目录在 Session 存续期间跨 Run 复用，不在每轮结束后删除重建。

最小元数据约定：workspace_snapshots 保存 id、space_id、session_id、source_run_id、base_snapshot_id、object_id、tos_uri、archive_sha256、manifest_ref、format_version、created_at；URI 使用稳定 `tos://bucket/key`，不保存临时签名下载 URL。Manifest 包含规范相对路径、类型（文件/目录）、文件大小及 SHA-256，保存空目录，不保存可执行权限或链接。sessions.latest_workspace_snapshot_id 指向最后确认快照；初始来源快照也通过 session.created 固定记录，Session 字段为派生指针。Run 记录开始时的 base 和结束时的 workspace_snapshot_id。

快照提交按以下顺序执行，MVP 只做全量归档，不引入 Git、增量快照或跨快照去重服务：

1. Supervisor 等待所有执行/I/O 退出，仍持有会话占用，锁定目录，生成 Manifest 和 tar.gz；检查文件数、单文件/目录总字节和收尾超时。
2. 分配唯一对象 ID，记录上传意图；上传 TOS 不覆盖旧 key。确认内容可读、大小及摘要后标 ready；不在 SQL 事务中等待上传。
3. 一个 Journal 事务追加 workspace.snapshot.created，写入快照 URI/索引、最新指针和 run.finished/usage，释放占用。终态返回快照 ID；只有此步成功才代表目录持久化完成。
4. SQL 提交结果未知时先按稳定快照 ID/append_key 核实，不能再次执行 Agent；上传成功而无引用的对象不可见，首版不自动回收。

publish_artifact 和 Trace 原文仍在调用期间单独上传，不等待目录收尾；每次已读内容已保存到日志引用的不可变对象。快照正文不用于模型消息拼装；发生回退时仅按 T07 投影 workspace.restored 的状态说明。ContextProjector 不扫描当前工作目录，即使文件被修改也能重建先前 input。

恢复先鉴权解析快照 ID、检查 TOS 对象和摘要，解压到新建空临时目录；验证全部清单后才绑定 Session 并追加 workspace.restored。逐条拒绝绝对路径、..、重复路径、链接、设备文件及越界项，限制解压大小、文件数和时长，禁止归档覆盖目录外文件；失败删除未绑定的临时副本，不提供部分恢复目录。原 Session 恢复最新快照；新 Session 可显式选择同空间可访问快照并独立复制，不自动继承原对话或执行状态。

### T11.4 失败、保留与恢复边界

正常完成、失败、超时、权限撤销及取消的已执行 Run 均尝试保存当前目录；未开始或目录尚未成功恢复的任务不生成伪快照。快照失败时追加 workspace.snapshot.failed，设置 sessions.workspace_error，保留旧快照指针；原本成功的任务改为 failed/workspace_snapshot_failed，原本失败或取消的任务保留原因并附 snapshot_error。禁止仅因已有最终模型回复就报告整个 Run 成功。

实际执行停止且失败终态事务成功后可释放占用，但 workspace_error 未消除前拒绝新 Run。临时目录仍在时，授权成员可通过 `POST /sessions/{id}/workspace-snapshots/retry` 重试打包上传：占用同一会话锁，只保存当前目录及快照事实、清除错误，不重跑代码、不改写旧 Run 终态；追加事件绑定 source_run_id，终态后的事件仅供 Trace/快照查询，不再续写已关闭的 AG-UI 流。若提交结果未知，先核实原快照 ID，不能并发再保存。数据库无法保存终态时沿用 Supervisor 的故障处理，不能假报结束。

临时目录丢失或进程重启后，只恢复最近已提交快照，明确展示来源及中断/未保存变更可能丢失；恢复成功后清除 workspace_error，不尝试重放工具。没有 Run 完成快照时，恢复 session.created 固定的初始来源快照；初始来源也为空才恢复空目录。workspace.restored 记录实际 snapshot_id、恢复原因和涉及的未完成/未保存 Run，异常回退同时向界面及后续模型投影说明状态（T07）。TOS 不可用或摘要不符时停止恢复并报错，不能静默降级为空目录。系统收尾权限仅能写已发生事实及已有目录快照，不能继续模型/工具执行或向失权用户提供下载。

版本、日志、投影、AG-UI 历史、目录快照和产物持续保留；不增加数据 TTL、过期标记、保留时长配置、自动 GC 或生产对象生命周期删除策略。临时归档/失败解压副本随操作结束清理，已完整保存且无活跃任务的会话目录可在服务退出时清理，这是临时资源释放，不删除远端历史。认证会话有效期和供应商自身缓存有效期是既有协议约束，不属于业务历史过期方案；MVP 不因此增加后台清理系统。

读取私有 Bucket 由 Go 服务鉴权代理，浏览器不获得存储凭证；恢复需要 MariaDB 备份、完整 TOS 对象和解密材料。删除原主机临时目录后仍可读取版本、Trace、产物并恢复最后确认的会话文件；不承诺恢复未保存的进程内状态。

### T11.5 D01 已实现的对象与目录边界

ContentStore 将输入流写到权限受限的临时文件，边写边计数和计算 SHA-256；记录包含唯一 key、媒体类型、长度、摘要和 uploading 状态的意图后，使用锁定的 TOS SDK PutObjectV2/ForbidOverwrite。SDK 隐式重试关闭，重试由稳定 object_id 驱动：已有元数据必须完全一致，再通过完整读回核实已上传字节。Put 响应丢失或禁止覆盖冲突不等于失败/成功，只有读回长度和摘要相同才可将意图置为 ready；SQL 失败保留不可见意图/对象供核实。ETag 不作为 SHA-256。临时文件完成即清理，不作为本地持久 BlobStore。

生产只使用 TOS 适配器；tests/fakes 是测试专用内存替身。对象元数据、事件引用和快照均使用带 space_id 的联合外键。下载必须同时满足当前授权、ready 和已提交 event_objects 引用；未引用的成功上传不会因猜到 UUID 而可见。主数据没有自动 TTL、删除或 GC。新增 ready 探针使用 MariaDB schema 校验和 TOS HeadBucket，单次最多 3 秒、缓存 5 秒；HeadBucket 证明连接/桶级授权，对象读写/不可变性必须由独立真实 smoke 验收，不夸大探针结果。

storage 配置是向后兼容的可选段：max_object_bytes 默认 256 MiB（配置上限 1 GiB）、max_file_bytes 32 MiB、max_workspace_bytes 128 MiB、max_entries 10000；文件/目录上限不超过对象上限，Manifest 上限 4 MiB。SnapshotStore 使用已有 runtime.snapshot_timeout；临时根沿用 workspace_temp_root。生成的 Session 目录名只含经过规范 UUID 校验的 space/session ID，使用 os.Root 与可取消的 Session 目录锁。快照要求调用者先停止所有工作目录写入；D04 Supervisor 统一持有占用并使用相同目录入口。

全量 tar.gz 的 Manifest 包含文件/空目录、规范相对路径、大小和 SHA-256；不保留执行权限、软/硬链接或设备项。恢复先验证格式、数量、总解压量、单文件容量、父目录清单、重复路径和大小写冲突，再解压到未绑定的临时目录；逐文件核验摘要并读完 gzip 校验尾部，全部成功才替换空目标目录。已有非空目录不自动覆盖；恢复失败删除 staging，恢复日志提交失败清理本次新副本。路径与文件接口是应用隔离，不声称对宿主所有者提供 OS 沙箱。

SnapshotStore.Save 接收稳定的快照/归档/Manifest/事件 ID、append_key 和发生时间，可将 run.finished 一同提交。未知提交先按已提交事件核实，成功重试返回原快照，不重新打包已变化的目录；若无提交则用原对象 ID 核实内容。SnapshotBase 防止旧 Run 的重试覆盖较新 Run 或其他活跃占用。Restore 只读取目标 Session 的已提交最新指针，初始 source_snapshot_id 由 session.created 固定；跨 Session 只复制文件，不复制源日志。D04 已补齐终结失败记录、系统收尾权限与后台恢复协调，见 T05.1/T11.6。

### T11.6 D04 文件工具与快照操作

resource.FileService 通过 Session 目录锁和 os.Root 提供 read_file/write_file/publish_artifact 三个 v1 工具。路径必须是合法相对路径，拒绝软链接及非普通文件；本阶段单文件至多 1 MiB UTF-8，写前检查目录总字节/条目上限。写操作要求 missing 或当前 SHA-256，使用同目录临时文件、Sync/rename 后记录实际效果。publish_artifact 校验摘要，将准确字节保存为独立不可变对象，产物登记与工具结果同事务提交；下载验证完整字节后以 attachment 提供并持续复核权限。未实现工具在提交阶段拒绝。

下一轮优先复用当前完整目录，目录丢失或服务重启则从提交快照恢复。恢复失败不能以空目录继续。快照失败状态阻止新 Run，`POST /sessions/{id}/workspace-snapshots/retry` 只重试保存，不改旧 Run 终态、不重跑动作；`POST /sessions/{id}/workspace/restore` 显式丢弃未保存目录后恢复已提交状态。两者都要求无活跃 Run，数据库维护锁防止与新提交竞态；新 Session 的 source_snapshot_id 仅复制文件到独立目录。

## T12 M12 前端管理与调试

依赖：React、TypeScript、Vite、React Router、TanStack Query、Monaco Editor、react-markdown、openapi-typescript、@ag-ui/core。选用库的用途见官方说明：[Vite](https://vite.dev/guide/)、[React Router](https://reactrouter.com/start/declarative/installation)、[TanStack Query](https://tanstack.com/query/latest/docs/framework/react/overview)、[Monaco](https://github.com/microsoft/monaco-editor)。

### T12.1 页面与数据流

前端源码只放在 `frontend/src/`，按 features/spaces、agents、skills、sessions、debug、runs、settings 组织；package.json、lockfile、Vite 配置和前端测试均位于 `frontend/`。公共组件为定义编辑器、版本差异、依赖选择、Trace 时间线、原始载荷查看、上下文检查、计划进度和产物列表。代码工具展示原始代码、文件/网络 I/O、日志、错误及已发生效果；计划进度与 Run 终态分别展示。spaces 提供创建/切换和成员列表/添加/移除；sessions 展示最近快照、快照错误及重试保存入口，新会话可选择授权快照作为文件起点。

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

这里检查的是连续的 Run 传输序号，不是允许跳号的 Session 原始序号。客户端统一控制重连：onerror/解码或应用失败时关闭旧 EventSource，先用普通查询确认权限；401/403 显示权限错误并停止重连，其余按最后成功应用的 after 恢复；不让浏览器已接收但未成功应用的 Last-Event-ID 跳过事件。刷新时从 0 或成对保存的快照/游标恢复，不仅凭一个旧游标重连。

TEXT_MESSAGE_CONTENT 按 messageId 追加，工具参数按 toolCallId 聚合，TOOL_CALL_END 只关闭参数，结果到达后才展示执行结果。失败 attempt 的已显示文本标为 incomplete，重试新建消息；RUN_CANCELLED 显示已取消，其他 RUN_ERROR 显示失败，终态关闭订阅。事件回放只更新界面，不发起工具调用。

调试页同时展示原始日志、投影 Manifest、实际模型请求、epoch/前缀变化、真实缓存统计和载荷引用；摘要调用放在 Trace 区而非聊天助手回复中。普通 AG-UI 消息流不能冒充完整模型原文。空间切换立即关闭订阅并清空私有视图，大历史通过水位一致的 ui-snapshot 恢复并采用窗口展示。

### T12.3 D08 工作台实现

Catalog 的固定版本直接创建幂等调试 Session 并导航到对话；保留创建调试快照/发布版本与手动选择 Session 的原入口。草稿编辑不改变已固定版本，离开编辑器取消未完成的调试导航请求。

AguiRunClient 管理 GET 快照、EventSource 生命周期和单一重连计时器。快照经 Zod 校验协议、会话、状态、唯一实体及产物空间/会话/Run/下载路径，游标校验独立于 Session 日志水位。帧先过 @ag-ui/core/custom Schema 与 reducer，成功后才确认游标；重复忽略、缺口/应用错误关闭旧连接，经权限查询后使用最后应用的 after 重连。500ms—5s 有界退避，连续失败超过五次转为手动同步；401/403 清除私有状态并停止。只有标准 RUN_FINISHED/RUN_ERROR 或 closed 快照停止订阅，CUSTOM 状态不能提前跳过标准终态。旧连接及卸载后的回调不能更新新作用域。

Trace 与安全 Markdown 延迟加载；原文查看是可取消的空间查询，按实际流字节限制为 1 MiB，不依赖 Content-Length。运行消息/工具各按最近 50 条展示，可逐窗展开；旧消息和 Trace 保留分页。Trace 分类不改变固定日志水位，每页空结果明确显示。上下文检查继续导航到源事件和实际请求，工具参数、结果、代码与 I/O 均可从原始事件载荷下载。

新增 `GET /api/v1/spaces/{space_id}/runs/{run_id}/metrics`，按固定 Run 配置、task/summary 及 prefix_change_reason 聚合缓存覆盖率/命中率/输入占比、结束尝试数和可用耗时；零分母为 null，查询水位明确展示。统计最多读取 20000 个已授权事件；无隐式模型调用或当前草稿读取。公共 ui-snapshot 不暴露内部 SummaryAttempts；该字段仍用于后端跨批次回放。

D08 页面使用本地真实 Eino/HTTP 与 TOS 协议夹具验收，不依赖真实云凭证；D07 真实供应商验证已于 2026-10-24 单独授权并通过，实际缓存/延迟样本见 E15.8；D08 页面完成本身不代替真实证据。

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

服务日志输出到 stdout，附 request_id/space_id/run_id，不复制用户原始内容。监控队列、并发、模型错误、Trace/TOS 写入失败、上传字节数和数据库连接；仅配置会话临时目录，不提供本地持久 BlobStore。关闭时停止认领并完成有界 TOS/事件刷写。

### T13.1 D10 交付标识与离线恢复

构建锁定 Go/Node/npm 及模块/前端依赖，生成契约类型并嵌入前端；Go 使用 CGO_ENABLED=0、-mod=readonly、-trimpath、-buildvcs=false。VERSION、Git revision、SOURCE_DATE_EPOCH 注入二进制，`mass build-info` 输出版本/源码/时间/Go/平台/schema；时间默认为提交 Unix 时间。release 打包仅使用明确白名单：二进制、单份配置模板、README、四文档及图片、构建信息和文件 SHA-256。归档排序、文件权限/时间及 gzip 头固定，默认产物位于忽略的 .cache/release；不纳入真实配置或本机秘密。目前支持主机构建平台。

停服且没有排队/运行中的 Run 后，以 MariaDB 原生逻辑导出保存关系和密文；TOS 对象及其版本独立保留，主密钥由操作者另行安全保存。恢复先将导出导入空的隔离库，再执行 `mass restore-storage --config restored.yaml --source-config original.yaml --expect-database NAME`。源配置只提供留存 TOS 及原库名边界，命令不连接源数据库；目标配置仍采用统一 Config。源/目标库名必须不同，对象前缀必须互不包含。

目标库持有运行监督器同名数据库锁，运行中服务阻止恢复；核对 schema、目标库名、全部凭证解密、日志连续序列/事件摘要及对象引用，拒绝未完成上传和未终结 Run。按对象大小/SHA-256 校验源字节，使用 ForbidOverwrite 写目标独立前缀并读回校验；同一源 Bucket 使用原版本 ID；独立备份 Bucket 按原 key 查找留存副本并核对内容哈希，不套用原 Bucket 的版本 ID。版本 ID/ETag 以新对象为准。全部对象验证通过后在单个事务里校正 content_objects 的 key/URI/版本与 workspace_snapshots URI。不可变 runtime_events、event_digest、对象 ID/内容摘要保持原值，历史日志中的 URI 表示原位置；当前内容和快照恢复从对象索引解析，不直接访问历史 URI。迁移失败不改变位置索引，已上传的独立副本留存以便重试；同参数重试校验已有字节，不覆盖或删除源对象。

随后离线 rebuild-views 校验日志/对象并重建视图；启动后历史读取不执行模型/工具，新 Run 才解密原凭证并恢复最后确认的会话快照。无法解密、源或目标字节损坏、丢失对象/版本、未完成上传都必须先处理，不能忽略校验。完整操作步骤与当前真实云验证边界见 E15.11。

D10 真实恢复验收使用交付包提取的二进制、两个随机隔离 MariaDB 库和实际 TOS Bucket 内互斥的 `tmp/mass-d10-UUID/source/` / `restored/` 前缀。源对象持续保留至迁移校验成功，再删除本次源对象验证恢复实例独立性；本次没有新建 Bucket，跨 Bucket 仅保留本地协议测试证据。测试先持久化精确 key/版本账本，再允许上传或预登记恢复目标；清理依据账本和测试库清单，不列举整个 Bucket，不操作其他前缀。账本不序列化 SDK 客户端或凭证，失败后保留以核对清理。模型始终为本地夹具，传入 mass 的环境没有真实模型密钥；恢复读取本身不调用模型，新 Run 的两次 HTTP 仅访问回环端点。结果及版本限制见工程文档 E15.11。

`member add/remove` 是停服维护 CLI，使用已有 owner ID，经相同成员服务检查权限及写审计，并持有运行数据库锁；运行中使用鉴权页面/API，避免跨进程撤权通知缺失。

## T14 接口清单与错误语义

空间创建/列表及成员接口见 T01；下表的空间内业务接口均在 `/api/v1/spaces/{space_id}` 下。字段约定在各模块与后续机器可读契约内保持一致；发布和调用操作必须记录审计或 Trace。

| 接口组 | 主要操作 | 重要语义 |
| --- | --- | --- |
| /agents、/skills | 创建、列表、详情、复制、归档 | 空间范围；不物理破坏历史引用 |
| /agents/{id}/draft、/skills/{id}/draft | GET/PUT、资源管理 | expected revision，409 冲突 |
| /agents/{id}/versions、/skills/{id}/versions | POST 发布、GET 列表/详情 | 发布内容不可变，依赖精确版本 |
| /agents/{id}/default-version | PUT | 指向合法发布版本，只影响新会话 |
| /agents/{id}/debug-snapshots | POST | 固定草稿 revision 与 Skill 依赖 |
| /sessions | POST/GET | 固定定义；发布版本和调试快照二选一；可选 source_workspace_snapshot_id 只用于文件起点 |
| /sessions/{id}/workspace-snapshots、/sessions/{id}/workspace-snapshots/retry | GET 快照清单、POST 重试保存 | 同空间鉴权；与 Run 互斥，重试只保存未落盘的目录，不重跑任务 |
| /sessions/{id}/runs | POST | 幂等键；同会话最多一个活跃 Run |
| /runs/{id}、/runs/{id}/cancel | GET、POST | 取消请求不等于已停止；返回 workspace_snapshot_id/snapshot_error |
| /runs/{id}/events、/runs/{id}/ui-snapshot | AG-UI over SSE、快照 GET | 同源鉴权，独立 ui_sequence、协议版本与水位一致性 |
| /runs/{id}/trace、/sessions/{id}/journal | 分页 GET | 内部原始日志及内容引用，Session sequence 与 UI 游标分离 |
| /runs/{id}/contexts、/payloads/{id} | GET | 投影/Manifest、epoch、缓存统计和受控完整载荷 |
| /attachments | POST 上传并返回同空间附件 ID | 先保存原文，随 user.message.accepted 绑定后才允许工具读取；未引用上传不可用，不自动清理 |
| /artifacts/{id}/download | GET | 权限校验、受控文件名、对象可用性 |
| /models、/credentials、/members | 基础 owner 管理 API 及页面；成员规则见 T01 | 不引入权限矩阵；秘密不回显 |

错误码至少包括 unauthenticated、forbidden、not_found、draft_conflict、version_conflict、run_active、idempotency_conflict、context_overflow、unsupported_capability、unsupported_event_schema、projection_unavailable、event_conflict、budget_exceeded、trace_unavailable、invalid_cursor、workspace_unavailable、workspace_snapshot_failed、interrupted。错误码与展示文案分离，内部堆栈不返回给客户端。

OpenAPI 是接口的可执行契约来源；事件 schema_version 单独管理，不把供应商原始响应强制转成自有固定字段。接口变更同步生成类型与契约测试，不靠修改页面绕过不一致。

### T14.1 D09 故障验证边界

D09 验收代码直接使用生产 Bootstrap、Supervisor、Journal、ContentStore、工具分发与 goja，仅注入有界可控模型/对象存储故障；数据库为随机独立库，进程崩溃只针对测试创建的服务。覆盖应用池丢失、上传响应未知、部分模型输出、代码撤权、摘要/重试共用预算，以及终态/快照/占用的一致性。生产错误处理与恢复协议不因测试新增旁路。

时间保留测试仅推进隔离 SQL 会话时钟并恢复原设置，不改写不可变事件及其摘要；认证有效期与业务历史保留分离。离线重建先验证历史/对象后替换派生视图，损坏时保留旧视图。SIGKILL 后确认过的旧工具事实保留，但没有已提交快照的本地文件仍可能丢失；queued 可继续，running 的已发生/未知副作用不自动重放。

A01—A51 证据索引保留 local_evidence、pending_real、pending_delivery、historical_real 和 not_enabled 五种状态；`verify-d09-local` 不调用真实云资源，不能替代 D07 的真实模型/缓存观测或 D10 的交付/备份恢复。精确证据与命令记录在工程文档 E15.10。

## T15 工程布局与待收敛参数

```text
mass/
├── cmd/mass/                       # 唯一程序：serve/migrate/user/member
├── internal/
│   ├── domain/                     # 跨模块值类型、事件及纯领域规则
│   ├── app/                        # 定义/发布、会话/Run 提交、查询与取消
│   ├── journal/                    # 统一事件追加/读取、可信 reducer 与视图规则
│   ├── resource/                   # 不可变内容、会话目录及快照编排
│   ├── runtime/                    # 组织目录；不设包级全局 RuntimeService
│   │   ├── scheduler/              # 轮询、准入、认领、交接
│   │   ├── supervisor/             # Run context、取消/心跳、终态与恢复
│   │   ├── executor/               # 唯一 ReAct 循环及消费接口
│   │   ├── control/                # Run 限额、预算预留与结算
│   │   ├── context/
│   │   │   ├── planner/            # 容量/epoch、摘要编排、投影保存
│   │   │   └── projector/          # 固定事实只读投影、配对与稳定渲染
│   │   ├── modelgateway/           # attempt、重试、聚合、完整性与 usage
│   │   └── tool/
│   │       ├── registry/           # 工具版本、Schema 与 Handler 解析
│   │       ├── dispatcher/         # 授权、限额、I/O 审计和结果提交
│   │       └── builtin/            # T09 的 14 个工具，按工具拆文件
│   ├── adapter/
│   │   ├── mariadb/                # 事务持久化与查询接口实现
│   │   ├── tos/                    # TOS 对象访问，不拥有工作区业务规则
│   │   ├── workspace/             # 当前会话真实文件、tar.gz 打包与受控恢复
│   │   ├── eino/                   # Eino 组件、DTO 转换和原文捕获
│   │   └── javascript/             # goja 引擎及注入的 fs/net 接口，不能越过授权范围
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

不建立持久化的 `data/blobs` 或前端独立服务配置；允许通过 workspace_temp_root 创建会话临时工作目录。持久资源仍只使用 MariaDB/TOS，本地目录可由快照重建。

D00 已固定工具链、MariaDB 目标版本、Go/前端依赖和首个 Ark 组件。D01/D04 已接入实际 TOS/模型、部署批准端点、保守计数器和快照容量上限；精确 tokenizer、模型缓存及完整工具策略待后续阶段收敛。已指定的单服务、MariaDB、TOS、Eino、frontend、简单登录及 AG-UI over SSE 不再作为待选方案。AG-UI Go/TS SDK 精确版本与兼容子集见 api/events/agui-profile.json；验证证据见工程开发文档。

D06 的默认压缩阈值、目标余量和投影策略版本见 T07.8；真实模型缓存能力在 D07 验证；未知缓存命中不能用假数据补齐。

goja/doublestar 精确版本已由 go.mod 锁定；生产 JS 子集和默认扫描/代码限额见 T09.6；引擎限制不能被配置成“已提供沙箱”。

真实云资源缺失时用内存 TOS fake 和 Eino FakeChatModel 完成可验证编码，但不能以 fake 测试替代真实 TOS/模型接入验收。框架与 SDK 的实际方法、字段和重试行为以锁定版本核验，并将证据记录到工程开发文档。
