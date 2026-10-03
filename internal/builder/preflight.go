package builder

import "fmt"

// jvmBuildMinMemory is the container-engine memory below which an on-server
// JVM (Gradle/Maven) build is likely to be OOM-killed. Measured, not guessed:
// a 3.8 GiB Docker Desktop VM (~3.5 GiB reported) already running ~1.3 GiB of
// apps killed a Spring build (exit 137); 6.8 GiB built fine. Docker reports a
// little less than the configured size, so 5 GiB separates the two.
const jvmBuildMinMemory = 5 << 30

// MemoryAdvice is the note to show before an on-server JVM build when the
// container engine has too little memory; "" means nothing to say (enough
// memory, or unknown). It advises and never blocks the build.
func MemoryAdvice(total uint64) string {
	if total == 0 || total >= jvmBuildMinMemory {
		return ""
	}
	return fmt.Sprintf("Heads-up: Docker has %.1f GiB of memory and a Java (Gradle/Maven) build usually needs about 5 GiB — it may be killed with exit 137 / \"cannot allocate memory\". "+
		"Give Docker more memory, or switch this app to Prebuilt mode (Settings → Git → Deploy mode) so GitHub Actions builds the JAR instead.",
		float64(total)/(1<<30))
}
