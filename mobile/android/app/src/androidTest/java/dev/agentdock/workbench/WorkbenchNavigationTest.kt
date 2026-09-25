package dev.agentdock.workbench

import android.content.Intent
import android.graphics.Bitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.agentdock.workbench.model.WorkbenchScreen
import org.junit.After
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

@RunWith(AndroidJUnit4::class)
class WorkbenchNavigationTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private lateinit var scenario: ActivityScenario<MainActivity>

    @Before
    fun launchFixture() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val intent = Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            .putExtra(MainActivity.EXTRA_FIXTURE, true)
        scenario = ActivityScenario.launch(intent)
        compose.waitForIdle()
    }

    @After
    fun close() {
        scenario.close()
    }

    @Test
    fun everyWorkbenchPageIsReachableOnCompactLayout() {
        WorkbenchScreen.entries.forEach { screen ->
            compose.onNodeWithTag("open-navigation").performClick()
            compose.onNodeWithTag("nav-${screen.route}").performScrollTo().performClick()
            compose.onNodeWithTag("screen-${screen.route}").assertIsDisplayed()
        }
    }

    @Test
    fun captureKeyPages() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val directory = File(context.getExternalFilesDir(null), "screenshots").apply { mkdirs() }
        capture(directory, WorkbenchScreen.Home)
        listOf(
            WorkbenchScreen.Tasks,
            WorkbenchScreen.Permissions,
            WorkbenchScreen.InstallUpdate,
            WorkbenchScreen.Settings
        ).forEach { screen ->
            compose.onNodeWithTag("open-navigation").performClick()
            compose.onNodeWithTag("nav-${screen.route}").performScrollTo().performClick()
            compose.onNodeWithTag("screen-${screen.route}").assertIsDisplayed()
            capture(directory, screen)
        }
    }

    private fun capture(directory: File, screen: WorkbenchScreen) {
        compose.waitForIdle()
        val bitmap = compose.onNodeWithTag("screen-${screen.route}").captureToImage().asAndroidBitmap()
        File(directory, "${screen.route}.png").outputStream().use { output ->
            check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, output))
        }
    }
}
