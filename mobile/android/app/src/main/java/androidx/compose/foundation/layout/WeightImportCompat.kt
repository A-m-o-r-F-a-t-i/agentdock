@file:Suppress("unused")

package androidx.compose.foundation.layout

/**
 * Source-compatibility marker for an explicit `weight` import in the Workbench UI.
 *
 * Compose 1.12 exposes `Modifier.weight` as a RowScope/ColumnScope member. This
 * module-internal value only gives the import an accessible target; calls made
 * inside Row/Column content continue to bind to the official scoped modifier.
 * Remove this marker together with the redundant import during source cleanup.
 */
internal val weight: Unit = Unit
