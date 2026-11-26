# MASS Android App (Form A + OpenAPI)

Runs the MASS Go server in-process inside the Android APK via JNI (`libmassbridge.so`, built with `go build -buildmode=c-shared` + Android NDK) on `http://127.0.0.1:15336`, with native Jetpack Compose screens backed by MASS OpenAPI Bearer endpoints:

- **Home — Session List** (`SessionListScreen.kt`): resolves the token's space via `GET /api/v1/openapi/context`, lists sessions via `GET /api/v1/spaces/{space_id}/sessions`, supports pagination, creating new sessions (`GET /agents` + `POST /sessions`), and opening a session on tap.
- **Session Chat** (`SessionChatScreen.kt`): loads session details (`GET /sessions/{id}`), paginated history (`GET /sessions/{id}/messages`), uploads attachments (`POST /sessions/{id}/attachments`), submits runs (`POST /sessions/{id}/runs`), cancels runs (`POST /runs/{run_id}/cancel`), and streams replies + tool calls via AG-UI SSE (`GET /runs/{run_id}/events`).

## Build

1. Build the Go JNI shared library (`arm64-v8a` by default):
   ```sh
   sh scripts/build-android.sh
   ```
   Optional flags:
   - `--all-abis`: build both `arm64-v8a` and `x86_64`.
   - `--apk`: also run `./gradlew assembleDebug`.

2. Build & install the APK:
   ```sh
   proj/android/gradlew -p proj/android assembleDebug
   adb install -r proj/android/app/build/outputs/apk/debug/app-debug.apk
   ```

## Configuration

- Default OpenAPI Bearer token (`MassConfig.DEFAULT_OPENAPI_TOKEN`), default Space ID (`4lca4gankkuxip2p`), and default Agent ID (`szqcoir3iaxi6ay4`) are pre-configured and editable in Settings.
- Leave **Remote Base URL** blank to connect to the embedded local Go server (`http://127.0.0.1:15336`, Form A using remote MariaDB + TOS), or specify a remote MASS server URL directly.
