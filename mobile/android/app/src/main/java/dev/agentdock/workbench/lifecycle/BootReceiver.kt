package dev.agentdock.workbench.lifecycle

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import dev.agentdock.workbench.WorkbenchApplication
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action !in setOf(Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED)) return
        val result = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                val graph = (context.applicationContext as WorkbenchApplication).graph
                val settings = graph.settings.current()
                GuardianScheduler.configure(context, settings)
                GuardianScheduler.enqueueBootCheck(context, settings)
            } finally {
                result.finish()
            }
        }
    }
}
