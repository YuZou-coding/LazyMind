#!/usr/bin/env python3
"""Reject missing or skipped PostgreSQL migration coverage in Go JSON results."""

import argparse
import json
from pathlib import Path
import sys


PACKAGES = ('lazymind/core/migrate', 'lazymind/core/migrations')


def postgres_case(name):
    parts = name.split('/')
    return 'postgres' in parts[1:] or (len(parts) == 1 and 'postgres' in name.lower())


def validate(lines):
    completed = set()
    passed = {package: set() for package in PACKAGES}
    errors = []
    for number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise ValueError(f'Invalid Go JSON event on line {number}: {error}') from error
        package = event.get('Package')
        if package not in PACKAGES:
            continue
        action = event.get('Action')
        test = event.get('Test', '')
        if not test and action == 'pass':
            completed.add(package)
        if not test or not postgres_case(test):
            continue
        if action == 'pass':
            passed[package].add(test)
        elif action in ('skip', 'fail'):
            errors.append(f'{package}: {test} {action}')
    for package in PACKAGES:
        if package not in completed:
            errors.append(f'{package}: missing successful package result')
        if not passed[package]:
            errors.append(f'{package}: no passing PostgreSQL migration cases')
    if errors:
        raise ValueError('\n'.join(errors))
    return {package: len(tests) for package, tests in passed.items()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('results', type=Path, help='go test -json output')
    args = parser.parse_args()
    try:
        with args.results.open(encoding='utf-8') as results:
            counts = validate(results)
    except (OSError, ValueError) as error:
        print(f'PostgreSQL migration verification failed:\n{error}', file=sys.stderr)
        return 1
    for package, count in counts.items():
        print(f'{package}: {count} PostgreSQL migration cases passed; none skipped')
    return 0


if __name__ == '__main__':
    sys.exit(main())
