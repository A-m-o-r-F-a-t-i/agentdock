# Android lifecycle and desired state

WB07 keeps three controls independent:

1. Core desired state: `running` or `stopped`.
2. Guardian enabled: user opted into visible periodic health checks.
3. Guardian paused: checks are temporarily suspended without changing Core.

Therefore closing the activity, pausing guardian or disabling guardian does not stop Core. Stopping Core persists `desired=stopped` before signaling the process so a guardian cannot treat the stop as a crash. Auto-repair runs only when guardian is enabled, not paused, auto-repair is enabled and desired state is `running`. Repair restarts the current version only; it never updates.

The optional foreground guardian exposes Open, Pause/Resume and Stop Core actions. It checks no more frequently than every 15 minutes and does not promise permanent survival. WorkManager handles periodic constraints such as unmetered network or charging. A boot/package-replaced receiver schedules one delayed health check when enabled; it does not launch Core or a foreground service from the broadcast.

The Quick Settings tile toggles guardian enabled/paused state. It is not a hidden process bypass and does not change Core desired state.

Termux process control records PID, boot identity, start time, process group and session. Signals require a matching owned identity. A reused PID, missing ownership record or surviving unknown group is reported as requiring user review, not as stopped. Stop intent is persisted first. Probe/status do not create node files. A mandatory bridge lock serializes mutations; restoration policy, circuit breaking and complete transaction recovery remain outstanding.
