# Unity Client

The Unity project lives in [`client/unity`](unity/).

## Responsibilities

- touch/swipe input and local gesture visualization;
- board rendering and animation;
- audio/haptics;
- presentation of authoritative server state;
- bounded client prediction for presentation responsiveness;
- reconnect UX;
- telemetry emission through the approved client event layer.

## Non-responsibilities

The client must not be authoritative for:

- score;
- word validity;
- ownership of cells;
- match completion;
- rewards or inventory;
- ranking/MMR.

## Unity project conventions

Unity version, packages and build settings are pinned under `client/unity/`. The Android build entry point is `Words.BuildCommand.BuildAndroid`; CI details are documented in [`docs/unity-ci.md`](../docs/unity-ci.md) and [`docs/android-build.md`](../docs/android-build.md).

Keep networking, gameplay state and presentation separated so the simulation can be exercised without rendering.
