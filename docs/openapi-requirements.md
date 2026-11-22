# MASS OpenAPI 与 Bearer Token 需求

状态为评审稿。

## 1. 目标与使用场景

空间所有者在「空间管理 → OpenAPI」签发 Bearer Token，供外部服务调用 MASS 的会话接口。外部服务可以创建会话、上传附件、发送消息、接收流式回复和读取历史消息。MASS 网页继续使用 `mass_session` 登录 Cookie。

## 2. 会话接口与鉴权

首版在下表五个现有接口上支持 `Authorization: Bearer <token>`。路径均以 `/api/v1/spaces/{space_id}` 开头。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/sessions` | 创建会话，返回 `201` 和会话信息。 |
| `POST` | `/sessions/{session_id}/attachments` | 上传当前会话的附件，返回附件 ID。 |
| `POST` | `/sessions/{session_id}/runs` | 提交消息，返回 `202` 和 Run 信息。 |
| `GET` | `/sessions/{session_id}/messages` | 分页读取会话内的历史消息。 |
| `GET` | `/runs/{run_id}/events` | 通过 SSE 接收该 Run 的增量回复和终态事件。 |

- **凭证选择。** 带 `Authorization` 请求头时验证 Bearer Token。格式错误、凭证无效、过期或已吊销均返回 `401`，即使同时带有有效 Cookie。省略 `Authorization` 时使用 `mass_session`。
- **授权与幂等。** 每枚 Token 绑定一个空间，访问其他空间返回 `403`。会话操作使用签发人的用户身份，沿用空间成员和资源归属校验。创建会话、提交消息继续使用现有 `idempotency_key`。同一签发人轮换 Token 后，也可用原键重试同一请求。
- **写请求。** Cookie 写请求继续校验 `Origin`。Bearer 写请求可以省略 `Origin`。两种方式都要求 `Content-Type: application/json`。
- **流式连接。** 订阅时及连接期间校验凭证和空间权限。Token 到期、被吊销或失去空间权限后，关闭受影响的 SSE 连接。已经受理的 Run 继续按运行规则执行。

请求体、响应结构、消息分页和 SSE 事件沿用现有契约。OpenAPI 对这五个操作声明 Cookie 和 Bearer 两种鉴权方式，并同步生成前端 API 类型。 [现有接口契约](../api/openapi.yaml)

## 3. OpenAPI 管理页

页面路径为 `/spaces/{space_id}/openapi`。仅当前空间所有者可看到入口、打开页面和调用管理接口。管理接口通过 `mass_session` 识别登录用户，并在服务端校验空间所有者权限。

| 操作 | 页面行为 |
| --- | --- |
| 查看列表 | 展示名称、创建时间、到期时间和状态。状态为有效、已过期或已吊销。 |
| 创建 | 填写名称和到期时间。默认到期时间为签发后 180 天，也可指定将来的到期时间。创建成功即可复制 Token。 |
| 复制 | 有效 Token 随时可从页面复制。点击时由服务端读取并返回完整原文。 |
| 续期 | 有效 Token 默认在原到期时间上延长 180 天，已过期 Token 默认从续期时起计算 180 天。确认前展示新的到期时间，提交时使用具体的 `expires_at`。续期沿用原 Token，已过期 Token 续期后恢复使用。 |
| 吊销 | 确认后立即阻止新请求，列表保留已吊销记录。已吊销 Token 的状态为终态。 |

### 管理接口

以下路径均以 `/api/v1/spaces/{space_id}/openapi/tokens` 开头。

| 方法 | 路径后缀 | 契约 |
| --- | --- | --- |
| `GET` | 根路径 | 返回 Token 列表和展示字段。 |
| `POST` | 根路径 | 接收 `name` 和可选的 `expires_at`，返回创建结果和 Token 原文。 |
| `GET` | `/{token_id}` | 返回有效 Token 的原文，供所有者复制。 |
| `POST` | `/{token_id}/renew` | 接收新的 `expires_at`，返回更新后的到期时间。 |
| `DELETE` | `/{token_id}` | 吊销指定 Token，成功返回 `204`。 |

## 4. 存储与审计

新增空间 Token 表，保存 Token ID、空间 ID、签发人 ID、名称、Token 原文、创建时间、到期时间及吊销时间。服务端为每枚 Token 生成 32 字节随机值并保存编码后的原文，鉴权时精确匹配。状态根据到期时间和吊销时间计算，吊销优先。

只有创建和复制接口返回 Token 原文，响应设置 `Cache-Control: no-store`。列表只返回展示字段。日志和审计以 Token ID 标识令牌。创建、续期和吊销应与对应审计记录一起提交。 [现有审计机制](../internal/adapter/mariadb/database.go)

## 5. 验收标准

1. 空间所有者能创建 Token，默认有效期为 180 天。离开页面后重新进入，仍能复制同一个有效 Token。普通成员直达页面或调用管理接口时无法执行管理操作。
2. 外部服务仅使用 Bearer Token，即可完成创建会话、上传附件、提交消息、接收 SSE 终态和读取历史。网页使用现有 Cookie 完成同一流程。
3. 跨空间访问返回 `403`。无效、过期或已吊销的 Bearer 返回 `401`。同时带有效 Cookie 时，失败的 Bearer 验证仍返回 `401`。
4. 有效和已过期的 Token 按对应规则续期。续期后原 Token 可按新的到期时间使用。已吊销 Token 无法复制、续期或通过鉴权。
5. Token 到期或被吊销时，已有 SSE 连接关闭，已受理的 Run 继续执行。新请求按当前凭证状态处理。
6. 列表、日志和审计记录均无 Token 原文。创建和复制响应含 `Cache-Control: no-store`。创建、续期和吊销可按 Token ID 查询审计记录。

## 6. 待评审的会话访问范围

当前方案按空间授权，允许外部服务继续向本空间已有会话发消息并读取其历史。请确认对接方是否需要这个范围，还是需要将会话访问限定为该 Token 创建的会话。
