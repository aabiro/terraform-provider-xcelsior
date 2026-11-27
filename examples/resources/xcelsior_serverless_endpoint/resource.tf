resource "xcelsior_serverless_endpoint" "chat" {
  name        = "chat"
  model_ref   = "Qwen/Qwen2.5-7B-Instruct"
  gpu_tier    = "RTX 4090"
  min_workers = 0 # nothing billed while idle
  max_workers = 2

  env = {
    HF_TOKEN = var.hf_token
  }
}

variable "hf_token" {
  type      = string
  sensitive = true
}
