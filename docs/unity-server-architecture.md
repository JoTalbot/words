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
