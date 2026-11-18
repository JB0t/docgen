# The cluster name.
variable "cluster_name" {
  type        = string
  default     = "aks03"
  description = "Name of the AKS cluster"
}

resource "azurerm_resource_group" "main" {
  name = var.cluster_name
}

module "network" {
  source = "./modules/aks-network"
}

output "cluster_id" {
  description = "ID of the cluster"
  value       = azurerm_resource_group.main.id
}
