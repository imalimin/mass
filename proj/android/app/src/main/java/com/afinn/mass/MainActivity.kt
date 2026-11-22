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
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
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
        if (!MassServerService.status.value.isRunning && !MassServerService.status.value.isStarting) {
            if (initialConfig.mode == MassStatus.MODE_UI_ONLY || initialConfig.hasFullBackendCredentials()) {
                MassServerService.start(this, initialConfig)
            }
        }

        setContent {
            MassTheme {
                MassDemoApp(initialConfig = initialConfig)
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
private fun MassDemoApp(initialConfig: MassConfig) {
    val context = LocalContext.current
    val status by MassServerService.status.collectAsState()
    var config by remember { mutableStateOf(initialConfig) }
    var showSettings by remember {
        mutableStateOf(
            !status.isRunning &&
                !status.isStarting &&
                initialConfig.mode == MassStatus.MODE_FULL &&
                !initialConfig.hasFullBackendCredentials()
        )
    }
    var showLogs by remember { mutableStateOf(false) }
    var reloadToken by remember { mutableIntStateOf(0) }
    var webError by remember { mutableStateOf("") }

    LaunchedEffect(status.state, status.url) {
        if (status.isRunning) {
            webError = ""
        }
    }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        topBar = {
            TopAppBar(
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        StatusDot(status.state)
                        Spacer(modifier = Modifier.width(8.dp))
                        Column {
                            Text(
                                text = "MASS Local",
                                style = MaterialTheme.typography.titleMedium,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                            val modeTag = if (status.mode == MassStatus.MODE_UI_ONLY) "UI Preview" else "Form A"
                            val subtitle = if (status.url.isNotBlank()) "$modeTag · ${status.url}" else "$modeTag · ${status.state}"
                            Text(
                                text = subtitle,
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                    }
                },
                actions = {
                    if (status.isRunning && !showSettings) {
                        IconButton(onClick = { reloadToken++ }) {
                            Icon(Icons.Default.Refresh, contentDescription = "Reload")
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
            if (showSettings) {
                ConfigScreen(
                    config = config,
                    status = status,
                    onConfigChange = { config = it },
                    onStart = { updated ->
                        config = updated
                        webError = ""
                        showSettings = false
                        MassServerService.start(context, updated)
                    },
                    onStop = {
                        MassServerService.stop(context)
                    },
                    onQuickPreview = {
                        val preview = config.copy(mode = MassStatus.MODE_UI_ONLY)
                        config = preview
                        webError = ""
                        showSettings = false
                        MassServerService.start(context, preview)
                    }
                )
            } else {
                when {
                    status.isRunning && status.url.isNotBlank() -> {
                        Column(modifier = Modifier.fillMaxSize()) {
                            if (webError.isNotBlank()) {
                                Surface(color = MaterialTheme.colorScheme.errorContainer) {
                                    Row(
                                        modifier = Modifier
                                            .fillMaxWidth()
                                            .padding(horizontal = 12.dp, vertical = 8.dp),
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.SpaceBetween
                                    ) {
                                        Text(
                                            text = "WebView: $webError",
                                            color = MaterialTheme.colorScheme.onErrorContainer,
                                            style = MaterialTheme.typography.bodySmall,
                                            modifier = Modifier.weight(1f)
                                        )
                                        TextButton(onClick = {
                                            webError = ""
                                            reloadToken++
                                        }) {
                                            Text("Retry")
                                        }
                                    }
                                }
                            }
                            MassWebView(
                                url = status.url,
                                reloadToken = reloadToken,
                                modifier = Modifier.weight(1f),
                                onPageError = { webError = it }
                            )
                        }
                    }

                    status.isStarting -> {
                        StartingView(status = status, onOpenSettings = { showSettings = true })
                    }

                    else -> {
                        StoppedOrErrorView(
                            status = status,
                            onStart = { MassServerService.start(context, config) },
                            onOpenSettings = { showSettings = true },
                            onQuickPreview = {
                                val preview = config.copy(mode = MassStatus.MODE_UI_ONLY)
                                config = preview
                                MassServerService.start(context, preview)
                            }
                        )
                    }
                }
            }
        }
    }

    if (showLogs) {
        LogDialog(
            status = status,
            onDismiss = { showLogs = false },
            onCopy = {
                val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                val text = buildString {
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
private fun StartingView(status: MassStatus, onOpenSettings: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        CircularProgressIndicator()
        Spacer(modifier = Modifier.height(16.dp))
        Text("Starting embedded MASS Go server…", style = MaterialTheme.typography.titleMedium)
        Spacer(modifier = Modifier.height(8.dp))
        Text(
            text = status.logs.lastOrNull() ?: "Connecting to dependencies and binding loopback port",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant
        )
        Spacer(modifier = Modifier.height(20.dp))
        OutlinedButton(onClick = onOpenSettings) {
            Text("Edit Configuration")
        }
    }
}

@Composable
private fun StoppedOrErrorView(
    status: MassStatus,
    onStart: () -> Unit,
    onOpenSettings: () -> Unit,
    onQuickPreview: () -> Unit
) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        Text(
            text = if (status.isError) "MASS Server Failed to Start" else "MASS Local Server Stopped",
            style = MaterialTheme.typography.titleLarge
        )
        Spacer(modifier = Modifier.height(12.dp))
        if (status.error.isNotBlank()) {
            Surface(
                color = MaterialTheme.colorScheme.errorContainer,
                shape = MaterialTheme.shapes.medium,
                modifier = Modifier.fillMaxWidth()
            ) {
                Text(
                    text = status.error,
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodyMedium,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier.padding(12.dp)
                )
            }
            Spacer(modifier = Modifier.height(16.dp))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Button(onClick = onStart) {
                Text("Start Server")
            }
            OutlinedButton(onClick = onOpenSettings) {
                Text("Configure")
            }
        }
        Spacer(modifier = Modifier.height(12.dp))
        TextButton(onClick = onQuickPreview) {
            Text("Launch Embedded UI Preview (No MariaDB/TOS)")
        }
    }
}

@Composable
private fun ConfigScreen(
    config: MassConfig,
    status: MassStatus,
    onConfigChange: (MassConfig) -> Unit,
    onStart: (MassConfig) -> Unit,
    onStop: () -> Unit,
    onQuickPreview: () -> Unit
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
        Text("Run Mode", style = MaterialTheme.typography.titleMedium)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            FilterChip(
                selected = config.mode == MassStatus.MODE_FULL,
                onClick = { onConfigChange(config.copy(mode = MassStatus.MODE_FULL)) },
                label = { Text("Form A: Full Server") }
            )
            FilterChip(
                selected = config.mode == MassStatus.MODE_UI_ONLY,
                onClick = { onConfigChange(config.copy(mode = MassStatus.MODE_UI_ONLY)) },
                label = { Text("UI Preview Only") }
            )
        }
        Text(
            text = if (config.mode == MassStatus.MODE_FULL) {
                "Runs full bootstrap.Serve in-process on 127.0.0.1, connecting to your remote MariaDB and TOS."
            } else {
                "Runs embedded Go HTTP server + MASS frontend assets on 127.0.0.1 without MariaDB/TOS."
            },
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

        if (config.mode == MassStatus.MODE_FULL) {
            HorizontalDivider()
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text("Remote Backend (Form A)", style = MaterialTheme.typography.titleMedium)
                TextButton(onClick = { showImportDialog = true }) {
                    Text("Paste .env")
                }
            }

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
                placeholder = { Text("https://tos-cn-beijing.volces.com") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth()
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = config.tosRegion,
                    onValueChange = { onConfigChange(config.copy(tosRegion = it)) },
                    label = { Text("MASS_TOS_REGION") },
                    placeholder = { Text("cn-beijing") },
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
                    Text("Gen")
                }
            }
        }

        Spacer(modifier = Modifier.height(4.dp))
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            Button(
                onClick = { onStart(config) },
                modifier = Modifier.weight(1f)
            ) {
                Text(if (status.isRunning) "Save & Restart" else "Save & Start")
            }
            if (status.isRunning || status.isStarting) {
                OutlinedButton(onClick = onStop) {
                    Text("Stop")
                }
            }
        }

        if (config.mode == MassStatus.MODE_FULL && !config.hasFullBackendCredentials()) {
            TextButton(
                onClick = onQuickPreview,
                modifier = Modifier.align(Alignment.CenterHorizontally)
            ) {
                Text("Skip backend setup & preview embedded UI now")
            }
        }
    }

    if (showImportDialog) {
        AlertDialog(
            onDismissRequest = { showImportDialog = false },
            title = { Text("Import from .env") },
            text = {
                OutlinedTextField(
                    value = envInput,
                    onValueChange = { envInput = it },
                    placeholder = {
                        Text(
                            "MASS_MARIADB_DSN=...\nMASS_TOS_ENDPOINT=...\nMASS_TOS_REGION=...\nMASS_TOS_BUCKET=...\nMASS_TOS_ACCESS_KEY=...\nMASS_TOS_SECRET_KEY=...\nMASS_MASTER_KEY=..."
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
                    Text("Apply")
                }
            },
            dismissButton = {
                TextButton(onClick = { showImportDialog = false }) {
                    Text("Cancel")
                }
            }
        )
    }
}

@Composable
private fun LogDialog(
    status: MassStatus,
    onDismiss: () -> Unit,
    onCopy: () -> Unit
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Server Diagnostics (${status.state})") },
        text = {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(340.dp)
                    .verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                Text(
                    text = "Version: ${status.version.ifBlank { "unknown" }}\nMode: ${status.mode}\nURL: ${status.url.ifBlank { "-" }}\nHTTP Live: ${status.httpLive}",
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
                    Text("No logs yet.", style = MaterialTheme.typography.bodySmall)
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
            TextButton(onClick = onDismiss) { Text("Close") }
        },
        dismissButton = {
            TextButton(onClick = onCopy) { Text("Copy") }
        }
    )
}
