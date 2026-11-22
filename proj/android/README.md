# MASS Android Demo (Form A)

Runs the MASS Go server in-process inside the Android APK via JNI (`libmassbridge.so`, built with `go build -buildmode=c-shared` + Android NDK) and renders the embedded MASS frontend in an Android `WebView` at `http://127.0.0.1:<port>`.

## Architecture

- **Go JNI Bridge** (`../../cmd/mass-android`):
  - `bridge.go`: manages the in-process server lifecycle (`Start` / `Stop` / `Status`), sets `TMPDIR` and `MASS_*` env vars, configures `runtime.workspace_temp_root` under Android app private storage, and captures `slog` logs.
  - `jni_android.go` & `jni_android.c`: exports `MassBridge.nativeStart`, `nativeStop`, `nativeStatus` and forwards logs to `logcat` (`MassGo`).
- **Android App** (`com.afinn.mass`):
  - `MassServerService`: `ForegroundService` (`dataSync`) keeping the Go server alive and probing `GET /health/live`.
  - `MassWebView`: WebView with JS, DOM Storage, Cookie persistence (`mass_session`), file chooser support, and SPA back navigation.
  - `MassConfigStore`: persists Form A credentials (`MASS_MARIADB_DSN`, `MASS_TOS_*`, `MASS_MASTER_KEY`), supports pasting `.env` snippets, and optionally reads bundled defaults from `app/src/main/assets/mass_defaults.env`.

## Build

1. Build the Go JNI shared library (`arm64-v8a` by default):
   ```sh
   sh scripts/build-android.sh
   ```
   Optional flags:
   - `--with-frontend`: rebuild `frontend` and re-embed into `internal/transport/http/ui/dist` first.
   - `--all-abis`: build both `arm64-v8a` and `x86_64` (useful for x86_64 emulators).
   - `--apk`: also run `./gradlew assembleDebug`.

2. Build & install the APK:
   ```sh
   proj/android/gradlew -p proj/android assembleDebug
   adb install -r proj/android/app/build/outputs/apk/debug/app-debug.apk
   ```

## Usage

- **Form A (Full Server)**: Open Settings (gear icon), paste your `.env` containing `MASS_MARIADB_DSN`, `MASS_TOS_ENDPOINT`, `MASS_TOS_REGION`, `MASS_TOS_BUCKET`, `MASS_TOS_ACCESS_KEY`, `MASS_TOS_SECRET_KEY`, and `MASS_MASTER_KEY` (must be reachable from the device/emulator network), then tap **Save & Start**.
- **UI Preview Only**: Switch to **UI Preview Only** (or tap *Skip backend setup & preview embedded UI now*) to run the embedded Go HTTP server + MASS React UI on `127.0.0.1` without configuring MariaDB/TOS.
- **Debugging**:
  - Go server logs: tap the Info icon in the top bar or run `adb logcat -s MassGo`.
  - WebView DevTools: open `chrome://inspect` on desktop Chrome while the device is connected via ADB.
