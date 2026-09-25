// Platform-independent protocol and sync logic, testable on a plain JVM.
plugins {
    id("org.jetbrains.kotlin.jvm")
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

kotlin {
    compilerOptions.jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
}

dependencies {
    api("com.squareup.okhttp3:okhttp:4.12.0")
    // Android ships org.json; only the JVM tests need a copy.
    compileOnly("org.json:json:20240303")
    testImplementation("org.json:json:20240303")
    testImplementation(kotlin("test"))
}

tasks.test {
    useJUnitPlatform()
    // Set by CI (and tools/interop) to test against the real Go implementation.
    listOf("VOIDBRIDGE_INTEROP_PEER", "VOIDBRIDGE_INTEROP_CODE", "VOIDBRIDGE_INTEROP_SERVER").forEach { k ->
        System.getenv(k)?.let { environment(k, it) }
    }
}
