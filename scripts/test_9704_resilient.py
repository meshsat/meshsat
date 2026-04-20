import serial
import time
import json
import os
import fcntl

PORT = "/dev/ttyUSB0"
BAUD = 230400

RX_TIMEOUT = 10
HEALTH_INTERVAL = 5
MO_TEST = False

USBDEVFS_RESET = 21780


def usb_reset(dev_path="/dev/bus/usb"):
    for root, _, files in os.walk(dev_path):
        for f in files:
            path = os.path.join(root, f)
            try:
                fd = os.open(path, os.O_WRONLY)
                fcntl.ioctl(fd, USBDEVFS_RESET, 0)
                os.close(fd)
                print(f"[USB] Reset triggered on {path}")
                return
            except Exception:
                continue
    print("[USB] Reset failed (device not found)")


def open_serial():
    return serial.Serial(
        PORT,
        BAUD,
        timeout=1,
        exclusive=True
    )


def send_cmd(ser, cmd):
    ser.reset_input_buffer()
    time.sleep(0.1)
    line = cmd.strip() + "\r"
    ser.write(line.encode())
    time.sleep(0.05)


def read_lines(ser, timeout=2):
    start = time.time()
    buf = b""
    lines = []

    while time.time() - start < timeout:
        n = ser.in_waiting
        if n > 0:
            data = ser.read(n)
            buf += data

            while b'\r' in buf:
                line, buf = buf.split(b'\r', 1)
                lines.append(line.decode(errors="ignore"))
        else:
            time.sleep(0.01)

    return lines


def handshake(ser):
    send_cmd(ser, 'GET apiVersion {}')
    read_lines(ser)

    send_cmd(ser, 'PUT apiVersion {"active_version": {"major": 1, "minor": 7, "patch": 0}}')
    read_lines(ser)

    send_cmd(ser, 'PUT simConfig {"interface": "internal"}')
    read_lines(ser)

    send_cmd(ser, 'PUT operationalState {"state": "active"}')
    read_lines(ser)

    print("[INIT] Handshake complete")


def health_check(ser):
    send_cmd(ser, 'GET constellationState {}')
    lines = read_lines(ser)

    for l in lines:
        if "200 constellationState" in l:
            return True

    return False


def mo_test(ser):
    payload = b"ping"
    b64 = payload.hex()

    send_cmd(ser, f'PUT messageOriginate {{"topic_id": 244, "message_length": {len(payload)}, "request_reference": 1}}')

    start = time.time()
    while time.time() - start < 5:
        lines = read_lines(ser)

        for l in lines:
            if "299 messageOriginateSegment" in l:
                obj = json.loads(l.split(" ", 2)[2])
                obj["data"] = b64
                send_cmd(ser, f'PUT messageOriginateSegment {json.dumps(obj)}')
                return True

    return False


def wait_for_device(port, timeout=15):
    """Wait for serial device to re-appear after USB reset."""
    print(f"[USB] Waiting for {port} to re-enumerate...")
    start = time.time()
    while time.time() - start < timeout:
        if os.path.exists(port):
            time.sleep(1)  # extra settle time
            print(f"[USB] {port} re-appeared after {time.time()-start:.1f}s")
            return True
        time.sleep(0.5)
    print(f"[USB] {port} did not re-appear within {timeout}s")
    return False


def main():
    ser = open_serial()
    handshake(ser)

    last_rx = time.time()
    cycle = 0

    while True:
        lines = read_lines(ser, timeout=1)

        if lines:
            last_rx = time.time()
            for l in lines:
                ts = time.strftime("%H:%M:%S")
                print(f"[{ts}] [RX]", l)

        if time.time() - last_rx > RX_TIMEOUT:
            cycle += 1
            print(f"[ERROR] RX silence detected (cycle {cycle})")
            ser.close()
            usb_reset()
            if not wait_for_device(PORT):
                print("[FATAL] Device gone, exiting")
                return
            ser = open_serial()
            handshake(ser)
            last_rx = time.time()
            continue

        if not health_check(ser):
            print("[ERROR] Control plane failure")

        if MO_TEST:
            if not mo_test(ser):
                print("[ERROR] MO test failed")

        time.sleep(HEALTH_INTERVAL)


if __name__ == "__main__":
    main()
