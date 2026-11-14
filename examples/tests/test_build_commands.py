import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location('sdk_build_commands', Path(__file__).resolve().parents[2] / 'tools/build.py')
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)


class BuildCommandsTests(unittest.TestCase):
    def test_windows_resolves_npm_command_using_the_child_path(self):
        env = {'PATH': r'C:\fixture Node\bin'}
        with patch.object(BUILD.os, 'name', 'nt'), patch.object(BUILD.shutil, 'which', return_value=r'C:\fixture Node\bin\npm.cmd') as which, patch.object(BUILD.subprocess, 'run') as run:
            BUILD.run(['npm', 'ci', '--ignore-scripts'], env=env)
        which.assert_called_once_with('npm.cmd', path=env['PATH'])
        run.assert_called_once_with([r'C:\fixture Node\bin\npm.cmd', 'ci', '--ignore-scripts'], cwd=BUILD.ROOT, env=env, check=True)

    def test_missing_windows_npm_is_reported_before_spawning(self):
        with patch.object(BUILD.os, 'name', 'nt'), patch.object(BUILD.shutil, 'which', return_value=None), patch.object(BUILD.subprocess, 'run') as run:
            with self.assertRaisesRegex(FileNotFoundError, 'npm.cmd is required'):
                BUILD.run(['npm', 'run', 'check'], env={'PATH': ''})
        run.assert_not_called()

    def test_non_windows_npm_preserves_normal_executable_lookup(self):
        with patch.object(BUILD.os, 'name', 'posix'), patch.object(BUILD.shutil, 'which') as which, patch.object(BUILD.subprocess, 'run') as run:
            BUILD.run(['npm', 'run', 'check'])
        which.assert_not_called()
        run.assert_called_once_with(['npm', 'run', 'check'], cwd=BUILD.ROOT, env=None, check=True)


if __name__ == '__main__':
    unittest.main()
