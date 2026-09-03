#!/usr/bin/env python3
"""Unit tests for meshsat-oob-agent's allowlist. Run from the repo root:

    python3 -m unittest deploy/oob/test_agent.py

They exercise plan() and dispatch() without root and without executing
anything on the host. [MESHSAT-756]
"""

import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load_agent():
    spec = importlib.util.spec_from_loader(
        "meshsat_oob_agent",
        importlib.machinery.SourceFileLoader("meshsat_oob_agent", os.path.join(HERE, "meshsat-oob-agent")),
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


agent = load_agent()


class PlanTests(unittest.TestCase):
    def test_unknown_action_rejected(self):
        with self.assertRaises(agent.Refused):
            agent.plan("shell", {"cmd": "id"})

    def test_ping_reports_version(self):
        argv, _timeout, post = agent.plan("ping", {})
        self.assertIsNone(argv)
        self.assertEqual(post(None), {"version": agent.VERSION})

    def test_reboot_delay_clamped_and_argv_list(self):
        argv, _t, post = agent.plan("reboot", {"delay": 99999})
        self.assertIsInstance(argv, list)
        self.assertIn("--on-active=%ds" % agent.MAX_DELAY, argv)
        self.assertEqual(argv[-2:], ["systemctl", "reboot"])
        self.assertEqual(post(None), {"delay": agent.MAX_DELAY})
        argv, _t, _p = agent.plan("reboot", {"delay": "not-a-number"})
        self.assertIn("--on-active=10s", argv)
        argv, _t, _p = agent.plan("reboot", {"delay": 0})
        self.assertIn("--on-active=1s", argv)

    def test_journal_unit_table_and_lines_clamp(self):
        argv, _t, _p = agent.plan("journal_tail", {"unit": "docker", "lines": 500})
        self.assertEqual(argv[:3], ["journalctl", "-u", "docker.service"])
        self.assertIn(str(agent.MAX_LINES), argv)
        argv, _t, _p = agent.plan("journal_tail", {"unit": "docker", "lines": 0})
        self.assertIn("1", argv)
        with self.assertRaises(agent.Refused):
            agent.plan("journal_tail", {"unit": "sshd", "lines": 5})

    def test_no_shell_interpolation(self):
        evil = "docker; rm -rf /"
        with self.assertRaises(agent.Refused):
            agent.plan("journal_tail", {"unit": evil})
        with self.assertRaises(agent.Refused):
            agent.plan("service_restart", {"unit": evil})
        with self.assertRaises(agent.Refused):
            agent.plan("wifi_reassociate", {"iface": "wlan0; reboot"})
        with self.assertRaises(agent.Refused):
            agent.plan("usb_rebind", {"device": "../../etc"})
        # Every argv that plan() produces is a list of fixed strings.
        for action, args in [
            ("reboot", {"delay": 5}),
            ("journal_tail", {"unit": "bluetooth", "lines": 3}),
            ("wifi_reassociate", {}),
            ("wifi_restart", {"iface": "wlan0"}),
            ("p2p_restart", {}),
            ("service_restart", {"unit": "bluetooth"}),
        ]:
            argv, _t, _p = agent.plan(action, args)
            self.assertIsInstance(argv, list, action)
            for a in argv:
                self.assertIsInstance(a, str)
                self.assertNotIn(";", a)
                self.assertNotIn("|", a)

    def test_no_netplan_action(self):
        for action in ("netplan", "netplan_apply", "netplan_safe"):
            with self.assertRaises(agent.Refused):
                agent.plan(action, {})

    def test_service_restart_allowlist(self):
        argv, _t, _p = agent.plan("service_restart", {"unit": "bluetooth"})
        self.assertEqual(argv, ["systemctl", "restart", "bluetooth.service"])
        with self.assertRaises(agent.Refused):
            agent.plan("service_restart", {"unit": "docker"})

    def test_usb_rebind_table(self):
        argv, _t, post = agent.plan("usb_rebind", {"device": "aioc"})
        self.assertIsNone(argv)
        self.assertTrue(callable(post))
        with self.assertRaises(agent.Refused):
            agent.plan("usb_rebind", {"device": "mesh"})


FAKE_DEV = {"name": "2-1.3", "vidpid": "2886:0059", "hub": "2-1", "twin": "3-1", "port": 3}


class USBPowerCycleTests(unittest.TestCase):
    """usb_power_cycle / usb_switchable with the sysfs and uhubctl lookups
    replaced, so nothing on the test host is read or switched. [MESHSAT-786]"""

    def setUp(self):
        self._resolve = agent.resolve_usb_port
        self._switchable = agent.hub_switchable
        self._rebind = agent.usb_rebind
        agent.resolve_usb_port = lambda role, tty=None: dict(FAKE_DEV)
        agent.hub_switchable = lambda hub, port: True
        agent.usb_rebind = lambda ids: {"rebound": ["2-1.3"]}

    def tearDown(self):
        agent.resolve_usb_port = self._resolve
        agent.hub_switchable = self._switchable
        agent.usb_rebind = self._rebind

    def test_parse_usb_name(self):
        self.assertEqual(agent.parse_usb_name("2-1.3"), ("2-1", 3))
        self.assertEqual(agent.parse_usb_name("2-1.3.2"), ("2-1.3", 2))
        with self.assertRaises(agent.Refused):
            agent.parse_usb_name("2-2")  # root port: ganged on the Pi 5
        with self.assertRaises(agent.Refused):
            agent.parse_usb_name("../drivers")
        with self.assertRaises(agent.Refused):
            agent.parse_usb_name("")

    def test_power_cycle_argv_is_detached_and_covers_both_hubs(self):
        argv, timeout, post = agent.plan("usb_power_cycle", {"device": "mesh", "tty": "/dev/ttyACM1"})
        self.assertIsInstance(argv, list)
        self.assertEqual(argv[0], "systemd-run")
        self.assertIn("--collect", argv)
        self.assertTrue(any(a.startswith("--unit=meshsat-usb-cycle-mesh-") for a in argv))
        i = argv.index("--usb-cycle")
        self.assertEqual(argv[i + 1:], ["3", str(agent.DEFAULT_OFF_MS), "2-1", "3-1"])
        for a in argv:
            self.assertIsInstance(a, str)
            self.assertNotIn(";", a)
            self.assertNotIn("|", a)
        result = post(None)
        self.assertTrue(result["scheduled"])
        self.assertEqual(result["method"], "power_cycle")
        self.assertEqual((result["hub"], result["twin"], result["port"]), ("2-1", "3-1", 3))

    def test_off_ms_clamped(self):
        argv, _t, post = agent.plan("usb_power_cycle", {"device": "mesh", "off_ms": 99999})
        self.assertIn(str(agent.MAX_OFF_MS), argv)
        self.assertEqual(post(None)["off_ms"], agent.MAX_OFF_MS)
        argv, _t, _p = agent.plan("usb_power_cycle", {"device": "mesh", "off_ms": 1})
        self.assertIn(str(agent.MIN_OFF_MS), argv)
        argv, _t, _p = agent.plan("usb_power_cycle", {"device": "mesh", "off_ms": "nope"})
        self.assertIn(str(agent.DEFAULT_OFF_MS), argv)

    def test_single_hub_when_no_twin(self):
        agent.resolve_usb_port = lambda role, tty=None: dict(FAKE_DEV, twin=None)
        argv, _t, _p = agent.plan("usb_power_cycle", {"device": "zigbee"})
        self.assertEqual(argv[argv.index("--usb-cycle") + 1:], ["3", str(agent.DEFAULT_OFF_MS), "2-1"])

    def test_unknown_role_and_root_port_refused(self):
        agent.resolve_usb_port = self._resolve  # the real resolver rejects unknown roles first
        with self.assertRaises(agent.Refused):
            agent.plan("usb_power_cycle", {"device": "../../etc"})
        with self.assertRaises(agent.Refused):
            agent.plan("usb_power_cycle", {"device": "imt"})

        def root(role, tty=None):
            raise agent.Refused("device is on a root port, not switchable")
        agent.resolve_usb_port = root
        with self.assertRaises(agent.Refused):
            agent.plan("usb_power_cycle", {"device": "mesh", "tty": "/dev/ttyACM1"})

    def test_ganged_hub_falls_back_to_rebind_only_for_rebindable_roles(self):
        agent.hub_switchable = lambda hub, port: False
        # aioc has a sysfs rebind path: fallback (the default) keeps today's behaviour.
        argv, _t, post = agent.plan("usb_power_cycle", {"device": "aioc"})
        self.assertIsNone(argv)
        self.assertEqual(post(None)["method"], "rebind")
        # mesh has no rebind path (shared-chip rule): refused, the bridge does its own reset.
        with self.assertRaises(agent.Refused):
            agent.plan("usb_power_cycle", {"device": "mesh", "tty": "/dev/ttyACM1"})
        # explicit fallback=false never rebinds.
        with self.assertRaises(agent.Refused):
            agent.plan("usb_power_cycle", {"device": "aioc", "fallback": False})

    def test_usb_switchable_reports_every_role(self):
        argv, _t, post = agent.plan("usb_switchable", {})
        self.assertIsNone(argv)
        out = post(None)
        self.assertEqual(sorted(out), sorted(agent.USB_DEVICES))
        self.assertTrue(out["mesh"]["switchable"])
        self.assertEqual(out["mesh"]["name"], "2-1.3")

        def absent(role, tty=None):
            raise agent.Refused("device not present")
        agent.resolve_usb_port = absent
        out = agent.plan("usb_switchable", {"device": "gps"})[2](None)
        self.assertEqual(out, {"gps": {"switchable": False, "reason": "device not present"}})

    def test_usb_cycle_helper_rejects_bad_args(self):
        self.assertEqual(agent.usb_cycle_main([]), 2)
        self.assertEqual(agent.usb_cycle_main(["0", "3000", "2-1"]), 2)
        self.assertEqual(agent.usb_cycle_main(["3", "3000", "../x"]), 2)


class DispatchTests(unittest.TestCase):
    def test_dispatch_unknown_is_ok_false(self):
        reply = agent.dispatch({"action": "nope"})
        self.assertFalse(reply["ok"])
        self.assertEqual(reply["error"], "unknown action")
        self.assertEqual(reply["version"], agent.VERSION)

    def test_dispatch_ping(self):
        reply = agent.dispatch({"action": "ping"})
        self.assertTrue(reply["ok"])
        self.assertEqual(reply["result"], {"version": agent.VERSION})

    def test_dispatch_args_must_be_object(self):
        reply = agent.dispatch({"action": "ping", "args": [1, 2]})
        self.assertFalse(reply["ok"])

    def test_reply_is_one_json_line(self):
        reply = agent.dispatch({"action": "ping"})
        line = json.dumps(reply)
        self.assertNotIn("\n", line)


if __name__ == "__main__":
    unittest.main()
