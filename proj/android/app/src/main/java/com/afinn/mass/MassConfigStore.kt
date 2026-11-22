package com.afinn.mass

import android.content.Context
import android.util.Base64
import org.json.JSONObject
import java.io.File
import java.security.SecureRandom

data class MassConfig(
    val mode: String = MassStatus.MODE_FULL,
    val port: Int = 15336,
    val mariadbDsn: String = "",
    val tosEndpoint: String = "",
    val tosRegion: String = "",
    val tosBucket: String = "",
    val tosPrefix: String = "mass/",
    val tosAccessKey: String = "",
    val tosSecretKey: String = "",
    val masterKey: String = ""
) {
    val listenAddress: String get() = "127.0.0.1:$port"
    val publicUrl: String get() = "http://127.0.0.1:$port"

    fun hasFullBackendCredentials(): Boolean {
        return mariadbDsn.isNotBlank() &&
            tosEndpoint.isNotBlank() &&
            tosRegion.isNotBlank() &&
            tosBucket.isNotBlank() &&
            tosAccessKey.isNotBlank() &&
            tosSecretKey.isNotBlank() &&
            masterKey.isNotBlank()
    }

    fun toStartRequestJson(context: Context): String {
        val workspaceDir = File(context.filesDir, "mass-workspaces")
        val tmpDir = File(context.cacheDir, "mass-tmp")
        val env = JSONObject().apply {
            put("MASS_MARIADB_DSN", mariadbDsn.trim())
            put("MASS_TOS_ENDPOINT", tosEndpoint.trim())
            put("MASS_TOS_REGION", tosRegion.trim())
            put("MASS_TOS_BUCKET", tosBucket.trim())
            put("MASS_TOS_ACCESS_KEY", tosAccessKey.trim())
            put("MASS_TOS_SECRET_KEY", tosSecretKey.trim())
            put("MASS_MASTER_KEY", masterKey.trim())
        }
        return JSONObject().apply {
            put("mode", mode)
            put("listen", listenAddress)
            put("public_url", publicUrl)
            put("workspace_temp_root", workspaceDir.absolutePath)
            put("tmp_dir", tmpDir.absolutePath)
            put("tos_prefix", tosPrefix.trim().ifEmpty { "mass/" })
            put("env", env)
        }.toString()
    }

    companion object {
        fun generateMasterKey(): String {
            val bytes = ByteArray(32)
            SecureRandom().nextBytes(bytes)
            return Base64.encodeToString(bytes, Base64.NO_WRAP)
        }

        fun applyEnvText(base: MassConfig, text: String): MassConfig {
            val parsed = mutableMapOf<String, String>()
            text.lineSequence().forEach { rawLine ->
                val line = rawLine.trim().removePrefix("export ").trim()
                if (line.isEmpty() || line.startsWith("#")) return@forEach
                val idx = line.indexOf('=')
                if (idx <= 0) return@forEach
                val key = line.substring(0, idx).trim()
                var value = line.substring(idx + 1).trim()
                if ((value.startsWith("\"") && value.endsWith("\"") && value.length >= 2) ||
                    (value.startsWith("'") && value.endsWith("'") && value.length >= 2)
                ) {
                    value = value.substring(1, value.length - 1)
                }
                parsed[key] = value
            }
            return base.copy(
                mariadbDsn = parsed["MASS_MARIADB_DSN"] ?: base.mariadbDsn,
                tosEndpoint = parsed["MASS_TOS_ENDPOINT"] ?: base.tosEndpoint,
                tosRegion = parsed["MASS_TOS_REGION"] ?: base.tosRegion,
                tosBucket = parsed["MASS_TOS_BUCKET"] ?: base.tosBucket,
                tosPrefix = parsed["MASS_TOS_PREFIX"] ?: base.tosPrefix,
                tosAccessKey = parsed["MASS_TOS_ACCESS_KEY"] ?: base.tosAccessKey,
                tosSecretKey = parsed["MASS_TOS_SECRET_KEY"] ?: base.tosSecretKey,
                masterKey = parsed["MASS_MASTER_KEY"] ?: base.masterKey,
                port = parsed["MASS_ANDROID_PORT"]?.toIntOrNull()?.takeIf { it in 1024..65535 } ?: base.port
            )
        }
    }
}

object MassConfigStore {
    private const val PREF_NAME = "mass_android_config"
    private const val KEY_MODE = "mode"
    private const val KEY_PORT = "port"
    private const val KEY_DSN = "mariadb_dsn"
    private const val KEY_TOS_ENDPOINT = "tos_endpoint"
    private const val KEY_TOS_REGION = "tos_region"
    private const val KEY_TOS_BUCKET = "tos_bucket"
    private const val KEY_TOS_PREFIX = "tos_prefix"
    private const val KEY_TOS_ACCESS_KEY = "tos_access_key"
    private const val KEY_TOS_SECRET_KEY = "tos_secret_key"
    private const val KEY_MASTER_KEY = "master_key"

    fun load(context: Context): MassConfig {
        val prefs = context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        val hasSaved = prefs.contains(KEY_MODE)
        var config = MassConfig(
            mode = prefs.getString(KEY_MODE, MassStatus.MODE_FULL) ?: MassStatus.MODE_FULL,
            port = prefs.getInt(KEY_PORT, 15336),
            mariadbDsn = prefs.getString(KEY_DSN, "") ?: "",
            tosEndpoint = prefs.getString(KEY_TOS_ENDPOINT, "") ?: "",
            tosRegion = prefs.getString(KEY_TOS_REGION, "") ?: "",
            tosBucket = prefs.getString(KEY_TOS_BUCKET, "") ?: "",
            tosPrefix = prefs.getString(KEY_TOS_PREFIX, "mass/") ?: "mass/",
            tosAccessKey = prefs.getString(KEY_TOS_ACCESS_KEY, "") ?: "",
            tosSecretKey = prefs.getString(KEY_TOS_SECRET_KEY, "") ?: "",
            masterKey = prefs.getString(KEY_MASTER_KEY, "") ?: ""
        )
        if (!hasSaved) {
            val bundledEnv = runCatching {
                context.assets.open("mass_defaults.env").bufferedReader().use { it.readText() }
            }.getOrNull()
            if (!bundledEnv.isNullOrBlank()) {
                config = MassConfig.applyEnvText(config, bundledEnv)
            }
        }
        return config
    }

    fun save(context: Context, config: MassConfig) {
        context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE).edit()
            .putString(KEY_MODE, config.mode)
            .putInt(KEY_PORT, config.port)
            .putString(KEY_DSN, config.mariadbDsn.trim())
            .putString(KEY_TOS_ENDPOINT, config.tosEndpoint.trim())
            .putString(KEY_TOS_REGION, config.tosRegion.trim())
            .putString(KEY_TOS_BUCKET, config.tosBucket.trim())
            .putString(KEY_TOS_PREFIX, config.tosPrefix.trim().ifEmpty { "mass/" })
            .putString(KEY_TOS_ACCESS_KEY, config.tosAccessKey.trim())
            .putString(KEY_TOS_SECRET_KEY, config.tosSecretKey.trim())
            .putString(KEY_MASTER_KEY, config.masterKey.trim())
            .apply()
    }
}
