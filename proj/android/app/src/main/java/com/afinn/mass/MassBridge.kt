package com.afinn.mass

import org.json.JSONObject

data class MassStatus(
    val ok: Boolean = true,
    val state: String = STATE_STOPPED,
    val mode: String = MODE_FULL,
    val url: String = "",
    val error: String = "",
    val version: String = "",
    val logs: List<String> = emptyList(),
    val httpLive: Boolean = false
) {
    val isRunning: Boolean get() = state == STATE_RUNNING
    val isStarting: Boolean get() = state == STATE_STARTING
    val isError: Boolean get() = state == STATE_ERROR

    companion object {
        const val MODE_FULL = "full"
        const val MODE_UI_ONLY = "ui_only"

        const val STATE_STOPPED = "stopped"
        const val STATE_STARTING = "starting"
        const val STATE_RUNNING = "running"
        const val STATE_STOPPING = "stopping"
        const val STATE_ERROR = "error"

        fun fromJson(raw: String, httpLive: Boolean = false): MassStatus {
            return runCatching {
                val obj = JSONObject(raw)
                val logsArr = obj.optJSONArray("logs")
                val logs = buildList {
                    if (logsArr != null) {
                        for (i in 0 until logsArr.length()) {
                            add(logsArr.optString(i))
                        }
                    }
                }
                MassStatus(
                    ok = obj.optBoolean("ok", true),
                    state = obj.optString("state", STATE_STOPPED),
                    mode = obj.optString("mode", MODE_FULL),
                    url = obj.optString("url", ""),
                    error = obj.optString("error", ""),
                    version = obj.optString("version", ""),
                    logs = logs,
                    httpLive = httpLive
                )
            }.getOrElse { err ->
                MassStatus(
                    ok = false,
                    state = STATE_ERROR,
                    error = "Failed to parse Go bridge response: ${err.message}"
                )
            }
        }
    }
}

object MassBridge {
    private var loadError: Throwable? = null

    init {
        try {
            System.loadLibrary("massbridge")
        } catch (t: Throwable) {
            loadError = t
        }
    }

    private external fun nativeStart(reqJson: String): String
    private external fun nativeStop(): String
    private external fun nativeStatus(): String

    fun start(reqJson: String): MassStatus {
        loadError?.let {
            return MassStatus(
                ok = false,
                state = MassStatus.STATE_ERROR,
                error = "Native library libmassbridge.so not loaded: ${it.message}"
            )
        }
        return MassStatus.fromJson(nativeStart(reqJson))
    }

    fun stop(): MassStatus {
        loadError?.let {
            return MassStatus(
                ok = false,
                state = MassStatus.STATE_STOPPED,
                error = "Native library libmassbridge.so not loaded: ${it.message}"
            )
        }
        return MassStatus.fromJson(nativeStop())
    }

    fun status(): MassStatus {
        loadError?.let {
            return MassStatus(
                ok = false,
                state = MassStatus.STATE_ERROR,
                error = "Native library libmassbridge.so not loaded: ${it.message}"
            )
        }
        return MassStatus.fromJson(nativeStatus())
    }
}
