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
    // Android ships org.json; only the JVM tests need a copy.
    compileOnly("org.json:json:20240303")
    testImplementation("org.json:json:20240303")
    testImplementation(kotlin("test"))
}

tasks.test {
    useJUnitPlatform()
    // Set by CI to run the interop test against the real Go PC app.
    System.getenv("VOIDBRIDGE_PC_ADDR")?.let { environment("VOIDBRIDGE_PC_ADDR", it) }
    System.getenv("VOIDBRIDGE_PC_CODE")?.let { environment("VOIDBRIDGE_PC_CODE", it) }
}
