package dev.agentdock.workbench.lifecycle

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import dev.agentdock.workbench.WorkbenchApplication
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import org.json.JSONObject

class GuardianActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val result = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                val graph = (context.applicationContext as WorkbenchApplication).graph
                when (intent.action) {
                    ACTION_PAUSE -> {
                        graph.settings.setGuardianPaused(true)
                        context.stopService(Intent(context, GuardianService::class.java))
                        GuardianScheduler.configure(context, graph.settings.current())
                    }
                    ACTION_RESUME -> {
                        graph.settings.setGuardianPaused(false)
                        val settings = graph.settings.current()
                        GuardianScheduler.configure(context, settings)
                        if (settings.guardianEnabled) ContextCompat.startForegroundService(context, Intent(context, GuardianService::class.java))
                    }
                    ACTION_STOP_CORE -> {
                        // Persist first: guardian pause and Core stop are distinct states.
                        graph.settings.setDesiredNodeState("stopped")
                        graph.termux.dispatch("stop", JSONObject().put("source", "notification"))
                    }
                }
            } finally {
                result.finish()
            }
        }
    }

    companion object {
        const val ACTION_PAUSE = "dev.agentdock.workbench.guardian.PAUSE"
        const val ACTION_RESUME = "dev.agentdock.workbench.guardian.RESUME"
        const val ACTION_STOP_CORE = "dev.agentdock.workbench.guardian.STOP_CORE"
    }
}
