# Git 提交

- Git 提交信息使用英文。

# MASS 会话与 Run 排查

收到 `session_id` 或 `run_id` 后，先下载对应记录，按 Run 定位失败原因。会话消息、模型与工具步骤、运行终态共用一份 `session.jsonl`，通过 `run_id` 区分各轮，通过 `seq` 确定顺序。JSONL 的 `seq` 与 Trace 页面的事件序号各自独立。

## 下载诊断 JSONL

优先使用当前部署的 `GET /api/v1/diagnostics`。接口需要已登录且有空间访问权限的 `mass_session` Cookie。只传 `session_id` 或 `run_id` 中的一个。接口读取 TOS 上的会话记录，返回包含 `items` 的 JSON，用 `jq -c '.items[]'` 保存为每行一条记录的 JSONL。

```sh
# 使用已登录的 curl Cookie jar。服务地址按实际部署填写。
: "${MASS_COOKIE_JAR:?请设置已登录的 Cookie jar 路径}"
MASS_URL=${MASS_URL:-http://127.0.0.1:8080}
LOOKUP_FIELD=session_id  # 只查一轮时改为 run_id
LOOKUP_ID='填入实际 ID'

umask 077
DIAG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/mass-diag.XXXXXX") &&
  curl -fsS --cookie "$MASS_COOKIE_JAR" --get \
    --data-urlencode "$LOOKUP_FIELD=$LOOKUP_ID" \
    --output "$DIAG_DIR/diagnostics.json" "$MASS_URL/api/v1/diagnostics" &&
  jq -c '.items[]' "$DIAG_DIR/diagnostics.json" > "$DIAG_DIR/$LOOKUP_ID.jsonl"
```

也可以在已登录的浏览器里打开同一个诊断 URL，保存 JSON 响应后用相同的 `jq` 表达式转换。遇到 `401` 时检查登录状态与服务地址，遇到 `404` 时核对 ID、部署实例和空间访问权限。

### 读取宿主或 TOS 原始记录

当服务不可用、需要检查快照清单，或需要确认 TOS 上实际保存了哪一轮时，查看原始 `session.jsonl`。从会话页面 URL 获取 `space_id`。只有 `run_id` 时，可用诊断接口直接查该轮；如果需要原始对象且只有 `run_id`，通过有权限的只读数据库查询将 `runs.session_id` 关联到 `sessions.id`，取 `sessions.space_id` 和会话 ID。

- 宿主文件为 `<runtime.workspace_temp_root>/.sessions/<prefix>/<space_id>/sessions/<session_id>/session.jsonl`。
- TOS 对象键为 `<prefix>/<space_id>/sessions/<session_id>/session.jsonl`。按部署的 `tos.prefix` 和 Bucket 在 TOS 中下载这个对象。前缀为空时省去该段。
- 诊断接口会省略请求去重信息和快照详情，用 `workspace_snapshot_committed` 表示快照是否提交。原始记录保留 `run_finished.workspace_snapshot` 及其中的 `manifest.local_files`。
- 排查上传时对照宿主文件和诊断接口的最后一条记录。MASS 先写宿主文件，再覆盖上传到 TOS。如果宿主已有 `run_finished`，而诊断接口仍只有 `run_accepted`，检查 `session upload pending` 等服务日志，必要时下载原始 TOS 对象核对。

## 定位最新 Run 和失败环节

拿到整个会话的 JSONL 时，用最后一条 `run_accepted` 确定最新 Run。这也能找到尚未产生 `run_finished` 的一轮。用户给定 `run_id` 时直接选指定的 Run。

```sh
SESSION_JSONL="$DIAG_DIR/$LOOKUP_ID.jsonl"  # 这里使用整个会话的文件
RUN_ID=$(jq -ser 'map(select(.type == "run_accepted")) | max_by(.seq).run_id' "$SESSION_JSONL") &&
  jq -c --arg id "$RUN_ID" 'select(.run_id == $id)' "$SESSION_JSONL" > "$DIAG_DIR/$RUN_ID.jsonl"
```

按 `run_id` 直接下载时，先设置 `RUN_ID=$LOOKUP_ID`。先看终态，再按 `seq` 检查模型和工具步骤。

```sh
RUN_JSONL="$DIAG_DIR/$RUN_ID.jsonl"

jq -c 'select(.type == "run_finished") |
  {seq, run_id, started_at, finished_at, status, error, snapshot_error, failure_detail,
   snapshot_committed: (if has("workspace_snapshot_committed") then .workspace_snapshot_committed
                        else .workspace_snapshot != null end)}' "$RUN_JSONL"

jq -c 'select(.type == "model_step") |
  {seq, step, attempt_id, retry_of, purpose, started_at,
   model: (.model | {status, http_status, error, finish_reason}),
   tools: [.tools[]? | {name, tool_call_id, call_id, status,
     error: .result.error, side_effect: .result.side_effect}]}' "$RUN_JSONL"
```

1. 找到与终态有关的第一个异常。用 `model.tool_calls[].id` 对应 `tools[].tool_call_id`，查看该次调用的参数和 `result.data.error_detail`，宿主命令再看退出码及 `stderr`。模型 HTTP 200 只表示请求成功返回，工具结果要单独核对。发生重试时用 `retry_of` 判断异常是否已恢复。
2. 分别检查运行错误和快照结果。例如 `tool_effect_unknown` 要追到具体工具及其 `side_effect`；`snapshot_error=workspace_snapshot_failed` 要检查本地工作区和归档路径。工作区归档超限时，按配置统计实际参与归档的普通文件字节数，排除符号链接与大于 `max_object_bytes` 的文件。快照失败时，`session.jsonl` 仍会记录终态并尝试上传。
3. 只有 `run_accepted` 时，查运行详情、实时 Trace 或服务日志。模型和工具步骤会在 Run 收尾时批量写入，TOS 上传失败也可能让远端暂时只显示接收记录。结合运行状态和宿主日志确认；证据缺失时说明缺失的记录。
4. 给出 Run ID、异常时间、对应的 `seq` 和工具或错误码，说明直接触发点、最终 Run 状态和快照结果。日志留在临时目录，结论引用所需的诊断字段。
