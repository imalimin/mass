package com.afinn.mass

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.pulltorefresh.PullToRefreshContainer
import androidx.compose.material3.pulltorefresh.rememberPullToRefreshState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.afinn.mass.ui.theme.MassColors

data class SessionListUiState(
    val loadedKey: String = "",
    val spaceId: String = MassConfig.DEFAULT_SPACE_ID,
    val sessions: List<SessionSummary> = emptyList(),
    val agentMap: Map<String, AgentSummary> = emptyMap(),
    val nextCursor: String = "",
    val loading: Boolean = false,
    val refreshing: Boolean = false,
    val loadingMore: Boolean = false,
    val errorMessage: String = ""
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionListScreen(
    config: MassConfig,
    serverStatus: MassStatus,
    uiState: SessionListUiState,
    onRefresh: () -> Unit,
    onLoadMore: () -> Unit,
    onSelectSession: (spaceId: String, session: SessionSummary, agentDisplayName: String) -> Unit,
    onNewSession: (spaceId: String, agentId: String, agentDisplayName: String) -> Unit,
    onOpenSettings: () -> Unit,
    modifier: Modifier = Modifier
) {
    val backendReady = config.useRemoteServer || serverStatus.isRunning
    val effectiveAgentId = config.agentId.trim().ifBlank { MassConfig.DEFAULT_AGENT_ID }
    val effectiveSpaceId = uiState.spaceId.ifBlank {
        config.spaceId.trim().ifBlank { MassConfig.DEFAULT_SPACE_ID }
    }

    val pullRefreshState = rememberPullToRefreshState()
    LaunchedEffect(pullRefreshState.isRefreshing) {
        if (pullRefreshState.isRefreshing) {
            if (backendReady) {
                onRefresh()
            } else {
                pullRefreshState.endRefresh()
            }
        }
    }
    LaunchedEffect(uiState.refreshing, uiState.loading) {
        if (!uiState.refreshing && !uiState.loading && pullRefreshState.isRefreshing) {
            pullRefreshState.endRefresh()
        }
    }

    val openNewDraft: () -> Unit = {
        val agentName = uiState.agentMap[effectiveAgentId]?.name ?: effectiveAgentId
        onNewSession(effectiveSpaceId, effectiveAgentId, agentName)
    }

    Box(
        modifier = modifier
            .fillMaxSize()
            .background(MassColors.Bg)
            .nestedScroll(pullRefreshState.nestedScrollConnection)
    ) {
        when {
            !backendReady && !config.useRemoteServer -> {
                LocalServerNotReadyPlaceholder(
                    serverStatus = serverStatus,
                    onOpenSettings = onOpenSettings
                )
            }

            uiState.loading && uiState.sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    CircularProgressIndicator(
                        color = MassColors.Focus,
                        strokeWidth = 2.5.dp,
                        modifier = Modifier.size(32.dp)
                    )
                    Spacer(modifier = Modifier.height(14.dp))
                    Text(
                        text = "正在加载会话列表…",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MassColors.Muted
                    )
                }
            }

            uiState.errorMessage.isNotBlank() && uiState.sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .verticalScroll(rememberScrollState())
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    Text(
                        text = "加载会话列表失败",
                        style = MaterialTheme.typography.titleMedium,
                        color = MassColors.Text
                    )
                    Spacer(modifier = Modifier.height(12.dp))
                    Surface(
                        color = MassColors.ErrorBg,
                        shape = RoundedCornerShape(10.dp),
                        border = BorderStroke(1.dp, MassColors.Danger.copy(alpha = 0.25f)),
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Text(
                            text = uiState.errorMessage,
                            color = MassColors.Danger,
                            style = MaterialTheme.typography.bodySmall,
                            fontFamily = FontFamily.Monospace,
                            modifier = Modifier.padding(14.dp)
                        )
                    }
                    Spacer(modifier = Modifier.height(18.dp))
                    Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        Button(
                            onClick = onRefresh,
                            shape = RoundedCornerShape(10.dp),
                            colors = ButtonDefaults.buttonColors(
                                containerColor = MassColors.Accent,
                                contentColor = Color.White
                            )
                        ) {
                            Text("重试")
                        }
                        OutlinedButton(
                            onClick = onOpenSettings,
                            shape = RoundedCornerShape(10.dp),
                            border = BorderStroke(1.dp, MassColors.Border),
                            colors = ButtonDefaults.outlinedButtonColors(contentColor = MassColors.Text)
                        ) {
                            Text("配置后端 / Token")
                        }
                    }
                }
            }

            uiState.sessions.isEmpty() -> {
                Column(
                    modifier = Modifier
                        .fillMaxSize()
                        .verticalScroll(rememberScrollState())
                        .padding(24.dp),
                    verticalArrangement = Arrangement.Center,
                    horizontalAlignment = Alignment.CenterHorizontally
                ) {
                    Surface(
                        color = MassColors.Surface,
                        shape = RoundedCornerShape(16.dp),
                        border = BorderStroke(1.dp, MassColors.Border),
                        modifier = Modifier.fillMaxWidth()
                    ) {
                        Column(
                            modifier = Modifier.padding(horizontal = 24.dp, vertical = 36.dp),
                            horizontalAlignment = Alignment.CenterHorizontally
                        ) {
                            Text(
                                text = "还没有会话",
                                style = MaterialTheme.typography.titleMedium,
                                color = MassColors.Text
                            )
                            Spacer(modifier = Modifier.height(8.dp))
                            Text(
                                text = "下拉可手动刷新，或点击下方按钮开始新对话",
                                style = MaterialTheme.typography.bodySmall,
                                color = MassColors.Muted
                            )
                            Spacer(modifier = Modifier.height(20.dp))
                            Button(
                                onClick = openNewDraft,
                                shape = RoundedCornerShape(10.dp),
                                colors = ButtonDefaults.buttonColors(
                                    containerColor = MassColors.Accent,
                                    contentColor = Color.White
                                )
                            ) {
                                Text("新建会话")
                            }
                        }
                    }
                }
            }

            else -> {
                LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(
                        start = 14.dp,
                        end = 14.dp,
                        top = 12.dp,
                        bottom = 88.dp
                    ),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    if (uiState.errorMessage.isNotBlank()) {
                        item(key = "error-banner") {
                            Surface(
                                color = MassColors.ErrorBg,
                                shape = RoundedCornerShape(10.dp),
                                modifier = Modifier.fillMaxWidth()
                            ) {
                                Text(
                                    text = uiState.errorMessage,
                                    color = MassColors.Danger,
                                    style = MaterialTheme.typography.bodySmall,
                                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp)
                                )
                            }
                        }
                    }

                    items(uiState.sessions, key = { it.id }) { item ->
                        val agentName = item.agentName.ifBlank {
                            uiState.agentMap[item.definitionId]?.name ?: item.definitionId
                        }
                        SessionListItemCard(
                            session = item,
                            onClick = {
                                val spaceId = effectiveSpaceId.ifBlank { item.spaceId }
                                onSelectSession(spaceId, item, agentName)
                            }
                        )
                    }

                    if (uiState.nextCursor.isNotBlank()) {
                        item(key = "load-more") {
                            Box(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(vertical = 8.dp),
                                contentAlignment = Alignment.Center
                            ) {
                                if (uiState.loadingMore) {
                                    CircularProgressIndicator(
                                        color = MassColors.Focus,
                                        strokeWidth = 2.dp,
                                        modifier = Modifier.size(24.dp)
                                    )
                                } else {
                                    OutlinedButton(
                                        onClick = onLoadMore,
                                        shape = RoundedCornerShape(10.dp),
                                        border = BorderStroke(1.dp, MassColors.Border),
                                        colors = ButtonDefaults.outlinedButtonColors(
                                            containerColor = MassColors.Surface,
                                            contentColor = MassColors.Text
                                        )
                                    ) {
                                        Text("加载更多")
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        if (pullRefreshState.isRefreshing || pullRefreshState.progress > 0f) {
            PullToRefreshContainer(
                state = pullRefreshState,
                containerColor = MassColors.SurfaceRaised,
                contentColor = MassColors.Focus,
                modifier = Modifier.align(Alignment.TopCenter)
            )
        }

        if (backendReady) {
            FloatingActionButton(
                onClick = openNewDraft,
                shape = RoundedCornerShape(14.dp),
                containerColor = MassColors.Accent,
                contentColor = Color.White,
                modifier = Modifier
                    .align(Alignment.BottomEnd)
                    .padding(16.dp)
            ) {
                Icon(Icons.Default.Add, contentDescription = "新建会话")
            }
        }
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
            CircularProgressIndicator(
                color = MassColors.Focus,
                strokeWidth = 2.5.dp,
                modifier = Modifier.size(32.dp)
            )
            Spacer(modifier = Modifier.height(16.dp))
            Text(
                text = "正在启动本地 MASS 服务端…",
                style = MaterialTheme.typography.titleMedium,
                color = MassColors.Text
            )
            Spacer(modifier = Modifier.height(8.dp))
            Text(
                text = serverStatus.logs.lastOrNull() ?: "正在连接 MariaDB / TOS 并监听本地端口",
                style = MaterialTheme.typography.bodySmall,
                color = MassColors.Muted
            )
        } else {
            Text(
                text = if (serverStatus.isError) "本地服务端启动失败" else "尚未连接 MASS 后端",
                style = MaterialTheme.typography.titleMedium,
                color = MassColors.Text
            )
            Spacer(modifier = Modifier.height(10.dp))
            if (serverStatus.error.isNotBlank()) {
                Surface(
                    color = MassColors.ErrorBg,
                    shape = RoundedCornerShape(10.dp),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(
                        text = serverStatus.error,
                        color = MassColors.Danger,
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace,
                        modifier = Modifier.padding(12.dp)
                    )
                }
                Spacer(modifier = Modifier.height(14.dp))
            } else {
                Text(
                    text = "请在设置中配置形态 A 本地服务端（MariaDB + TOS），或填写远端 MASS 后端地址。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MassColors.Muted
                )
                Spacer(modifier = Modifier.height(18.dp))
            }
            Button(
                onClick = onOpenSettings,
                shape = RoundedCornerShape(10.dp),
                colors = ButtonDefaults.buttonColors(
                    containerColor = MassColors.Accent,
                    contentColor = Color.White
                )
            ) {
                Text("前往配置")
            }
        }
    }
}

@Composable
private fun SessionListItemCard(
    session: SessionSummary,
    onClick: () -> Unit
) {
    Surface(
        color = MassColors.Surface,
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.65f)),
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick)
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp, vertical = 14.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(
                text = session.displayTitle,
                color = MassColors.Text,
                fontSize = 15.sp,
                fontWeight = FontWeight.Medium,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f)
            )
            Spacer(modifier = Modifier.width(12.dp))
            Text(
                text = formatTimestamp(session.createdAt),
                color = MassColors.Muted,
                fontSize = 12.sp,
                maxLines = 1
            )
        }
    }
}

internal fun formatTimestamp(iso: String): String {
    if (iso.length >= 16) {
        return iso.substring(5, 16).replace('T', ' ')
    }
    return iso
}
