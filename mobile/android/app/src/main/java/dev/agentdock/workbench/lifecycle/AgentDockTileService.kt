package dev.agentdock.workbench.lifecycle

import android.content.Intent
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import androidx.core.content.ContextCompat
import dev.agentdock.workbench.WorkbenchApplication
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

class AgentDockTileService : TileService() {
    private val scope = CoroutineScope(Job() + Dispatchers.IO)

    override fun onStartListening() {
        super.onStartListening()
        refresh()
    }

    override fun onClick() {
        super.onClick()
        scope.launch {
            val graph = (application as WorkbenchApplication).graph
            val settings = graph.settings.current()
            if (!settings.guardianEnabled) {
                graph.settings.update { it.copy(guardianEnabled = true, guardianPaused = false) }
                GuardianScheduler.configure(this@AgentDockTileService, graph.settings.current())
                ContextCompat.startForegroundService(this@AgentDockTileService, Intent(this@AgentDockTileService, GuardianService::class.java))
            } else if (settings.guardianPaused) {
                graph.settings.setGuardianPaused(false)
                GuardianScheduler.configure(this@AgentDockTileService, graph.settings.current())
                ContextCompat.startForegroundService(this@AgentDockTileService, Intent(this@AgentDockTileService, GuardianService::class.java))
            } else {
                graph.settings.setGuardianPaused(true)
                stopService(Intent(this@AgentDockTileService, GuardianService::class.java))
                GuardianScheduler.configure(this@AgentDockTileService, graph.settings.current())
            }
            refresh()
        }
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun refresh() {
        scope.launch {
            val settings = (application as WorkbenchApplication).graph.settings.current()
            qsTile?.apply {
                state = if (settings.guardianEnabled && !settings.guardianPaused) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
                if (android.os.Build.VERSION.SDK_INT >= 29) {
                    subtitle = when {
                        !settings.guardianEnabled -> "未启用"
                        settings.guardianPaused -> "守护已暂停"
                        else -> "守护已启用"
                    }
                }
                updateTile()
            }
        }
    }
}
