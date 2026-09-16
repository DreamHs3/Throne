"""Check the SDDL actually constructed by the Inno/Pascal command string.

Portable regression only; Windows descriptor parsing and SCM smoke remain required.
Run: python tests/proxycore/test_installer_sddl_contract.py
"""
import re
import unittest
from pathlib import Path


def installer_sddl(source, sid):
    lines = [line for line in source.splitlines()
             if "PsParams :=" in line and "THRONE_SERVICE_SDDL=" in line]
    if len(lines) != 1:
        raise ValueError("Expected one installer environment command")
    expression = lines[0].split(":=", 1)[1].strip().removesuffix(";")
    variables = {"Sid": sid, "DataDir": r"C:\ProgramData\ProxyCore"}
    tokens = re.findall(r"'(?:''|[^'])*'|[A-Za-z_][A-Za-z_0-9]*|\+", expression)
    if re.sub(r"\s+", "", expression) != re.sub(r"\s+", "", "".join(tokens)):
        raise ValueError("Unsupported Pascal expression")
    result = []
    expect_value = True
    for token in tokens:
        if expect_value:
            result.append(token[1:-1].replace("''", "'")
                          if token.startswith("'") else variables[token])
        elif token != "+":
            raise ValueError("Expected concatenation")
        expect_value = not expect_value
    if expect_value:
        raise ValueError("Incomplete expression")
    command = "".join(result)
    match = re.search(r"'THRONE_SERVICE_SDDL=([^']*)'", command)
    if not match:
        raise ValueError("No SDDL environment value")
    return match.group(1)


class InstallerSddlContract(unittest.TestCase):
    def test_installer_emits_complete_restricted_descriptor(self):
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        for sid in ("S-1-5-21-123-456-789-1001", "S-1-5-21-9-8-7-1002"):
            with self.subTest(sid=sid):
                actual = installer_sddl(source, sid)
                self.assertEqual(actual, "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")")


if __name__ == "__main__":
    unittest.main()
