# Releasing

1. Run CI and review all tracked files. Keep API keys, Terraform state, local
   logs and private signing material out of Git. Do not add co-author trailers.
2. Ensure repository secrets `GPG_PRIVATE_KEY` and `PASSPHRASE` contain the
   dedicated release key. Its public half is in `signing/public-key.asc`.
3. Tag the reviewed commit, for example `v0.3.0`, and push the tag. The Release
   workflow builds six platform archives with GoReleaser v2.18.2, writes the
   Protocol 6 manifest, signs SHA256SUMS and creates a **draft** GitHub release.
4. Download the draft assets. Verify signature, every checksum, archive names
   and contents, and the provider version. Publish the draft after validation.
5. For first publication, claim the `tatnet-ru` namespace in the Terraform
   Registry/HCP organization, register the public key, and connect the public
   GitHub repository. Registration may require organization-owner approval.
6. Confirm `terraform init` downloads the signed version from the public
   Registry without development overrides. Run `terraform validate` and a
   read-only image plan. Do not create billable resources in automated releases.

Never replace assets on a published version. Fix problems in a new version.
The workflow creates drafts intentionally so malformed artifacts are caught
before Registry ingestion. Subsequent published GitHub releases are imported
through the Registry's repository webhook.
