package com.afinn.mass

import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.afinn.mass.ui.theme.MassColors
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

private data class LiveUiMessage(
    val id: String,
    val text: String,
    val closed: Boolean,
    val incomplete: Boolean = false
)

private data class LiveRunState(
    val runId: String,
    val status: String = "queued",
    val error: String = "",
    val cancelRequested: Boolean = false,
    val messages: List<LiveUiMessage> = emptyList(),
    val tools: List<ToolCallState> = emptyList(),
    val plan: RunPlan? = null
)

private data class SubmittedDraft(
    val run: RunSummary,
    val text: String,
    val attachments: List<AttachmentInfo>
)

private data class AssistantStep(
    val id: String,
    val text: String,
    val live: LiveUiMessage? = null,
    val truncated: Boolean = false,
    val tools: List<ToolCallState> = emptyList()
)

private data class ConversationTurn(
    val key: String,
    val runId: String?,
    val user: ChatMessage? = null,
    val assistant: List<ChatMessage> = emptyList()
)

private fun runStatusLabel(status: String): String = when (status) {
    "queued" -> "排队中"
    "running" -> "运行中"
    "succeeded" -> "已完成"
    "failed" -> "失败"
    "cancelled" -> "已取消"
    else -> status
}

private fun toolStatusLabel(status: String): String = when (status) {
    "parameters", "parameters_complete" -> "准备中"
    "not_started" -> "待执行"
    "running" -> "运行中"
    "completed", "succeeded" -> "已完成"
    "failed", "error" -> "失败"
    "cancelled" -> "已取消"
    "result_unavailable" -> "结果不可用"
    else -> status.ifBlank { "运行中" }
}

private fun isRunStatusActive(status: String?): Boolean = status == "queued" || status == "running"

@Composable
fun SessionChatScreen(
    navKey: String,
    baseUrl: String,
    token: String,
    spaceId: String,
    agentId: String,
    initialSession: SessionSummary?,
    refreshTrigger: Int,
    onTitleUpdated: (String) -> Unit,
    onSessionUpserted: (SessionSummary) -> Unit,
    onLatestRunChanged: (String) -> Unit = {},
    onBack: () -> Unit,
    modifier: Modifier = Modifier
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val listState = rememberLazyListState()

    var session by remember(navKey) { mutableStateOf(initialSession) }
    var messages by remember(navKey) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var runs by remember(navKey) { mutableStateOf<List<RunSummary>>(emptyList()) }
    var hasMoreOlder by remember(navKey) { mutableStateOf(false) }
    var loading by remember(navKey) { mutableStateOf(initialSession != null) }
    var loadingOlder by remember(navKey) { mutableStateOf(false) }
    var errorMessage by remember(navKey) { mutableStateOf("") }

    var inputText by remember(navKey) { mutableStateOf("") }
    var inputFocused by remember(navKey) { mutableStateOf(false) }
    var pendingAttachments by remember(navKey) { mutableStateOf<List<AttachmentInfo>>(emptyList()) }
    var uploadingAttachment by remember(navKey) { mutableStateOf(false) }
    var sending by remember(navKey) { mutableStateOf(false) }
    var pendingDraftText by remember(navKey) { mutableStateOf<String?>(null) }
    var acceptedDraft by remember(navKey) { mutableStateOf<SubmittedDraft?>(null) }

    var liveState by remember(navKey) { mutableStateOf<LiveRunState?>(null) }
    var streamJob by remember(navKey) { mutableStateOf<Job?>(null) }

    BackHandler {
        onBack()
    }

    DisposableEffect(navKey) {
        onDispose {
            streamJob?.cancel()
        }
    }

    suspend fun ensureSessionCreated(): SessionSummary {
        session?.let { return it }
        val created = withContext(Dispatchers.IO) {
            MassOpenApiClient.createSession(
                baseUrl = baseUrl,
                token = token,
                spaceId = spaceId,
                agentId = agentId
            )
        }
        session = created
        onTitleUpdated(created.displayTitle)
        onSessionUpserted(created)
        return created
    }

    fun startStreamingRun(targetSessionId: String, runId: String) {
        streamJob?.cancel()
        liveState = LiveRunState(runId = runId, status = "queued")

        streamJob = scope.launch {
            var currentState = LiveRunState(runId = runId, status = "queued")
            var streamErr = ""

            fun publish(next: LiveRunState) {
                currentState = next
                scope.launch {
                    liveState = next
                }
            }

            try {
                withContext(Dispatchers.IO) {
                    MassOpenApiClient.streamRunEvents(
                        baseUrl = baseUrl,
                        token = token,
                        spaceId = spaceId,
                        runId = runId,
                        after = "0",
                        isCancelled = { !isActive }
                    ) { event ->
                        val next = reduceAguiEvent(currentState, event.type, event.payload)
                        publish(next)
                    }
                }
            } catch (t: Throwable) {
                if (isActive) {
                    streamErr = t.message ?: "事件流连接中断"
                }
            } finally {
                if (isActive) {
                    // Fetch settled persisted messages/runs/session BEFORE clearing liveState,
                    // so the turn smoothly swaps from streaming state to final persisted result in one step.
                    val settled = runCatching {
                        withContext(Dispatchers.IO) {
                            val detail = runCatching {
                                MassOpenApiClient.getSession(baseUrl, token, spaceId, targetSessionId)
                            }.getOrNull()
                            val msgPage = MassOpenApiClient.listMessages(
                                baseUrl = baseUrl,
                                token = token,
                                spaceId = spaceId,
                                sessionId = targetSessionId,
                                beforeSequence = "0",
                                limit = 50
                            )
                            val updatedRuns = runCatching {
                                MassOpenApiClient.listRuns(baseUrl, token, spaceId, targetSessionId, limit = 20)
                            }.getOrNull()
                            Triple(detail, msgPage, updatedRuns)
                        }
                    }.getOrNull()

                    if (settled != null) {
                        val (detail, msgPage, updatedRuns) = settled
                        if (detail != null) {
                            session = detail
                            onTitleUpdated(detail.displayTitle)
                            onSessionUpserted(detail)
                        }
                        messages = msgPage.items
                        hasMoreOlder = msgPage.hasMore
                        if (updatedRuns != null) {
                            runs = updatedRuns
                        }
                        if (acceptedDraft != null &&
                            msgPage.items.any { it.sourceEventId == acceptedDraft?.run?.userEventId || it.runId == runId }
                        ) {
                            acceptedDraft = null
                        }
                        // Atomically replace streaming messages with persisted result once run settles.
                        val latestRun = updatedRuns?.firstOrNull { it.id == runId }
                        val stillRunning = latestRun?.isActive == true
                        if (!stillRunning) {
                            liveState = null
                        }
                    } else if (streamErr.isNotBlank()) {
                        errorMessage = streamErr
                        liveState = currentState.copy(
                            status = if (isRunStatusActive(currentState.status)) "failed" else currentState.status,
                            error = currentState.error.ifBlank { streamErr }
                        )
                    }
                }
            }
        }
    }

    LaunchedEffect(navKey, refreshTrigger) {
        val currentSession = session ?: initialSession
        if (currentSession == null) {
            loading = false
            return@LaunchedEffect
        }
        loading = true
        errorMessage = ""
        try {
            val loaded = withContext(Dispatchers.IO) {
                val detail = runCatching {
                    MassOpenApiClient.getSession(baseUrl, token, spaceId, currentSession.id)
                }.getOrDefault(currentSession)
                val msgPage = MassOpenApiClient.listMessages(
                    baseUrl = baseUrl,
                    token = token,
                    spaceId = spaceId,
                    sessionId = currentSession.id,
                    beforeSequence = "0",
                    limit = 50
                )
                val runList = runCatching {
                    MassOpenApiClient.listRuns(baseUrl, token, spaceId, currentSession.id, limit = 20)
                }.getOrDefault(emptyList())
                Triple(detail, msgPage, runList)
            }
            val (detail, msgPage, runList) = loaded
            session = detail
            onTitleUpdated(detail.displayTitle)
            onSessionUpserted(detail)
            messages = msgPage.items
            hasMoreOlder = msgPage.hasMore
            runs = runList
            val running = runList.firstOrNull { it.isActive }
            if (running != null && liveState?.runId != running.id) {
                startStreamingRun(detail.id, running.id)
            }
        } catch (t: Throwable) {
            errorMessage = t.message ?: "加载会话失败"
        } finally {
            loading = false
        }
    }

    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocument()
    ) { uri: Uri? ->
        if (uri == null) return@rememberLauncherForActivityResult
        scope.launch {
            uploadingAttachment = true
            errorMessage = ""
            try {
                val ensuredSession = ensureSessionCreated()
                val uploaded = withContext(Dispatchers.IO) {
                    val cr = context.contentResolver
                    val mime = cr.getType(uri) ?: "application/octet-stream"
                    var fileName = "attachment"
                    cr.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
                        if (c.moveToFirst()) {
                            val idx = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                            if (idx >= 0) {
                                fileName = c.getString(idx).orEmpty().ifBlank { fileName }
                            }
                        }
                    }
                    val bytes = cr.openInputStream(uri)?.use { it.readBytes() }
                        ?: throw IllegalStateException("无法读取所选文件")
                    if (bytes.size > 8 * 1024 * 1024) {
                        throw IllegalStateException("附件大小超过 8 MiB 上限")
                    }
                    MassOpenApiClient.uploadAttachment(
                        baseUrl = baseUrl,
                        token = token,
                        spaceId = spaceId,
                        sessionId = ensuredSession.id,
                        name = fileName,
                        mediaType = mime,
                        bytes = bytes
                    )
                }
                pendingAttachments = pendingAttachments + uploaded
            } catch (t: Throwable) {
                errorMessage = t.message ?: "上传附件失败"
            } finally {
                uploadingAttachment = false
            }
        }
    }

    val sortedRuns = remember(runs) {
        runs.sortedWith(compareByDescending<RunSummary> { it.createdAt }.thenByDescending { it.id })
    }
    val latestRun = sortedRuns.firstOrNull()
    val effectiveRunStatus = remember(latestRun, liveState) {
        val streamSt = liveState?.status
        when {
            liveState != null && streamSt != null -> streamSt
            latestRun != null -> latestRun.status
            else -> ""
        }
    }
    val busy = isRunStatusActive(effectiveRunStatus) || (liveState != null && isRunStatusActive(liveState?.status))
    val stopRequested = (liveState?.cancelRequested == true) || (latestRun?.cancelRequested == true)
    val activeRunId = liveState?.runId ?: latestRun?.takeIf { it.isActive }?.id.orEmpty()
    val reportedRunId = liveState?.runId ?: latestRun?.id.orEmpty()
    LaunchedEffect(reportedRunId) {
        onLatestRunChanged(reportedRunId)
    }

    // Group persisted history + active run into ConversationTurns exactly like frontend Conversation.tsx
    val turns = remember(messages, sortedRuns, acceptedDraft, latestRun, liveState) {
        val runsByOrigin = sortedRuns
            .filter { it.userEventId.isNotBlank() }
            .associate { it.userEventId to it.id }
        val result = mutableListOf<ConversationTurn>()
        for (msg in messages) {
            val rid = msg.runId.ifBlank { runsByOrigin[msg.sourceEventId].orEmpty() }.ifBlank { null }
            val last = result.lastOrNull()
            if (last == null || rid == null || last.runId != rid || msg.role == "user") {
                val created = ConversationTurn(
                    key = rid ?: msg.id,
                    runId = rid,
                    user = if (msg.role == "user") msg else null,
                    assistant = if (msg.role == "user") emptyList() else listOf(msg)
                )
                result.add(created)
            } else {
                result[result.lastIndex] = last.copy(assistant = last.assistant + msg)
            }
        }

        val draft = acceptedDraft
        if (draft != null && messages.none { it.sourceEventId == draft.run.userEventId && draft.run.userEventId.isNotBlank() }) {
            val draftUserMsg = ChatMessage(
                id = "accepted-user-${draft.run.id}",
                runId = draft.run.id,
                role = "user",
                sequence = "",
                content = draft.text,
                attachments = draft.attachments
            )
            val existingIdx = result.indexOfFirst { it.runId == draft.run.id }
            if (existingIdx >= 0) {
                if (result[existingIdx].user == null) {
                    result[existingIdx] = result[existingIdx].copy(user = draftUserMsg)
                }
            } else {
                result.add(ConversationTurn(key = draft.run.id, runId = draft.run.id, user = draftUserMsg))
            }
        }

        val liveId = liveState?.runId ?: latestRun?.id
        if (!liveId.isNullOrBlank() && result.none { it.runId == liveId }) {
            result.add(ConversationTurn(key = liveId, runId = liveId))
        }
        result
    }

    val scrollSignature = remember(
        turns.size,
        liveState?.messages?.size,
        liveState?.messages?.lastOrNull()?.text?.length,
        liveState?.tools?.size,
        pendingDraftText
    ) {
        "${turns.size}:${liveState?.messages?.size}:${liveState?.messages?.lastOrNull()?.text?.length}:${liveState?.tools?.size}:${pendingDraftText != null}"
    }

    LaunchedEffect(scrollSignature) {
        val totalItems = turns.size +
            (if (hasMoreOlder && session != null) 1 else 0) +
            (if (liveState?.plan != null) 1 else 0) +
            (if (latestRun != null || liveState != null) 1 else 0) +
            (if (pendingDraftText != null) 1 else 0)
        if (totalItems > 0) {
            listState.scrollToItem(totalItems - 1)
        }
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .background(MassColors.Bg)
            .imePadding()
    ) {
        if (errorMessage.isNotBlank()) {
            Surface(
                color = MassColors.ErrorBg,
                modifier = Modifier.fillMaxWidth()
            ) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 14.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = errorMessage,
                        color = MassColors.Danger,
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.weight(1f)
                    )
                    IconButton(onClick = { errorMessage = "" }, modifier = Modifier.size(28.dp)) {
                        Icon(
                            Icons.Default.Close,
                            contentDescription = "关闭",
                            tint = MassColors.Danger,
                            modifier = Modifier.size(16.dp)
                        )
                    }
                }
            }
        }

        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading && turns.isEmpty() -> {
                    Column(
                        modifier = Modifier.fillMaxSize(),
                        verticalArrangement = Arrangement.Center,
                        horizontalAlignment = Alignment.CenterHorizontally
                    ) {
                        CircularProgressIndicator(
                            color = MassColors.Focus,
                            strokeWidth = 2.5.dp,
                            modifier = Modifier.size(28.dp)
                        )
                        Spacer(modifier = Modifier.height(10.dp))
                        Text("正在读取消息…", color = MassColors.Muted, fontSize = 13.sp)
                    }
                }

                turns.isEmpty() && pendingDraftText == null -> {
                    Column(
                        modifier = Modifier
                            .fillMaxSize()
                            .padding(24.dp),
                        verticalArrangement = Arrangement.Center,
                        horizontalAlignment = Alignment.CenterHorizontally
                    ) {
                        Icon(
                            painter = painterResource(id = R.drawable.ic_mass_bot),
                            contentDescription = null,
                            tint = MassColors.AccentText,
                            modifier = Modifier.size(42.dp)
                        )
                        Spacer(modifier = Modifier.height(12.dp))
                        Text(
                            text = "开始对话",
                            color = MassColors.Text,
                            fontSize = 18.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                        Spacer(modifier = Modifier.height(6.dp))
                        Text(
                            text = "发送第一条消息开始对话",
                            color = MassColors.Muted,
                            fontSize = 13.sp
                        )
                    }
                }

                else -> {
                    LazyColumn(
                        state = listState,
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(horizontal = 16.dp, vertical = 16.dp),
                        verticalArrangement = Arrangement.spacedBy(20.dp)
                    ) {
                        if (hasMoreOlder && session != null) {
                            item(key = "load-older") {
                                Box(
                                    modifier = Modifier.fillMaxWidth(),
                                    contentAlignment = Alignment.Center
                                ) {
                                    if (loadingOlder) {
                                        CircularProgressIndicator(
                                            color = MassColors.Focus,
                                            strokeWidth = 2.dp,
                                            modifier = Modifier.size(22.dp)
                                        )
                                    } else {
                                        TextButton(onClick = {
                                            val currentId = session?.id.orEmpty()
                                            val firstSeq = messages.firstOrNull()?.sequence.orEmpty()
                                            if (currentId.isBlank() || firstSeq.isBlank() || loadingOlder) return@TextButton
                                            scope.launch {
                                                loadingOlder = true
                                                try {
                                                    val older = withContext(Dispatchers.IO) {
                                                        MassOpenApiClient.listMessages(
                                                            baseUrl = baseUrl,
                                                            token = token,
                                                            spaceId = spaceId,
                                                            sessionId = currentId,
                                                            beforeSequence = firstSeq,
                                                            limit = 50
                                                        )
                                                    }
                                                    val existing = messages.map { it.id }.toSet()
                                                    messages = older.items.filterNot { it.id in existing } + messages
                                                    hasMoreOlder = older.hasMore
                                                } catch (t: Throwable) {
                                                    errorMessage = t.message ?: "加载历史消息失败"
                                                } finally {
                                                    loadingOlder = false
                                                }
                                            }
                                        }) {
                                            Text("加载更早的消息", color = MassColors.AccentText, fontSize = 12.sp)
                                        }
                                    }
                                }
                            }
                        }

                        items(turns, key = { it.key }) { turn ->
                            val turnLiveState = if (turn.runId != null && turn.runId == liveState?.runId) liveState else null
                            Column(
                                modifier = Modifier.fillMaxWidth(),
                                verticalArrangement = Arrangement.spacedBy(16.dp)
                            ) {
                                if (turn.user != null && (turn.user.content.isNotBlank() || turn.user.attachments.isNotEmpty())) {
                                    UserMessageBubble(message = turn.user, pending = false)
                                }
                                AssistantTurnView(
                                    messages = turn.assistant,
                                    liveState = turnLiveState
                                )
                            }
                        }

                        val currentPlan = liveState?.plan
                        if (currentPlan != null && currentPlan.steps.isNotEmpty()) {
                            item(key = "run-plan-${currentPlan.revision}") {
                                RunPlanCard(plan = currentPlan)
                            }
                        }

                        if (latestRun != null || liveState != null) {
                            item(key = "run-status") {
                                val hasLiveText = liveState?.messages?.any { it.text.isNotEmpty() && !it.closed } == true
                                val statusErr = liveState?.error?.ifBlank { latestRun?.error.orEmpty() }
                                    ?: latestRun?.error.orEmpty()
                                RunStatusRow(
                                    status = effectiveRunStatus,
                                    busy = busy,
                                    stopRequested = stopRequested,
                                    hasLiveText = hasLiveText,
                                    error = statusErr
                                )
                            }
                        }

                        if (pendingDraftText != null) {
                            item(key = "pending-user") {
                                UserMessageBubble(
                                    message = ChatMessage(
                                        id = "pending-user",
                                        runId = "",
                                        role = "user",
                                        sequence = "",
                                        content = pendingDraftText.orEmpty(),
                                        attachments = pendingAttachments
                                    ),
                                    pending = true
                                )
                            }
                        }
                    }
                }
            }
        }

        if (pendingAttachments.isNotEmpty()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 14.dp, vertical = 4.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                pendingAttachments.forEach { att ->
                    Surface(
                        color = MassColors.AccentSoft,
                        shape = RoundedCornerShape(8.dp)
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier.padding(start = 9.dp, end = 3.dp, top = 3.dp, bottom = 3.dp)
                        ) {
                            Text(
                                text = att.name,
                                color = MassColors.AccentText,
                                fontSize = 12.sp,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.widthIn(max = 150.dp)
                            )
                            IconButton(
                                onClick = { pendingAttachments = pendingAttachments.filterNot { it.id == att.id } },
                                modifier = Modifier.size(22.dp)
                            ) {
                                Icon(
                                    Icons.Default.Close,
                                    contentDescription = "移除附件",
                                    tint = MassColors.AccentText,
                                    modifier = Modifier.size(13.dp)
                                )
                            }
                        }
                    }
                }
            }
        }

        // Composer matching frontend .chat-composer
        val composerShape = RoundedCornerShape(20.dp)
        val composerBorder = if (inputFocused) MassColors.Focus else MassColors.Border.copy(alpha = 0.7f)
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 12.dp, vertical = 10.dp)
                .clip(composerShape)
                .background(MassColors.SurfaceRaised)
                .border(1.dp, composerBorder, composerShape)
                .padding(start = 6.dp, end = 8.dp, top = 6.dp, bottom = 6.dp),
            verticalAlignment = Alignment.Bottom,
            horizontalArrangement = Arrangement.spacedBy(6.dp)
        ) {
            IconButton(
                enabled = !uploadingAttachment && !busy && !sending,
                onClick = { filePicker.launch(arrayOf("*/*")) },
                modifier = Modifier.size(38.dp)
            ) {
                if (uploadingAttachment) {
                    CircularProgressIndicator(
                        color = MassColors.Focus,
                        modifier = Modifier.size(18.dp),
                        strokeWidth = 2.dp
                    )
                } else {
                    Icon(
                        Icons.Default.Add,
                        contentDescription = "添加附件",
                        tint = MassColors.Muted
                    )
                }
            }

            Box(
                modifier = Modifier
                    .weight(1f)
                    .padding(vertical = 8.dp),
                contentAlignment = Alignment.CenterStart
            ) {
                if (inputText.isEmpty()) {
                    Text(
                        text = "输入任务，继续对话…",
                        color = MassColors.Subtle,
                        fontSize = 14.5.sp
                    )
                }
                BasicTextField(
                    value = inputText,
                    onValueChange = { inputText = it },
                    textStyle = TextStyle(
                        color = MassColors.Text,
                        fontSize = 14.5.sp,
                        lineHeight = 22.sp
                    ),
                    cursorBrush = SolidColor(MassColors.Focus),
                    maxLines = 5,
                    modifier = Modifier
                        .fillMaxWidth()
                        .onFocusChanged { inputFocused = it.isFocused }
                )
            }

            if (busy && activeRunId.isNotBlank()) {
                Button(
                    enabled = !stopRequested,
                    shape = RoundedCornerShape(10.dp),
                    contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = MassColors.ErrorBg,
                        contentColor = MassColors.Danger,
                        disabledContainerColor = MassColors.Surface,
                        disabledContentColor = MassColors.Muted
                    ),
                    modifier = Modifier.height(38.dp),
                    onClick = {
                        val runToCancel = activeRunId
                        scope.launch {
                            runCatching {
                                withContext(Dispatchers.IO) {
                                    MassOpenApiClient.cancelRun(baseUrl, token, spaceId, runToCancel)
                                }
                            }.onSuccess { cancelled ->
                                runs = listOf(cancelled) + runs.filterNot { it.id == cancelled.id }
                                liveState = liveState?.copy(cancelRequested = true)
                            }.onFailure { err ->
                                errorMessage = err.message ?: "取消失败"
                            }
                        }
                    }
                ) {
                    Text(if (stopRequested) "正在停止" else "停止", fontSize = 13.sp)
                }
            } else {
                val canSend = inputText.isNotBlank() && !sending && !uploadingAttachment
                Surface(
                    shape = RoundedCornerShape(10.dp),
                    color = if (canSend) MassColors.Accent else MassColors.Hover,
                    modifier = Modifier.size(38.dp)
                ) {
                    IconButton(
                        enabled = canSend,
                        onClick = {
                            val textToSend = inputText.trim()
                            if (textToSend.isEmpty()) return@IconButton
                            val attachmentsToSend = pendingAttachments
                            inputText = ""
                            pendingAttachments = emptyList()
                            pendingDraftText = textToSend
                            sending = true
                            errorMessage = ""

                            scope.launch {
                                try {
                                    val ensuredSession = ensureSessionCreated()
                                    val run = withContext(Dispatchers.IO) {
                                        MassOpenApiClient.submitRun(
                                            baseUrl = baseUrl,
                                            token = token,
                                            spaceId = spaceId,
                                            sessionId = ensuredSession.id,
                                            text = textToSend,
                                            attachmentIds = attachmentsToSend.map { it.id }
                                        )
                                    }
                                    pendingDraftText = null
                                    acceptedDraft = SubmittedDraft(run, textToSend, attachmentsToSend)
                                    runs = listOf(run) + runs.filterNot { it.id == run.id }
                                    startStreamingRun(ensuredSession.id, run.id)
                                } catch (t: Throwable) {
                                    pendingDraftText = null
                                    inputText = textToSend
                                    pendingAttachments = attachmentsToSend
                                    errorMessage = t.message ?: "发送消息失败"
                                } finally {
                                    sending = false
                                }
                            }
                        }
                    ) {
                        if (sending) {
                            CircularProgressIndicator(
                                color = Color.White,
                                modifier = Modifier.size(18.dp),
                                strokeWidth = 2.dp
                            )
                        } else {
                            Icon(
                                Icons.AutoMirrored.Filled.Send,
                                contentDescription = "发送",
                                tint = if (canSend) Color.White else MassColors.Subtle,
                                modifier = Modifier.size(18.dp)
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun UserMessageBubble(
    message: ChatMessage,
    pending: Boolean
) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.End
    ) {
        Surface(
            color = MassColors.SurfaceRaised,
            shape = RoundedCornerShape(16.dp),
            modifier = Modifier.widthIn(max = 310.dp)
        ) {
            Column(
                modifier = Modifier.padding(horizontal = 15.dp, vertical = 11.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                if (message.attachments.isNotEmpty()) {
                    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        message.attachments.forEach { att ->
                            Text(
                                text = "📎 ${att.name}",
                                color = MassColors.AccentText,
                                fontSize = 12.sp,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                    }
                }
                Text(
                    text = message.content.ifEmpty { "(空消息)" },
                    color = MassColors.Text,
                    fontSize = 14.5.sp,
                    lineHeight = 23.sp
                )
                if (pending) {
                    Text(
                        text = "正在发送…",
                        color = MassColors.Muted,
                        fontSize = 11.5.sp
                    )
                }
            }
        }
    }
}

@Composable
private fun AssistantTurnView(
    messages: List<ChatMessage>,
    liveState: LiveRunState?
) {
    val (steps, unattachedTools) = remember(messages, liveState) {
        buildAssistantSteps(messages, liveState)
    }
    if (steps.isEmpty() && unattachedTools.isEmpty()) return

    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            Icon(
                painter = painterResource(id = R.drawable.ic_mass_bot),
                contentDescription = "AI助手",
                tint = MassColors.AccentText,
                modifier = Modifier.size(22.dp)
            )
            Text(
                text = "AI助手",
                color = MassColors.Text,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold
            )
        }

        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 30.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            steps.forEach { step ->
                Column(
                    modifier = Modifier.fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(6.dp)
                ) {
                    if (step.text.isNotEmpty()) {
                        val isLiveUnclosed = step.live != null && (!step.live.closed || step.live.incomplete)
                        if (isLiveUnclosed) {
                            val showCursor = !step.live!!.closed && !step.live.incomplete
                            Text(
                                text = if (showCursor) "${step.text} ▍" else step.text,
                                color = MassColors.Text,
                                fontSize = 14.5.sp,
                                lineHeight = 23.sp
                            )
                        } else {
                            MassMarkdown(text = step.text)
                        }
                    }
                    if (step.live?.incomplete == true) {
                        Text(
                            text = "输出未完成",
                            color = MassColors.Warning,
                            fontSize = 12.sp
                        )
                    }
                    if (step.truncated) {
                        Text(
                            text = "（内容超过 8192 字节已在历史接口截断）",
                            color = MassColors.Warning,
                            fontSize = 12.sp
                        )
                    }
                    if (step.tools.isNotEmpty()) {
                        ToolActivityCollapsible(tools = step.tools)
                    }
                }
            }

            if (unattachedTools.isNotEmpty()) {
                ToolActivityCollapsible(tools = unattachedTools)
            }
        }
    }
}

@Composable
private fun ToolActivityCollapsible(tools: List<ToolCallState>) {
    if (tools.isEmpty()) return
    var expanded by remember { mutableStateOf(false) }
    val runningCount = tools.count { it.status == "running" || it.status == "parameters" || it.status == "parameters_complete" }

    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(6.dp)
    ) {
        Row(
            modifier = Modifier
                .clip(RoundedCornerShape(6.dp))
                .clickable { expanded = !expanded }
                .padding(vertical = 4.dp, horizontal = 2.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp)
        ) {
            Text(
                text = if (expanded) "▾" else "▸",
                color = MassColors.Muted,
                fontSize = 12.sp
            )
            Text(
                text = if (runningCount > 0) "工具调用 · ${tools.size}（运行中）" else "工具调用 · ${tools.size}",
                color = MassColors.Muted,
                fontSize = 12.5.sp
            )
        }

        if (expanded) {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(start = 12.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                tools.forEach { tool ->
                    SingleToolItem(tool = tool)
                }
            }
        }
    }
}

@Composable
private fun SingleToolItem(tool: ToolCallState) {
    var detailOpen by remember(tool.id) { mutableStateOf(false) }
    val statusColor = when (tool.status) {
        "completed", "succeeded" -> MassColors.Success
        "failed", "error", "result_unavailable" -> MassColors.Danger
        "cancelled" -> MassColors.Warning
        else -> MassColors.Focus
    }

    Surface(
        color = MassColors.Surface,
        shape = RoundedCornerShape(10.dp),
        border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.7f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.fillMaxWidth()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { detailOpen = !detailOpen }
                    .padding(horizontal = 10.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                Text(
                    text = if (detailOpen) "▾" else "▸",
                    color = MassColors.Muted,
                    fontSize = 11.5.sp
                )
                Text(
                    text = tool.name,
                    color = MassColors.Text,
                    fontSize = 12.5.sp,
                    fontFamily = FontFamily.Monospace,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f)
                )
                Text(
                    text = toolStatusLabel(tool.status),
                    color = statusColor,
                    fontSize = 11.5.sp
                )
            }

            if (detailOpen) {
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(start = 10.dp, end = 10.dp, bottom = 10.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    if (tool.standard && tool.arguments.isNotBlank()) {
                        ToolPayloadBox(label = "参数", content = tool.arguments)
                    } else if (!tool.standard) {
                        Text(
                            text = "完整参数可在运行详情 Trace 中查看。",
                            color = MassColors.Muted,
                            fontSize = 11.5.sp
                        )
                    }
                    if (tool.result.isNotBlank()) {
                        ToolPayloadBox(label = "结果", content = tool.result)
                    }
                }
            }
        }
    }
}

@Composable
private fun ToolPayloadBox(label: String, content: String) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(text = label, color = MassColors.Subtle, fontSize = 11.sp)
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .clip(RoundedCornerShape(8.dp))
                .background(MassColors.SurfaceRaised)
                .horizontalScroll(rememberScrollState())
                .padding(10.dp)
        ) {
            Text(
                text = content.take(4000),
                color = MassColors.Text,
                fontSize = 11.5.sp,
                lineHeight = 18.sp,
                fontFamily = FontFamily.Monospace
            )
        }
    }
}

@Composable
private fun RunPlanCard(plan: RunPlan) {
    var open by remember { mutableStateOf(true) }
    val total = plan.steps.size
    val resolved = plan.steps.count { it.status == "completed" || it.status == "skipped" }
    val active = plan.steps.firstOrNull { it.status == "in_progress" }
    val allDone = total > 0 && resolved == total
    val ratio = if (total > 0) resolved.toFloat() / total.toFloat() else 0f

    Surface(
        color = MassColors.Surface,
        shape = RoundedCornerShape(14.dp),
        border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.7f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Column(modifier = Modifier.fillMaxWidth()) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(2.dp)
                    .background(Color.White.copy(alpha = 0.06f))
            ) {
                Box(
                    modifier = Modifier
                        .fillMaxWidth(ratio)
                        .height(2.dp)
                        .background(if (allDone) MassColors.Success else MassColors.Focus)
                )
            }
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { open = !open }
                    .padding(horizontal = 14.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Text(
                    text = "任务计划",
                    color = MassColors.Text,
                    fontSize = 12.5.sp,
                    fontWeight = FontWeight.SemiBold
                )
                Text(
                    text = "$resolved/$total",
                    color = when {
                        allDone -> MassColors.Success
                        active != null -> MassColors.Focus
                        else -> MassColors.Muted
                    },
                    fontSize = 12.sp
                )
                if (!open && active != null) {
                    Text(
                        text = "· ${active.title}",
                        color = MassColors.Muted,
                        fontSize = 12.sp,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f)
                    )
                } else {
                    Spacer(modifier = Modifier.weight(1f))
                }
                Text(
                    text = if (open) "▾" else "▸",
                    color = MassColors.Muted,
                    fontSize = 12.sp
                )
            }
            if (open) {
                Column(
                    modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp),
                    verticalArrangement = Arrangement.spacedBy(6.dp)
                ) {
                    plan.steps.forEach { step ->
                        val dotColor = when (step.status) {
                            "completed" -> MassColors.Success
                            "in_progress" -> MassColors.Focus
                            "skipped" -> MassColors.Subtle
                            else -> MassColors.Border
                        }
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            Box(
                                modifier = Modifier
                                    .size(7.dp)
                                    .clip(CircleShape)
                                    .background(dotColor)
                            )
                            Text(
                                text = step.title,
                                color = if (step.status == "skipped") MassColors.Subtle else MassColors.Text,
                                fontSize = 12.5.sp,
                                modifier = Modifier.weight(1f)
                            )
                            if (step.status == "in_progress") {
                                Text("进行中", color = MassColors.Focus, fontSize = 11.sp)
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun RunStatusRow(
    status: String,
    busy: Boolean,
    stopRequested: Boolean,
    hasLiveText: Boolean,
    error: String
) {
    if (status.isBlank()) return
    val textColor = when (status) {
        "failed" -> MassColors.Danger
        "succeeded" -> MassColors.Muted
        else -> MassColors.Muted
    }
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp)
    ) {
        when {
            busy -> {
                Box(
                    modifier = Modifier
                        .size(8.dp)
                        .clip(CircleShape)
                        .background(MassColors.Focus)
                )
            }
            status == "succeeded" -> {
                Text("✓", color = MassColors.Success, fontSize = 13.sp, fontWeight = FontWeight.Bold)
            }
            status == "failed" -> {
                Text("!", color = MassColors.Danger, fontSize = 13.sp, fontWeight = FontWeight.Bold)
            }
        }
        Text(
            text = runStatusLabel(status),
            color = textColor,
            fontSize = 12.sp,
            fontWeight = FontWeight.SemiBold
        )
        val detail = when {
            busy && stopRequested -> "正在停止，等待文件收尾"
            error.isNotBlank() -> error
            busy && !hasLiveText -> "等待回复…"
            else -> ""
        }
        if (detail.isNotBlank()) {
            Text(
                text = detail,
                color = if (status == "failed") MassColors.Danger else MassColors.Muted,
                fontSize = 12.sp,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis
            )
        }
    }
}

/**
 * Mirrors frontend Conversation.tsx AssistantTurn step-merging logic:
 * merges persisted messages and live streamed messages by message ID,
 * attaches tools via parentMessageId, and folds textless tool steps into the previous step.
 */
private fun buildAssistantSteps(
    messages: List<ChatMessage>,
    liveState: LiveRunState?
): Pair<List<AssistantStep>, List<ToolCallState>> {
    val positions = messages.mapIndexed { idx, msg -> msg.id to idx }.toMap()
    val liveToolsById = liveState?.tools?.associateBy { it.id }.orEmpty()
    val toolsByMessage = linkedMapOf<String, MutableList<ToolCallState>>()
    liveToolsById.values.forEach { tool ->
        if (tool.parentMessageId.isNotBlank()) {
            toolsByMessage.getOrPut(tool.parentMessageId) { mutableListOf() }.add(tool)
        }
    }

    val attachedIds = mutableSetOf<String>()
    val steps = mutableListOf<AssistantStep>()

    fun addStep(saved: ChatMessage?, live: LiveUiMessage?) {
        val id = live?.id ?: saved?.id ?: return
        val savedText = saved?.content.orEmpty()
        val text = if (live != null && live.text.length >= savedText.length) live.text else savedText
        val truncated = saved?.truncated == true && text.length <= savedText.length

        val mergedTools = linkedMapOf<String, ToolCallState>()
        saved?.tools?.forEach { mergedTools[it.id] = it }
        toolsByMessage[id]?.forEach { tool ->
            if (!mergedTools.containsKey(tool.id)) {
                mergedTools[tool.id] = tool
            }
        }
        val calls = mergedTools.values.map { tool ->
            attachedIds.add(tool.id)
            val active = liveToolsById[tool.id]
            if (active != null) {
                active.copy(
                    arguments = active.arguments.ifBlank { tool.arguments },
                    result = active.result.ifBlank { tool.result }
                )
            } else {
                tool
            }
        }

        if (text.isEmpty() && !truncated) {
            if (calls.isEmpty()) return
            val previous = steps.lastOrNull()
            if (previous != null) {
                steps[steps.lastIndex] = previous.copy(tools = previous.tools + calls)
                return
            }
        }
        steps.add(
            AssistantStep(
                id = id,
                text = text,
                live = live,
                truncated = truncated,
                tools = calls
            )
        )
    }

    var next = 0
    for (liveMsg in liveState?.messages.orEmpty()) {
        val pos = positions[liveMsg.id]
        if (pos == null) {
            addStep(null, liveMsg)
        } else {
            while (next < pos) {
                addStep(messages[next++], null)
            }
            addStep(messages[next++], liveMsg)
        }
    }
    while (next < messages.size) {
        addStep(messages[next++], null)
    }

    val unattached = liveToolsById.values.filter { it.id !in attachedIds }
    return steps to unattached
}

/**
 * Mirrors frontend stream.ts reduceEvent() for AG-UI SSE events.
 */
private fun reduceAguiEvent(
    previous: LiveRunState,
    type: String,
    payload: JSONObject
): LiveRunState {
    val msgs = previous.messages.toMutableList()
    val tools = previous.tools.toMutableList()
    var status = previous.status
    var error = previous.error
    var cancelRequested = previous.cancelRequested
    var plan = previous.plan

    fun ensureParentMessage(parentId: String): String {
        if (parentId.isBlank()) return ""
        if (msgs.none { it.id == parentId }) {
            msgs.add(LiveUiMessage(id = parentId, text = "", closed = true, incomplete = false))
        }
        return parentId
    }

    when (type) {
        "RUN_STARTED" -> {
            status = "queued"
        }
        "RUN_FINISHED" -> {
            status = "succeeded"
        }
        "RUN_ERROR" -> {
            val code = payload.optString("code")
            val msg = payload.optString("message").ifBlank { code }
            status = if (code == "RUN_CANCELLED") "cancelled" else "failed"
            error = if (code == "RUN_CANCELLED") "run_cancelled" else msg.ifBlank { "run_failed" }
        }
        "TEXT_MESSAGE_START", "THINKING_TEXT_MESSAGE_START", "REASONING_MESSAGE_START" -> {
            val id = payload.optString("messageId").ifBlank { "msg-${msgs.size}" }
            if (msgs.none { it.id == id }) {
                msgs.add(LiveUiMessage(id = id, text = "", closed = false, incomplete = false))
            }
        }
        "TEXT_MESSAGE_CONTENT", "THINKING_TEXT_MESSAGE_CONTENT", "REASONING_MESSAGE_CONTENT" -> {
            val id = payload.optString("messageId").ifBlank { msgs.lastOrNull()?.id ?: "msg-0" }
            val delta = payload.optString("delta")
            val idx = msgs.indexOfFirst { it.id == id }
            if (idx >= 0) {
                msgs[idx] = msgs[idx].copy(text = msgs[idx].text + delta)
            } else {
                msgs.add(LiveUiMessage(id = id, text = delta, closed = false, incomplete = false))
            }
        }
        "TEXT_MESSAGE_END", "THINKING_TEXT_MESSAGE_END", "REASONING_MESSAGE_END" -> {
            val id = payload.optString("messageId")
            val idx = msgs.indexOfFirst { it.id == id }
            if (idx >= 0) {
                msgs[idx] = msgs[idx].copy(closed = true)
            }
        }
        "TOOL_CALL_START" -> {
            val id = payload.optString("toolCallId")
            val name = payload.optString("toolCallName").ifBlank { "tool" }
            val parentId = ensureParentMessage(payload.optString("parentMessageId"))
            if (id.isNotBlank() && tools.none { it.id == id }) {
                tools.add(
                    ToolCallState(
                        id = id,
                        name = name,
                        status = "parameters",
                        standard = true,
                        parentMessageId = parentId
                    )
                )
            }
        }
        "TOOL_CALL_ARGS" -> {
            val id = payload.optString("toolCallId")
            val delta = payload.optString("delta")
            val idx = tools.indexOfFirst { it.id == id }
            if (idx >= 0) {
                tools[idx] = tools[idx].copy(arguments = tools[idx].arguments + delta)
            }
        }
        "TOOL_CALL_END" -> {
            val id = payload.optString("toolCallId")
            val idx = tools.indexOfFirst { it.id == id }
            if (idx >= 0) {
                tools[idx] = tools[idx].copy(status = "parameters_complete")
            }
        }
        "TOOL_CALL_RESULT" -> {
            val id = payload.optString("toolCallId")
            val content = payload.optString("content")
            val idx = tools.indexOfFirst { it.id == id }
            if (idx >= 0) {
                tools[idx] = tools[idx].copy(result = content, status = "completed")
            }
        }
        "CUSTOM" -> {
            val customName = payload.optString("name")
            val v = payload.optJSONObject("value")
            if (v != null) {
                when (customName) {
                    "mass.run.status.v1" -> {
                        val st = v.optString("status")
                        if (st.isNotBlank()) status = st
                        if (v.has("cancelRequested")) {
                            cancelRequested = v.optBoolean("cancelRequested", cancelRequested)
                        }
                    }
                    "mass.message.status.v1" -> {
                        val msgId = v.optString("messageId")
                        if (v.optString("status") == "incomplete") {
                            val idx = msgs.indexOfFirst { it.id == msgId }
                            if (idx >= 0) {
                                msgs[idx] = msgs[idx].copy(incomplete = true)
                            }
                        }
                    }
                    "mass.tool.status.v1" -> {
                        val toolId = v.optString("toolCallId")
                        if (toolId.isNotBlank()) {
                            val st = v.optString("status")
                            val idx = tools.indexOfFirst { it.id == toolId }
                            if (idx >= 0) {
                                if (st.isNotBlank()) {
                                    tools[idx] = tools[idx].copy(status = st)
                                }
                            } else {
                                val toolName = v.optString("toolName").ifBlank { "tool" }
                                val parentId = ensureParentMessage(v.optString("parentMessageId"))
                                tools.add(
                                    ToolCallState(
                                        id = toolId,
                                        name = toolName,
                                        status = st,
                                        standard = false,
                                        parentMessageId = parentId
                                    )
                                )
                            }
                        }
                    }
                    "mass.plan.v1" -> {
                        val planObj = v.optJSONObject("plan")
                        if (planObj != null) {
                            val rev = planObj.optLong("revision", 0L)
                            val stepsArr = planObj.optJSONArray("steps")
                            val parsedSteps = buildList {
                                if (stepsArr != null) {
                                    for (i in 0 until stepsArr.length()) {
                                        val s = stepsArr.optJSONObject(i) ?: continue
                                        add(
                                            PlanStep(
                                                id = s.optString("id"),
                                                title = s.optString("title"),
                                                status = s.optString("status")
                                            )
                                        )
                                    }
                                }
                            }
                            if (rev >= 1 && (plan == null || rev >= plan.revision)) {
                                plan = RunPlan(revision = rev, steps = parsedSteps)
                            }
                        }
                    }
                }
            }
        }
    }

    return previous.copy(
        status = status,
        error = error,
        cancelRequested = cancelRequested,
        messages = msgs,
        tools = tools,
        plan = plan
    )
}
