# Word Arena Unity client

This directory is the single Unity project for the mobile client.

- Unity version: `6000.0.59f2`
- Primary target: Android APK
- Build entry point: `Words.BuildCommand.BuildAndroid`
- Android package: `com.jotalbot.words`
- Debug output: an APK containing ARM64 and x86_64 slices for hosted-emulator smoke tests
- Release output: ARM64 APK; signing is supplied by CI secrets, never committed

The project is intentionally a minimal, valid bootstrap. Gameplay, networking and presentation are added incrementally; the server remains authoritative for rules and match state.

## Runtime controls

The IMGUI bootstrap starts in local demo mode. To exercise the authoritative
path, run the Go game server, enter its base URL in the Server field, then tap
`Create server match`. The client creates a match over REST, connects the active
seat to the binary protobuf WebSocket, sends selected cell paths as
`SubmitWordIntent`, and renders canonical snapshots/events. Android builds force
INTERNET permission from `Assets/Editor/BuildCommand.cs`.
