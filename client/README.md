# Unity Client

The Unity project will live here once the M0 client prototype starts.

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

Recommended starting packages should be pinned in the Unity project and updated deliberately, not floating with automatic package upgrades.

Keep networking, gameplay state and presentation separated so the simulation can be exercised without rendering.
