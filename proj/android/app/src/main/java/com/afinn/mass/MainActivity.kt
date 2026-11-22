package com.afinn.mass

import android.Manifest
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import com.afinn.mass.ui.theme.MassTheme

private data class ActiveSessionNav(
    val spaceId: String,
    val session: SessionSummary,
    val agentName: String
)

class MainActivity : ComponentActivity() {
    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { /* optional for foreground notification visibility */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
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
                MassApp(initialConfig = initialConfig)
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
private fun MassApp(initialConfig: MassConfig) {
    val context = LocalContext.current
    val status by MassServerService.status.collectAsState()
    var config by remember { mutableStateOf(initialConfig) }
    var activeSession by remember { mutableStateOf<ActiveSessionNav?>(null) }
    var activeTitle by remember { mutableStateOf("") }
    var resolvedSpaceId by remember { mutableStateOf(initialConfig.spaceId) }
    var showSettings by remember { mutableStateOf(false) }
    var showLogs by remember { mutableStateOf(false) }
    var listRefreshTrigger by remember { mutableIntStateOf(0) }
    var chatRefreshTrigger by remember { mutableIntStateOf(0) }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        topBar = {
            TopAppBar(
                navigationIcon = {
                    when {
                        showSettings -> {
                            IconButton(onClick = { showSettings = false }) {
                                Icon(Icons.Default.ArrowBack, contentDescription = "Back")
                            }
                        }
                        activeSession != null -> {
                            IconButton(onClick = {
                                activeSession = null
                                listRefreshTrigger++
                            }) {
                                Icon(Icons.Default.ArrowBack, contentDescription = "Back to sessions")
                            }
                        }
                    }
                },
                title = {
                    when {
                        showSettings -> {
                            Text("设置", maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }

                        activeSession != null -> {
                            val nav = activeSession!!
                            Column {
                                Text(
                                    text = activeTitle.ifBlank { nav.session.displayTitle },
                                    style = MaterialTheme.typography.titleMedium,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis
                                )
                                Text(
                                    text = nav.agentName.ifBlank { nav.session.definitionId },
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis
                                )
                            }
                        }

                        else -> {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                StatusDot(
                                    state = if (config.useRemoteServer) MassStatus.STATE_RUNNING else status.state
                                )
                                Spacer(modifier = Modifier.width(8.dp))
                                Column {
                                    Text(
                                        text = "会话列表",
                                        style = MaterialTheme.typography.titleMedium,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis
                                    )
                                    val endpointLabel = config.effectiveApiBaseUrl(status)
                                    val spaceLabel = resolvedSpaceId.ifBlank { "OpenAPI" }
                                    Text(
                                        text = "$spaceLabel · $endpointLabel",
                                        style = MaterialTheme.typography.labelSmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis
                                    )
                                }
                            }
                        }
                    }
                },
                actions = {
                    if (!showSettings) {
                        IconButton(onClick = {
                            if (activeSession != null) {
                                chatRefreshTrigger++
                            } else {
                                listRefreshTrigger++
                            }
                        }) {
                            Icon(Icons.Default.Refresh, contentDescription = "Refresh")
                        }
                    }
                    IconButton(onClick = { showLogs = true }) {
                        Icon(Icons.Default.Info, contentDescription = "Logs")
                    }
                    IconButton(onClick = { showSettings = !showSettings }) {
                        Icon(Icons.Default.Settings, contentDescription = "Settings")
                    }
                }
            )
        }
    ) { innerPadding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(innerPadding)
        ) {
            when {
                showSettings -> {
                    ConfigScreen(
                        config = config,
                        status = status,
                        onConfigChange = { config = it },
                        onSaveAndApply = { updated ->
                            val normalized = updated.copy(mode = MassStatus.MODE_FULL)
                            config = normalized
                            MassConfigStore.save(context, normalized)
                            if (normalized.useRemoteServer) {
                                if (status.isRunning || status.isStarting) {
                                    MassServerService.stop(context)
                                }
                            } else if (normalized.hasFullBackendCredentials()) {
                                MassServerService.start(context, normalized)
                            }
                            showSettings = false
                            activeSession = null
                            listRefreshTrigger++
                        },
                        onStopLocalServer = {
                            MassServerService.stop(context)
                        }
                    )
                }

                activeSession != null -> {
                    val nav = activeSession!!
                    SessionChatScreen(
                        baseUrl = config.effectiveApiBaseUrl(status),
                        token = config.openapiToken,
                        spaceId = nav.spaceId,
                        initialSession = nav.session,
                        agentDisplayName = nav.agentName,
                        refreshTrigger = chatRefreshTrigger,
                        onTitleUpdated = { activeTitle = it },
                        onBack = {
                            activeSession = null
                            listRefreshTrigger++
                        }
                    )
                }

                else -> {
                    SessionListScreen(
                        config = config,
                        serverStatus = status,
                        refreshTrigger = listRefreshTrigger,
                        onResolvedSpace = { resolvedSpaceId = it },
                        onSelectSession = { spaceId, session, agentName ->
                            activeTitle = session.displayTitle
                            activeSession = ActiveSessionNav(spaceId, session, agentName)
                        },
                        onOpenSettings = { showSettings = true }
                    )
                }
            }
        }
    }

    if (showLogs) {
        LogDialog(
            config = config,
            status = status,
            onDismiss = { showLogs = false },
            onCopy = {
                val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                val text = buildString {
                    appendLine("api_base=${config.effectiveApiBaseUrl(status)}")
                    appendLine("state=${status.state} mode=${status.mode} url=${status.url}")
                    if (status.error.isNotBlank()) appendLine("error=${status.error}")
                    status.logs.forEach { appendLine(it) }
                }
                cm.setPrimaryClip(ClipData.newPlainText("MASS Logs", text))
            }
        )
    }
}

@Composable
private fun StatusDot(state: String) {
    val color = when (state) {
        MassStatus.STATE_RUNNING -> Color(0xFF2E7D32)
        MassStatus.STATE_STARTING, MassStatus.STATE_STOPPING -> Color(0xFFF9A825)
        MassStatus.STATE_ERROR -> Color(0xFFC62828)
        else -> Color(0xFF757575)
    }
    Box(
        modifier = Modifier
            .size(10.dp)
            .clip(CircleShape)
            .background(color)
    )
}

@Composable
private fun ConfigScreen(
    config: MassConfig,
    status: MassStatus,
    onConfigChange: (MassConfig) -> Unit,
    onSaveAndApply: (MassConfig) -> Unit,
    onStopLocalServer: () -> Unit
) {
    var showImportDialog by remember { mutableStateOf(false) }
    var envInput by remember { mutableStateOf("") }
    var portText by remember(config.port) { mutableStateOf(config.port.toString()) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text("OpenAPI 访问配置", style = MaterialTheme.typography.titleMedium)
            TextButton(onClick = { showImportDialog = true }) {
                Text("粘贴 .env")
            }
        }

        OutlinedTextField(
            value = config.openapiToken,
            onValueChange = { onConfigChange(config.copy(openapiToken = it)) },
            label = { Text("OpenAPI Bearer Token") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.spaceId,
            onValueChange = { onConfigChange(config.copy(spaceId = it)) },
            label = { Text("Space ID（默认 ${MassConfig.DEFAULT_SPACE_ID}）") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.agentId,
            onValueChange = { onConfigChange(config.copy(agentId = it)) },
            label = { Text("Agent ID（默认 ${MassConfig.DEFAULT_AGENT_ID}）") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.remoteBaseUrl,
            onValueChange = { onConfigChange(config.copy(remoteBaseUrl = it)) },
            label = { Text("远端后端 URL（留空则使用本地内置 Go 服务端）") },
            placeholder = { Text("例如 http://192.168.1.10:8080") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )

        HorizontalDivider()
        Text("本地内置 Go 服务端（形态 A）", style = MaterialTheme.typography.titleMedium)
        Text(
            text = "当上方远端 URL 留空时，应用在本地 127.0.0.1 启动 MASS Go 服务端连接以下 MariaDB 与 TOS。",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant
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
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.mariadbDsn,
            onValueChange = { onConfigChange(config.copy(mariadbDsn = it)) },
            label = { Text("MASS_MARIADB_DSN") },
            placeholder = { Text("user:pass@tcp(192.168.1.10:3306)/mass") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.tosEndpoint,
            onValueChange = { onConfigChange(config.copy(tosEndpoint = it)) },
            label = { Text("MASS_TOS_ENDPOINT") },
            placeholder = { Text("https://tos-cn-guangzhou.volces.com") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedTextField(
                value = config.tosRegion,
                onValueChange = { onConfigChange(config.copy(tosRegion = it)) },
                label = { Text("MASS_TOS_REGION") },
                singleLine = true,
                modifier = Modifier.weight(1f)
            )
            OutlinedTextField(
                value = config.tosPrefix,
                onValueChange = { onConfigChange(config.copy(tosPrefix = it)) },
                label = { Text("TOS Prefix") },
                singleLine = true,
                modifier = Modifier.weight(1f)
            )
        }
        OutlinedTextField(
            value = config.tosBucket,
            onValueChange = { onConfigChange(config.copy(tosBucket = it)) },
            label = { Text("MASS_TOS_BUCKET") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.tosAccessKey,
            onValueChange = { onConfigChange(config.copy(tosAccessKey = it)) },
            label = { Text("MASS_TOS_ACCESS_KEY") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        OutlinedTextField(
            value = config.tosSecretKey,
            onValueChange = { onConfigChange(config.copy(tosSecretKey = it)) },
            label = { Text("MASS_TOS_SECRET_KEY") },
            visualTransformation = PasswordVisualTransformation(),
            singleLine = true,
            modifier = Modifier.fillMaxWidth()
        )
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            OutlinedTextField(
                value = config.masterKey,
                onValueChange = { onConfigChange(config.copy(masterKey = it)) },
                label = { Text("MASS_MASTER_KEY (32B Base64)") },
                singleLine = true,
                modifier = Modifier.weight(1f)
            )
            OutlinedButton(onClick = {
                onConfigChange(config.copy(masterKey = MassConfig.generateMasterKey()))
            }) {
                Text("生成")
            }
        }

        Spacer(modifier = Modifier.height(4.dp))
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Button(
                onClick = { onSaveAndApply(config) },
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
                OutlinedButton(onClick = onStopLocalServer) {
                    Text("停止")
                }
            }
        }
    }

    if (showImportDialog) {
        AlertDialog(
            onDismissRequest = { showImportDialog = false },
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
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                Button(onClick = {
                    onConfigChange(MassConfig.applyEnvText(config, envInput))
                    showImportDialog = false
                }) {
                    Text("应用")
                }
            },
            dismissButton = {
                TextButton(onClick = { showImportDialog = false }) {
                    Text("取消")
                }
            }
        )
    }
}

@Composable
private fun LogDialog(
    config: MassConfig,
    status: MassStatus,
    onDismiss: () -> Unit,
    onCopy: () -> Unit
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("运行诊断 (${status.state})") },
        text = {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(340.dp)
                    .verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                Text(
                    text = "OpenAPI Base: ${config.effectiveApiBaseUrl(status)}\n" +
                        "Local Server: ${status.state} (${status.version.ifBlank { "unknown" }})\n" +
                        "HTTP Live: ${status.httpLive}",
                    style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace
                )
                if (status.error.isNotBlank()) {
                    Text(
                        text = "Error: ${status.error}",
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace
                    )
                }
                HorizontalDivider(modifier = Modifier.padding(vertical = 4.dp))
                if (status.logs.isEmpty()) {
                    Text("暂无本地服务端日志。", style = MaterialTheme.typography.bodySmall)
                } else {
                    status.logs.forEach { line ->
                        Text(
                            text = line,
                            style = MaterialTheme.typography.bodySmall,
                            fontFamily = FontFamily.Monospace
                        )
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = onDismiss) { Text("关闭") }
        },
        dismissButton = {
            TextButton(onClick = onCopy) { Text("复制") }
        }
    )
}
