"""Regressions for the demo runner's broker cleanup."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("trials", Path(__file__).with_name("verify-integration.py"))
trials = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trials)


class DurableCleanupTests(unittest.TestCase):
    def trial(self):
        trial = trials.Trials.__new__(trials.Trials)
        trial.env = {"NATS_TOKEN": "synthetic-secret", "NATS_STREAM_NAME": "HAZARD_EVENTS"}
        return trial

    def test_delete_succeeds_even_when_nc_does_not_return_api_reply(self):
        trial = self.trial()
        durable = "third-consumer-trial-123"
        trial.broker_consumers = Mock(side_effect=[{durable: {}}, {}])
        with patch.object(trials.subprocess, "check_output", return_value=b"INFO {}\r\n") as command:
            trial.remove_trial_consumer(durable)
        command.assert_called_once()
        self.assertEqual(trial.broker_consumers.call_count, 2)

    def test_existing_application_durables_cannot_be_deleted(self):
        trial = self.trial()
        with patch.object(trials.subprocess, "check_output") as command:
            with self.assertRaises(AssertionError):
                trial.remove_trial_consumer("dashboard-v1")
        command.assert_not_called()

    def test_missing_durable_needs_no_delete(self):
        trial = self.trial()
        trial.broker_consumers = Mock(return_value={})
        with patch.object(trials.subprocess, "check_output") as command:
            trial.remove_trial_consumer("third-consumer-trial-123")
        command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
