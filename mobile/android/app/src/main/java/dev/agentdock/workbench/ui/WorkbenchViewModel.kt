package dev.agentdock.workbench.ui

import android.app.Application
import android.content.Intent
import android.net.Uri
import androidx.core.content.ContextCompat
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import dev.agentdock.workbench.BuildConfig
import dev.agentdock.workbench.WorkbenchApplication
import dev.agentdock.workbench.lifecycle.GuardianScheduler
import dev.agentdock.workbench.lifecycle.GuardianService
import dev.agentdock.workbench.model.ActionOutcome
import dev.agentdock.workbench.model.BridgeOperation
import dev.agentdock.workbench.model.WorkbenchItem
import dev.agentdock.workbench.model.WorkbenchScreen
import dev.agentdock.workbench.model.WorkbenchSettings
import dev.agentdock.workbench.model.WorkbenchSnapshot
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import org.json.JSONObject


data class WorkbenchUiState(
    val settings: WorkbenchSettings = WorkbenchSettings(),
    val snapshot: WorkbenchSnapshot = WorkbenchSnapshot(),
    val screen: WorkbenchScreen = WorkbenchScreen.Home,
    val selectedConversationId: String = "",
    val selectedCallId: String = "",
    val selectedTaskId: String = "",
    val selectedApprovalId: String = "",
    val loading: Boolean = false,
    val message: String = "",
    val liveStatus: String = "尚未连接活动流",
    val fixture: Boolean = false,
    val operations: List<BridgeOperation> = emptyList(),
    val buildIdentity: String = "${BuildConfig.PRODUCT_VERSION} · ${BuildConfig.CANDIDATE_SHA.take(12)} · ${BuildConfig.SIGNING_LABEL}"
)

class WorkbenchViewModel(application: Application, private val fixtureRequested: Boolean) : AndroidViewModel(application) {
    private val graph = (application as WorkbenchApplication).graph
    private val _state = MutableStateFlow(WorkbenchUiState(fixture = fixtureRequested && BuildConfig.DEBUG))
    val state: StateFlow<WorkbenchUiState> = _state.asStateFlow()
    private var activityStream: Job? = null
    private var callStream: Job? = null
    private var refreshDebounce: Job? = null

    init {
        viewModelScope.launch {
            graph.settings.settings.collectLatest { settings ->
                _state.update { it.copy(settings = settings) }
                GuardianScheduler.configure(getApplication(), settings)
            }
        }
        refresh()
    }

    fun navigate(screen: WorkbenchScreen) {
        _state.update { it.copy(screen = screen, message = "") }
    }

    fun selectConversation(item: WorkbenchItem) {
        _state.update { it.copy(selectedConversationId = item.id, screen = WorkbenchScreen.Conversations) }
        refresh()
    }

    fun selectCall(item: WorkbenchItem) {
        _state.update { it.copy(selectedCallId = item.id, screen = WorkbenchScreen.CallDetail) }
    }

    fun selectTask(item: WorkbenchItem) {
        _state.update { it.copy(selectedTaskId = item.id, screen = WorkbenchScreen.Tasks) }
    }

    fun selectApproval(item: WorkbenchItem) {
        _state.update { it.copy(selectedApprovalId = item.id, screen = WorkbenchScreen.Approvals) }
    }

    fun clearMessage() = _state.update { it.copy(message = "") }

    fun refresh() {
        if (_state.value.loading) return
        viewModelScope.launch {
            _state.update { it.copy(loading = true, message = "") }
            val current = _state.value
            runCatching { graph.repository.refresh(current.fixture, current.selectedConversationId) }
                .onSuccess { snapshot ->
                    _state.update {
                        it.copy(
                            snapshot = snapshot,
                            loading = false,
                            operations = graph.operations.list(),
                            message = if (snapshot.fixture) "CI fixture 数据已显式启用" else ""
                        )
                    }
                    if (!current.fixture && snapshot.coreHealth.name !in setOf("Stopped", "Unknown")) startStreams()
                }
                .onFailure { error ->
                    _state.update { it.copy(loading = false, operations = graph.operations.list(), message = error.message ?: "刷新失败") }
                }
        }
    }

    fun updateSettings(transform: (WorkbenchSettings) -> WorkbenchSettings) {
        viewModelScope.launch {
            runCatching { graph.settings.update(transform) }
                .onFailure { showError(it) }
        }
    }

    fun saveConnection(endpoint: String, remoteEnabled: Boolean, bearer: String) {
        viewModelScope.launch {
            runCatching {
                dev.agentdock.workbench.data.EndpointPolicy.resolve(endpoint, remoteEnabled)
                if (bearer.isNotBlank()) graph.credentials.put("core_bearer", bearer.trim())
                graph.settings.update { it.copy(endpoint = endpoint.trim().trimEnd('/'), remoteEndpointEnabled = remoteEnabled) }
            }.onSuccess { refresh() }.onFailure { showError(it) }
        }
    }

    fun clearBearer() {
        graph.credentials.clear("core_bearer")
        _state.update { it.copy(message = "Core Bearer 已从 Android Keystore 删除") }
    }

    fun sendInsertion(text: String) = perform {
        graph.repository.sendInsertion(requireConversation(), text)
    }

    fun insertionAction(insertionId: String, action: String) = perform {
        graph.repository.insertionAction(requireConversation(), insertionId, action)
    }

    fun terminateConversation() = perform {
        graph.repository.terminateConversation(requireConversation())
    }

    fun callAction(action: String) = perform {
        val id = _state.value.selectedCallId.ifBlank { error("请先选择调用") }
        graph.repository.callAction(id, action)
    }

    fun approvalAction(approve: Boolean, allowWorkspace: Boolean = false) = perform {
        val id = _state.value.selectedApprovalId.ifBlank { error("请先选择审批") }
        graph.repository.approvalAction(id, approve, allowWorkspace)
    }

    fun capabilityAction(kind: String, item: WorkbenchItem, enable: Boolean) = perform {
        graph.repository.capabilityAction(kind, item.id, enable)
    }

    fun savePermissions() = perform {
        val settings = _state.value.settings
        if (!settings.customPermissionEnabled) error("请先启用自定义权限设置")
        val root = _state.value.snapshot.effectivePermission ?: error("Core 未返回有效权限修订号")
        val effective = root.optJSONObject("effective") ?: root
        val revision = effective.optLong("revision", 0)
        if (revision <= 0) error("Core 权限修订号无效，请刷新")
        val approval = JSONObject().put("mode", settings.approvalPolicy)
        if (settings.approvalPolicy == "granular") {
            approval.put("granular", JSONObject()
                .put("file_writes", settings.granularFileWrites)
                .put("commands", settings.granularCommands)
                .put("network", settings.granularNetwork)
                .put("mcp", settings.granularMcp)
                .put("management", settings.granularManagement)
                .put("other", settings.granularOther))
        }
        val profile = JSONObject()
            .put("filesystem", settings.permissionFilesystem)
            .put("network", settings.permissionNetwork)
            .put("sandbox_boundary", settings.permissionBoundary)
        val body = JSONObject()
            .put("scope", "global")
            .put("expected_revision", revision)
            .put("settings", JSONObject()
                .put("permission_profile", profile)
                .put("approval_policy", approval)
                .put("approval_reviewer", settings.approvalReviewer))
        graph.repository.savePermission(body)
    }

    fun runTermux(operation: String) {
        viewModelScope.launch {
            _state.update { it.copy(loading = true, message = "") }
            runCatching {
                when (operation) {
                    "start", "restart" -> graph.settings.setDesiredNodeState("running")
                    "stop" -> graph.settings.setDesiredNodeState("stopped")
                }
                graph.termux.dispatch(operation, JSONObject().put("source", "android_ui"))
            }.onSuccess { op ->
                _state.update { it.copy(loading = false, operations = graph.operations.list(), message = "已提交 ${op.operation}：${op.operationId}") }
            }.onFailure { error ->
                _state.update { it.copy(loading = false, operations = graph.operations.list(), message = error.message ?: "Termux 操作失败") }
            }
        }
    }

    fun setGuardianEnabled(enabled: Boolean) {
        viewModelScope.launch {
            runCatching {
                graph.settings.update { it.copy(guardianEnabled = enabled, guardianPaused = if (enabled) false else it.guardianPaused) }
                val settings = graph.settings.current()
                GuardianScheduler.configure(getApplication(), settings)
                if (enabled) ContextCompat.startForegroundService(getApplication(), Intent(getApplication(), GuardianService::class.java))
                else getApplication<Application>().stopService(Intent(getApplication(), GuardianService::class.java))
            }.onSuccess {
                _state.update { it.copy(message = if (enabled) "守护已启用" else "守护已关闭；Core 未被停止") }
            }.onFailure { showError(it) }
        }
    }

    fun setGuardianPaused(paused: Boolean) {
        viewModelScope.launch {
            graph.settings.setGuardianPaused(paused)
            val settings = graph.settings.current()
            GuardianScheduler.configure(getApplication(), settings)
            if (paused) getApplication<Application>().stopService(Intent(getApplication(), GuardianService::class.java))
            else if (settings.guardianEnabled) ContextCompat.startForegroundService(getApplication(), Intent(getApplication(), GuardianService::class.java))
            _state.update { it.copy(message = if (paused) "仅暂停守护，Core 未停止" else "守护已恢复") }
        }
    }

    fun saveTreeUri(kind: String, uri: Uri) {
        val flags = Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION
        runCatching { getApplication<Application>().contentResolver.takePersistableUriPermission(uri, flags) }
        updateSettings {
            when (kind) {
                "project" -> it.copy(projectTreeUri = uri.toString())
                "artifact" -> it.copy(artifactTreeUri = uri.toString())
                else -> it
            }
        }
    }

    fun exportBundledAsset(uri: Uri, assetName: String) {
        viewModelScope.launch {
            runCatching {
                require(assetName in setOf("agentdock-workbench", "agentdock-workbench-bootstrap.sh"))
                getApplication<Application>().assets.open(assetName).use { input ->
                    getApplication<Application>().contentResolver.openOutputStream(uri, "w")?.use { output -> input.copyTo(output) }
                        ?: error("无法打开导出目标")
                }
            }.onSuccess { _state.update { it.copy(message = "已导出 $assetName") } }
                .onFailure { showError(it) }
        }
    }

    private fun perform(block: suspend () -> ActionOutcome) {
        viewModelScope.launch {
            _state.update { it.copy(loading = true, message = "") }
            runCatching { block() }.onSuccess { outcome ->
                _state.update { it.copy(loading = false, message = outcome.message) }
                refresh()
            }.onFailure { error ->
                _state.update { it.copy(loading = false, message = error.message ?: "操作失败") }
            }
        }
    }

    private fun requireConversation(): String = _state.value.selectedConversationId.ifBlank { error("请先选择对话") }
    private fun showError(error: Throwable) = _state.update { it.copy(message = error.message ?: "操作失败") }

    private fun startStreams() {
        if (activityStream == null) {
            activityStream = viewModelScope.launch {
                graph.repository.observeActivity(0) { event ->
                    _state.update { it.copy(liveStatus = if (event.event == "disconnected") "活动流断开：${event.data}" else "活动流 #${event.id}") }
                    if (event.event != "disconnected") scheduleRefresh()
                }
            }
        }
        if (callStream == null) {
            callStream = viewModelScope.launch {
                graph.repository.observeCalls(0) { event ->
                    _state.update { it.copy(liveStatus = if (event.event == "disconnected") "调用流断开：${event.data}" else "调用流 #${event.id}") }
                    if (event.event != "disconnected") scheduleRefresh()
                }
            }
        }
    }

    private fun scheduleRefresh() {
        refreshDebounce?.cancel()
        refreshDebounce = viewModelScope.launch { delay(500); refresh() }
    }

    class Factory(private val application: Application, private val fixture: Boolean) : ViewModelProvider.Factory {
        @Suppress("UNCHECKED_CAST")
        override fun <T : ViewModel> create(modelClass: Class<T>): T = WorkbenchViewModel(application, fixture) as T
    }
}
