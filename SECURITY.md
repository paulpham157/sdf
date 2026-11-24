# Security

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub:
[Report a vulnerability](https://github.com/paulpham157/sdf/security/advisories/new)
(the **Security** tab of this repository, then **Report a vulnerability**).

Please do not open a public issue, pull request, or discussion that
includes exploit details for an unreleased vulnerability. Include the
sdf version (`sdf --version`), how you run it, and steps to reproduce.

## Security behavior

Product security behavior (what sdf stores, file permissions, the
optional receiver token, and the `sdf verify` threat model) is
documented in:

- [Content storage](docs/receiver.md#content-storage)
- [File permissions](docs/security.md)
- [Optional token authentication](docs/receiver.md#optional-token-authentication)
- [sdf verify](docs/verify.md) and [the GitHub Action](docs/action.md)
