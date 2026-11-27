data "xcelsior_gpus" "rtx4090" {
  gpu_model = "RTX 4090"
}

resource "xcelsior_ssh_key" "me" {
  name       = "laptop"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "xcelsior_volume" "datasets" {
  name    = "datasets"
  size_gb = 200

  lifecycle {
    prevent_destroy = true
  }
}

resource "xcelsior_instance" "train" {
  name         = "train-llm"
  gpu_model    = "RTX 4090"
  num_gpus     = 1
  pricing_mode = "spot"
  image        = "pytorch/pytorch:2.5.1-cuda12.4-cudnn9-runtime"
  volume_ids   = [xcelsior_volume.datasets.id]

  timeouts {
    create = "45m"
  }

  # Registered keys are installed at launch.
  depends_on = [xcelsior_ssh_key.me]
}

output "ssh" {
  value = "ssh -p ${xcelsior_instance.train.ssh_port} root@${xcelsior_instance.train.host_ip}"
}
