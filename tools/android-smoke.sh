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

echo "--- install ---"
retry 3 6 adb install -r -g "$apk" || { echo "FAIL: adb install never succeeded"; exit 1; }

echo "--- launch ---"
retry 3 5 adb shell monkey -p "$PKG" 1 || { echo "FAIL: monkey launch failed"; exit 1; }
adb shell pm path "$PKG" || { echo "FAIL: package not installed"; exit 1; }

echo "--- gesture smoke (batch 17E) ---"
# Let Unity draw the runtime board, then clear the log so the assertion below
# can only see lines produced by the swipe itself.
sleep 14
adb logcat -c >/dev/null 2>&1 || true

# pixel_2 is 1080x1920 and the board grid renders around y=660..1150. Drag
# across a row: the client must resolve a multi-cell path and log WORDS_SWIPE.
swipe_ok=0
for i in 1 2 3; do
  if adb shell input swipe 100 880 950 880 800; then swipe_ok=1; break; fi
  echo "  swipe attempt $i failed; retrying"; sleep 3
done
if [ "$swipe_ok" != 1 ]; then
  echo "FAIL: adb input swipe never succeeded"
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
