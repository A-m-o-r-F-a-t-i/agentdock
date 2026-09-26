package dev.agentdock.workbench.ui

import android.app.Application
import android.content.Intent
import android.net.Uri
import androidx.core.content.ContextCompat
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.createSavedStateHandle
import androidx.lifecycle.viewmodel.CreationExtras
import androidx.lifecycle.viewModelScope
import dev.agentdock.workbench.BuildConfig
import dev.agentdock.workbench.WorkbenchApplication
import dev.agentdock.workbench.data.ListQuery
import dev.agentdock.workbench.data.ManagementContract
import dev.agentdock.workbench.lifecycle.GuardianScheduler
import dev.agentdock.workbench.lifecycle.GuardianService
import dev.agentdock.workbench.model.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.util.UUID

/** Only transient client presentation is saved here. Core owns every business state. */
data class WorkbenchUiState(
    val settings: WorkbenchSettings = WorkbenchSettings(),
    val snapshot: WorkbenchSnapshot = WorkbenchSnapshot(),
    val screen: WorkbenchScreen = WorkbenchScreen.Home,
    val selectedConversationId: String = "",
    val selectedCallId: String = "",
    val selectedTaskId: String = "",
    val selectedApprovalId: String = "",
    val tasksQuery: ListQuery = ListQuery(),
    val conversationsQuery: ListQuery = ListQuery(),
    val checkedIds: Set<String> = emptySet(),
    val detail: JSONObject? = null,
    val detailKind: String = "",
    val detailId: String = "",
    val detailError: String = "",
    val payload: JSONObject? = null,
    val payloadKind: String = "response",
    val children: List<WorkbenchItem> = emptyList(),
    val batchResult: JSONObject? = null,
    val insertionDraft: String = "",
    val loading: Boolean = false,
    val actionBusy: Boolean = false,
    val message: String = "",
    val liveStatus: String = "尚未连接活动流",
    val fixture: Boolean = false,
    val operations: List<BridgeOperation> = emptyList(),
    val buildIdentity: String = "${BuildConfig.PRODUCT_VERSION} · ${BuildConfig.CANDIDATE_SHA.take(12)} · ${BuildConfig.SIGNING_LABEL}"
)

class WorkbenchViewModel(
    application: Application,
    fixtureRequested: Boolean,
    private val saved: SavedStateHandle = SavedStateHandle()
) : AndroidViewModel(application) {
    private val graph = (application as WorkbenchApplication).graph
    private val _state = MutableStateFlow(WorkbenchUiState(
        fixture = fixtureRequested && BuildConfig.DEBUG,
        screen = WorkbenchScreen.fromRoute(saved["route"]),
        selectedConversationId = saved["conversation"] ?: "",
        selectedTaskId = saved["task"] ?: "",
        selectedCallId = saved["call"] ?: "",
        selectedApprovalId = saved["approval"] ?: "",
        tasksQuery = restoreQuery("tasks"), conversationsQuery = restoreQuery("conversations"),
        insertionDraft = saved["draft"] ?: ""
    ))
    val state: StateFlow<WorkbenchUiState> = _state.asStateFlow()
    private var activityStream: Job? = null
    private var callStream: Job? = null
    private var refreshJob: Job? = null
    private var detailJob: Job? = null
    private var refreshDebounce: Job? = null
    private var refreshGeneration = 0L

    init {
        viewModelScope.launch {
            graph.settings.settings.collectLatest { settings ->
                _state.update { it.copy(settings = settings) }
                if (!_state.value.fixture) GuardianScheduler.configure(getApplication(), settings)
            }
        }
        refresh()
    }

    fun navigate(screen: WorkbenchScreen) {
        val current = _state.value.screen
        if (current == screen) return
        val history = (saved.get<ArrayList<String>>("history") ?: arrayListOf()).toMutableList()
        history.add(current.route)
        saved["history"] = ArrayList(history.takeLast(24))
        saved["route"] = screen.route
        _state.update { it.copy(screen = screen, checkedIds = emptySet()) }
    }

    fun back() {
        val history = (saved.get<ArrayList<String>>("history") ?: arrayListOf()).toMutableList()
        val route = if (history.isEmpty()) WorkbenchScreen.Home.route else history.removeAt(history.lastIndex)
        saved["history"] = ArrayList(history)
        saved["route"] = route
        _state.update { it.copy(screen = WorkbenchScreen.fromRoute(route), checkedIds = emptySet()) }
    }

    fun selectWorkspace(item: WorkbenchItem) {
        updateQuery("tasks", _state.value.tasksQuery.copy(workspaceId = item.id, offset = 0), false)
        updateQuery("conversations", _state.value.conversationsQuery.copy(workspaceId = item.id, offset = 0), false)
        navigate(WorkbenchScreen.Conversations)
        refresh()
    }

    fun selectConversation(item: WorkbenchItem) {
        if (item.id != _state.value.selectedConversationId) {
            saved["draft"] = ""
            saved["submission_id"] = ""
        }
        saved["conversation"] = item.id
        _state.update { it.copy(selectedConversationId = item.id, insertionDraft = saved["draft"] ?: "") }
        navigate(WorkbenchScreen.Conversations)
        loadDetail("conversations", item.id)
        refresh()
    }

    fun selectTask(item: WorkbenchItem) {
        saved["task"] = item.id
        _state.update { it.copy(selectedTaskId = item.id) }
        navigate(WorkbenchScreen.Tasks)
        loadDetail("tasks", item.id)
    }

    fun selectCall(item: WorkbenchItem) {
        saved["call"] = item.id
        _state.update { it.copy(selectedCallId = item.id, payload = null, children = emptyList()) }
        navigate(WorkbenchScreen.CallDetail)
        loadDetail("calls", item.id)
    }

    fun selectApproval(item: WorkbenchItem) {
        saved["approval"] = item.id
        _state.update { it.copy(selectedApprovalId = item.id) }
        navigate(WorkbenchScreen.Approvals)
        loadDetail("approvals", item.id)
    }

    fun updateQuery(kind: String, value: ListQuery, reload: Boolean = true) {
        for ((key, entry) in mapOf("view" to value.view, "search" to value.search, "status" to value.status,
            "workspace" to value.workspaceId, "tag" to value.tag)) saved["${kind}_$key"] = entry
        saved["${kind}_offset"] = value.offset
        _state.update { if (kind == "tasks") it.copy(tasksQuery = value, checkedIds = emptySet()) else it.copy(conversationsQuery = value, checkedIds = emptySet()) }
        if (reload) refresh()
    }

    fun toggleChecked(identity: String) {
        _state.update {
            val ids = if (identity in it.checkedIds) it.checkedIds - identity else it.checkedIds + identity
            it.copy(checkedIds = ids.take(ManagementContract.MAX_BATCH).toSet())
        }
    }

    fun checkPage(kind: String) {
        val items = if (kind == "tasks") _state.value.snapshot.tasks else _state.value.snapshot.conversations
        _state.update { it.copy(checkedIds = items.map { row -> row.id }.toSet()) }
    }

    fun manage(kind: String, ids: List<String>, action: String, title: String = "", tags: List<String> = emptyList(), confirmed: Boolean = false) = perform {
        val outcome = graph.repository.batch(kind, ids.toList(), action, title, tags, confirmed)
        _state.update { it.copy(batchResult = outcome.serverValue) }
        outcome
    }

    fun clearMessage() = _state.update { it.copy(message = "") }

    fun refresh() {
        refreshJob?.cancel()
        val generation = ++refreshGeneration
        refreshJob = viewModelScope.launch {
            _state.update { it.copy(loading = true) }
            val selected = _state.value
            try {
                val received = graph.repository.refresh(selected.fixture, selected.selectedConversationId, selected.tasksQuery, selected.conversationsQuery)
                val snapshot = if (selected.fixture) fixtureSnapshot(received) else received
                if (generation != refreshGeneration) return@launch
                val operations = withContext(Dispatchers.IO) { graph.operations.list() }
                _state.update { it.copy(snapshot = snapshot, loading = false, operations = operations) }
                if (!selected.fixture && snapshot.coreHealth in setOf(NodeHealth.Healthy, NodeHealth.Degraded)) startStreams()
            } catch (error: CancellationException) {
                throw error
            } catch (error: Exception) {
                if (generation == refreshGeneration) _state.update { it.copy(loading = false, message = ManagementContract.failure(error)) }
            }
        }
    }

    private fun loadDetail(kind: String, identity: String) {
        detailJob?.cancel()
        _state.update { it.copy(detail = null, detailKind = kind, detailId = identity, detailError = "") }
        if (_state.value.fixture) return
        detailJob = viewModelScope.launch {
            try {
                val detail = graph.repository.detail(kind, identity)
                if (_state.value.detailId == identity && _state.value.detailKind == kind) _state.update { it.copy(detail = detail) }
            } catch (error: CancellationException) { throw error
            } catch (error: Exception) { _state.update { it.copy(detailError = ManagementContract.failure(error)) } }
        }
    }

    fun loadPayload(kind: String, offset: Long = 0) {
        if (_state.value.fixture) return
        val identity = _state.value.selectedCallId
        viewModelScope.launch {
            try {
                val value = graph.repository.callPayload(identity, kind, offset)
                if (_state.value.selectedCallId == identity) _state.update { it.copy(payload = value, payloadKind = kind, detailError = "") }
            } catch (error: CancellationException) { throw error
            } catch (error: Exception) { _state.update { it.copy(detailError = ManagementContract.failure(error)) } }
        }
    }

    fun updateSettings(transform: (WorkbenchSettings) -> WorkbenchSettings) {
        viewModelScope.launch { try { graph.settings.update(transform) } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error) } }
    }

    fun saveConnection(endpoint: String, remoteEnabled: Boolean, bearer: String) {
        viewModelScope.launch {
            try {
                dev.agentdock.workbench.data.EndpointPolicy.resolve(endpoint, remoteEnabled)
                stopStreams()
                withContext(Dispatchers.IO) { if (bearer.isNotBlank()) graph.credentials.put("core_bearer", bearer.trim()) }
                graph.settings.update { it.copy(endpoint = endpoint.trim().trimEnd('/'), remoteEndpointEnabled = remoteEnabled) }
                refresh()
            } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error) }
        }
    }

    fun clearBearer() {
        stopStreams()
        graph.credentials.clear("core_bearer")
        _state.update { it.copy(message = "Core Bearer 已删除") }
    }

    fun setInsertionDraft(text: String) {
        if (text.toByteArray(Charsets.UTF_8).size > 8192) return
        if (text != _state.value.insertionDraft) saved["submission_id"] = ""
        saved["draft"] = text
        _state.update { it.copy(insertionDraft = text) }
    }

    fun sendInsertion(text: String) = perform {
        val conversation = requireConversation()
        val submission = saved.get<String>("submission_id").orEmpty().ifBlank {
            UUID.randomUUID().toString().replace("-", "").also { saved["submission_id"] = it }
        }
        val result = graph.repository.sendInsertion(conversation, text, submission)
        if (result.accepted && _state.value.selectedConversationId == conversation && _state.value.insertionDraft == text) setInsertionDraft("")
        result
    }

    fun insertionAction(insertionId: String, action: String) = perform { graph.repository.insertionAction(requireConversation(), insertionId, action) }
    fun terminateConversation() = perform { graph.repository.terminateConversation(requireConversation()) }
    fun callAction(action: String) = perform { graph.repository.callAction(_state.value.selectedCallId.ifBlank { error("请先选择调用") }, action) }
    fun approvalAction(approve: Boolean, allowWorkspace: Boolean = false) = perform {
        graph.repository.approvalAction(_state.value.selectedApprovalId.ifBlank { error("请先选择审批") }, approve, allowWorkspace)
    }
    fun capabilityAction(kind: String, item: WorkbenchItem, enable: Boolean) = perform { graph.repository.capabilityAction(kind, item.id, enable) }

    fun savePermissions() = perform {
        val settings = _state.value.settings
        val root = _state.value.snapshot.effectivePermission ?: error("Core 未返回有效权限")
        val effective = root.optJSONObject("effective") ?: root
        check(effective.has("custom_permissions_enabled")) { "pending_integration：当前 Core 尚未整合 WB02 自定义权限开关" }
        val revision = effective.optLong("revision", root.optLong("revision", 0))
        check(revision > 0) { "Core 权限修订号无效，请刷新" }
        val approval = JSONObject().put("mode", settings.approvalPolicy)
        if (settings.approvalPolicy == "granular") approval.put("granular", JSONObject()
            .put("file_writes", settings.granularFileWrites).put("commands", settings.granularCommands)
            .put("network", settings.granularNetwork).put("mcp", settings.granularMcp)
            .put("management", settings.granularManagement).put("other", settings.granularOther))
        graph.repository.savePermission(JSONObject().put("scope", "global").put("expected_revision", revision)
            .put("custom_permissions_enabled", settings.customPermissionEnabled)
            .put("settings", JSONObject().put("permission_profile", JSONObject()
                .put("filesystem", settings.permissionFilesystem).put("network", settings.permissionNetwork).put("sandbox_boundary", settings.permissionBoundary))
                .put("approval_policy", approval).put("approval_reviewer", settings.approvalReviewer)))
    }

    fun runTermux(operation: String) = perform {
        when (operation) {
            "start", "restart" -> graph.settings.setDesiredNodeState("running")
            "stop" -> graph.settings.setDesiredNodeState("stopped")
        }
        val pending = withContext(Dispatchers.IO) { graph.termux.dispatch(operation, JSONObject().put("source", "android_ui")) }
        ActionOutcome(true, "queued", "已提交 ${pending.operation}；结果以 Termux 回执为准")
    }

    fun setGuardianEnabled(enabled: Boolean) {
        if (_state.value.fixture) { fixtureBlocked(); return }
        viewModelScope.launch {
            try {
                graph.settings.update { it.copy(guardianEnabled = enabled, guardianPaused = if (enabled) false else it.guardianPaused) }
                GuardianScheduler.configure(getApplication(), graph.settings.current())
                if (enabled) ContextCompat.startForegroundService(getApplication(), Intent(getApplication(), GuardianService::class.java))
                else getApplication<Application>().stopService(Intent(getApplication(), GuardianService::class.java))
            } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error) }
        }
    }

    fun setGuardianPaused(paused: Boolean) {
        if (_state.value.fixture) { fixtureBlocked(); return }
        viewModelScope.launch {
            try {
                graph.settings.setGuardianPaused(paused)
                val settings = graph.settings.current()
                GuardianScheduler.configure(getApplication(), settings)
                if (paused) getApplication<Application>().stopService(Intent(getApplication(), GuardianService::class.java))
                else if (settings.guardianEnabled) ContextCompat.startForegroundService(getApplication(), Intent(getApplication(), GuardianService::class.java))
            } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error) }
        }
    }

    fun saveTreeUri(kind: String, uri: Uri) {
        try {
            getApplication<Application>().contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
            updateSettings { if (kind == "project") it.copy(projectTreeUri = uri.toString()) else it.copy(artifactTreeUri = uri.toString()) }
        } catch (error: Exception) { showError(error) }
    }

    fun exportBundledAsset(uri: Uri, assetName: String) {
        viewModelScope.launch {
            try {
                require(assetName in setOf("agentdock-workbench", "agentdock-workbench-bootstrap.sh"))
                withContext(Dispatchers.IO) {
                    getApplication<Application>().assets.open(assetName).use { input ->
                        getApplication<Application>().contentResolver.openOutputStream(uri, "w")?.use { input.copyTo(it) } ?: error("无法打开导出目标")
                    }
                }
                _state.update { it.copy(message = "已导出 $assetName") }
            } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error) }
        }
    }

    private fun perform(block: suspend () -> ActionOutcome) {
        if (_state.value.fixture) { fixtureBlocked(); return }
        if (_state.value.actionBusy) return
        _state.update { it.copy(actionBusy = true) }
        viewModelScope.launch {
            try {
                val outcome = block()
                _state.update { it.copy(message = outcome.message) }
                refresh()
            } catch (error: CancellationException) { throw error } catch (error: Exception) { showError(error)
            } finally { _state.update { it.copy(actionBusy = false) } }
        }
    }

    fun setFixtureScenario(value: String) {
        check(BuildConfig.DEBUG && _state.value.fixture)
        require(value in setOf("normal", "empty", "error"))
        saved["fixture_scenario"] = value
        refresh()
    }

    private fun fixtureSnapshot(base: WorkbenchSnapshot): WorkbenchSnapshot = when (saved.get<String>("fixture_scenario")) {
        "empty" -> base.copy(tasks = emptyList(), conversations = emptyList(), calls = emptyList(),
            approvals = emptyList(), insertions = emptyList(), taskPage = dev.agentdock.workbench.data.ResourcePage(total = 0))
        "error" -> base.copy(coreHealth = NodeHealth.Unknown, errors = mapOf("tasks" to "FIXTURE_UNAVAILABLE：测试用断连状态"))
        else -> base
    }

    private fun fixtureBlocked() = _state.update { it.copy(message = "Fixture 模式禁止实际写入与设备操作") }
    private fun requireConversation(): String = _state.value.selectedConversationId.ifBlank { error("请先选择对话") }
    private fun showError(error: Throwable) = _state.update { it.copy(message = ManagementContract.failure(error)) }

    private fun startStreams() {
        if (activityStream == null) activityStream = viewModelScope.launch {
            graph.repository.observeActivity(0) { event ->
                _state.update { it.copy(liveStatus = if (event.event in setOf("disconnected", "stopped")) event.data else "活动流 #${event.id}") }
                if (event.event !in setOf("disconnected", "stopped")) scheduleRefresh()
            }
        }
        if (callStream == null) callStream = viewModelScope.launch {
            graph.repository.observeCalls(0) { event ->
                _state.update { it.copy(liveStatus = if (event.event in setOf("disconnected", "stopped")) event.data else "调用流 #${event.id}") }
                if (event.event !in setOf("disconnected", "stopped")) scheduleRefresh()
            }
        }
    }

    private fun stopStreams() {
        activityStream?.cancel(); activityStream = null
        callStream?.cancel(); callStream = null
        refreshDebounce?.cancel()
    }

    private fun scheduleRefresh() {
        if (refreshDebounce?.isActive == true) return
        refreshDebounce = viewModelScope.launch { delay(1000); if (!_state.value.loading) refresh() }
    }

    private fun restoreQuery(kind: String) = ListQuery(
        view = saved["${kind}_view"] ?: "active", search = saved["${kind}_search"] ?: "",
        status = saved["${kind}_status"] ?: "", workspaceId = saved["${kind}_workspace"] ?: "",
        tag = saved["${kind}_tag"] ?: "", offset = saved["${kind}_offset"] ?: 0
    )

    class Factory(private val application: Application, private val fixture: Boolean) : ViewModelProvider.Factory {
        @Suppress("UNCHECKED_CAST")
        override fun <T : ViewModel> create(modelClass: Class<T>, extras: CreationExtras): T =
            WorkbenchViewModel(application, fixture, extras.createSavedStateHandle()) as T
    }
}
