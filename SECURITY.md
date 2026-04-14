# Security Policy

## Vulnerability Reporting

We take the security of `docker-hash` seriously. If you discover a security vulnerability, please report it via the [GitHub Private Vulnerability Reporting](https://github.com/RemkoMolier/docker-hash/security/advisories/new) workflow.

Please provide:
- A detailed description of the vulnerability.
- Steps to reproduce the issue (e.g., a Dockerfile that triggers the bug).
- The potential impact of the vulnerability.

We aim to acknowledge reports within 72 hours and provide a fix or mitigation as quickly as possible.

## Supply Chain Security

`docker-hash` implements several industry-standard protections to ensure the integrity of its release artifacts:

### Cryptographic Provenance
All official releases are signed using **Sigstore keyless signing**. This binds the release artifacts to the official GitHub Actions workflow identity via OIDC, removing the need for long-lived private keys.

### SLSA Attestations
We provide **SLSA Build Level 3** provenance for all binaries and OCI images. This allows users to verify that the artifact was built on GitHub Actions, from the official repository, and from a specific tagged commit.

### Software Bill of Materials (SBOM)
Every release includes a machine-readable SBOM in **SPDX** and **CycloneDX** formats, available as GitHub release assets and as OCI attestations attached to the images in GHCR.

### Continuous Scanning
The project employs several automated security tools in CI:
- **OSSF Scorecard**: Monitors the overall security posture of the project.
- **CodeQL**: Performs static analysis of the Go source code.
- **Trivy**: Scans the official OCI images for known vulnerabilities.
- **Dependency Review**: Blocks PRs that introduce dependencies with high CVSS scores.
