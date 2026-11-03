# Planned VM3 join, three-node quorum, demotion, kexec and reinstallation.
# No phase-3 success or checkpoint is reported until these operations exist.
def main [run: path] {
    open ($run | path join "phase2.json") | ignore
    error make {msg: "Phase 3 VM3/quorum/kexec acceptance is not implemented"}
}
