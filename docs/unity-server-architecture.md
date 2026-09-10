# Архитектура Unity/Android и ARM-сервера

## Фактическая конфигурация

```text
Android phone
     ↓ source / artifact / manual install
GitHub repository JoTalbot/words
     ↓ workflow_dispatch
GitHub Actions ubuntu-latest (x86_64)
     ↓ game-ci/unity-builder@v4
Unity 6000.0.59f2 + Android Build Support
     ↓ Words.BuildCommand.BuildAndroid
APK artifact
     ↓ optional hosted emulator smoke test / physical phone
Android test device

OCI ARM64 server
     ├─ Go backend and tests
     ├─ agents and orchestration
     ├─ existing Docker services
     ├─ Git/GitHub coordination
     └─ storage for project metadata and external artifacts
```

## Почему Unity не размещён на ARM64 server

Аудит подтвердил `aarch64`, Ubuntu 24.04, 4 vCPU, 23.4 GiB RAM и отсутствие Unity Editor. Docker на этом сервере также работает как `linux/arm64`; контейнер не меняет архитектуру CPU. Установка x86_64 Unity через QEMU при текущей загрузке RAM и без официально подтверждённой стабильности сделала бы production build непредсказуемым.

ARM-сервер не изменяет существующие контейнеры, volumes, firewall или SSH configuration. Новая роль сервера — backend/agents/orchestration и координация pipeline.

## Повторяемость

- Unity version закреплена в `ProjectSettings/ProjectVersion.txt` и workflow.
- UPM manifest и lock file находятся в Git.
- Build entry point находится в `Assets/Editor/BuildCommand.cs`.
- Build parameters передаются явно: `buildType` и `buildPath`.
- APK не коммитится: он создаётся как Actions artifact.
- Secrets передаются только через GitHub Actions Secrets.
- Production release не публикуется без отдельного smoke/release gate.

## Ограничения и следующие шаги

1. Добавить Unity activation secret подходящего типа.
2. Запустить debug workflow с `run_smoke_tests=true`.
3. Проверить APK на hosted emulator и физическом Android phone.
4. Только после этого добавить production signing и release workflow.
5. Реализовать Unity gameplay/network slice поверх серверного authoritative protocol.

Если GitHub-hosted runner или Unity license недоступны, альтернативой является отдельный x86_64 build runner. ARM64 server остаётся backend host и не становится Unity Editor host через эмуляцию.


## M1 client UX bootstrap (2026-09-10)

`client/unity/Assets/Scripts/WordArenaBootstrap.cs` creates the initial runtime
UI from code at scene load. This avoids hand-authoring scene objects on the
ARM64 server where Unity Editor is unavailable, while still letting GitHub's
x86_64 Unity CI compile/build the APK.

The bootstrap uses IMGUI (already present via `com.unity.modules.imgui`) rather
than uGUI/EventSystem dependencies. It is intentionally non-authoritative: it demonstrates shared board
presentation, local touch selection, 3-second lock visualization,
Cross-Steal-ready ownership colors, combo display and a Sudden Death banner.
It must be replaced/extended by the server protocol client; the server remains
the only source of word validity, score, ownership and match completion.

## M1 server-bound protocol slice (2026-09-10)

The runtime bootstrap now includes a package-light authoritative path for
Android CI and early device testing:

1. `POST /v1/matches` is called with UnityWebRequest to create an `en`/`ru`/`uk`
   match and receive the two seat tokens.
2. The selected seat connects to `GET /v1/match/ws?match_id=...&token=...` via
   `System.Net.WebSockets.ClientWebSocket`.
3. `client/unity/Assets/Scripts/WordArenaProtocol.cs` encodes
   `SubmitWordIntent` as binary protobuf `ClientEnvelope` and decodes the
   current `ServerEnvelope` subset (`MatchStateSnapshot` and
   `WordValidatedEvent`). Unknown protobuf fields are skipped, preserving
   forward compatibility with the v1 schema.
4. `WordArenaBootstrap` renders canonical cells, owners, lock countdowns,
   player scores, combo multipliers, server tick and state version from server
   snapshots/events. Local demo scoring remains available only when not in
   server mode.

This slice intentionally avoids adding a C# protobuf runtime package until the
client protocol surface grows enough to justify code generation. The Go server
regression `TestUnityClientSubmitEnvelopeCompatibility` pins that the server
accepts the Unity encoder's unpacked repeated `letter_indices` representation.
Unity CI remains the compile/build gate because local Arena tooling has no Unity
Editor or C# compiler.

## M1 session lifecycle controls (2026-09-10)

The same bootstrap exposes operational lifecycle controls needed for safer live
session testing:

- `Ready check` calls `/readyz` and displays readiness/storage/active match
  status before match creation.
- `Rotate token` calls `POST /v1/matches/{id}/token/rotate` for the active seat,
  updates the in-memory credential and reconnects with the fresh token. Raw seat
  tokens are not displayed in IMGUI status messages.
- `Fetch result` calls `GET /v1/matches/{id}/result` and displays the final
  authoritative score/winner once the server has persisted the completed match.
- A terminal `over=true` WebSocket snapshot triggers one automatic result fetch.

These controls exercise the server's M1 production-shaped endpoints from the
Android client while preserving the rule that credentials stay scoped and the
server remains authoritative.

## M1 prediction/reconciliation shell (2026-09-10)

Server mode now adds a bounded presentation-only prediction layer:

- submitting selected cells creates a visible `PENDING #client_sequence` overlay;
- the client correlates `WordValidatedEvent.client_sequence` to the pending
  overlay and marks it accepted or rolled back;
- canonical `MatchStateSnapshot` messages rebuild cell ownership/locks/scores
  from server state and remove resolved/stale pending overlays;
- the overlay never changes authoritative score, cell owner, lock status or
  match completion locally.

This satisfies the first Unity-side reconciliation skeleton without committing
to final mobile animations. Rich swipe trails and smoother rollback visuals are
left for a later polish task after live-device latency evidence.
