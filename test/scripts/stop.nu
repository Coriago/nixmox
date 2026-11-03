# Explicit cleanup also works if the test driver was interrupted.
def main [run: path] {
    let run = ($run | path expand)
    let session = (open --raw ($run | path join "session.pid") | str trim | into int)
    let processes = (^ps -s ($session | into string) -o args= | complete)
    if $processes.exit_code != 0 { return }
    if not ($processes.stdout | str contains $run) {
        error make {msg: "Refusing to stop a session that no longer belongs to this run"}
    }
    # Avoid platform-dependent negative-PID parsing in external kill utilities.
    ^python3 -c 'import os, signal, sys; os.killpg(int(sys.argv[1]), signal.SIGTERM)' ($session | into string)
}
