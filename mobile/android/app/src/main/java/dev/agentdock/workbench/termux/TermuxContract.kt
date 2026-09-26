package dev.agentdock.workbench.termux

import android.content.ComponentName

object TermuxContract {
    const val PACKAGE = "com.termux"
    const val PERMISSION_RUN_COMMAND = "com.termux.permission.RUN_COMMAND"
    val RUN_COMMAND_COMPONENT = ComponentName(PACKAGE, "com.termux.app.RunCommandService")
    const val ACTION_RUN_COMMAND = "com.termux.RUN_COMMAND"
    const val EXTRA_COMMAND_PATH = "com.termux.RUN_COMMAND_PATH"
    const val EXTRA_ARGUMENTS = "com.termux.RUN_COMMAND_ARGUMENTS"
    const val EXTRA_STDIN = "com.termux.RUN_COMMAND_STDIN"
    const val EXTRA_WORKDIR = "com.termux.RUN_COMMAND_WORKDIR"
    const val EXTRA_BACKGROUND = "com.termux.RUN_COMMAND_BACKGROUND"
    const val EXTRA_PENDING_INTENT = "com.termux.RUN_COMMAND_PENDING_INTENT"
    const val EXTRA_RESULT_BUNDLE = "result"
    const val RESULT_STDOUT = "stdout"
    const val RESULT_STDOUT_ORIGINAL_LENGTH = "stdout_original_length"
    const val RESULT_STDERR = "stderr"
    const val RESULT_STDERR_ORIGINAL_LENGTH = "stderr_original_length"
    const val RESULT_EXIT_CODE = "exitCode"
    const val RESULT_ERR = "err"
    const val RESULT_ERRMSG = "errmsg"

    const val COMMAND = "/data/data/com.termux/files/home/.termux/tasker/agentdock-workbench"
    const val WORKDIR = "/data/data/com.termux/files/home"
    const val CALLBACK_ACTION_PREFIX = "dev.agentdock.workbench.TERMUX_RESULT."
    const val MAX_PAYLOAD_BYTES = 64 * 1024
    const val MAX_RESULT_CHARS = 64 * 1024

    val OPERATIONS = setOf(
        "probe", "bootstrap", "install", "adopt", "status", "start", "stop", "restart",
        "repair", "guardian_check", "update", "rollback", "export_diagnostics",
        "resume", "cancel_operation", "operation_query", "configure", "logs",
        "diagnostic_preview", "cleanup_preview", "cleanup", "path_probe", "project_create"
    )
}
