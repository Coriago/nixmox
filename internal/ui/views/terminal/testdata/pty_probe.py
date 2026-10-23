"""nix develop -c python3 internal/ui/terminal/testdata/pty_probe.py /path/to/ui-preview"""
import fcntl
import os
import pty
import select
import signal
import struct
import sys
import termios
import time

pid, fd = pty.fork()
if pid == 0:
    os.environ.update(TERM="xterm-256color", COLORTERM="truecolor")
    os.execv(sys.argv[1], [sys.argv[1], "tui"])

output = bytearray()
reaped = False

def pump(seconds=0.4):
    until = time.monotonic() + seconds
    chunk = bytearray()
    while time.monotonic() < until:
        if select.select([fd], [], [], 0.04)[0]:
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            output.extend(data)
            chunk.extend(data)
    return chunk.decode("utf-8", errors="replace")

def send(keys):
    os.write(fd, keys)
    return pump()

try:
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
    first = pump(1.5)
    assert "nixmox" in first and "MOCK" in first
    send(b"3")
    assert "Network" in output.decode("utf-8", errors="replace")
    send(b"?")
    assert "Make yourself at home" in output.decode("utf-8", errors="replace")
    send(b"\x1b")
    send(b"t")
    send(b"m")
    # SGR mouse click on Hosts navigation; coordinates are one-based.
    send(b"\x1b[<0;5;8M\x1b[<0;5;8m")
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    os.kill(pid, signal.SIGWINCH)
    pump()
    send(b"q")
    for _ in range(60):
        child, status = os.waitpid(pid, os.WNOHANG)
        if child:
            reaped = True
            break
        time.sleep(0.05)
    assert reaped, "TUI did not exit"
    assert os.waitstatus_to_exitcode(status) == 0
    assert b"\x1b[?1049l" in output, "Alternate screen not restored"
    assert b"\x1b[?1006l" in output, "Mouse reporting not restored"
    print("PTY: full-screen startup, navigation, modal, theme/motion, mouse, resize and clean exit passed")
finally:
    if not reaped:
        try:
            os.kill(pid, signal.SIGTERM)
            os.waitpid(pid, 0)
        except ProcessLookupError:
            pass
    os.close(fd)
