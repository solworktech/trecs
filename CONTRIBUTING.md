# Contributing to Trecs

Thank you for wanting to help. Bug reports, fixes, documentation, ideas, and recordings that show a problem are all welcome.

Before anything else, please read [the contributor licence agreement](#contributor-licence-agreement) below: every contribution is made under it.

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
6. When asked, confirm the [contributor licence agreement](#contributor-licence-agreement).

Keep user-facing text, such as usage output, error messages and documentation, plain and consistent with what is already there. If you change a flag or a command, update the `usage` text and the [README](README.md) in the same pull request.

## Contributor licence agreement

Trecs is released under the [GNU AGPL v3](LICENSE), and Solwork also offers it under [enterprise licences](README.md#enterprise-licences). To be able to do both, we need every contributor to agree to the following. By submitting a contribution (a pull request, a patch, or any other way of sending code, text or other material to the project) you agree to it.

1. **You keep your copyright.** This agreement grants rights; it does not transfer ownership.

2. **Copyright licence.** You grant Solwork a perpetual, worldwide, non-exclusive, royalty-free, irrevocable licence to use, reproduce, modify, publicly display, publicly perform, distribute and sublicense your contribution, and to relicense it under any terms, including the GNU AGPL v3 and proprietary or commercial licences.

3. **Patent licence.** You grant Solwork and everyone who receives the project a perpetual, worldwide, non-exclusive, royalty-free, irrevocable patent licence to make, use, sell, offer to sell, import and otherwise transfer your contribution, for those patent claims you can license that are necessarily infringed by your contribution alone or combined with the project.

4. **Your right to contribute.** You confirm that:
   - the contribution is your own original work, or you have the right to submit it under these terms and have said so, with its source and licence, in the pull request;
   - you have the right to grant these licences. If your employer, or anyone else, has rights in what you write (for example, you wrote it as part of your job), you have their permission to contribute it;
   - to your knowledge, it does not infringe anyone else's rights.

5. **No obligation.** Solwork is not obliged to accept or use your contribution, and nobody owes you payment for it. The contribution is provided as it is, without warranties.

6. **Records.** Your name and the contribution are kept in the project's public history. You agree that this record is public and will not be removed.

If you are contributing on behalf of a company, please write to jesse@solwork.io first, so that the company can sign a corporate version of this agreement.

### Why we ask for this

The AGPL lets anyone use and share Trecs, provided they share their source too. Some organisations need something different, such as a private, white-labelled platform or a rebranded recorder, and for those we offer enterprise licences. That is only possible when we may license every part of Trecs, including contributions, on those terms. The AGPL stays the licence of the public project; contributions to it will always be available under it.

## Code of conduct

Be kind and be constructive. Assume good faith, criticise code and not people, and remember that many contributors are volunteers. We may remove comments, contributions or participants that make the project an unpleasant place to work.
