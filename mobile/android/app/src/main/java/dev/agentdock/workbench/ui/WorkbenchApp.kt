package dev.agentdock.workbench.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.DrawerValue
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.material3.ModalNavigationDrawer
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationDrawerItem
import androidx.compose.material3.NavigationRail
import androidx.compose.material3.NavigationRailItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import dev.agentdock.workbench.model.WorkbenchScreen
import kotlinx.coroutines.launch

private val compactDestinations = listOf(
    WorkbenchScreen.Home,
    WorkbenchScreen.Tasks,
    WorkbenchScreen.Activity,
    WorkbenchScreen.Settings
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun WorkbenchApp(state: WorkbenchUiState, viewModel: WorkbenchViewModel) {
    val drawerState = androidx.compose.material3.rememberDrawerState(DrawerValue.Closed)
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    val savedPages = rememberSaveableStateHolder()
    BackHandler(drawerState.isOpen || state.screen != WorkbenchScreen.Home) {
        if (drawerState.isOpen) scope.launch { drawerState.close() } else viewModel.back()
    }
    LaunchedEffect(state.message) {
        if (state.message.isNotBlank()) {
            snackbar.showSnackbar(state.message)
            viewModel.clearMessage()
        }
    }

    ModalNavigationDrawer(
        drawerState = drawerState,
        drawerContent = {
            ModalDrawerSheet {
                Column(Modifier.verticalScroll(rememberScrollState())) {
                    Text("AgentDock Workbench", modifier = Modifier.testTag("drawer-title"))
                    WorkbenchScreen.entries.groupBy { it.section }.forEach { (section, screens) ->
                        Text(section)
                        screens.forEach { screen ->
                            NavigationDrawerItem(
                                label = { Text(screen.title) },
                                selected = state.screen == screen,
                                modifier = Modifier.testTag("nav-${screen.route}"),
                                onClick = {
                                    viewModel.navigate(screen)
                                    scope.launch { drawerState.close() }
                                }
                            )
                        }
                    }
                    Text(state.buildIdentity)
                    if (state.fixture) Text("Fixture 模式（仅调试构建）")
                }
            }
        }
    ) {
        BoxWithConstraints(Modifier.fillMaxSize()) {
            val compact = maxWidth < 700.dp
            Scaffold(
                topBar = {
                    TopAppBar(
                        title = { Text(state.screen.title) },
                        navigationIcon = {
                            TextButton(onClick = { scope.launch { drawerState.open() } }, modifier = Modifier.testTag("open-navigation")) {
                                Text("菜单")
                            }
                        },
                        actions = {
                            TextButton(onClick = viewModel::refresh, enabled = !state.loading, modifier = Modifier.testTag("refresh")) {
                                Text("刷新")
                            }
                        }
                    )
                },
                snackbarHost = { SnackbarHost(snackbar) },
                bottomBar = {
                    if (compact) {
                        NavigationBar {
                            compactDestinations.forEach { screen ->
                                NavigationBarItem(
                                    selected = state.screen == screen,
                                    onClick = { viewModel.navigate(screen) },
                                    icon = { Text(screen.title.take(1)) },
                                    label = { Text(screen.title) },
                                    modifier = Modifier.testTag("bottom-${screen.route}")
                                )
                            }
                        }
                    }
                }
            ) { padding ->
                Box(Modifier.fillMaxSize()) {
                    Row(Modifier.fillMaxSize()) {
                        if (!compact) {
                            NavigationRail {
                                compactDestinations.forEach { screen ->
                                    NavigationRailItem(
                                        selected = state.screen == screen,
                                        onClick = { viewModel.navigate(screen) },
                                        icon = { Text(screen.title.take(1)) },
                                        label = { Text(screen.title) },
                                        modifier = Modifier.testTag("rail-${screen.route}")
                                    )
                                }
                            }
                        }
                        savedPages.SaveableStateProvider(state.screen.route) {
                        WorkbenchPage(
                            state = state,
                            viewModel = viewModel,
                            modifier = Modifier.fillMaxSize().testTag("screen-${state.screen.route}"),
                            contentPadding = padding
                        )
                        }
                    }
                    if (state.loading) LinearProgressIndicator()
                }
            }
        }
    }
}
