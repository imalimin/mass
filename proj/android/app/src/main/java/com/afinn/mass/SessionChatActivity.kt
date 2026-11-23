package com.afinn.mass

import android.content.Context
import android.content.Intent
import android.graphics.Color as AndroidColor
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.afinn.mass.ui.theme.MassColors
import com.afinn.mass.ui.theme.MassTheme
import org.json.JSONObject

class SessionChatActivity : ComponentActivity() {
    @OptIn(ExperimentalMaterial3Api::class)
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT)
        )

        val config = MassConfigStore.load(this)
        val spaceId = intent.getStringExtra(EXTRA_SPACE_ID)
            ?.takeIf { it.isNotBlank() }
            ?: config.spaceId.trim().ifBlank { MassConfig.DEFAULT_SPACE_ID }
        val agentId = intent.getStringExtra(EXTRA_AGENT_ID)
            ?.takeIf { it.isNotBlank() }
            ?: config.agentId.trim().ifBlank { MassConfig.DEFAULT_AGENT_ID }
        val initialSession = intent.getStringExtra(EXTRA_SESSION_JSON)
            ?.takeIf { it.isNotBlank() }
            ?.let { decodeSession(it) }
        val navKey = initialSession?.id?.let { "session-$it" } ?: "new-${System.currentTimeMillis()}"

        setContent {
            MassTheme {
                val status by MassServerService.status.collectAsState()
                val baseUrl = config.effectiveApiBaseUrl(status)
                var title by remember { mutableStateOf(initialSession?.displayTitle ?: "新会话") }
                var currentSessionId by remember { mutableStateOf(initialSession?.id.orEmpty()) }
                var latestRunId by remember { mutableStateOf("") }

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
                                    IconButton(onClick = { finish() }) {
                                        Icon(
                                            Icons.AutoMirrored.Filled.ArrowBack,
                                            contentDescription = "返回"
                                        )
                                    }
                                },
                                title = {
                                    Text(
                                        text = title,
                                        fontSize = 17.sp,
                                        fontWeight = FontWeight.SemiBold,
                                        color = MassColors.Text,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis
                                    )
                                },
                                actions = {
                                    IconButton(
                                        onClick = {
                                            DiagnosticsActivity.start(
                                                context = this@SessionChatActivity,
                                                spaceId = spaceId,
                                                sessionId = currentSessionId,
                                                agentId = agentId,
                                                runId = latestRunId,
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
                                }
                            )
                            HorizontalDivider(
                                color = MassColors.Border.copy(alpha = 0.6f),
                                thickness = 1.dp
                            )
                        }
                    }
                ) { innerPadding ->
                    Box(
                        modifier = Modifier
                            .fillMaxSize()
                            .padding(innerPadding)
                    ) {
                        SessionChatScreen(
                            navKey = navKey,
                            baseUrl = baseUrl,
                            token = config.openapiToken,
                            spaceId = spaceId,
                            agentId = agentId,
                            initialSession = initialSession,
                            refreshTrigger = 0,
                            onTitleUpdated = { title = it },
                            onSessionUpserted = { updated ->
                                currentSessionId = updated.id
                                SessionListCache.upsertSession(updated)
                            },
                            onLatestRunChanged = { latestRunId = it },
                            onBack = { finish() }
                        )
                    }
                }
            }
        }
    }

    companion object {
        private const val EXTRA_SPACE_ID = "extra_space_id"
        private const val EXTRA_AGENT_ID = "extra_agent_id"
        private const val EXTRA_AGENT_NAME = "extra_agent_name"
        private const val EXTRA_SESSION_JSON = "extra_session_json"

        fun start(
            context: Context,
            spaceId: String,
            agentId: String,
            agentName: String,
            session: SessionSummary?
        ) {
            val intent = Intent(context, SessionChatActivity::class.java).apply {
                putExtra(EXTRA_SPACE_ID, spaceId)
                putExtra(EXTRA_AGENT_ID, agentId)
                putExtra(EXTRA_AGENT_NAME, agentName)
                if (session != null) {
                    putExtra(EXTRA_SESSION_JSON, encodeSession(session))
                }
            }
            context.startActivity(intent)
        }

        private fun encodeSession(s: SessionSummary): String {
            return JSONObject().apply {
                put("id", s.id)
                put("space_id", s.spaceId)
                put("title", s.title)
                put("definition_id", s.definitionId)
                put("agent_name", s.agentName)
                put("version_label", s.versionLabel)
                put("draft_revision", s.draftRevision)
                put("workspace_error", s.workspaceError)
                put("last_sequence", s.lastSequence)
                put("created_at", s.createdAt)
                put("hide", s.hide)
            }.toString()
        }

        private fun decodeSession(raw: String): SessionSummary? {
            return runCatching {
                val obj = JSONObject(raw)
                SessionSummary(
                    id = obj.optString("id"),
                    spaceId = obj.optString("space_id"),
                    title = obj.optString("title"),
                    definitionId = obj.optString("definition_id"),
                    agentName = obj.optString("agent_name"),
                    versionLabel = obj.optString("version_label"),
                    draftRevision = obj.optLong("draft_revision", 0L),
                    workspaceError = obj.optString("workspace_error"),
                    lastSequence = obj.optString("last_sequence", "0"),
                    createdAt = obj.optString("created_at"),
                    hide = obj.optBoolean("hide", false)
                )
            }.getOrNull()
        }
    }
}
