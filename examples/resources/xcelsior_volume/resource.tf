resource "xcelsior_volume" "datasets" {
  name    = "datasets"
  size_gb = 200

  # A change to size_gb, region or encrypted replaces the volume and destroys
  # its data. Guard volumes that hold data.
  lifecycle {
    prevent_destroy = true
  }
}
