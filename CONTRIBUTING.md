# Contributing to Trecs

Thank you for wanting to help. Bug reports, fixes, documentation, ideas, and recordings that show a problem are all welcome.

Before anything else, please read the [contributor licence agreement](#contributor-licence-agreement) below: every contribution is made under it, and you sign it once.

## Reporting a problem

Open an issue and tell us:

- what you did, what you expected and what happened instead;
- your operating system, shell and the output of `trecs help` or `trecs-edit help` (so we know the version in use);
- if you can, a recording that shows the problem. A `terminal.jsonl` is plain text, so please check it for anything private (tokens, passwords, host names) before attaching it. `trecs-edit view` lets you edit or delete those commands first.

Security problems should not go in a public issue. Write to jesse@solwork.io instead.

## Suggesting a change

For anything bigger than a small fix, please open an issue first, so we can agree on the direction before you spend time on it. A change that doesn't fit the project's direction is better heard about early than after a week of work.

## Sending a pull request

1. Fork the repository and create a branch for your change.
2. Keep the change focused: one fix or feature per pull request, and no unrelated reformatting.
3. Format the code with `gofmt`, and add tests where you can. A bug fix should come with a test that fails without it.
4. Make sure the tests pass:

   ```sh
   go test ./...
   ```

5. Explain the change in the pull request: what it does and why, and how you tried it.
6. If the CLA bot asks, sign the [contributor licence agreement](#contributor-licence-agreement) by replying to its comment.

Keep user-facing text, such as usage output, error messages and documentation, plain and consistent with what is already there. If you change a flag or a command, update the `usage` text and the [README](README.md) in the same pull request.

## Contributor licence agreement

Trecs is released under the [GNU AGPL v3](LICENSE), and Solwork also offers it under [enterprise licences](README.md#enterprise-licences). To be able to do both, every contributor agrees to the [Contributor Licence Agreement](CLA.md). In short: you keep your copyright, and you give Solwork the right to use your contribution under the AGPL and under other licences, including commercial ones. Please read the full text in [CLA.md](CLA.md).

**You sign it once.** When you open your first pull request, a bot comments on it with instructions: reply with the sentence it gives you (`I have read the CLA Document and I hereby sign the CLA`). Your signature is recorded against your GitHub account and covers your later contributions, and a pull request cannot be merged until its author has signed.

### Why we ask for this

The AGPL lets anyone use and share Trecs, provided they share their source too. Some organisations need something different, such as a private, white-labelled platform or a rebranded recorder, and for those we offer enterprise licences. That is only possible when we may license every part of Trecs, including contributions, on those terms. The AGPL stays the licence of the public project; contributions to it will always be available under it.

## Code of conduct

Be kind and be constructive. Assume good faith, criticise code and not people, and remember that many contributors are volunteers. We may remove comments, contributions or participants that make the project an unpleasant place to work.
