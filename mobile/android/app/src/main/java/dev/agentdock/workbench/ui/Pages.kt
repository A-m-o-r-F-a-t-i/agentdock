package dev.agentdock.workbench.ui

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items as lazyItems
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Slider
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.agentdock.workbench.model.BridgeOperation
import dev.agentdock.workbench.model.WorkbenchItem
import dev.agentdock.workbench.model.WorkbenchScreen
import java.nio.charset.StandardCharsets

@Composable
fun WorkbenchPage(
    state: WorkbenchUiState,
    viewModel: WorkbenchViewModel,
    modifier: Modifier = Modifier,
    contentPadding: PaddingValues = PaddingValues()
) {
    val pageModifier = modifier.padding(contentPadding).padding(horizontal = 16.dp, vertical = if (state.settings.density == "compact") 6.dp else 12.dp)
    val errorKeys = when (state.screen) {
        WorkbenchScreen.CallDetail -> listOf("calls")
        WorkbenchScreen.Plugins -> listOf("plugins", "mcp")
        WorkbenchScreen.InsertAndStop -> listOf("conversations", "insert")
        else -> listOf(state.screen.route)
    }
    val errors = errorKeys.mapNotNull { state.snapshot.errors[it] }
    if (state.screen != WorkbenchScreen.Home && errors.isNotEmpty()) {
        Column(pageModifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("Core 数据读取失败", modifier = Modifier.testTag("resource-error"), style = MaterialTheme.typography.titleLarge)
            errors.forEach { Text(it) }
            Button(onClick = viewModel::refresh) { Text("重新读取") }
        }
        return
    }
    when (state.screen) {
        WorkbenchScreen.Home -> HomePage(state, viewModel, pageModifier)
        WorkbenchScreen.Workspaces -> WorkspacePage(state, viewModel, pageModifier)
        WorkbenchScreen.Conversations -> ManagementPage("conversations", state, viewModel, pageModifier)
        WorkbenchScreen.Tasks -> ManagementPage("tasks", state, viewModel, pageModifier)
        WorkbenchScreen.Activity -> ActivityPage(state, viewModel, pageModifier)
        WorkbenchScreen.CallDetail -> CallManagementPage(state, viewModel, pageModifier)
        WorkbenchScreen.InsertAndStop -> InsertAndStopPage(state, viewModel, pageModifier)
        WorkbenchScreen.Approvals -> ApprovalsPage(state, viewModel, pageModifier)
        WorkbenchScreen.Permissions -> PermissionsPage(state, viewModel, pageModifier)
        WorkbenchScreen.Skills -> SkillsPage(state, viewModel, pageModifier)
        WorkbenchScreen.Plugins -> PluginsPage(state, viewModel, pageModifier)
        WorkbenchScreen.CoreConnections -> ConnectionsPage(state, viewModel, pageModifier)
        WorkbenchScreen.InstallUpdate -> DeploymentPage(state, viewModel, pageModifier)
        WorkbenchScreen.ProjectsFiles -> ProjectsFilesPage(state, viewModel, pageModifier)
        WorkbenchScreen.LogsDiagnostics -> LogsDiagnosticsPage(state, viewModel, pageModifier)
        WorkbenchScreen.Settings -> SettingsPage(state, viewModel, pageModifier)
    }
}

@Composable
private fun HomePage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            PageHeader("Android 完整 Workbench", "原生 Compose 控制面；Core 与 Termux 仍是独立、可验证的外部组件。")
        }
        item {
            ElevatedCard(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("Core ${state.snapshot.coreHealth.name}", style = MaterialTheme.typography.titleLarge)
                    Text(state.snapshot.connectionMessage)
                    Text("版本 ${state.snapshot.coreVersion.ifBlank { "未知" }} · ${state.liveStatus}")
                    Text("期望节点状态：${state.settings.desiredNodeState}")
                }
            }
        }
        item {
            MetricRow(
                "对话" to state.snapshot.conversations.size,
                "任务" to state.snapshot.tasks.size,
                "调用" to state.snapshot.calls.size,
                "审批" to state.snapshot.approvals.size
            )
        }
        item {
            HorizontalActions(
                listOf(
                    "任务中心" to { viewModel.navigate(WorkbenchScreen.Tasks) },
                    "安装与更新" to { viewModel.navigate(WorkbenchScreen.InstallUpdate) },
                    "Core 连接" to { viewModel.navigate(WorkbenchScreen.CoreConnections) },
                    "权限" to { viewModel.navigate(WorkbenchScreen.Permissions) }
                )
            )
        }
        item {
            InfoCard(
                "边界说明",
                "关闭本页面不会停止 Core。暂停守护只暂停健康检查；“停止 Core”会先写入 desired=stopped，再交给 Termux 执行。"
            )
        }
        if (state.fixture) item { InfoCard("测试数据", "当前是显式 CI fixture，不代表真实设备或 Core 成功状态。") }
    }
}

@Composable
private fun ActivityPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader("活动与调用", state.liveStatus) }
        item { SectionTitle("活动流") }
        if (state.snapshot.activity.isEmpty()) item { EmptyCard("暂无活动事件或活动端点不可用") }
        lazyItems(state.snapshot.activity.take(50), key = { "event-${it.id}" }) { WorkbenchItemCard(it) {} }
        item { SectionTitle("调用") }
        if (state.snapshot.calls.isEmpty()) item { EmptyCard("暂无调用") }
        lazyItems(state.snapshot.calls, key = { "call-${it.id}" }) { item ->
            WorkbenchItemCard(item, selected = item.id == state.selectedCallId) { viewModel.selectCall(item) }
        }
    }
}

@Composable
private fun InsertAndStopPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    val text = state.insertionDraft
    val selected = state.snapshot.conversations.firstOrNull { it.id == state.selectedConversationId }
    val bytes = text.toByteArray(StandardCharsets.UTF_8).size
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader("插入与停止", "插入有效窗 5 分钟；入口和停止资格使用最近 3 分钟；回执等待 30 秒。") }
        if (selected == null) {
            item { EmptyCard("请先选择对话") }
            lazyItems(state.snapshot.conversations, key = { "choose-${it.id}" }) { WorkbenchItemCard(it) { viewModel.selectConversation(it) } }
        } else {
            item { SelectedCard(selected) }
            item {
                OutlinedTextField(
                    value = text,
                    onValueChange = viewModel::setInsertionDraft,
                    modifier = Modifier.fillMaxWidth().testTag("insertion-text"),
                    label = { Text("补充要求") },
                    supportingText = { Text("$bytes / 8192 UTF-8 字节") },
                    minLines = 4
                )
            }
            item {
                HorizontalActions(
                    listOf(
                        "发送插入" to {
                            viewModel.sendInsertion(text)
                        },
                        "停止对话" to viewModel::terminateConversation,
                        "刷新回执" to viewModel::refresh
                    ),
                    enabled = !state.loading && !state.actionBusy
                )
            }
            item { SectionTitle("插入回执") }
            if (state.snapshot.insertions.isEmpty()) item { EmptyCard("当前对话没有插入记录") }
            lazyItems(state.snapshot.insertions, key = { "insertion-${it.id}" }) { insertion ->
                Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        Text(insertion.title, maxLines = 3, overflow = TextOverflow.Ellipsis)
                        Text(insertion.status.ifBlank { "状态未知" })
                        Row {
                            TextButton(onClick = { viewModel.insertionAction(insertion.id, "retry") }) { Text("有限重投") }
                            TextButton(onClick = { viewModel.insertionAction(insertion.id, "cancel") }) { Text("撤回") }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun ApprovalsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    val selected = state.snapshot.approvals.firstOrNull { it.id == state.selectedApprovalId }
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader("审批", "审批绑定 CallID；never 表示需要审批的操作被拒绝，不等于完全授权。") }
        if (selected != null) {
            item {
                SelectedCard(selected) {
                    HorizontalActions(
                        listOf(
                            "仅本次批准" to { viewModel.approvalAction(true, false) },
                            "工作区批准" to { viewModel.approvalAction(true, true) },
                            "拒绝" to { viewModel.approvalAction(false) }
                        )
                    )
                }
            }
        }
        if (state.snapshot.approvals.isEmpty()) item { EmptyCard("当前没有待处理审批") }
        lazyItems(state.snapshot.approvals, key = { "approval-${it.id}" }) { item ->
            WorkbenchItemCard(item, selected = item.id == state.selectedApprovalId) { viewModel.selectApproval(item) }
        }
    }
}

@Composable
private fun PermissionsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    val settings = state.settings
    Column(modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        PageHeader("权限", "新安装的完全权限默认值由 Core/安装边界提供；Android 不覆盖已有安装。")
        InfoCard("Core 有效权限", state.snapshot.effectivePermissionSummary)
        SettingSwitch(
            "启用自定义权限设置",
            "关闭时保持 Core 当前有效权限；开启后才展开并允许保存以下字段。",
            settings.customPermissionEnabled
        ) { value -> viewModel.updateSettings { it.copy(customPermissionEnabled = value) } }
        if (settings.customPermissionEnabled) {
            ChoiceSetting("文件系统", listOf("deny" to "拒绝", "read" to "只读", "write" to "读写"), settings.permissionFilesystem) {
                viewModel.updateSettings { s -> s.copy(permissionFilesystem = it) }
            }
            ChoiceSetting("网络", listOf("deny" to "拒绝", "allow" to "允许"), settings.permissionNetwork) {
                viewModel.updateSettings { s -> s.copy(permissionNetwork = it) }
            }
            ChoiceSetting("沙箱边界", listOf("none" to "无额外边界", "workspace" to "工作区", "strict" to "严格"), settings.permissionBoundary) {
                viewModel.updateSettings { s -> s.copy(permissionBoundary = it) }
            }
            ChoiceSetting("审批策略", listOf("on-request" to "按需", "never" to "拒绝需审批操作", "granular" to "分类"), settings.approvalPolicy) {
                viewModel.updateSettings { s -> s.copy(approvalPolicy = it) }
            }
            ChoiceSetting("审批者", listOf("user" to "用户", "auto_review" to "自动审查"), settings.approvalReviewer) {
                viewModel.updateSettings { s -> s.copy(approvalReviewer = it) }
            }
            if (settings.approvalPolicy == "granular") {
                SectionTitle("分类审批")
                CheckSetting("文件写入", settings.granularFileWrites) { viewModel.updateSettings { s -> s.copy(granularFileWrites = it) } }
                CheckSetting("命令", settings.granularCommands) { viewModel.updateSettings { s -> s.copy(granularCommands = it) } }
                CheckSetting("网络", settings.granularNetwork) { viewModel.updateSettings { s -> s.copy(granularNetwork = it) } }
                CheckSetting("MCP", settings.granularMcp) { viewModel.updateSettings { s -> s.copy(granularMcp = it) } }
                CheckSetting("管理操作", settings.granularManagement) { viewModel.updateSettings { s -> s.copy(granularManagement = it) } }
                CheckSetting("其他", settings.granularOther) { viewModel.updateSettings { s -> s.copy(granularOther = it) } }
            }
        }
        Button(onClick = viewModel::savePermissions, enabled = !state.actionBusy, modifier = Modifier.testTag("save-permissions")) { Text("按 Core 修订号保存") }
    }
}

@Composable
private fun SkillsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    CapabilityPage(modifier, "Skill", "Skill 清单来自 Core；本客户端不会直接修改 Skill 文件。", state.snapshot.skills, "skills", viewModel)
}

@Composable
private fun PluginsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader("插件与 MCP", "库存读取已接入；写操作由共享控制接口支持情况决定，失败会原样显示。") }
        item { SectionTitle("插件") }
        if (state.snapshot.plugins.isEmpty()) item { EmptyCard("未返回插件库存") }
        lazyItems(state.snapshot.plugins, key = { "plugin-${it.id}" }) { item -> CapabilityCard(item, "plugins", viewModel) }
        item { SectionTitle("MCP") }
        if (state.snapshot.mcpServers.isEmpty()) item { EmptyCard("未返回 MCP 服务") }
        lazyItems(state.snapshot.mcpServers, key = { "mcp-${it.id}" }) { item -> CapabilityCard(item, "mcp", viewModel) }
    }
}

@Composable
private fun ConnectionsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    var endpoint by remember(state.settings.endpoint) { mutableStateOf(state.settings.endpoint) }
    var bearer by remember { mutableStateOf("") }
    var remote by remember(state.settings.remoteEndpointEnabled) { mutableStateOf(state.settings.remoteEndpointEnabled) }
    Column(modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        PageHeader("Core 与连接", "默认仅允许 loopback 明文；远程节点必须显式启用并使用 HTTPS。")
        OutlinedTextField(endpoint, { endpoint = it }, label = { Text("Core Origin") }, modifier = Modifier.fillMaxWidth().testTag("core-endpoint"), singleLine = true)
        SettingSwitch("启用远程 Core", "关闭时只允许 localhost / 127.0.0.1。", remote) { remote = it }
        OutlinedTextField(
            bearer,
            { bearer = it },
            label = { Text("Bearer（同节点留空保留）") },
            modifier = Modifier.fillMaxWidth().testTag("core-bearer"),
            singleLine = true,
            visualTransformation = PasswordVisualTransformation()
        )
        HorizontalActions(
            listOf(
                "保存并连接" to { viewModel.saveConnection(endpoint, remote, bearer); bearer = "" },
                "删除 Bearer" to viewModel::clearBearer,
                "刷新" to viewModel::refresh
            )
        )
        InfoCard("连接状态", state.snapshot.connectionMessage)
        InfoCard("凭据存储", "凭据只在保存时绑定的 scheme、host 和 port 使用。切换节点或旧凭据尚未绑定时需重新配置；不会把原节点 Bearer 发送到新地址。密文保存在 Android Keystore 包封的应用私有存储。")
    }
}

@Composable
private fun ProjectsFilesPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    val projectPicker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri -> if (uri != null) viewModel.saveTreeUri("project", uri) }
    val artifactPicker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri -> if (uri != null) viewModel.saveTreeUri("artifact", uri) }
    Column(modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        PageHeader("项目与文件", "Android 使用系统 SAF 授权目录；不会扫描整个共享存储或绕过作用域存储。")
        InfoCard("项目目录", state.settings.projectTreeUri.ifBlank { "尚未授权" })
        Button(onClick = { projectPicker.launch(null) }) { Text("选择项目目录") }
        InfoCard("候选产物目录", state.settings.artifactTreeUri.ifBlank { "尚未授权" })
        Button(onClick = { artifactPicker.launch(null) }) { Text("选择候选产物目录") }
        InfoCard("边界", "Core 的工作区仍由 Core 管理；SAF URI 仅供 Android 明确选择的导入、导出和查看流程使用。")
    }
}

@Composable
private fun LogsDiagnosticsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader("日志与诊断", "敏感值不展示；Termux 诊断包保留在其私有目录，需用户明确导出。") }
        item { InfoCard("实时流", state.liveStatus) }
        item {
            HorizontalActions(
                listOf(
                    "刷新快照" to viewModel::refresh,
                    "生成 Termux 诊断包" to { viewModel.runTermux("export_diagnostics") }
                )
            )
        }
        item { SectionTitle("调用摘要") }
        lazyItems(state.snapshot.calls.take(30), key = { "diag-call-${it.id}" }) { WorkbenchItemCard(it) { viewModel.selectCall(it) } }
        item { SectionTitle("Termux 操作摘要") }
        lazyItems(state.operations.take(30), key = { "diag-op-${it.operationId}" }) { OperationCard(it) }
    }
}

@Composable
private fun SettingsPage(state: WorkbenchUiState, viewModel: WorkbenchViewModel, modifier: Modifier) {
    val settings = state.settings
    val notificationPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { }
    Column(modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        PageHeader("设置", "安卓独有设置与 Workbench 通用显示设置统一管理。")
        ChoiceSetting("主题", listOf("system" to "跟随系统", "light" to "浅色", "dark" to "深色"), settings.theme) {
            viewModel.updateSettings { s -> s.copy(theme = it) }
        }
        ChoiceSetting("语言", listOf("system" to "跟随系统", "zh-CN" to "简体中文", "en" to "English"), settings.language) {
            viewModel.updateSettings { s -> s.copy(language = it) }
        }
        ChoiceSetting("密度", listOf("comfortable" to "舒适", "compact" to "紧凑"), settings.density) {
            viewModel.updateSettings { s -> s.copy(density = it) }
        }
        SectionTitle("通知与守护")
        SettingSwitch("通知", "Android 13 及以上需要系统通知权限。", settings.notificationsEnabled) { enabled ->
            viewModel.updateSettings { it.copy(notificationsEnabled = enabled) }
            if (enabled && Build.VERSION.SDK_INT >= 33) notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        SettingSwitch("启用节点守护", "由用户显式启用的可见前台服务；关闭不会停止 Core。", settings.guardianEnabled, viewModel::setGuardianEnabled)
        SettingSwitch("暂停守护", "仅暂停健康检查和受限恢复。", settings.guardianPaused, viewModel::setGuardianPaused)
        SettingSwitch("允许受限自动恢复", "只在 desired=running 时重启当前版本；不会自动更新。", settings.autoRepairEnabled) {
            viewModel.updateSettings { s -> s.copy(autoRepairEnabled = it) }
        }
        SettingSwitch("开机健康检查", "通过 WorkManager 延迟检查；不会从开机广播强启前台服务。", settings.bootHealthCheckEnabled) {
            viewModel.updateSettings { s -> s.copy(bootHealthCheckEnabled = it) }
        }
        Text("检查间隔：${settings.guardianIntervalMinutes} 分钟（最短 15 分钟）")
        Slider(
            value = settings.guardianIntervalMinutes.toFloat(),
            onValueChange = { value -> viewModel.updateSettings { it.copy(guardianIntervalMinutes = value.toInt().coerceIn(15, 1440)) } },
            valueRange = 15f..1440f
        )
        SettingSwitch("仅 Wi-Fi", "用于需要网络的后台约束。", settings.onlyOnWifi) { viewModel.updateSettings { s -> s.copy(onlyOnWifi = it) } }
        SettingSwitch("仅充电时", "限制后台健康检查。", settings.onlyWhileCharging) { viewModel.updateSettings { s -> s.copy(onlyWhileCharging = it) } }
        SettingSwitch("允许移动数据", "仅影响用户发起的下载/更新策略。", settings.allowMobileData) { viewModel.updateSettings { s -> s.copy(allowMobileData = it) } }
        SettingSwitch("公网访问", "默认关闭；仅显示和提交显式配置，不自动创建隧道。", settings.publicAccessEnabled) { viewModel.updateSettings { s -> s.copy(publicAccessEnabled = it) } }
        SectionTitle("显示与日志")
        SettingSwitch("显示详细调用", "展开 RPC 耗时、原始状态和截断信息。", settings.detailedCalls) { viewModel.updateSettings { s -> s.copy(detailedCalls = it) } }
        SettingSwitch("显示工具输出", "输出仍受 Core 和客户端双重上限约束。", settings.toolOutputEnabled) { viewModel.updateSettings { s -> s.copy(toolOutputEnabled = it) } }
        Text("工具输出上限：${settings.toolOutputMaxChars} 字符")
        Slider(
            value = settings.toolOutputMaxChars.toFloat(),
            onValueChange = { value -> viewModel.updateSettings { it.copy(toolOutputMaxChars = value.toInt().coerceIn(1_000, 100_000)) } },
            valueRange = 1_000f..100_000f,
            steps = 98
        )
        InfoCard("候选构建", state.buildIdentity)
    }
}

@Composable
private fun CapabilityPage(
    modifier: Modifier,
    title: String,
    subtitle: String,
    items: List<WorkbenchItem>,
    kind: String,
    viewModel: WorkbenchViewModel
) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader(title, subtitle) }
        if (items.isEmpty()) item { EmptyCard("Core 未返回 $title 库存") }
        lazyItems(items, key = { "$kind-${it.id}" }) { item -> CapabilityCard(item, kind, viewModel) }
    }
}

@Composable
private fun CapabilityCard(item: WorkbenchItem, kind: String, viewModel: WorkbenchViewModel) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(item.title, fontWeight = FontWeight.SemiBold)
            if (item.subtitle.isNotBlank()) Text(item.subtitle)
            Text(item.status.ifBlank { "状态由 Core 决定" })
            Row {
                TextButton(onClick = { viewModel.capabilityAction(kind, item, true) }) { Text("启用") }
                TextButton(onClick = { viewModel.capabilityAction(kind, item, false) }) { Text("停用") }
            }
        }
    }
}

@Composable
private fun ItemListPage(
    modifier: Modifier,
    title: String,
    subtitle: String,
    items: List<WorkbenchItem>,
    empty: String,
    onClick: (WorkbenchItem) -> Unit
) {
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item { PageHeader(title, subtitle) }
        if (items.isEmpty()) item { EmptyCard(empty) }
        lazyItems(items, key = { it.id }) { item -> WorkbenchItemCard(item) { onClick(item) } }
    }
}

@Composable
private fun WorkbenchItemCard(item: WorkbenchItem, selected: Boolean = false, onClick: () -> Unit) {
    ElevatedCard(
        modifier = Modifier.fillMaxWidth().clickable(onClick = onClick),
    ) {
        ListItem(
            headlineContent = { Text(item.title, maxLines = 2, overflow = TextOverflow.Ellipsis) },
            supportingContent = {
                Column {
                    if (item.subtitle.isNotBlank()) Text(item.subtitle, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    if (item.metadata.isNotBlank()) Text(item.metadata, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            },
            trailingContent = { Text(if (selected) "已选" else item.status.ifBlank { "—" }) }
        )
    }
}

@Composable
private fun SelectedCard(item: WorkbenchItem, content: @Composable (() -> Unit)? = null) {
    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(item.title, style = MaterialTheme.typography.titleMedium)
            Text(item.id, style = MaterialTheme.typography.bodySmall)
            if (item.subtitle.isNotBlank()) Text(item.subtitle)
            if (item.status.isNotBlank()) Text("状态：${item.status}")
            content?.invoke()
        }
    }
}

@Composable
private fun RawJsonCard(item: WorkbenchItem) {
    val raw = remember(item.raw) {
        runCatching { item.raw?.toString(2).orEmpty().take(20_000) }.getOrElse { "无法格式化原始记录" }
    }
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp)) {
            Text("Core 原始记录", fontWeight = FontWeight.SemiBold)
            Text(raw.ifBlank { "无原始记录" })
        }
    }
}

@Composable
private fun OperationCard(operation: BridgeOperation) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Text("${operation.operation} · ${operation.phase}", fontWeight = FontWeight.SemiBold)
            Text(operation.operationId, style = MaterialTheme.typography.bodySmall)
            if (operation.message.isNotBlank()) Text(operation.message)
            if (operation.stdoutTruncated || operation.stderrTruncated) Text("Termux 输出已截断")
        }
    }
}

@Composable
private fun OperationButtons(operations: List<String>, viewModel: WorkbenchViewModel) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        operations.chunked(3).forEach { row ->
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                row.forEach { operation ->
                    OutlinedButton(onClick = { viewModel.runTermux(operation) }, modifier = Modifier.weight(1f).testTag("termux-$operation")) {
                        Text(operation.replace('_', ' '), maxLines = 1)
                    }
                }
                repeat(3 - row.size) { Spacer(Modifier.weight(1f)) }
            }
        }
    }
}

@Composable
private fun PageHeader(title: String, subtitle: String) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(title, style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.SemiBold)
        Text(subtitle, style = MaterialTheme.typography.bodyMedium)
        HorizontalDivider(Modifier.padding(top = 4.dp))
    }
}

@Composable
private fun SectionTitle(value: String) {
    Text(value, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
}

@Composable
private fun EmptyCard(message: String) {
    Card(Modifier.fillMaxWidth()) { Text(message, Modifier.padding(16.dp)) }
}

@Composable
private fun InfoCard(title: String, body: String) {
    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(title, fontWeight = FontWeight.SemiBold)
            Text(body)
        }
    }
}

@Composable
private fun MetricRow(vararg values: Pair<String, Int>) {
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        values.forEach { (label, value) ->
            ElevatedCard(Modifier.width(112.dp)) {
                Column(Modifier.padding(12.dp)) {
                    Text(value.toString(), style = MaterialTheme.typography.headlineMedium)
                    Text(label)
                }
            }
        }
    }
}

@Composable
private fun HorizontalActions(actions: List<Pair<String, () -> Unit>>, enabled: Boolean = true) {
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        actions.forEach { (label, action) -> OutlinedButton(onClick = action, enabled = enabled) { Text(label) } }
    }
}

@Composable
private fun SettingSwitch(title: String, description: String, checked: Boolean, onChecked: (Boolean) -> Unit) {
    Card(Modifier.fillMaxWidth()) {
        Row(Modifier.fillMaxWidth().padding(12.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Column(Modifier.weight(1f)) {
                Text(title, fontWeight = FontWeight.SemiBold)
                Text(description, style = MaterialTheme.typography.bodySmall)
            }
            Switch(checked, onChecked)
        }
    }
}

@Composable
private fun ChoiceSetting(title: String, options: List<Pair<String, String>>, selected: String, onSelected: (String) -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(title, fontWeight = FontWeight.SemiBold)
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            options.forEach { (value, label) ->
                FilterChip(selected = selected == value, onClick = { onSelected(value) }, label = { Text(label) })
            }
        }
    }
}

@Composable
private fun CheckSetting(title: String, checked: Boolean, onChecked: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().clickable { onChecked(!checked) }, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Checkbox(checked, onChecked)
        Text(title, Modifier.padding(top = 12.dp))
    }
}
