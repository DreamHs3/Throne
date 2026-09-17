"""Check the SDDL and service contracts actually constructed by the Inno/Pascal
command strings.

Portable regression only; Windows descriptor parsing and SCM smoke remain required.
Run: python tests/proxycore/test_installer_sddl_contract.py
"""
import re
import unittest
from pathlib import Path

Q = chr(34)      # double quote
BS = chr(92)     # backslash


def installer_sddl(source, sid):
    lines = [line for line in source.splitlines()
             if "PsParams :=" in line and "THRONE_SERVICE_SDDL=" in line
             and "Set-ItemProperty" in line]
    if len(lines) != 1:
        raise ValueError("Expected one installer environment command")
    expression = lines[0].split(":=", 1)[1].strip().removesuffix(";")
    service_name = re.search(r"ServiceName = '([^']+)'", source)
    if not service_name:
        raise ValueError("missing ServiceName const")
    variables = {"Sid": sid,
                 "DataDir": r"C:\ProgramData\ProxyCore",
                 "ServiceName": service_name.group(1)}
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

    def test_service_imagepath_quotes_the_executable(self):
        """R4: binPath must be one quoted group whose inner quotes mark only
        the exe ('service' outside), so SCM resolves spaced paths to our
        binary and a planted 'C:\\Program.exe' can never win."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        self.assertIn('Parameters: "{code:ScCreateParams}"', source)
        # the Pascal builder assembles: binPath= "\"<exe>\" service" ...
        self.assertIn("binPath= " + Q + BS + Q + "' +", source)
        self.assertIn(BS + Q + " service" + Q + " start= demand';", source)

    def test_service_steps_have_ownership_guard_and_readbacks(self):
        """R4: create is guarded against foreign same-name services; the data
        folder DACL and the service Environment are read back after writing."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        create_lines = [line for line in source.splitlines()
                        if "{code:ScCreateParams}" in line]
        self.assertTrue(create_lines, "expected the sc create [Run] entry")
        self.assertIn("AfterInstall: SetupServiceEnv", create_lines[0])
        self.assertNotIn("BeforeInstall", create_lines[0])
        icacls_lines = [line for line in source.splitlines()
                        if "/inheritance:r" in line]
        self.assertTrue(icacls_lines, "expected the data-folder icacls entry")
        self.assertIn("AfterInstall: VerifyDataDirAcl", icacls_lines[0])
        for needle in ("function TryReadServiceImagePath",
                       "function ForeignServiceCollision",
                       "function PrepareToInstall",
                       "procedure VerifyDataDirAcl",
                       "procedure RollbackService",
                       "Environment read-back does not match"):
            self.assertIn(needle, source, f"missing hardening piece: {needle}")

    def test_uninstall_never_touches_foreign_service(self):
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        self.assertIn("left untouched", source)
        self.assertIn("TryReadServiceImagePath", source)
        # silent installs must be able to select admin mode (REC-01 finding)
        self.assertIn("PrivilegesRequiredOverridesAllowed=dialog commandline",
                      source)

    def test_rec01c_refusals_run_before_the_first_payload_write(self):
        """REC-01C R2 T4/T5/T7: the reparse and protected-dir refusals must be
        decided in PrepareToInstall (with existing ancestor components), i.e.
        BEFORE the [Files] section copies anything; the old in-SetupServiceEnv
        reparse gate ran after [Files] and leaked the payload through the
        junction."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        prepare = re.search(r"function PrepareToInstall.*?\nend;",
                            source, re.S).group(0)
        self.assertIn("InstallTargetViolations", prepare)
        self.assertIn("ProtectedDirViolation", prepare)
        self.assertIn("function ReparsePathViolation", source)
        self.assertIn("function ProtectedDirViolation", source)
        setupsvc = re.search(r"procedure SetupServiceEnv;.*?\nend;",
                             source, re.S).group(0)
        self.assertNotIn("IsReparsePoint", setupsvc)
        self.assertNotIn("ReparsePathViolation", setupsvc)

    def test_rec01c_rollback_is_fresh_repair_aware(self):
        """REC-01C R2 T3: a repair failure must keep the pre-existing service
        and restore its captured Environment; only a fresh attempt (whose
        guard confirmed the service was absent) may delete the service."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        rollback = re.search(r"procedure RollbackService;.*?\nend;",
                             source, re.S).group(0)
        self.assertIn("HadOurService", rollback)
        self.assertIn("SavedEnvironment", rollback)
        collision = re.search(r"function ForeignServiceCollision.*?\nend;",
                              source, re.S).group(0)
        self.assertIn("CaptureServiceEnvironment", collision)

    def test_rec01c_service_read_errors_are_not_absence(self):
        """REC-01C: only a confirmed Test-Path miss is 'absent'; read/access
        errors map to a distinct ERROR result the callers must refuse on, and
        ownership also requires the expected LocalSystem account."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        reader = re.search(r"function TryReadServiceImagePath.*?\nend;",
                           source, re.S).group(0)
        self.assertIn("Test-Path", reader)
        self.assertIn("ObjectName", reader)
        self.assertIn("ExpectedServiceAccount = 'LocalSystem'", source)

    def test_rec01c_app_dir_acl_check_and_deny_list(self):
        """REC-01C R2 T7: the install folder / service binary must be checked
        for unprivileged write access, and the protected-dir deny-list is a
        separate path-selection policy (not a replacement for the ACL check)."""
        source = (Path(__file__).resolve().parents[2]
                  / "script/windows_installer.iss").read_text(encoding="utf-8")
        self.assertIn("procedure VerifyAppDirAcl", source)
        acl = re.search(r"procedure VerifyAppDirAcl;.*?\nend;",
                        source, re.S).group(0)
        self.assertIn("ThroneCore.exe", acl)
        self.assertIn("TakeOwnership", acl)
        self.assertIn("S-1-5-32-544", acl)  # Administrators allow-list
        policy = re.search(r"function ProtectedDirViolation.*?\nend;",
                           source, re.S).group(0)
        for needle in ("{win}", "{commonpf}", "{commonpf32}"):
            self.assertIn(needle, policy)

    @staticmethod
    def _pf_suffix_and_first_component(d, pf):
        """Mirror of the exact Pascal arithmetic in ProtectedDirViolation rule
        (c): Rest = suffix after '<pf>\\'; first component = up to the first
        backslash. Regression for the T5 false positive where Copy(Rest, 2, ...)
        cut the first CHARACTER instead of a leading backslash and rejected
        the default install dir 'C:\\Program Files\\ProxyCore'."""
        def add_bs(p):
            return p if p.endswith("\\") else p + "\\"
        pf = pf.rstrip("\\")
        d = d if len(d) <= 3 else d.rstrip("\\")
        rest = ""
        under_pf = add_bs(d).upper().startswith(add_bs(pf).upper())
        if under_pf:
            rest = add_bs(d)[len(add_bs(pf)):]
        first = rest
        pos = first.find("\\")
        if pos >= 0:
            first = first[:pos]
        return under_pf, rest, first

    def test_rec01c_deny_list_pf_component_extraction(self):
        add_bs = lambda p: p if p.endswith("\\") else p + "\\"
        pf = "C:\\Program Files"
        pf32 = "C:\\Program Files (x86)"
        # default install dir must stay allowed (R2 T5 false positive)
        under, rest, first = self._pf_suffix_and_first_component(
            "C:\\Program Files\\ProxyCore", pf)
        self.assertTrue(under)
        self.assertEqual(first, "ProxyCore")
        # deeper nesting inside our own folder stays allowed
        _, _, first = self._pf_suffix_and_first_component(
            "C:\\Program Files\\ProxyCore\\sub", pf)
        self.assertEqual(first, "ProxyCore")
        # foreign application areas are violations
        _, _, first = self._pf_suffix_and_first_component(
            "C:\\Program Files\\SomeOtherApp\\ProxyCore", pf)
        self.assertNotEqual(first, "ProxyCore")
        # the (x86) root area is caught by the second prefix
        under32, rest32, first32 = self._pf_suffix_and_first_component(
            "C:\\Program Files (x86)\\Foo", pf32)
        self.assertTrue(under32)
        self.assertNotEqual(first32, "ProxyCore")
        # the Program Files root itself yields an empty suffix -> violation
        under_root, rest_root, _ = self._pf_suffix_and_first_component(
            "C:\\Program Files", pf)
        self.assertTrue(under_root)
        self.assertEqual(rest_root, "")
        # unrelated paths match neither prefix
        under_x, _, _ = self._pf_suffix_and_first_component("C:\\Foo", pf)
        self.assertFalse(under_x)
        self.assertFalse(
            add_bs("c:\\foo").upper().startswith(add_bs(pf32).upper()))


if __name__ == "__main__":
    unittest.main()
