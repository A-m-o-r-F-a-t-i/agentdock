package dev.agentdock.workbench.lifecycle

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import dev.agentdock.workbench.MainActivity
import dev.agentdock.workbench.R
import dev.agentdock.workbench.WorkbenchApplication
import dev.agentdock.workbench.model.NodeHealth
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONObject

class GuardianService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var loop: Job? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(NOTIFICATION_ID, notification("正在检查本地节点", paused = false))
        loop?.cancel()
        loop = scope.launch { runLoop() }
        return START_STICKY
    }

    override fun onDestroy() {
        loop?.cancel()
        scope.cancel()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private suspend fun runLoop() {
        val graph = (application as WorkbenchApplication).graph
        while (scope.isActive) {
            val settings = graph.settings.current()
            if (!settings.guardianEnabled || settings.guardianPaused) {
                updateNotification(notification(getString(R.string.guardian_paused), paused = true))
                stopSelf()
                return
            }
            val snapshot = runCatching { graph.repository.refresh() }.getOrNull()
            val healthy = snapshot?.coreHealth == NodeHealth.Healthy
            val message = when {
                healthy -> "Core ${snapshot?.coreVersion.orEmpty()} 正常"
                settings.desiredNodeState == "stopped" -> "Core 已按用户期望停止"
                else -> snapshot?.connectionMessage ?: "Core 状态不可用"
            }
            updateNotification(notification(message, paused = false))
            if (!healthy && settings.autoRepairEnabled && settings.desiredNodeState == "running") {
                runCatching { graph.termux.dispatch("guardian_check", JSONObject().put("source", "foreground_guardian")) }
            }
            delay(settings.guardianIntervalMinutes.coerceIn(15, 1440) * 60_000L)
        }
    }

    private fun notification(message: String, paused: Boolean): Notification {
        val open = PendingIntent.getActivity(
            this, 1, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val pauseAction = Intent(this, GuardianActionReceiver::class.java).setAction(
            if (paused) GuardianActionReceiver.ACTION_RESUME else GuardianActionReceiver.ACTION_PAUSE
        )
        val stopAction = Intent(this, GuardianActionReceiver::class.java).setAction(GuardianActionReceiver.ACTION_STOP_CORE)
        val pause = PendingIntent.getBroadcast(this, 2, pauseAction, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getBroadcast(this, 3, stopAction, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_agentdock)
            .setContentTitle(getString(R.string.guardian_running))
            .setContentText(message)
            .setContentIntent(open)
            .setOngoing(!paused)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .addAction(0, if (paused) getString(R.string.action_resume) else getString(R.string.action_pause), pause)
            .addAction(0, getString(R.string.action_stop), stop)
            .build()
    }

    private fun updateNotification(value: Notification) {
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, value)
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            getSystemService(NotificationManager::class.java).createNotificationChannel(
                NotificationChannel(CHANNEL_ID, getString(R.string.guardian_channel), NotificationManager.IMPORTANCE_LOW).apply {
                    description = getString(R.string.guardian_channel_description)
                    setShowBadge(false)
                }
            )
        }
    }

    companion object {
        const val CHANNEL_ID = "agentdock_guardian"
        const val NOTIFICATION_ID = 11707
    }
}
