# Release signing key

`public-key.asc` verifies TatNet Terraform provider release checksums.

Fingerprint: `343B 4386 F5C2 803C D829 3EA3 F1A4 B97D 33F2 9CC5`.

Only the public key is stored in Git. Private signing material is held in
GitHub Actions repository secrets and must never be committed or attached
to releases. Register this public key for the `tatnet-ru` Registry namespace.

```sh
gpg --import signing/public-key.asc
gpg --verify terraform-provider-tatnet_0.1.0_SHA256SUMS.sig \
  terraform-provider-tatnet_0.1.0_SHA256SUMS
shasum -a 256 -c terraform-provider-tatnet_0.1.0_SHA256SUMS
```
