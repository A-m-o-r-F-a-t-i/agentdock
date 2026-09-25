# Android security model

## Credentials

Core Bearer, pairing and public-access secrets use an Android Keystore AES-GCM key with app-private ciphertext/IV storage. They are excluded from backup/device transfer and never written to DataStore, logs, operation summaries, Intent arguments or Termux command arguments.

A local Bearer returned by Termux is imported only after operation ID, request ID, nonce, operation and schema validation, and only when it is exactly 64 hexadecimal characters.

## Network

- Loopback HTTP/HTTPS is allowed.
- Non-loopback requires explicit remote enablement and HTTPS.
- Redirects and system proxies are rejected/bypassed.
- Configured origins cannot contain path, query, fragment or user-info.
- Release manifest, signature and asset URLs must be HTTPS.

## Release trust

A digest is not publisher authentication when its expected value is delivered beside the asset. WB07 therefore requires a trusted Ed25519 public key, signed manifest, signature verification before field use, Linux/ARM64 identity, archive SHA-256, structure validation, Core-reported version equality, health validation and rollback. Without this contract install/update returns `pending_manifest` and downloads no Core payload.

## Permissions

The Android custom permission editor is closed by default and does not alter the Core policy. When enabled, writes use the latest Core revision and exact profile/approval shape. `never` means operations requiring approval are denied; it is not complete authorization. Explicit deny rules remain effective under full permission. Existing installs are preserved; fresh-install defaults belong to the installer/Core boundary.

## Android surface

The Termux result service and guardian action receiver are non-exported. The boot receiver only schedules a delayed WorkManager check. The tile is protected by `BIND_QUICK_SETTINGS_TILE`. The optional guardian foreground service is started only after explicit user enablement.

Projects/artifacts use Storage Access Framework URIs selected by the user. The app requests no broad shared-storage access. Diagnostics remain in Termux private storage until explicit export.

WB07 contains no ADB/root/Shizuku/accessibility path, media keepalive, one-minute watchdog, vendor bypass, hidden API, embedded Termux/RootFS/Core, or promise of permanent background survival.
