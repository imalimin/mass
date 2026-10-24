# MASS 工程开发文档

> 文档约束
> - 本文回答：AI 应按什么顺序完成编码、验证、集成和交付，怎样判断每一步完成。
> - 包含：建设阶段、前置条件、具体任务、交付物、测试门槛、验收清单、运行交付步骤和进度证据。
> - 不包含：替代用户目标或维护另一套技术方案；发现缺失细节应先补入技术方案再实施。
> - 输入依次为[系统设计指引](design-guide.md)、[系统架构设计](architecture.md)、[技术方案](technical-design.md)。所有开发计划、验收和操作说明集中在本文，不再拆计划、测试或操作主文档。
> - 状态：D00—D06 已完成；既有阶段兼容回归通过，D07 实现中、真实验证待授权；D08 页面与本地验收已完成；D09—D10 未开始；更新：2026-10-24。当前实现与验证证据见 E15.1—E15.9，后续阶段的命令和路径仍为待建立约定。

## E00 四份文档与 AI 执行规则

### E00.1 唯一职责

| 文档 | 维护内容 | 修改触发条件 |
| --- | --- | --- |
| 系统设计指引 | 用户目标、功能和产品边界 | 用户需求或范围改变 |
| 系统架构设计 | 层级、功能模块、职责和协作 | 模块关系或功能设计改变 |
| 技术方案 | 实现、技术依赖、数据/API 契约说明和核心代码 | 实现方案、选型或契约改变 |
| 工程开发文档 | 阶段、编码任务、测试、交付、运行说明和进度证据 | 任务推进、验证或交付方式改变 |

`docs/` 只维护这四份建设文档。README 仅保留项目简介和四份入口；代码中的机器可读契约、迁移、测试夹具及生成物不是额外建设文档。新主题在对应文件增加章节，不另建分类规范、模块专题、计划或验收主文档。

各文档头部持续声明内容边界。技术细节不写入设计指引，具体实现不写入架构。允许摘要引用，不复制整段规则多处维护；功能约束冲突时以上游目标为输入并同步修订，不用更新时间决定覆盖关系。

### E00.2 每次接续开发的流程

1. 读取四份文档的头部、当前阶段和目标模块章节，检查实际代码、工作区变更与已有验证记录。
2. 找到第一个前置条件满足且未完成的阶段，不因文档写了“应该有”就假定代码已存在。
3. 把本阶段拆成可验证的小步；先确定契约和失败语义，再完成真实代码路径。
4. 每步完成业务实现及必要验证，失败时定位原因并修复；测试不通过不能把依赖阶段标为完成。
5. 更新技术方案中实际改变的内容，在本文件进度表记录代码位置、执行命令、环境和结果。
6. 当前阶段通过后，在用户已授权的实施范围内继续后续阶段，不逐步重复索要确认。
7. 真实凭证等外部输入缺失时记录阻塞点，继续不依赖它的工作；不得把假实现标为真实接入成功。

本文件指导后续已获授权的开发工作；整理文档本身不等于用户已要求立即编码、购买服务或发布到外部环境。未经明确要求不另开任务、部署到外部服务或发送通知。

### E00.3 完成定义

完成必须有真实实现、必要的边界测试、可运行入口与验证证据。空目录、只有接口、TODO、返回固定成功或仅通过 Mock 的业务路径不算完整交付。

测试覆盖行为与关键不变量，不为纯粹可逆的样式或文字修改堆积镜像测试。数据库并发行为使用真实 MariaDB；对象存储使用专用 TOS 测试前缀完成集成验证，内存替身只用于单元测试；模型协议使用可控假服务做确定性验证，真实模型另做最小冒烟。

## E01 阶段总览与依赖

| 阶段 | 对应模块 | 前置条件 | 阶段产出 | 当前状态 |
| --- | --- | --- | --- | --- |
| D00 工程与契约基础 | M08/M09/M13 | 四文档基线 | 可构建服务/页面、依赖锁定、Eino 传输与 goja 宿主接口验证 | 已完成，2026-10-18，见 E15.1 |
| D01 数据、文件与事件基础 | M10/M11/M13 | D00 | MariaDB/TOS、会话目录与快照、会话日志、基础迁移 | 已完成，2026-10-18，见 E15.3 |
| D02 身份、空间与模型配置 | M01/M03/M12 | D01 | 登录、空间/成员页面与 API、最小模型/凭证配置及隔离 | 已完成；D01 补齐后兼容回归通过，见 E15.2/E15.3 |
| D03 定义与版本闭环 | M02/M12 | D02 | Agent/Skill 编辑、发布、引用与版本查看 | 已完成，2026-10-18，见 E15.4 |
| D04 最小运行纵向闭环 | M04—M11 | D03 | Eino 接入、原文 Trace、最小投影、读写文件/产物、取消和快照恢复 | 已完成，2026-10-23，见 E15.5 |
| D05 内置工具闭环 | M09/M11 | D04 | 补齐 14 个工具、代码文件/网络能力、计划/交付与 I/O 记录 | 已完成，见 E15.6 |
| D06 上下文完整能力 | M07 | D05 | 确定性投影、阶段摘要、稳定前缀、大结果和来源检查 | 已完成 |
| D07 模型能力完善与真实验证 | M03/M08 | D06 | 多工具/摘要、凭证撤销/轮换、异常协议与真实缓存观测 | 实现中；真实验证待授权，见 E15.8 |
| D08 完整调试工作台 | M12 | D07 | 版本化调试、SSE 续传、完整输入输出与产物 | 页面与本地验收已完成；D07 真实验证另行待授权，见 E15.9 |
| D09 故障与隔离验收 | 全部 | D08 | 并发、取消、故障、权限及快照恢复证据 | 未开始 |
| D10 构建与交付 | 全部 | D09 | 可重建产物、启动与备份恢复验证 | 未开始 |

主路径为 D00 → D01 → D02 → D03 → D04 → D05 → D06 → D07 → D08 → D09 → D10。页面伴随所属业务阶段实现，不把全部 UI 拖到最后；D08 负责整合复杂调试体验。D00 先验证所选 Eino 组件可注入 HTTPClient、捕获实际字节并控制重试；D02 提供模型配置，D04 首次集成真实适配器，不把这一风险拖到 D07。每个子步骤单独实现、验证后再进入下一步；下面的拆分只表达工程依赖，不要求多 Agent 并行。

## E02 D00 工程与契约基础

**输入**：技术方案 T00、T06.1—T06.2、T13—T15；仓库现有内容。

**编码步骤**：

1. 检查已有工具链，选择仍受支持且满足依赖要求的 Go、Node.js 与 MariaDB 版本，写入版本配置和技术方案，锁定 Eino、模型扩展组件、TOS SDK、goja、doublestar、AG-UI Go SDK 与 @ag-ui/core 等第三方依赖，并验证 AG-UI 必需事件子集的跨语言兼容性。
2. 初始化 Go module、cmd/mass 与统一 mass.yaml 配置加载；建立单进程启动/停止入口和 live/ready 检查骨架。按 T06/T15 确定包依赖规则，叶子包随能力实现创建，不用空目录或通用 RuntimeService 代替实现。
3. 在 ./frontend 初始化 React/TypeScript/Vite 及独立 package.json/锁文件，建立路由、应用壳与空间占位；实现构建产物复制和 Go embed，页面、API、SSE 使用同一 Go 地址，默认开发不另起前端 HTTP 服务。
4. 建立 OpenAPI/事件契约目录、SQL 查询和迁移工具配置；先定义错误格式、ID、分页、幂等键和序号传输约定；AG-UI 的 type/camelCase、CUSTOM 约定、协议版本和独立 UI 游标纳入机器可读契约。
5. 建立统一 Makefile/scripts 入口及 CI 骨架；CI 使用固定版本，不依赖开发者私有环境。
6. 优先使用用户指定供应商；未指定时依据所需工具/流式能力明确首个组件候选并在 T08 记录选择，不把这一决定留到 D07。做两个小型依赖验证：所选真实 Eino 组件连接本地 httptest 供应商，验证序列化、流式/非成功响应原文捕获与关闭隐藏重试；goja 注入临时目录 fs 和本地 HTTP net 接口，验证取消、连续读取及禁用 source map 外部加载。仅接口 fake 不能代替依赖编译和协议试验，发现组件不支持应先调整适配设计。

**验证**：单个 Go 二进制能启动停止并提供页面与 API，运行不需要 Node.js；前端类型检查与构建通过；健康接口响应；缺关键配置明确失败，示例配置没有秘密。

**完成条件**：干净环境可以重复安装和构建。此阶段尚无登录或 Agent 功能，不宣称产品可用。

## E03 D01 数据、文件与事件基础

**输入**：D00；T00.3、T10、T11、T13。

**编码步骤**：

1. 建立 MariaDB 连接池、database/sql 事务封装及同一二进制的 migrate 子命令，区分迁移与应用账号。
2. 实现基础用户/空间引用结构及 content_objects 元数据；后续业务表按相应阶段逐步迁移，不一次塞入未定义字段。
3. 使用 TOS SDK 实现对象上传/读取、上传意图与 ready 状态、不可变写入及摘要检查；实现 Session 临时目录、全量 tar.gz 快照、Manifest 和 workspace_snapshots 索引，支持 TOS 恢复与路径边界检查。不实现自动 GC/过期策略。
4. 实现 SessionJournal.Append、runtime_events/event_objects、Session 级序号、append_key/event_id 幂等、格式版本校验及分页；建立 Session/Run 和活跃占用最小表结构及统一锁顺序，验证日志与必要视图同事务提交，完整业务 reducer 在 D04 实现。
5. 建立专用 MariaDB 测试库、TOS 隔离测试前缀和内存对象替身；清理仅限本次测试对象。

**验证**：迁移空库成功，重复执行无变化；事务失败回滚；同 Session 跨 Run 并发事件序号唯一有序；同 ID 同内容重试不重复、不同内容冲突；事件与视图共同回滚；数据库失败不会留下可见的缺失对象引用；跨空间对象访问拒绝；TOS 失败不把临时目录当作持久化成功；连续写入/打包/删除临时目录/恢复后内容与空目录一致，非法归档和越界链接拒绝。

**完成条件**：关键持久化接口有真实 MariaDB/TOS 实现与集成证据，测试数据与生产数据隔离；缺少 TOS 凭证时可继续编码与单元测试，但不得将真实存储接入标记为已验证。

## E04 D02 身份、空间与模型配置

**输入**：D01；T01、T03、T12、T14。

**编码步骤**：

1. 实现 mass user create 初始化账号、bcrypt 密码摘要、密码校验、随机会话 Cookie、退出和身份查询。
2. 实现 T01 的空间创建/列表与成员查看/添加已有用户/移除 API，创建者为 owner，禁止移除 owner；接入统一授权，不建立细粒度角色矩阵。
3. 前端完成登录、退出、空间创建/切换和成员管理页面；用两个预置账号验证通过页面添加和移除成员，不能用 CLI 替代这项交付。
4. 完成登录基础限速、同源写请求 Origin/Content-Type 检查、会话到期和成员撤销处理；不引入 JWT 刷新、独立认证服务或 CSRF Token 子系统。
5. 对所有已有资源读写接入空间授权和 Repository 条件；实现成员撤销通知与 Run 主体复核接口，D04 接入 Supervisor。401/403 明确返回，前端停止重连，无等待授权/自动恢复任务状态。
6. 实现最小 model_configs/credentials、AES-GCM、owner 配置 API 和基础页面，校验 D00 已验证组件的能力并提供可用模型查询。D03 的定义绑定此配置，D04 直接使用；轮换等完整场景在 D07 验证。

**验证**：至少两个用户、两个空间，验证成员与 owner 的基本边界；错误密码、跨空间 ID、无登录、过期会话、跨站写请求、移除 owner 均有结果断言。

**完成条件**：浏览器真实完成登录→创建空间→添加/移除已有用户→配置模型→权限内操作→退出；后端拒绝越权，不依赖前端按钮隐藏。

## E05 D03 Agent/Skill 定义与版本闭环

**输入**：D02；T02、T11、T12、T14。

**编码步骤**：

1. 建立 Agent/Skill 元数据、草稿 revision、版本、依赖及资源引用迁移。
2. 实现内容编辑、资源上传、严格配置校验及乐观锁。
3. 实现发布事务、准确 Skill 版本解析、默认版本切换、归档和反向引用查询。
4. 实现不可变调试快照创建及读取，为后续 Run 做准备。
5. 实现 Agent/Skill 列表、编辑器、版本选择、差异和发布页面。

**验证**：两个 Skill 的不同版本可绑定；发布 Skill 新版不改旧 Agent；同版本重复发布被拒绝；并发编辑冲突明确；缺失/越权依赖不能发布；被引用版本不能删除。

**完成条件**：真实浏览器完成 Skill v1→Agent v1→Skill v2→Agent v2，并能查看旧版本内容。调试按钮可以尚未开放运行，但不能伪造调试成功。

## E06 D04 最小运行纵向闭环

**输入**：D03；T04—T06、T08、T10。此阶段先提供只从统一日志读取的最小确定性投影与明确容量拒绝，不临时静默截断。

**编码步骤**：

1. 在 domain/journal/app 实现 session.created、user.message.accepted、run.created、Run/消息 reducer、幂等与会话占用；先完成 Claim/Finish 的统一状态规则、固定锁顺序和响应来源校验。
2. 实现 runtime/control 的停止检查、调用额度预留与结算；用已记录用量初始化 Run 状态，区分未知和零，固定重复结算的幂等语义。
3. 按 Eino 契约在 adapter/eino 实现可编程 FakeChatModel；接通 runtime/modelgateway 的 attempt、聚合、完整性、失败记录和 control 接口。摘要与任务共用调用契约，假模型记录注明来源；将 D00 验证过的实际 Eino 组件和 HTTP 原文捕获接到同一 Gateway，以本地 HTTP 供应商验证真实序列化路径；具备授权凭证后立即完成最小真实文本冒烟，D07 不再承担首次模型接入。
4. 先实现 context/projector 的固定边界最小只读投影，再实现 context/planner 的容量拒绝、投影内容保存与事件登记。此阶段不做隐式摘要或静默截断；Projector 不依赖 Gateway。
5. 实现 runtime/executor 的窄接口及唯一 ReAct 循环，文本回复只返回 Outcome，不写 Run 终态。先接通 read_file/write_file/publish_artifact 的最小真实路径及鉴权下载；尚未实现的工具拒绝执行，不假造成功。
6. 实现 runtime/supervisor：Run context、取消/心跳、成功与失败统一收尾、工作目录快照保存、panic 恢复、权限撤销报错停止、取消与成功竞态、有界收尾及名额释放；随后接入 runtime/scheduler 的单实例锁、并发准入、事务认领、交接和轮询补漏。
7. 在 bootstrap 完成具体实现注入与启停；实现 AguiProjector、agui_streams/agui_events、映射版本、UI 序号和事务检查点，提供 Trace/Run 查询、SSE 及基础页面。AG-UI 不反向驱动执行。
8. 由 Supervisor 实现异常退出识别、operation.reconciled 与中断终结；实现连续 Run 复用目录、重启恢复及新 Session 指定快照复制，快照保存失败阻止新 Run，并提供只重试保存的 API。建立离线视图重建入口，不重放动作。

**按顺序交付的小步**：D04a＝提交/状态/最小投影与假模型文本；D04b＝真实 Eino 传输和完整 Trace；D04c＝最小文件工具/产物与 Run 快照、下一轮恢复；D04d＝调度监督、取消/撤权/重启与基础 AG-UI。每步都有协议或集成验证，D04 全部完成后再扩展工具；D04a—c 可在测试用例中同步驱动执行器，只有 D04d 才开放完整后台运行入口。

**验证**：重复请求只产生一个 Run；同会话并发拒绝；断开客户端不取消运行；queued/running 取消按实际状态处理；模型和日志写入失败可观察；原始 Session 序号可跳跃，一对多 AG-UI 输出使用连续 UI 序号恢复；清空派生视图后可从日志重建同样业务结果且不触发外部调用。

**完成条件**：真实服务、MariaDB/TOS 与实际 Eino 适配器跑通“输入→读写文件→产物→快照→下一轮读取”；FakeChatModel 和本地 HTTP 供应商提供确定性回归，授权真实模型至少完成文本冒烟。缺少真实模型凭证时如实保留验证缺口，可继续后续不依赖该凭证的工作，不标记 D04 全部完成。T06 的消费接口可分别使用小型 fake 验证；依赖检查无反向 import，认领交接/取消/成功竞态不重复终结或释放名额；收尾失败不提前删除会话占用。

## E07 D05 内置工具闭环

**输入**：D04；T06、T09、T11。

**编码步骤**：

1. 扩展 D04 的 Registry 与最小工具链，完成全部 14 个工具的版本/参数/结果 Schema 与授权子集；统一实际副作用、快照持久状态和 I/O 记录，空 Handler 不算完成。
2. 补齐 list_files/edit_file/move_file/delete_file 并完善 read_file/write_file；实现 expected_sha256、精确匹配、Session 目录锁及单文件替换；文件操作与 Journal 不能假装是共同事务。
3. 实现 glob_files/search_files，在当前 Session 的真实目录内遍历，保存有界清单、查询结果和游标，验证跨页稳定及扫描上限。
4. 实现 list_skill_resources/read_skill_resource/read_attachment；补齐附件上传及已接受输入的绑定，只解析已声明的文本/JSON/CSV 格式，保存来源与解析版本。
5. 实现 adapter/javascript 的 CodeRunner/run_code Handler：独立 VM、当前 Session 文件接口、net.request、I/O prepared/completed/failed 记录、取消等待及容量限制。支持循环连续读取和即时写入，异常保留已发生效果；网络用可控本地 HTTP 服务验证，不以外部公网依赖阻塞测试。测试进程隔离不等于生产沙箱。
6. 实现 update_plan 并完善 D04 的 publish_artifact、plan.updated/artifact.published、事务内视图和幂等；完成鉴权下载，不将可变路径作为交付物引用。
7. 接入 AG-UI TOOL_CALL_START/ARGS/END/RESULT 与 CUSTOM mass.plan.v1/mass.artifact.v1；快照保存同水位计划/产物，代码日志通过 Trace 引用查看。投影只加入工具结果，不再重复插入业务事件或当前视图。
8. 用 FakeChatModel 驱动“附件/Skill → 搜索与读取 → 更新计划 → run_code 计算 → 编辑/整理 → 发布产物 → 最终回复”，覆盖全部 14 个工具的独立行为，并补错误、取消、超限和提交结果未知用例。

**按顺序交付的小步**：D05a＝文件发现/编辑与只读资源；D05b＝goja 文件/网络接口及 I/O 审计；D05c＝计划/产物 UI 和所有工具组合；每步先通过独立行为与故障用例，再串联完整流程。

**验证**：跨空间/Session、未接受附件、版本越权、绝对路径/穿越/链接访问均拒绝；当前目录连续读取及授权网络访问成功。编辑歧义不写入、跨页查询稳定；代码先写文件/发请求后异常，已发生效果仍在且 Trace 忠实记录，不能断言全部回滚；日志不完整显式标记。计划/产物事件与工具结果共同提交且不重复进入上下文；原文件修改或删除后已发布产物仍可下载原版本；工具结果未知先核实，不自动重做。

**完成条件**：14 个工具均有实际实现、行为测试和可审计闭环；完成真实 TOS/MariaDB 集成与产物下载验证。JS 引擎的实测限制与支持子集有记录，不声称强内存隔离；有 run_code 内置网络能力，无 Shell、Python、任意系统脚本、独立自定义 HTTP、MCP、浏览器或联网搜索工具。仅通过 fake 不能标记真实资源接入完成。

## E08 D06 上下文完整能力

**输入**：D05；T07、T08、T10。日志、内容与事件格式契约已具备，D04 的最小投影可以运行。

**编码步骤**：

1. 扩展 context/projector：固定 through_sequence、版本化策略/渲染/解析器和有效配置，只读取日志及不可变对象；由 context/planner 保存投影内容、context_projections 和 context.projected，Projector 不写入。
2. 实现完整交互单元、失败/未知副作用投影规则及准确事件来源；摘要调用与用户任务分开，不重复用户、最终回复或流式片段。
3. 实现 ContextPlanner 的容量规划和 context_epoch；通过同一 ModelGateway 生成 purpose=summary 的摘要，显式固定摘要输入并禁用工具，禁止 Gateway 回调 Planner；保存准确来源、内容摘要和生成调用，完成验证后切换阶段，后续投影复用结果。
4. 实现固定的大结果节选、模型协议字段保留、ordered_block_refs、配置/工具指纹及 parent_projection_id；同阶段正常追加不改写已有前缀。
5. 实现投影索引/检查点与有界读取缓存；缓存键包含空间、历史边界及全部语义版本，读取仍校验授权。
6. 实现原始事件→投影 Manifest→实际请求的检查 API 和展示，保存 epoch 和 prefix_change_reason。

**验证**：相同原始记录与显式输入重投影得到相同模型可见内容；当前时间和 map 插入顺序变化不改变指令/工具顺序；不读取草稿或页面消息视图；一组工具结果缺失时按规则拒绝或转换，禁止假成功。强制小容量触发一次阶段压缩，原文与摘要来源保留，摘要生成调用进入日志且不作为任务回复；后续容量充足的追加轮次复用摘要和前缀。必要输入超限、摘要失败及未知事件版本明确失败。

**完成条件**：确定性、配对完整性、失败历史和前缀稳定性有确定性测试证据；多轮 FakeChatModel 可验证阶段切换和大结果处理。此阶段不能声称真实模型缓存已命中。

## E09 D07 模型能力完善与真实验证

**输入**：D06；T03、T07.5—T07.6、T08。需要一个明确供应商协议、可用模型和测试凭证。

**编码步骤**：

1. 沿用 D00 锁定、D04 接通的首个供应商组件及协议，确认真实测试环境与预算；如用户更换供应商，先重新通过 D00 的兼容性验证。缺凭证只阻塞真实调用，其他实现继续推进。
2. 基于 D02/D04 的模型配置、AES-GCM 凭证与已接通 Gateway，补齐轮换/禁用及后台使用授权回归。
3. 完善已接入 Eino 组件的 ToolCallingChatModel、WithTools 和 Stream 路径，覆盖多工具、摘要、能力检查、协议状态保存、usage、超时与显式重试；由 MASS 执行器统一管理循环，不叠加另一套完整 Agent 循环。
4. 复用 D00/D04 已验证的 HTTPClient/RoundTripper 与关闭隐藏重试配置，扩展异常协议矩阵；用本地 HTTP 假供应商验证实际序列化请求体、响应帧、非成功响应、非法 JSON 和连接中断，检查回调语义事件与 TOS 原文对应；AG-UI 增量只能从已持久化响应派生，跨字节块和协议帧解析正确，完整响应不重复输出文本。
5. 归一化实际供应商缓存 usage，保留原字段，未知值不得补零；比较模型适配后的稳定前缀，排查 SDK 序列化产生的变化。
6. 获得授权凭证后执行最小真实文本、工具调用及摘要冒烟；在预算内测量冷启动、正常追加和压缩切换的缓存读取 token 与延迟，记录模型/配置/时间和统计覆盖率。
7. MVP 不要求所有供应商支持显式缓存；若启用，先验证 Eino 组件/API 能力，再实现作用域隔离、有效期与失效回退，保留远端引用对应的完整逻辑上下文。

**验证**：记录的请求体等于实际发送字节；认证头不进入 Trace；失败响应原文保留；已输出部分内容后的重试不会拼接；撤销凭证后后续调用拒绝；缓存统计可核对原始 usage，不用前缀 hash 推断命中。显式缓存启用时验证失效后重建输入，不重复用户消息或工具副作用。

**完成条件**：协议测试和最小真实冒烟都通过，并有实际缓存观测结果或供应商不支持/未返回的证据；不以每次必命中作为门槛。只有假供应商测试通过时标记“实现完成、真实验证待完成”，不能标记全阶段完成。

## E10 D08 完整调试工作台

**输入**：D07；T10、T12、T14。若 D07 仅凭证验证阻塞，可以先完成本阶段不依赖真实供应商的页面工作。

**编码步骤**：

1. 串联草稿快照、固定会话、Run 提交、取消和状态展示。
2. 完成统一日志时间线、原始事件到投影/实际请求的来源导航、epoch/前缀变化、工具结果、用量和产物组件；缓存统计区分未知、未命中及冷启动。
3. 实现 MASS 提交/GET 订阅适配器，按 @ag-ui/core Schema 消费事件；完成 UI 级 SSE 游标、去重、应用后确认、受控重连、水位一致的 ui-snapshot 和终态停止订阅，不将原始 Session sequence 当 UI 游标。
4. 完成空间切换清理、过期会话、成员撤销、权限错误和空状态。
5. 大载荷按需加载，历史分页，编辑器延迟加载，Markdown 受控渲染。

**验证**：Playwright 覆盖发布→运行→工具→查看完整 Trace；断线续传无丢失/重复展示，覆盖一条日志生成多个 UI 事件时中途断线；排队取消、错误、消息中断和工具参数结束不被误判成功；空间切换不混入旧事件；调试中编辑草稿不改变当前快照；下载产物校验权限；计划 revision/产物清单在断线恢复后与同水位快照一致，计划完成不被误判为 Run 成功。

**完成条件**：用户可通过页面完成整个闭环，无需手动调用内部接口修补数据。

## E11 D09 故障、隔离与回归验收

**输入**：D08；本文件 E14 验收矩阵。

**实施步骤**：

1. 执行数据库并发、幂等、取消竞争、空间隔离和资源边界测试。
2. 故障注入：数据库不可用、Trace 保存失败、模型超时、响应中断、TOS 不可用、上传结果未知、工具结果未知。
3. 强制退出进程并重启，验证 queued 可继续、running 被标记中断、无自动副作用重放。
4. 验证归档引用、摘要历史完整性、大载荷、工作目录快照失败与重试保存；删除临时目录后从 TOS 恢复。时间推进不导致业务历史/快照/产物消失，不建设生产孤儿回收或过期系统。
5. 检查关键页面、权限反馈、日志脱敏和运行记录完整性；验证缓存命中也不能绕过权限，历史内容缺失/损坏时明确不能重建。
6. 修复问题后重跑受影响检查，再进行一次完整回归；结果记录环境和代码版本。

**完成条件**：验收矩阵中的 MVP 项全部有证据。未支持的高级能力明确标为非目标，不能作为绕过核心测试的理由。

## E12 D10 构建、运行与交付

**输入**：D09；T11、T13、T15。

**实施步骤**：

1. 建立可重现构建：锁定依赖，生成 API 类型，构建 frontend 并复制到 Go 嵌入目录，打包单一 mass 二进制并携带构建标识。
2. 提供唯一配置模板 mass.yaml，以及同一二进制的 migrate、user/member、serve 子命令和停止方式；所有命令支持明确失败退出。
3. 在干净环境准备 MariaDB 和授权 TOS bucket/prefix，安装统一配置，迁移、初始化账号并启动一个 Go 服务；运行环境不安装 Node.js，仅创建受限会话临时目录；持久数据依赖 MariaDB/TOS。
4. 对打包后的产物执行健康检查、登录、Skill/Agent 发布、实际运行、工具和 Trace 查看。
5. 完成一次 MariaDB 备份还原与 TOS 对象保留/恢复验证，恢复到隔离数据库和对象前缀，校正对象映射并检查密钥与引用完整性；业务对象不以本地目录作为持久化备份方案。
6. 在本文件补充实际运行命令、配置说明、备份恢复步骤、故障处理和已知限制。

**交付物**：内嵌前端与迁移的可启动 Go 二进制、单份示例配置、版本与构建标识、自动化验证入口、四份同步文档、注明环境的验收记录。

**完成条件**：他人或后续 AI 可以只依赖仓库与授权配置完成构建和运行；不存在仅开发机可用的隐式依赖。交付限于本服务，不额外执行外部发布。

## E13 统一开发与验证入口

D00—D03 已建立下表标明相应阶段的入口；后续阶段入口随实现增加，不提供固定成功占位。实际通过范围见 E15.1—E15.4。

| 入口 | 行为 | 建立阶段 |
| --- | --- | --- |
| make setup | 检查 Node/npm 精确版本，下载 Go 依赖、npm ci、安装固定 Playwright Chromium | D00 |
| make setup-browser | 安装 Chromium；Linux 可传 PLAYWRIGHT_INSTALL_FLAGS=--with-deps | D00 |
| make generate / make generate-check | 生成 OpenAPI 类型与 Go AG-UI JSON/SSE 夹具；后者检查漂移 | D00/D01 |
| make fmt / make lint | Go 格式、静态检查、前端 lint | D00 |
| make typecheck | 单独执行 TypeScript 类型检查 | D00 |
| make test-unit | Go 单元/协议测试与 Vitest | D00 起逐步增加 |
| make test-integration | 真实 MariaDB 的 D01—D03 升级、事务、定义/版本、隔离与 API 契约；默认不访问云存储 | D01—D03 |
| make test-e2e | 进程冒烟与 Playwright 双账号空间/成员/模型/凭证闭环；需隔离 MariaDB | D02 |
| make test-catalog-e2e | 真实 Go/MariaDB、官方 SDK 对接回环 TOS 协议夹具，Chromium 完成定义/版本与编辑冲突闭环 | D03 |
| make test-race | 支持平台上执行 Go race 检查 | D00 起 |
| make build-frontend | 构建 frontend 并同步 Go embed 目录，Go 编译/测试的前置步骤 | D00 |
| make dev | 前端构建监听与 Go 重建，浏览器只访问 Go 服务地址 | D00 |
| make build | 前端嵌入与单一 Go 二进制交付构建 | D00/D10 |
| make smoke-tos | 真实 TOS 内容/快照和 D03 发布/资源读取、故障回滚；只清理本次精确对象 key | D01/D03 |
| make smoke-real | 显式配置凭证后的最小真实模型验证，D07 扩展工具/摘要 | D04/D07 |
| make verify-d00 | 保留生成物、类型、静态与单元/协议检查；D02 后业务进程测试需数据库 | D00 兼容入口 |
| make verify-d02 | 生成、类型、lint、单元/协议、真实 MariaDB 集成、进程与 D02 浏览器检查（含 D01 本地回归） | D01/D02 |
| make verify-d01 | verify-d02 加 smoke-tos；必须显式配置专用 MariaDB、TOS 与测试前缀 | D01 全量验收 |
| make verify-d03 | verify-d02 加 D03 浏览器闭环；CI 默认门槛，真实 TOS 另跑 smoke-tos | D03 |
| make verify | 完整 MVP 门槛；当前明确失败并提示使用 verify-d03，不能伪报集成通过 | D10 前完成 |

Vite 构建不能代替 TypeScript 类型检查。Vitest 用于前端状态与工具函数；Playwright 用于实际浏览器流程，不通过页面内部状态注入绕过核心后端行为。工具参考：[Vitest](https://vitest.dev/guide/)、[Playwright](https://playwright.dev/docs/intro)。

测试数据库和 TOS 前缀必须与开发/生产隔离，清理只针对测试命名空间。普通 CI 不注入云凭证，使用内存替身和假 HTTP 端点完成离线检查；受控集成任务显式注入专用 TOS 测试凭证，真实模型测试另行启用并设置预算。缺少凭证时集成项标记未验证，make verify 不得伪报全套通过。内存替身不作为生产存储实现。

每次变更先跑适用检查；合并或交付前跑全套门槛。命令执行失败或环境缺失应如实记录，不用“未发现问题”代替“未验证”。

## E14 MVP 验收矩阵

以下是完整 MVP 验收要求；D00 证据见 E15.1；D02 的 A01/A50 和 A02/A49 身份管理部分见 E15.2；D01 的 A25/A26/A47 基础存储与日志部分见 E15.3；D03 的 A03、A04 定义绑定部分见 E15.4，其余仍待相应阶段验证。各用例使用两个用户、两个空间、至少两个 Skill 和一个可控模型夹具，涉及真实模型的用例单独注明。

| 编号 | 目标/模块 | 操作与故障注入 | 预期结果 | 验证阶段 |
| --- | --- | --- | --- | --- |
| A01 | P01/M01 | 正确/错误密码登录、加入/移除成员、注销 | 权限即时用于后续请求，注销后拒绝访问 | D02 |
| A02 | P01/M01/M11 | 使用另一个空间的资源 ID、下载地址和凭证引用 | 所有入口拒绝越权，不泄露内容 | D02/D09 |
| A03 | P02/P03/M02 | 创建 Skill、资源和 Agent，编辑后发布 | 草稿和版本独立，已发布内容不可变 | D03 |
| A04 | P03/P04/M02 | Agent 分别绑定 Skill A v1 与 B v2；发布 A v2 | 原 Agent 仍准确使用 A v1/B v2 | D03/D05 |
| A05 | P04/M02/M04 | 切换默认 Agent 版本，继续旧会话并新建会话 | 旧会话不变，新会话使用新默认版本 | D04 |
| A06 | P05/M02/M04 | 开始调试后修改草稿 | 当前 Run 与会话使用原快照 | D04/D08 |
| A07 | P06/M04/M05 | 同键重复提交、同键不同内容、并发新请求 | 同请求不重复，冲突明确，同会话只有一个活跃 Run | D04 |
| A08 | P06/M05/M06 | 取消 queued 和 running；断开浏览器 | 取消状态与实际执行一致，断网不自动取消 | D04/D09 |
| A09 | P07/M07 | 小上下文窗口下连续多轮并触发摘要 | 原文保留、摘要来源正确、摘要调用进入 Trace | D06 |
| A10 | P07/M07 | 关键输入超限、摘要失败、工具链占满容量 | 明确失败或合法压缩，不静默丢弃关键内容/配对 | D06 |
| A11 | P08/M09 | 模型请求工具，工具真实执行并返回 | 参数、结果、call_id 可追踪，结果进入下一次 input | D05 |
| A12 | P08/M09/M11 | 当前目录路径穿越、伪造对象引用、修改已发布 Skill 文件 | 拒绝越界或写入只读内容 | D05/D09 |
| A13 | P09/M08/M10 | 流式、多工具、摘要、失败响应和重试 | 每次实际 input/output、结果与尝试分别完整保存 | D06/D07 |
| A14 | P09/M08 | 本地假供应商捕获请求字节并与 Trace 比较 | 实际请求一致，秘密头不被记录 | D00/D04/D07 |
| A15 | P09/M10/M12 | AG-UI 一对多映射中途断线、页面刷新、慢客户端、重复事件 | 独立 UI 游标正确，状态应用后确认，快照与游标匹配，无丢失/重复 | D08 |
| A16 | P09/M10 | Trace 保存失败或响应中途断开 | 停止后续动作，已存记录可见，未完成明确标记 | D09 |
| A17 | P10/M05/M06 | 服务退出时已有工具动作，随后重启 | 不重放未知副作用；已开始任务标记中断 | D09 |
| A18 | P10/M06 | 超过轮数、工具次数、时长、并发与预算 | 限制生效，错误与已发生用量保留 | D09 |
| A19 | P09/P10/M11 | 超大结果、删除工作文件、引用版本归档 | 缩略与完整区分，历史快照及产物仍可读 | D09 |
| A20 | P01/M12 | 运行中切换空间或撤销成员权限 | 无旧空间缓存/事件泄漏，连接正确结束 | D08/D09 |
| A21 | P06/P08/P09/M03/M08 | 通过 Eino 组件调用授权真实模型，执行文本、工具和摘要冒烟 | 实际供应商可用，凭证与调用均受控 | D07 |
| A22 | 全流程/M13 | 干净环境安装、构建产物运行、备份还原 | 完整闭环可重现，历史与产物恢复一致 | D10 |
| A23 | 部署约束/M12/M13 | 无 Node.js 环境仅启动一个 mass 进程，访问页面/API/SSE | 同一配置、端口和进程提供完整应用 | D00/D10 |
| A24 | P10/M11 | 连续 Run 使用临时文件、移除临时目录后重启、TOS 不可用 | 从最后确认快照恢复；保存失败不报成功，历史与产物不依赖原目录 | D09/D10 |
| A25 | P04/P09/M11 | 真实 TOS SDK 上传、读取、重复写、数据库提交失败 | 内容完整、不可变，引用一致，未引用对象不可见 | D01/D09 |
| A26 | P07/P09/M04/M10 | 重复追加、并发取消/结果返回、视图事务失败及离线重建 | 日志与视图一致，重建不调用模型或工具，结果不因序号冲突丢失 | D01/D04/D09 |
| A27 | P07/M07 | 固定日志边界和所有版本重复投影，变更外部草稿或时钟 | 模型可见内容一致，不受可变业务来源影响；新事件不改旧投影 | D06 |
| A28 | P07/P08/M07 | 多工具、失败重试、未闭合组、摘要调用、最终回复引用 | 消息不重复、工具正确配对、summary 不混入任务对话，未知副作用明确 | D05/D06 |
| A29 | P07/M07/M08 | 同阶段追加用户消息/工具结果，改变 map 构造顺序 | 既有内容/工具顺序稳定；适配器保持可复用前缀，变化有原因 | D06/D07 |
| A30 | P07/M07 | 强制压缩后继续多轮，再切换模型配置 | 摘要只生成并保存一次后复用，epoch 有界切换，原始事实不丢失 | D06 |
| A31 | P09/M08/M10 | 实际缓存 usage 缺失/为零/非零，测量冷启动和追加 | unknown 与零区分，命中率及 token 占比口径正确，不伪造必命中 | D07 |
| A32 | P07/P09/M07/M11 | 事件版本未知、内容损坏/缺失、视图重建 | 明确不可投影/重建范围，不返回貌似完整的空上下文 | D06/D09 |
| A33（启用时） | P01/P07/M08 | 显式缓存过期、作用域不匹配或权限撤销 | 从固定事实重建或拒绝，无跨空间复用、无重复副作用 | D07/D09 |
| A34 | P06/P08/P09/M10/M12 | Go 编码的 AG-UI 事件由锁定 TS Schema 校验并消费 | type/camelCase/必填字段正确，CUSTOM 有版本，工具参数 END 与结果分开 | D00/D05/D08 |
| A35 | P06/M10/M12 | 排队取消、执行失败、可恢复模型重试、连接中断 | 单个完整流有一个开始及一个终态，RUN_CANCELLED 正确展示，断网不伪造 Run 失败 | D04/D08 |
| A36 | P09/M10/M12 | UI 投影事务崩溃、同 Run 重连、协议升级及半消息恢复 | 检查点与输出原子提交、ID/游标稳定、无重复副作用，非法协议停止消费 | D04/D08/D09 |
| A37 | M05—M11/T06 | 编译与 go list 依赖检查；独立替换 Executor 消费接口；只读投影重放 | 无循环/反向依赖，核心不导入具体 adapter/transport，Projector 无模型调用/事件写入 | D04/D06 |
| A38 | P06/P10/M05/M06 | 认领后交接失败、成功与取消竞态、panic、Finish 存储失败 | Supervisor 统一终结，停止后名额仅释放一次，占用只随终态事务释放，故障不重放 | D04/D09 |
| A39 | P08/P09/M09/M11 | 文件替换前/后失败、摘要冲突、日志提交未知 | 未执行与已发生效果区分，日志失败不宣称文件回滚，未知先核实且不重做 | D05/D09 |
| A40 | P07/P09/P10/M06/M08 | 同一 Run 包含任务、摘要、模型重试及 usage 补报 | 实际 attempt 各预留一次、统一受限，结算不重复，未知不冒充零用量 | D06/D07/D09 |
| A41 | P08/M09/M11 | 文件修改/删除/重建、精确替换歧义、移动冲突、连续分段读取 | expected_sha256 阻止混读和误覆盖；单文件修改完整；既有快照不变 | D05/D09 |
| A42 | P07/P08/M09/M11 | 列表/搜索跨页期间修改文件、扫描超限、跨空间引用、未接受附件 | 固定清单/查询结果、覆盖范围明确、授权有效，未支持格式明确拒绝 | D05/D09 |
| A43 | P08/P10/M09 | run_code 正常计算、无限 JS 循环、异常、输出超限、无效返回与取消 | 代码/输入/I/O/日志可追溯，失败保留已发生效果，实际退出后释放名额 | D05/D09 |
| A44 | P08/P10/M09 | 代码连续读写当前目录及 HTTP 请求；尝试越界、链接、source map、环境/凭证、跨 VM 状态 | 授权 fs/net 可用且记录 I/O，越界或隐式能力拒绝；不等于强内存隔离 | D05/D09 |
| A45 | P07/P09/M07/M10/M12 | 计划/产物批次重试、视图重建、AG-UI 重连和重新投影 | 视图与日志一致，无重复登记/上下文，UI 快照恢复计划及产物状态 | D05/D06/D08 |
| A46 | P08/P10/M09/M11 | 发布产物后修改/移动/删除源文件、重复 operation、撤销成员下载 | 交付内容固定，幂等有效，下载实时授权，历史引用阻止误删 | D05/D09 |
| A47 | P06/P08/M04/M11 | 同会话下一轮读取；新会话指定旧快照；原目录丢失恢复 | 文件/空目录/摘要正确；独立副本，无跨空间访问或旧对话自动继承 | D01/D04/D09 |
| A48 | P06/P10/M05/M11 | 成功/失败/取消收尾、TOS 失败、快照事务未知、重试保存与崩溃 | 成功须有快照；原错误保留，快照失败可见；不重跑工具，恢复只用已提交快照 | D04/D09 |
| A49 | P01/M01/M05/M12 | 排队、模型流、代码文件/网络、下载、SSE 各阶段移除成员 | 当前业务访问拒绝；Run 主体失权则 failed/forbidden，调用取消，无自动续跑；系统可完成快照收尾但不泄露内容 | D02/D04/D05/D08/D09 |
| A50 | P01/M01/M12 | 两个预置账号经页面创建空间、添加/移除成员、尝试移除 owner | 浏览器与 API 完整闭环、权限实时生效，不能移除 owner | D02 |
| A51 | P09/P10/M10/M11 | 推进时间后读取版本、Journal/AG-UI、快照及产物 | 无业务 TTL 或自动删除；认证及供应商缓存有效期不改变历史保存 | D09 |

仅检查最终回复不算完整验收；必须验证版本、上下文来源、协议块、工具结果和持久化事件之间的对应关系。

## E15 进度、证据与交接

截至 2026-10-17，本次完成的是四份文档维护及单服务、MariaDB、TOS、Eino、最简登录及统一会话日志/上下文投影/缓存稳定性及 AG-UI over SSE 方案更新，没有初始化业务工程，没有执行系统功能测试。E01 中所有编码阶段保持“未开始”。

此前选型更新的文档检查通过：四份文件的内部链接、代码围栏、章节唯一性及旧选型残留检查；8 个 Go 片段经 gofmt 语法解析，3 个仅使用标准库的独立示例在临时目录编译通过。Eino/TOS 示例依据官方接口核对，尚未执行依赖编译、MariaDB/TOS 集成或真实模型验证；这些结果不计为编码阶段完成。

本轮统一日志与缓存方案更新的检查通过：四文档边界与内部链接、章节唯一性、代码围栏、事件 JSON、8 个 Go 片段语法，以及旧事件名称和旧 Run 连续序号假设的残留检查；git diff --check 通过。本轮未执行运行时功能或真实缓存测试，A26—A33 是后续待验证要求，不能视为已通过。

AG-UI 方案更新已核对官方事件、SSE 和 SDK 文档；文档链接/围栏/章节、事件 JSON、8 个 Go 片段语法检查通过，2 个 SSE 示例 data 可解析，独立帧发送示例编译通过。尚未安装锁定 SDK 或执行跨语言与端到端测试，A34—A36 保持待验证。

Runtime 拆分方案更新完成：技术方案定义 10 个叶子包、窄接口、调用方向与事务/终态所有权，同步架构职责及 D00/D04—D06 实现顺序。四份建设文档的相对链接、章节唯一性、代码围栏、JSON 和 8 个 Go 片段语法检查通过，git diff --check 通过。未创建业务包或运行功能测试；A37—A40 是后续验收要求，依赖图与并发行为仍需在实现后验证。

此前 MVP 工具扩展方案更新完成（其中计算输出事务及无宿主/网络访问约定，已被本轮 P1 的会话目录和显式 fs/net 能力取代）：14 个工具的参数/结果、权限、计算与文件事务、计划/产物事件及 AG-UI 展示已同步四份文档。goja 的实例使用/中断及 doublestar 的逻辑路径匹配依据官方文档核对；四文档链接/章节/围栏、JSON、8 个 Go 片段语法、14 项工具清单、新事件投影规则和 git diff --check 均通过。尚未安装新依赖或执行 JS 引擎、数据库、TOS 与工具功能测试；A41—A46 保持待验证，所有编码阶段仍未开始。

P1 审查整改文档更新完成：按用户确认将 run_code 文件范围限定为当前 Session 真实临时目录，开放内置网络请求；补齐跨 Run 文件连续使用、TOS 全量快照/URI 入库与恢复、空间成员页面/API、权限撤销后报错终止，以及提前模型适配验证和分步交付。删除业务历史过期/自动回收方案。四文档相对链接、章节唯一性、围栏、JSON、8 个 Go 片段语法、Markdown 表格、14 项工具和 51 项验收编号检查通过，git diff --check 通过。goja source map 与中断限制已核对官方说明；未执行业务功能、依赖集成或真实云验证，新增 A47—A51 及修改用例均待后续编码验证。

后续每个阶段完成时在下面新增记录，保留失败与修复的关键证据，不凭口头结论覆盖历史：

| 阶段/日期 | 代码版本及实现位置 | 验证环境与命令 | 结果与证据位置 | 剩余事项 |
| --- | --- | --- | --- | --- |
| D00 / 2026-10-18 | 基于 c61cd19 的当前工作区；cmd/mass、internal、frontend、api、db、scripts、tests/dependencies | macOS arm64；Go 1.26.9、Node 24.22.0、npm 11.19.1 | 总门槛、race、隔离源码重建与浏览器通过，见 E15.1 | D01 存储/日志实现与真实 MariaDB/TOS 验证 |
| D02 / 2026-10-18 | 基于 8d7a615；identity/mariadb/secrets、迁移、HTTP、空间页面与集成测试 | 同上；真实 MariaDB 11.8.9 | verify-d02、race、双账号浏览器闭环通过，见 E15.2 | D01 TOS/快照/Journal 与 D04 Supervisor 接入 |
| D01 补齐 / 2026-10-18 | 基于 3d40695；新增迁移 00003、TOS、Journal、快照、读取 API 与集成测试 | 同上；真实 MariaDB 11.8.9 与 TOS | verify-d01、race、旧库升级与 D02 浏览器回归通过，见 E15.3 | D03 定义管理；D04 完整 reducer 与 Supervisor 接入 |
| D03 / 2026-10-18 | 基于 69e7eeb；迁移 00004、app/catalog、MariaDB/HTTP、Monaco 页面与集成测试 | 固定工具链、真实 MariaDB/TOS、Chromium | verify-d03、smoke-tos、race 与 D01/D02 升级回归，见 E15.4 | D04 最小运行纵向闭环 |
| D04 / 2026-10-23 | 8459a09；迁移 00005、Session/Run、Eino/Gateway、三工具、Supervisor/AG-UI | 固定工具链、真实 MariaDB/TOS、授权真实模型、Chromium | verify-d04、race、真实模型及 TOS 冒烟，见 E15.5 | D05 完整工具 |
| D05 / 2026-10-24 | 基于 8459a09；迁移 00006、全部工具、真实 goja、附件/计划/UI 与测试 | 固定工具链、真实 MariaDB/TOS、实际 Eino/HTTP 协议夹具、Chromium | verify-d05、race、smoke-tos，见 E15.6 | D06 上下文完整能力 |
| D06 / 2026-10-24 | 基于 d2010bf；迁移 00007、context/planner/projector、摘要/索引、来源 API/UI | 固定工具链、真实 MariaDB 11.8.9、Eino FakeChatModel 与实际 HTTP 协议夹具、Chromium | verify-d06、race，见 E15.7 | D07 模型能力与真实缓存观测 |
| D08 / 2026-10-24 | 基于 db55c28；AguiRunClient、debug/Trace/Markdown、模型统计 API、调试导航 | 真实 MariaDB、本地 Eino/HTTP 与 TOS 协议夹具、Chromium | 本地回归与 race，见 E15.9 | D07 真实验证待授权；D09 故障/隔离验收 |

每次交接应说明当前阶段、已完成小步、实际文件、通过的检查、未通过/未运行的检查及下一步。不得把设计片段写入仓库后即标记功能完成，也不得跳过真实模型验证而宣称完整交付。

需要新增重要技术决定时写入技术方案相应章节；需要改变范围时先更新设计指引与相关下游文档。文档数量维持四份，验收结果、运行说明和交付记录继续追加在本文。


### E15.1 D00 实现与验证记录（2026-10-18）

已完成单一 mass CLI（serve/check-config/version）、严格有界配置解析、环境秘密来源检查与信号关闭；内嵌 React 工作台/空间占位与真实同源系统信息 API。live=200，ready=503/dependencies_not_initialized。只有声明的页面路由回退 index.html，API/健康/静态文件未命中返回错误。登录、Agent、Run、存储迁移与真实 SSE 订阅尚未开放。

OpenAPI 定义当前接口及共享错误/ID/分页/幂等/序号契约；api/events 保存 Journal envelope、CUSTOM schema、AG-UI profile 和 Go SDK 生成的 JSON/SSE 夹具。db/migrations/config.json 固定 MariaDB 11.8.9 与 goose/mysql 约定，db/queries/health.sql 为 D01 查询准备；没有执行业务 DDL。CI 配置已建立，远端 CI 尚未触发。

依赖精确版本以 go.mod/go.sum、frontend/package-lock.json 为准。主要版本：Eino v0.9.21、Ark v0.1.71、TOS v2.9.10、goja v0.0.0-20261017142635-e6862644b38f、doublestar v4.10.3、AG-UI Go v0.0.0-20261016232758-89cedd3fd549；React 19.3.1、Vite 8.3.4、TypeScript 5.9.3、@ag-ui/core 1.0.3、zod 3.25.76、Playwright 1.64.1。Node 官方发行包已核对 SHA-256；Go 1.26 为受支持系列，MariaDB 11.8.9 依据[官方维护发布](https://mariadb.org/mariadb-server-12-3-11-8-11-4-and-10-11-q3-2026-maintenance-releases-and-goodbye-10-6/)锁定。本机未安装 MariaDB/Docker，未连接真实存储或模型。

| 检查 | 命令/实现证据 | 实际结果与边界 |
| --- | --- | --- |
| D00 总门槛 | make verify-d00；Makefile | 生成物漂移、tsc、ESLint、go vet、格式/依赖方向、Go/Vitest、构建及进程/浏览器冒烟通过 |
| Eino 实际组件协议 | tests/dependencies/eino_test.go | Ark WithTools 序列化后的请求等于本地 httptest 收到的字节；流式/非流式、429、非法 JSON、记录失败通过；每次最多一次 HTTP 发送，无隐藏重试，认证秘密未进入记录器 |
| goja 宿主试验 | tests/dependencies/goja_test.go | 连续文件读取/写入、本地 HTTP、写入后异常保留效果、越界拒绝、跨 VM 状态隔离、循环/网络取消通过；顶层/eval/Function source map 加载次数为零；仅测试宿主，非 D05 完整 CodeRunner |
| AG-UI 跨语言 | scripts/agui-fixtures、frontend/src/lib/agui.test.ts | Go 编码的 11 种标准类型（含 CUSTOM）和 8 个 CUSTOM 名称通过 TS Schema；4 项 Vitest 通过，覆盖 SSE、取消、工具 END/RESULT、版本与大整数游标 |
| 并发 | make test-catalog-e2e | 真实 Go/MariaDB、官方 SDK 对接回环 TOS 协议夹具，Chromium 完成定义/版本与编辑冲突闭环 | D03 |
| make test-race | 当前 Go 包 race 检查通过，包括 VM 中断 goroutine 和本地 HTTP |
| 单二进制运行 | scripts/smoke.mjs | 从独立临时 cwd、空 PATH 启动，页面/静态资源/API 正常；缺关键配置失败，SIGTERM 正常退出；运行无需 Node 或原 frontend/dist |
| 开发监听 | scripts/dev.mjs；临时配置进程试验 | 首次构建/启动、配置变化后重建/重启及 SIGTERM 清理通过，全程仅 Go 服务提供 HTTP |
| 实际浏览器 | scripts/browser-smoke.mjs | 完整 Chromium headless 模式通过：页面渲染、同源 API、导航/刷新、390px 布局，无 pageerror；截图在 .cache/d00-desktop.png、.cache/d00-mobile.png，可再生不入库 |
| 隔离源码重建 | Git 可见源码复制到临时目录，make setup、make build typecheck generate-check | 无 .git、node_modules、dist、bin 及旧 Go 编译缓存的副本可安装构建；复用已校验的 Go 模块下载缓存与浏览器缓存，不依赖全局 npm 包 |
| 依赖/补丁检查 | npm install 的 registry audit；git diff --check | 最终 0 项 npm 漏洞；补丁格式检查通过 |

发现与修复：TypeScript 7 与生成器/ESLint peer 范围冲突，固定 TypeScript 5.9.3 与受维护的 ESLint 10。AG-UI Schema 使用新入口 @ag-ui/core/schemas，MASS 额外拒绝 SDK 允许的空 delta。goose 拒绝空迁移集合，依赖试验使用测试内 SQL fixture，不计为数据库执行证据。Go 检查限定 cmd/internal/tests/scripts，避免纳入 node_modules 内第三方 Go 文件。Monaco 0.57.0 的 DOMPurify 传递依赖用 override 固定到修复版本 3.4.17。Chromium headless shell 曾在稳定性等待上超时，同版完整 Chromium headless 导航及截图通过。最初沙箱禁止本地监听，获准执行回环协议测试后通过。

安装 .go-version、.node-version 指定工具链及 npm 11.19.1 后，可重复执行：

```sh
make setup
make verify-d00
make test-race
```

Linux 缺 Chromium 系统库时执行 `make setup-browser PLAYWRIGHT_INSTALL_FLAGS=--with-deps`；CI 已配置。Go wrapper 默认把模块/编译缓存放在项目 .cache；编译/测试入口先构建并嵌入前端。测试使用本地假供应商、临时目录与明确的测试环境值，无需真实云凭证。完整 make verify 当前故意失败；真实 MariaDB/TOS/模型及产品业务闭环尚未验证。

日常运行：复制 configs/mass.example.yaml 为不入库的 mass.yaml，设置其中各 *_env 指定的值（主密钥为 base64 编码的 32 字节），执行 `bin/mass check-config --config mass.yaml`，再 `bin/mass serve --config mass.yaml`。`make dev CONFIG=mass.yaml` 监听源码/配置变化并构建重启，浏览器始终只访问 Go 地址。D00 的 ready=503 是阶段边界，不是云服务验证结果。`make test-e2e` 可执行无需私人配置的进程和浏览器预览检查。

下一阶段为 D01：MariaDB 事务/迁移、TOS 对象与 ready 引用、会话目录/快照、SessionJournal 与视图原子提交。专用数据库及 TOS 测试配置用于真实集成验收，D00 的 SDK 编译和本地协议试验不能替代它。


### E15.2 D02 身份、空间与模型配置（2026-10-18）

以下保留 D02 交付时的阶段记录；后续补齐的 D01 能力、schema 3 和当前部署要求见 E15.3。

用户明确要求继续 D02，仓库起点为 8d7a615（只有 D00）。本次先补齐 D02 必需的 D01 子集：MariaDB 连接池、UTC/utf8mb4、事务封装、嵌入式 goose 迁移、区分应用/迁移账号、用户与空间基础表；随后完成 D02 管理闭环。D01 的 content_objects、TOS、SessionJournal、会话目录和快照均未实现，D01 不标记完成，后续运行相关阶段仍需先补这些前置。

交付入口：

- cmd/mass 新增 migrate、user create；internal/app/identity 实现登录会话、成员、凭证/模型规则及权限复核/撤销订阅，internal/adapter/mariadb 实现真实事务和空间条件，internal/adapter/secrets 实现 AES-GCM。
- internal/transport/http/identity.go 与 api/openapi.yaml 提供登录/退出/me、空间、成员、凭证、模型/能力查询；更新自动生成前端类型。
- frontend/src/features/spaces 与应用壳提供真实登录、空间创建/切换、成员添加/移除/本人退出、凭证创建/替换/禁用、模型创建/修改/停用。401/403 清空私有视图，取消旧请求并停止权限失败页面的重复查询。
- db/migrations/00001_identity.sql、00002_models.sql 为真实迁移；应用启动必须已有版本 2，不自动借用迁移权限。数据库 schema 检查通过仍不表示 TOS 接入，/health/ready 继续诚实返回 503/dependencies_not_initialized。

验证环境为 macOS arm64、Go 1.26.9、Node 24.22.0、npm 11.19.1、真实 MariaDB 11.8.9。MariaDB Homebrew bottle 与 OpenSSL 依赖下载到 /tmp 后仅用于测试，数据目录 /tmp/mass-d02-tools/data，禁用 TCP、使用专用 Unix socket；没有安装/启动系统服务。每次测试随机创建 mass_test_* 数据库及独立应用账号，清理只删除本次生成的库/账号，不读取部署 DSN。tests/testdb 同时验证空库迁移和重复迁移，应用账号仅授予 SELECT/INSERT/UPDATE/DELETE。

执行结果：make verify-d02 与 make test-race 全部通过；远端 CI 未触发。

```sh
# PATH 中使用 .node-version 指定的工具链；设置的必须是专用测试服务器。
export MASS_TEST_ADMIN_DSN='root@unix(/tmp/mass-d02-tools/mysql.sock)/'
make verify-d02
make test-race
```

真实数据库/API 集成覆盖：密码摘要、错误/未知账号、匿名/到期/退出会话、Cookie 属性、Origin/Content-Type 拒绝、基础限速；两用户两空间、owner 不可删除、添加成员幂等、本人退出、跨空间模型/凭证引用拒绝；游标作用域防移植；审计触发器故障导致成员变更回滚；并发读取与撤销、撤销订阅通知及后续秘密解析拒绝；凭证密文不含明文、替换/禁用、revision 冲突、可用模型过滤。集成测试还按 OpenAPI Schema 校验实际 HTTP 响应，独立密码学测试覆盖 nonce 随机性、AAD 空间/ID/key 绑定和篡改拒绝。

Playwright 使用 CLI 预置的 alice/bob 两个账号，经页面完成登录→分别创建空间→添加已有成员→普通成员查看→创建凭证和模型→修改模型→移除成员→被撤销页面清理→禁用凭证→退出后访问拒绝。桌面与 390px 页面检查无横向溢出、无浏览器运行错误；截图为忽略的 .cache/d02-desktop.png / d02-mobile.png。CLI 迁移/用户创建/serve 的子进程使用空 PATH，验证运行不依赖 Node。A01/A50 通过；A02/A49 当前已有身份/空间/模型资源部分通过，下载、SSE、运行期 I/O 与 Supervisor 取消尚待 D04/D05/D08/D09，不能视为整条 MVP 验收通过。

启动步骤（配置值通过受控环境提供，密码不要放命令行明文）：

```sh
make build
cp configs/mass.example.yaml mass.yaml
# 配置 MASS_MARIADB_DSN、MASS_MARIADB_MIGRATION_DSN、MASS_MASTER_KEY
# 及模板列出的 TOS 环境引用；D02 不调用 TOS，但保留配置校验。
bin/mass migrate --config mass.yaml
# MASS_INITIAL_PASSWORD 由你的秘密环境提供，12—72 字节。
bin/mass user create --config mass.yaml --username alice --password-env MASS_INITIAL_PASSWORD
# 为第二个账号设置独立密码后再次执行 user create。
bin/mass serve --config mass.yaml
```

应用账号的授权范围为该业务库的 SELECT/INSERT/UPDATE/DELETE；迁移账号另行提供建表/索引/外键等 DDL 权限，生产 serve 无需注入 migration DSN。http.public_url 必须是浏览器实际访问的 origin，HTTPS 使用 cookie_secure=true。新建空间后通过页面添加已有账号，再配置 Ark API 密钥与已开通模型；示例/测试密钥不代表真实供应商调用成功。主密钥备份与限制见 T03.1，当前不能直接更换主密钥后丢弃旧值。

CI 已配置固定 mariadb:11.8.9 服务与 verify-d02/race，本地验收不代表远端 CI 已执行。完整 make verify 仍拒绝宣称 MVP 完成。下一步应补完 D01 对象、日志与快照，再进入依赖它们的定义与运行阶段；D02 的后台权限复核接口将在 D04 接到 Supervisor。


### E15.3 补齐 D01 并兼容 D02（2026-10-18）

起点为 D02 提交 3d40695。本次不修改 00001_identity.sql / 00002_models.sql 的内容或编号，仅新增 00003_storage_journal.sql；schema 版本推进到 3。D02 账号、会话、空间/owner、凭证密文和模型记录均原位保留。tests/testdb.NewVersion(2) 先按旧版建立并写入完整 D02 业务数据，再执行新增迁移两次，验证原 Cookie、成员归属及模型凭证解密仍然有效；D02 页面闭环再次通过。

代码位置与交付范围：

- internal/domain/storage、internal/resource/content.go、internal/adapter/tos、internal/adapter/mariadb/content.go：唯一对象 ID/服务生成 key、uploading 意图、SDK ForbidOverwrite、全量读回 SHA-256/大小验证、ready 事务与引用可见性。已上传但未提交引用的对象保留为不可见对象，不实现 GC。
- internal/journal、internal/adapter/mariadb/journal.go：Journal.Append 唯一入口，支持稳定 append_key/event_id、请求摘要冲突、先幂等后 expected_sequence、Session 连续序号、格式版本、同空间 ready 引用、因果来源和固定 through_sequence 分页；事件、引用、基础 Run 视图和占用同事务。
- internal/adapter/workspace、internal/resource/snapshot.go、internal/adapter/mariadb/snapshots.go：按 Session 加锁的真实临时目录、跨轮复用、完整 tar.gz/Manifest（含空目录）、快照索引/最新指针与 run.finished 同批次提交；稳定 ID 核实未知提交；授权恢复和新 Session 独立复制。恢复失败不绑定半份目录，日志提交失败清理新恢复副本。
- bootstrap 组装真实存储服务，ready 检查 schema 3 与 TOS HeadBucket，3 秒超时、5 秒缓存。200 表示这些探针通过；对象读写权限另由 smoke-tos 实测，不将 HeadBucket 当作写入证明。D02 管理界面保持原有接口与行为。
- 已开放鉴权读取：GET /spaces/{space_id}/sessions/{session_id}/journal、GET /spaces/{space_id}/workspace-snapshots/{id}、GET /spaces/{space_id}/payloads/{id}（均在 /api/v1 下）。原始字节下载验证完整摘要后才发送，传输中复核 Cookie 和成员，撤权停止后续字节。创建 Session/Run、保存/恢复快照当前是内部 Go 服务能力；绑定定义、运行入口、完整 reducer、系统收尾身份及 Supervisor 等仍属于 D03/D04。

验证环境：macOS arm64，固定 Go/Node/npm 工具链及真实 MariaDB 11.8.9；使用用户提供的广州区 TOS 测试环境。凭证只从环境读取，未写入代码或日志。每次真实 TOS 测试在指定 tmp/ 下生成独立 mass-d01-UUID/，只上传合成测试文本/归档，逐一删除并再次 GET 确认本次生成的对象不存在；最后一轮共 8 个 key，无按 Bucket 或前缀批量删除。数据库也只清理本次随机 mass_test_* 库/账号。

验证入口：

执行结果：`make verify-d01 test-race` 全部通过，包含真实 TOS、MariaDB、D02 浏览器回归及 Go race 检查。

```sh
# 使用 .go-version/.node-version 指定的工具链。
# 由测试环境提供以下变量；不要将真实密钥写入 shell 命令或版本库。
# MASS_TEST_ADMIN_DSN
# MASS_TEST_TOS_ENDPOINT / MASS_TEST_TOS_REGION / MASS_TEST_TOS_BUCKET
# MASS_TEST_TOS_PREFIX（显式相对目录前缀，末尾 /）
# TOS_ACCESS_KEY / TOS_SECRET_KEY
make verify-d01
make test-race
```

验收覆盖：空库与 D02 旧库升级/重复迁移；不同 Run 并发追加同一 Session 的唯一连续序号；相同内容幂等和不同内容冲突；未知事件版本与跨空间内容/快照引用拒绝；固定分页水位；上传后 ready 事务失败且安全重试；日志视图失败共同回滚；快照已经写入但 Run 占用释放失败时，快照/事件/终态/占用整体回滚，远端孤立归档不可下载；TOS 故障不报告快照成功；无快照的运行不能成功终结；删除本地目录后恢复原内容、摘要和空目录；新 Session 副本不共享修改或继承原对话；恢复日志失败可清理后重试。

归档独立测试覆盖路径穿越/绝对路径、软硬链接/设备、重复与大小写冲突路径、缺失文件、摘要错误、gzip 截断、格式版本、文件大小上限、目录锁取消，恢复权限固定为目录 0700/文件 0600。HTTP 校验覆盖新接口的 OpenAPI 响应、未登录与到期会话拒绝，以及流式输出中的权限撤销停止。D02 双账号、空间、成员、凭证、模型与退出浏览器流程保持通过；前端无需新增配置。

部署升级：先备份数据库及原主密钥/TOS 对象，用迁移账号执行新二进制的 mass migrate，再启动 mass serve；不重建旧库、不重新初始化已有账号、不更换旧主密钥。旧 mass.yaml 不增加 storage 字段也可运行，缺省限制见 T11.5；新增模板字段可按实际容量调整。运行账号仍只有业务库 DML 权限，迁移账号独立。旧二进制不支持 schema 3，不建议在新增数据后直接降级。

普通 CI 继续执行 verify-d02/race，包含 D01 本地、真实 MariaDB 与 D02 浏览器回归，不注入用户云凭证；真实云验收由显式 smoke-tos/verify-d01 执行。远端 CI 未触发。本阶段没有实现后台 Agent 执行、模型调用、完整业务投影或自动重跑；下一阶段可按 E05 进入 D03。

### E15.4 D03 Agent/Skill 定义与版本闭环（2026-10-18）

基于 69e7eeb 新增 00004_definitions，保留 00001—00003 不变，schema 推进到 4。共用定义表以 kind 区分 Agent/Skill，外部接口仍独立；迁移、结构与限额见 T02.1。D02 Cookie、空间/owner、凭证密文和模型继续有效；额外从 schema 3 写入真实 D01 日志与内容引用后升级两次，验证事件/序号和原始字节不变。

交付入口：internal/app/catalog 实现严格配置、草稿与不可变发布/调试快照；internal/adapter/mariadb/catalog.go 实现空间授权、revision、依赖/资源/审计原子提交、默认与归档；internal/transport/http/catalog.go 和 OpenAPI 提供创建、复制、草稿、资源上传、版本、差异、反向引用和快照读取。D01 ContentStore 的可见性扩展为已提交事件引用、定义草稿引用或定义版本引用，仍拒绝未引用 ready 对象。

frontend/src/features/catalog 提供完整 Skill/Agent 页面，Monaco 编辑器和差异按需加载，保留双编辑者冲突时的本地文本。空间切换/权限失效清理私有查询与编辑状态，目录查询定期复核成员资格。正文预览限制 HTML/链接/图片，启用 run_code 时展示文件和 HTTP 权限范围。调试仅创建固定快照，界面不会显示已运行成功。

验收覆盖：两 Skill 准确版本组合、升级不改变旧 Agent；同 label 重复发布 409；并发草稿仅一个成功；未知 YAML/重复字段/alias/多文档/无效工具与限额拒绝；缺失或跨空间 Skill/资源不能发布；准备后模型变化拒绝提交；审计故障使版本、依赖、引用与指针一起回滚且孤立对象不可下载；TOS 故障不能发布；旧资源与调试快照独立于后续草稿；默认版本/归档约束、引用查询及数据库外键阻止破坏引用；新接口实际响应逐一通过 OpenAPI Schema 校验。

浏览器通过页面配置模型，完成 Skill 资源上传→两 Skill 发布→Agent v1→Skill 升级→Agent v2，查看原版本内容/下载资源、创建调试快照、切换默认版本和 Monaco 差异；另用两个页面同时编辑验证 409 与显式载入服务器草稿。桌面和 390px 页面无横向溢出、无 pageerror；可再生截图位于忽略的 .cache/d03-desktop.png / d03-mobile.png。浏览器采用本地 TOS HTTP 协议夹具，调用生产官方 SDK；此结果与真实云验证分别记录，不把夹具当真实云。

真实 TOS 验收复用已授权测试配置，在 tmp/ 下创建独立 mass-d01-UUID/，其中 catalog/ 仅包含合成定义/资源。运行 D01 和 D03 共享集成流程，本轮共清理 21 个对象；清理仅逐个删除本轮精确 key/版本并 GET 确认 404。密钥从环境读取，不进入源码/日志。测试数据库每轮随机创建隔离库与 DML 应用账号，退出清理本轮库/账号。

```sh
# 使用固定工具链及专用测试 MariaDB：MASS_TEST_ADMIN_DSN。
make verify-d03
# 真实 TOS 变量同 E15.3，必须显式设置测试前缀。
make smoke-tos
make test-race
```

执行结果：`make verify-d03 smoke-tos test-race` 全部通过，包含 D01/D02 升级兼容、两个浏览器闭环、真实 MariaDB/TOS 和 Go race 检查。Monaco 按需加载，构建仍提示其压缩前约 2.7 MB 的独立编辑器包较大；普通管理页不加载该包。

部署时使用新二进制执行 mass migrate，再启动 mass serve；保留已有配置、主密钥和数据。前端入口为选择空间→Agent 与 Skill；定义名使用小写标识，配置引用实际模型/Skill ID。首次发布自动设置 Agent 默认版本；后续发布须显式切换。更新 CI 默认门槛为 verify-d03/race，远端 CI 未触发。D04 的 Session 固定定义、后台 Run、模型调用、工具执行与完整 reducer 尚未实现。


### E15.5 D04 最小运行纵向闭环（2026-10-23）

基于 863ff25 新增 00005_runtime，schema 推进到 5，保留前三阶段迁移不变。D04a—d 已按同一契约落地，生产 bootstrap 只注入实际 Eino 适配器，FakeChatModel 仅由测试显式注入。

| 小步 | 代码与可体验结果 | 验证证据 |
| --- | --- | --- |
| D04a | domain/run、app/session、Journal/reducer、control、context/projector/planner：固定准确发布/调试定义、幂等提交、占用、容量拒绝、唯一 ReAct 循环 | FakeChatModel 与真实 MariaDB；相同请求只产生一个 Run、内容变化拒绝、同会话占用拒绝；固定 through_sequence 重算与已提交投影一致；超容量时零模型 attempt |
| D04b | adapter/eino、runtime/modelgateway：实际 SDK 序列化、原文先保存后消费、完整响应/usage、参数聚合、失败事件 | 本地 HTTP 供应商经生产 Eino/HTTP 捕获链路完成四次文件工具循环；授权真实模型单次文本调用返回指定标记，保存 wire/usage/快照；未知与明确零分开 |
| D04c | resource.FileService/RunWorkspaces/Finisher：三个真实文件工具、不可变产物、统一快照、下一轮恢复、新会话独立复制 | 真实 MariaDB/TOS 完成输入→写入→读取→产物→快照→删除本地目录→下一轮读取；新会话复制文件；快照故障阻止下一轮，重试仅保存文件且不改旧终态 |
| D04d | runtime/scheduler/supervisor、AG-UI projector/持久帧、HTTP/SSE、会话页面、rebuild-views | 运行实例锁互斥；queued/running 取消、撤权、panic、实际退出后收尾/释放；浏览器关闭重载不调用模型；SIGKILL 后 interrupted/快照恢复；连续 UI 游标及离线重建 |

新 HTTP 响应实际通过 OpenAPI Schema 校验，202 幂等响应也参与校验。工具结果只接受同 Run 完成响应中的 call_id/名称/参数摘要；普通 Repository 不能伪造 system 收尾身份。SQL 触发器注入语义日志失败时无最终成功回复；终结事务失败时快照、终态与占用一起回滚，执行入口停止接单，去除故障后复用原收尾计划核实提交。取消未退出的 fake runner 不提前收尾或释放名额；最终回复已完成但归档保存被阻塞时发起取消，验证只产生一个 cancelled 终态并保存快照；panic 只产生一次失败收尾。

离线重建测试先清空消息、attempt/call、工具、产物、活跃占用、AG-UI 流与帧，再校验全部源对象并重建，Run 业务结果及逐帧输出与原状态一致。不可变 Run 身份和快照索引保留作外键锚点；心跳属于协调状态，不要求重放为原时间。在线执行锁存在时拒绝重建，重建入口没有模型/工具依赖。

浏览器经页面完成模型配置→发布三个工具的 Agent→会话任务→流式回复/工具状态→产物下载→Trace/投影载荷→第二轮读取→页面刷新→取消→SIGKILL 重启→恢复已提交快照→继续读取。1440px 与 390px 布局无横向溢出、无 pageerror；截图位于忽略的 .cache/d04-desktop.png / d04-mobile.png。本地协议夹具使用官方 TOS SDK 与实际 Eino Ark 组件，与真实云验收分别执行。

真实云配置由已授权的终端环境读取：source ~/.zshrc 后可获得 MASS_LLM_MODEL_BASE_URL、MASS_LLM_MODEL_KEY、MASS_LLM_MODEL_ID。未将密钥写入仓库或日志。TOS 每轮在授权 tmp/ 下生成独立 mass-d01-UUID/，只使用合成内容；最后一轮共清理 104 个对象，清理仅针对本轮精确 key/版本并逐个 GET 确认删除，不按 Bucket/前缀批量删除。测试数据库使用独立临时 MariaDB 11.8.9 Unix socket，随机创建/清理本轮库及 DML 账号，不使用部署 DSN。

```sh
# 固定 Go/Node/npm 工具链；MASS_TEST_ADMIN_DSN 必须指向专用测试 MariaDB。
make verify-d04
make test-race
# 另行提供 E15.3 的 TOS 测试变量，以及三个 MASS_LLM_MODEL_* 变量。
# 此命令显式授权一次受限真实文本调用；云测试清理自己的对象。
make smoke-real
```

执行结果：make verify-d04、make test-race 及真实 TOS/模型冒烟通过；普通检查包含 D01/D02/D03 回归、配置兼容、迁移重复执行、生成物一致性、TypeScript/Go 检查、依赖方向、API 和三个浏览器流程。额外从 schema 4 写入真实发布定义及加密模型后升级两次，并继续以原定义执行任务。CI 默认门槛更新为 verify-d04/race，不注入用户云凭证；远端 CI 未触发。Vite 仍提示较大的主应用/Monaco 独立包，功能构建成功。

部署：停止旧服务并备份，保留原数据库/TOS/主密钥，使用迁移账号执行新二进制 mass migrate，再以 DML 应用账号 serve。旧 mass.yaml 无 base_url_env 时仍默认 Ark；使用自定义兼容模型地址需配置 llm.base_url_env: MASS_LLM_MODEL_BASE_URL，并在页面保存对应模型/凭证。环境模型密钥不会自动导入空间。完整体验步骤已同步 README Quickstart。

D04 交付时的限制（D05 扩展见 E15.6）：仅 read_file/write_file/publish_artifact 三个 v1 工具，单文件 1 MiB UTF-8，其余工具提交时拒绝；没有附件/代码执行/自动摘要，保守计数器容量不足明确拒绝。单数据库单执行实例；中断不自动重放，未保存目录修改可能丢失；界面提供显式快照恢复/只重试保存。D05—D10 与完整 MVP 验收尚未完成，make verify 继续明确失败。

### E15.6 D05 全部内置工具（2026-10-24）

基于 8459a09，按 D05a→b→c 补齐全部十四个 v1 工具。新增 00006_tools，schema 推进到 6；已有 00001—00005 不改写，真实 schema 5 定义先发布、迁移两次再运行的兼容用例通过。

| 小步 | 实际实现 | 验证证据 |
| --- | --- | --- |
| D05a | resource 文件发现、精确编辑、移动/删除、不可变查询分页；Sources 固定 Skill 资源、已接受附件；附件上传与输入绑定 | 摘要冲突、歧义/重叠拒绝、CRLF 保留、硬链接/目录拒绝、范围读取、跨页修改仍稳定、扫描上限与撤权拒绝；Skill v2 发布后会话继续使用 v1；未接受、跨 Session、未绑定资源、非法 JSON/二进制明确拒绝 |
| D05b | adapter/javascript 的真实 goja、resource.CodeService/Network、input_resolved 与递增 I/O 审计 | 所有 fs 方法、连续读取、CSV 转换、HTTP 状态/跨源认证头清理/脱敏/超时；文件/网络次数及实际读取总量超限；写文件和请求后异常保留确认效果；取消无限循环及等待未退出宿主不释放 VM 名额；无 source map 隐式读取、跨 VM 状态或 Go 对象导出 |
| D05c | update_plan 与工具终结同事务、不可变产物、Manifest 同水位状态、AG-UI 和附件/计划页面 | FakeChatModel 串联 15 次调用覆盖十四工具；summary.json 删除后仍下载原始 total=10/final 版本；计划修订 2、快照状态、I/O 引用、在线禁止/离线视图重建及模型/请求次数不增长 |

生产 bootstrap 仅注入实际适配器；确定性 FakeChatModel 和本地 HTTP 供应商由测试显式提供。浏览器使用实际 Eino/HTTP 与 TOS 协议夹具，从页面创建 Skill/Agent、选择十四工具及输入容量、上传 JSON 附件，再完成计算、编辑/整理、计划、发布与删除源文件；附件原文和产物下载、刷新后恢复计划/结果且不执行新调用、390px 布局与浏览器错误检查通过。截图位于忽略的 .cache/d05-desktop.png / d05-mobile.png。

真实 TOS/MariaDB 验收使用隔离数据库及 tmp/mass-d01-UUID/ 下合成数据，D01—D04 回归与 D05 十四工具组合、代码异常/取消、来源校验、计划/产物状态和离线重建通过。最终完整 smoke-tos 复验用时约 178 秒，清理 284 个精确对象/版本并逐项 GET 确认 404；不扫描或删除其他前缀。D05 本次未新增收费模型调用；真实文本模型接入证据保留在 E15.5。

故障注入额外验证：文件已经写入、终结 I/O 日志被 SQL 触发器拒绝时，工具效果标记 unknown、Run 以 tool_effect_unknown 结束，不发起下一次模型调用或重复文件操作；plan.updated 与对应工具完成任意一个不能提交时，视图和计划事实共同回滚。离线删除派生表后，校验 TOS 对象摘要并重建 Run/计划/产物/UI，与原状态和帧一致，没有模型或网络调用。

参数与兼容修复：供应商原生调用 ID 仅在对应响应范围内使用，平台独立生成调用 ID，回归刻意复用同一个 native_id 仍正确配对。源码支持 64 KiB，模型参数最大 2 MiB；大参数保存在 TOS 完整响应，Journal 记录摘要并限制内联总量，前端通过 Trace 查看，不把空参数当作完整标准事件。保留旧 D04 内联参数格式及已有工具 Schema；100 项长路径 Skill 资源清单通过不可变版本引用读取，不重复塞入有界 Run 事件，迁移后提交/运行/重建通过。新的容量计数器显式使用 utf8-byte-upper-bound-v2，排除未发送给模型的结果 Schema，完整契约仍固定在 Run 中。

本地通过的工程命令（MASS_TEST_ADMIN_DSN 指向独立 MariaDB 11.8.9；不使用实际应用 DSN）：

```sh
make verify-d05
make test-race
# 另行注入专用 TOS 配置、访问凭证与测试前缀；不会调用真实模型。
make smoke-tos
```

verify-d05 包含生成契约检查、TypeScript/ESLint/go vet/依赖方向和 gofmt、Go/Vitest、MariaDB 集成、进程烟测及 D02—D05 浏览器流程。CI 门槛更新到 verify-d05/race，本地通过不代表远端 CI 已触发。完整 make verify 仍明确拒绝提前宣称 MVP 完成。

部署：保留原数据库、TOS、配置和主密钥，停止旧服务，用新二进制及迁移账号执行 mass migrate 到 schema 6，再用应用 DML 账号启动 serve；已有会话继续绑定原定义，体验新增工具需给 Agent 启用工具、发布新版本并创建对应会话。README 已同步附件、输入容量、代码限额和操作示例。

当前限制：附件解析仅 UTF-8 文本/JSON/CSV，8 MiB 上限；文件写入/编辑 1 MiB，读取返回 64 KiB 范围；默认同步 JS 每次 5 秒、源码 64 KiB、累计输入/文件读取 2 MiB、宿主输出和返回值各 2 MiB、日志 64 KiB、100 次文件与 10 次网络操作，限制由 tools.code 固定到每个新 Run。无 OS 沙箱或 VM 硬内存隔离，无 Shell/Python/任意系统脚本；异常和取消不回滚确认效果。保守容量不足明确拒绝，没有自动摘要、模型自动重试或缓存命中推断；这些及 D06—D10 的剩余验收继续按后续阶段实现。

## E15.7 D06 上下文完整能力（2026-10-24）

基于 d2010bf 的当前工作区，新增 00007_context.sql，schema 7。保留 D00—D05 已有迁移与不可变对象；context_projections 与 context.projected 同事务，离线重建恢复相同索引及消息视图。实现版本、阈值、限额和旧 Session 的 reject 兼容规则见 T07.8。

确定性单元测试覆盖固定水位与时钟变化、完整多工具原顺序/原生协议、失败 Run 已闭合交互、大结果 UTF-8 节选、未闭合工具组中断及未知效果、未知解析器、损坏对象和追加未来事件不影响旧投影。真实 MariaDB 专项测试覆盖小容量首次压缩、连续后续轮复用、模型配置变化开启解释明确的 epoch、再次压缩的递归来源、来源 API 的 OpenAPI 响应校验、必要输入超限、模型摘要失败、摘要日志触发器故障未激活新阶段、缓存命中仍拒绝无权限用户、离线重建不混入摘要消息。

实际 Eino → OpenAI SSE 本地供应商协议夹具配合 Chromium 验证：一次摘要、后续多轮复用；summary 请求无工具，任务真实 HTTP messages 保持同阶段前缀；摘要不出现在用户消息及 AG-UI 助手输出；来源检查可导航到实际请求，刷新不新增调用，390px 无横向溢出。`make verify-d06` 与 `make test-race` 均通过（后者真实数据库集成 79.026s）；生成契约、TypeScript、ESLint、go vet 和依赖边界检查通过。截图位于忽略的 .cache/d06-*.png，验证日志位于 .cache/d06-verify.log、.cache/d06-race.log。

验证中发现并修复：测试数据库默认 schema/RuntimeDefinition API Schema 未同步；正常恢复提示触发摘要边界；摘要 JSON 转录自身可能比原消息大，改为按完整旧轮次缩小范围；页面仍显示 D05 不支持摘要的旧提示。真实模型付费工具/摘要调用和缓存命中收益未在 D06 新测，仍由 D07 验证；平台前缀稳定不能作为真实缓存命中证据。

## E15.8 D07 模型能力完善（进行中）

基于 db1248d。已加入显式平台重试、逐 attempt 关联/凭证解析/预算、完成与失败 observation、失败流 usage 保留和缓存统计覆盖率；不改 SDK 隐藏重试配置，不启用显式云缓存。测试覆盖实际 Eino/HTTP 逐字节中文 SSE、缺少结束标记、非法 JSON、401/429/503、空错误正文、部分输出、失败 usage、原文匹配、持久化失败不重试，以及数据库凭证轮换/禁用和模型禁用。协议矩阵暴露的非法 JSON 原文前缀与 D06 摘要检查点跨批次问题已修正。

真实文本/双工具/追加/摘要脚本已加入 smoke-real，读取此前环境变量、仅使用唯一测试前缀；尚未执行。自动审批拒绝了真实模型/TOS 调用，要求本次具体载荷、目的地与最多 8 次付费请求获得明确授权，已向用户询问。全阶段不得标记完成；本地验证结果及真实结果随后补充。

D07 本地验收：`make verify-d06`（包含新增协议/重试/凭证用例）及全量 `make test-race` 通过；后者真实 MariaDB 集成耗时 80.562s。新增并行工具跨帧参数、观测组件标识与检查点加载断言后，Eino/Gateway/domain 针对性 race 与 TestContextStages 再次通过。日志为 .cache/d07-regression.log、.cache/d07-race.log。真实验证仍未执行，D07 保持进行中；没有新增迁移，schema 保持 7，改动尚未提交。

## E15.9 D08 完整调试工作台（2026-10-24）

按 E10 允许在 D07 真实凭证验证阻塞时先完成页面。基于 db55c28，数据库 schema 仍为 7，无新迁移。Agent 固定版本直接进入幂等调试 Session；实际模型配置、定义快照、工具、日志和快照继续沿用既有执行路径。D07 的真实外部调用没有执行，不因本阶段验收改变其状态。

AguiRunClient 对快照运行时校验，帧经 Schema/reducer 后才推进 UI 游标；重新授权查询后受控重连，去重、缺口拒绝、旧回调隔离、终态停止及连续失败转人工同步。修复旧前端因 CUSTOM 成功状态提前关闭而漏接标准终态的问题。公共 ui-snapshot 去掉内部摘要标记，后端仍保留回放用途。大载荷按流实际字节限制 1 MiB；消息/工具按 50 条窗口展示，Trace 保留分类/水位分页/来源/下载，Markdown 控制 HTML、图片和 URL。

新 `/runs/{run_id}/metrics` 通过 OpenAPI 契约校验，按 purpose/前缀阶段聚合已结束尝试、实际缓存统计覆盖率、配对输入占比及可用耗时；未知不补零，不把公共前缀当命中。统计同空间固定水位、最多 20000 事件、校验事件版本；大对象按需读取，空间切换取消查询和旧 SSE，过期会话/撤权清理私有内容。

本地证据：既有 `make verify-d06` 全流程通过；`make test-debug-e2e` 覆盖固定调试快照→Session→工具→产物→完整 Trace/投影/实际请求，随后编辑草稿不改变模型输入；在文本 START 中断 SSE 后从已应用游标恢复，刷新不调用模型/工具，空间切换停止旧订阅；Bob 活动订阅期间被撤权后内容清空，产物下载拒绝。首次新增成员测试误用了 201 而实际接口是 204，DELETE 需显式 JSON Content-Type，修正测试契约后通过。桌面和 390px 截图为 .cache/d08-desktop.png / d08-mobile.png。

前端 4 文件/13 用例通过，涵盖缺口/重复/非法帧不确认、部分消息恢复、CUSTOM 与标准终态区别、无权限清空、不可信快照，以及缺少 Content-Length 的大流拒绝/跨字节 UTF-8。生成契约、TypeScript、ESLint、go vet、依赖边界与 diff 检查通过。全量 `make test-race` 通过（数据库集成 61.497s）。日志为 .cache/d08-regression.log、d08-browser.log、d08-race.log、d08-final-check.log；工具参数 ARGS 中途断线后十四工具、计划修订 2 与不可变产物恢复通过；最终 HTTP/数据库针对性 race 通过（集成 60.769s），日志为 .cache/d08-tool-reconnect.log 与 d08-metrics-final.log。

新增 `make verify-d08` 与 CI 默认调用，保持全 MVP `make verify` 在 D10 前明确失败；本目标仅本地协议与 UI 验证，不包含待授权的真实模型/TOS 测量。改动尚未提交。
