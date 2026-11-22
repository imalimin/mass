package com.afinn.mass

import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Send
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@Composable
fun SessionChatScreen(
    baseUrl: String,
    token: String,
    spaceId: String,
    initialSession: SessionSummary,
    agentDisplayName: String,
    refreshTrigger: Int,
    onTitleUpdated: (String) -> Unit,
    onBack: () -> Unit,
    modifier: Modifier = Modifier
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val listState = rememberLazyListState()

    var session by remember(initialSession.id) { mutableStateOf(initialSession) }
    var messages by remember(initialSession.id) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var hasMoreOlder by remember(initialSession.id) { mutableStateOf(false) }
    var loading by remember(initialSession.id) { mutableStateOf(true) }
    var loadingOlder by remember(initialSession.id) { mutableStateOf(false) }
    var errorMessage by remember(initialSession.id) { mutableStateOf("") }

    var inputText by remember(initialSession.id) { mutableStateOf("") }
    var pendingAttachments by remember(initialSession.id) { mutableStateOf<List<AttachmentInfo>>(emptyList()) }
    var uploadingAttachment by remember(initialSession.id) { mutableStateOf(false) }
    var sending by remember(initialSession.id) { mutableStateOf(false) }

    var activeRunId by remember(initialSession.id) { mutableStateOf("") }
    var activeRunStatus by remember(initialSession.id) { mutableStateOf("") }
    var streamingMessages by remember(initialSession.id) { mutableStateOf<List<ChatMessage>>(emptyList()) }
    var streamingTools by remember(initialSession.id) { mutableStateOf<List<ToolCallState>>(emptyList()) }
    var streamJob by remember(initialSession.id) { mutableStateOf<Job?>(null) }

    BackHandler {
        onBack()
    }

    DisposableEffect(initialSession.id) {
        onDispose {
            streamJob?.cancel()
        }
    }

    suspend fun reloadMessages(scrollToBottom: Boolean) {
        val page = withContext(Dispatchers.IO) {
            MassOpenApiClient.listMessages(
                baseUrl = baseUrl,
                token = token,
                spaceId = spaceId,
                sessionId = session.id,
                beforeSequence = "0",
                limit = 50
            )
        }
        messages = page.items
        hasMoreOlder = page.hasMore
        if (scrollToBottom && page.items.isNotEmpty()) {
            listState.animateScrollToItem(page.items.size - 1)
        }
    }

    fun startStreamingRun(runId: String) {
        streamJob?.cancel()
        activeRunId = runId
        activeRunStatus = "running"
        streamingMessages = emptyList()
        streamingTools = emptyList()

        streamJob = scope.launch {
            val streamTexts = linkedMapOf<String, StringBuilder>()
            val streamToolMap = linkedMapOf<String, ToolCallState>()
            var runFailedMsg = ""

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
                        val p = event.payload
                        when (event.type) {
                            "RUN_STARTED" -> {
                                scope.launch { activeRunStatus = "running" }
                            }
                            "TEXT_MESSAGE_START" -> {
                                val id = p.optString("messageId").ifBlank { "stream-${streamTexts.size}" }
                                streamTexts.getOrPut(id) { StringBuilder() }
                                val snapshot = streamTexts.entries.map { (msgId, sb) ->
                                    ChatMessage(
                                        id = "stream-$msgId",
                                        runId = runId,
                                        role = "assistant",
                                        sequence = "",
                                        content = sb.toString(),
                                        complete = false
                                    )
                                }
                                scope.launch { streamingMessages = snapshot }
                            }
                            "TEXT_MESSAGE_CONTENT" -> {
                                val id = p.optString("messageId").ifBlank { streamTexts.keys.lastOrNull() ?: "stream-0" }
                                val delta = p.optString("delta")
                                streamTexts.getOrPut(id) { StringBuilder() }.append(delta)
                                val snapshot = streamTexts.entries.map { (msgId, sb) ->
                                    ChatMessage(
                                        id = "stream-$msgId",
                                        runId = runId,
                                        role = "assistant",
                                        sequence = "",
                                        content = sb.toString(),
                                        complete = false
                                    )
                                }
                                scope.launch { streamingMessages = snapshot }
                            }
                            "TEXT_MESSAGE_END" -> {
                                val snapshot = streamTexts.entries.map { (msgId, sb) ->
                                    ChatMessage(
                                        id = "stream-$msgId",
                                        runId = runId,
                                        role = "assistant",
                                        sequence = "",
                                        content = sb.toString(),
                                        complete = true
                                    )
                                }
                                scope.launch { streamingMessages = snapshot }
                            }
                            "TOOL_CALL_START" -> {
                                val id = p.optString("toolCallId")
                                val name = p.optString("toolCallName").ifBlank { "tool" }
                                if (id.isNotBlank()) {
                                    streamToolMap[id] = ToolCallState(id = id, name = name, status = "running")
                                    val toolsSnapshot = streamToolMap.values.toList()
                                    scope.launch { streamingTools = toolsSnapshot }
                                }
                            }
                            "TOOL_CALL_ARGS" -> {
                                val id = p.optString("toolCallId")
                                val delta = p.optString("delta")
                                streamToolMap[id]?.let { existing ->
                                    streamToolMap[id] = existing.copy(arguments = existing.arguments + delta)
                                }
                            }
                            "TOOL_CALL_RESULT" -> {
                                val id = p.optString("toolCallId")
                                val content = p.optString("content")
                                streamToolMap[id]?.let { existing ->
                                    streamToolMap[id] = existing.copy(status = "completed", result = content)
                                    val toolsSnapshot = streamToolMap.values.toList()
                                    scope.launch { streamingTools = toolsSnapshot }
                                }
                            }
                            "CUSTOM" -> {
                                val customName = p.optString("name")
                                val value = p.optJSONObject("value")
                                if (customName == "mass.run.status.v1" && value != null) {
                                    val st = value.optString("status")
                                    if (st.isNotBlank()) {
                                        scope.launch { activeRunStatus = st }
                                    }
                                } else if (customName == "mass.tool.status.v1" && value != null) {
                                    val id = value.optString("toolCallId")
                                    val st = value.optString("status")
                                    val toolName = value.optString("toolName").ifBlank { "tool" }
                                    if (id.isNotBlank()) {
                                        val existing = streamToolMap[id] ?: ToolCallState(id = id, name = toolName, status = st)
                                        streamToolMap[id] = existing.copy(status = st.ifBlank { existing.status })
                                        val toolsSnapshot = streamToolMap.values.toList()
                                        scope.launch { streamingTools = toolsSnapshot }
                                    }
                                }
                            }
                            "RUN_ERROR" -> {
                                val code = p.optString("code")
                                val msg = p.optString("message").ifBlank { code }
                                if (code != "RUN_CANCELLED") {
                                    runFailedMsg = msg.ifBlank { "Run failed" }
                                }
                            }
                        }
                    }
                }
            } catch (t: Throwable) {
                if (isActive) {
                    errorMessage = t.message ?: "Stream disconnected"
                }
            } finally {
                if (isActive) {
                    if (runFailedMsg.isNotBlank()) {
                        errorMessage = runFailedMsg
                    }
                    activeRunId = ""
                    activeRunStatus = ""
                    streamingMessages = emptyList()
                    streamingTools = emptyList()
                    runCatching {
                        val updated = withContext(Dispatchers.IO) {
                            MassOpenApiClient.getSession(baseUrl, token, spaceId, session.id)
                        }
                        session = updated
                        onTitleUpdated(updated.displayTitle)
                    }
                    runCatching { reloadMessages(scrollToBottom = true) }
                }
            }
        }
    }

    LaunchedEffect(initialSession.id, refreshTrigger) {
        loading = true
        errorMessage = ""
        try {
            val loaded = withContext(Dispatchers.IO) {
                val detail = runCatching {
                    MassOpenApiClient.getSession(baseUrl, token, spaceId, initialSession.id)
                }.getOrDefault(initialSession)
                val msgPage = MassOpenApiClient.listMessages(
                    baseUrl = baseUrl,
                    token = token,
                    spaceId = spaceId,
                    sessionId = initialSession.id,
                    beforeSequence = "0",
                    limit = 50
                )
                val runs = runCatching {
                    MassOpenApiClient.listRuns(baseUrl, token, spaceId, initialSession.id, limit = 5)
                }.getOrDefault(emptyList())
                Triple(detail, msgPage, runs)
            }
            val (detail, msgPage, runs) = loaded
            session = detail
            onTitleUpdated(detail.displayTitle)
            messages = msgPage.items
            hasMoreOlder = msgPage.hasMore
            if (msgPage.items.isNotEmpty()) {
                listState.scrollToItem(msgPage.items.size - 1)
            }
            val running = runs.firstOrNull { it.isActive }
            if (running != null && activeRunId != running.id) {
                startStreamingRun(running.id)
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
                        sessionId = session.id,
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

    val combinedMessages = remember(messages, streamingMessages, streamingTools, activeRunId) {
        if (activeRunId.isBlank()) {
            messages
        } else {
            val streamList = if (streamingMessages.isNotEmpty()) {
                streamingMessages.mapIndexed { index, msg ->
                    if (index == streamingMessages.lastIndex && streamingTools.isNotEmpty()) {
                        msg.copy(tools = streamingTools)
                    } else {
                        msg
                    }
                }
            } else {
                listOf(
                    ChatMessage(
                        id = "stream-pending-$activeRunId",
                        runId = activeRunId,
                        role = "assistant",
                        sequence = "",
                        content = "",
                        complete = false,
                        tools = streamingTools
                    )
                )
            }
            messages + streamList
        }
    }

    LaunchedEffect(combinedMessages.size, streamingMessages.lastOrNull()?.content?.length) {
        if (combinedMessages.isNotEmpty()) {
            listState.scrollToItem(combinedMessages.lastIndex)
        }
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .imePadding()
    ) {
        if (errorMessage.isNotBlank()) {
            Surface(color = MaterialTheme.colorScheme.errorContainer) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 12.dp, vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = errorMessage,
                        color = MaterialTheme.colorScheme.onErrorContainer,
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.weight(1f)
                    )
                    IconButton(onClick = { errorMessage = "" }, modifier = Modifier.size(28.dp)) {
                        Icon(
                            Icons.Default.Close,
                            contentDescription = "Dismiss",
                            tint = MaterialTheme.colorScheme.onErrorContainer
                        )
                    }
                }
            }
        }

        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading && combinedMessages.isEmpty() -> {
                    Column(
                        modifier = Modifier.fillMaxSize(),
                        verticalArrangement = Arrangement.Center,
                        horizontalAlignment = Alignment.CenterHorizontally
                    ) {
                        CircularProgressIndicator()
                        Spacer(modifier = Modifier.height(10.dp))
                        Text("正在加载会话消息…", style = MaterialTheme.typography.bodySmall)
                    }
                }

                combinedMessages.isEmpty() -> {
                    Column(
                        modifier = Modifier
                            .fillMaxSize()
                            .padding(24.dp),
                        verticalArrangement = Arrangement.Center,
                        horizontalAlignment = Alignment.CenterHorizontally
                    ) {
                        Text(
                            text = agentDisplayName.ifBlank { session.definitionId },
                            style = MaterialTheme.typography.titleMedium
                        )
                        Spacer(modifier = Modifier.height(6.dp))
                        Text(
                            text = "发送第一条消息开始对话",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }

                else -> {
                    LazyColumn(
                        state = listState,
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 12.dp),
                        verticalArrangement = Arrangement.spacedBy(10.dp)
                    ) {
                        if (hasMoreOlder) {
                            item(key = "load-older") {
                                Box(
                                    modifier = Modifier.fillMaxWidth(),
                                    contentAlignment = Alignment.Center
                                ) {
                                    if (loadingOlder) {
                                        CircularProgressIndicator(modifier = Modifier.size(24.dp))
                                    } else {
                                        TextButton(onClick = {
                                            val firstSeq = messages.firstOrNull()?.sequence.orEmpty()
                                            if (firstSeq.isBlank() || loadingOlder) return@TextButton
                                            scope.launch {
                                                loadingOlder = true
                                                try {
                                                    val older = withContext(Dispatchers.IO) {
                                                        MassOpenApiClient.listMessages(
                                                            baseUrl = baseUrl,
                                                            token = token,
                                                            spaceId = spaceId,
                                                            sessionId = session.id,
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
                                            Text("加载更早消息")
                                        }
                                    }
                                }
                            }
                        }

                        items(combinedMessages, key = { it.id }) { msg ->
                            ChatMessageBubble(
                                message = msg,
                                streaming = activeRunId.isNotBlank() && msg.runId == activeRunId && !msg.complete
                            )
                        }
                    }
                }
            }
        }

        HorizontalDivider()

        if (pendingAttachments.isNotEmpty()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 12.dp, vertical = 6.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                pendingAttachments.forEach { att ->
                    Surface(
                        color = MaterialTheme.colorScheme.secondaryContainer,
                        shape = MaterialTheme.shapes.small
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier.padding(start = 8.dp, end = 2.dp, top = 2.dp, bottom = 2.dp)
                        ) {
                            Text(
                                text = att.name,
                                style = MaterialTheme.typography.labelSmall,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.widthIn(max = 140.dp)
                            )
                            IconButton(
                                onClick = { pendingAttachments = pendingAttachments.filterNot { it.id == att.id } },
                                modifier = Modifier.size(22.dp)
                            ) {
                                Icon(
                                    Icons.Default.Close,
                                    contentDescription = "Remove attachment",
                                    modifier = Modifier.size(14.dp)
                                )
                            }
                        }
                    }
                }
            }
        }

        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 10.dp, vertical = 8.dp),
            verticalAlignment = Alignment.Bottom,
            horizontalArrangement = Arrangement.spacedBy(6.dp)
        ) {
            IconButton(
                enabled = !uploadingAttachment && activeRunId.isBlank() && !sending,
                onClick = { filePicker.launch(arrayOf("*/*")) }
            ) {
                if (uploadingAttachment) {
                    CircularProgressIndicator(modifier = Modifier.size(20.dp), strokeWidth = 2.dp)
                } else {
                    Icon(Icons.Default.Add, contentDescription = "Attach file")
                }
            }

            OutlinedTextField(
                value = inputText,
                onValueChange = { inputText = it },
                placeholder = { Text("输入消息…") },
                maxLines = 4,
                modifier = Modifier.weight(1f)
            )

            if (activeRunId.isNotBlank()) {
                Button(
                    colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
                    onClick = {
                        val runToCancel = activeRunId
                        scope.launch {
                            runCatching {
                                withContext(Dispatchers.IO) {
                                    MassOpenApiClient.cancelRun(baseUrl, token, spaceId, runToCancel)
                                }
                            }.onFailure { err ->
                                errorMessage = err.message ?: "取消失败"
                            }
                        }
                    }
                ) {
                    Text("停止")
                }
            } else {
                IconButton(
                    enabled = inputText.isNotBlank() && !sending && !uploadingAttachment,
                    onClick = {
                        val textToSend = inputText.trim()
                        if (textToSend.isEmpty()) return@IconButton
                        val attachmentsToSend = pendingAttachments
                        inputText = ""
                        pendingAttachments = emptyList()
                        sending = true
                        errorMessage = ""

                        val optimisticUserMsg = ChatMessage(
                            id = "local-user-${System.currentTimeMillis()}",
                            runId = "",
                            role = "user",
                            sequence = "",
                            content = textToSend,
                            complete = true,
                            attachments = attachmentsToSend
                        )
                        messages = messages + optimisticUserMsg

                        scope.launch {
                            try {
                                val run = withContext(Dispatchers.IO) {
                                    MassOpenApiClient.submitRun(
                                        baseUrl = baseUrl,
                                        token = token,
                                        spaceId = spaceId,
                                        sessionId = session.id,
                                        text = textToSend,
                                        attachmentIds = attachmentsToSend.map { it.id }
                                    )
                                }
                                startStreamingRun(run.id)
                            } catch (t: Throwable) {
                                messages = messages.filterNot { it.id == optimisticUserMsg.id }
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
                        CircularProgressIndicator(modifier = Modifier.size(20.dp), strokeWidth = 2.dp)
                    } else {
                        Icon(Icons.Default.Send, contentDescription = "Send")
                    }
                }
            }
        }
    }
}

@Composable
private fun ChatMessageBubble(
    message: ChatMessage,
    streaming: Boolean
) {
    val isUser = message.role == "user"
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = if (isUser) Arrangement.End else Arrangement.Start
    ) {
        Surface(
            color = if (isUser) {
                MaterialTheme.colorScheme.primaryContainer
            } else {
                MaterialTheme.colorScheme.surfaceVariant
            },
            shape = MaterialTheme.shapes.medium,
            modifier = Modifier.widthIn(max = 320.dp)
        ) {
            Column(
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                if (message.attachments.isNotEmpty()) {
                    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        message.attachments.forEach { att ->
                            Surface(
                                color = MaterialTheme.colorScheme.surface.copy(alpha = 0.65f),
                                shape = MaterialTheme.shapes.extraSmall
                            ) {
                                Text(
                                    text = "📎 ${att.name}",
                                    style = MaterialTheme.typography.labelSmall,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                                )
                            }
                        }
                    }
                }

                if (message.tools.isNotEmpty()) {
                    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        message.tools.forEach { tool ->
                            Surface(
                                color = MaterialTheme.colorScheme.surface.copy(alpha = 0.7f),
                                shape = MaterialTheme.shapes.extraSmall,
                                modifier = Modifier.fillMaxWidth()
                            ) {
                                Row(
                                    modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp),
                                    horizontalArrangement = Arrangement.SpaceBetween,
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Text(
                                        text = "Tool: ${tool.name}",
                                        style = MaterialTheme.typography.labelSmall,
                                        fontFamily = FontFamily.Monospace,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis,
                                        modifier = Modifier.weight(1f)
                                    )
                                    Spacer(modifier = Modifier.width(6.dp))
                                    Text(
                                        text = tool.status.ifBlank { "running" },
                                        style = MaterialTheme.typography.labelSmall,
                                        color = MaterialTheme.colorScheme.primary
                                    )
                                }
                            }
                        }
                    }
                }

                val displayText = when {
                    message.content.isNotEmpty() -> if (streaming) "${message.content} ▍" else message.content
                    streaming -> "正在思考…"
                    message.tools.isNotEmpty() -> ""
                    else -> "(空消息)"
                }
                if (displayText.isNotEmpty()) {
                    Text(
                        text = displayText,
                        style = MaterialTheme.typography.bodyMedium,
                        color = if (isUser) {
                            MaterialTheme.colorScheme.onPrimaryContainer
                        } else {
                            MaterialTheme.colorScheme.onSurfaceVariant
                        }
                    )
                }

                if (message.truncated) {
                    Text(
                        text = "（内容超过 8192 字节已在历史接口截断）",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        }
    }
}
