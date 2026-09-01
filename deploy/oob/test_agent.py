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
