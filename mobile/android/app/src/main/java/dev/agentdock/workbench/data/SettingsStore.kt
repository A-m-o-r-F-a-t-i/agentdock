package dev.agentdock.workbench.data

import android.content.Context
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import dev.agentdock.workbench.model.WorkbenchSettings
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

private val Context.workbenchDataStore by preferencesDataStore(name = "workbench_settings")

class SettingsStore(private val context: Context) {
    private object Keys {
        val theme = stringPreferencesKey("theme")
        val language = stringPreferencesKey("language")
        val density = stringPreferencesKey("density")
        val endpoint = stringPreferencesKey("endpoint")
        val remoteEndpoint = booleanPreferencesKey("remote_endpoint_enabled")
        val notifications = booleanPreferencesKey("notifications_enabled")
        val guardian = booleanPreferencesKey("guardian_enabled")
        val guardianPaused = booleanPreferencesKey("guardian_paused")
        val autoRepair = booleanPreferencesKey("auto_repair_enabled")
        val bootCheck = booleanPreferencesKey("boot_health_check_enabled")
        val guardianInterval = intPreferencesKey("guardian_interval_minutes")
        val onlyWifi = booleanPreferencesKey("only_on_wifi")
        val onlyCharging = booleanPreferencesKey("only_while_charging")
        val mobileData = booleanPreferencesKey("allow_mobile_data")
        val publicAccess = booleanPreferencesKey("public_access_enabled")
        val detailedCalls = booleanPreferencesKey("detailed_calls")
        val toolOutput = booleanPreferencesKey("tool_output_enabled")
        val toolOutputMax = intPreferencesKey("tool_output_max_chars")
        val customPermission = booleanPreferencesKey("custom_permission_enabled")
        val permissionFilesystem = stringPreferencesKey("permission_filesystem")
        val permissionNetwork = stringPreferencesKey("permission_network")
        val permissionBoundary = stringPreferencesKey("permission_boundary")
        val approvalPolicy = stringPreferencesKey("approval_policy")
        val approvalReviewer = stringPreferencesKey("approval_reviewer")
        val granularFileWrites = booleanPreferencesKey("granular_file_writes")
        val granularCommands = booleanPreferencesKey("granular_commands")
        val granularNetwork = booleanPreferencesKey("granular_network")
        val granularMcp = booleanPreferencesKey("granular_mcp")
        val granularManagement = booleanPreferencesKey("granular_management")
        val granularOther = booleanPreferencesKey("granular_other")
        val desiredNodeState = stringPreferencesKey("desired_node_state")
        val projectTree = stringPreferencesKey("project_tree_uri")
        val artifactTree = stringPreferencesKey("artifact_tree_uri")
        val onboarding = booleanPreferencesKey("onboarding_complete")
    }

    val settings: Flow<WorkbenchSettings> = context.workbenchDataStore.data.map(::decode)

    suspend fun current(): WorkbenchSettings = settings.first()

    suspend fun update(transform: (WorkbenchSettings) -> WorkbenchSettings) {
        context.workbenchDataStore.edit { preferences -> encode(preferences, transform(decode(preferences))) }
    }

    suspend fun setDesiredNodeState(value: String) = update { it.copy(desiredNodeState = value) }
    suspend fun setGuardianPaused(value: Boolean) = update { it.copy(guardianPaused = value) }

    private fun decode(p: Preferences): WorkbenchSettings = WorkbenchSettings(
        theme = p[Keys.theme] ?: "system",
        language = p[Keys.language] ?: "system",
        density = p[Keys.density] ?: "comfortable",
        endpoint = p[Keys.endpoint] ?: "http://127.0.0.1:8765",
        remoteEndpointEnabled = p[Keys.remoteEndpoint] ?: false,
        notificationsEnabled = p[Keys.notifications] ?: true,
        guardianEnabled = p[Keys.guardian] ?: false,
        guardianPaused = p[Keys.guardianPaused] ?: false,
        autoRepairEnabled = p[Keys.autoRepair] ?: false,
        bootHealthCheckEnabled = p[Keys.bootCheck] ?: false,
        guardianIntervalMinutes = (p[Keys.guardianInterval] ?: 15).coerceIn(15, 1440),
        onlyOnWifi = p[Keys.onlyWifi] ?: false,
        onlyWhileCharging = p[Keys.onlyCharging] ?: false,
        allowMobileData = p[Keys.mobileData] ?: false,
        publicAccessEnabled = p[Keys.publicAccess] ?: false,
        detailedCalls = p[Keys.detailedCalls] ?: false,
        toolOutputEnabled = p[Keys.toolOutput] ?: true,
        toolOutputMaxChars = (p[Keys.toolOutputMax] ?: 20_000).coerceIn(1_000, 100_000),
        customPermissionEnabled = p[Keys.customPermission] ?: false,
        permissionFilesystem = p[Keys.permissionFilesystem] ?: "write",
        permissionNetwork = p[Keys.permissionNetwork] ?: "allow",
        permissionBoundary = p[Keys.permissionBoundary] ?: "none",
        approvalPolicy = p[Keys.approvalPolicy] ?: "on-request",
        approvalReviewer = p[Keys.approvalReviewer] ?: "user",
        granularFileWrites = p[Keys.granularFileWrites] ?: true,
        granularCommands = p[Keys.granularCommands] ?: true,
        granularNetwork = p[Keys.granularNetwork] ?: true,
        granularMcp = p[Keys.granularMcp] ?: true,
        granularManagement = p[Keys.granularManagement] ?: true,
        granularOther = p[Keys.granularOther] ?: false,
        desiredNodeState = p[Keys.desiredNodeState] ?: "stopped",
        projectTreeUri = p[Keys.projectTree] ?: "",
        artifactTreeUri = p[Keys.artifactTree] ?: "",
        onboardingComplete = p[Keys.onboarding] ?: false
    )

    private fun encode(p: androidx.datastore.preferences.core.MutablePreferences, value: WorkbenchSettings) {
        p[Keys.theme] = value.theme
        p[Keys.language] = value.language
        p[Keys.density] = value.density
        p[Keys.endpoint] = value.endpoint
        p[Keys.remoteEndpoint] = value.remoteEndpointEnabled
        p[Keys.notifications] = value.notificationsEnabled
        p[Keys.guardian] = value.guardianEnabled
        p[Keys.guardianPaused] = value.guardianPaused
        p[Keys.autoRepair] = value.autoRepairEnabled
        p[Keys.bootCheck] = value.bootHealthCheckEnabled
        p[Keys.guardianInterval] = value.guardianIntervalMinutes.coerceIn(15, 1440)
        p[Keys.onlyWifi] = value.onlyOnWifi
        p[Keys.onlyCharging] = value.onlyWhileCharging
        p[Keys.mobileData] = value.allowMobileData
        p[Keys.publicAccess] = value.publicAccessEnabled
        p[Keys.detailedCalls] = value.detailedCalls
        p[Keys.toolOutput] = value.toolOutputEnabled
        p[Keys.toolOutputMax] = value.toolOutputMaxChars.coerceIn(1_000, 100_000)
        p[Keys.customPermission] = value.customPermissionEnabled
        p[Keys.permissionFilesystem] = value.permissionFilesystem
        p[Keys.permissionNetwork] = value.permissionNetwork
        p[Keys.permissionBoundary] = value.permissionBoundary
        p[Keys.approvalPolicy] = value.approvalPolicy
        p[Keys.approvalReviewer] = value.approvalReviewer
        p[Keys.granularFileWrites] = value.granularFileWrites
        p[Keys.granularCommands] = value.granularCommands
        p[Keys.granularNetwork] = value.granularNetwork
        p[Keys.granularMcp] = value.granularMcp
        p[Keys.granularManagement] = value.granularManagement
        p[Keys.granularOther] = value.granularOther
        p[Keys.desiredNodeState] = value.desiredNodeState
        p[Keys.projectTree] = value.projectTreeUri
        p[Keys.artifactTree] = value.artifactTreeUri
        p[Keys.onboarding] = value.onboardingComplete
    }
}
