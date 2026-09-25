# Android compatibility

- Minimum SDK: 26
- Target/compile SDK: 37
- External Core architecture: Linux ARM64 (`aarch64`)
- Java toolchain: 17
- UI: native Kotlin + Jetpack Compose
- Candidate emulator matrix: API 33, 34, 35 and 37

The app display name is **AgentDock Workbench**. Product version is parsed from `internal/buildinfo/buildinfo.go`. CI injects source SHA, run ID and attempt into candidate metadata and artifact names. Release-shaped candidate APKs use the Android debug key and are explicitly labeled `test-signed`; they are not production-signing evidence.

WB07 targets the official `com.termux` package and `com.termux.app.RunCommandService`. Forks with other package/component names are not silently discovered because that would broaden the command surface.

Android caveats:

- notification permission is runtime-controlled on Android 13+;
- foreground/background execution can change by Android version/vendor;
- WorkManager timing is inexact;
- PRoot is user-space compatibility, not a kernel container boundary;
- browser/EDA automation remains an external Core/Skill/plugin capability;
- emulator passes do not prove physical ARM64 PRoot or vendor power-management behavior.
