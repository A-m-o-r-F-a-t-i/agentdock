package dev.agentdock.workbench

import android.content.Intent
import android.graphics.Bitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.lifecycle.ViewModelProvider
import androidx.test.core.app.ActivityScenario
import androidx.test.espresso.Espresso.pressBack
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.agentdock.workbench.model.WorkbenchScreen
import dev.agentdock.workbench.ui.WorkbenchViewModel
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

@RunWith(AndroidJUnit4::class)
class WorkbenchNavigationTest {
    @get:Rule val compose = createEmptyComposeRule()
    private lateinit var scenario: ActivityScenario<MainActivity>
    private lateinit var model: WorkbenchViewModel

    @Before fun launchFixture() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        scenario = ActivityScenario.launch(Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK).putExtra(MainActivity.EXTRA_FIXTURE, true))
        scenario.onActivity { model = ViewModelProvider(it)[WorkbenchViewModel::class.java] }
        compose.waitUntil(10000) { !model.state.value.loading && model.state.value.snapshot.fixture }
    }

    @After fun close() { scenario.close() }

    @Test fun everyWorkbenchPageIsReachableOnCompactLayout() {
        WorkbenchScreen.entries.forEach(::navigate)
    }

    @Test fun captureKeyPages() {
        // The orchestrator clears target-app data between cases. Keep evidence in
        // the instrumentation package, which is not the app under test.
        val context = InstrumentationRegistry.getInstrumentation().context
        val directory = File(context.getExternalFilesDir(null), "screenshots").apply { check(mkdirs() || isDirectory) }
        WorkbenchScreen.entries.forEach { screen ->
            navigate(screen)
            capture(directory, screen, screen.route)
        }
        navigate(WorkbenchScreen.Tasks)
        compose.runOnIdle { model.setFixtureScenario("empty") }
        compose.waitUntil(10000) { !model.state.value.loading && model.state.value.snapshot.tasks.isEmpty() }
        compose.onNodeWithTag("tasks-empty").performScrollTo().assertIsDisplayed()
        capture(directory, WorkbenchScreen.Tasks, "tasks-empty")
        compose.runOnIdle { model.setFixtureScenario("error") }
        compose.waitUntil(10000) { !model.state.value.loading && model.state.value.snapshot.errors.containsKey("tasks") }
        compose.onNodeWithTag("resource-error").assertIsDisplayed()
        compose.onNodeWithTag("tasks-empty").assertDoesNotExist()
        capture(directory, WorkbenchScreen.Tasks, "tasks-error")
    }

    @Test fun restoresNavigationAndCommittedFiltersAfterRecreation() {
        navigate(WorkbenchScreen.Tasks)
        compose.onNodeWithTag("tasks-search").performTextInput("回执检查")
        compose.onNodeWithText("应用筛选").performScrollTo().performClick()
        compose.waitUntil(10000) { !model.state.value.loading }
        scenario.recreate()
        scenario.onActivity { model = ViewModelProvider(it)[WorkbenchViewModel::class.java] }
        compose.onNodeWithTag("screen-tasks").assertIsDisplayed()
        compose.onNodeWithTag("tasks-search").assertTextContains("回执检查")
        navigate(WorkbenchScreen.Settings)
        pressBack()
        compose.onNodeWithTag("screen-tasks").assertIsDisplayed()
    }

    @Test fun fixtureNeverDispatchesRealTermuxOrCoreWrites() {
        compose.runOnIdle {
            val before = model.state.value.settings.desiredNodeState
            model.runTermux("start")
            assertEquals(before, model.state.value.settings.desiredNodeState)
            assertFalse(model.state.value.actionBusy)
            assertTrue(model.state.value.message.contains("禁止实际写入"))
            model.manage("tasks", listOf("tsk_android"), "delete", confirmed = true)
            assertNull(model.state.value.batchResult)
        }
    }

    @Test fun draftSurvivesNavigationWithoutSending() {
        navigate(WorkbenchScreen.Conversations)
        compose.runOnIdle { model.selectConversation(model.state.value.snapshot.conversations.first()) }
        navigate(WorkbenchScreen.InsertAndStop)
        compose.onNodeWithTag("insertion-text").performTextInput("保留这条补充要求")
        navigate(WorkbenchScreen.Home)
        navigate(WorkbenchScreen.InsertAndStop)
        compose.onNodeWithTag("insertion-text").assertTextContains("保留这条补充要求")
        scenario.recreate()
        compose.onNodeWithTag("insertion-text").assertTextContains("保留这条补充要求")
    }

    private fun navigate(screen: WorkbenchScreen) {
        compose.onNodeWithTag("open-navigation").performClick()
        compose.onNodeWithTag("nav-${screen.route}").performScrollTo().performClick()
        compose.onNodeWithTag("screen-${screen.route}").assertIsDisplayed()
    }

    private fun capture(directory: File, screen: WorkbenchScreen, name: String) {
        compose.waitForIdle()
        val bitmap = compose.onNodeWithTag("screen-${screen.route}").captureToImage().asAndroidBitmap()
        File(directory, "$name.png").outputStream().use { check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }
}
