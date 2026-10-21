import unittest

from recon import run


class _FakeProcess(object):
    def __init__(self, return_codes, kill_error=None):
        self.return_codes = list(return_codes)
        self.kill_error = kill_error
        self.kill_called = False

    def kill(self):
        self.kill_called = True
        if self.kill_error is not None:
            raise self.kill_error

    def wait(self):
        raise AssertionError("cleanup must not call unbounded wait")

    def poll(self):
        if self.return_codes:
            return self.return_codes.pop(0)
        return None


class RunConfigurationTests(unittest.TestCase):
    def test_missing_python_version_defaults_to_three(self):
        self.assertEqual(run.PYTHON_VERSION_3, run.resolve_python_version({}))

    def test_config_and_cli_can_select_supported_versions(self):
        self.assertEqual(run.PYTHON_VERSION_2, run.resolve_python_version({"python_version": 2}))
        self.assertEqual(run.PYTHON_VERSION_3, run.resolve_python_version({"python_version": 2}, 3))

    def test_invalid_versions_fail_closed(self):
        for value in (True, 0, 1, 4, "python"):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    run.resolve_python_version({"python_version": value})

    def test_environment_selector_replaces_case_variants(self):
        base = {
            "Path": "fixture",
            "ghs_ide_use_python3": "inherited",
            "GHS_IDE_USE_PYTHON3": "duplicate",
        }
        environment = run.embedded_python_environment(run.PYTHON_VERSION_2, base)
        self.assertEqual({"Path": "fixture", "GHS_IDE_USE_PYTHON3": "0"}, environment)

    def test_wait_for_process_uses_poll_loop(self):
        process = _FakeProcess([None, 0])
        return_code, timed_out = run.wait_for_process(process, 1.0, poll_interval=0)
        self.assertEqual(0, return_code)
        self.assertFalse(timed_out)

    def test_wait_for_process_reports_timeout_without_py3_api(self):
        process = _FakeProcess([None])
        return_code, timed_out = run.wait_for_process(process, 0)
        self.assertIsNone(return_code)
        self.assertTrue(timed_out)

    def test_timeout_cleanup_propagates_kill_failure_for_live_process(self):
        error = OSError("kill denied")
        process = _FakeProcess([None], kill_error=error)
        with self.assertRaises(OSError) as caught:
            run.kill_timed_out_process(process, timeout=0)
        self.assertIs(error, caught.exception)
        self.assertTrue(process.kill_called)

    def test_timeout_cleanup_accepts_exit_race_after_kill_error(self):
        process = _FakeProcess([0], kill_error=OSError("already exited"))
        self.assertEqual(0, run.kill_timed_out_process(process, timeout=0))
        self.assertTrue(process.kill_called)

    def test_timeout_cleanup_bounds_wait_after_successful_kill(self):
        process = _FakeProcess([None])
        with self.assertRaises(RuntimeError) as caught:
            run.kill_timed_out_process(process, timeout=0)
        self.assertIn("may still be running", str(caught.exception))
        self.assertTrue(process.kill_called)

    def test_timeout_cleanup_accepts_confirmed_exit(self):
        process = _FakeProcess([-9])
        self.assertEqual(-9, run.kill_timed_out_process(process, timeout=0))
        self.assertTrue(process.kill_called)


if __name__ == "__main__":
    unittest.main()
