#!/usr/bin/python3

"""Disabled until ProxyCore has its own authenticated release channel."""

import sys


def main() -> int:
    print(
        "The ProxyCore Linux release installer is not available yet. "
        "Build from this checkout instead.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
