# MASS OpenAPI 与 Bearer Token 需求

状态：已定稿，可进入开发。

## 1. 目标与使用场景

空间所有者在「空间管理 → OpenAPI」签发 Bearer Token，供外部服务调用 MASS 的会话接口。外部服务可以创建会话、上传附件、发送消息、接收流式回复和读取历史消息。MASS 网页继续使用 `mass_session` 登录 Cookie。

首版范围与边界：

- **空间级访问。** 每枚 Token 绑定单个空间，允许外部服务创建新会话，也允许继续向本空间已有会话提交消息并读取历史。
- **静态配置前提。** 首版约定外部服务通过配置获取 `space_id` 和 `agent_id`，不开放空间/Agent 查询接口。
- **非目标。** 消息超过 `8192` 字节被截断后的全文读取（`/payloads/{id}`）、产物下载、取消 Run 以及 `ui-snapshot` 不在首版 Bearer 开放范围。

## 2. 会话接口与鉴权

首版仅在下表五个现有接口上支持 `Authorization: Bearer <token>`。路径均以 `/api/v1/spaces/{space_id}` 开头。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/sessions` | 创建会话，返回 `201` 和会话信息。 |
| `POST` | `/sessions/{session_id}/attachments` | 上传当前会话的附件，返回附件 ID。 |
| `POST` | `/sessions/{session_id}/runs` | 提交消息，返回 `202` 和 Run 信息。 |
| `GET` | `/sessions/{session_id}/messages` | 分页读取会话内的历史消息。 |
| `GET` | `/runs/{run_id}/events` | 通过 SSE 接收该 Run 的增量回复和终态事件。 |

- **接口范围与凭证选择。**
  - 上述五个接口：只要请求携带 `Authorization` 请求头即进入 Bearer 鉴权，不再回退 `mass_session` Cookie。要求且仅允许单个 `Authorization` 头，格式为 `Bearer <token>`（`Bearer` 方案名不区分大小写，后接单个空格与非空且不含空白字符的令牌）；格式错误、令牌不存在、已过期或已吊销均返回 `401`（`unauthenticated`），即使同时携带有效 Cookie。省略 `Authorization` 头时使用 `mass_session` Cookie。
  - 其他所有 `/api/v1/...` 接口（含 OpenAPI 管理接口）：保持只认 `mass_session` Cookie。若携带 `Authorization` 请求头，统一返回 `401`（`unauthenticated`），不回退 Cookie。
- **校验顺序与授权。** 先校验 Bearer 凭证有效（无效、过期或已吊销返回 `401`），再校验 Token 绑定的 `space_id` 与路径参数 `space_id` 一致（不一致返回 `403` `forbidden`），最后以签发人用户身份执行空间成员与资源访问校验。当前单空间仅有一名不可移除的所有者，签发人即该空间所有者。
- **幂等。** `POST /sessions` 和 `POST /sessions/{session_id}/runs` 沿用现有按 `(space_id, actor_id, operation, idempotency_key)` 的去重机制。同一签发人轮换 Token 后，仍可用原键重试同一请求。
- **写请求检查。**
  - Cookie 写请求继续校验 `Origin`、`Sec-Fetch-Site != cross-site` 和 `Content-Type: application/json`。
  - Bearer 写请求跳过 `Origin` 和 `Sec-Fetch-Site` 校验，保留 `Content-Type: application/json` 校验。凭证无效时优先返回 `401`；凭证通过但 `Content-Type` 不符时返回 `415`（`unsupported_media_type`）。
- **流式连接（SSE）。** 订阅建立前校验凭证和空间权限（失败返回 `401` 或 `403`）。连接期间沿用现有 `300ms` 轮询检查和访问撤销监听；Token 到期、被吊销或签发人失去空间权限后，直接关闭受影响的 SSE 连接（不发送额外错误事件）。已经受理的 Run 继续按运行规则执行。

请求体、响应结构、消息分页和 SSE 事件沿用现有契约。在 [现有接口契约](../api/openapi.yaml) 中新增 `BearerAuth`（`type: http`, `scheme: bearer`），为这五个操作声明 Cookie 和 Bearer 两种鉴权方式，并同步生成前端 API 类型。

## 3. OpenAPI 管理页与管理接口

### 页面行为

页面路径为 `/spaces/{space_id}/openapi`（后端 SPA 路由白名单同步加入 `openapi`），导航入口位于「空间管理 → OpenAPI」。仅当前空间所有者（`space.is_owner`）可见入口并执行管理操作；普通成员直达页面时展示无权限提示并引导返回空间首页，不发起管理请求。所有管理接口仅通过 `mass_session` 识别登录用户，并在服务端事务内校验空间所有者权限（非所有者返回 `403`）。

| 操作 | 页面行为 |
| --- | --- |
| 查看列表 | 按创建时间倒序展示名称、创建时间、到期时间和状态。状态为有效（`active`）、已过期（`expired`）或已吊销（`revoked`）。 |
| 创建 | 填写名称和到期时间。默认到期时间为当前时间起 180 天，也可指定将来的到期时间（按本地时区选择，提交 UTC RFC 3339）。创建成功后展示并可一键复制 Token。 |
| 复制 | 仅有效（`active`）Token 显示复制操作。点击时由服务端解密读取并返回完整原文。 |
| 续期 | 有效 Token 默认在原到期时间上延长 180 天，已过期 Token 默认从续期时起计算 180 天。确认弹窗展示并允许调整新的到期时间（须晚于当前时间），提交具体的 `expires_at`。续期沿用原 Token，已过期 Token 续期后恢复为 `active`。 |
| 吊销 | 二次确认后立即阻止新请求，列表保留已吊销记录。已吊销 Token 的状态为终态，不可复制或续期。 |

### 管理接口契约

以下路径均以 `/api/v1/spaces/{space_id}/openapi/tokens` 开头，仅支持 `mass_session` Cookie；写请求（含 `DELETE`）按现有规范校验 `Origin` 与 `Content-Type: application/json`。

- `token_id` 使用 UUID（`ResourceID`，`CHAR(36)`）。
- 展示对象 `OpenAPIToken` 字段：`id`（uuid）、`name`（string）、`status`（`active` | `expired` | `revoked`）、`created_at`（RFC 3339 UTC）、`expires_at`（RFC 3339 UTC）、`revoked_at`（RFC 3339 UTC 或 `null`）。不包含 Token 原文及密文元数据。
- `name` 沿用现有命名校验：UTF-8、1–100 字符、不含首尾空白及 `\x00\r\n`，同一空间允许重名。

| 方法 | 路径后缀 | 请求与响应契约 |
| --- | --- | --- |
| `GET` | 根路径 | 支持 `limit`（1–100，默认 20）与 `cursor` 分页，返回 `200` 和 `{ "items": OpenAPIToken[], "next_cursor": string \| null }`。 |
| `POST` | 根路径 | 请求体 `{ "name": string, "expires_at"?: string }`。省略 `expires_at` 时默认服务端当前时间 + 180 天；传入时必须为合法 RFC 3339 且严格晚于当前服务端时间，否则返回 `400`（`invalid_request`）。成功返回 `201`，响应头含 `Cache-Control: no-store`，响应体为 `OpenAPIToken` 字段加 `"token": string`。 |
| `GET` | `/{token_id}` | 仅 `active` 状态允许复制。成功返回 `200` 和 `{ "id": string, "token": string }`，响应头含 `Cache-Control: no-store`。Token 不存在返回 `404`（`not_found`）；已过期或已吊销返回 `409`（`conflict`）。 |
| `POST` | `/{token_id}/renew` | 请求体 `{ "expires_at": string }`（必填，合法 RFC 3339 且严格晚于当前服务端时间，否则 `400` `invalid_request`）。成功返回 `200` 和更新后的 `OpenAPIToken`（不含原文）。Token 不存在返回 `404`；已吊销返回 `409`（`conflict`）。 |
| `DELETE` | `/{token_id}` | 吊销指定 Token，成功返回 `204`。Token 不存在返回 `404`；对已吊销 Token 重复调用幂等返回 `204` 且不重复记录审计。 |

## 4. 存储、加密与审计

新增 `space_openapi_tokens` 表（通过新的 Goose 迁移脚本创建），保存以下字段：

- `id CHAR(36)`（UUID）、`space_id CHAR(16)`（关联 `spaces.id`）、`issuer_id CHAR(36)`（关联 `users.id`）、`name VARCHAR(100)`
- `token_hash BINARY(32)`（唯一索引，存储完整 Token 字符串的 `SHA-256` 摘要，用于鉴权精确查找）
- `key_id VARCHAR(64)`、`ciphertext BLOB`（复用现有 `identity.Cipher`，通过 `Seal(space_id, "openapi-token:"+id, token)` 加密存储；仅复制接口调用 `Open` 解密，不以明文落库）
- `created_at DATETIME(6)`、`expires_at DATETIME(6)`、`revoked_at DATETIME(6) NULL`

Token 生成与状态规则：

- 服务端用 `crypto/rand` 生成 32 字节随机数，经 `base64.RawURLEncoding` 编码作为 Token 原文（43 字符）。
- 鉴权时对请求中的 Bearer Token 计算 `SHA-256`，按 `token_hash` 唯一索引查询。
- 状态按服务端当前 UTC 时间计算：`revoked_at IS NOT NULL` 为 `revoked`，否则 `expires_at <= now` 为 `expired`，否则为 `active`。

脱敏与审计：

- 只有创建和复制接口返回 Token 原文，响应头设置 `Cache-Control: no-store`。列表接口、服务端日志、诊断日志和审计记录均不包含 Token 原文，统一以 `token_id` 标识。
- 复用 [现有审计机制](../internal/adapter/mariadb/database.go)（`audit_logs` 表，在同一事务内提交，`target_id` 填 `token_id`），事件名约定如下：
  - 创建：`openapi_token.created`
  - 复制：`openapi_token.copied`
  - 续期：`openapi_token.renewed`
  - 吊销：`openapi_token.revoked`（仅首次吊销时记录）

## 5. 验收标准

1. 空间所有者能创建 Token（默认 180 天有效期），离开页面后重新进入仍可复制同一个有效 Token；普通成员看不到入口，直达 `/spaces/{space_id}/openapi` 或调用管理接口均无法执行管理操作（服务端返回 `403`）。
2. 外部服务仅使用 `Authorization: Bearer <token>`（无需 `Origin`）即可完成创建会话、上传附件、提交消息、接收 SSE 终态和读取历史（含访问空间内已有会话）；网页使用 `mass_session` Cookie 完成同一流程。
3. 跨空间访问返回 `403`。无效、格式错误、过期或已吊销的 Bearer 返回 `401`（即使同时带有效 Cookie）；使用 Bearer 访问五个开放接口以外的 API 返回 `401`。
4. 有效和已过期 Token 按对应规则续期，续期后原 Token 可按新的到期时间使用。已过期和已吊销 Token 调用复制接口返回 `409`；已吊销 Token 无法续期（`409`）或通过鉴权（`401`），重复吊销幂等返回 `204`。
5. Token 到期或被吊销时，已有 SSE 连接关闭，已受理的 Run 继续执行；新请求按当前凭证状态处理。
6. 数据库不存 Token 明文（校验 `token_hash` 索引鉴权与 `Cipher` 密文解密）；列表、日志和审计记录均无 Token 原文；创建和复制响应含 `Cache-Control: no-store`；创建、复制、续期和吊销均可按 `token_id` 查到对应审计记录。
