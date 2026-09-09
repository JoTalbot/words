# Unity Android CI

Дата аудита и настройки: 2026-09-09.

## Решение по архитектуре

Текущий сервер `arm-server-01` фактически является Ubuntu 24.04 LTS на `aarch64`/ARM64, 4 vCPU Neoverse-N1 и примерно 23.4 GiB RAM. Unity Editor для этого pipeline на сервере не найден: нет `unity`, `unity-editor`, Unity Docker image или Unity container. Поэтому Unity не устанавливается нативно и не запускается через нестабильную x86_64-эмуляцию.

Используется безопасная схема:

```text
ARM64 OCI server
  ├─ backend / Go tests
  ├─ agents / orchestration
  ├─ Docker services
  └─ repository and artifact coordination

GitHub Actions ubuntu-latest (x86_64)
  └─ GameCI Unity Editor + Android Build Support
       └─ APK artifact
```

## Версия и runner

- Unity: `6000.0.59f2`.
- Раннер: GitHub-hosted `ubuntu-latest`, x86_64, сборка внутри контейнера
  `unityci/editor:ubuntu-6000.0.59f2-android-3.2.2` (прямой запуск
  `unity-editor -batchmode`, без GameCI activation).
- Project path: `client/unity`.
- Build method: `Words.BuildCommand.BuildAndroid`.
- Workflow: `.github/workflows/unity-android.yml`.
- Запуск: только `workflow_dispatch`, чтобы случайный push не потреблял Unity license seat и минуты CI.

Unity 6000.0.59f2 выбрана как стабильный Unity 6 patch release с Linux и Android build support. Версия закреплена одновременно в `ProjectSettings/ProjectVersion.txt` и workflow.

## Лицензирование

Сборка больше не зависит от механизма активации GameCI (который требует
`UNITY_LICENSE`/`UNITY_SERIAL` и не умеет активировать Personal по
email/password). Workflow `unity-android.yml` сам активирует лицензию внутри
`unityci/editor` контейнера через Unity Licensing Client:

```bash
/opt/unity/Editor/Data/Resources/Licensing/Client/Unity.Licensing.Client \
  --activate-ulf --include-personal --username "$UNITY_EMAIL" --password "$UNITY_PASSWORD"
# fallback: --activate-all --include-personal
```

Если после активации `.ulf`/`.xml` не найден, шаг завершается ошибкой с
понятным сообщением (а не молча — как раньше через `|| true`).

### Приоритет: UNITY_LICENSE (ручная активация, работает при MFA)

Workflow `unity-android.yml` теперь использует `UNITY_LICENSE` в первую
очередь: если секрет задан, его содержимое (`.ulf`) записывается в
`/root/.local/share/unity3d/Unity/Unity_lic.ulf` и сборка идёт без
обращения к email/password. Это надёжный путь для аккаунтов с включённым
MFA/SSO.

Как получить `UNITY_LICENSE` (ручная активация):

1. Запустить workflow «Unity Manual Activation Request» (Actions → Run
   workflow) — он сгенерирует `.alf` на x86_64 GitHub-раннере.
2. Скачать артефакт `unity-activation-request` (`.alf`).
3. Зайти на https://license.unity3d.com/manual под Unity-аккаунтом
   (MFA допускается), загрузить `.alf`, скачать полученный `.ulf`.
4. Добавить содержимое `.ulf` как Actions secret `UNITY_LICENSE`
   (repo → Settings → Secrets and variables → Actions → New repository
   secret).

Локальный `.ulf` из Unity Hub на Windows (`C:\ProgramData\Unity\Unity_lic.ulf`)
привязан к железу ПК и для CI не подходит — нужен `.ulf`, полученный через
`.alf` именно с CI-раннера.

Требуемые секреты:

| Secret | Назначение | Где получить |
|---|---|---|
| `UNITY_LICENSE` | содержимое `.ulf` (приоритет) | ручная активация: `.alf` → license.unity3d.com/manual → `.ulf` |
| `UNITY_EMAIL` | email Unity account | Unity account (нужен только если `UNITY_LICENSE` не задан) |
| `UNITY_PASSWORD` | пароль Unity account | Unity account (нужен только если `UNITY_LICENSE` не задан) |

Проверка на 2026-09-09: Unity Licensing API отвечает
`{"message": "Invalid Credential", "code": "143.002"}` на текущие значения
секретов. Возможные причины: неверный пароль, неподтверждённый email,
MFA/SSO на аккаунте (для автоматизации MFA должен быть выключен), либо
спецсимволы/кодировка пароля. Пока креды не станут валидными, сборка APK
невозможна — это единственный блокер Unity-пути.

## Сборка

Вручную в GitHub: Actions → Unity Android → Run workflow.

- `build_type=debug`: APK с development/debug flags и ARM64 + x86_64, что позволяет запускать его на hosted x86_64 emulator.
- `build_type=release`: ARM64 APK; требует Android signing secrets.
- `run_smoke_tests=true`: после сборки запускает APK на временном Android API 35 x86_64 emulator GitHub-hosted runner.

Эквивалентная команда Unity для локального совместимого x86_64 Editor:

```bash
Unity -batchmode -nographics -quit \
  -projectPath client/unity \
  -executeMethod Words.BuildCommand.BuildAndroid \
  -buildType debug \
  -buildPath Builds/Android/words-debug.apk
```

`BuildCommand` включает сцену, переключает Android target, создаёт каталог вывода, возвращает exit code `1` при исключении или неуспешном `BuildReport` и завершает batchmode с `0` только при успешной сборке.

## Артефакты и release

После успешной сборки сохраняется Actions artifact:

- `words-android-debug/*.apk`;
- `words-android-release/*.apk`.

Production GitHub Release автоматически не создаётся. Сначала требуется `run_smoke_tests=true`, ручная проверка APK и отдельное решение о публикации. После этого APK можно прикрепить к versioned GitHub Release через approved release procedure/`gh release create`; signing secrets и токены не коммитятся.

## Источники механизма GameCI

- GameCI Builder: https://game.ci/docs/github/builder/
- GameCI Activation: https://game.ci/docs/github/activation/
- Unity 6000.0.59f2 release: https://unity.com/releases/editor/whats-new/6000.0.59f2
