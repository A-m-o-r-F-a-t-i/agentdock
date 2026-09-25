package dev.agentdock.workbench.lifecycle

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import dev.agentdock.workbench.WorkbenchApplication
import dev.agentdock.workbench.model.NodeHealth
import org.json.JSONObject

class GuardianWorker(context: Context, parameters: WorkerParameters) : CoroutineWorker(context, parameters) {
    override suspend fun doWork(): Result {
        val graph = (applicationContext as WorkbenchApplication).graph
        val settings = graph.settings.current()
        if (!settings.guardianEnabled || settings.guardianPaused) return Result.success()
        val snapshot = runCatching { graph.repository.refresh() }.getOrNull()
        if (snapshot?.coreHealth == NodeHealth.Healthy) return Result.success()
        if (settings.desiredNodeState != "running" || !settings.autoRepairEnabled) return Result.success()
        return runCatching {
            graph.termux.dispatch("guardian_check", JSONObject().put("source", "work_manager"))
            Result.success()
        }.getOrElse { Result.retry() }
    }
}
