package com.afinn.mass

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.graphics.Color as AndroidColor
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.afinn.mass.ui.theme.MassColors
import com.afinn.mass.ui.theme.MassTheme

class DiagnosticsActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT)
        )

        val spaceId = intent.getStringExtra(EXTRA_SPACE_ID).orEmpty()
        val sessionId = intent.getStringExtra(EXTRA_SESSION_ID).orEmpty()
        val agentId = intent.getStringExtra(EXTRA_AGENT_ID).orEmpty()
        val runId = intent.getStringExtra(EXTRA_RUN_ID).orEmpty()
        val baseUrl = intent.getStringExtra(EXTRA_BASE_URL).orEmpty()

        setContent {
            MassTheme {
                DiagnosticsScreen(
                    spaceId = spaceId,
                    sessionId = sessionId,
                    agentId = agentId,
                    runId = runId,
                    baseUrl = baseUrl,
                    onBack = { finish() }
                )
            }
        }
    }

    companion object {
        private const val EXTRA_SPACE_ID = "extra_space_id"
        private const val EXTRA_SESSION_ID = "extra_session_id"
        private const val EXTRA_AGENT_ID = "extra_agent_id"
        private const val EXTRA_RUN_ID = "extra_run_id"
        private const val EXTRA_BASE_URL = "extra_base_url"

        fun start(
            context: Context,
            spaceId: String,
            sessionId: String,
            agentId: String,
            runId: String,
            baseUrl: String
        ) {
            val intent = Intent(context, DiagnosticsActivity::class.java).apply {
                putExtra(EXTRA_SPACE_ID, spaceId)
                putExtra(EXTRA_SESSION_ID, sessionId)
                putExtra(EXTRA_AGENT_ID, agentId)
                putExtra(EXTRA_RUN_ID, runId)
                putExtra(EXTRA_BASE_URL, baseUrl)
            }
            context.startActivity(intent)
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun DiagnosticsScreen(
    spaceId: String,
    sessionId: String,
    agentId: String,
    runId: String,
    baseUrl: String,
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val status by MassServerService.status.collectAsState()
    var copiedKey by remember { mutableStateOf("") }

    fun copyText(key: String, label: String, value: String) {
        if (value.isBlank()) return
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText(label, value))
        copiedKey = key
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
                        navigationIconContentColor = MassColors.Text
                    ),
                    navigationIcon = {
                        IconButton(onClick = onBack) {
                            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回")
                        }
                    },
                    title = {
                        Text(
                            text = "诊断信息",
                            fontSize = 17.sp,
                            fontWeight = FontWeight.SemiBold,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis
                        )
                    }
                )
                HorizontalDivider(color = MassColors.Border.copy(alpha = 0.6f), thickness = 1.dp)
            }
        }
    ) { innerPadding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(MassColors.Bg)
                .padding(innerPadding)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            CopyableDiagnosticCard(
                label = "Space ID",
                value = spaceId,
                emptyText = "未配置",
                copied = copiedKey == "space_id",
                onCopy = { copyText("space_id", "Space ID", spaceId) }
            )

            CopyableDiagnosticCard(
                label = "会话 ID",
                value = sessionId,
                emptyText = "尚未创建",
                copied = copiedKey == "session_id",
                onCopy = { copyText("session_id", "Session ID", sessionId) }
            )

            if (agentId.isNotBlank()) {
                CopyableDiagnosticCard(
                    label = "Agent ID",
                    value = agentId,
                    emptyText = "-",
                    copied = copiedKey == "agent_id",
                    onCopy = { copyText("agent_id", "Agent ID", agentId) }
                )
            }

            if (runId.isNotBlank()) {
                CopyableDiagnosticCard(
                    label = "最新 Run ID",
                    value = runId,
                    emptyText = "-",
                    copied = copiedKey == "run_id",
                    onCopy = { copyText("run_id", "Run ID", runId) }
                )
            }

            Surface(
                color = MassColors.Surface,
                shape = RoundedCornerShape(12.dp),
                border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.7f)),
                modifier = Modifier.fillMaxWidth()
            ) {
                Column(
                    modifier = Modifier.padding(14.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Text(
                            text = "服务运行状态",
                            color = MassColors.Text,
                            fontSize = 14.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                        OutlinedButton(
                            shape = RoundedCornerShape(8.dp),
                            border = BorderStroke(1.dp, MassColors.Border),
                            contentPadding = PaddingValues(horizontal = 10.dp, vertical = 4.dp),
                            onClick = {
                                val summary = buildString {
                                    appendLine("space_id=$spaceId")
                                    if (sessionId.isNotBlank()) appendLine("session_id=$sessionId")
                                    if (agentId.isNotBlank()) appendLine("agent_id=$agentId")
                                    if (runId.isNotBlank()) appendLine("run_id=$runId")
                                    appendLine("api_base=$baseUrl")
                                    appendLine("local_server=${status.state} version=${status.version} http_live=${status.httpLive}")
                                    if (status.error.isNotBlank()) appendLine("error=${status.error}")
                                    status.logs.forEach { appendLine(it) }
                                }
                                copyText("all", "MASS Diagnostics", summary)
                            }
                        ) {
                            Text(
                                text = if (copiedKey == "all") "已复制全部" else "复制全部",
                                color = if (copiedKey == "all") MassColors.Success else MassColors.AccentText,
                                fontSize = 12.sp
                            )
                        }
                    }

                    Text(
                        text = "API Base: ${baseUrl.ifBlank { "-" }}\n" +
                            "Local Server: ${status.state} (${status.version.ifBlank { "unknown" }})\n" +
                            "HTTP Live: ${status.httpLive}",
                        color = MassColors.Muted,
                        fontSize = 12.sp,
                        lineHeight = 19.sp,
                        fontFamily = FontFamily.Monospace
                    )

                    if (status.error.isNotBlank()) {
                        Text(
                            text = "Error: ${status.error}",
                            color = MassColors.Danger,
                            fontSize = 12.sp,
                            fontFamily = FontFamily.Monospace
                        )
                    }

                    if (status.logs.isNotEmpty()) {
                        HorizontalDivider(color = MassColors.Border.copy(alpha = 0.6f))
                        Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                            status.logs.takeLast(40).forEach { line ->
                                Text(
                                    text = line,
                                    color = MassColors.Text,
                                    fontSize = 11.5.sp,
                                    lineHeight = 17.sp,
                                    fontFamily = FontFamily.Monospace
                                )
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun CopyableDiagnosticCard(
    label: String,
    value: String,
    emptyText: String,
    copied: Boolean,
    onCopy: () -> Unit
) {
    Surface(
        color = MassColors.Surface,
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, MassColors.Border.copy(alpha = 0.7f)),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Column(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                Text(
                    text = label,
                    color = MassColors.Muted,
                    fontSize = 12.sp
                )
                Text(
                    text = value.ifBlank { emptyText },
                    color = if (value.isBlank()) MassColors.Subtle else MassColors.Text,
                    fontSize = 14.sp,
                    fontFamily = if (value.isBlank()) FontFamily.Default else FontFamily.Monospace,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis
                )
            }
            Spacer(modifier = Modifier.width(10.dp))
            OutlinedButton(
                enabled = value.isNotBlank(),
                onClick = onCopy,
                shape = RoundedCornerShape(8.dp),
                border = BorderStroke(
                    1.dp,
                    if (copied) MassColors.Success.copy(alpha = 0.6f) else MassColors.Border
                ),
                contentPadding = PaddingValues(horizontal = 10.dp, vertical = 6.dp)
            ) {
                Icon(
                    painter = painterResource(id = R.drawable.ic_mass_copy),
                    contentDescription = "复制",
                    tint = if (copied) MassColors.Success else MassColors.AccentText,
                    modifier = Modifier.size(15.dp)
                )
                Spacer(modifier = Modifier.width(5.dp))
                Text(
                    text = if (copied) "已复制" else "复制",
                    color = if (copied) MassColors.Success else MassColors.AccentText,
                    fontSize = 12.sp
                )
            }
        }
    }
}
