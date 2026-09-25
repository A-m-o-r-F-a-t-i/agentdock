# AgentDock Workbench for Android

This directory contains the native Kotlin + Jetpack Compose Android Workbench. It is a controller and productized bridge for an **external** Termux installation and an AgentDock Core running in a verified Debian PRoot. It does not embed Termux, a root filesystem, Chromium, the Go Core, ADB, root, Shizuku, accessibility automation, or vendor-specific keepalive hacks.

The Android UI consumes the same server-authoritative Core HTTP/SSE contracts as the desktop Workbench. Installation and lifecycle requests are sent through Termux `RUN_COMMAND` to a fixed, user-installed bridge script. Existing nodes are discovery-only until the user explicitly adopts or changes them.

Builds, dependency resolution, lint, unit tests, emulator tests, screenshots and APK packaging for this lane run only in `.github/workflows/parallel-android.yml`.
