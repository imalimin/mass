# mass

Multi-use Agent Systems：支持空间协作、Agent/Skill 定义、版本管理与连续任务执行的单服务应用。

当前 D00—D10 的 MVP 建设及验收已完成，D07 真实模型与 D10 真实 TOS 恢复证据已补齐：可配置模型、发布 Agent/Skill、创建连续对话，使用全部十四个内置工具处理文件、已接受附件和固定版本 Skill 资源。支持 JavaScript 文件/HTTP/CSV 操作、计划展示、产物下载、Trace、取消和快照恢复；支持阶段摘要复用和原始事件 → 投影 Manifest → 实际请求的来源检查。

> 本文件提供项目简介、Quickstart 和建设文档入口。系统方案及工程验收继续由文末四份建设文档维护，不新增专题主文档。

## Quickstart

以下以本机 MariaDB `127.0.0.1:3306`、数据库名 `mass`、浏览器访问 `http://127.0.0.1:8080` 为例。命令均在仓库根目录执行。

### 1. 准备依赖并构建

- 构建工具：Go 1.26.9、Node.js 24.22.0、npm 11.19.1、Make。精确版本以 `.go-version`、`.node-version` 和 `frontend/package.json` 为准。
- 外部服务：MariaDB 11.8.9、可访问的 TOS Bucket；TOS 凭证需支持 Bucket 就绪检查及对象上传、读取。
- Go、Node.js 和 npm 用于构建；运行时只需 `bin/mass`、配置及外部服务，无需单独运行前端或 Node.js 服务。

```sh
node scripts/check-toolchain.mjs
npm --prefix frontend ci
make build
```

构建结果为 `bin/mass`，其中已嵌入前端页面。若已有适用于部署平台的二进制，可跳过构建步骤。

### 2. 创建数据库和数据库账号

先启动 MariaDB，再使用数据库管理员连接，例如 `mariadb -u root -p`。在 MariaDB 客户端中执行以下 SQL，将两个密码占位符替换为自己的密码：

```sql
CREATE DATABASE mass
  CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;

CREATE USER 'mass_migrator'@'127.0.0.1'
  IDENTIFIED BY 'REPLACE_MIGRATION_PASSWORD';
GRANT ALL PRIVILEGES ON mass.*
  TO 'mass_migrator'@'127.0.0.1';

CREATE USER 'mass_app'@'127.0.0.1'
  IDENTIFIED BY 'REPLACE_APP_PASSWORD';
GRANT SELECT, INSERT, UPDATE, DELETE ON mass.*
  TO 'mass_app'@'127.0.0.1';
```

`mass_migrator` 用于初始化和升级表结构；`mass_app` 用于日常运行。若已有满足权限要求的数据库和账号，可直接复用。服务与数据库不在同一机器时，需要相应调整连接地址和数据库账号的来源主机范围。

`mass migrate` 会建立业务表，但不会创建数据库或数据库账号；也不需要先启动 MASS 服务。

### 3. 准备配置和环境变量

首次复制配置模板，已有 `mass.yaml` 时保留原文件：

```sh
test -f mass.yaml || cp configs/mass.example.yaml mass.yaml
```

`mass.yaml` 中的 `*_env` 字段填写**环境变量名**，实际连接信息和密钥通过环境变量提供：

```yaml
database:
  dsn_env: "MASS_MARIADB_DSN"
  migration_dsn_env: "MASS_MARIADB_MIGRATION_DSN"
secrets:
  master_key_env: "MASS_MASTER_KEY"
```

仅首次初始化时运行下面的命令生成主密钥，并保存输出：

```sh
openssl rand -base64 32
```

将输出的完整字符串作为 `MASS_MASTER_KEY` 的实际值。它用于加密模型凭证，后续启动与升级必须持续使用同一个值，不能每次重新生成；已有部署直接使用原主密钥。

在仓库根目录创建 `.env`，填入真实值。以下内容是示例，所有 `REPLACE_*` 都需要替换；TOS endpoint、region 与 Bucket 应属于同一区域：

```sh
MASS_MARIADB_DSN='mass_app:REPLACE_APP_PASSWORD@tcp(127.0.0.1:3306)/mass'
MASS_MARIADB_MIGRATION_DSN='mass_migrator:REPLACE_MIGRATION_PASSWORD@tcp(127.0.0.1:3306)/mass'

MASS_TOS_ENDPOINT='https://tos-cn-guangzhou.volces.com'
MASS_TOS_REGION='cn-guangzhou'
MASS_TOS_BUCKET='REPLACE_BUCKET'
MASS_TOS_ACCESS_KEY='REPLACE_ACCESS_KEY'
MASS_TOS_SECRET_KEY='REPLACE_SECRET_KEY'

MASS_MASTER_KEY='REPLACE_WITH_GENERATED_MASTER_KEY'

# 服务端批准的模型兼容地址；使用 Ark 时保持此值。
MASS_LLM_MODEL_BASE_URL='https://ark.cn-beijing.volces.com/api/v3'
```

`.env` 和 `mass.yaml` 已被 Git 忽略。程序**不会自动读取 `.env`**，在执行后续命令的终端中手动加载：

```sh
set -a
. ./.env
set +a
```

也可以通过部署平台直接注入同名环境变量。若现有 TOS 变量名是 `TOS_ACCESS_KEY` / `TOS_SECRET_KEY`，可修改 `mass.yaml` 中对应的 `access_key_env` / `secret_key_env` 来复用。

当前 `check-config`、`migrate` 和 `user create` 共用完整配置校验，因此也需要提供应用 DSN、TOS 和主密钥变量；`migrate` 本身只连接 MariaDB。新模板的 `llm.base_url_env` 引用 `MASS_LLM_MODEL_BASE_URL`，填写兼容模型服务的 Base URL；该地址须使用 HTTPS。旧配置未设置 `base_url_env` 时继续使用默认 Ark 地址。模型 ID 与 API key 在每个空间的页面中配置并加密保存，运行不会自动导入环境凭证。`MASS_LLM_MODEL_ID` / `MASS_LLM_MODEL_KEY` 仅供显式真实模型冒烟使用。

### 4. 初始化表结构

```sh
bin/mass check-config --config mass.yaml
bin/mass migrate --config mass.yaml
```

依次看到 `configuration valid` 和 `migrations applied` 即表示配置格式有效、迁移已完成。`check-config` 不验证外部服务连通性。当前 schema 版本为 **7**；迁移文件已嵌入二进制，无需手工执行 SQL 文件。

### 5. 创建登录账号

将示例密码替换为自己的登录密码，长度要求为 **12—72 字节**：

```sh
export MASS_INITIAL_PASSWORD='REPLACE_WITH_A_STRONG_PASSWORD'

bin/mass user create --config mass.yaml \
  --username alice --password-env MASS_INITIAL_PASSWORD

unset MASS_INITIAL_PASSWORD
```

命令成功会输出用户 ID。MASS 登录账号与 MariaDB 数据库账号相互独立；目前通过 CLI 创建登录账号，页面不提供注册入口。

### 6. 启动并体验

```sh
unset MASS_MARIADB_MIGRATION_DSN
bin/mass serve --config mass.yaml
```

浏览器打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)，使用刚创建的账号登录。

可按以下流程体验：

1. 创建空间，在「成员与模型」中配置 Ark 凭证及可用模型。
2. 进入「Agent 与 Skill」，创建 Skill、编辑 `SKILL.md`、上传资源并发布 v1。
3. 创建 Agent，编辑 `AGENTS.md`，选择模型和准确的 Skill 版本，应用配置、保存草稿并发布 v1。
4. 修改 Skill 并发布 v2；旧 Agent v1 继续引用原 Skill v1。
5. 编辑 Agent，选择 Skill v2 后发布 Agent v2，查看旧内容、版本差异并切换默认版本。
6. 按任务选择工具，支持全部十四个选项。先体验文件任务可选 `read_file`、`write_file`、`publish_artifact`；组合任务再选附件、资源、搜索、计划及 `run_code`。输入 Token 上限须符合实际模型容量；长工具流程可在配置选择中调高，然后发布新版本或创建调试快照。
7. 进入「会话与运行」，选择 Agent 及发布/调试版本，新建会话。发送“写入 notes.txt，内容为 hello；读取确认后发布成产物”。可观察输出、工具状态，下载产物并展开「Trace 与上下文」。
8. 在同一会话发送“读取 notes.txt 的内容”验证跨轮文件延续。每轮停止后保存快照；关闭页面不取消后台任务，取消按钮会等待实际执行停止。
9. 使用启用了 `read_attachment`、`run_code`、`update_plan`、`publish_artifact` 的 Agent 新建会话，添加 UTF-8 JSON/CSV 附件后发送：“先更新计划，读取附件，用 JavaScript 汇总 amount，保存 summary.json 并发布产物，最后完成计划。”上传后须发送任务才接受附件；页面可查看计划、附件原文、工具结果及代码 I/O Trace。

会话固定创建时的 Agent/Skill 定义，后续修改默认版本不会改变已有会话。容量不足会明确报错，当前不自动压缩或截断上下文。文件写入/编辑限每文件 1 MiB UTF-8，读取返回至多 64 KiB 范围及摘要；附件每个最多 8 MiB，工具解析支持 UTF-8 文本、JSON、CSV。写入、编辑、移动、删除通过 `expected_sha256` 防止冲突；发布的产物保持原版本，即使源文件被删除也可下载。

启用 `run_code` 会授权代码读取/修改当前会话文件及发送 HTTP(S) 请求，无需逐项启用同名文件工具。代码为同步 JavaScript 函数体，支持 `fs`、`net.request`、`CSV`、`JSON`、`log`；不提供 Shell、Python、Node 或 OS 沙箱。默认每次 5 秒、源码 64 KiB、累计输入/文件读取 2 MiB、宿主输出 2 MiB、返回值 2 MiB、日志 64 KiB、100 次文件和 10 次网络操作；可通过 `tools.code` 配置限额。代码异常/取消保留已经确认的文件修改和请求效果，审计不完整明确报告未知，不自动重做。

`GET /health/live` 检查进程存活；`GET /health/ready` 检查数据库 schema 与 TOS HeadBucket，就绪结果有短时缓存。对象写入权限可通过页面上传资源验证。

### 7. 创建普通成员

在已加载环境变量的终端中，以独立密码创建第二个账号，例如 `bob`：

```sh
export MASS_INITIAL_PASSWORD='REPLACE_WITH_ANOTHER_STRONG_PASSWORD'

bin/mass user create --config mass.yaml \
  --username bob --password-env MASS_INITIAL_PASSWORD

unset MASS_INITIAL_PASSWORD
```

服务运行期间也可以创建账号。随后由空间 owner 在「空间成员」输入 `bob` 并添加，即成为该空间的普通成员，可管理 Agent/Skill，不能管理成员和模型凭证。

账号没有全局管理员/普通用户角色：空间创建者为该空间的 owner，加入已有空间的账号为普通成员；`bob` 自己创建空间时会成为新空间的 owner。

### 日常启动与升级

- 日常启动：加载原来的环境变量，执行 `bin/mass serve --config mass.yaml`；迁移环境变量可以不提供。重开终端后需要重新加载环境变量。
- 升级：保留数据库、TOS 对象、配置和原主密钥；停止旧服务，提供迁移 DSN 后用新二进制执行 `mass migrate`，再启动新服务。无需重复创建已有账号。
- 同一数据库只运行一个 MASS 执行实例；第二个实例无法取得运行锁。反向代理需允许长连接 SSE。
- 进程异常结束后，遗留运行会标记 `interrupted`，不会自动重跑模型或工具。未保存文件可能丢失；页面可恢复最近已提交快照，或在目录仍完整且无活跃运行时「仅重试保存文件」。快照失败会阻止下一轮。
- 离线重建：停止服务后执行 `bin/mass rebuild-views --config mass.yaml`。命令校验日志及 TOS 内容并重建运行、计划、产物及 UI 派生视图，不调用模型或工具；先保留数据库与 TOS 备份。
- `serve` 不自动迁移；数据库版本不匹配会拒绝启动。首次启动也必须先执行迁移。
- 对外部署：将 `http.listen` 设为部署所需的监听地址，`http.public_url` 设为浏览器实际使用的 HTTPS origin，设置 `auth.cookie_secure: true`，由 HTTPS 反向代理转发到 Go 服务。写请求会校验 Origin，地址和端口需与配置一致。

## 建设文档

| 顺序 | 文档 | 内容边界 |
| --- | --- | --- |
| 1 | [系统设计指引](docs/design-guide.md) | 用户功能、目标与产品规则，不涉及技术细节 |
| 2 | [系统架构设计](docs/architecture.md) | 详细架构、模块职责与协作，不涉及具体实现 |
| 3 | [技术方案](docs/technical-design.md) | 各模块实现、技术依赖、第三方库、数据/接口及核心代码 |
| 4 | [工程开发文档](docs/engineering.md) | 指导 AI 按顺序编码、测试、验收、运行与交付 |

建设顺序、验证证据和后续阶段要求以工程开发文档为准。

## TODOS

- [x] AGENTS.md、skill资源版本、Ageng运行时工程资源均采用git版本管理合理吗？
- [x] 按照当前方案，发送给LLM的上下文是从哪里来的，最原始的上下文记录是怎么管理的？
- [x] SSE采用AG‑UI协议
- [x] 技术方案的runtime模块粒度还是有点大，再细分下
- [x] MVP版本都支持哪些tool？

### 上下文与阶段摘要（D06）

新建 Session 使用 Agent 的 `context.compression: summary` 时，上下文达到输入预算的 85% 会尝试压缩完整旧轮次，保留当前请求和全部工具配对；`reject` 直接拒绝超限输入。摘要通过同一模型网关调用，禁用工具，消耗当前 Run 的调用次数与 Token 预算。保存并验证成功后才切换 epoch，后续轮次复用已保存摘要。D05 及更早已创建 Session 的固定定义没有压缩字段，继续采用拒绝策略；需要创建新 Session 才会使用新配置。

在“Trace 与上下文”中展开 `context.projected`，点击“检查上下文来源”，可查看 epoch、前缀变化原因、选取/省略事件、完整投影与实际模型请求。摘要及递归来源会保留，摘要响应不会显示为助手任务回复。大工具结果使用固定的 4 KiB 数据节选，完整结果仍可在 Trace 下载。

计数仍为保守估计。关键输入、当前轮工具链或摘要请求无法容纳时明确失败；摘要失败显示 `context_summary_failed`，容量失败显示 `context_overflow`。阶段前缀稳定不代表云端缓存命中，真实缓存收益验证属于 D07。

升级 D05 时，构建新二进制后先运行 `mass migrate --config mass.yaml`，再启动服务。开发验收使用 `make verify-d06`，需要显式设置隔离 MariaDB 的 `MASS_TEST_ADMIN_DSN`。

### 模型重试与观测（D07 实现中）

`llm.max_attempts` 可设为 1—3，省略时为 1。该策略固定到每个新 Run；开启后仅对尚未输出文本的 HTTP 429/502/503/504 进行有限重试，每次均重新检查授权、解析当前凭证、预留和结算预算。SDK 重试始终关闭。取消、协议错误、持久化失败以及部分输出不自动重试；已有工具不会因为模型重试重新执行。

Trace 的每个模型完成/失败事件保存 `observation`（调用耗时、首个文本片段延迟、HTTP 状态、组件版本），失败流仍保留原始 usage。无文本、无 HTTP 响应或缺失供应商统计时使用 null 或状态 0 表示未知，不推断缓存命中。原始供应商字段保留在实际响应及完整模型响应对象中。

D07 的真实文本、工具与摘要验收使用 `make smoke-real`，需明确授权及隔离测试配置；测试最多 8 次模型调用。缓存观测报告保存在忽略的 `.cache/d07-real-observation.json`。2026-10-24 已获授权并通过：实际 6 次 HTTP 模型请求（上限 8），真实文本、双工具、追加及一次摘要，563 个测试对象已精确清理；观测模型 deepseek-flash 的缓存读取和延迟，数据见工程文档 E15.8。这是本次样本结果，不代表所有请求必命中。

### 调试工作台（D08）

在 Agent 编辑页保存草稿、创建调试快照，或选择已发布版本后，点击“使用此版本开始调试”。页面会创建固定版本的 Session 并进入对话；之后修改草稿不会影响它。

“Trace 与上下文”提供事件分类、固定水位分页、原始载荷按需读取/下载和上下文来源导航；模型统计按任务/摘要及冷启动/追加/压缩阶段分组，区分未知缓存统计和明确的零命中。首个文本片段延迟不等同于供应商内部推理耗时。

连接中断后，前端先检查授权，再从最后成功应用的 UI 游标继续；重复事件不重复应用，自定义成功状态之后仍接收标准终态事件。连续恢复失败五次后停止自动重连，可点击“重新同步事件”取得新的成对快照与游标。空间切换和权限失效清理订阅与私有内容。Markdown 禁止原始 HTML、远端图片和非 HTTP(S) 链接；载荷在线预览最多 1 MiB，运行输出按 50 条窗口展开。

`make verify-d08` 只使用隔离数据库与本地供应商夹具，包含既有阶段回归及调试闭环浏览器测试；不代替仍待授权的 D07 真实云验证。数据库 schema 保持 7，无新增迁移。

### 故障与隔离验收（D09 本地）

设置隔离 MariaDB 的 `MASS_TEST_ADMIN_DSN` 后运行 `make verify-d09-local`。该目标串联 D00—D08 回归、故障进程测试、全量竞态检测及 A01—A51 证据引用检查，只使用本地假供应商和 TOS 协议夹具；该目标显式关闭真实 TOS/模型测试开关，避免继承环境配置意外触发云调用。故障操作只作用于随机测试库的应用连接池及测试自行启动的 `mass` 子进程。

新增验收包括：数据库不可用时停止接单、未提交终态不释放占用、恢复后 queued 继续且 running 不重放；两个已确认文件写入后 SIGKILL，重启不重做未知动作；部分模型输出超时、Trace 存储失败、上传响应丢失核实；真实 Goja 文件操作后撤权、系统收尾与下载/历史拒绝；认证到期后历史、快照和产物仍保留，损坏/缺失内容阻止完整重建；同 Run 摘要和重试统一记账，未知用量不当零。

证据索引为 `scripts/acceptance-evidence.json`，检查报告输出到忽略的 `.cache/d09-acceptance.json`。引用检查只核对用例与源文件，不代替功能测试。A21/A31 的 D07 真实模型与缓存观测已于 2026-10-24 补齐；A22 的干净部署/备份还原已在 D10 补齐真实 TOS 验收；A25 使用此前真实 TOS 验收证据，本阶段没有再次调用云资源；A33 显式缓存未启用。因此本地通过不等于全部 MVP 验收完成，当前交付和完整门槛见下文 D10。

数据库 schema 保持 7，无新增迁移。数据库/日志故障导致执行入口停止时，先修复依赖并检查已保存记录，再重启；已有 running 会标记中断，未确认副作用须人工核实。不要直接重放旧 Run。快照保存失败时使用已有“仅重试保存文件”或“恢复最近已提交快照”，后者会放弃未保存文件。

## D10 交付包与恢复演练

```sh
make setup                 # 首次构建机准备；安装锁定依赖及测试浏览器
make release               # 生成 API、嵌入前端，CGO_ENABLED=0 构建当前主机平台
bin/mass build-info
```

产物为 `.cache/release/mass-<os>-<arch>.tar.gz` 及同名 `.sha256` 校验文件。压缩包内有 `bin/mass`、唯一配置模板 `mass.yaml`、README、四份文档及图片、`build-info.json`、逐文件 SHA-256 `manifest.json`。不打包工作区中的真实 `mass.yaml`、环境变量或凭证。相同源码、锁定工具链、`VERSION` 和 `SOURCE_DATE_EPOCH` 会生成相同字节；默认使用 Git 提交时间作为构建时间，未提交源码的版本含 `dirty`。目前打包入口支持构建主机的平台，不宣称已验证跨平台产物。

部署机器解压后按 Quickstart 配置环境、执行 `bin/mass migrate`、`bin/mass user create` 和 `bin/mass serve --config mass.yaml`。仅应用进程不需要 Go、Node、npm 或仓库；MariaDB/TOS 是外部依赖。`serve` 不自动建库/迁移。用 `kill -TERM <mass-pid>` 停止并等待退出后再备份或升级，避免直接覆盖运行中的二进制。

停服维护时还可管理已有用户的空间成员关系；数据库操作者需指定现有空间 owner 的 ID，仍检查 owner 权限并写审计。运行期间通过页面/API 管理成员。

```sh
bin/mass member add --config mass.yaml --actor OWNER_ID --space SPACE_ID --username bob
bin/mass member remove --config mass.yaml --actor OWNER_ID --space SPACE_ID --user USER_ID
```

备份需要停服、无排队/运行中的任务，保留 MariaDB 导出、对应 TOS 对象的全部版本，以及单独安全保管的原主密钥。仅复制会话临时目录不能恢复业务。完整步骤和故障处理见[工程文档 E15.11](docs/engineering.md#e1511-d10-本地交付与备份还原)。恢复到隔离库后，可用同一二进制复制留存对象到独立前缀并校正引用：

```sh
bin/mass restore-storage --config restored.yaml --source-config original.yaml --expect-database mass_restore
bin/mass rebuild-views --config restored.yaml
bin/mass serve --config restored.yaml
```

`original.yaml` 和 `restored.yaml` 使用同一份模板的结构，各自引用原库/恢复库及源/目标 TOS 环境；源库不会被此命令连接或修改；源 TOS 也可指向保留了原对象键和字节的备份 Bucket。目标库必须是独立库名、对象前缀必须互不包含；命令核对主密钥、日志、对象大小和 SHA-256，所有复制成功后一次性提交位置索引，支持重试，不删除源对象。历史日志保留原始 URI，当前读取以校正后的对象索引为准。缺失对象或未完成上传会明确拒绝，不能跳过后启动服务。

本地演练需隔离 MariaDB 及 MariaDB 客户端工具；不调用真实云资源：

```sh
export MASS_TEST_ADMIN_DSN='root:test-password@tcp(127.0.0.1:3306)/'
# mariadb / mariadb-dump 不在 PATH 时，用 MASS_TEST_MARIADB_CLIENT / MASS_TEST_MARIADB_DUMP 指定绝对路径。
make verify-d10-local
```

本地验证包括两次完整构建的字节对比、空库迁移、压缩包二进制以空 PATH 启动、发布 Skill/Agent、运行和 Trace/产物查看、真实 SQL 导出还原、TOS 协议夹具对象迁移、错误主密钥/损坏数据/运行中服务拒绝、历史无执行重建，以及原临时目录和源对象删除后的连续对话。报告在忽略的 `.cache/d10-delivery.json`。D07 真实模型与 D10 真实 TOS 恢复已验收；`make verify` 执行当前本地全套，并检查 A01—A51 的受控真实验收记录，普通 CI 不重复付费调用。需要重新执行真实云测试时，单独授权并配置 `MASS_VERIFY_REAL_APPROVED=1 make verify-live`；此前授权不代表可反复使用模型额度。


D10 真实恢复入口为 `make smoke-recovery`，读取现有 MASS_TOS_ENDPOINT/REGION/BUCKET/ACCESS_KEY/SECRET_KEY 及专用 MASS_TEST_ADMIN_DSN。命令构建交付包，仅在 `tmp/mass-d10-随机ID/source/` 与 `restored/` 写入合成测试内容，不使用真实模型；从清单逐个清理本次 key/版本并确认不可读，不列出或删除 Bucket 中其他内容。2026-10-24 实测 22 个对象还原成功，57 个独立键完成清理，真实模型请求 0；删除源对象和原临时目录后，交付二进制仍可查看历史、Trace、产物并从快照继续。报告 `.cache/d10-recovery-real.json`、精确清理账本 `.cache/d10-recovery-mass-d10-UUID.json` 被 Git 忽略；异常时保留账本以核对清理，不保存存储客户端或凭证。真实跨 Bucket 恢复仅在本地协议夹具验证，本次云验收是同一 Bucket 的隔离前缀。
