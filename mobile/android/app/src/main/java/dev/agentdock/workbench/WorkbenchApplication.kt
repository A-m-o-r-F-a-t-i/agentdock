package dev.agentdock.workbench

import android.app.Application
import dev.agentdock.workbench.data.CredentialStore
import dev.agentdock.workbench.data.SettingsStore
import dev.agentdock.workbench.data.WorkbenchRepository
import dev.agentdock.workbench.termux.PendingOperationStore
import dev.agentdock.workbench.termux.TermuxCommandDispatcher

class WorkbenchApplication : Application() {
    lateinit var graph: AppGraph
        private set

    override fun onCreate() {
        super.onCreate()
        graph = AppGraph(this)
    }
}

class AppGraph(application: Application) {
    val settings = SettingsStore(application)
    val credentials = CredentialStore(application)
    val operations = PendingOperationStore(application)
    val repository = WorkbenchRepository(application, settings, credentials)
    val termux = TermuxCommandDispatcher(application, operations)
}
