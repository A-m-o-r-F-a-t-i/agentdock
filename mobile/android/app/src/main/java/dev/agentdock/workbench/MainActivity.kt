package dev.agentdock.workbench

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.runtime.getValue
import dev.agentdock.workbench.ui.WorkbenchApp
import dev.agentdock.workbench.ui.WorkbenchTheme
import dev.agentdock.workbench.ui.WorkbenchViewModel

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val fixture = intent.getBooleanExtra(EXTRA_FIXTURE, false)
        val viewModel = ViewModelProvider(this, WorkbenchViewModel.Factory(application, fixture))[WorkbenchViewModel::class.java]
        setContent {
            val state by viewModel.state.collectAsStateWithLifecycle()
            WorkbenchTheme(state.settings.theme) {
                WorkbenchApp(state = state, viewModel = viewModel)
            }
        }
    }

    companion object {
        const val EXTRA_FIXTURE = "dev.agentdock.workbench.extra.FIXTURE"
    }
}
