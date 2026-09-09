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
- Раннер: GitHub-hosted `ubuntu-latest`, x86_64.
- Action: `game-ci/unity-builder@v4`.
- Project path: `client/unity`.
- Build method: `Words.BuildCommand.BuildAndroid`.
- Workflow: `.github/workflows/unity-android.yml`.
- Запуск: только `workflow_dispatch`, чтобы случайный push не потреблял Unity license seat и минуты CI.

Unity 6000.0.59f2 выбрана как стабильный Unity 6 patch release с Linux и Android build support. Версия закреплена одновременно в `ProjectSettings/ProjectVersion.txt` и workflow.

## Лицензирование

Проверка `gh secret list --repo JoTalbot/words` на дату аудита не нашла ни одного Actions Secret. Поэтому APK ещё не генерировался: workflow намеренно завершится понятной ошибкой на preflight активации, пока секреты не добавлены.

Для Unity Personal по документации GameCI нужны:

| Secret | Назначение | Где получить | Проверка |
|---|---|---|---|
| `UNITY_LICENSE` | содержимое активированного `.ulf` license file | Unity Hub: ручная активация лицензии на совместимой машине | Secret существует; значение никогда не печатается |
| `UNITY_EMAIL` | email Unity account | Unity account | Secret существует; значение не печатается |
| `UNITY_PASSWORD` | пароль Unity account | Unity account | Secret существует; значение не печатается |

Для Unity Pro/Plus вместо `UNITY_LICENSE` используется:

| Secret | Назначение | Где получить | Проверка |
|---|---|---|---|
| `UNITY_SERIAL` | Unity serial | Unity Subscriptions | Secret существует; значение не печатается |

Не добавляйте одновременно случайные или придуманные значения. Workflow принимает либо `UNITY_LICENSE`, либо `UNITY_SERIAL`, и в обоих вариантах требует email/password.

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
