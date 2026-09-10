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

## Лицензирование (Unity 6, Personal)

Важно (проверено на 2026-09-09, согласуется с game-ci/cli): **Unity 6 убрал
офлайн/ручную активацию (`.ulf`) для Personal-мест** — `license.unity3d.com/manual`
теперь только для Enterprise/Industry. Поэтому:

- `.ulf`, полученный из Unity Hub на Windows, для Personal **не работает**
  в CI (ошибка «Found 0 entitlement groups / com.unity.editor.headless not
  found»);
- `-createManualActivationFile` (.alf) для Personal-аккаунта тоже не даёт
  результата.

Единственный путь для бесплатного Personal в Unity 6 — **активация по
email+password** через Unity Licensing Client (метод `personal` в game-ci):

```bash
/opt/unity/Editor/Data/Resources/Licensing/Client/Unity.Licensing.Client \
  --activate-all --include-personal --username "$UNITY_EMAIL" --password "$UNITY_PASSWORD"
```

Workflow `unity-android.yml` делает именно это (с ретраями на transient-сбои
и понятной диагностикой при `Invalid Credential`).

Требуемые секреты:

| Secret | Назначение |
|---|---|
| `UNITY_EMAIL` | email Unity-аккаунта (обязательно) |
| `UNITY_PASSWORD` | пароль Unity-аккаунта (обязательно) |

Требования к аккаунту (иначе `Invalid Credential` 143.002):

- email подтверждён;
- у аккаунта есть обычный Unity ID с паролем (не только вход через
  Google/Apple SSO);
- на аккаунте автоматизации выключен MFA/2FA;
- в Unity Hub на любой машине активирован план **Unity Personal**
  (Licenses → Add → Get a free personal license).

Secret `UNITY_LICENSE` для Personal-сборки больше не нужен.

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

## Эмуляторный smoke и жест-ассершен (batch 17E)

`run_smoke_tests=true` дополнительно выполняет на эмуляторе реальный жест:
`adb shell input swipe 100 880 950 880 800` по строкам доски и проверяет в
logcat маркер `WORDS_SWIPE`, который пишет клиент при зафиксированном
свайпе (клиентская презентация; авторитетность пути проверяет сервер).

Известный класс фейлов hosted-эмулятора (не код проекта):

- `adb shell monkey ...` — "Monkey aborted due to error" при падении
  системного провайдера (напр. media.module);
- `adb shell input keyevent 82` — Broken pipe / exit 224 на этапе
  разблокировки эмулятора;
- `adb install` — "Failure calling service package: Broken pipe".

Правило: такой фейл считается инфраструктурным, один повторный прогон; при
повторении — задача не блокируется, устройство-ассершен остаётся в статусе
"ожидает стабилизации hosted-эмулятора" (прецеденты: 34437012338 →
34437841806; серия 34455925881/34457772896/34459251481).

## Релог smoke-скрипта и автоматический повтор (batch 21B)

Серия 34455925881/34457772896/34459251481 показала, что ручное "перезапусти и
посмотри" не масштабируется: три прогона подряд падали в инфраструктуре, а
человеческая сессия-оркестратор тратила цикл на каждый повтор.

Изменения:

1. **Скрипт в репозитории** — `tools/android-smoke.sh`. Внутри
   `android-emulator-runner` каждая строка `script:` выполняется отдельным
   `sh -c`, поэтому состояние между строками теряется; раньше весь прогон
   был склеен в одну `&&`-строку прямо в YAML и его было невозможно ни
   прочитать, ни проверить. Теперь flow живёт в файле, а YAML вызывает его
   одной командой.
2. **Инфраструктурные операции ретраятся внутри скрипта** —
   `wait-for-device`, опрос `sys.boot_completed`, `adb install` (3 попытки),
   `monkey` (3 попытки), `input swipe` (3 попытки). `input keyevent 82`
   переведён в best-effort: он падал на полностью рабочем устройстве.
3. **Вторая нога — другой образ эмулятора.** Job `smoke-retry` запускается
   только если `smoke` упал (`needs.smoke.result == 'failure'`), и всегда на
   API 34. Это повтор *бэкенда*, а не ослабление проверки: скрипт тот же,
   ассершен тот же.
4. **Вердикт в одном месте** — job `smoke-gate` зелён, если хотя бы одна нога
   напечатала `WORDS_SWIPE_SMOKE_OK`. Обе нозги красные = реальный продукт-фейл.
5. **Диагностика сохраняется** — `logcat.txt` и `android-smoke.png`
   выгружаются артефактом в обеих ногах (`if: always()`), поэтому скриншот
   можно посмотреть глазами, не пересобирая прогон.
6. **Ночной само-хил** — `workflow_dispatch` больше не единственный вход:
   `schedule` (03:17 и 15:17 UTC) гоняет main со smoke. У расписания нет
   `inputs`, поэтому значения входных параметров нормализует job `setup`
   (`build_type=debug`, `run_smoke=true`, `api_level=35`) — иначе
   `if: inputs.run_smoke_tests == true` на schedule-событии молча
   выключал бы именно smoke-ногу.

Отчёт о бэкенде: скрипт печатает `ANDROID_BACKEND=...`
(`github-hosted-emulator-api35`, при повторе `github-hosted-emulator-api34-retry`).
Локальный эмулятор на OCI-хосте недоступен физически: нет `/dev/kvm`, нет
виртуализационных флагов CPU (Ampere A1 без вложенной виртуализации) и не
установлен Android SDK — проверено 2026-09-10. Поэтому fallback-цепочка из
`docs/AUTONOMOUS-DEVELOPMENT-MASTER.md` §9 для этого проекта сворачивается в
пункт 4 (доступный облачный эмулятор с реальным автоматизационным API), а
`myandroid.org` и аналоги остаются интерактивными и не используются.
