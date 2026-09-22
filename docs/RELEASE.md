# Deployment and release flow

This project deploys to 2 environments:

- `stage` — the pre-production test environment.
- `prod` — the production environment.

The flow uses immutable tags. It builds a container once, tests it on `stage`,
and promotes the same image to `prod`.

## Principles

- `main` is the source of truth. Regular changes enter `main` through a pull
  request.
- `stage` and `prod` are environments, not branches.
- A release candidate and a final release are immutable tags, not branches.
- Build the image once. Deploy the same digest to `stage`, then to `prod`.

## Versioning

Versions follow Semantic Versioning: `vMAJOR.MINOR.PATCH`.

- `MAJOR` — a breaking change.
- `MINOR` — a new backward-compatible function.
- `PATCH` — a backward-compatible bug fix.

A release candidate adds a suffix: `vMAJOR.MINOR.PATCH-rc.N`.

| Tag | Example | Deploys to |
|---|---|---|
| Release candidate | `v0.2.0-rc.1` | `stage` |
| Final release | `v0.2.0` | `prod` |

The final release tag and the approved release candidate tag point to the same
commit.

## The 2 workflows

| File | Trigger | What it does |
|---|---|---|
| `.github/workflows/release-candidate.yml` | a tag `vX.Y.Z-rc.N` | Builds the RC image. Tags it with the rc version and `sha-<commit>`. Pushes it. Deploys it to `stage`. |
| `.github/workflows/release.yml` | a tag `vX.Y.Z` | Finds the `sha-<commit>` image that the RC built. Adds the release tag and `latest` to that same digest. Deploys it to `prod`. |

The image goes to `ghcr.io/<owner>/<repo>`. The workflow signs in with the
built-in `GITHUB_TOKEN`.

The final release does not rebuild. It promotes the tested digest. If no
`sha-<commit>` image exists, the release workflow stops, because a final release
must promote an approved release candidate.

## The prod gate

The `prod` deploy runs in the GitHub `prod` environment. To make it a manual
gate, add a required reviewer to that environment in the repository settings.
Remove the reviewer to make the prod deploy automatic.

Put the real deploy command in the `Deploy` step of each workflow. The step is
a placeholder now.

## Normal release

1. Merge the changes into `main` through pull requests.
2. Tag the commit as a release candidate, and push the tag.

   ```sh
   git tag v0.2.0-rc.1
   git push origin v0.2.0-rc.1
   ```

3. The workflow deploys `v0.2.0-rc.1` to `stage`. Test it.
4. If you find a bug, fix it on `main`, then tag `v0.2.0-rc.2`. Repeat.
5. When a candidate passes, tag the same commit as the final release.

   ```sh
   git tag v0.2.0 <commit-of-approved-rc>
   git push origin v0.2.0
   ```

6. The workflow promotes the tested image and deploys `v0.2.0` to `prod`.

## Patch release from main

Use this when `main` is safe to release.

```text
bugfix PR -> main
tag v0.2.1-rc.1 -> stage
approve
tag v0.2.1 -> prod
```

## Hotfix release

Use this when `main` has unreleased changes that must not go to prod yet.

1. Make a hotfix branch from the current production tag.

   ```sh
   git checkout -b hotfix/v0.2.1 v0.2.0
   ```

2. Commit the fix on the branch.
3. Tag a release candidate on the hotfix commit, and push it.

   ```sh
   git tag v0.2.1-rc.1
   git push origin v0.2.1-rc.1
   ```

4. The workflow deploys `v0.2.1-rc.1` to `stage`. Test it.
5. Tag the same commit as `v0.2.1`, and push it. The workflow deploys to `prod`.
6. Merge or cherry-pick the hotfix back into `main`.

   ```sh
   git cherry-pick <hotfix-commit>   # onto main
   ```

## Rollback

A rollback redeploys a known-good production tag. Push the old tag reference
again, or trigger the prod deploy with the old release image. Do not make a
branch for a rollback.

If the rollback needs a new fix, make a new patch release, for example
`v0.2.2`.
