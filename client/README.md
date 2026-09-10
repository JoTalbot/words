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

## Current M1 server-bound bootstrap

The runtime IMGUI bootstrap can operate either as a local presentation demo or
as an authoritative server-bound client. In server mode it:

- creates a match through `POST /v1/matches`;
- connects the active seat token to the binary protobuf WebSocket;
- sends only `SubmitWordIntent` messages for selected cell paths;
- renders canonical `MatchStateSnapshot` and `WordValidatedEvent` data from the server;
- checks `/readyz`, rotates the active seat token without printing raw tokens,
  and fetches the authoritative final result;
- displays selected server-mode submissions as pending overlays and reconciles
  them with `WordValidatedEvent.client_sequence` plus canonical snapshots.

The lightweight C# protobuf adapter is scoped to `proto/wordarena/v1/match.proto`
and does not make the client authoritative for score, ownership, locks, word
validity, match completion, or session credentials.
