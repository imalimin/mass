package com.afinn.mass

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@Composable
fun SessionListScreen(
    config: MassConfig,
    serverStatus: MassStatus,
    refreshTrigger: Int,
    onResolvedSpace: (String) -> Unit,
    onSelectSession: (spaceId: String, session: SessionSummary, agentDisplayName: String) -> Unit,
    onOpenSettings: () -> Unit,
    modifier: Modifier = Modifier
) {
    val scope = rememberCoroutineScope()
    val baseUrl = config.effectiveApiBaseUrl(serverStatus)
    val backendReady = config.useRemoteServer || serverStatus.isRunning
    val effectiveAgentId = config.agentId.trim().ifBlank { MassConfig.DEFAULT_AGENT_ID }

    var resolvedSpaceId by remember(config.spaceId, baseUrl, config.openapiToken) {
        mutableStateOf(config.spaceId.trim().ifBlank { MassConfig.DEFAULT_SPACE_ID })
    }
    var sessions by remember { mutableStateOf<List<SessionSummary>>(emptyList()) }
    var agentMap by remember { mutableStateOf<Map<String, AgentSummary>>(emptyMap()) }
    var nextCursor by remember { mutableStateOf("") }
    var loading by remember { mutableStateOf(false) }
    var loadingMore by remember { mutableStateOf(false) }
    var errorMessage by remember { mutableStateOf("") }

    var showCreateDialog by remember { mutableStateOf(false) }
    var creating by remember { mutableStateOf(false) }
    var createError by remember { mutableStateOf("") }

    suspend fun loadInitial() {
        if (!backendReady) return
        loading = true
        errorMessage = ""
        try {
            val result = withContext(Dispatchers.IO) {
                val spaceId = config.spaceId.trim().ifBlank { MassConfig.DEFAULT_SPACE_ID }
                val agents = runCatching {
                    MassOpenApiClient.listAgents(baseUrl, config.openapiToken, spaceId)
                }.getOrDefault(emptyList())
                val page = MassOpenApiClient.listSessions(
                    baseUrl = baseUrl,
                    token = config.openapiToken,
                    spaceId = spaceId,
                    agentId = effectiveAgentId,
                    cursor = "",
                    limit = 50
                )
                Triple(spaceId, agents, page)
            }
            val (spaceId, agents, page) = result
            resolvedSpaceId = spaceId
            onResolvedSpace(spaceId)
            agentMap = agents.associateBy { it.id }
            sessions = page.items.filterNot { it.hide }
                .sortedWith(compareByDescending<SessionSummary> { it.createdAt }.thenByDescending { it.id })
            nextCursor = page.nextCursor
        } catch (t: Throwable) {
            errorMessage = t.message ?: "Failed to load sessions"
        } finally {
            loading = false
        }
    }

    LaunchedEffect(backendReady, baseUrl, config.openapiToken, config.spaceId, effectiveAgentId, refreshTrigger) {
        if (backendReady) {
            loadInitial()
        }
    }

    Box(modifier = modifier.fillMaxSize()) {
        when {
            !backendReady && !config.useRemoteServer -> {
                LocalServerNotReadyPlaceholder(
                    serverStatus = serverStatus,
                    onOpenSettings = onOpenSettings
                )
            }

            loading && sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    CircularProgressIndicator()
                    Spacer(modifier = Modifier.height(12.dp))
                    Text("正在通过 OpenAPI 加载会话列表…", style = MaterialTheme.typography.bodyMedium)
                }
            }

            errorMessage.isNotBlank() && sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    Text("加载会话列表失败", style = MaterialTheme.typography.titleMedium)
                    Spacer(modifier = Modifier.height(10.dp))
                    Surface(
                        color = MaterialTheme.colorScheme.errorContainer,
                        shape = MaterialTheme.shapes.medium,
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text(
                            text = errorMessage,
                            color = MaterialTheme.colorScheme.onErrorContainer,
                            style = MaterialTheme.typography.bodySmall,
                            fontFamily = FontFamily.Monospace,
                            modifier = Modifier.padding(12.dp)
                        )
                    }
                    Spacer(modifier = Modifier.height(16.dp))
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        Button(onClick = { scope.launch { loadInitial() } }) {
                            Text("重试")
                        }
                        OutlinedButton(onClick = onOpenSettings) {
                            Text("配置后端 / Token")
                        }
                    }
                }
            }

            sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    Text("暂无会话", style = MaterialTheme.typography.titleMedium)
                    Spacer(modifier = Modifier.height(8.dp))
                    Text(
                        text = "当前 Agent（$effectiveAgentId）还没有会话，点击下方或右下角新建会话。",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(modifier = Modifier.height(16.dp))
                    Button(onClick = { showCreateDialog = true }) {
                        Text("新建会话")
                    }
                }
            }

            else -> {
                LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(
                        start = 12.dp,
                        end = 12.dp,
                        top = 10.dp,
                        bottom = 84.dp
                    ),
                    verticalArrangement = Arrangement.spacedBy(10.dp)
                ) {
                    if (errorMessage.isNotBlank()) {
                        item(key = "error-banner") {
                            Surface(
                                color = MaterialTheme.colorScheme.errorContainer,
                                shape = MaterialTheme.shapes.small,
                                modifier = Modifier.fillMaxWidth()
                            ) {
                                Text(
                                    text = errorMessage,
                                    color = MaterialTheme.colorScheme.onErrorContainer,
                                    style = MaterialTheme.typography.bodySmall,
                                    modifier = Modifier.padding(10.dp)
                                )
                            }
                        }
                    }

                    items(sessions, key = { it.id }) { item ->
                        val agentName = item.agentName.ifBlank {
                            agentMap[item.definitionId]?.name ?: item.definitionId
                        }
                        SessionListCard(
                            session = item,
                            agentName = agentName,
                            onClick = {
                                val spaceId = resolvedSpaceId.ifBlank { item.spaceId }
                                onSelectSession(spaceId, item, agentName)
                            }
                        )
                    }

                    if (nextCursor.isNotBlank()) {
                        item(key = "load-more") {
                            Box(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(vertical = 8.dp),
                                contentAlignment = Alignment.Center
                            ) {
                                if (loadingMore) {
                                    CircularProgressIndicator()
                                } else {
                                    OutlinedButton(onClick = {
                                        val spaceId = resolvedSpaceId
                                        if (spaceId.isBlank() || loadingMore) return@OutlinedButton
                                        scope.launch {
                                            loadingMore = true
                                            try {
                                                val page = withContext(Dispatchers.IO) {
                                                    MassOpenApiClient.listSessions(
                                                        baseUrl = baseUrl,
                                                        token = config.openapiToken,
                                                        spaceId = spaceId,
                                                        agentId = effectiveAgentId,
                                                        cursor = nextCursor,
                                                        limit = 50
                                                    )
                                                }
                                                val existingIds = sessions.map { it.id }.toSet()
                                                val appended = page.items.filterNot { it.hide || it.id in existingIds }
                                                sessions = (sessions + appended)
                                                    .sortedWith(compareByDescending<SessionSummary> { it.createdAt }.thenByDescending { it.id })
                                                nextCursor = page.nextCursor
                                            } catch (t: Throwable) {
                                                errorMessage = t.message ?: "Load more failed"
                                            } finally {
                                                loadingMore = false
                                            }
                                        }
                                    }) {
                                        Text("加载更多")
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        if (backendReady && resolvedSpaceId.isNotBlank()) {
            FloatingActionButton(
                onClick = {
                    createError = ""
                    showCreateDialog = true
                },
                modifier = Modifier
                    .align(Alignment.BottomEnd)
                    .padding(16.dp)
            ) {
                Icon(Icons.Default.Add, contentDescription = "New Session")
            }
        }
    }

    if (showCreateDialog) {
        val currentAgentName = agentMap[effectiveAgentId]?.name.orEmpty()
        CreateSessionDialog(
            agentId = effectiveAgentId,
            agentName = currentAgentName,
            creating = creating,
            error = createError,
            onDismiss = { if (!creating) showCreateDialog = false },
            onCreate = { title ->
                val spaceId = resolvedSpaceId.ifBlank { MassConfig.DEFAULT_SPACE_ID }
                val agentId = effectiveAgentId
                scope.launch {
                    creating = true
                    createError = ""
                    try {
                        val created = withContext(Dispatchers.IO) {
                            MassOpenApiClient.createSession(
                                baseUrl = baseUrl,
                                token = config.openapiToken,
                                spaceId = spaceId,
                                agentId = agentId,
                                title = title
                            )
                        }
                        showCreateDialog = false
                        val agentName = created.agentName.ifBlank { agentMap[agentId]?.name ?: agentId }
                        sessions = listOf(created) + sessions.filterNot { it.id == created.id }
                        onSelectSession(spaceId, created, agentName)
                    } catch (t: Throwable) {
                        createError = t.message ?: "创建会话失败"
                    } finally {
                        creating = false
                    }
                }
            }
        )
    }
}

@Composable
private fun LocalServerNotReadyPlaceholder(
    serverStatus: MassStatus,
    onOpenSettings: () -> Unit
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        if (serverStatus.isStarting) {
            CircularProgressIndicator()
            Spacer(modifier = Modifier.height(16.dp))
            Text("正在启动本地 MASS Go 服务端…", style = MaterialTheme.typography.titleMedium)
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = serverStatus.logs.lastOrNull() ?: "正在连接 MariaDB / TOS 并监听本地端口",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
        } else {
            Text(
                text = if (serverStatus.isError) "本地服务端启动失败" else "尚未连接 MASS 后端",
                style = MaterialTheme.typography.titleMedium
            )
            Spacer(modifier = Modifier.height(8.dp))
            if (serverStatus.error.isNotBlank()) {
                Surface(
                    color = MaterialTheme.colorScheme.errorContainer,
                    shape = MaterialTheme.shapes.medium,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(
                        text = serverStatus.error,
                        color = MaterialTheme.colorScheme.onErrorContainer,
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace,
                        modifier = Modifier.padding(12.dp)
                    )
                }
                Spacer(modifier = Modifier.height(12.dp))
            } else {
                Text(
                    text = "请在设置中配置形态 A 本地服务端（MariaDB + TOS），或直接填写远端 MASS 后端地址。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(16.dp))
            }
            Button(onClick = onOpenSettings) {
                Text("前往配置")
            }
        }
    }
}

@Composable
private fun SessionListCard(
    session: SessionSummary,
    agentName: String,
    onClick: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant)
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(6.dp)
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text(
                    text = session.displayTitle,
                    style = MaterialTheme.typography.titleMedium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f)
                )
                Spacer(modifier = Modifier.width(8.dp))
                Text(
                    text = formatTimestamp(session.createdAt),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                if (agentName.isNotBlank()) {
                    Surface(
                        color = MaterialTheme.colorScheme.secondaryContainer,
                        shape = MaterialTheme.shapes.extraSmall
                    ) {
                        Text(
                            text = agentName,
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSecondaryContainer,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                        )
                    }
                }
                if (session.draftRevision > 0) {
                    Surface(
                        color = MaterialTheme.colorScheme.tertiaryContainer,
                        shape = MaterialTheme.shapes.extraSmall
                    ) {
                        Text(
                            text = "草稿调试",
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onTertiaryContainer,
                            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                        )
                    }
                }
                if (session.workspaceError.isNotBlank()) {
                    Surface(
                        color = MaterialTheme.colorScheme.errorContainer,
                        shape = MaterialTheme.shapes.extraSmall
                    ) {
                        Text(
                            text = "文件待恢复",
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onErrorContainer,
                            modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                        )
                    }
                }
                Spacer(modifier = Modifier.weight(1f))
                Text(
                    text = session.id,
                    style = MaterialTheme.typography.labelSmall,
                    fontFamily = FontFamily.Monospace,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
        }
    }
}

@Composable
private fun CreateSessionDialog(
    agentId: String,
    agentName: String,
    creating: Boolean,
    error: String,
    onDismiss: () -> Unit,
    onCreate: (title: String) -> Unit
) {
    var title by remember { mutableStateOf("") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("新建会话") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(
                    text = if (agentName.isNotBlank()) "Agent: $agentName ($agentId)" else "Agent: $agentId",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )

                OutlinedTextField(
                    value = title,
                    onValueChange = { title = it },
                    label = { Text("会话标题（可选）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )

                if (error.isNotBlank()) {
                    Text(
                        text = error,
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.bodySmall
                    )
                }
            }
        },
        confirmButton = {
            Button(
                enabled = !creating,
                onClick = { onCreate(title) }
            ) {
                Text(if (creating) "创建中…" else "创建")
            }
        },
        dismissButton = {
            TextButton(enabled = !creating, onClick = onDismiss) {
                Text("取消")
            }
        }
    )
}

internal fun formatTimestamp(iso: String): String {
    if (iso.length >= 16) {
        return iso.substring(5, 16).replace('T', ' ')
    }
    return iso
}
