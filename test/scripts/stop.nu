# Stop all sessions owned by a suite, or one individual VM directory.
def stop-session [run: path] {
    let file = ($run | path join "session.pid")
    if not ($file | path exists) { return }
    let session = (open --raw $file | str trim | into int)
    let processes = (^ps -s ($session | into string) -o args= | complete)
    if $processes.exit_code != 0 { return }
    if not ($processes.stdout | str contains $run) {
        error make {msg: $"Refusing to stop an unrelated session: ($session)"}
    }
    do --capture-errors {
        ^python3 -c 'import os, signal, sys; os.killpg(int(sys.argv[1]), signal.SIGTERM)' ($session | into string)
    }
}

def main [run: path] {
    let run = ($run | path expand)
    let failures = ([$run ($run | path join "vm2") ($run | path join "vm3")]
        | each {|directory| try { stop-session $directory; null } catch {|err| $err.msg} }
        | compact)
    if ($failures | is-not-empty) { error make {msg: ($failures | str join "\n")} }
}
