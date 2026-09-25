plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

fun productVersion(): String {
    val source = rootProject.file("../../internal/buildinfo/buildinfo.go")
    check(source.isFile) { "Missing product version source: $source" }
    val match = Regex("Version\\s*=\\s*\"([^\"]+)\"").find(source.readText())
        ?: error("Unable to parse AgentDock product version")
    return match.groupValues[1]
}

val productVersion = productVersion()
val androidVersionCode = providers.gradleProperty("agentdockAndroidVersionCode")
    .orElse("1010701").get().toInt()
val candidateSha = providers.gradleProperty("agentdockCandidateSha").orElse("local").get()
val candidateRunId = providers.gradleProperty("agentdockCandidateRunId").orElse("local").get()
val candidateRunAttempt = providers.gradleProperty("agentdockCandidateRunAttempt").orElse("1").get()

android {
    namespace = "dev.agentdock.workbench"
    compileSdk = 37

    defaultConfig {
        applicationId = "dev.agentdock.workbench"
        minSdk = 26
        targetSdk = 37
        versionCode = androidVersionCode
        versionName = productVersion
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        testInstrumentationRunnerArguments["clearPackageData"] = "true"
        vectorDrawables.useSupportLibrary = true

        buildConfigField("String", "PRODUCT_VERSION", "\"$productVersion\"")
        buildConfigField("String", "CANDIDATE_SHA", "\"$candidateSha\"")
        buildConfigField("String", "CANDIDATE_RUN_ID", "\"$candidateRunId\"")
        buildConfigField("String", "CANDIDATE_RUN_ATTEMPT", "\"$candidateRunAttempt\"")
        buildConfigField("String", "SIGNING_LABEL", "\"test-signed-debug\"")
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".candidate"
            isDebuggable = true
        }
        release {
            // Candidate release-shaped APKs intentionally use the Android debug key in CI.
            // They are never publication artifacts and the filename/metadata say test-signed.
            signingConfig = signingConfigs.getByName("debug")
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
        isCoreLibraryDesugaringEnabled = true
    }

    sourceSets["main"].assets.srcDir("../termux")

    packaging {
        resources.excludes += setOf("/META-INF/{AL2.0,LGPL2.1}")
    }

    testOptions {
        unitTests.isIncludeAndroidResources = true
        animationsDisabled = true
        execution = "ANDROIDX_TEST_ORCHESTRATOR"
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2026.09.00")
    implementation(composeBom)
    androidTestImplementation(composeBom)

    implementation("androidx.core:core-ktx:1.17.0")
    implementation("androidx.activity:activity-compose:1.12.3")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.4")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.9.4")
    implementation("androidx.datastore:datastore-preferences:1.1.7")
    implementation("androidx.work:work-runtime-ktx:2.10.5")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.2")
    coreLibraryDesugaring("com.android.tools:desugar_jdk_libs:2.1.5")

    debugImplementation("androidx.compose.ui:ui-tooling")
    debugImplementation("androidx.compose.ui:ui-test-manifest")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.10.2")
    testImplementation("androidx.test:core-ktx:1.7.0")

    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test:rules:1.7.0")
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    androidTestUtil("androidx.test:orchestrator:1.6.1")
}
