# Skill CLI 访问当前空间平台资源需求与方案

状态：已定稿，可进入开发。

## 1. 目标与使用场景

在 MASS 平台中运行自定义 Skill 时，Agent 通过内置 `run_command` 工具调用该 Skill 提供的 CLI，查询当前空间及平台的只读资源（如当前空间的 Skills 列表、平台受支持的 Tools 列表等）。

核心目标：

- **自动绑定当前空间**：Agent 在哪个空间执行 Skill CLI，CLI 就自动访问该空间的资源；同一个 Skill 无需任何空间硬编码配置，即可在任意空间直接复用。
- **复用现有 Bearer 鉴权体系**：不引入第二套独立鉴权系统，统一复用 [openapi-requirements.md](openapi-requirements.md) 已落地的 `Authorization: Bearer <token>` 解析、校验与空间隔离链路。
- **最小权限与零长效泄露**：CLI 仅获得本次命令执行期间有效的只读短时凭证，不落库、不污染「空间管理 → OpenAPI」管理页与审计日志，命令结束立即失效。

## 2. 统一 Bearer 鉴权与 Scope 区分

沿用现有 `parseBearerHeader`、`registerBearer` 与 `identity.Service.AuthenticateBearer`，通过服务端 `token_hash` 命中的来源区分 Token 类型并赋予 `Scope`，不依赖客户端传递额外标记。

### 2.1 Token 来源与区分规则

| 类型 | 存储位置 | 生命周期 | `Scope` | 适用接口 |
| --- | --- | --- | --- | --- |
| OpenAPI 长效 Token | 数据库 `space_openapi_tokens` | 由空间所有者创建、续期、吊销（默认 180 天） | `openapi` | 现有 5 个会话接口 + 本文 6 个只读资源接口 |
| 命令级短时 Token | 服务端内存（不落库） | 单次 `run_command` 执行前签发，退出/超时/取消后 `defer` 立即销毁 | `run_read` | 仅限本文 6 个只读资源接口 |

### 2.2 统一鉴权流程

1. 对 `Authorization: Bearer <token>` 中的 43 字符 `base64.RawURLEncoding` 令牌计算 `SHA-256` 摘要得到 `token_hash`（与现有 OpenAPI Token 生成格式完全一致）。
2. 在 `identity.Service.AuthenticateBearer` 内按固定顺序查找：
   - 先查进程内 `ephemeralTokens[token_hash]`：命中则返回 `BearerPrincipal{User, SpaceID, TokenID, Scope: "run_read"}`。
   - 未命中再查数据库 `space_openapi_tokens`：命中且未过期、未吊销则返回 `BearerPrincipal{User, SpaceID, TokenID, Scope: "openapi"}`。
   - 均未命中返回 `401`（`unauthenticated`）。
3. 校验顺序保持一致：
   - 凭证无效/已销毁/已过期 → `401`（`unauthenticated`）。
   - `principal.Scope` 不满足当前路由所需范围 → `401`（`unauthenticated`）。
   - `principal.SpaceID != 路径 space_id` → `403`（`forbidden`）。
   - 以 `principal.User.ID` 校验目标空间成员资格（含执行中成员被移除场景） → 失效返回 `403`（`forbidden`）。

## 3. `run_command` 凭证签发与环境变量注入

当前 [command_darwin.go](../internal/adapter/command/command_darwin.go) 会重置子进程环境变量，仅保留 `PATH`、`HOME`、`TMPDIR`、`LANG`。在保持沙箱隔离边界不变的前提下，由运行时在每次执行 `run_command` 时自动注入平台只读上下文。

### 3.1 执行生命周期

在 [command.go](../internal/resource/command.go) 的 `CommandService.Run(ctx, s run.Snapshot, name, args)` 中：

1. 使用 `s.SpaceID`（当前 Run 所属空间，跨空间调用公开 Agent 时同样是当前运行空间）和 `s.ActorID` 向 `identity.Service` 签发一枚 `Scope = "run_read"` 的短时 Token。
2. 立即注册 `defer revoke(token)`，保证无论命令正常退出、失败、超时还是 Run 取消，短时 Token 都在 `CommandService.Run` 返回模型之前从内存中移除。
3. 即使 CLI 意外将环境变量输出到 `stdout`/`stderr`，待模型读取到输出时该 Token 也已失效。

### 3.2 注入的环境变量

| 环境变量 | 值来源 | 说明 |
| --- | --- | --- |
| `MASS_API_URL` | 当前服务本机回环监听地址（如 `http://127.0.0.1:8080`） | 即使服务以 `--lan` 模式启动，CLI 仍走本机回环地址访问 |
| `MASS_SPACE_ID` | `run.Snapshot.SpaceID` | 当前执行所在空间 ID（16 位短 ID） |
| `MASS_RUN_TOKEN` | 本次命令签发的短时只读 Token | 用于请求头 `Authorization: Bearer $MASS_RUN_TOKEN` |

### 3.3 Skill CLI 调用示例

```sh
# 获取当前空间全部 Skills（含 description）
curl -fsS "$MASS_API_URL/api/v1/spaces/$MASS_SPACE_ID/skills?limit=100&include_description=true" \
  -H "Authorization: Bearer $MASS_RUN_TOKEN"

# 获取平台受支持的 Tools
curl -fsS "$MASS_API_URL/api/v1/spaces/$MASS_SPACE_ID/tools" \
  -H "Authorization: Bearer $MASS_RUN_TOKEN"
```

## 4. 只读资源接口清单

所有接口均沿用 `/api/v1/spaces/{space_id}` 前缀，支持现有 `mass_session` Cookie、命令短时 `run_read` Bearer 与空间 `openapi` Bearer 三种鉴权（遵循“带 `Authorization` 头不回退 Cookie”原则）。

### 4.1 开放的只读接口清单

| 方法 | 路径 | 现状 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/spaces/{space_id}/skills` | 已有（[catalog.go](../internal/transport/http/catalog.go)） | 分页返回当前空间的 Skills 列表，新增可选查询参数 `include_description`（默认 `false`） |
| `GET` | `/api/v1/spaces/{space_id}/skills/{id}/versions` | 已有（[catalog.go](../internal/transport/http/catalog.go)） | 返回指定 Skill 的已发布版本列表 |
| `GET` | `/api/v1/spaces/{space_id}/skills/{id}/versions/{version_id}/package` | **新增** | 下载指定 Skill 已发布版本的干净 ZIP 包（含 `SKILL.md`、目录与校验通过的资源文件，不含内部 `bundle.json`），并通过响应头返回 `X-Mass-Skill-Id`、`X-Mass-Skill-Name`、`X-Mass-Skill-Version-Id`、`X-Mass-Skill-Version` |
| `GET` | `/api/v1/spaces/{space_id}/agents` | 已有（[catalog.go](../internal/transport/http/catalog.go)） | 分页返回当前空间的 Agents 列表 |
| `GET` | `/api/v1/spaces/{space_id}/models` | 已有（[identity.go](../internal/transport/http/identity.go)） | 分页返回当前空间模型列表（支持 `available=true`，仅返回非敏感配置字段，不含 API Key 与密文） |
| `GET` | `/api/v1/spaces/{space_id}/tools` | **新增** | 返回平台受支持的内置 Tools 列表 |

### 4.2 `GET /api/v1/spaces/{space_id}/skills` 扩展参数

- **查询参数**：新增可选布尔参数 `include_description`（仅接受 `true`、`false` 或省略，默认 `false`；非法值返回 `400` `invalid_request`，与现有 `GET /models?available=...` 校验规则一致）。分页游标 `scope` 纳入 `include_description` 参数，避免不同查询模式混用游标。
- **默认行为（`include_description=false` 或省略）**：保持现有响应结构与性能完全不变，不额外读取 `SKILL.md`，响应项不返回 `description` 字段。
- **开启行为（`include_description=true`）**：
  - 为当前页的每个 Skill 提取 `description` 字符串并附加在列表项中（提取不到时返回 `""`，不因单个 Skill 缺少描述报错）。
  - 提取来源：优先读取该 Skill 最新非归档正式版本的 `SKILL.md`；尚未发布正式版本时回退读取当前草稿的 `SKILL.md`。
  - 提取规则：解析 `SKILL.md` 开头标准 YAML frontmatter（`---` 分隔块）中的 `description` 字段并去除首尾空白；无 frontmatter 或无 `description` 字段时返回 `""`。
  - 在 [openapi.yaml](../api/openapi.yaml) 中为 `GET /api/v1/spaces/{space_id}/skills` 补充 `include_description` 参数，并为 `Definition` 增加可选字段 `description`（`type: string`）。

### 4.3 新增接口：`GET /api/v1/spaces/{space_id}/tools`

- **数据来源**：由 `bootstrap` 将 [registry.go](../internal/runtime/tool/registry/registry.go) 的 `registry.Specs()` 注入 HTTP 传输层（满足 `scripts/check-boundaries.mjs` 架构边界约束，不让 `app`/`resource` 反向依赖 `runtime`）。
- **权限校验**：校验调用方具备目标 `space_id` 成员权限（与 `GET /api/v1/spaces/{space_id}/model-capabilities` 一致）。
- **响应结构（`200 OK`）**：仅返回 `name`、`version`、`description` 三个字段，不返回工具参数 `schema`。

```json
{
  "items": [
    {
      "name": "read_file",
      "version": "1",
      "description": "Read current file bytes and SHA-256..."
    }
  ]
}
```

- 同步在 [openapi.yaml](../api/openapi.yaml) 中补充 `GET /api/v1/spaces/{space_id}/tools`、`PlatformTool` / `PlatformToolList` Schema，以及这 5 个只读接口的 `BearerAuth` 声明，并重新生成前端类型 `api.generated.ts`；同步更新 [openapi-guide.md](openapi-guide.md) 补充这 5 个只读接口说明。

## 5. 安全边界与非目标

- **严格只读**：`run_read` Token 不能调用任何写接口（`POST`/`PUT`/`DELETE`），不能调用现有 5 个 OpenAPI 会话接口（防止在命令中反向创建会话或提交新 Run），不能读取 Skill 草稿（`GET /skills/{id}/draft`），也不能访问 OpenAPI Token 管理接口。
- **跨空间隔离**：即使 CLI 篡改请求路径中的 `space_id` 为其他空间，服务端仍因 `principal.SpaceID != space_id` 拒绝并返回 `403`。
- **单实例生效说明**：短时 Token 仅存于发起 `run_command` 的当前服务进程内存中，并通过本机回环 `MASS_API_URL` 回连同一进程；这与 MASS 当前单实例/本机执行 `run_command` 的架构一致，不引入额外外部状态存储。
- **不暴露给 `run_code_*`**：本文仅面向 `run_command` 下的 Skill CLI；WASM/JS 沙箱代码工具的隔离边界保持不变。

## 6. 验收标准

1. **自动空间绑定**：在空间 A 和空间 B 分别运行同一个 Skill 的 CLI，无需任何空间配置，CLI 分别获取到空间 A 和空间 B 各自的 `skills`、`skills/{id}/versions`、`agents`、`models` 与 `tools`（`{name, version, description}`）。
2. **可选描述字段**：调用 `GET /skills`（省略或 `include_description=false`）时响应不含 `description`；传 `include_description=true` 时返回从 `SKILL.md` frontmatter 提取的 `description`（无描述时返回 `""`）；传非法布尔值返回 `400`。
3. **用完即毁**：在 CLI 中将 `$MASS_RUN_TOKEN` 输出或保存，命令退出后再用该 Token 请求上述 5 个只读接口，均返回 `401`。
4. **跨空间拦截**：CLI 在空间 A 运行时，使用 `$MASS_RUN_TOKEN` 请求空间 B 的任意只读接口，返回 `403`。
5. **范围隔离与外部 OpenAPI 互通**：使用 `$MASS_RUN_TOKEN` 调用 `POST /sessions`、`POST /sessions/{id}/runs`、`GET /skills/{id}/draft`、`GET /openapi/tokens` 或 `POST /skills` 等非白名单接口，均返回 `401`；使用空间 `openapi` Bearer Token 可正常调用上述 5 个只读接口（跨空间同样返回 `403`）。
6. **零管理面污染**：执行多次 `run_command` 后，「空间管理 → OpenAPI」列表、`space_openapi_tokens` 表与 `audit_logs` 均不产生临时 Token 记录。
7. **回归兼容**：现有 OpenAPI 5 个会话接口、Cookie 鉴权与前端管理页行为完全不受影响。
