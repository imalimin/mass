package com.afinn.mass

import android.Manifest
import android.content.pm.PackageManager
import android.graphics.Color as AndroidColor
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.ContextCompat
import com.afinn.mass.ui.theme.MassColors
import com.afinn.mass.ui.theme.MassTheme
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { /* optional for foreground notification visibility */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT)
        )
        requestNotificationPermissionIfNeeded()
        MassServerService.refreshStatus()

        val initialConfig = MassConfigStore.load(this)
        if (!initialConfig.useRemoteServer &&
            !MassServerService.status.value.isRunning &&
            !MassServerService.status.value.isStarting &&
            initialConfig.hasFullBackendCredentials()
        ) {
            MassServerService.start(this, initialConfig.copy(mode = MassStatus.MODE_FULL))
        }

        setContent {
            MassTheme {
                MassHomeApp(
                    initialConfig = initialConfig,
                    onExitApp = {
                        MassServerService.stop(this)
                        finish()
                    }
                )
            }
        }
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val granted = ContextCompat.checkSelfPermission(
                this,
                Manifest.permission.POST_NOTIFICATIONS
            ) == PackageManager.PERMISSION_GRANTED
            if (!granted) {
                notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun MassHomeApp(
    initialConfig: MassConfig,
    onExitApp: () -> Unit
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val status by MassServerService.status.collectAsState()
    val listUiState by SessionListCache.state.collectAsState()

    var config by remember { mutableStateOf(initialConfig) }
    var showSettings by remember { mutableStateOf(false) }

    BackHandler {
        if (showSettings) {
            showSettings = false
        } else {
            onExitApp()
        }
    }

    val baseUrl = config.effectiveApiBaseUrl(status)
    val backendReady = config.useRemoteServer || status.isRunning
    val effectiveSpaceId = config.spaceId.trim().ifBlank { MassConfig.DEFAULT_SPACE_ID }
    val effectiveAgentId = config.agentId.trim().ifBlank { MassConfig.DEFAULT_AGENT_ID }
    val listKey = "$baseUrl|${config.openapiToken}|$effectiveSpaceId|$effectiveAgentId"

    fun fetchSessions(isPullRefresh: Boolean) {
        if (!backendReady) return
        scope.launch {
            SessionListCache.update { cur ->
                cur.copy(
                    loading = !isPullRefresh && cur.sessions.isEmpty(),
                    refreshing = isPullRefresh,
                    errorMessage = ""
                )
            }
            try {
                val result = withContext(Dispatchers.IO) {
                    val agents = runCatching {
                        MassOpenApiClient.listAgents(baseUrl, config.openapiToken, effectiveSpaceId)
                    }.getOrDefault(emptyList())
                    val page = MassOpenApiClient.listSessions(
                        baseUrl = baseUrl,
                        token = config.openapiToken,
                        spaceId = effectiveSpaceId,
                        agentId = effectiveAgentId,
                        cursor = "",
                        limit = 50
                    )
                    agents to page
                }
                val (agents, page) = result
                val sorted = page.items.filterNot { it.hide }
                    .sortedWith(compareByDescending<SessionSummary> { it.createdAt }.thenByDescending { it.id })
                SessionListCache.update { cur ->
                    cur.copy(
                        loadedKey = listKey,
                        spaceId = effectiveSpaceId,
                        sessions = sorted,
                        agentMap = agents.associateBy { it.id },
                        nextCursor = page.nextCursor,
                        loading = false,
                        refreshing = false,
                        errorMessage = ""
                    )
                }
            } catch (t: Throwable) {
                SessionListCache.update { cur ->
                    cur.copy(
                        loadedKey = listKey,
                        loading = false,
                        refreshing = false,
                        errorMessage = t.message ?: "加载会话列表失败"
                    )
                }
            }
        }
    }

    fun loadMoreSessions() {
        val cursor = listUiState.nextCursor
        if (!backendReady || cursor.isBlank() || listUiState.loadingMore) return
        scope.launch {
            SessionListCache.update { it.copy(loadingMore = true) }
            try {
                val page = withContext(Dispatchers.IO) {
                    MassOpenApiClient.listSessions(
                        baseUrl = baseUrl,
                        token = config.openapiToken,
                        spaceId = effectiveSpaceId,
                        agentId = effectiveAgentId,
                        cursor = cursor,
                        limit = 50
                    )
                }
                SessionListCache.update { cur ->
                    val existingIds = cur.sessions.map { it.id }.toSet()
                    val appended = page.items.filterNot { it.hide || it.id in existingIds }
                    val merged = (cur.sessions + appended)
                        .sortedWith(compareByDescending<SessionSummary> { it.createdAt }.thenByDescending { it.id })
                    cur.copy(
                        sessions = merged,
                        nextCursor = page.nextCursor,
                        loadingMore = false
                    )
                }
            } catch (t: Throwable) {
                SessionListCache.update { cur ->
                    cur.copy(
                        loadingMore = false,
                        errorMessage = t.message ?: "加载更多失败"
                    )
                }
            }
        }
    }

    // Load only on first readiness or when connection/space/agent settings change.
    // Returning from SessionChatActivity does NOT trigger an automatic refresh.
    LaunchedEffect(backendReady, listKey) {
        if (backendReady && listUiState.loadedKey != listKey) {
            fetchSessions(isPullRefresh = false)
        }
    }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        containerColor = MassColors.Bg,
        topBar = {
            Column {
                TopAppBar(
                    colors = TopAppBarDefaults.topAppBarColors(
                        containerColor = MassColors.Bg,
                        titleContentColor = MassColors.Text,
                        navigationIconContentColor = MassColors.Text,
                        actionIconContentColor = MassColors.Muted
                    ),
                    navigationIcon = {
                        if (showSettings) {
                            IconButton(onClick = { showSettings = false }) {
                                Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回")
                            }
                        }
                    },
                    title = {
                        if (showSettings) {
                            Text(
                                text = "设置",
                                fontWeight = FontWeight.SemiBold,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        } else {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                StatusDot(
                                    state = if (config.useRemoteServer) MassStatus.STATE_RUNNING else status.state
                                )
                                Spacer(modifier = Modifier.width(10.dp))
                                Text(
                                    text = "MASS",
                                    fontSize = 19.sp,
                                    fontWeight = FontWeight.Bold,
                                    letterSpacing = 0.6.sp,
                                    color = MassColors.Text,
                                    maxLines = 1
                                )
                            }
                        }
                    },
                    actions = {
                        IconButton(
                            onClick = {
                                DiagnosticsActivity.start(
                                    context = context,
                                    spaceId = effectiveSpaceId,
                                    sessionId = "",
                                    agentId = effectiveAgentId,
                                    runId = "",
                                    baseUrl = baseUrl
                                )
                            }
                        ) {
                            Icon(
                                painter = painterResource(id = R.drawable.ic_mass_activity),
                                contentDescription = "诊断信息",
                                tint = MassColors.Muted
                            )
                        }
                        IconButton(onClick = { showSettings = !showSettings }) {
                            Icon(Icons.Default.Settings, contentDescription = "设置")
                        }
                    }
                )
                HorizontalDivider(color = MassColors.Border.copy(alpha = 0.6f), thickness = 1.dp)
            }
        }
    ) { innerPadding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(innerPadding)
        ) {
            if (showSettings) {
                ConfigScreen(
                    config = config,
                    status = status,
                    onConfigChange = { config = it },
                    onSaveAndApply = { updated ->
                        val normalized = updated.copy(mode = MassStatus.MODE_FULL)
                        config = normalized
                        MassConfigStore.save(context, normalized)
                        SessionListCache.update { it.copy(loadedKey = "") }
                        if (normalized.useRemoteServer) {
                            if (status.isRunning || status.isStarting) {
                                MassServerService.stop(context)
                            }
                        } else if (normalized.hasFullBackendCredentials()) {
                            MassServerService.start(context, normalized)
                        }
                        showSettings = false
                    },
                    onStopLocalServer = {
                        MassServerService.stop(context)
                    }
                )
            } else {
                SessionListScreen(
                    config = config,
                    serverStatus = status,
                    uiState = listUiState,
                    onRefresh = { fetchSessions(isPullRefresh = true) },
                    onLoadMore = { loadMoreSessions() },
                    onSelectSession = { spaceId, session, agentName ->
                        SessionChatActivity.start(
                            context = context,
                            spaceId = spaceId,
                            agentId = session.definitionId.ifBlank { effectiveAgentId },
                            agentName = agentName,
                            session = session
                        )
                    },
                    onNewSession = { spaceId, agentId, agentName ->
                        SessionChatActivity.start(
                            context = context,
                            spaceId = spaceId,
                            agentId = agentId,
                            agentName = agentName,
                            session = null
                        )
                    },
                    onOpenSettings = { showSettings = true }
                )
            }
        }
    }
}

@Composable
private fun StatusDot(state: String) {
    val color = when (state) {
        MassStatus.STATE_RUNNING -> MassColors.Success
        MassStatus.STATE_STARTING, MassStatus.STATE_STOPPING -> MassColors.Warning
        MassStatus.STATE_ERROR -> MassColors.Danger
        else -> MassColors.Subtle
    }
    Box(
        modifier = Modifier
            .size(8.dp)
            .clip(CircleShape)
            .background(color)
    )
}

@Composable
private fun massTextFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = MassColors.Text,
    unfocusedTextColor = MassColors.Text,
    focusedContainerColor = MassColors.SurfaceRaised,
    unfocusedContainerColor = MassColors.SurfaceRaised,
    focusedBorderColor = MassColors.Focus,
    unfocusedBorderColor = MassColors.Border,
    focusedLabelColor = MassColors.AccentText,
    unfocusedLabelColor = MassColors.Muted,
    cursorColor = MassColors.Focus,
    focusedPlaceholderColor = MassColors.Subtle,
    unfocusedPlaceholderColor = MassColors.Subtle
)

@Composable
private fun ConfigScreen(
    config: MassConfig,
    status: MassStatus,
    onConfigChange: (MassConfig) -> Unit,
    onSaveAndApply: (MassConfig) -> Unit,
    onStopLocalServer: () -> Unit
) {
    var showImportDialog by remember { mutableStateOf(false) }
    var showSecretKey by remember { mutableStateOf(false) }
    var envInput by remember { mutableStateOf("") }
    var portText by remember(config.port) { mutableStateOf(config.port.toString()) }
    val fieldColors = massTextFieldColors()
    val fieldShape = RoundedCornerShape(10.dp)

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(MassColors.Bg)
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(
                text = "OpenAPI 访问配置",
                style = MaterialTheme.typography.titleMedium,
                color = MassColors.Text
            )
            TextButton(onClick = { showImportDialog = true }) {
                Text("粘贴 .env", color = MassColors.AccentText)
            }
        }

        OutlinedTextField(
            value = config.openapiToken,
            onValueChange = { onConfigChange(config.copy(openapiToken = it)) },
            label = { Text("OpenAPI Bearer Token") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.spaceId,
            onValueChange = { onConfigChange(config.copy(spaceId = it)) },
            label = { Text("Space ID（默认 ${MassConfig.DEFAULT_SPACE_ID}）") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.agentId,
            onValueChange = { onConfigChange(config.copy(agentId = it)) },
            label = { Text("Agent ID（默认 ${MassConfig.DEFAULT_AGENT_ID}）") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.remoteBaseUrl,
            onValueChange = { onConfigChange(config.copy(remoteBaseUrl = it)) },
            label = { Text("远端后端 URL（留空则使用本地内置 Go 服务端）") },
            placeholder = { Text("例如 http://192.168.1.10:8080") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )

        HorizontalDivider(color = MassColors.Border)
        Text(
            text = "本地内置 Go 服务端（形态 A）",
            style = MaterialTheme.typography.titleMedium,
            color = MassColors.Text
        )
        Text(
            text = "当上方远端 URL 留空时，应用在本地 127.0.0.1 启动 MASS Go 服务端连接以下 MariaDB 与 TOS。",
            style = MaterialTheme.typography.bodySmall,
            color = MassColors.Muted
        )

        OutlinedTextField(
            value = portText,
            onValueChange = { raw ->
                portText = raw.filter { it.isDigit() }.take(5)
                portText.toIntOrNull()?.takeIf { it in 1024..65535 }?.let { valid ->
                    onConfigChange(config.copy(port = valid))
                }
            },
            label = { Text("Local HTTP Port (127.0.0.1)") },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.mariadbDsn,
            onValueChange = { onConfigChange(config.copy(mariadbDsn = it)) },
            label = { Text("MASS_MARIADB_DSN") },
            placeholder = { Text("user:pass@tcp(192.168.1.10:3306)/mass") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.tosEndpoint,
            onValueChange = { onConfigChange(config.copy(tosEndpoint = it)) },
            label = { Text("MASS_TOS_ENDPOINT") },
            placeholder = { Text("https://tos-cn-guangzhou.volces.com") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedTextField(
                value = config.tosRegion,
                onValueChange = { onConfigChange(config.copy(tosRegion = it)) },
                label = { Text("MASS_TOS_REGION") },
                singleLine = true,
                shape = fieldShape,
                colors = fieldColors,
                modifier = Modifier.weight(1f)
            )
            OutlinedTextField(
                value = config.tosPrefix,
                onValueChange = { onConfigChange(config.copy(tosPrefix = it)) },
                label = { Text("TOS Prefix") },
                singleLine = true,
                shape = fieldShape,
                colors = fieldColors,
                modifier = Modifier.weight(1f)
            )
        }
        OutlinedTextField(
            value = config.tosBucket,
            onValueChange = { onConfigChange(config.copy(tosBucket = it)) },
            label = { Text("MASS_TOS_BUCKET") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.tosAccessKey,
            onValueChange = { onConfigChange(config.copy(tosAccessKey = it)) },
            label = { Text("MASS_TOS_ACCESS_KEY") },
            singleLine = true,
            shape = fieldShape,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth()
        )
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            OutlinedTextField(
                value = config.tosSecretKey,
                onValueChange = { onConfigChange(config.copy(tosSecretKey = it)) },
                label = { Text("MASS_TOS_SECRET_KEY (${config.tosSecretKey.trim().length} 字符)") },
                visualTransformation = if (showSecretKey) VisualTransformation.None else PasswordVisualTransformation(),
                singleLine = true,
                shape = fieldShape,
                colors = fieldColors,
                modifier = Modifier.weight(1f)
            )
            OutlinedButton(
                onClick = { showSecretKey = !showSecretKey },
                shape = fieldShape,
                border = BorderStroke(1.dp, MassColors.Border),
                colors = ButtonDefaults.outlinedButtonColors(contentColor = MassColors.Text)
            ) {
                Text(if (showSecretKey) "隐藏" else "显示")
            }
        }
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            OutlinedTextField(
                value = config.masterKey,
                onValueChange = { onConfigChange(config.copy(masterKey = it)) },
                label = { Text("MASS_MASTER_KEY (32B Base64)") },
                singleLine = true,
                shape = fieldShape,
                colors = fieldColors,
                modifier = Modifier.weight(1f)
            )
            OutlinedButton(
                onClick = {
                    onConfigChange(config.copy(masterKey = MassConfig.generateMasterKey()))
                },
                shape = fieldShape,
                border = BorderStroke(1.dp, MassColors.Border),
                colors = ButtonDefaults.outlinedButtonColors(contentColor = MassColors.Text)
            ) {
                Text("生成")
            }
        }

        Spacer(modifier = Modifier.height(6.dp))
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Button(
                onClick = { onSaveAndApply(config) },
                shape = fieldShape,
                colors = ButtonDefaults.buttonColors(
                    containerColor = MassColors.Accent,
                    contentColor = Color.White
                ),
                modifier = Modifier.weight(1f)
            ) {
                Text(
                    if (config.useRemoteServer) {
                        "保存并连接远端"
                    } else if (status.isRunning) {
                        "保存并重启本地服务"
                    } else {
                        "保存并启动"
                    }
                )
            }
            if (status.isRunning || status.isStarting) {
                OutlinedButton(
                    onClick = onStopLocalServer,
                    shape = fieldShape,
                    border = BorderStroke(1.dp, MassColors.Border),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = MassColors.Danger)
                ) {
                    Text("停止")
                }
            }
        }
    }

    if (showImportDialog) {
        AlertDialog(
            onDismissRequest = { showImportDialog = false },
            containerColor = MassColors.SurfaceRaised,
            titleContentColor = MassColors.Text,
            textContentColor = MassColors.Text,
            title = { Text("粘贴 .env 导入") },
            text = {
                OutlinedTextField(
                    value = envInput,
                    onValueChange = { envInput = it },
                    placeholder = {
                        Text(
                            "MASS_OPENAPI_TOKEN=...\nMASS_SPACE_ID=...\nMASS_AGENT_ID=...\nMASS_API_URL=...\nMASS_MARIADB_DSN=...\nMASS_TOS_ENDPOINT=...\nMASS_TOS_REGION=...\nMASS_TOS_BUCKET=...\nMASS_TOS_ACCESS_KEY=...\nMASS_TOS_SECRET_KEY=...\nMASS_MASTER_KEY=..."
                        )
                    },
                    minLines = 6,
                    maxLines = 12,
                    shape = fieldShape,
                    colors = fieldColors,
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                Button(
                    onClick = {
                        onConfigChange(MassConfig.applyEnvText(config, envInput))
                        showImportDialog = false
                    },
                    shape = fieldShape,
                    colors = ButtonDefaults.buttonColors(
                        containerColor = MassColors.Accent,
                        contentColor = Color.White
                    )
                ) {
                    Text("应用")
                }
            },
            dismissButton = {
                TextButton(onClick = { showImportDialog = false }) {
                    Text("取消", color = MassColors.Muted)
                }
            }
        )
    }
}
