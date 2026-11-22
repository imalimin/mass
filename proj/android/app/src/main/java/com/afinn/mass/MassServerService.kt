package com.afinn.mass

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.net.HttpURLConnection
import java.net.URL

class MassServerService : Service() {
    private val serviceScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var pollJob: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopForegroundCompat()
                serviceScope.launch {
                    pollJob?.cancel()
                    val stopped = MassBridge.stop()
                    _status.value = stopped
                    stopSelf()
                }
                return START_NOT_STICKY
            }

            ACTION_START, null -> {
                promoteToForeground("Starting local MASS server…")
                val config = MassConfigStore.load(applicationContext)
                startManagedServer(config)
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        pollJob?.cancel()
        serviceScope.cancel()
        super.onDestroy()
    }

    private fun startManagedServer(config: MassConfig) {
        pollJob?.cancel()
        pollJob = serviceScope.launch {
            val reqJson = config.toStartRequestJson(applicationContext)
            val initial = MassBridge.start(reqJson)
            _status.value = initial
            updateNotification(initial)

            while (isActive) {
                val current = MassBridge.status()
                val live = if (current.url.isNotBlank() && (current.isRunning || current.isStarting)) {
                    probeLive(current.url)
                } else {
                    false
                }
                val merged = if (live && current.isStarting) {
                    current.copy(state = MassStatus.STATE_RUNNING, httpLive = true)
                } else {
                    current.copy(httpLive = live)
                }
                if (merged != _status.value) {
                    _status.value = merged
                    updateNotification(merged)
                }
                if (merged.state == MassStatus.STATE_STOPPED || merged.state == MassStatus.STATE_ERROR) {
                    break
                }
                delay(if (merged.isRunning) 2500L else 300L)
            }
        }
    }

    private fun probeLive(baseUrl: String): Boolean {
        return runCatching {
            val conn = (URL("${baseUrl.trimEnd('/')}/health/live").openConnection() as HttpURLConnection).apply {
                connectTimeout = 800
                readTimeout = 800
                requestMethod = "GET"
            }
            try {
                conn.responseCode == HttpURLConnection.HTTP_OK
            } finally {
                conn.disconnect()
            }
        }.getOrDefault(false)
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "MASS Local Server",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Keeps the embedded MASS Go server running in the background"
            }
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(channel)
        }
    }

    private fun buildNotification(text: String): Notification {
        val openIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val pendingIntent = PendingIntent.getActivity(
            this,
            0,
            openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("MASS Local Server")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_stat_mass)
            .setOngoing(true)
            .setContentIntent(pendingIntent)
            .build()
    }

    private fun promoteToForeground(text: String) {
        val notification = buildNotification(text)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun updateNotification(status: MassStatus) {
        val text = when (status.state) {
            MassStatus.STATE_RUNNING -> {
                val label = if (status.mode == MassStatus.MODE_UI_ONLY) "UI Preview" else "Full (Form A)"
                "Running [$label] at ${status.url}"
            }
            MassStatus.STATE_STARTING -> "Starting MASS server…"
            MassStatus.STATE_ERROR -> "Error: ${status.error.take(80)}"
            else -> "Stopped"
        }
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIFICATION_ID, buildNotification(text))
    }

    private fun stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }

    companion object {
        private const val CHANNEL_ID = "mass_local_server"
        private const val NOTIFICATION_ID = 1001
        private const val ACTION_START = "com.afinn.mass.action.START"
        private const val ACTION_STOP = "com.afinn.mass.action.STOP"

        private val _status = MutableStateFlow(MassStatus())
        val status: StateFlow<MassStatus> = _status.asStateFlow()

        fun refreshStatus() {
            _status.value = MassBridge.status()
        }

        fun start(context: Context, config: MassConfig) {
            MassConfigStore.save(context, config)
            val intent = Intent(context, MassServerService::class.java).apply {
                action = ACTION_START
            }
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, MassServerService::class.java).apply {
                action = ACTION_STOP
            }
            context.startService(intent)
        }
    }
}
