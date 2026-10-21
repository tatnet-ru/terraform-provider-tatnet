# tatnet_vm_backup_policy

Manage daily automatic VM backups without replacing the VM. Requires `vm:read`
and `vm:update` permission in the project.

```hcl
resource "tatnet_vm_backup_policy" "daily" {
  project_id     = var.project_id
  vm_id          = tatnet_vm.example.id
  enabled        = true
  hour_utc       = 2
  retention_days = 7
}
```

Required: `project_id`, `vm_id` (UUIDs), `enabled`, `hour_utc` (0–23 UTC),
`retention_days` (1–365). Changing project/VM replaces the policy resource;
other changes update it in place. Computed: `id` (`project_id/vm_id`),
`next_run_at`, `storage_bytes`, `price_per_gb_hour`.

Storage is billed by actual compressed decimal GB-hours, including manual
copies. Retention changes apply to future automatic copies. Manual copies do
not expire, and the last good automatic copy is preserved until a newer one
exists. Disabling or destroying this resource stops future captures; it does
not delete existing copies or stop charging for their storage. Delete copies
explicitly before destroying the VM. VM deletion fails while backups remain.

Import an existing policy (including a currently disabled policy):

```sh
terraform import tatnet_vm_backup_policy.daily PROJECT_UUID/VM_UUID
```

Use only one resource per VM. Destroying an old resource after changing its
Terraform address would disable the same VM policy; use a `moved` block.
