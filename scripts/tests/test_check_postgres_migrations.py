import importlib.util
import json
from pathlib import Path
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / 'check_postgres_migrations.py'
SPEC = importlib.util.spec_from_file_location('check_postgres_migrations', SCRIPT)
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


def event(package, action, test=None):
    value = {'Package': package, 'Action': action}
    if test:
        value['Test'] = test
    return json.dumps(value)


def successful_results():
    return [row for package in CHECK.PACKAGES for row in (
        event(package, 'pass', 'TestRoundTrip/postgres'),
        event(package, 'pass'),
    )]


class PostgresMigrationResultsTests(unittest.TestCase):
    def test_complete_results_pass_including_cached_replay(self):
        rows = successful_results() * 2
        self.assertEqual(CHECK.validate(rows), dict.fromkeys(CHECK.PACKAGES, 1))

    def test_postgres_skip_is_an_error(self):
        for name in ('TestRoundTrip/postgres', 'TestPaths/postgres/dev',
                     'TestRepositoryPostgresMigrationPaths'):
            with self.subTest(name=name):
                rows = successful_results() + [event(CHECK.PACKAGES[0], 'skip', name)]
                with self.assertRaisesRegex(ValueError, 'skip'):
                    CHECK.validate(rows)

    def test_postgres_failure_is_an_error(self):
        rows = successful_results() + [event(CHECK.PACKAGES[0], 'fail', 'TestCase/postgres')]
        with self.assertRaisesRegex(ValueError, 'fail'):
            CHECK.validate(rows)

    def test_missing_package_is_an_error(self):
        with self.assertRaisesRegex(ValueError, 'missing successful package'):
            CHECK.validate(successful_results()[:2])

    def test_no_postgres_cases_is_an_error(self):
        rows = [event(package, 'pass') for package in CHECK.PACKAGES]
        with self.assertRaisesRegex(ValueError, 'no passing PostgreSQL'):
            CHECK.validate(rows)

    def test_truncated_package_results_are_an_error(self):
        with self.assertRaisesRegex(ValueError, 'missing successful package'):
            CHECK.validate(successful_results()[:-1])

    def test_unrelated_live_tests_and_unavailable_target_release_can_skip(self):
        rows = successful_results() + [
            event('lazymind/core/academic', 'skip', 'TestLiveProvider'),
            event(CHECK.PACKAGES[0], 'skip',
                  'TestRepositoryPostgresMigrationPaths/target_current_release'),
        ]
        self.assertEqual(CHECK.validate(rows), dict.fromkeys(CHECK.PACKAGES, 1))

    def test_malformed_results_are_an_error(self):
        with self.assertRaisesRegex(ValueError, 'Invalid Go JSON'):
            CHECK.validate(['not JSON'])


if __name__ == '__main__':
    unittest.main()
