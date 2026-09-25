plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.theformofvoid.voidbridge"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.theformofvoid.voidbridge"
        minSdk = 26
        targetSdk = 35
        versionCode = (System.getenv("VOIDBRIDGE_VERSION_CODE") ?: "1").toInt()
        versionName = System.getenv("VOIDBRIDGE_VERSION_NAME") ?: "0.1.0-dev"
    }

    // CI signs releases with a stable key when the VOIDBRIDGE_KEYSTORE* secrets
    // are set, so new versions install over old ones. Without them the build
    // falls back to the debug key.
    val keystore = System.getenv("VOIDBRIDGE_KEYSTORE_PATH")
    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = file(keystore)
                storePassword = System.getenv("VOIDBRIDGE_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("VOIDBRIDGE_KEY_ALIAS")
                keyPassword = System.getenv("VOIDBRIDGE_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

kotlin {
    compilerOptions.jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
}

dependencies {
    implementation(project(":core"))
}
