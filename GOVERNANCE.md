# Governance

This document describes how decisions get made in NKTP and how that is
expected to change as the project grows. For who the maintainers are, what
they are responsible for, and how to reach them, see
[MAINTAINERS.md](MAINTAINERS.md).

## Roles

NKTP has two roles today.

**Contributors** are anyone who opens an issue, a pull request, or a
discussion. There is no application and no prior commitment: the requirements
are the [Code of Conduct](CODE_OF_CONDUCT.md) and a `Signed-off-by` line on
every commit, as described under
[Developer Certificate of Origin](CONTRIBUTING.md#developer-certificate-of-origin).

**Maintainers** are the `@dsx-ai-factory/nktp-maintainers` team in
[.github/CODEOWNERS](.github/CODEOWNERS). They review and merge pull requests,
triage issues, decide what the chart installs by default, and cut releases.

The project deliberately does not yet split maintainers into reviewers,
approvers, and owners, or partition CODEOWNERS by directory. NKTP is one chart
with a small maintainer team, and a finer-grained ladder would describe
authority the project does not actually exercise. As the contributor base
grows, expect this section to gain tiers and expect CODEOWNERS to be split by
area; those changes go through [Changing this document](#changing-this-document)
like any other.

## How decisions get made

Most changes are decided in review. A pull request merges when it has one
approving review from a code owner and all threads are resolved, under
[Review process](CONTRIBUTING.md#review-process). Maintainers do not
self-merge substantive changes.

Changes that are hard to reverse once released are decided in the open before
the code is written. That covers new or removed chart dependencies, changes to
what is enabled by default, breaking changes to values, and changes to the
blueprint schema. Open an issue describing the change and what it costs
existing users, give maintainers a week to weigh in, and record the outcome on
that issue so the reasoning survives the pull request.

When maintainers disagree and discussion is not converging, the maintainers
decide as a group and write the decision and its rationale on the issue. A
maintainer who was overruled is free to say so in the record. Disagreement
about conduct rather than technical direction is not resolved this way; use
the reporting path in [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Releases

Releases are maintainer actions. What gets versioned, tagged, and published,
and the checks that gate it, are described in [RELEASING.md](RELEASING.md).
Release timing is not a community vote, but the contents of a release should
not be a surprise: user-visible changes are expected to have been discussed on
an issue before they ship.

## Security

Security reports do not go through this process. NKTP follows NVIDIA's product
security response process, and reports are handled privately under
[SECURITY.md](SECURITY.md). Maintainers may merge a fix without the usual
public discussion when embargo requires it, and will explain the change after
disclosure.

## Changing this document

Governance changes are pull requests against this file. Leave the pull request
open for at least a week so people who are not watching closely have a chance
to object, and merge on maintainer consensus rather than a single approval.

## Sponsorship

NKTP is sponsored by NVIDIA. NVIDIA employs the current maintainers and
retains final say over the project's name, trademarks, and the repositories
and registries it publishes to. Technical direction is otherwise set by the
maintainers under this document, and contributions are accepted on their
merits regardless of who submits them.
