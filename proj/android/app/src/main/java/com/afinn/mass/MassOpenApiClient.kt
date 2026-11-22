package com.afinn.mass

import android.util.Base64
import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.UUID

data class OpenApiContext(
    val spaceId: String,
    val userId: String,
    val username: String
)

data class AgentSummary(
    val id: String,
    val name: String,
    val archived: Boolean,
    val defaultVersionId: String,
    val latestVersion: String
)

data class SessionSummary(
    val id: String,
    val spaceId: String,
    val title: String,
    val definitionId: String,
    val agentName: String,
    val versionLabel: String,
    val draftRevision: Long,
    val workspaceError: String,
    val lastSequence: String,
    val createdAt: String,
    val hide: Boolean
) {
    val displayTitle: String get() = title.ifBlank { id }
}

data class SessionPage(
    val items: List<SessionSummary>,
    val nextCursor: String
)

data class RunSummary(
    val id: String,
    val status: String,
    val error: String,
    val cancelRequested: Boolean,
    val createdAt: String
) {
    val isActive: Boolean get() = status == "queued" || status == "running"
}

data class AttachmentInfo(
    val id: String,
    val name: String,
    val mediaType: String,
    val sizeBytes: Long
)

data class ToolCallState(
    val id: String,
    val name: String,
    val status: String,
    val arguments: String = "",
    val result: String = ""
)

data class ChatMessage(
    val id: String,
    val runId: String,
    val role: String,
    val sequence: String,
    val content: String,
    val complete: Boolean = true,
    val truncated: Boolean = false,
    val attachments: List<AttachmentInfo> = emptyList(),
    val tools: List<ToolCallState> = emptyList()
)

data class MessagesPage(
    val items: List<ChatMessage>,
    val nextAfter: String,
    val hasMore: Boolean
)

data class AguiStreamEvent(
    val sequence: String,
    val type: String,
    val payload: JSONObject
)

class OpenApiException(
    val statusCode: Int,
    val errorCode: String,
    override val message: String
) : Exception(message)

object MassOpenApiClient {
    private const val CONNECT_TIMEOUT_MS = 10_000
    private const val READ_TIMEOUT_MS = 20_000

    fun getContext(baseUrl: String, token: String): OpenApiContext {
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = "/api/v1/openapi/context"
        )
        val user = json.optJSONObject("user")
        return OpenApiContext(
            spaceId = json.optString("space_id"),
            userId = user?.optString("id").orEmpty(),
            username = user?.optString("username").orEmpty()
        )
    }

    fun listAgents(baseUrl: String, token: String, spaceId: String): List<AgentSummary> {
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = "/api/v1/spaces/${enc(spaceId)}/agents?limit=100"
        )
        val arr = json.optJSONArray("items") ?: JSONArray()
        return buildList(arr.length()) {
            for (i in 0 until arr.length()) {
                val obj = arr.optJSONObject(i) ?: continue
                add(
                    AgentSummary(
                        id = obj.optString("id"),
                        name = obj.optString("name").ifBlank { obj.optString("id") },
                        archived = obj.optBoolean("archived", false),
                        defaultVersionId = obj.optString("default_version_id"),
                        latestVersion = obj.optString("latest_version")
                    )
                )
            }
        }
    }

    fun listSessions(
        baseUrl: String,
        token: String,
        spaceId: String,
        agentId: String = "",
        cursor: String = "",
        limit: Int = 50
    ): SessionPage {
        val query = buildString {
            append("?limit=").append(limit)
            if (cursor.isNotBlank()) {
                append("&cursor=").append(enc(cursor))
            }
        }
        val path = if (agentId.isNotBlank()) {
            "/api/v1/spaces/${enc(spaceId)}/agents/${enc(agentId)}/sessions$query"
        } else {
            "/api/v1/spaces/${enc(spaceId)}/sessions$query"
        }
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = path
        )
        val arr = json.optJSONArray("items") ?: JSONArray()
        val items = buildList(arr.length()) {
            for (i in 0 until arr.length()) {
                val obj = arr.optJSONObject(i) ?: continue
                add(parseSession(obj))
            }
        }
        return SessionPage(
            items = items,
            nextCursor = json.optString("next_cursor")
        )
    }

    fun getSession(baseUrl: String, token: String, spaceId: String, sessionId: String): SessionSummary {
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions/${enc(sessionId)}"
        )
        return parseSession(json)
    }

    fun createSession(
        baseUrl: String,
        token: String,
        spaceId: String,
        agentId: String,
        title: String = ""
    ): SessionSummary {
        val body = JSONObject().apply {
            put("idempotency_key", "android-sess-${UUID.randomUUID()}")
            put("agent_id", agentId)
            if (title.isNotBlank()) {
                put("title", title.trim())
            }
        }
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "POST",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions",
            body = body
        )
        return parseSession(json)
    }

    fun listRuns(
        baseUrl: String,
        token: String,
        spaceId: String,
        sessionId: String,
        limit: Int = 5
    ): List<RunSummary> {
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions/${enc(sessionId)}/runs?limit=$limit"
        )
        val arr = json.optJSONArray("items") ?: JSONArray()
        return buildList(arr.length()) {
            for (i in 0 until arr.length()) {
                val obj = arr.optJSONObject(i) ?: continue
                add(parseRun(obj))
            }
        }
    }

    fun listMessages(
        baseUrl: String,
        token: String,
        spaceId: String,
        sessionId: String,
        beforeSequence: String = "0",
        limit: Int = 50
    ): MessagesPage {
        val query = "?before_sequence=${enc(beforeSequence)}&limit=$limit"
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "GET",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions/${enc(sessionId)}/messages$query"
        )
        val arr = json.optJSONArray("items") ?: JSONArray()
        val items = buildList(arr.length()) {
            for (i in 0 until arr.length()) {
                val obj = arr.optJSONObject(i) ?: continue
                add(parseMessage(obj))
            }
        }
        return MessagesPage(
            items = items,
            nextAfter = json.optString("next_after"),
            hasMore = json.optBoolean("has_more", false)
        )
    }

    fun uploadAttachment(
        baseUrl: String,
        token: String,
        spaceId: String,
        sessionId: String,
        name: String,
        mediaType: String,
        bytes: ByteArray
    ): AttachmentInfo {
        val body = JSONObject().apply {
            put("name", name)
            put("media_type", mediaType.ifBlank { "application/octet-stream" })
            put("content_base64", Base64.encodeToString(bytes, Base64.NO_WRAP))
        }
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "POST",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions/${enc(sessionId)}/attachments",
            body = body
        )
        return AttachmentInfo(
            id = json.optString("id"),
            name = json.optString("name", name),
            mediaType = json.optString("media_type", mediaType),
            sizeBytes = json.optLong("size_bytes", bytes.size.toLong())
        )
    }

    fun submitRun(
        baseUrl: String,
        token: String,
        spaceId: String,
        sessionId: String,
        text: String,
        attachmentIds: List<String> = emptyList()
    ): RunSummary {
        val body = JSONObject().apply {
            put("idempotency_key", "android-run-${UUID.randomUUID()}")
            put("text", text)
            if (attachmentIds.isNotEmpty()) {
                put("attachment_ids", JSONArray(attachmentIds))
            }
        }
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "POST",
            path = "/api/v1/spaces/${enc(spaceId)}/sessions/${enc(sessionId)}/runs",
            body = body
        )
        return parseRun(json)
    }

    fun cancelRun(baseUrl: String, token: String, spaceId: String, runId: String): RunSummary {
        val json = requestJson(
            baseUrl = baseUrl,
            token = token,
            method = "POST",
            path = "/api/v1/spaces/${enc(spaceId)}/runs/${enc(runId)}/cancel",
            body = JSONObject()
        )
        return parseRun(json)
    }

    fun streamRunEvents(
        baseUrl: String,
        token: String,
        spaceId: String,
        runId: String,
        after: String = "0",
        isCancelled: () -> Boolean,
        onEvent: (AguiStreamEvent) -> Unit
    ) {
        val url = URL("${baseUrl.trimEnd('/')}/api/v1/spaces/${enc(spaceId)}/runs/${enc(runId)}/events?after=${enc(after)}")
        val conn = (url.openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = CONNECT_TIMEOUT_MS
            readTimeout = 60_000
            setRequestProperty("Authorization", "Bearer ${token.trim()}")
            setRequestProperty("Accept", "text/event-stream")
            setRequestProperty("Cache-Control", "no-cache")
        }
        try {
            val code = conn.responseCode
            if (code !in 200..299) {
                throw parseError(code, readBody(conn.errorStream))
            }
            BufferedReader(InputStreamReader(conn.inputStream, StandardCharsets.UTF_8)).use { reader ->
                var eventId = ""
                val dataLines = mutableListOf<String>()
                while (!isCancelled()) {
                    val line = reader.readLine() ?: break
                    when {
                        line.isEmpty() -> {
                            if (dataLines.isNotEmpty()) {
                                val raw = dataLines.joinToString("\n")
                                dataLines.clear()
                                val payload = runCatching { JSONObject(raw) }.getOrNull()
                                if (payload != null) {
                                    onEvent(
                                        AguiStreamEvent(
                                            sequence = eventId,
                                            type = payload.optString("type"),
                                            payload = payload
                                        )
                                    )
                                }
                            }
                            eventId = ""
                        }
                        line.startsWith(":") -> {
                            // SSE comment / heartbeat
                        }
                        line.startsWith("id:") -> {
                            eventId = line.removePrefix("id:").trim()
                        }
                        line.startsWith("data:") -> {
                            dataLines.add(line.removePrefix("data:").trimStart())
                        }
                    }
                }
            }
        } finally {
            conn.disconnect()
        }
    }

    private fun requestJson(
        baseUrl: String,
        token: String,
        method: String,
        path: String,
        body: JSONObject? = null
    ): JSONObject {
        val cleanBase = baseUrl.trim().trimEnd('/')
        if (cleanBase.isEmpty()) {
            throw OpenApiException(0, "missing_base_url", "Backend base URL is empty")
        }
        val cleanToken = token.trim()
        if (cleanToken.isEmpty()) {
            throw OpenApiException(401, "missing_token", "OpenAPI Bearer token is empty")
        }
        val conn = (URL(cleanBase + path).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = CONNECT_TIMEOUT_MS
            readTimeout = READ_TIMEOUT_MS
            setRequestProperty("Authorization", "Bearer $cleanToken")
            setRequestProperty("Accept", "application/json")
            if (body != null) {
                doOutput = true
                setRequestProperty("Content-Type", "application/json; charset=utf-8")
            }
        }
        try {
            if (body != null) {
                val payload = body.toString().toByteArray(StandardCharsets.UTF_8)
                conn.outputStream.use { it.write(payload) }
            }
            val code = conn.responseCode
            val raw = readBody(if (code in 200..299) conn.inputStream else conn.errorStream)
            if (code !in 200..299) {
                throw parseError(code, raw)
            }
            if (raw.isBlank()) {
                return JSONObject()
            }
            return JSONObject(raw)
        } finally {
            conn.disconnect()
        }
    }

    private fun readBody(stream: java.io.InputStream?): String {
        if (stream == null) return ""
        return stream.bufferedReader(StandardCharsets.UTF_8).use { it.readText() }
    }

    private fun parseError(statusCode: Int, rawBody: String): OpenApiException {
        val obj = runCatching { JSONObject(rawBody) }.getOrNull()
        val code = obj?.optString("code").orEmpty().ifBlank { "http_$statusCode" }
        val message = obj?.optString("message").orEmpty().ifBlank {
            rawBody.take(200).ifBlank { "HTTP $statusCode" }
        }
        return OpenApiException(statusCode, code, "$message ($code / HTTP $statusCode)")
    }

    private fun parseSession(obj: JSONObject): SessionSummary {
        return SessionSummary(
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
    }

    private fun parseRun(obj: JSONObject): RunSummary {
        return RunSummary(
            id = obj.optString("id"),
            status = obj.optString("status"),
            error = obj.optString("error"),
            cancelRequested = obj.optBoolean("cancel_requested", false),
            createdAt = obj.optString("created_at")
        )
    }

    private fun parseMessage(obj: JSONObject): ChatMessage {
        val attachmentsArr = obj.optJSONArray("attachments")
        val attachments = buildList {
            if (attachmentsArr != null) {
                for (i in 0 until attachmentsArr.length()) {
                    val a = attachmentsArr.optJSONObject(i) ?: continue
                    add(
                        AttachmentInfo(
                            id = a.optString("id"),
                            name = a.optString("name"),
                            mediaType = a.optString("media_type"),
                            sizeBytes = a.optLong("size_bytes", 0L)
                        )
                    )
                }
            }
        }
        val toolsArr = obj.optJSONArray("tools")
        val tools = buildList {
            if (toolsArr != null) {
                for (i in 0 until toolsArr.length()) {
                    val t = toolsArr.optJSONObject(i) ?: continue
                    add(
                        ToolCallState(
                            id = t.optString("id"),
                            name = t.optString("name").ifBlank { "tool" },
                            status = t.optString("status"),
                            arguments = t.optString("arguments"),
                            result = t.optString("result")
                        )
                    )
                }
            }
        }
        return ChatMessage(
            id = obj.optString("id"),
            runId = obj.optString("run_id"),
            role = obj.optString("role"),
            sequence = obj.optString("sequence", "0"),
            content = obj.optString("content"),
            complete = obj.optBoolean("complete", true),
            truncated = obj.optBoolean("truncated", false),
            attachments = attachments,
            tools = tools
        )
    }

    private fun enc(value: String): String = URLEncoder.encode(value, "UTF-8")
}
