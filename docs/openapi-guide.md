# MASS OpenAPI 使用指南

本文档介绍外部服务如何使用空间所有者签发的 Bearer Token 调用 MASS 会话接口，完成创建会话、上传附件、提交消息、接收流式回复（SSE）与读取历史消息。

---

## 1. 接入准备

调用前需要从空间所有者处获取以下三项静态配置：

| 参数 | 说明 | 示例 |
| --- | --- | --- |
| `MASS_URL` | MASS 服务地址 | `http://127.0.0.1:8080` |
| `SPACE_ID` | 目标空间 ID（16 位短 ID） | `4lca4gankkuxip2p` |
| `AGENT_ID` | 目标空间内的 Agent ID（16 位短 ID） | `mkc3hugms3bfbutp` |
| `BEARER_TOKEN` | 在「空间管理 → OpenAPI」创建并复制的 43 位 Bearer Token | `<token>` |

---

## 2. 鉴权与通用规范

### 2.1 请求头

- **鉴权头**：所有开放接口均通过 HTTP Header 传递 Token：
  ```http
  Authorization: Bearer <token>
  ```
- **内容类型**：所有写请求（`POST`）必须设置：
  ```http
  Content-Type: application/json
  ```
- **无需 `Origin`**：使用 Bearer Token 时无需携带 `Origin` 请求头，也不会回退到 `mass_session` Cookie。

### 2.2 开放接口清单

首版仅以下 **5 个接口**支持 Bearer 鉴权（路径均以 `/api/v1/spaces/{space_id}` 为前缀）：

| 方法 | 路径 | 用途 | 成功状态码 |
| --- | --- | --- | --- |
| `POST` | `/sessions` | 创建会话 | `201` |
| `POST` | `/sessions/{session_id}/attachments` | 上传会话附件 | `201` |
| `POST` | `/sessions/{session_id}/runs` | 提交消息并启动 Run | `202` |
| `GET` | `/runs/{run_id}/events` | 通过 SSE 订阅增量回复与终态事件 | `200` |
| `GET` | `/sessions/{session_id}/messages` | 分页读取会话历史消息 | `200` |

> 其他所有 `/api/v1/...` 接口（含空间查询、模型配置、OpenAPI Token 管理等）仅支持网页 Cookie 鉴权；携带 `Authorization` 头调用这些接口将返回 `401`。

### 2.3 授权范围与幂等

- **空间级访问**：每枚 Token 绑定单个空间，可以创建新会话，也可以向本空间已有会话继续发消息、上传附件或读取历史；跨空间访问返回 `403`。
- **请求幂等**：创建会话与提交消息均要求传入 `idempotency_key`（1–128 字符，允许字母、数字及 `._:-`）。相同 `idempotency_key` 与相同请求体可安全重试；同空间签发人轮换 Token 后，仍可用原 `idempotency_key` 重试同一请求。

---

## 3. 接口详细说明与示例

以下示例统一假设已设置环境变量：

```sh
export MASS_URL="http://127.0.0.1:8080"
export SPACE_ID="<space_id>"
export AGENT_ID="<agent_id>"
export BEARER_TOKEN="<token>"
```

### 3.1 创建会话

`POST /api/v1/spaces/{space_id}/sessions`

**请求体字段：**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `idempotency_key` | string | 是 | 幂等键，匹配 `^[A-Za-z0-9._:-]{1,128}$` |
| `agent_id` | string | 是 | 目标 Agent ID |
| `version_id` | string | 否 | 省略时使用 Agent 默认正式版本；传 `"latest"` 使用最新草稿；也可指定具体发布版本 UUID |
| `title` | string | 否 | 会话标题，最长 200 字符；省略时默认使用生成的会话 ID |
| `hide` | boolean | 否 | 是否在空间会话列表中隐藏该会话 |

**示例：**

```sh
curl -fsS -X POST "$MASS_URL/api/v1/spaces/$SPACE_ID/sessions" \
  -H "Authorization: Bearer $BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "sess-20261122-001",
    "agent_id": "'"$AGENT_ID"'",
    "title": "外部服务对话"
  }'
```

返回 `201 Created`，从响应 JSON 中取出 `id` 作为后续调用的 `SESSION_ID`。

---

### 3.2 上传附件（可选）

`POST /api/v1/spaces/{space_id}/sessions/{session_id}/attachments`

附件内容以标准 Base64 编码通过 JSON 提交，单文件原始大小上限 **8 MiB**。

**请求体字段：**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 是 | 文件名（1–200 字符，不含 `/`、`\`、换行或空字符） |
| `media_type` | string | 是 | MIME 类型，如 `text/plain`、`application/pdf`、`image/png` |
| `content_base64` | string | 是 | 严格标准 Base64 编码的文件内容 |

**示例：**

```sh
CONTENT_B64=$(printf 'Hello MASS\n' | base64)

curl -fsS -X POST "$MASS_URL/api/v1/spaces/$SPACE_ID/sessions/$SESSION_ID/attachments" \
  -H "Authorization: Bearer $BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "note.txt",
    "media_type": "text/plain",
    "content_base64": "'"$CONTENT_B64"'"
  }'
```

返回 `201 Created`，从响应 JSON 中取出 `id` 作为 `ATTACHMENT_ID`。

---

### 3.3 提交消息（发起 Run）

`POST /api/v1/spaces/{space_id}/sessions/{session_id}/runs`

**请求体字段：**

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `idempotency_key` | string | 是 | 幂等键，匹配 `^[A-Za-z0-9._:-]{1,128}$` |
| `text` | string | 是 | 用户输入文本（非空白，上限 256 KiB） |
| `attachment_ids` | string[] | 否 | 当前会话已上传的附件 ID 列表（最多 16 个，不可重复） |

**示例：**

```sh
curl -fsS -X POST "$MASS_URL/api/v1/spaces/$SPACE_ID/sessions/$SESSION_ID/runs" \
  -H "Authorization: Bearer $BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "run-20261122-001",
    "text": "请总结附件里的内容",
    "attachment_ids": ["'"$ATTACHMENT_ID"'"]
  }'
```

返回 `202 Accepted`，从响应 JSON 中取出 `id` 作为 `RUN_ID`。同一会话存在进行中的 Run 时，新提交会返回 `409`（`run_active`）。

---

### 3.4 订阅流式回复（SSE）

`GET /api/v1/spaces/{space_id}/runs/{run_id}/events`

响应类型为 `text/event-stream`，按 AG-UI 协议（`mass-agui-v1`）输出事件帧，心跳以 `: heartbeat` 注释行发送，Run 进入终态后服务端主动关闭流。

**可选查询参数与请求头：**

| 参数/头 | 位置 | 说明 |
| --- | --- | --- |
| `after` | query | 从指定事件序号之后续读（十进制非负整数字符串，默认 `"0"`） |
| `Last-Event-ID` | header | 标准 SSE 断线重连游标；若与 `after` 同时传入，两者必须一致 |
| `stream_id` | query | 校验流 ID（可选） |

**示例：**

```sh
curl -N -fsS "$MASS_URL/api/v1/spaces/$SPACE_ID/runs/$RUN_ID/events" \
  -H "Authorization: Bearer $BEARER_TOKEN"
```

常见事件类型（每条 `data:` 为 JSON 对象）：

- `RUN_STARTED`：Run 开始执行
- `TEXT_MESSAGE_START` / `TEXT_MESSAGE_CONTENT` / `TEXT_MESSAGE_END`：助手文本增量流（增量内容在 `delta` 字段）
- `TOOL_CALL_START` / `TOOL_CALL_ARGS` / `TOOL_CALL_END` / `TOOL_CALL_RESULT`：工具调用过程
- `RUN_FINISHED` / `RUN_ERROR`：Run 终态事件

> 若在订阅期间 Token 到期或被吊销，服务端会直接关闭受影响的 SSE 连接；已经受理的 Run 仍按原有运行规则继续执行完成。

---

### 3.5 分页读取历史消息

`GET /api/v1/spaces/{space_id}/sessions/{session_id}/messages`

**查询参数：**

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `after_sequence` | string | 升序向后分页，读取序号大于该值的消息（与 `before_sequence` 互斥） |
| `before_sequence` | string | 降序向前分页，读取序号小于该值的最新消息（传 `"0"` 获取最新一页，返回结果已按序号升序排列） |
| `limit` | integer | 每页条数，范围 `1`–`100`，默认 `20` |

**示例：**

```sh
curl -fsS "$MASS_URL/api/v1/spaces/$SPACE_ID/sessions/$SESSION_ID/messages?limit=20" \
  -H "Authorization: Bearer $BEARER_TOKEN"
```

**响应结构：**

```json
{
  "items": [
    {
      "id": "...",
      "run_id": "...",
      "role": "assistant",
      "sequence": "8",
      "complete": true,
      "content": "...",
      "truncated": false
    }
  ],
  "next_after": "8",
  "has_more": false
}
```

> 单条消息正文超过 `8192` 字节时会在接口中截断并标记 `"truncated": true`；如需获取完整回复，推荐在执行阶段通过 SSE 完整接收 `TEXT_MESSAGE_CONTENT` 增量片段。

---

## 4. 常见错误码

错误响应统一为 JSON 结构：`{ "code": "...", "message": "...", "request_id": "..." }`。

| HTTP 状态码 | `code` | 常见原因与处理建议 |
| --- | --- | --- |
| `400` | `invalid_request` / `invalid_cursor` / `invalid_definition` | 请求体字段不合法、游标非法或 Agent 配置无效 |
| `401` | `unauthenticated` | `Authorization` 格式错误、Token 不存在/已过期/已吊销，或调用了未开放 Bearer 的接口 |
| `403` | `forbidden` | Token 不属于目标 `space_id`（跨空间访问），或签发人已失去空间权限 |
| `404` | `not_found` | 会话、Run 或 Agent 不存在 |
| `409` | `run_active` / `conflict` | 会话已有运行中的 Run，或相同 `idempotency_key` 使用了不同的请求体 |
| `415` | `unsupported_media_type` | `POST` 请求未设置 `Content-Type: application/json` |
| `503` | `runtime_unavailable` / `content_unavailable` | 运行时暂不可用或对象存储内容不可读取 |

---

## 5. Token 运维建议

- **轮换与续期**：默认有效期为 180 天，建议在到期前于「空间管理 → OpenAPI」页面直接续期（沿用原 Token），或签发新 Token 完成平滑切换后再吊销旧 Token。
- **妥善保管**：请将 Token 保存在调用方的环境变量或密钥管理服务中，避免写入代码仓库或明文日志。
- **紧急止损**：发生泄露时在管理页立即「吊销」，服务端会立即拒绝新请求并断开该 Token 对应的活跃 SSE 连接。
