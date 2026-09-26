package dev.agentdock.workbench.lifecycle

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import dev.agentdock.workbench.model.WorkbenchSettings
import java.util.concurrent.TimeUnit

object GuardianScheduler {
    private const val UNIQUE_PERIODIC = "agentdock-guardian-periodic"
    private const val UNIQUE_BOOT = "agentdock-guardian-boot-check"

    fun configure(context: Context, settings: WorkbenchSettings) {
        val manager = WorkManager.getInstance(context)
        if (!settings.guardianEnabled || settings.guardianPaused) {
            manager.cancelUniqueWork(UNIQUE_PERIODIC)
            return
        }
        val request = PeriodicWorkRequestBuilder<GuardianWorker>(
            settings.guardianIntervalMinutes.coerceIn(15, 1440).toLong(), TimeUnit.MINUTES
        ).setConstraints(constraints(settings)).build()
        manager.enqueueUniquePeriodicWork(UNIQUE_PERIODIC, ExistingPeriodicWorkPolicy.UPDATE, request)
    }

    fun enqueueBootCheck(context: Context, settings: WorkbenchSettings) {
        if (!settings.bootHealthCheckEnabled || settings.guardianPaused) return
        val request = OneTimeWorkRequestBuilder<GuardianWorker>()
            .setInitialDelay(30, TimeUnit.SECONDS)
            .setConstraints(constraints(settings))
            .build()
        WorkManager.getInstance(context).enqueueUniqueWork(UNIQUE_BOOT, ExistingWorkPolicy.REPLACE, request)
    }

    private fun constraints(settings: WorkbenchSettings) = Constraints.Builder()
        .setRequiredNetworkType(if (settings.onlyOnWifi) NetworkType.UNMETERED else NetworkType.NOT_REQUIRED)
        .setRequiresCharging(settings.onlyWhileCharging)
        .build()
}
