#!/usr/bin/env bash
# Word Arena — Android device smoke for the built APK.
#
# Runs inside a reactivecircus/android-emulator-runner step (the runner gives
# each script line its own `sh -c`, so no shell state may cross lines: this
# file exists so the whole flow lives in one script instead).
#
# The script is the single source of truth for the device assertion and is
# deliberately retry-heavy: four consecutive Unity Android runs on 2026-09-10
# failed in hosted-emulator plumbing (`input keyevent 82` Broken pipe, `adb
# install` Broken pipe, monkey aborted on a media-provider crash) before any
# product code was involved. Retrying the *device* operations, and printing
# diagnostics when the *app* fails, separates those two classes of failure.
#
# Usage: tools/android-smoke.sh [APK_DIR]
set -uo pipefail

APK_DIR="${1:-artifact}"
PKG="com.jotalbot.words"
BACKEND="${ANDROID_BACKEND:-github-hosted-emulator}"
DIAG="${DIAG_DIR:-.}"

retry() { # retry <attempts> <delay_s> <cmd...>
  local n="$1" d="$2"; shift 2
  local i
  for i in $(seq 1 "$n"); do
    if "$@"; then return 0; fi
    echo "  attempt $i/$n failed: $* (retrying in ${d}s)"
    sleep "$d"
  done
  return 1
}

echo "ANDROID_BACKEND=$BACKEND"

apk=$(find "$APK_DIR" -name '*.apk' -type f | head -1)
if [ -z "$apk" ]; then
  echo "FAIL: no APK under $APK_DIR"
  exit 1
fi
echo "APK: $apk ($(wc -c <"$apk") bytes)"

echo "--- waiting for the device to be visible ---"
timeout 180 adb wait-for-device || { echo "FAIL: device never appeared"; exit 1; }
booted=0
for _ in $(seq 1 45); do
  if [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then booted=1; break; fi
  sleep 2
done
if [ "$booted" != 1 ]; then
  echo "FAIL: emulator did not finish booting"; exit 1
fi
echo "boot_completed=1"

# Screen unlock is best effort: this emulator build has no PIN, and `input
# keyevent 82` has been observed to die with a broken pipe while the device is
# perfectly usable. Failing here would misreport an infra hiccup as a product
# failure.
adb shell input keyevent 82 >/dev/null 2>&1 || echo "note: unlock keyevent skipped (known flake)"
adb shell settings put global window_animation_scale 0 >/dev/null 2>&1 || true
adb shell settings put global transition_animation_scale 0 >/dev/null 2>&1 || true
adb shell settings put global animator_duration_scale 0 >/dev/null 2>&1 || true

# The hosted emulator pool's actual failure mode, captured in a screenshot on
# 2026-09-10: "System UI isn't responding" steals focus, the synthetic swipe
# lands on the system dialog, and the app never sees a gesture - which looks
# exactly like a broken gesture implementation in the log. hide_error_dialogs
# suppresses ANR/crash dialogs; the focus loop below is the real gate.
adb shell settings put global hide_error_dialogs 1 >/dev/null 2>&1 || true
# The API 35 screenshot also showed the first-run "Viewing full screen / Got it"
# system sheet, which sits above the app and eats the first gesture.
adb shell settings put global immersive_mode_confirmations confirmed >/dev/null 2>&1 || true
adb shell settings put system accelerometer_rotation 1 >/dev/null 2>&1 || true

echo "--- install ---"
retry 3 6 adb install -r -g "$apk" || { echo "FAIL: adb install never succeeded"; exit 1; }

echo "--- launch ---"
retry 3 5 adb shell am start -n "$PKG/com.unity3d.player.UnityPlayerActivity" -W || {
  echo "FAIL: activity never started"; exit 1; }
adb shell pm path "$PKG" || { echo "FAIL: package not installed"; exit 1; }

# Focus must be on our package before any input is meaningful. Three separate
# runs were lost to a system dialog holding the foreground, so "is the app
# actually in front?" is now an explicit, retryable step with its own failure
# class (INFRA_FAIL) instead of a confusing missing-marker assertion.
focused=0
for attempt in 1 2 3 4 5 6; do
  if adb shell dumpsys window 2>/dev/null | grep -qE "mCurrentFocus=Window\{[^ ]* $PKG|mFocusedApp.*$PKG"; then
    focused=1
    break
  fi
  echo "  foreground is not $PKG yet (attempt $attempt); dismissing system dialogs"
  adb shell input keyevent 4 >/dev/null 2>&1 || true
  adb shell input keyevent KEYCODE_BACK >/dev/null 2>&1 || true
  sleep 5
done
if [ "$focused" != 1 ]; then
  echo "INFRA_FAIL: $PKG never took the foreground on $BACKEND"
  adb shell dumpsys window 2>/dev/null | grep -E "mCurrentFocus|mFocusedApp" | head -5 || true
  adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
  adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
  exit 8
fi
adb shell dumpsys window 2>/dev/null | grep -E "mCurrentFocus" | head -2 || true

echo "--- gesture smoke (batch 17E) ---"
# Readiness, not a sleep. Measured on run 34498217023 (API 34): the emulator
# reported "Fully drawn ... +23s785ms" and the client's own log still said
# "SetGameState: isLoading: true" when the previous version of this script
# fired its swipe after a fixed 14 s - the gesture landed on a black loading
# screen, produced no marker, and looked like a broken input path. The client
# already logs its state transitions, so the device smoke waits for the
# authoritative one and fails with the evidence if it never arrives.
ready=0
for _ in $(seq 1 60); do
  if adb logcat -d 2>/dev/null | grep -q "SetGameState: isLoading: false"; then ready=1; break; fi
  if ! adb shell pidof "$PKG" >/dev/null 2>&1; then
    echo "INFRA_FAIL: the app process died before it finished loading"
    adb logcat -d 2>/dev/null | grep -iE "Unity|lowmemorykiller|has died|signal 9" | tail -15 || true
    adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
    adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
    exit 1
  fi
  sleep 3
done
if [ "$ready" != 1 ]; then
  echo "INFRA_FAIL: the client never reported isLoading=false within 180 s on $BACKEND"
  adb logcat -d 2>/dev/null | grep -E "SetGameState|Unity" | tail -10 || true
  adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
  adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
  exit 1
fi
echo "client ready: SetGameState isLoading=false"
# Clear after readiness so the assertion below can only see the swipe's output.
adb logcat -c >/dev/null 2>&1 || true

# pixel_2 is 1080x1920 and the board grid renders around y=660..1150. Drag
# across a row: the client must resolve a multi-cell path and log WORDS_SWIPE.
swipe_ok=0
for i in 1 2 3; do
  if adb shell input swipe 100 880 950 880 800; then swipe_ok=1; break; fi
  echo "  swipe attempt $i failed; retrying"; sleep 3
done
if [ "$swipe_ok" != 1 ]; then
  echo "INFRA_FAIL: adb input swipe never succeeded on $BACKEND"
  adb logcat -d >"$DIAG/logcat-swipe-fail.txt" 2>&1 || true
  exit 1
fi
sleep 3

adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
if adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null; then
  echo "screenshot: $DIAG/android-smoke.png"
fi

if grep -q WORDS_SWIPE "$DIAG/logcat.txt"; then
  echo "WORDS_SWIPE_SMOKE_OK"
  grep -m 8 "WORDS_" "$DIAG/logcat.txt" || true
  exit 0
fi

echo "FAIL: no WORDS_SWIPE marker in logcat after a real swipe"
echo "--- unity/app lines ---"
grep -iE "jotalbot|words|AndroidRuntime|FATAL" "$DIAG/logcat.txt" | tail -40 || true
exit 1
