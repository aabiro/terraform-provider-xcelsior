data "xcelsior_gpus" "all" {}

# The cheapest GPU free right now.
locals {
  prices   = [for g in data.xcelsior_gpus.all.gpus : g.price_per_hour_micros]
  cheapest = [for g in data.xcelsior_gpus.all.gpus : g if g.price_per_hour_micros == min(local.prices...)][0]
}

output "cheapest_gpu" {
  value = local.cheapest.gpu_model
}
