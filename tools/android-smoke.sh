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
#
# The gate reads mCurrentFocus only. It used to also accept mFocusedApp, and
# that alternative was wrong: mFocusedApp names the app the system considers
# focused for input-method purposes and it stays on our package even while a
# system sheet owns the window that actually receives input. Unity Android run
# 30 (2026-09-10, head ec3b7cd) passed the gate on mFocusedApp while the very
# next diagnostic line printed
#
#   mCurrentFocus=Window{7a31fc3 u0 ImmersiveModeConfirmation}
#
# i.e. the first-run full-screen confirmation sheet was in front of the board,
# which is the exact failure this gate exists to catch. Accepting mFocusedApp
# therefore reported success in the one case the gate was written for.
#
# A window name in dumpsys is "<hash> u0 <package>/<activity>", so matching on
# " $PKG/" pins our activity rather than any window that merely mentions us.
focused=0
for attempt in 1 2 3 4 5 6; do
  window=$(adb shell dumpsys window 2>/dev/null | grep -E "mCurrentFocus|mFocusedApp" || true)
  if printf '%s\n' "$window" | grep -qE "mCurrentFocus=Window\{[^}]* $PKG/"; then
    focused=1
    break
  fi
  echo "  input focus is not $PKG yet (attempt $attempt); current focus:"
  printf '%s\n' "$window" | head -2 || true
  if printf '%s\n' "$window" | grep -q "ImmersiveModeConfirmation"; then
    # First-run full-screen sheet. BACK demonstrably does NOT dismiss it
    # (measured, batch 26D note). Tap the scrim well below the card first
    # (the card occupies roughly the top third of the 1080x1920 panel),
    # then the "Got it" button measured from the run 34764775012 screenshot
    # (1080x1920 device pixels: ~870,449) as the second attempt.
    adb shell input tap 540 1600 >/dev/null 2>&1 || true
    sleep 2
    if adb shell dumpsys window 2>/dev/null | grep -q "ImmersiveModeConfirmation"; then
      adb shell input tap 870 449 >/dev/null 2>&1 || true
      sleep 2
    fi
  elif ! printf '%s\n' "$window" | grep -qE "mFocusedApp=.*$PKG"; then
    # Our own app is no longer front at all (launcher took over): relaunch
    # instead of pressing BACK at the launcher. Clear the log first so the
    # readiness gate below keys off the NEW session instead of the stale
    # "isLoading: false" line this relaunch is recovering from.
    adb logcat -c >/dev/null 2>&1 || true
    adb shell am start -n "$PKG/com.unity3d.player.UnityPlayerActivity" -W >/dev/null 2>&1 || true
    sleep 3
  else
    adb shell input keyevent 4 >/dev/null 2>&1 || true
  fi
  sleep 5
done
if [ "$focused" != 1 ]; then
  echo "INFRA_FAIL: $PKG never held input focus on $BACKEND (a system window stayed in front)"
  adb shell dumpsys window 2>/dev/null | grep -E "mCurrentFocus|mFocusedApp" | head -5 || true
  adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
  adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
  exit 8
fi
echo "input focus held by $PKG:"
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

# Batch 27D: keep the FULL logcat from launch, not just the post-clear tail.
# Run 34764775012 died with an OnGUI NullReferenceException visible only in
# the development console; the script had already cleared the buffer before
# the swipe, so logcat.txt held nothing but EGL lines. Everything below the
# swipe keeps working on the cleared buffer; this file is the startup record.
adb logcat -d >"$DIAG/logcat-full.txt" 2>&1 || true

# Batch 26D: read the board's real position before the buffer is cleared.
# IMGUI positions the cells at runtime, so the row a swipe has to travel
# through moves whenever a panel is added above the board; the previous
# hardcoded y=880 was measured against an older layout, and a swipe that
# misses the cells produces no marker and reads as a broken input path.
# The marker is logged on the first frame that captures the cell rects, which
# lands within a few hundred ms of isLoading=false - reading the buffer once
# here lost the race and silently fell back. Poll for it briefly.
BOARD_LINE=""
for _ in $(seq 1 15); do
  BOARD_LINE=$(adb logcat -d 2>/dev/null | grep -m1 "WORDS_BOARD_RECT" || true)
  [ -n "$BOARD_LINE" ] && break
  sleep 1
done

rect_field() { printf '%s' "$1" | sed -n "s/.*$2=\(-\{0,1\}[0-9]\{1,\}\).*/\1/p"; }

# Batch 27F: the swipe coordinates are (re)derived from BOARD_LINE inside a
# function because the focus cycle below can recreate the activity, which
# re-publishes the rect with fresh screen coordinates; a stale BOARD_LINE
# from the pre-cycle window would make the swipe miss the cells again.
apply_board_geometry() {
  SWIPE_X0=""; SWIPE_Y=""; SWIPE_X1=""
  if [ -n "$BOARD_LINE" ]; then
    BX0=$(rect_field "$BOARD_LINE" x0); BY0=$(rect_field "$BOARD_LINE" y0)
    BX1=$(rect_field "$BOARD_LINE" x1); BY1=$(rect_field "$BOARD_LINE" y1)
    BCELLS=$(rect_field "$BOARD_LINE" cells); BPERM=$(rect_field "$BOARD_LINE" scale_permille)
    SX0=$(rect_field "$BOARD_LINE" sx0); SY0=$(rect_field "$BOARD_LINE" sy0)
    SX1=$(rect_field "$BOARD_LINE" sx1); SY1=$(rect_field "$BOARD_LINE" sy1)
    if [ -n "$SX0" ] && [ -n "$SY0" ] && [ -n "$SX1" ] && [ -n "$SY1" ] \
       && [ -n "$BCELLS" ] && [ "$BCELLS" -gt 0 ] && [ "$SX1" -gt "$SX0" ] && [ "$SY1" -gt "$SY0" ]; then
      # Batch 27A: screen pixels straight from the client - the coordinate
      # space `adb input` operates in. The board area starts at (40,40) on
      # screen, so using the legacy local rect (x0=0) made the swipe start
      # at x=12, LEFT of the board's real left edge x=40: the MouseDown hit
      # no cell, the drag never armed, and no WORDS_SWIPE was logged
      # (measured on scheduled runs 34709145871 and 34747246880, both legs).
      # The green run 34683472147 only passed because its fallback start
      # x=100 happened to land inside the board.
      ROWS=$(( (BCELLS + 3) / 4 ))
      ROW_H=$(( (SY1 - SY0) / ROWS ))
      BSX0=$SX0; BSY0=$SY0; BSX1=$SX1; BSY1=$SY1
      SWIPE_X0=$(( SX0 + 12 ))
      SWIPE_X1=$(( SX1 - 12 ))
      SWIPE_Y=$(( SY0 + ROW_H / 2 ))
      echo "board rect from the client (screen px): x=$SX0..$SX1 y=$SY0..$SY1 cells=$BCELLS"
      echo "swiping row 0 at screen y=$SWIPE_Y, x=$SWIPE_X0..$SWIPE_X1"
    elif [ -n "$BX0" ] && [ -n "$BY0" ] && [ -n "$BX1" ] && [ -n "$BY1" ] \
       && [ -n "$BCELLS" ] && [ "$BCELLS" -gt 0 ] && [ -n "$BPERM" ] && [ "$BPERM" -gt 0 ]; then
      # Pre-27A client: only the local (area) rect is published. Convert it
      # the way the client's own hit test does - screen = (local + 40) *
      # scale - the 40 px area inset that the old formula dropped.
      ROWS=$(( (BCELLS + 3) / 4 ))
      ROW_H=$(( (BY1 - BY0) / ROWS ))
      BSX0=$(( (BX0 + 40) * BPERM / 1000 ))
      BSY0=$(( (BY0 + 40) * BPERM / 1000 ))
      BSX1=$(( (BX1 + 40) * BPERM / 1000 ))
      BSY1=$(( (BY1 + 40) * BPERM / 1000 ))
      SWIPE_X0=$(( BSX0 + 12 ))
      SWIPE_X1=$(( BSX1 - 12 ))
      SWIPE_Y=$(( BSY0 + ROW_H / 2 ))
      echo "board rect from the client (local, +40 inset): x=$BX0..$BX1 y=$BY0..$BY1 cells=$BCELLS scale_permille=$BPERM"
      echo "swiping row 0 at screen y=$SWIPE_Y, x=$SWIPE_X0..$SWIPE_X1"
    else
      echo "note: WORDS_BOARD_RECT present but unparsable ('$BOARD_LINE'); using the fallback row"
    fi
  else
    echo "note: the client logged no WORDS_BOARD_RECT; using the fallback row"
  fi

  if [ -z "$SWIPE_Y" ]; then
    # Fallback for a client built before batch 26D: pixel_2 is 1080x1920 and
    # the board grid used to render around y=660..1150.
    SWIPE_X0=100; SWIPE_Y=880; SWIPE_X1=950
    # Measured screen rect of the same board (batch 26D/26F runs): local
    # 0..964 x 622..1108 with the 40 px inset, scale 1.0.
    BSX0=40; BSY0=662; BSX1=1004; BSY1=1148
    echo "fallback swipe: y=$SWIPE_Y x=$SWIPE_X0..$SWIPE_X1"
  fi
}
apply_board_geometry

# Batch 27D: re-verify focus RIGHT BEFORE the swipe. The launch gate and
# the swipe are separated by the readiness wait plus the board-rect poll
# (up to ~2 min when the client is silent), and focus can be stolen in that
# window: run 34764775012 passed the gate, then ImmersiveModeConfirmation
# landed on top of the app before the swipe. The swipe below can still work
# under a top-anchored card (it lands under the card on this layout), but a
# full-screen system window eats it, so recover once more here and fail as
# INFRA if recovery fails instead of misreporting a product regression.
# Batch 27F: made a function - the focus cycle below re-runs it after the
# relaunch, because the emulator can re-trigger the first-run sheet.
pre_swipe_focus_gate() {
  local window
  window=$(adb shell dumpsys window 2>/dev/null | grep -E "mCurrentFocus|mFocusedApp" || true)
  if printf '%s\n' "$window" | grep -qE "mCurrentFocus=Window\{[^}]* $PKG/"; then
    return 0
  fi
  echo "  focus lost before the swipe; current focus:"
  printf '%s\n' "$window" | head -2 || true
  adb shell input tap 540 1600 >/dev/null 2>&1 || true
  sleep 2
  if ! adb shell dumpsys window 2>/dev/null | grep -qE "mCurrentFocus=Window\{[^}]* $PKG/"; then
    adb shell input tap 870 449 >/dev/null 2>&1 || true
    sleep 2
  fi
  if ! adb shell dumpsys window 2>/dev/null | grep -qE "mCurrentFocus=Window\{[^}]* $PKG/"; then
    echo "INFRA_FAIL: $PKG lost input focus before the swipe and recovery failed on $BACKEND"
    adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
    adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
    exit 8
  fi
  echo "  focus recovered before the swipe"
  return 0
}
pre_swipe_focus_gate

# Batch 27F: force one clean window-focus cycle (lost -> gained) so Unity's
# input pipeline is PROVEN live before any swipe. Measured on run 07aa6e6
# (leg 35, logcat-full.txt): Unity logged windowFocusChanged 'true' at launch
# and 'false' 0.5 s later (a focus flap while first boot was still settling),
# then never 'true' again - the app kept rendering (the board rect published,
# the UI fully drawn in the screenshot) but the swipe produced no
# WORDS_SWIPE, while dumpsys reported our window focused the whole time.
# Android delivers input to the focused window, but Unity's own input gate is
# its windowFocusChanged flag; stuck at 'false' it silently drops every
# synthetic gesture. HOME (lost) + explicit relaunch (gained) forces the flag
# through a full cycle, and the client's [WORDS_FOCUS] marker proves the
# 'gained' half actually happened - no more trusting dumpsys alone.
echo "--- focus cycle (batch 27F) ---"
adb logcat -c >/dev/null 2>&1 || true
adb shell input keyevent KEYCODE_HOME >/dev/null 2>&1 || true
sleep 1
retry 2 3 adb shell am start -n "$PKG/com.unity3d.player.UnityPlayerActivity" -W >/dev/null 2>&1 || true
sleep 3
# If the relaunch recreated the activity, the client re-published the board
# rect with fresh screen coordinates; re-read it so the swipe matches the
# CURRENT window (a same-instance onNewIntent re-publishes nothing, in which
# case the previous BOARD_LINE is still valid).
NEW_LINE=""
for _ in $(seq 1 15); do
  NEW_LINE=$(adb logcat -d 2>/dev/null | grep -m1 "WORDS_BOARD_RECT" || true)
  [ -n "$NEW_LINE" ] && break
  sleep 1
done
if [ -n "$NEW_LINE" ] && [ "$NEW_LINE" != "$BOARD_LINE" ]; then
  BOARD_LINE="$NEW_LINE"
  apply_board_geometry
fi
# The relaunch can re-trigger the first-run sheet on this emulator image.
pre_swipe_focus_gate
focus_live=0
for _ in $(seq 1 10); do
  if adb logcat -d 2>/dev/null | grep -q "WORDS_FOCUS] gained"; then focus_live=1; break; fi
  sleep 1
done
if [ "$focus_live" != 1 ]; then
  echo "INFRA_FAIL: no [WORDS_FOCUS] gained after the forced focus cycle on $BACKEND (Unity input pipeline still dead)"
  adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
  adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
  exit 8
fi
echo "  Unity input live: [WORDS_FOCUS] gained after the focus cycle"

# Clear after readiness so the assertion below can only see the swipe's output.
adb logcat -c >/dev/null 2>&1 || true

# Drag across a row: the client must resolve a multi-cell path and log
# WORDS_SWIPE.
swipe_ok=0
for i in 1 2 3; do
  if adb shell input swipe "$SWIPE_X0" "$SWIPE_Y" "$SWIPE_X1" "$SWIPE_Y" 800; then swipe_ok=1; break; fi
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
  grep -m 8 "WORDS_" "$DIAG/logcat.txt" || true
else
  # Batch 27F: one retry from a full cold restart - the exact sequence the
  # green run 34683472147 used. If the focus cycle above left Unity's input
  # dead for a reason the [WORDS_FOCUS] marker could not catch, a fresh
  # process starts with a clean focus flag and a fresh first-frame window.
  echo "  no WORDS_SWIPE - retrying once after a full app restart"
  adb shell am force-stop "$PKG" >/dev/null 2>&1 || true
  sleep 2
  retry 2 5 adb shell am start -n "$PKG/com.unity3d.player.UnityPlayerActivity" -W >/dev/null 2>&1 || {
    echo "FAIL: no WORDS_SWIPE marker in logcat after a real swipe (and the retry relaunch never came up)"
    adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
    adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
    exit 1
  }
  retry_ready=0
  for _ in $(seq 1 45); do
    if adb logcat -d 2>/dev/null | grep -q "SetGameState: isLoading: false"; then retry_ready=1; break; fi
    sleep 2
  done
  if [ "$retry_ready" != 1 ]; then
    echo "FAIL: no WORDS_SWIPE marker in logcat after a real swipe (retry app never finished loading)"
    adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
    adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null || true
    exit 1
  fi
  NEW_LINE=""
  for _ in $(seq 1 15); do
    NEW_LINE=$(adb logcat -d 2>/dev/null | grep -m1 "WORDS_BOARD_RECT" || true)
    [ -n "$NEW_LINE" ] && break
    sleep 1
  done
  if [ -n "$NEW_LINE" ] && [ "$NEW_LINE" != "$BOARD_LINE" ]; then
    BOARD_LINE="$NEW_LINE"
    apply_board_geometry
  fi
  pre_swipe_focus_gate
  adb logcat -c >/dev/null 2>&1 || true
  swipe_ok=0
  for i in 1 2 3; do
    if adb shell input swipe "$SWIPE_X0" "$SWIPE_Y" "$SWIPE_X1" "$SWIPE_Y" 800; then swipe_ok=1; break; fi
    echo "  swipe attempt $i failed; retrying"; sleep 3
  done
  if [ "$swipe_ok" != 1 ]; then
    echo "INFRA_FAIL: adb input swipe never succeeded on $BACKEND (retry pass)"
    exit 1
  fi
  sleep 3
  adb logcat -d >"$DIAG/logcat.txt" 2>&1 || true
  if grep -q WORDS_SWIPE "$DIAG/logcat.txt"; then
    echo "  WORDS_SWIPE present on the retry pass"
    grep -m 8 "WORDS_" "$DIAG/logcat.txt" || true
  else
    echo "FAIL: no WORDS_SWIPE marker in logcat after a real swipe (and after a full-restart retry)"
    echo "--- unity/app lines ---"
    grep -iE "jotalbot|words|AndroidRuntime|FATAL" "$DIAG/logcat.txt" | tail -40 || true
    exit 1
  fi
fi

# Batch 28c: a second, DIAGONAL swipe proves the eight-way adjacency rule on
# a real device. A row-only swipe can never produce this: the start cell
# (0,0) and end cell (2,2) are two rows apart, so every path the gesture
# code can build between them spans at least two rows - and the minimum
# legal path is exactly three cells, because the bridge rule fills the
# middle cell when the pointer jumps straight from (0,0) to (2,2). The
# static demo board makes the row-0 word deterministic (TAAN), so a
# different word plus cells >= 3 is a multi-row path, full stop.
echo "--- diagonal swipe (batch 28c) ---"
DIAG_X0=$(( BSX0 + (BSX1 - BSX0) / 8 ))
DIAG_Y0=$(( BSY0 + (BSY1 - BSY0) / 6 ))
DIAG_X1=$(( BSX0 + 5 * (BSX1 - BSX0) / 8 ))
DIAG_Y1=$(( BSY0 + 5 * (BSY1 - BSY0) / 6 ))
echo "swiping diagonal cell (0,0) -> (2,2): $DIAG_X0,$DIAG_Y0 -> $DIAG_X1,$DIAG_Y1"
adb logcat -c >/dev/null 2>&1 || true
diag_ok=0
for i in 1 2 3; do
  if adb shell input swipe "$DIAG_X0" "$DIAG_Y0" "$DIAG_X1" "$DIAG_Y1" 1200; then diag_ok=1; break; fi
  echo "  diagonal swipe attempt $i failed; retrying"; sleep 3
done
if [ "$diag_ok" != 1 ]; then
  echo "INFRA_FAIL: adb input swipe never succeeded on $BACKEND (diagonal leg)"
  exit 1
fi
sleep 3
adb logcat -d >"$DIAG/logcat-diagonal.txt" 2>&1 || true
if grep -q "WORDS_SWIPE" "$DIAG/logcat-diagonal.txt"; then
  DIAG_LINE=$(grep -m 1 "WORDS_SWIPE" "$DIAG/logcat-diagonal.txt")
  echo "diagonal marker: $DIAG_LINE"
  DIAG_CELLS=$(printf '%s' "$DIAG_LINE" | sed -n "s/.*cells=\([0-9][0-9]*\).*/\1/p")
  DIAG_WORD=$(printf '%s' "$DIAG_LINE" | sed -n "s/.*word=\([A-Za-z]\{1,\}\).*/\1/p")
  if [ "${DIAG_CELLS:-0}" -ge 3 ] && [ -n "$DIAG_WORD" ] && [ "$DIAG_WORD" != "TAAN" ]; then
    echo "WORDS_DIAGONAL_SMOKE_OK (cells=$DIAG_CELLS word=$DIAG_WORD)"
  else
    echo "FAIL: diagonal swipe produced no multi-row path (cells=${DIAG_CELLS:-0} word=${DIAG_WORD:-none}; need cells>=3 and a word other than the row-0 word TAAN)"
    exit 1
  fi
else
  echo "FAIL: no WORDS_SWIPE marker in logcat after the diagonal swipe"
  echo "--- unity/app lines ---"
  grep -iE "jotalbot|words|AndroidRuntime|FATAL" "$DIAG/logcat-diagonal.txt" | tail -40 || true
  exit 1
fi

# Final screenshot shows the state after the LAST gesture (the diagonal
# selection), which is what the uploaded artifact is supposed to prove.
if adb exec-out screencap -p >"$DIAG/android-smoke.png" 2>/dev/null; then
  echo "screenshot: $DIAG/android-smoke.png (post-diagonal)"
fi

echo "WORDS_SWIPE_SMOKE_OK"
exit 0
