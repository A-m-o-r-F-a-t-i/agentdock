using System.Globalization;
using System.Windows.Markup;

namespace AgentDock.ControlPanel;

internal static class ActivityText
{
    private static readonly IReadOnlyDictionary<string, (string English, string Chinese)> Text = new Dictionary<string, (string, string)>(StringComparer.Ordinal)
    {
        ["NeedsAttention"] = ("Needs attention", "待处理任务"),
        ["InProgressTasks"] = ("In progress", "进行中任务"),
        ["EndedTasks"] = ("Ended", "已结束任务"),
        ["Records"] = ("Activity records", "活动记录"),
        ["NoStep"] = ("No current step recorded", "尚未记录当前步骤"),
        ["NoReason"] = ("No reason recorded", "尚未记录原因"),
        ["NoNext"] = ("No next action recorded", "尚未记录下一动作"),
        ["CancelReason"] = ("Cancellation reason", "取消原因"),
        ["TaskBlockReason"] = ("Task blocked", "任务阻塞原因"),
        ["ThreadBlockReason"] = ("Branch blocked", "分支阻塞原因"),
        ["NoWorkspace"] = ("No workspace bound", "未绑定工作区"),
        ["WorkspaceMissing"] = ("Bound workspace is missing", "绑定的工作区记录已缺失"),
        ["WorkspaceButton"] = ("Open workspace", "打开工作区"),
        ["Summary"] = ("Checkpoint summary", "阶段摘要"),
        ["CommandDirectory"] = ("Command directory", "命令目录"),
        ["RunningNow"] = ("Running at latest check", "最近检查：运行中"),
        ["TaskCancelledEvent"] = ("Task cancelled", "任务已取消"),
        ["Continue"] = ("Copy continuation instruction", "复制继续指令"),
        ["Retry"] = ("Copy retry instruction", "复制重试指令"),
        ["ContinueInstruction"] = ("Read this task and branch checkpoint, confirm its workspace, then continue the unfinished work. This instruction does not itself start execution.", "读取此任务与分支的检查点，确认绑定工作区后继续未完成工作。复制本指令不会自动启动执行。"),
        ["RetryInstruction"] = ("Keep the original task cancelled. Read its goal and cancellation reason, then create a new task for a retry.", "保留原任务的已取消状态。读取其目标和取消原因，另建新任务重试。"),
        ["ResumeTask"] = ("Unblock task state", "解除任务阻塞"),
        ["ResumeHint"] = ("The state is restored. Continue from the connected client; no model was started.", "状态已恢复，请在已连接的客户端继续执行。此操作不会启动模型。"),
        ["LiveCommands"] = ("Live command sessions · all branches of this task", "实时命令会话 · 当前任务所有分支"),
        ["LiveUnavailable"] = ("Live session state unavailable; stopping is disabled until checked.", "实时会话状态不可用，重新检查前无法停止命令。"),
        ["NoRunning"] = ("No running commands at the latest check. Task progress does not indicate client execution.", "最近检查没有运行命令。任务进度不代表客户端当前执行状态。"),
        ["RunningAfterCancel"] = ("The task has ended, but its command processes are still running.", "任务已结束，但其命令进程仍在运行。"),
        ["UnknownCommand"] = ("Command session · command text not retained", "命令会话 · 命令正文未保留"),
        ["Technical"] = ("Technical details", "技术详情"),
        ["Progress"] = ("Step progress", "步骤进度"),
        ["TaskSteps"] = ("Task steps", "任务步骤"),
        ["BranchSteps"] = ("Selected branch steps", "当前查看分支步骤"),
        ["History"] = ("Activity history", "活动记录"),
        ["ReturnLatest"] = ("Return to latest activity", "返回最新活动"),
        ["NoResults"] = ("No matching tasks", "没有符合筛选条件的任务"),
        ["CategoryAll"] = ("All activity types", "全部活动类型"),
        ["CategoryCommand"] = ("Commands", "命令"),
        ["CategoryFile"] = ("File changes", "文件修改"),
        ["CategoryCheckpoint"] = ("Checkpoints and review", "阶段摘要与验收"),
        ["CategoryLifecycle"] = ("Lifecycle and other events", "生命周期及其他活动"),
        ["LastChecked"] = ("Last check", "最近检查"),
        ["ClipboardReady"] = ("Continuation instruction copied.", "继续指令已复制。"),
        ["DiffCurrentHint"] = ("This compares the current working tree, not the file contents at event time.", "此处比较当前工作树，不代表事件发生时的历史文件内容。"),
        ["Title"] = ("AgentDock · Task activity", "AgentDock · 任务活动中心"),
        ["Open"] = ("Task activity center", "任务活动中心"),
        ["Facts"] = ("Recorded AgentDock execution only. Threads are recoverable contexts, not background AI workers.", "仅展示 AgentDock 已记录的执行事实。线程用于恢复任务，不表示后台 AI 工作进程。"),
        ["Tasks"] = ("Tasks", "任务"),
        ["Search"] = ("Filter by title, project, workspace or task ID", "按标题、项目、工作区或任务 ID 筛选"),
        ["All"] = ("All states", "全部状态"),
        ["Refresh"] = ("Refresh", "刷新"),
        ["Archived"] = ("Include archived", "显示归档任务"),
        ["Thread"] = ("Execution branch", "执行分支"),
        ["ThreadActions"] = ("Branch actions ▾", "分支操作 ▾"),
        ["TaskActions"] = ("Task actions ▾", "任务操作 ▾"),
        ["SetActive"] = ("Use as default continuation branch", "设为默认继续分支"),
        ["Create"] = ("Create thread", "新建线程"),
        ["Fork"] = ("Fork checkpoint", "从检查点分叉"),
        ["Block"] = ("Block thread", "阻塞线程"),
        ["Resume"] = ("Unblock branch state", "解除分支阻塞"),
        ["Close"] = ("Close thread", "关闭线程"),
        ["CancelTask"] = ("Cancel task", "取消任务"),
        ["Archive"] = ("Archive task", "归档任务"),
        ["Unarchive"] = ("Restore from archive", "解除归档"),
        ["Cleanup"] = ("Clean activity older than 30 days", "清理 30 天前的活动日志"),
        ["Stop"] = ("Stop command", "停止命令"),
        ["Copy"] = ("Copy redacted command", "复制脱敏命令"),
        ["Directory"] = ("Open command directory", "打开命令目录"),
        ["File"] = ("Locate modified file", "定位修改文件"),
        ["Diff"] = ("View current diff", "查看当前差异"),
        ["Follow"] = ("Follow latest", "跟随最新活动"),
        ["Output"] = ("Show output", "展开输出"),
        ["OutputTruncated"] = ("Show output · bounded preview, truncated", "展开输出 · 有界预览，已截断"),
        ["Details"] = ("Steps and acceptance", "步骤与验收"),
        ["Conditions"] = ("Acceptance conditions", "验收条件"),
        ["Review"] = ("Final review", "最终复核"),
        ["Risks"] = ("Open risks / missing checks", "剩余风险与未完成检查"),
        ["Workspace"] = ("Workspace", "工作区"),
        ["Step"] = ("Current step", "当前步骤"),
        ["Next"] = ("Next action", "下一动作"),
        ["DefaultThread"] = ("Continuation thread", "默认继续线程"),
        ["NoTask"] = ("Select a task to view its execution timeline.", "选择任务后查看执行时间线。"),
        ["NoEvents"] = ("No activity recorded for this thread yet.", "该线程尚无活动记录。"),
        ["Unassigned"] = ("Unassigned legacy activity", "未绑定任务的历史活动"),
        ["AllActivity"] = ("All recorded activity", "全部执行记录"),
        ["AllActivityDetail"] = ("All tasks and legacy unassigned operations", "所有任务及旧版未绑定操作"),
        ["CancelConfirmation"] = ("There are {0} running commands in this task. Cancelling changes the task state only and will not stop these processes. Cancel this task?", "该任务当前有 {0} 条运行中的命令。取消任务只修改任务状态，不会停止这些进程。是否取消任务？"),
        ["Loading"] = ("Loading local activity…", "正在加载本地活动…"),
        ["Live"] = ("Connected · live events", "已连接 · 实时活动"),
        ["Reconnecting"] = ("Disconnected. Reconnecting to the local service…", "连接中断，正在重连本地服务…"),
        ["Unavailable"] = ("Local activity is unavailable. Check the service connection.", "本地活动暂不可用，请检查服务连接。"),
        ["Gap"] = ("Some older activity was removed by retention. Remaining records are still available.", "较早的活动已按保留策略清理，其余记录仍可查看。"),
        ["Reset"] = ("The local journal changed; the replay cursor has been reset.", "本地活动日志已变化，已重置重放位置。"),
        ["Warning"] = ("Activity warning", "活动记录提示"),
        ["RecentOnly"] = ("Only the latest 1,000 timeline entries are retained in this window.", "窗口只保留最近 1,000 条时间线记录。"),
        ["MoreTasks"] = ("Showing at most 200 recent tasks. Narrow the server-side state filter to see older tasks.", "最多显示最近 200 个任务，可按状态缩小范围查看较早任务。"),
        ["Confirm"] = ("Confirm operation", "确认操作"),
        ["ConfirmAction"] = ("Apply this operation to the selected task/thread? Existing history will be retained.", "将此操作应用于所选任务或线程？已有历史记录将保留。"),
        ["ConfirmStop"] = ("Stop this command session? Unsaved work in that process may be lost.", "停止当前命令会话？该进程中尚未保存的工作可能丢失。"),
        ["ConfirmCleanup"] = ("Remove complete activity segments older than 30 days? Task checkpoints and current output are retained.", "删除 30 天前的完整活动日志分段？任务检查点与当前输出会保留。"),
        ["Name"] = ("Thread title", "线程名称"),
        ["Reason"] = ("Reason", "原因"),
        ["Required"] = ("Enter a non-empty value (at most 512 characters).", "请填写非空内容，最多 512 个字符。"),
        ["OK"] = ("OK", "确定"),
        ["Dismiss"] = ("Cancel", "取消"),
        ["OperationFailed"] = ("The operation did not complete.", "操作未完成。"),
        ["InvalidPath"] = ("The recorded path is missing, redacted, remote, or not a local filesystem path.", "记录中的路径不存在、已脱敏、属于远端环境，或不是本地文件系统路径。"),
        ["DiffTitle"] = ("Current repository diff", "当前仓库差异"),
        ["DiffEmpty"] = ("No current tracked diff for this file.", "该文件当前没有已跟踪差异。"),
        ["Interrupted"] = ("Last recorded running · not confirmed live", "历史记录为运行中 · 实时状态未确认")
    };

    internal static string Get(string key)
    {
        if (!Text.TryGetValue(key, out var value)) return key;
        var culture = CultureInfo.DefaultThreadCurrentUICulture ?? CultureInfo.CurrentUICulture;
        return UiText.NormalizeCultureName(culture.Name) == UiText.SimplifiedChinesePreference ? value.Chinese : value.English;
    }

    internal static string State(string value) => value switch
    {
        "active" => Choose("Active", "进行中"), "blocked" => Choose("Blocked", "已阻塞"),
        "completed" => Choose("Completed", "已完成"), "cancelled" => Choose("Cancelled", "已取消"),
        "archived" => Choose("Archived", "已归档"), "open" => Choose("Open", "开放"),
        "closed" => Choose("Closed", "已关闭"), "success" or "pass" => Choose("Succeeded", "成功"),
        "failed" => Choose("Failed", "失败"), "running" => Choose("Running · last observed", "运行中 · 最后记录"),
        "timeout" => Choose("Timed out", "已超时"), "killed" => Choose("Stopped", "已停止"),
        "pending" => Choose("Pending", "待执行"), "in_progress" => Choose("In progress", "执行中"),
        "partial" => Choose("Partial", "部分完成"), "exited" => Choose("Exited", "已退出"), _ => value
    };

    internal static string Kind(string value) => value switch
    {
        "command.started" or "command.output" or "command.completed" => Choose("Command", "命令"),
        "task.created" => Choose("Task created", "任务已创建"),
        "task.completed" => Choose("Task completed", "任务已完成"),
        "task.blocked" => Choose("Task blocked", "任务已阻塞"),
        "task.resumed" => Choose("Task unblocked", "任务已解除阻塞"),
        "task.archived" => Choose("Task archived", "任务已归档"),
        "task.unarchived" => Choose("Task restored from archive", "任务已解除归档"),
        "thread.created" => Choose("Branch created", "分支已创建"),
        "thread.switched" => Choose("Continuation branch changed", "默认继续分支已更改"),
        "thread.blocked" => Choose("Branch blocked", "分支已阻塞"),
        "thread.resumed" => Choose("Branch unblocked", "分支已解除阻塞"),
        "thread.closed" => Choose("Branch closed", "分支已关闭"),
        "activity.cleaned" => Choose("History cleaned", "历史活动已清理"),
        "file.changed" => Choose("File changed", "文件修改"),
        "step.started" => Choose("Step started", "步骤开始"), "step.summary" => Choose("Checkpoint", "阶段检查点"),
        "tool.started" or "tool.completed" => Choose("Tool call", "工具调用"),
        "review.completed" => Choose("Final review", "最终复核"),
        _ => value
    };

    private static string Choose(string en, string zh) => UiText.NormalizeCultureName((CultureInfo.DefaultThreadCurrentUICulture ?? CultureInfo.CurrentUICulture).Name) == UiText.SimplifiedChinesePreference ? zh : en;
}

[MarkupExtensionReturnType(typeof(string))]
internal sealed class ActivityLocExtension(string key) : MarkupExtension
{
    [ConstructorArgument("key")]
    public string Key { get; set; } = key;
    public override object ProvideValue(IServiceProvider serviceProvider) => ActivityText.Get(Key);
}
