# Android build и smoke test

## Toolchain

### CI

Android-сборка выполняется внутри x86_64 GameCI Unity environment, выбранного по Unity `6000.0.59f2` и target `Android`. Unity Android Build Support image предоставляет согласованные SDK, NDK и OpenJDK для этой версии; workflow не устанавливает случайные версии вручную.

Совместимость проверяется самой Unity во время `game-ci/unity-builder@v4`. Полные фактические пути и версии SDK/NDK/JDK остаются в логе конкретного runner и не подменяются значениями с ARM-сервера.

### ARM-сервер

Аудит 2026-09-09 зафиксировал:

- системный OpenJDK 17.0.20 (`java` и `javac` доступны);
- `/opt/android-sdk` существует;
- `adb` и `emulator` в PATH отсутствуют;
- AVD не найден и `adb devices`/`emulator -list-avds` локально запускать не из чего;
- Unity Editor и Android Build Support локально отсутствуют.

Этот SDK не используется как Unity toolchain: наличие каталога без `adb`, emulator и Unity compatibility не доказывает готовность Android build host.

## Outputs

- Application ID: `com.jotalbot.words`.
- Debug: `Builds/Android/words-debug.apk`, ARM64 + x86_64, development flags.
- Release: `Builds/Android/words-release.apk`, ARM64, без debug flags.
- GitHub artifact: `words-android-debug` или `words-android-release`.

AAB intentionally не включён в bootstrap workflow: сначала нужен проверенный signed APK и smoke test. Для production AAB можно добавить отдельный release workflow после прохождения smoke/release gate.

## Signing

Debug APK использует стандартный CI/debug signing путь.

Release build требует GitHub Secrets:

- `ANDROID_KEYSTORE_BASE64` — base64 содержимое keystore;
- `ANDROID_KEYSTORE_PASS` — пароль keystore;
- `ANDROID_KEYALIAS_NAME` — alias;
- `ANDROID_KEYALIAS_PASS` — пароль alias.

Keystore, passwords, `.pem`, `.key` и другие credentials запрещено помещать в Git.

## Smoke test

При `run_smoke_tests=true` workflow:

1. скачивает APK artifact;
2. поднимает временный API 35 x86_64 Android emulator на GitHub runner;
3. устанавливает APK через `adb install -r`;
4. запускает package через `monkey`;
5. проверяет наличие установленного package через `adb shell pm path`.

Это smoke test установки/старта, а не замена полноценному тесту gameplay, сети и производительности на физическом Android phone. На ARM64 OCI server эмулятор не блокирует Unity → APK pipeline.

## Physical device

Для проверки на телефоне скачайте debug artifact, разрешите установку через USB/ADB на тестовом устройстве и выполните:

```bash
adb install -r words-debug.apk
adb shell monkey -p com.jotalbot.words 1
adb logcat --pid="$(adb shell pidof com.jotalbot.words)"
```

Команды выполняются только на авторизованном тестовом устройстве.
