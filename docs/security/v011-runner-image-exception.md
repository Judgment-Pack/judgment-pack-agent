# v0.11.0 runner image exception

On 2026-10-28 the maintainer explicitly approved: “Approve scoped two-image
exception.” This approval covers Judgment-Pack/judgment-pack-agent (repository
ID 1424951648), release tag v0.11.0 only. It expires at 2026-11-07 00:00:00 UTC;
verification at or after that instant refuses, including collection that crosses
the deadline. It does not authorize another release or a general image override.

The actual producer and verifier ImageVersions must each be exactly
20261023.163.3 or 20261026.180.1. Both must report ImageOS ubuntu26, RUNNER_OS
Linux, RUNNER_ARCH X64, and os_release SHA-256
`cf72627ff81aef345d0a9f3807eb80f1035e0f80fe2932d12d94efc02fd68104`.
All configured tool identities and every other evidence/admission check remain
exact. Capture and verification retain raw actual identities.

This accepts unknown differences in host libraries, kernel and preinstalled
tools between those two images; it is not proof that they are equivalent. It
does not claim a product behavior change or live validation. The original main
and single fresh recovery pair each produced these different image versions in
required jobs. Further arbitrary reruns are not the remedy.

The approval is bound by its SHA-256 in the source-controlled evidence config
and independently hashed release-source inputs. The implementation requires a
new validated main commit and independent review before release. Old-commit
artifacts do not qualify for that new commit. Advisory review and scoped advisory
approvals, signing/tag/main/protection checks, packaging, attestations, protected
publication approval, asset identity and the paired Go tag remain required.
