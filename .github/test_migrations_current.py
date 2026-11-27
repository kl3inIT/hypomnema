import unittest

from migrations_current import migration_status, numbered_migrations


class MigrationStatusTest(unittest.TestCase):
    def test_status_for_added_files(self):
        directory = "internal/controlplane/orchestration/migrations"
        last = max(numbered_migrations([
            f"{directory}/00012_released.sql",
            f"{directory}/00010_released.sql",
        ]))
        success = ("success", "Migration numbers are current")
        cases = [
            ("taken number", [f"{directory}/00012_new.sql"], (
                "failure", "12 is taken on main: merge main and run make migration-fix",
            )),
            ("lower number in a gap", [f"{directory}/00013_new.sql", f"{directory}/00011_new.sql"], (
                "failure", "11 is taken on main: merge main and run make migration-fix",
            )),
            ("above main", [f"{directory}/00013_new.sql"], success),
            ("time-named", [f"{directory}/20261127124500_new.sql"], success),
            ("none added", [], success),
        ]
        for name, paths, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(migration_status(last, paths), expected)


if __name__ == "__main__":
    unittest.main()
