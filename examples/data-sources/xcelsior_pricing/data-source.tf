data "xcelsior_pricing" "rtx4090" {
  gpu_model = "RTX 4090"
}

output "rtx4090_on_demand_per_hour" {
  # Rates are in millionths of data.xcelsior_pricing.rtx4090.currency.
  value = data.xcelsior_pricing.rtx4090.rates[0].on_demand_per_hour_micros / 1000000
}
