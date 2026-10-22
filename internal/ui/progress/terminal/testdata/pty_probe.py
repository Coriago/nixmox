"""Run with the flake's python3: pty_probe.py /path/to/progress-preview.
A real PTY checks startup, keyboard dispatch, resize, and terminal restoration.
"""
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
    os.environ.update(TERM="dumb", NO_COLOR="1")
    os.execv(sys.argv[1], [sys.argv[1], "tui"])

output = bytearray()
reaped = False

def pump(seconds):
    until = time.monotonic() + seconds
    chunk = bytearray()
    while time.monotonic() < until:
        if select.select([fd], [], [], 0.05)[0]:
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            output.extend(data)
            chunk.extend(data)
    return chunk.decode("utf-8", errors="replace")

def send(keys):
    os.write(fd, keys)
    return pump(0.3)

try:
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
    first = pump(1.2)
    assert "Building cedar" in first and "DOWNLOADS" in first
    assert "PAUSED DEMO" in send(b"p")
    send(b"j")
    assert "download on cache.nixos.org" in send(b"\r")
    assert "Speed 2" in send(b"s")
    assert "Animate" in send(b"m")
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 90, 0, 0))
    os.kill(pid, signal.SIGWINCH)
    pump(0.3)
    send(b"r")
    send(b"q")
    for _ in range(60):
        child, status = os.waitpid(pid, os.WNOHANG)
        if child:
            reaped = True
            break
        time.sleep(0.05)
    assert reaped, "TUI did not exit"
    assert os.waitstatus_to_exitcode(status) == 0
    assert b"\x1b[?1049l" in output, "alternate screen not restored"
    print("PTY: startup, pause, selection, expansion, speed, motion, resize, reset, exit passed")
finally:
    if not reaped:
        try:
            os.kill(pid, signal.SIGTERM)
            os.waitpid(pid, 0)
        except ProcessLookupError:
            pass
    os.close(fd)
