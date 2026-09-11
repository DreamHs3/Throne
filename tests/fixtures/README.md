# Parser fixtures (PC-000)

Safe, synthetic fixtures for the SSH / Shadowsocks / SOCKS5 profile
parsers/builders (`src/configs/outbounds/{ssh,shadowsocks,socks}.cpp`).

- **No real credentials, private keys, subscription URLs or full core
  configs.** Hosts use `example.com` / RFC 5737 / RFC 1986 documentation
  addresses; credentials are `example-user` / `example-password`; key
  material is synthetic ASCII marked `PC-TEST` / `pc-test`.
- `expected` fields are derived from the unchanged baseline parser code
  ( Throne `21b8f680b95d1dfe7906b6b64f6c3c51c263ba40` ), not from execution —
  the C++ harness (PC-030) validates them once a toolchain is available.
- `round_trip` cases assert the invariant
  `parse(export(parse(input))) == parse(input)` rather than exact output
  strings (QUrl encoding makes exact-string comparison brittle).

Schema (consumed by the PC-030 characterization harness):

```jsonc
{
  "protocol": "ssh|shadowsocks|socks",
  "cases": [
    {
      "id": "...",
      "input_link": "...",            // or "input_json": {...}
      "parse_ok": true|false,         // expected return of ParseFromLink
      "expected": { ...fields... },   // struct fields after parse
      "round_trip_link": true,        // assert invariant above (link form)
      "round_trip_json": true,        // assert parse(export(parse)) via JSON form
      "build_json_contains": {...},   // keys expected in Build() output
      "known_defect": "..."           // optional reference, do not "fix" here
    }
  ]
}
```
