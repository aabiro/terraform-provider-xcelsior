# Terraform provider for Xcelsior

Declare [Xcelsior](https://xcelsior.ca) GPU instances, volumes, SSH keys and
serverless inference endpoints in `.tf` files, and create them with
`terraform apply`.

| Kind | Name |
|---|---|
| Resource | `xcelsior_instance` |
| Resource | `xcelsior_volume` |
| Resource | `xcelsior_volume_attachment` |
| Resource | `xcelsior_ssh_key` |
| Resource | `xcelsior_serverless_endpoint` |
| Data source | `xcelsior_gpus` (live inventory) |
| Data source | `xcelsior_pricing` (published rate card) |

The user documentation is in [`docs/`](docs/), generated from the schema, the
[`examples/`](examples/) and the [`templates/`](templates/). Start with
[`docs/index.md`](docs/index.md) and the
[spend approval guide](docs/guides/spend-approval.md).

> **Status: first version, not published.** There is no registry release yet.
> Use it from a local build (below).

## Spend approval in one paragraph

Xcelsior never starts billed compute without an approved launch plan.
`terraform plan` quotes each new instance and endpoint without creating
anything. `terraform apply` creates the plan and, if it is inside the token's
**spend envelope** (ceilings a person sets on the token), approves and executes
it unattended. Outside the envelope it waits up to `approval_timeout` (default
`0s`: fail at once) for a person to approve it in the dashboard, and revokes
the plan when the wait ends, so nothing is left waiting.

## Use a local build

```sh
make install          # builds into $GOBIN (or ~/go/bin)
```

Then point Terraform at it with a `dev_overrides` block in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "registry.terraform.io/aabiro/xcelsior" = "/home/you/go/bin"
  }
  direct {}
}
```

With `dev_overrides`, skip `terraform init` and run `terraform plan` directly.

## Develop

Requirements: Go 1.26, and Terraform 1.5 or later for the tests that drive the
CLI.

```sh
make build            # go build ./...
make lint             # gofmt and go vet
make test             # unit tests; the provider tests run Terraform against an
                      # in-memory fake of the API (skipped without the CLI)
make docs             # regenerate docs/ with tfplugindocs
```

`make test` finds Terraform on `PATH`; set `TF_ACC_TERRAFORM_PATH` to use a
particular binary.

The API client is in `internal/client`, the resources and data sources in
`internal/provider`. The fake API the provider tests run against is
`internal/provider/fakeapi_test.go`. When the real API changes shape, change
the fake with it: the fake answering errors in a shape the API never sends is
how a real bug hid here once.

## Acceptance tests

Acceptance tests create real, billed resources. They run only with `TF_ACC=1`
and **only against a local or staging API, never production**: the endpoint
must be loopback, or a host with a label such as `staging`, `dev` or `test`.
Production is refused before any request is sent.

| Variable | |
|---|---|
| `XCELSIOR_ACC_ENDPOINT` | Required. For example `http://127.0.0.1:9500`. |
| `XCELSIOR_ACC_API_TOKEN` | Required. A CLI API key on that API, with a spend envelope. |
| `XCELSIOR_ACC_GPU_MODEL` | For the instance test: a GPU model that API prices. |
| `XCELSIOR_ACC_WAIT_FOR_RUNNING` | `false` when no host on that API can run the instance. |
| `XCELSIOR_ACC_SERVERLESS_GPU_TIER`, `XCELSIOR_ACC_SERVERLESS_MODEL_REF` | For the serverless test. |
| `XCELSIOR_ACC_SKIP_VOLUMES` | Set when that API cannot provision volume storage. |

The token variable is deliberately not `XCELSIOR_API_TOKEN`: on a machine that
runs Xcelsior, that name may hold the server's operator token.

Preparing an account on a staging API:

1. Register and sign in, and fund the wallet with test credit.
2. Get a CLI API key: `GET /api/mcp/quick-connect?surface=cli` from the
   signed-in session (the dashboard's Connect MCP page, CLI tab).
3. Put a spend envelope on it from the signed-in session, for example:
   `PUT /api/v1/spend-envelopes/{client_id}` with
   `{"daily_spend_max_micros": 5000000, "per_action_max_micros": 2000000, "max_gpus_per_launch": 1}`.
   An API token cannot set an envelope, so this step needs the session.
4. The API must be in a scheduler mode that owns new jobs (`active` or
   `canary`); in `shadow` mode every execute answers 503.

Then:

```sh
TF_ACC=1 XCELSIOR_ACC_ENDPOINT=http://127.0.0.1:9500 \
  XCELSIOR_ACC_API_TOKEN=... XCELSIOR_ACC_GPU_MODEL="RTX 2060" \
  make testacc
```

## Releasing

The registry address is `registry.terraform.io/aabiro/xcelsior`. The public
repository is `github.com/aabiro/terraform-provider-xcelsior`. It cannot live
inside the Xcelsior monorepo: the registry only detects a public repository
named `terraform-provider-xcelsior`.

`.goreleaser.yml` signs the checksum file. `.github/workflows/release.yml`
runs GoReleaser when a `v*` tag is pushed, and the GitHub release stays a
draft. The Terraform Registry ignores drafts. No registry version exists until
that draft is published, and a published version cannot be replaced.

Before the first tag:

1. Create an RSA key. The registry accepts RSA and DSA, not an ECC or ed25519
   key, and not the SSH signing key already on the GitHub account.

   ```sh
   gpg --full-generate-key
   ```

   Choose `RSA and RSA`, 4096 bits, and the identity
   `aaryn.alexander@gmail.com`. Set a passphrase and keep it out of chat and
   out of the repository.

2. Note the long key id:

   ```sh
   gpg --list-secret-keys --keyid-format LONG
   ```

3. Export the public key only, and add it at
   [registry.terraform.io](https://registry.terraform.io) under User Settings,
   Signing Keys, for namespace `aabiro`:

   ```sh
   gpg --armor --export aaryn.alexander@gmail.com
   ```

   Do not add this key to GitHub. GitHub's GPG settings are a different list.

4. On the GitHub repository, authorize the Terraform Registry OAuth app
   (GitHub Settings, Applications, Authorized OAuth Apps). Then add two
   Actions secrets, `GPG_PRIVATE_KEY` and `PASSPHRASE`. The private export is:

   ```sh
   gpg --armor --export-secret-keys aaryn.alexander@gmail.com
   ```

   Put that block in the secret. Do not commit it and do not paste it into
   chat.

5. Tag once, for example `v0.1.0`. The workflow writes a draft release. Check
   the zip files, the manifest, `SHA256SUMS`, and `SHA256SUMS.sig`. Publish
   that one draft after the public key is on the registry. If verification
   fails, fix the draft or the registered key. Do not delete the tag and push
   another `v0.1.0`.

## License

Business Source License 1.1. See [LICENSE](LICENSE). Production use is
permitted for connecting to the Xcelsior platform. A competing compute
marketplace needs a separate commercial license. On November 11, 2030 this
version is licensed under Apache-2.0.
