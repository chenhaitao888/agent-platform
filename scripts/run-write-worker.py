#!/usr/bin/env python3
"""Run the local write Worker using the pilot's explicit private write settings."""
import argparse
import importlib.util
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('pilot_launcher', Path(__file__).with_name('run-local-pilot.py'))
pilot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pilot)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--check', action='store_true', help='validate local files only; do not connect to services')
    args = parser.parse_args()
    os.umask(0o077)
    try:
        config, state, token = pilot.configuration(args.config)
        if not {'serviceCatalogFile', 'planningConnectorsFile', 'archiveDatabaseFile', 'writesFile'}.issubset(config):
            raise pilot.PilotError('write Worker requires catalog, planning, archive and writes files')
        api, _, build = pilot.environments(config, state, token)
        allowed = {'HOME', 'LANG', 'PATH', 'TMPDIR'} | {'AGENT_PLATFORM_' + key for key in (
            'AUTH_FILE', 'SERVICE_CATALOG_FILE', 'PLANNING_CONNECTORS_FILE', 'ARCHIVE_DATABASE_FILE', 'WRITES_FILE', 'SIMULATION_FILE',
            'GITLAB_BASE_URL', 'GITLAB_TOKEN', 'GITLAB_CA_FILE', 'WORKSPACE_ROOT')}
        environment = {key: value for key, value in api.items() if key in allowed}
        if args.check:
            print('Write Worker files checked; no services, model calls or writes started.')
            return 0
        binary = state / 'bin/writeworker'
        binary.parent.mkdir(mode=0o700, exist_ok=True)
        subprocess.run([config['goBinary'], 'build', '-o', str(binary), './cmd/writeworker'],
                       cwd=pilot.REPO / 'backend', env=build, check=True, timeout=180)
        os.execve(binary, [str(binary)], environment)
    except pilot.PilotError as error:
        print(f'Write Worker: {error}', file=sys.stderr)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        print('Write Worker configuration or process failed; credential values are omitted.', file=sys.stderr)
    return 1


if __name__ == '__main__':
    sys.exit(main())
