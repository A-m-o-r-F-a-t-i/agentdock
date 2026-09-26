# Android security model

## Credentials

Core Bearer, pairing and public-access secrets use an Android Keystore AES-GCM key with app-private ciphertext/IV storage. They are excluded from backup/device transfer and never written to DataStore, logs, operation summaries, Intent arguments or Termux command arguments.

The bridge does not return a Bearer through Intent extras. Android rejects credential fields at any nesting depth, expired callbacks and replay after a terminal result. Pairing through a dedicated authenticated Core channel remains pending integration; the existing explicit connection editor stores user-entered credentials with Keystore. Invalid JSON, raw stderr and plugin error text are never copied into operation summaries.

Core Bearer records bind the credential to its scheme, lowercased host and effective port inside the encrypted envelope. Only host case and the default HTTP/HTTPS port are normalized; localhost and its numeric aliases remain distinct scopes. Changing origins never reuses the previous node's credential. Legacy unbound ciphertext is preserved but not transmitted until the user explicitly saves a scoped credential. Removing a locally saved Bearer does not revoke that credential at the server.

CoreCredentialBindingTest covers origin changes, malformed envelopes and invalid token inputs. CoreCredentialStoreTest verifies real Android Keystore encryption, exact-origin reads and legacy preservation without publishing secret values.

## Network

- Loopback HTTP/HTTPS is allowed.
- Non-loopback requires explicit remote enablement and HTTPS.
- Redirects and system proxies are rejected/bypassed.
- Configured origins cannot contain path, query, fragment or user-info.
- Release manifest, signature and asset URLs must be HTTPS.

## Release trust

A digest is not publisher authentication when its expected value is delivered beside the asset. WB07 therefore requires a trusted Ed25519 public key, signed manifest, signature verification before field use, Linux/ARM64 identity, archive SHA-256, structure validation, Core-reported version equality, health validation and rollback. Without this contract install/update returns `pending_manifest` and downloads no Core payload. Signature gates and guarded extraction are implemented, while complete transaction journaling, data-schema backup/rollback and one-fallback retention still require implementation and acceptance.

## Permissions

The Android custom permission editor is closed by default and does not alter the Core policy. When enabled, writes use the latest Core revision and exact profile/approval shape. `never` means operations requiring approval are denied; it is not complete authorization. Explicit deny rules remain effective under full permission. Existing installs are preserved; fresh-install defaults belong to the installer/Core boundary.

## Android surface

The Termux result service and guardian action receiver are non-exported. The boot receiver only schedules a delayed WorkManager check. The tile is protected by `BIND_QUICK_SETTINGS_TILE`. The optional guardian foreground service is started only after explicit user enablement.

Projects/artifacts use Storage Access Framework URIs selected by the user. The app requests no broad shared-storage access. Diagnostics remain in Termux private storage until explicit export.

WB07 contains no ADB/root/Shizuku/accessibility path, media keepalive, one-minute watchdog, vendor bypass, hidden API, embedded Termux/RootFS/Core, or promise of permanent background survival.
